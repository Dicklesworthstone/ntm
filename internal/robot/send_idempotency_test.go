package robot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestSendOperationBindingHashCanonicalizesSelectors(t *testing.T) {
	base := SendOptions{
		Session:    "proj",
		Message:    "hello",
		Panes:      []string{"1", "2"},
		AgentTypes: []string{"claude", "codex"},
	}

	a := sendOperationBindingHash(base)
	reordered := base
	reordered.Panes = []string{"2", "1"}
	reordered.AgentTypes = []string{"codex", "claude"}
	if sendOperationBindingHash(reordered) != a {
		t.Error("selector list order changed the binding hash; want canonical ordering")
	}

	otherMessage := base
	otherMessage.Message = "different"
	if sendOperationBindingHash(otherMessage) == a {
		t.Error("different input message produced the same binding hash")
	}
	otherSession := base
	otherSession.Session = "other"
	if sendOperationBindingHash(otherSession) == a {
		t.Error("different session produced the same binding hash")
	}
	otherSelector := base
	otherSelector.Panes = []string{"1"}
	if sendOperationBindingHash(otherSelector) == a {
		t.Error("different selector produced the same binding hash")
	}
	allFlag := base
	allFlag.All = true
	if sendOperationBindingHash(allFlag) == a {
		t.Error("--all did not change the binding hash")
	}
	noEnter := base
	enterOff := false
	noEnter.Enter = &enterOff
	if sendOperationBindingHash(noEnter) == a {
		t.Error("--enter=false did not change the binding hash")
	}
	clearInput := base
	clearInput.ClearInput = true
	if sendOperationBindingHash(clearInput) == a {
		t.Error("--clear-input did not change the binding hash")
	}

	// The hash binds the caller's COMMAND — selector, toggles, and input
	// message — never the resolved pane list or the delivered
	// (post-CASS-injection) payload: a byte-identical retry replays even
	// when panes changed or CASS would inject different context this time.
	if sendOperationBindingHash(base) != a {
		t.Error("identical command produced a different binding hash")
	}
	withCASS := base
	withCASS.WithCASS = true
	if sendOperationBindingHash(withCASS) == a {
		t.Error("--with-cass toggle did not change the binding hash")
	}
	if sendOperationBindingHash(withCASS) != sendOperationBindingHash(withCASS) {
		t.Error("identical --with-cass command must bind identically regardless of injected content")
	}
}

func TestSendPayloadDigest(t *testing.T) {
	sha, n := sendPayloadDigest("abc")
	if n != 3 {
		t.Errorf("payload bytes = %d, want 3", n)
	}
	if len(sha) != 64 {
		t.Errorf("payload sha length = %d, want 64 hex chars", len(sha))
	}
	sha2, _ := sendPayloadDigest("abc")
	if sha != sha2 {
		t.Error("digest is not deterministic")
	}
}

func TestAdmissionsFromSendOutput(t *testing.T) {
	output := &SendOutput{
		Targets:    []string{"cc_1", "cc_2", "cc_3"},
		Successful: []string{"cc_1"},
		Failed:     []SendError{{Pane: "cc_2", Error: "paste failed"}},
	}
	admissions := admissionsFromSendOutput(output)
	if len(admissions) != 3 {
		t.Fatalf("admissions = %+v, want 3 entries", admissions)
	}
	byTarget := map[string]SendAdmission{}
	for _, adm := range admissions {
		byTarget[adm.Target] = adm
	}
	if byTarget["cc_1"].State != AdmissionSubmitted {
		t.Errorf("cc_1 state = %s, want submitted", byTarget["cc_1"].State)
	}
	if byTarget["cc_2"].State != AdmissionRejected || byTarget["cc_2"].Error == "" {
		t.Errorf("cc_2 = %+v, want rejected with error", byTarget["cc_2"])
	}
	if byTarget["cc_3"].State != AdmissionNotAttempted {
		t.Errorf("cc_3 state = %s, want not_attempted", byTarget["cc_3"].State)
	}
}

func TestApplyReplayedOutcomeRestoresOriginalResult(t *testing.T) {
	sentAt := time.Now().UTC().Truncate(time.Second)
	outcome := sendOperationOutcome{
		Success:    true,
		SentAt:     sentAt,
		Targets:    []string{"cc_1"},
		Successful: []string{"cc_1"},
		Failed:     []SendError{},
		Admissions: []SendAdmission{{Target: "cc_1", State: AdmissionSubmitted}},
	}
	data, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal outcome: %v", err)
	}
	completed := time.Now().UTC()
	op := &state.SendOperation{
		OperationID:   "op-replay",
		SessionName:   "proj",
		PayloadSHA256: "sha",
		PayloadBytes:  10,
		Status:        state.SendOperationCompleted,
		OutcomeJSON:   string(data),
		CreatedAt:     sentAt,
		CompletedAt:   &completed,
	}

	var output SendOutput
	output.RobotResponse = NewRobotResponse(true)
	if err := applyReplayedOutcome(&output, op); err != nil {
		t.Fatalf("applyReplayedOutcome error = %v", err)
	}
	if !output.Success || len(output.Successful) != 1 || output.Successful[0] != "cc_1" {
		t.Errorf("replayed output = %+v, want original successful targets", output)
	}
	if output.Operation == nil || !output.Operation.Replayed {
		t.Fatalf("operation info = %+v, want replayed=true", output.Operation)
	}
	if len(output.Operation.Admissions) != 1 || output.Operation.Admissions[0].State != AdmissionSubmitted {
		t.Errorf("replayed admissions = %+v", output.Operation.Admissions)
	}
	if !output.SentAt.Equal(sentAt) {
		t.Errorf("replayed sent_at = %v, want original %v", output.SentAt, sentAt)
	}
}

func TestUnknownAdmissions(t *testing.T) {
	admissions := unknownAdmissions([]string{"a", "b"})
	if len(admissions) != 2 || admissions[0].State != AdmissionUnknown || admissions[1].State != AdmissionUnknown {
		t.Errorf("unknown admissions = %+v", admissions)
	}
}
