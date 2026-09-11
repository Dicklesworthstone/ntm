package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ScaleAction represents a single scale-up or scale-down action
type ScaleAction struct {
	ActionType string   `json:"type"`       // "spawn" or "kill"
	AgentType  string   `json:"agent_type"` // "cc", "cod", "gmi", etc.
	Count      int      `json:"count"`
	Agents     []string `json:"agents"` // pane titles affected
}

// ScaleResponse is the JSON response for the scale command
type ScaleResponse struct {
	output.TimestampedResponse
	Session   string         `json:"session"`
	Before    map[string]int `json:"before"`
	After     map[string]int `json:"after"`
	Actions   []ScaleAction  `json:"actions"`
	Success   bool           `json:"success"`
	DryRun    bool           `json:"dry_run,omitempty"`
	Errors    []string       `json:"errors,omitempty"`
	ErrorCode string         `json:"error_code,omitempty"`
}

// scaleTarget holds a parsed target count for one agent type
type scaleTarget struct {
	agentType AgentType
	count     int
	set       bool // whether the user explicitly set this flag
}

func newScaleCmd() *cobra.Command {
	var (
		targetCC  int
		targetCod int
		targetGmi int
		targetAgy int
		dryRun    bool
		force     bool
		setCc     bool
		setCod    bool
		setGmi    bool
		setAgy    bool
	)

	cmd := &cobra.Command{
		Use:   "scale <session>",
		Short: "Scale agents to target counts",
		Long: `Scale agents in a session to exact target counts.

Computes the delta between current and target counts, then spawns or kills
agents as needed. Scale-up runs before scale-down to avoid losing agents
that might still be needed.

Examples:
  ntm scale myproject --cc=5                  # Scale Claude to 5
  ntm scale myproject --cc=3 --cod=2 --gmi=1  # Scale all types
  ntm scale myproject --cc=10 --dry-run        # Preview changes
  ntm scale myproject --cc=1 --force           # Skip confirmation on scale-down
  ntm scale myproject --cod=0                  # Remove all Codex agents`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setCc = cmd.Flags().Changed("cc")
			setCod = cmd.Flags().Changed("cod")
			setGmi = cmd.Flags().Changed("gmi")
			setAgy = cmd.Flags().Changed("agy")
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]

			targets := []scaleTarget{
				{agentType: AgentTypeClaude, count: targetCC, set: setCc},
				{agentType: AgentTypeCodex, count: targetCod, set: setCod},
				{agentType: AgentTypeGemini, count: targetGmi, set: setGmi},
				{agentType: AgentTypeAntigravity, count: targetAgy, set: setAgy},
			}

			return runScale(cmd.Context(), session, targets, dryRun, force)
		},
	}

	cmd.Flags().IntVar(&targetCC, "cc", 0, "Target Claude agent count")
	cmd.Flags().IntVar(&targetCod, "cod", 0, "Target Codex agent count")
	cmd.Flags().IntVar(&targetGmi, "gmi", 0, "Target Gemini agent count")
	cmd.Flags().IntVar(&targetAgy, "agy", 0, "Target Antigravity agent count")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without executing")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation on scale-down")

	return cmd
}

func runScale(ctx context.Context, session string, targets []scaleTarget, dryRun, force bool) error {
	outputError := func(err error) error {
		if IsJSONOutput() {
			code := robot.ErrCodeInternalError
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = robot.ErrCodeTimeout
			}
			return emitJSONFailureEnvelope(ScaleResponse{
				TimestampedResponse: output.NewTimestamped(),
				Session:             strings.TrimSpace(session),
				Before:              map[string]int{},
				After:               map[string]int{},
				Actions:             []ScaleAction{},
				Success:             false,
				Errors:              []string{err.Error()},
				ErrorCode:           code,
			})
		}
		return err
	}
	if ctx == nil {
		return outputError(errors.New("scale requires a command context"))
	}
	if err := ctx.Err(); err != nil {
		return outputError(fmt.Errorf("scale canceled: %w", err))
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return outputError(err)
	}

	// Resolve session name consistently across human and JSON modes.
	resolvedSession, err := resolveScaleSession(ctx, session)
	if err != nil {
		return outputError(err)
	}
	session = resolvedSession

	exists, err := tmux.SessionExistsContext(ctx, session)
	if err != nil {
		return outputError(fmt.Errorf("checking session %q before scale: %w", session, err))
	}
	if !exists {
		return outputError(fmt.Errorf("session '%s' does not exist", session))
	}

	// Check at least one target was set
	anySet := false
	for _, t := range targets {
		if t.set {
			anySet = true
			break
		}
	}
	if !anySet {
		return outputError(fmt.Errorf("no target counts specified (use --cc, --cod, --gmi, or --agy)"))
	}

	// Validate target counts
	for _, t := range targets {
		if t.set && t.count < 0 {
			return outputError(fmt.Errorf("target count for %s must be non-negative, got %d", t.agentType, t.count))
		}
	}

	// Get current panes
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return outputError(fmt.Errorf("getting panes: %w", err))
	}

	// Count current agents by type and collect pane info
	currentCounts := make(map[string]int)
	panesByType := make(map[string][]tmux.Pane)
	for _, p := range panes {
		typeStr := scaleAgentTypeLabel(p.Type)
		if typeStr == "user" || typeStr == "unknown" {
			continue
		}
		currentCounts[typeStr]++
		panesByType[typeStr] = append(panesByType[typeStr], p)
	}

	// Build before snapshot
	before := map[string]int{
		"cc":  currentCounts["cc"],
		"cod": currentCounts["cod"],
		"gmi": currentCounts["gmi"],
		"agy": currentCounts["agy"],
	}

	// Calculate deltas and build actions
	var scaleUpActions []ScaleAction
	var scaleDownActions []ScaleAction
	after := make(map[string]int)
	for k, v := range before {
		after[k] = v
	}

	for _, t := range targets {
		if !t.set {
			continue
		}
		typeStr := string(t.agentType)
		current := currentCounts[typeStr]
		delta := t.count - current

		if delta > 0 {
			// Scale up
			scaleUpActions = append(scaleUpActions, ScaleAction{
				ActionType: "spawn",
				AgentType:  typeStr,
				Count:      delta,
				Agents:     []string{},
			})
			after[typeStr] = t.count
		} else if delta < 0 {
			// Scale down - select agents to kill (highest index first)
			killCount := -delta
			agentPanes := panesByType[typeStr]

			// Sort by NTMIndex descending so we kill highest indices first
			sort.Slice(agentPanes, func(i, j int) bool {
				return agentPanes[i].NTMIndex > agentPanes[j].NTMIndex
			})

			toKill := killCount
			if toKill > len(agentPanes) {
				toKill = len(agentPanes)
			}

			killTitles := make([]string, 0, toKill)
			for i := 0; i < toKill; i++ {
				killTitles = append(killTitles, agentPanes[i].Title)
			}

			scaleDownActions = append(scaleDownActions, ScaleAction{
				ActionType: "kill",
				AgentType:  typeStr,
				Count:      toKill,
				Agents:     killTitles,
			})
			after[typeStr] = current - toKill
		}
		// delta == 0 means no change needed
	}

	allActions := append(scaleUpActions, scaleDownActions...)
	if err := ctx.Err(); err != nil {
		return outputError(fmt.Errorf("scale canceled before execution: %w", err))
	}

	if len(allActions) == 0 {
		resp := ScaleResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             session,
			Before:              before,
			After:               after,
			Actions:             []ScaleAction{},
			Success:             true,
		}
		if IsJSONOutput() {
			return output.PrintJSON(resp)
		}
		fmt.Println("Already at target counts. No changes needed.")
		return nil
	}

	// Preview mode
	if dryRun {
		resp := ScaleResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             session,
			Before:              before,
			After:               after,
			Actions:             allActions,
			Success:             true,
			DryRun:              true,
		}
		if IsJSONOutput() {
			return output.PrintJSON(resp)
		}
		printScalePlan(session, before, after, allActions)
		fmt.Println("\n(dry-run: no changes made)")
		return nil
	}

	// Confirm scale-down if not forced
	if len(scaleDownActions) > 0 && !force && !IsJSONOutput() {
		printScalePlan(session, before, after, allActions)
		fmt.Println()

		// Count agents to be terminated
		terminateCount := 0
		for _, action := range scaleDownActions {
			terminateCount += action.Count
		}

		var confirmed bool
		err := huh.NewConfirm().
			Title(fmt.Sprintf("Scale down %d agents?", terminateCount)).
			Description("Scaling down will terminate agents and lose their context.").
			Affirmative("Yes, proceed").
			Negative("Cancel").
			Value(&confirmed).
			WithTheme(theme.HuhDestructiveTheme()).
			Run()
		if err != nil {
			return fmt.Errorf("confirmation dialog: %w", err)
		}
		if !confirmed {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if !IsJSONOutput() {
		printScalePlan(session, before, after, allActions)
		fmt.Println()
	}

	// Execute scale-up first (spawn new agents before killing old ones)
	var operationErrors []string
	var cancellationErr error
	recordOperationError := func(message string, err error) bool {
		operationErrors = append(operationErrors, fmt.Sprintf("%s: %v", message, err))
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			cancellationErr = err
			return true
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			cancellationErr = ctxErr
			return true
		}
		return false
	}
	finalCounts := make(map[string]int, len(before))
	for agentType, count := range before {
		finalCounts[agentType] = count
	}

	for _, action := range scaleUpActions {
		if err := ctx.Err(); err != nil {
			recordOperationError("scale-up canceled", err)
			break
		}
		slog.Default().Debug("scale: spawn", "session", session, "agent_type", action.AgentType, "count", action.Count)
		specs := AgentSpecs{
			{Type: AgentType(action.AgentType), Count: action.Count},
		}
		// executeAdd runs with emitResult=false here, so scale sees neither
		// its JSON response nor its warnings. Read the outcome instead of
		// assuming the requested count landed: CAAM seat selection can refuse
		// a pool and launch fewer panes without failing the add (ntm#319).
		var outcome AddOutcome
		opts := AddOptions{
			Session: session,
			Agents:  specs,
			Outcome: &outcome,
		}
		if err := executeAdd(ctx, opts, false); err != nil {
			stop := recordOperationError(fmt.Sprintf("spawn %s", action.AgentType), err)
			if !IsJSONOutput() {
				fmt.Printf("  ERROR spawning %s agents: %v\n", action.AgentType, err)
			}
			if stop {
				break
			}
		} else {
			finalCounts[action.AgentType] += outcome.Added
			if !IsJSONOutput() {
				fmt.Printf("  Spawned %d %s agent(s)\n", outcome.Added, action.AgentType)
				for _, skip := range outcome.SeatSkips {
					output.PrintWarningf("%s", caamSeatSkipMessage(AgentType(skip.AgentType), skip.Reason))
				}
			}
			for _, skip := range outcome.SeatSkips {
				recordOperationError(
					fmt.Sprintf("spawn %s", action.AgentType),
					fmt.Errorf("seat selection skipped a pane: %s", skip.Reason),
				)
			}
		}
	}

	// Execute scale-down (kill agents)
	for _, action := range scaleDownActions {
		if cancellationErr != nil {
			break
		}
		if err := ctx.Err(); err != nil {
			recordOperationError("scale-down canceled", err)
			break
		}
		slog.Default().Debug("scale: kill", "session", session, "agent_type", action.AgentType, "count", action.Count)

		// Re-fetch panes to get current state after scale-up
		currentPanes, err := tmux.GetPanesContext(ctx, session)
		if err != nil {
			if recordOperationError("refetch panes for kill", err) {
				break
			}
			continue
		}

		// Find panes matching the agent type, sorted by NTMIndex descending
		var matchingPanes []tmux.Pane
		for _, p := range currentPanes {
			if scaleAgentTypeLabel(p.Type) == action.AgentType {
				matchingPanes = append(matchingPanes, p)
			}
		}
		sort.Slice(matchingPanes, func(i, j int) bool {
			return matchingPanes[i].NTMIndex > matchingPanes[j].NTMIndex
		})

		killed := 0
		for i := 0; i < action.Count && i < len(matchingPanes); i++ {
			if err := ctx.Err(); err != nil {
				recordOperationError("scale-down canceled", err)
				break
			}
			p := matchingPanes[i]
			slog.Default().Debug("scale: kill pane", "session", session, "agent_type", action.AgentType, "pane_id", p.ID, "title", p.Title)
			if err := tmux.KillPaneContext(ctx, p.ID); err != nil {
				stop := recordOperationError(fmt.Sprintf("kill pane %s", p.Title), err)
				if !IsJSONOutput() {
					fmt.Printf("  ERROR killing %s: %v\n", p.Title, err)
				}
				if stop {
					break
				}
			} else {
				killed++
				finalCounts[action.AgentType]--
				if !IsJSONOutput() {
					fmt.Printf("  Terminated %s\n", p.Title)
				}
			}
		}
		// Update action agents list with actual titles killed
		if killed < action.Count && cancellationErr == nil {
			operationErrors = append(operationErrors, fmt.Sprintf("only killed %d/%d %s agents", killed, action.Count, action.AgentType))
		}
	}

	// Re-tile layout after changes
	if cancellationErr == nil {
		if err := tmux.ApplyTiledLayoutContext(ctx, session); err != nil {
			recordOperationError("apply tiled layout", err)
		}
	}

	// Re-fetch final state
	if cancellationErr == nil {
		finalPanes, fetchErr := tmux.GetPanesContext(ctx, session)
		if fetchErr != nil {
			recordOperationError("verify final pane state", fetchErr)
			slog.Debug("scale: could not verify final pane state", "session", session, "error", fetchErr)
		} else {
			for agentType := range finalCounts {
				finalCounts[agentType] = 0
			}
			for _, p := range finalPanes {
				typeStr := scaleAgentTypeLabel(p.Type)
				if _, ok := finalCounts[typeStr]; ok {
					finalCounts[typeStr]++
				}
			}
		}
	}
	success := len(operationErrors) == 0

	resp := ScaleResponse{
		TimestampedResponse: output.NewTimestamped(),
		Session:             session,
		Before:              before,
		After:               finalCounts,
		Actions:             allActions,
		Success:             success,
	}
	if len(operationErrors) > 0 {
		resp.Errors = operationErrors
		resp.ErrorCode = robot.ErrCodeInternalError
		if cancellationErr != nil {
			resp.ErrorCode = robot.ErrCodeTimeout
		}
	}

	slog.Default().Debug("scale: complete",
		"session", session,
		"success", success,
		"before", before,
		"after", finalCounts,
		"errors", len(operationErrors))

	if IsJSONOutput() {
		if success {
			return output.PrintJSON(resp)
		}
		return emitJSONFailureEnvelope(resp)
	}

	fmt.Printf("\nScaling complete. Current state: cc=%d, cod=%d, gmi=%d, agy=%d\n",
		finalCounts["cc"], finalCounts["cod"], finalCounts["gmi"], finalCounts["agy"])

	if len(operationErrors) > 0 {
		fmt.Println("\nErrors encountered:")
		for _, e := range operationErrors {
			fmt.Printf("  - %s\n", e)
		}
		if cancellationErr != nil {
			return fmt.Errorf("scale canceled: %w", cancellationErr)
		}
		return fmt.Errorf("scale failed: %s", strings.Join(operationErrors, "; "))
	}

	return nil
}

func resolveScaleSession(ctx context.Context, session string) (string, error) {
	if ctx == nil {
		return "", errors.New("scale session resolution requires a context")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	session = strings.TrimSpace(session)
	if session == "" {
		return "", fmt.Errorf("session is required")
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}
	sessions, err := tmux.ListSessionsContext(ctx)
	if err != nil {
		return "", err
	}
	resolved, _, err := resolveExplicitSessionName(session, sessions, !IsJSONOutput())
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// scaleAgentTypeLabel maps a tmux.AgentType to the short string label used in scale
func scaleAgentTypeLabel(t tmux.AgentType) string {
	switch tmux.AgentType(t).Canonical() {
	case tmux.AgentClaude:
		return "cc"
	case tmux.AgentCodex:
		return "cod"
	case tmux.AgentGemini:
		return "gmi"
	case tmux.AgentAntigravity:
		return "agy"
	case tmux.AgentUser:
		return "user"
	default:
		return "unknown"
	}
}

// printScalePlan displays a human-readable summary of planned scale actions
func printScalePlan(session string, before, after map[string]int, actions []ScaleAction) {
	fmt.Printf("Scale plan for session '%s':\n\n", session)

	types := []string{"cc", "cod", "gmi", "agy"}
	labels := map[string]string{"cc": "Claude", "cod": "Codex", "gmi": "Gemini", "agy": "Antigravity"}

	for _, t := range types {
		b := before[t]
		a := after[t]
		if b != a {
			delta := a - b
			sign := "+"
			if delta < 0 {
				sign = ""
			}
			fmt.Printf("  %s (%s): %d → %d (%s%d)\n", labels[t], t, b, a, sign, delta)
		}
	}

	if len(actions) > 0 {
		fmt.Println("\nActions:")
		for _, a := range actions {
			if a.ActionType == "spawn" {
				fmt.Printf("  + Spawn %d %s agent(s)\n", a.Count, a.AgentType)
			} else {
				fmt.Printf("  - Kill %d %s agent(s)\n", a.Count, a.AgentType)
				for _, title := range a.Agents {
					fmt.Printf("      %s\n", title)
				}
			}
		}
	}
}
