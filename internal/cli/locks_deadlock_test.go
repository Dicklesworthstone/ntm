package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

var (
	locksDeadlockT0 = time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	locksDeadlockT1 = time.Date(2026, 8, 16, 10, 5, 0, 0, time.UTC)
)

func locksDeadlockReservation(id int, agent, pattern string, created time.Time) agentmail.FileReservation {
	return agentmail.FileReservation{
		ID:          id,
		PathPattern: pattern,
		AgentName:   agent,
		Exclusive:   true,
		Reason:      "test fixture",
		CreatedTS:   agentmail.FlexTime{Time: created},
		ExpiresTS:   agentmail.FlexTime{Time: created.Add(24 * time.Hour)},
	}
}

// locksTwoCycleFixture is a genuine 2-cycle reservation deadlock: each
// agent claimed a path inside territory the other grabbed first.
func locksTwoCycleFixture() []agentmail.FileReservation {
	return []agentmail.FileReservation{
		locksDeadlockReservation(1, "AgentA", "src/a/**", locksDeadlockT0),
		locksDeadlockReservation(2, "AgentB", "src/a/core.go", locksDeadlockT1),
		locksDeadlockReservation(3, "AgentB", "docs/**", locksDeadlockT0),
		locksDeadlockReservation(4, "AgentA", "docs/x.md", locksDeadlockT1),
	}
}

func locksAcyclicFixture() []agentmail.FileReservation {
	return []agentmail.FileReservation{
		locksDeadlockReservation(1, "AgentA", "src/a/**", locksDeadlockT0),
		locksDeadlockReservation(2, "AgentB", "src/a/core.go", locksDeadlockT1),
		locksDeadlockReservation(3, "AgentB", "docs/**", locksDeadlockT0),
	}
}

func TestLocksListCmd_HasCheckDeadlocksFlag(t *testing.T) {
	t.Parallel()

	cmd := newLocksListCmd()
	if cmd.Flags().Lookup("check-deadlocks") == nil {
		t.Fatalf("locks list is missing the --check-deadlocks flag")
	}
}

func TestLocksDeadlockReport_JSONEnvelopeNamesCycle(t *testing.T) {
	t.Parallel()

	now := locksDeadlockT1.Add(time.Minute)
	report := locksDeadlockReport(locksTwoCycleFixture(), now)
	if len(report.Cycles) != 1 {
		t.Fatalf("expected one cycle, got %+v", report.Cycles)
	}

	result := LocksResult{
		Success:      true,
		Session:      "sess",
		ProjectKey:   "/test/project",
		Reservations: locksTwoCycleFixture(),
		Count:        4,
		Deadlocks:    &report,
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshaling LocksResult: %v", err)
	}
	envelope := string(payload)
	t.Logf("robot envelope: %s", envelope)
	for _, want := range []string{`"deadlocks"`, `"cycles"`, `"AgentA"`, `"AgentB"`, `"suggestion"`} {
		if !strings.Contains(envelope, want) {
			t.Fatalf("robot envelope missing %s:\n%s", want, envelope)
		}
	}
}

func TestLocksDeadlockReport_AcyclicAndOmittedWhenUnchecked(t *testing.T) {
	t.Parallel()

	now := locksDeadlockT1.Add(time.Minute)
	report := locksDeadlockReport(locksAcyclicFixture(), now)
	if len(report.Cycles) != 0 {
		t.Fatalf("acyclic fixture must yield no cycles, got %+v", report.Cycles)
	}
	if len(report.Sources) != 1 || report.Sources[0].Name != "agentmail_reservations" {
		t.Fatalf("report must carry its source provenance, got %+v", report.Sources)
	}

	// Without --check-deadlocks the additive envelope key must be absent
	// so existing consumers see byte-identical output.
	unchecked := LocksResult{Success: true, Session: "sess", ProjectKey: "/test/project"}
	payload, err := json.Marshal(unchecked)
	if err != nil {
		t.Fatalf("marshaling LocksResult: %v", err)
	}
	if strings.Contains(string(payload), "deadlocks") {
		t.Fatalf("deadlocks key must be omitted when the check did not run:\n%s", payload)
	}
}

// captureLocksStdout redirects os.Stdout around fn, returning what the
// human-facing printer wrote.
func captureLocksStdout(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fnErr := fn()
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("closing pipe writer: %v", closeErr)
	}
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("reading captured stdout: %v", readErr)
	}
	if fnErr != nil {
		t.Fatalf("printer returned error: %v (output: %s)", fnErr, out)
	}
	return string(out)
}

func TestPrintLocksResult_CheckDeadlocksNamesCycle(t *testing.T) {
	now := locksDeadlockT1.Add(time.Minute)
	report := locksDeadlockReport(locksTwoCycleFixture(), now)
	result := LocksResult{
		Success:      true,
		Session:      "sess",
		ProjectKey:   "/test/project",
		Reservations: locksTwoCycleFixture(),
		Count:        4,
		Deadlocks:    &report,
	}

	out := captureLocksStdout(t, func() error { return printLocksResult(result, true) })
	t.Logf("cli output:\n%s", out)
	for _, want := range []string{"DEADLOCK DETECTED", "AgentA -> AgentB -> AgentA"} {
		if !strings.Contains(out, want) {
			t.Fatalf("cli output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintLocksResult_CheckDeadlocksAcyclicAllClear(t *testing.T) {
	now := locksDeadlockT1.Add(time.Minute)
	report := locksDeadlockReport(locksAcyclicFixture(), now)
	result := LocksResult{
		Success:      true,
		Session:      "sess",
		ProjectKey:   "/test/project",
		Reservations: locksAcyclicFixture(),
		Count:        3,
		Deadlocks:    &report,
	}

	out := captureLocksStdout(t, func() error { return printLocksResult(result, true) })
	t.Logf("cli output:\n%s", out)
	if strings.Contains(out, "DEADLOCK DETECTED") {
		t.Fatalf("acyclic fixture must not report a deadlock:\n%s", out)
	}
	if !strings.Contains(out, "no reservation cycles detected") {
		t.Fatalf("check-deadlocks run must print an explicit all-clear:\n%s", out)
	}
}

// setupLocksDeadlockE2E stands up the fake Agent Mail httptest stub with
// the given reservations and a resolvable session identity, then runs
// `ntm locks list <session> --all-agents --check-deadlocks --json`
// end-to-end through runLocks, returning the decoded robot envelope.
func setupLocksDeadlockE2E(t *testing.T, reservations []agentmail.FileReservation) LocksResult {
	t.Helper()
	resetFlags()
	isolateSessionAgentStorage(t)

	projectsBase := canonicalTempDir(t)
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	session := "mysession"
	saveSessionAgentForTest(t, session, projectKey, "BlueLake")

	stub := newMailStub(t, nil)
	defer stub.Close()
	stub.reservations = reservations

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(canonicalTempDir(t))

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	out := captureLocksStdout(t, func() error {
		return runLocks(t.Context(), session, true, true)
	})
	t.Logf("locks list --check-deadlocks --json output:\n%s", out)

	var result LocksResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decoding robot envelope: %v\n%s", err, out)
	}
	return result
}

func TestRunLocks_CheckDeadlocksE2E_CycleNamed(t *testing.T) {
	result := setupLocksDeadlockE2E(t, locksTwoCycleFixture())
	if !result.Success {
		t.Fatalf("expected success envelope, got %+v", result)
	}
	if result.Deadlocks == nil {
		t.Fatalf("--check-deadlocks must attach the deadlocks report")
	}
	if len(result.Deadlocks.Cycles) != 1 {
		t.Fatalf("expected the 2-cycle to be detected, got %+v", result.Deadlocks.Cycles)
	}
	got := strings.Join(result.Deadlocks.Cycles[0].Participants, "->")
	if got != "AgentA->AgentB" {
		t.Fatalf("cycle does not name the deadlocked agents: %q", got)
	}
}

func TestRunLocks_CheckDeadlocksE2E_AcyclicNoFalsePositive(t *testing.T) {
	result := setupLocksDeadlockE2E(t, locksAcyclicFixture())
	if !result.Success {
		t.Fatalf("expected success envelope, got %+v", result)
	}
	if result.Deadlocks == nil {
		t.Fatalf("--check-deadlocks must attach the deadlocks report even when clean")
	}
	if len(result.Deadlocks.Cycles) != 0 {
		t.Fatalf("acyclic fixture must yield no cycles, got %+v", result.Deadlocks.Cycles)
	}
}

func TestPrintLocksResult_NoCheckNoDeadlockSection(t *testing.T) {
	result := LocksResult{
		Success:      true,
		Session:      "sess",
		ProjectKey:   "/test/project",
		Reservations: locksAcyclicFixture(),
		Count:        3,
	}
	out := captureLocksStdout(t, func() error { return printLocksResult(result, true) })
	if strings.Contains(strings.ToLower(out), "deadlock") {
		t.Fatalf("without --check-deadlocks no deadlock text may appear:\n%s", out)
	}
}
