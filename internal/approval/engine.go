// Package approval provides a unified approval workflow engine for NTM.
// It handles approval requests, SLB (two-person rule) enforcement, notifications,
// and event emission for sensitive operations like force-releasing reservations.
package approval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/notify"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// DefaultExpiry is the default time before an approval request expires.
const DefaultExpiry = 24 * time.Hour

// Config holds configuration for the approval engine.
type Config struct {
	// DefaultExpiry is how long approvals stay pending before expiring.
	DefaultExpiry time.Duration

	// ApproverList is the list of identities allowed to approve requests.
	// If empty, any identity can approve (except the requester for SLB).
	ApproverList []string

	// NotifyOnRequest enables notifications when requests are created.
	NotifyOnRequest bool

	// NotifyOnDecision enables notifications when requests are approved/denied.
	NotifyOnDecision bool

	// EnableSLB routes SLB-required approvals to the slb queue when available.
	EnableSLB bool
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
	return Config{
		DefaultExpiry:    DefaultExpiry,
		ApproverList:     nil, // Anyone can approve
		NotifyOnRequest:  true,
		NotifyOnDecision: true,
		EnableSLB:        true,
	}
}

// Engine manages approval workflows.
type Engine struct {
	store    *state.Store
	notifier *notify.Notifier
	eventBus *events.EventBus
	config   Config
	mu       sync.Mutex

	// waiters maps approval ID to channels waiting for completion
	waiters   map[string][]chan struct{}
	waitersMu sync.Mutex
}

// New creates a new approval engine.
func New(store *state.Store, notifier *notify.Notifier, eventBus *events.EventBus, cfg Config) *Engine {
	if cfg.DefaultExpiry == 0 {
		cfg.DefaultExpiry = DefaultExpiry
	}
	return &Engine{
		store:    store,
		notifier: notifier,
		eventBus: eventBus,
		config:   cfg,
		waiters:  make(map[string][]chan struct{}),
	}
}

// RequestParams contains parameters for creating an approval request.
type RequestParams struct {
	Action        string        // What action needs approval (e.g., "force_release")
	Resource      string        // What resource is being acted on
	Reason        string        // Why approval is needed
	RequestedBy   string        // Who is requesting (agent or user identity)
	CorrelationID string        // Optional correlation ID for tracing
	RequiresSLB   bool          // Whether two-person rule applies
	ExpiresIn     time.Duration // Override default expiry (0 = use default)
}

// Request creates a new approval request.
func (e *Engine) Request(ctx context.Context, params RequestParams) (*state.Approval, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Set expiry
	expiry := params.ExpiresIn
	if expiry == 0 {
		expiry = e.config.DefaultExpiry
	}

	now := time.Now().UTC()
	approval := &state.Approval{
		ID:            generateApprovalID(),
		Action:        params.Action,
		Resource:      params.Resource,
		Reason:        params.Reason,
		RequestedBy:   params.RequestedBy,
		CorrelationID: params.CorrelationID,
		RequiresSLB:   params.RequiresSLB,
		CreatedAt:     now,
		ExpiresAt:     now.Add(expiry),
		Status:        state.ApprovalPending,
	}

	// If SLB is required and available, enqueue an SLB request first.
	if params.RequiresSLB && e.config.EnableSLB {
		slbID, err := e.enqueueSLBRequest(ctx, params)
		if err != nil {
			return nil, err
		}
		if slbID != "" && approval.CorrelationID == "" {
			approval.CorrelationID = "slb:" + slbID
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Store the approval
	if err := e.store.CreateApproval(approval); err != nil {
		return nil, fmt.Errorf("create approval: %w", err)
	}

	// Emit event
	if e.eventBus != nil {
		e.eventBus.Publish(events.BaseEvent{
			Type:      "approval.requested",
			Timestamp: now,
		})
	}

	// Send notification
	if e.notifier != nil && e.config.NotifyOnRequest {
		e.notifyApprovalRequest(approval)
	}

	return approval, nil
}

func (e *Engine) enqueueSLBRequest(ctx context.Context, params RequestParams) (string, error) {
	adapter := tools.NewSLBAdapter()
	if _, installed := adapter.Detect(); !installed {
		return "", nil
	}

	command := buildSLBCommand(params.Action, params.Resource)
	raw, err := adapter.Request(ctx, command, params.Reason)
	if err != nil {
		// Graceful fallback to internal approvals if SLB is unavailable or errors.
		slog.Warn("SLB adapter request failed, falling back to internal approval", "error", err, "command", command)
		return "", nil
	}

	return parseSLBRequestID(raw), nil
}

func buildSLBCommand(action, resource string) string {
	action = strings.TrimSpace(action)
	resource = strings.TrimSpace(resource)
	if action == "" {
		return ""
	}
	if resource == "" {
		return "ntm approval: " + action
	}
	return "ntm approval: " + action + " " + resource
}

func parseSLBRequestID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	if id, ok := payload["id"].(string); ok {
		return id
	}
	return ""
}

// Check returns the current status of an approval request.
func (e *Engine) Check(ctx context.Context, id string) (*state.Approval, error) {
	approval, err := e.store.GetApproval(id)
	if err != nil {
		return nil, fmt.Errorf("get approval: %w", err)
	}
	if approval == nil {
		return nil, fmt.Errorf("approval not found: %s", id)
	}

	// Check if expired
	if approval.Status == state.ApprovalPending && time.Now().After(approval.ExpiresAt) {
		if err := e.expireApproval(approval, time.Now().UTC()); err != nil {
			return nil, err
		}
	}

	return approval, nil
}

// Approve grants an approval request.
func (e *Engine) Approve(ctx context.Context, id string, approverID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	approval, err := e.store.GetApproval(id)
	if err != nil {
		return fmt.Errorf("get approval: %w", err)
	}
	if approval == nil {
		return fmt.Errorf("approval not found: %s", id)
	}

	now := time.Now().UTC()
	if err := e.ensurePendingApproval(approval); err != nil {
		return err
	}
	expired, err := e.expireIfNeeded(approval, now)
	if err != nil {
		return err
	}
	if expired {
		return fmt.Errorf("approval has expired")
	}
	if len(e.config.ApproverList) > 0 && !contains(e.config.ApproverList, approverID) {
		return fmt.Errorf("%s is not an authorized approver", approverID)
	}
	if approval.RequiresSLB && approverID == approval.RequestedBy {
		return fmt.Errorf("SLB violation: approver cannot be the same as requester")
	}

	// Update approval
	approval.Status = state.ApprovalApproved
	approval.ApprovedBy = approverID
	approval.ApprovedAt = &now

	if err := e.store.UpdateApproval(approval); err != nil {
		return fmt.Errorf("update approval: %w", err)
	}

	// Emit event
	if e.eventBus != nil {
		e.eventBus.Publish(events.BaseEvent{
			Type:      "approval.approved",
			Timestamp: now,
		})
	}

	// Notify
	if e.notifier != nil && e.config.NotifyOnDecision {
		e.notifyApprovalDecision(approval, "approved")
	}

	// Wake up waiters
	e.notifyWaiters(id)

	return nil
}

// Deny rejects an approval request.
func (e *Engine) Deny(ctx context.Context, id string, approverID string, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	approval, err := e.store.GetApproval(id)
	if err != nil {
		return fmt.Errorf("get approval: %w", err)
	}
	if approval == nil {
		return fmt.Errorf("approval not found: %s", id)
	}

	now := time.Now().UTC()
	if err := e.ensurePendingApproval(approval); err != nil {
		return err
	}
	expired, err := e.expireIfNeeded(approval, now)
	if err != nil {
		return err
	}
	if expired {
		return fmt.Errorf("approval has expired")
	}

	// Update approval
	approval.Status = state.ApprovalDenied
	approval.ApprovedBy = approverID // Record who denied
	approval.ApprovedAt = &now
	approval.DeniedReason = reason

	if err := e.store.UpdateApproval(approval); err != nil {
		return fmt.Errorf("update approval: %w", err)
	}

	// Emit event
	if e.eventBus != nil {
		e.eventBus.Publish(events.BaseEvent{
			Type:      "approval.denied",
			Timestamp: now,
		})
	}

	// Notify
	if e.notifier != nil && e.config.NotifyOnDecision {
		e.notifyApprovalDecision(approval, "denied")
	}

	// Wake up waiters
	e.notifyWaiters(id)

	return nil
}

// WaitForApproval blocks until the approval is approved, denied, or times out.
func (e *Engine) WaitForApproval(ctx context.Context, id string, timeout time.Duration) (*state.Approval, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	baseCtx := ctx

	// First check current status
	approval, err := e.Check(baseCtx, id)
	if err != nil {
		return nil, err
	}

	// If already decided, return immediately
	if approval.Status != state.ApprovalPending {
		return approval, nil
	}

	// Create a wait channel
	waitCh := make(chan struct{}, 1)
	e.waitersMu.Lock()
	e.waiters[id] = append(e.waiters[id], waitCh)
	e.waitersMu.Unlock()

	// Clean up on exit
	defer func() {
		e.waitersMu.Lock()
		waiters := e.waiters[id]
		for i, ch := range waiters {
			if ch == waitCh {
				e.waiters[id] = append(waiters[:i], waiters[i+1:]...)
				break
			}
		}
		e.waitersMu.Unlock()
	}()

	waitCtx, cancel := context.WithTimeout(baseCtx, timeout)
	defer cancel()

	select {
	case <-waitCh:
		// Approval was decided
		return e.Check(baseCtx, id)
	case <-waitCtx.Done():
		if baseCtx.Err() != nil {
			return nil, baseCtx.Err()
		}
		// Timeout - check final status
		return e.Check(baseCtx, id)
	}
}

// ListPending returns all pending approval requests.
func (e *Engine) ListPending(ctx context.Context) ([]state.Approval, error) {
	approvals, err := e.store.ListPendingApprovals()
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}

	// Filter out expired ones and update them
	var pending []state.Approval
	now := time.Now()
	for _, a := range approvals {
		if a.Status == state.ApprovalPending && now.After(a.ExpiresAt) {
			// Mark as expired (best-effort, continue even if update fails)
			_ = e.expireApproval(&a, now.UTC())
		} else if a.Status == state.ApprovalPending {
			pending = append(pending, a)
		}
	}

	return pending, nil
}

// ExpireStale marks all expired pending approvals as expired.
func (e *Engine) ExpireStale(ctx context.Context) (int, error) {
	// Use ListExpiredPendingApprovals which returns pending approvals where expires_at <= now
	approvals, err := e.store.ListExpiredPendingApprovals()
	if err != nil {
		return 0, fmt.Errorf("list expired pending approvals: %w", err)
	}

	count := 0
	for _, a := range approvals {
		if err := e.expireApproval(&a, time.Now().UTC()); err == nil {
			count++
		}
	}

	return count, nil
}

// notifyWaiters wakes up all goroutines waiting on this approval.
func (e *Engine) notifyWaiters(id string) {
	e.waitersMu.Lock()
	defer e.waitersMu.Unlock()

	for _, ch := range e.waiters[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// notifyApprovalRequest sends a notification when an approval is requested.
func (e *Engine) notifyApprovalRequest(appr *state.Approval) {
	if e.notifier == nil {
		return
	}

	slbNote := ""
	if appr.RequiresSLB {
		slbNote = " [SLB: Two-person rule required]"
	}

	// Notification is best-effort; don't fail the operation if it fails
	_ = e.notifier.Notify(notify.Event{
		Type:    "approval.requested",
		Message: fmt.Sprintf("Approval needed: %s on %s%s", appr.Action, appr.Resource, slbNote),
		Session: appr.CorrelationID,
		Details: map[string]string{
			"approval_id":  appr.ID,
			"action":       appr.Action,
			"resource":     appr.Resource,
			"reason":       appr.Reason,
			"requested_by": appr.RequestedBy,
			"expires_at":   appr.ExpiresAt.Format(time.RFC3339),
		},
	})
}

// notifyApprovalDecision sends a notification when an approval is decided.
func (e *Engine) notifyApprovalDecision(appr *state.Approval, decision string) {
	if e.notifier == nil {
		return
	}

	details := map[string]string{
		"approval_id": appr.ID,
		"action":      appr.Action,
		"resource":    appr.Resource,
		"decision":    decision,
		"decided_by":  appr.ApprovedBy,
	}
	if appr.DeniedReason != "" {
		details["reason"] = appr.DeniedReason
	}

	// Notification is best-effort; don't fail the operation if it fails
	_ = e.notifier.Notify(notify.Event{
		Type:    notify.EventType(fmt.Sprintf("approval.%s", decision)),
		Message: fmt.Sprintf("Approval %s: %s on %s", decision, appr.Action, appr.Resource),
		Session: appr.CorrelationID,
		Details: details,
	})
}

func (e *Engine) ensurePendingApproval(approval *state.Approval) error {
	if approval.Status != state.ApprovalPending {
		return fmt.Errorf("approval is not pending (status: %s)", approval.Status)
	}
	return nil
}

func (e *Engine) expireIfNeeded(approval *state.Approval, now time.Time) (bool, error) {
	if !now.After(approval.ExpiresAt) {
		return false, nil
	}
	if err := e.expireApproval(approval, now); err != nil {
		return false, err
	}
	return true, nil
}

func (e *Engine) expireApproval(approval *state.Approval, now time.Time) error {
	approval.Status = state.ApprovalExpired
	if err := e.store.UpdateApproval(approval); err != nil {
		return fmt.Errorf("update expired approval: %w", err)
	}
	if e.eventBus != nil {
		e.eventBus.Publish(events.BaseEvent{
			Type:      "approval.expired",
			Timestamp: now,
		})
	}
	e.notifyWaiters(approval.ID)
	return nil
}

// contains checks if a string is in a slice.
func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// generateApprovalID creates a unique approval ID.
func generateApprovalID() string {
	timestamp := time.Now().Format("20060102-150405")
	randBytes := make([]byte, 8)
	if _, err := rand.Read(randBytes); err != nil {
		// Fallback to timestamp-based if crypto/rand fails
		return fmt.Sprintf("appr-%s-%x", timestamp, time.Now().UnixNano()%0xffffffff)
	}
	return fmt.Sprintf("appr-%s-%s", timestamp, hex.EncodeToString(randBytes))
}
