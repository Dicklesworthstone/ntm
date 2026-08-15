package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/approval"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// This file implements the approval gate for `ntm locks force-release`
// (bd-2y2on). Before this gate existed, runForceRelease went straight from
// scope resolution to Agent Mail plumbing: the policy knobs
// (automation.force_release, the SLB-flagged force_release approval rule)
// were display-only and no production path ever created a durable approval
// record. The gate makes them load-bearing:
//
//   - automation.force_release = "never"    -> refuse outright.
//   - automation.force_release = "approval" -> durable two-person workflow
//     over approval.Engine / state.db; one approval authorizes exactly one
//     execution (the approved record is consumed at gate-pass time).
//   - automation.force_release = "auto"     -> proceed (policy allowed it).
//
// The --yes flag only skips the cosmetic local confirmation prompt; it never
// bypasses this gate.

// forceReleaseGateDecision is the outcome of evaluating the approval gate.
type forceReleaseGateDecision struct {
	// Allowed reports whether the force-release may proceed.
	Allowed bool
	// ApprovalID is the durable approval record involved, when one exists.
	ApprovalID string
	// ApprovalStatus is the gate outcome for envelopes/messages: one of
	// "auto", "policy_never", "pending", "denied", or "consumed".
	ApprovalStatus string
	// Created reports that this evaluation created a fresh approval request.
	Created bool
	// Message is a human-readable explanation of a blocked outcome, or a
	// short note for allowed outcomes.
	Message string
}

// forceReleaseOperationKey builds the stable operation key stored in the
// approval record's correlation_id. Re-running the identical command computes
// the identical key, so an attempt finds its own earlier request (and its
// approval) instead of enqueueing duplicates. The reservation scope
// (session + reservation ID) is digested so the key stays a single opaque
// token regardless of session naming.
func forceReleaseOperationKey(projectKey, session string, reservationID int) string {
	scope := fmt.Sprintf("session=%s|reservation=%d", session, reservationID)
	digest := sha256.Sum256([]byte(scope))
	return fmt.Sprintf("force_release:%s:%s", projectKey, hex.EncodeToString(digest[:])[:16])
}

// evaluateForceReleaseGate applies the policy's automation.force_release
// setting to one force-release attempt, creating/consulting/consuming durable
// approval records as required. It returns an error only for infrastructure
// failures (store/engine unavailable); policy refusals are expressed as a
// non-Allowed decision.
func evaluateForceReleaseGate(ctx context.Context, pol *policy.Policy, eng *approval.Engine, opKey, requester, resource, reason string) (forceReleaseGateDecision, error) {
	switch pol.ForceReleasePolicy() {
	case "never":
		policyPath, _, pathErr := policy.ResolveEffectivePath()
		if pathErr != nil {
			policyPath = "the active policy file"
		}
		return forceReleaseGateDecision{
			Allowed:        false,
			ApprovalStatus: "policy_never",
			Message:        fmt.Sprintf("force-release is disabled by policy (automation.force_release=never in %s)", policyPath),
		}, nil

	case "auto":
		return forceReleaseGateDecision{
			Allowed:        true,
			ApprovalStatus: "auto",
			Message:        "policy allows force-release without approval (automation.force_release=auto)",
		}, nil
	}

	// Default ("approval", or empty/unknown -> ForceReleasePolicy() already
	// normalizes empty to "approval"): durable two-person workflow.
	record, err := eng.LatestForCorrelation(ctx, opKey)
	if err != nil {
		return forceReleaseGateDecision{}, fmt.Errorf("look up approval for %s: %w", opKey, err)
	}

	if record != nil {
		switch record.Status {
		case state.ApprovalPending:
			return forceReleaseGateDecision{
				Allowed:        false,
				ApprovalID:     record.ID,
				ApprovalStatus: string(record.Status),
				Message:        fmt.Sprintf("approval required: %s is still pending — have a second operator run `ntm approve %s`", record.ID, record.ID),
			}, nil
		case state.ApprovalDenied:
			// A denial stands for the record's original validity window;
			// after expires_at it becomes inert and a fresh request may be
			// filed below.
			if record.ExpiresAt.After(time.Now()) {
				msg := fmt.Sprintf("approval %s was denied", record.ID)
				if record.DeniedReason != "" {
					msg += fmt.Sprintf(" (%s)", record.DeniedReason)
				}
				if record.ApprovedBy != "" {
					msg += fmt.Sprintf(" by %s", record.ApprovedBy)
				}
				return forceReleaseGateDecision{
					Allowed:        false,
					ApprovalID:     record.ID,
					ApprovalStatus: string(record.Status),
					Message:        msg,
				}, nil
			}
		case state.ApprovalApproved:
			// An approval, like a denial above, stands only for the record's
			// original validity window: a grant that sat unused past
			// expires_at is inert (fail closed) and a fresh request is filed
			// below. Within the window, one approval authorizes exactly one
			// execution: consume the record at gate-pass time so a later
			// attempt needs a fresh approval, then let the attempt proceed.
			// Consume's approved->consumed transition is guarded in SQL, so
			// of two racing invocations only one can pass; the loser gets an
			// error here rather than a second execution.
			if record.ExpiresAt.After(time.Now()) {
				if err := eng.Consume(ctx, record.ID, requester); err != nil {
					return forceReleaseGateDecision{}, fmt.Errorf("consume approval %s: %w", record.ID, err)
				}
				return forceReleaseGateDecision{
					Allowed:        true,
					ApprovalID:     record.ID,
					ApprovalStatus: string(state.ApprovalConsumed),
					Message:        fmt.Sprintf("approval %s granted by %s; consumed for this execution", record.ID, record.ApprovedBy),
				}, nil
			}
		}
		// Expired (pending or approved), consumed, or an inert denial: fall
		// through to file a fresh request for this new attempt.
	}

	if reason == "" {
		reason = "force-release requested via ntm locks force-release"
	}
	created, err := eng.Request(ctx, approval.RequestParams{
		Action:        "force_release",
		Resource:      resource,
		Reason:        reason,
		RequestedBy:   requester,
		CorrelationID: opKey,
		RequiresSLB:   true,
	})
	if err != nil {
		return forceReleaseGateDecision{}, fmt.Errorf("create approval request: %w", err)
	}
	return forceReleaseGateDecision{
		Allowed:        false,
		ApprovalID:     created.ID,
		ApprovalStatus: string(created.Status),
		Created:        true,
		Message:        fmt.Sprintf("approval required: %s — have a second operator run `ntm approve %s`", created.ID, created.ID),
	}, nil
}
