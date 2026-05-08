package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ReplaySchemaVersion pins the manifest format. Bump on
// backward-incompatible changes; additive fields keep the same value.
const ReplaySchemaVersion = 1

// FixtureMode classifies what envelope shape a captured outcome is
// expected to satisfy.
type FixtureMode string

const (
	// FixtureModeSuccess: success=true, no error fields, exit code 0.
	FixtureModeSuccess FixtureMode = "success"
	// FixtureModeFailure: success=false with an error message, an
	// error_code, and a non-zero exit code (bd-oqwmf parity).
	FixtureModeFailure FixtureMode = "failure"
	// FixtureModeUnavailable: a specific failure shape where the
	// command's required dependency was offline. Same envelope
	// constraints as FixtureModeFailure plus a documented error_code
	// of NOT_IMPLEMENTED / SESSION_NOT_FOUND / similar.
	FixtureModeUnavailable FixtureMode = "unavailable"
)

// Fixture is one captured robot-mode response a replay run will
// re-validate. Stdout is held inline as raw JSON so the manifest
// stays repo-local — small fixtures live in a single file beside the
// harness rather than fanning out into a tree of stdout/stderr
// files.
type Fixture struct {
	// Name is a unique identifier within the manifest.
	Name string `json:"name"`
	// Command is the canonical invocation that produced this
	// outcome, used in assertion failure diagnostics.
	Command string `json:"command"`
	// Mode picks which envelope contract to enforce.
	Mode FixtureMode `json:"mode"`
	// Stdout is the raw JSON body the captured run emitted on
	// stdout. Replay re-parses it through ParseEnvelope.
	Stdout json.RawMessage `json:"stdout"`
	// Stderr is the captured stderr (informational; not validated
	// against the contract since stderr is reserved for diagnostics).
	Stderr string `json:"stderr,omitempty"`
	// ExitCode is the captured process exit code.
	ExitCode int `json:"exit_code"`
	// ExpectedErrorCode constrains AssertFailureEnvelope. Empty
	// means "any non-empty error_code is acceptable".
	ExpectedErrorCode string `json:"expected_error_code,omitempty"`
	// CriticalArrays names every top-level field that must be a
	// (possibly empty) array in the envelope, never null and never
	// absent. Each becomes one AssertCriticalArrayPresent call.
	CriticalArrays []string `json:"critical_arrays,omitempty"`
	// CapabilityName, when set, is the matching name in the
	// capabilities surface. Replay records it but the harness does
	// not load capabilities itself — the test that drives Replay
	// can cross-check.
	CapabilityName string `json:"capability_name,omitempty"`
	// Notes is free-text context for reviewers.
	Notes string `json:"notes,omitempty"`
}

// ReplayManifest is the on-disk JSON shape consumed by LoadReplayManifest.
type ReplayManifest struct {
	SchemaVersion int       `json:"schema_version"`
	Description   string    `json:"description,omitempty"`
	Fixtures      []Fixture `json:"fixtures"`
}

// LoadReplayManifest reads and decodes a manifest file. The schema
// version must equal ReplaySchemaVersion.
func LoadReplayManifest(path string) (*ReplayManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("LoadReplayManifest: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m ReplayManifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("LoadReplayManifest: decode %s: %w", path, err)
	}
	if m.SchemaVersion != ReplaySchemaVersion {
		return nil, fmt.Errorf("LoadReplayManifest: schema_version = %d, want %d", m.SchemaVersion, ReplaySchemaVersion)
	}
	if len(m.Fixtures) == 0 {
		return nil, fmt.Errorf("LoadReplayManifest: %s declares zero fixtures", path)
	}
	if err := validateManifest(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func validateManifest(m *ReplayManifest) error {
	seen := make(map[string]struct{}, len(m.Fixtures))
	for i, f := range m.Fixtures {
		if strings.TrimSpace(f.Name) == "" {
			return fmt.Errorf("fixture[%d]: name empty", i)
		}
		if _, dup := seen[f.Name]; dup {
			return fmt.Errorf("fixture[%d]: duplicate name %q", i, f.Name)
		}
		seen[f.Name] = struct{}{}
		switch f.Mode {
		case FixtureModeSuccess, FixtureModeFailure, FixtureModeUnavailable:
			// ok
		default:
			return fmt.Errorf("fixture[%d] %q: invalid mode %q", i, f.Name, f.Mode)
		}
		if len(f.Stdout) == 0 {
			return fmt.Errorf("fixture[%d] %q: stdout is empty", i, f.Name)
		}
	}
	return nil
}

// ReplayFinding records one assertion failure encountered during a
// Replay call. Empty Findings on every result means the manifest is
// fully contract-clean.
type ReplayFinding struct {
	Fixture string `json:"fixture"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ReplayResult is the aggregated per-manifest outcome.
type ReplayResult struct {
	Total    int             `json:"total"`
	Passed   int             `json:"passed"`
	Findings []ReplayFinding `json:"findings,omitempty"`
}

// Replay runs every fixture's documented contract assertions through
// the existing Assert* helpers in this package, capturing any
// failure into ReplayResult.Findings rather than calling t.Fatal. A
// caller can either:
//
//   - feed a *testing.T directly via tester to make a failure
//     fail the test, OR
//   - capture findings offline (CI artifact analysis) by passing a
//     replayCaptureT and inspecting Findings.
//
// Replay does not call t itself; the tester is forwarded to Assert*
// helpers for diagnostic context only. Callers that want the test
// to fail on findings should call result.Fail(t) afterward.
func Replay(mf *ReplayManifest) ReplayResult {
	res := ReplayResult{Total: len(mf.Fixtures)}
	for _, f := range mf.Fixtures {
		findings := replayOne(f)
		if len(findings) == 0 {
			res.Passed++
			continue
		}
		res.Findings = append(res.Findings, findings...)
	}
	sort.SliceStable(res.Findings, func(i, j int) bool {
		if res.Findings[i].Fixture != res.Findings[j].Fixture {
			return res.Findings[i].Fixture < res.Findings[j].Fixture
		}
		return res.Findings[i].Field < res.Findings[j].Field
	})
	return res
}

// Fail forwards every Finding into the tester, calling Fatalf on the
// first one. Callers that prefer to surface every finding before
// failing can iterate result.Findings directly.
func (r ReplayResult) Fail(t tester) {
	t.Helper()
	if len(r.Findings) == 0 {
		return
	}
	first := r.Findings[0]
	t.Fatalf("contract replay: %d/%d fixtures failed; first: fixture=%q field=%q msg=%s",
		len(r.Findings), r.Total, first.Fixture, first.Field, first.Message)
}

// replayOne runs the contract assertions for one fixture using a
// replayCaptureT shim so failures become Findings instead of t.Fatalf
// halts. Returns one Finding per assertion violation.
func replayOne(f Fixture) []ReplayFinding {
	out := &Outcome{
		Command:  f.Command,
		Stdout:   []byte(f.Stdout),
		Stderr:   []byte(f.Stderr),
		ExitCode: f.ExitCode,
	}

	cap := newReplayCapture()
	switch f.Mode {
	case FixtureModeSuccess:
		AssertSuccessEnvelope(cap, out)
	case FixtureModeFailure, FixtureModeUnavailable:
		AssertFailureEnvelope(cap, out, f.ExpectedErrorCode)
	}
	// AssertNoStdoutProse applies to every mode — the contract says
	// stdout MUST be parseable JSON regardless of success/failure.
	AssertNoStdoutProse(cap, out)

	// Collect Findings from the envelope-level assertions before the
	// critical-array sweep, so a malformed envelope doesn't double-
	// report through the per-array calls.
	findings := capToFindings(f.Name, cap.fatals)

	// Critical-array assertions only make sense if the envelope
	// parsed; if envelope-level assertions already failed, skip the
	// per-array sweep to avoid noise.
	if len(findings) == 0 && len(f.CriticalArrays) > 0 {
		env, err := ParseEnvelope(out)
		if err == nil {
			for _, field := range f.CriticalArrays {
				cap2 := newReplayCapture()
				AssertCriticalArrayPresent(cap2, env, field)
				for _, msg := range cap2.fatals {
					findings = append(findings, ReplayFinding{
						Fixture: f.Name,
						Field:   field,
						Message: msg,
					})
				}
			}
		}
	}
	return findings
}

func capToFindings(fixture string, fatals []string) []ReplayFinding {
	if len(fatals) == 0 {
		return nil
	}
	out := make([]ReplayFinding, 0, len(fatals))
	for _, msg := range fatals {
		out = append(out, ReplayFinding{
			Fixture: fixture,
			Field:   inferField(msg),
			Message: msg,
		})
	}
	return out
}

// inferField picks a short label from an Assert* failure message so
// findings can be sorted by field. Best-effort string parsing — if
// it can't tell, it returns "envelope".
func inferField(msg string) string {
	switch {
	case strings.Contains(msg, "missing timestamp"):
		return "timestamp"
	case strings.Contains(msg, "is not RFC3339"):
		return "timestamp"
	case strings.Contains(msg, "leaked failure fields"):
		return "error"
	case strings.Contains(msg, "exit code"):
		return "exit_code"
	case strings.Contains(msg, "empty error message"):
		return "error"
	case strings.Contains(msg, "empty error_code"):
		return "error_code"
	case strings.Contains(msg, "error_code ="):
		return "error_code"
	case strings.Contains(msg, "stdout"):
		return "stdout"
	case strings.Contains(msg, "AssertSuccessEnvelope") || strings.Contains(msg, "AssertFailureEnvelope"):
		return "envelope"
	default:
		return "envelope"
	}
}

// replayCaptureT records every Fatalf call instead of halting. Replay uses
// it so a malformed fixture does not abort the loop after the first
// finding.
type replayCaptureT struct {
	fatals []string
}

func newReplayCapture() *replayCaptureT { return &replayCaptureT{} }

func (c *replayCaptureT) Helper() {}

func (c *replayCaptureT) Fatalf(format string, args ...any) {
	c.fatals = append(c.fatals, fmt.Sprintf(format, args...))
}
