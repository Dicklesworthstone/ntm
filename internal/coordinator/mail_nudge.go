// Package coordinator: mail_nudge.go wires the Agent Mail idle-pane nudge
// into the coordinator tick (GH#231), mirroring rotation.go's and
// caam_failover.go's structure.
//
// Agent Mail is deliberately pull-only: when mail arrives for an agent whose
// pane is idle at its prompt, nobody wakes it and the mail sits unread. When
// [coordinator] mail_nudge = true, every coordinator cycle checks each
// registered pane agent's inbox for unread messages and — for panes that are
// verifiably idle — types a short "check your inbox" prompt through the SAME
// gated dispatch service every other prompt delivery uses (composer-ready
// gating, dead-pane refusal, per-agent submission verification).
//
// Safety gates, all mandatory and checked at fire time on a fresh capture:
//
//  1. per-pane nudge cooldown — never within [coordinator]
//     nudge_cooldown_seconds (default 60s) of the last nudge ATTEMPT for
//     that pane, persisted in the runtime store as a 'mail_nudge' watermark
//     (caam_failover/disk_sample precedent); attempts start the cooldown so
//     a failing dispatch is never hammered every tick
//  2. unread mail exists     — the same fetch_inbox surface `ntm mail inbox`
//     reads; header-only probe, no bodies
//  3. not working            — the agent working detectors (Claude/Codex);
//     agent types without a working detector are refused outright (fail
//     closed, rotation.go's rotationSafetySkipReason is reused verbatim, so
//     rate-limit banners, interactive gates, unsubmitted composer input and
//     queued messages all also skip)
//  4. gated dispatch          — TMUXDeliverer re-checks composer readiness at
//     delivery time and verifies the prompt actually submitted
//
// Every decision — nudge, nudge failure, and every safety skip — is logged
// and published to the attention feed. Identical consecutive skip reasons for
// a pane are re-published only every mailNudgeRepublishInterval so a pane
// that stays busy with unread mail does not flood the feed on every tick.
//
// DEFAULT-OFF GUARANTEE: with [coordinator] mail_nudge unset (false) the
// checker is never constructed — zero inbox probing, zero new subprocess
// calls, zero behavior change.
package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func init() {
	// WS0-G2 config-key liveness claims: this package reads the
	// [coordinator] mail-nudge knobs in newMailNudgeChecker (via the
	// CoordinatorConfig the CLI bridges from TOML).
	config.RegisterReader("coordinator.mail_nudge", newMailNudgeChecker)
	config.RegisterReader("coordinator.nudge_cooldown_seconds", newMailNudgeChecker)
	config.RegisterReader("coordinator.nudge_message", newMailNudgeChecker)
}

// mailNudgeCaptureLines is how much fresh pane tail is captured for the
// fire-time safety gates (same bound rotation.go and caam_failover.go use).
const mailNudgeCaptureLines = 100

// defaultMailNudgeCooldown is the per-pane minimum spacing between nudges
// when [coordinator] nudge_cooldown_seconds is unset or non-positive.
const defaultMailNudgeCooldown = 60 * time.Second

// mailNudgeRepublishInterval bounds attention-feed noise: an unchanged skip
// reason for a pane is re-published at most this often (every occurrence is
// still logged via slog).
const mailNudgeRepublishInterval = 10 * time.Minute

// mailNudgeInboxProbeLimit is how many inbox headers are fetched per agent
// per tick to count unread mail. Headers only, never bodies.
const mailNudgeInboxProbeLimit = 25

// watermarkTypeMailNudge is the runtime-store watermark type persisting the
// last nudge attempt per pane, following the caam_failover / disk_sample /
// output_seq precedent of documenting per-type column reuse instead of
// widening the schema. For rows of this type:
//
//	Scope      — "<session>:<agent ID>" (pane title; pane ID when untitled)
//	LastTs     — when the last nudge attempt fired
//	Consumer   — the Agent Mail name that was nudged, informational only
const watermarkTypeMailNudge = "mail_nudge"

// defaultMailNudgeMessage is the prompt typed into an idle pane with unread
// mail; %d is the unread count. [coordinator] nudge_message overrides it
// verbatim (no substitution).
const defaultMailNudgeMessage = "You have %d unread Agent Mail message(s). Check your inbox now, promptly respond to any messages that need it, acknowledge any contact requests, then resume your current task."

// mailNudgeDecision records one nudge decision for logging and tests.
type mailNudgeDecision struct {
	PaneID     string // tmux pane ID (%N)
	AgentID    string // canonical agent ID (pane title, e.g. sess__cc_1)
	MailAgent  string // Agent Mail identity (e.g. "BlueLake")
	Action     string // "nudged", "nudge_failed", "skipped"
	SkipReason string // populated when Action == "skipped"
	Unread     int    // evidence: unread message count at decision time
	Error      string // populated when Action == "nudge_failed"
}

// mailNudgeChecker performs the per-tick unread-mail nudge check. All
// collaborators are injectable for tests; production wiring is installed by
// newMailNudgeChecker.
type mailNudgeChecker struct {
	session    string
	projectKey string
	cooldown   time.Duration
	message    string // "" means the default template

	// Seams (default to real implementations).
	getPanes    func(session string) ([]tmux.Pane, error)
	agentLookup func() func(paneTitle, paneID string) (string, bool)
	fetchInbox  func(ctx context.Context, opts agentmail.FetchInboxOptions) ([]agentmail.InboxMessage, error)
	capturePane func(paneID string, lines int) (string, error)
	dispatch    func(ctx context.Context, panes []tmux.Pane, target tmux.Pane, message string) error
	lastNudgeAt func(scope string) (time.Time, bool)
	recordNudge func(scope, mailAgent string, at time.Time)
	publish     func(record robot.ActuationRecord)
	now         func() time.Time

	// Cooldown fallback when the runtime store is unavailable, and skip
	// republish bookkeeping.
	mu            sync.Mutex
	memLastNudge  map[string]time.Time
	lastPublished map[string]declineMark // pane ID -> last published skip

	storeOnce sync.Once
	store     *state.Store
}

// newMailNudgeChecker builds a production checker, or nil when the feature is
// disabled (mail_nudge false) or no Agent Mail client is available (there is
// no inbox to poll and no registry to map agents through).
func newMailNudgeChecker(session, projectKey string, coordCfg CoordinatorConfig, mailClient *agentmail.Client) *mailNudgeChecker {
	if !coordCfg.MailNudge || mailClient == nil {
		return nil
	}

	cooldown := coordCfg.NudgeCooldown
	if cooldown <= 0 {
		cooldown = defaultMailNudgeCooldown
	}

	mc := &mailNudgeChecker{
		session:    session,
		projectKey: projectKey,
		cooldown:   cooldown,
		message:    strings.TrimSpace(coordCfg.NudgeMessage),
		getPanes:   tmux.GetPanes,
		agentLookup: func() func(paneTitle, paneID string) (string, bool) {
			registry, err := agentmail.LoadSessionAgentRegistry(session, projectKey)
			if err != nil || registry == nil {
				if err != nil {
					slog.Debug("mail nudge: Agent Mail pane registry unavailable",
						"session", session, "error", err)
				}
				return func(string, string) (string, bool) { return "", false }
			}
			return registry.GetAgent
		},
		fetchInbox:  mailClient.FetchInbox,
		capturePane: tmux.CapturePaneOutput,
		dispatch: func(ctx context.Context, panes []tmux.Pane, target tmux.Pane, message string) error {
			return dispatchMailNudge(ctx, session, panes, target, message)
		},
		publish: func(record robot.ActuationRecord) {
			robot.GetAttentionFeed().PublishActuation(record)
		},
		now:           time.Now,
		memLastNudge:  make(map[string]time.Time),
		lastPublished: make(map[string]declineMark),
	}
	mc.lastNudgeAt = mc.storedLastNudge
	mc.recordNudge = mc.storeLastNudge
	return mc
}

// dispatchMailNudge delivers one nudge through the shared gated dispatch
// service: composer-ready gating, dead-pane refusal, and per-agent
// submission verification all come from the same ports `ntm send` and the
// bugs watcher use. The nudge text is a fixed operator-configured template
// with no payload data, so redaction is a pass-through.
func dispatchMailNudge(ctx context.Context, session string, panes []tmux.Pane, target tmux.Pane, message string) error {
	service, err := dispatchsvc.NewService(dispatchsvc.Ports{
		Redactor:  dispatchsvc.AllowAllRedactor{},
		Protocols: dispatchsvc.DefaultProtocolPlanner{},
		Deliverer: dispatchsvc.TMUXDeliverer{},
	})
	if err != nil {
		return fmt.Errorf("preparing mail nudge dispatch: %w", err)
	}
	selector := target.ID
	if selector == "" {
		selector = target.Ref().Physical()
	}
	result, err := service.Execute(ctx, dispatchsvc.Request{
		Session:       session,
		Panes:         panes,
		Selectors:     []string{selector},
		Message:       message,
		Submit:        true,
		StopOnFailure: true,
	})
	if err != nil {
		return err
	}
	if result.Delivered != 1 {
		return fmt.Errorf("mail nudge delivered to %d of 1 target", result.Delivered)
	}
	return nil
}

// runtimeStore lazily opens the shared runtime store. nil means the store is
// unavailable; the in-memory cooldown map still bounds this process.
func (mc *mailNudgeChecker) runtimeStore() *state.Store {
	mc.storeOnce.Do(func() {
		store, err := state.Open("")
		if err != nil {
			slog.Debug("mail nudge: runtime store unavailable; cooldown is process-local",
				"session", mc.session, "error", err)
			return
		}
		if err := store.Migrate(); err != nil {
			slog.Debug("mail nudge: runtime store migration failed; cooldown is process-local",
				"session", mc.session, "error", err)
			_ = store.Close()
			return
		}
		mc.store = store
	})
	return mc.store
}

// storedLastNudge reads the per-pane nudge watermark, falling back to the
// in-memory map when the store is unavailable.
func (mc *mailNudgeChecker) storedLastNudge(scope string) (time.Time, bool) {
	if store := mc.runtimeStore(); store != nil {
		wm, err := store.GetWatermark(watermarkTypeMailNudge, scope)
		if err == nil && wm != nil && wm.LastTs != nil {
			return *wm.LastTs, true
		}
		if err != nil {
			slog.Debug("mail nudge: watermark read failed", "scope", scope, "error", err)
		}
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()
	at, ok := mc.memLastNudge[scope]
	return at, ok
}

// storeLastNudge persists the per-pane nudge watermark (and always records
// it in memory so cooldown survives a store outage within this process).
func (mc *mailNudgeChecker) storeLastNudge(scope, mailAgent string, at time.Time) {
	mc.mu.Lock()
	mc.memLastNudge[scope] = at
	mc.mu.Unlock()

	store := mc.runtimeStore()
	if store == nil {
		return
	}
	ts := at.UTC()
	if err := store.SetWatermark(&state.OutputWatermark{
		WatermarkType: watermarkTypeMailNudge,
		Scope:         scope,
		LastTs:        &ts,
		Consumer:      mailAgent,
		CreatedAt:     ts,
		UpdatedAt:     ts,
	}); err != nil {
		slog.Debug("mail nudge: watermark write failed", "scope", scope, "error", err)
	}
}

// runOnce executes one mail-nudge check pass and returns the decisions made.
// Panes without a registered Agent Mail identity, panes inside their nudge
// cooldown, and panes without unread mail produce no decision at all.
func (mc *mailNudgeChecker) runOnce(ctx context.Context) []mailNudgeDecision {
	if mc == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return nil
	}

	panes, err := mc.getPanes(mc.session)
	if err != nil {
		slog.Warn("mail nudge check could not list panes",
			"session", mc.session, "error", err)
		return nil
	}

	lookup := mc.agentLookup()
	if lookup == nil {
		return nil
	}

	// Deterministic order for logs and tests.
	ordered := append([]tmux.Pane(nil), panes...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	var decisions []mailNudgeDecision
	for _, pane := range ordered {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if decision, acted := mc.checkPane(ctx, panes, pane, lookup); acted {
			decisions = append(decisions, decision)
		}
	}
	return decisions
}

// checkPane inspects one pane agent's inbox and, when unread mail exists,
// runs the mandatory safety gates and dispatches/declines the nudge. The
// bool result reports whether a decision was made.
//
// Gate order (documented invariant, mirrored by the tests): registry mapping
// → cooldown → unread mail → fresh capture → working/safety detectors →
// gated dispatch.
func (mc *mailNudgeChecker) checkPane(ctx context.Context, panes []tmux.Pane, pane tmux.Pane, lookup func(paneTitle, paneID string) (string, bool)) (mailNudgeDecision, bool) {
	now := mc.now()

	mailAgent, ok := lookup(pane.Title, pane.ID)
	if !ok || mailAgent == "" {
		return mailNudgeDecision{}, false
	}

	agentID := strings.TrimSpace(pane.Title)
	if agentID == "" {
		agentID = pane.ID
	}
	scope := mc.session + ":" + agentID

	// Cooldown precedes the inbox probe: within the window there is nothing
	// this checker would do, so it stays silent (debug log only) instead of
	// publishing skips whose mail state it never confirmed.
	if last, found := mc.lastNudgeAt(scope); found && now.Sub(last) < mc.cooldown {
		slog.Debug("mail nudge: pane inside cooldown",
			"session", mc.session, "pane", pane.ID, "agent", agentID,
			"since_last", now.Sub(last).String(), "cooldown", mc.cooldown.String())
		return mailNudgeDecision{}, false
	}

	inbox, err := mc.fetchInbox(ctx, agentmail.FetchInboxOptions{
		ProjectKey: mc.projectKey,
		AgentName:  mailAgent,
		Limit:      mailNudgeInboxProbeLimit,
	})
	if err != nil {
		slog.Warn("mail nudge could not inspect Agent Mail inbox",
			"session", mc.session, "pane", pane.ID, "agent", mailAgent, "error", err)
		return mailNudgeDecision{}, false
	}
	unread := 0
	for _, message := range inbox {
		if message.ReadAt == nil {
			unread++
		}
	}
	if unread == 0 {
		return mailNudgeDecision{}, false
	}

	decision := mailNudgeDecision{
		PaneID:    pane.ID,
		AgentID:   agentID,
		MailAgent: mailAgent,
		Unread:    unread,
	}

	// Fire-time safety gates on a FRESH capture. A pane we cannot observe is
	// a pane we must not type into.
	captured, err := mc.capturePane(pane.ID, mailNudgeCaptureLines)
	if err != nil {
		decision.Action = "skipped"
		decision.SkipReason = "capture_failed"
		mc.reportSkip(decision, now)
		return decision, true
	}
	// rotationSafetySkipReason applies the shared detectors: working (per
	// agent type, failing closed as unsupported_agent_type for types without
	// a working detector), rate_limited, interactive_gate, unsubmitted_input,
	// queued_messages.
	if reason := rotationSafetySkipReason(captured, pane); reason != "" {
		decision.Action = "skipped"
		decision.SkipReason = reason
		mc.reportSkip(decision, now)
		return decision, true
	}

	// The attempt starts the cooldown (caam_failover precedent): a dispatch
	// that keeps failing must not be retried every tick.
	mc.recordNudge(scope, mailAgent, now)

	message := mc.message
	if message == "" {
		message = fmt.Sprintf(defaultMailNudgeMessage, unread)
	}
	if err := mc.dispatch(ctx, panes, pane, message); err != nil {
		decision.Action = "nudge_failed"
		decision.Error = err.Error()
		slog.Warn("mail nudge dispatch failed",
			"session", mc.session, "pane", pane.ID, "agent", agentID,
			"mail_agent", mailAgent, "unread", unread, "error", err)
		mc.publishDecision(robot.ActuationRecord{
			Stage:          robot.ActuationStageOutcome,
			Targets:        []string{agentID},
			Summary:        fmt.Sprintf("mail nudge for %s failed: %v", agentID, err),
			ReasonCode:     "mail_nudge_failed",
			MessagePreview: mailNudgeEvidence(decision),
			Result:         "failed",
			Error:          err.Error(),
			Severity:       robot.SeverityWarning,
		})
		return decision, true
	}

	decision.Action = "nudged"
	slog.Info("mail nudge delivered to idle pane",
		"session", mc.session, "pane", pane.ID, "agent", agentID,
		"mail_agent", mailAgent, "unread", unread, "cooldown", mc.cooldown.String())
	mc.publishDecision(robot.ActuationRecord{
		Stage:          robot.ActuationStageOutcome,
		Targets:        []string{agentID},
		Summary:        fmt.Sprintf("nudged %s to read %d unread Agent Mail message(s)", agentID, unread),
		ReasonCode:     "mail_nudge_delivered",
		MessagePreview: mailNudgeEvidence(decision),
		Result:         "delivered",
		Severity:       robot.SeverityInfo,
	})
	return decision, true
}

// reportSkip logs and publishes a safety-gate skip, suppressing republication
// of an unchanged reason for the same pane within mailNudgeRepublishInterval.
func (mc *mailNudgeChecker) reportSkip(decision mailNudgeDecision, now time.Time) {
	slog.Info("mail nudge skipped by safety gate",
		"session", mc.session, "pane", decision.PaneID, "agent", decision.AgentID,
		"mail_agent", decision.MailAgent, "reason", decision.SkipReason,
		"unread", decision.Unread)

	mc.mu.Lock()
	mark, seen := mc.lastPublished[decision.PaneID]
	if seen && mark.reason == decision.SkipReason && now.Sub(mark.at) < mailNudgeRepublishInterval {
		mc.mu.Unlock()
		return
	}
	mc.lastPublished[decision.PaneID] = declineMark{reason: decision.SkipReason, at: now}
	mc.mu.Unlock()

	target := decision.AgentID
	if target == "" {
		target = decision.PaneID
	}
	mc.publishDecision(robot.ActuationRecord{
		Stage:          robot.ActuationStageOutcome,
		Targets:        []string{target},
		Summary:        fmt.Sprintf("mail nudge for %s skipped: %s (%d unread)", target, decision.SkipReason, decision.Unread),
		ReasonCode:     "mail_nudge_safety_skip",
		MessagePreview: mailNudgeEvidence(decision),
		Blocked:        true,
		Severity:       robot.SeverityInfo,
	})
}

// publishDecision fills the shared actuation-record fields and publishes to
// the attention feed.
func (mc *mailNudgeChecker) publishDecision(record robot.ActuationRecord) {
	if mc.publish == nil {
		return
	}
	record.Session = mc.session
	record.Action = "mail_nudge"
	record.Source = "coordinator.mail_nudge"
	record.Method = "inbox_poll"
	record.Actionability = robot.ActionabilityInteresting
	mc.publish(record)
}

// mailNudgeEvidence renders the decision evidence for attention-feed
// consumers.
func mailNudgeEvidence(d mailNudgeDecision) string {
	return fmt.Sprintf("mail_agent=%s unread=%d pane=%s", d.MailAgent, d.Unread, d.PaneID)
}
