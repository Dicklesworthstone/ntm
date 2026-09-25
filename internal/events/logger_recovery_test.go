package events

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoggerRecoverySeparatesInterruptedTail(t *testing.T) {
	complete, err := json.Marshal(NewEvent(EventError, "complete-no-newline", nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		tail     []byte
		complete bool
	}{
		{"partial-json", []byte(`{"timestamp":"2026-09-24T00:00:00Z","session":"unfinished`), false},
		{"partial-envelope", []byte(`{"encrypted":true,"ciphertext":"AA`), false},
		{"partial-utf8", []byte{'{', '"', 0xf0, 0x9f}, false},
		{"complete-json", complete, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(path, tc.tail, 0600); err != nil {
				t.Fatal(err)
			}
			l := sharedLogForTest(t, path)
			appendSharedLogEvent(t, l, "accepted-after-crash")
			got := sharedLogSessions(t, path)
			if got["accepted-after-crash"] != 1 {
				t.Fatalf("newly acknowledged record swallowed by incomplete tail: %v", got)
			}
			if tc.complete && got["complete-no-newline"] != 1 {
				t.Fatalf("recovery discarded a complete undelimited record: %v", got)
			}
			raw, err := os.ReadFile(path)
			prefix := append(append([]byte(nil), tc.tail...), '\n')
			if err != nil || !bytes.HasPrefix(raw, prefix) {
				t.Fatalf("recovery truncated or changed crash evidence: %q err=%v", raw, err)
			}
			if err := l.rotateOldEntries(); err != nil {
				t.Fatal(err)
			}
			if sharedLogSessions(t, path)["accepted-after-crash"] != 1 {
				t.Fatal("rotation discarded recovered append")
			}
		})
	}
}

func TestLoggerRecoveryAfterReplacementAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	old := sharedLogForTest(t, path)
	appendSharedLogEvent(t, old, "old-generation")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	partial := []byte(`{"new-generation":"interrupted`)
	if err := os.WriteFile(path, partial, 0600); err != nil {
		t.Fatal(err)
	}
	appendSharedLogEvent(t, old, "after-rebind")
	fresh := sharedLogForTest(t, path)
	appendSharedLogEvent(t, fresh, "after-reopen")
	got := sharedLogSessions(t, path)
	if len(got) != 2 || got["after-rebind"] != 1 || got["after-reopen"] != 1 {
		t.Fatalf("writer-generation recovery lost new history: %v", got)
	}
	if saved := sharedLogSessions(t, path+".saved"); len(saved) != 1 || saved["old-generation"] != 1 {
		t.Fatalf("recovery changed archived generation: %v", saved)
	}
}

func TestLoggerRecoveryDoesNotChangeTailForRejectedEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	partial := []byte(`{"session":"incomplete`)
	if err := os.WriteFile(path, partial, 0600); err != nil {
		t.Fatal(err)
	}
	l := sharedLogForTest(t, path)
	invalid := NewEvent(EventError, "not-accepted", map[string]interface{}{"invalid": make(chan int)})
	if err := l.Log(invalid); err == nil {
		t.Fatal("accepted unencodable event")
	}
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, partial) {
		t.Fatalf("rejected event modified existing evidence: %q err=%v", raw, err)
	}
}

func TestReadEventSnapshotExcludesLaterAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l := sharedLogForTest(t, path)
	appendSharedLogEvent(t, l, "at-snapshot")
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		t.Fatal(err)
	}
	appendSharedLogEvent(t, l, "after-snapshot")
	got, err := readEventSnapshot(src, info.Size(), path, time.Time{})
	if err != nil || len(got) != 1 || got[0].Session != "at-snapshot" {
		t.Fatalf("snapshot chased a moving EOF: %v err=%v", got, err)
	}
	if err := l.rotateOldEntries(); err != nil {
		t.Fatal(err)
	}
	got, err = readEventSnapshot(src, info.Size(), path, time.Time{})
	if err != nil || len(got) != 1 || got[0].Session != "at-snapshot" {
		t.Fatalf("rotation invalidated an open read snapshot: %v err=%v", got, err)
	}
	if current := sharedLogSessions(t, path); len(current) != 2 || current["after-snapshot"] != 1 {
		t.Fatalf("next read missed the later append: %v", current)
	}
}

func TestReadEventSnapshotLeavesPartialRecordForNextRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l := sharedLogForTest(t, path)
	appendSharedLogEvent(t, l, "before")
	info, err := l.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	appendSharedLogEvent(t, l, "completed-after-snapshot")
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	// A reader can observe the size in the middle of another process's write.
	// Do not read beyond its boundary to guess at the rest of the record.
	got, err := readEventSnapshot(src, info.Size()+12, path, time.Time{})
	if err != nil || len(got) != 1 || got[0].Session != "before" {
		t.Fatalf("partial snapshot fabricated a completed record: %v err=%v", got, err)
	}
	if current := sharedLogSessions(t, path); len(current) != 2 || current["completed-after-snapshot"] != 1 {
		t.Fatalf("read modified or lost the later completed record: %v", current)
	}
}

func TestReadSinceDoesNotCreateCoordinationState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if _, err := ReadSince(path, time.Time{}); !os.IsNotExist(err) {
		t.Fatalf("missing log read: %v", err)
	}
	line, err := json.Marshal(NewEvent(EventError, "read-only", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0400); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSince(path, time.Time{})
	if err != nil || len(got) != 1 || got[0].Session != "read-only" {
		t.Fatalf("read-only history unavailable: %v err=%v", got, err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("history read created coordination state: %v", err)
	}
}
