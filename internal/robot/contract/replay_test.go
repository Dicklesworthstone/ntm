package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const replayManifestPath = "testdata/replay_manifest.json"

func TestReplay_GoldenManifestIsContractClean(t *testing.T) {
	t.Parallel()
	mf, err := LoadReplayManifest(replayManifestPath)
	if err != nil {
		t.Fatalf("LoadReplayManifest: %v", err)
	}
	res := Replay(mf)
	if res.Total != len(mf.Fixtures) {
		t.Errorf("Total = %d, want %d", res.Total, len(mf.Fixtures))
	}
	if res.Passed != res.Total {
		t.Errorf("Passed = %d, want %d. Findings: %+v", res.Passed, res.Total, res.Findings)
	}
}

func TestLoadReplayManifest_RejectsWrongSchemaVersion(t *testing.T) {
	t.Parallel()
	tmp := writeManifest(t, `{"schema_version": 99, "fixtures": [{"name":"x","mode":"success","stdout":{"success":true}}]}`)
	if _, err := LoadReplayManifest(tmp); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("err = %v, want schema_version mismatch", err)
	}
}

func TestLoadReplayManifest_RejectsDuplicateFixtureName(t *testing.T) {
	t.Parallel()
	manifest := `{
		"schema_version": 1,
		"fixtures": [
			{"name":"a","mode":"success","exit_code":0,"command":"ntm x","stdout":{"success":true,"timestamp":"2026-05-08T12:00:00Z"}},
			{"name":"a","mode":"success","exit_code":0,"command":"ntm x","stdout":{"success":true,"timestamp":"2026-05-08T12:00:00Z"}}
		]
	}`
	tmp := writeManifest(t, manifest)
	if _, err := LoadReplayManifest(tmp); err == nil || !strings.Contains(err.Error(), "duplicate name") {
		t.Errorf("err = %v, want duplicate-name error", err)
	}
}

func TestLoadReplayManifest_RejectsInvalidMode(t *testing.T) {
	t.Parallel()
	manifest := `{
		"schema_version": 1,
		"fixtures": [
			{"name":"a","mode":"bogus","exit_code":0,"command":"ntm x","stdout":{"success":true}}
		]
	}`
	tmp := writeManifest(t, manifest)
	if _, err := LoadReplayManifest(tmp); err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Errorf("err = %v, want invalid-mode error", err)
	}
}

func TestReplay_DetectsTamperedSuccessFixture_MissingTimestamp(t *testing.T) {
	t.Parallel()
	// Same shape as the golden success fixture but with timestamp
	// removed; replay should report exactly one finding pointing at
	// the timestamp field.
	mf := &ReplayManifest{
		SchemaVersion: 1,
		Fixtures: []Fixture{{
			Name:    "tampered",
			Command: "ntm x",
			Mode:    FixtureModeSuccess,
			Stdout:  json.RawMessage(`{"success":true,"sessions":[]}`),
		}},
	}
	res := Replay(mf)
	if res.Passed != 0 {
		t.Errorf("Passed = %d, want 0 on tampered fixture", res.Passed)
	}
	if len(res.Findings) == 0 {
		t.Fatal("expected at least one Finding for missing timestamp")
	}
	hasTimestamp := false
	for _, f := range res.Findings {
		if f.Field == "timestamp" {
			hasTimestamp = true
		}
	}
	if !hasTimestamp {
		t.Errorf("Findings = %+v, want one with Field=timestamp", res.Findings)
	}
}

func TestReplay_DetectsFailureFixtureMissingErrorCode(t *testing.T) {
	t.Parallel()
	mf := &ReplayManifest{
		SchemaVersion: 1,
		Fixtures: []Fixture{{
			Name:     "tampered_failure",
			Command:  "ntm x",
			Mode:     FixtureModeFailure,
			ExitCode: 1,
			Stdout:   json.RawMessage(`{"success":false,"timestamp":"2026-05-08T12:00:00Z","error":"boom"}`),
		}},
	}
	res := Replay(mf)
	if res.Passed != 0 {
		t.Errorf("Passed = %d, want 0", res.Passed)
	}
	hasErrorCode := false
	for _, f := range res.Findings {
		if f.Field == "error_code" {
			hasErrorCode = true
		}
	}
	if !hasErrorCode {
		t.Errorf("Findings = %+v, want one with Field=error_code", res.Findings)
	}
}

func TestReplay_DetectsExitCodeContractViolation(t *testing.T) {
	t.Parallel()
	// success=false but exit code 0 violates the bd-oqwmf parity rule.
	mf := &ReplayManifest{
		SchemaVersion: 1,
		Fixtures: []Fixture{{
			Name:     "tampered_exit",
			Command:  "ntm x",
			Mode:     FixtureModeFailure,
			ExitCode: 0,
			Stdout:   json.RawMessage(`{"success":false,"timestamp":"2026-05-08T12:00:00Z","error":"boom","error_code":"X"}`),
		}},
	}
	res := Replay(mf)
	if res.Passed != 0 {
		t.Errorf("Passed = %d, want 0 (exit code violation)", res.Passed)
	}
	hasExit := false
	for _, f := range res.Findings {
		if f.Field == "exit_code" {
			hasExit = true
		}
	}
	if !hasExit {
		t.Errorf("Findings = %+v, want one with Field=exit_code", res.Findings)
	}
}

func TestReplay_DetectsMissingCriticalArray(t *testing.T) {
	t.Parallel()
	mf := &ReplayManifest{
		SchemaVersion: 1,
		Fixtures: []Fixture{{
			Name:           "missing_array",
			Command:        "ntm --robot-status",
			Mode:           FixtureModeSuccess,
			ExitCode:       0,
			CriticalArrays: []string{"sessions", "panes"},
			// "panes" is missing from the envelope.
			Stdout: json.RawMessage(`{"success":true,"timestamp":"2026-05-08T12:00:00Z","sessions":[]}`),
		}},
	}
	res := Replay(mf)
	if res.Passed != 0 {
		t.Errorf("Passed = %d, want 0", res.Passed)
	}
	hasPanes := false
	for _, f := range res.Findings {
		if strings.Contains(f.Field, "panes") || strings.Contains(f.Message, "panes") {
			hasPanes = true
		}
	}
	if !hasPanes {
		t.Errorf("Findings = %+v, want one mentioning panes", res.Findings)
	}
}

func TestReplay_FailForwardsFirstFinding(t *testing.T) {
	t.Parallel()
	mf := &ReplayManifest{
		SchemaVersion: 1,
		Fixtures: []Fixture{{
			Name:    "broken",
			Command: "ntm x",
			Mode:    FixtureModeSuccess,
			Stdout:  json.RawMessage(`{"success":true}`),
		}},
	}
	res := Replay(mf)
	cap := newReplayCapture()
	res.Fail(cap)
	if len(cap.fatals) == 0 {
		t.Fatal("Fail did not forward any finding to the tester")
	}
	if !strings.Contains(cap.fatals[0], "broken") {
		t.Errorf("fatal message = %q, want fixture name 'broken'", cap.fatals[0])
	}
}

func TestReplay_StdoutWithLeadingProseIsRejected(t *testing.T) {
	t.Parallel()
	mf := &ReplayManifest{
		SchemaVersion: 1,
		Fixtures: []Fixture{{
			Name:    "with_prose",
			Command: "ntm x",
			Mode:    FixtureModeSuccess,
			Stdout:  json.RawMessage(`{"success":true,"timestamp":"2026-05-08T12:00:00Z"}`),
		}},
	}
	// Mutate the raw bytes to splice a prose preamble in front of the JSON.
	mf.Fixtures[0].Stdout = json.RawMessage("Some debug log line\n" + string(mf.Fixtures[0].Stdout))
	// The manifest validator will reject if invalid JSON; bypass it
	// by calling Replay directly here.
	res := Replay(mf)
	if res.Passed != 0 {
		t.Errorf("Passed = %d, want 0 for stdout-with-prose", res.Passed)
	}
}

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "replay_manifest.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}
