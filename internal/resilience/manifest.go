package resilience

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

// sanitizeSessionName ensures a session name is safe for use in file paths.
// It rejects names containing path separators or traversal components.
func sanitizeSessionName(session string) (string, error) {
	if session == "" {
		return "", fmt.Errorf("empty session name")
	}
	// Reject any path separator characters
	if strings.ContainsAny(session, "/\\") {
		return "", fmt.Errorf("session name %q contains path separators", session)
	}
	// Reject path traversal components
	if session == "." || session == ".." || strings.Contains(session, "..") {
		return "", fmt.Errorf("session name %q contains path traversal", session)
	}
	return session, nil
}

// SpawnManifest represents the configuration of a spawned session for monitoring
type SpawnManifest struct {
	Session           string                  `json:"session"`
	SessionIdentity   string                  `json:"session_identity,omitempty"`
	ProjectDir        string                  `json:"project_dir"`
	Agents            []AgentConfig           `json:"agents"`
	AutoRestart       bool                    `json:"auto_restart"`
	ConfigPath        string                  `json:"config_path,omitempty"`
	MonitorGeneration string                  `json:"monitor_generation,omitempty"`
	AccountRotation   *RotationMonitorOptions `json:"account_rotation,omitempty"`
}

// RotationMonitorOptions is the explicit operator intent carried into the
// resident process. A non-nil value selects rotation-only monitoring; it does
// not opt the swarm into unrelated restart, daemon or assignment features.
type RotationMonitorOptions struct {
	ForceGlobalAuthClobber bool     `json:"force_global_auth_clobber"`
	Providers              []string `json:"providers"`
	CAAMBinary             string   `json:"caam_binary,omitempty"`
	ResetHorizonMinutes    int      `json:"reset_horizon_minutes"`
	PollSeconds            int      `json:"poll_seconds"`
}

// AgentConfig represents the configuration for a single agent
type AgentConfig struct {
	PaneID        string         `json:"pane_id"`
	PaneIndex     int            `json:"pane_index"`
	ProjectDir    string         `json:"project_dir,omitempty"`
	Type          string         `json:"type"`
	Model         string         `json:"model"`
	Command       string         `json:"command"`
	LaunchBinding *LaunchBinding `json:"launch_binding,omitempty"`
}

// ManifestDir returns the directory for storing session manifests
func ManifestDir() string {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "ntm", "manifests")
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataDir, "ntm", "manifests")
}

// LogDir returns the directory for storing session monitor logs
func LogDir() string {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "ntm", "logs")
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataDir, "ntm", "logs")
}

// mutateManifest serializes the entire mutation, including any read, against
// other NTM processes using the same manifest directory. Locking the JSON file
// itself would not work: AtomicWriteFile replaces its inode. The separate lock
// is never removed, including by DeleteManifest, and is distinct from the
// resident monitor's lifetime lease. No caller waits on a monitor's lifetime.
//
// The wait is bounded even for APIs without a caller context. Acquisition never
// falls back to unlocked writes. File I/O after acquisition remains synchronous;
// a timeout/error is not permission to replay an uncertain filesystem mutation.
func mutateManifest(ctx context.Context, session string, apply func(string) error) error {
	if ctx == nil || apply == nil {
		return errors.New("manifest mutation requires a context and callback")
	}
	safe, err := sanitizeSessionName(session)
	if err != nil {
		return fmt.Errorf("invalid session for manifest: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := monitorPlatformSupported(); err != nil {
		return fmt.Errorf("manifest mutation requires process-shared locking: %w", err)
	}
	dir := ManifestDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating manifest directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("manifest directory must be a real directory: %s", dir)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("restricting manifest directory permissions: %w", err)
	}
	path := filepath.Join(dir, safe+".json")
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("waiting to update manifest %s: %w", session, err)
		}
		lock, err := tryMonitorLock(filepath.Join(dir, safe+".mutation.lock"))
		if err == nil {
			defer lock.Close()
			if err := ctx.Err(); err != nil {
				return err
			}
			return apply(path)
		}
		if !errors.Is(err, ErrSessionMonitorOwned) {
			return fmt.Errorf("lock manifest %s for update: %w", session, err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting to update manifest %s: %w", session, ctx.Err())
		case <-timer.C:
		}
	}
}

// SaveManifest intentionally replaces the complete session snapshot. It shares
// the mutation fence with UpsertAgentConfig and DeleteManifest, but is not a
// merge or a compare-and-swap for snapshots loaded before this call. Callers
// updating individual agents must use UpsertAgentConfig instead.
func SaveManifest(manifest *SpawnManifest) error {
	if manifest == nil {
		return errors.New("cannot save a nil manifest")
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}
	return mutateManifest(context.Background(), manifest.Session, func(path string) error {
		return util.AtomicWriteFile(path, data, 0600)
	})
}

// LoadManifest loads the spawn manifest for a session.
func LoadManifest(session string) (*SpawnManifest, error) {
	safe, err := sanitizeSessionName(session)
	if err != nil {
		return nil, fmt.Errorf("invalid session for manifest: %w", err)
	}
	return loadManifestFile(filepath.Join(ManifestDir(), safe+".json"), session)
}

// Readers do not acquire the mutation fence: atomic replacement gives them one
// complete version while a writer holds the fence through read/modify/write.
func loadManifestFile(path, session string) (*SpawnManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	var manifest *SpawnManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("unmarshaling manifest: %w", err)
	}
	if manifest == nil {
		return nil, errors.New("manifest must be a JSON object, not null")
	}
	if manifest.Session != "" && manifest.Session != session {
		return nil, fmt.Errorf("manifest session %q does not match requested session %q", manifest.Session, session)
	}
	return manifest, nil
}

// DeleteManifest removes the manifest, but never unlinks its mutation fence.
func DeleteManifest(session string) error {
	safe, err := sanitizeSessionName(session)
	if err != nil {
		return fmt.Errorf("invalid session for manifest: %w", err)
	}
	path := filepath.Join(ManifestDir(), safe+".json")
	// A missing file is already deleted at this instant. Do not create a
	// directory/lock merely to delete an absent session.
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return mutateManifest(context.Background(), session, func(path string) error {
		if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return util.SyncDirectory(filepath.Dir(path))
	})
}

// UpsertAgentConfig persists one agent without losing rows written by another
// process. The latest manifest is read only after acquiring the session fence;
// an atomic rename alone cannot protect a read/modify/write from lost updates.
// All existing session policy and unrelated agent rows are preserved. A missing
// manifest starts with auto-restart disabled, as before. This does not grant
// permission to restart an agent or change the resident monitor's generation.
func UpsertAgentConfig(session, projectDir string, agent AgentConfig) error {
	if strings.TrimSpace(agent.PaneID) == "" {
		return errors.New("cannot persist agent restart metadata without a pane ID")
	}
	agent.LaunchBinding = CloneLaunchBinding(agent.LaunchBinding)
	return mutateManifest(context.Background(), session, func(path string) error {
		manifest, err := loadManifestFile(path, session)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			manifest = &SpawnManifest{Session: session, ProjectDir: projectDir, Agents: []AgentConfig{}}
		}
		if manifest.Session == "" {
			manifest.Session = session
		}
		if strings.TrimSpace(manifest.ProjectDir) == "" {
			manifest.ProjectDir = projectDir
		}
		match := -1
		for i := range manifest.Agents {
			if manifest.Agents[i].PaneID == agent.PaneID {
				if match >= 0 {
					return fmt.Errorf("manifest contains duplicate restart records for pane %s", agent.PaneID)
				}
				match = i
			}
		}
		if match >= 0 {
			manifest.Agents[match] = agent
		} else {
			manifest.Agents = append(manifest.Agents, agent)
		}
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling manifest: %w", err)
		}
		// Do not call SaveManifest here: it acquires the same fence. Keep the
		// locked transaction on the exact path selected before reading.
		return util.AtomicWriteFile(path, data, 0600)
	})
}

// RebindAgentPaneContext moves an observed restart record to its replacement
// physical pane without changing its launch settings or session policy. A
// rotation must not leave the resident monitor watching its retired pane, or
// replace launch metadata concurrently changed by another process.
//
// The caller owns the rotation's durable continuation barrier and must verify
// the two live process identities before calling. A false result means the
// observed record is no longer current; it does not authorize retiring either
// process. Retrying an already-persisted, exactly matching replacement succeeds.
func RebindAgentPaneContext(ctx context.Context, session string, expected AgentConfig, newPaneID string, newPaneIndex int) (bool, error) {
	validPaneID := func(id string) bool {
		if len(id) < 2 || id[0] != '%' {
			return false
		}
		for _, ch := range id[1:] {
			if ch < '0' || ch > '9' {
				return false
			}
		}
		return true
	}
	if !validPaneID(expected.PaneID) || !validPaneID(newPaneID) || expected.PaneID == newPaneID || newPaneIndex < 0 {
		return false, errors.New("manifest continuation requires distinct physical pane IDs and a non-negative pane index")
	}
	expected.LaunchBinding = CloneLaunchBinding(expected.LaunchBinding)
	replacement := expected
	replacement.PaneID = newPaneID
	replacement.PaneIndex = newPaneIndex
	applied := false
	err := mutateManifest(ctx, session, func(path string) error {
		manifest, err := loadManifestFile(path, session)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		oldIndex, newIndex := -1, -1
		for i := range manifest.Agents {
			switch manifest.Agents[i].PaneID {
			case expected.PaneID:
				if oldIndex >= 0 {
					return fmt.Errorf("manifest contains duplicate restart records for pane %s", expected.PaneID)
				}
				oldIndex = i
			case newPaneID:
				if newIndex >= 0 {
					return fmt.Errorf("manifest contains duplicate restart records for pane %s", newPaneID)
				}
				newIndex = i
			}
		}
		if oldIndex < 0 {
			applied = newIndex >= 0 && reflect.DeepEqual(manifest.Agents[newIndex], replacement)
			return nil
		}
		if !reflect.DeepEqual(manifest.Agents[oldIndex], expected) {
			return nil
		}
		if newIndex >= 0 {
			return fmt.Errorf("replacement pane %s already has restart metadata", newPaneID)
		}
		manifest.Agents[oldIndex] = replacement
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling manifest continuation: %w", err)
		}
		if err := util.AtomicWriteFile(path, data, 0600); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}
