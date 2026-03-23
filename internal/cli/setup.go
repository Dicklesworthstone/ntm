package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

func newSetupCmd() *cobra.Command {
	var installWrappers bool
	var installHooks bool
	var force bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Initialize NTM for a project",
		Long: `Initialize NTM orchestration for the current project.

Creates the .ntm/ directory structure with:
  - .ntm/config.yaml    - Project configuration
  - .ntm/policy.yaml    - Command safety policy
  - .ntm/logs/          - Agent log directory
  - .ntm/pids/          - Daemon PID files
  - .ntm/cache/         - Temporary cache files

Optional:
  --wrappers    Install PATH wrappers for git/rm (safety interception)
  --hooks       Install Claude Code PreToolUse hooks

This is the recommended first step for using NTM with a new project.`,
		Aliases: []string{"project-init"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(installWrappers, installHooks, force)
		},
	}

	cmd.Flags().BoolVarP(&installWrappers, "wrappers", "w", false, "Install PATH wrappers for git and rm")
	cmd.Flags().BoolVar(&installHooks, "hooks", false, "Install Claude Code PreToolUse hooks")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing files")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show setup status and core tool availability",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetupStatus()
		},
	}
	cmd.AddCommand(statusCmd)

	return cmd
}

// SetupResponse is the JSON output for setup command.
type SetupResponse struct {
	output.TimestampedResponse
	Success           bool     `json:"success"`
	ProjectPath       string   `json:"project_path"`
	NTMDir            string   `json:"ntm_dir"`
	CreatedDirs       []string `json:"created_dirs"`
	CreatedFiles      []string `json:"created_files"`
	WrappersInstalled bool     `json:"wrappers_installed,omitempty"`
	HooksInstalled    bool     `json:"hooks_installed,omitempty"`
	BeadsInitialized  bool     `json:"beads_initialized,omitempty"`
	BeadsWarning      string   `json:"beads_warning,omitempty"`
}

func runSetup(installWrappers, installHooks, force bool) error {
	// Get current directory
	projectPath, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	ntmDir := filepath.Join(projectPath, ".ntm")

	// Check if already initialized
	if fileExists(ntmDir) && !force {
		if IsJSONOutput() {
			return output.PrintJSON(SetupResponse{
				TimestampedResponse: output.NewTimestamped(),
				Success:             true,
				ProjectPath:         projectPath,
				NTMDir:              ntmDir,
				CreatedDirs:         []string{},
				CreatedFiles:        []string{},
			})
		}

		okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
		fmt.Println()
		fmt.Printf("  %s NTM already initialized at %s\n", okStyle.Render("✓"), ntmDir)
		fmt.Printf("    Use --force to reinitialize\n")
		fmt.Println()
		return nil
	}

	var createdDirs []string
	var createdFiles []string

	// Create directory structure
	dirs := []string{
		".ntm",
		".ntm/logs",
		".ntm/pids",
		".ntm/cache",
		".ntm/bin",
	}

	for _, dir := range dirs {
		dirPath := filepath.Join(projectPath, dir)
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
		createdDirs = append(createdDirs, dir)
	}

	// Write default config
	configPath := filepath.Join(ntmDir, "config.yaml")
	if !fileExists(configPath) || force {
		if err := writeDefaultConfig(configPath); err != nil {
			return fmt.Errorf("writing config: %w", err)
		}
		createdFiles = append(createdFiles, ".ntm/config.yaml")
	}

	// Write default policy
	policyPath := filepath.Join(ntmDir, "policy.yaml")
	if !fileExists(policyPath) || force {
		if err := writeDefaultSetupPolicy(policyPath); err != nil {
			return fmt.Errorf("writing policy: %w", err)
		}
		createdFiles = append(createdFiles, ".ntm/policy.yaml")
	}

	// Write AGENTS.md
	agentsCreated, err := writeAgentsFile(projectPath, force)
	if err != nil {
		return err
	}
	if agentsCreated {
		createdFiles = append(createdFiles, "AGENTS.md")
	}

	// Add .ntm to .gitignore if it exists
	gitignorePath := filepath.Join(projectPath, ".gitignore")
	if fileExists(gitignorePath) {
		if err := ensureGitignoreEntry(gitignorePath, ".ntm/"); err != nil {
			// Non-fatal, just warn
			fmt.Fprintf(os.Stderr, "Warning: could not update .gitignore: %v\n", err)
		}
	}

	beadsInitialized, beadsWarning, err := initBeadsIfAvailable(projectPath)
	if err != nil {
		return fmt.Errorf("initializing beads: %w", err)
	}

	// Install wrappers if requested
	wrappersInstalled := false
	if installWrappers {
		if err := runSafetyInstall(force); err != nil {
			return fmt.Errorf("installing wrappers: %w", err)
		}
		wrappersInstalled = true
	}

	// Install hooks if requested
	hooksInstalled := false
	if installHooks {
		// Reuse the safety install which includes hooks
		if !installWrappers { // Only if not already installed above
			if err := runSafetyInstall(force); err != nil {
				return fmt.Errorf("installing hooks: %w", err)
			}
		}
		hooksInstalled = true
	}

	if IsJSONOutput() {
		return output.PrintJSON(SetupResponse{
			TimestampedResponse: output.NewTimestamped(),
			Success:             true,
			ProjectPath:         projectPath,
			NTMDir:              ntmDir,
			CreatedDirs:         createdDirs,
			CreatedFiles:        createdFiles,
			WrappersInstalled:   wrappersInstalled,
			HooksInstalled:      hooksInstalled,
			BeadsInitialized:    beadsInitialized,
			BeadsWarning:        beadsWarning,
		})
	}

	// TUI output
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render("NTM Project Setup"))
	fmt.Println()

	fmt.Printf("  %s Created .ntm/ directory structure\n", okStyle.Render("✓"))
	for _, dir := range createdDirs {
		fmt.Printf("    %s %s/\n", mutedStyle.Render("•"), dir)
	}
	fmt.Println()

	fmt.Printf("  %s Created configuration files\n", okStyle.Render("✓"))
	for _, file := range createdFiles {
		fmt.Printf("    %s %s\n", mutedStyle.Render("•"), file)
	}
	fmt.Println()

	if wrappersInstalled {
		fmt.Printf("  %s Installed PATH wrappers\n", okStyle.Render("✓"))
	}
	if hooksInstalled {
		fmt.Printf("  %s Installed Claude Code hooks\n", okStyle.Render("✓"))
	}
	if beadsInitialized {
		fmt.Printf("  %s Initialized beads tracking (.beads/)\n", okStyle.Render("✓"))
	}
	if beadsWarning != "" {
		warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
		fmt.Printf("  %s %s\n", warnStyle.Render("⚠"), beadsWarning)
	}

	fmt.Printf("  %s\n", mutedStyle.Render("Project ready for NTM orchestration"))
	fmt.Println()

	// Show next steps
	fmt.Println(mutedStyle.Render("  Next steps:"))
	fmt.Println(mutedStyle.Render("    1. Review .ntm/config.yaml for project settings"))
	fmt.Println(mutedStyle.Render("    2. Review .ntm/policy.yaml for safety rules"))
	fmt.Println(mutedStyle.Render("    3. Run 'ntm quick' to start orchestrating"))
	fmt.Println()

	return nil
}

func runSetupStatus() error {
	status, err := robot.GetACFSStatus()
	if err != nil {
		return err
	}

	if IsJSONOutput() {
		return output.PrintJSON(status)
	}

	fmt.Println()
	acfsLine := "missing"
	if status.ACFSAvailable {
		acfsLine = "installed"
		if status.ACFSVersion != "" {
			acfsLine += " (" + status.ACFSVersion + ")"
		}
	} else if status.Error != "" {
		acfsLine = "missing"
	}
	fmt.Printf("ACFS: %s\n", acfsLine)
	if !status.ACFSAvailable && status.Hint != "" {
		fmt.Printf("  Hint: %s\n", status.Hint)
	}

	if len(status.Tools) == 0 {
		fmt.Println("Tools: none")
		fmt.Println()
		return nil
	}

	fmt.Println("Tools:")
	keys := make([]string, 0, len(status.Tools))
	for key := range status.Tools {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		tool := status.Tools[key]
		label := "missing"
		if tool.Installed {
			label = "ok"
		} else if tool.Required {
			label = "missing (required)"
		}

		details := ""
		if tool.Version != "" {
			details = tool.Version
		}
		if tool.Path != "" {
			if details != "" {
				details += " "
			}
			details += tool.Path
		}
		fmt.Printf("  %-5s %-16s %s\n", key, label, details)
		if !tool.Installed && tool.Hint != "" {
			fmt.Printf("    Hint: %s\n", tool.Hint)
		}
	}

	fmt.Println()
	return nil
}

func writeDefaultConfig(path string) error {
	content := `# NTM Project Configuration
# Generated by 'ntm setup'

# Session defaults
session:
  default_agents: 2
  default_layout: tiled
  auto_create_pane: true

# Agent defaults
agents:
  claude: "claude --dangerously-skip-permissions"
  codex: "codex"
  gemini: "gemini"

# Dashboard settings
dashboard:
  refresh_interval: 2s
  show_activity: true
  show_health: true

# Logging
logging:
  level: info
  file: .ntm/logs/ntm.log
  max_size_mb: 10
  max_backups: 3
`
	return util.AtomicWriteFile(path, []byte(content), 0644)
}

func writeDefaultSetupPolicy(path string) error {
	content := `# NTM Safety Policy
# Generated by 'ntm setup'
version: 1

# Automation settings
automation:
  auto_commit: true        # Allow automatic git commits
  auto_push: false         # Require explicit git push
  force_release: approval  # "never", "approval", or "auto"

# Explicitly allowed patterns (checked first)
allowed:
  - pattern: 'git\s+push\s+.*--force-with-lease'
    reason: "Safe force push with lease protection"
  - pattern: 'git\s+reset\s+--soft'
    reason: "Soft reset preserves changes"
  - pattern: 'git\s+reset\s+HEAD~?\d*$'
    reason: "Mixed reset preserves working directory"

# Blocked patterns (dangerous operations)
blocked:
  - pattern: 'git\s+reset\s+--hard'
    reason: "Hard reset loses uncommitted changes"
  - pattern: 'git\s+clean\s+-fd'
    reason: "Removes untracked files permanently"
  - pattern: 'git\s+push\s+.*--force'
    reason: "Force push can overwrite remote history"
  - pattern: 'git\s+push\s+.*\s-f(\s|$)'
    reason: "Force push can overwrite remote history"
  - pattern: 'rm\s+-rf\s+/$'
    reason: "Recursive delete of root is catastrophic"
  - pattern: 'rm\s+-rf\s+~'
    reason: "Recursive delete of home directory"
  - pattern: 'rm\s+-rf\s+\*'
    reason: "Recursive delete of everything"
  - pattern: 'git\s+branch\s+-D'
    reason: "Force delete branch loses unmerged work"
  - pattern: 'git\s+stash\s+drop'
    reason: "Dropping stash loses saved work"
  - pattern: 'git\s+stash\s+clear'
    reason: "Clearing all stashes loses saved work"

# Approval required patterns (need confirmation)
approval_required:
  - pattern: 'git\s+rebase\s+-i'
    reason: "Interactive rebase rewrites history"
  - pattern: 'git\s+commit\s+--amend'
    reason: "Amending rewrites history"
  - pattern: 'rm\s+-rf\s+\S'
    reason: "Recursive force delete"
  - pattern: 'force_release'
    reason: "Force release another agent's reservation"
    slb: true  # Requires two-person approval
`
	return util.AtomicWriteFile(path, []byte(content), 0644)
}

func ensureGitignoreEntry(gitignorePath, entry string) error {
	if entry == "" {
		return nil // Nothing to add
	}

	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		return err
	}

	// Check if entry already exists (with or without trailing slash)
	lines := splitLines(string(content))
	entryWithoutSlash := strings.TrimSuffix(entry, "/")
	for _, line := range lines {
		lineWithoutSlash := strings.TrimSuffix(line, "/")
		if lineWithoutSlash == entryWithoutSlash {
			return nil // Already present
		}
	}

	// Append entry
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Add newline if file doesn't end with one
	if len(content) > 0 && content[len(content)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}

	if _, err := f.WriteString(entry + "\n"); err != nil {
		return err
	}

	return nil
}

// splitLines splits a string into lines, handling both Unix (\n) and Windows (\r\n) line endings.
// Trailing newlines do not create empty elements; an empty string returns an empty slice.
func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	// Remove trailing newline to avoid empty last element
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r") // Handle lone \r at end

	var lines []string
	for _, line := range strings.Split(s, "\n") {
		// Handle Windows line endings by trimming trailing \r
		lines = append(lines, strings.TrimSuffix(line, "\r"))
	}
	return lines
}

func initBeadsIfAvailable(projectPath string) (bool, string, error) {
	beadsPath := filepath.Join(projectPath, ".beads")
	if fileExists(beadsPath) {
		return false, "", nil
	}

	if _, err := exec.LookPath("br"); err != nil {
		return false, "beads_rust (br) not found in PATH; install beads_rust to enable .beads tracking", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "br", "init", "--json")
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = projectPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return false, "", fmt.Errorf("br init timed out")
		}
		return false, "", fmt.Errorf("br init failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return true, "", nil
}

func registerAgentMailProject(projectPath, configPath string) (bool, string, error) {
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		return false, "", fmt.Errorf("resolving project path: %w", err)
	}

	client := newAgentMailClient(absPath)
	registered := false
	warning := ""

	if !client.IsAvailable() {
		warning = "server not available"
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		if _, err := client.EnsureProject(ctx, absPath); err != nil {
			warning = fmt.Sprintf("ensure_project failed: %v", err)
		} else {
			registered = true
		}
	}

	updates := map[string]string{
		"agent_mail":             "true",
		"agent_mail_project_key": strconv.Quote(absPath),
		"agent_mail_registered":  strconv.FormatBool(registered),
	}
	if registered {
		updates["agent_mail_registered_at"] = strconv.Quote(time.Now().UTC().Format(time.RFC3339))
	}

	if err := updateTomlSection(configPath, "integrations", updates); err != nil {
		return registered, warning, err
	}

	return registered, warning, nil
}

func updateTomlSection(path, section string, updates map[string]string) error {
	if len(updates) == 0 {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}

	lines := splitLines(string(data))
	sectionHeader := "[" + section + "]"

	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == sectionHeader {
			start = i
			break
		}
	}

	keys := make([]string, 0, len(updates))
	for k := range updates {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if start == -1 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, sectionHeader)
		for _, key := range keys {
			lines = append(lines, fmt.Sprintf("%s = %s", key, updates[key]))
		}
		return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}

	existing := make(map[string]int)
	for i := start + 1; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if idx := strings.Index(trimmed, "="); idx != -1 {
			key := strings.TrimSpace(trimmed[:idx])
			if key != "" {
				existing[key] = i
			}
		}
	}

	for _, key := range keys {
		line := fmt.Sprintf("%s = %s", key, updates[key])
		if idx, ok := existing[key]; ok {
			lines[idx] = line
		} else {
			lines = append(lines[:end], append([]string{line}, lines[end:]...)...)
			end++
		}
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}
