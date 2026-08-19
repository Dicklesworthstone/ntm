package assurance

import "testing"

func TestEvaluateQuiescenceQueueDry(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{})

	if got.State != QuiescenceQueueDry {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceQueueDry)
	}
	if !got.SafeToStandDown {
		t.Fatalf("SafeToStandDown = false, want true")
	}
	assertReasonCodes(t, got.ReasonCodes, []ReasonCode{ReasonQuiescenceQueueDry})
	if got.Signal.Type != SignalQuiescenceCandidate {
		t.Fatalf("Signal.Type = %q, want %q", got.Signal.Type, SignalQuiescenceCandidate)
	}
	if got.Signal.Status != SignalStatusHealthy {
		t.Fatalf("Signal.Status = %q, want %q", got.Signal.Status, SignalStatusHealthy)
	}
}

func TestEvaluateQuiescenceBlockedByPeer(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		InProgressCount:        1,
		ActiveReservationCount: 2,
	})

	if got.State != QuiescenceBlockedByPeer {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceBlockedByPeer)
	}
	if got.SafeToStandDown {
		t.Fatalf("SafeToStandDown = true, want false")
	}
	assertReasonCodes(t, got.ReasonCodes, []ReasonCode{
		ReasonQuiescenceInProgressWork,
		ReasonReservationPathConflict,
	})
}

// bd-u068s: LiveOwnedInProgressCount alone — the operator's OWN
// in-flight work — must produce QuiescenceBlockedBySelf, not
// QuiescenceBlockedByPeer. Pre-fix this case was silently routed
// through the peer-attributed state and consumers rendering
// "blocked by peer X" had no peer to name.
func TestEvaluateQuiescenceBlockedBySelfWhenOnlyLiveOwnedInProgressNonZero(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		LiveOwnedInProgressCount: 2,
	})

	if got.State != QuiescenceBlockedBySelf {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceBlockedBySelf)
	}
	if got.SafeToStandDown {
		t.Fatalf("SafeToStandDown = true, want false")
	}
	assertReasonCodes(t, got.ReasonCodes, []ReasonCode{ReasonQuiescenceInProgressWork})
	if got.Signal.Status != SignalStatusDegraded {
		t.Errorf("Signal.Status = %q, want degraded", got.Signal.Status)
	}
}

// Peer-attributed signals take precedence when both kinds are present.
// A swarm with one peer-held in-progress AND one own live-in-progress
// surfaces as BlockedByPeer (the stronger blocker), preserving the
// existing test corpus's expectations.
func TestEvaluateQuiescenceBlockedByPeerWinsWhenPeerAndSelfBothPresent(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		InProgressCount:          1,
		LiveOwnedInProgressCount: 1,
	})

	if got.State != QuiescenceBlockedByPeer {
		t.Fatalf("State = %q, want %q (peer takes precedence)", got.State, QuiescenceBlockedByPeer)
	}
}

// ActiveReservationCount alone still routes to BlockedByPeer because
// reservations are peer-attributable in the existing data model.
func TestEvaluateQuiescenceBlockedByPeerWhenOnlyReservationsNonZero(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		ActiveReservationCount: 3,
	})

	if got.State != QuiescenceBlockedByPeer {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceBlockedByPeer)
	}
}

func TestEvaluateQuiescenceSaturatedReviewLoop(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		ReviewRounds:         3,
		RecentReviewFindings: 0,
	})

	if got.State != QuiescenceSaturatedReviewLoop {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceSaturatedReviewLoop)
	}
	if !got.SafeToStandDown {
		t.Fatalf("SafeToStandDown = false, want true")
	}
	assertReasonCodes(t, got.ReasonCodes, []ReasonCode{
		ReasonQuiescenceQueueDry,
		ReasonQuiescenceReviewSaturated,
	})
}

func TestEvaluateQuiescenceUnsafeReadyWork(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		ReadyCount:      1,
		ActionableCount: 1,
	})

	if got.State != QuiescenceUnsafeToStandDown {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceUnsafeToStandDown)
	}
	if got.SafeToStandDown {
		t.Fatalf("SafeToStandDown = true, want false")
	}
	assertReasonCodes(t, got.ReasonCodes, []ReasonCode{ReasonQuiescenceReadyWork})
}

func TestEvaluateQuiescenceUnsafeUrgentMail(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		UrgentMailCount: 1,
	})

	if got.State != QuiescenceUnsafeToStandDown {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceUnsafeToStandDown)
	}
	assertReasonCodes(t, got.ReasonCodes, []ReasonCode{ReasonQuiescenceUrgentMail})
}

// bd-zy2c1: a swarm with PendingAckCount>0 and UrgentMailCount=0 must
// surface ReasonQuiescencePendingAckMail (not ReasonQuiescenceUrgentMail).
// Pre-fix the two were collapsed under the urgent code, mis-labeling
// non-urgent ack-required mail as urgent for downstream consumers.
func TestEvaluateQuiescenceUnsafePendingAckMailGetsDistinctReason(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		PendingAckCount: 3,
	})
	if got.State != QuiescenceUnsafeToStandDown {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceUnsafeToStandDown)
	}
	assertReasonCodes(t, got.ReasonCodes, []ReasonCode{ReasonQuiescencePendingAckMail})
}

// bd-zy2c1: when both counts are non-zero, both reason codes must
// fire so a consumer can route on the urgency boundary independently.
func TestEvaluateQuiescenceUnsafeBothMailReasonsFireWhenBothCountsNonZero(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		UrgentMailCount: 2,
		PendingAckCount: 5,
	})
	if got.State != QuiescenceUnsafeToStandDown {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceUnsafeToStandDown)
	}
	assertReasonCodes(t, got.ReasonCodes, []ReasonCode{
		ReasonQuiescenceUrgentMail,
		ReasonQuiescencePendingAckMail,
	})
}

func TestEvaluateQuiescenceUnsafeDirtyTracker(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		TrackerNeedsFlush: true,
	})

	if got.State != QuiescenceUnsafeToStandDown {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceUnsafeToStandDown)
	}
	assertReasonCodes(t, got.ReasonCodes, []ReasonCode{ReasonQuiescenceTrackerDirty})
}

func TestEvaluateQuiescenceUnsafeDirtyWorktree(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		DirtyWorktree: true,
	})

	if got.State != QuiescenceUnsafeToStandDown {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceUnsafeToStandDown)
	}
	assertReasonCodes(t, got.ReasonCodes, []ReasonCode{ReasonQuiescenceDirtyWorktree})
}

func TestEvaluateQuiescenceReviewFindingsRemainUnsafe(t *testing.T) {
	got := EvaluateQuiescence(QuiescenceInput{
		ReviewRounds:         2,
		RecentReviewFindings: 1,
	})

	if got.State != QuiescenceUnsafeToStandDown {
		t.Fatalf("State = %q, want %q", got.State, QuiescenceUnsafeToStandDown)
	}
	assertReasonCodes(t, got.ReasonCodes, []ReasonCode{ReasonQuiescencePendingWork})
}

func assertReasonCodes(t *testing.T, got, want []ReasonCode) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("reason codes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reason codes = %v, want %v", got, want)
		}
	}
}
