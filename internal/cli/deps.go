package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

Also checks for recommended tools like fzf.

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

func init() {
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
			Path:   "/deps",
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
	kernel.MustRegisterHandler("core.deps", func(ctx context.Context, _ any) (any, error) {
		return buildDepsResponse()
	})
}

// defaultDepChecks returns the built-in probes followed by one probe per
// agent plugin declared under <config>/agents (ntm#260), so a plugin agent
// is reported — and counted as available — alongside the built-ins.
func defaultDepChecks() []depCheck {
	checks := builtinDepChecks()
	return append(checks, pluginDepChecks(registerAgentPluginTypes(pluginAgentsDirForArgs(os.Args[1:])))...)
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
	checkAgentMail(t, verbose)
	fmt.Println()

	// Flywheel Tools section
	fmt.Printf("%sFlywheel Tools:%s\n\n", "\033[1m", "\033[0m")
	flywheelCount := checkFlywheelTools(t, verbose)
	fmt.Println()

	// Summary
	fmt.Printf("%s───────────────────────────────────────────────────%s\n", "\033[2m", "\033[0m")

	if missingRequired {
		fmt.Printf("%s✗%s Missing required dependencies!\n", colorize(t.Error), "\033[0m")
		os.Exit(1)
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

	// Collect all dependency statuses
	var depResults []output.DependencyCheck
	missingRequired := false

	for _, dep := range deps {
		status, version, path := checkDepWithPath(dep)
		installed := status == "found"

		if !installed && dep.Required {
			missingRequired = true
		}

		depResults = append(depResults, output.DependencyCheck{
			Name:      dep.Name,
			Required:  dep.Required,
			Installed: installed,
			Version:   version,
			Path:      path,
		})
	}

	// Add Agent Mail as a service check
	client := newAgentMailClient("")
	agentMailAvailable := client.IsAvailable()
	depResults = append(depResults, output.DependencyCheck{
		Name:      "Agent Mail",
		Required:  false,
		Installed: agentMailAvailable,
		Path:      agentmail.DefaultBaseURL,
	})

	return output.DepsResponse{
		TimestampedResponse: output.NewTimestamped(),
		AllInstalled:        !missingRequired,
		Dependencies:        depResults,
	}, nil
}

// checkAgentMail checks Agent Mail server availability
func checkAgentMail(t theme.Theme, verbose bool) {
	client := newAgentMailClient("")

	if client.IsAvailable() {
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

// checkFlywheelTools checks and displays flywheel ecosystem tools (bv, bd, caam, etc.)
func checkFlywheelTools(t theme.Theme, verbose bool) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	allInfo := tools.GetAllInfo(ctx)
	installedCount := 0

	// Key flywheel tools to display (in priority order)
	keyTools := []tools.ToolName{
		tools.ToolBV,   // Required for triage
		tools.ToolBD,   // Beads issue tracker
		tools.ToolCAAM, // Multi-account management
		tools.ToolCaut, // Quota monitoring
		tools.ToolDCG,  // Destructive command guard
		tools.ToolUBS,  // Bug scanner
		tools.ToolCASS, // Cross-agent search
		tools.ToolCM,   // CASS memory
	}

	// Build a map for quick lookup
	infoMap := make(map[tools.ToolName]*tools.ToolInfo)
	for _, info := range allInfo {
		if info != nil {
			infoMap[info.Name] = info
		}
	}

	for _, toolName := range keyTools {
		info, exists := infoMap[toolName]
		if !exists {
			// Tool not in registry, show as unavailable
			fmt.Printf("  %s○%s %-15s", colorize(t.Overlay), "\033[0m", string(toolName))
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
		displayName := string(info.Name)
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
		ctx, cancel := context.WithTimeout(context.Background(), depVersionTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, dep.Command, dep.VersionArgs...)
		cmd.WaitDelay = 2 * time.Second
		out, err := cmd.CombinedOutput()

		// Some commands print version info but exit non-zero (or get killed on timeout).
		// Return any output we got, otherwise fall back to "installed".
		if s := sanitizeDependencyVersion(string(out)); s != "" {
			return "found", s, path
		}
		_ = err
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
