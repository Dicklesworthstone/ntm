package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// agentIDPattern matches agent IDs like "cc_1", "cod_2", "gmi_3"
var agentIDPattern = regexp.MustCompile(`^(cc|cod|gmi)_(\d+)$`)

// ProfileSwitchResult contains the result of a profile switch operation
type ProfileSwitchResult struct {
	Success    bool   `json:"success"`
	AgentID    string `json:"agent_id"`
	PaneID     string `json:"pane_id"`
	OldProfile string `json:"old_profile,omitempty"`
	NewProfile string `json:"new_profile"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
}

func newProfileSwitchCmd() *cobra.Command {
	var (
		session          string
		transitionPrompt string
		noPrompt         bool
		dryRun           bool
	)

	cmd := &cobra.Command{
		Use:   "switch <agent-id> <new-profile>",
		Short: "Switch an agent's active profile",
		Long: `Switch an agent to a different profile dynamically.

The agent-id format is: {type}_{index}
  - cc_1  = First Claude agent
  - cod_2 = Second Codex agent
  - gmi_1 = First Gemini agent

This command:
1. Finds the target pane by agent ID
2. Loads the new profile configuration
3. Sends a transition prompt to inform the agent of the change
4. Updates the pane title to reflect the new profile

Examples:
  ntm profiles switch cc_1 reviewer     # Switch cc_1 to reviewer profile
  ntm profiles switch cc_2 architect    # Switch cc_2 to architect profile
  ntm profiles switch cc_1 reviewer --session myproject
  ntm profiles switch cc_1 reviewer --no-prompt  # Skip transition prompt
  ntm profiles switch cc_1 reviewer --dry-run    # Preview without applying`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProfileSwitch(args[0], args[1], session, transitionPrompt, noPrompt, dryRun)
		},
	}

	cmd.Flags().StringVarP(&session, "session", "s", "", "Target session (default: current or most recent)")
	cmd.Flags().StringVar(&transitionPrompt, "prompt", "", "Custom transition prompt (default: auto-generated)")
	cmd.Flags().BoolVar(&noPrompt, "no-prompt", false, "Skip sending transition prompt to agent")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without applying them")

	return cmd
}

func runProfileSwitch(agentID, newProfileName, sessionName, customPrompt string, noPrompt, dryRun bool) error {
	// Parse agent ID
	agentType, agentIndex, err := parseAgentID(agentID)
	if err != nil {
		return outputProfileSwitchError(agentID, "", "", err)
	}

	// Get project directory
	cwd, err := os.Getwd()
	if err != nil {
		return outputProfileSwitchError(agentID, "", "", fmt.Errorf("getting working directory: %w", err))
	}

	// Load persona registry
	registry, err := persona.LoadRegistry(cwd)
	if err != nil {
		return outputProfileSwitchError(agentID, "", "", fmt.Errorf("loading persona registry: %w", err))
	}

	// Find the new profile
	newProfile, ok := registry.Get(newProfileName)
	if !ok {
		return outputProfileSwitchError(agentID, "", newProfileName, fmt.Errorf("profile %q not found", newProfileName))
	}

	// Resolve session name
	if sessionName == "" {
		sessions, err := tmux.ListSessions()
		if err != nil {
			return outputProfileSwitchError(agentID, "", newProfileName, fmt.Errorf("listing sessions: %w", err))
		}
		if len(sessions) == 0 {
			return outputProfileSwitchError(agentID, "", newProfileName, fmt.Errorf("no tmux sessions found"))
		}
		sessionName = sessions[0].Name
	}

	// Get session panes
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		return outputProfileSwitchError(agentID, "", newProfileName, fmt.Errorf("getting panes for session %s: %w", sessionName, err))
	}

	// Find target pane
	targetPane, oldProfile, err := findPaneByAgentID(panes, sessionName, agentType, agentIndex)
	if err != nil {
		return outputProfileSwitchError(agentID, "", newProfileName, err)
	}

	// Dry run - just show what would happen
	if dryRun {
		return outputProfileSwitchDryRun(agentID, targetPane.ID, oldProfile, newProfileName, sessionName)
	}

	// Prepare system prompt file
	promptFile, err := persona.PrepareSystemPrompt(newProfile, cwd)
	if err != nil {
		// Non-fatal: log warning but continue
		if !IsJSONOutput() {
			fmt.Printf("Warning: could not prepare system prompt file: %v\n", err)
		}
	}

	// Generate transition prompt
	transitionText := customPrompt
	if transitionText == "" && !noPrompt {
		transitionText = generateTransitionPrompt(oldProfile, newProfile, promptFile)
	}

	// Send transition prompt to agent
	if transitionText != "" && !noPrompt {
		if err := sendPromptWithDoubleEnter(targetPane.ID, transitionText); err != nil {
			return outputProfileSwitchError(agentID, targetPane.ID, newProfileName, fmt.Errorf("sending transition prompt: %w", err))
		}
	}

	// Update pane title
	newTitle := tmux.FormatPaneName(sessionName, agentType, agentIndex, newProfile.Name)
	if err := tmux.SetPaneTitle(targetPane.ID, newTitle); err != nil {
		return outputProfileSwitchError(agentID, targetPane.ID, newProfileName, fmt.Errorf("updating pane title: %w", err))
	}

	return outputProfileSwitchSuccess(agentID, targetPane.ID, oldProfile, newProfile.Name)
}

// parseAgentID parses an agent ID like "cc_1" into type and index
func parseAgentID(id string) (string, int, error) {
	matches := agentIDPattern.FindStringSubmatch(id)
	if matches == nil {
		return "", 0, fmt.Errorf("invalid agent ID %q (expected format: cc_1, cod_2, gmi_3)", id)
	}

	index, err := strconv.Atoi(matches[2])
	if err != nil {
		return "", 0, fmt.Errorf("invalid index in agent ID: %w", err)
	}

	return matches[1], index, nil
}

// findPaneByAgentID finds a pane matching the agent type and index
func findPaneByAgentID(panes []tmux.Pane, session, agentType string, agentIndex int) (*tmux.Pane, string, error) {
	// Build expected title prefix: {session}__{type}_{index}
	prefix := fmt.Sprintf("%s__%s_%d", session, agentType, agentIndex)

	for i := range panes {
		pane := &panes[i]
		// Match exact prefix or prefix with variant suffix
		if pane.Title == prefix || strings.HasPrefix(pane.Title, prefix+"_") {
			// Extract old profile from variant if present
			oldProfile := ""
			if len(pane.Title) > len(prefix)+1 {
				oldProfile = pane.Title[len(prefix)+1:]
			}
			return pane, oldProfile, nil
		}
	}

	return nil, "", fmt.Errorf("no pane found for agent %s_%d in session %s", agentType, agentIndex, session)
}

// generateTransitionPrompt creates a prompt to inform the agent of the profile change
func generateTransitionPrompt(oldProfile string, newProfile *persona.Persona, promptFile string) string {
	var sb strings.Builder

	sb.WriteString("---\n")
	sb.WriteString("**Profile Switch Notification**\n\n")

	if oldProfile != "" {
		sb.WriteString(fmt.Sprintf("You are transitioning from the '%s' profile to the '%s' profile.\n\n", oldProfile, newProfile.Name))
	} else {
		sb.WriteString(fmt.Sprintf("You are now adopting the '%s' profile.\n\n", newProfile.Name))
	}

	sb.WriteString("**New Profile Description:**\n")
	if newProfile.Description != "" {
		sb.WriteString(newProfile.Description + "\n\n")
	}

	sb.WriteString("**New Focus:**\n")
	sb.WriteString(newProfile.SystemPrompt + "\n")

	if len(newProfile.FocusPatterns) > 0 {
		sb.WriteString("\n**Focus Files:**\n")
		for _, pattern := range newProfile.FocusPatterns {
			sb.WriteString(fmt.Sprintf("- %s\n", pattern))
		}
	}

	sb.WriteString("\n---\n")
	sb.WriteString("Please acknowledge this profile change and adjust your behavior accordingly.\n")

	return sb.String()
}

func outputProfileSwitchSuccess(agentID, paneID, oldProfile, newProfile string) error {
	result := ProfileSwitchResult{
		Success:    true,
		AgentID:    agentID,
		PaneID:     paneID,
		OldProfile: oldProfile,
		NewProfile: newProfile,
		Message:    fmt.Sprintf("Successfully switched %s to profile '%s'", agentID, newProfile),
	}

	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	fmt.Printf("Profile switched successfully!\n")
	fmt.Printf("  Agent:       %s\n", agentID)
	fmt.Printf("  Pane:        %s\n", paneID)
	if oldProfile != "" {
		fmt.Printf("  Old profile: %s\n", oldProfile)
	}
	fmt.Printf("  New profile: %s\n", newProfile)

	return nil
}

func outputProfileSwitchError(agentID, paneID, newProfile string, err error) error {
	if IsJSONOutput() {
		result := ProfileSwitchResult{
			Success:    false,
			AgentID:    agentID,
			PaneID:     paneID,
			NewProfile: newProfile,
			Error:      err.Error(),
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
	}
	return err
}

func outputProfileSwitchDryRun(agentID, paneID, oldProfile, newProfile, session string) error {
	if IsJSONOutput() {
		result := struct {
			DryRun     bool   `json:"dry_run"`
			AgentID    string `json:"agent_id"`
			PaneID     string `json:"pane_id"`
			Session    string `json:"session"`
			OldProfile string `json:"old_profile,omitempty"`
			NewProfile string `json:"new_profile"`
		}{
			DryRun:     true,
			AgentID:    agentID,
			PaneID:     paneID,
			Session:    session,
			OldProfile: oldProfile,
			NewProfile: newProfile,
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	fmt.Printf("Dry run - no changes applied\n")
	fmt.Printf("  Would switch:  %s\n", agentID)
	fmt.Printf("  Target pane:   %s\n", paneID)
	fmt.Printf("  Session:       %s\n", session)
	if oldProfile != "" {
		fmt.Printf("  From profile:  %s\n", oldProfile)
	}
	fmt.Printf("  To profile:    %s\n", newProfile)

	return nil
}
