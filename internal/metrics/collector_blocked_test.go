package metrics

import (
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func openMetricsStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// `ntm metrics` builds a Collector and reports immediately, so a count that
// lives only in this process's memory is always zero — which made the Tier-0
// destructive_cmd_incidents target report a green "met" for every session while
// the blocked_commands rows sat in the database unread.
func TestReportCountsBlockedCommandsFromStore(t *testing.T) {
	store := openMetricsStore(t)

	recorder := NewCollector(store, "sess")
	recorder.RecordBlockedCommand("agent-1", "rm -rf /", "destructive")
	recorder.RecordBlockedCommand("agent-1", "git reset --hard", "safety")
	recorder.Close()

	// A fresh collector, as the CLI builds: nothing in memory.
	reporter := NewCollector(store, "sess")
	t.Cleanup(reporter.Close)

	report, err := reporter.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if report.BlockedCommands != 2 {
		t.Errorf("BlockedCommands = %d, want 2 from the persisted rows", report.BlockedCommands)
	}
}

// The Tier-0 target must be able to fail.
func TestDestructiveTargetCanFail(t *testing.T) {
	store := openMetricsStore(t)

	recorder := NewCollector(store, "sess")
	recorder.RecordBlockedCommand("agent-1", "rm -rf /", "destructive")
	recorder.Close()

	reporter := NewCollector(store, "sess")
	t.Cleanup(reporter.Close)
	report, err := reporter.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	var found bool
	for _, tc := range report.TargetComparison {
		if tc.Metric != "destructive_cmd_incidents" {
			continue
		}
		found = true
		if tc.Current != 1 {
			t.Errorf("current = %.1f, want 1", tc.Current)
		}
		if tc.Status == "met" {
			t.Errorf("1 blocked command against a target of %.1f reported status %q", tc.Target, tc.Status)
		}
	}
	if !found {
		t.Error("no destructive_cmd_incidents comparison emitted")
	}
}

// Counts are per session, so one session's incidents never land in another's
// report.
func TestBlockedCommandsAreSessionScoped(t *testing.T) {
	store := openMetricsStore(t)

	other := NewCollector(store, "other-session")
	other.RecordBlockedCommand("agent-9", "rm -rf /", "destructive")
	other.Close()

	reporter := NewCollector(store, "sess")
	t.Cleanup(reporter.Close)
	report, err := reporter.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if report.BlockedCommands != 0 {
		t.Errorf("BlockedCommands = %d, want 0; another session's incidents leaked in", report.BlockedCommands)
	}
}

// With no store the in-memory counter is all there is, and it must still be
// reported rather than overwritten by an unmeasured zero.
func TestReportFallsBackToInMemoryCountWithoutStore(t *testing.T) {
	c := NewCollector(nil, "sess")
	t.Cleanup(c.Close)
	c.RecordBlockedCommand("agent-1", "rm -rf /", "destructive")

	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if report.BlockedCommands != 1 {
		t.Errorf("BlockedCommands = %d, want the in-memory count of 1", report.BlockedCommands)
	}
}

// A nil *state.Store is not a nil interface. getDB used to assert it to an
// interface and call DB() on the nil receiver, so recording a blocked command
// without storage panicked instead of skipping persistence — and
// getMetricsCollector builds exactly that collector when state.db will not open.
func TestNilStoreDoesNotPanic(t *testing.T) {
	c := NewCollector(nil, "sess")
	t.Cleanup(c.Close)

	c.RecordBlockedCommand("agent-1", "rm -rf /", "destructive")
	if _, err := c.GenerateReport(); err != nil {
		t.Fatalf("GenerateReport with a nil store: %v", err)
	}
}
