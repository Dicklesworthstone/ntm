package serve

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func recoveryAPIGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func recoveryAPIWrite(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func recoveryAPIRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	recoveryAPIGit(t, root, "init", "-q", "-b", "main")
	recoveryAPIGit(t, root, "config", "user.name", "Checkpoint tests")
	recoveryAPIGit(t, root, "config", "user.email", "checkpoint@example.invalid")
	recoveryAPIGit(t, root, "config", "commit.gpgsign", "false")
	recoveryAPIWrite(t, root, "tracked.txt", []byte("base\n"))
	recoveryAPIWrite(t, root, "asset.bin", []byte{0, 1, 255})
	recoveryAPIWrite(t, root, "sub/.keep", nil)
	recoveryAPIGit(t, root, "add", ".")
	recoveryAPIGit(t, root, "commit", "-qm", "base")
	return root, recoveryAPIGit(t, root, "rev-parse", "HEAD")
}

func recoveryAPIRequest(t *testing.T, s *Server, session, body string, create bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", session)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	if create {
		s.handleCreateCheckpoint(w, r)
	} else {
		s.handleRollback(w, r)
	}
	return w
}

func recoveryAPISession(t *testing.T, root string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("requires a tmux session")
	}
	testutil.RequireTmuxThrottled(t)
	session := fmt.Sprintf("ntm-api-capture-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })
	return session
}

func recoveryAPICheckpoint(t *testing.T, root, commit string) *checkpoint.Checkpoint {
	t.Helper()
	recoveryAPIWrite(t, root, "tracked.txt", []byte("saved\n"))
	cmd := exec.Command("git", "-C", root, "diff", "--binary", "--full-index", commit, "--")
	patch, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	cp := &checkpoint.Checkpoint{Version: checkpoint.CurrentVersion, ID: "git-recovery", SessionName: "recovery-test", WorkingDir: root, CreatedAt: time.Now(),
		Git: checkpoint.GitState{Captured: true, Commit: commit, IsDirty: true, PatchFile: checkpoint.GitPatchFile}}
	storage := checkpoint.NewStorage()
	if err := storage.SaveGitPatch(cp.SessionName, cp.ID, string(patch)); err != nil {
		t.Fatal(err)
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	return cp
}

func TestCheckpointAPIIncludeUntrackedRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a tmux session")
	}
	testutil.RequireTmuxThrottled(t)
	root, _ := recoveryAPIRepo(t)
	session := fmt.Sprintf("ntm-api-code-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })
	savedBinary := []byte{0, 252, 3, 9, 255}
	recoveryAPIWrite(t, root, "asset.bin", savedBinary)
	recoveryAPIWrite(t, root, "new.go", []byte("package saved\n"))
	s := &Server{projectDir: root}
	w := recoveryAPIRequest(t, s, session, `{"name":"code","include_untracked":true,"scrollback_lines":0}`, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("capture HTTP %d: %s", w.Code, w.Body.String())
	}
	cp, err := checkpoint.NewStorage().GetLatest(session)
	if err != nil || cp.Git.UntrackedCaptured != 1 {
		t.Fatalf("request did not persist untracked capture: %+v, %v", cp, err)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"untracked_captured":1`)) {
		t.Fatalf("response omitted preserved-file count: %s", w.Body.String())
	}
	recoveryAPIWrite(t, root, "asset.bin", []byte{0, 111, 222})
	recoveryAPIWrite(t, root, "new.go", []byte("package later\n"))
	recoveryAPIWrite(t, root, "later-only.go", []byte("package current\n"))
	w = recoveryAPIRequest(t, s, session, fmt.Sprintf(`{"checkpoint_ref":%q}`, cp.ID), false)
	if w.Code != http.StatusOK {
		t.Fatalf("rollback HTTP %d: %s", w.Code, w.Body.String())
	}
	if got, err := os.ReadFile(filepath.Join(root, "asset.bin")); err != nil || !bytes.Equal(got, savedBinary) {
		t.Fatalf("API did not restore binary bytes: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "new.go")); err != nil || string(got) != "package saved\n" {
		t.Fatalf("API did not restore new source: %q, %v", got, err)
	}
	if got := recoveryAPIGit(t, root, "show", "stash@{0}^3:new.go"); got != "package later" {
		t.Fatalf("pre-rollback untracked work was not stashed: %q", got)
	}
	if got := recoveryAPIGit(t, root, "show", "stash@{0}^3:later-only.go"); got != "package current" {
		t.Fatalf("other current untracked work was not preserved: %q", got)
	}
}

func TestCheckpointAPIRejectsIncompatibleCaptureBeforeSessionLookup(t *testing.T) {
	w := recoveryAPIRequest(t, &Server{}, "absent-session", `{"capture_git":false,"include_untracked":true}`, true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "include_untracked requires capture_git") {
		t.Fatalf("incompatible options were not rejected first: HTTP %d: %s", w.Code, w.Body.String())
	}
}

func TestCheckpointAPIRollbackCorruptionFailsBeforeMutation(t *testing.T) {
	root, commit := recoveryAPIRepo(t)
	cp := recoveryAPICheckpoint(t, root, commit)
	storage := checkpoint.NewStorage()
	recoveryAPIWrite(t, storage.CheckpointDir(cp.SessionName, cp.ID), checkpoint.GitPatchFile, []byte("corrupted patch\n"))
	before := recoveryAPIGit(t, root, "status", "--porcelain")
	for _, dry := range []bool{false, true} {
		w := recoveryAPIRequest(t, &Server{projectDir: root}, cp.SessionName, fmt.Sprintf(`{"checkpoint_ref":%q,"dry_run":%t}`, cp.ID, dry), false)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("corrupt checkpoint accepted (dry=%t): HTTP %d: %s", dry, w.Code, w.Body.String())
		}
		if recoveryAPIGit(t, root, "rev-parse", "HEAD") != commit || recoveryAPIGit(t, root, "status", "--porcelain") != before || recoveryAPIGit(t, root, "stash", "list") != "" {
			t.Fatal("corrupt-checkpoint rejection changed HEAD, working files or stashes")
		}
	}
}

func TestCheckpointAPIRollbackApplyFailureIsNotSuccess(t *testing.T) {
	root, commit := recoveryAPIRepo(t)
	cp := recoveryAPICheckpoint(t, root, commit)
	// A concurrent writer after preflight is modeled by a real checkout hook.
	hook := filepath.Join(root, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'changed after preflight\\n' > tracked.txt\n"), 0755); err != nil {
		t.Fatal(err)
	}
	w := recoveryAPIRequest(t, &Server{projectDir: root}, cp.SessionName, fmt.Sprintf(`{"checkpoint_ref":%q}`, cp.ID), false)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "rollback incomplete") {
		t.Fatalf("partial recovery reported success: HTTP %d: %s", w.Code, w.Body.String())
	}
	if recoveryAPIGit(t, root, "stash", "list") == "" || !strings.Contains(w.Body.String(), "stash_created") {
		t.Fatalf("failed recovery lost stash evidence: %s", w.Body.String())
	}
}

func TestCheckpointAPIRollbackCannotEscapeSubdirectoryProject(t *testing.T) {
	root, commit := recoveryAPIRepo(t)
	cp := recoveryAPICheckpoint(t, root, commit)
	cp.WorkingDir = filepath.Join(root, "sub")
	if err := checkpoint.NewStorage().Save(cp); err != nil {
		t.Fatal(err)
	}
	s := &Server{projectDir: cp.WorkingDir}
	if _, err := s.resolveCheckpointWorkDir(cp.WorkingDir); err != nil {
		t.Fatal("test subdirectory must be authorized:", err)
	}
	if _, err := s.resolveCheckpointWorkDir(root); err == nil {
		t.Fatal("test parent directory must be outside server confinement")
	}
	before := recoveryAPIGit(t, root, "status", "--porcelain")
	w := recoveryAPIRequest(t, s, cp.SessionName, fmt.Sprintf(`{"checkpoint_ref":%q}`, cp.ID), false)
	if w.Code != http.StatusForbidden || recoveryAPIGit(t, root, "status", "--porcelain") != before || recoveryAPIGit(t, root, "stash", "list") != "" {
		t.Fatalf("parent repository recovery escaped project boundary: HTTP %d: %s", w.Code, w.Body.String())
	}
}

func TestCheckpointAPIDefaultDoesNotCaptureUntrackedFiles(t *testing.T) {
	root, _ := recoveryAPIRepo(t)
	recoveryAPIWrite(t, root, "new.go", []byte("not opted in\n"))
	session := recoveryAPISession(t, root)
	w := recoveryAPIRequest(t, &Server{projectDir: root}, session, `{"name":"default","scrollback_lines":0}`, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create default: %d %s", w.Code, w.Body.String())
	}
	cp, err := checkpoint.NewStorage().GetLatest(session)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Git.UntrackedCount != 1 || cp.Git.UntrackedCaptured != 0 || cp.HasGitPatch() {
		t.Fatalf("default API capture stored unrequested files or hid their omission: %+v", cp.Git)
	}
}

func TestCheckpointAPIExplicitUntrackedCaptureFailureReturnsPartialIdentity(t *testing.T) {
	root, _ := recoveryAPIRepo(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	recoveryAPIGit(t, nested, "init", "-q", "-b", "main")
	recoveryAPIWrite(t, nested, "new.go", []byte("nested repository must not be silently omitted\n"))
	session := recoveryAPISession(t, root)
	w := recoveryAPIRequest(t, &Server{projectDir: root}, session,
		`{"name":"partial","include_untracked":true,"scrollback_lines":0}`, true)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("incomplete explicit capture returned success: %d %s", w.Code, w.Body.String())
	}
	cp, err := checkpoint.NewStorage().GetLatest(session)
	if err != nil {
		t.Fatalf("partial session checkpoint was not preserved: %v", err)
	}
	if cp.Git.Captured || cp.Git.SkipReason != checkpoint.GitSkipCaptureFailed || cp.HasGitPatch() {
		t.Fatalf("partial checkpoint falsely reports saved code: %+v", cp.Git)
	}
	if !strings.Contains(w.Body.String(), cp.ID) || !strings.Contains(w.Body.String(), "git_unavailable") {
		t.Fatalf("failure omitted the retained checkpoint identity: %s", w.Body.String())
	}
}
