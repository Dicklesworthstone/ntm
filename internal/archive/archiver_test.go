package archive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

func TestNewArchiver(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("requires session name", func(t *testing.T) {
		_, err := NewArchiver(ArchiverOptions{
			OutputDir: tmpDir,
		})
		if err == nil {
			t.Error("expected error for empty session name")
		}
	})

	t.Run("rejects negative interval", func(t *testing.T) {
		_, err := NewArchiver(ArchiverOptions{
			SessionName: "test-session",
			OutputDir:   tmpDir,
			Interval:    -time.Second,
		})
		if err == nil || !strings.Contains(err.Error(), "interval") {
			t.Fatalf("NewArchiver() error = %v, want interval validation error", err)
		}
	})

	t.Run("rejects negative lines per capture", func(t *testing.T) {
		_, err := NewArchiver(ArchiverOptions{
			SessionName:     "test-session",
			OutputDir:       tmpDir,
			LinesPerCapture: -1,
		})
		if err == nil || !strings.Contains(err.Error(), "lines per capture") {
			t.Fatalf("NewArchiver() error = %v, want lines per capture validation error", err)
		}
	})

	t.Run("rejects negative file size limit", func(t *testing.T) {
		_, err := NewArchiver(ArchiverOptions{
			SessionName:  "test-session",
			OutputDir:    tmpDir,
			MaxFileBytes: -1,
		})
		if err == nil || !strings.Contains(err.Error(), "maximum archive file bytes") {
			t.Fatalf("NewArchiver() error = %v, want file size validation error", err)
		}
	})

	t.Run("creates archiver with defaults", func(t *testing.T) {
		a, err := NewArchiver(ArchiverOptions{
			SessionName: "test-session",
			OutputDir:   tmpDir,
		})
		if err != nil {
			t.Fatalf("NewArchiver() error: %v", err)
		}
		defer a.Close()

		if a.sessionName != "test-session" {
			t.Errorf("sessionName = %q, want %q", a.sessionName, "test-session")
		}
		if a.interval != DefaultInterval {
			t.Errorf("interval = %v, want %v", a.interval, DefaultInterval)
		}
		if a.linesPerCapture != DefaultLinesPerCapture {
			t.Errorf("linesPerCapture = %d, want %d", a.linesPerCapture, DefaultLinesPerCapture)
		}
		if a.maxFileBytes != DefaultMaxFileBytes {
			t.Errorf("maxFileBytes = %d, want %d", a.maxFileBytes, DefaultMaxFileBytes)
		}
	})

	t.Run("creates output directory", func(t *testing.T) {
		subDir := filepath.Join(tmpDir, "nested", "archive")
		a, err := NewArchiver(ArchiverOptions{
			SessionName: "test",
			OutputDir:   subDir,
		})
		if err != nil {
			t.Fatalf("NewArchiver() error: %v", err)
		}
		defer a.Close()

		if _, err := os.Stat(subDir); os.IsNotExist(err) {
			t.Error("output directory not created")
		}
	})

	t.Run("creates archive file", func(t *testing.T) {
		a, err := NewArchiver(ArchiverOptions{
			SessionName: "myproject",
			OutputDir:   tmpDir,
		})
		if err != nil {
			t.Fatalf("NewArchiver() error: %v", err)
		}
		defer a.Close()

		// Check file exists
		pattern := filepath.Join(tmpDir, "myproject_*.jsonl")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("Glob() error: %v", err)
		}
		if len(matches) == 0 {
			t.Error("archive file not created")
		}
	})
}

func TestNewArchiverSanitizesSessionNameForArchiveFilename(t *testing.T) {
	dir := t.TempDir()
	sessionName := "../escape"
	a, err := NewArchiver(ArchiverOptions{
		SessionName: sessionName,
		OutputDir:   dir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	matches, err := filepath.Glob(filepath.Join(dir, "*_*.jsonl"))
	if err != nil {
		t.Fatalf("Glob() error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("archive files in output dir = %v, want exactly one sanitized file", matches)
	}

	filename := filepath.Base(matches[0])
	if !strings.HasPrefix(filename, util.SanitizeFilename(sessionName)+"_") {
		t.Fatalf("archive filename = %q, want sanitized session prefix", filename)
	}
	if filepath.Dir(matches[0]) != dir {
		t.Fatalf("archive path escaped output dir: %s", matches[0])
	}
	if a.sessionName != sessionName {
		t.Fatalf("logical session name = %q, want original %q", a.sessionName, sessionName)
	}
}

func TestArchiver_WriteRecord(t *testing.T) {
	tmpDir := t.TempDir()

	var records []*ArchiveRecord
	a, err := NewArchiver(ArchiverOptions{
		SessionName: "test-session",
		OutputDir:   tmpDir,
		OnRecord: func(r *ArchiveRecord) {
			records = append(records, r)
		},
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Write a record
	record := &ArchiveRecord{
		Session:   "test-session",
		Pane:      "cc_2",
		PaneIndex: 2,
		Agent:     "cc",
		Model:     "opus-4.5",
		Timestamp: time.Now().UTC(),
		Content:   "Hello, world!",
		Lines:     1,
		Sequence:  1,
	}

	if err := a.writeRecord(record); err != nil {
		t.Fatalf("writeRecord() error: %v", err)
	}

	// Verify callback was called
	if len(records) != 1 {
		t.Errorf("callback received %d records, want 1", len(records))
	}

	// Verify stats
	stats := a.Stats()
	if stats.TotalRecords != 1 {
		t.Errorf("TotalRecords = %d, want 1", stats.TotalRecords)
	}

	// Flush and read back
	a.flush()

	// Find the archive file
	pattern := filepath.Join(tmpDir, "test-session_*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("archive file not found")
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("reading archive: %v", err)
	}

	var readRecord ArchiveRecord
	if err := json.Unmarshal(data, &readRecord); err != nil {
		t.Fatalf("unmarshaling record: %v", err)
	}

	if readRecord.Session != "test-session" {
		t.Errorf("Session = %q, want %q", readRecord.Session, "test-session")
	}
	if readRecord.Agent != "cc" {
		t.Errorf("Agent = %q, want %q", readRecord.Agent, "cc")
	}
	if readRecord.Content != "Hello, world!" {
		t.Errorf("Content = %q, want %q", readRecord.Content, "Hello, world!")
	}
}

func TestArchiver_RunContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "cancel-test",
		OutputDir:   tmpDir,
		Interval:    100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	started := make(chan struct{})
	var startedOnce sync.Once
	a.getPanes = func(ctx context.Context, _ string) ([]tmux.Pane, error) {
		startedOnce.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- a.Run(ctx)
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not start pane discovery")
	}
	cancel()

	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not exit after context cancellation")
	}
}

func TestArchiver_RunRejectsInvalidInterval(t *testing.T) {
	a := &Archiver{interval: -time.Second}

	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "interval") {
		t.Fatalf("Run() error = %v, want interval validation error", err)
	}
}

func TestArchiver_RotatesAtCompleteRecordBoundary(t *testing.T) {
	dir := t.TempDir()
	record := ArchiveRecord{
		Session: "rotation", Pane: "cc_2", PaneIndex: 2, Agent: "cc",
		Timestamp: time.Unix(100, 0).UTC(), Content: "quoted \"text\"\nUnicode: 世界", Lines: 2, Sequence: 1,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(2 * (len(encoded) + 1))
	callbacks := 0
	a, err := NewArchiver(ArchiverOptions{
		SessionName: "rotation", OutputDir: dir, MaxFileBytes: limit,
		OnRecord: func(*ArchiveRecord) { callbacks++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	first := a.file
	for seq := 1; seq <= 3; seq++ {
		record.Sequence = seq
		if err := a.writeRecord(&record); err != nil {
			t.Fatalf("write record %d: %v", seq, err)
		}
		if seq == 2 && a.file != first {
			t.Fatal("rotated before reaching the exact byte limit")
		}
	}
	if a.file == first {
		t.Fatal("a third record did not rotate the full chunk")
	}
	if _, err := first.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("old descriptor is still usable after rotation: %v", err)
	}
	if callbacks != 3 || a.Stats().TotalRecords != 3 {
		t.Fatalf("records = %d, callbacks = %d, want 3 each", a.Stats().TotalRecords, callbacks)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	files, records := readArchiveChunks(t, dir)
	if len(files) != 2 || len(records) != 3 {
		t.Fatalf("files = %d, records = %d, want 2 files with 3 records", len(files), len(records))
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > limit {
			t.Errorf("chunk %s has %d bytes, limit %d", path, info.Size(), limit)
		}
	}
	for i, got := range records {
		if got.Sequence != i+1 || got.Content != record.Content || got.Pane != record.Pane {
			t.Errorf("record %d changed across rotation: %+v", i, got)
		}
	}
}

func TestArchiver_RotationPreservesOversizedRecord(t *testing.T) {
	dir := t.TempDir()
	a, err := NewArchiver(ArchiverOptions{SessionName: "large", OutputDir: dir, MaxFileBytes: 512})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	record := ArchiveRecord{Session: "large", Content: strings.Repeat("界", 1024), Sequence: 1}
	if err := a.writeRecord(&record); err != nil {
		t.Fatal(err)
	}
	first := a.file
	if a.fileBytes <= a.maxFileBytes {
		t.Fatal("test record must exceed the per-chunk limit")
	}
	record.Content, record.Sequence = "next", 2
	if err := a.writeRecord(&record); err != nil {
		t.Fatal(err)
	}
	if a.file == first {
		t.Fatal("next record was appended to the oversized record's chunk")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	files, records := readArchiveChunks(t, dir)
	if len(files) != 2 || len(records) != 2 {
		t.Fatalf("files = %d, records = %d, want two intact records in two chunks", len(files), len(records))
	}
	if records[0].Content != strings.Repeat("界", 1024) || records[1].Content != "next" {
		t.Fatal("rotation truncated or duplicated an oversized record")
	}
}

func TestArchiver_RetriesPartialWriteAtConfirmedBoundary(t *testing.T) {
	dir := t.TempDir()
	callbacks := 0
	a, err := NewArchiver(ArchiverOptions{
		SessionName: "partial", OutputDir: dir,
		OnRecord: func(*ArchiveRecord) { callbacks++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	record := ArchiveRecord{Session: "partial", Content: "first complete record", Sequence: 1}
	if err := a.writeRecord(&record); err != nil {
		t.Fatal(err)
	}
	confirmedBytes := a.fileBytes
	writeErr := errors.New("injected disk-full write")
	a.writeFile = func(file *os.File, data []byte) (int, error) {
		n, err := file.Write(data[:len(data)/2])
		if err != nil {
			return n, err
		}
		return n, writeErr
	}
	record.Content, record.Sequence = "record retried after a partial append", 2
	if err := a.writeRecord(&record); !errors.Is(err, writeErr) {
		t.Fatalf("write error = %v, want injected failure", err)
	}
	info, err := a.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != confirmedBytes || a.fileBytes != confirmedBytes || a.Stats().TotalRecords != 1 || callbacks != 1 {
		t.Fatalf("partial write advanced the committed boundary: size=%d bytes=%d records=%d callbacks=%d", info.Size(), a.fileBytes, a.Stats().TotalRecords, callbacks)
	}
	a.writeFile = nil
	if err := a.writeRecord(&record); err != nil {
		t.Fatalf("retry after repaired partial write: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	files, records := readArchiveChunks(t, dir)
	if len(files) != 1 || len(records) != 2 || callbacks != 2 {
		t.Fatalf("files=%d records=%d callbacks=%d, want one chunk and exactly two successful records", len(files), len(records), callbacks)
	}
	if records[0].Content != "first complete record" || records[1].Content != record.Content || records[1].Sequence != 2 {
		t.Fatalf("retry overwrote an earlier record or left a partial line: %+v", records)
	}
}

func TestArchiver_RefusesWritesAfterFailedPartialRollback(t *testing.T) {
	dir := t.TempDir()
	a, err := NewArchiver(ArchiverOptions{SessionName: "failed-rollback", OutputDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	path := a.file.Name()
	a.writeFile = func(file *os.File, data []byte) (int, error) {
		n, err := file.Write(data[:len(data)/2])
		if err != nil {
			return n, err
		}
		return n, errors.Join(io.ErrShortWrite, file.Close())
	}
	record := ArchiveRecord{Session: "failed-rollback", Content: "incomplete record", Sequence: 1}
	err = a.writeRecord(&record)
	if err == nil || !strings.Contains(err.Error(), "restoring archive record boundary") {
		t.Fatalf("expected a failed rollback error, got %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a.writeFile = func(*os.File, []byte) (int, error) {
		t.Fatal("a later write reached the damaged chunk")
		return 0, nil
	}
	if err := a.writeRecord(&record); err == nil || !strings.Contains(err.Error(), "restoring archive record boundary") {
		t.Fatalf("damaged archive accepted another write: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) || a.Stats().TotalRecords != 0 {
		t.Fatalf("failed rollback modified the damaged chunk: records=%d error=%v", a.Stats().TotalRecords, err)
	}
}

func TestArchiver_RotatesOnDateChange(t *testing.T) {
	dir := t.TempDir()
	a, err := NewArchiver(ArchiverOptions{SessionName: "daily", OutputDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	record := ArchiveRecord{Session: "daily", Content: "before midnight", Sequence: 1}
	if err := a.writeRecord(&record); err != nil {
		t.Fatal(err)
	}
	first := a.file
	// Make the current file belong to yesterday without waiting for midnight.
	a.fileDate = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	record.Content, record.Sequence = "after midnight", 2
	if err := a.writeRecord(&record); err != nil {
		t.Fatal(err)
	}
	if a.file == first {
		t.Fatal("date change did not rotate the archive")
	}
	if _, err := first.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("previous day's descriptor is still open: %v", err)
	}
	if !strings.Contains(filepath.Base(a.file.Name()), "_"+time.Now().Format("2006-01-02")+"_") {
		t.Fatalf("new archive is not dated today: %s", a.file.Name())
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	files, records := readArchiveChunks(t, dir)
	if len(files) != 2 || len(records) != 2 || records[0].Content != "before midnight" || records[1].Content != "after midnight" {
		t.Fatalf("date rotation changed archived content: files=%v records=%+v", files, records)
	}
}

func TestArchiver_RestartNeverReopensRetiredChunks(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "restart_"+time.Now().Format("2006-01-02")+".jsonl")
	legacy := []byte("existing archive, owned by an earlier monitor\n")
	if err := os.WriteFile(legacyPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for i := 0; i < 2; i++ {
		a, err := NewArchiver(ArchiverOptions{SessionName: "restart", OutputDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = a.Close() })
		paths = append(paths, a.file.Name())
		if err := a.writeRecord(&ArchiveRecord{Session: "restart", Content: "new monitor", Sequence: i + 1}); err != nil {
			t.Fatal(err)
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if paths[0] == paths[1] || paths[0] == legacyPath || paths[1] == legacyPath {
		t.Fatalf("archive restart reused a previous pathname: %v", paths)
	}
	data, err := os.ReadFile(legacyPath)
	if err != nil || string(data) != string(legacy) {
		t.Fatalf("legacy archive changed: %q, %v", data, err)
	}
	first, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var record ArchiveRecord
	if err := json.Unmarshal(first, &record); err != nil || record.Sequence != 1 {
		t.Fatalf("retired chunk was modified by restart: %q, %v", first, err)
	}
}

// Exercise the same resident capture loop used by internal-monitor, with a
// real tmux subprocess boundary and real files. A failed handoff must neither
// lose the pending output nor advance its sequence/callback before retry.
func TestArchiver_RunRetriesFailedRotationWithoutLosingCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux capture fixture uses a POSIX shell")
	}
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "capture.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\nif [ \"$1\" != capture-pane ]; then exit 9; fi\ncat \"$NTM_ARCHIVE_TEST_CAPTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", tmuxPath)
	t.Setenv("NTM_ARCHIVE_TEST_CAPTURE", capturePath)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	callbacks := 0
	archiveDir := filepath.Join(dir, "archive")
	movedDir := filepath.Join(dir, "archive-unavailable")
	a, err := NewArchiver(ArchiverOptions{
		SessionName: "live-rotation", OutputDir: archiveDir, MaxFileBytes: 1,
		Interval: 5 * time.Millisecond,
		OnRecord: func(*ArchiveRecord) {
			callbacks++
			if callbacks == 2 {
				cancel()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	first := a.file
	observations := 0
	var preservedAfterFailure bool
	a.getPanes = func(ctx context.Context, _ string) ([]tmux.Pane, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		observations++
		content := "first line"
		if observations >= 2 {
			content += "\nsecond line"
		}
		if observations == 2 {
			if err := os.Rename(archiveDir, movedDir); err != nil {
				return nil, err
			}
		}
		if observations == 3 {
			a.mu.RLock()
			state := a.paneStates[2]
			preservedAfterFailure = state != nil && state.Sequence == 1 && state.LastContent == "first line" && a.totalRecords == 1 && a.file == first && callbacks == 1
			a.mu.RUnlock()
			if err := os.Rename(movedDir, archiveDir); err != nil {
				return nil, err
			}
		}
		if err := os.WriteFile(capturePath, []byte(content), 0600); err != nil {
			return nil, err
		}
		return []tmux.Pane{{ID: "%2", Index: 2, Type: tmux.AgentClaude}}, nil
	}
	if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v, want cancellation after two successful captures", err)
	}
	if observations != 3 || !preservedAfterFailure {
		t.Fatalf("rotation failure consumed a capture: observations=%d, state preserved=%v", observations, preservedAfterFailure)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	files, records := readArchiveChunks(t, archiveDir)
	if len(files) != 2 || len(records) != 2 || callbacks != 2 {
		t.Fatalf("files=%d records=%d callbacks=%d, want two each", len(files), len(records), callbacks)
	}
	if records[0].Content != "first line" || records[1].Content != "second line" || records[0].Sequence != 1 || records[1].Sequence != 2 {
		t.Fatalf("retried capture lost, duplicated, or changed sequence: %+v", records)
	}
	if _, err := first.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("successful retry left the old descriptor open: %v", err)
	}
}

func readArchiveChunks(t *testing.T, dir string) ([]string, []ArchiveRecord) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var records []ArchiveRecord
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			t.Fatalf("chunk %s ends inside a JSONL record", path)
		}
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		for {
			var record ArchiveRecord
			if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatalf("invalid JSONL chunk %s: %v", path, err)
			}
			records = append(records, record)
		}
	}
	return files, records
}

func TestFindNewContent(t *testing.T) {
	tests := []struct {
		name     string
		previous string
		current  string
		want     string
	}{
		{
			name:     "empty previous",
			previous: "",
			current:  "line1\nline2",
			want:     "line1\nline2",
		},
		{
			name:     "empty current",
			previous: "line1",
			current:  "",
			want:     "",
		},
		{
			name:     "no overlap",
			previous: "old1\nold2",
			current:  "new1\nnew2",
			want:     "new1\nnew2",
		},
		{
			name:     "partial overlap at end",
			previous: "line1\nline2\nline3",
			current:  "line3\nline4\nline5",
			want:     "line4\nline5",
		},
		{
			name:     "complete overlap",
			previous: "line1\nline2",
			current:  "line1\nline2",
			want:     "",
		},
		{
			name:     "scrolling buffer",
			previous: "line1\nline2\nline3\nline4\nline5",
			current:  "line4\nline5\nline6\nline7",
			want:     "line6\nline7",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findNewContent(tc.previous, tc.current)
			if got != tc.want {
				t.Errorf("findNewContent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSimpleHash(t *testing.T) {
	// Same input should give same hash
	h1 := simpleHash("hello world")
	h2 := simpleHash("hello world")
	if h1 != h2 {
		t.Errorf("same input gave different hashes: %d != %d", h1, h2)
	}

	// Different input should give different hash
	h3 := simpleHash("hello world!")
	if h1 == h3 {
		t.Errorf("different input gave same hash: %d", h1)
	}
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"single", 1},
		{"two\nlines", 2},
		{"three\nlines\nhere", 3},
		{"trailing\n", 2},
	}

	for _, tc := range tests {
		got := countLines(tc.input)
		if got != tc.want {
			t.Errorf("countLines(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestSplitLines(t *testing.T) {
	lines := splitLines("one\ntwo\nthree")
	if len(lines) != 3 {
		t.Errorf("len(lines) = %d, want 3", len(lines))
	}
	if lines[0] != "one" || lines[1] != "two" || lines[2] != "three" {
		t.Errorf("lines = %v, want [one two three]", lines)
	}
}

func TestStartsWithLines(t *testing.T) {
	lines := []string{"a", "b", "c", "d"}

	if !startsWithLines(lines, []string{"a", "b"}) {
		t.Error("should start with [a, b]")
	}
	if startsWithLines(lines, []string{"b", "c"}) {
		t.Error("should not start with [b, c]")
	}
	if startsWithLines(lines, []string{"a", "b", "c", "d", "e"}) {
		t.Error("prefix longer than lines should return false")
	}
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		input string
		want  string
	}{
		{"~", home},
		{"~/test", filepath.Join(home, "test")},
		{"~\\test", filepath.Join(home, "test")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, tc := range tests {
		got := util.ExpandPath(tc.input)
		if got != tc.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestArchiveRecord_JSONFormat(t *testing.T) {
	record := ArchiveRecord{
		Session:   "myproject",
		Pane:      "cc_2",
		PaneIndex: 2,
		Agent:     "cc",
		Model:     "opus-4.5",
		Timestamp: time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC),
		Content:   "Test content\nwith newlines",
		Lines:     2,
		Sequence:  5,
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	// Verify required CASS fields are present
	jsonStr := string(data)
	requiredFields := []string{"session", "pane", "agent", "timestamp", "content"}
	for _, field := range requiredFields {
		if !strings.Contains(jsonStr, `"`+field+`"`) {
			t.Errorf("JSON missing required field: %s", field)
		}
	}

	// Verify round-trip
	var decoded ArchiveRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	if decoded.Session != record.Session {
		t.Errorf("Session = %q, want %q", decoded.Session, record.Session)
	}
	if decoded.Agent != record.Agent {
		t.Errorf("Agent = %q, want %q", decoded.Agent, record.Agent)
	}
	if decoded.Content != record.Content {
		t.Errorf("Content = %q, want %q", decoded.Content, record.Content)
	}
}

func TestDefaultArchiverOptions(t *testing.T) {
	opts := DefaultArchiverOptions("test")

	if opts.SessionName != "test" {
		t.Errorf("SessionName = %q, want %q", opts.SessionName, "test")
	}
	if opts.Interval != DefaultInterval {
		t.Errorf("Interval = %v, want %v", opts.Interval, DefaultInterval)
	}
	if opts.LinesPerCapture != DefaultLinesPerCapture {
		t.Errorf("LinesPerCapture = %d, want %d", opts.LinesPerCapture, DefaultLinesPerCapture)
	}
	if opts.MaxFileBytes != DefaultMaxFileBytes {
		t.Errorf("MaxFileBytes = %d, want %d", opts.MaxFileBytes, DefaultMaxFileBytes)
	}
}

func TestPaneState(t *testing.T) {
	state := &PaneState{
		LastHash:    12345,
		LastCapture: time.Now(),
		Sequence:    3,
		TotalLines:  100,
		LastContent: "old content",
	}

	// Verify state fields are accessible
	if state.Sequence != 3 {
		t.Errorf("Sequence = %d, want 3", state.Sequence)
	}
	if state.TotalLines != 100 {
		t.Errorf("TotalLines = %d, want 100", state.TotalLines)
	}
}

// ============================================================================
// Additional tests for bd-s2l4: Output archive capture and storage
// ============================================================================

func TestArchiver_ANSIEscapeContent(t *testing.T) {
	tmpDir := t.TempDir()

	var records []*ArchiveRecord
	a, err := NewArchiver(ArchiverOptions{
		SessionName: "ansi-test",
		OutputDir:   tmpDir,
		OnRecord: func(r *ArchiveRecord) {
			records = append(records, r)
		},
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Content with ANSI escape sequences (colors, formatting)
	ansiContent := "\033[32mGreen text\033[0m\n\033[1;31mBold red\033[0m\n\033[4mUnderline\033[0m"
	record := &ArchiveRecord{
		Session:   "ansi-test",
		Pane:      "cc_2",
		PaneIndex: 2,
		Agent:     "cc",
		Timestamp: time.Now().UTC(),
		Content:   ansiContent,
		Lines:     3,
		Sequence:  1,
	}

	if err := a.writeRecord(record); err != nil {
		t.Fatalf("ARCHIVE_TEST: ANSI content | writeRecord error: %v", err)
	}

	a.flush()

	// Read back and verify ANSI sequences are preserved
	pattern := filepath.Join(tmpDir, "ansi-test_*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("ARCHIVE_TEST: ANSI content | archive file not found")
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ARCHIVE_TEST: ANSI content | reading archive: %v", err)
	}

	var readRecord ArchiveRecord
	if err := json.Unmarshal(data, &readRecord); err != nil {
		t.Fatalf("ARCHIVE_TEST: ANSI content | unmarshaling: %v", err)
	}

	if readRecord.Content != ansiContent {
		t.Errorf("ARCHIVE_TEST: ANSI content | Content mismatch, ANSI sequences not preserved")
	}
	t.Logf("ARCHIVE_TEST: ANSI content | Size=%d | Content preserved correctly", len(readRecord.Content))
}

func TestArchiver_LargeScrollbackContent(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "large-test",
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Generate large content (10000 lines)
	var builder strings.Builder
	lineCount := 10000
	for i := 0; i < lineCount; i++ {
		builder.WriteString("Line ")
		builder.WriteString(string(rune('0' + (i % 10))))
		builder.WriteString(": This is line number ")
		builder.WriteString(strings.Repeat("x", 80)) // ~80 chars per line
		builder.WriteString("\n")
	}
	largeContent := builder.String()
	originalSize := len(largeContent)

	record := &ArchiveRecord{
		Session:   "large-test",
		Pane:      "cc_2",
		PaneIndex: 2,
		Agent:     "cc",
		Timestamp: time.Now().UTC(),
		Content:   largeContent,
		Lines:     lineCount,
		Sequence:  1,
	}

	if err := a.writeRecord(record); err != nil {
		t.Fatalf("ARCHIVE_TEST: Large scrollback | writeRecord error: %v", err)
	}

	a.flush()

	// Read back and verify
	pattern := filepath.Join(tmpDir, "large-test_*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("ARCHIVE_TEST: Large scrollback | archive file not found")
	}

	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatalf("ARCHIVE_TEST: Large scrollback | stat error: %v", err)
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ARCHIVE_TEST: Large scrollback | reading archive: %v", err)
	}

	var readRecord ArchiveRecord
	if err := json.Unmarshal(data, &readRecord); err != nil {
		t.Fatalf("ARCHIVE_TEST: Large scrollback | unmarshaling: %v", err)
	}

	if readRecord.Content != largeContent {
		t.Error("ARCHIVE_TEST: Large scrollback | content mismatch")
	}

	t.Logf("ARCHIVE_TEST: Large scrollback | Size=%d | Lines=%d | FileSize=%d | Path=%s",
		originalSize, lineCount, info.Size(), matches[0])
}

func TestArchiver_UnicodeContent(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "unicode-test",
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Content with various Unicode characters
	unicodeContent := "Hello 世界\n" +
		"日本語テスト\n" +
		"Emoji: 🚀 💻 🎉 ✅ ❌\n" +
		"Mathematical: ∑ ∫ π √ ≠\n" +
		"Currency: € £ ¥ ₹ ₿\n" +
		"Arrows: → ← ↑ ↓ ⇒\n" +
		"Greek: α β γ δ ε"

	record := &ArchiveRecord{
		Session:   "unicode-test",
		Pane:      "cc_2",
		PaneIndex: 2,
		Agent:     "cc",
		Timestamp: time.Now().UTC(),
		Content:   unicodeContent,
		Lines:     7,
		Sequence:  1,
	}

	if err := a.writeRecord(record); err != nil {
		t.Fatalf("ARCHIVE_TEST: Unicode content | writeRecord error: %v", err)
	}

	a.flush()

	// Read back and verify
	pattern := filepath.Join(tmpDir, "unicode-test_*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("ARCHIVE_TEST: Unicode content | archive file not found")
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ARCHIVE_TEST: Unicode content | reading archive: %v", err)
	}

	var readRecord ArchiveRecord
	if err := json.Unmarshal(data, &readRecord); err != nil {
		t.Fatalf("ARCHIVE_TEST: Unicode content | unmarshaling: %v", err)
	}

	if readRecord.Content != unicodeContent {
		t.Errorf("ARCHIVE_TEST: Unicode content | Content mismatch\nGot: %q\nWant: %q",
			readRecord.Content, unicodeContent)
	}
	t.Logf("ARCHIVE_TEST: Unicode content | Size=%d | Runes=%d", len(unicodeContent), len([]rune(unicodeContent)))
}

func TestArchiver_MultipleRecordsAppend(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "append-test",
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Write multiple records
	numRecords := 5
	for i := 1; i <= numRecords; i++ {
		record := &ArchiveRecord{
			Session:   "append-test",
			Pane:      "cc_2",
			PaneIndex: 2,
			Agent:     "cc",
			Timestamp: time.Now().UTC(),
			Content:   strings.Repeat("content ", i*10),
			Lines:     1,
			Sequence:  i,
		}
		if err := a.writeRecord(record); err != nil {
			t.Fatalf("ARCHIVE_TEST: Append record %d | writeRecord error: %v", i, err)
		}
	}

	a.flush()

	// Verify stats
	stats := a.Stats()
	if stats.TotalRecords != numRecords {
		t.Errorf("ARCHIVE_TEST: Append | TotalRecords = %d, want %d", stats.TotalRecords, numRecords)
	}

	// Read back and count records (JSONL format = one record per line)
	pattern := filepath.Join(tmpDir, "append-test_*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("ARCHIVE_TEST: Append | archive file not found")
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ARCHIVE_TEST: Append | reading archive: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != numRecords {
		t.Errorf("ARCHIVE_TEST: Append | file has %d lines, want %d", len(lines), numRecords)
	}

	// Verify each line is valid JSON
	for i, line := range lines {
		var record ArchiveRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Errorf("ARCHIVE_TEST: Append | line %d invalid JSON: %v", i, err)
		}
		if record.Sequence != i+1 {
			t.Errorf("ARCHIVE_TEST: Append | line %d Sequence = %d, want %d", i, record.Sequence, i+1)
		}
	}

	t.Logf("ARCHIVE_TEST: Append | Records=%d | Lines=%d | FileSize=%d",
		numRecords, len(lines), len(data))
}

func TestFindNewContent_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		previous string
		current  string
		want     string
	}{
		{
			name:     "single line overlap",
			previous: "a",
			current:  "a\nb",
			want:     "b",
		},
		{
			name:     "multi-line exact overlap",
			previous: "a\nb\nc",
			current:  "a\nb\nc",
			want:     "",
		},
		{
			name:     "complete content replacement",
			previous: "old content here",
			current:  "completely new content",
			want:     "completely new content",
		},
		{
			name:     "whitespace handling",
			previous: "line1\n   \nline2",
			current:  "line2\nline3\n   ",
			want:     "line3\n   ",
		},
		{
			name:     "long overlap with small new content",
			previous: "a\nb\nc\nd\ne\nf\ng\nh\ni\nj",
			current:  "i\nj\nk",
			want:     "k",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findNewContent(tc.previous, tc.current)
			if got != tc.want {
				t.Errorf("findNewContent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSimpleHash_Collisions(t *testing.T) {
	// Test that slightly different inputs produce different hashes
	inputs := []string{
		"hello world",
		"hello world!",
		"Hello world",
		" hello world",
		"hello world ",
		"helloworld",
	}

	hashes := make(map[uint64]string)
	for _, input := range inputs {
		h := simpleHash(input)
		if prev, exists := hashes[h]; exists {
			t.Errorf("Hash collision: %q and %q both hash to %d", prev, input, h)
		}
		hashes[h] = input
	}
}

func TestCountLines_EdgeCases(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"\n", 2},        // Empty line + trailing
		{"\n\n", 3},      // Two empty lines + trailing
		{"a\n\nb", 3},    // Line with empty middle
		{"a\nb\nc\n", 4}, // Three lines with trailing newline
		{"\na\nb\n", 4},  // Leading newline
		{"   ", 1},       // Whitespace only, no newline
		{"   \n   ", 2},  // Whitespace with newline
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := countLines(tc.input)
			if got != tc.want {
				t.Errorf("countLines(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestArchiver_FileNamingConvention(t *testing.T) {
	tmpDir := t.TempDir()

	sessionName := "my-project-2026"
	a, err := NewArchiver(ArchiverOptions{
		SessionName: sessionName,
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Each writer owns a unique chunk: {session}_{date}_{time}-{unique}.jsonl.
	pattern := filepath.Join(tmpDir, sessionName+"_*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("Glob() error: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("Expected 1 archive file, found %d", len(matches))
	}

	// Verify date format in filename (YYYY-MM-DD)
	filename := filepath.Base(matches[0])
	expectedPrefix := sessionName + "_"
	if !strings.HasPrefix(filename, expectedPrefix) {
		t.Errorf("Filename %q should start with %q", filename, expectedPrefix)
	}

	datePart, uniquePart, ok := strings.Cut(strings.TrimPrefix(filename, expectedPrefix), "_")
	if _, err := time.Parse("2006-01-02", datePart); err != nil {
		t.Errorf("Date part %q should use YYYY-MM-DD: %v", datePart, err)
	}
	if !ok || !strings.HasSuffix(uniquePart, ".jsonl") || !strings.Contains(uniquePart, "-") {
		t.Errorf("Filename %q must include a unique chunk suffix", filename)
	}

	t.Logf("ARCHIVE_TEST: File naming | Session=%s | Filename=%s | DatePart=%s",
		sessionName, filename, datePart)
}

func TestArchiver_CloseIdempotent(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "close-test",
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}

	// Close multiple times should not panic or error
	if err := a.Close(); err != nil {
		t.Errorf("First Close() error: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("Second Close() error: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("Third Close() error: %v", err)
	}
}

// =============================================================================
// Additional Pure Function Tests for Coverage Improvement
// =============================================================================

func TestMin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b, want int
	}{
		{1, 2, 1},
		{2, 1, 1},
		{0, 0, 0},
		{-1, 1, -1},
		{100, 50, 50},
		{-100, -50, -100},
	}

	for _, tc := range tests {
		got := min(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("min(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestFindOverlap_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		prev []string
		curr []string
		want int
	}{
		{
			name: "empty prev",
			prev: []string{},
			curr: []string{"a", "b"},
			want: 0,
		},
		{
			name: "empty curr",
			prev: []string{"a", "b"},
			curr: []string{},
			want: 0,
		},
		{
			name: "no overlap at all",
			prev: []string{"x", "y", "z"},
			curr: []string{"a", "b", "c"},
			want: 0,
		},
		{
			name: "last line of prev in middle of curr",
			prev: []string{"a", "b", "c"},
			curr: []string{"x", "c", "d"},
			want: 2,
		},
		{
			name: "single line prev matches first of curr",
			prev: []string{"x"},
			curr: []string{"x", "y", "z"},
			want: 1,
		},
		{
			name: "complete match",
			prev: []string{"a", "b"},
			curr: []string{"a", "b"},
			want: 2,
		},
		{
			name: "suffix of prev at start of curr",
			prev: []string{"1", "2", "3", "4", "5"},
			curr: []string{"4", "5", "6", "7"},
			want: 2,
		},
		{
			name: "partial suffix match",
			prev: []string{"a", "b", "c", "d"},
			curr: []string{"c", "d", "e"},
			want: 2, // Last 2 lines of prev match first 2 of curr
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findOverlap(tc.prev, tc.curr)
			if got != tc.want {
				t.Errorf("findOverlap() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSplitLines_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  int // Number of lines expected
	}{
		{"empty", "", 0},
		{"single line no newline", "hello", 1},
		{"single line with newline", "hello\n", 1},
		{"two lines", "a\nb", 2},
		{"empty lines", "\n\n\n", 3},
		{"mixed empty", "a\n\nb", 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitLines(tc.input)
			if len(got) != tc.want {
				t.Errorf("splitLines(%q) = %d lines, want %d", tc.input, len(got), tc.want)
			}
		})
	}
}

func TestFlush_NilFile(t *testing.T) {
	a := &Archiver{
		file: nil, // No file set
	}

	// flush with nil file should not panic and return nil
	err := a.flush()
	if err != nil {
		t.Errorf("flush() with nil file should return nil, got %v", err)
	}
}

func TestStartsWithLines_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		lines  []string
		prefix []string
		want   bool
	}{
		{
			name:   "empty prefix",
			lines:  []string{"a", "b"},
			prefix: []string{},
			want:   true,
		},
		{
			name:   "empty lines",
			lines:  []string{},
			prefix: []string{"a"},
			want:   false,
		},
		{
			name:   "both empty",
			lines:  []string{},
			prefix: []string{},
			want:   true,
		},
		{
			name:   "single element match",
			lines:  []string{"x"},
			prefix: []string{"x"},
			want:   true,
		},
		{
			name:   "single element no match",
			lines:  []string{"x"},
			prefix: []string{"y"},
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := startsWithLines(tc.lines, tc.prefix)
			if got != tc.want {
				t.Errorf("startsWithLines() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExpandPath_NoHome(t *testing.T) {
	// Test with path that doesn't start with ~
	result := util.ExpandPath("/absolute/path")
	if result != "/absolute/path" {
		t.Errorf("ExpandPath(/absolute/path) = %q, want /absolute/path", result)
	}

	result = util.ExpandPath("relative/path")
	if result != "relative/path" {
		t.Errorf("ExpandPath(relative/path) = %q, want relative/path", result)
	}

	result = util.ExpandPath("")
	if result != "" {
		t.Errorf("ExpandPath('') = %q, want ''", result)
	}
}
