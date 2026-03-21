package robot

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/recovery"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Pre-compiled prompt patterns for isAgentReady (anchored to end of lines or output).
var promptPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^\$\s*$`), // Empty Shell prompt
	regexp.MustCompile(`(?m)^%\s*$`),  // Empty Zsh prompt
	regexp.MustCompile(`❯\s*$`),       // Modern prompts (U+276F)
	regexp.MustCompile(`›\s*$`),       // Codex prompt (U+203A) empty
	regexp.MustCompile(`(?m)^›`),      // Codex prompt with hint text
	regexp.MustCompile(`>\s*$`),       // Simple prompt at end of output
	regexp.MustCompile(`(?m)^>\s*$`),  // Simple prompt on its own line
}

// SpawnOptions configures the robot-spawn operation.
type SpawnOptions struct {
	Session        string
	Label          string   // Session label — constructs "{Session}--{Label}" if set
	CCCount        int      // Claude agents
	CodCount       int      // Codex agents
	GmiCount       int      // Gemini agents
	Preset         string   // Recipe/preset name
	NoUserPane     bool     // Don't create user pane
	WorkingDir     string   // Override working directory
	WaitReady      bool     // Wait for agents to be ready
	ReadyTimeout   int      // Timeout in seconds for ready detection
	DryRun         bool     // Preview mode: show what would happen without executing
	Safety         bool     // Fail if session already exists
	AssignWork     bool     // Enable orchestrator work assignment mode
	AssignStrategy string   // Assignment strategy: top-n, diverse, dependency-aware, skill-matched
	CustomNames    []string // Custom agent names (used in order, then NATO alphabet)
}

// SpawnOutput is the structured output for --robot-spawn.
type SpawnOutput struct {
	RobotResponse
	Session        string            `json:"session"`
	CreatedAt      string            `json:"created_at"`
	PresetUsed     string            `json:"preset_used,omitempty"`
	WorkingDir     string            `json:"working_dir"`
	Agents         []SpawnedAgent    `json:"agents"`
	Layout         string            `json:"layout"`
	TotalStartupMs int64             `json:"total_startup_ms"`
	Error          string            `json:"error,omitempty"`
	DryRun         bool              `json:"dry_run,omitempty"`
	WouldCreate    []SpawnedAgent    `json:"would_create,omitempty"`
	Mode           string            `json:"mode,omitempty"`            // "orchestrator" when AssignWork is enabled
	Assignments    []SpawnAssignment `json:"assignments,omitempty"`     // Work assignments when AssignWork is enabled
	AssignStrategy string            `json:"assign_strategy,omitempty"` // Strategy used for assignments
	Recovery       *SpawnRecovery    `json:"recovery,omitempty"`        // Session recovery context from handoff
}

// SpawnRecovery contains session recovery context loaded from handoff.
type SpawnRecovery struct {
	HandoffPath  string `json:"handoff_path,omitempty"`  // Path to handoff file
	HandoffAge   string `json:"handoff_age,omitempty"`   // Human-readable age
	Goal         string `json:"goal,omitempty"`          // What previous session achieved
	Now          string `json:"now,omitempty"`           // What this session should do
	Status       string `json:"status,omitempty"`        // Previous session status
	Outcome      string `json:"outcome,omitempty"`       // Previous session outcome
	InjectedText string `json:"injected_text,omitempty"` // Formatted text injected into agents
}

// SpawnAssignment represents a work assignment to a spawned agent.
type SpawnAssignment struct {
	Pane        string `json:"pane"`                   // Pane reference (e.g., "0.1")
	AgentType   string `json:"agent_type"`             // claude, codex, gemini
	BeadID      string `json:"bead_id"`                // Assigned bead ID
	BeadTitle   string `json:"bead_title"`             // Bead title for context
	Priority    string `json:"priority"`               // Bead priority (P0-P4)
	Claimed     bool   `json:"claimed"`                // Whether bead was successfully claimed (marked in_progress)
	PromptSent  bool   `json:"prompt_sent"`            // Whether the work prompt was sent to the agent
	ClaimError  string `json:"claim_error,omitempty"`  // Error during claim, if any
	PromptError string `json:"prompt_error,omitempty"` // Error sending prompt, if any
}

// SpawnedAgent represents an agent created during spawn.
type SpawnedAgent struct {
	Pane      string `json:"pane"`
	Name      string `json:"name,omitempty"`
	Type      string `json:"type"`
	Variant   string `json:"variant,omitempty"`
	Title     string `json:"title"`
	Ready     bool   `json:"ready"`
	StartupMs int64  `json:"startup_ms"`
	Error     string `json:"error,omitempty"`
}

// GetSpawn creates a session with agents and returns structured output.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSpawn(opts SpawnOptions, cfg *config.Config) (*SpawnOutput, error) {
	startTime := time.Now()
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	auditWorkingDir := ""
	auditSessionCreated := false
	auditPanesAdded := 0

	// Validate project name unconditionally: "--" is reserved for labels.
	if err := config.ValidateProjectName(opts.Session); err != nil {
		errOutput := &SpawnOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInvalidFlag, "Project names cannot contain '--' (reserved as label separator)"),
			Session:       opts.Session,
			Error:         err.Error(),
		}
		return errOutput, nil
	}

	// Apply goal label to session name (bd-1933u)
	if opts.Label != "" {
		if err := config.ValidateLabel(opts.Label); err != nil {
			errOutput := &SpawnOutput{
				RobotResponse: NewErrorResponse(fmt.Errorf("invalid label: %w", err), ErrCodeInvalidFlag, "Use a valid label (alphanumeric, dash, underscore)"),
				Session:       opts.Session,
				Error:         fmt.Sprintf("invalid label: %v", err),
			}
			return errOutput, nil
		}
		opts.Session = config.FormatSessionName(opts.Session, opts.Label)
	}

	output := &SpawnOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		CreatedAt:     startTime.UTC().Format(time.RFC3339),
		PresetUsed:    opts.Preset,
		Agents:        []SpawnedAgent{},
		Layout:        "tiled",
	}
	_ = audit.LogEvent(opts.Session, audit.EventTypeSpawn, audit.ActorSystem, "robot.spawn", map[string]interface{}{
		"phase":           "start",
		"session":         opts.Session,
		"total_agents":    opts.CCCount + opts.CodCount + opts.GmiCount,
		"preset":          opts.Preset,
		"no_user_pane":    opts.NoUserPane,
		"dry_run":         opts.DryRun,
		"safety":          opts.Safety,
		"assign_work":     opts.AssignWork,
		"assign_strategy": opts.AssignStrategy,
		"correlation_id":  correlationID,
	}, nil)
	defer func() {
		agentsLaunched := 0
		if output != nil {
			agentsLaunched = len(output.Agents)
		}
		success := output != nil && output.Success
		payload := map[string]interface{}{
			"phase":           "finish",
			"session":         opts.Session,
			"total_agents":    opts.CCCount + opts.CodCount + opts.GmiCount,
			"preset":          opts.Preset,
			"no_user_pane":    opts.NoUserPane,
			"dry_run":         opts.DryRun,
			"safety":          opts.Safety,
			"assign_work":     opts.AssignWork,
			"assign_strategy": opts.AssignStrategy,
			"session_created": auditSessionCreated,
			"panes_added":     auditPanesAdded,
			"agents_launched": agentsLaunched,
			"success":         success,
			"duration_ms":     time.Since(auditStart).Milliseconds(),
			"working_dir":     auditWorkingDir,
			"correlation_id":  correlationID,
		}
		if output != nil && output.Error != "" {
			payload["error"] = output.Error
		}
		_ = audit.LogEvent(opts.Session, audit.EventTypeSpawn, audit.ActorSystem, "robot.spawn", payload, nil)
	}()

	// Validate session name
	if err := tmux.ValidateSessionName(opts.Session); err != nil {
		output.Error = fmt.Sprintf("invalid session name: %v", err)
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Use a valid tmux session name")
		return output, nil
	}

	// Check tmux availability
	if !tmux.IsInstalled() {
		output.Error = "tmux is not installed"
		output.RobotResponse = NewErrorResponse(fmt.Errorf("%s", output.Error), ErrCodeDependencyMissing, "Install tmux to spawn sessions")
		return output, nil
	}

	// Safety check: fail if session already exists (when --spawn-safety is enabled)
	if opts.Safety && tmux.SessionExists(opts.Session) {
		output.Error = fmt.Sprintf("session '%s' already exists (--spawn-safety mode prevents reuse; use 'ntm kill %s' first)", opts.Session, opts.Session)
		output.RobotResponse = NewErrorResponse(fmt.Errorf("%s", output.Error), ErrCodeInvalidFlag, "Choose a new session name or disable --spawn-safety")
		return output, nil
	}

	// Get working directory
	dir := opts.WorkingDir
	if dir == "" && cfg != nil {
		dir = cfg.GetProjectDir(opts.Session)
	}
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			output.Error = fmt.Sprintf("could not determine working directory: %v", err)
			output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check working directory permissions")
			return output, nil
		}
	}
	output.WorkingDir = dir
	auditWorkingDir = dir

	// Load handoff context for session recovery (non-fatal if not found)
	spawnRecovery, handoffCtx := loadLatestHandoff(dir, opts.Session)
	if spawnRecovery != nil {
		output.Recovery = spawnRecovery
	}
	// handoffCtx is available for use in work prompts below
	_ = handoffCtx // silence unused warning when not in orchestrator mode

	totalAgents := opts.CCCount + opts.CodCount + opts.GmiCount
	if totalAgents == 0 {
		output.Error = "no agents specified (use cc, cod, or gmi counts)"
		output.RobotResponse = NewErrorResponse(fmt.Errorf("%s", output.Error), ErrCodeInvalidFlag, "Specify at least one agent count")
		return output, nil
	}

	// Calculate total panes needed
	totalPanes := totalAgents
	if !opts.NoUserPane {
		totalPanes++
	}

	// Dry-run mode: show what would happen without executing
	if opts.DryRun {
		output.DryRun = true
		output.WouldCreate = []SpawnedAgent{}

		// Initialize name map for dry-run preview
		var dryRunNameMap *AgentNameMap
		if len(opts.CustomNames) > 0 {
			dryRunNameMap = NewAgentNameMapWithCustomNames(opts.Session, opts.CustomNames)
		} else {
			dryRunNameMap = NewAgentNameMap(opts.Session)
		}

		// Build list of what would be created
		paneIdx := 0
		if !opts.NoUserPane {
			userPane := fmt.Sprintf("0.%d", paneIdx)
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  userPane,
				Name:  dryRunNameMap.AssignNew("user", userPane),
				Type:  "user",
				Title: fmt.Sprintf("%s__user", opts.Session),
				Ready: true,
			})
			paneIdx++
		}

		for i := 0; i < opts.CCCount; i++ {
			ccPane := fmt.Sprintf("0.%d", paneIdx)
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  ccPane,
				Name:  dryRunNameMap.AssignNew("claude", ccPane),
				Type:  "claude",
				Title: fmt.Sprintf("%s__cc_%d", opts.Session, i+1),
			})
			paneIdx++
		}

		for i := 0; i < opts.CodCount; i++ {
			codPane := fmt.Sprintf("0.%d", paneIdx)
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  codPane,
				Name:  dryRunNameMap.AssignNew("codex", codPane),
				Type:  "codex",
				Title: fmt.Sprintf("%s__cod_%d", opts.Session, i+1),
			})
			paneIdx++
		}

		for i := 0; i < opts.GmiCount; i++ {
			gmiPane := fmt.Sprintf("0.%d", paneIdx)
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  gmiPane,
				Name:  dryRunNameMap.AssignNew("gemini", gmiPane),
				Type:  "gemini",
				Title: fmt.Sprintf("%s__gmi_%d", opts.Session, i+1),
			})
			paneIdx++
		}

		output.Layout = "tiled"
		return output, nil
	}

	// Ensure directory exists (only for real spawns, not dry-run)
	if err := os.MkdirAll(dir, 0755); err != nil {
		output.Error = fmt.Sprintf("creating directory: %v", err)
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check directory permissions")
		return output, nil
	}

	// Create session if it doesn't exist
	sessionCreated := false
	if !tmux.SessionExists(opts.Session) {
		historyLimit := tmux.DefaultHistoryLimit
		if cfg != nil && cfg.Tmux.HistoryLimit > 0 {
			historyLimit = cfg.Tmux.HistoryLimit
		}
		if err := tmux.CreateSessionWithHistoryLimit(opts.Session, dir, historyLimit); err != nil {
			output.Error = fmt.Sprintf("creating session: %v", err)
			output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux availability and session name")
			return output, nil
		}
		sessionCreated = true
		auditSessionCreated = true
	}

	// Get current panes
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.Error = fmt.Sprintf("getting panes: %v", err)
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux session state")
		return output, nil
	}

	// Add more panes if needed
	existingPanes := len(panes)
	if existingPanes < totalPanes {
		toAdd := totalPanes - existingPanes
		auditPanesAdded = toAdd
		for i := 0; i < toAdd; i++ {
			if _, err := tmux.SplitWindow(opts.Session, dir); err != nil {
				output.Error = fmt.Sprintf("creating pane: %v", err)
				output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux pane layout constraints")
				return output, nil
			}
		}
	}

	// Get updated pane list
	panes, err = tmux.GetPanes(opts.Session)
	if err != nil {
		output.Error = fmt.Sprintf("getting panes: %v", err)
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux session state")
		return output, nil
	}

	// Apply tiled layout
	_ = tmux.ApplyTiledLayout(opts.Session)

	// Initialize agent name map
	var nameMap *AgentNameMap
	if len(opts.CustomNames) > 0 {
		nameMap = NewAgentNameMapWithCustomNames(opts.Session, opts.CustomNames)
	} else {
		nameMap = NewAgentNameMap(opts.Session)
	}

	// Start assigning agents (skip first pane if user pane)
	startIdx := 0
	if !opts.NoUserPane {
		startIdx = 1
		// Add user pane info
		if len(panes) > 0 {
			userPaneRef := fmt.Sprintf("0.%d", panes[0].Index)
			userName := nameMap.AssignNew("user", userPaneRef)
			output.Agents = append(output.Agents, SpawnedAgent{
				Pane:      userPaneRef,
				Name:      userName,
				Type:      "user",
				Title:     panes[0].Title,
				Ready:     true,
				StartupMs: 0,
			})
		}
	}

	agentNum := startIdx
	agentCommands := getAgentCommands(cfg)

	// Launch Claude agents
	for i := 0; i < opts.CCCount && agentNum < len(panes); i++ {
		agent := launchAgent(panes[agentNum], opts.Session, "claude", i+1, dir, agentCommands["claude"])
		agent.Name = nameMap.AssignNew("claude", agent.Pane)
		output.Agents = append(output.Agents, agent)
		agentNum++
	}

	// Launch Codex agents
	for i := 0; i < opts.CodCount && agentNum < len(panes); i++ {
		agent := launchAgent(panes[agentNum], opts.Session, "codex", i+1, dir, agentCommands["codex"])
		agent.Name = nameMap.AssignNew("codex", agent.Pane)
		output.Agents = append(output.Agents, agent)
		agentNum++
	}

	// Launch Gemini agents
	for i := 0; i < opts.GmiCount && agentNum < len(panes); i++ {
		agent := launchAgent(panes[agentNum], opts.Session, "gemini", i+1, dir, agentCommands["gemini"])
		agent.Name = nameMap.AssignNew("gemini", agent.Pane)
		output.Agents = append(output.Agents, agent)
		agentNum++
	}

	// Wait for agents to be ready if requested
	if opts.WaitReady {
		timeout := opts.ReadyTimeout
		if timeout <= 0 {
			timeout = 30 // default 30 seconds
		}
		waitForAgentsReady(output, time.Duration(timeout)*time.Second)
	}

	// Orchestrator work assignment mode
	if opts.AssignWork {
		output.Mode = "orchestrator"
		output.AssignStrategy = normalizeAssignStrategy(opts.AssignStrategy)
		assignments := assignWorkToAgents(output, dir, opts.Session, output.AssignStrategy)
		output.Assignments = assignments
	}

	output.TotalStartupMs = time.Since(startTime).Milliseconds()

	// Update layout based on what was created
	if sessionCreated {
		output.Layout = "tiled"
	}

	return output, nil
}

// PrintSpawn creates a session with agents and outputs structured JSON.
// This is a thin wrapper around GetSpawn() for CLI output.
func PrintSpawn(opts SpawnOptions, cfg *config.Config) error {
	output, err := GetSpawn(opts, cfg)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// launchAgent launches a single agent and returns its info.
func launchAgent(pane tmux.Pane, session, agentType string, num int, dir, command string) SpawnedAgent {
	startTime := time.Now()

	title := fmt.Sprintf("%s__%s_%d", session, agentTypeShort(agentType), num)
	agent := SpawnedAgent{
		Pane:  fmt.Sprintf("0.%d", pane.Index),
		Type:  agentType,
		Title: title,
		Ready: false,
	}

	// Set pane title
	if err := tmux.SetPaneTitle(pane.ID, title); err != nil {
		agent.Error = fmt.Sprintf("setting title: %v", err)
		agent.StartupMs = time.Since(startTime).Milliseconds()
		return agent
	}

	// Launch agent command
	safeCommand, err := tmux.SanitizePaneCommand(command)
	if err != nil {
		agent.Error = fmt.Sprintf("invalid command: %v", err)
		agent.StartupMs = time.Since(startTime).Milliseconds()
		return agent
	}

	cmd, err := tmux.BuildPaneCommand(dir, safeCommand)
	if err != nil {
		agent.Error = fmt.Sprintf("building command: %v", err)
		agent.StartupMs = time.Since(startTime).Milliseconds()
		return agent
	}

	if err := tmux.SendKeys(pane.ID, cmd, true); err != nil {
		agent.Error = fmt.Sprintf("launching: %v", err)
		agent.StartupMs = time.Since(startTime).Milliseconds()
		return agent
	}

	agent.StartupMs = time.Since(startTime).Milliseconds()
	return agent
}

// waitForAgentsReady polls agents for ready state.
func waitForAgentsReady(output *SpawnOutput, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	pollInterval := 500 * time.Millisecond

	for time.Now().Before(deadline) {
		allReady := true

		for i := range output.Agents {
			if output.Agents[i].Type == "user" {
				continue // User pane is always ready
			}
			if output.Agents[i].Ready {
				continue // Already detected as ready
			}

			// Build tmux target from session and pane reference
			// The Pane field is in "window.index" format (e.g., "0.2")
			// For tmux capture, use "session:window.pane" format
			paneRef := output.Agents[i].Pane

			// We can use the paneRef directly as it contains window.index
			target := fmt.Sprintf("%s:%s", output.Session, paneRef)

			// Capture pane output (50 lines to catch Claude's TUI)
			captured, err := tmux.CapturePaneOutput(target, 50)
			if err != nil {
				allReady = false
				continue
			}

			// Check for ready indicators
			if isAgentReady(captured, output.Agents[i].Type) {
				output.Agents[i].Ready = true
			} else {
				allReady = false
			}
		}

		if allReady {
			return
		}

		time.Sleep(pollInterval)
	}
}

// isAgentReady checks if agent output indicates ready state.
// Note: agentType is accepted for future type-specific detection but currently unused.
func isAgentReady(output, _ string) bool {
	lower := strings.ToLower(output)

	// Common ready indicators (case-insensitive)
	lowerPatterns := []string{
		"claude>",
		"claude >",
		"codex>",
		"openai codex",
		"context left",
		"gemini>",
		">>>", // Python REPL
		"waiting for input",
		"ready",
		"how can i help",
		// Claude Code TUI indicators
		"claude code v",      // Version banner
		"welcome back",       // Greeting
		"bypass permissions", // Status line
		"try \"",             // Example prompt
	}

	for _, pattern := range lowerPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}

	for _, p := range promptPatterns {
		if p.MatchString(output) {
			return true
		}
	}

	return false
}

// agentTypeShort returns short form for pane naming.
func agentTypeShort(agentType string) string {
	switch agentType {
	case "claude":
		return "cc"
	case "codex":
		return "cod"
	case "gemini":
		return "gmi"
	default:
		return agentType
	}
}

// getAgentCommands returns the commands to launch each agent type.
// Templates are rendered with empty vars (optional fields only).
func getAgentCommands(cfg *config.Config) map[string]string {
	defaults := map[string]string{
		"claude": "claude",
		"codex":  "codex",
		"gemini": "gemini",
	}

	if cfg != nil && cfg.Agents.Claude != "" {
		defaults["claude"] = cfg.Agents.Claude
	}
	if cfg != nil && cfg.Agents.Codex != "" {
		defaults["codex"] = cfg.Agents.Codex
	}
	if cfg != nil && cfg.Agents.Gemini != "" {
		defaults["gemini"] = cfg.Agents.Gemini
	}

	// Render templates with empty vars (all template fields are optional)
	vars := config.AgentTemplateVars{}
	for agentType, cmdTemplate := range defaults {
		if rendered, err := config.GenerateAgentCommand(cmdTemplate, vars); err == nil {
			defaults[agentType] = rendered
		}
		// On error, keep original command (non-template or invalid template)
	}

	return defaults
}

// loadLatestHandoff loads the most recent handoff for a session and returns recovery context.
// Returns nil if no handoff is found or an error occurs (non-fatal).
func loadLatestHandoff(workDir, sessionName string) (*SpawnRecovery, *recovery.HandoffContext) {
	reader := handoff.NewReader(workDir)
	h, path, err := reader.FindLatest(sessionName)
	if err != nil || h == nil {
		return nil, nil
	}

	// Convert to recovery context
	ctx := recovery.HandoffContextFromHandoff(h, path)
	if ctx == nil {
		return nil, nil
	}

	// Format the injection text for fresh spawn
	injectedText := recovery.GetInjectionForType(recovery.SessionFreshSpawn, ctx, nil)

	// Build spawn recovery info
	spawnRecovery := &SpawnRecovery{
		HandoffPath:  path,
		HandoffAge:   recovery.HumanizeDuration(ctx.Age),
		Goal:         ctx.Goal,
		Now:          ctx.Now,
		Status:       ctx.Status,
		Outcome:      ctx.Outcome,
		InjectedText: injectedText,
	}

	return spawnRecovery, ctx
}

// normalizeAssignStrategy validates and normalizes the assignment strategy.
func normalizeAssignStrategy(strategy string) string {
	s := strings.ToLower(strings.TrimSpace(strategy))
	switch s {
	case "top-n", "topn":
		return "top-n"
	case "diverse":
		return "diverse"
	case "dependency-aware", "dependency":
		return "dependency-aware"
	case "skill-matched", "skill":
		return "skill-matched"
	default:
		return "top-n" // Default strategy
	}
}

// assignWorkToAgents gets triage recommendations, claims beads, and sends work prompts.
func assignWorkToAgents(output *SpawnOutput, workDir, session, strategy string) []SpawnAssignment {
	var assignments []SpawnAssignment

	// Get non-user agents that are ready
	var readyAgents []SpawnedAgent
	for _, agent := range output.Agents {
		if agent.Type == "user" {
			continue
		}
		// Include agents even if not marked ready (best effort)
		readyAgents = append(readyAgents, agent)
	}

	if len(readyAgents) == 0 {
		return assignments
	}

	// Get triage recommendations from bv
	triage, err := bv.GetTriage(workDir)
	if err != nil || triage == nil {
		return assignments
	}

	// Get work items based on strategy
	workItems := getWorkItemsForStrategy(triage, strategy, len(readyAgents))
	if len(workItems) == 0 {
		return assignments
	}

	// Assign work to agents
	for i, agent := range readyAgents {
		if i >= len(workItems) {
			break
		}

		item := workItems[i]
		assignment := SpawnAssignment{
			Pane:      agent.Pane,
			AgentType: agent.Type,
			BeadID:    item.ID,
			BeadTitle: item.Title,
			Priority:  fmt.Sprintf("P%d", item.Priority),
		}

		// Claim the bead (mark as in_progress)
		if err := claimBead(workDir, item.ID); err != nil {
			assignment.ClaimError = err.Error()
		} else {
			assignment.Claimed = true
		}

		// Send work prompt to the agent (only if claimed or best effort)
		if assignment.Claimed {
			prompt := generateWorkPrompt(item)
			if err := sendWorkPrompt(session, agent.Pane, prompt); err != nil {
				assignment.PromptError = err.Error()
			} else {
				assignment.PromptSent = true
			}
		}

		assignments = append(assignments, assignment)
	}

	return assignments
}

// workItem represents a work item from triage for assignment.
type workItem struct {
	ID       string
	Title    string
	Priority int
	Score    float64
	Type     string
	Reasons  []string
}

// getWorkItemsForStrategy returns work items based on the selected strategy.
func getWorkItemsForStrategy(triage *bv.TriageResponse, strategy string, count int) []workItem {
	var items []workItem

	switch strategy {
	case "diverse":
		// Get a mix of different task types
		items = getDiverseWorkItems(triage, count)
	case "dependency-aware":
		// Prioritize items that unblock others
		items = getDependencyAwareItems(triage, count)
	case "skill-matched":
		// This would ideally match agent types to task types
		// For now, fall through to top-n
		fallthrough
	case "top-n":
		fallthrough
	default:
		// Get top N recommendations by score
		items = getTopNWorkItems(triage, count)
	}

	return items
}

// getTopNWorkItems returns the top N recommendations by score.
func getTopNWorkItems(triage *bv.TriageResponse, count int) []workItem {
	var items []workItem

	for i, rec := range triage.Triage.Recommendations {
		if i >= count {
			break
		}
		items = append(items, workItem{
			ID:       rec.ID,
			Title:    rec.Title,
			Priority: rec.Priority,
			Score:    rec.Score,
			Type:     rec.Type,
			Reasons:  rec.Reasons,
		})
	}

	return items
}

// getDiverseWorkItems returns a diverse set of work items by type.
func getDiverseWorkItems(triage *bv.TriageResponse, count int) []workItem {
	var items []workItem
	seenTypes := make(map[string]bool)

	// First pass: get one of each type
	for _, rec := range triage.Triage.Recommendations {
		if len(items) >= count {
			break
		}
		if !seenTypes[rec.Type] {
			items = append(items, workItem{
				ID:       rec.ID,
				Title:    rec.Title,
				Priority: rec.Priority,
				Score:    rec.Score,
				Type:     rec.Type,
				Reasons:  rec.Reasons,
			})
			seenTypes[rec.Type] = true
		}
	}

	// Second pass: fill remaining slots with top items
	if len(items) < count {
		for _, rec := range triage.Triage.Recommendations {
			if len(items) >= count {
				break
			}
			// Check if already included
			found := false
			for _, existing := range items {
				if existing.ID == rec.ID {
					found = true
					break
				}
			}
			if !found {
				items = append(items, workItem{
					ID:       rec.ID,
					Title:    rec.Title,
					Priority: rec.Priority,
					Score:    rec.Score,
					Type:     rec.Type,
					Reasons:  rec.Reasons,
				})
			}
		}
	}

	return items
}

// getDependencyAwareItems prioritizes items that unblock the most work.
func getDependencyAwareItems(triage *bv.TriageResponse, count int) []workItem {
	var items []workItem

	// First, add blockers to clear (these unblock other work)
	for _, blocker := range triage.Triage.BlockersToClear {
		if len(items) >= count {
			break
		}
		if blocker.Actionable {
			items = append(items, workItem{
				ID:       blocker.ID,
				Title:    blocker.Title,
				Priority: 0, // Blockers get high priority
				Score:    float64(blocker.UnblocksCount),
				Type:     "blocker",
				Reasons:  []string{fmt.Sprintf("Unblocks %d items", blocker.UnblocksCount)},
			})
		}
	}

	// Then fill with top recommendations
	if len(items) < count {
		for _, rec := range triage.Triage.Recommendations {
			if len(items) >= count {
				break
			}
			// Check if already included
			found := false
			for _, existing := range items {
				if existing.ID == rec.ID {
					found = true
					break
				}
			}
			if !found {
				items = append(items, workItem{
					ID:       rec.ID,
					Title:    rec.Title,
					Priority: rec.Priority,
					Score:    rec.Score,
					Type:     rec.Type,
					Reasons:  rec.Reasons,
				})
			}
		}
	}

	return items
}

// claimBead marks a bead as in_progress using bd CLI.
func claimBead(workDir, beadID string) error {
	_, err := bv.RunBd(workDir, "update", beadID, "--status", "in_progress")
	return err
}

// generateWorkPrompt creates a prompt for an agent to work on a bead.
func generateWorkPrompt(item workItem) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Work on bead %s: %s\n\n", item.ID, item.Title))
	sb.WriteString("Use `br show " + item.ID + "` to see full details.\n")
	sb.WriteString("This bead has been marked as in_progress.\n")

	if len(item.Reasons) > 0 {
		sb.WriteString("\nContext:\n")
		for _, reason := range item.Reasons {
			sb.WriteString("- " + reason + "\n")
		}
	}

	sb.WriteString("\nWhen done, close it with: `br close " + item.ID + " --reason \"Completed\"`")

	return sb.String()
}

// sendWorkPrompt sends a work prompt to an agent via tmux.
func sendWorkPrompt(session, paneRef, prompt string) error {
	// Build the full pane target
	target := fmt.Sprintf("%s:%s", session, paneRef)

	// Send the prompt directly.
	// tmux.SendKeys uses 'send-keys -l' which treats input literally.
	// Newlines in the prompt string are interpreted by the shell as Enter presses.
	// The final 'true' argument ensures a trailing Enter is sent to execute the last line.
	return tmux.SendKeys(target, prompt, true)
}
