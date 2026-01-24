package ensemble

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const ensembleStateFilename = "ensemble.json"

func ensembleSessionBaseDir() string {
	ntmDir, err := util.NTMDir()
	if err != nil || ntmDir == "" {
		return filepath.Join(os.TempDir(), "ntm", "sessions")
	}
	return filepath.Join(ntmDir, "sessions")
}

func ensembleSessionDir(sessionName string) string {
	return filepath.Join(ensembleSessionBaseDir(), sessionName)
}

// SessionStatePath returns the filesystem path for an ensemble session state file.
func SessionStatePath(sessionName string) string {
	return filepath.Join(ensembleSessionDir(sessionName), ensembleStateFilename)
}

// LoadSession loads an ensemble session state from disk.
func LoadSession(sessionName string) (*EnsembleSession, error) {
	path := SessionStatePath(sessionName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ensemble state: %w", err)
	}

	var state EnsembleSession
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse ensemble state: %w", err)
	}

	if state.SessionName == "" {
		state.SessionName = sessionName
	}

	if state.CreatedAt.IsZero() {
		if info, statErr := os.Stat(path); statErr == nil {
			state.CreatedAt = info.ModTime().UTC()
		}
	}

	return &state, nil
}

// SaveSession persists an ensemble session state to disk.
func SaveSession(sessionName string, state *EnsembleSession) error {
	if state == nil {
		return errors.New("ensemble state is nil")
	}
	if sessionName == "" {
		sessionName = state.SessionName
	}
	if sessionName == "" {
		return errors.New("session name is required")
	}

	if state.SessionName == "" {
		state.SessionName = sessionName
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = time.Now().UTC()
	}

	dir := ensembleSessionDir(sessionName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create ensemble state dir: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ensemble state: %w", err)
	}

	if err := util.AtomicWriteFile(SessionStatePath(sessionName), data, 0644); err != nil {
		return fmt.Errorf("write ensemble state: %w", err)
	}

	return nil
}
