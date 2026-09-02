// Package grok contains the provider-native Grok transports. It deliberately
// does not depend on tmux: ACP sessions have their own authoritative identity
// and completion receipt.
package grok

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

const (
	// MaxProtocolLineBytes bounds a single JSON-RPC message. ACP is line
	// delimited; accepting an unbounded line would let a faulty child exhaust
	// the NTM process.
	MaxProtocolLineBytes = 1 << 20
	// MaxStderrCaptureBytes bounds the diagnostic material retained from the
	// provider. The stream is still drained beyond this cap so the child cannot
	// block on a full stderr pipe.
	MaxStderrCaptureBytes = 64 << 10
	// DefaultPostResponseQuietWindow captures late session/update events that
	// xAI may emit just after a session/prompt response. It is bounded and
	// remains subject to the caller's context.
	DefaultPostResponseQuietWindow = 100 * time.Millisecond
	maxPostResponseQuietWindow     = 5 * time.Second
)

// ErrorCode is the stable, provider-native failure classification. It is
// intentionally separate from robot error codes; the robot adapter can map it
// without parsing error text.
type ErrorCode string

const (
	ErrInvalidRequest    ErrorCode = "GROK_ACP_INVALID_REQUEST"
	ErrDependencyMissing ErrorCode = "DEPENDENCY_MISSING"
	ErrProtocol          ErrorCode = "GROK_ACP_PROTOCOL_ERROR"
	// ErrCachedAuthUnavailable means the local CLI did not advertise its
	// cached-login authentication method. Automated ACP intentionally never
	// falls back to an exported API key, so an operator must establish a cached
	// CLI login in the local account before retrying.
	ErrCachedAuthUnavailable ErrorCode = "GROK_ACP_CACHED_AUTH_UNAVAILABLE"
	ErrAuthRequired          ErrorCode = "GROK_ACP_AUTH_REQUIRED"
	ErrAuthFailed            ErrorCode = "GROK_ACP_AUTH_FAILED"
	ErrUnsupported           ErrorCode = "GROK_ACP_UNSUPPORTED"
	ErrTimeout               ErrorCode = "TIMEOUT"
	ErrOutcomeUnknown        ErrorCode = "DISPATCH_UNKNOWN"
	ErrProvider              ErrorCode = "GROK_ACP_PROVIDER_ERROR"
	ErrProcessFailed         ErrorCode = "GROK_ACP_PROCESS_FAILED"
	// ErrAcknowledgementUnconfirmed means ACP completed but did not emit the
	// nonce-bound acknowledgement required by the orchestrator's contract.
	ErrAcknowledgementUnconfirmed ErrorCode = "GROK_ACP_ACK_UNCONFIRMED"
	ErrIdentityMismatch           ErrorCode = "GROK_ACP_IDENTITY_MISMATCH"
)

// Error adds a machine-readable classification without exposing protocol
// packets, prompts, credentials, or provider output.
type Error struct {
	Code ErrorCode
	Err  error
}

func (e *Error) Error() string {
	if e == nil || e.Err == nil {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// StartSpec is the narrow process boundary. Credentials are intentionally not
// represented here: automated ACP uses only the CLI's existing cached login,
// and ACP authenticate carries only the cached_token method identifier.
type StartSpec struct {
	Binary string
	Args   []string
	CWD    string
}

// Process is the testable subset of an ACP child process.
type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Wait() error
	Kill() error
}

// Runner starts the provider process. Tests supply a transcript-backed fake;
// OSRunner is the production implementation, but this package never starts a
// process by itself until an adapter calls Run.
type Runner interface {
	Start(context.Context, StartSpec) (Process, error)
}

// OSRunner starts the official local Grok CLI.
type OSRunner struct{}

func (OSRunner) Start(ctx context.Context, spec StartSpec) (Process, error) {
	cmd := exec.CommandContext(ctx, spec.Binary, spec.Args...)
	cmd.Dir = spec.CWD
	cmd.Env = minimalGrokEnvironment(os.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return osProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

type osProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func (p osProcess) Stdin() io.WriteCloser { return p.stdin }
func (p osProcess) Stdout() io.Reader     { return p.stdout }
func (p osProcess) Stderr() io.Reader     { return p.stderr }
func (p osProcess) Wait() error           { return p.cmd.Wait() }
func (p osProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

// Request is one direct, single-prompt ACP execution.
type Request struct {
	Prompt string
	CWD    string
	Binary string
	// Model is passed as an exact --model value before the ACP subcommand. It
	// is not echoed in Result unless the provider's completion metadata confirms
	// the effective model.
	Model string
	// ExpectedNonce is a non-secret acknowledgement token supplied by an
	// orchestrator. It is never retained in Result; only the verified boolean
	// is exposed after a bounded streaming match over assistant chunks.
	ExpectedNonce string
	// PostResponseQuietWindow bounds the drain of updates that are emitted
	// after session/prompt completes. Zero selects the safe default.
	PostResponseQuietWindow time.Duration
	// AutomationPolicyArgs are the named, per-invocation least-privilege
	// --allow/--deny flags placed before `agent stdio`. Raw approval bypasses
	// such as --always-approve are rejected rather than silently accepted.
	AutomationPolicyArgs []string
}

// DefaultReadOnlyAutomationPolicyArgs is the named ACP policy used when an
// adapter has not supplied a narrower profile. Deny wins over allow in Grok,
// so filesystem writes, shell execution, and MCP tools stay unavailable.
var DefaultReadOnlyAutomationPolicyArgs = []string{
	"--sandbox=read-only",
	"--permission-mode=dontAsk",
	"--allow=Read",
	"--allow=Grep",
	"--allow=WebSearch",
	"--deny=Bash(*)",
	"--deny=Edit(*)",
	"--deny=MCPTool(*)",
}

// StderrDigest is safe diagnostic evidence. It contains no stderr body.
type StderrDigest struct {
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	Truncated bool   `json:"truncated"`
}

// Result is a redaction-safe provider receipt. Assistant output is never
// retained, only counted and hashed.
type Result struct {
	Success                 bool         `json:"success"`
	State                   string       `json:"state"`
	FailureCode             ErrorCode    `json:"failure_code,omitempty"`
	ProviderSessionID       string       `json:"provider_session_id,omitempty"`
	StopReason              string       `json:"stop_reason,omitempty"`
	CompletionConfirmed     bool         `json:"completion_confirmed"`
	AcknowledgementVerified bool         `json:"acknowledgement_verified"`
	AssistantTextChunks     int          `json:"assistant_text_chunks"`
	AssistantTextBytes      int64        `json:"assistant_text_bytes"`
	OutputSHA256            string       `json:"output_sha256,omitempty"`
	ToolEventCount          int          `json:"tool_event_count"`
	ToolEventsSHA256        string       `json:"tool_events_sha256"`
	Model                   string       `json:"model,omitempty"`
	ModelEvidence           string       `json:"model_evidence,omitempty"`
	Usage                   *Usage       `json:"usage,omitempty"`
	ExitCode                *int         `json:"exit_code,omitempty"`
	CleanupState            string       `json:"cleanup_state,omitempty"`
	Stderr                  StderrDigest `json:"stderr"`
	StartedAt               time.Time    `json:"started_at"`
	CompletedAt             time.Time    `json:"completed_at"`
}

const (
	StateCompleted      = "completed"
	StateAbortedSafe    = "aborted_safe"
	StateOutcomeUnknown = "outcome_unknown"
	StateFailed         = "failed"
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
}

type rpcEvent struct {
	message rpcMessage
	err     error
}

// Run executes the minimal ACP lifecycle. The caller's context is honored at
// every request boundary. A timeout before session/prompt is safely abortable;
// after the prompt has been accepted, a timeout is explicitly outcome-unknown
// because the provider may continue the already-accepted request.
func Run(ctx context.Context, runner Runner, req Request) (result Result, returnErr error) {
	started := time.Now().UTC()
	result = Result{State: StateFailed, StartedAt: started}
	if ctx == nil {
		return finishFailure(result, ErrInvalidRequest, errors.New("context is required"))
	}
	if runner == nil {
		return finishFailure(result, ErrInvalidRequest, errors.New("runner is required"))
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return finishFailure(result, ErrInvalidRequest, errors.New("prompt is required"))
	}
	if strings.TrimSpace(req.CWD) == "" {
		return finishFailure(result, ErrInvalidRequest, errors.New("cwd is required"))
	}
	if req.ExpectedNonce != "" && !ValidNonce(req.ExpectedNonce) {
		return finishFailure(result, ErrInvalidRequest, errors.New("expected nonce must be an NTM_ACK_ token with at least 128 bits of hex entropy"))
	}
	if err := ctx.Err(); err != nil {
		return finishFailure(result, ErrTimeout, err)
	}
	if strings.TrimSpace(req.Binary) == "" {
		req.Binary = "grok"
	}
	if len(req.AutomationPolicyArgs) == 0 {
		req.AutomationPolicyArgs = append([]string(nil), DefaultReadOnlyAutomationPolicyArgs...)
	}
	if err := validateAutomationPolicyArgs(req.AutomationPolicyArgs); err != nil {
		return finishFailure(result, ErrInvalidRequest, err)
	}
	args := make([]string, 0, 2+len(req.AutomationPolicyArgs)+4)
	args = append(args, "--no-auto-update")
	args = append(args, req.AutomationPolicyArgs...)
	if model := strings.TrimSpace(req.Model); model != "" {
		if strings.IndexFunc(model, unicode.IsControl) >= 0 {
			return finishFailure(result, ErrInvalidRequest, errors.New("model must not contain control characters"))
		}
		args = append(args, "--model", model)
	}
	args = append(args, "agent", "stdio")

	proc, err := runner.Start(ctx, StartSpec{
		Binary: req.Binary,
		Args:   args,
		CWD:    req.CWD,
	})
	if err != nil {
		return finishFailure(result, ErrDependencyMissing, fmt.Errorf("start grok ACP: %w", err))
	}

	stderrDone := make(chan StderrDigest, 1)
	go func() { stderrDone <- drainAndDigest(proc.Stderr(), MaxStderrCaptureBytes) }()
	events := readRPCEvents(proc.Stdout())

	// Closing stdin and reaping the child is mandatory even after a confirmed
	// reply: this is a one-shot adapter, not a daemon owner.
	defer func() {
		_ = proc.Stdin().Close()
		killErr := proc.Kill()
		waitErr := proc.Wait()
		result.CleanupState = "reaped"
		if killErr == nil {
			result.CleanupState = "reaped_after_termination"
		}
		if waitErr != nil {
			if exitCode, ok := exitCodeFromError(waitErr); ok {
				result.ExitCode = &exitCode
			} else {
				result.CleanupState = "wait_failed"
			}
		}
		result.Stderr = <-stderrDone
		result.CompletedAt = time.Now().UTC()
	}()

	// Once session/prompt has been written, the provider may accept it even if
	// the local response is lost. From that point a timeout is never safe to
	// replay automatically.
	promptMayHaveBeenAccepted := false
	outputHasher := sha256.New()
	updates := newUpdateAccumulator(outputHasher, req.ExpectedNonce, strings.TrimSpace(req.Model))
	nextID := 1
	call := func(method string, params any) (json.RawMessage, error) {
		id := nextID
		nextID++
		if err := writeRequest(ctx, proc.Stdin(), id, method, params); err != nil {
			return nil, err
		}
		return waitResponse(ctx, events, id, &updates)
	}

	initRaw, err := call("initialize", initializeParams{
		ProtocolVersion:    1,
		ClientCapabilities: map[string]any{},
	})
	if err != nil {
		return contextAwareFailure(result, promptMayHaveBeenAccepted, err)
	}
	var init initializeResult
	if err := json.Unmarshal(initRaw, &init); err != nil {
		return finishFailure(result, ErrProtocol, fmt.Errorf("decode initialize result: %w", err))
	}
	methodID, err := selectAuthMethod(init.AuthMethods)
	if err != nil {
		return finishFailure(result, ErrCachedAuthUnavailable, err)
	}
	if _, err := call("authenticate", authenticateParams{MethodID: methodID, Meta: map[string]bool{"headless": true}}); err != nil {
		if isRPCError(err) {
			return finishFailure(result, ErrAuthFailed, err)
		}
		return contextAwareFailure(result, promptMayHaveBeenAccepted, err)
	}

	newRaw, err := call("session/new", sessionNewParams{CWD: req.CWD, MCPServers: []any{}})
	if err != nil {
		return contextAwareFailure(result, promptMayHaveBeenAccepted, err)
	}
	var session sessionNewResult
	if err := json.Unmarshal(newRaw, &session); err != nil || strings.TrimSpace(session.SessionID) == "" {
		if err == nil {
			err = errors.New("session/new result omitted sessionId")
		}
		return finishFailure(result, ErrProtocol, fmt.Errorf("decode session/new result: %w", err))
	}
	result.ProviderSessionID = session.SessionID
	updates.providerSessionID = session.SessionID

	promptID := nextID
	nextID++
	promptMayHaveBeenAccepted, err = writeRequestWithEvidence(ctx, proc.Stdin(), promptID, "session/prompt", sessionPromptParams{
		SessionID: session.SessionID,
		Prompt:    []promptPart{{Type: "text", Text: req.Prompt}},
	})
	if err != nil {
		return contextAwareFailure(result, promptMayHaveBeenAccepted, err)
	}
	promptRaw, err := waitResponse(ctx, events, promptID, &updates)
	if err != nil {
		return contextAwareFailure(result, promptMayHaveBeenAccepted, err)
	}
	var prompt sessionPromptResult
	if err := json.Unmarshal(promptRaw, &prompt); err != nil {
		return finishFailure(result, ErrProtocol, fmt.Errorf("decode session/prompt result: %w", err))
	}
	if strings.TrimSpace(prompt.StopReason) == "" {
		return finishFailure(result, ErrOutcomeUnknown, errors.New("session/prompt completion omitted stopReason"))
	}
	if err := drainPostResponseUpdates(ctx, events, &updates, postResponseQuietWindow(req.PostResponseQuietWindow)); err != nil {
		return contextAwareFailure(result, promptMayHaveBeenAccepted, err)
	}
	updates.nonce.Finalize()
	result.CompletionConfirmed = true
	result.StopReason = prompt.StopReason
	result.AssistantTextChunks = updates.chunks
	result.AssistantTextBytes = updates.bytes
	result.AcknowledgementVerified = updates.nonce.verified
	result.OutputSHA256 = hex.EncodeToString(outputHasher.Sum(nil))
	result.ToolEventCount = updates.toolEventCount
	result.ToolEventsSHA256 = hex.EncodeToString(updates.toolEventHasher.Sum(nil))
	result.Model = strings.TrimSpace(prompt.Model)
	if result.Model != "" {
		result.ModelEvidence = "completion_metadata"
	} else if updates.sessionModelObserved {
		result.Model = strings.TrimSpace(req.Model)
		result.ModelEvidence = "provider_session_notification_plus_exact_launch"
	}
	result.Usage = prompt.Usage.canonical()
	if req.ExpectedNonce != "" && !result.AcknowledgementVerified {
		return finishFailure(result, ErrAcknowledgementUnconfirmed, errors.New("session/prompt completion omitted the required standalone nonce acknowledgement"))
	}
	result.Success = true
	result.State = StateCompleted
	return result, nil
}

func postResponseQuietWindow(value time.Duration) time.Duration {
	if value <= 0 {
		return DefaultPostResponseQuietWindow
	}
	if value > maxPostResponseQuietWindow {
		return maxPostResponseQuietWindow
	}
	return value
}

// drainPostResponseUpdates receives all notifications that arrive before one
// complete quiet window. It does not retain their text; updateAccumulator only
// maintains the receipt's hashes, counters, and bounded nonce-line state.
func drainPostResponseUpdates(ctx context.Context, events <-chan rpcEvent, updates *updateAccumulator, quietWindow time.Duration) error {
	timer := time.NewTimer(quietWindow)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if event.err != nil {
				return event.err
			}
			if isBenignACPNotification(event.message) {
				updates.observeVendorNotification(event.message.Method, event.message.Params)
				continue
			}
			if event.message.Method != "session/update" {
				return errors.New("unexpected ACP message after session/prompt response")
			}
			updates.observe(event.message.Params)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quietWindow)
		}
	}
}

func validateAutomationPolicyArgs(args []string) error {
	hasReadOnlySandbox := false
	hasDontAsk := false
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "--sandbox=read-only" {
			hasReadOnlySandbox = true
			continue
		}
		if arg == "--permission-mode=dontAsk" {
			hasDontAsk = true
			continue
		}
		if !strings.HasPrefix(arg, "--allow=") && !strings.HasPrefix(arg, "--deny=") {
			return fmt.Errorf("automation policy argument %q is not a named --allow= or --deny= rule", arg)
		}
		if strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(arg, "--allow="), "--deny=")) == "" {
			return fmt.Errorf("automation policy argument %q has an empty rule", arg)
		}
	}
	if !hasReadOnlySandbox {
		return errors.New("automation policy must select --sandbox=read-only")
	}
	if !hasDontAsk {
		return errors.New("automation policy must select --permission-mode=dontAsk")
	}
	return nil
}

func exitCodeFromError(err error) (int, bool) {
	var exitCoder interface{ ExitCode() int }
	if errors.As(err, &exitCoder) {
		return exitCoder.ExitCode(), true
	}
	return 0, false
}

func finishFailure(result Result, code ErrorCode, err error) (Result, error) {
	result.Success = false
	result.FailureCode = code
	result.State = StateFailed
	if code == ErrTimeout {
		result.State = StateAbortedSafe
	}
	if code == ErrOutcomeUnknown {
		result.State = StateOutcomeUnknown
	}
	if result.CompletedAt.IsZero() {
		result.CompletedAt = time.Now().UTC()
	}
	return result, &Error{Code: code, Err: err}
}

func contextAwareFailure(result Result, promptAccepted bool, err error) (Result, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if promptAccepted {
			return finishFailure(result, ErrOutcomeUnknown, err)
		}
		return finishFailure(result, ErrTimeout, err)
	}
	if isRPCError(err) {
		return finishFailure(result, ErrProvider, err)
	}
	return finishFailure(result, ErrProtocol, err)
}

type initializeParams struct {
	ProtocolVersion    int            `json:"protocolVersion"`
	ClientCapabilities map[string]any `json:"clientCapabilities"`
}

type authMethod struct {
	ID string `json:"id"`
}

type initializeResult struct {
	AuthMethods []authMethod `json:"authMethods"`
}

type authenticateParams struct {
	MethodID string          `json:"methodId"`
	Meta     map[string]bool `json:"_meta"`
}

type sessionNewParams struct {
	CWD        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers"`
}

type sessionNewResult struct {
	SessionID string `json:"sessionId"`
}

type promptPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type sessionPromptParams struct {
	SessionID string       `json:"sessionId"`
	Prompt    []promptPart `json:"prompt"`
}

type sessionPromptResult struct {
	StopReason string      `json:"stopReason"`
	Model      string      `json:"model"`
	Usage      promptUsage `json:"usage"`
}

// Usage records only provider-supplied counters. Nil means the corresponding
// counter was absent, rather than falsely asserting a zero value.
type Usage struct {
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
	TotalTokens  *int64 `json:"total_tokens,omitempty"`
}

type promptUsage struct {
	InputTokens       *int64 `json:"inputTokens"`
	OutputTokens      *int64 `json:"outputTokens"`
	TotalTokens       *int64 `json:"totalTokens"`
	InputTokensSnake  *int64 `json:"input_tokens"`
	OutputTokensSnake *int64 `json:"output_tokens"`
	TotalTokensSnake  *int64 `json:"total_tokens"`
}

func (u promptUsage) canonical() *Usage {
	usage := &Usage{
		InputTokens:  firstNonNilInt64(u.InputTokens, u.InputTokensSnake),
		OutputTokens: firstNonNilInt64(u.OutputTokens, u.OutputTokensSnake),
		TotalTokens:  firstNonNilInt64(u.TotalTokens, u.TotalTokensSnake),
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.TotalTokens == nil {
		return nil
	}
	return usage
}

func firstNonNilInt64(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			copy := *value
			return &copy
		}
	}
	return nil
}

func selectAuthMethod(methods []authMethod) (string, error) {
	available := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		available[method.ID] = struct{}{}
	}
	if _, ok := available["cached_token"]; ok {
		return "cached_token", nil
	}
	return "", errors.New("Grok ACP did not offer cached_token; establish a cached Grok CLI login before automated ACP use")
}

func writeRequest(ctx context.Context, writer io.Writer, id int, method string, params any) error {
	_, err := writeRequestWithEvidence(ctx, writer, id, method, params)
	return err
}

// writeRequestWithEvidence distinguishes a locally proven zero-byte failure
// from a write that may have reached the provider. The latter must be treated
// as replay-unsafe for session/prompt even when the response is lost.
func writeRequestWithEvidence(ctx context.Context, writer io.Writer, id int, method string, params any) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	payload, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return false, fmt.Errorf("encode %s request: %w", method, err)
	}
	payload = append(payload, '\n')
	written, err := writer.Write(payload)
	mayHaveReachedProvider := written > 0
	if err != nil {
		return mayHaveReachedProvider, fmt.Errorf("write %s request: %w", method, err)
	}
	if written != len(payload) {
		return mayHaveReachedProvider, fmt.Errorf("write %s request: %w", method, io.ErrShortWrite)
	}
	return true, nil
}

func readRPCEvents(reader io.Reader) <-chan rpcEvent {
	events := make(chan rpcEvent, 1)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 32<<10), MaxProtocolLineBytes)
		for scanner.Scan() {
			var message rpcMessage
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				events <- rpcEvent{err: fmt.Errorf("decode ACP message: %w", err)}
				return
			}
			if message.JSONRPC != "2.0" {
				events <- rpcEvent{err: fmt.Errorf("invalid ACP jsonrpc version %q", message.JSONRPC)}
				return
			}
			events <- rpcEvent{message: message}
		}
		if err := scanner.Err(); err != nil {
			events <- rpcEvent{err: fmt.Errorf("read ACP message: %w", err)}
		}
	}()
	return events
}

func waitResponse(ctx context.Context, events <-chan rpcEvent, requestID int, updates *updateAccumulator) (json.RawMessage, error) {
	wantID := fmt.Sprintf("%d", requestID)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case event, ok := <-events:
			if !ok {
				return nil, io.EOF
			}
			if event.err != nil {
				return nil, event.err
			}
			if event.message.Method == "session/update" {
				updates.observe(event.message.Params)
				continue
			}
			if isBenignACPNotification(event.message) {
				updates.observeVendorNotification(event.message.Method, event.message.Params)
				continue
			}
			if event.message.Method != "" {
				return nil, fmt.Errorf("unexpected ACP notification %q", event.message.Method)
			}
			if string(event.message.ID) != wantID {
				return nil, fmt.Errorf("unexpected ACP response id %s, want %s", string(event.message.ID), wantID)
			}
			if event.message.Error != nil {
				return nil, &providerError{message: event.message.Error.Message}
			}
			if len(event.message.Result) == 0 {
				return nil, errors.New("ACP response omitted both result and error")
			}
			return event.message.Result, nil
		}
	}
}

// isBenignACPNotification recognizes xAI's vendor-extension notification
// namespace. The CLI emits several evolving housekeeping methods during
// initialization (models, settings, announcements, MCP, and session indexes).
// They are accepted only without a request ID, contribute no completion or
// identity evidence, and cannot widen permissions. Provider-to-client requests
// and non-vendor unknown methods continue to fail closed.
func isBenignACPNotification(message rpcMessage) bool {
	return len(message.ID) == 0 && strings.HasPrefix(message.Method, "_x.ai/")
}

type providerError struct{ message string }

func (e *providerError) Error() string {
	if e == nil || e.message == "" {
		return "Grok ACP provider returned an error"
	}
	return "Grok ACP provider: " + e.message
}

func isRPCError(err error) bool {
	var provider *providerError
	return errors.As(err, &provider)
}

type updateAccumulator struct {
	hasher               io.Writer
	chunks               int
	bytes                int64
	nonce                nonceMatcher
	toolEventHasher      hash.Hash
	toolEventCount       int
	expectedModel        string
	sessionModelObserved bool
	providerSessionID    string
}

func newUpdateAccumulator(hasher io.Writer, nonce, expectedModel string) updateAccumulator {
	return updateAccumulator{
		hasher: hasher, nonce: newNonceMatcher(nonce), toolEventHasher: sha256.New(),
		expectedModel: strings.TrimSpace(expectedModel),
	}
}

// grokSessionModelNotification is the reviewed Grok CLI 1.0.13 notification
// contract for session-scoped model evidence. Other xAI notifications are
// tolerated as housekeeping only; they cannot confirm an operation identity.
const grokSessionModelNotification = "_x.ai/session/models/update"

type grokSessionModelNotificationParams struct {
	SessionID string `json:"sessionId"`
	Model     string `json:"model"`
}

// observeVendorNotification accepts model evidence only from the reviewed
// notification and only when its top-level sessionId and model fields bind the
// same record to this ACP session and exact launch. Raw catalogs, nested
// indexes, and unrelated vendor metadata are intentionally not evidence.
func (a *updateAccumulator) observeVendorNotification(method string, params json.RawMessage) {
	if a == nil || method != grokSessionModelNotification || a.expectedModel == "" || a.providerSessionID == "" {
		return
	}
	var notification grokSessionModelNotificationParams
	if err := json.Unmarshal(params, &notification); err != nil {
		return
	}
	if notification.SessionID == a.providerSessionID && notification.Model == a.expectedModel {
		a.sessionModelObserved = true
	}
}

func (a *updateAccumulator) observe(params json.RawMessage) {
	var envelope struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &envelope) != nil {
		a.recordToolEvent("unknown")
		return
	}
	if envelope.Update.SessionUpdate != "agent_message_chunk" {
		name := envelope.Update.SessionUpdate
		if name == "" {
			name = "unknown"
		}
		a.recordToolEvent(name)
		return
	}
	if envelope.Update.Content.Text == "" {
		return
	}
	_, _ = io.WriteString(a.hasher, envelope.Update.Content.Text)
	a.nonce.WriteString(envelope.Update.Content.Text)
	a.chunks++
	a.bytes += int64(len(envelope.Update.Content.Text))
}

func (a *updateAccumulator) recordToolEvent(name string) {
	if a == nil || a.toolEventHasher == nil {
		return
	}
	// Length-prefixing makes the sequence unambiguous while hashing only the
	// classified sessionUpdate names, never raw params, tool arguments, or
	// tool results.
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(name)))
	_, _ = a.toolEventHasher.Write(length[:])
	_, _ = io.WriteString(a.toolEventHasher, name)
	a.toolEventCount++
}

const maxAcknowledgementLineBytes = 256

// nonceMatcher retains only one bounded candidate line. Acknowledgements are
// accepted only when the trimmed assistant line is exactly the nonce; a nonce
// embedded in prose, a tool result, or another token is never evidence.
type nonceMatcher struct {
	needle   string
	line     []byte
	overlong bool
	verified bool
}

func newNonceMatcher(needle string) nonceMatcher {
	return nonceMatcher{needle: needle, line: make([]byte, 0, len(needle)+8)}
}

func (m *nonceMatcher) WriteString(chunk string) {
	if m == nil || m.needle == "" || m.verified {
		return
	}
	for i := 0; i < len(chunk); i++ {
		if chunk[i] == '\n' {
			m.finishLine()
			continue
		}
		if len(m.line) >= maxAcknowledgementLineBytes {
			m.overlong = true
			continue
		}
		m.line = append(m.line, chunk[i])
	}
}

// Finalize treats a final unterminated assistant line as a line at the end of
// the response stream. This preserves split-chunk matching without requiring
// the provider to append a newline.
func (m *nonceMatcher) Finalize() {
	if m == nil || m.verified {
		return
	}
	m.finishLine()
}

func (m *nonceMatcher) finishLine() {
	if !m.overlong && strings.TrimSpace(string(m.line)) == m.needle {
		m.verified = true
	}
	m.line = m.line[:0]
	m.overlong = false
}

// ValidNonce accepts only the generated-token format: an NTM_ACK_ prefix and
// at least 128 bits (32 lowercase hex characters) of entropy. It rejects
// whitespace, controls, and hand-authored short tokens.
func ValidNonce(value string) bool {
	const prefix = "NTM_ACK_"
	if !strings.HasPrefix(value, prefix) || len(value) < len(prefix)+32 || len(value) > len(prefix)+128 {
		return false
	}
	for _, char := range value[len(prefix):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func minimalGrokEnvironment(environ []string) []string {
	order := []string{
		"PATH", "HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "TMPDIR", "TMP", "TEMP",
		"SystemRoot", "WINDIR", "ComSpec", "PATHEXT",
	}
	allowed := make(map[string]string, len(order))
	allowedFolded := make(map[string]string, len(order))
	for _, canonical := range order {
		allowed[canonical] = canonical
		// The folded lookup handles Windows' case-insensitive environment
		// (notably Path, SystemRoot, and ComSpec) without broadening the
		// allowlist. Proxy variables are intentionally excluded because proxy
		// URLs can embed credentials; automated ACP uses a direct child with no
		// inherited credential-bearing network configuration.
		folded := strings.ToLower(canonical)
		if _, exists := allowedFolded[folded]; !exists {
			allowedFolded[folded] = canonical
		}
	}
	values := make(map[string]string, len(order))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		canonical, ok := allowed[name]
		if !ok {
			canonical, ok = allowedFolded[strings.ToLower(name)]
		}
		if ok {
			values[canonical] = value
		}
	}
	result := make([]string, 0, len(values))
	for _, name := range order {
		if value, ok := values[name]; ok {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func drainAndDigest(reader io.Reader, capBytes int64) StderrDigest {
	if reader == nil {
		return StderrDigest{SHA256: hex.EncodeToString(sha256.New().Sum(nil))}
	}
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
			total += int64(n)
		}
		if err != nil {
			break
		}
	}
	return StderrDigest{Bytes: total, SHA256: hex.EncodeToString(hash.Sum(nil)), Truncated: total > capBytes}
}
