package agentmail

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const coordinatorStateVersion = 1

// CoordinatorIdentity is the durable Agent Mail sender used by the NTM
// coordinator. The registration token is intentionally persisted in the
// same mode-0600 session artifact as the identity; dispatch never invents a
// sender or silently registers one.
type CoordinatorIdentity struct {
	AgentName         string `json:"agent_name"`
	RegistrationToken string `json:"registration_token"`
	Program           string `json:"program"`
	Model             string `json:"model"`
}

// CoordinatorPaneBinding is an explicitly imported, live pane identity.
// PanePID prevents a recycled tmux pane id from inheriting the old binding.
type CoordinatorPaneBinding struct {
	AgentName  string    `json:"agent_name"`
	PanePID    int       `json:"pane_pid"`
	ProjectDir string    `json:"project_dir"`
	ImportedAt time.Time `json:"imported_at"`
}

// CoordinatorSessionState separates the physical checkout used by bv/br from
// the canonical Agent Mail namespace. It is written only by the explicit
// `ntm coordinator bootstrap` workflow.
type CoordinatorSessionState struct {
	Version        int                               `json:"version"`
	SessionName    string                            `json:"session_name"`
	ProjectDir     string                            `json:"project_dir"`
	MailProjectKey string                            `json:"mail_project_key"`
	Coordinator    CoordinatorIdentity               `json:"coordinator"`
	PaneBindings   map[string]CoordinatorPaneBinding `json:"pane_bindings"`
	CreatedAt      time.Time                         `json:"created_at"`
	UpdatedAt      time.Time                         `json:"updated_at"`
}

func coordinatorStatePath(sessionName, projectDir string) string {
	return filepath.Join(getSessionsBaseDir(), sessionName, primaryProjectSlug(projectDir), "coordinator.json")
}

// LoadCoordinatorSessionState loads only the artifact namespaced by the exact
// physical project directory. It deliberately has no best-effort or legacy
// fallback: stale state from another checkout/mail namespace is not authority.
func LoadCoordinatorSessionState(sessionName, projectDir string) (*CoordinatorSessionState, error) {
	if err := validateSessionStorageName(sessionName); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(strings.TrimSpace(projectDir)) {
		return nil, fmt.Errorf("coordinator project directory must be absolute: %q", projectDir)
	}
	path := coordinatorStatePath(sessionName, filepath.Clean(projectDir))
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading coordinator state: %w", err)
	}
	var state CoordinatorSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing coordinator state: %w", err)
	}
	if err := ValidateCoordinatorSessionState(&state, sessionName, projectDir); err != nil {
		return nil, fmt.Errorf("invalid coordinator state %s: %w", path, err)
	}
	return &state, nil
}

// SaveCoordinatorSessionState atomically persists a validated coordinator
// sender/token and the exact pane bindings imported by the operator.
func SaveCoordinatorSessionState(state *CoordinatorSessionState) error {
	if state == nil {
		return fmt.Errorf("cannot save nil coordinator state")
	}
	if state.Version == 0 {
		state.Version = coordinatorStateVersion
	}
	if err := ValidateCoordinatorSessionState(state, state.SessionName, state.ProjectDir); err != nil {
		return err
	}
	path := coordinatorStatePath(state.SessionName, state.ProjectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating coordinator state directory: %w", err)
	}
	now := time.Now().UTC()
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	state.UpdatedAt = now
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling coordinator state: %w", err)
	}
	if err := util.AtomicWriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing coordinator state: %w", err)
	}
	return nil
}

// ValidateCoordinatorSessionState enforces the fail-closed runtime contract.
func ValidateCoordinatorSessionState(state *CoordinatorSessionState, sessionName, projectDir string) error {
	if state == nil {
		return fmt.Errorf("coordinator state is required")
	}
	if err := validateSessionStorageName(sessionName); err != nil {
		return err
	}
	if state.Version != coordinatorStateVersion {
		return fmt.Errorf("unsupported version %d", state.Version)
	}
	if strings.TrimSpace(state.SessionName) != strings.TrimSpace(sessionName) {
		return fmt.Errorf("session %q does not match %q", state.SessionName, sessionName)
	}
	cleanProjectDir := filepath.Clean(strings.TrimSpace(projectDir))
	if !filepath.IsAbs(cleanProjectDir) || filepath.Clean(state.ProjectDir) != cleanProjectDir {
		return fmt.Errorf("project directory %q does not match %q", state.ProjectDir, projectDir)
	}
	if key := strings.TrimSpace(state.MailProjectKey); !filepath.IsAbs(key) {
		return fmt.Errorf("mail project key must be an absolute canonical key: %q", state.MailProjectKey)
	}
	if strings.TrimSpace(state.Coordinator.AgentName) == "" {
		return fmt.Errorf("coordinator agent name is required")
	}
	if strings.TrimSpace(state.Coordinator.RegistrationToken) == "" {
		return fmt.Errorf("coordinator registration token is required")
	}
	if state.Coordinator.Program != "ntm" || state.Coordinator.Model != "coordinator" {
		return fmt.Errorf("coordinator identity must use program=ntm and model=coordinator")
	}
	for paneID, binding := range state.PaneBindings {
		if strings.TrimSpace(paneID) == "" || strings.TrimSpace(binding.AgentName) == "" {
			return fmt.Errorf("pane binding has an empty pane id or agent name")
		}
		if binding.PanePID <= 0 {
			return fmt.Errorf("pane %s binding has invalid pid %d", paneID, binding.PanePID)
		}
		if filepath.Clean(binding.ProjectDir) != cleanProjectDir {
			return fmt.Errorf("pane %s project %q does not match %q", paneID, binding.ProjectDir, cleanProjectDir)
		}
	}
	return nil
}
