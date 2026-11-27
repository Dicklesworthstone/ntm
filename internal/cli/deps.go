package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newDepsCmd() *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:     "deps",
		Aliases: []string{"check"},
		Short:   "Check for required dependencies and agent CLIs",
		Long: `Check that all required tools and AI agent CLIs are installed:

Required:
  - tmux (terminal multiplexer)

Optional agents:
  - claude (Claude Code CLI)
  - codex (OpenAI Codex CLI)
  - agy (Antigravity CLI, Google agent)
  - grok (Grok Build CLI, xAI)
  - gemini (Google Gemini CLI, legacy)
  - cursor-agent (Cursor Agent CLI; not the 'cursor' IDE binary)
  - opencode (OpenCode)
  - omp (Oh My Pi, the pi-agent fork)

Also checks for recommended tools like fzf, the Agent Mail service, and the
optional flywheel tools (br, bv, dcg, pt, cass, cm, caam, caut, ubs).

Examples:
  ntm deps           # Quick check
  ntm deps -v        # Verbose output with versions
  ntm deps --json    # JSON output for scripts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeps(verbose)
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show detailed version info")

	return cmd
}

type depCheck struct {
	Name        string
	Command     string
	VersionArgs []string
	Required    bool
	Category    string
	InstallHint string
}

var depVersionTimeout = 2 * time.Second

// DepsInput is the kernel input for core.deps.
type DepsInput struct {
	Verbose bool `json:"verbose,omitempty"`
}

func registerDepsKernel() {
	kernel.MustRegister(kernel.Command{
		Name:        "core.deps",
		Description: "Check required dependencies and optional agent CLIs",
		Category:    "core",
		Input: &kernel.SchemaRef{
			Name: "DepsInput",
			Ref:  "cli.DepsInput",
		},
		Output: &kernel.SchemaRef{
			Name: "DepsResponse",
			Ref:  "output.DepsResponse",
		},
		REST: &kernel.RESTBinding{
			Method: "GET",
			Path:   "/api/v1/deps",
		},
		Examples: []kernel.Example{
			{
				Name:        "deps",
				Description: "Check dependencies",
				Command:     "ntm deps",
			},
			{
				Name:        "deps-verbose",
				Description: "Check dependencies with versions",
				Command:     "ntm deps --verbose",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("core.deps", func(ctx context.Context, input any) (any, error) {
		if _, err := decodeKernelInput[DepsInput]("core.deps", input); err != nil {
			return nil, err
		}
		return buildDepsResponse()
	})
}

// defaultDepChecks returns the built-in probes followed by one probe per
// agent plugin declared under <config>/agents (ntm#260), so a plugin agent
// is reported — and counted as available — alongside the built-ins.
func defaultDepChecks() []depCheck {
	checks := builtinDepChecks()
	seenCmd := make(map[string]struct{}, len(checks))
	for _, c := range checks {
		if cmd := strings.ToLower(strings.TrimSpace(c.Command)); cmd != "" {
			seenCmd[cmd] = struct{}{}
		}
	}
	for _, p := range pluginDepChecks(registerEffectiveAgentPluginTypes(pluginAgentsDirForArgs(os.Args[1:]))) {
		cmd := strings.ToLower(strings.TrimSpace(p.Command))
		if cmd != "" {
			if _, dup := seenCmd[cmd]; dup {
				continue
			}
			seenCmd[cmd] = struct{}{}
		}
		checks = append(checks, p)
	}
	return checks
}

func builtinDepChecks() []depCheck {
	return []depCheck{
		// Required
		{
			Name:        "tmux",
			Command:     "tmux",
			VersionArgs: []string{"-V"},
			Required:    true,
			Category:    "Required",
			InstallHint: "brew install tmux (macOS) / apt install tmux (Linux)",
		},

		// Agents
		{
			Name:        "Claude Code",
			Command:     "claude",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "npm install -g @anthropic-ai/claude-code",
		},
		{
			Name:        "OpenAI Codex",
			Command:     "codex",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "npm install -g @openai/codex",
		},
		{
			Name:        "Antigravity CLI (agy)",
			Command:     "agy",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "curl -fsSL https://antigravity.google/cli/install.sh | bash (then `agy` once to authenticate via Google OAuth)",
		},
		{
			Name:        "Grok Build (xAI)",
			Command:     "grok",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "curl -fsSL https://x.ai/cli/install.sh | bash (then `grok login`; use `grok login --device-auth` on a remote host)",
		},
		{
			Name:        "Gemini CLI (legacy)",
			Command:     "gemini",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "npm install -g @google/gemini-cli",
		},
		{
			// Deliberately `cursor-agent`, not `cursor`: on Linux `cursor` is
			// the GUI editor launcher (useless in a tmux pane) and is often
			// absent entirely on headless hosts, so checking it would report
			// the wrong binary as the Cursor dependency (GH#233).
			Name:        "Cursor Agent CLI",
			Command:     "cursor-agent",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "curl https://cursor.com/install -fsS | bash (installs `cursor-agent`; the `cursor` IDE binary is NOT the agent CLI)",
		},
		{
			Name:        "OpenCode",
			Command:     "opencode",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "curl -fsSL https://opencode.ai/install | bash (or `npm install -g opencode-ai`)",
		},
		{
			Name:        "Oh My Pi (omp)",
			Command:     "omp",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "bun install -g @oh-my-pi/pi-coding-agent (see https://omp.sh), then run `omp setup` once interactively before the first spawn",
		},
		{
			Name:        "Pi Agent",
			Command:     "pi",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "npm install -g @mariozechner/pi-coding-agent",
		},
		{
			Name:        "Aider",
			Command:     "aider",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "pip install aider-chat",
		},
		{
			Name:        "Ollama",
			Command:     "ollama",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "curl -fsSL https://ollama.com/install.sh | sh (or brew install ollama)",
		},

		// Recommended
		{
			Name:        "fzf",
			Command:     "fzf",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "Recommended",
			InstallHint: "brew install fzf (macOS) / apt install fzf (Linux)",
		},
		{
			Name:        "git",
			Command:     "git",
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "Recommended",
			InstallHint: "brew install git (macOS) / apt install git (Linux)",
		},
	}
}

func runDeps(verbose bool) error {
	// In text mode we also display flywheel health details; start probing those
	// concurrently with core.deps so both complete in one round-trip instead of
	// probing flywheel tools twice back-to-back.
	var (
		flywheelInfoCh chan map[tools.ToolName]*tools.ToolInfo
		flywheelCancel context.CancelFunc
	)
	if !IsJSONOutput() {
		var fwCtx context.Context
		fwCtx, flywheelCancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer flywheelCancel()
		flywheelInfoCh = make(chan map[tools.ToolName]*tools.ToolInfo, 1)
		go func() {
			flywheelInfoCh <- probeFlywheelToolInfo(fwCtx)
		}()
	}

	result, err := kernel.Run(context.Background(), "core.deps", DepsInput{Verbose: verbose})
	if err != nil {
		if IsJSONOutput() {
			response := depsJSONErrorResponse(err, robot.ErrCodeInternalError, "Retry the dependency check or inspect ntm diagnostics")
			return emitJSONFailureEnvelopeWithCause(response, err)
		}
		return err
	}

	resp, err := coerceDepsResponse(result)
	if err != nil {
		if IsJSONOutput() {
			response := depsJSONErrorResponse(err, robot.ErrCodeInternalError, "Retry the dependency check or inspect ntm diagnostics")
			return emitJSONFailureEnvelopeWithCause(response, err)
		}
		return err
	}

	// JSON output mode
	if IsJSONOutput() {
		if !resp.AllInstalled {
			cause := fmt.Errorf("required dependencies are missing")
			failure := depsJSONErrorResponse(
				cause,
				robot.ErrCodeDependencyMissing,
				"Install each required dependency listed with installed=false, then rerun 'ntm deps --json'",
			)
			return emitJSONFailureEnvelopeWithCause(struct {
				robot.RobotResponse
				output.DepsResponse
			}{
				RobotResponse: failure,
				DepsResponse:  resp,
			}, cause)
		}
		return output.PrintJSON(resp)
	}

	// Text output mode
	t := theme.Current()

	// Group by category
	categories := []string{"Required", "AI Agents", "Recommended"}
	deps := defaultDepChecks()
	byCategory := make(map[string][]depCheck)
	for _, d := range deps {
		byCategory[d.Category] = append(byCategory[d.Category], d)
	}

	depByName := make(map[string]output.DependencyCheck, len(resp.Dependencies))
	for _, dep := range resp.Dependencies {
		depByName[dep.Name] = dep
	}

	missingRequired := !resp.AllInstalled
	agentsAvailable := 0

	fmt.Println()
	fmt.Printf("%s NTM Dependency Check%s\n", "\033[1m", "\033[0m")
	fmt.Printf("%s═══════════════════════════════════════════════════%s\n\n", "\033[2m", "\033[0m")

	for _, cat := range categories {
		items := byCategory[cat]
		if len(items) == 0 {
			continue
		}

		fmt.Printf("%s%s:%s\n\n", "\033[1m", cat, "\033[0m")

		for _, dep := range items {
			installed := false
			version := ""
			if result, ok := depByName[dep.Name]; ok {
				installed = result.Installed
				version = result.Version
			}
			status := "not found"
			if installed {
				status = "found"
			}
			if installed && dep.Category == "AI Agents" {
				agentsAvailable++
			}

			var statusIcon, statusColor string
			switch status {
			case "found":
				statusIcon = "✓"
				statusColor = colorize(t.Success)
			case "not found":
				statusIcon = "✗"
				if dep.Required {
					statusColor = colorize(t.Error)
				} else {
					statusColor = colorize(t.Warning)
				}
			case "error":
				statusIcon = "?"
				statusColor = colorize(t.Overlay)
			}

			fmt.Printf("  %s%s%s %-15s", statusColor, statusIcon, "\033[0m", dep.Name)

			if verbose && version != "" {
				// Clean up version output
				version = strings.TrimSpace(version)
				if len(version) > 40 {
					version = version[:40] + "..."
				}
				fmt.Printf(" %s%s%s", "\033[2m", version, "\033[0m")
			}

			fmt.Println()

			if status == "not found" && verbose {
				fmt.Printf("      %sInstall: %s%s\n", "\033[2m", dep.InstallHint, "\033[0m")
			}
		}

		fmt.Println()
	}

	// Services section
	fmt.Printf("%sServices:%s\n\n", "\033[1m", "\033[0m")
	renderAgentMailStatus(t, depByName["Agent Mail"].Installed, verbose)
	fmt.Println()

	// Flywheel Tools section
	fmt.Printf("%sFlywheel Tools:%s\n\n", "\033[1m", "\033[0m")
	flywheelCount := renderFlywheelTools(t, <-flywheelInfoCh, verbose)
	fmt.Println()

	// Summary
	fmt.Printf("%s───────────────────────────────────────────────────%s\n", "\033[2m", "\033[0m")

	if missingRequired {
		fmt.Printf("%s✗%s Missing required dependencies!\n", colorize(t.Error), "\033[0m")
		return newSilentExitError(1)
	} else if agentsAvailable == 0 {
		fmt.Printf("%s⚠%s No AI agents installed. Install at least one to use ntm spawn.\n",
			colorize(t.Warning), "\033[0m")
	} else {
		fmt.Printf("%s✓%s All required dependencies installed. %d agent(s), %d flywheel tool(s) available.\n",
			colorize(t.Success), "\033[0m", agentsAvailable, flywheelCount)
	}

	fmt.Println()
	return nil
}

func depsJSONErrorResponse(err error, code, hint string) robot.RobotResponse {
	response := robot.NewErrorResponse(err, code, hint)
	response.OutputFormat = string(robot.FormatJSON)
	response.Meta = robot.NewResponseMeta("deps").WithExitCode(1)
	return response
}

func coerceDepsResponse(result any) (output.DepsResponse, error) {
	switch value := result.(type) {
	case output.DepsResponse:
		return value, nil
	case *output.DepsResponse:
		if value != nil {
			return *value, nil
		}
		return output.DepsResponse{}, fmt.Errorf("core.deps returned nil response")
	default:
		return output.DepsResponse{}, fmt.Errorf("core.deps returned unexpected type %T", result)
	}
}

func buildDepsResponse() (output.DepsResponse, error) {
	deps := defaultDepChecks()

	// Collect core dependency checks, Agent Mail liveness, and optional flywheel
	// tools concurrently while preserving deterministic output order.
	baseResults := make([]output.DependencyCheck, len(deps))
	var (
		agentMailAvailable bool
		flywheelResults    []output.DependencyCheck
		wg                 sync.WaitGroup
	)
	for i, dep := range deps {
		wg.Add(1)
		go func(idx int, dep depCheck) {
			defer wg.Done()
			status, version, path := checkDepWithPath(dep)
			baseResults[idx] = output.DependencyCheck{
				Name:      dep.Name,
				Required:  dep.Required,
				Installed: status == "found",
				Version:   version,
				Path:      path,
				Category:  dep.Category,
			}
		}(i, dep)
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		agentMailAvailable = probeAgentMailAvailable(ctx)
	}()
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		flywheelResults = flywheelDependencyChecks(ctx)
	}()
	wg.Wait()

	missingRequired := false
	for _, r := range baseResults {
		if !r.Installed && r.Required {
			missingRequired = true
			break
		}
	}

	depResults := make([]output.DependencyCheck, 0, len(baseResults)+1+len(flywheelResults))
	depResults = append(depResults, baseResults...)
	depResults = append(depResults, output.DependencyCheck{
		Name:      "Agent Mail",
		Required:  false,
		Installed: agentMailAvailable,
		Path:      agentmail.DefaultBaseURL,
		Category:  "Services",
	})
	depResults = append(depResults, flywheelResults...)

	return output.DepsResponse{
		TimestampedResponse: output.NewTimestamped(),
		AllInstalled:        !missingRequired,
		Dependencies:        depResults,
	}, nil
}

// probeAgentMailAvailable checks Agent Mail availability via the single-shot
// liveness endpoint so a down server resolves immediately instead of spending
// the full mutating-verb retry budget, escalating to IsAvailableContext only
// when the liveness route cannot decide.
func probeAgentMailAvailable(ctx context.Context) bool {
	client := newAgentMailClient("")
	if available, decided := client.QuickAvailable(ctx); decided {
		return available
	}
	return client.IsAvailableContext(ctx)
}

// renderAgentMailStatus displays Agent Mail server availability from the core.deps probe.
func renderAgentMailStatus(t theme.Theme, available, verbose bool) {
	if available {
		fmt.Printf("  %s✓%s %-15s", colorize(t.Success), "\033[0m", "Agent Mail")
		if verbose {
			fmt.Printf(" %srunning (%s)%s", "\033[2m", agentmail.DefaultBaseURL, "\033[0m")
		}
		fmt.Println()
	} else {
		fmt.Printf("  %s○%s %-15s", colorize(t.Overlay), "\033[0m", "Agent Mail")
		if verbose {
			fmt.Printf(" %snot detected (optional)%s", "\033[2m", "\033[0m")
		}
		fmt.Println()
	}
}

// renderFlywheelTools displays flywheel ecosystem tools (bv, bd, caam, etc.)
func renderFlywheelTools(t theme.Theme, infoMap map[tools.ToolName]*tools.ToolInfo, verbose bool) int {
	installedCount := 0

	for _, toolName := range flywheelDepTools {
		info, exists := infoMap[toolName]
		if !exists {
			// Tool not in registry, show as unavailable
			fmt.Printf("  %s○%s %-15s", colorize(t.Overlay), "\033[0m", flywheelToolLabel(toolName, ""))
			if verbose {
				fmt.Printf(" %snot in registry%s", "\033[2m", "\033[0m")
			}
			fmt.Println()
			continue
		}

		var statusIcon, statusColor string
		if info.Installed {
			installedCount++
			if info.Health.Healthy {
				statusIcon = "✓"
				statusColor = colorize(t.Success)
			} else {
				statusIcon = "⚠"
				statusColor = colorize(t.Warning)
			}
		} else {
			statusIcon = "○"
			statusColor = colorize(t.Overlay)
		}

		// Format tool name with required indicator
		displayName := flywheelToolLabel(info.Name, info.Path)
		if toolName == tools.ToolBV {
			displayName += " *" // Mark BV as required
		}

		fmt.Printf("  %s%s%s %-15s", statusColor, statusIcon, "\033[0m", displayName)

		if verbose && info.Installed {
			versionStr := info.Version.String()
			if versionStr == "" {
				versionStr = "installed"
			}
			if len(versionStr) > 30 {
				versionStr = versionStr[:30] + "..."
			}
			fmt.Printf(" %s%s%s", "\033[2m", versionStr, "\033[0m")

			// Show health status for unhealthy tools
			if !info.Health.Healthy && info.Health.Message != "" {
				fmt.Printf(" %s(%s)%s", "\033[33m", info.Health.Message, "\033[0m")
			}
		}

		fmt.Println()
	}

	return installedCount
}

// flywheelDepTools are the optional flywheel tools `ntm deps` reports, in
// display order. They cover the README "Requirements" list (br, bv, dcg, pt,
// cass) plus the other integrations ntm drives. ToolBD resolves `br` first and
// falls back to the legacy `bd` binary.
var flywheelDepTools = []tools.ToolName{
	tools.ToolBV,   // Required for triage
	tools.ToolBD,   // Beads issue tracker (br, legacy bd)
	tools.ToolCAAM, // Multi-account management
	tools.ToolCaut, // Quota monitoring
	tools.ToolDCG,  // Destructive command guard
	tools.ToolUBS,  // Bug scanner
	tools.ToolCASS, // Cross-agent search
	tools.ToolCM,   // CASS memory
	tools.ToolPT,   // Process triage
}

// flywheelToolLabel names a flywheel tool the way users invoke it. The beads
// adapter is registered as "bd" but prefers the `br` binary, so it is shown
// as "br" unless only the legacy `bd` binary was found.
func flywheelToolLabel(name tools.ToolName, path string) string {
	if name == tools.ToolBD {
		if path != "" && filepath.Base(path) == "bd" {
			return "bd"
		}
		return "br"
	}
	return string(name)
}

// probeFlywheelToolInfo probes only flywheelDepTools (concurrently) instead of
// every registered adapter.
func probeFlywheelToolInfo(ctx context.Context) map[tools.ToolName]*tools.ToolInfo {
	infoMap := make(map[tools.ToolName]*tools.ToolInfo, len(flywheelDepTools))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range flywheelDepTools {
		adapter, ok := tools.Get(name)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(name tools.ToolName, adapter tools.Adapter) {
			defer wg.Done()
			info, _ := adapter.Info(ctx)
			if info == nil {
				return
			}
			mu.Lock()
			infoMap[name] = info
			mu.Unlock()
		}(name, adapter)
	}
	wg.Wait()
	return infoMap
}

// flywheelDependencyChecks reports flywheelDepTools as optional dependency
// rows for `ntm deps --json`. It only detects the binary and reads its
// version; health probes stay in the text output.
func flywheelDependencyChecks(ctx context.Context) []output.DependencyCheck {
	rows := make([]output.DependencyCheck, len(flywheelDepTools))
	var wg sync.WaitGroup
	for i, name := range flywheelDepTools {
		rows[i] = output.DependencyCheck{
			Name:     flywheelToolLabel(name, ""),
			Category: "Flywheel Tools",
		}
		adapter, ok := tools.Get(name)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(row *output.DependencyCheck, name tools.ToolName, adapter tools.Adapter) {
			defer wg.Done()
			path, installed := adapter.Detect()
			if !installed {
				return
			}
			row.Installed = true
			row.Path = path
			row.Name = flywheelToolLabel(name, path)
			if v, err := adapter.Version(ctx); err == nil {
				row.Version = sanitizeDependencyVersion(v.String())
			}
		}(&rows[i], name, adapter)
	}
	wg.Wait()
	return rows
}

type depVersionCacheKey struct {
	path        string
	modUnix     int64
	size        int64
	versionArgs string
}

type depVersionCacheEntry struct {
	version   string
	checkedAt time.Time
}

const (
	depVersionCacheTTL = 15 * time.Second
	maxDepVersionCache = 128
)

var (
	depVersionCacheMu sync.RWMutex
	depVersionCache   = map[depVersionCacheKey]depVersionCacheEntry{}
)

// checkDepWithPath checks if a dependency is installed and returns its status, version, and path
func checkDepWithPath(dep depCheck) (status string, version string, path string) {
	// Check if command exists
	foundPath, err := exec.LookPath(dep.Command)
	if err != nil {
		return "not found", "", ""
	}

	path = foundPath

	// Get version if possible
	if len(dep.VersionArgs) > 0 {
		var cacheKey depVersionCacheKey
		cacheable := false
		tmpRoot := filepath.Clean(os.TempDir())
		cleanPath := filepath.Clean(foundPath)
		if !strings.HasPrefix(cleanPath, tmpRoot+string(os.PathSeparator)) && cleanPath != tmpRoot {
			if info, statErr := os.Stat(foundPath); statErr == nil && info.Mode().IsRegular() {
				cacheKey = depVersionCacheKey{
					path:        cleanPath,
					modUnix:     info.ModTime().UnixNano(),
					size:        info.Size(),
					versionArgs: strings.Join(dep.VersionArgs, "\x00"),
				}
				cacheable = true
				depVersionCacheMu.RLock()
				entry, ok := depVersionCache[cacheKey]
				depVersionCacheMu.RUnlock()
				if ok && time.Since(entry.checkedAt) < depVersionCacheTTL {
					return "found", entry.version, path
				}
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), depVersionTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, foundPath, dep.VersionArgs...)
		cmd.WaitDelay = 2 * time.Second
		out, err := cmd.CombinedOutput()

		// Some commands print version info but exit non-zero (or get killed on timeout).
		// Return any output we got, otherwise fall back to "installed". Only cache
		// clean zero-exit probes so transient errors/timeouts never poison the cache.
		if s := sanitizeDependencyVersion(string(out)); s != "" {
			if cacheable && err == nil && ctx.Err() == nil {
				depVersionCacheMu.Lock()
				if len(depVersionCache) >= maxDepVersionCache {
					clear(depVersionCache)
				}
				depVersionCache[cacheKey] = depVersionCacheEntry{version: s, checkedAt: time.Now()}
				depVersionCacheMu.Unlock()
			}
			return "found", s, path
		}
		return "found", "", path
	}

	return "found", "", path
}

// sanitizeDependencyVersion constrains untrusted child-process output before it
// reaches JSON, logs, or a terminal. Version commands should be one short line;
// ANSI/OSC sequences, controls, bidi formatting, and whitespace runs have no
// legitimate role in that contract.
func sanitizeDependencyVersion(raw string) string {
	clean := statuspkg.StripANSI(raw)
	runes := make([]rune, 0, len(clean))
	spacePending := false
	for _, r := range clean {
		if unicode.IsSpace(r) {
			spacePending = len(runes) > 0
			continue
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			continue
		}
		if spacePending {
			runes = append(runes, ' ')
			spacePending = false
		}
		runes = append(runes, r)
	}

	const maxVersionRunes = 240
	if len(runes) > maxVersionRunes {
		return string(runes[:maxVersionRunes]) + "..."
	}
	return string(runes)
}
