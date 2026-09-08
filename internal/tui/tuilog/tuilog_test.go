package tuilog

import (
	"bytes"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// terminal stands in for the stderr both loggers write to before Redirect.
// The tests install it as the standard log output and as the default slog
// handler's sink so any leak through either path is visible.
func installTerminal(t *testing.T) *bytes.Buffer {
	t.Helper()
	term := &bytes.Buffer{}
	prevLogger := slog.Default()
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(term)
	log.SetFlags(0)
	slog.SetDefault(slog.New(slog.NewTextHandler(term, nil)))
	t.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	return term
}

func useTempLogDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "logs")
	prev := logDir
	logDir = func() string { return dir }
	t.Cleanup(func() { logDir = prev })
	return dir
}

func TestRedirectKeepsSlogAndStdLogOffTheTerminal(t *testing.T) {
	term := installTerminal(t)
	dir := useTempLogDir(t)

	restore := Redirect("dashboard", false)
	slog.Info("refreshed PID map", "pane_count", 3)
	log.Printf("[dashboard] resize width=%d", 80)
	slog.Debug("hidden at info level")
	restore()

	if term.Len() != 0 {
		t.Fatalf("logs reached the terminal while redirected:\n%s", term.String())
	}

	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	content := string(data)
	for _, want := range []string{`msg="refreshed PID map"`, "pane_count=3", "tui=dashboard", "[dashboard] resize width=80"} {
		if !strings.Contains(content, want) {
			t.Fatalf("log file missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "hidden at info level") {
		t.Fatalf("debug record must be dropped at the default level:\n%s", content)
	}

	// After restore both loggers write to the terminal again.
	slog.Info("after restore slog")
	log.Print("after restore log")
	for _, want := range []string{"after restore slog", "after restore log"} {
		if !strings.Contains(term.String(), want) {
			t.Fatalf("terminal missing %q after restore:\n%s", want, term.String())
		}
	}
}

func TestRedirectDebugLowersFileLevel(t *testing.T) {
	installTerminal(t)
	dir := useTempLogDir(t)

	restore := Redirect("dashboard", true)
	slog.Debug("visible at debug level")
	restore()

	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	if !strings.Contains(string(data), "visible at debug level") {
		t.Fatalf("debug record missing with debug enabled:\n%s", data)
	}
}

func TestRedirectDiscardsWhenLogFileCannotBeOpened(t *testing.T) {
	term := installTerminal(t)
	// A regular file where the log directory should be makes MkdirAll fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := logDir
	logDir = func() string { return filepath.Join(blocker, "logs") }
	t.Cleanup(func() { logDir = prev })

	restore := Redirect("palette", false)
	slog.Info("nowhere to go")
	log.Print("nowhere to go either")
	restore()

	if term.Len() != 0 {
		t.Fatalf("logs must be discarded, not written to the terminal:\n%s", term.String())
	}
}

func TestRedirectTruncatesOversizedFile(t *testing.T) {
	installTerminal(t)
	dir := useTempLogDir(t)
	// Large enough for a couple of records, small enough for the seeded
	// file to exceed it.
	prevMax := maxFileSize
	maxFileSize = 512
	t.Cleanup(func() { maxFileSize = prevMax })

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, bytes.Repeat([]byte("old\n"), 200), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := Redirect("dashboard", false)
	slog.Info("fresh")
	restore()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "old") {
		t.Fatalf("oversized log file must be truncated on open:\n%s", data)
	}
	if !strings.Contains(string(data), "fresh") {
		t.Fatalf("new record missing after truncation:\n%s", data)
	}

	// Under the cap the file is appended to, not truncated.
	restore = Redirect("dashboard", false)
	slog.Info("second")
	restore()
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fresh") || !strings.Contains(string(data), "second") {
		t.Fatalf("small log file must be appended to:\n%s", data)
	}
}
