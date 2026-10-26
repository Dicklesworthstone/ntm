package robot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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

// TestSendOperationBindingHashIsStableAcrossVersions pins the exact digests
// the pre-shared-hasher implementation produced (computed with the verbatim
// v1.26 sendOperationBindingHash). Moving the encoding into
// operationBindingHasher must not change a single byte: a changed digest
// turns every recorded operation ID into IDEMPOTENCY_CONFLICT on retry.
func TestSendOperationBindingHashIsStableAcrossVersions(t *testing.T) {
	enterOff := false
	cases := []struct {
		name string
		opts SendOptions
		want string
	}{
		{
			name: "selector lists with whitespace and empties",
			opts: SendOptions{
				Session: "proj", Message: "deploy",
				Panes: []string{" 2", "1", ""}, AgentTypes: []string{"codex", "claude"}, Exclude: []string{"3"},
			},
			want: "cc064cd39ad44f118f4353d1b890a8f504104756b6ceda46fb41cf6c474a5aa0",
		},
		{
			name: "every toggle set",
			opts: SendOptions{
				Session: "proj", All: true, Pane: "%7", Enter: &enterOff, ClearInput: true,
				WithCASS: true, WithMemory: true, Message: "hello world",
			},
			want: "064f1f4f4b8005fcb2b6b58f1445a021b32e89af8da0b6ef59dea65c70bef8f7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sendOperationBindingHash(tc.opts); got != tc.want {
				t.Fatalf("binding hash = %s, want pinned %s", got, tc.want)
			}
		})
	}
}

func TestOperationPayloadDigest(t *testing.T) {
	sha, n := operationPayloadDigest("abc")
	if n != 3 {
		t.Errorf("payload bytes = %d, want 3", n)
	}
	if len(sha) != 64 {
		t.Errorf("payload sha length = %d, want 64 hex chars", len(sha))
	}
	sha2, _ := operationPayloadDigest("abc")
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
	byTarget := map[string]OperationAdmission{}
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
		Admissions: []OperationAdmission{{Target: "cc_1", State: AdmissionSubmitted}},
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
	if err := applyReplayedSendOutcome(&output, op); err != nil {
		t.Fatalf("applyReplayedSendOutcome error = %v", err)
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

// Exercise both public actuation engines against real SQLite and a recording
// tmux executable. The transport records actual input commands, so a refusal
// envelope cannot hide duplicate keystrokes. These tests run under -short and
// need neither a live tmux server nor an agent installation.
func TestOperationCrashRecoveryTransport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording tmux transport requires a POSIX shell")
	}
	for _, kind := range []string{state.OperationKindSend, state.OperationKindInterrupt} {
		for _, scenario := range []string{"stale-started", "fresh-started", "stale-unstarted", "start-failure", "completion-failure", "message-guard"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				root := t.TempDir()
				const transport = `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_OPERATION_TEST_ROOT/calls"
case "$1" in
  -V) printf 'tmux 3.4\n' ;;
  has-session) ;;
  list-panes)
    activity=
    case "$*" in *'#{window_activity}'*) activity=0_NTM_SEP_ ;; esac
    printf '%s_NTM_SEP_%s_NTM_SEP_proj__aider_1_NTM_SEP_aider_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_%s%s_NTM_SEP_0_NTM_SEP_aider_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$NTM_OPERATION_PANE_ID" "$NTM_OPERATION_PANE_INDEX" "$activity" "$NTM_OPERATION_TEST_PID"
    ;;
  display-message)
    case "$*" in
      *'@ntm_rotation_token'*) printf '[]\n' ;;
      *) printf '%s\n' "$NTM_OPERATION_TEST_ROOT" ;;
    esac
    ;;
  capture-pane) printf 'Ready for work\n' ;;
  load-buffer)
    printf '%s\n' "$*" >> "$NTM_OPERATION_TEST_ROOT/mutations"
    cat >> "$NTM_OPERATION_TEST_ROOT/payload"
    ;;
  paste-buffer)
    printf '%s\n' "$*" >> "$NTM_OPERATION_TEST_ROOT/mutations"
    ;;
  send-keys)
    printf '%s\n' "$*" >> "$NTM_OPERATION_TEST_ROOT/mutations"
    literal=0
    for argument do
      if [ "$argument" = -l ]; then literal=1; fi
    done
    if [ "$literal" = 1 ]; then
      printf '%s' "$argument" >> "$NTM_OPERATION_TEST_ROOT/payload"
    fi
    ;;
  delete-buffer) ;;
  *) printf 'unexpected tmux command: %s\n' "$*" >&2; exit 21 ;;
esac
`
				binary := filepath.Join(root, "tmux")
				if err := os.WriteFile(binary, []byte(transport), 0o700); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOperationCrashRecoveryTransportHelper$", "-test.short", "-test.v")
				cmd.Env = append(os.Environ(),
					"NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+binary,
					"NTM_OPERATION_TEST_ROOT="+root, "NTM_OPERATION_TEST_KIND="+kind,
					"NTM_OPERATION_TEST_SCENARIO="+scenario,
				)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("operation transport regression: %v\n%s", err, output)
				}
			})
		}
	}
}

func TestOperationCrashRecoveryTransportHelper(t *testing.T) {
	root := os.Getenv("NTM_OPERATION_TEST_ROOT")
	if root == "" {
		return
	}
	kind := os.Getenv("NTM_OPERATION_TEST_KIND")
	scenario := os.Getenv("NTM_OPERATION_TEST_SCENARIO")
	t.Chdir(root)
	t.Setenv("NTM_CONFIG", filepath.Join(root, "config.toml"))
	t.Setenv("NTM_OPERATION_TEST_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("NTM_OPERATION_PANE_ID", "%7")
	t.Setenv("NTM_OPERATION_PANE_INDEX", "1")
	installIdempotencyBranchFeed(t)
	store := installIdempotencyBranchStore(t)
	const operationID = "crash-recovery-operation"
	const session = "proj"
	const message = "prefix password=hunter2hunter2 suffix"
	redactionConfig := redaction.Config{Mode: redaction.ModeRedact}
	sendOptions := SendOptions{
		Session: session, All: true, Message: message,
		IdempotencyKey: operationID, Redaction: redactionConfig,
	}
	interruptOptions := InterruptOptions{
		Session: session, All: true, Force: true, NoWait: true,
		Message: message, IdempotencyKey: operationID, Redaction: redactionConfig,
		TimeoutMs: 10000, PollMs: 300,
	}

	type actuationResult struct {
		response  RobotResponse
		operation *OperationInfo
		warnings  []string
	}
	invoke := func() actuationResult {
		t.Helper()
		switch kind {
		case state.OperationKindSend:
			output, err := GetSend(sendOptions)
			if err != nil || output == nil {
				t.Fatalf("GetSend returned (%+v, %v)", output, err)
			}
			return actuationResult{output.RobotResponse, output.Operation, output.Warnings}
		case state.OperationKindInterrupt:
			output, err := GetInterrupt(interruptOptions)
			if err != nil || output == nil {
				t.Fatalf("GetInterrupt returned (%+v, %v)", output, err)
			}
			return actuationResult{output.RobotResponse, output.Operation, output.Warnings}
		default:
			t.Fatalf("unknown actuation kind %q", kind)
			return actuationResult{}
		}
	}
	readLog := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatalf("read transport %s: %v", name, err)
		}
		return data
	}
	execSQL := func(statement string, args ...any) {
		t.Helper()
		if _, err := store.DB().Exec(statement, args...); err != nil {
			t.Fatalf("prepare durable crash fixture: %v", err)
		}
	}
	readOperation := func() *state.SendOperation {
		t.Helper()
		op, err := store.GetSendOperation(operationID, session)
		if err != nil || op == nil {
			t.Fatalf("read operation: (%+v, %v)", op, err)
		}
		return op
	}
	assertMetadata := func(info *OperationInfo, original *state.SendOperation, admission string) {
		t.Helper()
		if info == nil || info.OperationID != operationID || info.Kind != kind ||
			info.Status != original.Status || info.PayloadSHA256 != original.PayloadSHA256 ||
			info.PayloadBytes != original.PayloadBytes || info.DispatchStartedAt == nil ||
			original.DispatchStartedAt == nil || !info.DispatchStartedAt.Equal(*original.DispatchStartedAt) {
			t.Fatalf("operation metadata = %+v, want frozen record %+v", info, original)
		}
		if len(info.Admissions) != len(original.Targets) {
			t.Fatalf("admissions = %+v, want original targets %v", info.Admissions, original.Targets)
		}
		for i, target := range original.Targets {
			if info.Admissions[i].Target != target || info.Admissions[i].State != admission {
				t.Fatalf("admission[%d] = %+v, want original target %s state %s", i, info.Admissions[i], target, admission)
			}
		}
	}
	assertDelivered := func(result actuationResult, status string) *state.SendOperation {
		t.Helper()
		if !result.response.Success {
			t.Fatalf("initial actuation failed: %+v, warnings=%v, calls=%s", result.response, result.warnings, readLog("calls"))
		}
		mutations, payload := readLog("mutations"), readLog("payload")
		if len(mutations) == 0 || len(payload) == 0 {
			t.Fatalf("actuation did not reach tmux: mutations=%q payload=%q", mutations, payload)
		}
		if kind == state.OperationKindInterrupt && !bytes.Contains(mutations, []byte("C-c")) {
			t.Fatalf("interrupt did not deliver its control key: %q", mutations)
		}
		if bytes.Contains(payload, []byte("hunter2hunter2")) {
			t.Fatalf("recording transport received the unredacted payload: %q", payload)
		}
		op := readOperation()
		if op.Status != status || op.DispatchStartedAt == nil || op.ClaimToken == "" || !slices.Equal(op.Targets, []string{"1"}) {
			t.Fatalf("delivered operation lost its boundary or targets: %+v", op)
		}
		if op.PayloadSHA256 != fmt.Sprintf("%x", sha256.Sum256(payload)) || op.PayloadBytes != int64(len(payload)) {
			t.Fatalf("receipt digest does not describe actual transport payload %q: %+v", payload, op)
		}
		admission := AdmissionSubmitted
		if status == state.SendOperationInProgress {
			admission = AdmissionUnknown
		}
		assertMetadata(result.operation, op, admission)
		return op
	}
	assertRefusedWithoutInput := func(original *state.SendOperation, code string, mutations []byte) {
		t.Helper()
		for attempt := 0; attempt < 2; attempt++ {
			result := invoke()
			if result.response.Success || result.response.ErrorCode != code {
				t.Fatalf("retry %d = %+v, want %s", attempt, result.response, code)
			}
			if code == ErrCodeOperationOutcomeUnknown && !strings.Contains(result.response.Hint, "--robot-send-receipt="+operationID) {
				t.Fatalf("unknown outcome has no reconciliation hint: %+v", result.response)
			}
			if current := readLog("mutations"); !bytes.Equal(current, mutations) {
				t.Fatalf("retry %d repeated pane input: before=%q after=%q", attempt, mutations, current)
			}
			assertMetadata(result.operation, original, AdmissionUnknown)
			stored := readOperation()
			if stored.Status != state.SendOperationInProgress || stored.ClaimToken != original.ClaimToken ||
				stored.PayloadSHA256 != original.PayloadSHA256 || stored.PayloadBytes != original.PayloadBytes ||
				!stored.CreatedAt.Equal(original.CreatedAt) || !slices.Equal(stored.Targets, original.Targets) ||
				stored.DispatchStartedAt == nil || !stored.DispatchStartedAt.Equal(*original.DispatchStartedAt) || stored.OutcomeJSON != "" {
				t.Fatalf("retry changed the original crash evidence: got %+v, original %+v", stored, original)
			}
			receipt, err := GetSendReceipt(operationID)
			if err != nil || receipt == nil || !receipt.Success || receipt.Outcome != nil || receipt.InterruptOutcome != nil {
				t.Fatalf("unknown receipt = (%+v, %v), want pending evidence without invented outcome", receipt, err)
			}
			assertMetadata(receipt.Operation, original, AdmissionUnknown)
		}
	}

	switch scenario {
	case "message-guard":
		refused := errors.New("selected project's message policy refuses delivery")
		calls := 0
		guard := func(_ context.Context, payload string, panes []tmux.Pane) error {
			calls++
			if len(panes) != 1 || panes[0].ID != "%7" || panes[0].PID != os.Getpid() {
				t.Fatalf("request guard did not receive the resolved physical targets: %+v", panes)
			}
			if !strings.Contains(payload, "prefix ") || !strings.Contains(payload, " suffix") {
				t.Fatalf("request guard did not receive the prepared message: %q", payload)
			}
			if strings.Contains(payload, "hunter2hunter2") {
				t.Fatalf("request guard received input before final redaction: %q", payload)
			}
			return refused
		}
		var guardErr error
		if kind == state.OperationKindSend {
			binding := sendOperationBindingHash(sendOptions)
			before, err := json.Marshal(sendOptions)
			if err != nil {
				t.Fatal(err)
			}
			sendOptions.MessageGuard = guard
			after, err := json.Marshal(sendOptions)
			if err != nil || !bytes.Equal(before, after) || binding != sendOperationBindingHash(sendOptions) {
				t.Fatalf("request guard changed serialized options or durable identity: %v", err)
			}
			_, guardErr = GetSend(sendOptions)
		} else {
			binding := interruptOperationBindingHash(interruptOptions)
			before, err := json.Marshal(interruptOptions)
			if err != nil {
				t.Fatal(err)
			}
			interruptOptions.MessageGuard = guard
			after, err := json.Marshal(interruptOptions)
			if err != nil || !bytes.Equal(before, after) || binding != interruptOperationBindingHash(interruptOptions) {
				t.Fatalf("request guard changed serialized options or durable identity: %v", err)
			}
			_, guardErr = GetInterrupt(interruptOptions)
		}
		if !errors.Is(guardErr, refused) || calls != 1 || len(readLog("mutations")) != 0 {
			t.Fatalf("guard refusal did not stop actuation: calls=%d err=%v mutations=%s", calls, guardErr, readLog("mutations"))
		}
		if op, err := store.GetSendOperation(operationID, session); err != nil || op != nil {
			t.Fatalf("guard refusal claimed its operation ID: (%+v, %v)", op, err)
		}
		// The request-local refusal cannot poison another request or consume
		// its retry identity. A fresh allowed request can dispatch normally.
		var checkedPayloads []string
		allow := func(_ context.Context, payload string, panes []tmux.Pane) error {
			if len(panes) != 1 || panes[0].ID != "%7" || panes[0].PID != os.Getpid() {
				t.Fatalf("delivery guard changed the resolved physical target: %+v", panes)
			}
			checkedPayloads = append(checkedPayloads, payload)
			return nil
		}
		sendOptions.MessageGuard, interruptOptions.MessageGuard = allow, allow
		assertDelivered(invoke(), state.SendOperationCompleted)
		if len(checkedPayloads) != 2 || checkedPayloads[0] != string(readLog("payload")) || checkedPayloads[1] != checkedPayloads[0] {
			t.Fatalf("preflight and delivery guards did not check the exact transported bytes: checks=%q payload=%q", checkedPayloads, readLog("payload"))
		}
	case "stale-started", "fresh-started":
		assertDelivered(invoke(), state.SendOperationCompleted)
		mutations := readLog("mutations")
		createdAt := time.Now().UTC()
		code := ErrCodeOperationInProgress
		if scenario == "stale-started" {
			createdAt = createdAt.Add(-2 * operationStaleClaimWindow)
			code = ErrCodeOperationOutcomeUnknown
		}
		// Model a caller dying after transport accepted input but before its
		// completion persisted. Keep the original dispatch marker and payload.
		execSQL(`UPDATE send_operations SET status = ?, outcome_json = '', completed_at = NULL, created_at = ? WHERE operation_id = ? AND session_name = ?`,
			state.SendOperationInProgress, createdAt, operationID, session)
		original := readOperation()
		// The same --all selector now resolves to a replacement pane. The
		// refusal and receipt must still describe the original target.
		t.Setenv("NTM_OPERATION_PANE_ID", "%8")
		t.Setenv("NTM_OPERATION_PANE_INDEX", "2")
		assertRefusedWithoutInput(original, code, mutations)
	case "stale-unstarted":
		binding := sendOperationBindingHash(sendOptions)
		if kind == state.OperationKindInterrupt {
			binding = interruptOperationBindingHash(interruptOptions)
		}
		original, claimed, err := store.ClaimSendOperation(&state.SendOperation{
			OperationID: operationID, SessionName: session, Kind: kind, BindingHash: binding,
			PayloadSHA256: "preflight-digest", PayloadBytes: 123, Targets: []string{"previous-topology"},
			CreatedAt: time.Now().UTC().Add(-2 * operationStaleClaimWindow),
		})
		if err != nil || !claimed {
			t.Fatalf("seed unstarted claim: claimed=%t err=%v", claimed, err)
		}
		completed := assertDelivered(invoke(), state.SendOperationCompleted)
		if completed.ClaimToken == original.ClaimToken || !completed.CreatedAt.After(original.CreatedAt) {
			t.Fatalf("unstarted recovery did not fence the old owner: original=%+v completed=%+v", original, completed)
		}
		mutations := readLog("mutations")
		replayed := invoke()
		if !replayed.response.Success || replayed.operation == nil || !replayed.operation.Replayed || !bytes.Equal(readLog("mutations"), mutations) {
			t.Fatalf("completed recovered operation did not replay without input: %+v", replayed)
		}
		assertMetadata(replayed.operation, completed, AdmissionSubmitted)
	case "start-failure":
		execSQL(`CREATE TRIGGER fail_operation_start BEFORE UPDATE OF dispatch_started_at ON send_operations WHEN NEW.dispatch_started_at IS NOT NULL BEGIN SELECT RAISE(ABORT, 'injected dispatch boundary failure'); END`)
		result := invoke()
		if result.response.Success || result.response.ErrorCode != ErrCodeInternalError ||
			!strings.Contains(result.response.Error, "injected dispatch boundary failure") || len(readLog("mutations")) != 0 {
			t.Fatalf("failed start boundary permitted input: response=%+v mutations=%q", result.response, readLog("mutations"))
		}
		if op, err := store.GetSendOperation(operationID, session); err != nil || op != nil {
			t.Fatalf("failed start did not release its unstarted claim: (%+v, %v)", op, err)
		}
		execSQL(`DROP TRIGGER fail_operation_start`)
		assertDelivered(invoke(), state.SendOperationCompleted)
	case "completion-failure":
		execSQL(`CREATE TRIGGER fail_operation_completion BEFORE UPDATE OF status ON send_operations WHEN NEW.status = 'completed' BEGIN SELECT RAISE(ABORT, 'injected receipt persistence failure'); END`)
		result := invoke()
		assertDelivered(result, state.SendOperationInProgress)
		if len(result.warnings) == 0 || !strings.Contains(strings.Join(result.warnings, " "), "outcome not recorded") {
			t.Fatalf("lost completion did not report its persistence warning: %+v", result)
		}
		mutations := readLog("mutations")
		execSQL(`DROP TRIGGER fail_operation_completion`)
		execSQL(`UPDATE send_operations SET created_at = ? WHERE operation_id = ? AND session_name = ?`,
			time.Now().UTC().Add(-2*operationStaleClaimWindow), operationID, session)
		original := readOperation()
		t.Setenv("NTM_OPERATION_PANE_ID", "%8")
		t.Setenv("NTM_OPERATION_PANE_INDEX", "2")
		assertRefusedWithoutInput(original, ErrCodeOperationOutcomeUnknown, mutations)
	default:
		t.Fatalf("unknown crash scenario %q", scenario)
	}
}
