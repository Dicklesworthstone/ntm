package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// setupMetricsStateDB points the state store default path into a temp dir
// (via NTM_CONFIG) and pre-creates the session row that metric_snapshots
// rows reference (FK). Returns the temp dir for reference.
func setupMetricsStateDB(t *testing.T, sessionID string) string {
	t.Helper()
	tmp := t.TempDir()
	// state.DefaultPath() resolves NTM_CONFIG first, so getMetricsCollector's
	// state.Open("") lands on <tmp>/state.db — a REAL temp SQLite state DB.
	t.Setenv("NTM_CONFIG", filepath.Join(tmp, "config.toml"))
	t.Setenv("XDG_CONFIG_HOME", tmp)

	store, err := state.Open(filepath.Join(tmp, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	if err := store.CreateSession(&state.Session{
		ID:          sessionID,
		Name:        sessionID,
		ProjectPath: tmp,
		CreatedAt:   time.Now(),
		Status:      state.SessionActive,
	}); err != nil {
		t.Fatalf("CreateSession(%q): %v", sessionID, err)
	}
	return tmp
}

func withJSONOutput(t *testing.T) {
	t.Helper()
	prev := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prev })
}

// snapshotListEnvelope mirrors the --json envelope of `ntm metrics snapshot list`.
type snapshotListEnvelope struct {
	Session   string `json:"session"`
	Count     int    `json:"count"`
	Snapshots []struct {
		ID        int64     `json:"id"`
		SessionID string    `json:"session_id"`
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"snapshots"`
}

// TestMetricsSnapshotList_SaveTwoThenListJSON proves the A3 fix: two real
// saves against a real temp SQLite state DB are returned by list, ordered
// oldest-first, with a non-null snapshots array in the --json envelope.
func TestMetricsSnapshotList_SaveTwoThenListJSON(t *testing.T) {
	const session = "metrics-list-e2e"
	setupMetricsStateDB(t, session)
	withJSONOutput(t)

	if _, err := captureStdout(t, func() error {
		return runMetricsSnapshotSave(session, "before-refactor")
	}); err != nil {
		t.Fatalf("save 'before-refactor': %v", err)
	}
	if _, err := captureStdout(t, func() error {
		return runMetricsSnapshotSave(session, "after-refactor")
	}); err != nil {
		t.Fatalf("save 'after-refactor': %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runMetricsSnapshotList(session)
	})
	if err != nil {
		t.Fatalf("runMetricsSnapshotList: %v", err)
	}

	// Envelope must never encode snapshots as null (arrays-never-null contract).
	if strings.Contains(out, `"snapshots":null`) || strings.Contains(out, `"snapshots": null`) {
		t.Fatalf("snapshots array encoded as null: %s", out)
	}

	var env snapshotListEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\noutput: %s", err, out)
	}
	if env.Session != session {
		t.Errorf("session = %q, want %q", env.Session, session)
	}
	if env.Count != 2 || len(env.Snapshots) != 2 {
		t.Fatalf("count = %d, len(snapshots) = %d, want 2 and 2\noutput: %s",
			env.Count, len(env.Snapshots), out)
	}
	// Ordered oldest-first: insertion order (created_at ASC, id ASC).
	if env.Snapshots[0].Name != "before-refactor" || env.Snapshots[1].Name != "after-refactor" {
		t.Errorf("order = [%q, %q], want [before-refactor, after-refactor]",
			env.Snapshots[0].Name, env.Snapshots[1].Name)
	}
	for i, s := range env.Snapshots {
		if s.SessionID != session {
			t.Errorf("snapshots[%d].session_id = %q, want %q", i, s.SessionID, session)
		}
		if s.ID == 0 {
			t.Errorf("snapshots[%d].id = 0, want non-zero row id", i)
		}
		if s.CreatedAt.IsZero() {
			t.Errorf("snapshots[%d].created_at is zero", i)
		}
	}
	if env.Snapshots[0].ID >= env.Snapshots[1].ID {
		t.Errorf("ids not ascending: %d then %d", env.Snapshots[0].ID, env.Snapshots[1].ID)
	}
}

// TestMetricsSnapshotList_EmptyDBJSON proves the empty case: a real state DB
// with zero snapshots yields an empty (NOT null) array and a nil error (exit 0).
func TestMetricsSnapshotList_EmptyDBJSON(t *testing.T) {
	const session = "metrics-list-empty"
	setupMetricsStateDB(t, session)
	withJSONOutput(t)

	out, err := captureStdout(t, func() error {
		return runMetricsSnapshotList(session)
	})
	if err != nil {
		t.Fatalf("runMetricsSnapshotList on empty DB: %v (want nil, exit 0)", err)
	}

	if strings.Contains(out, `"snapshots":null`) || strings.Contains(out, `"snapshots": null`) {
		t.Fatalf("empty snapshots encoded as null: %s", out)
	}

	var env snapshotListEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\noutput: %s", err, out)
	}
	if env.Count != 0 || len(env.Snapshots) != 0 {
		t.Errorf("count = %d, len(snapshots) = %d, want 0 and 0", env.Count, len(env.Snapshots))
	}

	// Belt and braces: the raw envelope must contain an actual [] for snapshots.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("unmarshal raw envelope: %v", err)
	}
	if string(raw["snapshots"]) == "null" {
		t.Fatalf("raw snapshots value is null, want []")
	}
}

// TestMetricsSnapshotList_TextOutput checks the human-readable listing shows
// saved snapshot names instead of the old placebo message.
func TestMetricsSnapshotList_TextOutput(t *testing.T) {
	const session = "metrics-list-text"
	setupMetricsStateDB(t, session)

	prev := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = prev })

	if _, err := captureStdout(t, func() error {
		return runMetricsSnapshotSave(session, "week-1")
	}); err != nil {
		t.Fatalf("save 'week-1': %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runMetricsSnapshotList(session)
	})
	if err != nil {
		t.Fatalf("runMetricsSnapshotList: %v", err)
	}
	if !strings.Contains(out, "week-1") {
		t.Errorf("text output missing snapshot name 'week-1':\n%s", out)
	}
	if strings.Contains(out, "requires active session with state store") {
		t.Errorf("text output still shows the old placebo message:\n%s", out)
	}
}
