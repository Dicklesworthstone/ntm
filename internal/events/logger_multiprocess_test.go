package events

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func sharedLogForTest(t *testing.T, path string) *Logger {
	t.Helper()
	l, err := NewLogger(LoggerOptions{Path: path, Enabled: true, RetentionDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()
		l.rotationWg.Wait()
		l.mu.Lock()
		defer l.mu.Unlock()
		_ = l.file.Close()
	})
	return l
}

func appendSharedLogEvent(t *testing.T, l *Logger, id string) {
	t.Helper()
	if err := l.Log(NewEvent(EventAgentSpawn, id, nil)); err != nil {
		t.Fatal(err)
	}
}

func sharedLogSessions(t *testing.T, path string) map[string]int {
	t.Helper()
	events, err := ReadSince(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, event := range events {
		counts[event.Session]++
	}
	return counts
}

func TestLoggerSharedRotationRetainsOtherWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	rotator := sharedLogForTest(t, path)
	writer := sharedLogForTest(t, path)
	old := NewEvent(EventAgentSpawn, "expired", nil)
	old.Timestamp = time.Now().AddDate(0, 0, -60)
	if err := writer.Log(old); err != nil {
		t.Fatal(err)
	}
	appendSharedLogEvent(t, writer, "before")
	if err := rotator.rotateOldEntries(); err != nil {
		t.Fatal(err)
	}
	appendSharedLogEvent(t, writer, "after")
	got := sharedLogSessions(t, path)
	if len(got) != 2 || got["before"] != 1 || got["after"] != 1 {
		t.Fatalf("rotation lost an acknowledged append: %v", got)
	}
	visible, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := writer.file.Stat()
	if err != nil || !os.SameFile(visible, opened) {
		t.Fatalf("writer did not rebind to the published log: %v", err)
	}
}

func TestLoggerSharedRotationConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	rotator := sharedLogForTest(t, path)
	const writers, eventsPerWriter = 4, 60
	start := make(chan struct{})
	failures := make(chan error, writers+1)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		l := sharedLogForTest(t, path)
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			for n := 0; n < eventsPerWriter; n++ {
				if err := l.Log(NewEvent(EventAgentSpawn, fmt.Sprintf("%d/%d", id, n), nil)); err != nil {
					failures <- err
					return
				}
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for n := 0; n < 12; n++ {
			if err := rotator.rotateOldEntries(); err != nil {
				failures <- err
				return
			}
		}
	}()
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	got := sharedLogSessions(t, path)
	if len(got) != writers*eventsPerWriter {
		t.Fatalf("got %d unique events, want %d", len(got), writers*eventsPerWriter)
	}
	for id, count := range got {
		if count != 1 {
			t.Errorf("event %s appeared %d times", id, count)
		}
	}
}

func TestLoggerSharedRotationRejectsSupersededSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	first := sharedLogForTest(t, path)
	second := sharedLogForTest(t, path)
	appendSharedLogEvent(t, first, "before")
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	snapshot, err := src.Stat()
	if err != nil {
		t.Fatal(err)
	}
	staging, err := os.CreateTemp(filepath.Dir(path), "staging-*")
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Close()
	if _, err := io.Copy(staging, src); err != nil {
		t.Fatal(err)
	}
	if err := second.rotateOldEntries(); err != nil {
		t.Fatal(err)
	}
	appendSharedLogEvent(t, second, "after")
	if err := first.commitRotation(src, snapshot, staging); err == nil {
		t.Fatal("stale rotation replaced another process's committed generation")
	}
	appendSharedLogEvent(t, first, "retry")
	got := sharedLogSessions(t, path)
	if len(got) != 3 || got["before"] != 1 || got["after"] != 1 || got["retry"] != 1 {
		t.Fatalf("stale rotation lost or duplicated records: %v", got)
	}
}

func TestLoggerSharedPathAliases(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "events.jsonl")
	first := sharedLogForTest(t, path)
	alias := filepath.Join(root, "alias.jsonl")
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	second := sharedLogForTest(t, alias)
	if first.path != second.path {
		t.Fatalf("aliases have separate lock namespaces: %q != %q", first.path, second.path)
	}
	appendSharedLogEvent(t, second, "before")
	if err := first.rotateOldEntries(); err != nil {
		t.Fatal(err)
	}
	appendSharedLogEvent(t, second, "after")
	if got := sharedLogSessions(t, path); len(got) != 2 || got["after"] != 1 {
		t.Fatalf("alias writer lost after rotation: %v", got)
	}
	info, err := os.Lstat(alias)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("rotation replaced the alias instead of its target: %v", err)
	}
}

func TestLoggerSharedLockCancellationAndIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	lock, err := lockEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseEventLogLock(lock)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	other, err := acquireEventLogLock(ctx, path)
	if other != nil {
		releaseEventLogLock(other)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("independent descriptor bypassed the held lock: %v", err)
	}
	before, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	after, err := lock.Stat()
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("timed-out waiter replaced the lock inode: %v", err)
	}
}

func TestLoggerSharedLockRejectsUnsafePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.Mkdir(path+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	l, err := NewLogger(LoggerOptions{Path: path, Enabled: true})
	if l != nil || err == nil {
		t.Fatalf("accepted a directory as the coordination lock: logger=%v err=%v", l, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed admission created the data file: %v", err)
	}
}

// A helper subprocess gives the tests a genuinely independent open file
// description and process lifetime. It never runs during an ordinary test run.
func TestLoggerSharedProcessHelper(t *testing.T) {
	mode := os.Getenv("NTM_TEST_EVENT_LOG_PROCESS")
	if mode == "" {
		return
	}
	path := os.Getenv("NTM_TEST_EVENT_LOG_PATH")
	if mode == "lock" {
		lock, err := lockEventLog(path)
		if err != nil {
			t.Fatal(err)
		}
		defer releaseEventLogLock(lock)
		fmt.Println("READY")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return
	}
	l := sharedLogForTest(t, path)
	fmt.Println("READY")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	appendSharedLogEvent(t, l, "child-after-rotation")
}

func sharedLogChild(t *testing.T, path, mode string) (*exec.Cmd, io.WriteCloser, *bytes.Buffer) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestLoggerSharedProcessHelper$")
	cmd.Env = append(os.Environ(), "NTM_TEST_EVENT_LOG_PROCESS="+mode, "NTM_TEST_EVENT_LOG_PATH="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "READY\n" {
		t.Fatalf("child did not open its persistent writer: %q %v", line, err)
	}
	return cmd, stdin, stderr
}

func TestLoggerSharedRotationAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	parent := sharedLogForTest(t, path)
	cmd, stdin, stderr := sharedLogChild(t, path, "writer")
	appendSharedLogEvent(t, parent, "parent-before")
	if err := parent.rotateOldEntries(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stdin, "append\n"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child append failed: %v\n%s", err, stderr)
	}
	got := sharedLogSessions(t, path)
	if len(got) != 2 || got["child-after-rotation"] != 1 || got["parent-before"] != 1 {
		t.Fatalf("cross-process rotation lost acknowledged history: %v", got)
	}
}

func TestLoggerSharedLockReleasedOnProcessExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	cmd, _, _ := sharedLogChild(t, path, "lock")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	lock, err := acquireEventLogLock(ctx, path)
	if lock != nil {
		releaseEventLogLock(lock)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("other process did not fence the log: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	lock, err = lockEventLog(path)
	if err != nil {
		t.Fatalf("a crashed writer left permanent ownership: %v", err)
	}
	releaseEventLogLock(lock)
}
