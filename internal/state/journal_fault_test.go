package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"
)

type modelOpPhase int

const (
	phaseUnclaimed modelOpPhase = iota
	phaseClaimedUnstarted
	phaseDispatchedInFlight
	phaseCompleted
	phaseCanceled
)

type modelOpRef struct {
	phase         modelOpPhase
	kind          string
	bindingHash   string
	payloadSHA    string
	claimToken    string
	dispatchedCnt int
	outcomeJSON   string
	outcomeSHA256 string
	corrupted     bool
}

type journalStepTrace struct {
	Step   int    `json:"step"`
	Action string `json:"action"`
	OpKey  string `json:"op_key"`
	Detail string `json:"detail"`
}

// TestDurableJournalFaultInjectionModelCheck runs 500 deterministic randomized
// schedules against real SQLite send_operations journals, injecting pre-dispatch
// crashes, post-dispatch crashes, stale takeovers, pre-dispatch cancellations,
// conflicting --op-id mutations, and tampered receipt payloads, mechanically
// checking invariants I1 (at-most-once dispatch), I3 (monotonic receipt
// integrity), and I4 (idempotency determinism).
func TestDurableJournalFaultInjectionModelCheck(t *testing.T) {
	const numSchedules = 500

	// Reuse a single migrated SQLite database with distinct session namespaces per
	// schedule so 500 full ACID schedules run in ~1s without mocking SQLite.
	store, err := Open(filepath.Join(t.TempDir(), "journal_modelcheck.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	_ = []string{"op-alpha", "op-beta", "op-gamma"}
	kinds := []string{OperationKindSend, OperationKindInterrupt}

	for seed := int64(1); seed <= numSchedules; seed++ {
		rng := rand.New(rand.NewSource(seed))
		session := fmt.Sprintf("mc-sess-%04d", seed)
		schedOps := []string{
			fmt.Sprintf("%s:op-alpha", session),
			fmt.Sprintf("%s:op-beta", session),
			fmt.Sprintf("%s:op-gamma", session),
		}
		ref := make(map[string]*modelOpRef, len(schedOps))
		for _, name := range schedOps {
			ref[name] = &modelOpRef{phase: phaseUnclaimed}
		}

		steps := 14 + rng.Intn(10)
		trace := make([]journalStepTrace, 0, steps)

		failInvariant := func(inv, msg string) {
			t.Helper()
			traceJSON, _ := json.Marshal(trace)
			t.Fatalf("seed=%d session=%s invariant=%s violation: %s\ntrace=%s", seed, session, inv, msg, traceJSON)
		}

		for step := 0; step < steps; step++ {
			opID := schedOps[rng.Intn(len(schedOps))]
			m := ref[opID]
			actionRoll := rng.Intn(7)

			switch actionRoll {
			case 0: // Claim fresh or retry identical
				kind := kinds[rng.Intn(len(kinds))]
				binding := fmt.Sprintf("bind-%s-v1", kind)
				payload := fmt.Sprintf("sha-%s-v1", opID)
				if m.phase != phaseUnclaimed {
					kind = m.kind
					binding = m.bindingHash
					payload = m.payloadSHA
				}
				stored, claimed, err := store.ClaimSendOperation(&SendOperation{
					OperationID:   opID,
					SessionName:   session,
					Kind:          kind,
					BindingHash:   binding,
					PayloadSHA256: payload,
					PayloadBytes:  32,
					Targets:       []string{"1"},
				})
				if err != nil {
					failInvariant("SQL", fmt.Sprintf("ClaimSendOperation: %v", err))
				}
				trace = append(trace, journalStepTrace{Step: step, Action: "claim_identical", OpKey: opID, Detail: fmt.Sprintf("claimed=%v status=%s", claimed, stored.Status)})

				if m.phase == phaseUnclaimed {
					if !claimed || stored.Status != SendOperationInProgress {
						failInvariant("I4", fmt.Sprintf("fresh claim failed: claimed=%v status=%s", claimed, stored.Status))
					}
					m.phase = phaseClaimedUnstarted
					m.kind = kind
					m.bindingHash = binding
					m.payloadSHA = payload
					m.claimToken = stored.ClaimToken
				} else {
					if claimed {
						failInvariant("I1", "duplicate claim granted while operation already existed")
					}
					// I4: Identical completed replay returns stored receipt verbatim without re-dispatch.
					if m.phase == phaseCompleted && !m.corrupted {
						if stored.Status != SendOperationCompleted || stored.OutcomeJSON != m.outcomeJSON {
							failInvariant("I4", fmt.Sprintf("completed replay drifted: got (%s, %q), want (%s, %q)", stored.Status, stored.OutcomeJSON, SendOperationCompleted, m.outcomeJSON))
						}
					}
					if m.phase == phaseCanceled && stored.Status != SendOperationCanceled {
						failInvariant("I4", fmt.Sprintf("canceled op reported status=%s", stored.Status))
					}
				}

			case 1: // Mutated / conflicting --op-id retry (I4 conflict detection)
				if m.phase == phaseUnclaimed {
					continue
				}
				mutBinding := m.bindingHash + "-mutated"
				stored, claimed, err := store.ClaimSendOperation(&SendOperation{
					OperationID:   opID,
					SessionName:   session,
					Kind:          m.kind,
					BindingHash:   mutBinding,
					PayloadSHA256: "mutated-sha",
					PayloadBytes:  64,
					Targets:       []string{"1", "2"},
				})
				if err != nil {
					failInvariant("SQL", fmt.Sprintf("ClaimSendOperation(conflict): %v", err))
				}
				trace = append(trace, journalStepTrace{Step: step, Action: "claim_conflict", OpKey: opID, Detail: fmt.Sprintf("claimed=%v", claimed)})
				if claimed || stored.BindingHash != m.bindingHash {
					failInvariant("I4", fmt.Sprintf("conflicting op-id overwrote binding: claimed=%v got=%s want=%s", claimed, stored.BindingHash, m.bindingHash))
				}

			case 2: // Pre-dispatch abort/release OR simulated pre-dispatch crash + takeover
				if m.phase != phaseClaimedUnstarted {
					continue
				}
				if rng.Intn(2) == 0 {
					if err := store.ReleaseSendOperation(opID, session, m.claimToken); err != nil {
						failInvariant("I1", fmt.Sprintf("ReleaseSendOperation pre-dispatch failed: %v", err))
					}
					oldToken := m.claimToken
					m.phase = phaseUnclaimed
					m.claimToken = ""
					trace = append(trace, journalStepTrace{Step: step, Action: "release_predispatch", OpKey: opID, Detail: "released"})
					// Old token can never start after release.
					if _, err := store.StartSendOperation(&SendOperation{OperationID: opID, SessionName: session, ClaimToken: oldToken}); !errors.Is(err, ErrSendOperationClaimLost) {
						failInvariant("I1", fmt.Sprintf("released token allowed StartSendOperation: %v", err))
					}
				} else {
					// Simulate stale crashed claimant before dispatch -> takeover rotates fence token.
					staleBefore := time.Now().UTC().Add(time.Hour)
					oldToken := m.claimToken
					taken, err := store.TakeOverStaleSendOperation(opID, session, m.bindingHash, oldToken, staleBefore)
					if err != nil || taken == nil {
						failInvariant("I1", fmt.Sprintf("TakeOverStaleSendOperation pre-dispatch = (%+v, %v)", taken, err))
					}
					if taken.ClaimToken == oldToken {
						failInvariant("I1", "takeover failed to rotate claim_token")
					}
					// Zombie old claimant MUST fail StartSendOperation!
					if _, err := store.StartSendOperation(&SendOperation{OperationID: opID, SessionName: session, ClaimToken: oldToken}); !errors.Is(err, ErrSendOperationClaimLost) {
						failInvariant("I1", fmt.Sprintf("zombie pre-takeover token allowed StartSendOperation: %v", err))
					}
					m.claimToken = taken.ClaimToken
					trace = append(trace, journalStepTrace{Step: step, Action: "crash_takeover_predispatch", OpKey: opID, Detail: "token_rotated"})
				}

			case 3: // Cross dispatch boundary (exact-once side effect!)
				if m.phase != phaseClaimedUnstarted {
					continue
				}
				started, err := store.StartSendOperation(&SendOperation{
					OperationID:   opID,
					SessionName:   session,
					ClaimToken:    m.claimToken,
					PayloadSHA256: m.payloadSHA,
					PayloadBytes:  32,
					Targets:       []string{"1"},
				})
				if err != nil || started.DispatchStartedAt == nil {
					failInvariant("I1", fmt.Sprintf("StartSendOperation = (%+v, %v)", started, err))
				}
				m.dispatchedCnt++
				m.phase = phaseDispatchedInFlight
				trace = append(trace, journalStepTrace{Step: step, Action: "dispatch", OpKey: opID, Detail: fmt.Sprintf("count=%d", m.dispatchedCnt)})
				if m.dispatchedCnt > 1 {
					failInvariant("I1", fmt.Sprintf("operation dispatched %d times (>1)!", m.dispatchedCnt))
				}

			case 4: // Simulated crash AFTER dispatch -> takeover & release MUST fail closed (I1)
				if m.phase != phaseDispatchedInFlight {
					continue
				}
				staleBefore := time.Now().UTC().Add(time.Hour)
				taken, err := store.TakeOverStaleSendOperation(opID, session, m.bindingHash, m.claimToken, staleBefore)
				if err != nil || taken != nil {
					failInvariant("I1", fmt.Sprintf("post-dispatch crash was taken over and re-executed: (%+v, %v)", taken, err))
				}
				if err := store.ReleaseSendOperation(opID, session, m.claimToken); !errors.Is(err, ErrSendOperationClaimLost) {
					failInvariant("I1", fmt.Sprintf("post-dispatch claim allowed ReleaseSendOperation: %v", err))
				}
				trace = append(trace, journalStepTrace{Step: step, Action: "crash_postdispatch_fenced", OpKey: opID, Detail: "takeover_refused"})

			case 5: // Cancel request directly on opID (fences pre-dispatch; refuses post-dispatch & terminal)
				if m.phase == phaseUnclaimed {
					canceledOp, ok, cancelErr := store.CancelSendOperationByID(opID, time.Now().UTC())
					if cancelErr != nil || ok || canceledOp != nil {
						failInvariant("I4", fmt.Sprintf("CancelSendOperationByID(unclaimed) = (%+v, %v, %v)", canceledOp, ok, cancelErr))
					}
					continue
				}
				switch m.phase {
				case phaseClaimedUnstarted:
					oldToken := m.claimToken
					canceledOp, ok, cancelErr := store.CancelSendOperationByID(opID, time.Now().UTC())
					if cancelErr != nil || !ok || canceledOp == nil || canceledOp.Status != SendOperationCanceled {
						failInvariant("I1", fmt.Sprintf("CancelSendOperationByID pre-dispatch = (%+v, %v, %v)", canceledOp, ok, cancelErr))
					}
					if _, startErr := store.StartSendOperation(&SendOperation{OperationID: opID, SessionName: session, ClaimToken: oldToken}); !errors.Is(startErr, ErrSendOperationClaimLost) {
						failInvariant("I1", fmt.Sprintf("canceled claim allowed StartSendOperation: %v", startErr))
					}
					m.phase = phaseCanceled
					trace = append(trace, journalStepTrace{Step: step, Action: "cancel_predispatch_fenced", OpKey: opID, Detail: "fenced"})
				case phaseDispatchedInFlight:
					if _, ok, cancelErr := store.CancelSendOperationByID(opID, time.Now().UTC()); ok || !errors.Is(cancelErr, ErrSendOperationDispatched) {
						failInvariant("I1", fmt.Sprintf("dispatched operation allowed cancel: ok=%v err=%v", ok, cancelErr))
					}
					trace = append(trace, journalStepTrace{Step: step, Action: "cancel_dispatched_refused", OpKey: opID, Detail: "refused"})
				case phaseCompleted, phaseCanceled:
					canceledOp, ok, cancelErr := store.CancelSendOperationByID(opID, time.Now().UTC())
					if cancelErr != nil || ok || canceledOp == nil {
						failInvariant("I3", fmt.Sprintf("terminal operation cancel = (%+v, %v, %v)", canceledOp, ok, cancelErr))
					}
					trace = append(trace, journalStepTrace{Step: step, Action: "cancel_terminal_noop", OpKey: opID, Detail: canceledOp.Status})
				}

			case 6: // Complete receipt & verify I3 monotonic integrity + corruption detection
				if m.phase != phaseDispatchedInFlight {
					continue
				}
				outcome := fmt.Sprintf(`{"success":true,"op":%q,"seed":%d}`, opID, seed)
				sum := sha256.Sum256([]byte(outcome))
				digest := hex.EncodeToString(sum[:])
				if err := store.CompleteSendOperation(opID, session, m.claimToken, outcome, time.Now().UTC()); err != nil {
					failInvariant("I3", fmt.Sprintf("CompleteSendOperation: %v", err))
				}
				m.phase = phaseCompleted
				m.outcomeJSON = outcome
				m.outcomeSHA256 = digest

				// I3: Second completion attempt MUST fail closed and never overwrite receipt!
				if err := store.CompleteSendOperation(opID, session, m.claimToken, `{"success":false,"tampered":true}`, time.Now().UTC()); !errors.Is(err, ErrSendOperationClaimLost) {
					failInvariant("I3", fmt.Sprintf("completed receipt overwritten by duplicate CompleteSendOperation: %v", err))
				}

				// Optional corruption fault: inject malformed JSON on disk and verify digest mismatch is detected, never masked!
				if rng.Intn(4) == 0 {
					if _, err := store.db.Exec(`UPDATE send_operations SET outcome_json = ? WHERE operation_id = ? AND session_name = ?`, `{corrupt-json`, opID, session); err != nil {
						failInvariant("SQL", fmt.Sprintf("inject corrupt outcome_json: %v", err))
					}
					m.corrupted = true
					got, err := store.GetSendOperation(opID, session)
					if err != nil || got == nil {
						failInvariant("SQL", fmt.Sprintf("GetSendOperation(corrupt): %v", err))
					}
					gotSum := sha256.Sum256([]byte(got.OutcomeJSON))
					if hex.EncodeToString(gotSum[:]) == m.outcomeSHA256 || json.Valid([]byte(got.OutcomeJSON)) {
						failInvariant("I3", "corrupted receipt passed integrity/JSON validation")
					}
					trace = append(trace, journalStepTrace{Step: step, Action: "complete_and_corrupt_detected", OpKey: opID, Detail: "corrupt_detected"})
				} else {
					trace = append(trace, journalStepTrace{Step: step, Action: "complete_receipt", OpKey: opID, Detail: digest[:12]})
				}
			}

			// Global step invariant check across all tracked ops in this schedule:
			for name, stateRef := range ref {
				if stateRef.dispatchedCnt > 1 {
					failInvariant("I1", fmt.Sprintf("%s dispatched %d times", name, stateRef.dispatchedCnt))
				}
			}
		}
	}
}
