package webhook

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	outboxVersion      = 1
	outboxRecordLimit  = 2 * 1024 * 1024
	outboxPollInterval = time.Second
)

// An outbox record contains the already-redacted event and frozen wire payload,
// never the endpoint URL, headers, or signing secret. Replay requires the same
// registered configuration fingerprint. Permanent failures remain on disk for
// inspection instead of being evicted by the in-memory dead-letter ring.
type outboxRecord struct {
	Version     int         `json:"version"`
	ID          string      `json:"id"`
	WebhookID   string      `json:"webhook_id"`
	Fingerprint string      `json:"fingerprint"`
	Event       Event       `json:"event"`
	Payload     []byte      `json:"payload"`
	Attempt     int         `json:"attempt"`
	NextRetry   time.Time   `json:"next_retry,omitempty"`
	Failed      bool        `json:"failed,omitempty"`
	LastAttempt *AttemptLog `json:"last_attempt,omitempty"`
	// Recovery is an append-only record of explicit operator retry authorizations.
	// Attempt remains cumulative; each authorization starts a fresh retry budget.
	Recovery []outboxRecovery `json:"recovery,omitempty"`
}

type deliveryOutbox struct {
	dir      string
	root     os.FileInfo
	limit    int
	wake     chan struct{}
	mu       sync.Mutex
	active   map[string]bool
	reported map[string]bool
}

type outboxClaim struct {
	store  *deliveryOutbox
	key    string
	lock   *os.File
	record outboxRecord
}

func newDeliveryOutbox(dir string, limit int) (*deliveryOutbox, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	outbox, err := openExistingOutbox(dir, limit)
	if err != nil {
		return nil, err
	}
	dir = outbox.dir
	// Establish support before Start accepts anything. Locks are a fixed-size
	// bank, not one permanent sidecar per delivered event.
	lock, err := openOutboxLock(filepath.Join(dir, "admission.lock"))
	if err != nil && !errors.Is(err, errOutboxBusy) {
		return nil, err
	}
	if lock != nil {
		_ = lock.Close()
	}
	// MkdirAll may have created several ancestors. Sync their directory
	// entries too; syncing only a receipt's leaf directory would not make
	// the newly created path to that receipt durable.
	for ancestor := dir; ; ancestor = filepath.Dir(ancestor) {
		if err := syncOutboxDir(ancestor); err != nil {
			return nil, fmt.Errorf("sync webhook outbox directory: %w", err)
		}
		if filepath.Dir(ancestor) == ancestor {
			break
		}
	}
	return outbox, nil
}

func (o *deliveryOutbox) checkRoot() error {
	info, err := os.Lstat(o.dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || !os.SameFile(o.root, info) || info.Mode().Perm()&0077 != 0 {
		return errors.New("webhook outbox directory identity or privacy changed; restart after inspection")
	}
	return nil
}

func outboxKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]) + ".json"
}

func validOutboxKey(key string) bool {
	if len(key) != 69 || !strings.HasSuffix(key, ".json") {
		return false
	}
	_, err := hex.DecodeString(key[:64])
	return err == nil && strings.ToLower(key) == key
}

func (m *WebhookManager) outboxFingerprint(wh *WebhookConfig) string {
	// Include redaction policy as well as destination, credentials and format:
	// changing policy must not accidentally release a previously frozen body.
	data, _ := json.Marshal(struct {
		Webhook   *WebhookConfig
		Redaction interface{}
	}{wh, m.redactionCfg})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (o *deliveryOutbox) lockKey(key string) (*os.File, error) {
	if err := o.checkRoot(); err != nil {
		return nil, err
	}
	if !validOutboxKey(key) {
		return nil, errors.New("invalid webhook outbox record name")
	}
	prefix, _ := hex.DecodeString(key[:2])
	return openOutboxLock(filepath.Join(o.dir, fmt.Sprintf("shard-%02x.lock", prefix[0]&63)))
}

func (o *deliveryOutbox) read(key string) (outboxRecord, error) {
	var record outboxRecord
	if err := o.checkRoot(); err != nil {
		return record, err
	}
	if !validOutboxKey(key) {
		return record, errors.New("invalid webhook outbox record name")
	}
	path := filepath.Join(o.dir, key)
	info, err := os.Lstat(path)
	if err != nil {
		return record, err
	}
	if !info.Mode().IsRegular() || info.Size() > outboxRecordLimit || info.Mode().Perm()&0077 != 0 {
		return record, errors.New("webhook outbox record must be a bounded private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return record, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, outboxRecordLimit+1))
	if err != nil {
		return record, err
	}
	if len(data) > outboxRecordLimit {
		return record, errors.New("webhook outbox record too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, fmt.Errorf("decode webhook outbox: %w", err)
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return record, errors.New("trailing webhook outbox data")
	}
	fingerprint, err := hex.DecodeString(record.Fingerprint)
	if record.Version != outboxVersion || record.ID == "" || outboxKey(record.ID) != key ||
		record.WebhookID == "" || err != nil || len(fingerprint) != sha256.Size ||
		record.Event.ID == "" || record.ID != record.Event.ID+"_"+record.WebhookID || record.Event.Timestamp.IsZero() || record.Payload == nil ||
		record.Attempt < 0 || record.Attempt > 1000000 || (record.Failed && record.LastAttempt == nil) {
		return record, errors.New("invalid or unsupported webhook outbox record")
	}
	if err := validateOutboxRecovery(record); err != nil {
		return record, err
	}
	return record, nil
}

// save holds admission ownership for a new record or shard ownership for an
// attempt transition. Sync before and after rename: a successful Dispatch
// means acceptance is on disk, not merely
// buffered in this process. A failed write never authorizes an HTTP attempt.
func (o *deliveryOutbox) save(key string, record outboxRecord) error {
	if err := o.checkRoot(); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > outboxRecordLimit {
		return errors.New("webhook outbox record too large")
	}
	file, err := os.CreateTemp(o.dir, ".outbox-*")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(o.dir, key)); err != nil {
		return err
	}
	return syncOutboxDir(o.dir)
}

func syncOutboxDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

// enqueue retains matching existing receipts rather than resetting attempts
// or receiver cooldowns. All admissions share a lock so concurrent producers
// cannot exceed the configured disk record limit. Contention is an explicit
// admission error, never permission to silently fall back to volatile delivery.
func (o *deliveryOutbox) enqueue(record outboxRecord) error {
	if err := o.checkRoot(); err != nil {
		return err
	}
	admission, err := openOutboxLock(filepath.Join(o.dir, "admission.lock"))
	deadline := time.Now().Add(time.Second)
	for errors.Is(err, errOutboxBusy) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		admission, err = openOutboxLock(filepath.Join(o.dir, "admission.lock"))
	}
	if err != nil {
		return err
	}
	defer admission.Close()
	key := outboxKey(record.ID)
	// Existing receipts are immutable except for worker-owned attempt state.
	// Do not take a long-lived HTTP shard lock for admission: an unrelated
	// slow request in that shard must not prevent durable acceptance.
	prior, err := o.read(key)
	if err == nil {
		oldEvent, _ := json.Marshal(prior.Event)
		newEvent, _ := json.Marshal(record.Event)
		if prior.Fingerprint != record.Fingerprint || prior.WebhookID != record.WebhookID ||
			!bytes.Equal(prior.Payload, record.Payload) || !bytes.Equal(oldEvent, newEvent) {
			return errors.New("webhook delivery ID already has different durable content")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return err
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	if count >= o.limit {
		return errors.New("webhook outbox full; retained deliveries and terminal records require inspection")
	}
	if err := o.save(key, record); err != nil {
		return err
	}
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return nil
}

func (m *WebhookManager) dispatchOutbox(event Event, webhooks []*WebhookConfig) error {
	var failures []error
	// Build every payload before persisting any destination. A template error
	// should not produce an avoidable partial fanout.
	records := make([]outboxRecord, 0, len(webhooks))
	for _, wh := range webhooks {
		d := Delivery{ID: fmt.Sprintf("%s_%s", event.ID, wh.ID), Event: event, Webhook: wh}
		payload, err := m.buildPayload(&d)
		if err != nil {
			return err
		}
		records = append(records, outboxRecord{Version: outboxVersion, ID: d.ID, WebhookID: wh.ID,
			Fingerprint: m.outboxFingerprint(wh), Event: event, Payload: append([]byte{}, payload...)})
	}
	for _, record := range records {
		if err := m.outbox.enqueue(record); err != nil {
			failures = append(failures, fmt.Errorf("persist delivery %s: %w", record.ID, err))
			continue
		}
		// Dispatch still holds lifecycleMu. Offer accepted first attempts now,
		// so a command that immediately closes its bridge drains them instead
		// of relying on the next process to start the periodic recovery feeder.
		for _, wh := range webhooks {
			if wh.ID == record.WebhookID {
				m.offerOutbox(record, wh)
				break
			}
		}
	}
	return errors.Join(failures...)
}

// Each process has a bounded feeder, but all feeders use the SAME HTTP worker
// pool and retry classifier. A shard lock is held from receipt reload through
// request completion and settlement. The OS releases it on a crash; another
// process can then retry the same delivery ID, never a concurrent second send.
func (m *WebhookManager) outboxFeeder() {
	defer m.wg.Done()
	ticker := time.NewTicker(outboxPollInterval)
	defer ticker.Stop()
	for {
		m.feedOutbox()
		select {
		case <-m.retryStop:
			return
		case <-m.outbox.wake:
		case <-ticker.C:
		}
	}
}

func (m *WebhookManager) feedOutbox() {
	entries, err := os.ReadDir(m.outbox.dir)
	if err != nil {
		slog.Warn("cannot scan webhook outbox", "error", err)
		return
	}
	for _, entry := range entries {
		if m.stopping.Load() {
			return
		}
		key := entry.Name()
		if !validOutboxKey(key) {
			continue
		}
		record, err := m.outbox.read(key)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			slog.Warn("invalid webhook outbox record retained", "record", key, "error", err)
			continue
		}
		if record.Failed || record.NextRetry.After(time.Now()) || !m.mayPersistWebhook(record.Event) {
			continue
		}
		m.webhooksMu.RLock()
		wh := m.webhooks[record.WebhookID]
		m.webhooksMu.RUnlock()
		if wh == nil || !wh.Enabled || record.Fingerprint != m.outboxFingerprint(wh) || !m.matchesWebhook(wh, record.Event) {
			m.outbox.mu.Lock()
			first := !m.outbox.reported[key]
			m.outbox.reported[key] = true
			m.outbox.mu.Unlock()
			if first {
				slog.Warn("webhook outbox receipt retained: matching enabled configuration required", "delivery", record.ID)
			}
			continue
		}
		m.lifecycleMu.Lock()
		if !m.started.Load() {
			m.lifecycleMu.Unlock()
			return
		}
		m.offerOutbox(record, wh)
		m.lifecycleMu.Unlock()
	}
}

// offerOutbox requires lifecycleMu. Queue pressure never evicts a receipt;
// the feeder can pick it up later once bounded worker capacity is available.
func (m *WebhookManager) offerOutbox(record outboxRecord, wh *WebhookConfig) {
	key := outboxKey(record.ID)
	m.outbox.mu.Lock()
	defer m.outbox.mu.Unlock()
	if m.outbox.active[key] {
		return
	}
	m.outbox.active[key] = true
	m.pending.Add(1)
	select {
	case m.queue <- Delivery{ID: record.ID, Webhook: wh, outboxKey: key}:
	default:
		delete(m.outbox.active, key)
		m.pending.Add(-1)
	}
}

func (m *WebhookManager) releaseOutboxDelivery(d Delivery) {
	m.outbox.mu.Lock()
	delete(m.outbox.active, d.outboxKey)
	m.outbox.mu.Unlock()
	m.completePendingDelivery()
}

func (m *WebhookManager) claimOutboxDelivery(d *Delivery) (*outboxClaim, error) {
	lock, err := m.outbox.lockKey(d.outboxKey)
	if errors.Is(err, errOutboxBusy) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record, err := m.outbox.read(d.outboxKey)
	if errors.Is(err, os.ErrNotExist) {
		_ = lock.Close()
		return nil, nil
	}
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	// A queued delivery may predate Register disabling or replacing its
	// destination. Revalidate the current registry at the dispatch boundary,
	// not just the configuration snapshot the feeder originally observed.
	m.webhooksMu.RLock()
	wh := m.webhooks[record.WebhookID]
	m.webhooksMu.RUnlock()
	if record.Failed || record.NextRetry.After(time.Now()) || !m.mayPersistWebhook(record.Event) ||
		wh == nil || !wh.Enabled || record.Fingerprint != m.outboxFingerprint(wh) || !m.matchesWebhook(wh, record.Event) {
		_ = lock.Close()
		return nil, nil
	}
	claim := &outboxClaim{store: m.outbox, key: d.outboxKey, lock: lock, record: record}
	d.Webhook = wh
	d.ID, d.Event, d.Attempt, d.payload = record.ID, record.Event, record.Attempt, record.Payload
	return claim, nil
}

func (c *outboxClaim) begin(attempt int) error {
	c.record.Attempt = attempt
	return c.store.save(c.key, c.record)
}

func (m *WebhookManager) settleOutbox(c *outboxClaim, d *Delivery, last AttemptLog, deliveryErr error) {
	if deliveryErr == nil {
		if err := c.store.checkRoot(); err != nil {
			slog.Error("webhook delivered but outbox identity changed", "delivery", d.ID, "error", err)
			return
		}
		if err := os.Remove(filepath.Join(c.store.dir, c.key)); err != nil {
			slog.Error("webhook delivered but receipt acknowledgement failed", "delivery", d.ID, "error", err)
			return
		}
		if err := syncOutboxDir(c.store.dir); err != nil {
			slog.Error("webhook acknowledgement durability uncertain", "delivery", d.ID, "error", err)
			return
		}
		m.deliveries.Add(1)
		return
	}
	// Never persist transport errors containing credential-bearing endpoint
	// URLs. Status/attempt/time remain useful without saving secrets.
	last.Error = "delivery failed; transport or request error"
	if last.StatusCode != 0 {
		last.Error = fmt.Sprintf("HTTP status %d", last.StatusCode)
	}
	c.record.LastAttempt = &last
	d.Error = deliveryErr
	// Shutdown cancellation is not a permanent receiver failure. Keep work for
	// a future manager even if this happened at the retry-attempt boundary.
	// Recovery never rewinds the wire attempt number or overwrites the previous
	// failure. Only the configured per-cycle retry budget starts afresh.
	cycle := *d
	if n := len(c.record.Recovery); n > 0 {
		cycle.Attempt -= c.record.Recovery[n-1].AfterAttempt
	}
	retry := m.shouldRetry(&cycle, last.StatusCode, deliveryErr) || (m.ctx.Err() != nil && last.StatusCode == 0)
	// Retain receiver cooldowns even for terminal failures. An explicit retry
	// must not bypass Retry-After merely because the previous budget ran out.
	c.record.NextRetry = m.calculateNextRetry(&cycle)
	if !retry {
		c.record.Failed = true
	}
	if err := c.store.save(c.key, c.record); err != nil {
		slog.Error("webhook result could not be checkpointed; receipt retained", "delivery", d.ID, "error", err)
		return
	}
	if !retry {
		m.addToDeadLetter(*d, last)
		m.failures.Add(1)
		slog.Warn("durable webhook failed; receipt retained for inspection", "delivery", d.ID, "status", last.StatusCode)
	}
}

func (m *WebhookManager) mayPersistWebhook(event Event) bool {
	return !event.Transient && (m.config.PersistenceAllowed == nil || m.config.PersistenceAllowed(event.Session))
}

// Project bridges opt in through NTM_WEBHOOK_OUTBOX_DIR. Always namespace the
// root by canonical project identity, even when an absolute root is shared by
// many projects. A caller running from another cwd must not replay their data.
func projectWebhookOutboxDir(projectDir string) (string, error) {
	root := strings.TrimSpace(os.Getenv("NTM_WEBHOOK_OUTBOX_DIR"))
	if root == "" {
		return "", nil
	}
	project, err := filepath.Abs(projectDir)
	if err != nil {
		return "", err
	}
	project, err = filepath.EvalSymlinks(project)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(project, root)
	}
	identity := sha256.Sum256([]byte(project))
	return filepath.Join(root, hex.EncodeToString(identity[:])), nil
}

func newWebhookEventID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return "evt_" + hex.EncodeToString(id[:]), nil
}
