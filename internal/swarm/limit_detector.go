package swarm

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// LimitEvent represents a detected usage limit.
type LimitEvent struct {
	SessionPane string    `json:"session_pane"`
	AgentType   string    `json:"agent_type"`
	Pattern     string    `json:"pattern"`    // Which pattern matched
	RawOutput   string    `json:"raw_output"` // Last N lines
	DetectedAt  time.Time `json:"detected_at"`
}

// LimitDetector monitors panes for usage limit patterns.
type LimitDetector struct {
	// TmuxClient for capturing pane output.
	// If nil, the default tmux client is used.
	TmuxClient paneCapturer

	// Tracker records limit events for learning (optional).
	Tracker *ratelimit.RateLimitTracker

	// CheckInterval is the interval between pane checks (default 5s).
	CheckInterval time.Duration

	// CaptureLines is the number of lines to capture from panes (default 50).
	CaptureLines int

	// Logger for structured logging.
	Logger *slog.Logger

	// eventChan emits detected limit events.
	eventChan chan LimitEvent

	// mu protects internal state.
	mu sync.RWMutex

	// monitoredPanes tracks which panes are being monitored.
	monitoredPanes map[string]context.CancelFunc

	// cancel stops all monitoring goroutines.
	cancel context.CancelFunc

	// ctx is the context for all monitoring goroutines.
	ctx context.Context
}

type paneCapturer interface {
	CapturePaneOutput(target string, lines int) (string, error)
}

// NewLimitDetector creates a new LimitDetector with default settings.
func NewLimitDetector() *LimitDetector {
	return &LimitDetector{
		TmuxClient:     nil,
		Tracker:        nil,
		CheckInterval:  5 * time.Second,
		CaptureLines:   50,
		Logger:         slog.Default(),
		eventChan:      make(chan LimitEvent, 100),
		monitoredPanes: make(map[string]context.CancelFunc),
	}
}

// NewLimitDetectorWithTracker creates a LimitDetector with a rate limit tracker.
func NewLimitDetectorWithTracker(tracker *ratelimit.RateLimitTracker) *LimitDetector {
	ld := NewLimitDetector()
	ld.Tracker = tracker
	return ld
}

// NewLimitDetectorWithClient creates a LimitDetector with a custom tmux client.
func NewLimitDetectorWithClient(client *tmux.Client) *LimitDetector {
	ld := NewLimitDetector()
	ld.TmuxClient = client
	return ld
}

// tmuxClient returns the configured tmux client or the default client.
func (d *LimitDetector) tmuxClient() paneCapturer {
	if d.TmuxClient != nil {
		return d.TmuxClient
	}
	return tmux.DefaultClient
}

// logger returns the configured logger or the default logger.
func (d *LimitDetector) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// Events returns the channel that emits limit events.
func (d *LimitDetector) Events() <-chan LimitEvent {
	return d.eventChan
}

// Start begins monitoring all panes in the swarm.
func (d *LimitDetector) Start(ctx context.Context, plan *SwarmPlan) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if plan == nil {
		return nil
	}

	d.mu.Lock()
	d.ctx, d.cancel = context.WithCancel(ctx)
	d.mu.Unlock()

	d.logger().Info("[LimitDetector] Starting swarm monitoring",
		"sessions", len(plan.Sessions),
		"check_interval", d.CheckInterval)

	var runner tmuxContextRunner = tmux.DefaultClient
	if d.TmuxClient != nil {
		if typed, ok := d.TmuxClient.(tmuxContextRunner); ok {
			runner = typed
		}
	}

	targetingCache := make(map[string]swarmSessionTargeting, len(plan.Sessions))

	for _, sess := range plan.Sessions {
		targeting, targetingOK := targetingCache[sess.Name]
		if !targetingOK && d.ctx.Err() == nil {
			resolved, err := resolveSwarmSessionTargeting(d.ctx, runner, sess.Name)
			if err != nil {
				d.logger().Warn("[LimitDetector] session_targeting_resolve_failed",
					"session", sess.Name,
					"error", err)
			} else {
				targeting = resolved
				targetingCache[sess.Name] = resolved
				targetingOK = true
			}
		}

		for _, pane := range sess.Panes {
			target := formatPaneTarget(sess.Name, pane.Index)
			if targetingOK {
				if resolved, err := swarmPaneTargetFromPlanIndex(sess.Name, targeting, pane.Index); err == nil {
					target = resolved
				} else {
					d.logger().Warn("[LimitDetector] pane_target_resolve_failed",
						"session", sess.Name,
						"pane_index", pane.Index,
						"error", err)
				}
			}
			if err := d.StartPane(d.ctx, target, pane.AgentType); err != nil {
				d.logger().Warn("[LimitDetector] Failed to start pane monitoring",
					"session_pane", target,
					"error", err)
				// Continue with other panes
			}
		}
	}

	return nil
}

// StartPane begins monitoring a single pane.
func (d *LimitDetector) StartPane(ctx context.Context, sessionPane string, agentType string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check if already monitoring
	if _, exists := d.monitoredPanes[sessionPane]; exists {
		return nil // Already monitoring
	}

	// Create cancellable context for this pane
	paneCtx, paneCancel := context.WithCancel(ctx)
	d.monitoredPanes[sessionPane] = paneCancel

	d.logger().Info("[LimitDetector] monitoring_start",
		"session_pane", sessionPane,
		"agent_type", agentType)

	// Start monitoring goroutine
	go d.monitorPane(paneCtx, sessionPane, agentType)

	return nil
}

// StopPane stops monitoring a single pane.
func (d *LimitDetector) StopPane(sessionPane string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if cancel, exists := d.monitoredPanes[sessionPane]; exists {
		cancel()
		delete(d.monitoredPanes, sessionPane)
		d.logger().Info("[LimitDetector] monitoring_stop",
			"session_pane", sessionPane)
	}
}

// Stop halts all monitoring.
func (d *LimitDetector) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}

	// Clear all monitored panes
	for sessionPane, cancel := range d.monitoredPanes {
		cancel()
		d.logger().Debug("[LimitDetector] stopped monitoring",
			"session_pane", sessionPane)
	}
	d.monitoredPanes = make(map[string]context.CancelFunc)
}

// CheckPane captures pane output and checks for limit patterns (synchronous).
func (d *LimitDetector) CheckPane(sessionPane string, agentType string) (*LimitEvent, error) {
	client := d.tmuxClient()

	// Capture pane output
	output, err := client.CapturePaneOutput(sessionPane, d.CaptureLines)
	if err != nil {
		return nil, err
	}

	// Check for limit patterns
	event := d.checkOutput(sessionPane, agentType, output)
	return event, nil
}

// monitorPane runs the monitoring loop for a single pane.
func (d *LimitDetector) monitorPane(ctx context.Context, sessionPane string, agentType string) {
	ticker := time.NewTicker(d.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			event, err := d.CheckPane(sessionPane, agentType)
			if err != nil {
				d.logger().Debug("[LimitDetector] check_failed",
					"session_pane", sessionPane,
					"error", err)
				continue
			}

			if event != nil {
				d.handleLimitEvent(event)
			}
		}
	}
}

// checkOutput checks captured output for limit patterns.
func (d *LimitDetector) checkOutput(sessionPane string, agentType string, output string) *LimitEvent {
	if output == "" {
		return nil
	}

	// Get patterns for this agent type
	patterns := d.getPatternsForAgent(agentType)
	if len(patterns) == 0 {
		return nil
	}

	// Check each pattern
	outputLower := strings.ToLower(output)
	for _, pattern := range patterns {
		if strings.Contains(outputLower, strings.ToLower(pattern)) {
			d.logger().Info("[LimitDetector] limit_detected",
				"session_pane", sessionPane,
				"pattern", pattern)

			return &LimitEvent{
				SessionPane: sessionPane,
				AgentType:   agentType,
				Pattern:     pattern,
				RawOutput:   output,
				DetectedAt:  time.Now(),
			}
		}
	}

	return nil
}

// getPatternsForAgent returns rate limit patterns for the given agent type.
func (d *LimitDetector) getPatternsForAgent(agentType string) []string {
	// Map agent type aliases to canonical types
	var at agent.AgentType
	switch agentType {
	case "cc", "claude", "claude-code":
		at = agent.AgentTypeClaudeCode
	case "cod", "codex", "openai":
		at = agent.AgentTypeCodex
	case "gmi", "gemini", "google":
		at = agent.AgentTypeGemini
	default:
		// Return default patterns for unknown agent types
		return defaultLimitPatterns
	}

	patternSet := agent.GetPatternSet(at)
	if patternSet == nil || len(patternSet.RateLimitPatterns) == 0 {
		return defaultLimitPatterns
	}
	return patternSet.RateLimitPatterns
}

// handleLimitEvent processes a detected limit event.
func (d *LimitDetector) handleLimitEvent(event *LimitEvent) {
	// Record in tracker if available
	if d.Tracker != nil {
		provider := ratelimit.NormalizeProvider(event.AgentType)
		waitSeconds := ratelimit.ParseWaitSeconds(event.RawOutput)
		cooldown := d.Tracker.RecordRateLimitWithCooldown(provider, "swarm", waitSeconds)
		if err := d.Tracker.SaveToDir(""); err != nil {
			d.logger().Warn("[LimitDetector] tracker_persist_failed",
				"provider", provider,
				"error", err)
		} else {
			d.logger().Info("[LimitDetector] tracker_updated",
				"provider", provider,
				"cooldown", cooldown.String(),
				"wait_seconds", waitSeconds)
		}
	}

	// Send event to channel (non-blocking)
	select {
	case d.eventChan <- *event:
		// Event sent successfully
	default:
		d.logger().Warn("[LimitDetector] event_channel_full",
			"session_pane", event.SessionPane,
			"pattern", event.Pattern)
	}
}

// IsMonitoring returns true if the given pane is being monitored.
func (d *LimitDetector) IsMonitoring(sessionPane string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, exists := d.monitoredPanes[sessionPane]
	return exists
}

// MonitoredPanes returns a list of currently monitored panes.
func (d *LimitDetector) MonitoredPanes() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	panes := make([]string, 0, len(d.monitoredPanes))
	for pane := range d.monitoredPanes {
		panes = append(panes, pane)
	}
	return panes
}

// defaultLimitPatterns are used when agent-specific patterns aren't available.
var defaultLimitPatterns = []string{
	"rate limit",
	"usage limit",
	"quota exceeded",
	"too many requests",
	"please wait",
	"try again later",
	"exceeded.*limit",
}
