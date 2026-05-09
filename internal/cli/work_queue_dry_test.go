package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/commitlint"
	ideaplan "github.com/Dicklesworthstone/ntm/internal/ideation"
	"github.com/Dicklesworthstone/ntm/internal/robot/assurance"
)

func TestEvaluateQueueDrySyncInSync(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	mustMkdirAll(t, beadsDir)

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	dbPath := filepath.Join(beadsDir, "beads.db")
	mustWriteFile(t, issuesPath, []byte("[]"))
	mustWriteFile(t, dbPath, []byte("sqlite"))

	now := time.Now().Add(-5 * time.Minute).UTC()
	mustChtimes(t, issuesPath, now, now)
	mustChtimes(t, dbPath, now, now)

	got := evaluateQueueDrySync(dir, 10*time.Minute)
	if !got.HasLocalBeadsDB {
		t.Fatalf("expected HasLocalBeadsDB=true")
	}
	if got.Status != "in_sync" {
		t.Fatalf("status=%q, want in_sync", got.Status)
	}
	if got.NeedsFlush {
		t.Fatalf("NeedsFlush=true, want false")
	}
}

func TestEvaluateQueueDrySyncDBNewerNeedsFlush(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	mustMkdirAll(t, beadsDir)

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	dbPath := filepath.Join(beadsDir, "beads.db")
	mustWriteFile(t, issuesPath, []byte("[]"))
	mustWriteFile(t, dbPath, []byte("sqlite"))

	now := time.Now().UTC()
	mustChtimes(t, issuesPath, now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	mustChtimes(t, dbPath, now, now)

	got := evaluateQueueDrySync(dir, 10*time.Minute)
	if got.Status != "beads_db_newer_than_jsonl" {
		t.Fatalf("status=%q, want beads_db_newer_than_jsonl", got.Status)
	}
	if !got.NeedsFlush {
		t.Fatalf("NeedsFlush=false, want true")
	}
}

func TestFindStaleInProgressSortAndLimit(t *testing.T) {
	now := time.Now().UTC()
	inProgress := []bv.BeadInProgress{
		{ID: "bd-newer", Title: "newer", UpdatedAt: now.Add(-30 * time.Hour)},
		{ID: "bd-oldest", Title: "oldest", UpdatedAt: now.Add(-90 * time.Hour)},
		{ID: "bd-fresh", Title: "fresh", UpdatedAt: now.Add(-2 * time.Hour)},
	}

	got := findStaleInProgress(inProgress, now, 24*time.Hour, 2)
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2", len(got))
	}
	if got[0].ID != "bd-oldest" || got[1].ID != "bd-newer" {
		t.Fatalf("order=%v, want [bd-oldest bd-newer]", []string{got[0].ID, got[1].ID})
	}
}

func TestBuildQueueDryRecommendationsQueueDry(t *testing.T) {
	report := QueueDryResponse{
		QueueDry: true,
		Evidence: QueueDryEvidence{
			ActionableCount: 0,
			ReadyCount:      0,
			Sync: QueueDrySyncStatus{
				NeedsFlush: true,
				Status:     "beads_db_newer_than_jsonl",
			},
			StaleInProgress: []QueueDryStaleIssue{
				{ID: "bd-stale-1", AgeHours: 72},
			},
			Reservations: QueueDryReservations{
				Available: true,
				Count:     2,
			},
		},
	}

	recs := buildQueueDryRecommendations(report)
	got := make([]string, 0, len(recs))
	for _, rec := range recs {
		got = append(got, rec.Code)
	}
	for _, code := range []string{"flush_jsonl", "inspect_stale_in_progress", "inspect_active_reservations", "review_pass", "alerts_sweep", "seed_new_task"} {
		if !containsStringSlice(got, code) {
			t.Fatalf("missing recommendation code %q in %v", code, got)
		}
	}
}

func TestBuildQueueDryRecommendationsActionable(t *testing.T) {
	report := QueueDryResponse{
		QueueDry: false,
		Evidence: QueueDryEvidence{
			ActionableCount: 1,
			ReadyCount:      1,
			TriageTopIDs:    []string{"bd-123", "bd-456"},
		},
	}

	recs := buildQueueDryRecommendations(report)
	if len(recs) == 0 {
		t.Fatalf("expected at least one recommendation")
	}
	if recs[len(recs)-1].Code != "claim_top_ready" {
		t.Fatalf("last code=%q, want claim_top_ready", recs[len(recs)-1].Code)
	}
	if !strings.Contains(recs[len(recs)-1].Command, "bd-123") {
		t.Fatalf("command=%q, expected top ID", recs[len(recs)-1].Command)
	}
}

func TestEvaluateQueueDryQuiescenceQueueDry(t *testing.T) {
	report := QueueDryResponse{
		QueueDry: true,
		Evidence: QueueDryEvidence{
			ActionableCount: 0,
			ReadyCount:      0,
			CountsVerified:  true,
			Sync: QueueDrySyncStatus{
				Status: "in_sync",
			},
			Reservations: QueueDryReservations{
				Available: true,
			},
		},
	}

	got := evaluateQueueDryQuiescence(report)
	if got.State != assurance.QuiescenceQueueDry {
		t.Fatalf("State = %q, want %q", got.State, assurance.QuiescenceQueueDry)
	}
	if !got.SafeToStandDown {
		t.Fatalf("SafeToStandDown = false, want true")
	}
}

func TestEvaluateQueueDryQuiescenceBlockedByPeer(t *testing.T) {
	report := QueueDryResponse{
		QueueDry: true,
		Evidence: QueueDryEvidence{
			InProgressCount: 1,
			CountsVerified:  true,
			Reservations: QueueDryReservations{
				Available: true,
				Count:     1,
			},
		},
	}

	got := evaluateQueueDryQuiescence(report)
	if got.State != assurance.QuiescenceBlockedByPeer {
		t.Fatalf("State = %q, want %q", got.State, assurance.QuiescenceBlockedByPeer)
	}
	if got.SafeToStandDown {
		t.Fatalf("SafeToStandDown = true, want false")
	}
	if !containsReasonCode(got.ReasonCodes, assurance.ReasonQuiescenceInProgressWork) {
		t.Fatalf("reason codes = %v, want in-progress marker", got.ReasonCodes)
	}
}

func TestEvaluateQueueDryQuiescenceUnsafeReservationUnknown(t *testing.T) {
	report := QueueDryResponse{
		QueueDry: true,
		Evidence: QueueDryEvidence{
			ActionableCount: 0,
			ReadyCount:      0,
			CountsVerified:  true,
			Reservations: QueueDryReservations{
				Available: false,
				Error:     "Agent Mail server unavailable",
			},
		},
	}

	got := evaluateQueueDryQuiescence(report)
	if got.State != assurance.QuiescenceUnsafeToStandDown {
		t.Fatalf("State = %q, want %q", got.State, assurance.QuiescenceUnsafeToStandDown)
	}
	if got.SafeToStandDown {
		t.Fatalf("SafeToStandDown = true, want false")
	}
	if !containsReasonCode(got.ReasonCodes, assurance.ReasonReservationUnknown) {
		t.Fatalf("reason codes = %v, want reservation unknown marker", got.ReasonCodes)
	}
}

func TestEvaluateQueueDryQuiescenceUnsafeReadyWork(t *testing.T) {
	report := QueueDryResponse{
		QueueDry: false,
		Evidence: QueueDryEvidence{
			ActionableCount: 1,
			ReadyCount:      1,
			CountsVerified:  true,
		},
	}

	got := evaluateQueueDryQuiescence(report)
	if got.State != assurance.QuiescenceUnsafeToStandDown {
		t.Fatalf("State = %q, want %q", got.State, assurance.QuiescenceUnsafeToStandDown)
	}
	if !containsReasonCode(got.ReasonCodes, assurance.ReasonQuiescenceReadyWork) {
		t.Fatalf("reason codes = %v, want ready-work marker", got.ReasonCodes)
	}
}

func TestEvaluateQueueDryQuiescenceUnsafeDirtyTracker(t *testing.T) {
	report := QueueDryResponse{
		QueueDry: true,
		Evidence: QueueDryEvidence{
			CountsVerified: true,
			Sync: QueueDrySyncStatus{
				NeedsFlush: true,
			},
		},
	}

	got := evaluateQueueDryQuiescence(report)
	if got.State != assurance.QuiescenceUnsafeToStandDown {
		t.Fatalf("State = %q, want %q", got.State, assurance.QuiescenceUnsafeToStandDown)
	}
	if !containsReasonCode(got.ReasonCodes, assurance.ReasonQuiescenceTrackerDirty) {
		t.Fatalf("reason codes = %v, want tracker marker", got.ReasonCodes)
	}
}

func TestQueueDryReservationTimeoutIsInteractive(t *testing.T) {
	// queue-dry is interactive — the operator runs `ntm work queue-dry`
	// and expects sub-second feedback. Guard the *intent* rather than
	// the literal value so future tuning (e.g. 1.5s, configurable) does
	// not break the test for no reason. The 5s ceiling matches the
	// agent-mail unhealthy-pause threshold; the >0 floor catches an
	// accidental zero (which would disable the timeout entirely).
	if queueDryReservationTimeout <= 0 {
		t.Fatalf("queueDryReservationTimeout = %s, must be positive", queueDryReservationTimeout)
	}
	if queueDryReservationTimeout >= 5*time.Second {
		t.Fatalf("queueDryReservationTimeout = %s, must be < 5s for an interactive diagnostic", queueDryReservationTimeout)
	}
}

func TestQueueDryTriageTimeoutIsInteractive(t *testing.T) {
	if queueDryTriageTimeout <= 0 {
		t.Fatalf("queueDryTriageTimeout = %s, must be positive", queueDryTriageTimeout)
	}
	if queueDryTriageTimeout >= 5*time.Second {
		t.Fatalf("queueDryTriageTimeout = %s, must be < 5s for an interactive diagnostic", queueDryTriageTimeout)
	}
}

func TestCollectQueueDryReportWarnsWhenTriageUnavailable(t *testing.T) {
	oldGetTriage := queueDryGetTriage
	queueDryGetTriage = func(string) (*bv.TriageResponse, error) {
		return nil, errors.New("bv timed out after 2s")
	}
	t.Cleanup(func() {
		queueDryGetTriage = oldGetTriage
	})

	report := collectQueueDryReport(t.TempDir(), time.Now().UTC(), 24*time.Hour, 0, 10*time.Minute, 1)

	if report.Evidence.TriageError != "bv timed out after 2s" {
		t.Fatalf("TriageError=%q, want timeout text", report.Evidence.TriageError)
	}
	if !containsWarning(report.Warnings, "bv triage unavailable: bv timed out after 2s") {
		t.Fatalf("warnings=%v, want triage timeout warning", report.Warnings)
	}
	if report.Evidence.CountsVerified {
		t.Fatalf("CountsVerified=true, want false when both Beads summary and bv triage are unavailable")
	}
	if report.QueueDry {
		t.Fatalf("QueueDry=true, want false when tracker counts are unavailable")
	}
	if report.Quiescence.SafeToStandDown {
		t.Fatalf("SafeToStandDown=true, want false when tracker counts are unavailable")
	}
	if report.Quiescence.State != assurance.QuiescenceUnsafeToStandDown {
		t.Fatalf("Quiescence.State=%q, want %q", report.Quiescence.State, assurance.QuiescenceUnsafeToStandDown)
	}
	if !containsReasonCode(report.Quiescence.ReasonCodes, assurance.ReasonQuiescenceTrackerUnknown) {
		t.Fatalf("reason codes=%v, want tracker unknown", report.Quiescence.ReasonCodes)
	}
	if containsQueueDryRecommendation(report.Recommendations, "review_pass") {
		t.Fatalf("recommendations=%v, should not recommend review_pass when tracker counts are unavailable", report.Recommendations)
	}
	if !containsQueueDryRecommendation(report.Recommendations, "refresh_triage") {
		t.Fatalf("recommendations=%v, want refresh_triage when tracker counts are unavailable", report.Recommendations)
	}
}

func TestAppendQueueDryReservationWarning(t *testing.T) {
	report := QueueDryResponse{
		Evidence: QueueDryEvidence{
			Reservations: QueueDryReservations{
				Available: false,
				Error:     "context deadline exceeded",
			},
		},
	}

	appendQueueDryReservationWarning(&report)

	if len(report.Warnings) != 1 {
		t.Fatalf("warnings=%v, want one warning", report.Warnings)
	}
	if !strings.Contains(report.Warnings[0], "reservations_unavailable") {
		t.Fatalf("warning=%q, want reservations_unavailable marker", report.Warnings[0])
	}
	if !strings.Contains(report.Warnings[0], "context deadline exceeded") {
		t.Fatalf("warning=%q, want original error text", report.Warnings[0])
	}
}

func TestQueueDryIdeationDryQueueRendersRoadmap(t *testing.T) {
	report := fixtureQueueDryDiagnostic(true)
	snapshot := fixtureQueueDryIdeationSnapshot()

	got := buildQueueDryIdeationReport(report, snapshot, QueueDryIdeationOptions{Requested: true})

	if got.Status != "rendered" {
		t.Fatalf("Status=%q, want rendered", got.Status)
	}
	if !got.DryRun {
		t.Fatalf("DryRun=false, want true")
	}
	if got.Roadmap == nil || got.Roadmap.RenderedCount != 1 {
		t.Fatalf("Roadmap=%+v, want one rendered candidate", got.Roadmap)
	}
	if got.Guard == nil || got.Guard.Recommendation != ideaplan.GuardRecommendationIdeate {
		t.Fatalf("Guard=%+v, want ideate", got.Guard)
	}
	if got.Creation == nil || !got.Creation.DryRun || len(got.Creation.RemainingCommands) == 0 {
		t.Fatalf("Creation=%+v, want dry-run creation preview", got.Creation)
	}
	if !containsQueueDryRecommendation(got.NextActions, "inspect_dry_run_bead_preview") {
		t.Fatalf("next actions=%+v, want dry-run preview action", got.NextActions)
	}
}

func TestQueueDryIdeationNonDrySkipsWithoutForce(t *testing.T) {
	report := fixtureQueueDryDiagnostic(false)
	report.Evidence.TriageTopIDs = []string{"bd-ready"}
	report.Recommendations = buildQueueDryRecommendations(report)

	got := skippedQueueDryIdeationReport(report, QueueDryIdeationOptions{Requested: true})

	if got.Status != "skipped_ready_work" {
		t.Fatalf("Status=%q, want skipped_ready_work", got.Status)
	}
	if got.Roadmap != nil {
		t.Fatalf("Roadmap=%+v, want nil when ready work exists", got.Roadmap)
	}
	if !containsQueueDryRecommendation(got.NextActions, "claim_top_ready") {
		t.Fatalf("next actions=%+v, want claim_top_ready", got.NextActions)
	}
}

func TestQueueDryIdeationForceAllowsNonDryPreview(t *testing.T) {
	report := fixtureQueueDryDiagnostic(false)
	snapshot := fixtureQueueDryIdeationSnapshot()
	snapshot.Queue.ActionableCount = 1
	snapshot.Queue.ReadyCount = 1

	got := buildQueueDryIdeationReport(report, snapshot, QueueDryIdeationOptions{Requested: true, Force: true})

	if got.Status != "forced_preview" {
		t.Fatalf("Status=%q, want forced_preview", got.Status)
	}
	if !got.Forced {
		t.Fatalf("Forced=false, want true")
	}
	if got.Roadmap == nil || got.Roadmap.RenderedCount != 1 {
		t.Fatalf("Roadmap=%+v, want forced preview roadmap", got.Roadmap)
	}
}

func TestQueueDryIdeationDegradedOptionalSourcesContinue(t *testing.T) {
	report := fixtureQueueDryDiagnostic(true)
	report.Evidence.Reservations = QueueDryReservations{
		Available: false,
		Error:     "Agent Mail server unavailable",
	}
	report.Warnings = []string{"reservations_unavailable: Agent Mail server unavailable"}
	snapshot := fixtureQueueDryIdeationSnapshot()
	annotateQueueDryOptionalSources(&snapshot, report)

	got := buildQueueDryIdeationReport(report, snapshot, QueueDryIdeationOptions{Requested: true})

	if got.Roadmap == nil || got.Roadmap.RenderedCount != 1 {
		t.Fatalf("Roadmap=%+v, want roadmap despite degraded optional sources", got.Roadmap)
	}
	for _, want := range []string{"agent_mail:reservations", "cass:context", "cm:context"} {
		if !containsWarning(got.Warnings, want) {
			t.Fatalf("warnings=%v, want degraded marker %q", got.Warnings, want)
		}
	}
	if got.Guard == nil || got.Guard.Recommendation != ideaplan.GuardRecommendationWaitForCoordination {
		t.Fatalf("Guard=%+v, want wait_for_coordination when reservations unavailable", got.Guard)
	}
}

func TestQueueDryIdeationJSONOutputContainsDryRunPreview(t *testing.T) {
	ideationReport := buildQueueDryIdeationReport(fixtureQueueDryDiagnostic(true), fixtureQueueDryIdeationSnapshot(), QueueDryIdeationOptions{Requested: true})
	report := fixtureQueueDryDiagnostic(true)
	report.Ideation = &ideationReport

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent failed: %v", err)
	}
	got := string(data)
	for _, want := range []string{`"dry_run": true`, `"command_preview"`, "br create --dry-run", `"guard"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("JSON missing %q\n%s", want, got)
		}
	}
}

func TestQueueDryIdeationMarkdownOutputContainsRoadmap(t *testing.T) {
	ideationReport := buildQueueDryIdeationReport(fixtureQueueDryDiagnostic(true), fixtureQueueDryIdeationSnapshot(), QueueDryIdeationOptions{Requested: true})
	report := fixtureQueueDryDiagnostic(true)
	report.Ideation = &ideationReport

	got := queueDryMarkdown(report)

	for _, want := range []string{"# Queue-Dry Diagnostic", "# Queue-Dry Ideation Dry Run", "queue-dry-ideation-dry-run", "Creation allowed: true", "br create --dry-run"} {
		if !strings.Contains(got, want) {
			t.Fatalf("markdown missing %q\n%s", want, got)
		}
	}
}

func TestQueueDryIdeationDryRunCommandsDoNotMutate(t *testing.T) {
	got := buildQueueDryIdeationReport(fixtureQueueDryDiagnostic(true), fixtureQueueDryIdeationSnapshot(), QueueDryIdeationOptions{Requested: true})
	if got.Roadmap == nil || len(got.Roadmap.CommandPreview) == 0 {
		t.Fatalf("roadmap commands empty: %+v", got.Roadmap)
	}
	for _, command := range got.Roadmap.CommandPreview {
		if !strings.Contains(command, "br create --dry-run") {
			t.Fatalf("command %q is not dry-run", command)
		}
	}
}

func TestQueueDryIdeationCreateBeadsRequiresConfirmation(t *testing.T) {
	got := buildQueueDryIdeationReport(fixtureQueueDryDiagnostic(true), fixtureQueueDryIdeationSnapshot(), QueueDryIdeationOptions{
		Requested:   true,
		CreateBeads: true,
	})

	if got.Status != "creation_blocked" {
		t.Fatalf("Status=%q, want creation_blocked", got.Status)
	}
	if got.Creation == nil || got.Creation.Success {
		t.Fatalf("Creation=%+v, want blocked creation report", got.Creation)
	}
	if !containsQueueDryCreationError(got.Creation, "creation_confirmation_required") {
		t.Fatalf("Creation=%+v, want explicit confirmation error", got.Creation)
	}
	if !containsQueueDryRecommendation(got.NextActions, "resolve_creation_gate") {
		t.Fatalf("next actions=%+v, want creation gate action", got.NextActions)
	}
}

func TestApplyCommitLintReportCopiesFindings(t *testing.T) {
	report := CommitReadyResponse{
		Success: true,
		Agent:   "YellowBluff",
	}
	lintReport := commitlint.Report{
		SafeToCommit: false,
		Summary:      commitlint.Summary{Critical: 1},
		Findings: []commitlint.Finding{{
			Code:     "stale_beads_export",
			Severity: commitlint.SeverityCritical,
			Summary:  "beads export is stale",
		}},
		Notes: []string{"advisory only"},
	}

	applyCommitLintReport(&report, lintReport)

	if report.SafeToCommit {
		t.Fatalf("SafeToCommit=true, want false")
	}
	if report.Summary.Critical != 1 {
		t.Fatalf("Summary=%+v, want one critical", report.Summary)
	}
	if !containsCommitReadyFinding(report.Findings, "stale_beads_export") {
		t.Fatalf("findings=%v, want stale_beads_export", report.Findings)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("Errors=%v, want one critical status", report.Errors)
	}
}

func TestAppendCommitReadyFindingMarksCriticalUnsafe(t *testing.T) {
	report := CommitReadyResponse{
		Success:      true,
		SafeToCommit: true,
	}

	appendCommitReadyFinding(&report, commitlint.Finding{
		Code:     "agent_mail_unavailable",
		Severity: commitlint.SeverityCritical,
		Summary:  "Agent Mail unavailable",
	})

	if report.SafeToCommit {
		t.Fatalf("SafeToCommit=true, want false")
	}
	if report.Summary.Critical != 1 {
		t.Fatalf("Summary=%+v, want one critical", report.Summary)
	}
	if !containsCommitReadyFinding(report.Findings, "agent_mail_unavailable") {
		t.Fatalf("findings=%v, want agent_mail_unavailable", report.Findings)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("Errors=%v, want one critical status", report.Errors)
	}
}

func fixtureQueueDryDiagnostic(dry bool) QueueDryResponse {
	report := QueueDryResponse{
		Success:  true,
		QueueDry: dry,
		Project:  "/repo",
		Evidence: QueueDryEvidence{
			CountsVerified: true,
			Sync: QueueDrySyncStatus{
				Status: "in_sync",
			},
			Reservations: QueueDryReservations{
				Available: true,
			},
		},
	}
	if dry {
		report.Evidence.ReadyCount = 0
		report.Evidence.ActionableCount = 0
	} else {
		report.Evidence.ReadyCount = 1
		report.Evidence.ActionableCount = 1
	}
	report.Quiescence = evaluateQueueDryQuiescence(report)
	report.Recommendations = buildQueueDryRecommendations(report)
	return report
}

func fixtureQueueDryIdeationSnapshot() ideaplan.IdeaEvidenceSnapshot {
	snapshot := ideaplan.NewIdeaEvidenceSnapshot("/repo")
	snapshot.Queue.CountsVerified = true
	snapshot.Queue.OpenCount = 0
	snapshot.Queue.ReadyCount = 0
	snapshot.Queue.ActionableCount = 0
	snapshot.Candidates = []ideaplan.IdeaCandidate{
		{
			ID:        "cli-dry-run",
			Title:     "Queue-dry CLI dry-run preview",
			Summary:   "Expose a duplicate-aware queue-dry roadmap preview through the existing work CLI.",
			Labels:    []string{"cli", "queue-dry"},
			Keywords:  []string{"cli", "operator", "queue", "dry", "test"},
			SourceIDs: []string{"manual:fixture"},
			Evidence:  []string{"queue is dry and operator requested a dry-run ideation preview"},
			Overlap: ideaplan.OverlapVerdict{
				Kind:       ideaplan.OverlapNovel,
				Confidence: 0.9,
				Evidence:   []string{"fixture candidate is intentionally novel"},
			},
		},
	}
	snapshot.RecordSource(ideaplan.CandidateSource{
		ID:        "manual:fixture",
		Kind:      ideaplan.SourceManual,
		Available: true,
		Evidence:  []string{"test fixture"},
	})
	return snapshot
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func mustChtimes(t *testing.T, path string, atime, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, atime, mtime); err != nil {
		t.Fatalf("Chtimes(%q): %v", path, err)
	}
}

func containsStringSlice(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func containsReasonCode(items []assurance.ReasonCode, target assurance.ReasonCode) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func containsWarning(items []string, substr string) bool {
	for _, item := range items {
		if strings.Contains(item, substr) {
			return true
		}
	}
	return false
}

func containsQueueDryRecommendation(items []QueueDryRecommendation, target string) bool {
	for _, item := range items {
		if item.Code == target {
			return true
		}
	}
	return false
}

func containsQueueDryCreationError(report *ideaplan.BeadCreationReport, target string) bool {
	if report == nil {
		return false
	}
	for _, item := range report.Errors {
		if item.Code == target {
			return true
		}
	}
	return false
}

func containsCommitReadyFinding(items []commitlint.Finding, target string) bool {
	for _, item := range items {
		if item.Code == target {
			return true
		}
	}
	return false
}
