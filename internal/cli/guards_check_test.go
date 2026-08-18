package cli

// guards_check_test.go — unit tests for the REAL pre-commit guard
// (bd-ws1-truth-safety-l5ddi.1). Pins that the installed fallback hook script
// contains the real check invocation (not the old `exit 0` placebo) and
// exercises the staged-reservation check against a fake Agent Mail httptest
// server plus real temp git repos: conflict blocks, no conflict passes,
// unreachable server fails open with a WARN AND a degraded-event row, and
// NTM_GUARD_STRICT=1 fails closed.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// --- installed-script pins ---------------------------------------------------

func TestInstallFallbackGuard_ScriptContainsRealCheck(t *testing.T) {
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "hooks", "pre-commit")

	if err := installFallbackGuard(hookPath, "/proj/key", "/repo/path"); err != nil {
		t.Fatalf("installFallbackGuard: %v", err)
	}
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	script := string(content)

	// The real check invocation must be present.
	if !strings.Contains(script, "ntm guards check --staged") {
		t.Errorf("installed hook does not invoke the real check; script:\n%s", script)
	}
	// The placebo must be gone: the old script unconditionally echoed a pass
	// line and exited 0 without checking anything.
	if strings.Contains(script, `echo "[ntm-guard] Pre-commit check passed"`) {
		t.Errorf("installed hook still contains the placebo pass line; script:\n%s", script)
	}
	if strings.Contains(script, "For now, just log and pass") {
		t.Errorf("installed hook still contains the placebo marker comment; script:\n%s", script)
	}
	// The only exit 0 allowed is the fail-open path when ntm itself is not on
	// PATH; the script must end by exec-ing the real check, not `exit 0`.
	trimmed := strings.TrimSpace(script)
	if strings.HasSuffix(trimmed, "exit 0") {
		t.Errorf("installed hook still terminates with an unconditional exit 0; script:\n%s", script)
	}
	// Marker used by install/uninstall/status detection must survive.
	if !strings.Contains(script, "ntm-precommit-guard") {
		t.Errorf("installed hook lost the ntm-precommit-guard marker")
	}

	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("stat hook: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Errorf("installed hook is not executable: mode %v", info.Mode())
	}
}

func TestInstallFallbackGuard_QuotesProjectKey(t *testing.T) {
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "pre-commit")

	key := "/tmp/it's a project"
	if err := installFallbackGuard(hookPath, key, "/repo"); err != nil {
		t.Fatalf("installFallbackGuard: %v", err)
	}
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	want := `--project-key '/tmp/it'\''s a project'`
	if !strings.Contains(string(content), want) {
		t.Errorf("project key not shell-quoted: want substring %q in:\n%s", want, string(content))
	}
}

// --- staged check against fake Agent Mail ------------------------------------

// guardTestRepo creates a real git repo in a temp dir and chdirs into it.
// Returns the resolved repo root (symlink-free, matching findGitRoot).
func guardTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "guard-test@example.com"},
		{"config", "user.name", "Guard Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	t.Chdir(dir)
	root, err := findGitRoot(dir)
	if err != nil {
		t.Fatalf("findGitRoot: %v", err)
	}
	return root
}

func guardStageFile(t *testing.T, repo, name, content string) {
	t.Helper()
	full := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if out, err := exec.Command("git", "-C", repo, "add", name).CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v (%s)", name, err, out)
	}
}

// isolateGuardState points the state DB (and Agent Mail defaults) at temp
// locations so tests never touch the developer's real state.
func isolateGuardState(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("AGENT_MAIL_TOKEN", "")
	t.Setenv(guardStrictEnv, "")
	t.Setenv(guardSelfAgentEnv, "")
	return filepath.Join(xdg, "ntm", "state.db")
}

// startFakeAgentMail serves the minimal MCP surface the guard check uses:
// resources/read is rejected (forcing the legacy tools fallback) and
// list_file_reservations returns the given reservations.
func startFakeAgentMail(t *testing.T, reservations []map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case body.Method == "resources/read":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": body.ID,
				"error": map[string]any{"code": -32601, "message": "resource view not supported"},
			})
		case body.Method == "tools/call" && (body.Params.Name == "list_file_reservations" || body.Params.Name == "list_reservations"):
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": body.ID, "result": reservations})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": body.ID,
				"error": map[string]any{"code": -32601, "message": "unsupported in guard test: " + body.Params.Name},
			})
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func exclusiveReservation(id int, holder, pattern string) map[string]any {
	now := time.Now().UTC()
	return map[string]any{
		"id": id, "path_pattern": pattern, "agent_name": holder, "project_id": 1,
		"exclusive": true, "reason": "guard test",
		"created_ts": now.Format(time.RFC3339Nano),
		"expires_ts": now.Add(time.Hour).Format(time.RFC3339Nano),
	}
}

func TestGuardsCheckStaged_ConflictBlocksNamingReservation(t *testing.T) {
	isolateGuardState(t)
	repo := guardTestRepo(t)
	guardStageFile(t, repo, "reserved.txt", "contested\n")

	server := startFakeAgentMail(t, []map[string]any{exclusiveReservation(42, "BlueLake", "reserved.txt")})
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	var stdout, stderr bytes.Buffer
	err := runGuardsCheckStaged(&stdout, &stderr, "")
	if err == nil {
		t.Fatalf("expected conflict error, got nil; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	combined := stderr.String() + err.Error()
	for _, want := range []string{"BlueLake", "#42", "reserved.txt"} {
		if !strings.Contains(combined, want) {
			t.Errorf("conflict output missing %q; stderr=%q err=%q", want, stderr.String(), err)
		}
	}
}

func TestGuardsCheckStaged_NoConflictPasses(t *testing.T) {
	dbPath := isolateGuardState(t)
	repo := guardTestRepo(t)
	guardStageFile(t, repo, "free.txt", "unreserved\n")

	server := startFakeAgentMail(t, []map[string]any{exclusiveReservation(42, "BlueLake", "reserved.txt")})
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	var stdout, stderr bytes.Buffer
	if err := runGuardsCheckStaged(&stdout, &stderr, ""); err != nil {
		t.Fatalf("expected pass, got %v; stderr=%q", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no reservation conflicts") {
		t.Errorf("pass message missing; stdout=%q", stdout.String())
	}
	assertGuardDegradedCount(t, dbPath, 0)
}

func TestGuardsCheckStaged_SelfHolderMayCommit(t *testing.T) {
	isolateGuardState(t)
	repo := guardTestRepo(t)
	guardStageFile(t, repo, "reserved.txt", "mine\n")

	server := startFakeAgentMail(t, []map[string]any{exclusiveReservation(42, "BlueLake", "reserved.txt")})
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv(guardSelfAgentEnv, "BlueLake")

	var stdout, stderr bytes.Buffer
	if err := runGuardsCheckStaged(&stdout, &stderr, ""); err != nil {
		t.Fatalf("holder's own commit should pass, got %v; stderr=%q", err, stderr.String())
	}
}

func TestGuardsCheckStaged_UnreachableFailsOpenWithDegradedRow(t *testing.T) {
	dbPath := isolateGuardState(t)
	repo := guardTestRepo(t)
	guardStageFile(t, repo, "anything.txt", "x\n")

	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")

	var stdout, stderr bytes.Buffer
	if err := runGuardsCheckStaged(&stdout, &stderr, ""); err != nil {
		t.Fatalf("fail-open expected, got %v", err)
	}
	if !strings.Contains(stderr.String(), "WARN") || !strings.Contains(stderr.String(), "degraded") {
		t.Errorf("degraded WARN missing from stderr: %q", stderr.String())
	}
	// The row is the real assertion: scrollback WARNs are unobserved.
	assertGuardDegradedCount(t, dbPath, 1)
}

func TestGuardsCheckStaged_StrictFailsClosed(t *testing.T) {
	dbPath := isolateGuardState(t)
	repo := guardTestRepo(t)
	guardStageFile(t, repo, "anything.txt", "x\n")

	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")
	t.Setenv(guardStrictEnv, "1")

	var stdout, stderr bytes.Buffer
	err := runGuardsCheckStaged(&stdout, &stderr, "")
	if err == nil {
		t.Fatalf("strict mode must fail closed when Agent Mail is unreachable")
	}
	if !strings.Contains(err.Error(), guardStrictEnv) {
		t.Errorf("strict failure must name %s; got %q", guardStrictEnv, err)
	}
	// Strict blocks the commit; nothing was allowed through unchecked, so no
	// degraded row is recorded.
	assertGuardDegradedCount(t, dbPath, 0)
}

func assertGuardDegradedCount(t *testing.T, dbPath string, want int) {
	t.Helper()
	if _, err := os.Stat(dbPath); err != nil {
		if want == 0 {
			return // DB never created — trivially zero rows
		}
		t.Fatalf("state DB missing at %s: %v", dbPath, err)
	}
	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("open state DB: %v", err)
	}
	defer func() { _ = store.Close() }()
	stats, err := store.GuardDegradedEventStats(time.Time{})
	if err != nil {
		t.Fatalf("degraded stats: %v", err)
	}
	if stats.Count != want {
		t.Errorf("degraded event rows = %d, want %d", stats.Count, want)
	}
}

// --- doctor surfacing --------------------------------------------------------

func TestGuardDegradationCheck_SurfacesDegradedRuns(t *testing.T) {
	dbPath := isolateGuardState(t)

	check := guardDegradationCheck()
	if check.Status != "ok" {
		t.Fatalf("empty ledger should be ok, got %+v", check)
	}

	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("open state DB: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate state DB: %v", err)
	}
	if err := store.RecordGuardDegradedEvent(&state.GuardDegradedEvent{
		RepoPath: "/repo", ProjectKey: "/repo", Reason: "agent-mail-unreachable", Detail: "dial refused",
	}); err != nil {
		t.Fatalf("record degraded event: %v", err)
	}
	_ = store.Close()

	check = guardDegradationCheck()
	if check.Status != "warning" {
		t.Fatalf("degraded runs must surface as a doctor warning, got %+v", check)
	}
	if !strings.Contains(check.Message, "degraded 1 time") {
		t.Errorf("doctor message should count degraded runs: %q", check.Message)
	}
}

// TestGuardDegradationCheck_WindowsOldEvents (bd-2c0yh.2): doctor reports a
// WINDOW, not an all-time count — an incident older than the window must not
// keep the light red forever.
func TestGuardDegradationCheck_WindowsOldEvents(t *testing.T) {
	dbPath := isolateGuardState(t)

	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("open state DB: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate state DB: %v", err)
	}
	if err := store.RecordGuardDegradedEvent(&state.GuardDegradedEvent{
		RepoPath: "/repo", ProjectKey: "/repo", Reason: guardReasonUnreachable,
		Detail:    "old incident",
		CreatedAt: time.Now().UTC().Add(-guardDegradedDoctorWindow - 24*time.Hour),
	}); err != nil {
		t.Fatalf("record degraded event: %v", err)
	}
	_ = store.Close()

	check := guardDegradationCheck()
	if check.Status != "ok" {
		t.Fatalf("event outside the 7-day window must not warn, got %+v", check)
	}
}

// TestGuardDegradationCheck_DistinguishesReasons (bd-2c0yh.2): the doctor
// message must separate transport failures from application errors so an
// unregistered repo does not masquerade as a down daemon.
func TestGuardDegradationCheck_DistinguishesReasons(t *testing.T) {
	dbPath := isolateGuardState(t)

	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("open state DB: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate state DB: %v", err)
	}
	for _, ev := range []state.GuardDegradedEvent{
		{RepoPath: "/repo", Reason: guardReasonUnreachable, Detail: "dial refused"},
		{RepoPath: "/repo", Reason: guardReasonAppError, Detail: "project not found"},
	} {
		ev := ev
		if err := store.RecordGuardDegradedEvent(&ev); err != nil {
			t.Fatalf("record degraded event: %v", err)
		}
	}
	_ = store.Close()

	check := guardDegradationCheck()
	if check.Status != "warning" {
		t.Fatalf("recent degraded runs must warn, got %+v", check)
	}
	if !strings.Contains(check.Message, "1 with Agent Mail unreachable") ||
		!strings.Contains(check.Message, "1 with Agent Mail errors") {
		t.Errorf("doctor message must break down reasons: %q", check.Message)
	}
}

// TestRecordGuardDegradedEvent_PrunesPastRetention (bd-2c0yh.2): recording a
// new degraded event prunes rows past retention, so the ledger is
// self-limiting.
func TestRecordGuardDegradedEvent_PrunesPastRetention(t *testing.T) {
	dbPath := isolateGuardState(t)

	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("open state DB: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate state DB: %v", err)
	}
	if err := store.RecordGuardDegradedEvent(&state.GuardDegradedEvent{
		RepoPath: "/repo", Reason: guardReasonUnreachable, Detail: "ancient",
		CreatedAt: time.Now().UTC().Add(-guardDegradedRetention - 24*time.Hour),
	}); err != nil {
		t.Fatalf("seed old event: %v", err)
	}
	_ = store.Close()

	var stderr bytes.Buffer
	recordGuardDegradedEvent(&stderr, "/repo", "/repo", guardReasonAppError, nil)

	store, err = state.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen state DB: %v", err)
	}
	defer func() { _ = store.Close() }()
	events, err := store.ListGuardDegradedEvents(10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("retention prune must leave exactly the fresh row, got %d rows", len(events))
	}
	if events[0].Reason != guardReasonAppError {
		t.Errorf("surviving row reason = %q, want %q", events[0].Reason, guardReasonAppError)
	}
}

// TestGuardTransportError pins the transport/application split (bd-2c0yh.2).
func TestGuardTransportError(t *testing.T) {
	if !guardTransportError(agentmail.ErrServerUnavailable) {
		t.Error("ErrServerUnavailable must classify as transport")
	}
	if !guardTransportError(agentmail.ErrTimeout) {
		t.Error("ErrTimeout must classify as transport")
	}
	if guardTransportError(errors.New("project not found")) {
		t.Error("an application error from a healthy server is NOT a transport failure")
	}
}

// TestGuardHookPathCheck_WarnsWhenNTMOffPath (bd-2c0yh.4): a repo with the
// guard hook installed but no `ntm` on PATH is the one fail-open path that
// can never record a ledger row — doctor must call it out.
func TestGuardHookPathCheck_WarnsWhenNTMOffPath(t *testing.T) {
	isolateGuardState(t)
	repo := guardTestRepo(t)
	hookPath := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := installFallbackGuard(hookPath, repo, repo); err != nil {
		t.Fatalf("install hook: %v", err)
	}
	t.Chdir(repo)

	// PATH with git but no ntm.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not available: %v", err)
	}
	t.Setenv("PATH", filepath.Dir(gitPath))
	if _, err := exec.LookPath("ntm"); err == nil {
		t.Skip("ntm resolves inside git's own directory; cannot isolate PATH here")
	}

	check := guardHookPathCheck()
	if check.Status != "warning" {
		t.Fatalf("hook installed + ntm off PATH must warn, got %+v", check)
	}
	if !strings.Contains(check.Message, "PATH") {
		t.Errorf("warning must explain the PATH problem: %q", check.Message)
	}

	// With ntm resolvable the check goes green.
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "ntm")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake ntm: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+filepath.Dir(gitPath))
	check = guardHookPathCheck()
	if check.Status != "ok" {
		t.Fatalf("hook installed + ntm on PATH must be ok, got %+v", check)
	}
}
