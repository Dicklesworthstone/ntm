package contract

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// captureT is a tester shim that records Fatalf invocations instead of
// halting the goroutine, so the assertion-helpers can be exercised both
// for accept and reject paths from this package's own tests. Each
// AssertX returns early after Fatalf, so a single recorded fatal is
// enough to verify the assertion fired without dragging in a panic.
type captureT struct {
	failed    bool
	lastFatal string
}

func (c *captureT) Helper() {}
func (c *captureT) Fatalf(format string, args ...any) {
	c.failed = true
	c.lastFatal = fmt.Sprintf(format, args...)
}

// TestParseEnvelope_ValidJSON covers the happy path: parseable JSON with
// the documented envelope fields populated.
func TestParseEnvelope_ValidJSON(t *testing.T) {
	out := &Outcome{
		Command: "ntm --robot-status",
		Stdout: []byte(`{
  "success": true,
  "timestamp": "2026-05-08T05:00:00Z",
  "version": "1.0.0",
  "sessions": []
}`),
		ExitCode: 0,
	}
	env, err := ParseEnvelope(out)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if !env.Success {
		t.Errorf("Success = false, want true")
	}
	if env.Timestamp != "2026-05-08T05:00:00Z" {
		t.Errorf("Timestamp = %q", env.Timestamp)
	}
	if env.Raw["sessions"] == nil {
		t.Errorf("Raw[sessions] missing")
	}
}

func TestParseEnvelope_EmptyStdoutReturnsError(t *testing.T) {
	out := &Outcome{Command: "ntm --robot-x", Stdout: nil, ExitCode: 1}
	if _, err := ParseEnvelope(out); err == nil {
		t.Fatal("expected error for empty stdout")
	}
}

func TestParseEnvelope_NonJSONStdoutReturnsError(t *testing.T) {
	out := &Outcome{Command: "ntm --robot-x", Stdout: []byte("Error: tmux not installed\n"), ExitCode: 1}
	_, err := ParseEnvelope(out)
	if err == nil {
		t.Fatal("expected error for prose stdout")
	}
	if !strings.Contains(err.Error(), "not parseable JSON") {
		t.Errorf("error = %q, want it to mention parseable JSON", err.Error())
	}
}

// TestAssertSuccessEnvelope_Happy verifies the assertion passes when the
// envelope is well-formed and the exit code agrees.
func TestAssertSuccessEnvelope_Happy(t *testing.T) {
	out := &Outcome{
		Command:  "ntm --robot-status",
		Stdout:   []byte(`{"success":true,"timestamp":"2026-05-08T05:00:00Z","sessions":[]}`),
		ExitCode: 0,
	}
	env := AssertSuccessEnvelope(t, out)
	if env == nil || !env.Success {
		t.Fatalf("envelope = %#v", env)
	}
}

// TestAssertSuccessEnvelope_RejectsLeakedFailureFields asserts the
// helper catches the "success=true but error=... was left set" class of
// responses. A success envelope leaking failure fields is a contract
// violation because agents that switch on error_code first would treat
// the response as a failure.
func TestAssertSuccessEnvelope_RejectsLeakedFailureFields(t *testing.T) {
	out := &Outcome{
		Command:  "ntm --robot-x",
		Stdout:   []byte(`{"success":true,"timestamp":"2026-05-08T05:00:00Z","error":"stale leak","error_code":"WHOOPS"}`),
		ExitCode: 0,
	}
	tt := &captureT{}
	AssertSuccessEnvelope(tt, out)
	if !tt.failed {
		t.Fatal("AssertSuccessEnvelope did not catch leaked failure fields on a success=true envelope")
	}
	if !strings.Contains(tt.lastFatal, "leaked failure fields") {
		t.Errorf("fatal message = %q, want to mention leaked failure fields", tt.lastFatal)
	}
}

// TestAssertFailureEnvelope_Happy verifies the assertion passes for a
// well-formed failure envelope with non-zero exit code.
func TestAssertFailureEnvelope_Happy(t *testing.T) {
	out := &Outcome{
		Command:  "ntm --robot-tail=missing",
		Stdout:   []byte(`{"success":false,"timestamp":"2026-05-08T05:00:00Z","error":"session not found","error_code":"SESSION_NOT_FOUND"}`),
		ExitCode: 1,
	}
	env := AssertFailureEnvelope(t, out, "SESSION_NOT_FOUND")
	if env.Success {
		t.Errorf("envelope.Success = true, want false")
	}
}

// TestAssertFailureEnvelope_RejectsZeroExitOnFailure locks the
// bd-oqwmf / bd-usgfy / bd-ixy2t parity contract: a success:false
// envelope MUST signal non-zero exit. Agents that gate on `$?` (or
// `set -e` shells) silently miss failures otherwise.
func TestAssertFailureEnvelope_RejectsZeroExitOnFailure(t *testing.T) {
	out := &Outcome{
		Command:  "ntm --robot-tail=missing",
		Stdout:   []byte(`{"success":false,"timestamp":"2026-05-08T05:00:00Z","error":"session not found","error_code":"SESSION_NOT_FOUND"}`),
		ExitCode: 0, // bug: failure envelope but exit 0
		Err:      nil,
	}
	tt := &captureT{}
	AssertFailureEnvelope(tt, out, "SESSION_NOT_FOUND")
	if !tt.failed {
		t.Fatal("AssertFailureEnvelope did not catch the bd-oqwmf parity violation (success=false but exit=0)")
	}
	if !strings.Contains(tt.lastFatal, "bd-oqwmf") {
		t.Errorf("fatal message = %q, want to cite bd-oqwmf parity contract", tt.lastFatal)
	}
}

// TestAssertFailureEnvelope_AcceptsErrJSONFailure: in-process tests use
// errors.Is(err, errJSONFailure) to detect the contract; ExitCode is
// often 0 in that path because we never actually exited the process.
// AssertFailureEnvelope should accept Err non-nil as the in-process
// equivalent of non-zero exit.
func TestAssertFailureEnvelope_AcceptsErrJSONFailure(t *testing.T) {
	out := &Outcome{
		Command:  "in-process: runActivity",
		Stdout:   []byte(`{"success":false,"timestamp":"2026-05-08T05:00:00Z","error":"tmux not installed","error_code":"DEPENDENCY_MISSING"}`),
		ExitCode: 0, // in-process: process didn't actually exit
		Err:      errors.New("ntm: command failed (JSON envelope written)"),
	}
	tt := &captureT{}
	AssertFailureEnvelope(tt, out, "")
	if tt.failed {
		t.Fatalf("AssertFailureEnvelope rejected an in-process failure with non-nil Err: %s", tt.lastFatal)
	}
}

// TestAssertFailureEnvelope_RequiresErrorCode locks the contract that
// every failure envelope MUST carry a machine-readable error_code.
// Without it, agents have no programmatic handle to switch on.
func TestAssertFailureEnvelope_RequiresErrorCode(t *testing.T) {
	out := &Outcome{
		Command:  "ntm --robot-x",
		Stdout:   []byte(`{"success":false,"timestamp":"2026-05-08T05:00:00Z","error":"something broke"}`),
		ExitCode: 1,
	}
	tt := &captureT{}
	AssertFailureEnvelope(tt, out, "")
	if !tt.failed {
		t.Fatal("AssertFailureEnvelope did not catch missing error_code")
	}
}

// TestAssertFailureEnvelope_PinsSpecificErrorCode locks the contract
// that callers can pin a specific code expected; mismatched code is a
// regression flag.
func TestAssertFailureEnvelope_PinsSpecificErrorCode(t *testing.T) {
	out := &Outcome{
		Command:  "ntm --robot-x",
		Stdout:   []byte(`{"success":false,"timestamp":"2026-05-08T05:00:00Z","error":"e","error_code":"ACTUAL"}`),
		ExitCode: 1,
	}
	tt := &captureT{}
	AssertFailureEnvelope(tt, out, "EXPECTED")
	if !tt.failed {
		t.Fatal("did not catch error_code mismatch")
	}
	if !strings.Contains(tt.lastFatal, "EXPECTED") {
		t.Errorf("fatal message = %q, want to mention expected code", tt.lastFatal)
	}
}

func TestAssertCriticalArrayPresent_AcceptsEmptyArray(t *testing.T) {
	out := &Outcome{
		Command:  "ntm --robot-status",
		Stdout:   []byte(`{"success":true,"timestamp":"2026-05-08T05:00:00Z","sessions":[]}`),
		ExitCode: 0,
	}
	env, err := ParseEnvelope(out)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	AssertCriticalArrayPresent(t, env, "sessions")
}

func TestAssertCriticalArrayPresent_RejectsAbsent(t *testing.T) {
	out := &Outcome{
		Command:  "ntm --robot-status",
		Stdout:   []byte(`{"success":true,"timestamp":"2026-05-08T05:00:00Z"}`),
		ExitCode: 0,
	}
	env, err := ParseEnvelope(out)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	tt := &captureT{}
	AssertCriticalArrayPresent(tt, env, "sessions")
	if !tt.failed {
		t.Fatal("AssertCriticalArrayPresent did not catch absent field")
	}
}

func TestAssertCriticalArrayPresent_RejectsNull(t *testing.T) {
	out := &Outcome{
		Command:  "ntm --robot-status",
		Stdout:   []byte(`{"success":true,"timestamp":"2026-05-08T05:00:00Z","sessions":null}`),
		ExitCode: 0,
	}
	env, err := ParseEnvelope(out)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	tt := &captureT{}
	AssertCriticalArrayPresent(tt, env, "sessions")
	if !tt.failed {
		t.Fatal("AssertCriticalArrayPresent did not catch null field")
	}
	if !strings.Contains(tt.lastFatal, "null") {
		t.Errorf("fatal message = %q, want to mention null", tt.lastFatal)
	}
}

func TestAssertOptionalFieldOmittedWhenEmpty_AbsentIsFine(t *testing.T) {
	out := &Outcome{
		Stdout:   []byte(`{"success":true,"timestamp":"2026-05-08T05:00:00Z"}`),
		ExitCode: 0,
	}
	env, err := ParseEnvelope(out)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	AssertOptionalFieldOmittedWhenEmpty(t, env, "hint")
}

func TestAssertOptionalFieldOmittedWhenEmpty_RejectsEmptyString(t *testing.T) {
	out := &Outcome{
		Stdout:   []byte(`{"success":true,"timestamp":"2026-05-08T05:00:00Z","hint":""}`),
		ExitCode: 0,
	}
	env, err := ParseEnvelope(out)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	tt := &captureT{}
	AssertOptionalFieldOmittedWhenEmpty(tt, env, "hint")
	if !tt.failed {
		t.Fatal("AssertOptionalFieldOmittedWhenEmpty did not catch empty string")
	}
}

func TestAssertOptionalFieldOmittedWhenEmpty_RejectsEmptyArray(t *testing.T) {
	out := &Outcome{
		Stdout:   []byte(`{"success":true,"timestamp":"2026-05-08T05:00:00Z","warnings":[]}`),
		ExitCode: 0,
	}
	env, err := ParseEnvelope(out)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	tt := &captureT{}
	AssertOptionalFieldOmittedWhenEmpty(tt, env, "warnings")
	if !tt.failed {
		t.Fatal("AssertOptionalFieldOmittedWhenEmpty did not catch empty array")
	}
}

func TestAssertNoStdoutProse_RejectsLeadingProse(t *testing.T) {
	out := &Outcome{
		Stdout: []byte("Warning: cache miss\n{\"success\":true,\"timestamp\":\"2026-05-08T05:00:00Z\"}\n"),
	}
	tt := &captureT{}
	AssertNoStdoutProse(tt, out)
	if !tt.failed {
		t.Fatal("AssertNoStdoutProse did not catch leading prose")
	}
}

func TestAssertNoStdoutProse_RejectsMultipleJSONValues(t *testing.T) {
	out := &Outcome{
		Stdout: []byte(`{"success":true,"timestamp":"2026-05-08T05:00:00Z"}{"oops":"second envelope"}`),
	}
	tt := &captureT{}
	AssertNoStdoutProse(tt, out)
	if !tt.failed {
		t.Fatal("AssertNoStdoutProse did not catch multiple JSON values on stdout")
	}
}

func TestAssertNoStdoutProse_AcceptsCleanEnvelope(t *testing.T) {
	out := &Outcome{
		Stdout: []byte(`{"success":true,"timestamp":"2026-05-08T05:00:00Z","sessions":[]}` + "\n"),
	}
	AssertNoStdoutProse(t, out)
}
