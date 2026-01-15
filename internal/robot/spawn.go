package robot

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// SpawnOptions configures the robot-spawn operation.
type SpawnOptions struct {
	Session      string
	CCCount      int    // Claude agents
	CodCount     int    // Codex agents
	GmiCount     int    // Gemini agents
	Preset       string // Recipe/preset name
	NoUserPane   bool   // Don't create user pane
	WorkingDir   string // Override working directory
	WaitReady    bool   // Wait for agents to be ready
	ReadyTimeout int    // Timeout in seconds for ready detection
	DryRun       bool   // Preview mode: show what would happen without executing
	Safety       bool   // Fail if session already exists
}

// SpawnOutput is the structured output for --robot-spawn.
type SpawnOutput struct {
	Session        string         `json:"session"`
	CreatedAt      string         `json:"created_at"`
	PresetUsed     string         `json:"preset_used,omitempty"`
	WorkingDir     string         `json:"working_dir"`
	Agents         []SpawnedAgent `json:"agents"`
	Layout         string         `json:"layout"`
	TotalStartupMs int64          `json:"total_startup_ms"`
	Error          string         `json:"error,omitempty"`
	DryRun         bool           `json:"dry_run,omitempty"`
	WouldCreate    []SpawnedAgent `json:"would_create,omitempty"`
}

// SpawnedAgent represents an agent created during spawn.
type SpawnedAgent struct {
	Pane      string `json:"pane"`
	Type      string `json:"type"`
	Variant   string `json:"variant,omitempty"`
	Title     string `json:"title"`
	Ready     bool   `json:"ready"`
	StartupMs int64  `json:"startup_ms"`
	Error     string `json:"error,omitempty"`
}

// PrintSpawn creates a session with agents and outputs structured JSON.
func PrintSpawn(opts SpawnOptions, cfg *config.Config) error {
	startTime := time.Now()

	output := SpawnOutput{
		Session:    opts.Session,
		CreatedAt:  startTime.UTC().Format(time.RFC3339),
		PresetUsed: opts.Preset,
		Agents:     []SpawnedAgent{},
		Layout:     "tiled",
	}

	// Validate session name
	if err := tmux.ValidateSessionName(opts.Session); err != nil {
		output.Error = fmt.Sprintf("invalid session name: %v", err)
		return encodeJSON(output)
	}

	// Check tmux availability
	if !tmux.IsInstalled() {
		output.Error = "tmux is not installed"
		return encodeJSON(output)
	}

	// Safety check: fail if session already exists (when --spawn-safety is enabled)
	if opts.Safety && tmux.SessionExists(opts.Session) {
		output.Error = fmt.Sprintf("session '%s' already exists (--spawn-safety mode prevents reuse; use 'ntm kill %s' first)", opts.Session, opts.Session)
		return encodeJSON(output)
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
			return encodeJSON(output)
		}
	}
	output.WorkingDir = dir

	// Ensure directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		output.Error = fmt.Sprintf("creating directory: %v", err)
		return encodeJSON(output)
	}

	totalAgents := opts.CCCount + opts.CodCount + opts.GmiCount
	if totalAgents == 0 {
		output.Error = "no agents specified (use cc, cod, or gmi counts)"
		return encodeJSON(output)
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

		// Build list of what would be created
		paneIdx := 0
		if !opts.NoUserPane {
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  fmt.Sprintf("0.%d", paneIdx),
				Type:  "user",
				Title: fmt.Sprintf("%s__user", opts.Session),
				Ready: true,
			})
			paneIdx++
		}

		for i := 0; i < opts.CCCount; i++ {
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  fmt.Sprintf("0.%d", paneIdx),
				Type:  "claude",
				Title: fmt.Sprintf("%s__cc_%d", opts.Session, i+1),
			})
			paneIdx++
		}

		for i := 0; i < opts.CodCount; i++ {
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  fmt.Sprintf("0.%d", paneIdx),
				Type:  "codex",
				Title: fmt.Sprintf("%s__cod_%d", opts.Session, i+1),
			})
			paneIdx++
		}

		for i := 0; i < opts.GmiCount; i++ {
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  fmt.Sprintf("0.%d", paneIdx),
				Type:  "gemini",
				Title: fmt.Sprintf("%s__gmi_%d", opts.Session, i+1),
			})
			paneIdx++
		}

		output.Layout = "tiled"
		return encodeJSON(output)
	}

	// Create session if it doesn't exist
	sessionCreated := false
	if !tmux.SessionExists(opts.Session) {
		if err := tmux.CreateSession(opts.Session, dir); err != nil {
			output.Error = fmt.Sprintf("creating session: %v", err)
			return encodeJSON(output)
		}
		sessionCreated = true
	}

	// Get current panes
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.Error = fmt.Sprintf("getting panes: %v", err)
		return encodeJSON(output)
	}

	// Add more panes if needed
	existingPanes := len(panes)
	if existingPanes < totalPanes {
		toAdd := totalPanes - existingPanes
		for i := 0; i < toAdd; i++ {
			if _, err := tmux.SplitWindow(opts.Session, dir); err != nil {
				output.Error = fmt.Sprintf("creating pane: %v", err)
				return encodeJSON(output)
			}
		}
	}

	// Get updated pane list
	panes, err = tmux.GetPanes(opts.Session)
	if err != nil {
		output.Error = fmt.Sprintf("getting panes: %v", err)
		return encodeJSON(output)
	}

	// Apply tiled layout
	_ = tmux.ApplyTiledLayout(opts.Session)

	// Start assigning agents (skip first pane if user pane)
	startIdx := 0
	if !opts.NoUserPane {
		startIdx = 1
		// Add user pane info
		if len(panes) > 0 {
			output.Agents = append(output.Agents, SpawnedAgent{
				Pane:      fmt.Sprintf("0.%d", panes[0].Index),
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
		output.Agents = append(output.Agents, agent)
		agentNum++
	}

	// Launch Codex agents
	for i := 0; i < opts.CodCount && agentNum < len(panes); i++ {
		agent := launchAgent(panes[agentNum], opts.Session, "codex", i+1, dir, agentCommands["codex"])
		output.Agents = append(output.Agents, agent)
		agentNum++
	}

	// Launch Gemini agents
	for i := 0; i < opts.GmiCount && agentNum < len(panes); i++ {
		agent := launchAgent(panes[agentNum], opts.Session, "gemini", i+1, dir, agentCommands["gemini"])
		output.Agents = append(output.Agents, agent)
		agentNum++
	}

	// Wait for agents to be ready if requested
	if opts.WaitReady {
		timeout := opts.ReadyTimeout
		if timeout <= 0 {
			timeout = 30 // default 30 seconds
		}
		waitForAgentsReady(&output, time.Duration(timeout)*time.Second)
	}

	output.TotalStartupMs = time.Since(startTime).Milliseconds()

	// Update layout based on what was created
	if sessionCreated {
		output.Layout = "tiled"
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
			// For tmux capture, use "session.pane_index" format
			paneRef := output.Agents[i].Pane
			// Extract pane index from "window.index" format
			paneIndex := paneRef
			if idx := strings.LastIndex(paneRef, "."); idx >= 0 {
				paneIndex = paneRef[idx+1:]
			}
			target := output.Session + "." + paneIndex

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
func isAgentReady(output, agentType string) bool {
	lower := strings.ToLower(output)

	// Common ready indicators (case-insensitive)
	lowerPatterns := []string{
		"claude>",
		"claude >",
		"codex>",
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

	// Case-sensitive patterns (Unicode symbols)
	exactPatterns := []string{
		"$ ", // Shell prompt
		"% ", // Zsh prompt
		"❯",  // Modern prompts (U+276F)
		">",  // Simple prompt
	}

	for _, pattern := range exactPatterns {
		if strings.Contains(output, pattern) {
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
