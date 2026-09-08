// Package tuilog keeps process logging off the terminal while a Bubble Tea
// program owns it.
//
// ntm never installs a slog handler of its own, so slog.Default() and the
// standard log package both write to stderr. That is the right destination
// for a one-shot command and the wrong one for a full-screen TUI: every
// INFO line a background refresh emits lands in the middle of the rendered
// frame. Redirect points both loggers at a file under the ntm log directory
// for the lifetime of the program and restores them afterwards.
package tuilog

import (
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Dicklesworthstone/ntm/internal/resilience"
)

const fileName = "tui.log"

// maxFileSize is the size at which the log file is truncated on the next
// TUI start instead of appended to, so an always-open dashboard cannot grow
// it without bound.
var maxFileSize int64 = 8 << 20

// logDir resolves the directory holding the TUI log; tests point it at a
// temporary directory.
var logDir = resilience.LogDir

// Path returns the file TUI logs are written to.
func Path() string {
	return filepath.Join(logDir(), fileName)
}

// Redirect routes slog.Default and the standard log package to the TUI log
// file and returns a function that restores the previous destinations and
// closes the file. Every record is tagged with the name of the TUI that
// produced it; debug lowers the file's level from Info to Debug. When the
// file cannot be opened the records are discarded - never written to the
// terminal. Call it before tea.Program.Run and defer the returned function.
func Redirect(name string, debug bool) (restore func()) {
	prevLogger := slog.Default()
	prevWriter := log.Writer()
	prevFlags := log.Flags()

	var (
		sink io.Writer = io.Discard
		file *os.File
	)
	if f, err := openLogFile(Path()); err == nil {
		sink = f
		file = f
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(sink, &slog.HandlerOptions{Level: level})
	// slog.SetDefault also bridges the standard log package to this handler,
	// so log.Printf callers on the TUI path land in the same file with the
	// same format.
	slog.SetDefault(slog.New(handler).With("tui", name))

	return func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
		if file != nil {
			_ = file.Close()
		}
	}
}

// openLogFile opens path for appending, creating its directory as needed,
// and truncates it first when it has outgrown maxFileSize.
func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(path); err == nil && info.Size() > maxFileSize {
		flags |= os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0o644)
}
