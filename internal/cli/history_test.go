package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
