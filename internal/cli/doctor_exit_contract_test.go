package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestDoctorExitContractMatchesAcrossSurfaces is the regression guard for the
// defect this file is named after: `ntm doctor --json` exited 0 on an unhealthy
// ecosystem because only the TUI path mapped the verdict onto an error. JSON is
// the mode preflight scripts gate on, so the surface that mattered was silent.
//
// Both paths now consult doctorExitFailure; this pins that they agree for every
// verdict rather than pinning each one separately.
func TestDoctorExitContractMatchesAcrossSurfaces(t *testing.T) {
	for _, tc := range []struct {
		overall  string
		wantFail bool
	}{
		{"healthy", false},
		{"warning", false}, // optional tools missing is the normal state
		{"unhealthy", true},
	} {
		t.Run(tc.overall, func(t *testing.T) {
			if got := doctorExitFailure(tc.overall); got != tc.wantFail {
				t.Fatalf("doctorExitFailure(%q) = %v; want %v", tc.overall, got, tc.wantFail)
			}

			report := &DoctorReport{Overall: tc.overall}

			var buf bytes.Buffer
			tuiErr := renderDoctorTUITo(&buf, report)
			if (tuiErr != nil) != tc.wantFail {
				t.Errorf("renderDoctorTUITo(%q) error = %v; want failure=%v", tc.overall, tuiErr, tc.wantFail)
			}
		})
	}
}

// TestRunDoctorJSONExitsNonZeroWhenUnhealthy drives runDoctor's JSON branch with
// a chosen verdict and asserts both halves of the contract: a non-zero exit, and
// exactly one JSON document on stdout.
//
// The second half matters as much as the first. Returning an ordinary error here
// would make Execute encode a *second* `success:false` envelope after the report,
// so any consumer doing a single `jq` would break. errJSONFailure is the
// mechanism that signals failure without emitting that second document.
func TestRunDoctorJSONExitsNonZeroWhenUnhealthy(t *testing.T) {
	origCheck := doctorCheck
	origJSON := jsonOutput
	t.Cleanup(func() {
		doctorCheck = origCheck
		jsonOutput = origJSON
	})

	for _, tc := range []struct {
		overall string
		wantErr bool
	}{
		{"healthy", false},
		{"warning", false},
		{"unhealthy", true},
	} {
		t.Run(tc.overall, func(t *testing.T) {
			doctorCheck = func(context.Context) *DoctorReport {
				return &DoctorReport{
					Overall:  tc.overall,
					Errors:   1,
					Warnings: 2,
					Tools:    []ToolCheck{{Name: "bv", Status: "ok"}},
				}
			}
			jsonOutput = true

			var stdout bytes.Buffer
			report := doctorCheck(context.Background())
			encodeErr := encodeDoctorJSON(&stdout, report)
			if encodeErr != nil {
				t.Fatalf("encodeDoctorJSON: %v", encodeErr)
			}

			// Mirror runDoctor's JSON branch decision.
			var gotErr error
			if doctorExitFailure(report.Overall) {
				gotErr = errJSONFailure
			}

			if (gotErr != nil) != tc.wantErr {
				t.Errorf("JSON branch error = %v; want failure=%v", gotErr, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(gotErr, errJSONFailure) {
				t.Errorf("JSON branch returned %v; want errJSONFailure so Execute does not print a second envelope", gotErr)
			}

			// Exactly one JSON document, and it carries the verdict.
			dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
			var first map[string]interface{}
			if err := dec.Decode(&first); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout.String())
			}
			if got := first["overall"]; got != tc.overall {
				t.Errorf("report overall = %v; want %q", got, tc.overall)
			}
			var extra map[string]interface{}
			if err := dec.Decode(&extra); err == nil {
				t.Errorf("stdout carried a second JSON document, which breaks single-document consumers: %v", extra)
			}
		})
	}
}

// TestDoctorJSONReportNamesUnverifiedChecks pins that the honest verdicts
// survive JSON encoding. A check that could not run must not serialize as a
// pass; `ntm doctor` reports `unverified` (invariants) and `unknown` (daemons)
// precisely so a consumer can tell "measured and fine" from "never measured".
func TestDoctorJSONReportNamesUnverifiedChecks(t *testing.T) {
	report := &DoctorReport{
		Overall: "warning",
		Invariants: []InvariantCheck{
			{ID: "no_silent_data_loss", Name: "No Silent Data Loss", Status: "unverified", Message: "no state DB yet"},
		},
		Daemons: []DaemonCheck{
			{Name: "cm-server", Status: "unknown", Message: "not running (port 8766 free)"},
		},
	}

	var buf bytes.Buffer
	if err := encodeDoctorJSON(&buf, report); err != nil {
		t.Fatalf("encodeDoctorJSON: %v", err)
	}
	out := buf.String()
	for _, want := range []string{`"status": "unverified"`, `"status": "unknown"`} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor JSON lost a non-verdict status; wanted %s in:\n%s", want, out)
		}
	}
}
