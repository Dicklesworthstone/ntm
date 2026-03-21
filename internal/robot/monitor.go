// Package robot provides machine-readable output for AI agents.
// monitor.go implements the proactive monitoring loop for usage limit detection.
package robot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/caut"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Proactive Monitor (bd-3gh5m)
// =============================================================================
//
// The Monitor runs a continuous loop that:
// 1. Captures pane output and parses agent state
// 2. Tracks context usage trends over time
// 3. Emits warnings when thresholds are crossed
// 4. Optionally queries caut for provider-level usage data
//
// Output is JSONL for easy consumption by controllers.

// Monitor runs continuous monitoring for a session.
type Monitor struct {
	config     MonitorConfig
	trends     *TrendTracker
	cautClient *caut.CachedClient
	output     io.Writer
	outputFile *os.File // If writing to file, we need to close it
	parser     agent.Parser
	lastCaut   time.Time
	cautCache  map[string]*caut.ProviderPayload
}

// NewMonitor creates a monitor with the given config.
func NewMonitor(config MonitorConfig) (*Monitor, error) {
	var output io.Writer = os.Stdout
	var outputFile *os.File

	if config.OutputFile != "" {
		f, err := os.OpenFile(config.OutputFile,
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("open output file: %w", err)
		}
		output = f
		outputFile = f
	}

	m := &Monitor{
		config:     config,
		trends:     NewTrendTracker(10), // Keep 10 samples per pane
		output:     output,
		outputFile: outputFile,
		parser:     agent.NewParser(),
		cautCache:  make(map[string]*caut.ProviderPayload),
	}

	// Set up caut client if requested
	if config.IncludeCaut {
		client := caut.NewClient(caut.WithTimeout(30 * time.Second))
		if client.IsInstalled() {
			m.cautClient = caut.NewCachedClient(client, config.CautInterval)
		}
	}

	return m, nil
}

// Close releases resources held by the monitor.
func (m *Monitor) Close() error {
	if m.outputFile != nil {
		return m.outputFile.Close()
	}
	return nil
}

// Run starts the monitoring loop. Blocks until context is cancelled.
func (m *Monitor) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()

	// Initial check
	if err := m.checkOnce(ctx); err != nil {
		m.emitError("Initial check failed", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := m.checkOnce(ctx); err != nil {
				m.emitError("Check failed", err)
			}
		}
	}
}

// RunOnce performs a single check iteration (useful for testing).
func (m *Monitor) RunOnce(ctx context.Context) error {
	return m.checkOnce(ctx)
}

func (m *Monitor) checkOnce(ctx context.Context) error {
	// Determine panes to check
	panes, err := m.getPanesToCheck()
	if err != nil {
		return fmt.Errorf("get panes: %w", err)
	}

	// Maybe refresh caut data
	if m.cautClient != nil && time.Since(m.lastCaut) > m.config.CautInterval {
		if err := m.refreshCautData(ctx); err != nil {
			m.emitError("caut refresh failed", err)
		}
		m.lastCaut = time.Now()
	}

	// Check each pane
	for _, pane := range panes {
		if err := m.checkPane(ctx, pane); err != nil {
			m.emitError(fmt.Sprintf("check pane %d failed", pane.Index), err)
		}
	}

	return nil
}

func (m *Monitor) getPanesToCheck() ([]tmux.Pane, error) {
	if len(m.config.Panes) == 0 {
		return m.getAgentPanes()
	}

	allPanes, err := tmux.GetPanes(m.config.Session)
	if err != nil {
		return nil, err
	}

	indexSet := make(map[int]struct{}, len(m.config.Panes))
	for _, idx := range m.config.Panes {
		indexSet[idx] = struct{}{}
	}

	var selected []tmux.Pane
	for _, p := range allPanes {
		if _, ok := indexSet[p.Index]; ok {
			selected = append(selected, p)
		}
	}
	return selected, nil
}

func (m *Monitor) getAgentPanes() ([]tmux.Pane, error) {
	allPanes, err := tmux.GetPanes(m.config.Session)
	if err != nil {
		return nil, err
	}

	var agentPanes []tmux.Pane
	controlPane, ok := controlPaneIndex(allPanes)
	if !ok {
		return agentPanes, nil
	}
	for _, p := range allPanes {
		if p.Index != controlPane { // Skip control pane (first pane)
			agentPanes = append(agentPanes, p)
		}
	}
	return agentPanes, nil
}

func controlPaneIndex(panes []tmux.Pane) (int, bool) {
	minIdx := -1
	for _, p := range panes {
		if minIdx == -1 || p.Index < minIdx {
			minIdx = p.Index
		}
	}
	if minIdx == -1 {
		return 0, false
	}
	return minIdx, true
}

func defaultMonitorPaneIndices(panes []tmux.Pane) []int {
	controlPane, ok := controlPaneIndex(panes)
	if !ok {
		return nil
	}

	indices := make([]int, 0, len(panes)-1)
	for _, p := range panes {
		if p.Index != controlPane {
			indices = append(indices, p.Index)
		}
	}
	return indices
}

func (m *Monitor) checkPane(ctx context.Context, pane tmux.Pane) error {
	// Build target
	target := pane.ID
	if target == "" {
		firstWin, err := tmux.GetFirstWindow(m.config.Session)
		if err != nil {
			return err
		}
		target = fmt.Sprintf("%s:%d.%d", m.config.Session, firstWin, pane.Index)
	}

	// Capture output
	output, err := tmux.CapturePaneOutputContext(ctx, target, m.config.LinesCaptured)
	if err != nil {
		return err
	}

	// Parse state
	state, err := m.parser.ParseWithHint(output, pane.Type)
	if err != nil {
		return err
	}

	// Record trend sample
	m.trends.AddSample(pane.Index, TrendSample{
		Timestamp:        time.Now(),
		ContextRemaining: state.ContextRemaining,
	})

	// Get trend
	trend, samples := m.trends.GetTrend(pane.Index)

	// Generate warnings based on context thresholds
	if state.ContextRemaining != nil {
		ctxPct := *state.ContextRemaining

		level := getWarningLevel(ctxPct, m.config)
		if level != "" {
			threshold := m.getThresholdForLevel(level)
			w := NewWarning(level, m.config.Session, pane.Index, string(state.Type),
				getWarningMessage(level, threshold),
				getSuggestedAction(level))
			w = w.WithContext(state.ContextRemaining, string(trend), samples)
			m.emitWarning(w)
		}
	}

	// Check for rate limit
	if state.IsRateLimited {
		w := NewWarning(LevelAlert, m.config.Session, pane.Index, string(state.Type),
			"Agent hit rate limit",
			"Wait for reset or switch account with caam")
		m.emitWarning(w)
	}

	// Check provider usage if caut is enabled
	if m.cautClient != nil {
		provider := caut.AgentTypeToProvider(string(state.Type))
		if payload, ok := m.cautCache[provider]; ok {
			if pct := payload.UsedPercent(); pct != nil && *pct >= m.config.AlertThreshold {
				w := NewWarning(LevelAlert, m.config.Session, pane.Index, string(state.Type),
					formatProviderUsageMessage(m.config.AlertThreshold),
					"Consider caam account switch")
				w = w.WithProvider(provider, pct)
				m.emitWarning(w)
			}
		}
	}

	return nil
}

func (m *Monitor) getThresholdForLevel(level WarningLevel) float64 {
	switch level {
	case LevelCritical:
		return m.config.CritThreshold
	case LevelWarning:
		return m.config.WarnThreshold
	case LevelInfo:
		return m.config.InfoThreshold
	default:
		return 0
	}
}

func (m *Monitor) refreshCautData(ctx context.Context) error {
	if m.cautClient == nil {
		return nil
	}

	for _, provider := range caut.SupportedProviders() {
		payload, err := m.cautClient.GetProviderUsage(ctx, provider)
		if err != nil {
			continue // Best effort
		}
		m.cautCache[provider] = payload
	}

	return nil
}

func (m *Monitor) emitWarning(w Warning) {
	data, _ := json.Marshal(w)
	fmt.Fprintln(m.output, string(data))
}

func (m *Monitor) emitError(msg string, err error) {
	w := Warning{
		Level:     LevelAlert,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Session:   m.config.Session,
		Message:   fmt.Sprintf("%s: %v", msg, err),
	}
	m.emitWarning(w)
}

// formatProviderUsageMessage formats the provider usage alert message.
func formatProviderUsageMessage(threshold float64) string {
	return fmt.Sprintf("Provider usage above %.0f%%", threshold)
}

// =============================================================================
// CLI Entry Point
// =============================================================================

// MonitorOutput is the initial response when starting the monitor.
type MonitorOutput struct {
	RobotResponse
	Session     string        `json:"session"`
	Panes       []int         `json:"panes"`
	Interval    string        `json:"interval"`
	Thresholds  MonitorThresh `json:"thresholds"`
	CautEnabled bool          `json:"caut_enabled"`
	Message     string        `json:"message"`
}

// MonitorThresh contains the configured thresholds.
type MonitorThresh struct {
	Info     float64 `json:"info"`
	Warning  float64 `json:"warning"`
	Critical float64 `json:"critical"`
	Alert    float64 `json:"alert"`
}

// PrintMonitor starts the monitoring loop and outputs JSONL.
func PrintMonitor(config MonitorConfig) error {
	// Validate session exists
	if !tmux.SessionExists(config.Session) {
		output := MonitorOutput{
			RobotResponse: RobotResponse{
				Success:   false,
				Error:     fmt.Sprintf("session '%s' not found", config.Session),
				ErrorCode: ErrCodeSessionNotFound,
			},
			Session: config.Session,
		}
		return encodeJSON(output)
	}

	// Determine panes
	panes := config.Panes
	if len(panes) == 0 {
		allPanes, err := tmux.GetPanes(config.Session)
		if err != nil {
			output := MonitorOutput{
				RobotResponse: RobotResponse{
					Success:   false,
					Error:     fmt.Sprintf("failed to get panes: %v", err),
					ErrorCode: ErrCodeInternalError,
				},
				Session: config.Session,
			}
			return encodeJSON(output)
		}
		panes = defaultMonitorPaneIndices(allPanes)
	}

	// Create monitor
	monitor, err := NewMonitor(config)
	if err != nil {
		output := MonitorOutput{
			RobotResponse: RobotResponse{
				Success:   false,
				Error:     fmt.Sprintf("failed to create monitor: %v", err),
				ErrorCode: ErrCodeInternalError,
			},
			Session: config.Session,
		}
		return encodeJSON(output)
	}
	defer monitor.Close()

	// Emit initial status
	initOutput := MonitorOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       config.Session,
		Panes:         panes,
		Interval:      config.Interval.String(),
		Thresholds: MonitorThresh{
			Info:     config.InfoThreshold,
			Warning:  config.WarnThreshold,
			Critical: config.CritThreshold,
			Alert:    config.AlertThreshold,
		},
		CautEnabled: monitor.cautClient != nil,
		Message:     "Monitor started, emitting JSONL warnings...",
	}
	if err := encodeJSON(initOutput); err != nil {
		return err
	}

	// Run until interrupted
	ctx := context.Background()
	return monitor.Run(ctx)
}

// ParseIntervalArg parses a duration string like "30s", "1m", "5m".
func ParseIntervalArg(s string) (time.Duration, error) {
	if s == "" {
		return 30 * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid interval '%s': %w", s, err)
	}
	if d < time.Second {
		return 0, fmt.Errorf("interval must be at least 1s, got %v", d)
	}
	return d, nil
}

// ParseThresholdArg parses a threshold percentage.
func ParseThresholdArg(s string, defaultVal float64) (float64, error) {
	if s == "" {
		return defaultVal, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid threshold '%s': %w", s, err)
	}
	if v < 0 || v > 100 {
		return 0, fmt.Errorf("threshold must be 0-100, got %.1f", v)
	}
	return v, nil
}
