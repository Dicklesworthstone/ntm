package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	maxBackgroundProgressBytes  = 64 << 20
	maxBackgroundProgressRecord = 64 << 10
	backgroundProgressPoll      = 100 * time.Millisecond
)

// backgroundProgressRecord is a private worker-to-controller transport. Events
// retain the executor's vocabulary; the footer proves the writer drained its
// channel. A terminal checkpoint alone does not prove the last event was read.
type backgroundProgressRecord struct {
	Event *ProgressEvent `json:"event,omitempty"`
	Done  bool           `json:"done,omitempty"`
	Error string         `json:"error,omitempty"`
}

// openBackgroundProgress must run before publishing the startup acknowledgment.
// A single goroutine owns the file, and finish joins it before run ownership is
// retired. Failed writes still drain the channel so telemetry cannot deadlock
// execution. The caller receives the failure rather than claiming full delivery.
func openBackgroundProgress(dir, runID string) (chan<- ProgressEvent, func() error, error) {
	if err := validateRunID(runID); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, runID+".events"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, nil, fmt.Errorf("create background progress: %w", err)
	}
	progress := make(chan ProgressEvent, 256)
	done := make(chan struct{})
	var writeErr error
	go func() {
		defer close(done)
		var written int64
		for event := range progress {
			if writeErr != nil {
				continue
			}
			data, err := json.Marshal(backgroundProgressRecord{Event: &event})
			if err == nil && len(data)+1 > maxBackgroundProgressRecord {
				err = errors.New("background progress record exceeds 64 KiB")
			}
			// Reserve space for the footer, including its bounded error text.
			if err == nil && written+int64(len(data)+1) > maxBackgroundProgressBytes-4096 {
				err = errors.New("background progress exceeds 64 MiB")
			}
			if err == nil {
				data = append(data, '\n')
				var n int
				n, err = file.Write(data)
				written += int64(n)
			}
			if err != nil {
				writeErr = err
			}
		}
		footer := backgroundProgressRecord{Done: true}
		if writeErr != nil {
			footer.Error = "background progress incomplete; inspect the worker log and durable run state"
		}
		data, err := json.Marshal(footer)
		if err == nil {
			_, err = file.Write(append(data, '\n'))
		}
		writeErr = errors.Join(writeErr, err, file.Sync(), file.Close())
	}()
	var once sync.Once
	finish := func() error {
		once.Do(func() { close(progress) })
		<-done
		return writeErr
	}
	return progress, finish, nil
}

// FollowBackgroundProgress relays actual detached-worker events. It never
// registers a second executor or synthesizes success from a stale checkpoint.
// The reader is bounded, checks replacement/truncation, and does not consume a
// partial final line. Cancellation stops observation, not the detached work.
func FollowBackgroundProgress(ctx context.Context, projectDir, runID string, publish func(ProgressEvent)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if publish == nil {
		return errors.New("background progress publisher is required")
	}
	if err := validateRunID(runID); err != nil {
		return err
	}
	path := filepath.Join(pipelineStateDir(normalizeLockRoot(projectDir)), "background", runID+".events")
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("background progress must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("background progress changed while opening")
	}
	ticker := time.NewTicker(backgroundProgressPoll)
	defer ticker.Stop()
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := os.Lstat(path)
		if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
			return errors.New("background progress file was replaced")
		}
		var finished bool
		offset, finished, err = readBackgroundProgress(ctx, file, offset, publish)
		if err != nil || finished {
			return err
		}
		// The producer closes its stream before releasing its stable run lock.
		// A free lock with no footer means the worker died, not a successful run.
		alive, err := backgroundProgressOwnerAlive(ctx, projectDir, runID)
		if err != nil {
			return err
		}
		if !alive {
			// Close may race the first EOF read. Drain once more after ownership
			// ended so a normally flushed footer is not mistaken for a crash.
			_, finished, err = readBackgroundProgress(ctx, file, offset, publish)
			if err != nil || finished {
				return err
			}
			return errors.New("background owner exited before completing progress; inspect durable run state")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func readBackgroundProgress(ctx context.Context, file *os.File, offset int64, publish func(ProgressEvent)) (int64, bool, error) {
	info, err := file.Stat()
	if err != nil {
		return offset, false, err
	}
	if offset < 0 || info.Size() < offset || info.Size() > maxBackgroundProgressBytes {
		return offset, false, errors.New("background progress size or cursor is invalid")
	}
	// Read the currently visible prefix only. A concurrent append is consumed
	// in the next poll; a partial write must not advance the continuation offset.
	reader := bufio.NewReaderSize(io.NewSectionReader(file, offset, info.Size()-offset), maxBackgroundProgressRecord)
	for {
		if err := ctx.Err(); err != nil {
			return offset, false, err
		}
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, io.EOF) {
			return offset, false, nil
		}
		if err != nil {
			return offset, false, fmt.Errorf("read background progress record: %w", err)
		}
		var record backgroundProgressRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return offset, false, fmt.Errorf("decode background progress: %w", err)
		}
		if record.Done {
			if record.Event != nil {
				return offset, false, errors.New("background progress footer contains an event")
			}
			offset += int64(len(line))
			if record.Error != "" {
				return offset, true, errors.New(record.Error)
			}
			return offset, true, nil
		}
		if record.Event == nil || record.Error != "" {
			return offset, false, errors.New("invalid background progress record")
		}
		publish(*record.Event)
		offset += int64(len(line))
	}
}

func backgroundProgressOwnerAlive(ctx context.Context, projectDir, runID string) (bool, error) {
	prefix, err := runControlPrefix(projectDir, runID)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(prefix + ".lock"); err != nil {
		return false, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Millisecond)
	defer cancel()
	lock, err := openLockedFile(probeCtx, prefix+".lock")
	if err == nil {
		lock.unlockAndClose()
		return false, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true, nil
	}
	return false, err
}
