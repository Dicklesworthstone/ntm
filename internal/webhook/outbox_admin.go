package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	Record       string    `json:"record"`
	Revision     string    `json:"revision,omitempty"`
	DeliveryID   string    `json:"delivery_id,omitempty"`
	WebhookID    string    `json:"webhook_id,omitempty"`
	Session      string    `json:"session,omitempty"`
	EventType    string    `json:"event_type,omitempty"`
	State        string    `json:"state"`
	BlockedBy    string    `json:"blocked_by,omitempty"`
	Attempt      int       `json:"attempt"`
	Recoveries   int       `json:"recoveries"`
	NextRetry    time.Time `json:"next_retry,omitempty"`
	LastStatus   int       `json:"last_status,omitempty"`
	RetryAllowed bool      `json:"retry_allowed"`
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
	case len(record.Recovery) >= maxOutboxRecoveries || record.Attempt >= 1000000:
		entry.BlockedBy = "recovery_limit"
	case record.Failed && (record.Attempt < 1 || record.LastAttempt == nil || record.LastAttempt.Attempt != record.Attempt || record.LastAttempt.Timestamp.IsZero()):
		entry.BlockedBy = "incomplete_failure_evidence"
	}
	entry.RetryAllowed = record.Failed && entry.BlockedBy == ""
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
	if wh == nil || !wh.Enabled || record.Fingerprint != m.outboxFingerprint(wh) {
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
