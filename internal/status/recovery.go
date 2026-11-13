package status

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	DefaultRecoveryPrompt       = "Reread AGENTS.md so it's still fresh in your mind. Use ultrathink."
	DefaultCooldown             = 30 * time.Second
	DefaultMaxRecoveriesPerPane = 5
)

// RecoveryEvent records a completed send, not merely a scheduled goroutine.
type RecoveryEvent struct {
	PaneID      string    `json:"pane_id"`
	Session     string    `json:"session"`
	PaneIndex   int       `json:"pane_index"`
	SentAt      time.Time `json:"sent_at"`
	Prompt      string    `json:"prompt"`
	TriggerText string    `json:"trigger_text"`
}

// RecoveryManager owns one delivery protocol for manual and monitored recovery.
// Sends are synchronous and serialized per pane, not fire-and-forget UI work.
type RecoveryManager struct {
	mu                 sync.RWMutex
	lastRecovery       map[string]time.Time
	recoveryCount      map[string]int
	recoveryEvents     []RecoveryEvent
	inFlight           map[string]bool
	cooldown           time.Duration
	prompt             string
	maxRecoveries      int
	maxEventAge        time.Duration
	includeBeadContext bool
	resolvePaneType    func(session string, paneIndex int, paneID string) (agent.AgentType, error)
	sendPrompt         func(target, prompt string, enter bool) error
	sendPromptContext  func(context.Context, string, string, bool, agent.AgentType) error
	verifyProcess      func(context.Context, string, string) error
}

type RecoveryConfig struct {
	Cooldown           time.Duration
	Prompt             string
	MaxRecoveries      int
	MaxEventAge        time.Duration
	IncludeBeadContext bool
}

func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{Cooldown: DefaultCooldown, Prompt: DefaultRecoveryPrompt,
		MaxRecoveries: DefaultMaxRecoveriesPerPane, MaxEventAge: 10 * time.Minute, IncludeBeadContext: true}
}

func NewRecoveryManager(config RecoveryConfig) *RecoveryManager {
	if config.Cooldown == 0 {
		config.Cooldown = DefaultCooldown
	}
	if config.Prompt == "" {
		config.Prompt = DefaultRecoveryPrompt
	}
	if config.MaxRecoveries == 0 {
		config.MaxRecoveries = DefaultMaxRecoveriesPerPane
	}
	if config.MaxEventAge == 0 {
		config.MaxEventAge = 10 * time.Minute
	}
	return &RecoveryManager{lastRecovery: make(map[string]time.Time), recoveryCount: make(map[string]int),
		recoveryEvents: make([]RecoveryEvent, 0), inFlight: make(map[string]bool),
		cooldown: config.Cooldown, prompt: config.Prompt, maxRecoveries: config.MaxRecoveries,
		maxEventAge: config.MaxEventAge, includeBeadContext: config.IncludeBeadContext}
}

func (rm *RecoveryManager) CanSendRecovery(paneID string) (bool, string) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.canSendRecoveryLocked(paneID)
}

func (rm *RecoveryManager) canSendRecoveryLocked(paneID string) (bool, string) {
	if rm.inFlight[paneID] {
		return false, "recovery already in progress"
	}
	if last, ok := rm.lastRecovery[paneID]; ok {
		if remaining := rm.cooldown - time.Since(last); remaining > 0 {
			return false, fmt.Sprintf("cooldown: %s remaining", remaining.Round(time.Second))
		}
	}
	if count := rm.recoveryCount[paneID]; count >= rm.maxRecoveries {
		return false, fmt.Sprintf("max recoveries reached: %d/%d", count, rm.maxRecoveries)
	}
	return true, ""
}

func (rm *RecoveryManager) SendRecoveryPrompt(session string, paneIndex int) (bool, error) {
	return rm.SendRecoveryPromptByID(session, paneIndex, makePaneID(session, paneIndex), "")
}

// SendRecoveryPromptByID preserves an explicit tmux pane ID across window changes.
func (rm *RecoveryManager) SendRecoveryPromptByID(session string, paneIndex int, paneID, triggerText string) (bool, error) {
	kind, err := rm.recoveryPaneType(session, paneIndex, paneID)
	if err != nil {
		return false, err
	}
	return rm.sendRecoveryPromptByIDForAgent(session, paneIndex, paneID, triggerText, kind)
}

func (rm *RecoveryManager) sendRecoveryPromptByIDForAgent(session string, paneIndex int, paneID, triggerText string, kind agent.AgentType) (bool, error) {
	return rm.deliverRecovery(context.Background(), session, paneIndex, paneID, triggerText, kind,
		func(_ context.Context, prompt string, include bool) string {
			return BuildContextAwarePrompt(prompt, include)
		}, nil)
}

// deliverRecovery never retries a possibly partial send. Cooldown and attempt
// count are reserved immediately before delivery and retained on delivery error;
// such an error does not prove no input reached the agent. Pre-send failures do
// not consume the attempt budget. No state mutex is held during external I/O.
func (rm *RecoveryManager) deliverRecovery(ctx context.Context, session string, paneIndex int, paneID, triggerText string, kind agent.AgentType,
	build func(context.Context, string, bool) string, authorize func(context.Context) error,
) (bool, error) {
	if ctx == nil {
		return false, errors.New("recovery requires a context")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := kind.ValidateAutomatedPromptDelivery(); err != nil {
		return false, err
	}
	rm.mu.Lock()
	if allowed, _ := rm.canSendRecoveryLocked(paneID); !allowed {
		rm.mu.Unlock()
		return false, nil
	}
	if rm.inFlight == nil {
		rm.inFlight = make(map[string]bool)
	}
	rm.inFlight[paneID] = true
	prompt, include := rm.prompt, rm.includeBeadContext
	rm.mu.Unlock()
	defer func() { rm.mu.Lock(); delete(rm.inFlight, paneID); rm.mu.Unlock() }()

	promptToSend := build(ctx, prompt, include)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if authorize != nil {
		if err := authorize(ctx); err != nil {
			return false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	target := makePaneID(session, paneIndex)
	if strings.HasPrefix(paneID, "%") {
		target = paneID
	}
	now := time.Now()
	rm.mu.Lock()
	rm.lastRecovery[paneID] = now
	rm.recoveryCount[paneID]++
	rm.mu.Unlock()
	err := rm.sendRecoveryPrompt(ctx, target, promptToSend, true, kind)
	if err != nil {
		return false, err
	}
	rm.mu.Lock()
	rm.recoveryEvents = append(rm.recoveryEvents, RecoveryEvent{PaneID: paneID, Session: session, PaneIndex: paneIndex,
		SentAt: time.Now(), Prompt: promptToSend, TriggerText: triggerText})
	rm.pruneEvents()
	rm.mu.Unlock()
	return true, nil
}

func (rm *RecoveryManager) recoveryPaneType(session string, paneIndex int, paneID string) (agent.AgentType, error) {
	if rm.resolvePaneType != nil {
		return rm.resolvePaneType(session, paneIndex, paneID)
	}
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return agent.AgentTypeUnknown, fmt.Errorf("resolve recovery pane type: %w", err)
	}
	var match *tmux.Pane
	for i := range panes {
		p := &panes[i]
		if strings.HasPrefix(paneID, "%") {
			if p.ID != paneID {
				continue
			}
		} else if p.Index != paneIndex {
			continue
		}
		if match != nil {
			return agent.AgentTypeUnknown, errors.New("recovery pane index is ambiguous; use its tmux ID")
		}
		match = p
	}
	if match == nil {
		return agent.AgentTypeUnknown, fmt.Errorf("recovery pane %q not found in session %q", paneID, session)
	}
	return match.Type, nil
}

// sendRecoveryPrompt retains agent-aware multiline buffering for explicit sends.
func (rm *RecoveryManager) sendRecoveryPrompt(ctx context.Context, target, prompt string, enter bool, kind agent.AgentType) error {
	if rm.sendPromptContext != nil {
		return rm.sendPromptContext(ctx, target, prompt, enter, kind)
	}
	if rm.sendPrompt != nil {
		return rm.sendPrompt(target, prompt, enter)
	}
	return tmux.SendKeysForAgentContext(ctx, target, prompt, enter, tmux.AgentType(kind))
}

func (rm *RecoveryManager) GetRecoveryEvents() []RecoveryEvent {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.pruneEvents()
	result := make([]RecoveryEvent, len(rm.recoveryEvents))
	copy(result, rm.recoveryEvents)
	return result
}
func (rm *RecoveryManager) GetRecoveryCount(paneID string) int {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.recoveryCount[paneID]
}
func (rm *RecoveryManager) GetLastRecoveryTime(paneID string) (time.Time, bool) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	last, ok := rm.lastRecovery[paneID]
	return last, ok
}
func (rm *RecoveryManager) ResetPane(paneID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	delete(rm.lastRecovery, paneID)
	delete(rm.recoveryCount, paneID)
}
func (rm *RecoveryManager) SetPrompt(prompt string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.prompt = prompt
}
func (rm *RecoveryManager) SetCooldown(cooldown time.Duration) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.cooldown = cooldown
}
func (rm *RecoveryManager) pruneEvents() {
	cutoff := time.Now().Add(-rm.maxEventAge)
	kept := rm.recoveryEvents[:0]
	for _, e := range rm.recoveryEvents {
		if e.SentAt.After(cutoff) {
			kept = append(kept, e)
		}
	}
	rm.recoveryEvents = kept
}
func makePaneID(session string, paneIndex int) string {
	return fmt.Sprintf("%s:.%d", session, paneIndex)
}

type BeadContext struct {
	TopBottlenecks  []string
	NextActions     []string
	HealthStatus    string
	HasDrift        bool
	InProgressTasks []string
	BlockedCount    int
	ReadyCount      int
	TopBlockers     []string
}

func BuildContextAwarePrompt(basePrompt string, includeBeadContext bool) string {
	if !includeBeadContext {
		return basePrompt
	}
	return appendRecoveryBeadContext(basePrompt, GetBeadContext())
}

// appendRecoveryBeadContext is shared by manual and monitor-scoped enrichment.
func appendRecoveryBeadContext(base string, ctx *BeadContext) string {
	if ctx == nil {
		return base
	}
	var sb strings.Builder
	sb.WriteString(base)
	sb.WriteString("\n\n# Project Context from Beads\n")
	writeList := func(heading string, values []string) {
		if len(values) == 0 {
			return
		}
		sb.WriteString(heading)
		for _, value := range values {
			fmt.Fprintf(&sb, "- %s\n", value)
		}
	}
	writeList("\n## Current Bottlenecks (resolve these to unblock progress):\n", ctx.TopBottlenecks)
	writeList("\n## Recommended Next Actions:\n", ctx.NextActions)
	if ctx.HealthStatus != "" {
		fmt.Fprintf(&sb, "\n## Project Health: %s\n", ctx.HealthStatus)
	}
	if ctx.HasDrift {
		sb.WriteString("\n**Warning**: Project has drifted from baseline. Consider running `bv` to review.\n")
	}
	if len(ctx.InProgressTasks) > 0 || ctx.BlockedCount > 0 || len(ctx.TopBlockers) > 0 {
		sb.WriteString("\n## Dependency Summary\n")
		writeList("\n### Tasks In Progress:\n", ctx.InProgressTasks)
		if ctx.BlockedCount > 0 || ctx.ReadyCount > 0 {
			fmt.Fprintf(&sb, "\n**Status**: %d blocked, %d ready to work on\n", ctx.BlockedCount, ctx.ReadyCount)
		}
		writeList("\n### Top Blockers (completing these unblocks many tasks):\n", ctx.TopBlockers)
	}
	return sb.String()
}

// GetBeadContext is the explicit/manual current-project context reader.
func GetBeadContext() *BeadContext {
	if !bv.IsInstalled() {
		return nil
	}
	ctx := &BeadContext{}
	if bottlenecks, err := bv.GetTopBottlenecks("", 3); err == nil {
		for _, b := range bottlenecks {
			ctx.TopBottlenecks = append(ctx.TopBottlenecks, b.ID)
		}
	}
	if actions, err := bv.GetNextActions("", 3); err == nil {
		for _, a := range actions {
			ctx.NextActions = append(ctx.NextActions, fmt.Sprintf("[%s] %s", a.IssueID, a.Title))
		}
	}
	if health, err := bv.GetHealthSummary(""); err == nil && health != nil {
		ctx.HealthStatus = health.DriftStatus.String()
		ctx.HasDrift = health.DriftStatus == bv.DriftCritical || health.DriftStatus == bv.DriftWarning
	}
	if bv.IsBdInstalled() {
		if dep, err := bv.GetDependencyContext("", 5); err == nil && dep != nil {
			ctx.BlockedCount, ctx.ReadyCount = dep.BlockedCount, dep.ReadyCount
			for _, task := range dep.InProgressTasks {
				ctx.InProgressTasks = append(ctx.InProgressTasks, fmt.Sprintf("[%s] %s", task.ID, task.Title))
			}
			for _, blocker := range dep.TopBlockers {
				blockedBy := ""
				if len(blocker.BlockedBy) > 0 {
					blockedBy = fmt.Sprintf(" (blocked by: %s)", strings.Join(blocker.BlockedBy, ", "))
				}
				ctx.TopBlockers = append(ctx.TopBlockers, fmt.Sprintf("[%s] %s%s", blocker.ID, blocker.Title, blockedBy))
			}
		}
	}
	return ctx
}

// Monitor enrichment uses one bounded query in the manifest project. It does
// not use the monitor process's CWD, claim tasks, or authorize assignment.
func buildMonitoredRecoveryPrompt(ctx context.Context, project, base string, include bool) string {
	if !include || !filepath.IsAbs(project) || !bv.IsInstalled() {
		return base
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	triage, err := bv.GetTriageContext(readCtx, project)
	if err != nil || triage == nil {
		return base
	}
	beads := &BeadContext{}
	for i, recommendation := range triage.Triage.Recommendations {
		if i == 3 {
			break
		}
		beads.NextActions = append(beads.NextActions, fmt.Sprintf("[%s] %s", recommendation.ID, recommendation.Title))
	}
	for i, blocker := range triage.Triage.BlockersToClear {
		if i == 3 {
			break
		}
		beads.TopBottlenecks = append(beads.TopBottlenecks, blocker.ID)
	}
	return appendRecoveryBeadContext(base, beads)
}

// CompactionRecoveryIntegration exposes read-only dashboard observations and
// monitor-owned, synchronous recovery through the same detector and manager.
type CompactionRecoveryIntegration struct {
	detector *CompactionDetector
	recovery *RecoveryManager
	gate     chan struct{}
	pending  map[string]CompactionEvent
	panes    map[string]recoveryPaneLifetime
}
type recoveryPaneLifetime struct {
	pid  int
	kind string
}

func NewCompactionRecoveryIntegration(config RecoveryConfig) *CompactionRecoveryIntegration {
	return &CompactionRecoveryIntegration{detector: NewCompactionDetector(5 * time.Minute), recovery: NewRecoveryManager(config),
		gate: make(chan struct{}, 1), pending: make(map[string]CompactionEvent), panes: make(map[string]recoveryPaneLifetime)}
}

// RecoveryAttempt is an outcome of one monitored recovery. An error may include
// partial delivery; it is reported, never converted into an automatic retry.
type RecoveryAttempt struct {
	PaneID string `json:"pane_id"`
	Sent   bool   `json:"sent"`
	Error  string `json:"error,omitempty"`
}

var errRecoveryPaneBusy = errors.New("recovery pane is no longer idle")

// RecoverObserved must be called by the owner of the session monitor lease.
// Detection may happen while an agent is working; its reminder waits for fresh
// idle evidence. A second observation, after enrichment and immediately before
// sending, rechecks identity, liveness metadata, freshness and idle state.
// The initial capture and captures after observation gaps only establish a
// baseline. No worker outlives ctx, and delivery uses the durable tmux pane ID.
func (cri *CompactionRecoveryIntegration) RecoverObserved(ctx context.Context, observation SessionObservation, project string,
	observe func(context.Context, string) (SessionObservation, error),
) []RecoveryAttempt {
	if ctx == nil || observe == nil || observation.Session == "" {
		return nil
	}
	select {
	case cri.gate <- struct{}{}:
	case <-ctx.Done():
		return nil
	}
	defer func() { <-cri.gate }()
	if ctx.Err() != nil {
		return nil
	}
	if !DispatchObservationIsCurrent(observation.ObservedAt, time.Now()) {
		cri.detector.Clear()
		clear(cri.pending)
		return nil
	}
	seen := make(map[string]bool)
	var attempts []RecoveryAttempt
	for _, pane := range observation.Panes {
		id := pane.Pane.ID
		if id == "" {
			continue
		}
		seen[id] = true
		unique, ok := observation.PaneByID(id)
		if !ok || !recoveryObservationValid(unique, observation.ObservedAt) {
			delete(cri.pending, id)
			cri.detector.ForgetPane(id)
			continue
		}
		life := recoveryPaneLifetime{pid: pane.Metadata.PID, kind: normalizedCompactionAgentType(pane.AgentType)}
		if old, ok := cri.panes[id]; ok && old != life {
			delete(cri.pending, id)
			cri.detector.ForgetPane(id)
			cri.recovery.ResetPane(id)
		}
		cri.panes[id] = life
		if event := cri.detector.Check(pane.RawOutput, pane.AgentType, id); event != nil {
			cri.pending[id] = *event
		}
		event, pending := cri.pending[id]
		if !pending {
			continue
		}
		if time.Since(event.DetectedAt) > 5*time.Minute || !cri.detector.HasRecentCompaction(id, 5*time.Minute) {
			delete(cri.pending, id)
			continue
		}
		if !pane.SafeToDispatch() {
			continue
		}
		if allowed, _ := cri.recovery.CanSendRecovery(id); !allowed {
			continue
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		sent, err := cri.recovery.deliverRecovery(attemptCtx, observation.Session, pane.Pane.PaneIndex, id, event.MatchedText,
			agent.AgentType(pane.AgentType),
			func(buildCtx context.Context, base string, include bool) string {
				return buildMonitoredRecoveryPrompt(buildCtx, project, base, include)
			},
			func(checkCtx context.Context) error {
				// Reuse the launcher's stable non-shell process check. A durable
				// agent tag and an old prompt do not make a bare shell an agent.
				if cri.recovery.verifyProcess != nil {
					if err := cri.recovery.verifyProcess(checkCtx, observation.Session, id); err != nil {
						return err
					}
				} else if _, err := tmux.WaitForPaneProcessStartContext(checkCtx, observation.Session, id); err != nil {
					return fmt.Errorf("verify recovery process: %w", err)
				}
				fresh, err := observe(checkCtx, observation.Session)
				if err != nil {
					return fmt.Errorf("recheck recovery pane: %w", err)
				}
				current, exists := fresh.PaneByID(id)
				if fresh.Session != observation.Session || !exists || !recoveryObservationValid(current, fresh.ObservedAt) ||
					current.Metadata.PID != life.pid || normalizedCompactionAgentType(current.AgentType) != life.kind {
					return errors.New("recovery pane identity or observation changed")
				}
				if !current.SafeToDispatch() {
					return errRecoveryPaneBusy
				}
				return nil
			})
		cancel()
		if errors.Is(err, errRecoveryPaneBusy) {
			continue
		}
		if sent || err != nil {
			delete(cri.pending, id)
			attempt := RecoveryAttempt{PaneID: id, Sent: sent}
			if err != nil {
				attempt.Error = err.Error()
			}
			attempts = append(attempts, attempt)
		}
		if ctx.Err() != nil {
			break
		}
	}
	if observation.Complete {
		for id := range cri.panes {
			if !seen[id] {
				delete(cri.pending, id)
				delete(cri.panes, id)
				cri.detector.ForgetPane(id)
				cri.recovery.ResetPane(id)
			}
		}
	}
	return attempts
}

func recoveryObservationValid(pane PaneObservation, observedAt time.Time) bool {
	now := time.Now()
	return strings.HasPrefix(pane.Pane.ID, "%") && pane.Metadata.ID == pane.Pane.ID &&
		pane.Metadata.PID > 0 && !pane.Metadata.Dead && !pane.Metadata.IsServicePane() && pane.RawOutput != "" &&
		pane.Current.Freshness == FreshnessFresh && pane.Current.Error == "" &&
		pane.Current.Status.ErrorType == ErrorNone && ObservationConfidenceIsActionable(pane.Current.Confidence) &&
		normalizedCompactionAgentType(string(pane.Metadata.Type)) == normalizedCompactionAgentType(pane.AgentType) &&
		DispatchObservationIsCurrent(observedAt, now) && DispatchObservationIsCurrent(pane.Current.ObservedAt, now)
}

func (cri *CompactionRecoveryIntegration) Detector() *CompactionDetector { return cri.detector }
func (cri *CompactionRecoveryIntegration) Recovery() *RecoveryManager    { return cri.recovery }
