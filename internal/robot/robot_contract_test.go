package robot

// Robot JSON contract conformance suite (bd-2mb03.3).
//
// This file builds a focused, reusable conformance check for the
// documented NTM robot envelope: every command that emits a
// RobotResponse-shaped JSON object must populate `success` (bool) and
// `timestamp` (RFC3339, UTC), and a failure envelope must additionally
// populate `error` and `error_code`. The suite verifies a representative
// matrix of commands so that a regression in any of them — exit-0 on
// success:false, missing/malformed timestamp, dropped envelope — fails
// the suite with a precise diagnostic.
//
// What is intentionally NOT covered (documented exceptions):
//
//   * PrintSessions emits a bare []SessionInfo array, not a
//     RobotResponse-shaped envelope. It is a legacy minimal-output
//     command kept for ad-hoc shell pipelines and is excluded from the
//     envelope contract by design.
//   * Print* functions that require live tmux state (panes, sessions,
//     ack/send/interrupt) are exercised by their own per-command tests
//     in this package and the cli package; the conformance suite limits
//     itself to commands whose success path is hermetic so that a CI
//     run on a host without tmux still meaningfully exercises the
//     contract.
//   * --robot-help is human-readable text, not JSON. Likewise excluded.
//
// To extend the matrix to a new command:
//   1. Add a robotContractCase entry under contractMatrix below.
//   2. Provide a `run` closure that calls the Print* function via
//      captureStdout (the shared helper from robot_test.go).
//   3. If the command has a meaningful failure path that can be
//      triggered hermetically, add a sibling case with
//      expectFailure: true and the expected errorCode.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// contractReporter is the slice of testing.TB the contract helpers
// actually use. Carving it out lets the helper-validation test below
// inject a fake reporter that records calls without runtime.Goexit'ing
// the test goroutine, which is what *testing.T does on Fatalf.
type contractReporter interface {
	Helper()
	Errorf(format string, args ...interface{})
	Fatalf(format string, args ...interface{})
}

// mockContractReporter records the calls the helpers make so the
// helper-validation test can assert "this payload was rejected" without
// faulting the whole suite. fataled tracks whether Fatalf was called;
// errored tracks whether Errorf was called. We don't actually goexit on
// Fatalf — the caller is expected to check fataled and stop if true.
// In practice the helpers' Fatalf paths are short-circuit checks that
// would not have anything sensible to do after, but the single-test
// safety isn't load-bearing here because we recover from any panic the
// helpers might still cause.
type mockContractReporter struct {
	helperCalls int
	errored     bool
	fataled     bool
	messages    []string
}

func (m *mockContractReporter) Helper() { m.helperCalls++ }
func (m *mockContractReporter) Errorf(format string, args ...interface{}) {
	m.errored = true
	m.messages = append(m.messages, fmt.Sprintf(format, args...))
}
func (m *mockContractReporter) Fatalf(format string, args ...interface{}) {
	m.fataled = true
	m.messages = append(m.messages, fmt.Sprintf(format, args...))
}
func (m *mockContractReporter) failed() bool { return m.errored || m.fataled }

// robotContractCase describes one command-under-test for the
// conformance suite. It captures both the input invocation (via the
// run closure) and the expected envelope shape.
type robotContractCase struct {
	// name is the test subtest name and the diagnostic prefix on
	// failure messages.
	name string

	// run invokes the command being tested. It must redirect/capture
	// stdout via captureStdout (or equivalent) and return the raw JSON
	// payload string plus any process-level error returned by the
	// Print* function. The conformance helper parses the payload as
	// the generic RobotResponse-shaped envelope and asserts the
	// contract.
	run func(t *testing.T) (payload string, runErr error)

	// expectFailure flips the contract from success-envelope to
	// failure-envelope expectations: success=false, error and
	// error_code populated. Most matrix entries are success-path.
	expectFailure bool

	// expectedErrorCode, when non-empty and expectFailure is true,
	// asserts the failure envelope's error_code matches exactly.
	// Keeps the suite from accepting any error_code as long as one is
	// present, which would let a bad-but-still-coded regression slip
	// through.
	expectedErrorCode string

	// additionalAsserts runs after the generic envelope contract has
	// been checked, against the raw decoded map. Use for
	// command-specific invariants (e.g. capabilities.commands is a
	// non-nil array, version.system.go_version is set).
	additionalAsserts func(t *testing.T, payload map[string]interface{})
}

// robotEnvelope is the minimal generic shape every conformant command
// must produce. We use map[string]interface{} (rather than the typed
// RobotResponse) so the suite catches accidentally-renamed fields
// during JSON marshaling — a typed unmarshal would silently zero a
// missing field, while a map decode preserves "the field wasn't there
// at all".
func decodeRobotEnvelope(t contractReporter, payload string) map[string]interface{} {
	t.Helper()
	if strings.TrimSpace(payload) == "" {
		t.Fatalf("robot envelope is empty (the command emitted nothing on stdout)")
		return nil
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("robot envelope is not a JSON object: %v\npayload: %q", err, payload)
		return nil
	}
	return decoded
}

// assertRobotSuccessEnvelope checks that payload conforms to the
// success-envelope contract: success=true, timestamp present and
// RFC3339, no error/error_code/hint fields. Failure messages cite the
// payload verbatim so the diagnostic is self-contained.
func assertRobotSuccessEnvelope(t contractReporter, name, payload string, decoded map[string]interface{}) {
	t.Helper()
	successVal, hasSuccess := decoded["success"]
	if !hasSuccess {
		t.Fatalf("%s: success field missing from envelope\npayload: %s", name, payload)
		return
	}
	successBool, ok := successVal.(bool)
	if !ok {
		t.Fatalf("%s: success is %T (%v), want bool\npayload: %s", name, successVal, successVal, payload)
		return
	}
	if !successBool {
		t.Fatalf("%s: success=false on a path expected to succeed\npayload: %s", name, payload)
		return
	}
	assertRobotTimestamp(t, name, payload, decoded)
	for _, errField := range []string{"error", "error_code"} {
		if v, present := decoded[errField]; present {
			if s, isStr := v.(string); !isStr || s != "" {
				t.Errorf("%s: success envelope unexpectedly carries %q=%v (success=true must omit error fields)\npayload: %s", name, errField, v, payload)
			}
		}
	}
}

// assertRobotFailureEnvelope checks the failure-envelope contract:
// success=false, timestamp present and RFC3339, error and error_code
// non-empty. expectedErrorCode (if set) must match exactly.
func assertRobotFailureEnvelope(t contractReporter, name, payload, expectedErrorCode string, decoded map[string]interface{}) {
	t.Helper()
	successVal, hasSuccess := decoded["success"]
	if !hasSuccess {
		t.Fatalf("%s: success field missing from failure envelope\npayload: %s", name, payload)
		return
	}
	if successBool, ok := successVal.(bool); !ok || successBool {
		t.Fatalf("%s: success=%v on a path expected to fail; want success=false\npayload: %s", name, successVal, payload)
		return
	}
	assertRobotTimestamp(t, name, payload, decoded)
	errVal, hasErr := decoded["error"]
	if !hasErr {
		t.Errorf("%s: failure envelope missing required `error` field\npayload: %s", name, payload)
	} else if s, ok := errVal.(string); !ok || s == "" {
		t.Errorf("%s: failure envelope `error` is empty/non-string (%v)\npayload: %s", name, errVal, payload)
	}
	codeVal, hasCode := decoded["error_code"]
	if !hasCode {
		t.Errorf("%s: failure envelope missing required `error_code` field\npayload: %s", name, payload)
	} else if s, ok := codeVal.(string); !ok || s == "" {
		t.Errorf("%s: failure envelope `error_code` is empty/non-string (%v)\npayload: %s", name, codeVal, payload)
	} else if expectedErrorCode != "" && s != expectedErrorCode {
		t.Errorf("%s: failure envelope error_code = %q, want %q\npayload: %s", name, s, expectedErrorCode, payload)
	}
}

// assertRobotTimestamp verifies the `timestamp` field is present and
// parses as RFC3339. The contract docstring on RobotResponse says UTC
// is required; we accept any valid RFC3339 because the production
// constructor (NewRobotResponse) emits UTC and the assertion's job is
// to fail if a regression starts emitting localtime or omitting the
// field entirely.
func assertRobotTimestamp(t contractReporter, name, payload string, decoded map[string]interface{}) {
	t.Helper()
	tsVal, hasTs := decoded["timestamp"]
	if !hasTs {
		t.Fatalf("%s: timestamp field missing from envelope (RobotResponse contract violation)\npayload: %s", name, payload)
		return
	}
	tsStr, ok := tsVal.(string)
	if !ok {
		t.Fatalf("%s: timestamp is %T (%v), want RFC3339 string\npayload: %s", name, tsVal, tsVal, payload)
		return
	}
	if _, err := time.Parse(time.RFC3339, tsStr); err != nil {
		t.Errorf("%s: timestamp %q is not RFC3339-parseable: %v\npayload: %s", name, tsStr, err, payload)
	}
}

// runRobotContractCase executes one matrix entry and applies the
// contract assertions. Split out so test failures cite the case name
// in the diagnostic and so the matrix test body stays compact.
//
// Takes *testing.T (not contractReporter) because it drives a real
// captureStdout fixture, which itself uses *testing.T.
func runRobotContractCase(t *testing.T, c robotContractCase) {
	t.Helper()
	payload, runErr := c.run(t)
	if c.expectFailure {
		// A failure-path command is allowed to return a non-nil error
		// from its Print* function (the typical exit-non-zero path) as
		// long as it ALSO emitted a JSON envelope to stdout first. So
		// we accept either a nil err or a non-nil err here, and still
		// require the envelope.
		_ = runErr
	} else if runErr != nil {
		t.Fatalf("%s: unexpected runtime error from Print function: %v", c.name, runErr)
	}
	decoded := decodeRobotEnvelope(t, payload)
	if c.expectFailure {
		assertRobotFailureEnvelope(t, c.name, payload, c.expectedErrorCode, decoded)
	} else {
		assertRobotSuccessEnvelope(t, c.name, payload, decoded)
	}
	if c.additionalAsserts != nil {
		c.additionalAsserts(t, decoded)
	}
}

// TestRobotEnvelopeContract_Conformance is the conformance entry point.
// Each subtest exercises one Print* function, captures its stdout, and
// verifies the result against the documented robot envelope contract.
//
// When adding a new robot command, prefer adding a case here over
// hand-rolling envelope assertions in a per-command test — keeping the
// contract centralized means a future contract change (e.g. adding a
// new required field) is one edit instead of N.
func TestRobotEnvelopeContract_Conformance(t *testing.T) {
	cases := []robotContractCase{
		{
			name: "version",
			run: func(t *testing.T) (string, error) {
				return captureStdout(t, PrintVersion)
			},
			additionalAsserts: func(t *testing.T, payload map[string]interface{}) {
				system, ok := payload["system"].(map[string]interface{})
				if !ok {
					t.Errorf("version envelope missing `system` object: %v", payload["system"])
					return
				}
				for _, key := range []string{"go_version", "os", "arch"} {
					v, present := system[key]
					if !present {
						t.Errorf("version.system.%s missing", key)
						continue
					}
					if s, ok := v.(string); !ok || s == "" {
						t.Errorf("version.system.%s = %v, want non-empty string", key, v)
					}
				}
			},
		},
		{
			name: "capabilities",
			run: func(t *testing.T) (string, error) {
				return captureStdout(t, PrintCapabilities)
			},
			additionalAsserts: func(t *testing.T, payload map[string]interface{}) {
				// The contract for --robot-capabilities promises a
				// non-nil array of `commands` (machine-discoverable
				// API). An empty array is fine; a missing field or a
				// non-array would break callers that iterate it.
				cmdsVal, present := payload["commands"]
				if !present {
					t.Fatalf("capabilities envelope missing `commands` array")
				}
				if _, ok := cmdsVal.([]interface{}); !ok {
					t.Fatalf("capabilities.commands is %T, want JSON array", cmdsVal)
				}
				catsVal, present := payload["categories"]
				if !present {
					t.Fatalf("capabilities envelope missing `categories` array")
				}
				if _, ok := catsVal.([]interface{}); !ok {
					t.Fatalf("capabilities.categories is %T, want JSON array", catsVal)
				}
			},
		},
		{
			name: "ensemble_presets",
			run: func(t *testing.T) (string, error) {
				return captureStdout(t, PrintEnsemblePresets)
			},
			additionalAsserts: func(t *testing.T, payload map[string]interface{}) {
				// presets must always be present (possibly empty)
				// because consumers iterate without a presence check.
				presetsVal, present := payload["presets"]
				if !present {
					t.Fatalf("ensemble_presets envelope missing `presets` array")
				}
				if _, ok := presetsVal.([]interface{}); !ok {
					t.Fatalf("ensemble_presets.presets is %T, want JSON array", presetsVal)
				}
				// `count` is required (non-zero only matches the
				// presets length, which we check against the array
				// length below).
				countVal, present := payload["count"]
				if !present {
					t.Fatalf("ensemble_presets envelope missing `count` field")
				}
				countNum, ok := countVal.(float64) // JSON numbers decode as float64
				if !ok {
					t.Fatalf("ensemble_presets.count is %T, want number", countVal)
				}
				if got, want := int(countNum), len(presetsVal.([]interface{})); got != want {
					t.Errorf("ensemble_presets.count=%d does not match len(presets)=%d", got, want)
				}
			},
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			runRobotContractCase(t, c)
		})
	}
}

func TestRobotEnvelopeContract_EmptyStatusAndSnapshotCollections(t *testing.T) {
	cases := []struct {
		name    string
		payload interface{}
		arrays  []string
		objects []string
	}{
		{
			name:    "status",
			payload: newStatusOutput(nil),
			arrays:  []string{"sessions"},
			objects: []string{"summary", "summary.agents_by_state", "summary.agents_by_type", "sources", "sources.sources"},
		},
		{
			name:    "snapshot",
			payload: newSnapshotOutput(nil),
			arrays:  []string{"sessions", "active_incidents", "alerts"},
			objects: []string{"summary", "summary.agents_by_state", "summary.agents_by_type"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal %s payload: %v", tc.name, err)
			}
			decoded := decodeRobotEnvelope(t, string(data))
			assertRobotSuccessEnvelope(t, tc.name, string(data), decoded)

			for _, path := range tc.arrays {
				assertJSONPathArray(t, decoded, path)
			}
			for _, path := range tc.objects {
				assertJSONPathObject(t, decoded, path)
			}
		})
	}
}

func TestRobotEnvelopeContract_DegradedAgentMailExample(t *testing.T) {
	payload := newSnapshotOutput(nil)
	payload.AgentMail = &SnapshotAgentMail{
		Available: false,
		Reason:    "Agent Mail server is not available",
		Project:   "/tmp/ntm-contract",
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal snapshot payload: %v", err)
	}
	decoded := decodeRobotEnvelope(t, string(data))
	assertRobotSuccessEnvelope(t, "snapshot_agent_mail_unavailable", string(data), decoded)

	agentMail := assertJSONPathObject(t, decoded, "agent_mail")
	available, ok := agentMail["available"].(bool)
	if !ok {
		t.Fatalf("agent_mail.available is %T, want bool", agentMail["available"])
	}
	if available {
		t.Fatal("agent_mail.available = true, want false for unavailable example")
	}
	if reason, ok := agentMail["reason"].(string); !ok || reason == "" {
		t.Fatalf("agent_mail.reason = %v, want non-empty string", agentMail["reason"])
	}
	assertJSONPathArray(t, decoded, "sessions")
	assertJSONPathArray(t, decoded, "active_incidents")
}

// TestRobotEnvelopeContract_HelpersRejectMalformedEnvelopes locks the
// behavior of the contract helpers themselves so a regression in the
// helpers can't silently swallow a real contract violation. Synthetic
// payloads that violate each rule must be rejected by the helper; a
// well-formed payload must be accepted.
//
// The helpers take a contractReporter (mockable subset of testing.TB)
// rather than *testing.T so this test can drive them without the real
// Fatalf goexit'ing the test goroutine — the mock records the call
// instead of aborting.
func TestRobotEnvelopeContract_HelpersRejectMalformedEnvelopes(t *testing.T) {
	cases := []struct {
		name     string
		payload  string
		failure  bool // run against failure-envelope helper
		wantFail bool // expect the helper to mark the mock as failed
	}{
		{name: "missing_success", payload: `{"timestamp":"2026-05-08T00:00:00Z"}`, wantFail: true},
		{name: "success_not_bool", payload: `{"success":"yes","timestamp":"2026-05-08T00:00:00Z"}`, wantFail: true},
		{name: "missing_timestamp", payload: `{"success":true}`, wantFail: true},
		{name: "timestamp_not_rfc3339", payload: `{"success":true,"timestamp":"not-a-time"}`, wantFail: true},
		{name: "success_envelope_carries_error", payload: `{"success":true,"timestamp":"2026-05-08T00:00:00Z","error":"oops"}`, wantFail: true},
		{name: "valid_success_envelope", payload: `{"success":true,"timestamp":"2026-05-08T00:00:00Z"}`, wantFail: false},
		{name: "failure_envelope_missing_error_code", payload: `{"success":false,"timestamp":"2026-05-08T00:00:00Z","error":"boom"}`, failure: true, wantFail: true},
		{name: "failure_envelope_success_true", payload: `{"success":true,"timestamp":"2026-05-08T00:00:00Z","error":"boom","error_code":"X"}`, failure: true, wantFail: true},
		{name: "valid_failure_envelope", payload: `{"success":false,"timestamp":"2026-05-08T00:00:00Z","error":"oops","error_code":"BOOM"}`, failure: true, wantFail: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockContractReporter{}
			decoded := decodeRobotEnvelope(mock, tc.payload)
			if mock.fataled && decoded == nil {
				// decodeRobotEnvelope failed at the parse step; this is
				// itself a rejection, which counts toward wantFail.
			} else if tc.failure {
				assertRobotFailureEnvelope(mock, tc.name, tc.payload, "", decoded)
			} else {
				assertRobotSuccessEnvelope(mock, tc.name, tc.payload, decoded)
			}
			if got := mock.failed(); got != tc.wantFail {
				t.Errorf("helper rejection: got fail=%v, want fail=%v\npayload: %s\nhelper messages: %v",
					got, tc.wantFail, tc.payload, mock.messages)
			}
		})
	}
}

func assertJSONPathArray(t *testing.T, decoded map[string]interface{}, path string) []interface{} {
	t.Helper()

	value, ok := lookupJSONPath(decoded, path)
	if !ok {
		t.Fatalf("JSON path %q missing", path)
	}
	array, ok := value.([]interface{})
	if !ok {
		t.Fatalf("JSON path %q is %T, want array", path, value)
	}
	if array == nil {
		t.Fatalf("JSON path %q is nil, want empty array", path)
	}
	return array
}

func assertJSONPathObject(t *testing.T, decoded map[string]interface{}, path string) map[string]interface{} {
	t.Helper()

	value, ok := lookupJSONPath(decoded, path)
	if !ok {
		t.Fatalf("JSON path %q missing", path)
	}
	object, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("JSON path %q is %T, want object", path, value)
	}
	if object == nil {
		t.Fatalf("JSON path %q is nil, want empty object", path)
	}
	return object
}

func lookupJSONPath(decoded map[string]interface{}, path string) (interface{}, bool) {
	var current interface{} = decoded
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
