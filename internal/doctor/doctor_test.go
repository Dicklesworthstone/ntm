package doctor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func clock() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}

// healthy returns an Inputs where every check passes — the baseline
// the operator hopes for.
func healthy() Inputs {
	return Inputs{
		Tmux:    TmuxView{Available: true, BinaryVersion: "tmux 3.4", ServerRunning: true},
		Sessions: nil, // no sessions yet is a Pass
		Mail:    MailView{Configured: false},
		Beads:   BeadsView{HasLocalDB: false},
		Disk:    DiskView{Path: "/", UsedRatio: 0.40},
		Memory:  MemoryView{UsedRatio: 0.40},
		Rch:     RchView{Configured: false},
		Now:     clock(),
	}
}

func TestEvaluate_HealthyBaselineScores100(t *testing.T) {
	t.Parallel()
	r := Evaluate(healthy())
	if r.Score != 100 {
		t.Errorf("Score = %d, want 100 on healthy baseline", r.Score)
	}
	if r.Status != CheckPass {
		t.Errorf("Status = %s, want pass", r.Status)
	}
	if len(r.Findings) != 0 {
		t.Errorf("Findings = %+v, want none", r.Findings)
	}
	if len(r.Checks) != 7 {
		t.Errorf("Checks = %d, want 7", len(r.Checks))
	}
}

func TestEvaluate_TmuxUnavailableFailsCriticallyAndDocksScore(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Tmux = TmuxView{Available: false}
	r := Evaluate(in)
	if r.Status != CheckFail {
		t.Errorf("Status = %s, want fail", r.Status)
	}
	if r.Score != 75 {
		t.Errorf("Score = %d, want 75 (one fail = -25)", r.Score)
	}
	if !findHasCode(r.Findings, "tmux.unavailable") {
		t.Errorf("missing tmux.unavailable: %+v", r.Findings)
	}
}

func TestEvaluate_TmuxProbeErrorIsUnknownNotFail(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Tmux = TmuxView{ProbeError: "exec failed"}
	r := Evaluate(in)
	for _, c := range r.Checks {
		if c.Name == "tmux" && c.Status != CheckUnknown {
			t.Errorf("tmux check status = %s, want unknown", c.Status)
		}
	}
	if r.Score != 98 { // one unknown = -2
		t.Errorf("Score = %d, want 98", r.Score)
	}
}

func TestEvaluate_SessionWithUnresponsivePanesWarns(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Sessions = []SessionView{
		{Name: "proj1", PaneCount: 4, UnresponsivePanes: 2},
	}
	r := Evaluate(in)
	if r.Status != CheckWarn {
		t.Errorf("Status = %s, want warn", r.Status)
	}
	if r.Score != 95 {
		t.Errorf("Score = %d, want 95 (one warn = -5)", r.Score)
	}
	if !findHasCode(r.Findings, "session.unresponsive_panes") {
		t.Errorf("missing session.unresponsive_panes: %+v", r.Findings)
	}
}

func TestEvaluate_SessionMajorityStaleWarnsSeparately(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Sessions = []SessionView{
		{Name: "proj1", PaneCount: 8, StalePanes: 6},
	}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "session.majority_stale") {
		t.Errorf("missing session.majority_stale: %+v", r.Findings)
	}
}

func TestEvaluate_MailConfiguredButUnreachableFails(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Mail = MailView{Configured: true, Reachable: false, LastError: "connection refused"}
	r := Evaluate(in)
	if r.Status != CheckFail {
		t.Errorf("Status = %s, want fail", r.Status)
	}
	if !findHasCode(r.Findings, "mail.unreachable") {
		t.Errorf("missing mail.unreachable: %+v", r.Findings)
	}
}

func TestEvaluate_MailNotConfiguredIsPassNotFail(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Mail = MailView{Configured: false}
	r := Evaluate(in)
	for _, c := range r.Checks {
		if c.Name == "mail" && c.Status != CheckPass {
			t.Errorf("mail check status = %s, want pass when not configured", c.Status)
		}
	}
}

func TestEvaluate_MailStaleSnapshotWarns(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Mail = MailView{
		Configured:       true,
		Reachable:        true,
		StaleSnapshot:    true,
		StaleSnapshotAge: 3 * time.Hour,
	}
	in.FreshnessWindow = 1 * time.Hour
	r := Evaluate(in)
	if !findHasCode(r.Findings, "mail.stale_snapshot") {
		t.Errorf("missing mail.stale_snapshot: %+v", r.Findings)
	}
}

func TestEvaluate_BeadsMissingJSONLIsCritical(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Beads = BeadsView{HasLocalDB: true, IssuesJSONLExists: false}
	r := Evaluate(in)
	if r.Status != CheckFail {
		t.Errorf("Status = %s, want fail", r.Status)
	}
	if !findHasCode(r.Findings, "beads.jsonl_missing") {
		t.Errorf("missing beads.jsonl_missing: %+v", r.Findings)
	}
}

func TestEvaluate_BeadsNeedsFlushWarns(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Beads = BeadsView{HasLocalDB: true, IssuesJSONLExists: true, NeedsFlush: true}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "beads.needs_flush") {
		t.Errorf("missing beads.needs_flush: %+v", r.Findings)
	}
}

func TestEvaluate_BeadsStaleSyncWarns(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Beads = BeadsView{HasLocalDB: true, IssuesJSONLExists: true, LastSyncAge: 4 * time.Hour}
	in.FreshnessWindow = 1 * time.Hour
	r := Evaluate(in)
	if !findHasCode(r.Findings, "beads.stale_sync") {
		t.Errorf("missing beads.stale_sync: %+v", r.Findings)
	}
}

func TestEvaluate_DiskCriticalFailsWithRemediation(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Disk = DiskView{Path: "/data", UsedRatio: 0.97}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "disk.critical") {
		t.Errorf("missing disk.critical: %+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Code == "disk.critical" && !strings.Contains(f.Remediation, "/data") {
			t.Errorf("disk.critical remediation = %q, want path", f.Remediation)
		}
	}
}

func TestEvaluate_DiskElevatedWarns(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Disk = DiskView{Path: "/data", UsedRatio: 0.88}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "disk.elevated") {
		t.Errorf("missing disk.elevated: %+v", r.Findings)
	}
}

func TestEvaluate_MemoryCriticalFailsAndDocksScore(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Memory = MemoryView{UsedRatio: 0.95}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "memory.critical") {
		t.Errorf("missing memory.critical: %+v", r.Findings)
	}
	if r.Status != CheckFail {
		t.Errorf("Status = %s, want fail", r.Status)
	}
}

// bd-a7ky6: a memory probe that returns UsedRatio<=0 must surface a
// memory.no_probe Info finding so consumers iterating Report.Findings
// see the failure the same way disk's no_probe and rch's
// no_workers_known surface theirs. Pre-fix, the checkMemory path
// returned (CheckUnknown, nil) and the failure was visible only via
// CheckResult.Status — silently dropped from any TUI/agent that scans
// Findings as the canonical operator-actionable list.
func TestEvaluate_MemoryNoProbeEmitsInfoFinding(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Memory = MemoryView{UsedRatio: 0}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "memory.no_probe") {
		t.Errorf("missing memory.no_probe: %+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Code == "memory.no_probe" {
			if f.Severity != SeverityInfo {
				t.Errorf("memory.no_probe Severity = %s, want info", f.Severity)
			}
			if f.Check != "memory" {
				t.Errorf("memory.no_probe Check = %q, want memory", f.Check)
			}
		}
	}
	// Memory check must report Unknown so the score still subtracts -2,
	// matching disk's behavior.
	for _, c := range r.Checks {
		if c.Name == "memory" && c.Status != CheckUnknown {
			t.Errorf("memory Check.Status = %s, want unknown", c.Status)
		}
	}
}

func TestEvaluate_RchAllWorkersUnhealthyFails(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Rch = RchView{Configured: true, WorkersHealthy: 0, WorkersTotal: 8}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "rch.no_healthy_workers") {
		t.Errorf("missing rch.no_healthy_workers: %+v", r.Findings)
	}
}

func TestEvaluate_RchPartialHealthyWarns(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Rch = RchView{Configured: true, WorkersHealthy: 5, WorkersTotal: 8}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "rch.degraded") {
		t.Errorf("missing rch.degraded: %+v", r.Findings)
	}
	if r.Status != CheckWarn {
		t.Errorf("Status = %s, want warn", r.Status)
	}
}

func TestEvaluate_FindingsSortedBySeverityThenCheckThenCode(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Tmux = TmuxView{Available: false}      // critical
	in.Sessions = []SessionView{{Name: "p", PaneCount: 4, UnresponsivePanes: 2}} // warning
	in.Mail = MailView{Configured: true, Reachable: false}                        // critical
	r := Evaluate(in)
	for i := 1; i < len(r.Findings); i++ {
		ri := severityRank(r.Findings[i-1].Severity)
		rj := severityRank(r.Findings[i].Severity)
		if rj > ri {
			t.Errorf("findings out of order at %d: %s after %s", i,
				r.Findings[i].Severity, r.Findings[i-1].Severity)
		}
	}
}

func TestEvaluate_JSONShapeIsStableAndContainsAllExpectedFields(t *testing.T) {
	t.Parallel()
	in := healthy()
	in.Disk = DiskView{Path: "/data", UsedRatio: 0.88} // produces a warn finding
	a, err := json.Marshal(Evaluate(in))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	b, err := json.Marshal(Evaluate(in))
	if err != nil {
		t.Fatalf("Marshal twice: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("JSON drifted between calls:\nfirst:  %s\nsecond: %s", a, b)
	}
	for _, want := range []string{
		`"score":`, `"status":`, `"checks":`, `"findings":`, `"generated_at":`,
		`"name":"tmux"`, `"name":"session"`, `"name":"mail"`, `"name":"beads"`,
		`"name":"disk"`, `"name":"memory"`, `"name":"rch"`,
	} {
		if !strings.Contains(string(a), want) {
			t.Errorf("JSON missing %s: %s", want, a)
		}
	}
}

func TestEvaluate_ScoreFloorsAtZero(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now:      clock(),
		Tmux:     TmuxView{Available: false},                                  // -25
		Sessions: nil,                                                          // pass
		Mail:     MailView{Configured: true, Reachable: false},                // -25
		Beads:    BeadsView{HasLocalDB: true, IssuesJSONLExists: false},       // -25
		Disk:     DiskView{Path: "/", UsedRatio: 0.99},                         // -25
		Memory:   MemoryView{UsedRatio: 0.99},                                  // -25
		Rch:      RchView{Configured: true, WorkersHealthy: 0, WorkersTotal: 8}, // -25
	}
	r := Evaluate(in)
	if r.Score != 0 {
		t.Errorf("Score = %d, want 0 (floored)", r.Score)
	}
	if r.Status != CheckFail {
		t.Errorf("Status = %s, want fail", r.Status)
	}
}

func findHasCode(findings []Finding, code string) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}
