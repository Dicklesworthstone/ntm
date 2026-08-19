package util

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// NTMDir returns the path to the ~/.ntm directory.
func NTMDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, ".ntm"), nil
}

// ExpandPath expands a leading "~/" (or "~\\") to the current user's home directory.
//
// It intentionally does not expand "~user/..." (which is shell-specific).
func ExpandPath(path string) string {
	if path == "" {
		return path
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}

	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	if strings.HasPrefix(path, "~\\") {
		return filepath.Join(home, path[2:])
	}

	return path
}

// EnsureDir ensures that a directory exists, creating it if necessary.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

// gitRootCache memoizes FindGitRoot results per start directory so hot paths
// (e.g. --robot-status resolving project dirs per collection) do not spawn
// `git rev-parse --show-toplevel` repeatedly for the same directory.
// Entries expire after gitRootCacheTTL; the map is bounded at
// gitRootCacheMaxEntries and reset wholesale when full (bd-6afgy).
var gitRootCache = struct {
	sync.Mutex
	entries map[string]gitRootCacheEntry
}{entries: make(map[string]gitRootCacheEntry)}

type gitRootCacheEntry struct {
	root    string
	err     error
	expires time.Time
}

const (
	gitRootCacheTTL        = 30 * time.Second
	gitRootCacheMaxEntries = 256
)

// FindGitRoot attempts to find the root of the git repository
// containing the given directory. Returns empty string if not found.
// Results (including failures) are cached for a short TTL per start
// directory; repo moves/deletions are picked up after the TTL lapses.
func FindGitRoot(startDir string) (string, error) {
	now := time.Now()

	gitRootCache.Lock()
	if entry, ok := gitRootCache.entries[startDir]; ok && now.Before(entry.expires) {
		gitRootCache.Unlock()
		return entry.root, entry.err
	}
	gitRootCache.Unlock()

	root, err := findGitRootUncached(startDir)

	gitRootCache.Lock()
	if len(gitRootCache.entries) >= gitRootCacheMaxEntries {
		gitRootCache.entries = make(map[string]gitRootCacheEntry)
	}
	gitRootCache.entries[startDir] = gitRootCacheEntry{
		root:    root,
		err:     err,
		expires: now.Add(gitRootCacheTTL),
	}
	gitRootCache.Unlock()

	return root, err
}

func findGitRootUncached(startDir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = startDir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// FindProjectConfigRoot walks up from startDir looking for a local .ntm/config.toml.
// It returns the directory that owns the project config.
func FindProjectConfigRoot(startDir string) (string, error) {
	if strings.TrimSpace(startDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		startDir = cwd
	}

	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for {
		configPath := filepath.Join(dir, ".ntm", "config.toml")
		if info, err := os.Stat(configPath); err == nil && !info.IsDir() {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("project config not found from %s", startDir)
		}
		dir = parent
	}
}

// FindBeadsRoot walks up from startDir looking for a local .beads directory.
// It returns the directory that owns the beads state.
func FindBeadsRoot(startDir string) (string, error) {
	if strings.TrimSpace(startDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		startDir = cwd
	}

	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for {
		beadsPath := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsPath); err == nil && info.IsDir() {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("beads root not found from %s", startDir)
		}
		dir = parent
	}
}

// ResolveProjectDir prefers an explicit project config root, then a beads root,
// then a git root, and finally falls back to the starting directory itself.
func ResolveProjectDir(startDir string) string {
	if strings.TrimSpace(startDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		startDir = cwd
	}

	absDir, err := filepath.Abs(startDir)
	if err == nil {
		startDir = absDir
	}

	if projectDir, err := FindProjectConfigRoot(startDir); err == nil && projectDir != "" {
		return projectDir
	}
	if beadsRoot, err := FindBeadsRoot(startDir); err == nil && beadsRoot != "" {
		return beadsRoot
	}
	if gitRoot, err := FindGitRoot(startDir); err == nil && gitRoot != "" {
		return gitRoot
	}
	return filepath.Clean(startDir)
}

func normalizeProjectCandidate(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}

	path = ExpandPath(path)
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
	}

	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return ResolveProjectDir(path)
	}

	return filepath.Clean(path)
}

func pathHasDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func pathHasFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ProjectDirScore returns a confidence score for how strongly a path looks like
// a real ntm project root. Higher scores indicate better candidates.
func ProjectDirScore(path string) int {
	path = normalizeProjectCandidate(path)
	if path == "" {
		return 0
	}

	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return 0
	}

	score := 1 // Existing directory

	if pathHasFile(filepath.Join(path, ".ntm", "config.toml")) {
		score += 8
	}
	if pathHasDir(filepath.Join(path, ".beads")) {
		score += 6
	}
	if pathHasDir(filepath.Join(path, ".git")) {
		score += 4
	}
	if gitRoot, err := FindGitRoot(path); err == nil && gitRoot != "" {
		score += 2
		if filepath.Clean(gitRoot) == filepath.Clean(path) {
			score++
		}
	}

	return score
}

// BestProjectDir selects the strongest candidate project directory from the
// provided paths. Ties preserve the first candidate order.
func BestProjectDir(candidates ...string) string {
	best := ""
	bestScore := -1
	seen := make(map[string]struct{})

	for _, candidate := range candidates {
		normalized := normalizeProjectCandidate(candidate)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}

		score := ProjectDirScore(normalized)
		if score > bestScore {
			best = normalized
			bestScore = score
		}
	}

	return best
}
