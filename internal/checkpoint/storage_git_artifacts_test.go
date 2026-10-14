package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Test accessors for stored git artifacts. Production reads patches through
// PrepareGitRollback (61f900e6); these keep the capture and storage tests
// able to inspect what SaveGitPatch/SaveGitStatus wrote.

func (s *Storage) loadGitArtifact(sessionName, checkpointID, relPath, defaultName, kind string) (string, error) {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return "", err
	}

	target := defaultName
	if relPath != "" {
		target = relPath
	}

	path, err := resolveExistingCheckpointArtifactPath(dir, target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && relPath == "" {
			return "", nil
		}
		return "", fmt.Errorf("resolving %s path: %w", kind, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && relPath == "" {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", kind, err)
	}

	return string(data), nil
}

func (s *Storage) loadGitPatchForState(sessionName, checkpointID string, git GitState) (string, error) {
	return s.loadGitArtifact(sessionName, checkpointID, git.PatchFile, GitPatchFile, "git patch")
}

func (s *Storage) loadGitStatusForState(sessionName, checkpointID string, git GitState) (string, error) {
	return s.loadGitArtifact(sessionName, checkpointID, git.StatusFile, GitStatusFile, "git status")
}

// LoadGitPatch reads the git diff patch from the checkpoint.
func (s *Storage) LoadGitPatch(sessionName, checkpointID string) (string, error) {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return "", err
	}
	if checkpointStateExists(dir) {
		cp, err := s.Load(sessionName, checkpointID)
		if err != nil {
			return "", err
		}
		return s.loadGitPatchForState(sessionName, checkpointID, cp.Git)
	}
	return s.loadGitArtifact(sessionName, checkpointID, "", GitPatchFile, "git patch")
}

// LoadGitStatus reads the git status text from the checkpoint.
func (s *Storage) LoadGitStatus(sessionName, checkpointID string) (string, error) {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return "", err
	}
	if checkpointStateExists(dir) {
		cp, err := s.Load(sessionName, checkpointID)
		if err != nil {
			return "", err
		}
		return s.loadGitStatusForState(sessionName, checkpointID, cp.Git)
	}
	return s.loadGitArtifact(sessionName, checkpointID, "", GitStatusFile, "git status")
}

func checkpointStateExists(dir string) bool {
	for _, name := range []string{MetadataFile, SessionFile} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err == nil {
			return true
		}
		if !os.IsNotExist(err) && info != nil {
			return true
		}
	}
	return false
}
