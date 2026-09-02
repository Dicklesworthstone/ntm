package grok

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunCompletesFromACPTranscript(t *testing.T) {
	transcript := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"},{"id":"xai.api_key"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"grok-session-7"}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"hello "}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"world"}}}}`,
		`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}`,
	}, "\n") + "\n"
	stderr := strings.Repeat("diagnostic ", MaxStderrCaptureBytes/5+4)
	proc := newFakeProcess(strings.NewReader(transcript), strings.NewReader(stderr))
	runner := &fakeRunner{proc: proc}

	result, err := Run(t.Context(), runner, Request{
		Prompt: "secret prompt must not appear in the receipt",
		CWD:    "/work/project",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Success || result.State != StateCompleted || !result.CompletionConfirmed {
		t.Fatalf("result = %+v, want confirmed completion", result)
	}
	if result.ProviderSessionID != "grok-session-7" || result.StopReason != "end_turn" {
		t.Fatalf("provider receipt = %+v", result)
	}
	if result.AssistantTextChunks != 2 || result.AssistantTextBytes != int64(len("hello world")) {
		t.Fatalf("update summary = %+v", result)
	}
	wantOutput := sha256.Sum256([]byte("hello world"))
	if result.OutputSHA256 != hex.EncodeToString(wantOutput[:]) {
		t.Fatalf("output hash = %q", result.OutputSHA256)
	}
	wantStderr := sha256.Sum256([]byte(stderr))
	if result.Stderr.SHA256 != hex.EncodeToString(wantStderr[:]) || !result.Stderr.Truncated {
		t.Fatalf("stderr digest = %+v", result.Stderr)
	}
	if !proc.closed || proc.killCalls != 1 || proc.waitCalls != 1 {
		t.Fatalf("process cleanup = %+v", proc)
	}
	if got := runner.spec; got.Binary != "grok" || got.CWD != "/work/project" || !sameStrings(got.Args, append(append([]string{"--no-auto-update"}, DefaultReadOnlyAutomationPolicyArgs...), "agent", "stdio")) {
		t.Fatalf("start spec = %+v", got)
	}

	requests := decodeRequests(t, proc.stdin.String())
	if len(requests) != 4 {
		t.Fatalf("request count = %d, want 4", len(requests))
	}
	for index, want := range []string{"initialize", "authenticate", "session/new", "session/prompt"} {
		if got := requests[index].Method; got != want {
			t.Fatalf("request[%d].method = %q, want %q", index, got, want)
		}
	}
	if got := nestedString(t, requests[1].Params, "methodId"); got != "cached_token" {
		t.Fatalf("authenticate methodId = %q, want cached_token", got)
	}
	if strings.Contains(string(mustJSON(t, result)), "secret prompt") || strings.Contains(string(mustJSON(t, result)), "diagnostic") {
		t.Fatalf("receipt retained sensitive payload or stderr: %s", mustJSON(t, result))
	}
}

func TestRunPlacesNamedPolicyAndExactModelBeforeACPSubcommand(t *testing.T) {
	proc := newFakeProcess(strings.NewReader(successfulTranscript(`[{"id":"cached_token"}]`)), strings.NewReader(""))
	runner := &fakeRunner{proc: proc}
	_, err := Run(t.Context(), runner, Request{
		Prompt: "hello", CWD: "/repo", Model: "grok-exact-model",
		AutomationPolicyArgs: []string{"--sandbox=read-only", "--permission-mode=dontAsk", "--allow=Read", "--deny=Bash(*)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--no-auto-update", "--sandbox=read-only", "--permission-mode=dontAsk", "--allow=Read", "--deny=Bash(*)", "--model", "grok-exact-model", "agent", "stdio"}
	if !sameStrings(runner.spec.Args, want) {
		t.Fatalf("ACP args = %#v, want %#v", runner.spec.Args, want)
	}
}

func TestRunRejectsUnsafeAutomationPolicyArgument(t *testing.T) {
	_, err := Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(""), strings.NewReader(""))}, Request{
		Prompt: "hello", CWD: "/repo", AutomationPolicyArgs: []string{"--always-approve"},
	})
	assertCode(t, err, ErrInvalidRequest)
}

func TestRunRequiresReadOnlySandboxAndDontAsk(t *testing.T) {
	for _, policy := range [][]string{
		{"--permission-mode=dontAsk", "--allow=Read"},
		{"--sandbox=read-only", "--allow=Read"},
		{"--sandbox=workspace", "--permission-mode=dontAsk", "--allow=Read"},
	} {
		runner := &fakeRunner{proc: newFakeProcess(strings.NewReader(""), strings.NewReader(""))}
		_, err := Run(t.Context(), runner, Request{
			Prompt: "hello", CWD: "/repo", AutomationPolicyArgs: policy,
		})
		assertCode(t, err, ErrInvalidRequest)
		if runner.spec.Binary != "" {
			t.Fatalf("runner started for unsafe policy %#v: %+v", policy, runner.spec)
		}
	}
}

func TestRunRejectsControlCharactersInModel(t *testing.T) {
	runner := &fakeRunner{proc: newFakeProcess(strings.NewReader(""), strings.NewReader(""))}
	_, err := Run(t.Context(), runner, Request{
		Prompt: "hello", CWD: "/repo", Model: "grok-safe\n--always-approve",
	})
	assertCode(t, err, ErrInvalidRequest)
	if runner.spec.Binary != "" {
		t.Fatalf("runner started for invalid model: %+v", runner.spec)
	}
}

func TestRunExposesConcreteReapAndExitEvidence(t *testing.T) {
	proc := newFakeProcess(strings.NewReader(successfulTranscript(`[{"id":"cached_token"}]`)), strings.NewReader(""))
	proc.waitErr = fakeExitError{code: -1}
	result, err := Run(t.Context(), &fakeRunner{proc: proc}, Request{Prompt: "hello", CWD: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if result.CleanupState != "reaped_after_termination" || result.ExitCode == nil || *result.ExitCode != -1 {
		t.Fatalf("cleanup receipt = %+v", result)
	}
}

func TestRunUsesCachedTokenWithoutExposingCredentials(t *testing.T) {
	transcript := successfulTranscript(`[{"id":"cached_token"},{"id":"xai.api_key"}]`)
	proc := newFakeProcess(strings.NewReader(transcript), strings.NewReader(""))
	_, err := Run(t.Context(), &fakeRunner{proc: proc}, Request{Prompt: "hello", CWD: "/repo"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requests := decodeRequests(t, proc.stdin.String())
	if got := nestedString(t, requests[1].Params, "methodId"); got != "cached_token" {
		t.Fatalf("authenticate methodId = %q, want cached_token", got)
	}
	if strings.Contains(proc.stdin.String(), "XAI_API_KEY") || strings.Contains(proc.stdin.String(), "secret") {
		t.Fatalf("ACP wire request leaked a credential marker: %s", proc.stdin.String())
	}
}

func TestRunVerifiesNonceAcrossAssistantChunksWithoutRetainingOutput(t *testing.T) {
	transcript := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"s"}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"NTM_ACK_0123456789ab"}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"cdef0123456789abcdef\n"}}}}`,
		`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}`,
	}, "\n") + "\n"
	result, err := Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(transcript), strings.NewReader(""))}, Request{
		Prompt:        "reply with nonce",
		ExpectedNonce: "NTM_ACK_0123456789abcdef0123456789abcdef",
		CWD:           "/repo",
	})
	if err != nil || !result.AcknowledgementVerified {
		t.Fatalf("result = %+v, err = %v; want verified acknowledgement", result, err)
	}
	if strings.Contains(string(mustJSON(t, result)), "NTM_ACK_0123456789abcdef0123456789abcdef") {
		t.Fatalf("result leaked acknowledgement token: %s", mustJSON(t, result))
	}
}

func TestRunRejectsWeakOrEmbeddedNonceAcknowledgement(t *testing.T) {
	weak := "NTM_ACK_short"
	_, err := Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(successfulTranscript(`[{"id":"cached_token"}]`)), strings.NewReader(""))}, Request{Prompt: "hello", CWD: "/repo", ExpectedNonce: weak})
	assertCode(t, err, ErrInvalidRequest)

	nonce := "NTM_ACK_0123456789abcdef0123456789abcdef"
	transcript := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"s"}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"completed ` + nonce + ` successfully\n"}}}}`,
		`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}`,
	}, "\n") + "\n"
	result, err := Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(transcript), strings.NewReader(""))}, Request{Prompt: "hello", CWD: "/repo", ExpectedNonce: nonce})
	assertCode(t, err, ErrAcknowledgementUnconfirmed)
	if result.Success || result.AcknowledgementVerified || result.CompletionConfirmed != true {
		t.Fatalf("embedded nonce result = %+v, err = %v", result, err)
	}
}

func TestRunDrainsPostResponseUpdatesBeforeReceipt(t *testing.T) {
	reader, writer := io.Pipe()
	proc := newFakeProcess(reader, strings.NewReader(""))
	go func() {
		for _, line := range []string{
			`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
			`{"jsonrpc":"2.0","id":2,"result":{}}`,
			`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"s"}}`,
			`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}`,
		} {
			_, _ = io.WriteString(writer, line+"\n")
		}
		time.Sleep(time.Millisecond)
		_, _ = io.WriteString(writer, `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"late update"}}}}`+"\n")
		_ = writer.Close()
	}()
	result, err := Run(t.Context(), &fakeRunner{proc: proc}, Request{Prompt: "hello", CWD: "/repo", PostResponseQuietWindow: 20 * time.Millisecond})
	if err != nil || result.AssistantTextChunks != 1 || result.AssistantTextBytes != int64(len("late update")) {
		t.Fatalf("late update receipt = %+v, err = %v", result, err)
	}
}

func TestRunRequiresStopReasonForAuthoritativeCompletion(t *testing.T) {
	result, err := Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"s"}}`,
		`{"jsonrpc":"2.0","id":4,"result":{}}`,
	}, "\n")+"\n"), strings.NewReader(""))}, Request{Prompt: "hello", CWD: "/repo"})
	assertCode(t, err, ErrOutcomeUnknown)
	if result.CompletionConfirmed || result.State != StateOutcomeUnknown {
		t.Fatalf("stop-reason-free receipt = %+v", result)
	}
}

func TestMinimalGrokEnvironmentExcludesUnrelatedValues(t *testing.T) {
	env := minimalGrokEnvironment([]string{"Path=C:\\tools", "XAI_API_KEY=secret", "xai_api_key=secret-lowercase", "AWS_SECRET_ACCESS_KEY=must-not-pass", "HTTPS_PROXY=https://user:secret@proxy.example", "http_proxy=http://proxy-token@proxy.example", "NO_PROXY=localhost", "LANG=en_US.UTF-8", "HOME=/tmp/home", "systemroot=C:\\Windows"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "PATH=C:\\tools") || !strings.Contains(joined, "SystemRoot=C:\\Windows") || !strings.Contains(joined, "HOME=/tmp/home") || strings.Contains(strings.ToLower(joined), "xai_api_key") || strings.Contains(joined, "AWS_SECRET") || strings.Contains(joined, "LANG=") || strings.Contains(strings.ToLower(joined), "proxy=") {
		t.Fatalf("minimal environment = %q", joined)
	}
}

func TestRunHashesOnlyNonAssistantUpdateNamesAndProviderUsage(t *testing.T) {
	transcript := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"s"}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_call","arguments":{"api_key":"must-not-retain"}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_result","content":{"text":"must-not-retain"}}}}`,
		`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn","model":"grok-4.6","usage":{"inputTokens":12,"output_tokens":34}}}`,
	}, "\n") + "\n"
	result, err := Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(transcript), strings.NewReader(""))}, Request{Prompt: "hello", CWD: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ToolEventCount != 2 || result.ToolEventsSHA256 != toolEventHash("tool_call", "tool_result") {
		t.Fatalf("tool evidence = %+v", result)
	}
	if result.Model != "grok-4.6" || result.ModelEvidence != "completion_metadata" || result.Usage == nil || result.Usage.InputTokens == nil || *result.Usage.InputTokens != 12 || result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 34 || result.Usage.TotalTokens != nil {
		t.Fatalf("provider metadata = %+v", result)
	}
	serialized := string(mustJSON(t, result))
	if strings.Contains(serialized, "must-not-retain") || strings.Contains(serialized, "api_key") {
		t.Fatalf("receipt retained raw event material: %s", serialized)
	}
}

func TestRunUsesStructuredCatalogOnlyWithExactModelLaunchAssertion(t *testing.T) {
	transcript := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"_x.ai/models/update","params":{"models":[{"id":"grok-4.6"},{"id":"grok-4.5"}]}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"s"}}`,
		`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}`,
	}, "\n") + "\n"
	result, err := Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(transcript), strings.NewReader(""))}, Request{
		Prompt: "hello", CWD: "/repo", Model: "grok-4.6",
	})
	if err != nil || result.Model != "" || result.ModelEvidence != "" {
		t.Fatalf("catalog must not confirm session model identity: result=%+v err=%v", result, err)
	}

	result, err = Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(transcript), strings.NewReader(""))}, Request{
		Prompt: "hello", CWD: "/repo", Model: "grok-unlisted",
	})
	if err != nil || result.Model != "" || result.ModelEvidence != "" {
		t.Fatalf("unlisted launch assertion became evidence: result=%+v err=%v", result, err)
	}
}

func TestRunAcceptsOnlySessionBoundVendorModelEvidence(t *testing.T) {
	transcript := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"session-7"}}`,
		`{"jsonrpc":"2.0","method":"_x.ai/session/models/update","params":{"sessionId":"session-7","model":"grok-4.6"}}`,
		`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}`,
	}, "\n") + "\n"
	result, err := Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(transcript), strings.NewReader(""))}, Request{
		Prompt: "hello", CWD: "/repo", Model: "grok-4.6",
	})
	if err != nil || result.Model != "grok-4.6" || result.ModelEvidence != "provider_session_notification_plus_exact_launch" {
		t.Fatalf("session-bound model evidence result=%+v err=%v", result, err)
	}
}

func TestRunRejectsUnstructuredOrUnrelatedVendorModelSessionFields(t *testing.T) {
	for name, notification := range map[string]string{
		"split_list_elements": `{"jsonrpc":"2.0","method":"_x.ai/session/models/update","params":{"sessions":[{"sessionId":"session-7"},{"model":"grok-4.6"}]}}`,
		"global_index":        `{"jsonrpc":"2.0","method":"_x.ai/models/update","params":{"sessionId":"session-7","model":"grok-4.6"}}`,
		"nested_session_only": `{"jsonrpc":"2.0","method":"_x.ai/session/models/update","params":{"sessionId":"other-session","model":"grok-4.6","index":{"sessionId":"session-7"}}}`,
		"nested_model_only":   `{"jsonrpc":"2.0","method":"_x.ai/session/models/update","params":{"sessionId":"session-7","model":"other-model","index":{"model":"grok-4.6"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			transcript := strings.Join([]string{
				`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
				`{"jsonrpc":"2.0","id":2,"result":{}}`,
				`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"session-7"}}`,
				notification,
				`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}`,
			}, "\n") + "\n"
			result, err := Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(transcript), strings.NewReader(""))}, Request{
				Prompt: "hello", CWD: "/repo", Model: "grok-4.6",
			})
			if err != nil || result.Model != "" || result.ModelEvidence != "" {
				t.Fatalf("unstructured vendor fields became identity evidence: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestRunAllowsOnlyKnownNoIDHousekeepingNotification(t *testing.T) {
	nonce := "NTM_ACK_0123456789abcdef0123456789abcdef"
	transcript := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"_x.ai/mcp/servers_updated","params":{"servers":[]}}`,
		`{"jsonrpc":"2.0","method":"_x.ai/models/update","params":{"models":[]}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"s"}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"` + nonce + `\n"}}}}`,
		`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}`,
		`{"jsonrpc":"2.0","method":"_x.ai/mcp/servers_updated","params":{"servers":[]}}`,
	}, "\n") + "\n"
	result, err := Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(transcript), strings.NewReader(""))}, Request{
		Prompt: "hello", ExpectedNonce: nonce, CWD: "/repo", PostResponseQuietWindow: time.Millisecond,
	})
	if err != nil || !result.AcknowledgementVerified {
		t.Fatalf("known housekeeping notification result=%+v err=%v", result, err)
	}

	serverRequest := strings.Join([]string{
		`{"jsonrpc":"2.0","id":99,"method":"_x.ai/mcp/servers_updated","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
	}, "\n") + "\n"
	_, err = Run(t.Context(), &fakeRunner{proc: newFakeProcess(strings.NewReader(serverRequest), strings.NewReader(""))}, Request{Prompt: "hello", CWD: "/repo"})
	assertCode(t, err, ErrProtocol)
}

func TestRunFailsClosedWhenAuthMethodUnavailable(t *testing.T) {
	proc := newFakeProcess(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"browser_login"}]}}`+"\n"), strings.NewReader(""))
	result, err := Run(t.Context(), &fakeRunner{proc: proc}, Request{Prompt: "hello", CWD: "/repo"})
	assertCode(t, err, ErrCachedAuthUnavailable)
	if result.State != StateFailed || result.ProviderSessionID != "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunRejectsOversizedProtocolLine(t *testing.T) {
	overseized := `{"jsonrpc":"2.0","id":1,"result":{"padding":"` + strings.Repeat("x", MaxProtocolLineBytes) + `"}}` + "\n"
	proc := newFakeProcess(strings.NewReader(overseized), strings.NewReader(""))
	_, err := Run(t.Context(), &fakeRunner{proc: proc}, Request{Prompt: "hello", CWD: "/repo"})
	assertCode(t, err, ErrProtocol)
}

func TestRunTimeoutBeforePromptIsSafelyAborted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	proc := newBlockingTranscriptProcess([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
	})
	proc.onWrite = func(message wireRequest) {
		if message.Method == "authenticate" {
			cancel()
		}
	}
	result, err := Run(ctx, &fakeRunner{proc: proc}, Request{Prompt: "hello", CWD: "/repo"})
	assertCode(t, err, ErrTimeout)
	if result.State != StateAbortedSafe || result.ProviderSessionID != "" {
		t.Fatalf("result = %+v, want safe pre-prompt abort", result)
	}
}

func TestRunTimeoutAfterPromptWriteIsOutcomeUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	proc := newBlockingTranscriptProcess([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"provider-session"}}`,
	})
	proc.onWrite = func(message wireRequest) {
		if message.Method == "session/prompt" {
			cancel()
		}
	}
	result, err := Run(ctx, &fakeRunner{proc: proc}, Request{Prompt: "hello", CWD: "/repo"})
	assertCode(t, err, ErrOutcomeUnknown)
	if result.State != StateOutcomeUnknown || result.ProviderSessionID != "provider-session" {
		t.Fatalf("result = %+v, want outcome unknown after prompt write", result)
	}
}

func TestRunCancellationBeforePromptWritesNoBytesIsSafelyAborted(t *testing.T) {
	proc := newFakeProcess(strings.NewReader(successfulTranscript(`[{"id":"cached_token"}]`)), strings.NewReader(""))
	proc.beforeWrite = func(message wireRequest) error {
		if message.Method == "session/prompt" {
			return context.Canceled
		}
		return nil
	}
	result, err := Run(t.Context(), &fakeRunner{proc: proc}, Request{Prompt: "hello", CWD: "/repo"})
	assertCode(t, err, ErrTimeout)
	if result.State != StateAbortedSafe || result.ProviderSessionID != "s" {
		t.Fatalf("result = %+v, want safe abort after a proven zero-byte prompt write", result)
	}
	requests := decodeRequests(t, proc.stdin.String())
	if len(requests) != 3 || requests[2].Method != "session/new" {
		t.Fatalf("requests = %+v, want no session/prompt bytes", requests)
	}
}

type fakeRunner struct {
	proc *fakeProcess
	spec StartSpec
	err  error
}

func (r *fakeRunner) Start(_ context.Context, spec StartSpec) (Process, error) {
	r.spec = spec
	if r.err != nil {
		return nil, r.err
	}
	return r.proc, nil
}

type fakeProcess struct {
	stdin       *recordingWriteCloser
	stdout      io.Reader
	stderr      io.Reader
	beforeWrite func(wireRequest) error
	onWrite     func(wireRequest)
	mu          sync.Mutex
	closed      bool
	killCalls   int
	waitCalls   int
	kill        func()
	waitErr     error
}

func newFakeProcess(stdout, stderr io.Reader) *fakeProcess {
	return &fakeProcess{stdin: &recordingWriteCloser{}, stdout: stdout, stderr: stderr}
}

func newBlockingTranscriptProcess(lines []string) *fakeProcess {
	reader, writer := io.Pipe()
	proc := newFakeProcess(reader, strings.NewReader(""))
	proc.kill = func() { _ = writer.Close() }
	go func() {
		for _, line := range lines {
			_, _ = io.WriteString(writer, line+"\n")
		}
		// Keep the pipe open until Kill. This models an ACP child that accepted
		// a request but has not yet produced the next response.
	}()
	return proc
}

func (p *fakeProcess) Stdin() io.WriteCloser { return fakeStdin{parent: p} }
func (p *fakeProcess) Stdout() io.Reader     { return p.stdout }
func (p *fakeProcess) Stderr() io.Reader     { return p.stderr }
func (p *fakeProcess) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waitCalls++
	return p.waitErr
}

type fakeExitError struct{ code int }

func (e fakeExitError) Error() string { return "process exited" }
func (e fakeExitError) ExitCode() int { return e.code }
func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	p.killCalls++
	kill := p.kill
	p.mu.Unlock()
	if kill != nil {
		kill()
	}
	return nil
}

type fakeStdin struct{ parent *fakeProcess }

func (s fakeStdin) Write(data []byte) (int, error) {
	var message wireRequest
	_ = json.Unmarshal(bytes.TrimSpace(data), &message)
	if s.parent.beforeWrite != nil {
		if err := s.parent.beforeWrite(message); err != nil {
			return 0, err
		}
	}
	n, err := s.parent.stdin.Write(data)
	if err != nil {
		return n, err
	}
	if json.Unmarshal(bytes.TrimSpace(data), &message) == nil && s.parent.onWrite != nil {
		s.parent.onWrite(message)
	}
	return n, nil
}

func (s fakeStdin) Close() error {
	s.parent.mu.Lock()
	s.parent.closed = true
	s.parent.mu.Unlock()
	return nil
}

type recordingWriteCloser struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *recordingWriteCloser) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(data)
}

func (w *recordingWriteCloser) Close() error { return nil }

func (w *recordingWriteCloser) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

type wireRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func decodeRequests(t *testing.T, raw string) []wireRequest {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	requests := make([]wireRequest, 0, len(lines))
	for _, line := range lines {
		var request wireRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatalf("decode request %q: %v", line, err)
		}
		requests = append(requests, request)
	}
	return requests
}

func nestedString(t *testing.T, raw json.RawMessage, key string) string {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	value, _ := decoded[key].(string)
	return value
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sameStrings(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

func assertCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}

func successfulTranscript(authMethods string) string {
	return strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"authMethods":` + authMethods + `}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"sessionId":"s"}}`,
		`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}`,
	}, "\n") + "\n"
}

func toolEventHash(names ...string) string {
	hash := sha256.New()
	for _, name := range names {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(name)))
		_, _ = hash.Write(length[:])
		_, _ = io.WriteString(hash, name)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
