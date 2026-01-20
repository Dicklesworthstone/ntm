// Package git provides git worktree isolation services for multi-agent coordination.
package git

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// WorktreeService provides high-level git worktree isolation services
type WorktreeService struct {
	managers map[string]*WorktreeManager // project path -> manager
	config   *config.Config
}

// NewWorktreeService creates a new worktree service
func NewWorktreeService(cfg *config.Config) *WorktreeService {
	return &WorktreeService{
		managers: make(map[string]*WorktreeManager),
		config:   cfg,
	}
}

// AutoProvisionRequest represents a request for automatic worktree provisioning
type AutoProvisionRequest struct {
	SessionName string    `json:"session_name"`
	ProjectDir  string    `json:"project_dir"`
	AgentPanes  []AgentPane `json:"agent_panes"`
}

// AgentPane represents an agent pane that needs worktree isolation
type AgentPane struct {
	PaneID    string `json:"pane_id"`
	AgentType string `json:"agent_type"`
	AgentNum  int    `json:"agent_num"`
	Title     string `json:"title"`
}

// AutoProvisionResponse represents the result of automatic provisioning
type AutoProvisionResponse struct {
	SessionName    string               `json:"session_name"`
	ProjectDir     string               `json:"project_dir"`
	Provisions     []WorktreeProvision  `json:"provisions"`
	Skipped        []SkippedProvision   `json:"skipped"`
	Errors         []ProvisionError     `json:"errors"`
	TotalProvisions int                 `json:"total_provisions"`
	SuccessCount    int                 `json:"success_count"`
	ProcessingTime  string              `json:"processing_time"`
}

// WorktreeProvision represents a successful worktree provision
type WorktreeProvision struct {
	PaneID        string `json:"pane_id"`
	AgentType     string `json:"agent_type"`
	WorktreePath  string `json:"worktree_path"`
	Branch        string `json:"branch"`
	Commit        string `json:"commit"`
	ChangeDir     string `json:"change_dir_command"`
}

// SkippedProvision represents a skipped provision (e.g., not a git repo)
type SkippedProvision struct {
	PaneID    string `json:"pane_id"`
	AgentType string `json:"agent_type"`
	Reason    string `json:"reason"`
}

// ProvisionError represents a provision error
type ProvisionError struct {
	PaneID    string `json:"pane_id"`
	AgentType string `json:"agent_type"`
	Error     string `json:"error"`
}

// AutoProvisionSession automatically provisions worktrees for all agent panes in a session
func (ws *WorktreeService) AutoProvisionSession(ctx context.Context, sessionName string) (*AutoProvisionResponse, error) {
	startTime := time.Now()

	// Get project directory for this session
	projectDir := ws.config.GetProjectDir(sessionName)
	if projectDir == "" {
		// Try to detect from current working directory
		if cwd, err := os.Getwd(); err == nil && IsGitRepository(cwd) {
			projectDir = cwd
		}
	}

	response := &AutoProvisionResponse{
		SessionName: sessionName,
		ProjectDir:  projectDir,
		Provisions:  []WorktreeProvision{},
		Skipped:     []SkippedProvision{},
		Errors:      []ProvisionError{},
	}

	// Check if we can provision worktrees for this project
	if projectDir == "" || !IsGitRepository(projectDir) {
		response.Skipped = append(response.Skipped, SkippedProvision{
			PaneID:    "session",
			AgentType: "all",
			Reason:    "project directory not found or not a git repository",
		})
		response.ProcessingTime = time.Since(startTime).String()
		return response, nil
	}

	// Get agent panes from the session
	agentPanes, err := ws.detectAgentPanes(sessionName)
	if err != nil {
		return nil, fmt.Errorf("failed to detect agent panes: %w", err)
	}

	// Get or create worktree manager for this project
	manager, err := ws.getManager(projectDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create worktree manager: %w", err)
	}

	// Provision worktrees for each agent pane
	for _, agentPane := range agentPanes {
		// Generate a unique session ID for this agent
		sessionID := fmt.Sprintf("%s-%s-%d", sessionName, agentPane.AgentType, agentPane.AgentNum)

		// Provision worktree
		worktreeInfo, err := manager.ProvisionWorktree(ctx, agentPane.AgentType, sessionID)
		if err != nil {
			response.Errors = append(response.Errors, ProvisionError{
				PaneID:    agentPane.PaneID,
				AgentType: agentPane.AgentType,
				Error:     err.Error(),
			})
			continue
		}

		// Generate cd command for the pane
		changeDirCommand := fmt.Sprintf("cd %s", worktreeInfo.Path)

		provision := WorktreeProvision{
			PaneID:        agentPane.PaneID,
			AgentType:     agentPane.AgentType,
			WorktreePath:  worktreeInfo.Path,
			Branch:        worktreeInfo.Branch,
			Commit:        worktreeInfo.Commit,
			ChangeDir:     changeDirCommand,
		}

		response.Provisions = append(response.Provisions, provision)

		// Optionally, automatically change directory in the pane
		if err := ws.changeDirectoryInPane(agentPane.PaneID, worktreeInfo.Path); err != nil {
			log.Printf("Warning: failed to change directory in pane %s: %v", agentPane.PaneID, err)
		}
	}

	response.TotalProvisions = len(agentPanes)
	response.SuccessCount = len(response.Provisions)
	response.ProcessingTime = time.Since(startTime).String()

	return response, nil
}

// CleanupSessionWorktrees removes worktrees associated with a specific session
func (ws *WorktreeService) CleanupSessionWorktrees(ctx context.Context, sessionName string) error {
	projectDir := ws.config.GetProjectDir(sessionName)
	if projectDir == "" || !IsGitRepository(projectDir) {
		return nil // Nothing to clean up
	}

	manager, err := ws.getManager(projectDir)
	if err != nil {
		return fmt.Errorf("failed to create worktree manager: %w", err)
	}

	// List all worktrees and find ones associated with this session
	worktrees, err := manager.ListWorktrees(ctx)
	if err != nil {
		return fmt.Errorf("failed to list worktrees: %w", err)
	}

	for _, wt := range worktrees {
		// Check if this worktree is associated with the session
		// Branch format: agent/<agent-type>/<session-id>
		if len(wt.Branch) > 6 && wt.Branch[:6] == "agent/" {
			parts := []string{}
			if len(wt.Branch) > 6 {
				parts = []string{wt.Branch[6:]}
				for idx := 0; idx < len(parts[0]); idx++ {
					if parts[0][idx] == '/' {
						parts = []string{parts[0][:idx], parts[0][idx+1:]}
						break
					}
				}
			}

			if len(parts) >= 2 {
				agentType := parts[0]
				sessionID := parts[1]

				// Extract session name from session ID (format: sessionName-agentType-num)
				if len(sessionID) > len(sessionName) && sessionID[:len(sessionName)] == sessionName {
					// This worktree belongs to our session
					if err := manager.RemoveWorktree(ctx, agentType, sessionID); err != nil {
						log.Printf("Warning: failed to remove worktree for %s: %v", sessionID, err)
					}
				}
			}
		}
	}

	return nil
}

// GetSessionWorktreeStatus returns the status of worktrees for a session
func (ws *WorktreeService) GetSessionWorktreeStatus(ctx context.Context, sessionName string) (map[string]*WorktreeInfo, error) {
	projectDir := ws.config.GetProjectDir(sessionName)
	if projectDir == "" || !IsGitRepository(projectDir) {
		return make(map[string]*WorktreeInfo), nil
	}

	manager, err := ws.getManager(projectDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create worktree manager: %w", err)
	}

	worktrees, err := manager.ListWorktrees(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	sessionWorktrees := make(map[string]*WorktreeInfo)

	for _, wt := range worktrees {
		// Check if this worktree belongs to the session
		if len(wt.Branch) > 6 && wt.Branch[:6] == "agent/" {
			parts := []string{}
			if len(wt.Branch) > 6 {
				remaining := wt.Branch[6:]
				for i, r := range remaining {
					if r == '/' {
						parts = []string{remaining[:i], remaining[i+1:]}
						break
					}
				}
				if len(parts) == 0 {
					parts = []string{remaining}
				}
			}

			if len(parts) >= 2 {
				sessionID := parts[1]
				if len(sessionID) > len(sessionName) && sessionID[:len(sessionName)] == sessionName {
					sessionWorktrees[wt.Agent] = wt
				}
			}
		}
	}

	return sessionWorktrees, nil
}

// Helper methods

// getManager gets or creates a worktree manager for a project
func (ws *WorktreeService) getManager(projectDir string) (*WorktreeManager, error) {
	if manager, exists := ws.managers[projectDir]; exists {
		return manager, nil
	}

	manager, err := NewWorktreeManager(projectDir)
	if err != nil {
		return nil, err
	}

	ws.managers[projectDir] = manager
	return manager, nil
}

// detectAgentPanes detects agent panes in a tmux session
func (ws *WorktreeService) detectAgentPanes(sessionName string) ([]AgentPane, error) {
	if !tmux.SessionExists(sessionName) {
		return nil, fmt.Errorf("session %s does not exist", sessionName)
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

	var agentPanes []AgentPane

	for _, pane := range panes {
		// Parse agent type and number from pane title
		// Expected format: sessionName__agentType_num (e.g., "mysession__cc_1")
		if agentType, agentNum, isAgent := parseAgentTitle(pane.Title, sessionName); isAgent {
			agentPanes = append(agentPanes, AgentPane{
				PaneID:    pane.ID,
				AgentType: agentType,
				AgentNum:  agentNum,
				Title:     pane.Title,
			})
		}
	}

	return agentPanes, nil
}

// changeDirectoryInPane sends a cd command to a tmux pane
func (ws *WorktreeService) changeDirectoryInPane(paneID, workingDir string) error {
	// Send Ctrl-C first to interrupt any running command
	if err := tmux.SendKeys(paneID, "C-c", false); err != nil {
		return fmt.Errorf("failed to send interrupt: %w", err)
	}

	// Wait a moment for the interrupt to take effect
	time.Sleep(100 * time.Millisecond)

	// Send the cd command
	cdCommand := fmt.Sprintf("cd %s", workingDir)
	if err := tmux.SendKeys(paneID, cdCommand, true); err != nil {
		return fmt.Errorf("failed to send cd command: %w", err)
	}

	return nil
}

// parseAgentTitle parses agent information from a pane title
func parseAgentTitle(title, sessionName string) (agentType string, agentNum int, isAgent bool) {
	// Expected format: sessionName__agentType_num
	prefix := sessionName + "__"
	if len(title) <= len(prefix) || title[:len(prefix)] != prefix {
		return "", 0, false
	}

	suffix := title[len(prefix):]

	// Look for the pattern: agentType_num
	for i := len(suffix) - 1; i >= 0; i-- {
		if suffix[i] == '_' {
			agentTypeStr := suffix[:i]
			agentNumStr := suffix[i+1:]

			// Parse the number
			num := 0
			for _, r := range agentNumStr {
				if r >= '0' && r <= '9' {
					num = num*10 + int(r-'0')
				} else {
					return "", 0, false
				}
			}

			// Valid agent types
			if agentTypeStr == "cc" || agentTypeStr == "cod" || agentTypeStr == "gmi" {
				return agentTypeStr, num, true
			}
		}
	}

	return "", 0, false
}

// GetAllWorktrees returns worktrees across all managed projects
func (ws *WorktreeService) GetAllWorktrees(ctx context.Context) (map[string][]*WorktreeInfo, error) {
	result := make(map[string][]*WorktreeInfo)

	for projectDir, manager := range ws.managers {
		worktrees, err := manager.ListWorktrees(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list worktrees for %s: %w", projectDir, err)
		}
		result[projectDir] = worktrees
	}

	return result, nil
}

// CleanupStaleWorktrees removes stale worktrees across all managed projects
func (ws *WorktreeService) CleanupStaleWorktrees(ctx context.Context, maxAge time.Duration) error {
	for _, manager := range ws.managers {
		if err := manager.CleanupStaleWorktrees(ctx, maxAge); err != nil {
			return err
		}
	}
	return nil
}