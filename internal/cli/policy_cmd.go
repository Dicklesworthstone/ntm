package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/policy"
)

func newPolicyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Manage NTM policy configuration",
		Long: `Manage NTM's policy configuration for command protection and automation.

The policy system controls:
  - Blocked commands (dangerous operations)
  - Approval-required commands (need confirmation)
  - Allowed commands (explicitly safe)
  - Automation settings (auto-push, auto-commit, force-release)
  - SLB (two-person approval) requirements

Use 'ntm policy show' to see the current policy.
Use 'ntm policy validate' to check policy file syntax.
Use 'ntm policy reset' to reset to defaults.
Use 'ntm policy edit' to open in your editor.`,
	}

	cmd.AddCommand(
		newPolicyShowCmd(),
		newPolicyValidateCmd(),
		newPolicyResetCmd(),
		newPolicyEditCmd(),
		newPolicyAutomationCmd(),
	)

	return cmd
}

func newPolicyShowCmd() *cobra.Command {
	var showAll bool

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Display current policy configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyShow(showAll)
		},
	}

	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show all rules including patterns")

	return cmd
}

// PolicyShowResponse is the JSON output for policy show.
type PolicyShowResponse struct {
	output.TimestampedResponse
	Version    int                     `json:"version"`
	PolicyPath string                  `json:"policy_path,omitempty"`
	IsDefault  bool                    `json:"is_default"`
	Stats      PolicyStats             `json:"stats"`
	Automation policy.AutomationConfig `json:"automation"`
	Rules      *PolicyRulesDetail      `json:"rules,omitempty"`
}

// PolicyStats contains rule counts.
type PolicyStats struct {
	Blocked  int `json:"blocked"`
	Approval int `json:"approval"`
	Allowed  int `json:"allowed"`
	SLBRules int `json:"slb_rules"`
}

// PolicyRulesDetail contains detailed rule information.
type PolicyRulesDetail struct {
	Blocked          []RuleSummary `json:"blocked,omitempty"`
	ApprovalRequired []RuleSummary `json:"approval_required,omitempty"`
	Allowed          []RuleSummary `json:"allowed,omitempty"`
}

// RuleSummary is a simplified rule representation.
type RuleSummary struct {
	Pattern string `json:"pattern"`
	Reason  string `json:"reason,omitempty"`
	SLB     bool   `json:"slb,omitempty"`
}

func runPolicyShow(showAll bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}
	policyPath := filepath.Join(home, ".ntm", "policy.yaml")

	p, err := policy.LoadOrDefault()
	if err != nil {
		return fmt.Errorf("loading policy: %w", err)
	}

	isDefault := !fileExists(policyPath)
	blocked, approval, allowed := p.Stats()

	// Count SLB rules
	slbCount := 0
	for _, r := range p.ApprovalRequired {
		if r.SLB {
			slbCount++
		}
	}

	if IsJSONOutput() {
		resp := PolicyShowResponse{
			TimestampedResponse: output.NewTimestamped(),
			Version:             p.Version,
			IsDefault:           isDefault,
			Automation:          p.Automation,
			Stats: PolicyStats{
				Blocked:  blocked,
				Approval: approval,
				Allowed:  allowed,
				SLBRules: slbCount,
			},
		}
		if !isDefault {
			resp.PolicyPath = policyPath
		}

		if showAll {
			resp.Rules = &PolicyRulesDetail{
				Blocked:          toRuleSummaries(p.Blocked),
				ApprovalRequired: toRuleSummaries(p.ApprovalRequired),
				Allowed:          toRuleSummaries(p.Allowed),
			}
		}

		return output.PrintJSON(resp)
	}

	// TUI output
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))

	fmt.Println()
	fmt.Println(titleStyle.Render("NTM Policy Configuration"))
	fmt.Println()

	// Policy source
	if isDefault {
		fmt.Printf("  %s %s\n", labelStyle.Render("Source:"), mutedStyle.Render("Default policy (no custom file)"))
	} else {
		fmt.Printf("  %s %s\n", labelStyle.Render("Source:"), valueStyle.Render(policyPath))
	}
	fmt.Printf("  %s %s\n", labelStyle.Render("Version:"), valueStyle.Render(fmt.Sprintf("%d", p.Version)))
	fmt.Println()

	// Rule counts
	fmt.Println(labelStyle.Render("  Rules:"))
	fmt.Printf("    %s blocked patterns\n", errorStyle.Render(fmt.Sprintf("%d", blocked)))
	fmt.Printf("    %s approval-required patterns", warnStyle.Render(fmt.Sprintf("%d", approval)))
	if slbCount > 0 {
		fmt.Printf(" (%s require SLB)", mutedStyle.Render(fmt.Sprintf("%d", slbCount)))
	}
	fmt.Println()
	fmt.Printf("    %s explicitly allowed patterns\n", okStyle.Render(fmt.Sprintf("%d", allowed)))
	fmt.Println()

	// Automation settings
	fmt.Println(labelStyle.Render("  Automation:"))
	fmt.Printf("    Auto-commit:   %s\n", formatBool(p.Automation.AutoCommit))
	fmt.Printf("    Auto-push:     %s\n", formatBool(p.Automation.AutoPush))
	fmt.Printf("    Force-release: %s\n", valueStyle.Render(p.ForceReleasePolicy()))
	fmt.Println()

	// Show detailed rules if requested
	if showAll {
		printRuleSection("Blocked", p.Blocked, errorStyle, mutedStyle)
		printRuleSection("Approval Required", p.ApprovalRequired, warnStyle, mutedStyle)
		printRuleSection("Allowed", p.Allowed, okStyle, mutedStyle)
	} else {
		fmt.Printf("  %s\n", mutedStyle.Render("Use --all to see detailed rules"))
	}

	fmt.Println()
	return nil
}

func toRuleSummaries(rules []policy.Rule) []RuleSummary {
	result := make([]RuleSummary, len(rules))
	for i, r := range rules {
		result[i] = RuleSummary{
			Pattern: r.Pattern,
			Reason:  r.Reason,
			SLB:     r.SLB,
		}
	}
	return result
}

func printRuleSection(title string, rules []policy.Rule, titleStyle, mutedStyle lipgloss.Style) {
	if len(rules) == 0 {
		return
	}

	fmt.Printf("  %s:\n", titleStyle.Render(title))
	for _, r := range rules {
		fmt.Printf("    • %s\n", r.Pattern)
		if r.Reason != "" {
			fmt.Printf("      %s\n", mutedStyle.Render(r.Reason))
		}
		if r.SLB {
			fmt.Printf("      %s\n", mutedStyle.Render("[Requires SLB two-person approval]"))
		}
	}
	fmt.Println()
}

func formatBool(b bool) string {
	if b {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("enabled")
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("disabled")
}

func newPolicyValidateCmd() *cobra.Command {
	var policyFile string

	cmd := &cobra.Command{
		Use:   "validate [file]",
		Short: "Validate policy file syntax",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				policyFile = args[0]
			}
			return runPolicyValidate(policyFile)
		},
	}

	return cmd
}

// PolicyValidateResponse is the JSON output for policy validate.
type PolicyValidateResponse struct {
	output.TimestampedResponse
	Valid      bool     `json:"valid"`
	PolicyPath string   `json:"policy_path"`
	Errors     []string `json:"errors,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

func runPolicyValidate(policyFile string) error {
	// Determine policy file path
	if policyFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("getting home directory: %w", err)
		}
		policyFile = filepath.Join(home, ".ntm", "policy.yaml")
	}

	var errors []string
	var warnings []string

	// Check if file exists
	if !fileExists(policyFile) {
		errors = append(errors, "Policy file does not exist")
		return outputValidationResult(policyFile, false, errors, warnings)
	}

	// Try to load and parse
	data, err := os.ReadFile(policyFile)
	if err != nil {
		errors = append(errors, fmt.Sprintf("Cannot read file: %v", err))
		return outputValidationResult(policyFile, false, errors, warnings)
	}

	// Parse YAML
	var p policy.Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		errors = append(errors, fmt.Sprintf("Invalid YAML: %v", err))
		return outputValidationResult(policyFile, false, errors, warnings)
	}

	// Check for warnings before Validate() mutates the policy
	if p.Version == 0 {
		warnings = append(warnings, "No version specified, defaulting to 1")
	}

	// Validate the policy
	if err := p.Validate(); err != nil {
		errors = append(errors, err.Error())
		return outputValidationResult(policyFile, false, errors, warnings)
	}

	blocked, approval, allowed := p.Stats()
	if blocked == 0 && approval == 0 && allowed == 0 {
		warnings = append(warnings, "Policy has no rules defined")
	}

	return outputValidationResult(policyFile, true, errors, warnings)
}

func outputValidationResult(policyPath string, valid bool, errors, warnings []string) error {
	if IsJSONOutput() {
		return output.PrintJSON(PolicyValidateResponse{
			TimestampedResponse: output.NewTimestamped(),
			Valid:               valid,
			PolicyPath:          policyPath,
			Errors:              errors,
			Warnings:            warnings,
		})
	}

	// TUI output
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Policy Validation"))
	fmt.Printf("  %s\n", mutedStyle.Render(policyPath))
	fmt.Println()

	if valid {
		fmt.Printf("  %s Policy is valid\n", okStyle.Render("✓"))
	} else {
		fmt.Printf("  %s Policy is invalid\n", errorStyle.Render("✗"))
	}

	for _, e := range errors {
		fmt.Printf("    %s %s\n", errorStyle.Render("•"), e)
	}

	for _, w := range warnings {
		fmt.Printf("    %s %s\n", warnStyle.Render("⚠"), w)
	}

	fmt.Println()

	if !valid {
		os.Exit(1)
	}

	return nil
}

func newPolicyResetCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset policy to defaults",
		Long: `Reset the policy configuration to default values.

This will overwrite any custom policy file with the default policy.
Use --force to skip the confirmation prompt.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyReset(force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")

	return cmd
}

func runPolicyReset(force bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}

	policyPath := filepath.Join(home, ".ntm", "policy.yaml")
	exists := fileExists(policyPath)

	if !force && exists && !IsJSONOutput() {
		fmt.Printf("This will overwrite %s with default policy. Continue? [y/N] ", policyPath)
		var response string
		fmt.Scanln(&response)
		if !strings.HasPrefix(strings.ToLower(response), "y") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Create directory if needed
	if err := os.MkdirAll(filepath.Dir(policyPath), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	// Write default policy with new automation section
	content := generateDefaultPolicyYAML()
	if err := os.WriteFile(policyPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing policy file: %w", err)
	}

	if IsJSONOutput() {
		return output.PrintJSON(map[string]interface{}{
			"success":     true,
			"policy_path": policyPath,
			"action":      "reset",
		})
	}

	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	fmt.Println()
	fmt.Printf("  %s Policy reset to defaults: %s\n", okStyle.Render("✓"), policyPath)
	fmt.Println()

	return nil
}

func generateDefaultPolicyYAML() string {
	return `# NTM Policy Configuration
# Version 1 - with automation settings and SLB support
version: 1

# Automation settings
automation:
  auto_commit: true        # Allow automatic git commits
  auto_push: false         # Require explicit git push
  force_release: approval  # "never", "approval", or "auto" for file reservation force-release

# Explicitly allowed patterns (checked first - highest priority)
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
  - pattern: 'git\s+push\s+-f(\s|$)'
    reason: "Force push can overwrite remote history"
  - pattern: 'rm\s+-rf\s+/$'
    reason: "Recursive delete of root is catastrophic"
  - pattern: 'rm\s+-rf\s+~'
    reason: "Recursive delete of home directory"
  - pattern: 'rm\s+-rf\s+\*'
    reason: "Recursive delete of everything in current directory"
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
}

func newPolicyEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open policy file in editor",
		Long: `Open the policy configuration file in your default editor.

Uses $EDITOR environment variable, falling back to vim if not set.
Creates a default policy file if one doesn't exist.`,
		RunE: runPolicyEdit,
	}
}

func runPolicyEdit(cmd *cobra.Command, args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}

	policyPath := filepath.Join(home, ".ntm", "policy.yaml")

	// Create default if doesn't exist
	if !fileExists(policyPath) {
		if err := os.MkdirAll(filepath.Dir(policyPath), 0755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}
		content := generateDefaultPolicyYAML()
		if err := os.WriteFile(policyPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("writing default policy: %w", err)
		}
	}

	// Get editor
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}

	// Open in editor
	editCmd := exec.Command(editor, policyPath)
	editCmd.Stdin = os.Stdin
	editCmd.Stdout = os.Stdout
	editCmd.Stderr = os.Stderr

	if err := editCmd.Run(); err != nil {
		return fmt.Errorf("running editor: %w", err)
	}

	// Validate after edit
	p, err := policy.Load(policyPath)
	if err != nil {
		fmt.Printf("\n⚠️  Warning: Policy file has errors: %v\n", err)
		fmt.Println("   The file was saved but may not work correctly.")
		return nil
	}

	if err := p.Validate(); err != nil {
		fmt.Printf("\n⚠️  Warning: Policy validation failed: %v\n", err)
		return nil
	}

	if !IsJSONOutput() {
		okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
		fmt.Printf("\n  %s Policy saved and validated successfully\n\n", okStyle.Render("✓"))
	}

	return nil
}

func newPolicyAutomationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "automation",
		Short: "Show or modify automation settings",
		Long: `View or modify the automation settings in the policy.

Automation settings control:
  - auto_commit: Whether to allow automatic git commits
  - auto_push: Whether to allow automatic git pushes
  - force_release: Policy for force-releasing file reservations
    - "never": Never allow force release
    - "approval": Require approval before force release (default)
    - "auto": Allow automatic force release

Use flags to modify settings, or run without flags to view current settings.`,
		RunE: runPolicyAutomation,
	}

	cmd.Flags().Bool("auto-commit", false, "Enable auto-commit")
	cmd.Flags().Bool("no-auto-commit", false, "Disable auto-commit")
	cmd.Flags().Bool("auto-push", false, "Enable auto-push")
	cmd.Flags().Bool("no-auto-push", false, "Disable auto-push")
	cmd.Flags().String("force-release", "", "Set force-release policy (never|approval|auto)")

	return cmd
}

// AutomationResponse is the JSON output for automation settings.
type AutomationResponse struct {
	output.TimestampedResponse
	AutoCommit   bool   `json:"auto_commit"`
	AutoPush     bool   `json:"auto_push"`
	ForceRelease string `json:"force_release"`
	Modified     bool   `json:"modified,omitempty"`
}

func runPolicyAutomation(cmd *cobra.Command, args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}
	policyPath := filepath.Join(home, ".ntm", "policy.yaml")

	// Check if any flags were set
	autoCommit, _ := cmd.Flags().GetBool("auto-commit")
	noAutoCommit, _ := cmd.Flags().GetBool("no-auto-commit")
	autoPush, _ := cmd.Flags().GetBool("auto-push")
	noAutoPush, _ := cmd.Flags().GetBool("no-auto-push")
	forceRelease, _ := cmd.Flags().GetString("force-release")

	hasChanges := autoCommit || noAutoCommit || autoPush || noAutoPush || forceRelease != ""

	if hasChanges {
		return updateAutomationSettings(policyPath, autoCommit, noAutoCommit, autoPush, noAutoPush, forceRelease)
	}

	// Just show current settings
	p, err := policy.LoadOrDefault()
	if err != nil {
		return fmt.Errorf("loading policy: %w", err)
	}

	if IsJSONOutput() {
		return output.PrintJSON(AutomationResponse{
			TimestampedResponse: output.NewTimestamped(),
			AutoCommit:          p.Automation.AutoCommit,
			AutoPush:            p.Automation.AutoPush,
			ForceRelease:        p.ForceReleasePolicy(),
		})
	}

	// TUI output
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	labelStyle := lipgloss.NewStyle().Bold(true).Width(16)
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Automation Settings"))
	fmt.Println()
	fmt.Printf("  %s %s\n", labelStyle.Render("Auto-commit:"), formatBool(p.Automation.AutoCommit))
	fmt.Printf("  %s %s\n", labelStyle.Render("Auto-push:"), formatBool(p.Automation.AutoPush))
	fmt.Printf("  %s %s\n", labelStyle.Render("Force-release:"), valueStyle.Render(p.ForceReleasePolicy()))
	fmt.Println()

	return nil
}

func updateAutomationSettings(policyPath string, autoCommit, noAutoCommit, autoPush, noAutoPush bool, forceRelease string) error {
	// Load existing or create default
	var p *policy.Policy
	var err error

	if fileExists(policyPath) {
		p, err = policy.Load(policyPath)
		if err != nil {
			return fmt.Errorf("loading policy: %w", err)
		}
	} else {
		p = policy.DefaultPolicy()
	}

	// Apply changes
	if autoCommit {
		p.Automation.AutoCommit = true
	}
	if noAutoCommit {
		p.Automation.AutoCommit = false
	}
	if autoPush {
		p.Automation.AutoPush = true
	}
	if noAutoPush {
		p.Automation.AutoPush = false
	}
	if forceRelease != "" {
		switch forceRelease {
		case "never", "approval", "auto":
			p.Automation.ForceRelease = forceRelease
		default:
			return fmt.Errorf("invalid force-release value: %q (must be never, approval, or auto)", forceRelease)
		}
	}

	// Validate
	if err := p.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Read existing file to preserve structure and comments if possible
	var content string
	if fileExists(policyPath) {
		data, err := os.ReadFile(policyPath)
		if err == nil {
			content = updateAutomationInYAML(string(data), p.Automation)
		}
	}

	if content == "" {
		// Generate fresh YAML
		content = generatePolicyYAML(p)
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(policyPath), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	if err := os.WriteFile(policyPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing policy: %w", err)
	}

	if IsJSONOutput() {
		return output.PrintJSON(AutomationResponse{
			TimestampedResponse: output.NewTimestamped(),
			AutoCommit:          p.Automation.AutoCommit,
			AutoPush:            p.Automation.AutoPush,
			ForceRelease:        p.ForceReleasePolicy(),
			Modified:            true,
		})
	}

	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	fmt.Println()
	fmt.Printf("  %s Automation settings updated\n", okStyle.Render("✓"))
	fmt.Printf("    Auto-commit:   %s\n", formatBool(p.Automation.AutoCommit))
	fmt.Printf("    Auto-push:     %s\n", formatBool(p.Automation.AutoPush))
	fmt.Printf("    Force-release: %s\n", p.ForceReleasePolicy())
	fmt.Println()

	return nil
}

func updateAutomationInYAML(content string, auto policy.AutomationConfig) string {
	// Simple replacement approach - look for automation section and update values
	lines := strings.Split(content, "\n")
	var result []string
	inAutomation := false
	foundAutomation := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "automation:" {
			inAutomation = true
			foundAutomation = true
			result = append(result, line)
			continue
		}

		// Check if we've left the automation section (new top-level key)
		if inAutomation && len(line) > 0 && line[0] != ' ' && line[0] != '#' && strings.Contains(line, ":") {
			inAutomation = false
		}

		if inAutomation {
			if strings.HasPrefix(trimmed, "auto_commit:") {
				indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
				result = append(result, fmt.Sprintf("%sauto_commit: %v", indent, auto.AutoCommit))
				continue
			}
			if strings.HasPrefix(trimmed, "auto_push:") {
				indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
				result = append(result, fmt.Sprintf("%sauto_push: %v", indent, auto.AutoPush))
				continue
			}
			if strings.HasPrefix(trimmed, "force_release:") {
				indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
				result = append(result, fmt.Sprintf("%sforce_release: %s", indent, auto.ForceRelease))
				continue
			}
		}

		result = append(result, line)
	}

	// If no automation section was found, return empty string to signal regeneration
	if !foundAutomation {
		return ""
	}

	return strings.Join(result, "\n")
}

func generatePolicyYAML(p *policy.Policy) string {
	// Generate YAML from policy struct
	var sb strings.Builder

	sb.WriteString("# NTM Policy Configuration\n")
	sb.WriteString(fmt.Sprintf("version: %d\n\n", p.Version))

	sb.WriteString("automation:\n")
	sb.WriteString(fmt.Sprintf("  auto_commit: %v\n", p.Automation.AutoCommit))
	sb.WriteString(fmt.Sprintf("  auto_push: %v\n", p.Automation.AutoPush))
	sb.WriteString(fmt.Sprintf("  force_release: %s\n\n", p.ForceReleasePolicy()))

	if len(p.Allowed) > 0 {
		sb.WriteString("allowed:\n")
		for _, r := range p.Allowed {
			sb.WriteString(fmt.Sprintf("  - pattern: '%s'\n", escapeYAMLSingleQuote(r.Pattern)))
			if r.Reason != "" {
				sb.WriteString(fmt.Sprintf("    reason: \"%s\"\n", escapeYAMLDoubleQuote(r.Reason)))
			}
		}
		sb.WriteString("\n")
	}

	if len(p.Blocked) > 0 {
		sb.WriteString("blocked:\n")
		for _, r := range p.Blocked {
			sb.WriteString(fmt.Sprintf("  - pattern: '%s'\n", escapeYAMLSingleQuote(r.Pattern)))
			if r.Reason != "" {
				sb.WriteString(fmt.Sprintf("    reason: \"%s\"\n", escapeYAMLDoubleQuote(r.Reason)))
			}
		}
		sb.WriteString("\n")
	}

	if len(p.ApprovalRequired) > 0 {
		sb.WriteString("approval_required:\n")
		for _, r := range p.ApprovalRequired {
			sb.WriteString(fmt.Sprintf("  - pattern: '%s'\n", escapeYAMLSingleQuote(r.Pattern)))
			if r.Reason != "" {
				sb.WriteString(fmt.Sprintf("    reason: \"%s\"\n", escapeYAMLDoubleQuote(r.Reason)))
			}
			if r.SLB {
				sb.WriteString("    slb: true\n")
			}
		}
	}

	return sb.String()
}

// escapeYAMLSingleQuote escapes single quotes for YAML single-quoted strings.
// In YAML single-quoted strings, single quotes are escaped by doubling them.
func escapeYAMLSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// escapeYAMLDoubleQuote escapes characters for YAML double-quoted strings.
// Backslashes, double quotes, and newlines need to be escaped.
func escapeYAMLDoubleQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}
