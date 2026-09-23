package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

const MaxRuntimeWorkSnapshotBytes = 32 << 20

var ErrWorkSnapshotSuperseded = errors.New("work snapshot was superseded by another collector")

// RuntimeWorkSnapshot retains a complete collection, including empty results.
// Payload is the versioned adapter envelope, not authorization to dispatch. Its
// consumer must verify the source and policy again before advertising readiness.
// The revision binds the project, payload, and original collection timestamps;
// revalidation must not extend the collection's lifetime.
type RuntimeWorkSnapshot struct {
	ProjectDir  string
	Revision    string
	CollectedAt time.Time
	StaleAfter  time.Time
	Payload     []byte
}

func NewRuntimeWorkSnapshot(project string, payload []byte, collectedAt, staleAfter time.Time) (*RuntimeWorkSnapshot, error) {
	snapshot := &RuntimeWorkSnapshot{
		ProjectDir: project, Payload: append([]byte(nil), payload...),
		CollectedAt: collectedAt.UTC(), StaleAfter: staleAfter.UTC(),
	}
	if err := snapshot.validate(); err != nil {
		return nil, err
	}
	snapshot.Revision = snapshot.revision()
	return snapshot, nil
}

func (s *RuntimeWorkSnapshot) validate() error {
	if s == nil || !filepath.IsAbs(s.ProjectDir) || filepath.Clean(s.ProjectDir) != s.ProjectDir {
		return errors.New("work snapshot requires a canonical absolute project directory")
	}
	if len(s.Payload) == 0 || len(s.Payload) > MaxRuntimeWorkSnapshotBytes {
		return errors.New("work snapshot payload must be between 1 byte and 32 MiB")
	}
	if s.CollectedAt.IsZero() || s.StaleAfter.IsZero() || !s.StaleAfter.After(s.CollectedAt) {
		return errors.New("work snapshot requires a positive freshness window")
	}
	// SQLite stores signed nanoseconds. Never silently wrap an out-of-range
	// timestamp into a fresh-looking value during serialization.
	for _, ts := range []time.Time{s.CollectedAt, s.StaleAfter} {
		if !time.Unix(0, ts.UnixNano()).Equal(ts) {
			return errors.New("work snapshot timestamp is outside the supported range")
		}
	}
	return nil
}

func (s *RuntimeWorkSnapshot) revision() string {
	h := sha256.New()
	_, _ = h.Write([]byte(s.ProjectDir))
	_, _ = h.Write([]byte{0})
	var timestamps [16]byte
	binary.BigEndian.PutUint64(timestamps[:8], uint64(s.CollectedAt.UnixNano()))
	binary.BigEndian.PutUint64(timestamps[8:], uint64(s.StaleAfter.UnixNano()))
	_, _ = h.Write(timestamps[:])
	_, _ = h.Write(s.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

func (s *RuntimeWorkSnapshot) IsFreshAt(now time.Time) bool {
	return s != nil && !now.Before(s.CollectedAt) && now.Before(s.StaleAfter)
}

// UpsertRuntimeWorkSnapshot publishes with the normalized rows in the caller's
// transaction. A late, older collector cannot overwrite a newer snapshot or
// its unavailable marker. Equal timestamps permit only byte-identical retries.
func (tx *Tx) UpsertRuntimeWorkSnapshot(ctx context.Context, snapshot *RuntimeWorkSnapshot) error {
	if ctx == nil {
		return errors.New("work snapshot requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := snapshot.validate(); err != nil {
		return err
	}
	if snapshot.Revision != snapshot.revision() {
		return errors.New("work snapshot changed after it was prepared")
	}
	result, err := tx.tx.ExecContext(ctx, `
		INSERT INTO runtime_work_snapshots
			(project_dir, revision, collected_at_ns, stale_after_ns, payload)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_dir) DO UPDATE SET
			revision = excluded.revision,
			collected_at_ns = excluded.collected_at_ns,
			stale_after_ns = excluded.stale_after_ns,
			payload = excluded.payload
		WHERE excluded.collected_at_ns > runtime_work_snapshots.collected_at_ns
		   OR (excluded.collected_at_ns = runtime_work_snapshots.collected_at_ns
		       AND excluded.revision = runtime_work_snapshots.revision)`,
		snapshot.ProjectDir, snapshot.Revision, snapshot.CollectedAt.UnixNano(), snapshot.StaleAfter.UnixNano(), snapshot.Payload,
	)
	if err != nil {
		return fmt.Errorf("publish work snapshot: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("publish work snapshot rows affected: %w", err)
	}
	if n != 1 {
		return ErrWorkSnapshotSuperseded
	}
	// Prune expired projects without touching active project snapshots. This
	// shares the publication transaction and preserves the normal GC grace.
	_, err = tx.tx.ExecContext(ctx, `DELETE FROM runtime_work_snapshots WHERE stale_after_ns < ?`,
		time.Now().UTC().Add(-DefaultRuntimeProjectionGCGrace).UnixNano())
	return err
}

// GetRuntimeWorkSnapshot reads one project only. SQL time-string comparisons
// are deliberately avoided; freshness is checked with the original timestamps.
// Source verification happens after this method releases the DB/store locks.
func (s *Store) GetRuntimeWorkSnapshot(ctx context.Context, project string) (*RuntimeWorkSnapshot, error) {
	if ctx == nil {
		return nil, errors.New("work snapshot requires a context")
	}
	if !filepath.IsAbs(project) || filepath.Clean(project) != project {
		return nil, errors.New("work snapshot requires a canonical absolute project directory")
	}
	release, err := s.acquireWorkSnapshotRead(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	item := &RuntimeWorkSnapshot{ProjectDir: project}
	var collected, expires int64
	err = s.db.QueryRowContext(ctx, `
		SELECT revision, collected_at_ns, stale_after_ns,
		       CASE WHEN length(payload) <= ? THEN payload ELSE NULL END
		FROM runtime_work_snapshots WHERE project_dir = ?`, MaxRuntimeWorkSnapshotBytes, project,
	).Scan(&item.Revision, &collected, &expires, &item.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read work snapshot: %w", err)
	}
	item.CollectedAt = time.Unix(0, collected).UTC()
	item.StaleAfter = time.Unix(0, expires).UTC()
	if err := item.validate(); err != nil {
		return nil, fmt.Errorf("invalid persisted work snapshot: %w", err)
	}
	if item.Revision != item.revision() {
		return nil, errors.New("persisted work snapshot revision does not match its contents")
	}
	return item, nil
}

// RuntimeWorkSnapshotIsCurrent detects a refresh/replacement while a consumer
// was verifying source files or waiting for a fresh reservation observation.
func (s *Store) RuntimeWorkSnapshotIsCurrent(ctx context.Context, project, revision string) (bool, error) {
	if ctx == nil {
		return false, errors.New("work snapshot requires a context")
	}
	release, err := s.acquireWorkSnapshotRead(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	var current string
	err = s.db.QueryRowContext(ctx, `SELECT revision FROM runtime_work_snapshots WHERE project_dir = ?`, project).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && current == revision, err
}

// The caller's budget includes waiting for a local writer, not only the SQL
// query. Do not block on RWMutex.RLock after the request has been cancelled.
func (s *Store) acquireWorkSnapshotRead(ctx context.Context) (func(), error) {
	return acquireWorkSnapshotLock(ctx, s.mu.TryRLock, s.mu.RUnlock)
}

func acquireWorkSnapshotLock(ctx context.Context, tryLock func() bool, unlock func()) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tryLock() {
		if err := ctx.Err(); err != nil {
			unlock()
			return nil, err
		}
		return unlock, nil
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if tryLock() {
				if err := ctx.Err(); err != nil {
					unlock()
					return nil, err
				}
				return unlock, nil
			}
		}
	}
}

// PublishRuntimeWorkSnapshot is the context-aware publication boundary for a
// complete standalone work observation. Consumers of display-row transactions
// can instead call Tx.UpsertRuntimeWorkSnapshot in their existing transaction.
// Waiting for the local writer and the database is part of the same budget.
func (s *Store) PublishRuntimeWorkSnapshot(ctx context.Context, snapshot *RuntimeWorkSnapshot) error {
	if ctx == nil {
		return errors.New("work snapshot requires a context")
	}
	release, err := acquireWorkSnapshotLock(ctx, s.mu.TryLock, s.mu.Unlock)
	if err != nil {
		return err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin work snapshot publication: %w", err)
	}
	defer tx.Rollback()
	if err := (&Tx{tx: tx}).UpsertRuntimeWorkSnapshot(ctx, snapshot); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit work snapshot publication: %w", err)
	}
	return nil
}
