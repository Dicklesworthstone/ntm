// Package webhook provides a webhook management system for NTM events.
// It supports async dispatch, retry with exponential backoff, event queueing,
// and optional HMAC signing for secure delivery.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// Default configuration values.
// Retry defaults align with config.DefaultRetryConfig().Webhook.
const (
	DefaultQueueSize       = 1000
	DefaultWorkerCount     = 10
	DefaultMaxRetries      = 5 // Canonical default: config.RetryConfig.Webhook.MaxAttempts
	DefaultTimeout         = 10 * time.Second
	DefaultBaseBackoff     = 1 * time.Second
	DefaultMaxBackoff      = 30 * time.Second
	DefaultDeadLetterLimit = 100
)

// Event represents a webhook event to be dispatched
type Event struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	Session   string            `json:"session,omitempty"`
	Pane      string            `json:"pane,omitempty"`
	Agent     string            `json:"agent,omitempty"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty"`
}

// WebhookConfig holds configuration for a single webhook endpoint
type WebhookConfig struct {
	ID       string `toml:"id" json:"id"`
	Name     string `toml:"name" json:"name"`
	URL      string `toml:"url" json:"url"`
	Method   string `toml:"method" json:"method"`     // HTTP method (default POST)
	Template string `toml:"template" json:"template"` // Go template for payload
	// Format selects a built-in JSON payload formatter when Template is empty.
	// Supported: json, slack, discord, teams.
	Format  string            `toml:"format" json:"format"`
	Headers map[string]string `toml:"headers" json:"headers"`
	Events  []string          `toml:"events" json:"events"` // Event types to receive (empty = all)
	Enabled bool              `toml:"enabled" json:"enabled"`

	// Per-webhook timeout (overrides default)
	Timeout time.Duration `toml:"timeout" json:"timeout,omitempty"`

	// Retry configuration
	Retry RetryConfig `toml:"retry" json:"retry"`

	// HMAC signing configuration
	Secret string `toml:"secret" json:"secret,omitempty"` // HMAC-SHA256 secret
}

// RetryConfig holds retry policy for a webhook
type RetryConfig struct {
	Enabled    bool          `toml:"enabled" json:"enabled"`
	MaxRetries int           `toml:"max_retries" json:"max_retries"`
	BaseDelay  time.Duration `toml:"base_delay" json:"base_delay,omitempty"`
	MaxDelay   time.Duration `toml:"max_delay" json:"max_delay,omitempty"`
}

// DefaultRetryConfig returns the default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		Enabled:    true,
		MaxRetries: DefaultMaxRetries,
		BaseDelay:  DefaultBaseBackoff,
		MaxDelay:   DefaultMaxBackoff,
	}
}

// Delivery represents a pending webhook delivery
type Delivery struct {
	ID        string
	Event     Event
	Webhook   *WebhookConfig
	Attempt   int
	NextRetry time.Time
	Error     error
}

// DeadLetter represents a failed delivery that exhausted retries
type DeadLetter struct {
	Delivery   Delivery
	FailedAt   time.Time
	LastError  string
	AttemptLog []AttemptLog
}

// AttemptLog records a single delivery attempt
type AttemptLog struct {
	Attempt    int
	Timestamp  time.Time
	StatusCode int
	Error      string
	Duration   time.Duration
}

// ManagerConfig holds configuration for the WebhookManager
type ManagerConfig struct {
	QueueSize       int           `toml:"queue_size" json:"queue_size"`
	WorkerCount     int           `toml:"worker_count" json:"worker_count"`
	DefaultTimeout  time.Duration `toml:"default_timeout" json:"default_timeout,omitempty"`
	DeadLetterLimit int           `toml:"dead_letter_limit" json:"dead_letter_limit"`
}

// DefaultManagerConfig returns the default manager configuration
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		QueueSize:       DefaultQueueSize,
		WorkerCount:     DefaultWorkerCount,
		DefaultTimeout:  DefaultTimeout,
		DeadLetterLimit: DefaultDeadLetterLimit,
	}
}

type managerContext struct {
	done     chan struct{}
	canceled atomic.Bool
	once     sync.Once
}

func newManagerContext() (*managerContext, context.CancelFunc) {
	ctx := &managerContext{done: make(chan struct{})}
	cancel := func() {
		ctx.once.Do(func() {
			ctx.canceled.Store(true)
			close(ctx.done)
		})
	}
	return ctx, cancel
}

func (c *managerContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *managerContext) Done() <-chan struct{}       { return c.done }
func (c *managerContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}
func (c *managerContext) Value(key any) any { return nil }

// WebhookManager manages webhook registration and event dispatch
type WebhookManager struct {
	config ManagerConfig

	// Registered webhooks
	webhooks   map[string]*WebhookConfig
	webhooksMu sync.RWMutex

	// Event queue
	queue     chan Delivery
	queueFull atomic.Int64 // Counter for dropped events

	// Retry queue (priority by next retry time)
	retryQueue   []Delivery
	retryQueueMu sync.Mutex
	retryCond    *sync.Cond

	// Dead letter queue
	deadLetters   []DeadLetter
	deadLettersMu sync.Mutex

	// HTTP client with connection pooling
	httpClient *http.Client

	// Lifecycle management
	lifecycleMu sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	stopDone    chan struct{}
	started     atomic.Bool
	stopping    atomic.Bool
	pending     atomic.Int64
	deliveries  atomic.Int64 // Total successful deliveries
	failures    atomic.Int64 // Total failed deliveries

	// Logging callback (optional)
	Logger func(format string, args ...interface{})

	redactionCfg *redaction.Config
}

// NewManager creates a new WebhookManager with the given configuration
func NewManager(cfg ManagerConfig) *WebhookManager {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = DefaultQueueSize
	}
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = DefaultWorkerCount
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = DefaultTimeout
	}
	if cfg.DeadLetterLimit <= 0 {
		cfg.DeadLetterLimit = DefaultDeadLetterLimit
	}

	m := &WebhookManager{
		config:      cfg,
		webhooks:    make(map[string]*WebhookConfig),
		queue:       make(chan Delivery, cfg.QueueSize),
		retryQueue:  make([]Delivery, 0, 100),
		deadLetters: make([]DeadLetter, 0, cfg.DeadLetterLimit),
		httpClient: &http.Client{
			Timeout: cfg.DefaultTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
	m.retryCond = sync.NewCond(&m.retryQueueMu)

	return m
}

// NewManagerWithRedaction creates a webhook manager that redacts event content
// (message + details) before serialization/dispatch.
func NewManagerWithRedaction(cfg ManagerConfig, redactionCfg redaction.Config) *WebhookManager {
	m := NewManager(cfg)
	cfgCopy := redactionCfg
	m.redactionCfg = &cfgCopy
	return m
}

// Register adds a webhook configuration to the manager
func (m *WebhookManager) Register(cfg WebhookConfig) error {
	if cfg.URL == "" {
		return errors.New("webhook URL is required")
	}
	if cfg.ID == "" {
		cfg.ID = fmt.Sprintf("webhook_%d", time.Now().UnixNano())
	}
	if cfg.Method == "" {
		cfg.Method = "POST"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = m.config.DefaultTimeout
	}
	if cfg.Retry.MaxRetries <= 0 && cfg.Retry.Enabled {
		cfg.Retry = DefaultRetryConfig()
	}
	if cfg.Retry.BaseDelay <= 0 {
		cfg.Retry.BaseDelay = DefaultBaseBackoff
	}
	if cfg.Retry.MaxDelay <= 0 {
		cfg.Retry.MaxDelay = DefaultMaxBackoff
	}
	if cfg.Template == "" && cfg.Format != "" {
		if _, err := buildBuiltInPayload(Event{}, cfg.Format); err != nil {
			return err
		}
	}
	if strings.TrimSpace(cfg.Template) != "" {
		if _, err := parseWebhookTemplate(cfg.Template); err != nil {
			return err
		}
	}

	m.webhooksMu.Lock()
	defer m.webhooksMu.Unlock()

	m.webhooks[cfg.ID] = &cfg
	return nil
}

// Unregister removes a webhook by ID
func (m *WebhookManager) Unregister(id string) error {
	m.webhooksMu.Lock()
	defer m.webhooksMu.Unlock()

	if _, ok := m.webhooks[id]; !ok {
		return fmt.Errorf("webhook %q not found", id)
	}
	delete(m.webhooks, id)
	return nil
}

// Dispatch queues an event for delivery to all matching webhooks
func (m *WebhookManager) Dispatch(event Event) error {
	if !m.started.Load() {
		return errors.New("webhook manager not started")
	}

	if event.ID == "" {
		event.ID = fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	event = m.sanitizeEvent(event)

	m.webhooksMu.RLock()
	webhooks := make([]*WebhookConfig, 0, len(m.webhooks))
	for _, wh := range m.webhooks {
		if wh.Enabled && m.matchesEvent(wh, event.Type) {
			webhooks = append(webhooks, wh)
		}
	}
	m.webhooksMu.RUnlock()

	if len(webhooks) == 0 {
		return nil // No webhooks registered for this event type
	}

	// Queue delivery for each matching webhook
	for _, wh := range webhooks {
		delivery := Delivery{
			ID:      fmt.Sprintf("%s_%s", event.ID, wh.ID),
			Event:   event,
			Webhook: wh,
			Attempt: 0,
		}
		m.pending.Add(1)

		// Non-blocking send to queue
		select {
		case m.queue <- delivery:
			// Successfully queued
		default:
			// Queue full. Drop the oldest delivery so we keep the newest.
			select {
			case dropped := <-m.queue:
				// Count actual dropped deliveries (oldest).
				m.queueFull.Add(1)
				m.completePendingDelivery()
				m.log("webhook queue full, dropping oldest delivery %s", dropped.ID)
			default:
				// Race: another worker/drainer freed space before we could drop.
			}
			select {
			case m.queue <- delivery:
			default:
				// Queue still full. Drop the new delivery too.
				m.queueFull.Add(1)
				m.completePendingDelivery()
				m.log("webhook queue full, dropping event %s for webhook %s", event.ID, wh.ID)
			}
		}
	}

	return nil
}

func (m *WebhookManager) sanitizeEvent(event Event) Event {
	if m.redactionCfg == nil || m.redactionCfg.Mode == redaction.ModeOff {
		return event
	}

	cfg := *m.redactionCfg
	// Force redaction mode since we're past the ModeOff early return
	cfg.Mode = redaction.ModeRedact

	out := event
	if out.Message != "" {
		out.Message = redaction.ScanAndRedact(out.Message, cfg).Output
	}
	if len(out.Details) > 0 {
		redactedDetails := make(map[string]string, len(out.Details))
		for k, v := range out.Details {
			if v == "" {
				redactedDetails[k] = v
				continue
			}
			redactedDetails[k] = redaction.ScanAndRedact(v, cfg).Output
		}
		out.Details = redactedDetails
	}
	return out
}

// Start begins background processing of the webhook queue
func (m *WebhookManager) Start() error {
	for {
		m.lifecycleMu.Lock()
		if stopDone := m.stopDone; stopDone != nil {
			select {
			case <-stopDone:
				if m.stopDone == stopDone {
					m.stopDone = nil
					m.ctx = nil
					m.cancel = nil
				}
			default:
				m.lifecycleMu.Unlock()
				<-stopDone
				continue
			}
		}
		if m.started.Load() {
			m.lifecycleMu.Unlock()
			return errors.New("webhook manager already started")
		}

		m.ctx, m.cancel = newManagerContext()
		m.stopping.Store(false)
		m.started.Store(true)

		// Start worker goroutines
		for i := 0; i < m.config.WorkerCount; i++ {
			m.wg.Add(1)
			go m.worker(i)
		}

		// Start retry processor
		m.wg.Add(1)
		go m.retryProcessor()
		m.lifecycleMu.Unlock()

		m.log("webhook manager started with %d workers", m.config.WorkerCount)
		return nil
	}
}

// Stop gracefully shuts down the manager
func (m *WebhookManager) Stop() error {
	m.lifecycleMu.Lock()
	if stopDone := m.stopDone; stopDone != nil && !m.started.Load() {
		m.lifecycleMu.Unlock()
		<-stopDone
		return nil
	}
	if !m.started.Load() {
		m.lifecycleMu.Unlock()
		return nil
	}

	m.started.Store(false)
	m.stopping.Store(true)
	cancel := m.cancel
	done := make(chan struct{})
	m.stopDone = done
	go func(done chan struct{}) {
		m.wg.Wait()
		close(done)
		m.lifecycleMu.Lock()
		if m.stopDone == done {
			m.stopDone = nil
			m.ctx = nil
			m.cancel = nil
		}
		m.lifecycleMu.Unlock()
	}(done)
	m.lifecycleMu.Unlock()

	m.log("stopping webhook manager...")

	// Retries that have not started yet cannot outlive shutdown. Convert them to
	// dead letters now so Stop only has to wait for queued or in-flight work.
	m.abandonPendingRetries()

	deadline := time.Now().Add(10 * time.Second)
	for m.pending.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// Cancel context to stop idle workers and in-flight retry waits.
	if cancel != nil {
		cancel()
	}

	// Signal retry processor to wake up and exit.
	m.retryCond.Broadcast()

	waitTimeout := time.Until(deadline)
	if waitTimeout < 0 {
		waitTimeout = 0
	}

	select {
	case <-done:
		m.log("webhook manager stopped gracefully")
		return nil
	case <-time.After(waitTimeout):
		m.log("webhook manager stop timed out")
		return errors.New("webhook manager stop timed out")
	}
}

// Stats returns current manager statistics
type Stats struct {
	QueueLength     int   `json:"queue_length"`
	QueueCapacity   int   `json:"queue_capacity"`
	RetryQueueLen   int   `json:"retry_queue_length"`
	DeadLetterCount int   `json:"dead_letter_count"`
	Deliveries      int64 `json:"total_deliveries"`
	Failures        int64 `json:"total_failures"`
	DroppedEvents   int64 `json:"dropped_events"`
	WebhookCount    int   `json:"webhook_count"`
}

// Stats returns current manager statistics
func (m *WebhookManager) Stats() Stats {
	m.webhooksMu.RLock()
	webhookCount := len(m.webhooks)
	m.webhooksMu.RUnlock()

	m.retryQueueMu.Lock()
	retryLen := len(m.retryQueue)
	m.retryQueueMu.Unlock()

	m.deadLettersMu.Lock()
	deadLetterCount := len(m.deadLetters)
	m.deadLettersMu.Unlock()

	return Stats{
		QueueLength:     len(m.queue),
		QueueCapacity:   cap(m.queue),
		RetryQueueLen:   retryLen,
		DeadLetterCount: deadLetterCount,
		Deliveries:      m.deliveries.Load(),
		Failures:        m.failures.Load(),
		DroppedEvents:   m.queueFull.Load(),
		WebhookCount:    webhookCount,
	}
}

// DeadLetters returns a copy of the dead letter queue
func (m *WebhookManager) DeadLetters() []DeadLetter {
	m.deadLettersMu.Lock()
	defer m.deadLettersMu.Unlock()

	result := make([]DeadLetter, len(m.deadLetters))
	copy(result, m.deadLetters)
	return result
}

// ClearDeadLetters removes all entries from the dead letter queue
func (m *WebhookManager) ClearDeadLetters() int {
	m.deadLettersMu.Lock()
	defer m.deadLettersMu.Unlock()

	count := len(m.deadLetters)
	m.deadLetters = m.deadLetters[:0]
	return count
}

// worker processes deliveries from the queue
func (m *WebhookManager) worker(id int) {
	defer m.wg.Done()

	for {
		select {
		case <-m.ctx.Done():
			return
		case delivery, ok := <-m.queue:
			if !ok {
				return
			}
			m.processDelivery(&delivery)
		}
	}
}

// retryProcessor handles retrying failed deliveries
func (m *WebhookManager) retryProcessor() {
	defer m.wg.Done()

	for {
		m.retryQueueMu.Lock()

		// Wait until there's something to retry or we're shutting down
		for len(m.retryQueue) == 0 && m.ctx.Err() == nil {
			m.retryCond.Wait()
		}

		// Check for shutdown
		if m.ctx.Err() != nil {
			m.retryQueueMu.Unlock()
			return
		}

		// Find deliveries ready for retry
		now := time.Now()
		var ready []Delivery
		var notReady []Delivery
		var nextRetry time.Time

		for _, d := range m.retryQueue {
			if d.NextRetry.Before(now) || d.NextRetry.Equal(now) {
				ready = append(ready, d)
			} else {
				notReady = append(notReady, d)
				if nextRetry.IsZero() || d.NextRetry.Before(nextRetry) {
					nextRetry = d.NextRetry
				}
			}
		}

		m.retryQueue = notReady
		m.retryQueueMu.Unlock()

		// Process ready deliveries
		for i := range ready {
			delivery := ready[i]
			m.processDelivery(&delivery)
		}

		// If we processed items, check again immediately as new items might have been added
		if len(ready) > 0 {
			continue
		}

		// Calculate sleep duration
		sleepDuration := 1 * time.Second // Default cap
		if !nextRetry.IsZero() {
			until := time.Until(nextRetry)
			if until < sleepDuration {
				sleepDuration = until
			}
		}
		if sleepDuration < 5*time.Millisecond {
			sleepDuration = 5 * time.Millisecond
		}

		// Sleep until next retry or shutdown
		timer := time.NewTimer(sleepDuration)
		select {
		case <-m.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// processDelivery attempts to deliver a webhook
func (m *WebhookManager) processDelivery(d *Delivery) {
	d.Attempt++
	start := time.Now()

	statusCode, err := m.send(d)
	duration := time.Since(start)

	attemptLog := AttemptLog{
		Attempt:    d.Attempt,
		Timestamp:  start,
		StatusCode: statusCode,
		Duration:   duration,
	}
	if err != nil {
		attemptLog.Error = err.Error()
	}

	if err == nil {
		// Success
		m.deliveries.Add(1)
		m.completePendingDelivery()
		m.log("webhook %s delivered successfully (attempt %d, %v)", d.ID, d.Attempt, duration)
		return
	}

	// Check if we should retry
	shouldRetry := m.shouldRetry(d, statusCode, err)
	if !shouldRetry || m.stopping.Load() {
		// Move to dead letter queue
		m.addToDeadLetter(*d, attemptLog)
		m.failures.Add(1)
		m.completePendingDelivery()
		m.log("webhook %s failed permanently: %v", d.ID, err)
		return
	}

	// Schedule retry
	d.Error = err
	d.NextRetry = m.calculateNextRetry(d)
	m.scheduleRetry(*d)
	m.log("webhook %s failed (attempt %d), retrying at %s: %v",
		d.ID, d.Attempt, d.NextRetry.Format(time.RFC3339), err)
}

// send performs the actual HTTP request
func (m *WebhookManager) send(d *Delivery) (int, error) {
	// Build payload from template
	payload, err := m.buildPayload(d)
	if err != nil {
		return 0, fmt.Errorf("failed to build payload: %w", err)
	}

	// Create request with timeout context
	ctx, cancel := context.WithTimeout(m.ctx, d.Webhook.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, d.Webhook.Method, d.Webhook.URL, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "NTM-Webhook/1.0")
	req.Header.Set("X-NTM-Delivery-ID", d.ID)
	req.Header.Set("X-NTM-Event-Type", d.Event.Type)
	req.Header.Set("X-NTM-Attempt", fmt.Sprintf("%d", d.Attempt))

	for k, v := range d.Webhook.Headers {
		req.Header.Set(k, v)
	}

	// Add HMAC signature if secret is configured
	if d.Webhook.Secret != "" {
		sig := m.sign(payload, d.Webhook.Secret)
		req.Header.Set("X-NTM-Signature", "sha256="+sig)
	}

	// Send request
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body (limited to prevent memory issues)
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		m.log("failed to read response body: %v", readErr)
	}

	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("webhook returned %d: %s", resp.StatusCode, string(body))
	}

	return resp.StatusCode, nil
}

// buildPayload constructs the webhook payload from the template
func (m *WebhookManager) buildPayload(d *Delivery) ([]byte, error) {
	tmplStr := d.Webhook.Template
	if tmplStr == "" {
		if d.Webhook.Format != "" {
			return buildBuiltInPayload(d.Event, d.Webhook.Format)
		}
		return json.Marshal(d.Event)
	}

	tmpl, err := parseWebhookTemplate(tmplStr)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d.Event); err != nil {
		return nil, fmt.Errorf("template execution failed: %w", err)
	}

	return buf.Bytes(), nil
}

// sign creates an HMAC-SHA256 signature
func (m *WebhookManager) sign(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// shouldRetry determines if a delivery should be retried
func (m *WebhookManager) shouldRetry(d *Delivery, statusCode int, err error) bool {
	if !d.Webhook.Retry.Enabled {
		return false
	}
	if d.Attempt >= d.Webhook.Retry.MaxRetries {
		return false
	}

	// Don't retry 4xx errors (client errors) except rate limiting
	if statusCode >= 400 && statusCode < 500 && statusCode != 429 {
		return false
	}

	// Retry on 5xx, 429 (rate limit), and connection errors
	return true
}

// calculateNextRetry determines when to retry with exponential backoff
func (m *WebhookManager) calculateNextRetry(d *Delivery) time.Time {
	delay := d.Webhook.Retry.BaseDelay
	for i := 1; i < d.Attempt; i++ {
		delay *= 2
		if delay > d.Webhook.Retry.MaxDelay {
			delay = d.Webhook.Retry.MaxDelay
			break
		}
	}
	return time.Now().Add(delay)
}

// scheduleRetry adds a delivery to the retry queue
func (m *WebhookManager) scheduleRetry(d Delivery) {
	m.retryQueueMu.Lock()
	m.retryQueue = append(m.retryQueue, d)
	m.retryQueueMu.Unlock()
	m.retryCond.Signal()
}

func webhookTemplateFuncMap() template.FuncMap {
	return template.FuncMap{
		"jsonEscape": jsonEscape,
		"json":       func(v interface{}) string { b, _ := json.Marshal(v); return string(b) },
	}
}

func parseWebhookTemplate(tmplStr string) (*template.Template, error) {
	tmpl, err := template.New("webhook").Funcs(webhookTemplateFuncMap()).Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("invalid template: %w", err)
	}
	return tmpl, nil
}

func (m *WebhookManager) completePendingDelivery() {
	if remaining := m.pending.Add(-1); remaining < 0 {
		m.pending.Store(0)
		m.log("webhook pending delivery count went negative")
	}
}

func (m *WebhookManager) abandonPendingRetries() {
	m.retryQueueMu.Lock()
	pendingRetries := m.retryQueue
	m.retryQueue = nil
	m.retryQueueMu.Unlock()

	for _, delivery := range pendingRetries {
		lastError := "manager stopping before retry"
		if delivery.Error != nil {
			lastError = fmt.Sprintf("manager stopping before retry: %v", delivery.Error)
		}
		m.addToDeadLetter(delivery, AttemptLog{
			Attempt:   delivery.Attempt,
			Timestamp: time.Now().UTC(),
			Error:     lastError,
		})
		m.failures.Add(1)
		m.completePendingDelivery()
	}
}

// addToDeadLetter adds a failed delivery to the dead letter queue
func (m *WebhookManager) addToDeadLetter(d Delivery, lastAttempt AttemptLog) {
	m.deadLettersMu.Lock()
	defer m.deadLettersMu.Unlock()

	dl := DeadLetter{
		Delivery:   d,
		FailedAt:   time.Now().UTC(),
		LastError:  lastAttempt.Error,
		AttemptLog: []AttemptLog{lastAttempt},
	}

	// Enforce limit by removing oldest entries
	for len(m.deadLetters) >= m.config.DeadLetterLimit {
		m.deadLetters = m.deadLetters[1:]
	}

	m.deadLetters = append(m.deadLetters, dl)
}

// matchesEvent checks if a webhook is subscribed to an event type
func (m *WebhookManager) matchesEvent(wh *WebhookConfig, eventType string) bool {
	if len(wh.Events) == 0 {
		return true // Subscribe to all events
	}
	eventType = strings.ToLower(strings.TrimSpace(eventType))
	if eventType == "" {
		return false
	}
	for _, e := range wh.Events {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == eventType || e == "*" {
			return true
		}
		// Prefix wildcard match: "agent.*" matches "agent.started", etc.
		if strings.HasSuffix(e, ".*") {
			prefix := strings.TrimSuffix(e, "*")
			if strings.HasPrefix(eventType, prefix) {
				return true
			}
		}
	}
	return false
}

// log writes a log message if a logger is configured
func (m *WebhookManager) log(format string, args ...interface{}) {
	if m.Logger != nil {
		m.Logger(format, args...)
	}
}

// jsonEscape escapes a string for safe embedding in JSON.
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	// json.Marshal wraps in quotes, remove them for template use
	return string(b[1 : len(b)-1])
}
