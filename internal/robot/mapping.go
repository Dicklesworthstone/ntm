package robot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/spawnidentity"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	mappingSchemaID      = "ntm:robot:mapping:v1"
	mappingSchemaVersion = "1.0.0"

	MappingSchemaID      = mappingSchemaID
	MappingSchemaVersion = mappingSchemaVersion
)

// AgentMappingEntry is one Agent Mail <-> tmux pane mapping row.
type AgentMappingEntry struct {
	Name      string                     `json:"name"`
	Session   string                     `json:"session"`
	PaneIndex int                        `json:"pane_index"`
	PaneID    string                     `json:"pane_id"`
	PaneTitle string                     `json:"pane_title,omitempty"`
	AgentType string                     `json:"agent_type,omitempty"`
	Identity  *agentmail.PaneBadgeRecord `json:"identity,omitempty"`
}

// AgentMappingBadges summarizes tmux pane-border badge reconciliation for a session.
type AgentMappingBadges struct {
	Enabled         bool      `json:"enabled"`
	Published       int       `json:"published"`
	Cleared         int       `json:"cleared"`
	WindowsPrepared int       `json:"windows_prepared"`
	WindowsRestored int       `json:"windows_restored"`
	Warnings        []string  `json:"warnings,omitempty"`
	TmuxError       string    `json:"tmux_error,omitempty"`
	AttemptedAt     time.Time `json:"attempted_at"`
}

// AgentMappingOptions configures GetAgentMapping.
type AgentMappingOptions struct {
	Session        string
	ProjectDir     string
	Config         *config.Config
	BadgeTmux      spawnidentity.BadgeTmux
	ReconcileFn    func(context.Context, string) (*spawnidentity.BadgeReport, error)
	RequireSession bool
}

// AgentMappingOutput is the canonical robot envelope for --robot-mapping and GET /api/v1/sessions/{id}/mapping.
type AgentMappingOutput struct {
	RobotResponse
	SchemaID      string                      `json:"schema_id"`
	SchemaVersion string                      `json:"schema_version"`
	Session       string                      `json:"session"`
	Count         int                         `json:"count"`
	Entries       []AgentMappingEntry         `json:"entries"`
	Discrepancies []agentmail.PaneBadgeRecord `json:"discrepancies"`
	Badges        *AgentMappingBadges         `json:"badges,omitempty"`
	ReconcileErr  string                      `json:"reconcile_error,omitempty"`
	AgentHints    *AgentHints                 `json:"_agent_hints,omitempty"`
}

// LoadMappingForSession resolves persisted Agent Mail registry bindings against live tmux panes.
func LoadMappingForSession(sessionName, projectDir string) ([]AgentMappingEntry, error) {
	trimmed := strings.TrimSpace(sessionName)
	if strings.Compare(trimmed, "") == 0 {
		return nil, fmt.Errorf("session name is required")
	}
	registry, err := agentmail.LoadBestSessionAgentRegistry(trimmed, strings.TrimSpace(projectDir))
	if err != nil {
		return nil, fmt.Errorf("loading agent registry: %w", err)
	}
	if registry == nil {
		return nil, nil
	}

	panes, err := tmux.GetPanes(trimmed)
	if err != nil {
		return nil, fmt.Errorf("listing tmux panes: %w", err)
	}

	byID := make(map[string]tmux.Pane, len(panes))
	byTitle := make(map[string]tmux.Pane, len(panes))
	for _, p := range panes {
		byID[p.ID] = p
		if strings.Compare(p.Title, "") != 0 {
			byTitle[p.Title] = p
		}
	}

	seen := make(map[string]bool)
	entries := make([]AgentMappingEntry, 0, len(registry.PaneIDMap)+len(registry.Agents))

	for paneID, name := range registry.PaneIDMap {
		if seen[name] {
			continue
		}
		if p, ok := byID[paneID]; ok {
			seen[name] = true
			entries = append(entries, AgentMappingEntry{
				Name:      name,
				Session:   trimmed,
				PaneIndex: p.Index,
				PaneID:    p.ID,
				PaneTitle: p.Title,
				AgentType: string(p.Type),
			})
		}
	}
	for paneTitle, name := range registry.Agents {
		if seen[name] {
			continue
		}
		if p, ok := byTitle[paneTitle]; ok {
			seen[name] = true
			entries = append(entries, AgentMappingEntry{
				Name:      name,
				Session:   trimmed,
				PaneIndex: p.Index,
				PaneID:    p.ID,
				PaneTitle: p.Title,
				AgentType: string(p.Type),
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// GetAgentMapping resolves Agent Mail <-> tmux pane bindings and runs identity/badge reconciliation.
func GetAgentMapping(ctx context.Context, opts AgentMappingOptions) (*AgentMappingOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sessName := strings.TrimSpace(opts.Session)
	if strings.Compare(sessName, "") == 0 {
		return &AgentMappingOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session name is required"),
				ErrCodeInvalidFlag,
				"Pass --robot-mapping=<session>",
			),
			SchemaID:      mappingSchemaID,
			SchemaVersion: mappingSchemaVersion,
			Entries:       []AgentMappingEntry{},
			Discrepancies: []agentmail.PaneBadgeRecord{},
		}, nil
	}

	projectDir := strings.TrimSpace(opts.ProjectDir)
	if opts.RequireSession {
		registry, regErr := agentmail.LoadBestSessionAgentRegistry(sessName, projectDir)
		if regErr == nil && registry == nil && !tmux.SessionExists(sessName) {
			return &AgentMappingOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("session %q not found", sessName),
					ErrCodeSessionNotFound,
					"Run ntm --robot-status to inspect active sessions",
				),
				SchemaID:      mappingSchemaID,
				SchemaVersion: mappingSchemaVersion,
				Session:       sessName,
				Entries:       []AgentMappingEntry{},
				Discrepancies: []agentmail.PaneBadgeRecord{},
			}, nil
		}
	}

	entries, err := LoadMappingForSession(sessName, projectDir)
	if err != nil {
		code := ErrCodeInternalError
		if !tmux.SessionExists(sessName) {
			code = ErrCodeSessionNotFound
		}
		return &AgentMappingOutput{
			RobotResponse: NewErrorResponse(err, code, "Ensure the tmux session exists and Agent Mail registry is readable"),
			SchemaID:      mappingSchemaID,
			SchemaVersion: mappingSchemaVersion,
			Session:       sessName,
			Entries:       []AgentMappingEntry{},
			Discrepancies: []agentmail.PaneBadgeRecord{},
		}, nil
	}
	if entries == nil {
		entries = []AgentMappingEntry{}
	}

	out := &AgentMappingOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      mappingSchemaID,
		SchemaVersion: mappingSchemaVersion,
		Session:       sessName,
		Count:         len(entries),
		Entries:       entries,
		Discrepancies: []agentmail.PaneBadgeRecord{},
	}

	var (
		report       *spawnidentity.BadgeReport
		reconcileErr error
	)
	if opts.ReconcileFn != nil {
		report, reconcileErr = opts.ReconcileFn(ctx, sessName)
	} else {
		report, reconcileErr = spawnidentity.ReconcileSessionBadges(ctx, spawnidentity.BadgeReconcileOptions{
			Session:  sessName,
			Enabled:  spawnidentity.BadgesEnabled(opts.Config),
			Template: spawnidentity.BadgeTemplate(opts.Config),
			Tmux:     opts.BadgeTmux,
		})
	}
	if reconcileErr != nil {
		out.ReconcileErr = reconcileErr.Error()
	}
	if report != nil {
		byPane := make(map[string]agentmail.PaneBadgeRecord, len(report.Records))
		for _, rec := range report.Records {
			byPane[rec.PaneID] = rec
		}
		for i := range out.Entries {
			if rec, ok := byPane[out.Entries[i].PaneID]; ok {
				copied := rec
				out.Entries[i].Identity = &copied
			}
		}
		if report.Discrepancies != nil {
			out.Discrepancies = report.Discrepancies
		}
		out.Badges = &AgentMappingBadges{
			Enabled:         report.Enabled,
			Published:       report.Published,
			Cleared:         report.Cleared,
			WindowsPrepared: report.WindowsPrepared,
			WindowsRestored: report.WindowsRestored,
			Warnings:        report.Warnings,
			TmuxError:       report.TmuxError,
			AttemptedAt:     report.AttemptedAt,
		}
		if reconcileErr != nil && strings.Compare(out.Badges.TmuxError, "") == 0 {
			out.Badges.TmuxError = reconcileErr.Error()
		}
	}

	out.AgentHints = &AgentHints{
		Summary: fmt.Sprintf("Resolved %d mapped agent pane(s) and %d identity discrepancy record(s) in session %q.", out.Count, len(out.Discrepancies), sessName),
	}
	return out, nil
}

// PrintAgentMapping writes the terminal JSON/TOON response for --robot-mapping.
func PrintAgentMapping(ctx context.Context, opts AgentMappingOptions) error {
	out, err := GetAgentMapping(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot mapping failed")
}

func mappingCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "mapping",
		Flag:        "--robot-mapping",
		Category:    "state",
		Summary:     "Resolve Agent Mail pane mappings and badges.",
		Description: "Resolve Agent Mail agent names to live tmux panes, reconcile canonical identity files, publish/withdraw border badges, and report drift.",
		Parameters: []RobotParameter{
			{Name: "session", Flag: "--robot-mapping", Type: "string", Required: true, Description: "Target tmux session name"},
			{Name: "project", Flag: "--project", Type: "string", Required: false, Description: "Project directory override for Agent Mail registry lookup"},
		},
		Examples: []string{
			"ntm --robot-mapping=myproject",
		},
	}
}
