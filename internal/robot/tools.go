// Package robot provides machine-readable output for AI agents.
// tools.go provides the --robot-tools command for tool inventory and health.
package robot

import (
	"context"
	"os"
	"sort"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// ToolsOutput represents the output for --robot-tools
type ToolsOutput struct {
	RobotResponse
	Tools        []ToolInfoOutput    `json:"tools"`
	HealthReport *tools.HealthReport `json:"health_report"`
}

// ToolInfoOutput represents a single tool's info in robot output
type ToolInfoOutput struct {
	Name         string            `json:"name"`
	Installed    bool              `json:"installed"`
	Version      string            `json:"version,omitempty"`
	Path         string            `json:"path,omitempty"`
	Capabilities []string          `json:"capabilities"`
	Health       *ToolHealthOutput `json:"health"`
	Required     bool              `json:"required,omitempty"`
	// Disabled reports that configuration turned this integration off, so it
	// was never probed — distinct from installed=false, which means the
	// binary is genuinely absent from PATH (ntm#313).
	Disabled bool `json:"disabled,omitempty"`
}

// ToolHealthOutput represents tool health in robot output
type ToolHealthOutput struct {
	Healthy     bool   `json:"healthy"`
	Message     string `json:"message,omitempty"`
	Error       string `json:"error,omitempty"`
	LatencyMs   int64  `json:"latency_ms,omitempty"`
	LastChecked string `json:"last_checked"`
}

// RequiredTools lists tools that are required for NTM operation
var RequiredTools = map[tools.ToolName]bool{
	tools.ToolBV: true, // bv is required for triage
}

// DisabledTools maps the config toggles that gate an ecosystem integration
// onto the registry adapters they gate, so a tool the operator turned off is
// reported as disabled instead of being probed.
//
// Probing is not free: every adapter's Info runs the tool's `--version` and
// its health command, so an inventory pass on a machine with `[cass] enabled
// = false` still spent seconds inside `cass health --json` (ntm#313). Each
// toggle below is documented in config as a top-level switch for its
// integration, and every one defaults to true, so this only changes behaviour
// for an operator who explicitly turned something off.
//
// A nil cfg disables nothing: without configuration we cannot claim an
// integration is off, and probing is the safe default.
func DisabledTools(cfg *config.Config) map[tools.ToolName]bool {
	if cfg == nil {
		return nil
	}

	disabled := make(map[tools.ToolName]bool, 7)
	for name, enabled := range map[tools.ToolName]bool{
		tools.ToolCASS: cfg.CASS.Enabled,
		tools.ToolCM:   cfg.Memory.Enabled,
		tools.ToolAM:   cfg.AgentMail.Enabled,
		tools.ToolRCH:  cfg.Integrations.RCH.Enabled,
		tools.ToolPT:   cfg.Integrations.ProcessTriage.Enabled,
		tools.ToolRano: cfg.Integrations.Rano.Enabled,
		tools.ToolXF:   cfg.Integrations.XF.Enabled,
	} {
		if !enabled {
			disabled[name] = true
		}
	}

	if len(disabled) == 0 {
		return nil
	}
	return disabled
}

// collectToolInfo probes the registry, skipping every tool named in disabled,
// and renders the result as sorted robot output. It is the one conversion
// from tools.ToolInfo to ToolInfoOutput, shared by the full inventory and the
// snapshot summary.
func collectToolInfo(ctx context.Context, disabled map[tools.ToolName]bool) []ToolInfoOutput {
	allInfo := tools.GetAllInfoExcept(ctx, disabled)

	toolOutputs := make([]ToolInfoOutput, 0, len(allInfo))
	for _, info := range allInfo {
		if info == nil {
			continue
		}

		// Convert capabilities to strings
		caps := make([]string, len(info.Capabilities))
		for i, c := range info.Capabilities {
			caps[i] = string(c)
		}

		// Convert health
		healthOutput := &ToolHealthOutput{
			Healthy:     info.Health.Healthy,
			Message:     info.Health.Message,
			Error:       info.Health.Error,
			LatencyMs:   info.Health.Latency.Milliseconds(),
			LastChecked: FormatTimestamp(info.Health.LastChecked),
		}

		toolOutputs = append(toolOutputs, ToolInfoOutput{
			Name:         string(info.Name),
			Installed:    info.Installed,
			Version:      info.Version.String(),
			Path:         info.Path,
			Capabilities: caps,
			Health:       healthOutput,
			Required:     RequiredTools[info.Name],
			Disabled:     info.Disabled,
		})
	}

	// Sort by name for stable output
	sort.Slice(toolOutputs, func(i, j int) bool {
		return toolOutputs[i].Name < toolOutputs[j].Name
	})

	return toolOutputs
}

// GetTools collects tool inventory and health, skipping the probes for every
// tool named in disabled.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetTools(ctx context.Context, disabled map[tools.ToolName]bool) (*ToolsOutput, error) {
	return &ToolsOutput{
		RobotResponse: NewRobotResponse(true),
		Tools:         collectToolInfo(ctx, disabled),
		HealthReport:  tools.GetHealthReportExcept(ctx, disabled),
	}, nil
}

// PrintTools outputs tool inventory and health as JSON.
// This is a thin wrapper around GetTools() for CLI output.
func PrintTools() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The inventory answers "what does my configuration actually use", so it
	// honours the same toggles as the snapshot: a disabled tool is reported,
	// not executed (ntm#313).
	var disabled map[tools.ToolName]bool
	if wd, err := os.Getwd(); err == nil {
		if cfg, err := config.LoadMerged(wd, config.DefaultPath()); err == nil {
			disabled = DisabledTools(cfg)
		}
	}

	output, err := GetTools(ctx, disabled)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot tools failed")
}

// GetToolsSummary returns a lightweight tools summary for inclusion in
// snapshots, skipping the probes for every tool named in disabled.
func GetToolsSummary(ctx context.Context, disabled map[tools.ToolName]bool) []ToolInfoOutput {
	return collectToolInfo(ctx, disabled)
}
