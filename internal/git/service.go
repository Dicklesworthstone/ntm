// Package git provides git worktree isolation services for multi-agent coordination.
package git

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// WorktreeService provides high-level git worktree isolation services
type WorktreeService struct {
	mu       sync.Mutex
	managers map[string]*WorktreeManager // project path -> manager
	config   *config.Config

	detectAgentPanesFn      func(context.Context, string) ([]AgentPane, error)
	changeDirectoryInPaneFn func(context.Context, string, string) error
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
	SessionName string      `json:"session_name"`
	ProjectDir  string      `json:"project_dir"`
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
	SessionName     string              `json:"session_name"`
	ProjectDir      string              `json:"project_dir"`
	Provisions      []WorktreeProvision `json:"provisions"`
	Skipped         []SkippedProvision  `json:"skipped"`
	Errors          []ProvisionError    `json:"errors"`
	TotalProvisions int                 `json:"total_provisions"`
	SuccessCount    int                 `json:"success_count"`
	ProcessingTime  string              `json:"processing_time"`
}

// WorktreeProvision represents a successful worktree provision
type WorktreeProvision struct {
	PaneID       string `json:"pane_id"`
	AgentType    string `json:"agent_type"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`
	ChangeDir    string `json:"change_dir_command"`
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
	if err := validateWorktreeContext(ctx); err != nil {
		return nil, err
	}
	startTime := time.Now()

	// Get project directory for this session
	projectDir := ws.config.GetProjectDir(sessionName)
	if projectDir == "" {
		// Try to detect from current working directory
		if cwd, err := os.Getwd(); err == nil {
			isRepository, repoErr := IsGitRepository(ctx, cwd)
			if repoErr != nil {
				return nil, fmt.Errorf("detect current git repository: %w", repoErr)
			}
			if isRepository {
				projectDir = cwd
			}
		}
	}

	response := &AutoProvisionResponse{
		SessionName: sessionName,
		ProjectDir:  projectDir,
		Provisions:  []WorktreeProvision{},
		Skipped:     []SkippedProvision{},
		Errors:      []ProvisionError{},
	}

	// Check if we can provision worktrees for this project.
	isRepository, repoErr := IsGitRepository(ctx, projectDir)
	if repoErr != nil {
		return nil, fmt.Errorf("check worktree project repository: %w", repoErr)
	}
	if !isRepository {
		response.Skipped = append(response.Skipped, SkippedProvision{
			PaneID:    "session",
			AgentType: "all",
			Reason:    "project directory not found or not a git repository",
		})
		response.ProcessingTime = time.Since(startTime).String()
		return response, nil
	}

	// Get agent panes from the session
	agentPanes, err := ws.sessionAgentPanes(ctx, sessionName)
	if err != nil {
		return nil, fmt.Errorf("failed to detect agent panes: %w", err)
	}
	if err := validateWorktreeProvisionTargets(agentPanes); err != nil {
		return nil, fmt.Errorf("worktree provisioning preflight failed: %w", err)
	}

	// Get or create worktree manager for this project
	manager, err := ws.getManager(ctx, projectDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create worktree manager: %w", err)
	}

	// Provision worktrees for each agent pane
	for _, agentPane := range agentPanes {
		if err := validateWorktreeContext(ctx); err != nil {
			response.TotalProvisions = len(agentPanes)
			response.SuccessCount = len(response.Provisions)
			response.ProcessingTime = time.Since(startTime).String()
			return response, fmt.Errorf("automatic worktree provisioning canceled: %w", err)
		}

		// Generate a session ID that uses the same canonical agent key as
		// branch/worktree naming so cleanup/status matching remains consistent.
		sessionID := buildSessionWorktreeID(sessionName, agentPane.AgentType, agentPane.AgentNum)

		// Provision worktree
		worktreeInfo, err := manager.ProvisionWorktree(ctx, agentPane.AgentType, sessionID)
		if err != nil {
			if worktreeInfo != nil {
				response.Provisions = append(response.Provisions, worktreeProvisionForPane(agentPane, worktreeInfo))
			}
			response.Errors = append(response.Errors, ProvisionError{
				PaneID:    agentPane.PaneID,
				AgentType: agentPane.AgentType,
				Error:     err.Error(),
			})
			if ctx.Err() != nil {
				response.TotalProvisions = len(agentPanes)
				response.SuccessCount = len(response.Provisions)
				response.ProcessingTime = time.Since(startTime).String()
				if worktreeInfo != nil {
					return response, fmt.Errorf(
						"provision worktree for pane %s; checkout retained at %s on branch %s: %w",
						agentPane.PaneID, worktreeInfo.Path, worktreeInfo.Branch, err,
					)
				}
				return response, fmt.Errorf("provision worktree for pane %s: %w", agentPane.PaneID, err)
			}
			continue
		}

		response.Provisions = append(response.Provisions, worktreeProvisionForPane(agentPane, worktreeInfo))
		if err := validateWorktreeContext(ctx); err != nil {
			response.TotalProvisions = len(agentPanes)
			response.SuccessCount = len(response.Provisions)
			response.ProcessingTime = time.Since(startTime).String()
			return response, fmt.Errorf(
				"checkout provisioned at %s on branch %s before pane update was canceled: %w",
				worktreeInfo.Path, worktreeInfo.Branch, err,
			)
		}

		// Optionally, automatically change directory in the pane
		if err := ws.changePaneDirectory(ctx, agentPane.PaneID, worktreeInfo.Path); err != nil {
			if ctx.Err() != nil {
				response.TotalProvisions = len(agentPanes)
				response.SuccessCount = len(response.Provisions)
				response.ProcessingTime = time.Since(startTime).String()
				return response, fmt.Errorf(
					"checkout provisioned at %s on branch %s before pane update was canceled: %w",
					worktreeInfo.Path, worktreeInfo.Branch, err,
				)
			}
			log.Printf("Warning: failed to change directory in pane %s: %v", agentPane.PaneID, err)
		}
		if err := validateWorktreeContext(ctx); err != nil {
			response.TotalProvisions = len(agentPanes)
			response.SuccessCount = len(response.Provisions)
			response.ProcessingTime = time.Since(startTime).String()
			return response, fmt.Errorf(
				"checkout provisioned at %s on branch %s before cancellation: %w",
				worktreeInfo.Path, worktreeInfo.Branch, err,
			)
		}
	}

	response.TotalProvisions = len(agentPanes)
	response.SuccessCount = len(response.Provisions)
	response.ProcessingTime = time.Since(startTime).String()

	return response, nil
}

// CleanupSessionWorktrees removes worktrees associated with a specific session.
// It returns the number of worktrees actually removed so callers can
// distinguish a real cleanup from a no-op (#151).
func (ws *WorktreeService) CleanupSessionWorktrees(ctx context.Context, sessionName string) (int, error) {
	if err := validateWorktreeContext(ctx); err != nil {
		return 0, err
	}
	projectDir := ws.config.GetProjectDir(sessionName)
	isRepository, repoErr := IsGitRepository(ctx, projectDir)
	if repoErr != nil {
		return 0, fmt.Errorf("check cleanup project repository: %w", repoErr)
	}
	if !isRepository {
		return 0, nil // Nothing to clean up
	}

	manager, err := ws.getManager(ctx, projectDir)
	if err != nil {
		return 0, fmt.Errorf("failed to create worktree manager: %w", err)
	}

	// List all worktrees and find ones associated with this session
	worktrees, err := manager.ListWorktrees(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list worktrees: %w", err)
	}

	removed := 0
	for _, wt := range worktrees {
		if err := validateWorktreeContext(ctx); err != nil {
			return removed, fmt.Errorf("session worktree cleanup canceled: %w", err)
		}
		// Check if this worktree is associated with the session
		// Branch format: agent/<agent-type>/<session-id>
		if !strings.HasPrefix(wt.Branch, "agent/") {
			continue
		}

		parts := strings.SplitN(wt.Branch[6:], "/", 2)
		if len(parts) < 2 {
			continue
		}
		agentType := parts[0]
		sessionID := parts[1]

		// bd-y9ndb: sessionID format is <sessionName>-<agentType>-<num>.
		// HasPrefix(sessionID, sessionName+"-") alone matched
		// "my-app-claude-1" against sessionName="my", causing cleanup
		// of "my-app"'s worktree (data loss). Anchor on the known
		// agentType and require the trailing portion to be all
		// digits so "my" cannot match a "my-app-..." sessionID.
		if !sessionMatchesWorktree(sessionName, agentType, sessionID) {
			continue
		}

		// This worktree belongs to our session. Remove the exact path
		// reported by git so cleanup still works for renamed or older
		// worktree basename schemes.
		if err := manager.removeWorktreePathAndBranch(ctx, wt.Path, wt.Branch); err != nil {
			if ctx.Err() != nil {
				return removed, fmt.Errorf("remove session worktree %s: %w", wt.Path, err)
			}
			log.Printf("Warning: failed to remove worktree for %s: %v", sessionID, err)
			continue
		}
		removed++
	}
	if err := validateWorktreeContext(ctx); err != nil {
		return removed, fmt.Errorf("session worktree cleanup canceled: %w", err)
	}

	return removed, nil
}

// GetSessionWorktreeStatus returns the status of worktrees for a session
func (ws *WorktreeService) GetSessionWorktreeStatus(ctx context.Context, sessionName string) (map[string]*WorktreeInfo, error) {
	if err := validateWorktreeContext(ctx); err != nil {
		return nil, err
	}
	projectDir := ws.config.GetProjectDir(sessionName)
	isRepository, repoErr := IsGitRepository(ctx, projectDir)
	if repoErr != nil {
		return nil, fmt.Errorf("check status project repository: %w", repoErr)
	}
	if !isRepository {
		return make(map[string]*WorktreeInfo), nil
	}

	manager, err := ws.getManager(ctx, projectDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create worktree manager: %w", err)
	}

	worktrees, err := manager.ListWorktrees(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	sessionWorktrees := make(map[string]*WorktreeInfo)

	for _, wt := range worktrees {
		if err := validateWorktreeContext(ctx); err != nil {
			return nil, fmt.Errorf("session worktree status canceled: %w", err)
		}
		// Check if this worktree belongs to the session
		if strings.HasPrefix(wt.Branch, "agent/") {
			parts := strings.SplitN(wt.Branch[6:], "/", 2)

			if len(parts) >= 2 {
				agentType := parts[0]
				sessionID := parts[1]
				// bd-y9ndb: anchor match on known agentType + all-digit
				// suffix to avoid sessionName="my" matching "my-app-…".
				if sessionMatchesWorktree(sessionName, agentType, sessionID) {
					sessionWorktrees[wt.Agent] = wt
				}
			}
		}
	}
	if err := validateWorktreeContext(ctx); err != nil {
		return nil, fmt.Errorf("session worktree status canceled: %w", err)
	}

	return sessionWorktrees, nil
}

// Helper methods

// sessionMatchesWorktree reports whether sessionID corresponds to a
// worktree owned by (sessionName, agentType). AutoProvisionSession
// builds buildSessionWorktreeID(...) (session + canonical agent key +
// pane number), then ProvisionWorktree stores canonicalSessionKey(...) in
// the branch path.
func sessionMatchesWorktree(sessionName, agentType, sessionID string) bool {
	if sessionName == "" || agentType == "" || sessionID == "" {
		return false
	}

	// Manual `worktree provision <agent> <sessionID>` stores the sessionID
	// verbatim (canonicalized) as the branch's session segment, without the
	// auto-provision "-<agentType>-<num>" suffix. Match those by exact
	// canonical equality so manually-provisioned worktrees are cleanable
	// (#150). Exact match (not prefix) keeps the bd-y9ndb anchoring intact:
	// "my" still cannot match a "my-app-…" sessionID.
	if sessionID == canonicalSessionKey(sessionName) {
		return true
	}

	expectedPrefix := canonicalSessionKey(sessionName+"-"+agentType) + "-"
	if !strings.HasPrefix(sessionID, expectedPrefix) {
		return false
	}
	rest := sessionID[len(expectedPrefix):]
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func buildSessionWorktreeID(sessionName, agentType string, agentNum int) string {
	return canonicalSessionKey(fmt.Sprintf("%s-%s-%d", sessionName, canonicalAgentKey(agentType), agentNum))
}

func isUserAgentType(agentType tmux.AgentType) bool {
	switch agentType {
	case tmux.AgentUser:
		return true
	default:
		return false
	}
}

// getManager gets or creates a worktree manager for a project.
// Thread-safe: protects the managers map with a mutex.
func (ws *WorktreeService) getManager(ctx context.Context, projectDir string) (*WorktreeManager, error) {
	if err := validateWorktreeContext(ctx); err != nil {
		return nil, err
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if manager, exists := ws.managers[projectDir]; exists {
		return manager, nil
	}

	manager, err := NewWorktreeManager(ctx, projectDir)
	if err != nil {
		return nil, err
	}

	ws.managers[projectDir] = manager
	return manager, nil
}

// detectAgentPanes detects agent panes in a tmux session.
func (ws *WorktreeService) detectAgentPanes(ctx context.Context, sessionName string) ([]AgentPane, error) {
	if err := validateWorktreeContext(ctx); err != nil {
		return nil, err
	}
	exists, err := tmux.SessionExistsContext(ctx, sessionName)
	if err != nil {
		return nil, fmt.Errorf("failed to check session %s: %w", sessionName, err)
	}
	if !exists {
		return nil, fmt.Errorf("session %s does not exist", sessionName)
	}

	panes, err := tmux.GetPanesContext(ctx, sessionName)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

	var agentPanes []AgentPane

	for _, pane := range panes {
		// Skip user panes or panes that didn't parse as NTM agents.
		if isUserAgentType(pane.Type) || pane.NTMIndex == 0 {
			continue
		}

		agentPanes = append(agentPanes, AgentPane{
			PaneID:    pane.ID,
			AgentType: string(pane.Type),
			AgentNum:  pane.NTMIndex,
			Title:     pane.Title,
		})
	}

	if err := validateWorktreeContext(ctx); err != nil {
		return nil, fmt.Errorf("agent pane discovery canceled: %w", err)
	}
	return agentPanes, nil
}

func (ws *WorktreeService) sessionAgentPanes(ctx context.Context, sessionName string) ([]AgentPane, error) {
	if err := validateWorktreeContext(ctx); err != nil {
		return nil, err
	}
	if ws.detectAgentPanesFn != nil {
		return ws.detectAgentPanesFn(ctx, sessionName)
	}
	return ws.detectAgentPanes(ctx, sessionName)
}

func validateWorktreeProvisionTargets(agentPanes []AgentPane) error {
	for _, pane := range agentPanes {
		if err := agent.AgentType(pane.AgentType).ValidateAutomatedPromptDelivery(); err != nil {
			return fmt.Errorf("pane %s (%s): %w", pane.PaneID, agent.AgentType(pane.AgentType).Canonical(), err)
		}
	}
	return nil
}

func worktreeProvisionForPane(agentPane AgentPane, worktreeInfo *WorktreeInfo) WorktreeProvision {
	return WorktreeProvision{
		PaneID:       agentPane.PaneID,
		AgentType:    agentPane.AgentType,
		WorktreePath: worktreeInfo.Path,
		Branch:       worktreeInfo.Branch,
		Commit:       worktreeInfo.Commit,
		ChangeDir:    fmt.Sprintf("cd %s", tmux.ShellQuote(worktreeInfo.Path)),
	}
}

func (ws *WorktreeService) changePaneDirectory(ctx context.Context, paneID, workingDir string) error {
	if err := validateWorktreeContext(ctx); err != nil {
		return err
	}
	if ws.changeDirectoryInPaneFn != nil {
		return ws.changeDirectoryInPaneFn(ctx, paneID, workingDir)
	}
	return ws.changeDirectoryInPane(ctx, paneID, workingDir)
}

// changeDirectoryInPane sends a cd command to a tmux pane
func (ws *WorktreeService) changeDirectoryInPane(ctx context.Context, paneID, workingDir string) error {
	if err := validateWorktreeContext(ctx); err != nil {
		return err
	}
	// Send Ctrl-C first to interrupt any running command
	if err := tmux.DefaultClient.RunSilentContext(ctx, "send-keys", "-t", tmux.ExactTarget(paneID), "C-c"); err != nil {
		return fmt.Errorf("failed to send interrupt: %w", err)
	}

	// Wait a moment for the interrupt to take effect
	if err := waitForWorktreePaneDelay(ctx, 100*time.Millisecond); err != nil {
		return fmt.Errorf("pane update canceled after interrupt: %w", err)
	}

	// Send the cd command
	cdCommand := fmt.Sprintf("cd %s", tmux.ShellQuote(workingDir))
	if err := tmux.SendKeysContext(ctx, paneID, cdCommand, true); err != nil {
		return fmt.Errorf("failed to send cd command: %w", err)
	}

	return nil
}

func waitForWorktreePaneDelay(ctx context.Context, delay time.Duration) error {
	if err := validateWorktreeContext(ctx); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
