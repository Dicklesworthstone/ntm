package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHermeticEnvScrubsAmbientVarsAndIsolatesXDG(t *testing.T) {
	t.Setenv("AGENT_MAIL_URL", "http://ambient.invalid:8765")
	t.Setenv("AGENT_MAIL_TOKEN", "secret-token")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,999,0")
	t.Setenv("TMUX_PANE", "%42")
	t.Setenv("TMUX_TMPDIR", "/tmp/isolated-tmux-socket-root")
	t.Setenv("NTM_CONFIG", "/ambient/ntm/config.toml")
	t.Setenv("NTM_E2E_RETAIN", "on_failure")
	t.Setenv("BEADS_DB", "/ambient/.beads/beads.db")
	t.Setenv("BD_ACTOR", "ambient-agent")

	origHome := os.Getenv("HOME")
	root := HermeticEnv(t)
	if root == "" {
		t.Fatal("HermeticEnv returned empty root")
	}

	for _, scrubbed := range []string{
		"AGENT_MAIL_URL",
		"AGENT_MAIL_TOKEN",
		"TMUX",
		"TMUX_PANE",
		"NTM_CONFIG",
		"NTM_E2E_RETAIN",
		"BEADS_DB",
		"BD_ACTOR",
	} {
		if got := os.Getenv(scrubbed); got != "" {
			t.Errorf("%s = %q, want empty after HermeticEnv", scrubbed, got)
		}
	}

	if got := os.Getenv("TMUX_TMPDIR"); got != "/tmp/isolated-tmux-socket-root" {
		t.Errorf("TMUX_TMPDIR = %q, want preserved isolated socket root", got)
	}

	for envKey, sub := range map[string]string{
		"HOME":            "home",
		"XDG_CONFIG_HOME": "xdg-config",
		"XDG_DATA_HOME":   "xdg-data",
		"XDG_STATE_HOME":  "xdg-state",
		"XDG_CACHE_HOME":  "xdg-cache",
	} {
		want := filepath.Join(root, sub)
		got := os.Getenv(envKey)
		if got != want {
			t.Errorf("%s = %q, want %q", envKey, got, want)
		}
		if got == origHome && origHome != "" {
			t.Errorf("%s still points to ambient HOME %q", envKey, origHome)
		}
		info, err := os.Stat(got)
		if err != nil || !info.IsDir() {
			t.Errorf("stat isolated %s (%s): info=%v err=%v", envKey, got, info, err)
		}
	}
}

func TestNewHermeticSandboxPreservesConfiguredPrefixesAndKeys(t *testing.T) {
	t.Setenv("NTM_E2E_ARTIFACT_ROOT", "/tmp/e2e-artifacts")
	t.Setenv("NTM_TMUX_BINARY", "/tmp/fake-tmux")
	t.Setenv("NTM_BACKGROUND_WORKER", "1")

	sb := NewHermeticSandbox(
		t,
		WithPreservePrefixes("NTM_E2E_"),
		WithPreserveKeys("NTM_TMUX_BINARY"),
	)

	if got := os.Getenv("NTM_E2E_ARTIFACT_ROOT"); got != "/tmp/e2e-artifacts" {
		t.Fatalf("NTM_E2E_ARTIFACT_ROOT = %q, want preserved value", got)
	}
	if got := os.Getenv("NTM_TMUX_BINARY"); got != "/tmp/fake-tmux" {
		t.Fatalf("NTM_TMUX_BINARY = %q, want preserved value", got)
	}
	if got := os.Getenv("NTM_BACKGROUND_WORKER"); got != "" {
		t.Fatalf("NTM_BACKGROUND_WORKER = %q, want scrubbed", got)
	}
	if len(sb.PreservedKeys) != 2 {
		t.Fatalf("PreservedKeys = %v, want 2 keys", sb.PreservedKeys)
	}
}

type stubFailedTB struct {
	testing.TB
	name     string
	tempDir  string
	failed   bool
	cleanups []func()
	logs     []string
}

func (s *stubFailedTB) Helper() {}
func (s *stubFailedTB) Name() string {
	return s.name
}
func (s *stubFailedTB) TempDir() string {
	return s.tempDir
}
func (s *stubFailedTB) Failed() bool {
	return s.failed
}
func (s *stubFailedTB) Cleanup(fn func()) {
	s.cleanups = append(s.cleanups, fn)
}
func (s *stubFailedTB) Logf(format string, args ...any) {
	s.logs = append(s.logs, fmt.Sprintf(format, args...))
}

func TestHermeticEnvLogsDiagnosticsOnlyWhenFailed(t *testing.T) {
	// Ensure ambient env restoration happens through the outer real *testing.T.
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "TMUX", "TMUX_PANE", "BEADS_DB", "GOCACHE", "GOMODCACHE"} {
		t.Setenv(k, os.Getenv(k))
	}

	passStub := &stubFailedTB{TB: t, name: "TestPassing", tempDir: t.TempDir(), failed: false}
	_ = HermeticEnv(passStub)
	for i := len(passStub.cleanups) - 1; i >= 0; i-- {
		passStub.cleanups[i]()
	}
	if len(passStub.logs) != 0 {
		t.Fatalf("passing test emitted unexpected diagnostic logs: %v", passStub.logs)
	}

	failStub := &stubFailedTB{TB: t, name: "TestFailing", tempDir: t.TempDir(), failed: true}
	_ = HermeticEnv(failStub)
	for i := len(failStub.cleanups) - 1; i >= 0; i-- {
		failStub.cleanups[i]()
	}
	if len(failStub.logs) != 1 || !strings.Contains(failStub.logs[0], "[HERMETIC-DIAG]") || !strings.Contains(failStub.logs[0], "TestFailing") {
		t.Fatalf("failing test diagnostic log = %v, want single [HERMETIC-DIAG] JSON record", failStub.logs)
	}
}
