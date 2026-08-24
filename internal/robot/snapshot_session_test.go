package robot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// useSessionListTmuxBinary installs a fake tmux binary that reports the given
// session names to list-sessions and answers every other command with empty
// success, so GetSnapshotWithOptions can exercise its session-scoping path
// without a live tmux server.
func useSessionListTmuxBinary(t *testing.T, sessions ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("#!/bin/sh\ncase \"${1:-}\" in\n  list-sessions)\n    printf '")
	for _, s := range sessions {
		fmt.Fprintf(&b, "%s_NTM_SEP_1_NTM_SEP_0_NTM_SEP_created\\n", s)
	}
	b.WriteString("'\n    ;;\n  *)\n    exit 0\n    ;;\nesac\n")
	path := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(path, []byte(b.String()), 0o700); err != nil {
		t.Fatalf("write fake tmux binary: %v", err)
	}
	t.Setenv("NTM_TMUX_BINARY", path)
	// A refused local endpoint makes Agent Mail availability fail immediately.
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")
}

func TestFilterByName(t *testing.T) {
	sessions := []tmux.Session{{Name: "alpha"}, {Name: "beta"}}

	t.Run("empty scope returns all", func(t *testing.T) {
		got := filterByName(sessions, "", func(s tmux.Session) string { return s.Name })
		if len(got) != 2 {
			t.Fatalf("filterByName(empty) = %d sessions, want 2", len(got))
		}
	})

	t.Run("matching scope returns one", func(t *testing.T) {
		got := filterByName(sessions, "alpha", func(s tmux.Session) string { return s.Name })
		if len(got) != 1 || got[0].Name != "alpha" {
			t.Fatalf("filterByName(alpha) = %+v, want [alpha]", got)
		}
	})

	t.Run("non-matching scope returns none", func(t *testing.T) {
		got := filterByName(sessions, "gamma", func(s tmux.Session) string { return s.Name })
		if len(got) != 0 {
			t.Fatalf("filterByName(gamma) = %+v, want empty", got)
		}
	})
}

func TestGetSnapshotWithOptionsSessionScope(t *testing.T) {
	useSessionListTmuxBinary(t, "alpha", "beta")
	oldStore := currentProjectionStore()
	SetProjectionStore(nil)
	t.Cleanup(func() { SetProjectionStore(oldStore) })

	t.Run("unknown session is SESSION_NOT_FOUND", func(t *testing.T) {
		output, err := GetSnapshotWithOptions(config.Default(), PaginationOptions{Session: "gamma"})
		if err != nil {
			t.Fatalf("GetSnapshotWithOptions() error = %v", err)
		}
		if output.Success || output.ErrorCode != ErrCodeSessionNotFound {
			t.Fatalf("response = %+v, want SESSION_NOT_FOUND failure", output.RobotResponse)
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("Marshal failed: %v", err)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}
		if parsed["success"] != false {
			t.Errorf("success = %v, want false", parsed["success"])
		}
		if parsed["error_code"] != ErrCodeSessionNotFound {
			t.Errorf("error_code = %v, want %s", parsed["error_code"], ErrCodeSessionNotFound)
		}
		sessions, ok := parsed["sessions"].([]interface{})
		if !ok || sessions == nil || len(sessions) != 0 {
			t.Errorf("sessions = %#v, want empty array (no partial payload)", parsed["sessions"])
		}
	})

	t.Run("scoped snapshot returns only that session", func(t *testing.T) {
		output, err := GetSnapshotWithOptions(config.Default(), PaginationOptions{Session: "alpha"})
		if err != nil {
			t.Fatalf("GetSnapshotWithOptions() error = %v", err)
		}
		if !output.Success {
			t.Fatalf("response = %+v, want success", output.RobotResponse)
		}
		if len(output.Sessions) != 1 || output.Sessions[0].Name != "alpha" {
			t.Fatalf("Sessions = %+v, want exactly [alpha]", output.Sessions)
		}
	})
}

// TestGetSnapshotWithOptionsSessionScopeProjectionBacked guards the projection
// path: when a runtime projection store is present, sessions come from the
// store rather than tmux.ListSessions, so the scope must be applied there too.
func TestGetSnapshotWithOptionsSessionScopeProjectionBacked(t *testing.T) {
	useSessionListTmuxBinary(t, "alpha", "beta")

	tmpDir := t.TempDir()
	store, err := state.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	staleAfter := now.Add(time.Hour)
	for _, name := range []string{"alpha", "beta"} {
		if err := store.UpsertRuntimeSession(&state.RuntimeSession{
			Name:        name,
			Attached:    name == "alpha",
			CollectedAt: now,
			StaleAfter:  staleAfter,
		}); err != nil {
			t.Fatalf("UpsertRuntimeSession(%s): %v", name, err)
		}
	}

	oldStore := currentProjectionStore()
	SetProjectionStore(store)
	t.Cleanup(func() { SetProjectionStore(oldStore) })

	output, err := GetSnapshotWithOptions(config.Default(), PaginationOptions{Session: "alpha"})
	if err != nil {
		t.Fatalf("GetSnapshotWithOptions() error = %v", err)
	}
	if !output.Success {
		t.Fatalf("response = %+v, want success", output.RobotResponse)
	}
	if len(output.Sessions) != 1 || output.Sessions[0].Name != "alpha" {
		t.Fatalf("Sessions = %+v, want exactly [alpha]", output.Sessions)
	}
}
