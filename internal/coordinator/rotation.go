// Package coordinator: rotation.go wires the context rotation engine into the
// coordinator tick (bd-rpmg8).
//
// When [rotation] usage_percent_threshold is > 0, every coordinator cycle
// compares each agent pane's TRANSCRIPT-SOURCED context usage — ground truth
// read from the agent CLI's own session transcript, never scrollback
// estimates (that minimum-confidence gate is fixed by design, not
// configurable) — against the threshold. Panes above it get a pending
// rotation enqueued through internal/context's existing pending/confirm
// machinery (the same storage `ntm rotate context pending/confirm` reads);
// with [rotation] auto_confirm = true the pending rotation is executed
// immediately via the existing Rotator.ConfirmRotation path.
//
// Safety gates, all mandatory and checked at fire time on a fresh capture:
// the pane must not be actively working, rate-limited, showing an interactive
// gate, or holding unsubmitted composer input / queued messages. Every skip
// is logged with its reason and published to the attention feed.
package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/models"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// rotationCaptureLines is how much fresh pane tail is captured for the
// fire-time safety gates. The working/gate/composer detectors all operate on
// bounded tails, so a moderate capture is sufficient and cheap.
const rotationCaptureLines = 100

func init() {
	// G2 config-key liveness claims (WS6-wire): this package reads the
	// [rotation.thresholds] restart triggers in newRotationChecker.
	config.RegisterReader("rotation.thresholds.restart_if_tokens_above", newRotationChecker)
	config.RegisterReader("rotation.thresholds.restart_if_session_hours", newRotationChecker)
}

// rotationDecision records one trigger decision for logging and tests.
type rotationDecision struct {
	PaneID     string  // tmux pane ID (%N)
	AgentID    string  // canonical agent ID (pane title, e.g. sess__cc_1)
	Action     string  // "enqueued", "auto_confirmed", "auto_confirm_failed", "skipped"
	SkipReason string  // populated when Action == "skipped"
	UsagePct   float64 // transcript-sourced usage percentage
	Tokens     int     // evidence: transcript context occupancy tokens
	Limit      int     // evidence: context window (transcript-reported or models registry)
	Source     string  // evidence: transcript file path
	Confidence string  // evidence: transcript freshness confidence ("high"/"medium")
}

// rotationChecker performs the per-tick transcript-usage rotation check. All
// collaborators are injectable for tests; production wiring is installed by
// newRotationChecker.
type rotationChecker struct {
	session     string
	workDir     string
	threshold   float64
	autoConfirm bool

	// [rotation.thresholds] restart triggers (WS6-wire,
	// bd-ws6-config-truth-ienmd.1). Active only while the transcript-usage
	// checker itself is enabled (usage_percent_threshold > 0); zero disables
	// the individual trigger.
	restartTokensAbove float64       // rotation.thresholds.restart_if_tokens_above
	restartSessionAge  time.Duration // rotation.thresholds.restart_if_session_hours

	rotator    *ntmctx.Rotator
	ctxMonitor *ntmctx.ContextMonitor

	// Seams (default to real implementations).
	getPanes        func(session string) ([]tmux.Pane, error)
	paneCwd         func(paneID string) (string, bool)
	transcriptUsage func(agentType, cwd string) (*ntmctx.TranscriptUsage, bool)
	capturePane     func(paneID string, lines int) (string, error)
	contextLimit    func(model string) int
	sessionCreated  func(session string) (time.Time, bool)
	storedPending   func(agentID string) (*ntmctx.PendingRotation, error)
	enqueue         func(agentID, paneID string, usagePct float64) *ntmctx.PendingRotation
	confirm         func(agentID string) ntmctx.RotationResult
	publish         func(record robot.ActuationRecord)
	now             func() time.Time
}

// newRotationChecker builds a production checker, or nil when the trigger is
// disabled (threshold <= 0). ntmCfg may be nil; it is only used for the
// replacement-agent launch commands and context rotation tunables.
func newRotationChecker(session, workDir string, coordCfg CoordinatorConfig, ntmCfg *config.Config) *rotationChecker {
	if coordCfg.RotationUsageThreshold <= 0 {
		return nil
	}

	rotCfg := config.DefaultContextRotationConfig()
	thresholds := config.DefaultRotationConfig().Thresholds
	if ntmCfg != nil {
		rotCfg = ntmCfg.ContextRotation
		thresholds = ntmCfg.Rotation.Thresholds
	}
	// The checker decides eligibility itself and only uses the Rotator's
	// pending/confirm machinery; Enabled/RequireConfirm reflect that entry
	// point regardless of the [context_rotation] scrollback-driven settings.
	rotCfg.Enabled = true
	rotCfg.RequireConfirm = true

	ctxMonitor := ntmctx.NewContextMonitor(ntmctx.DefaultMonitorConfig())
	rotator := ntmctx.NewRotator(ntmctx.RotatorConfig{
		Monitor: ctxMonitor,
		Spawner: ntmctx.NewDefaultPaneSpawner(ntmCfg),
		Config:  rotCfg,
	})

	rc := &rotationChecker{
		session:     session,
		workDir:     workDir,
		threshold:   coordCfg.RotationUsageThreshold,
		autoConfirm: coordCfg.RotationAutoConfirm,
		// [rotation.thresholds] restart triggers ride the same enqueue/confirm
		// machinery (WS6-wire): tokens above restart_if_tokens_above or a
		// session older than restart_if_session_hours also trigger rotation.
		restartTokensAbove: thresholds.RestartIfTokensAbove,
		restartSessionAge:  time.Duration(thresholds.RestartIfSessionHours) * time.Hour,
		rotator:            rotator,
		ctxMonitor:         ctxMonitor,
		getPanes:           tmux.GetPanes,
		sessionCreated: func(session string) (time.Time, bool) {
			out, err := tmux.DefaultClient.Run("display-message", "-p", "-t", session, "#{session_created}")
			if err != nil {
				return time.Time{}, false
			}
			secs, convErr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
			if convErr != nil || secs <= 0 {
				return time.Time{}, false
			}
			return time.Unix(secs, 0), true
		},
		paneCwd: func(paneID string) (string, bool) {
			cwd, err := tmux.DefaultClient.Run("display-message", "-p", "-t", paneID, "#{pane_current_path}")
			if err != nil {
				return "", false
			}
			cwd = strings.TrimSpace(cwd)
			return cwd, cwd != ""
		},
		transcriptUsage: func(agentType, cwd string) (*ntmctx.TranscriptUsage, bool) {
			return ntmctx.LatestAgentTranscriptUsage(agentType, cwd, time.Time{})
		},
		capturePane:   tmux.CapturePaneOutput,
		contextLimit:  models.GetContextLimit,
		storedPending: ntmctx.GetPendingRotationByID,
		publish: func(record robot.ActuationRecord) {
			robot.GetAttentionFeed().PublishActuation(record)
		},
		now: time.Now,
	}
	rc.enqueue = func(agentID, paneID string, usagePct float64) *ntmctx.PendingRotation {
		return rc.rotator.EnqueuePendingRotation(rc.session, agentID, paneID, usagePct, rc.workDir)
	}
	rc.confirm = func(agentID string) ntmctx.RotationResult {
		return rc.rotator.ConfirmRotation(agentID, ntmctx.ConfirmRotate, 0)
	}
	return rc
}

// runOnce executes one rotation check pass and returns the decisions made.
// Panes without an unambiguous transcript-sourced usage reading are ignored
// entirely (the fixed minimum-confidence gate), as are panes at or below the
// threshold.
func (rc *rotationChecker) runOnce(ctx context.Context) []rotationDecision {
	if rc == nil || rc.threshold <= 0 {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return nil
	}

	panes, err := rc.getPanes(rc.session)
	if err != nil {
		slog.Warn("context rotation check could not list panes",
			"session", rc.session, "error", err)
		return nil
	}

	usages := rc.resolvePaneTranscripts(panes)
	if len(usages) == 0 {
		return nil
	}

	// Deterministic order for logs and tests.
	ordered := make([]tmux.Pane, 0, len(panes))
	for _, pane := range panes {
		if _, ok := usages[pane.ID]; ok {
			ordered = append(ordered, pane)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	var decisions []rotationDecision
	for _, pane := range ordered {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if decision, acted := rc.checkPane(pane, usages[pane.ID]); acted {
			decisions = append(decisions, decision)
		}
	}
	return decisions
}

// checkPane evaluates one pane's transcript usage against the threshold and,
// when exceeded, runs the safety gates and enqueues/auto-confirms a rotation.
// The bool result reports whether a decision (enqueue/confirm/skip) was made.
func (rc *rotationChecker) checkPane(pane tmux.Pane, usage *ntmctx.TranscriptUsage) (rotationDecision, bool) {
	now := rc.now()

	// Usage percent: transcript-reported window when available (Codex),
	// otherwise the models registry window for the transcript's model —
	// mirroring internal/robot's snapshot computation.
	limit := usage.ContextWindow
	if limit <= 0 {
		limit = rc.contextLimit(usage.Model)
	}
	if limit <= 0 {
		return rotationDecision{}, false
	}
	pct := float64(usage.Tokens) / float64(limit) * 100
	if pct > 100 {
		// An over-100% reading means the registry window for this model is
		// wrong; cap at the safe rotation-triggering ceiling.
		pct = 100
	}
	// Trigger evaluation: transcript usage percent is the primary trigger;
	// the [rotation.thresholds] restart knobs add two more (WS6-wire,
	// bd-ws6-config-truth-ienmd.1): absolute transcript tokens above
	// restart_if_tokens_above, and session age above
	// restart_if_session_hours. Each individual trigger is disabled at zero.
	triggerReason := ""
	switch {
	case pct > rc.threshold:
		triggerReason = "usage_percent"
	case rc.restartTokensAbove > 0 && float64(usage.Tokens) > rc.restartTokensAbove:
		triggerReason = "tokens_above"
	case rc.restartSessionAge > 0 && rc.sessionCreated != nil:
		if created, ok := rc.sessionCreated(rc.session); ok && now.Sub(created) > rc.restartSessionAge {
			triggerReason = "session_age"
		}
	}
	if triggerReason == "" {
		return rotationDecision{}, false
	}

	decision := rotationDecision{
		PaneID:     pane.ID,
		AgentID:    pane.Title,
		UsagePct:   pct,
		Tokens:     usage.Tokens,
		Limit:      limit,
		Source:     usage.Path,
		Confidence: ntmctx.TranscriptConfidence(usage.UpdatedAt, now),
	}

	if strings.TrimSpace(pane.Title) == "" {
		decision.Action = "skipped"
		decision.SkipReason = "missing_pane_title"
		rc.reportSkip(decision)
		return decision, true
	}

	// Already pending (in this rotator or in the persistent store another
	// process wrote): do not re-enqueue or republish every tick.
	if rc.rotator != nil && rc.rotator.HasPendingRotation(decision.AgentID) {
		if pending := rc.rotator.GetPendingRotation(decision.AgentID); pending != nil && !pending.IsExpired() {
			return rotationDecision{}, false
		}
	}
	if rc.storedPending != nil {
		if stored, err := rc.storedPending(decision.AgentID); err == nil && stored != nil {
			return rotationDecision{}, false
		}
	}

	// Fire-time safety gates on a FRESH capture. A pane we cannot observe is
	// a pane we must not rotate.
	captured, err := rc.capturePane(pane.ID, rotationCaptureLines)
	if err != nil {
		decision.Action = "skipped"
		decision.SkipReason = "capture_failed"
		rc.reportSkip(decision)
		return decision, true
	}
	if reason := rotationSafetySkipReason(captured, pane); reason != "" {
		decision.Action = "skipped"
		decision.SkipReason = reason
		rc.reportSkip(decision)
		return decision, true
	}

	pending := rc.enqueue(decision.AgentID, pane.ID, pct)
	decision.Action = "enqueued"
	slog.Info("context rotation enqueued from transcript usage",
		"session", rc.session, "pane", pane.ID, "agent", decision.AgentID,
		"trigger", triggerReason,
		"usage_percent", pct, "threshold", rc.threshold,
		"tokens", usage.Tokens, "limit", limit,
		"source", usage.Path, "confidence", decision.Confidence,
		"auto_confirm", rc.autoConfirm)
	rc.publishDecision(robot.ActuationRecord{
		Stage:          robot.ActuationStageRequest,
		Targets:        []string{decision.AgentID},
		Summary:        fmt.Sprintf("context rotation enqueued for %s: transcript usage %.1f%% > threshold %.1f%%", decision.AgentID, pct, rc.threshold),
		ReasonCode:     "context_rotation_enqueued",
		MessagePreview: rotationEvidence(decision),
		Severity:       robot.SeverityInfo,
	})

	if !rc.autoConfirm || pending == nil {
		return decision, true
	}

	// Auto-confirm executes through the existing confirm path. The rotator's
	// monitor must know the agent so rotateAgent can resolve its state.
	if rc.ctxMonitor != nil {
		rc.ctxMonitor.RegisterAgent(decision.AgentID, pane.ID, usage.Model)
		rc.ctxMonitor.SetAgentType(decision.AgentID, string(pane.Type.Canonical()))
	}
	result := rc.confirm(decision.AgentID)
	if result.Success {
		decision.Action = "auto_confirmed"
		slog.Info("context rotation auto-confirmed",
			"session", rc.session, "agent", decision.AgentID,
			"new_agent", result.NewAgentID, "new_pane", result.NewPaneID,
			"state", string(result.State))
	} else {
		decision.Action = "auto_confirm_failed"
		slog.Warn("context rotation auto-confirm failed",
			"session", rc.session, "agent", decision.AgentID,
			"state", string(result.State), "error", result.Error)
	}
	severity := robot.SeverityInfo
	resultWord := "completed"
	if !result.Success {
		severity = robot.SeverityWarning
		resultWord = "failed"
	}
	rc.publishDecision(robot.ActuationRecord{
		Stage:          robot.ActuationStageOutcome,
		Targets:        []string{decision.AgentID},
		Summary:        fmt.Sprintf("context rotation auto-confirm %s for %s (state=%s)", resultWord, decision.AgentID, result.State),
		ReasonCode:     "context_rotation_auto_confirm",
		MessagePreview: rotationEvidence(decision),
		Result:         resultWord,
		Error:          result.Error,
		Severity:       severity,
	})
	return decision, true
}

// reportSkip logs and publishes a safety-gate (or observability) skip.
func (rc *rotationChecker) reportSkip(decision rotationDecision) {
	slog.Info("context rotation skipped by safety gate",
		"session", rc.session, "pane", decision.PaneID, "agent", decision.AgentID,
		"reason", decision.SkipReason,
		"usage_percent", decision.UsagePct, "threshold", rc.threshold,
		"tokens", decision.Tokens, "limit", decision.Limit,
		"source", decision.Source, "confidence", decision.Confidence)
	target := decision.AgentID
	if target == "" {
		target = decision.PaneID
	}
	rc.publishDecision(robot.ActuationRecord{
		Stage:          robot.ActuationStageOutcome,
		Targets:        []string{target},
		Summary:        fmt.Sprintf("context rotation for %s skipped: %s (usage %.1f%% > threshold %.1f%%)", target, decision.SkipReason, decision.UsagePct, rc.threshold),
		ReasonCode:     "context_rotation_safety_skip",
		MessagePreview: rotationEvidence(decision),
		Blocked:        true,
		Severity:       robot.SeverityInfo,
	})
}

// publishDecision fills the shared actuation-record fields and publishes to
// the attention feed.
func (rc *rotationChecker) publishDecision(record robot.ActuationRecord) {
	if rc.publish == nil {
		return
	}
	record.Session = rc.session
	record.Action = "context_rotation"
	record.Source = "coordinator.rotation"
	record.Method = "transcript_usage"
	record.Actionability = robot.ActionabilityInteresting
	rc.publish(record)
}

// rotationEvidence renders the triggering evidence (tokens, limit, source
// path, confidence) for attention-feed consumers.
func rotationEvidence(d rotationDecision) string {
	return fmt.Sprintf("usage=%.1f%% tokens=%d limit=%d source=%s confidence=%s",
		d.UsagePct, d.Tokens, d.Limit, d.Source, d.Confidence)
}

// rotationSafetySkipReason applies the mandatory fire-time safety gates to a
// fresh capture and returns a non-empty machine-readable reason when the pane
// must not be rotated:
//
//  1. working            — the agent is mid-turn (live spinner / in-flight marker)
//  2. rate_limited       — the pane shows rate-limit output
//  3. interactive_gate   — a login/trust/permission dialog is on screen
//  4. unsubmitted_input  — the composer holds typed-but-unsent text
//  5. queued_messages    — the TUI reports queued unsubmitted messages
//
// Only Claude Code and Codex panes can reach this point (they are the only
// agent types with known transcript locations); any other type is refused
// outright because the automated rotation path does not support it.
func rotationSafetySkipReason(captured string, pane tmux.Pane) string {
	canonical := pane.Type.Canonical()
	switch canonical {
	case agent.AgentTypeClaudeCode:
		if agent.ClaudeActivelyWorking(captured, pane.Width) {
			return "working"
		}
	case agent.AgentTypeCodex:
		if agent.CodexActivelyWorking(captured, pane.Width) {
			return "working"
		}
	default:
		return "unsupported_agent_type"
	}

	if ratelimit.DetectRateLimitForAgent(captured, string(canonical)).RateLimited {
		return "rate_limited"
	}

	if gate, found := agent.DetectInteractiveGate(captured, pane.Width); found {
		return "interactive_gate:" + gate
	}

	composer := tmux.InspectComposer(captured, canonical)
	if composer.HoldsText {
		return "unsubmitted_input"
	}
	if composer.QueuedMessages {
		return "queued_messages"
	}

	return ""
}

// resolvePaneTranscripts correlates panes with their agent CLI's own session
// transcripts by (agent type, pane working directory), returning usage keyed
// by pane ID.
//
// This mirrors the semantics of internal/robot's unexported
// resolvePaneTranscripts (robot.go): correlation is by (agent type, cwd), so
// when SEVERAL panes of the same agent type share one directory — the normal
// NTM swarm layout — the newest transcript cannot be attributed to any
// specific pane and those panes get NO transcript reading (the check simply
// does not fire for them) rather than all being assigned the same session's
// numbers. Lookups are memoized per (type, cwd) group so a tick over many
// panes costs one filesystem probe per group. robot does not export the
// helper, hence this local implementation (kept in lockstep by the shared
// ambiguity rule above).
func (rc *rotationChecker) resolvePaneTranscripts(panes []tmux.Pane) map[string]*ntmctx.TranscriptUsage {
	type paneKey struct{ agentType, cwd string }
	groups := make(map[paneKey][]string)
	for _, pane := range panes {
		var agentType string
		switch pane.Type.Canonical() {
		case agent.AgentTypeClaudeCode:
			agentType = "claude"
		case agent.AgentTypeCodex:
			agentType = "codex"
		default:
			// Other agent CLIs have no known transcript locations; the fixed
			// transcript-only confidence gate excludes them.
			continue
		}
		cwd, ok := rc.paneCwd(pane.ID)
		if !ok {
			continue
		}
		groups[paneKey{agentType: agentType, cwd: cwd}] = append(groups[paneKey{agentType: agentType, cwd: cwd}], pane.ID)
	}

	result := make(map[string]*ntmctx.TranscriptUsage)
	for key, paneIDs := range groups {
		if len(paneIDs) != 1 {
			continue // ambiguous attribution: no transcript beats a wrong one
		}
		if usage, ok := rc.transcriptUsage(key.agentType, key.cwd); ok && usage != nil {
			result[paneIDs[0]] = usage
		}
	}
	return result
}
