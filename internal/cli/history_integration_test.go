package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/history"
)

// Integration-lite test: exercising history list JSON output after a recorded send.
// This avoids tmux by appending directly to history storage and invoking the list code path.
func TestHistoryJSONIncludesDurationAndTargets(t *testing.T) {
	// Isolate history storage to a temp dir (t.Setenv restores TestMain's XDG_DATA_HOME).
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	// Append a synthetic entry
	entry := history.NewEntry("sess", []string{"1", "2"}, "hello world", history.SourceCLI)
	entry.DurationMs = 123
	entry.SetSuccess()
	if err := history.Append(entry); err != nil {
		t.Fatalf("append history: %v", err)
	}

	// Build result
	entries, err := history.ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	res := &HistoryListResult{Entries: entries, TotalCount: len(entries), Showing: len(entries)}

	// Marshal JSON
	data, err := json.Marshal(res.JSON())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Validate presence of duration_ms and targets
	if !bytes.Contains(data, []byte("duration_ms")) {
		t.Fatalf("duration_ms missing in JSON: %s", data)
	}
	if !bytes.Contains(data, []byte("targets")) {
		t.Fatalf("targets missing in JSON: %s", data)
	}
}
