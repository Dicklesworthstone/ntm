package robot

import (
	"fmt"
	"os"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	savedSessionsSchemaID      = "ntm:robot:saved-sessions:v1"
	savedSessionsSchemaVersion = "1.0.0"

	SavedSessionsSchemaID      = savedSessionsSchemaID
	SavedSessionsSchemaVersion = savedSessionsSchemaVersion
)

// SavedSessionsOptions configures GetSavedSessions.
type SavedSessionsOptions struct {
	Action    string // "list" (default), "show", "save", "archive", "unarchive"
	Name      string // Saved session name (or custom save name when Action == "save")
	Session   string // Live tmux session name when Action == "save"
	Archived  bool   // List archived saved sessions when Action == "list"
	Overwrite bool   // Overwrite existing snapshot when Action == "save"
}

// SavedSessionsOutput is the canonical robot envelope for --robot-saved-sessions and /api/v1/saved-sessions*.
type SavedSessionsOutput struct {
	RobotResponse
	SchemaID       string                 `json:"schema_id"`
	SchemaVersion  string                 `json:"schema_version"`
	Action         string                 `json:"action"`
	Archived       bool                   `json:"archived,omitempty"`
	Sessions       []session.SavedSession `json:"sessions,omitempty"`
	Count          *int                   `json:"count,omitempty"`
	Session        string                 `json:"session,omitempty"`
	SavedAs        string                 `json:"saved_as,omitempty"`
	FilePath       string                 `json:"file_path,omitempty"`
	State          *session.SessionState  `json:"state,omitempty"`
	ResumablePanes *int                   `json:"resumable_panes,omitempty"`
	AgentHints     *AgentHints            `json:"_agent_hints,omitempty"`
}

// CountResumablePanes counts panes in state with a fresh, high-confidence casr/native session binding.
func CountResumablePanes(state *session.SessionState) int {
	if state == nil {
		return 0
	}
	resumable := 0
	for _, pane := range state.Panes {
		if pane.HasFreshSessionBinding() {
			resumable++
		}
	}
	return resumable
}

// FormatAgentConfigSummary formats all 12 agent harnesses plus user panes in human-readable lines.
func FormatAgentConfigSummary(cfg session.AgentConfig) []string {
	lines := []string{
		fmt.Sprintf("Claude: %d", cfg.Claude),
		fmt.Sprintf("Codex:  %d", cfg.Codex),
		fmt.Sprintf("Gemini: %d", cfg.Gemini),
	}
	optional := []struct {
		label string
		count int
	}{
		{"Antigravity", cfg.Antigravity},
		{"Grok", cfg.Grok},
		{"Cursor", cfg.Cursor},
		{"Windsurf", cfg.Windsurf},
		{"Aider", cfg.Aider},
		{"OpenCode", cfg.Opencode},
		{"Oh My Pi", cfg.Omp},
		{"Pi", cfg.Pi},
		{"Ollama", cfg.Ollama},
		{"User", cfg.User},
	}
	for _, item := range optional {
		if item.count > 0 {
			lines = append(lines, fmt.Sprintf("%s: %d", item.label, item.count))
		}
	}
	return lines
}

// GetSavedSessions executes non-destructive saved-session inspection and snapshot management.
func GetSavedSessions(opts SavedSessionsOptions) (*SavedSessionsOutput, error) {
	action := strings.ToLower(strings.TrimSpace(opts.Action))
	if strings.Compare(action, "") == 0 {
		action = "list"
	}

	switch action {
	case "list":
		listFn := session.List
		if opts.Archived {
			listFn = session.ListArchived
		}
		items, err := listFn()
		if err != nil {
			return &SavedSessionsOutput{
				RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Inspect ~/.ntm/sessions permissions"),
				SchemaID:      savedSessionsSchemaID,
				SchemaVersion: savedSessionsSchemaVersion,
				Action:        "list",
				Archived:      opts.Archived,
				Sessions:      []session.SavedSession{},
			}, nil
		}
		if items == nil {
			items = []session.SavedSession{}
		}
		count := len(items)
		scope := "active"
		if opts.Archived {
			scope = "archived"
		}
		return &SavedSessionsOutput{
			RobotResponse: NewRobotResponse(true),
			SchemaID:      savedSessionsSchemaID,
			SchemaVersion: savedSessionsSchemaVersion,
			Action:        "list",
			Archived:      opts.Archived,
			Sessions:      items,
			Count:         &count,
			AgentHints: &AgentHints{
				Summary: fmt.Sprintf("Found %d %s saved session(s).", count, scope),
			},
		}, nil

	case "show":
		name := strings.TrimSpace(opts.Name)
		if strings.Compare(name, "") == 0 {
			return &SavedSessionsOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("saved session name is required for show"),
					ErrCodeInvalidFlag,
					"Pass --saved-name=<name> with --saved-action=show",
				),
				SchemaID:      savedSessionsSchemaID,
				SchemaVersion: savedSessionsSchemaVersion,
				Action:        "show",
			}, nil
		}
		state, err := session.Load(name)
		if err != nil {
			code := ErrCodeInternalError
			if os.IsNotExist(err) || strings.Contains(err.Error(), "no saved session") || strings.Contains(err.Error(), "cannot be") {
				code = ErrCodeSessionNotFound
				if strings.Contains(err.Error(), "cannot be") {
					code = ErrCodeInvalidFlag
				}
			}
			return &SavedSessionsOutput{
				RobotResponse: NewErrorResponse(err, code, "Run --robot-saved-sessions to list available saved sessions"),
				SchemaID:      savedSessionsSchemaID,
				SchemaVersion: savedSessionsSchemaVersion,
				Action:        "show",
				SavedAs:       name,
			}, nil
		}
		resumable := CountResumablePanes(state)
		return &SavedSessionsOutput{
			RobotResponse:  NewRobotResponse(true),
			SchemaID:       savedSessionsSchemaID,
			SchemaVersion:  savedSessionsSchemaVersion,
			Action:         "show",
			Session:        state.Name,
			SavedAs:        name,
			State:          state,
			ResumablePanes: &resumable,
			AgentHints: &AgentHints{
				Summary: fmt.Sprintf("Saved session %q has %d pane(s) (%d with fresh casr/native resume binding).", name, len(state.Panes), resumable),
			},
		}, nil

	case "save":
		sessName := strings.TrimSpace(opts.Session)
		if strings.Compare(sessName, "") == 0 {
			return &SavedSessionsOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("live tmux session name is required for save"),
					ErrCodeInvalidFlag,
					"Pass --saved-session=<tmux-session> with --saved-action=save",
				),
				SchemaID:      savedSessionsSchemaID,
				SchemaVersion: savedSessionsSchemaVersion,
				Action:        "save",
			}, nil
		}
		if err := tmux.EnsureInstalled(); err != nil {
			return &SavedSessionsOutput{
				RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Ensure tmux is installed and running"),
				SchemaID:      savedSessionsSchemaID,
				SchemaVersion: savedSessionsSchemaVersion,
				Action:        "save",
				Session:       sessName,
			}, nil
		}
		if !tmux.SessionExists(sessName) {
			return &SavedSessionsOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("session '%s' not found", sessName),
					ErrCodeSessionNotFound,
					"Run ntm --robot-status to inspect active tmux sessions",
				),
				SchemaID:      savedSessionsSchemaID,
				SchemaVersion: savedSessionsSchemaVersion,
				Action:        "save",
				Session:       sessName,
			}, nil
		}
		state, err := session.Capture(sessName)
		if err != nil {
			return &SavedSessionsOutput{
				RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Verify tmux session panes are accessible"),
				SchemaID:      savedSessionsSchemaID,
				SchemaVersion: savedSessionsSchemaVersion,
				Action:        "save",
				Session:       sessName,
			}, nil
		}
		customName := strings.TrimSpace(opts.Name)
		path, err := session.Save(state, session.SaveOptions{
			Name:      customName,
			Overwrite: opts.Overwrite,
		})
		if err != nil {
			code := ErrCodeInternalError
			if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "cannot be") {
				code = ErrCodeInvalidFlag
			}
			return &SavedSessionsOutput{
				RobotResponse: NewErrorResponse(err, code, "Pass --saved-overwrite to replace an existing snapshot"),
				SchemaID:      savedSessionsSchemaID,
				SchemaVersion: savedSessionsSchemaVersion,
				Action:        "save",
				Session:       sessName,
			}, nil
		}
		savedAs := customName
		if strings.Compare(savedAs, "") == 0 {
			savedAs = sessName
		}
		resumable := CountResumablePanes(state)
		return &SavedSessionsOutput{
			RobotResponse:  NewRobotResponse(true),
			SchemaID:       savedSessionsSchemaID,
			SchemaVersion:  savedSessionsSchemaVersion,
			Action:         "save",
			Session:        sessName,
			SavedAs:        savedAs,
			FilePath:       path,
			State:          state,
			ResumablePanes: &resumable,
			AgentHints: &AgentHints{
				Summary: fmt.Sprintf("Captured session %q as %q (%d pane(s), %d resumable).", sessName, savedAs, len(state.Panes), resumable),
			},
		}, nil

	case "archive", "unarchive":
		name := strings.TrimSpace(opts.Name)
		if strings.Compare(name, "") == 0 {
			return &SavedSessionsOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("saved session name is required for %s", action),
					ErrCodeInvalidFlag,
					fmt.Sprintf("Pass --saved-name=<name> with --saved-action=%s", action),
				),
				SchemaID:      savedSessionsSchemaID,
				SchemaVersion: savedSessionsSchemaVersion,
				Action:        action,
			}, nil
		}
		fn := session.Archive
		if strings.EqualFold(action, "unarchive") {
			fn = session.Unarchive
		}
		path, err := fn(name)
		if err != nil {
			code := ErrCodeInternalError
			if strings.Contains(err.Error(), "no saved session") || strings.Contains(err.Error(), "no archived session") {
				code = ErrCodeSessionNotFound
			} else if strings.Contains(err.Error(), "already") || strings.Contains(err.Error(), "cannot be") {
				code = ErrCodeInvalidFlag
			}
			return &SavedSessionsOutput{
				RobotResponse: NewErrorResponse(err, code, "Check saved and archived session lists with --robot-saved-sessions"),
				SchemaID:      savedSessionsSchemaID,
				SchemaVersion: savedSessionsSchemaVersion,
				Action:        action,
				Session:       name,
				SavedAs:       name,
			}, nil
		}
		return &SavedSessionsOutput{
			RobotResponse: NewRobotResponse(true),
			SchemaID:      savedSessionsSchemaID,
			SchemaVersion: savedSessionsSchemaVersion,
			Action:        action,
			Session:       name,
			SavedAs:       name,
			FilePath:      path,
			AgentHints: &AgentHints{
				Summary: fmt.Sprintf("Completed %s for saved session %q.", action, name),
			},
		}, nil

	default:
		return &SavedSessionsOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("invalid saved-sessions action %q (expected list, show, save, archive, or unarchive)", opts.Action),
				ErrCodeInvalidFlag,
				"Use --saved-action=list, show, save, archive, or unarchive",
			),
			SchemaID:      savedSessionsSchemaID,
			SchemaVersion: savedSessionsSchemaVersion,
			Action:        action,
		}, nil
	}
}

// PrintSavedSessions writes the terminal JSON/TOON response for --robot-saved-sessions.
func PrintSavedSessions(opts SavedSessionsOptions) error {
	out, err := GetSavedSessions(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot saved-sessions failed")
}

func savedSessionsCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "saved-sessions",
		Flag:        "--robot-saved-sessions",
		Category:    "state",
		Summary:     "Manage saved session snapshots.",
		Description: "List, inspect (with 12-harness counts and casr/native resume readiness), capture, archive, or unarchive saved session snapshots.",
		Parameters: []RobotParameter{
			{Name: "saved-action", Flag: "--saved-action", Type: "string", Required: false, Default: "list", Description: "Action: list (default), show, save, archive, or unarchive"},
			{Name: "saved-name", Flag: "--saved-name", Type: "string", Required: false, Description: "Saved snapshot name for show/archive/unarchive, or custom destination name for save"},
			{Name: "saved-session", Flag: "--saved-session", Type: "string", Required: false, Description: "Live tmux session name to capture when --saved-action=save"},
			{Name: "saved-archived", Flag: "--saved-archived", Type: "bool", Required: false, Default: "false", Description: "List archived snapshots instead of active ones when --saved-action=list"},
			{Name: "saved-overwrite", Flag: "--saved-overwrite", Type: "bool", Required: false, Default: "false", Description: "Overwrite an existing snapshot when --saved-action=save"},
		},
		Examples: []string{
			"ntm --robot-saved-sessions",
			"ntm --robot-saved-sessions --saved-archived",
			"ntm --robot-saved-sessions --saved-action=show --saved-name=myproject",
			"ntm --robot-saved-sessions --saved-action=save --saved-session=myproject --saved-overwrite",
			"ntm --robot-saved-sessions --saved-action=archive --saved-name=myproject",
		},
	}
}
