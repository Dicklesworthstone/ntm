package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ScaleAction represents a single scale-up or scale-down action.
type ScaleAction = robot.ScaleAction

// ScaleResponse is the JSON response for the scale command.
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

// scaleTarget holds a parsed target count for one agent type.
type scaleTarget struct {
	agentType AgentType
	count     int
	set       bool // whether the user explicitly set this flag
}

var scaleHarnessOrder = []AgentType{
	AgentTypeClaude,
	AgentTypeCodex,
	AgentTypeGemini,
	AgentTypeAntigravity,
	AgentTypeOllama,
	AgentTypeCursor,
	AgentTypeWindsurf,
	AgentTypeAider,
	AgentTypeGrok,
	AgentTypeOmp,
	AgentTypePi,
	AgentTypeOpencode,
}

var scaleHarnessLabels = map[string]string{
	"cc":       "Claude",
	"cod":      "Codex",
	"gmi":      "Gemini",
	"agy":      "Antigravity",
	"ollama":   "Ollama",
	"cursor":   "Cursor",
	"windsurf": "Windsurf",
	"aider":    "Aider",
	"grok":     "Grok",
	"omp":      "Oh My Pi",
	"pi":       "Pi",
	"oc":       "OpenCode",
}

const scaleNoTargetsUsage = "no target counts specified (use --cc, --cod, --gmi, --agy, --ollama, --cursor, --windsurf, --aider, --grok, --omp, --pi, or --oc)"

func registerScaleKernel() {
	kernel.MustRegister(kernel.Command{
		Name:        "sessions.scale",
		Description: "Scale agents in a session to target harness counts across all 12 supported harnesses",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "ScaleInput",
			Ref:  "robot.ScaleInput",
		},
		Output: &kernel.SchemaRef{
			Name: "ScaleOutput",
			Ref:  "robot.ScaleOutput",
		},
		REST: &kernel.RESTBinding{
			Method: "POST",
			Path:   "/api/v1/sessions/{id}/scale",
		},
		Examples: []kernel.Example{
			{
				Name:        "scale-claude",
				Description: "Scale Claude agents in myproject to 5",
				Command:     "ntm scale myproject --cc=5",
			},
			{
				Name:        "scale-robot",
				Description: "Preview scaling via robot mode",
				Command:     "ntm --robot-scale=myproject --scale-cc=4 --scale-cod=2 --dry-run",
			},
		},
		SafetyLevel: kernel.SafetyCaution,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("sessions.scale", func(ctx context.Context, input any) (any, error) {
		opts, err := decodeKernelInput[robot.ScaleInput]("sessions.scale", input)
		if err != nil {
			return nil, fmt.Errorf("invalid sessions.scale input: %w", err)
		}
		out, err := executeScaleRobot(ctx, opts)
		if err != nil {
			return nil, err
		}
		if !out.Success {
			if strings.TrimSpace(out.Error) != "" {
				return nil, errors.New(out.Error)
			}
			if len(out.Errors) > 0 {
				return nil, errors.New(strings.Join(out.Errors, "; "))
			}
			return nil, errors.New("sessions.scale failed")
		}
		return out, nil
	})
}

func newScaleCmd() *cobra.Command {
	var (
		targetCC       int
		targetCod      int
		targetGmi      int
		targetAgy      int
		targetOllama   int
		targetCursor   int
		targetWindsurf int
		targetAider    int
		targetGrok     int
		targetOmp      int
		targetPi       int
		targetOc       int
		dryRun         bool
		force          bool
		setCc          bool
		setCod         bool
		setGmi         bool
		setAgy         bool
		setOllama      bool
		setCursor      bool
		setWindsurf    bool
		setAider       bool
		setGrok        bool
		setOmp         bool
		setPi          bool
		setOc          bool
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
  ntm scale myproject --cc=10 --dry-run       # Preview changes
  ntm scale myproject --cc=1 --force          # Skip confirmation on scale-down
  ntm scale myproject --cod=0                 # Remove all Codex agents`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setCc = cmd.Flags().Changed("cc")
			setCod = cmd.Flags().Changed("cod")
			setGmi = cmd.Flags().Changed("gmi")
			setAgy = cmd.Flags().Changed("agy")
			setOllama = cmd.Flags().Changed("ollama")
			setCursor = cmd.Flags().Changed("cursor")
			setWindsurf = cmd.Flags().Changed("windsurf")
			setAider = cmd.Flags().Changed("aider")
			setGrok = cmd.Flags().Changed("grok")
			setOmp = cmd.Flags().Changed("omp")
			setPi = cmd.Flags().Changed("pi")
			setOc = cmd.Flags().Changed("oc")
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]

			targets := []scaleTarget{
				{agentType: AgentTypeClaude, count: targetCC, set: setCc},
				{agentType: AgentTypeCodex, count: targetCod, set: setCod},
				{agentType: AgentTypeGemini, count: targetGmi, set: setGmi},
				{agentType: AgentTypeAntigravity, count: targetAgy, set: setAgy},
				{agentType: AgentTypeOllama, count: targetOllama, set: setOllama},
				{agentType: AgentTypeCursor, count: targetCursor, set: setCursor},
				{agentType: AgentTypeWindsurf, count: targetWindsurf, set: setWindsurf},
				{agentType: AgentTypeAider, count: targetAider, set: setAider},
				{agentType: AgentTypeGrok, count: targetGrok, set: setGrok},
				{agentType: AgentTypeOmp, count: targetOmp, set: setOmp},
				{agentType: AgentTypePi, count: targetPi, set: setPi},
				{agentType: AgentTypeOpencode, count: targetOc, set: setOc},
			}

			return runScale(cmd.Context(), session, targets, dryRun, force)
		},
	}

	cmd.Flags().IntVar(&targetCC, "cc", 0, "Target Claude agent count")
	cmd.Flags().IntVar(&targetCod, "cod", 0, "Target Codex agent count")
	cmd.Flags().IntVar(&targetGmi, "gmi", 0, "Target Gemini agent count")
	cmd.Flags().IntVar(&targetAgy, "agy", 0, "Target Antigravity agent count")
	cmd.Flags().IntVar(&targetOllama, "ollama", 0, "Target Ollama agent count")
	cmd.Flags().IntVar(&targetCursor, "cursor", 0, "Target Cursor agent count")
	cmd.Flags().IntVar(&targetWindsurf, "windsurf", 0, "Target Windsurf agent count")
	cmd.Flags().IntVar(&targetAider, "aider", 0, "Target Aider agent count")
	cmd.Flags().IntVar(&targetGrok, "grok", 0, "Target Grok Build agent count")
	cmd.Flags().IntVar(&targetOmp, "omp", 0, "Target Oh My Pi (omp) agent count")
	cmd.Flags().IntVar(&targetPi, "pi", 0, "Target Pi (pi) agent count")
	cmd.Flags().IntVar(&targetOc, "oc", 0, "Target OpenCode (oc) agent count")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without executing")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation on scale-down")

	return cmd
}

type scalePlan struct {
	before           map[string]int
	after            map[string]int
	scaleUpActions   []ScaleAction
	scaleDownActions []ScaleAction
	allActions       []ScaleAction
}

func planScaleSession(ctx context.Context, session string, targets []scaleTarget) (*scalePlan, error) {
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("getting panes: %w", err)
	}

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

	before := make(map[string]int, len(scaleHarnessOrder))
	after := make(map[string]int, len(scaleHarnessOrder))
	for _, harness := range scaleHarnessOrder {
		key := string(harness)
		before[key] = currentCounts[key]
		after[key] = currentCounts[key]
	}

	var scaleUpActions []ScaleAction
	var scaleDownActions []ScaleAction

	for _, t := range targets {
		if !t.set {
			continue
		}
		typeStr := string(t.agentType)
		current := currentCounts[typeStr]
		delta := t.count - current

		if delta > 0 {
			scaleUpActions = append(scaleUpActions, ScaleAction{
				ActionType: "spawn",
				AgentType:  typeStr,
				Count:      delta,
				Agents:     []string{},
			})
			after[typeStr] = t.count
		} else if delta < 0 {
			killCount := -delta
			agentPanes := append([]tmux.Pane(nil), panesByType[typeStr]...)
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
	}

	allActions := make([]ScaleAction, 0, len(scaleUpActions)+len(scaleDownActions))
	allActions = append(allActions, scaleUpActions...)
	allActions = append(allActions, scaleDownActions...)

	return &scalePlan{
		before:           before,
		after:            after,
		scaleUpActions:   scaleUpActions,
		scaleDownActions: scaleDownActions,
		allActions:       allActions,
	}, nil
}

func applyScalePlan(ctx context.Context, session string, plan *scalePlan, emitHuman bool) (map[string]int, []string, error) {
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

	finalCounts := make(map[string]int, len(plan.before))
	for agentType, count := range plan.before {
		finalCounts[agentType] = count
	}

	for _, action := range plan.scaleUpActions {
		if err := ctx.Err(); err != nil {
			recordOperationError("scale-up canceled", err)
			break
		}
		slog.Default().Debug("scale: spawn", "session", session, "agent_type", action.AgentType, "count", action.Count)
		specs := AgentSpecs{
			{Type: AgentType(action.AgentType), Count: action.Count},
		}
		var outcome AddOutcome
		opts := AddOptions{
			Session: session,
			Agents:  specs,
			Outcome: &outcome,
		}
		if action.AgentType == string(AgentTypePi) {
			loadedPlugins, _ := plugins.LoadAgentPluginsWithDefaults(filepath.Join(selectedConfigDir(), "agents"))
			pluginMap := make(map[string]plugins.AgentPlugin, len(loadedPlugins))
			for _, p := range loadedPlugins {
				pluginMap[p.Name] = p
				if p.Alias != "" {
					pluginMap[p.Alias] = p
				}
			}
			opts.PluginMap = pluginMap
		}
		if err := executeAdd(ctx, opts, false); err != nil {
			stop := recordOperationError(fmt.Sprintf("spawn %s", action.AgentType), err)
			if emitHuman {
				fmt.Printf("  ERROR spawning %s agents: %v\n", action.AgentType, err)
			}
			if stop {
				break
			}
		} else {
			finalCounts[action.AgentType] += outcome.Added
			if emitHuman {
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
			if outcome.Added != action.Count && len(outcome.SeatSkips) == 0 {
				recordOperationError(fmt.Sprintf("spawn %s", action.AgentType),
					fmt.Errorf("only added %d/%d agents", outcome.Added, action.Count))
			}
		}
	}

	// Scale-up protects existing capacity only if every requested replacement
	// actually launched. Preserve the old agents after any launch failure or
	// skipped seat; successful additions and any panes left by a failed launch
	// remain available for inspection. The final census reports their state.
	scaleUpIncomplete := len(operationErrors) > 0
	if scaleUpIncomplete && len(plan.scaleDownActions) > 0 && cancellationErr == nil {
		message := "scale-down skipped because scale-up did not reach every requested target; existing agents were preserved"
		operationErrors = append(operationErrors, message)
		if emitHuman {
			fmt.Printf("  %s\n", message)
		}
	}

	for _, action := range plan.scaleDownActions {
		if scaleUpIncomplete || cancellationErr != nil {
			break
		}
		if err := ctx.Err(); err != nil {
			recordOperationError("scale-down canceled", err)
			break
		}
		slog.Default().Debug("scale: kill", "session", session, "agent_type", action.AgentType, "count", action.Count)

		currentPanes, err := tmux.GetPanesContext(ctx, session)
		if err != nil {
			if recordOperationError("refetch panes for kill", err) {
				break
			}
			continue
		}

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
				if emitHuman {
					fmt.Printf("  ERROR killing %s: %v\n", p.Title, err)
				}
				if stop {
					break
				}
			} else {
				killed++
				finalCounts[action.AgentType]--
				if emitHuman {
					fmt.Printf("  Terminated %s\n", p.Title)
				}
			}
		}
		if killed < action.Count && cancellationErr == nil {
			operationErrors = append(operationErrors, fmt.Sprintf("only killed %d/%d %s agents", killed, action.Count, action.AgentType))
		}
	}

	if cancellationErr == nil {
		if err := tmux.ApplyTiledLayoutContext(ctx, session); err != nil {
			recordOperationError("apply tiled layout", err)
		}
	}

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

	return finalCounts, operationErrors, cancellationErr
}

func normalizeScaleTargetKey(raw string) (AgentType, bool) {
	switch tmux.AgentType(strings.TrimSpace(raw)).Canonical() {
	case tmux.AgentClaude:
		return AgentTypeClaude, true
	case tmux.AgentCodex:
		return AgentTypeCodex, true
	case tmux.AgentGemini:
		return AgentTypeGemini, true
	case tmux.AgentAntigravity:
		return AgentTypeAntigravity, true
	case tmux.AgentOllama:
		return AgentTypeOllama, true
	case tmux.AgentCursor:
		return AgentTypeCursor, true
	case tmux.AgentWindsurf:
		return AgentTypeWindsurf, true
	case tmux.AgentAider:
		return AgentTypeAider, true
	case tmux.AgentGrok:
		return AgentTypeGrok, true
	case tmux.AgentOmp:
		return AgentTypeOmp, true
	case tmux.AgentPi:
		return AgentTypePi, true
	case tmux.AgentOpencode:
		return AgentTypeOpencode, true
	default:
		return "", false
	}
}

func parseScaleInputTargets(targetMap map[string]int) ([]scaleTarget, error) {
	if len(targetMap) == 0 {
		return nil, fmt.Errorf("invalid scale request: %s", scaleNoTargetsUsage)
	}
	parsedCounts := make(map[AgentType]int, len(targetMap))
	keys := make([]string, 0, len(targetMap))
	for k := range targetMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		agentType, ok := normalizeScaleTargetKey(key)
		if !ok {
			return nil, fmt.Errorf("invalid scale target harness %q (supported: cc, cod, gmi, agy, ollama, cursor, windsurf, aider, grok, omp, oc)", key)
		}
		count := targetMap[key]
		if count < 0 {
			return nil, fmt.Errorf("invalid target count for %s: must be non-negative, got %d", agentType, count)
		}
		if prev, seen := parsedCounts[agentType]; seen && prev != count {
			return nil, fmt.Errorf("invalid conflicting target counts for %s (%d vs %d)", agentType, prev, count)
		}
		parsedCounts[agentType] = count
	}

	targets := make([]scaleTarget, 0, len(scaleHarnessOrder))
	for _, harness := range scaleHarnessOrder {
		count, set := parsedCounts[harness]
		targets = append(targets, scaleTarget{
			agentType: harness,
			count:     count,
			set:       set,
		})
	}
	return targets, nil
}

func newScaleRobotErrorOutput(session string, dryRun bool, err error, code, hint string) *robot.ScaleOutput {
	return &robot.ScaleOutput{
		RobotResponse: robot.NewErrorResponse(err, code, hint),
		SchemaID:      robot.ScaleSchemaID,
		SchemaVersion: robot.ScaleSchemaVersion,
		Session:       strings.TrimSpace(session),
		Before:        map[string]int{},
		After:         map[string]int{},
		Actions:       []ScaleAction{},
		DryRun:        dryRun,
		Errors:        []string{err.Error()},
	}
}

func executeScaleRobot(ctx context.Context, input robot.ScaleInput) (*robot.ScaleOutput, error) {
	ctx = withSuppressedCLIIdentityOutput(ctx)
	if err := ctx.Err(); err != nil {
		return newScaleRobotErrorOutput(input.Session, input.DryRun, fmt.Errorf("scale canceled: %w", err), robot.ErrCodeTimeout, "Retry --robot-scale with an active context"), nil
	}

	session := strings.TrimSpace(input.Session)
	if session == "" {
		return newScaleRobotErrorOutput("", input.DryRun, errors.New("session is required"), robot.ErrCodeInvalidFlag, "Pass --robot-scale=SESSION"), nil
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return newScaleRobotErrorOutput(session, input.DryRun, fmt.Errorf("invalid session name: %w", err), robot.ErrCodeInvalidFlag, "Provide a valid tmux session name"), nil
	}

	targets, err := parseScaleInputTargets(input.Targets)
	if err != nil {
		return newScaleRobotErrorOutput(session, input.DryRun, err, robot.ErrCodeInvalidFlag, "Specify one or more non-negative --scale-<harness>=N targets"), nil
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return newScaleRobotErrorOutput(session, input.DryRun, err, robot.ErrCodeInternalError, "Install tmux before running scale"), nil
	}

	resolvedSession, err := resolveScaleSessionWithMode(ctx, session, false)
	if err != nil {
		code := robot.ErrCodeInternalError
		lowerErr := strings.ToLower(err.Error())
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = robot.ErrCodeTimeout
		} else if strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "does not exist") || strings.Contains(lowerErr, "no session") {
			code = robot.ErrCodeSessionNotFound
		} else if strings.Contains(lowerErr, "invalid") || strings.Contains(lowerErr, "ambiguous") {
			code = robot.ErrCodeInvalidFlag
		}
		return newScaleRobotErrorOutput(session, input.DryRun, err, code, "Use an exact session name from 'ntm --robot-status'"), nil
	}
	session = resolvedSession

	exists, err := tmux.SessionExistsContext(ctx, session)
	if err != nil {
		code := robot.ErrCodeInternalError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = robot.ErrCodeTimeout
		}
		return newScaleRobotErrorOutput(session, input.DryRun, fmt.Errorf("checking session %q before scale: %w", session, err), code, "Check tmux server health"), nil
	}
	if !exists {
		return newScaleRobotErrorOutput(session, input.DryRun, fmt.Errorf("session '%s' does not exist", session), robot.ErrCodeSessionNotFound, "Use 'ntm --robot-status' to list active sessions"), nil
	}

	plan, err := planScaleSession(ctx, session, targets)
	if err != nil {
		code := robot.ErrCodeInternalError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = robot.ErrCodeTimeout
		}
		return newScaleRobotErrorOutput(session, input.DryRun, err, code, "Check tmux session health"), nil
	}
	if err := ctx.Err(); err != nil {
		return newScaleRobotErrorOutput(session, input.DryRun, fmt.Errorf("scale canceled before execution: %w", err), robot.ErrCodeTimeout, "Retry --robot-scale with an active context"), nil
	}

	if len(plan.allActions) == 0 {
		return &robot.ScaleOutput{
			RobotResponse: robot.NewRobotResponse(true),
			SchemaID:      robot.ScaleSchemaID,
			SchemaVersion: robot.ScaleSchemaVersion,
			Session:       session,
			Before:        plan.before,
			After:         plan.after,
			Actions:       []ScaleAction{},
			DryRun:        input.DryRun,
			AgentHints: &robot.AgentHints{
				Summary: fmt.Sprintf("Session %s is already at target counts (0 actions)", session),
			},
		}, nil
	}

	if input.DryRun {
		return &robot.ScaleOutput{
			RobotResponse: robot.NewRobotResponse(true),
			SchemaID:      robot.ScaleSchemaID,
			SchemaVersion: robot.ScaleSchemaVersion,
			Session:       session,
			Before:        plan.before,
			After:         plan.after,
			Actions:       plan.allActions,
			DryRun:        true,
			AgentHints: &robot.AgentHints{
				Summary: fmt.Sprintf("Dry-run scale plan for %s: %d action(s) planned", session, len(plan.allActions)),
			},
		}, nil
	}

	finalCounts, operationErrors, cancellationErr := applyScalePlan(ctx, session, plan, false)
	success := len(operationErrors) == 0
	out := &robot.ScaleOutput{
		RobotResponse: robot.NewRobotResponse(success),
		SchemaID:      robot.ScaleSchemaID,
		SchemaVersion: robot.ScaleSchemaVersion,
		Session:       session,
		Before:        plan.before,
		After:         finalCounts,
		Actions:       plan.allActions,
	}
	if !success {
		out.Errors = operationErrors
		code := robot.ErrCodeInternalError
		if cancellationErr != nil {
			code = robot.ErrCodeTimeout
		}
		out.RobotResponse = robot.NewErrorResponse(errors.New(strings.Join(operationErrors, "; ")), code, "Inspect errors[] and session pane health before retrying scale")
		out.AgentHints = &robot.AgentHints{
			Summary:  fmt.Sprintf("Scale failed for %s with %d error(s)", session, len(operationErrors)),
			Warnings: operationErrors,
		}
		return out, nil
	}

	out.AgentHints = &robot.AgentHints{
		Summary: fmt.Sprintf("Scaled %s (%d action(s) applied)", session, len(plan.allActions)),
	}
	return out, nil
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

	anySet := false
	for _, t := range targets {
		if t.set {
			anySet = true
			break
		}
	}
	if !anySet {
		return outputError(errors.New(scaleNoTargetsUsage))
	}

	for _, t := range targets {
		if t.set && t.count < 0 {
			return outputError(fmt.Errorf("target count for %s must be non-negative, got %d", t.agentType, t.count))
		}
	}

	plan, err := planScaleSession(ctx, session, targets)
	if err != nil {
		return outputError(err)
	}
	if err := ctx.Err(); err != nil {
		return outputError(fmt.Errorf("scale canceled before execution: %w", err))
	}

	if len(plan.allActions) == 0 {
		resp := ScaleResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             session,
			Before:              plan.before,
			After:               plan.after,
			Actions:             []ScaleAction{},
			Success:             true,
		}
		if IsJSONOutput() {
			return output.PrintJSON(resp)
		}
		fmt.Println("Already at target counts. No changes needed.")
		return nil
	}

	if dryRun {
		resp := ScaleResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             session,
			Before:              plan.before,
			After:               plan.after,
			Actions:             plan.allActions,
			Success:             true,
			DryRun:              true,
		}
		if IsJSONOutput() {
			return output.PrintJSON(resp)
		}
		printScalePlan(session, plan.before, plan.after, plan.allActions)
		fmt.Println("\n(dry-run: no changes made)")
		return nil
	}

	if len(plan.scaleDownActions) > 0 && !force && !IsJSONOutput() {
		printScalePlan(session, plan.before, plan.after, plan.allActions)
		fmt.Println()

		terminateCount := 0
		for _, action := range plan.scaleDownActions {
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
		printScalePlan(session, plan.before, plan.after, plan.allActions)
		fmt.Println()
	}

	finalCounts, operationErrors, cancellationErr := applyScalePlan(ctx, session, plan, !IsJSONOutput())
	success := len(operationErrors) == 0

	resp := ScaleResponse{
		TimestampedResponse: output.NewTimestamped(),
		Session:             session,
		Before:              plan.before,
		After:               finalCounts,
		Actions:             plan.allActions,
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
		"before", plan.before,
		"after", finalCounts,
		"errors", len(operationErrors))

	if IsJSONOutput() {
		if success {
			return output.PrintJSON(resp)
		}
		return emitJSONFailureEnvelope(resp)
	}

	parts := make([]string, 0, len(scaleHarnessOrder))
	for _, harness := range scaleHarnessOrder {
		key := string(harness)
		parts = append(parts, fmt.Sprintf("%s=%d", key, finalCounts[key]))
	}
	fmt.Printf("\nScaling complete. Current state: %s\n", strings.Join(parts, ", "))

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
	return resolveScaleSessionWithMode(ctx, session, !IsJSONOutput())
}

func resolveScaleSessionWithMode(ctx context.Context, session string, allowPrefix bool) (string, error) {
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
	resolved, _, err := resolveExplicitSessionName(session, sessions, allowPrefix)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// scaleAgentTypeLabel maps a tmux.AgentType to the short string label used in scale.
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
	case tmux.AgentOllama:
		return "ollama"
	case tmux.AgentCursor:
		return "cursor"
	case tmux.AgentWindsurf:
		return "windsurf"
	case tmux.AgentAider:
		return "aider"
	case tmux.AgentGrok:
		return "grok"
	case tmux.AgentOmp:
		return "omp"
	case tmux.AgentPi:
		return "pi"
	case tmux.AgentOpencode:
		return "oc"
	case tmux.AgentUser:
		return "user"
	default:
		return "unknown"
	}
}

// printScalePlan displays a human-readable summary of planned scale actions.
func printScalePlan(session string, before, after map[string]int, actions []ScaleAction) {
	fmt.Printf("Scale plan for session '%s':\n\n", session)

	for _, harness := range scaleHarnessOrder {
		t := string(harness)
		b := before[t]
		a := after[t]
		if b != a {
			delta := a - b
			sign := "+"
			if delta < 0 {
				sign = ""
			}
			fmt.Printf("  %s (%s): %d → %d (%s%d)\n", scaleHarnessLabels[t], t, b, a, sign, delta)
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
