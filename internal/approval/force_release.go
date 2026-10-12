package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// ErrForceReleaseScope rejects approval IDs belonging to another operation.
var ErrForceReleaseScope = errors.New("approval is not bound to this force-release operation")

// ForceReleaseOperation binds an approval to the complete release request.
// ProjectKey must already be the canonical Agent Mail project key.
// RequestedBy comes from the authenticated principal, never the request body.
type ForceReleaseOperation struct {
	ProjectKey     string `json:"project_key"`
	ReservationID  int    `json:"reservation_id"`
	RequestedBy    string `json:"requested_by"`
	AgentName      string `json:"agent_name"`
	Note           string `json:"note"`
	NotifyPrevious bool   `json:"notify_previous"`
}

// CorrelationKey deliberately versions the old, actor-independent CLI key.
// Existing approvals under the old key cannot authorize a new operation.
func (op ForceReleaseOperation) CorrelationKey() string {
	data, _ := json.Marshal(op) // This fixed struct contains no fallible values.
	digest := sha256.Sum256(data)
	return "force_release:v2:" + hex.EncodeToString(digest[:])
}

// ForceReleaseLease records the reservation the operator reviewed. Expiry is
// useful review evidence, but a renewal does not create a different lease.
type ForceReleaseLease struct {
	Holder    string `json:"holder"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	ProjectID int    `json:"project_id"`
	Exclusive bool   `json:"exclusive"`
}

func (lease ForceReleaseLease) sameReservation(other ForceReleaseLease) bool {
	return lease.Holder == other.Holder && lease.Path == other.Path &&
		lease.CreatedAt == other.CreatedAt && lease.ProjectID == other.ProjectID &&
		lease.Exclusive == other.Exclusive
}

// ForceReleaseLeaseLoader reads the current reservation without modifying it.
// The release API accepts only project + numeric ID, so its own inactivity
// check remains authoritative after this last observation.
type ForceReleaseLeaseLoader func(context.Context) (ForceReleaseLease, error)

type forceReleaseResource struct {
	Operation ForceReleaseOperation `json:"operation"`
	Lease     ForceReleaseLease     `json:"lease"`
}

// ForceReleaseDecision describes admission, including refused and uncertain
// outcomes. A consumed approval is permission for one attempt, not proof that
// the external release succeeded.
type ForceReleaseDecision struct {
	Allowed        bool
	ApprovalID     string
	ApprovalStatus string
	OperationKey   string
	Created        bool
	Message        string
}

// EvaluateForceRelease is the shared CLI/HTTP policy and approval gate.
// newApproval names one terminal predecessor; its deterministic successor is
// reused on retries. It never silently creates another request after a denial,
// expiry, consumption, or an uncertain external result.
func (e *Engine) EvaluateForceRelease(ctx context.Context, mode string, op ForceReleaseOperation, newApproval string, loadLease ForceReleaseLeaseLoader) (ForceReleaseDecision, error) {
	decision := ForceReleaseDecision{OperationKey: op.CorrelationKey()}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return decision, err
	}
	switch mode {
	case "never":
		decision.ApprovalStatus = "policy_never"
		decision.Message = "force-release is disabled by policy (automation.force_release=never)"
		return decision, nil
	case "auto":
		decision.Allowed = true
		decision.ApprovalStatus = "auto"
		decision.Message = "policy allows force-release without approval (automation.force_release=auto)"
		return decision, nil
	}
	if e == nil || e.store == nil {
		return decision, fmt.Errorf("approval store unavailable")
	}
	if strings.TrimSpace(op.ProjectKey) == "" || op.ReservationID < 1 ||
		strings.TrimSpace(op.RequestedBy) == "" || strings.TrimSpace(op.AgentName) == "" {
		return decision, fmt.Errorf("force-release requires a project, positive reservation ID, requester, and Agent Mail actor")
	}
	if loadLease == nil {
		return decision, fmt.Errorf("reservation inspection unavailable")
	}

	if newApproval != "" {
		parent, err := e.Check(ctx, newApproval)
		if err != nil {
			return decision, fmt.Errorf("inspect previous approval: %w", err)
		}
		resource, err := forceReleaseRecordResource(parent, op)
		if err != nil {
			return decision, err
		}
		terminal := parent.Status == state.ApprovalConsumed || parent.Status == state.ApprovalDenied ||
			parent.Status == state.ApprovalExpired || !parent.ExpiresAt.After(time.Now())
		if !terminal && parent.Status == state.ApprovalApproved {
			lease, err := loadLease(ctx)
			if err != nil {
				return decision, fmt.Errorf("inspect reservation before reauthorization: %w", err)
			}
			// An explicit new request may replace an approval for a lease
			// whose identity changed. It cannot bypass a still-valid grant.
			terminal = !resource.Lease.sameReservation(lease)
		}
		if !terminal {
			decision = forceReleaseRefusal(parent, decision.OperationKey, false)
			decision.Message = "approval " + parent.ID + " is still " + string(parent.Status) +
				"; retry the original operation without --new-approval / new_approval"
			return decision, nil
		}
		decision.OperationKey += "/after/" + parent.ID
	}

	digest := sha256.Sum256([]byte(decision.OperationKey))
	id := "apr-fr-" + hex.EncodeToString(digest[:])
	record, err := e.store.GetApproval(id)
	if err != nil {
		return decision, fmt.Errorf("look up force-release approval: %w", err)
	}
	if record == nil {
		lease, err := loadLease(ctx)
		if err != nil {
			return decision, fmt.Errorf("inspect reservation for approval: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return decision, err
		}
		resource, err := json.Marshal(forceReleaseResource{Operation: op, Lease: lease})
		if err != nil {
			return decision, err
		}
		reason := op.Note
		if reason == "" {
			reason = "force-release requested for an inactive reservation holder"
		}
		record, decision.Created, err = e.requestWithID(ctx, RequestParams{
			Action: "force_release", Resource: string(resource), Reason: reason,
			RequestedBy: op.RequestedBy, CorrelationID: decision.OperationKey, RequiresSLB: true,
		}, id, true)
		if err != nil {
			return decision, fmt.Errorf("create force-release approval: %w", err)
		}
	}
	if record.CorrelationID != decision.OperationKey {
		return decision, fmt.Errorf("force-release approval correlation mismatch")
	}
	resource, err := forceReleaseRecordResource(record, op)
	if err != nil {
		return decision, err
	}
	record, err = e.Check(ctx, record.ID)
	if err != nil {
		return decision, err
	}
	decision = forceReleaseRefusal(record, decision.OperationKey, decision.Created)
	if record.Status != state.ApprovalApproved || decision.ApprovalStatus == "expired" {
		return decision, nil
	}

	lease, err := loadLease(ctx)
	if err != nil {
		return decision, fmt.Errorf("inspect approved reservation: %w", err)
	}
	if !resource.Lease.sameReservation(lease) {
		decision.ApprovalStatus = "target_changed"
		decision.Message = "reservation identity changed after approval; inspect its current holder and request a new approval with --new-approval " + record.ID + " / new_approval"
		return decision, nil
	}
	if err := ctx.Err(); err != nil {
		return decision, err
	}
	if err := e.Consume(ctx, record.ID, op.RequestedBy); err != nil {
		// Another process may have won admission. Return its terminal state
		// without minting a successor or dispatching a second release.
		current, readErr := e.store.GetApproval(record.ID)
		if readErr == nil && current != nil &&
			(current.Status == state.ApprovalConsumed || !current.ExpiresAt.After(time.Now())) {
			return forceReleaseRefusal(current, decision.OperationKey, decision.Created), nil
		}
		decision.ApprovalStatus = "consumption_unknown"
		decision.Message = "approval consumption could not be confirmed; inspect approval and reservation state before retrying"
		return decision, fmt.Errorf("consume force-release approval: %w", err)
	}
	decision.Allowed = true
	decision.ApprovalStatus = string(state.ApprovalConsumed)
	decision.Message = "approval " + record.ID + " consumed for this force-release attempt"
	return decision, nil
}

func forceReleaseRecordResource(record *state.Approval, op ForceReleaseOperation) (forceReleaseResource, error) {
	var resource forceReleaseResource
	if record == nil || record.Action != "force_release" || record.RequestedBy != op.RequestedBy ||
		!record.RequiresSLB || (record.CorrelationID != op.CorrelationKey() &&
		!strings.HasPrefix(record.CorrelationID, op.CorrelationKey()+"/after/")) {
		return resource, fmt.Errorf("%w and requester", ErrForceReleaseScope)
	}
	if err := json.Unmarshal([]byte(record.Resource), &resource); err != nil || resource.Operation != op {
		return resource, fmt.Errorf("%w: resource mismatch", ErrForceReleaseScope)
	}
	return resource, nil
}

func forceReleaseRefusal(record *state.Approval, key string, created bool) ForceReleaseDecision {
	decision := ForceReleaseDecision{
		ApprovalID: record.ID, ApprovalStatus: string(record.Status), OperationKey: key, Created: created,
	}
	switch record.Status {
	case state.ApprovalConsumed:
		decision.Message = "approval " + record.ID + " was already consumed; inspect the reservation to determine whether the previous release succeeded"
	case state.ApprovalDenied:
		decision.Message = "approval " + record.ID + " was denied"
		if record.DeniedReason != "" {
			decision.Message += ": " + record.DeniedReason
		}
	case state.ApprovalPending, state.ApprovalApproved:
		if !record.ExpiresAt.After(time.Now()) {
			decision.ApprovalStatus = "expired"
			decision.Message = "approval " + record.ID + " has expired"
		} else {
			decision.Message = "force-release requires approval: have a second operator run " +
				"ntm approve " + record.ID + " or approve it through /api/v1/approvals/" + record.ID + "/approve"
			return decision
		}
	case state.ApprovalExpired:
		decision.Message = "approval " + record.ID + " has expired"
	default:
		decision.Message = "approval " + record.ID + " has an unsupported state"
		return decision
	}
	decision.Message += "; a deliberate new attempt requires --new-approval " + record.ID + " / new_approval"
	return decision
}
