package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// SessionCreateInput is the kernel input for sessions.create.
type SessionCreateInput struct {
	Session string `json:"session"`
	Panes   int    `json:"panes,omitempty"`
}

func init() {
	kernel.MustRegister(kernel.Command{
		Name:        "sessions.create",
		Description: "Create a tmux session",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "SessionCreateInput",
			Ref:  "cli.SessionCreateInput",
		},
		Output: &kernel.SchemaRef{
			Name: "CreateResponse",
			Ref:  "output.CreateResponse",
		},
		REST: &kernel.RESTBinding{
			Method: "POST",
			Path:   "/sessions",
		},
		Examples: []kernel.Example{
			{
				Name:        "create",
				Description: "Create a session with defaults",
				Command:     "ntm create myproject",
			},
			{
				Name:        "create-panes",
				Description: "Create a session with 6 panes",
				Command:     "ntm create myproject --panes=6",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  false,
	})
	kernel.MustRegisterHandler("sessions.create", func(ctx context.Context, input any) (any, error) {
		opts := SessionCreateInput{}
		switch value := input.(type) {
		case SessionCreateInput:
			opts = value
		case *SessionCreateInput:
			if value != nil {
				opts = *value
			}
		case map[string]interface{}:
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("marshal sessions.create input: %w", err)
			}
			if err := json.Unmarshal(raw, &opts); err != nil {
				return nil, fmt.Errorf("decode sessions.create input: %w", err)
			}
		}
		if strings.TrimSpace(opts.Session) == "" {
			return nil, fmt.Errorf("session is required")
		}
		base, label := config.ParseSessionLabel(opts.Session)
		if err := config.ValidateProjectName(base); err != nil {
			return nil, err
		}
		if label != "" {
			if err := config.ValidateLabel(label); err != nil {
				return nil, fmt.Errorf("invalid label: %w", err)
			}
		}
		return buildCreateResponse(opts.Session, opts.Panes)
	})
}

func newCreateCmd() *cobra.Command {
	var (
		panes int
		label string
	)

	cmd := &cobra.Command{
		Use:   "create <session-name>",
		Short: "Create a new tmux session with multiple panes",
		Long: `Create a new tmux session with the specified number of panes.
The session directory is created under PROJECTS_BASE if it doesn't exist.

Example:
  ntm create myproject           # Create with default panes
  ntm create myproject --panes=6 # Create with 6 panes
  ntm create myproject --label frontend  # Labeled session`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionName := args[0]

			// Reject project names containing "--" (reserved separator) (bd-1933u)
			if err := config.ValidateProjectName(sessionName); err != nil {
				return err
			}

			// Apply goal label to session name (bd-3cu02.5)
			if label != "" {
				if err := config.ValidateLabel(label); err != nil {
					return fmt.Errorf("invalid label: %w", err)
				}
				sessionName = config.FormatSessionName(sessionName, label)
			}

			return runCreate(sessionName, panes)
		},
	}

	cmd.Flags().IntVarP(&panes, "panes", "p", 0, "number of panes to create (default from config)")
	cmd.Flags().StringVarP(&label, "label", "l", "", "Goal label for multi-session support (e.g., --label frontend creates session PROJECT--frontend)")

	return cmd
}

func runCreate(session string, panes int) (err error) {
	if IsJSONOutput() {
		result, err := kernel.Run(context.Background(), "sessions.create", SessionCreateInput{
			Session: session,
			Panes:   panes,
		})
		if err != nil {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		resp, err := coerceCreateResponse(result)
		if err != nil {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		return output.PrintJSON(resp)
	}

	if err := tmux.EnsureInstalled(); err != nil {
		if IsJSONOutput() {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		return err
	}

	if err := tmux.ValidateSessionName(session); err != nil {
		if IsJSONOutput() {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		return err
	}

	// Get pane count from config if not specified
	if panes <= 0 {
		panes = cfg.Tmux.DefaultPanes
	}

	dir, err := resolveCreationProjectDirForSession(session)
	if err != nil {
		return err
	}
	auditStart := time.Now()
	auditCreated := false
	auditAlreadyExisted := false
	auditAborted := false
	_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorUser, "session.create", map[string]interface{}{
		"phase":          "start",
		"session":        session,
		"panes":          panes,
		"working_dir":    dir,
		"correlation_id": auditCorrelationID,
	}, nil)
	defer func() {
		success := err == nil && !auditAborted
		payload := map[string]interface{}{
			"phase":           "finish",
			"session":         session,
			"panes":           panes,
			"working_dir":     dir,
			"created":         auditCreated,
			"already_existed": auditAlreadyExisted,
			"aborted":         auditAborted,
			"success":         success,
			"duration_ms":     time.Since(auditStart).Milliseconds(),
			"correlation_id":  auditCorrelationID,
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorUser, "session.create", payload, nil)
	}()

	// Initialize hook executor
	hookExec, err := hooks.NewExecutorFromConfig()
	if err != nil {
		if !IsJSONOutput() {
			fmt.Printf("⚠ Warning: could not load hooks config: %v\n", err)
		}
		hookExec = hooks.NewExecutor(nil)
	}

	ctx := context.Background()
	hookCtx := hooks.ExecutionContext{
		SessionName: session,
		ProjectDir:  dir,
	}

	// Run pre-create hooks
	if hookExec.HasHooksForEvent(hooks.EventPreCreate) {
		if !IsJSONOutput() {
			fmt.Println("Running pre-create hooks...")
		}
		results, err := hookExec.RunHooksForEvent(ctx, hooks.EventPreCreate, hookCtx)
		if err != nil {
			if IsJSONOutput() {
				return output.PrintJSON(output.NewError(fmt.Sprintf("pre-create hooks failed: %v", err)))
			}
			return fmt.Errorf("pre-create hooks failed: %w", err)
		}
		if hooks.AnyFailed(results) {
			if IsJSONOutput() {
				return output.PrintJSON(output.NewError(hooks.AllErrors(results).Error()))
			}
			return hooks.AllErrors(results)
		}
	}

	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if IsJSONOutput() {
			// In JSON mode, auto-create directory without prompting
			if err := os.MkdirAll(dir, 0755); err != nil {
				return output.PrintJSON(output.NewError(fmt.Sprintf("creating directory: %v", err)))
			}
		} else {
			fmt.Printf("Directory not found: %s\n", dir)
			if !confirm("Create it?") {
				auditAborted = true
				fmt.Println("Aborted.")
				return nil
			}
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("creating directory: %w", err)
			}
			fmt.Printf("Created %s\n", dir)
		}
	}

	// Check if session already exists
	if tmux.SessionExists(session) {
		auditAlreadyExisted = true
		if IsJSONOutput() {
			// Return info about existing session
			existingPanes, _ := tmux.GetPanes(session)
			paneResponses := make([]output.PaneResponse, len(existingPanes))
			for i, p := range existingPanes {
				paneResponses[i] = output.PaneResponse{
					Index:   p.Index,
					Title:   p.Title,
					Type:    agentTypeToString(p.Type),
					Variant: p.Variant,
					Active:  p.Active,
					Width:   p.Width,
					Height:  p.Height,
					Command: p.Command,
				}
			}
			return output.PrintJSON(output.CreateResponse{
				TimestampedResponse: output.NewTimestamped(),
				Session:             session,
				Created:             false,
				AlreadyExisted:      true,
				WorkingDirectory:    dir,
				PaneCount:           len(existingPanes),
				Panes:               paneResponses,
			})
		}
		fmt.Printf("Session '%s' already exists\n", session)
		return tmux.AttachOrSwitch(session)
	}

	if !IsJSONOutput() {
		fmt.Printf("Creating session '%s' with %d pane(s)...\n", session, panes)
	}

	// Create the session with scrollback history
	historyLimit := tmux.DefaultHistoryLimit
	if cfg != nil && cfg.Tmux.HistoryLimit > 0 {
		historyLimit = cfg.Tmux.HistoryLimit
	}
	if err := tmux.CreateSessionWithHistoryLimit(session, dir, historyLimit); err != nil {
		if IsJSONOutput() {
			return output.PrintJSON(output.NewError(fmt.Sprintf("creating session: %v", err)))
		}
		return fmt.Errorf("creating session: %w", err)
	}
	auditCreated = true

	// Add additional panes
	if panes > 1 {
		for i := 1; i < panes; i++ {
			if _, err := tmux.SplitWindow(session, dir); err != nil {
				if IsJSONOutput() {
					return output.PrintJSON(output.NewError(fmt.Sprintf("creating pane %d: %v", i+1, err)))
				}
				return fmt.Errorf("creating pane %d: %w", i+1, err)
			}
		}
	}

	// Run post-create hooks
	if hookExec.HasHooksForEvent(hooks.EventPostCreate) {
		if !IsJSONOutput() {
			fmt.Println("Running post-create hooks...")
		}
		_, _ = hookExec.RunHooksForEvent(ctx, hooks.EventPostCreate, hookCtx)
	}

	// JSON output mode: return structured response
	if IsJSONOutput() {
		finalPanes, _ := tmux.GetPanes(session)
		paneResponses := make([]output.PaneResponse, len(finalPanes))
		for i, p := range finalPanes {
			paneResponses[i] = output.PaneResponse{
				Index:   p.Index,
				Title:   p.Title,
				Type:    agentTypeToString(p.Type),
				Variant: p.Variant,
				Active:  p.Active,
				Width:   p.Width,
				Height:  p.Height,
				Command: p.Command,
			}
		}
		return output.PrintJSON(output.CreateResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             session,
			Created:             true,
			WorkingDirectory:    dir,
			PaneCount:           len(finalPanes),
			Panes:               paneResponses,
		})
	}

	fmt.Printf("Created session '%s' with %d pane(s)\n", session, panes)
	return tmux.AttachOrSwitch(session)
}

func coerceCreateResponse(result any) (output.CreateResponse, error) {
	switch value := result.(type) {
	case output.CreateResponse:
		return value, nil
	case *output.CreateResponse:
		if value != nil {
			return *value, nil
		}
		return output.CreateResponse{}, fmt.Errorf("sessions.create returned nil response")
	default:
		return output.CreateResponse{}, fmt.Errorf("sessions.create returned unexpected type %T", result)
	}
}

func buildCreateResponse(session string, panes int) (resp output.CreateResponse, err error) {
	if err := tmux.EnsureInstalled(); err != nil {
		return output.CreateResponse{}, err
	}

	if err := tmux.ValidateSessionName(session); err != nil {
		return output.CreateResponse{}, err
	}

	// Get pane count from config if not specified
	if panes <= 0 {
		panes = cfg.Tmux.DefaultPanes
	}

	dir, err := resolveCreationProjectDirForSession(session)
	if err != nil {
		return output.CreateResponse{}, err
	}
	auditStart := time.Now()
	auditCreated := false
	auditAlreadyExisted := false
	_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorUser, "session.create", map[string]interface{}{
		"phase":          "start",
		"session":        session,
		"panes":          panes,
		"working_dir":    dir,
		"correlation_id": auditCorrelationID,
	}, nil)
	defer func() {
		success := err == nil
		payload := map[string]interface{}{
			"phase":           "finish",
			"session":         session,
			"panes":           panes,
			"working_dir":     dir,
			"created":         auditCreated,
			"already_existed": auditAlreadyExisted,
			"success":         success,
			"duration_ms":     time.Since(auditStart).Milliseconds(),
			"correlation_id":  auditCorrelationID,
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorUser, "session.create", payload, nil)
	}()

	// Initialize hook executor
	hookExec, err := hooks.NewExecutorFromConfig()
	if err != nil {
		hookExec = hooks.NewExecutor(nil)
	}

	ctx := context.Background()
	hookCtx := hooks.ExecutionContext{
		SessionName: session,
		ProjectDir:  dir,
	}

	// Run pre-create hooks
	if hookExec.HasHooksForEvent(hooks.EventPreCreate) {
		results, err := hookExec.RunHooksForEvent(ctx, hooks.EventPreCreate, hookCtx)
		if err != nil {
			return output.CreateResponse{}, fmt.Errorf("pre-create hooks failed: %w", err)
		}
		if hooks.AnyFailed(results) {
			return output.CreateResponse{}, hooks.AllErrors(results)
		}
	}

	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return output.CreateResponse{}, fmt.Errorf("creating directory: %w", err)
		}
	}

	// Check if session already exists
	if tmux.SessionExists(session) {
		auditAlreadyExisted = true
		existingPanes, _ := tmux.GetPanes(session)
		paneResponses := make([]output.PaneResponse, len(existingPanes))
		for i, p := range existingPanes {
			paneResponses[i] = output.PaneResponse{
				Index:   p.Index,
				Title:   p.Title,
				Type:    agentTypeToString(p.Type),
				Variant: p.Variant,
				Active:  p.Active,
				Width:   p.Width,
				Height:  p.Height,
				Command: p.Command,
			}
		}
		resp = output.CreateResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             session,
			Created:             false,
			AlreadyExisted:      true,
			WorkingDirectory:    dir,
			PaneCount:           len(existingPanes),
			Panes:               paneResponses,
		}
		return resp, nil
	}

	// Create the session with scrollback history
	createHistoryLimit := tmux.DefaultHistoryLimit
	if cfg != nil && cfg.Tmux.HistoryLimit > 0 {
		createHistoryLimit = cfg.Tmux.HistoryLimit
	}
	if err := tmux.CreateSessionWithHistoryLimit(session, dir, createHistoryLimit); err != nil {
		return output.CreateResponse{}, fmt.Errorf("creating session: %w", err)
	}
	auditCreated = true

	// Add additional panes
	if panes > 1 {
		for i := 1; i < panes; i++ {
			if _, err := tmux.SplitWindow(session, dir); err != nil {
				return output.CreateResponse{}, fmt.Errorf("creating pane %d: %w", i+1, err)
			}
		}
	}

	// Run post-create hooks
	if hookExec.HasHooksForEvent(hooks.EventPostCreate) {
		_, _ = hookExec.RunHooksForEvent(ctx, hooks.EventPostCreate, hookCtx)
	}

	finalPanes, _ := tmux.GetPanes(session)
	paneResponses := make([]output.PaneResponse, len(finalPanes))
	for i, p := range finalPanes {
		paneResponses[i] = output.PaneResponse{
			Index:   p.Index,
			Title:   p.Title,
			Type:    agentTypeToString(p.Type),
			Variant: p.Variant,
			Active:  p.Active,
			Width:   p.Width,
			Height:  p.Height,
			Command: p.Command,
		}
	}

	resp = output.CreateResponse{
		TimestampedResponse: output.NewTimestamped(),
		Session:             session,
		Created:             true,
		WorkingDirectory:    dir,
		PaneCount:           len(finalPanes),
		Panes:               paneResponses,
	}
	return resp, nil
}

// confirm prompts the user for y/n confirmation
func confirm(prompt string) bool {
	return output.ConfirmWithOptions(prompt, output.ConfirmOptions{
		Default: false,
	})
}

// agentTypeToString converts a tmux.AgentType to a string for JSON output
func agentTypeToString(t tmux.AgentType) string {
	switch t {
	case tmux.AgentClaude:
		return "claude"
	case tmux.AgentCodex:
		return "codex"
	case tmux.AgentGemini:
		return "gemini"
	case tmux.AgentCursor:
		return "cursor"
	case tmux.AgentWindsurf:
		return "windsurf"
	case tmux.AgentAider:
		return "aider"
	case tmux.AgentOllama:
		return "ollama"
	case tmux.AgentUser:
		return "user"
	default:
		if s := string(t); s != "" {
			return s
		}
		return "unknown"
	}
}

func incrementAgentCounts(counts *output.AgentCountsResponse, t tmux.AgentType) {
	if counts == nil {
		return
	}

	switch tmux.AgentType(t).Canonical() {
	case tmux.AgentClaude:
		counts.Claude++
	case tmux.AgentCodex:
		counts.Codex++
	case tmux.AgentGemini:
		counts.Gemini++
	case tmux.AgentOllama:
		counts.Ollama++
	case tmux.AgentCursor:
		counts.Cursor++
	case tmux.AgentWindsurf:
		counts.Windsurf++
	case tmux.AgentAider:
		counts.Aider++
	default:
		counts.User++
	}

	counts.Total++
}
