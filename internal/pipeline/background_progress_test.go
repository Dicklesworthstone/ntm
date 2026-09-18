package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func progressTestOwner(t *testing.T, root, runID string) *lockedFile {
	t.Helper()
	prefix, err := runControlPrefix(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0700); err != nil {
		t.Fatal(err)
	}
	owner, err := openLockedFile(context.Background(), prefix+".lock")
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func progressTestDir(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(pipelineStateDir(root), "background")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func progressTestRecord(t *testing.T, event ProgressEvent) []byte {
	t.Helper()
	data, err := json.Marshal(backgroundProgressRecord{Event: &event})
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func TestBackgroundProgressDrainsAndReplaysActualEvents(t *testing.T) {
	root := t.TempDir()
	dir := progressTestDir(t, root)
	owner := progressTestOwner(t, root, "run")
	defer owner.unlockAndClose()
	progress, finish, err := openBackgroundProgress(dir, "run")
	if err != nil {
		t.Fatal(err)
	}
	want := make([]ProgressEvent, 600)
	for i := range want {
		want[i] = ProgressEvent{Type: "step_complete", StepID: fmt.Sprintf("step-%d", i), Message: "actual worker event", Timestamp: time.Unix(int64(i), 0).UTC()}
		progress <- want[i]
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := finish(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for range 2 { // Fresh observers, including after the producer has finished.
		var got []ProgressEvent
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := FollowBackgroundProgress(ctx, root, "run", func(event ProgressEvent) { got = append(got, event) })
		cancel()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("events were lost/reordered: count=%d error=%v", len(got), err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "run.events"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatal("progress containing workflow messages was not private")
	}
	if _, _, err := openBackgroundProgress(dir, "run"); err == nil {
		t.Fatal("existing progress was overwritten")
	}
}

func TestBackgroundProgressPartialRecordsKeepCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data := progressTestRecord(t, ProgressEvent{Type: "step_complete", StepID: "once"})
	if _, err := file.Write(data[:len(data)/2]); err != nil {
		t.Fatal(err)
	}
	var got []ProgressEvent
	publish := func(event ProgressEvent) { got = append(got, event) }
	next, done, err := readBackgroundProgress(context.Background(), file, 0, publish)
	if err != nil || done || next != 0 || len(got) != 0 {
		t.Fatalf("partial record was consumed: %d %v %v", next, done, err)
	}
	if _, err := file.Write(append(data[len(data)/2:], []byte("{\"done\":true}\n")...)); err != nil {
		t.Fatal(err)
	}
	next, done, err = readBackgroundProgress(context.Background(), file, next, publish)
	if err != nil || !done || next != int64(len(data)+len("{\"done\":true}\n")) || len(got) != 1 || got[0].StepID != "once" {
		t.Fatalf("resume partial record: %d %v %v %+v", next, done, err, got)
	}
}

func TestBackgroundProgressWriteFailureStillDrains(t *testing.T) {
	root := t.TempDir()
	progress, finish, err := openBackgroundProgress(progressTestDir(t, root), "run")
	if err != nil {
		t.Fatal(err)
	}
	progress <- ProgressEvent{Message: strings.Repeat("x", maxBackgroundProgressRecord)}
	done := make(chan error, 1)
	go func() {
		for range 1024 {
			progress <- ProgressEvent{Type: "step_complete"}
		}
		done <- finish()
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("oversized event was silently accepted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("failed writer stranded the executor")
	}
	if err := FollowBackgroundProgress(context.Background(), root, "run", func(ProgressEvent) { t.Error("failed stream published invented data") }); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete stream reported success: %v", err)
	}
}

func TestBackgroundProgressOwnerExitIsNotCompletion(t *testing.T) {
	root := t.TempDir()
	dir := progressTestDir(t, root)
	owner := progressTestOwner(t, root, "run")
	released := false
	defer func() {
		if !released {
			owner.unlockAndClose()
		}
	}()
	path := filepath.Join(dir, "run.events")
	if err := os.WriteFile(path, progressTestRecord(t, ProgressEvent{Type: "workflow_start"}), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	seen := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- FollowBackgroundProgress(ctx, root, "run", func(ProgressEvent) { seen <- struct{}{} }) }()
	select {
	case <-seen:
	case <-ctx.Done():
		t.Fatal("event was not relayed")
	}
	owner.unlockAndClose()
	released = true
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "owner exited") {
			t.Fatalf("lost owner mistaken for completed workflow: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("reader hung after owner exit")
	}
}

func TestBackgroundProgressCancellationOnlyStopsObserver(t *testing.T) {
	root := t.TempDir()
	dir := progressTestDir(t, root)
	owner := progressTestOwner(t, root, "run")
	defer owner.unlockAndClose()
	if err := os.WriteFile(filepath.Join(dir, "run.events"), progressTestRecord(t, ProgressEvent{Type: "workflow_start"}), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen, done := make(chan struct{}, 1), make(chan error, 1)
	go func() { done <- FollowBackgroundProgress(ctx, root, "run", func(ProgressEvent) { seen <- struct{}{} }) }()
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost observer cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer did not stop")
	}
	if alive, err := backgroundProgressOwnerAlive(context.Background(), root, "run"); err != nil || !alive {
		t.Fatalf("observer canceled the worker: %v %v", alive, err)
	}
}

func TestBackgroundProgressRejectsCorruptAndOversizedStreams(t *testing.T) {
	for name, data := range map[string]string{
		"bad-json":         "not-json\n",
		"empty-record":     "{}\n",
		"ambiguous-footer": "{\"done\":true,\"event\":{}}\n",
		"oversized-record": strings.Repeat(" ", maxBackgroundProgressRecord) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, _, err := readBackgroundProgress(context.Background(), file, 0, func(ProgressEvent) {}); err == nil {
				t.Fatal("bad stream accepted")
			}
		})
	}
	file, err := os.CreateTemp(t.TempDir(), "oversized")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(maxBackgroundProgressBytes + 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readBackgroundProgress(context.Background(), file, 0, func(ProgressEvent) {}); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestBackgroundProgressRejectsReplacementAndTruncation(t *testing.T) {
	for _, truncate := range []bool{false, true} {
		t.Run(fmt.Sprintf("truncate=%v", truncate), func(t *testing.T) {
			root := t.TempDir()
			dir := progressTestDir(t, root)
			owner := progressTestOwner(t, root, "run")
			defer owner.unlockAndClose()
			path := filepath.Join(dir, "run.events")
			if err := os.WriteFile(path, progressTestRecord(t, ProgressEvent{Type: "workflow_start"}), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			seen, done := make(chan struct{}, 1), make(chan error, 1)
			go func() { done <- FollowBackgroundProgress(ctx, root, "run", func(ProgressEvent) { seen <- struct{}{} }) }()
			select {
			case <-seen:
			case <-ctx.Done():
				t.Fatal("no event")
			}
			if truncate {
				if err := os.Truncate(path, 0); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{\"done\":true}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if err == nil || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("bad file was not rejected promptly: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("reader stalled")
			}
		})
	}
}

func TestBackgroundProgressRejectsSymlinkAndPreCanceledReads(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := FollowBackgroundProgress(ctx, root, "run", func(ProgressEvent) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancellation ignored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".ntm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pre-canceled reader created files")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary")
	}
	dir := progressTestDir(t, root)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("{\"done\":true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "run.events")); err != nil {
		t.Fatal(err)
	}
	if err := FollowBackgroundProgress(context.Background(), root, "run", func(ProgressEvent) {}); err == nil {
		t.Fatal("symlink accepted")
	}
}
