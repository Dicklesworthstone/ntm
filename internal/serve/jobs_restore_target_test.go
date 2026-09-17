package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestCheckpointJobRestoresIntoAnotherSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("NTM_JOB_TARGET_LOG", log)
	bin := filepath.Join(dir, "tmux")
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_JOB_TARGET_LOG"
case "$1" in
  has-session)
    case "$*" in *source*) exit 0 ;; esac
    echo "can't find session: recovery" >&2; exit 1 ;;
  list-windows) echo 0 ;;
  list-panes) printf '%%91_NTM_SEP_0_NTM_SEP__NTM_SEP_bash_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP__NTM_SEP__NTM_SEP__NTM_SEP_0\n' ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	old := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = old })
	storage := checkpoint.NewStorage()
	cp := &checkpoint.Checkpoint{
		Version: 1, ID: "cp-job-target", SessionName: "source", CreatedAt: time.Now(),
		WorkingDir: t.TempDir(), PaneCount: 1,
		Session: checkpoint.SessionState{Panes: []checkpoint.PaneState{{Index: 0, AgentType: "user"}}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	result, err := (&Server{}).jobCheckpointRestore(context.Background(), map[string]interface{}{
		"session": "source", "checkpoint_id": cp.ID, "target_session": "recovery", "skip_git_check": true,
	})
	if err != nil || result["session_name"] != "recovery" || result["source_session"] != "source" || result["stage"] != "completed" {
		t.Fatalf("job did not preserve distinct source/target identities: %#v, %v", result, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "source") || strings.Contains(string(calls), "kill-session") || !strings.Contains(string(calls), "new-session") {
		t.Fatalf("job touched original session or did not restore: %s", calls)
	}
	loaded, err := storage.Load("source", cp.ID)
	if err != nil || loaded.SessionName != "source" {
		t.Fatalf("source checkpoint changed: %+v, %v", loaded, err)
	}
}

func TestCheckpointJobRejectsInvalidTargetBeforeLoading(t *testing.T) {
	result, err := (&Server{}).jobCheckpointRestore(context.Background(), map[string]interface{}{
		"session": "source", "checkpoint_id": "does-not-exist", "target_session": "bad:name",
	})
	if result != nil || err == nil || !strings.Contains(err.Error(), "invalid restore target session") {
		t.Fatalf("target was not rejected before storage access: %#v, %v", result, err)
	}
}
