package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/history"
)

func TestRunHistoryListRejectsNonPositiveLimit(t *testing.T) {
	err := runHistoryList(t.Context(), 0, 0, "", "", "", "", "", false)
	if err == nil {
		t.Fatalf("expected error for limit <= 0")
	}
}

func TestResolveHistorySessionFilterNormalizesProjectScopedPrefix(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "myproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	oldCfg := cfg
	oldJSON := jsonOutput
	cfg = &config.Config{ProjectsBase: projectsBase}
	jsonOutput = false
	t.Cleanup(func() {
		cfg = oldCfg
		jsonOutput = oldJSON
	})

	got, err := resolveHistorySessionFilter(t.Context(), "mypro")
	if err != nil {
		t.Fatalf("resolveHistorySessionFilter() error = %v", err)
	}
	if got != "myproject" {
		t.Fatalf("resolveHistorySessionFilter() = %q, want %q", got, "myproject")
	}
}

func TestResolveHistorySessionFilterRejectsAmbiguousProjectScopedPrefix(t *testing.T) {
	projectsBase := t.TempDir()
	for _, name := range []string{"myproject", "myproto"} {
		if err := os.MkdirAll(filepath.Join(projectsBase, name), 0o755); err != nil {
			t.Fatalf("mkdir project dir %q: %v", name, err)
		}
	}

	oldCfg := cfg
	oldJSON := jsonOutput
	cfg = &config.Config{ProjectsBase: projectsBase}
	jsonOutput = false
	t.Cleanup(func() {
		cfg = oldCfg
		jsonOutput = oldJSON
	})

	_, err := resolveHistorySessionFilter(t.Context(), "myp")
	if err == nil {
		t.Fatal("expected ambiguous prefix error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous prefix error, got %v", err)
	}
}

func TestRunHistoryShowRejectsAmbiguousIDPrefix(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)

	if err := history.Clear(); err != nil {
		t.Fatalf("clear history: %v", err)
	}

	entryA := history.NewEntry("session-a", []string{"1"}, "prompt a", history.SourceCLI)
	entryA.ID = "abc-111"
	if err := history.Append(entryA); err != nil {
		t.Fatalf("append entryA: %v", err)
	}

	entryB := history.NewEntry("session-b", []string{"2"}, "prompt b", history.SourceCLI)
	entryB.ID = "abc-222"
	if err := history.Append(entryB); err != nil {
		t.Fatalf("append entryB: %v", err)
	}

	err := runHistoryShow("abc")
	if err == nil {
		t.Fatal("expected ambiguous prefix error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous prefix error, got %v", err)
	}
}

func TestRunHistoryShowFallsBackToNumericIDPrefixWhenIndexIsOutOfRange(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)

	if err := history.Clear(); err != nil {
		t.Fatalf("clear history: %v", err)
	}

	entry := history.NewEntry("session-a", []string{"1"}, "prompt a", history.SourceCLI)
	entry.ID = "1234567890123-abcd"
	if err := history.Append(entry); err != nil {
		t.Fatalf("append entry: %v", err)
	}

	oldStdout := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = writePipe
	t.Cleanup(func() {
		os.Stdout = oldStdout
	})

	showErr := runHistoryShow("1234567890123")

	if err := writePipe.Close(); err != nil {
		t.Fatalf("close write pipe: %v", err)
	}
	if _, err := io.Copy(io.Discard, readPipe); err != nil {
		t.Fatalf("drain read pipe: %v", err)
	}
	if err := readPipe.Close(); err != nil {
		t.Fatalf("close read pipe: %v", err)
	}

	if showErr != nil {
		t.Fatalf("runHistoryShow() error = %v", showErr)
	}
}

func TestHistoryListCombinesSessionAndSearchBeforePagination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux fixture requires a Unix shell")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	// Return a valid, empty live-session list; history remains queryable for
	// offline sessions without relying on the operator's running tmux server.
	tmuxPath := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\nif [ \"$1\" = -V ]; then echo 'tmux 3.4'; fi\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", tmuxPath)
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	for i, entry := range []history.HistoryEntry{
		{ID: "a-old", Session: "session-a", Prompt: "Fix AUTH flow", Source: history.SourceCLI},
		{ID: "a-other", Session: "session-a", Prompt: "Run tests", Source: history.SourceCLI},
		{ID: "b-match", Session: "session-b", Prompt: "auth other", Source: history.SourceCLI},
		{ID: "a-new", Session: "session-a", Prompt: "Fix auth regression", Source: history.SourceCLI},
		{ID: "a-palette", Session: "session-a", Prompt: "auth palette", Source: history.SourcePalette},
	} {
		entry.Timestamp = time.Date(2026, time.January, 1, i, 0, 0, 0, time.UTC)
		if err := history.Append(&entry); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name, query, session, since, wantID string
		regex                               bool
		offset, total                       int
		more, wantErr                       bool
	}{
		{name: "literal newest", query: "AUTH", session: "session-a", wantID: "a-new", total: 2, more: true},
		{name: "literal older", query: "auth", session: "session-a", wantID: "a-old", offset: 1, total: 2},
		{name: "regex", query: "(?i)^fix auth", session: "session-a", regex: true, wantID: "a-new", total: 2, more: true},
		{name: "invalid regex", query: "(", session: "session-a", regex: true, wantErr: true},
		{name: "no match", query: "absent", session: "session-a"},
		{name: "session only", session: "session-a", wantID: "a-new", total: 3, more: true},
		{name: "search only", query: "auth", wantID: "a-new", total: 3, more: true},
		{name: "time intersection", query: "auth", session: "session-a", since: "2026-01-01T01:00:00Z", wantID: "a-new", total: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := captureHistoryReviewCommand(t, func() error {
				return runHistoryList(context.Background(), 1, tc.offset, tc.session, tc.since, "", tc.query, "cli", tc.regex)
			})
			if tc.wantErr {
				if err == nil {
					t.Fatal("invalid search was silently ignored")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var result HistoryListJSON
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatalf("decode history output: %v: %s", err, data)
			}
			if result.TotalCount != tc.total || result.HasMore != tc.more || result.Showing != len(result.Entries) {
				t.Fatalf("incorrect filtered pagination: %+v", result)
			}
			if tc.wantID == "" {
				if result.Entries == nil || len(result.Entries) != 0 {
					t.Fatalf("empty search returned entries: %+v", result.Entries)
				}
			} else if len(result.Entries) != 1 || result.Entries[0].ID != tc.wantID {
				t.Fatalf("entries = %+v, want %s", result.Entries, tc.wantID)
			}
		})
	}
}

func TestHistoryShowFullIDAndPrefixReturnSelectedEntry(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	entry := history.NewEntry("session-a", []string{"1"}, "selected history prompt", history.SourceCLI)
	entry.ID = "1760000000000-abcd"
	if err := history.Append(entry); err != nil {
		t.Fatal(err)
	}
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, asJSON := range []bool{true, false} {
		mode := "text"
		if asJSON {
			mode = "json"
		}
		for _, selector := range []string{entry.ID, "1760000000000-ab", "1760000000000", "1"} {
			t.Run(mode+"/"+selector, func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("show panicked after successful lookup: %v", r)
					}
				}()
				jsonOutput = asJSON
				data, err := captureHistoryReviewCommand(t, func() error { return runHistoryShow(selector) })
				if err != nil {
					t.Fatal(err)
				}
				if asJSON {
					var got *history.HistoryEntry
					if err := json.Unmarshal(data, &got); err != nil || got == nil || got.ID != entry.ID {
						t.Fatalf("selected entry lost in JSON output: %s (%v)", data, err)
					}
				} else if !strings.Contains(string(data), entry.ID) || !strings.Contains(string(data), entry.Prompt) {
					t.Fatalf("selected entry lost in text output: %s", data)
				}
			})
		}
	}
}

func captureHistoryReviewCommand(t *testing.T, run func() error) ([]byte, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "history-output-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	oldStdout := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = oldStdout }()
	runErr := run()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return data, runErr
}
