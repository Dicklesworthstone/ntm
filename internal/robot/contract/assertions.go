// Package contract provides reusable assertion helpers for verifying that
// every NTM robot JSON surface obeys the documented envelope contract:
// exit codes match the success boolean, RFC3339 timestamps are present,
// required arrays are never null, error envelopes carry an error_code,
// and stdout is parseable JSON with no prose intermixed.
//
// The helpers are split between Envelope (the parsed shape of a single
// response) and AssertX functions that take a tester and a captured
// command outcome (stdout, stderr, exit code or returned error). Failing
// assertions print the full diagnostic context — command, stdout, stderr,
// parsed JSON, exit code, and the specific contract field that violated —
// so a regression failure is self-explanatory in CI.
//
// Authored for bd-2mb03.3.
package contract

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// tester is the subset of *testing.T the assertion helpers depend on.
// Defined as an interface (not *testing.T) so the helpers can be
// exercised from this package's own tests via a captureT shim that
// records fatal invocations instead of halting the goroutine. *testing.T
// satisfies tester at no cost.
type tester interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Outcome bundles everything a contract assertion needs to know about a
// command invocation. All fields are observed, not configured: tests
// populate Stdout/Stderr from the captured streams, ExitCode from the
// process exit (or sentinel mapping for in-process runs), and Err from
// the run* function's returned error if applicable.
type Outcome struct {
	Command  string // canonical command text, e.g. "ntm --robot-status"
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	// Err is the Go error returned by an in-process run* invocation. May
	// be nil even when ExitCode is non-zero (in-process runs cannot fully
	// emulate process exits). Tests that exercise the binary directly via
	// os/exec leave this nil and use ExitCode.
	Err error
}

// Envelope is the documented robot output envelope. Required fields are
// success and timestamp; the remaining fields are populated as the
// command provides them. Critical arrays should be present (empty []
// when the command has nothing to report) so consumers can iterate
// without nil checks.
type Envelope struct {
	Success      bool            `json:"success"`
	Timestamp    string          `json:"timestamp"`
	Version      string          `json:"version,omitempty"`
	OutputFormat string          `json:"output_format,omitempty"`
	Error        string          `json:"error,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	Hint         string          `json:"hint,omitempty"`
	Raw          map[string]any  `json:"-"`
	Extra        json.RawMessage `json:"-"`
}

// ParseEnvelope decodes outcome.Stdout into an Envelope and the full
// raw object map. Returns an error annotated with the captured stdout
// when the bytes are not parseable JSON — that's a contract violation
// (robot stdout MUST be parseable JSON), not a test infrastructure
// problem. Whitespace/newlines around the JSON body are tolerated;
// any non-JSON prose is rejected.
func ParseEnvelope(out *Outcome) (*Envelope, error) {
	if out == nil {
		return nil, fmt.Errorf("contract.ParseEnvelope: nil outcome")
	}
	body := stripJSONWhitespace(out.Stdout)
	if len(body) == 0 {
		return nil, fmt.Errorf("contract: stdout is empty (command=%q exit=%d stderr=%q)",
			out.Command, out.ExitCode, truncate(string(out.Stderr), 200))
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("contract: stdout is not parseable JSON: %w (command=%q stdout=%q)",
			err, out.Command, truncate(string(body), 400))
	}

	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("contract: stdout JSON does not fit envelope shape: %w (command=%q stdout=%q)",
			err, out.Command, truncate(string(body), 400))
	}
	env.Raw = raw
	env.Extra = json.RawMessage(body)
	return &env, nil
}

// AssertSuccessEnvelope asserts that outcome describes a successful
// command per the robot contract:
//   - Stdout is parseable JSON
//   - success == true
//   - timestamp is non-empty and parses as RFC3339
//   - error / error_code / hint are absent (success responses must not
//     leak failure fields)
//   - exit code is 0 for binary invocations, or Err is nil for in-process
//
// Returns the parsed Envelope on success so the caller can run further
// command-specific assertions on the raw map.
func AssertSuccessEnvelope(t tester, out *Outcome) *Envelope {
	t.Helper()
	env, err := ParseEnvelope(out)
	if err != nil {
		t.Fatalf("AssertSuccessEnvelope: %v", err)
		return nil
	}
	if !env.Success {
		t.Fatalf("AssertSuccessEnvelope: success=false on a path expected to succeed (command=%q error=%q error_code=%q stdout=%q)",
			out.Command, env.Error, env.ErrorCode, truncate(string(out.Stdout), 400))
		return env
	}
	if env.Timestamp == "" {
		t.Fatalf("AssertSuccessEnvelope: missing timestamp (command=%q stdout=%q)", out.Command, truncate(string(out.Stdout), 400))
		return env
	}
	if _, perr := time.Parse(time.RFC3339, env.Timestamp); perr != nil {
		t.Fatalf("AssertSuccessEnvelope: timestamp %q is not RFC3339: %v", env.Timestamp, perr)
		return env
	}
	if env.Error != "" || env.ErrorCode != "" {
		t.Fatalf("AssertSuccessEnvelope: success=true response leaked failure fields (error=%q error_code=%q hint=%q command=%q)",
			env.Error, env.ErrorCode, env.Hint, out.Command)
		return env
	}
	if out.ExitCode != 0 {
		t.Fatalf("AssertSuccessEnvelope: exit code = %d on success response (command=%q)", out.ExitCode, out.Command)
		return env
	}
	return env
}

// AssertFailureEnvelope asserts that outcome describes a failed command
// per the robot contract:
//   - Stdout is parseable JSON
//   - success == false
//   - timestamp is non-empty and parses as RFC3339
//   - error message is non-empty
//   - error_code is non-empty (machine-readable handle for the failure)
//   - exit code is non-zero for binary invocations, or Err is non-nil
//     for in-process (the bd-oqwmf / bd-usgfy / bd-ixy2t parity contract)
//
// The optional wantErrorCode lets callers pin the specific code expected;
// pass "" to accept any non-empty code.
func AssertFailureEnvelope(t tester, out *Outcome, wantErrorCode string) *Envelope {
	t.Helper()
	env, err := ParseEnvelope(out)
	if err != nil {
		t.Fatalf("AssertFailureEnvelope: %v", err)
		return nil
	}
	if env.Success {
		t.Fatalf("AssertFailureEnvelope: success=true on a path expected to fail (command=%q stdout=%q)",
			out.Command, truncate(string(out.Stdout), 400))
		return env
	}
	if env.Timestamp == "" {
		t.Fatalf("AssertFailureEnvelope: missing timestamp (command=%q stdout=%q)", out.Command, truncate(string(out.Stdout), 400))
		return env
	}
	if _, perr := time.Parse(time.RFC3339, env.Timestamp); perr != nil {
		t.Fatalf("AssertFailureEnvelope: timestamp %q is not RFC3339: %v", env.Timestamp, perr)
		return env
	}
	if env.Error == "" {
		t.Fatalf("AssertFailureEnvelope: failure response has empty error message (command=%q stdout=%q)",
			out.Command, truncate(string(out.Stdout), 400))
		return env
	}
	if env.ErrorCode == "" {
		t.Fatalf("AssertFailureEnvelope: failure response has empty error_code — agents cannot programmatically handle (command=%q error=%q stdout=%q)",
			out.Command, env.Error, truncate(string(out.Stdout), 400))
		return env
	}
	if wantErrorCode != "" && env.ErrorCode != wantErrorCode {
		t.Fatalf("AssertFailureEnvelope: error_code = %q, want %q (command=%q error=%q)",
			env.ErrorCode, wantErrorCode, out.Command, env.Error)
		return env
	}
	// bd-oqwmf / bd-usgfy / bd-ixy2t contract: a success:false envelope
	// MUST signal non-zero exit so shell `$?` automation gates correctly.
	if out.ExitCode == 0 && out.Err == nil {
		t.Fatalf("AssertFailureEnvelope: success=false but exit code = 0 and Err = nil (command=%q error=%q error_code=%q) — bd-oqwmf parity broken: shell `$?` automation gates would treat this failure as success",
			out.Command, env.Error, env.ErrorCode)
		return env
	}
	return env
}

// AssertCriticalArrayPresent asserts that the named field exists in the
// raw envelope as a JSON array (possibly empty), not absent and not null.
// Critical arrays are the ones agents iterate without nil checks:
// sessions, panes, targets, agents, etc. Empty [] is the documented
// "checked, found nothing" sentinel; null or absent is a contract
// violation because callers cannot tell "checked vs didn't check".
func AssertCriticalArrayPresent(t tester, env *Envelope, field string) {
	t.Helper()
	if env == nil {
		t.Fatalf("AssertCriticalArrayPresent(%q): nil envelope", field)
		return
	}
	val, ok := env.Raw[field]
	if !ok {
		t.Fatalf("AssertCriticalArrayPresent(%q): field absent from envelope (raw_keys=%v)", field, sortedKeys(env.Raw))
		return
	}
	if val == nil {
		t.Fatalf("AssertCriticalArrayPresent(%q): field is null — must be empty array []", field)
		return
	}
	if _, isSlice := val.([]any); !isSlice {
		t.Fatalf("AssertCriticalArrayPresent(%q): field is %T, want []any (JSON array)", field, val)
		return
	}
}

// AssertOptionalFieldOmittedWhenEmpty asserts the inverse of
// AssertCriticalArrayPresent: an optional documentation/diagnostic field
// (hint, _agent_hints, warnings, notes) MUST be absent from the
// envelope when it has no meaningful value, rather than serialized as
// "" or null. This prevents agents from receiving "looks like there's a
// hint, but it's empty" surfaces.
func AssertOptionalFieldOmittedWhenEmpty(t tester, env *Envelope, field string) {
	t.Helper()
	if env == nil {
		t.Fatalf("AssertOptionalFieldOmittedWhenEmpty(%q): nil envelope", field)
		return
	}
	val, present := env.Raw[field]
	if !present {
		return
	}
	switch v := val.(type) {
	case nil:
		t.Fatalf("AssertOptionalFieldOmittedWhenEmpty(%q): field present as null — must be omitted", field)
		return
	case string:
		if v == "" {
			t.Fatalf("AssertOptionalFieldOmittedWhenEmpty(%q): field present as empty string — must be omitted", field)
			return
		}
	case []any:
		if len(v) == 0 {
			t.Fatalf("AssertOptionalFieldOmittedWhenEmpty(%q): field present as empty array — must be omitted (use AssertCriticalArrayPresent for fields that must always be []) ", field)
			return
		}
	}
}

// AssertNoStdoutProse asserts that stdout contains only a JSON document.
// Robot commands sometimes accidentally print human-readable status
// lines or warnings to stdout alongside the envelope; agents rely on
// stdout being parseable JSON in its entirety. Diagnostic prose belongs
// on stderr.
func AssertNoStdoutProse(t tester, out *Outcome) {
	t.Helper()
	body := stripJSONWhitespace(out.Stdout)
	if len(body) == 0 {
		// Empty stdout is handled by ParseEnvelope; do nothing here.
		return
	}
	if body[0] != '{' && body[0] != '[' {
		t.Fatalf("AssertNoStdoutProse: stdout does not start with '{' or '[' — prose detected (command=%q stdout=%q)",
			out.Command, truncate(string(out.Stdout), 400))
		return
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	var first any
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("AssertNoStdoutProse: stdout JSON decode failed: %v (command=%q)", err, out.Command)
		return
	}
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		t.Fatalf("AssertNoStdoutProse: stdout has multiple JSON values (command=%q stdout=%q) — exactly one envelope per invocation is required",
			out.Command, truncate(string(out.Stdout), 400))
		return
	}
}

func stripJSONWhitespace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...<truncated>"
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
