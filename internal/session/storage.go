package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	sessionDirName = "sessions"
	archiveDirName = "archived"
	fileExtension  = ".json"
)

// storedSessionState separates private recovery inputs from the public session
// JSON used by status/show surfaces. Pi's live process can inherit a store from
// an environment that no longer exists when the save is resumed. Persist the
// exact discovered transcript in the existing owner-only save, without making
// transcript paths part of PaneState's public JSON contract.
type storedSessionState struct {
	SessionState
	PiResumeTranscripts []storedPiResumeTranscript `json:"pi_resume_transcripts,omitempty"`
}

type storedPiResumeTranscript struct {
	Pane      int    `json:"pane"` // Position in the saved panes array, not a tmux index.
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
}

func (stored *storedSessionState) restorePiResumeTranscripts() error {
	seen := make(map[int]bool, len(stored.PiResumeTranscripts))
	for _, reference := range stored.PiResumeTranscripts {
		if reference.Pane < 0 || reference.Pane >= len(stored.Panes) || seen[reference.Pane] {
			return fmt.Errorf("saved pi transcript reference has an invalid or duplicate pane")
		}
		pane := &stored.Panes[reference.Pane]
		if agentsession.ResumeProvider(pane.AgentType) != "pi" || pane.SessionProvider != "pi" ||
			!pane.HasFreshSessionBinding() || reference.SessionID != pane.SessionID || !filepath.IsAbs(reference.Path) {
			return fmt.Errorf("saved pi transcript reference does not match its pane binding")
		}
		seen[reference.Pane] = true
		pane.SessionFile = filepath.Clean(reference.Path)
	}
	return nil
}

func decodeStoredSession(data []byte) (*SessionState, error) {
	var stored storedSessionState
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}
	if err := stored.restorePiResumeTranscripts(); err != nil {
		return nil, err
	}
	return &stored.SessionState, nil
}

// StorageDir returns the path to the session storage directory.
// Uses ~/.ntm/sessions by default.
// Falls back to temp directory if home directory is unavailable.
func StorageDir() string {
	ntmDir, err := util.NTMDir()
	if err != nil || ntmDir == "" {
		// Fallback to temp directory to ensure an absolute path
		// (relative paths would fragment sessions across working directories)
		return filepath.Join(os.TempDir(), "ntm", sessionDirName)
	}
	return filepath.Join(ntmDir, sessionDirName)
}

// ArchiveDir returns the path to the archived-session storage directory
// (~/.ntm/sessions/archived). Archiving moves a saved session here so it no
// longer appears in the active `ntm sessions list` while remaining restorable.
func ArchiveDir() string {
	return filepath.Join(StorageDir(), archiveDirName)
}

// Save writes a session state to disk.
func Save(state *SessionState, opts SaveOptions) (path string, err error) {
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	sessionName := ""
	if state != nil {
		sessionName = state.Name
	}
	saveName := opts.Name
	if saveName == "" {
		saveName = sessionName
	}
	_ = audit.LogEvent(sessionName, audit.EventTypeCommand, audit.ActorSystem, "session.save", map[string]interface{}{
		"phase":           "start",
		"session":         sessionName,
		"name":            saveName,
		"overwrite":       opts.Overwrite,
		"include_git":     opts.IncludeGit,
		"description_set": opts.Description != "",
		"correlation_id":  correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":          "finish",
			"session":        sessionName,
			"name":           saveName,
			"overwrite":      opts.Overwrite,
			"success":        err == nil,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if state != nil {
			payload["agents"] = state.Agents.Total()
			payload["panes"] = len(state.Panes)
			payload["work_dir"] = state.WorkDir
		}
		if path != "" {
			payload["path"] = path
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(sessionName, audit.EventTypeCommand, audit.ActorSystem, "session.save", payload, nil)
	}()

	if state == nil {
		return "", fmt.Errorf("session state cannot be nil")
	}

	unlock, err := acquireLock()
	if err != nil {
		return "", err
	}
	defer unlock()

	dir := StorageDir()

	// Ensure directory exists
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create sessions directory: %w", err)
	}

	// Determine filename
	name := opts.Name
	if name == "" {
		name = state.Name
	}

	// Sanitize filename
	name, err = normalizeSavedSessionName(name)
	if err != nil {
		return "", err
	}
	filename := name + fileExtension
	path = filepath.Join(dir, filename)

	// Check if file exists
	if !opts.Overwrite {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("session '%s' already saved (use --overwrite to replace)", name)
		}
	}

	// Only pi requires the exact captured transcript to disambiguate a store
	// inherited by its process. Other providers retain their existing format.
	stored := storedSessionState{SessionState: *state}
	for i, pane := range state.Panes {
		if agentsession.ResumeProvider(pane.AgentType) == "pi" && pane.SessionFile != "" && pane.HasFreshSessionBinding() {
			stored.PiResumeTranscripts = append(stored.PiResumeTranscripts, storedPiResumeTranscript{
				Pane: i, SessionID: pane.SessionID, Path: pane.SessionFile,
			})
		}
	}
	if len(stored.PiResumeTranscripts) > 0 {
		// Validate references before writing without mutating the caller's
		// panes. Saves without private references retain their exact encoding.
		stored.Panes = append([]PaneState(nil), state.Panes...)
		if err := stored.restorePiResumeTranscripts(); err != nil {
			return "", err
		}
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to serialize session state: %w", err)
	}

	// Atomic write using utility function
	if err := util.AtomicWriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("writing session file: %w", err)
	}

	return path, nil
}

// Load reads a session state from disk.
func Load(name string) (state *SessionState, err error) {
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	sanitized := sanitizeFilename(name)
	_ = audit.LogEvent(name, audit.EventTypeCommand, audit.ActorSystem, "session.load", map[string]interface{}{
		"phase":          "start",
		"requested":      name,
		"sanitized":      sanitized,
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":          "finish",
			"requested":      name,
			"sanitized":      sanitized,
			"success":        err == nil,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if state != nil {
			payload["session"] = state.Name
			payload["agents"] = state.Agents.Total()
			payload["panes"] = len(state.Panes)
			payload["work_dir"] = state.WorkDir
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(name, audit.EventTypeCommand, audit.ActorSystem, "session.load", payload, nil)
	}()

	name, err = normalizeSavedSessionName(name)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(StorageDir(), name+fileExtension)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no saved session named '%s'", name)
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	return decodeStoredSession(data)
}

// Delete removes a saved session.
func Delete(name string) (err error) {
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	sanitized := sanitizeFilename(name)
	_ = audit.LogEvent(name, audit.EventTypeCommand, audit.ActorSystem, "session.delete", map[string]interface{}{
		"phase":          "start",
		"requested":      name,
		"sanitized":      sanitized,
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":          "finish",
			"requested":      name,
			"sanitized":      sanitized,
			"deleted":        err == nil,
			"success":        err == nil,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(name, audit.EventTypeCommand, audit.ActorSystem, "session.delete", payload, nil)
	}()

	unlock, err := acquireLock()
	if err != nil {
		return err
	}
	defer unlock()

	name, err = normalizeSavedSessionName(name)
	if err != nil {
		return err
	}
	path := filepath.Join(StorageDir(), name+fileExtension)

	err = os.Remove(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("no saved session named '%s'", name)
	}
	return err
}

// SavedSession represents a saved session entry.
type SavedSession struct {
	Name      string    `json:"name"`
	SavedAt   time.Time `json:"saved_at"`
	WorkDir   string    `json:"cwd"`
	Agents    int       `json:"agents"`
	GitBranch string    `json:"git_branch,omitempty"`
	FilePath  string    `json:"file_path"`
	FileSize  int64     `json:"file_size"`
}

// List returns all saved sessions.
func List() ([]SavedSession, error) {
	unlock, err := acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	dir := StorageDir()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SavedSession{}, nil
		}
		return nil, fmt.Errorf("failed to read sessions directory: %w", err)
	}

	var sessions []SavedSession
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), fileExtension) {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), fileExtension)
		path := filepath.Join(dir, entry.Name())

		// Load minimal info
		state, err := Load(name)
		if err != nil {
			continue // Skip corrupted files
		}

		info, _ := entry.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}

		sessions = append(sessions, SavedSession{
			// List/save/show/delete address saved sessions by the on-disk entry name (filename),
			// not by the original tmux session name captured in the state.
			Name:      name,
			SavedAt:   state.SavedAt,
			WorkDir:   state.WorkDir,
			Agents:    state.Agents.Total(),
			GitBranch: state.GitBranch,
			FilePath:  path,
			FileSize:  size,
		})
	}

	// Sort by save time (newest first)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].SavedAt.After(sessions[j].SavedAt)
	})

	return sessions, nil
}

// Exists checks if a saved session exists.
func Exists(name string) bool {
	name, err := normalizeSavedSessionName(name)
	if err != nil {
		return false
	}
	path := filepath.Join(StorageDir(), name+fileExtension)
	_, err = os.Stat(path)
	return err == nil
}

// sanitizeFilename removes or replaces characters not suitable for filenames.
func sanitizeFilename(name string) string {
	// Replace problematic characters
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	return replacer.Replace(name)
}

// Archive moves a saved session from the active store into the archived store.
// The session remains restorable via ListArchived/Unarchive but no longer
// appears in List(). Returns the new archived file path.
func Archive(name string) (archivedPath string, err error) {
	unlock, err := acquireLock()
	if err != nil {
		return "", err
	}
	defer unlock()

	name, err = normalizeSavedSessionName(name)
	if err != nil {
		return "", err
	}

	src := filepath.Join(StorageDir(), name+fileExtension)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no saved session named '%s'", name)
		}
		return "", err
	}

	archiveDir := ArchiveDir()
	if err := os.MkdirAll(archiveDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create archive directory: %w", err)
	}

	dst := filepath.Join(archiveDir, name+fileExtension)
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("session '%s' is already archived", name)
	}

	if err := moveFile(src, dst); err != nil {
		return "", fmt.Errorf("archiving session: %w", err)
	}
	return dst, nil
}

// Unarchive moves an archived session back into the active store, making it
// appear in List() again. Returns the restored active file path.
func Unarchive(name string) (restoredPath string, err error) {
	unlock, err := acquireLock()
	if err != nil {
		return "", err
	}
	defer unlock()

	name, err = normalizeSavedSessionName(name)
	if err != nil {
		return "", err
	}

	src := filepath.Join(ArchiveDir(), name+fileExtension)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no archived session named '%s'", name)
		}
		return "", err
	}

	dir := StorageDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create sessions directory: %w", err)
	}

	dst := filepath.Join(dir, name+fileExtension)
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("an active session named '%s' already exists", name)
	}

	if err := moveFile(src, dst); err != nil {
		return "", fmt.Errorf("unarchiving session: %w", err)
	}
	return dst, nil
}

// ListArchived returns all archived saved sessions.
func ListArchived() ([]SavedSession, error) {
	unlock, err := acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	dir := ArchiveDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SavedSession{}, nil
		}
		return nil, fmt.Errorf("failed to read archive directory: %w", err)
	}

	var sessions []SavedSession
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), fileExtension) {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), fileExtension)
		path := filepath.Join(dir, entry.Name())

		state, err := loadFrom(path)
		if err != nil {
			continue // Skip corrupted files
		}

		info, _ := entry.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}

		sessions = append(sessions, SavedSession{
			Name:      name,
			SavedAt:   state.SavedAt,
			WorkDir:   state.WorkDir,
			Agents:    state.Agents.Total(),
			GitBranch: state.GitBranch,
			FilePath:  path,
			FileSize:  size,
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].SavedAt.After(sessions[j].SavedAt)
	})

	return sessions, nil
}

// loadFrom reads and parses a session state file at an explicit path. Unlike
// Load, it does not assume the file lives in the active StorageDir, so it works
// for archived sessions too.
func loadFrom(path string) (*SessionState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeStoredSession(data)
}

// moveFile relocates a file from src to dst, falling back to copy+remove when
// the two paths are on different filesystems (cross-device rename).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := util.AtomicWriteFile(dst, data, 0600); err != nil {
		return err
	}
	return os.Remove(src)
}

func normalizeSavedSessionName(name string) (string, error) {
	sanitized := strings.TrimSpace(sanitizeFilename(name))
	if sanitized == "" {
		return "", fmt.Errorf("session name cannot be empty")
	}
	if sanitized == "." || sanitized == ".." {
		return "", fmt.Errorf("session name cannot be '.' or '..'")
	}
	return sanitized, nil
}
