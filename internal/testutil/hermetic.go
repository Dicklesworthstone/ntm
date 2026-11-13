// Package testutil provides hermetic test environment isolation helpers shared
// across unit and end-to-end suites (GH #341).
package testutil

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

var (
	goEnvOnce     sync.Once
	cachedGoCache string
	cachedModDir  string
)

func resolveHostGoCaches() (goCache, goModCache string) {
	goEnvOnce.Do(func() {
		cachedGoCache = strings.TrimSpace(os.Getenv("GOCACHE"))
		cachedModDir = strings.TrimSpace(os.Getenv("GOMODCACHE"))
		if cachedGoCache != "" && cachedModDir != "" {
			return
		}
		out, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE").Output()
		if err != nil {
			return
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if cachedGoCache == "" && len(lines) >= 1 {
			cachedGoCache = strings.TrimSpace(lines[0])
		}
		if cachedModDir == "" && len(lines) >= 2 {
			cachedModDir = strings.TrimSpace(lines[1])
		}
	})
	return cachedGoCache, cachedModDir
}

// HermeticSandbox describes the isolated directory layout and scrubbed ambient
// environment variables applied to a test via NewHermeticSandbox / HermeticEnv.
type HermeticSandbox struct {
	Root          string   `json:"root"`
	HomeDir       string   `json:"home_dir"`
	ConfigHome    string   `json:"xdg_config_home"`
	DataHome      string   `json:"xdg_data_home"`
	StateHome     string   `json:"xdg_state_home"`
	CacheHome     string   `json:"xdg_cache_home"`
	ScrubbedKeys  []string `json:"scrubbed_keys"`
	PreservedKeys []string `json:"preserved_keys,omitempty"`
}

type hermeticConfig struct {
	preservePrefixes []string
	preserveKeys     map[string]bool
}

// HermeticOption customizes HermeticEnv / NewHermeticSandbox behavior.
type HermeticOption func(*hermeticConfig)

// WithPreservePrefixes retains matching environment variables (for example
// "NTM_E2E_" harness configuration knobs) that would otherwise be scrubbed.
func WithPreservePrefixes(prefixes ...string) HermeticOption {
	return func(cfg *hermeticConfig) {
		for _, p := range prefixes {
			if p != "" {
				cfg.preservePrefixes = append(cfg.preservePrefixes, p)
			}
		}
	}
}

// WithPreserveKeys retains specific environment variables (for example
// "NTM_TMUX_BINARY" when a test process pre-configured an isolated binary).
func WithPreserveKeys(keys ...string) HermeticOption {
	return func(cfg *hermeticConfig) {
		if cfg.preserveKeys == nil {
			cfg.preserveKeys = make(map[string]bool, len(keys))
		}
		for _, k := range keys {
			if k != "" {
				cfg.preserveKeys[k] = true
			}
		}
	}
}

// HermeticEnv scrubs ambient AGENT_MAIL_*, TMUX, TMUX_PANE, NTM_*, BEADS_DB,
// and BD_* environment variables via t.Setenv, isolates HOME and XDG_*
// directories inside t.TempDir(), and registers a failure diagnostic summary
// via t.Cleanup. It returns the sandbox root path.
func HermeticEnv(t testing.TB, opts ...HermeticOption) string {
	t.Helper()
	return NewHermeticSandbox(t, opts...).Root
}

// NewHermeticSandbox creates a hermetic environment sandbox for t and returns
// its resolved directory and environment metadata.
func NewHermeticSandbox(t testing.TB, opts ...HermeticOption) HermeticSandbox {
	t.Helper()

	cfg := hermeticConfig{
		preserveKeys: make(map[string]bool),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	// Pin Go build/module caches before isolating HOME and XDG_CACHE_HOME so any
	// child `go build` / `go test` invocations do not suffer cold-cache rebuilds.
	goCache, goModCache := resolveHostGoCaches()
	if goCache != "" {
		t.Setenv("GOCACHE", goCache)
	}
	if goModCache != "" {
		t.Setenv("GOMODCACHE", goModCache)
	}

	isPreserved := func(key string) bool {
		if cfg.preserveKeys[key] {
			return true
		}
		for _, prefix := range cfg.preservePrefixes {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		}
		return false
	}

	shouldScrub := func(key string) bool {
		switch key {
		case "TMUX", "TMUX_PANE", "BEADS_DB":
			return true
		case "TMUX_TMPDIR":
			// Preserve isolated tmux server socket roots established by TestMain.
			return false
		}
		return strings.HasPrefix(key, "AGENT_MAIL_") ||
			strings.HasPrefix(key, "NTM_") ||
			strings.HasPrefix(key, "BD_")
	}

	scrubSet := map[string]bool{
		"TMUX":      true,
		"TMUX_PANE": true,
		"BEADS_DB":  true,
	}
	preservedSet := make(map[string]bool)

	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "" || !shouldScrub(key) {
			continue
		}
		if isPreserved(key) {
			delete(scrubSet, key)
			preservedSet[key] = true
			continue
		}
		scrubSet[key] = true
	}

	for key := range scrubSet {
		if isPreserved(key) {
			delete(scrubSet, key)
			preservedSet[key] = true
		}
	}

	scrubbedKeys := make([]string, 0, len(scrubSet))
	for key := range scrubSet {
		scrubbedKeys = append(scrubbedKeys, key)
	}
	sort.Strings(scrubbedKeys)
	for _, key := range scrubbedKeys {
		t.Setenv(key, "")
	}

	preservedKeys := make([]string, 0, len(preservedSet))
	for key := range preservedSet {
		preservedKeys = append(preservedKeys, key)
	}
	sort.Strings(preservedKeys)

	root := t.TempDir()
	sandbox := HermeticSandbox{
		Root:          root,
		HomeDir:       filepath.Join(root, "home"),
		ConfigHome:    filepath.Join(root, "xdg-config"),
		DataHome:      filepath.Join(root, "xdg-data"),
		StateHome:     filepath.Join(root, "xdg-state"),
		CacheHome:     filepath.Join(root, "xdg-cache"),
		ScrubbedKeys:  scrubbedKeys,
		PreservedKeys: preservedKeys,
	}

	for _, dir := range []string{
		sandbox.HomeDir,
		sandbox.ConfigHome,
		sandbox.DataHome,
		sandbox.StateHome,
		sandbox.CacheHome,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("hermetic env mkdir %s: %v", dir, err)
		}
	}

	t.Setenv("HOME", sandbox.HomeDir)
	t.Setenv("XDG_CONFIG_HOME", sandbox.ConfigHome)
	t.Setenv("XDG_DATA_HOME", sandbox.DataHome)
	t.Setenv("XDG_STATE_HOME", sandbox.StateHome)
	t.Setenv("XDG_CACHE_HOME", sandbox.CacheHome)

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		diag := struct {
			Test          string         `json:"test"`
			Root          string         `json:"root"`
			HomeDir       string         `json:"home_dir"`
			ConfigHome    string         `json:"xdg_config_home"`
			DataHome      string         `json:"xdg_data_home"`
			StateHome     string         `json:"xdg_state_home"`
			CacheHome     string         `json:"xdg_cache_home"`
			TmuxTmpDir    string         `json:"tmux_tmpdir,omitempty"`
			ScrubbedKeys  []string       `json:"scrubbed_keys"`
			PreservedKeys []string       `json:"preserved_keys,omitempty"`
			EntryCounts   map[string]int `json:"entry_counts"`
		}{
			Test:          t.Name(),
			Root:          sandbox.Root,
			HomeDir:       sandbox.HomeDir,
			ConfigHome:    sandbox.ConfigHome,
			DataHome:      sandbox.DataHome,
			StateHome:     sandbox.StateHome,
			CacheHome:     sandbox.CacheHome,
			TmuxTmpDir:    os.Getenv("TMUX_TMPDIR"),
			ScrubbedKeys:  sandbox.ScrubbedKeys,
			PreservedKeys: sandbox.PreservedKeys,
			EntryCounts: map[string]int{
				"home":       countDirEntries(sandbox.HomeDir),
				"xdg_config": countDirEntries(sandbox.ConfigHome),
				"xdg_data":   countDirEntries(sandbox.DataHome),
				"xdg_state":  countDirEntries(sandbox.StateHome),
				"xdg_cache":  countDirEntries(sandbox.CacheHome),
			},
		}
		if raw, err := json.Marshal(diag); err == nil {
			t.Logf("[HERMETIC-DIAG] %s", raw)
		}
	})

	return sandbox
}

func countDirEntries(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return -1
	}
	return len(entries)
}
