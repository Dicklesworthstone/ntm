package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestMatchesNTMPattern(t *testing.T) {

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Lifecycle directories
		{name: "lifecycle pattern", input: "ntm-lifecycle-abc123", expected: "ntm-lifecycle-*"},
		{name: "lifecycle with uuid", input: "ntm-lifecycle-550e8400-e29b-41d4-a716-446655440000", expected: "ntm-lifecycle-*"},

		// Description files
		{name: "desc md file", input: "ntm-myproject-desc.md", expected: "ntm-*-desc.md"},
		{name: "desc md with numbers", input: "ntm-123-desc.md", expected: "ntm-*-desc.md"},

		// Graph files
		{name: "graph html exact", input: "ntm-graph.html", expected: "ntm-graph.html"},

		// Test directories
		{name: "test-ntm dir", input: "test-ntm-spawn", expected: "test-ntm-*"},
		{name: "test-ntm with timestamp", input: "test-ntm-20260205", expected: "test-ntm-*"},

		// Atomic files
		{name: "atomic file", input: "ntm-atomic-config.json", expected: "ntm-atomic-*"},

		// Prompt files
		{name: "prompt md file", input: "ntm-prompt-abc.md", expected: "ntm-prompt-*.md"},

		// Mail files
		{name: "mail md file", input: "ntm-mail-xyz.md", expected: "ntm-mail-*.md"},

		// Handoff files
		{name: "handoff tmp", input: ".handoff-myproject.tmp", expected: ".handoff-*.tmp"},

		// Events rotate
		{name: "events rotate", input: "events-rotate-12345.jsonl", expected: "events-rotate-*.jsonl"},

		// Rotation tmp
		{name: "rotation tmp", input: "rotation-abc.tmp", expected: "rotation-*.tmp"},

		// Pending tmp
		{name: "pending tmp", input: "pending-123.tmp", expected: "pending-*.tmp"},

		// Non-matching
		{name: "unrelated file", input: "some-other-file.txt", expected: ""},
		{name: "partial match no ext", input: "ntm-prompt-abc", expected: ""},
		{name: "wrong prefix", input: "foo-lifecycle-abc", expected: ""},
		{name: "empty string", input: "", expected: ""},
		{name: "just ntm", input: "ntm", expected: ""},
		{name: "graph wrong ext", input: "ntm-graph.json", expected: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := matchesNTMPattern(tc.input)
			if result != tc.expected {
				t.Errorf("matchesNTMPattern(%q) = %q; want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestFormatCleanupDuration(t *testing.T) {

	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		// Less than 1 minute (rounds to nearest minute)
		{name: "0 seconds", duration: 0, expected: "0m"},
		{name: "30 seconds", duration: 30 * time.Second, expected: "0m"}, // 0.5m rounds to 0 (banker's rounding)
		{name: "59 seconds", duration: 59 * time.Second, expected: "1m"}, // 0.98m rounds to 1

		// Less than 1 hour
		{name: "1 minute", duration: 1 * time.Minute, expected: "1m"},
		{name: "30 minutes", duration: 30 * time.Minute, expected: "30m"},
		{name: "59 minutes", duration: 59 * time.Minute, expected: "59m"},

		// Less than 24 hours
		{name: "1 hour", duration: 1 * time.Hour, expected: "1.0h"},
		{name: "1.5 hours", duration: 90 * time.Minute, expected: "1.5h"},
		{name: "12 hours", duration: 12 * time.Hour, expected: "12.0h"},
		{name: "23.9 hours", duration: 23*time.Hour + 54*time.Minute, expected: "23.9h"},

		// 24+ hours
		{name: "24 hours", duration: 24 * time.Hour, expected: "1.0d"},
		{name: "36 hours", duration: 36 * time.Hour, expected: "1.5d"},
		{name: "48 hours", duration: 48 * time.Hour, expected: "2.0d"},
		{name: "72 hours", duration: 72 * time.Hour, expected: "3.0d"},
		{name: "1 week", duration: 7 * 24 * time.Hour, expected: "7.0d"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := formatCleanupDuration(tc.duration)
			if result != tc.expected {
				t.Errorf("formatCleanupDuration(%v) = %q; want %q", tc.duration, result, tc.expected)
			}
		})
	}
}

func TestGetFileType(t *testing.T) {

	tests := []struct {
		name     string
		isDir    bool
		expected string
	}{
		{name: "directory", isDir: true, expected: "directory"},
		{name: "file", isDir: false, expected: "file"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := getFileType(tc.isDir)
			if result != tc.expected {
				t.Errorf("getFileType(%v) = %q; want %q", tc.isDir, result, tc.expected)
			}
		})
	}
}

func TestCleanupCmd_ProjectionsFlagClearsStaleRuntimeProjections(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	dbPath := filepath.Join(t.TempDir(), "state.db")
	seedStore, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := seedStore.Migrate(); err != nil {
		_ = seedStore.Close()
		t.Fatalf("seedStore.Migrate: %v", err)
	}

	now := time.Now().UTC()
	if err := seedStore.UpsertRuntimeSession(&state.RuntimeSession{
		Name:         "stale-proj-sess",
		HealthStatus: state.HealthStatusHealthy,
		CollectedAt:  now.Add(-6 * time.Hour),
		StaleAfter:   now.Add(-5 * time.Hour),
	}); err != nil {
		_ = seedStore.Close()
		t.Fatalf("UpsertRuntimeSession(stale): %v", err)
	}
	if err := seedStore.UpsertRuntimeSession(&state.RuntimeSession{
		Name:         "fresh-proj-sess",
		HealthStatus: state.HealthStatusHealthy,
		CollectedAt:  now,
		StaleAfter:   now.Add(1 * time.Hour),
	}); err != nil {
		_ = seedStore.Close()
		t.Fatalf("UpsertRuntimeSession(fresh): %v", err)
	}
	_ = seedStore.Close()

	origOpen := openCleanupStateStoreFunc
	openCleanupStateStoreFunc = func() (*state.Store, error) {
		st, openErr := state.Open(dbPath)
		if openErr != nil {
			return nil, openErr
		}
		if migErr := st.Migrate(); migErr != nil {
			_ = st.Close()
			return nil, migErr
		}
		return st, nil
	}
	t.Cleanup(func() { openCleanupStateStoreFunc = origOpen })

	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	// Dry-run must NOT delete stale projections.
	dryOut, err := captureStdout(t, func() error {
		cmd := newCleanupCmd()
		cmd.SetArgs([]string{"--projections", "--max-age=1", "--dry-run"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("cleanup --projections --dry-run failed: %v", err)
	}
	var dryResp cleanupResponse
	if err := json.Unmarshal([]byte(dryOut), &dryResp); err != nil {
		t.Fatalf("unmarshal dry-run cleanup response: %v (raw=%q)", err, dryOut)
	}
	if dryResp.ProjectionsCleared {
		t.Fatalf("expected ProjectionsCleared=false during dry-run, got %+v", dryResp)
	}

	checkStore, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen state.db after dry-run: %v", err)
	}
	stillThere, err := checkStore.GetRuntimeSession("stale-proj-sess")
	_ = checkStore.Close()
	if err != nil {
		t.Fatalf("GetRuntimeSession after dry-run: %v", err)
	}
	if stillThere == nil {
		t.Fatal("dry-run unexpectedly deleted stale runtime session")
	}

	// Live cleanup with --projections --max-age=1 must prune the 5h-stale row and keep the fresh row.
	liveOut, err := captureStdout(t, func() error {
		cmd := newCleanupCmd()
		cmd.SetArgs([]string{"--projections", "--max-age=1"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("cleanup --projections --max-age=1 failed: %v", err)
	}
	var liveResp cleanupResponse
	if err := json.Unmarshal([]byte(liveOut), &liveResp); err != nil {
		t.Fatalf("unmarshal live cleanup response: %v (raw=%q)", err, liveOut)
	}
	if !liveResp.ProjectionsCleared || liveResp.ProjectionError != "" {
		t.Fatalf("expected ProjectionsCleared=true without error, got %+v", liveResp)
	}

	verifyStore, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen state.db after live cleanup: %v", err)
	}
	defer verifyStore.Close()

	pruned, err := verifyStore.GetRuntimeSession("stale-proj-sess")
	if err != nil {
		t.Fatalf("GetRuntimeSession(stale): %v", err)
	}
	if pruned != nil {
		t.Fatalf("expected stale-proj-sess to be pruned, still got %+v", pruned)
	}

	kept, err := verifyStore.GetRuntimeSession("fresh-proj-sess")
	if err != nil {
		t.Fatalf("GetRuntimeSession(fresh): %v", err)
	}
	if kept == nil {
		t.Fatal("expected fresh-proj-sess to remain after ClearStaleProjections")
	}
}
