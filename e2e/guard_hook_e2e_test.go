//go:build e2e
// +build e2e

// guard_hook_e2e_test.go — E2E proof for the REAL pre-commit guard hook
// (bd-ws1-truth-safety-l5ddi.1), exercised through the FALLBACK install path
// (Agent Mail unreachable during `ntm guards install`) in real temp git repos
// with the actual `git commit` flow running the installed hook:
//
//   1. forced-fallback install writes a hook that invokes the real check
//   2. conflicting reservation  -> commit blocked, output names the holder
//      and reservation
//   3. no conflict              -> commit passes
//   4. server unreachable       -> commit passes WITH a WARN line AND a
//      degraded-event row in the state DB (both asserted)
//   5. NTM_GUARD_STRICT=1 + unreachable -> commit BLOCKED naming the setting
//
// Every step logs hook stdout/stderr and DB rows so a CI failure is
// diagnosable from the log alone.

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

const guardUnreachableURL = "http://127.0.0.1:1/mcp/"

type guardHookFixture struct {
	t        *testing.T
	repo     string
	xdg      string
	hookPath string
}

// newGuardHookFixture creates a real git repo, isolates HOME/XDG so neither
// the developer's git config nor their real NTM state DB is touched, and
// force-installs the FALLBACK guard hook by pointing Agent Mail at an
// unreachable endpoint during `ntm guards install`.
func newGuardHookFixture(t *testing.T) *guardHookFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := t.TempDir()
	f := &guardHookFixture{t: t, repo: repo, xdg: t.TempDir()}

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "guard-e2e@example.com"},
		{"config", "user.name", "Guard E2E"},
		{"config", "commit.gpgsign", "false"},
	} {
		f.git(args...)
	}

	// Forced fallback: Agent Mail unreachable during install, so the MCP
	// fast path fails and installFallbackGuard writes the script.
	out, code := f.runNTM(repo, map[string]string{"AGENT_MAIL_URL": guardUnreachableURL},
		"guards", "install")
	t.Logf("STEP install (forced fallback): exit=%d output:\n%s", code, out)
	if code != 0 {
		t.Fatalf("guards install failed: exit=%d output=%s", code, out)
	}
	if !strings.Contains(out, "fallback") {
		t.Fatalf("install did not take the fallback path; output=%s", out)
	}

	f.hookPath = filepath.Join(repo, ".git", "hooks", "pre-commit")
	script, err := os.ReadFile(f.hookPath)
	if err != nil {
		t.Fatalf("read installed hook: %v", err)
	}
	t.Logf("STEP installed hook script:\n%s", script)
	if !strings.Contains(string(script), "ntm guards check --staged") {
		t.Fatalf("installed fallback hook does not invoke the real check:\n%s", script)
	}
	if strings.Contains(string(script), "For now, just log and pass") {
		t.Fatalf("installed fallback hook still carries the placebo:\n%s", script)
	}
	return f
}

func (f *guardHookFixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", f.repo}, args...)...)
	cmd.Env = f.baseEnv(nil)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v (%s)", args, err, out)
	}
	return string(out)
}

// baseEnv builds a hermetic environment: temp HOME (no user gitconfig), temp
// XDG_CONFIG_HOME (isolated state DB), PATH inherited (TestMain prepends the
// built ntm binary's directory).
func (f *guardHookFixture) baseEnv(extra map[string]string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + f.xdg,
		"XDG_CONFIG_HOME=" + f.xdg,
		"GIT_CONFIG_NOSYSTEM=1",
		"NTM_CONFIG=",
		"AGENT_MAIL_TOKEN=",
	}
	if term := os.Getenv("TERM"); term != "" {
		env = append(env, "TERM="+term)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func (f *guardHookFixture) runNTM(dir string, extra map[string]string, args ...string) (string, int) {
	f.t.Helper()
	cmd := exec.Command("ntm", args...)
	cmd.Dir = dir
	cmd.Env = f.baseEnv(extra)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			f.t.Fatalf("run ntm %v: %v", args, err)
		}
	}
	return string(out), code
}

func (f *guardHookFixture) stage(name, content string) {
	f.t.Helper()
	full := filepath.Join(f.repo, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatalf("write %s: %v", name, err)
	}
	f.git("add", name)
}

// commit runs a real `git commit` (which runs the installed hook) with the
// given extra env; returns combined output and exit code.
func (f *guardHookFixture) commit(msg string, extra map[string]string) (string, int) {
	f.t.Helper()
	cmd := exec.Command("git", "-C", f.repo, "commit", "-m", msg)
	cmd.Env = f.baseEnv(extra)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			f.t.Fatalf("git commit: %v", err)
		}
	}
	return string(out), code
}

func (f *guardHookFixture) statePath() string {
	return filepath.Join(f.xdg, "ntm", "state.db")
}

func (f *guardHookFixture) degradedRows() []state.GuardDegradedEvent {
	f.t.Helper()
	if _, err := os.Stat(f.statePath()); err != nil {
		return nil
	}
	store, err := state.Open(f.statePath())
	if err != nil {
		f.t.Fatalf("open state DB: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Migrate(); err != nil {
		f.t.Fatalf("migrate state DB: %v", err)
	}
	rows, err := store.ListGuardDegradedEvents(50)
	if err != nil {
		f.t.Fatalf("list degraded events: %v", err)
	}
	return rows
}

func (f *guardHookFixture) logDegradedRows(label string) []state.GuardDegradedEvent {
	rows := f.degradedRows()
	f.t.Logf("STEP %s: state DB %s has %d degraded-event row(s)", label, f.statePath(), len(rows))
	for _, r := range rows {
		f.t.Logf("  row id=%d repo=%s reason=%s detail=%q at=%s", r.ID, r.RepoPath, r.Reason, r.Detail, r.CreatedAt)
	}
	return rows
}

// startGuardMailServer serves the minimal Agent Mail MCP surface the guard
// uses; reservations listed cover pattern "reserved.txt" held by holder.
func startGuardMailServer(t *testing.T, reservations []map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == "resources/read":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"error": map[string]any{"code": -32601, "message": "resource view not supported"},
			})
		case request.Method == "tools/call" && (request.Params.Name == "list_file_reservations" || request.Params.Name == "list_reservations"):
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": reservations})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"error": map[string]any{"code": -32601, "message": "unsupported: " + request.Params.Name},
			})
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func guardReservation(id int, holder, pattern string) map[string]any {
	now := time.Now().UTC()
	return map[string]any{
		"id": id, "path_pattern": pattern, "agent_name": holder, "project_id": 1,
		"exclusive": true, "reason": "guard e2e",
		"created_ts": now.Format(time.RFC3339Nano),
		"expires_ts": now.Add(time.Hour).Format(time.RFC3339Nano),
	}
}

func TestGuardHookFallbackE2E(t *testing.T) {
	f := newGuardHookFixture(t)
	server := startGuardMailServer(t, []map[string]any{guardReservation(77, "GoldFox", "reserved.txt")})
	mailEnv := map[string]string{"AGENT_MAIL_URL": server.URL + "/"}

	t.Run("ConflictingReservationBlocksCommit", func(t *testing.T) {
		f.stage("reserved.txt", "contested content\n")
		out, code := f.commit("try reserved", mailEnv)
		t.Logf("STEP conflicting commit: exit=%d output:\n%s", code, out)
		if code == 0 {
			t.Fatalf("commit touching a reserved path must be blocked; output:\n%s", out)
		}
		for _, want := range []string{"GoldFox", "#77", "reserved.txt"} {
			if !strings.Contains(out, want) {
				t.Errorf("blocking output must name the reservation (%q missing):\n%s", want, out)
			}
		}
		// Unstage so later subtests are not poisoned by the reserved path.
		f.git("reset", "-q")
	})

	t.Run("NoConflictPasses", func(t *testing.T) {
		f.stage("free.txt", "unreserved content\n")
		out, code := f.commit("free commit", mailEnv)
		t.Logf("STEP no-conflict commit: exit=%d output:\n%s", code, out)
		if code != 0 {
			t.Fatalf("commit with no conflicting reservation must pass; output:\n%s", out)
		}
		if rows := f.logDegradedRows("after clean commit"); len(rows) != 0 {
			t.Errorf("clean commit must not record degraded events, got %d", len(rows))
		}
	})

	t.Run("UnreachableServerFailsOpenVisibly", func(t *testing.T) {
		f.stage("degraded.txt", "committed while agent mail is down\n")
		out, code := f.commit("degraded commit", map[string]string{"AGENT_MAIL_URL": guardUnreachableURL})
		t.Logf("STEP degraded commit: exit=%d output:\n%s", code, out)
		if code != 0 {
			t.Fatalf("fail-open: commit must pass when Agent Mail is unreachable; output:\n%s", out)
		}
		if !strings.Contains(out, "WARN") {
			t.Errorf("degraded commit must print a WARN line; output:\n%s", out)
		}
		// The degraded-event ROW is asserted alongside the WARN precisely
		// because scrollback WARNs are unobserved by construction.
		rows := f.logDegradedRows("after degraded commit")
		if len(rows) != 1 {
			t.Fatalf("degraded commit must record exactly one degraded-event row, got %d", len(rows))
		}
		if rows[0].Reason != "agent-mail-unreachable" {
			t.Errorf("degraded row reason = %q, want agent-mail-unreachable", rows[0].Reason)
		}
	})

	t.Run("StrictModeFailsClosed", func(t *testing.T) {
		f.stage("strict.txt", "must not land while strict and degraded\n")
		out, code := f.commit("strict commit", map[string]string{
			"AGENT_MAIL_URL":   guardUnreachableURL,
			"NTM_GUARD_STRICT": "1",
		})
		t.Logf("STEP strict degraded commit: exit=%d output:\n%s", code, out)
		if code == 0 {
			t.Fatalf("NTM_GUARD_STRICT=1 with Agent Mail unreachable must BLOCK the commit; output:\n%s", out)
		}
		if !strings.Contains(out, "NTM_GUARD_STRICT") {
			t.Errorf("strict block must name NTM_GUARD_STRICT; output:\n%s", out)
		}
		f.git("reset", "-q")
	})

	t.Run("DoctorSurfacesDegradedRuns", func(t *testing.T) {
		out, code := f.runNTM(f.repo, nil, "--json", "doctor")
		t.Logf("STEP doctor: exit=%d (json length=%d)", code, len(out))
		if code != 0 {
			t.Fatalf("ntm doctor failed: %s", out)
		}
		if !strings.Contains(out, "guard-hook degraded runs") {
			t.Errorf("doctor JSON missing guard degraded check; output:\n%s", out)
		}
		if !strings.Contains(out, "ran degraded 1 time") {
			t.Errorf("doctor JSON must count the degraded run; output:\n%s", out)
		}
	})
}

// TestGuardHookInstalledScriptPinned is the E2E-level pin that a forced
// fallback install never regresses to a placebo script.
func TestGuardHookInstalledScriptPinned(t *testing.T) {
	f := newGuardHookFixture(t)
	script, err := os.ReadFile(f.hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	text := string(script)
	if strings.Contains(text, `echo "[ntm-guard] Pre-commit check passed"`) {
		t.Errorf("placebo pass line present in installed hook:\n%s", text)
	}
	if strings.HasSuffix(strings.TrimSpace(text), "exit 0") {
		t.Errorf("installed hook terminates with unconditional exit 0:\n%s", text)
	}
	if !strings.Contains(text, fmt.Sprintf("--project-key '%s'", f.repo)) &&
		!strings.Contains(text, "--project-key '/") {
		t.Errorf("installed hook missing quoted project key:\n%s", text)
	}
}
