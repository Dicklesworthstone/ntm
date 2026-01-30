package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/cli/suggestions"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// statusOptions holds configuration for the status command
type statusOptions struct {
	tags            []string
	showAssignments bool
	filterStatus    string
	filterAgent     string
	filterPane      int
	showSummary     bool
	watchMode       bool
	interval        time.Duration
}

// SessionListInput is the kernel input for sessions.list.
type SessionListInput struct {
	Tags []string `json:"tags,omitempty"`
}

// SessionStatusInput is the kernel input for sessions.status.
type SessionStatusInput struct {
	Session         string   `json:"session"`
	Tags            []string `json:"tags,omitempty"`
	ShowAssignments bool     `json:"show_assignments,omitempty"`
	FilterStatus    string   `json:"status,omitempty"`
	FilterAgent     string   `json:"agent,omitempty"`
	FilterPane      *int     `json:"pane,omitempty"`
	ShowSummary     bool     `json:"summary,omitempty"`
}

// SessionAttachInput is the kernel input for sessions.attach.
type SessionAttachInput struct {
	Session string `json:"session"`
}

type paneContextUsage struct {
	Tokens  int
	Limit   int
	Percent float64
	Model   string
}

type contextRow struct {
	Label   string
	Percent float64
	Tokens  int
	Limit   int
	Model   string
}

func init() {
	kernel.MustRegister(kernel.Command{
		Name:        "sessions.list",
		Description: "List tmux sessions",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "SessionListInput",
			Ref:  "cli.SessionListInput",
		},
		Output: &kernel.SchemaRef{
			Name: "SessionListResponse",
			Ref:  "output.ListResponse",
		},
		REST: &kernel.RESTBinding{
			Method: "GET",
			Path:   "/sessions",
		},
		Examples: []kernel.Example{
			{
				Name:        "list",
				Description: "List all sessions",
				Command:     "ntm list",
			},
			{
				Name:        "list-with-tag",
				Description: "List sessions filtered by tag",
				Command:     "ntm list --tag=frontend",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("sessions.list", func(ctx context.Context, input any) (any, error) {
		opts := SessionListInput{}
		switch value := input.(type) {
		case SessionListInput:
			opts = value
		case *SessionListInput:
			if value != nil {
				opts = *value
			}
		}
		return buildSessionListResponse(opts.Tags)
	})

	kernel.MustRegister(kernel.Command{
		Name:        "sessions.status",
		Description: "Detailed status for a session",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "SessionStatusInput",
			Ref:  "cli.SessionStatusInput",
		},
		Output: &kernel.SchemaRef{
			Name: "StatusResponse",
			Ref:  "output.StatusResponse",
		},
		REST: &kernel.RESTBinding{
			Method: "GET",
			Path:   "/sessions/{sessionId}/status",
		},
		Examples: []kernel.Example{
			{
				Name:        "status",
				Description: "Show session status",
				Command:     "ntm status myproject",
			},
			{
				Name:        "status-assignments",
				Description: "Show session status with assignments",
				Command:     "ntm status myproject --assignments",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("sessions.status", func(ctx context.Context, input any) (any, error) {
		opts := SessionStatusInput{}
		switch value := input.(type) {
		case SessionStatusInput:
			opts = value
		case *SessionStatusInput:
			if value != nil {
				opts = *value
			}
		}
		if strings.TrimSpace(opts.Session) == "" {
			return nil, fmt.Errorf("session is required")
		}
		filterPane := -1
		if opts.FilterPane != nil {
			filterPane = *opts.FilterPane
		}
		statusOpts := statusOptions{
			tags:            opts.Tags,
			showAssignments: opts.ShowAssignments,
			filterStatus:    opts.FilterStatus,
			filterAgent:     opts.FilterAgent,
			filterPane:      filterPane,
			showSummary:     opts.ShowSummary,
		}
		return buildStatusResponse(opts.Session, statusOpts)
	})

	kernel.MustRegister(kernel.Command{
		Name:        "sessions.attach",
		Description: "Attach to a session",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "SessionAttachInput",
			Ref:  "cli.SessionAttachInput",
		},
		Output: &kernel.SchemaRef{
			Name: "SessionResponse",
			Ref:  "output.SessionResponse",
		},
		REST: &kernel.RESTBinding{
			Method: "POST",
			Path:   "/sessions/{sessionId}/attach",
		},
		Examples: []kernel.Example{
			{
				Name:        "attach",
				Description: "Attach to session",
				Command:     "ntm attach myproject",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  false,
	})
	kernel.MustRegisterHandler("sessions.attach", func(ctx context.Context, input any) (any, error) {
		opts := SessionAttachInput{}
		switch value := input.(type) {
		case SessionAttachInput:
			opts = value
		case *SessionAttachInput:
			if value != nil {
				opts = *value
			}
		}
		if strings.TrimSpace(opts.Session) == "" {
			return nil, fmt.Errorf("session is required")
		}
		return buildAttachResponse(opts.Session)
	})
}

// filterAssignments filters assignments by status, agent type, and pane number.
// Empty filterStatus or filterAgent means no filtering on that field.
// filterPane < 0 means no filtering on pane.
func filterAssignments(assignments []*assignment.Assignment, filterStatus, filterAgent string, filterPane int) []*assignment.Assignment {
	if filterStatus == "" && filterAgent == "" && filterPane < 0 {
		return assignments // No filtering needed
	}

	result := make([]*assignment.Assignment, 0, len(assignments))
	for _, a := range assignments {
		// Filter by status
		if filterStatus != "" && string(a.Status) != filterStatus {
			continue
		}
		// Filter by agent type
		if filterAgent != "" && a.AgentType != filterAgent {
			continue
		}
		// Filter by pane
		if filterPane >= 0 && a.Pane != filterPane {
			continue
		}
		result = append(result, a)
	}
	return result
}

func newAttachCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "attach <session-name>",
		Aliases: []string{"a"},
		Short:   "Attach to a tmux session",
		Long: `Attach to an existing tmux session. If already inside tmux,
switches to the target session instead.

If the session doesn't exist, shows available sessions.

Examples:
  ntm attach myproject`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				// No session specified, list sessions
				return runList(nil)
			}
			return runAttach(args[0])
		},
	}

	cmd.ValidArgsFunction = completeSessionArgs

	return cmd
}

func runAttach(session string) error {
	if IsJSONOutput() {
		result, err := kernel.Run(context.Background(), "sessions.attach", SessionAttachInput{Session: session})
		if err != nil {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		resp, err := coerceSessionResponse(result)
		if err != nil {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		return output.PrintJSON(resp)
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	if tmux.SessionExists(session) {
		// Update Agent Mail activity (non-blocking)
		updateSessionActivity(session)
		return tmux.AttachOrSwitch(session)
	}

	if IsJSONOutput() {
		return output.PrintJSON(output.NewError(fmt.Sprintf("session '%s' does not exist", session)))
	}

	fmt.Printf("Session '%s' does not exist.\n\n", session)
	fmt.Println("Available sessions:")
	if err := runList(nil); err != nil {
		return err
	}
	fmt.Println()

	if confirm(fmt.Sprintf("Create '%s' with default settings?", session)) {
		return runCreate(session, 0)
	}

	return nil
}

func newListCmd() *cobra.Command {
	var tags []string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "l"},
		Short:   "List all tmux sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(tags)
		},
	}
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "filter sessions by agent tag (shows session if any agent matches)")
	return cmd
}

func runList(tags []string) error {
	result, err := kernel.Run(context.Background(), "sessions.list", SessionListInput{Tags: tags})
	if err != nil {
		if IsJSONOutput() {
			_ = output.PrintJSON(output.NewError(err.Error()))
		}
		return err
	}

	resp, err := coerceSessionListResponse(result)
	if err != nil {
		return err
	}

	if IsJSONOutput() {
		return output.PrintJSON(resp)
	}

	// Text output
	if len(resp.Sessions) == 0 {
		fmt.Println("No tmux sessions running")
		return nil
	}

	// Check terminal width for responsive output
	width, _, _ := term.GetSize(int(os.Stdout.Fd()))
	isWide := width >= 100

	if isWide {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "SESSION\tWINDOWS\tSTATE\tAGENTS")

		for _, s := range resp.Sessions {
			attached := "detached"
			if s.Attached {
				attached = "attached"
			}

			// Fetch agents summary
			agents := "-"
			if s.AgentCounts != nil {
				var parts []string
				if s.AgentCounts.Claude > 0 {
					parts = append(parts, fmt.Sprintf("%d CC", s.AgentCounts.Claude))
				}
				if s.AgentCounts.Codex > 0 {
					parts = append(parts, fmt.Sprintf("%d COD", s.AgentCounts.Codex))
				}
				if s.AgentCounts.Gemini > 0 {
					parts = append(parts, fmt.Sprintf("%d GMI", s.AgentCounts.Gemini))
				}
				if s.AgentCounts.Cursor > 0 {
					parts = append(parts, fmt.Sprintf("%d CUR", s.AgentCounts.Cursor))
				}
				if s.AgentCounts.Windsurf > 0 {
					parts = append(parts, fmt.Sprintf("%d WND", s.AgentCounts.Windsurf))
				}
				if s.AgentCounts.Aider > 0 {
					parts = append(parts, fmt.Sprintf("%d AID", s.AgentCounts.Aider))
				}
				if s.AgentCounts.User > 0 {
					parts = append(parts, fmt.Sprintf("%d Usr", s.AgentCounts.User))
				}
				if len(parts) > 0 {
					agents = strings.Join(parts, ", ")
				}
			}

			fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", s.Name, s.Windows, attached, agents)
		}
		w.Flush()
	} else {
		// Standard output for narrow screens
		for _, s := range resp.Sessions {
			attached := ""
			if s.Attached {
				attached = " (attached)"
			}
			fmt.Printf("  %s: %d windows%s\n", s.Name, s.Windows, attached)
		}
	}

	return nil
}

func coerceSessionListResponse(result any) (output.ListResponse, error) {
	switch value := result.(type) {
	case output.ListResponse:
		return value, nil
	case *output.ListResponse:
		if value != nil {
			return *value, nil
		}
		return output.ListResponse{}, fmt.Errorf("sessions.list returned nil response")
	default:
		return output.ListResponse{}, fmt.Errorf("sessions.list returned unexpected type %T", result)
	}
}

func buildSessionListResponse(tags []string) (output.ListResponse, error) {
	if err := tmux.EnsureInstalled(); err != nil {
		return output.ListResponse{}, err
	}

	sessions, err := tmux.ListSessions()
	if err != nil {
		return output.ListResponse{}, err
	}

	// Optimization: fetch all panes once
	allPanes, err := tmux.GetAllPanes()
	if err != nil {
		return output.ListResponse{}, err
	}

	// Filter sessions by tag
	if len(tags) > 0 {
		var filtered []tmux.Session
		for _, s := range sessions {
			panes := allPanes[s.Name]
			// Check if any pane has matching tag
			hasTag := false
			for _, p := range panes {
				if HasAnyTag(p.Tags, tags) {
					hasTag = true
					break
				}
			}
			if hasTag {
				filtered = append(filtered, s)
			}
		}
		sessions = filtered
	}

	items := make([]output.SessionListItem, len(sessions))
	for i, s := range sessions {
		item := output.SessionListItem{
			Name:             s.Name,
			Windows:          s.Windows,
			Attached:         s.Attached,
			WorkingDirectory: s.Directory,
		}

		// Get panes to count agents
		panes := allPanes[s.Name]
		if len(panes) > 0 {
			item.PaneCount = len(panes)

			// Count agent types
			var claudeCount, codexCount, geminiCount, cursorCount, windsurfCount, aiderCount, userCount int
			for _, p := range panes {
				switch p.Type {
				case tmux.AgentClaude:
					claudeCount++
				case tmux.AgentCodex:
					codexCount++
				case tmux.AgentGemini:
					geminiCount++
				case tmux.AgentCursor:
					cursorCount++
				case tmux.AgentWindsurf:
					windsurfCount++
				case tmux.AgentAider:
					aiderCount++
				default:
					userCount++
				}
			}
			item.AgentCounts = &output.AgentCountsResponse{
				Claude:   claudeCount,
				Codex:    codexCount,
				Gemini:   geminiCount,
				Cursor:   cursorCount,
				Windsurf: windsurfCount,
				Aider:    aiderCount,
				User:     userCount,
				Total:    len(panes),
			}
		}
		items[i] = item
	}

	return output.ListResponse{
		TimestampedResponse: output.NewTimestamped(),
		Sessions:            items,
		Count:               len(sessions),
	}, nil
}

func coerceStatusResponse(result any) (output.StatusResponse, error) {
	switch value := result.(type) {
	case output.StatusResponse:
		return value, nil
	case *output.StatusResponse:
		if value != nil {
			return *value, nil
		}
		return output.StatusResponse{}, fmt.Errorf("sessions.status returned nil response")
	default:
		return output.StatusResponse{}, fmt.Errorf("sessions.status returned unexpected type %T", result)
	}
}

func estimatePaneContextUsage(p tmux.Pane) (paneContextUsage, bool) {
	if p.Type == tmux.AgentUser {
		return paneContextUsage{}, false
	}
	modelName := modelNameForPane(p)
	if modelName == "" {
		return paneContextUsage{}, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := tmux.CaptureForFullContextContext(ctx, p.ID)
	if err != nil || out == "" {
		return paneContextUsage{}, false
	}

	usage := tokens.GetUsageInfo(out, modelName)
	if usage == nil {
		return paneContextUsage{}, false
	}

	return paneContextUsage{
		Tokens:  usage.EstimatedTokens,
		Limit:   usage.ContextLimit,
		Percent: usage.UsagePercent,
		Model:   usage.Model,
	}, true
}

func buildStatusResponse(session string, opts statusOptions) (output.StatusResponse, error) {
	if err := tmux.EnsureInstalled(); err != nil {
		return output.StatusResponse{}, err
	}

	if !tmux.SessionExists(session) {
		return output.StatusResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             session,
			Exists:              false,
		}, nil
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return output.StatusResponse{}, err
	}

	// Filter panes by tag
	if len(opts.tags) > 0 {
		var filtered []tmux.Pane
		for _, p := range panes {
			if HasAnyTag(p.Tags, opts.tags) {
				filtered = append(filtered, p)
			}
		}
		panes = filtered
	}

	dir := cfg.GetProjectDir(session)

	// Load handoff info (best-effort)
	var handoffGoal, handoffNow, handoffStatus, handoffPath string
	var handoffAge time.Duration
	{
		reader := handoff.NewReader(dir)
		if goal, now, err := reader.ExtractGoalNow(session); err == nil {
			handoffGoal = goal
			handoffNow = now
		}
		if h, path, err := reader.FindLatest(session); err == nil && h != nil {
			if handoffGoal == "" {
				handoffGoal = h.Goal
			}
			if handoffNow == "" {
				handoffNow = h.Now
			}
			handoffStatus = h.Status
			handoffPath = path
			handoffAge = time.Since(h.CreatedAt)
		}
	}

	// Calculate counts
	var ccCount, codCount, gmiCount, otherCount int
	for _, p := range panes {
		switch p.Type {
		case tmux.AgentClaude:
			ccCount++
		case tmux.AgentCodex:
			codCount++
		case tmux.AgentGemini:
			gmiCount++
		default:
			otherCount++
		}
	}

	// Estimate context usage per pane (best-effort)
	contextByIndex := make(map[int]paneContextUsage)
	for _, p := range panes {
		usage, ok := estimatePaneContextUsage(p)
		if !ok {
			continue
		}
		contextByIndex[p.Index] = usage
	}

	// Load assignments if requested (or if filtering/summary requires them)
	var assignmentStore *assignment.AssignmentStore
	needAssignments := opts.showAssignments || opts.filterStatus != "" || opts.filterAgent != "" || opts.filterPane >= 0 || opts.showSummary
	if needAssignments {
		assignmentStore, _ = assignment.LoadStore(session)
	}

	// Check if session is attached
	attached := false
	sessions, _ := tmux.ListSessions()
	for _, s := range sessions {
		if s.Name == session {
			attached = s.Attached
			break
		}
	}

	resp := output.StatusResponse{
		TimestampedResponse: output.NewTimestamped(),
		Session:             session,
		Exists:              true,
		Attached:            attached,
		WorkingDirectory:    dir,
		AgentCounts: output.AgentCountsResponse{
			Claude: ccCount,
			Codex:  codCount,
			Gemini: gmiCount,
			User:   otherCount,
			Total:  len(panes),
		},
	}

	if handoffGoal != "" || handoffNow != "" || handoffStatus != "" {
		handoffInfo := &output.HandoffStatus{
			Session: session,
			Goal:    handoffGoal,
			Now:     handoffNow,
			Path:    handoffPath,
			Status:  handoffStatus,
		}
		if handoffAge > 0 {
			handoffInfo.AgeSeconds = int64(handoffAge.Seconds())
		}
		resp.Handoff = handoffInfo
	}

	// Add panes
	for _, p := range panes {
		paneResp := output.PaneResponse{
			Index:   p.Index,
			Title:   p.Title,
			Type:    agentTypeToString(p.Type),
			Variant: p.Variant,
			Active:  p.Active,
			Width:   p.Width,
			Height:  p.Height,
			Command: p.Command,
		}
		if usage, ok := contextByIndex[p.Index]; ok {
			paneResp.ContextTokens = usage.Tokens
			paneResp.ContextLimit = usage.Limit
			paneResp.ContextPercent = usage.Percent
			paneResp.ContextModel = usage.Model
		}
		resp.Panes = append(resp.Panes, paneResp)
	}

	// Add assignments if requested (with optional filtering)
	if needAssignments && assignmentStore != nil {
		assignments := assignmentStore.List()
		// Apply filters
		assignments = filterAssignments(assignments, opts.filterStatus, opts.filterAgent, opts.filterPane)
		// Include individual assignments unless --summary is used
		if !opts.showSummary {
			for _, a := range assignments {
				assignResp := output.AssignmentResponse{
					BeadID:     a.BeadID,
					BeadTitle:  a.BeadTitle,
					Pane:       a.Pane,
					AgentType:  a.AgentType,
					AgentName:  a.AgentName,
					Status:     string(a.Status),
					AssignedAt: a.AssignedAt.Format(time.RFC3339),
					FailReason: a.FailReason,
				}
				if a.StartedAt != nil {
					ts := a.StartedAt.Format(time.RFC3339)
					assignResp.StartedAt = &ts
				}
				if a.CompletedAt != nil {
					ts := a.CompletedAt.Format(time.RFC3339)
					assignResp.CompletedAt = &ts
				}
				if a.FailedAt != nil {
					ts := a.FailedAt.Format(time.RFC3339)
					assignResp.FailedAt = &ts
				}
				resp.Assignments = append(resp.Assignments, assignResp)
			}
		}
		stats := assignmentStore.Stats()
		resp.AssignmentStats = &output.AssignmentStats{
			Total:      stats.Total,
			Assigned:   stats.Assigned,
			Working:    stats.Working,
			Completed:  stats.Completed,
			Failed:     stats.Failed,
			Reassigned: stats.Reassigned,
		}
	}

	return resp, nil
}

func coerceSessionResponse(result any) (output.SessionResponse, error) {
	switch value := result.(type) {
	case output.SessionResponse:
		return value, nil
	case *output.SessionResponse:
		if value != nil {
			return *value, nil
		}
		return output.SessionResponse{}, fmt.Errorf("sessions.attach returned nil response")
	default:
		return output.SessionResponse{}, fmt.Errorf("sessions.attach returned unexpected type %T", result)
	}
}

func buildAttachResponse(session string) (output.SessionResponse, error) {
	if err := tmux.EnsureInstalled(); err != nil {
		return output.SessionResponse{}, err
	}

	if !tmux.SessionExists(session) {
		return output.SessionResponse{
			Session: session,
			Exists:  false,
		}, nil
	}

	attached := false
	sessions, _ := tmux.ListSessions()
	for _, s := range sessions {
		if s.Name == session {
			attached = s.Attached
			break
		}
	}

	return output.SessionResponse{
		Session:  session,
		Exists:   true,
		Attached: attached,
	}, nil
}
func newStatusCmd() *cobra.Command {
	var tags []string
	var showAssignments bool
	var filterStatus string
	var filterAgent string
	var filterPane int
	var showSummary bool
	var watch bool
	var interval int
	cmd := &cobra.Command{
		Use:   "status <session-name>",
		Short: "Show detailed status of a session",
		Long: `Show detailed information about a session including:
- All panes with their titles and current commands
- Agent type counts (Claude, Codex, Gemini)
- Session directory
- Bead assignments (with --assignments flag)

Assignment Filtering (requires --assignments):
  --status=<status>  Filter by: assigned, working, completed, failed, reassigned
  --agent=<type>     Filter by: claude, codex, gemini
  --pane=<n>         Filter by pane number
  --summary          Show aggregated statistics only

Examples:
  ntm status myproject
  ntm status myproject --tag=frontend
  ntm status myproject --assignments
  ntm status myproject --assignments --status=working
  ntm status myproject --assignments --agent=claude
  ntm status myproject --assignments --status=failed --agent=codex
  ntm status myproject --assignments --summary
  ntm status myproject --watch`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := statusOptions{
				tags:            tags,
				showAssignments: showAssignments,
				filterStatus:    filterStatus,
				filterAgent:     filterAgent,
				filterPane:      filterPane,
				showSummary:     showSummary,
				watchMode:       watch,
				interval:        time.Duration(interval) * time.Millisecond,
			}
			return runStatus(cmd.OutOrStdout(), args[0], opts)
		},
	}
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "filter panes by tag")
	cmd.Flags().BoolVar(&showAssignments, "assignments", false, "show bead-to-agent assignments")
	cmd.Flags().StringVar(&filterStatus, "status", "", "filter assignments by status (assigned, working, completed, failed, reassigned)")
	cmd.Flags().StringVar(&filterAgent, "agent", "", "filter assignments by agent type (claude, codex, gemini)")
	cmd.Flags().IntVar(&filterPane, "pane", -1, "filter assignments by pane number")
	cmd.Flags().BoolVar(&showSummary, "summary", false, "show assignment summary statistics only")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "auto-refresh display")
	cmd.Flags().IntVar(&interval, "interval", 2000, "refresh interval in milliseconds (with --watch)")
	cmd.ValidArgsFunction = completeSessionArgs
	return cmd
}

func runStatus(w io.Writer, session string, opts statusOptions) error {
	if opts.watchMode {
		return runStatusWatch(w, session, opts)
	}
	return runStatusOnce(w, session, opts)
}

func runStatusOnce(w io.Writer, session string, opts statusOptions) error {
	// Helper for JSON error output
	outputError := func(err error) error {
		if IsJSONOutput() {
			_ = output.PrintJSON(output.NewError(err.Error()))
			return err
		}
		return err
	}

	if IsJSONOutput() {
		var filterPane *int
		if opts.filterPane >= 0 {
			filterPane = &opts.filterPane
		}
		result, err := kernel.Run(context.Background(), "sessions.status", SessionStatusInput{
			Session:         session,
			Tags:            opts.tags,
			ShowAssignments: opts.showAssignments,
			FilterStatus:    opts.filterStatus,
			FilterAgent:     opts.filterAgent,
			FilterPane:      filterPane,
			ShowSummary:     opts.showSummary,
		})
		if err != nil {
			return outputError(err)
		}
		resp, err := coerceStatusResponse(result)
		if err != nil {
			return err
		}
		return output.PrintJSON(resp)
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return outputError(err)
	}

	if !tmux.SessionExists(session) {
		if IsJSONOutput() {
			return output.PrintJSON(output.StatusResponse{
				TimestampedResponse: output.NewTimestamped(),
				Session:             session,
				Exists:              false,
			})
		}
		return fmt.Errorf("session '%s' not found", session)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return outputError(err)
	}

	// Filter panes by tag
	if len(opts.tags) > 0 {
		var filtered []tmux.Pane
		for _, p := range panes {
			if HasAnyTag(p.Tags, opts.tags) {
				filtered = append(filtered, p)
			}
		}
		panes = filtered
	}

	dir := cfg.GetProjectDir(session)

	// Load handoff info (best-effort)
	var handoffGoal, handoffNow, handoffStatus string
	var handoffAge time.Duration
	{
		reader := handoff.NewReader(dir)
		if goal, now, err := reader.ExtractGoalNow(session); err == nil {
			handoffGoal = goal
			handoffNow = now
		}
		if h, _, err := reader.FindLatest(session); err == nil && h != nil {
			if handoffGoal == "" {
				handoffGoal = h.Goal
			}
			if handoffNow == "" {
				handoffNow = h.Now
			}
			handoffStatus = h.Status
			handoffAge = time.Since(h.CreatedAt)
		}
	}

	// Calculate counts
	var ccCount, codCount, gmiCount, otherCount int
	for _, p := range panes {
		switch p.Type {
		case tmux.AgentClaude:
			ccCount++
		case tmux.AgentCodex:
			codCount++
		case tmux.AgentGemini:
			gmiCount++
		default:
			otherCount++
		}
	}

	// Estimate context usage per pane (best-effort)
	contextByIndex := make(map[int]paneContextUsage)
	for _, p := range panes {
		usage, ok := estimatePaneContextUsage(p)
		if !ok {
			continue
		}
		contextByIndex[p.Index] = usage
	}

	// Load assignments if requested (or if filtering/summary requires them)
	var assignmentStore *assignment.AssignmentStore
	needAssignments := opts.showAssignments || opts.filterStatus != "" || opts.filterAgent != "" || opts.filterPane >= 0 || opts.showSummary
	if needAssignments {
		assignmentStore, _ = assignment.LoadStore(session)
	}

	// Text output
	t := theme.Current()

	// ANSI helpers
	noColor := theme.NoColorEnabled()
	reset := ""
	bold := ""
	if !noColor {
		reset = "\033[0m"
		bold = "\033[1m"
	}
	color := func(c interface{}) string {
		if noColor {
			return ""
		}
		return colorize(c)
	}

	// Colors
	primary := color(t.Primary)
	surface := color(t.Surface0)
	text := color(t.Text)
	subtext := color(t.Subtext)
	overlay := color(t.Overlay)
	success := color(t.Success)
	claude := color(t.Claude)
	codex := color(t.Codex)
	gemini := color(t.Gemini)

	ic := icons.Current()

	// Detect terminal width and layout tier
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		width = 80 // Default fallback
	}
	tier := layout.TierForWidth(width)

	fmt.Fprintln(w)

	// Header with icon
	fmt.Fprintf(w, "  %s%s%s %s%s%s%s\n", primary, ic.Session, reset, bold, session, reset, text)
	fmt.Fprintf(w, "  %s%s%s\n", surface, "─────────────────────────────────────────────────────────", reset)
	fmt.Fprintln(w)

	// Directory info
	fmt.Fprintf(w, "  %s%s Directory:%s %s%s%s\n", subtext, ic.Folder, reset, text, dir, reset)
	fmt.Fprintf(w, "  %s%s Panes:%s    %s%d%s\n", subtext, ic.Pane, reset, text, len(panes), reset)
	fmt.Fprintln(w)

	maxTextWidth := maxInt(width-12, 20)
	if handoffGoal != "" || handoffNow != "" || handoffStatus != "" {
		fmt.Fprintf(w, "  %sHandoff%s\n", bold, reset)
		fmt.Fprintf(w, "  %s%s%s\n", surface, "─────────────────────────────────────────────────────────", reset)

		ageLabel := "unknown"
		if handoffAge > 0 {
			ageLabel = formatDuration(handoffAge) + " ago"
		}
		fmt.Fprintf(w, "    %sLatest:%s %s%s%s\n", subtext, reset, text, ageLabel, reset)
		if handoffStatus != "" {
			fmt.Fprintf(w, "    %sStatus:%s %s%s%s\n", subtext, reset, text, handoffStatus, reset)
		}
		if handoffGoal != "" {
			fmt.Fprintf(w, "    %sGoal:%s %s%s%s\n", subtext, reset, text, layout.TruncateWidthDefault(handoffGoal, maxTextWidth), reset)
		}
		if handoffNow != "" {
			fmt.Fprintf(w, "    %sNow:%s  %s%s%s\n", subtext, reset, text, layout.TruncateWidthDefault(handoffNow, maxTextWidth), reset)
		}
		fmt.Fprintln(w)
	}

	// Panes section
	fmt.Fprintf(w, "  %sPanes%s\n", bold, reset)
	fmt.Fprintf(w, "  %s%s%s\n", surface, "─────────────────────────────────────────────────────────", reset)

	// Create status detector for agent state detection
	detector := status.NewDetector()

	// Get error color for status display
	errorColor := color(t.Error)

	for i, p := range panes {
		var typeColor, typeIcon string
		switch p.Type {
		case tmux.AgentClaude:
			typeColor = claude
			typeIcon = ic.Claude
		case tmux.AgentCodex:
			typeColor = codex
			typeIcon = ic.Codex
		case tmux.AgentGemini:
			typeColor = gemini
			typeIcon = ic.Gemini
		default:
			typeColor = success
			typeIcon = ic.User
		}

		// Number for quick selection (1-9)
		num := ""
		if i < 9 {
			num = fmt.Sprintf("%s%d%s ", overlay, i+1, reset)
		} else {
			num = "  "
		}

		// Detect agent status
		agentStatus, _ := detector.Detect(p.ID)
		stateIcon := agentStatus.State.Icon()
		stateColor := overlay
		stateText := ""
		switch agentStatus.State {
		case status.StateIdle:
			stateColor = overlay
			stateText = "idle"
		case status.StateWorking:
			stateColor = success
			stateText = "working"
		case status.StateError:
			stateColor = errorColor
			stateText = "error"
			if agentStatus.ErrorType != status.ErrorNone {
				stateText = string(agentStatus.ErrorType)
			}
		default:
			stateColor = overlay
			stateText = "unknown"
		}

		// Calculate columns based on tier
		var variantPart, cmdPart string
		var titleWidth int
		var variantWidth int
		var cmdWidth int

		switch {
		case tier >= layout.TierUltra:
			titleWidth = 35
			variantWidth = 15
			cmdWidth = 40
		case tier >= layout.TierWide:
			titleWidth = 25
			variantWidth = 10
			cmdWidth = 25
		case tier >= layout.TierSplit:
			titleWidth = 20
			variantWidth = 0
			cmdWidth = 15
		default: // Narrow
			titleWidth = 15
			variantWidth = 0
			cmdWidth = 10
		}

		title := layout.TruncateWidthDefault(p.Title, titleWidth)
		titlePart := fmt.Sprintf("%-*s", titleWidth, title)

		if variantWidth > 0 {
			variant := ""
			if p.Variant != "" {
				variant = layout.TruncateWidthDefault(p.Variant, variantWidth)
			}
			variantPart = fmt.Sprintf(" %s%-*s%s", subtext, variantWidth, variant, reset)
		}

		if cmdWidth > 0 {
			cmd := ""
			if p.Command != "" {
				cmd = layout.TruncateWidthDefault(p.Command, cmdWidth)
			}
			cmdPart = fmt.Sprintf(" %s%-*s%s", subtext, cmdWidth, cmd, reset)
		}

		// Pane info with status
		fmt.Fprintf(w, "  %s%s %s%s %s%s%s%s %s│%s %s%-8s%s\n",
			num,
			stateIcon,
			typeColor, typeIcon,
			titlePart,
			reset,
			variantPart,
			cmdPart,
			surface, reset,
			stateColor, stateText, reset)
	}

	fmt.Fprintf(w, "  %s%s%s\n", surface, "─────────────────────────────────────────────────────────", reset)
	fmt.Fprintln(w)

	var contextRows []contextRow
	for _, p := range panes {
		usage, ok := contextByIndex[p.Index]
		if !ok {
			continue
		}
		label := layout.TruncateWidthDefault(paneLabel(session, p), 12)
		model := layout.TruncateWidthDefault(usage.Model, 28)
		contextRows = append(contextRows, contextRow{
			Label:   label,
			Percent: usage.Percent,
			Tokens:  usage.Tokens,
			Limit:   usage.Limit,
			Model:   model,
		})
	}

	if len(contextRows) > 0 {
		sort.Slice(contextRows, func(i, j int) bool {
			return contextRows[i].Percent > contextRows[j].Percent
		})

		barWidth := 18
		if width < 110 {
			barWidth = 12
		} else if width >= 160 {
			barWidth = 24
		}

		warnColor := color(t.Warning)
		fmt.Fprintf(w, "  %sContext Usage%s\n", bold, reset)
		fmt.Fprintf(w, "  %s%s%s\n", surface, "─────────────────────────────────────────────────────────", reset)

		for _, row := range contextRows {
			percentColor := text
			if row.Percent >= 85 {
				percentColor = errorColor
			} else if row.Percent >= 70 {
				percentColor = warnColor
			}
			percentText := fmt.Sprintf("%s%.0f%%%s", percentColor, row.Percent, reset)

			tokenInfo := ""
			if row.Limit > 0 {
				tokenInfo = fmt.Sprintf(" (%s/%s)", formatTokenCount(row.Tokens), formatTokenCount(row.Limit))
			}

			warnMark := ""
			if row.Percent >= 70 {
				warnMark = fmt.Sprintf(" %s%s%s", percentColor, ic.Warning, reset)
			}

			bar := renderProgressBar(row.Percent, barWidth)
			fmt.Fprintf(w, "    %s%-12s%s %s of %s context%s %s%s\n",
				text, row.Label, reset,
				percentText, row.Model, tokenInfo, bar, warnMark)
		}
		fmt.Fprintln(w)
	}

	// Agent summary with icons
	fmt.Fprintf(w, "  %sAgents%s\n", bold, reset)

	if ccCount > 0 {
		fmt.Fprintf(w, "    %s%s Claude%s  %s%d instance(s)%s\n", claude, ic.Claude, reset, text, ccCount, reset)
	}
	if codCount > 0 {
		fmt.Fprintf(w, "    %s%s Codex%s   %s%d instance(s)%s\n", codex, ic.Codex, reset, text, codCount, reset)
	}
	if gmiCount > 0 {
		fmt.Fprintf(w, "    %s%s Gemini%s  %s%d instance(s)%s\n", gemini, ic.Gemini, reset, text, gmiCount, reset)
	}
	if otherCount > 0 {
		fmt.Fprintf(w, "    %s%s User%s    %s%d pane(s)%s\n", success, ic.User, reset, text, otherCount, reset)
	}

	totalAgents := ccCount + codCount + gmiCount
	if totalAgents == 0 {
		fmt.Fprintf(w, "    %sNo agents running%s\n", overlay, reset)
	}

	fmt.Fprintln(w)

	// Agent Mail section
	agentMailStatus := fetchAgentMailStatus(dir)
	if agentMailStatus != nil && agentMailStatus.Available {
		mailColor := color(t.Lavender)
		lockIcon := "🔒"

		fmt.Fprintf(w, "  %sAgent Mail%s\n", bold, reset)
		fmt.Fprintf(w, "  %s%s%s\n", surface, "─────────────────────────────────────────────────────────", reset)

		if agentMailStatus.Connected {
			fmt.Fprintf(w, "    %s✓ Connected%s to %s%s%s\n", success, reset, subtext, agentMailStatus.ServerURL, reset)
		} else {
			fmt.Fprintf(w, "    %s○ Available%s at %s%s%s\n", overlay, reset, subtext, agentMailStatus.ServerURL, reset)
		}

		if agentMailStatus.ActiveLocks > 0 {
			fmt.Fprintf(w, "    %s%s Active Locks:%s %s%d reservation(s)%s\n",
				mailColor, lockIcon, reset, text, agentMailStatus.ActiveLocks, reset)
			for _, r := range agentMailStatus.Reservations {
				lockType := "shared"
				if r.Exclusive {
					lockType = "exclusive"
				}
				fmt.Fprintf(w, "      %s• %s%s  %s%s%s (%s, %s)\n",
					subtext, text, r.PathPattern, overlay, r.AgentName, reset, lockType, r.ExpiresIn)
			}
		} else {
			fmt.Fprintf(w, "    %s%s No active file locks%s\n", overlay, lockIcon, reset)
		}

		fmt.Fprintln(w)
	}

	// Assignments section (only if requested)
	needAssignmentsDisplay := opts.showAssignments || opts.filterStatus != "" || opts.filterAgent != "" || opts.filterPane >= 0 || opts.showSummary
	if needAssignmentsDisplay && assignmentStore != nil {
		assignColor := color(t.Peach)
		beadIcon := "◆"

		fmt.Fprintf(w, "  %sAssignments%s\n", bold, reset)
		fmt.Fprintf(w, "  %s%s%s\n", surface, "─────────────────────────────────────────────────────────", reset)

		assignments := filterAssignments(assignmentStore.List(), opts.filterStatus, opts.filterAgent, opts.filterPane)

		// If --summary, skip individual listings
		if !opts.showSummary {
			if len(assignments) == 0 {
				fmt.Fprintf(w, "    %sNo active assignments%s\n", overlay, reset)
			} else {
				// Sort by pane index for consistent display
				sort.Slice(assignments, func(i, j int) bool {
					return assignments[i].Pane < assignments[j].Pane
				})

				// Build a map of pane index -> assignments for grouped display
				for _, a := range assignments {
					// Status icon and color
					var statusIcon, statusColor string
					switch a.Status {
					case assignment.StatusAssigned:
						statusIcon = "○"
						statusColor = overlay
					case assignment.StatusWorking:
						statusIcon = "▶"
						statusColor = success
					case assignment.StatusCompleted:
						statusIcon = "✓"
						statusColor = success
					case assignment.StatusFailed:
						statusIcon = "✗"
						statusColor = errorColor
					case assignment.StatusReassigned:
						statusIcon = "→"
						statusColor = subtext
					default:
						statusIcon = "?"
						statusColor = overlay
					}

					// Agent type color
					var agentColor string
					switch a.AgentType {
					case "claude":
						agentColor = claude
					case "codex":
						agentColor = codex
					case "gemini":
						agentColor = gemini
					default:
						agentColor = text
					}

					// Duration since assigned
					duration := time.Since(a.AssignedAt)
					durationStr := formatDuration(duration)

					// Truncate bead title
					title := a.BeadTitle
					if len(title) > 40 {
						title = title[:37] + "..."
					}

					fmt.Fprintf(w, "    %s%s%s %s%-8s%s %s%s %s%s%s %s(%s)%s\n",
						statusColor, statusIcon, reset,
						assignColor, beadIcon+" "+a.BeadID, reset,
						agentColor, a.AgentType, text, title, reset,
						overlay, durationStr, reset)
				}
			}
		}

		// Show stats
		stats := assignmentStore.Stats()
		if stats.Total > 0 {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "    %sStats:%s %sTotal:%s %d  %sWorking:%s %d  %sCompleted:%s %d  %sFailed:%s %d\n",
				subtext, reset,
				subtext, reset, stats.Total,
				success, reset, stats.Working,
				success, reset, stats.Completed,
				errorColor, reset, stats.Failed)
		}

		fmt.Fprintln(w)
	}

	// Quick actions hint
	fmt.Fprintf(w, "  %sQuick actions:%s\n", overlay, reset)
	fmt.Fprintf(w, "    %sntm send %s --all \"prompt\"%s  %s# Broadcast to all agents%s\n",
		subtext, session, reset, overlay, reset)
	fmt.Fprintf(w, "    %sntm view %s%s                 %s# Tile all panes%s\n",
		subtext, session, reset, overlay, reset)
	fmt.Fprintf(w, "    %sntm zoom %s <n>%s             %s# Zoom pane n%s\n",
		subtext, session, reset, overlay, reset)
	fmt.Fprintln(w)

	// Contextual suggestion
	hasBeads := false
	if assignmentStore != nil && len(assignmentStore.ListActive()) > 0 {
		hasBeads = true
	}

	busyAgents := 0
	idleAgents := 0
	for _, p := range panes {
		if p.Type == tmux.AgentUser {
			continue
		}
		st, _ := detector.Detect(p.ID)
		if st.State == status.StateWorking {
			busyAgents++
		} else if st.State == status.StateIdle {
			idleAgents++
		}
	}

	sugState := suggestions.State{
		SessionCount:   1, // At least this one exists
		CurrentSession: session,
		BusyAgents:     busyAgents,
		IdleAgents:     idleAgents,
		HasBeads:       hasBeads,
	}

	if suggestion := suggestions.SuggestNextCommand(sugState); suggestion != nil {
		output.SuccessFooter(output.Suggestion{
			Command:     suggestion.Command,
			Description: suggestion.Description,
		})
	}

	return nil
}

func runStatusWatch(w io.Writer, session string, opts statusOptions) error {
	if opts.interval <= 0 {
		opts.interval = 2 * time.Second
	}
	opts.watchMode = false

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Print("\033[?25h") // Show cursor
		cancel()
	}()

	// Hide cursor for cleaner display
	fmt.Print("\033[?25l")
	defer fmt.Print("\033[?25h")

	// ubs:ignore - ticker stopped via defer below
	ticker := time.NewTicker(opts.interval)
	defer ticker.Stop()

	firstRun := true
	for {
		if !firstRun {
			select {
			case <-ctx.Done():
				fmt.Fprintln(w, "\nWatch mode stopped.")
				return nil
			case <-ticker.C:
			}
		} else {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
		}

		if !firstRun {
			fmt.Print("\033[H\033[J")
		}

		if err := runStatusOnce(w, session, opts); err != nil {
			fmt.Fprintf(w, "Error: %v\n", err)
		}

		firstRun = false
	}
}

// updateSessionActivity updates the Agent Mail activity for a session.
// This is non-blocking and silently ignores errors.
func updateSessionActivity(sessionName string) {
	projectKey := ""
	if cfg != nil {
		projectKey = cfg.GetProjectDir(sessionName)
	}

	client := newAgentMailClient(projectKey)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = client.UpdateSessionActivity(ctx, sessionName, projectKey)
}

// fetchAgentMailStatus retrieves Agent Mail status for display in ntm status.
// Returns nil if Agent Mail is unavailable (graceful degradation).
func fetchAgentMailStatus(projectKey string) *output.AgentMailStatus {
	client := newAgentMailClient(projectKey)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Build status response
	status := &output.AgentMailStatus{
		Available: false,
		Connected: false,
		ServerURL: client.BaseURL(),
	}

	// Check if server is available
	if !client.IsAvailable() {
		return status
	}
	status.Available = true

	// Ensure project exists
	_, err := client.EnsureProject(ctx, projectKey)
	if err != nil {
		return status
	}
	status.Connected = true

	// Fetch file reservations (locks)
	reservations, err := client.ListReservations(ctx, projectKey, "", true)
	if err == nil {
		status.ActiveLocks = len(reservations)
		for _, r := range reservations {
			expiresIn := ""
			if !r.ExpiresTS.IsZero() {
				remaining := time.Until(r.ExpiresTS.Time)
				if remaining > 0 {
					expiresIn = formatDuration(remaining)
				} else {
					expiresIn = "expired"
				}
			}
			status.Reservations = append(status.Reservations, output.FileReservationInfo{
				PathPattern: r.PathPattern,
				AgentName:   r.AgentName,
				Exclusive:   r.Exclusive,
				Reason:      r.Reason,
				ExpiresIn:   expiresIn,
			})
		}
	}

	// Note: Fetching inbox requires knowing agent names, which we don't have
	// in the general status view. This would need to iterate over all project
	// agents - deferred to ntm-161 (inbox command).

	return status
}

// formatDuration formats a duration in human-readable form
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func modelNameForPane(p tmux.Pane) string {
	if p.Variant != "" {
		return p.Variant
	}
	if cfg != nil {
		switch p.Type {
		case tmux.AgentClaude:
			if cfg.Models.DefaultClaude != "" {
				return cfg.Models.DefaultClaude
			}
		case tmux.AgentCodex:
			if cfg.Models.DefaultCodex != "" {
				return cfg.Models.DefaultCodex
			}
		case tmux.AgentGemini:
			if cfg.Models.DefaultGemini != "" {
				return cfg.Models.DefaultGemini
			}
		}
	}
	switch p.Type {
	case tmux.AgentClaude:
		return "claude-sonnet-4-20250514"
	case tmux.AgentCodex:
		return "gpt-4"
	case tmux.AgentGemini:
		return "gemini-2.0-flash"
	default:
		return ""
	}
}

func paneLabel(session string, pane tmux.Pane) string {
	label := strings.TrimSpace(pane.Title)
	prefix := session + "__"
	if strings.HasPrefix(label, prefix) {
		label = strings.TrimPrefix(label, prefix)
	}
	if label == "" {
		label = fmt.Sprintf("pane %d", pane.Index)
	}
	return label
}

func renderProgressBar(percent float64, width int) string {
	if width <= 0 {
		return ""
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int(percent / 100 * float64(width))
	if filled > width {
		filled = width
	}
	empty := width - filled
	return "[" + strings.Repeat("=", filled) + strings.Repeat("-", empty) + "]"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
