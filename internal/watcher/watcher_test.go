package watcher

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	fswatch "github.com/Dicklesworthstone/ntm/internal/fs/watcher"
)

// The complete pre-split engine suite lives unchanged in internal/fs/watcher.
// Exercise this package's compatibility surface with that same concrete engine.
func TestFilesystemWatcherCompatibility(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	got := make(chan Event, 16)
	var handler fswatch.Handler = func(events []Event) {
		for _, event := range events {
			select {
			case got <- event:
			default:
			}
		}
	}
	var option fswatch.Option = WithDebounceDuration(10 * time.Millisecond)
	w, err := New(handler, option, fswatch.WithEventFilter(fswatch.Create|fswatch.Write))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	var engine *fswatch.Watcher = w
	if err := engine.Add(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("value = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-got:
			if event.Path != path || event.Type&(Create|Write) == 0 {
				continue
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if err := w.Add(path); !errors.Is(err, ErrClosed) || !errors.Is(err, fswatch.ErrClosed) {
				t.Fatalf("close sentinel identity was lost: %v", err)
			}
			return
		case <-timer.C:
			t.Fatal("compatibility API did not deliver a filesystem event")
		}
	}
}
