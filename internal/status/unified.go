package status

import (
	"context"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// UnifiedDetector implements the Detector interface by combining
// activity, prompt, and error detection into a unified status check.
type UnifiedDetector struct {
	config DetectorConfig

	// paneContentMu guards paneContent. Long-lived detectors (the pipeline
	// executor's wait loop, watch loops) call Detect repeatedly for the same
	// pane, which lets us derive a PANE-LOCAL activity signal from capture
	// changes. tmux itself cannot provide one: pane_last_activity does not
	// exist (tmux <= 3.5a renders it as ""), and #{window_activity} is
	// window-scoped, so a busy neighbor pane in the same window keeps a
	// finished pane looking eternally active — the wait:completion timeout
	// direction of ntm#213.
	paneContentMu sync.Mutex
	paneContent   map[string]paneContentObservation
}

// paneContentObservation remembers the last captured content fingerprint for
// a pane and when that fingerprint was first seen.
type paneContentObservation struct {
	fingerprint uint64
	changedAt   time.Time
}

// NewDetector creates a new UnifiedDetector with default configuration
func NewDetector() *UnifiedDetector {
	return &UnifiedDetector{
		config: DefaultConfig(),
	}
}

// NewDetectorWithConfig creates a new UnifiedDetector with custom configuration
func NewDetectorWithConfig(config DetectorConfig) *UnifiedDetector {
	return &UnifiedDetector{
		config: config,
	}
}

// Config returns the current detector configuration
func (d *UnifiedDetector) Config() DetectorConfig {
	return d.config
}

// Analyze determines status from provided output and metadata without calling tmux.
// This allows reusing output captured for other purposes (e.g. live view).
func (d *UnifiedDetector) Analyze(paneID, paneName, agentType string, output string, lastActivity time.Time) AgentStatus {
	return d.AnalyzeAt(paneID, paneName, agentType, output, lastActivity, time.Now())
}

// AnalyzeAt is the deterministic form of Analyze. observedAt controls both the
// result timestamp and activity-recency classification for replayable snapshots.
func (d *UnifiedDetector) AnalyzeAt(paneID, paneName, agentType string, output string, lastActivity, observedAt time.Time) AgentStatus {
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	normalizedAgentType := string(agent.AgentType(agentType).Canonical())
	status := AgentStatus{
		PaneID:     paneID,
		PaneName:   paneName,
		AgentType:  normalizedAgentType,
		LastActive: lastActivity,
		UpdatedAt:  observedAt,
		State:      StateUnknown,
		LastOutput: truncateOutput(output, d.config.OutputPreviewLength),
	}

	state, errType := d.determineStateAt(output, normalizedAgentType, lastActivity, observedAt)
	status.State = state
	status.ErrorType = errType

	// Extract metrics using agent parser
	if isKnownAgentType(normalizedAgentType) {
		parser := agent.NewParser()
		if parsed, err := parser.ParseWithHint(output, agent.AgentType(normalizedAgentType)); err == nil {
			if parsed.ContextRemaining != nil {
				status.ContextUsage = 100.0 - *parsed.ContextRemaining
			}
			if parsed.TokensUsed != nil {
				status.TokensUsed = *parsed.TokensUsed
			}
		}
	}

	return status
}

// determineState calculates state based on output and activity
func (d *UnifiedDetector) determineState(output, agentType string, lastActivity time.Time) (AgentState, ErrorType) {
	return d.determineStateAt(output, agentType, lastActivity, time.Now())
}

func (d *UnifiedDetector) determineStateAt(output, agentType string, lastActivity, observedAt time.Time) (AgentState, ErrorType) {
	// Detection priority:
	// 1. Check authoritative live-tail markers for active work
	// 2. Check for idle prompt when velocity is low (agent waiting for input)
	// 3. Check for errors (but only if not clearly at a prompt)
	// 4. Check activity recency and agent-parser work evidence
	// 5. Heuristic check for likely-idle state
	//
	// Key insight: if an agent is showing an idle prompt and not actively outputting,
	// it should be classified as WAITING regardless of historical error messages
	// in the scrollback. Error patterns from earlier in the session are not relevant
	// when the agent has clearly recovered and is now waiting for input.

	agentType = string(agent.AgentType(agentType).Canonical())
	threshold := time.Duration(d.config.ActivityThreshold) * time.Second
	isLowVelocity := observedAt.Sub(lastActivity) >= threshold

	// Claude Code: the live-tail spinner classifier is authoritative and must
	// outrank the velocity heuristic. A long "thinking" turn produces no new
	// scrollback lines (low velocity) yet is unambiguously working; conversely a
	// finished turn keeps the input box drawn (looks prompt-like) yet is idle.
	// ClaudeActivelyWorking is biased to false-WORKING, so trusting it here can
	// only ever err on the safe side.
	if agentType == string(agent.AgentTypeClaudeCode) {
		if agent.ClaudeActivelyWorking(output) {
			return StateWorking, ErrorNone
		}
		if DetectIdleFromOutput(output, agentType) {
			return StateIdle, ErrorNone
		}
		// Fall through to error/heuristic handling below.
	}

	// Codex, like Claude, keeps its input chrome visible while a turn is in
	// flight. A structural spinner or interrupt hint in the bounded live tail is
	// therefore stronger evidence than a co-present chevron or a stale activity
	// timestamp. CodexActivelyWorking deliberately ignores markers that have
	// scrolled out of the live window.
	if agentType == string(agent.AgentTypeCodex) {
		if agent.CodexActivelyWorking(output) {
			return StateWorking, ErrorNone
		}
		// #234: mirror the Claude arm above. Codex only had the positive
		// short-circuit, so its idle verdict still had to survive the velocity
		// gate below — and velocity comes from the WINDOW-scoped
		// #{window_activity}, which any busy sibling pane keeps fresh. A Codex
		// pane parked at its composer in a busy window therefore never reached
		// the idle branch and fell through to "recent activity ⇒ working".
		// CodexActivelyWorking is biased to false-WORKING and has already
		// returned false here, so trusting the idle chrome cannot mask a live
		// turn.
		if DetectIdleFromOutput(output, agentType) {
			return StateIdle, ErrorNone
		}
	}

	// Check if at prompt (idle) - prioritize this when velocity is low
	isAtPrompt := DetectIdleFromOutput(output, agentType)
	if isAtPrompt && isLowVelocity {
		// Agent is at prompt and not actively outputting - clearly idle
		return StateIdle, ErrorNone
	}

	// Check for errors (only relevant if not clearly at a prompt waiting for input)
	if errType := DetectErrorInOutput(output); errType != ErrorNone {
		return StateError, errType
	}

	// Heuristic: for user panes with empty output, treat as idle
	if agentType == "" || agentType == "user" {
		if strings.TrimSpace(output) == "" {
			return StateIdle, ErrorNone
		}
	}

	// Recent activity means the agent is producing output — trust that
	// signal over prompt-like patterns, since agents often print >, ❯,
	// etc. as part of active work (code examples, progress lines).
	if !isLowVelocity {
		return StateWorking, ErrorNone
	}

	// Some agents expose useful work verbs but no durable spinner or tmux
	// activity update. Reuse the canonical agent parser for that positive
	// evidence after prompt and error handling, so low-velocity Windsurf/Cursor
	// panes do not collapse to unknown while preserving the detector's existing
	// idle and error priorities.
	switch agent.AgentType(agentType) {
	case agent.AgentTypeGemini,
		agent.AgentTypeCursor,
		agent.AgentTypeWindsurf,
		agent.AgentTypeAider,
		agent.AgentTypeOllama:
		parser := agent.NewParser()
		if parsed, err := parser.ParseWithHint(output, agent.AgentType(agentType)); err == nil && parsed.IsWorking {
			return StateWorking, ErrorNone
		}
	}

	// Defensive: prompt + low velocity should have been caught at line 88,
	// but keep this as a safety net in case the checks above are reordered.
	if isAtPrompt {
		return StateIdle, ErrorNone
	}

	// Heuristic: if no recent activity and output suggests agent is waiting,
	// prefer idle over unknown. This catches cases where:
	// - The prompt pattern isn't recognized but the agent is clearly done
	// - The last line is short (typical of prompts)
	// - The output ends without indication of ongoing work
	if looksLikeIdle(output) {
		return StateIdle, ErrorNone
	}

	// For known AI agent types (cc, cod, gmi) with no recent activity and no
	// prompt detected, use the looksLikeIdle heuristic above. If that didn't
	// match either, default to unknown rather than falsely reporting idle —
	// false idle readings cause operators to send redundant prompts and miss
	// that agents are actually working.
	return StateUnknown, ErrorNone
}

// isKnownAgentType returns true for AI agent types that have predictable
// working/idle behavior (cc=Claude Code, cod=Codex, gmi=Gemini).
func isKnownAgentType(agentType string) bool {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode,
		agent.AgentTypeCodex,
		agent.AgentTypeGemini,
		agent.AgentTypeCursor,
		agent.AgentTypeWindsurf,
		agent.AgentTypeAider,
		agent.AgentTypeOllama:
		return true
	default:
		return false
	}
}

// looksLikeIdle applies heuristics to detect likely idle state when
// explicit prompt patterns don't match. This reduces false "unknown" states.
func looksLikeIdle(output string) bool {
	clean := StripANSI(output)
	clean = strings.TrimSpace(clean)

	if clean == "" {
		return false
	}

	lines := strings.Split(clean, "\n")
	if len(lines) == 0 {
		return false
	}

	// Check the last non-empty line
	var lastLine string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			lastLine = line
			break
		}
	}

	if lastLine == "" {
		return false
	}

	// Heuristic 1: Last line is very short (< 20 chars) - likely a prompt
	if len(lastLine) < 20 {
		return true
	}

	// Heuristic 2: Last line ends with common prompt characters
	promptEndings := []string{">", "$", "%", ":", "❯", "→", "»", "#"}
	for _, ending := range promptEndings {
		if strings.HasSuffix(lastLine, ending) || strings.HasSuffix(lastLine, ending+" ") {
			return true
		}
	}

	// Heuristic 3: Last line contains common "done" indicators
	doneIndicators := []string{
		"completed",
		"finished",
		"done",
		"ready",
		"success",
	}
	lowerLine := strings.ToLower(lastLine)
	for _, indicator := range doneIndicators {
		if strings.Contains(lowerLine, indicator) {
			return true
		}
	}

	return false
}

// observePaneContent folds a freshly captured pane output into the detector's
// per-pane content history and returns the pane-local activity estimate: the
// time the captured content last changed as seen by this detector instance.
// hasHistory is false for the first observation of a pane, where a single
// capture carries no change information.
func (d *UnifiedDetector) observePaneContent(paneID, output string, observedAt time.Time) (contentChangedAt time.Time, hasHistory bool) {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(output))
	fingerprint := hasher.Sum64()

	d.paneContentMu.Lock()
	defer d.paneContentMu.Unlock()
	if d.paneContent == nil {
		d.paneContent = make(map[string]paneContentObservation)
	}
	prev, seen := d.paneContent[paneID]
	if seen && prev.fingerprint == fingerprint {
		return prev.changedAt, true
	}
	d.paneContent[paneID] = paneContentObservation{fingerprint: fingerprint, changedAt: observedAt}
	return observedAt, seen
}

// Detect returns the current status of a single pane.
// Detection priority: error > idle > working > unknown
func (d *UnifiedDetector) Detect(paneID string) (AgentStatus, error) {
	status := AgentStatus{
		PaneID:    paneID,
		UpdatedAt: time.Now(),
		State:     StateUnknown,
	}

	// Get pane activity time
	lastActivity, err := tmux.GetPaneActivity(paneID)
	if err != nil {
		return status, err
	}
	status.LastActive = lastActivity

	// Capture recent output for analysis
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := tmux.CapturePaneOutputContext(ctx, paneID, d.config.ScanLines)
	if err != nil {
		return status, err
	}
	if strings.TrimSpace(output) == "" {
		// Give tmux a brief moment to flush output, then retry once
		time.Sleep(100 * time.Millisecond)
		ctxRetry, cancelRetry := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRetry()
		if retry, err := tmux.CapturePaneOutputContext(ctxRetry, paneID, d.config.ScanLines); err == nil {
			output = retry
		}
	}
	status.LastOutput = truncateOutput(output, d.config.OutputPreviewLength)

	// Pane-local activity refinement (ntm#213): #{window_activity} is the best
	// signal tmux offers, but it is window-scoped — any neighbor pane's output
	// refreshes it, so a finished pane in a busy window never looks idle and
	// wait:completion times out with the answer sitting in the pane. When this
	// detector has observed the same pane before, the time its captured
	// content last changed is a strictly pane-local activity bound; use it
	// when it is older than the window-scoped timestamp. Agent-specific
	// spinner classifiers still outrank velocity in determineState, so a
	// quiet-but-working pane (e.g. Claude thinking) is unaffected.
	if contentChangedAt, hasHistory := d.observePaneContent(paneID, output, status.UpdatedAt); hasHistory && contentChangedAt.Before(status.LastActive) {
		status.LastActive = contentChangedAt
	}

	// Resolve the target to the physical pane that supplied the output. A tmux
	// target such as "session:0.1" is not a pane ID, so comparing it directly
	// with "%N" loses the pane's title and agent type.
	panes, err := tmux.GetPanesWithActivity(paneID)
	if err != nil {
		return status, err
	}
	pane, err := resolveDetectedPane(panes, paneID)
	if err != nil {
		return status, err
	}
	status.PaneName = pane.Title
	status.AgentType = string(pane.Type)

	// Use shared logic
	state, errType := d.determineState(output, status.AgentType, status.LastActive)
	status.State = state
	status.ErrorType = errType

	// Extract metrics using agent parser
	if isKnownAgentType(status.AgentType) {
		parser := agent.NewParser()
		if parsed, err := parser.ParseWithHint(output, agent.AgentType(status.AgentType)); err == nil {
			if parsed.ContextRemaining != nil {
				status.ContextUsage = 100.0 - *parsed.ContextRemaining
			}
			if parsed.TokensUsed != nil {
				status.TokensUsed = *parsed.TokensUsed
			}
		}
	}

	return status, nil
}

func resolveDetectedPane(panes []tmux.PaneActivity, target string) (tmux.Pane, error) {
	selector := target
	if _, paneSelector, hasSession := strings.Cut(target, ":"); hasSession {
		selector = paneSelector
	}

	available := make([]tmux.Pane, 0, len(panes))
	for _, pane := range panes {
		available = append(available, pane.Pane)
	}
	resolved, err := tmux.ResolvePaneSelectors(available, []string{selector}, true)
	if err != nil {
		return tmux.Pane{}, err
	}
	return resolved[0], nil
}

// DetectAll returns status for all panes in a session.
// Errors on individual panes don't fail the entire operation.
func (d *UnifiedDetector) DetectAll(session string) ([]AgentStatus, error) {
	return d.DetectAllContext(context.Background(), session)
}

// DetectAllContext returns status for all panes in a session with cancellation support.
// Errors on individual panes don't fail the entire operation.
func (d *UnifiedDetector) DetectAllContext(ctx context.Context, session string) ([]AgentStatus, error) {
	observation, err := NewSessionObserver(d).Observe(ctx, session)
	if err != nil {
		return nil, err
	}
	statuses := make([]AgentStatus, 0, len(observation.Panes))
	for _, pane := range observation.Panes {
		statuses = append(statuses, pane.Current.Status)
	}
	return statuses, nil
}

const (
	defaultObservationCaptureTimeout = 2 * time.Second
	defaultObservationMaxConcurrent  = 8
	defaultObservationMaxBytes       = 128 * 1024
	hardObservationMaxLines          = 500
	hardObservationMaxConcurrent     = 32
	hardObservationMaxBytes          = 1024 * 1024
	hardObservationMaxTimeout        = 10 * time.Second
)

// SessionObserverConfig bounds the work and memory used by one observation.
type SessionObserverConfig struct {
	CaptureLines          int
	CaptureTimeout        time.Duration
	MaxConcurrentCaptures int
	MaxCaptureBytes       int
}

// DefaultSessionObserverConfig derives the line budget from the detector and
// supplies conservative hard bounds for concurrency, duration, and output.
func DefaultSessionObserverConfig(detectorConfig DetectorConfig) SessionObserverConfig {
	return SessionObserverConfig{
		CaptureLines:          detectorConfig.ScanLines,
		CaptureTimeout:        defaultObservationCaptureTimeout,
		MaxConcurrentCaptures: defaultObservationMaxConcurrent,
		MaxCaptureBytes:       defaultObservationMaxBytes,
	}
}

// SessionObserverDependencies makes topology, capture, and time injectable for
// deterministic tests and alternate tmux clients.
type SessionObserverDependencies struct {
	ListPanes   func(context.Context, string) ([]tmux.PaneActivity, error)
	CapturePane func(context.Context, string, int) (string, error)
	Now         func() time.Time
}

// SessionObserver produces canonical session observations and retains only the
// last successful known state for each currently present pane.
type SessionObserver struct {
	detector *UnifiedDetector
	config   SessionObserverConfig
	deps     SessionObserverDependencies
	mu       sync.Mutex
	sessions map[string]*sessionObservationCache
}

type sessionObservationCache struct {
	mu           sync.Mutex
	lastKnown    map[string]StateObservation
	lastTopology []tmux.PaneActivity
}

// NewSessionObserver creates an observer backed by the default tmux client.
func NewSessionObserver(detector *UnifiedDetector) *SessionObserver {
	if detector == nil {
		detector = NewDetector()
	}
	return NewSessionObserverWithDependencies(
		detector,
		DefaultSessionObserverConfig(detector.Config()),
		SessionObserverDependencies{},
	)
}

// NewSessionObserverWithDependencies creates an observer with explicit,
// normalized bounds and injectable I/O.
func NewSessionObserverWithDependencies(detector *UnifiedDetector, config SessionObserverConfig, deps SessionObserverDependencies) *SessionObserver {
	if detector == nil {
		detector = NewDetector()
	}
	config = normalizeSessionObserverConfig(config, detector.Config())
	if deps.ListPanes == nil {
		deps.ListPanes = tmux.GetPanesWithActivityContext
	}
	if deps.CapturePane == nil {
		deps.CapturePane = tmux.CapturePaneOutputContext
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &SessionObserver{
		detector: detector,
		config:   config,
		deps:     deps,
		sessions: make(map[string]*sessionObservationCache),
	}
}

func normalizeSessionObserverConfig(config SessionObserverConfig, detectorConfig DetectorConfig) SessionObserverConfig {
	if config.CaptureLines <= 0 {
		config.CaptureLines = detectorConfig.ScanLines
	}
	if config.CaptureLines <= 0 {
		config.CaptureLines = DefaultConfig().ScanLines
	}
	if config.CaptureLines > hardObservationMaxLines {
		config.CaptureLines = hardObservationMaxLines
	}
	if config.CaptureTimeout <= 0 {
		config.CaptureTimeout = defaultObservationCaptureTimeout
	}
	if config.CaptureTimeout > hardObservationMaxTimeout {
		config.CaptureTimeout = hardObservationMaxTimeout
	}
	if config.MaxConcurrentCaptures <= 0 {
		config.MaxConcurrentCaptures = defaultObservationMaxConcurrent
	}
	if config.MaxConcurrentCaptures > hardObservationMaxConcurrent {
		config.MaxConcurrentCaptures = hardObservationMaxConcurrent
	}
	if config.MaxCaptureBytes <= 0 {
		config.MaxCaptureBytes = defaultObservationMaxBytes
	}
	if config.MaxCaptureBytes > hardObservationMaxBytes {
		config.MaxCaptureBytes = hardObservationMaxBytes
	}
	return config
}

type observationCaptureResult struct {
	index  int
	output string
	err    error
}

// Observe captures a deterministic session snapshot. A topology failure is the
// only operation-level error. Individual capture failures remain explicit pane
// observations so successful siblings are never discarded.
func (o *SessionObserver) Observe(ctx context.Context, session string) (SessionObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cache := o.sessionCache(session)
	cache.mu.Lock()
	defer cache.mu.Unlock()

	observedAt := o.deps.Now().UTC()
	result := SessionObservation{
		Session:    session,
		ObservedAt: observedAt,
		Complete:   false,
		Panes:      make([]PaneObservation, 0),
		Failures:   make([]ObservationFailure, 0),
	}

	panes, err := o.deps.ListPanes(ctx, session)
	if err != nil {
		result.Failures = append(result.Failures, ObservationFailure{Stage: "topology", Error: err.Error()})
		if len(cache.lastTopology) == 0 {
			return result, err
		}
		result.Panes = make([]PaneObservation, 0, len(cache.lastTopology))
		for _, pane := range cache.lastTopology {
			result.Panes = append(result.Panes, o.buildUnavailablePaneObservation(cache, pane, "topology", err, observedAt))
		}
		return result, err
	}
	panes = append([]tmux.PaneActivity(nil), panes...)
	sortPaneActivities(panes)
	if len(panes) == 0 {
		cache.lastKnown = make(map[string]StateObservation)
		cache.lastTopology = nil
		result.Complete = true
		return result, nil
	}
	cache.lastTopology = append([]tmux.PaneActivity(nil), panes...)

	captures := o.captureAll(ctx, panes)
	currentKeys := make(map[string]struct{}, len(panes))
	result.Panes = make([]PaneObservation, 0, len(panes))
	for index, pane := range panes {
		key := pane.Pane.Ref().StableKey()
		currentKeys[key] = struct{}{}
		capture := captures[index]
		observation := o.buildPaneObservation(cache, pane, capture.output, capture.err, observedAt)
		result.Panes = append(result.Panes, observation)
		if capture.err != nil {
			result.Failures = append(result.Failures, ObservationFailure{
				PaneID: pane.Pane.ID,
				Stage:  "capture",
				Error:  capture.err.Error(),
			})
		}
	}
	for key := range cache.lastKnown {
		if _, ok := currentKeys[key]; !ok {
			delete(cache.lastKnown, key)
		}
	}
	result.Complete = len(result.Failures) == 0
	return result, nil
}

// ObservePaneCapture incorporates output already captured by another consumer
// into the same last-known state machine without issuing another tmux command.
// It is safe to call concurrently with Observe; calls are serialized so the
// last-known cache remains race-free.
func (o *SessionObserver) ObservePaneCapture(session string, pane tmux.PaneActivity, output string, captureErr error) PaneObservation {
	cache := o.sessionCache(session)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	upsertCachedTopology(cache, pane)
	return o.buildPaneObservation(cache, pane, truncateOutput(output, o.config.MaxCaptureBytes), captureErr, o.deps.Now().UTC())
}

func (o *SessionObserver) sessionCache(session string) *sessionObservationCache {
	o.mu.Lock()
	defer o.mu.Unlock()
	cache := o.sessions[session]
	if cache == nil {
		cache = &sessionObservationCache{lastKnown: make(map[string]StateObservation)}
		o.sessions[session] = cache
	}
	return cache
}

func upsertCachedTopology(cache *sessionObservationCache, pane tmux.PaneActivity) {
	key := pane.Pane.Ref().StableKey()
	for index := range cache.lastTopology {
		if cache.lastTopology[index].Pane.Ref().StableKey() == key {
			cache.lastTopology[index] = pane
			sortPaneActivities(cache.lastTopology)
			return
		}
	}
	cache.lastTopology = append(cache.lastTopology, pane)
	sortPaneActivities(cache.lastTopology)
}

func (o *SessionObserver) captureAll(ctx context.Context, panes []tmux.PaneActivity) []observationCaptureResult {
	results := make([]observationCaptureResult, len(panes))
	jobs := make(chan int, len(panes))
	completed := make(chan observationCaptureResult, len(panes))
	for index := range panes {
		jobs <- index
	}
	close(jobs)

	workerCount := o.config.MaxConcurrentCaptures
	if workerCount > len(panes) {
		workerCount = len(panes)
	}
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				captureCtx, cancel := context.WithTimeout(ctx, o.config.CaptureTimeout)
				output, err := o.deps.CapturePane(captureCtx, panes[index].Pane.ID, o.config.CaptureLines)
				cancel()
				if err == nil {
					output = truncateOutput(output, o.config.MaxCaptureBytes)
				}
				completed <- observationCaptureResult{index: index, output: output, err: err}
			}
		}()
	}
	workers.Wait()
	close(completed)
	for capture := range completed {
		results[capture.index] = capture
	}
	return results
}

func (o *SessionObserver) buildPaneObservation(cache *sessionObservationCache, pane tmux.PaneActivity, output string, captureErr error, observedAt time.Time) PaneObservation {
	key := pane.Pane.Ref().StableKey()
	normalizedAgentType := string(agent.AgentType(pane.Pane.Type).Canonical())
	baseStatus := AgentStatus{
		PaneID:     pane.Pane.ID,
		PaneName:   pane.Pane.Title,
		AgentType:  normalizedAgentType,
		State:      StateUnknown,
		LastActive: pane.LastActivity,
		UpdatedAt:  observedAt,
	}
	topologyEvidence := ObservationEvidence{
		Provenance: ProvenanceTMUXTopology,
		ObservedAt: observedAt,
		Freshness:  FreshnessFresh,
		Confidence: 1,
	}
	observation := PaneObservation{
		Pane:      pane.Pane.Ref(),
		PaneName:  pane.Pane.Title,
		AgentType: normalizedAgentType,
		Metadata:  pane.Pane,
	}
	if captureErr != nil {
		observation.Current = StateObservation{
			Status:     baseStatus,
			ObservedAt: observedAt,
			Freshness:  FreshnessUnavailable,
			Confidence: 0,
			Evidence: []ObservationEvidence{
				topologyEvidence,
				{
					Provenance: ProvenanceTMUXCapture,
					ObservedAt: observedAt,
					Freshness:  FreshnessUnavailable,
					Confidence: 0,
					Error:      captureErr.Error(),
				},
			},
			Error: captureErr.Error(),
		}
		observation.LastKnown = staleStateObservation(cache.lastKnown[key], observedAt)
		return observation
	}

	// Pane-local activity refinement (ntm#213, extended for #234). The same
	// content-fingerprint bound UnifiedDetector.Detect applies has to be applied
	// here too: this observer is what --robot-is-working, wait:completion and
	// the dashboard actually run, and #{window_activity} is window-scoped, so a
	// chatty neighbor pane kept every quiet pane in the window looking fresh.
	// Only tighten the bound (the fingerprint's change time is never newer than
	// the window timestamp when it disagrees), and agent-specific classifiers
	// still outrank velocity in determineState, so a quiet-but-working pane is
	// unaffected.
	lastActivity := pane.LastActivity
	if contentChangedAt, hasHistory := o.detector.observePaneContent(pane.Pane.ID, output, observedAt); hasHistory && contentChangedAt.Before(lastActivity) {
		lastActivity = contentChangedAt
	}

	status := o.detector.AnalyzeAt(pane.Pane.ID, pane.Pane.Title, normalizedAgentType, output, lastActivity, observedAt)
	confidence := observationConfidence(status, output)
	observation.Current = StateObservation{
		Status:     status,
		ObservedAt: observedAt,
		Freshness:  FreshnessFresh,
		Confidence: confidence,
		Evidence: []ObservationEvidence{
			topologyEvidence,
			{
				Provenance: ProvenanceTMUXCapture,
				ObservedAt: observedAt,
				Freshness:  FreshnessFresh,
				Confidence: 1,
			},
			{
				Provenance: ProvenanceDetector,
				ObservedAt: observedAt,
				Freshness:  FreshnessFresh,
				Confidence: confidence,
			},
		},
	}
	observation.RawOutput = output
	if status.State != StateUnknown {
		cache.lastKnown[key] = cloneStateObservation(observation.Current)
	} else {
		observation.LastKnown = staleStateObservation(cache.lastKnown[key], observedAt)
	}
	return observation
}

func (o *SessionObserver) buildUnavailablePaneObservation(cache *sessionObservationCache, pane tmux.PaneActivity, stage string, observationErr error, observedAt time.Time) PaneObservation {
	key := pane.Pane.Ref().StableKey()
	normalizedAgentType := string(agent.AgentType(pane.Pane.Type).Canonical())
	errorText := observationErr.Error()
	return PaneObservation{
		Pane:      pane.Pane.Ref(),
		PaneName:  pane.Pane.Title,
		AgentType: normalizedAgentType,
		Metadata:  pane.Pane,
		Current: StateObservation{
			Status: AgentStatus{
				PaneID:     pane.Pane.ID,
				PaneName:   pane.Pane.Title,
				AgentType:  normalizedAgentType,
				State:      StateUnknown,
				LastActive: pane.LastActivity,
				UpdatedAt:  observedAt,
			},
			ObservedAt: observedAt,
			Freshness:  FreshnessUnavailable,
			Confidence: 0,
			Evidence: []ObservationEvidence{{
				Provenance: ProvenanceTMUXTopology,
				ObservedAt: observedAt,
				Freshness:  FreshnessUnavailable,
				Confidence: 0,
				Error:      stage + ": " + errorText,
			}},
			Error: errorText,
		},
		LastKnown: staleStateObservation(cache.lastKnown[key], observedAt),
	}
}

func observationConfidence(status AgentStatus, output string) float64 {
	if status.State == StateUnknown {
		return 0.25
	}
	if status.State == StateIdle && !DetectIdleFromOutput(output, status.AgentType) {
		// The detector's fallback idle heuristics intentionally improve display
		// quality for quiet panes, but a short line or a generic "done" marker is
		// not strong enough evidence to authorize dispatching more work.
		return 0.5
	}
	return 0.95
}

func cloneStateObservation(input StateObservation) StateObservation {
	copy := input
	copy.Evidence = append([]ObservationEvidence(nil), input.Evidence...)
	return copy
}

func staleStateObservation(input StateObservation, observedAt time.Time) *StateObservation {
	if input.ObservedAt.IsZero() {
		return nil
	}
	stale := cloneStateObservation(input)
	stale.Freshness = FreshnessStale
	stale.Evidence = append(stale.Evidence, ObservationEvidence{
		Provenance: ProvenanceLastKnown,
		ObservedAt: observedAt,
		Freshness:  FreshnessStale,
		Confidence: stale.Confidence,
	})
	return &stale
}

func sortPaneActivities(panes []tmux.PaneActivity) {
	sort.SliceStable(panes, func(i, j int) bool {
		left, right := panes[i], panes[j]
		if left.Pane.WindowIndex != right.Pane.WindowIndex {
			return left.Pane.WindowIndex < right.Pane.WindowIndex
		}
		if left.Pane.Index != right.Pane.Index {
			return left.Pane.Index < right.Pane.Index
		}
		if left.Pane.ID != right.Pane.ID {
			return left.Pane.ID < right.Pane.ID
		}
		if left.Pane.Title != right.Pane.Title {
			return left.Pane.Title < right.Pane.Title
		}
		if left.Pane.Type != right.Pane.Type {
			return left.Pane.Type < right.Pane.Type
		}
		return left.LastActivity.Before(right.LastActivity)
	})
}

// truncateOutput returns the last n bytes of output, respecting UTF-8 boundaries.
// If maxLen falls in the middle of a multi-byte rune, it advances to the next
// valid rune boundary to avoid producing invalid UTF-8.
func truncateOutput(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}

	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}

	// Start position for the tail
	start := len(s) - maxLen

	// If start is in the middle of a UTF-8 rune, advance to the next rune boundary.
	// UTF-8 continuation bytes have the form 10xxxxxx (0x80-0xBF).
	// We need to find the next byte that is NOT a continuation byte.
	for start < len(s) && s[start]&0xC0 == 0x80 {
		start++
	}

	return s[start:]
}

// GetStateSummary returns a summary of states for a set of statuses
func GetStateSummary(statuses []AgentStatus) map[AgentState]int {
	summary := make(map[AgentState]int)
	for _, s := range statuses {
		summary[s.State]++
	}
	return summary
}

// FilterByState returns only statuses matching the given state
func FilterByState(statuses []AgentStatus, state AgentState) []AgentStatus {
	var filtered []AgentStatus
	for _, s := range statuses {
		if s.State == state {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// FilterByAgentType returns only statuses for the given agent type
func FilterByAgentType(statuses []AgentStatus, agentType string) []AgentStatus {
	normalizedAgentType := string(agent.AgentType(agentType).Canonical())
	var filtered []AgentStatus
	for _, s := range statuses {
		if string(agent.AgentType(s.AgentType).Canonical()) == normalizedAgentType {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// HasErrors returns true if any status is in error state
func HasErrors(statuses []AgentStatus) bool {
	for _, s := range statuses {
		if s.State == StateError {
			return true
		}
	}
	return false
}

// AllHealthy returns true if all statuses are healthy (idle or working)
func AllHealthy(statuses []AgentStatus) bool {
	for _, s := range statuses {
		if !s.IsHealthy() {
			return false
		}
	}
	return len(statuses) > 0
}
