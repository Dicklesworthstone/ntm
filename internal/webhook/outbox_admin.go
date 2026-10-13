package webhook

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxOutboxRecoveries = 64

// outboxRecovery preserves the terminal evidence that an operator inspected.
// A recovery reuses the frozen body and delivery ID; it cannot authorize a
// changed endpoint or reset the cumulative attempt counter.
type outboxRecovery struct {
	AuthorizedAt time.Time  `json:"authorized_at"`
	AfterAttempt int        `json:"after_attempt"`
	Failure      AttemptLog `json:"failure"`
}

// OutboxEntry intentionally excludes payloads, messages, credentials and raw
// errors. Revision is an optimistic concurrency token, not an authenticity proof.
type OutboxEntry struct {
	Record         string    `json:"record"`
	Revision       string    `json:"revision,omitempty"`
	DeliveryID     string    `json:"delivery_id,omitempty"`
	WebhookID      string    `json:"webhook_id,omitempty"`
	Session        string    `json:"session,omitempty"`
	EventType      string    `json:"event_type,omitempty"`
	State          string    `json:"state"`
	BlockedBy      string    `json:"blocked_by,omitempty"`
	Attempt        int       `json:"attempt"`
	Recoveries     int       `json:"recoveries"`
	NextRetry      time.Time `json:"next_retry,omitempty"`
	LastStatus     int       `json:"last_status,omitempty"`
	RetryAllowed   bool      `json:"retry_allowed"`
	ArchiveAllowed bool      `json:"archive_allowed"`
}

// OutboxSnapshot is a read-only, per-record snapshot, not a transaction over
// the whole directory. Records may finish or change immediately after listing.
type OutboxSnapshot struct {
	Enabled   bool          `json:"enabled"`
	Directory string        `json:"directory,omitempty"`
	Limit     int           `json:"limit"`
	Entries   []OutboxEntry `json:"entries"`
}

// openExistingOutbox performs no writes, creates no locks and starts no worker.
// Both administration and normal startup use the same path/privacy checks.
func openExistingOutbox(dir string, limit int) (*deliveryOutbox, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("webhook outbox must be a private directory (mode 0700)")
	}
	return &deliveryOutbox{dir: dir, root: info, limit: limit, wake: make(chan struct{}, 1),
		active: make(map[string]bool), reported: make(map[string]bool)}, nil
}

func (m *WebhookManager) administrativeOutbox() (*deliveryOutbox, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.config.OutboxDir == "" {
		return nil, errors.New("durable webhook outbox is not configured")
	}
	if m.outbox != nil {
		return m.outbox, m.outbox.checkRoot()
	}
	return openExistingOutbox(m.config.OutboxDir, m.config.OutboxLimit)
}

func outboxRevision(record outboxRecord) string {
	data, _ := json.Marshal(record)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateOutboxRecovery(record outboxRecord) error {
	if len(record.Recovery) > maxOutboxRecoveries {
		return errors.New("too many webhook outbox recovery records")
	}
	previous := -1
	for _, recovery := range record.Recovery {
		if recovery.AuthorizedAt.IsZero() || recovery.Failure.Timestamp.IsZero() ||
			recovery.AfterAttempt <= previous || recovery.AfterAttempt > record.Attempt ||
			recovery.AfterAttempt < 1 || recovery.Failure.Attempt != recovery.AfterAttempt {
			return errors.New("inconsistent webhook outbox recovery evidence")
		}
		previous = recovery.AfterAttempt
	}
	if len(record.Recovery) > 0 && record.Failed && record.Attempt <= previous {
		return errors.New("recovered webhook cannot fail before another attempt")
	}
	return nil
}

func (m *WebhookManager) describeOutbox(key string, record outboxRecord) OutboxEntry {
	entry := OutboxEntry{Record: key, Revision: outboxRevision(record), DeliveryID: record.ID,
		WebhookID: record.WebhookID, Session: record.Event.Session, EventType: record.Event.Type,
		State: "pending", Attempt: record.Attempt, Recoveries: len(record.Recovery), NextRetry: record.NextRetry}
	if record.LastAttempt != nil {
		entry.LastStatus = record.LastAttempt.StatusCode
	}
	if record.Failed {
		entry.State = "failed"
	} else if record.NextRetry.After(time.Now()) {
		entry.State = "retry_wait"
	}
	m.webhooksMu.RLock()
	wh := m.webhooks[record.WebhookID]
	m.webhooksMu.RUnlock()
	switch {
	case !m.mayPersistWebhook(record.Event):
		entry.BlockedBy = "privacy"
	case wh == nil || !wh.Enabled:
		entry.BlockedBy = "endpoint_unavailable"
	case record.Fingerprint != m.outboxFingerprint(wh):
		entry.BlockedBy = "configuration_changed"
	case !m.matchesWebhook(wh, record.Event):
		entry.BlockedBy = "event_filtered"
	case len(record.Recovery) >= maxOutboxRecoveries || record.Attempt >= 1000000:
		entry.BlockedBy = "recovery_limit"
	case record.Failed && (record.Attempt < 1 || record.LastAttempt == nil || record.LastAttempt.Attempt != record.Attempt || record.LastAttempt.Timestamp.IsZero()):
		entry.BlockedBy = "incomplete_failure_evidence"
	}
	entry.RetryAllowed = record.Failed && entry.BlockedBy == ""
	entry.ArchiveAllowed = record.Failed
	return entry
}

// ListOutbox inspects retained work without creating storage, starting workers
// or sending HTTP. Missing storage is an empty snapshot. Invalid records still
// appear because they consume capacity; their untrusted content is never echoed.
func (m *WebhookManager) ListOutbox() (OutboxSnapshot, error) {
	result := OutboxSnapshot{Enabled: m.config.OutboxDir != "", Directory: m.config.OutboxDir,
		Limit: m.config.OutboxLimit, Entries: make([]OutboxEntry, 0)}
	if !result.Enabled {
		return result, nil
	}
	o, err := m.administrativeOutbox()
	if o == nil && errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Directory = o.dir
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := o.read(entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue // A worker acknowledged this entry during inspection.
		}
		if err != nil {
			result.Entries = append(result.Entries, OutboxEntry{Record: entry.Name(), State: "invalid", BlockedBy: "invalid_record"})
			continue
		}
		result.Entries = append(result.Entries, m.describeOutbox(entry.Name(), record))
	}
	// Root replacement is an operation failure, not a directory full of bad records.
	return result, o.checkRoot()
}

// RetryOutbox authorizes one new configured retry cycle for exactly the failed
// record inspected by ListOutbox. It does not start workers or send HTTP. A live
// or later manager for this project will deliver after the retained cooldown.
// Repeating the same command is refused once the revision has changed.
func (m *WebhookManager) RetryOutbox(key, revision string) (OutboxEntry, error) {
	var result OutboxEntry
	if !validOutboxKey(key) || len(revision) != 64 {
		return result, errors.New("retry requires a record name and revision from webhook-outbox list")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return result, errors.New("invalid webhook outbox revision")
	}
	o, err := m.administrativeOutbox()
	if err != nil {
		return result, err
	}
	lock, err := o.lockKey(key)
	if err != nil {
		return result, fmt.Errorf("claim webhook outbox recovery: %w", err)
	}
	defer lock.Close()
	record, err := o.read(key)
	if err != nil {
		return result, err
	}
	if outboxRevision(record) != revision {
		return result, errors.New("webhook outbox record changed; inspect it again before retrying")
	}
	entry := m.describeOutbox(key, record)
	if !entry.RetryAllowed {
		return result, fmt.Errorf("webhook outbox record is not retryable (state=%s, blocked_by=%s)", entry.State, entry.BlockedBy)
	}
	// Fence a concurrent Register through the durable authorization write. The
	// privacy callback above runs outside this lock and may inspect the manager.
	m.webhooksMu.RLock()
	wh := m.webhooks[record.WebhookID]
	if wh == nil || !wh.Enabled || record.Fingerprint != m.outboxFingerprint(wh) || !m.matchesWebhook(wh, record.Event) {
		m.webhooksMu.RUnlock()
		return result, errors.New("webhook configuration changed during recovery")
	}
	record.Recovery = append(record.Recovery, outboxRecovery{AuthorizedAt: time.Now().UTC(),
		AfterAttempt: record.Attempt, Failure: *record.LastAttempt})
	record.Failed = false
	if err := validateOutboxRecovery(record); err != nil {
		m.webhooksMu.RUnlock()
		return result, err
	}
	err = o.save(key, record)
	m.webhooksMu.RUnlock()
	if err != nil {
		return result, fmt.Errorf("persist webhook recovery authorization: %w", err)
	}
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return m.describeOutbox(key, record), nil
}

// OutboxArchive confirms that a terminal receipt and its recovery history are
// retained outside active capacity. Archiving never sends or edits the receipt.
type OutboxArchive struct {
	Record     string    `json:"record"`
	Revision   string    `json:"revision"`
	DeliveryID string    `json:"delivery_id"`
	ArchivedAt time.Time `json:"archived_at"`
	Path       string    `json:"path"`
}

type outboxArchiveManifest struct {
	Version    int       `json:"version"`
	Record     string    `json:"record"`
	Revision   string    `json:"revision"`
	ArchivedAt time.Time `json:"archived_at"`
}

// ArchiveOutbox reclaims active receipt capacity only after the exact inspected
// terminal record and an archive manifest have durable directory entries. A hard
// link preserves every source byte and avoids a remove-before-copy crash window.
// Both the admission lock and the sender's shard lock are held. Retrying after a
// crash is safe; a conflicting archive or a newly admitted source is never replaced.
func (m *WebhookManager) ArchiveOutbox(key, revision string) (OutboxArchive, error) {
	var result OutboxArchive
	if !validOutboxKey(key) || len(revision) != 64 || strings.ToLower(revision) != revision {
		return result, errors.New("archive requires a record name and revision from webhook-outbox list")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return result, errors.New("invalid webhook outbox revision")
	}
	o, err := m.administrativeOutbox()
	if err != nil {
		return result, err
	}
	admission, err := openOutboxLock(filepath.Join(o.dir, "admission.lock"))
	if err != nil {
		return result, err
	}
	defer admission.Close()
	lock, err := o.lockKey(key)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	record, err := o.read(key)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return result, err
	}
	if !missing && (outboxRevision(record) != revision || !record.Failed) {
		return result, errors.New("only the inspected, unchanged failed webhook receipt can be archived")
	}
	archiveRoot, archiveInfo, err := privateArchiveDirectory(o.dir, "archived", !missing)
	if err != nil {
		return result, err
	}
	dir, dirInfo, err := privateArchiveDirectory(archiveRoot, revision, !missing)
	if err != nil {
		return result, err
	}
	source := filepath.Join(o.dir, key)
	target := filepath.Join(dir, key)
	if !missing {
		if err := os.Link(source, target); err != nil && !errors.Is(err, os.ErrExist) {
			return result, fmt.Errorf("preserve webhook receipt before archival: %w", err)
		}
		if err := verifyArchivedSource(source, target); err != nil {
			return result, err
		}
	}
	archive, err := openExistingOutbox(dir, 0)
	if err != nil {
		return result, err
	}
	preserved, err := archive.read(key)
	if err != nil || !preserved.Failed || outboxRevision(preserved) != revision {
		return result, errors.New("archive does not contain the inspected terminal receipt; source retained")
	}
	manifest, err := ensureArchiveManifest(dir, key, revision, !missing)
	if err != nil {
		return result, err
	}
	// Make the retained data and the complete path to it durable BEFORE unlink.
	file, err := os.Open(target)
	if err != nil {
		return result, err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return result, errors.Join(err, closeErr)
	}
	for _, path := range []string{dir, archiveRoot, o.dir} {
		if err := syncOutboxDir(path); err != nil {
			return result, fmt.Errorf("sync webhook archive before removing active receipt: %w", err)
		}
	}
	if err := o.checkRoot(); err != nil {
		return result, err
	}
	for path, original := range map[string]os.FileInfo{archiveRoot: archiveInfo, dir: dirInfo} {
		current, err := os.Lstat(path)
		if err != nil || !current.IsDir() || !os.SameFile(original, current) || current.Mode().Perm()&0077 != 0 {
			return result, errors.New("webhook archive directory identity or privacy changed; source retained")
		}
	}
	if !missing {
		current, err := o.read(key)
		if err != nil || !current.Failed || outboxRevision(current) != revision {
			return result, errors.New("active webhook receipt changed during archival; inspect again")
		}
		if err := verifyArchivedSource(source, target); err != nil {
			return result, err
		}
		if err := os.Remove(source); err != nil {
			return result, fmt.Errorf("archive preserved but active receipt removal failed: %w", err)
		}
		if err := syncOutboxDir(o.dir); err != nil {
			return result, fmt.Errorf("archive preserved but capacity reclamation durability is uncertain: %w", err)
		}
	}
	return OutboxArchive{Record: key, Revision: revision, DeliveryID: preserved.ID,
		ArchivedAt: manifest.ArchivedAt, Path: target}, nil
}

func verifyArchivedSource(source, target string) error {
	active, err := os.Lstat(source)
	if err != nil {
		return err
	}
	preserved, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !active.Mode().IsRegular() || !preserved.Mode().IsRegular() ||
		active.Mode().Perm()&0077 != 0 || preserved.Mode().Perm()&0077 != 0 || !os.SameFile(active, preserved) {
		return errors.New("archive conflicts with active receipt identity; source retained")
	}
	return nil
}

func privateArchiveDirectory(parent, name string, create bool) (string, os.FileInfo, error) {
	dir := filepath.Join(parent, name)
	if create {
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", nil, errors.New("webhook archive must use private real directories, not symlinks")
	}
	return dir, info, nil
}

func readArchiveManifest(path, key, revision string) (outboxArchiveManifest, error) {
	var manifest outboxArchiveManifest
	info, err := os.Lstat(path)
	if err != nil {
		return manifest, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return manifest, errors.New("invalid webhook archive manifest file")
	}
	file, err := os.Open(path)
	if err != nil {
		return manifest, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return manifest, errors.New("webhook archive manifest identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return manifest, errors.New("cannot read bounded webhook archive manifest")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, errors.New("invalid webhook archive manifest")
	}
	if decoder.Decode(new(interface{})) != io.EOF || manifest.Version != 1 || manifest.Record != key ||
		manifest.Revision != revision || manifest.ArchivedAt.IsZero() {
		return manifest, errors.New("webhook archive manifest does not match the inspected receipt")
	}
	// An interrupted previous archive may have published the manifest without
	// syncing its directory yet. Every successful retry re-establishes durability.
	return manifest, file.Sync()
}

func ensureArchiveManifest(dir, key, revision string, create bool) (outboxArchiveManifest, error) {
	path := filepath.Join(dir, "manifest.json")
	manifest, err := readArchiveManifest(path, key, revision)
	if err == nil || !errors.Is(err, os.ErrNotExist) || !create {
		return manifest, err
	}
	manifest = outboxArchiveManifest{Version: 1, Record: key, Revision: revision, ArchivedAt: time.Now().UTC()}
	data, err := json.Marshal(manifest)
	if err != nil {
		return manifest, err
	}
	file, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return manifest, err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		return manifest, err
	}
	if err := file.Sync(); err != nil {
		return manifest, err
	}
	if err := file.Close(); err != nil {
		return manifest, err
	}
	// Publish without replacing any existing evidence, including a symlink or
	// malformed manifest. A failed write leaves the active receipt untouched.
	if err := os.Link(file.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return manifest, err
	}
	return readArchiveManifest(path, key, revision)
}
