package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// A source session already exists. Only the explicitly selected destination
// may be queried or mutated. The fake executable records complete arguments.
func restoreTargetFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("NTM_TARGET_TEST_LOG", log)
	t.Setenv("NTM_TARGET_TEST_EXISTS", "")
	bin := filepath.Join(dir, "tmux")
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_TARGET_TEST_LOG"
case "$1" in
  has-session)
    if [ "$NTM_TARGET_TEST_EXISTS" = 1 ]; then exit 0; fi
    case "$*" in *source*) exit 0 ;; esac
    echo "can't find session: recovery" >&2; exit 1 ;;
  list-windows) echo 0 ;;
  list-panes)
    printf '%%91_NTM_SEP_0_NTM_SEP_recovery__cc_1_NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n' ;;
  display-message) echo claude ;;
  capture-pane) printf '\342\235\257 \n' ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	old := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = old })
	return log
}

func targetCheckpoint(t *testing.T) *Checkpoint {
	t.Helper()
	return &Checkpoint{
		Version: 1, ID: "cp-target", Name: "target-test", SessionName: "source",
		WorkingDir: t.TempDir(), CreatedAt: time.Now(), PaneCount: 1,
		Session: SessionState{Panes: []PaneState{{
			ID: "%11", Index: 0, WindowIndex: 0, Title: "source__cc_1",
			AgentType: "cc", Command: "claude",
		}}},
	}
}

func TestRestoreIntoTargetPreservesCheckpointAndSource(t *testing.T) {
	log := restoreTargetFixture(t)
	cp := targetCheckpoint(t)
	storage := &Storage{BaseDir: t.TempDir()}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	file, err := storage.SaveScrollback(cp.SessionName, cp.ID, cp.Session.Panes[0].ID, "source recovery context")
	if err != nil {
		t.Fatal(err)
	}
	cp.Session.Panes[0].ScrollbackFile = file
	before, _ := json.Marshal(cp)
	r := NewRestorerWithStorage(storage)
	out, err := r.RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{
		TargetSession: "recovery", SkipGitCheck: true, InjectContext: true,
	})
	if err != nil || out == nil || out.SessionName != "recovery" || out.SourceSession != "source" || out.Stage != "completed" {
		t.Fatalf("restore target failed: %+v, %v", out, err)
	}
	if !out.ContextInjected {
		t.Fatal("source scrollback was not restored into the destination")
	}
	after, _ := json.Marshal(cp)
	if string(before) != string(after) || r.sourceSession != "" || r.ctx != nil {
		t.Fatal("operation modified its source checkpoint or shared restorer")
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "-t =source") || strings.Contains(string(calls), "-s source") || strings.Contains(string(calls), "source__cc_1") || strings.Contains(string(calls), "kill-session") {
		t.Fatalf("side-by-side restore touched the source session: %s", calls)
	}
	if !strings.Contains(string(calls), "recovery__cc_1") || !strings.Contains(string(calls), "new-session") {
		t.Fatalf("destination identity was not installed: %s", calls)
	}
	if _, err := os.Stat(filepath.Join(storage.BaseDir, "recovery")); !os.IsNotExist(err) {
		t.Fatalf("restore created/used a destination checkpoint namespace: %v", err)
	}
}

func TestRestoreTargetExistsRequiresForceOnDestination(t *testing.T) {
	log := restoreTargetFixture(t)
	t.Setenv("NTM_TARGET_TEST_EXISTS", "1")
	cp := targetCheckpoint(t)
	r := NewRestorer()
	_, err := r.RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{TargetSession: "recovery"})
	if !errors.Is(err, ErrSessionExists) {
		t.Fatalf("occupied destination must be refused: %v", err)
	}
	out, err := r.RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{TargetSession: "recovery", Force: true})
	if err != nil || out.SessionName != "recovery" {
		t.Fatalf("authorized destination replacement failed: %+v, %v", out, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "source") || strings.Count(string(calls), "kill-session") != 1 {
		t.Fatalf("force must replace only the selected destination: %s", calls)
	}
}

func TestRestoreTargetValidationBeforeIO(t *testing.T) {
	log := restoreTargetFixture(t)
	for _, target := range []string{" ", "../escape", "bad:name", "recovery.1", "bad\nname"} {
		_, err := NewRestorer().RestoreFromCheckpointContext(context.Background(), targetCheckpoint(t), RestoreOptions{TargetSession: target, Force: true})
		if err == nil {
			t.Fatalf("invalid target %q accepted", target)
		}
	}
	cp := targetCheckpoint(t)
	cp.SessionName = "../bad-source"
	if _, err := NewRestorer().RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{TargetSession: "recovery"}); err == nil {
		t.Fatal("valid target masked invalid source namespace")
	}
	calls, _ := os.ReadFile(log)
	if len(calls) != 0 {
		t.Fatalf("invalid request invoked tmux: %s", calls)
	}
}

func TestRestoreTargetDryRunPreservesCustomTitles(t *testing.T) {
	log := restoreTargetFixture(t)
	cp := targetCheckpoint(t)
	cp.Session.Panes[0].Title = "custom title referring to source"
	out, err := NewRestorer().RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{TargetSession: "recovery", DryRun: true})
	if err != nil || out == nil || out.SessionName != "recovery" || out.SourceSession != "source" {
		t.Fatalf("dry run lost source/destination: %+v, %v", out, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(strings.TrimSpace(string(calls)), "\n") != 0 || !strings.HasPrefix(string(calls), "has-session ") {
		t.Fatalf("dry run mutated session: %s", calls)
	}
	if cp.Session.Panes[0].Title != "custom title referring to source" {
		t.Fatal("custom title was rewritten")
	}
}
