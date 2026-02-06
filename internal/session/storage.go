package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	sessionDirName = "sessions"
	fileExtension  = ".json"
)

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
	name = sanitizeFilename(name)
	filename := name + fileExtension
	path = filepath.Join(dir, filename)

	// Check if file exists
	if !opts.Overwrite {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("session '%s' already saved (use --overwrite to replace)", name)
		}
	}

	// Marshal state
	data, err := json.MarshalIndent(state, "", "  ")
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

	name = sanitizeFilename(name)
	path := filepath.Join(StorageDir(), name+fileExtension)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no saved session named '%s'", name)
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	var parsed SessionState
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}

	state = &parsed
	return state, nil
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

	name = sanitizeFilename(name)
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
	name = sanitizeFilename(name)
	path := filepath.Join(StorageDir(), name+fileExtension)
	_, err := os.Stat(path)
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
