package ideation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunBeadCreationDefaultsToDryRunPreview(t *testing.T) {
	plan := fixtureCreationPlan()
	runner := recordingCreationRunner{}

	got := RunBeadCreation(context.Background(), plan, BeadCreationOptions{Runner: &runner})

	if !got.Success || !got.DryRun {
		t.Fatalf("report=%+v, want successful dry-run preview", got)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls=%v, want none for dry-run preview", runner.calls)
	}
	if len(got.RemainingCommands) != 2 {
		t.Fatalf("remaining=%v, want two preview commands", got.RemainingCommands)
	}
	for _, command := range got.RemainingCommands {
		if !strings.Contains(command, "--dry-run") {
			t.Fatalf("preview command is not dry-run: %s", command)
		}
	}
}

func TestRunBeadCreationRequiresExplicitConfirmation(t *testing.T) {
	plan := fixtureCreationPlan()
	runner := recordingCreationRunner{}

	got := RunBeadCreation(context.Background(), plan, BeadCreationOptions{
		CreateRequested: true,
		AllowCreate:     true,
		Runner:          &runner,
	})

	if got.Success {
		t.Fatalf("Success=true, want confirmation gate failure")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls=%v, want none without confirmation", runner.calls)
	}
	if !hasCreationError(got, "creation_confirmation_required") {
		t.Fatalf("errors=%+v, want confirmation error", got.Errors)
	}
}

func TestRunBeadCreationReportsPartialFailure(t *testing.T) {
	plan := fixtureCreationPlan()
	runner := recordingCreationRunner{
		outputs: [][]byte{[]byte(`{"id":"bd-created-1"}`)},
		errs:    []error{nil, errors.New("br create failed")},
	}

	got := RunBeadCreation(context.Background(), plan, BeadCreationOptions{
		CreateRequested: true,
		Confirmed:       true,
		AllowCreate:     true,
		Runner:          &runner,
	})

	if got.Success {
		t.Fatalf("Success=true, want failure")
	}
	if !got.PartialFailure {
		t.Fatalf("PartialFailure=false, want true")
	}
	if len(got.Created) != 1 || got.Created[0].BeadID != "bd-created-1" {
		t.Fatalf("created=%+v, want first bead mapping", got.Created)
	}
	if len(got.RemainingCommands) != 1 {
		t.Fatalf("remaining=%v, want failed command only", got.RemainingCommands)
	}
}

func TestRunBeadCreationUsesOnlyBrCommandsAndSkipsDuplicates(t *testing.T) {
	plan := fixtureCreationPlan()
	plan.ProposedBeads[1].Overlap.Kind = OverlapLikelyDuplicate
	runner := recordingCreationRunner{
		outputs: [][]byte{[]byte(`{"id":"bd-created-1"}`)},
	}

	got := RunBeadCreation(context.Background(), plan, BeadCreationOptions{
		CreateRequested: true,
		Confirmed:       true,
		AllowCreate:     true,
		Runner:          &runner,
	})

	if !got.Success {
		t.Fatalf("report=%+v, want success", got)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls=%v, want one br create call for non-duplicate", runner.calls)
	}
	call := runner.calls[0]
	if call.name != "br" || len(call.args) == 0 || call.args[0] != "create" {
		t.Fatalf("call=%+v, want br create", call)
	}
	for _, arg := range call.args {
		if strings.Contains(arg, "issues.jsonl") {
			t.Fatalf("direct JSONL path in br args: %v", call.args)
		}
	}
	if len(got.SkippedCandidates) != 1 || got.SkippedCandidates[0] != "dup" {
		t.Fatalf("skipped=%v, want duplicate candidate skipped", got.SkippedCandidates)
	}
}

func TestRunBeadCreationBlocksWhenGuardDisallowsCreation(t *testing.T) {
	got := RunBeadCreation(context.Background(), fixtureCreationPlan(), BeadCreationOptions{
		CreateRequested: true,
		Confirmed:       true,
		AllowCreate:     false,
		Runner:          &recordingCreationRunner{},
	})

	if got.Success {
		t.Fatalf("Success=true, want guard block")
	}
	if !hasCreationError(got, "creation_blocked_by_guard") {
		t.Fatalf("errors=%+v, want guard block", got.Errors)
	}
}

func TestRefinePlanForCreationAddsMissingVerificationSections(t *testing.T) {
	plan := fixtureCreationPlan()
	plan.ProposedBeads[0].AcceptanceCriteria = nil
	plan.ProposedBeads[0].VerificationCommands = nil
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.Documents = []ProjectDocumentMarker{
		{Path: "AGENTS.md", Exists: true},
		{Path: "README.md", Exists: false},
	}
	snapshot.Triage.GraphHealth.Metrics["cycle_count"] = "2"

	refined, report := RefinePlanForCreation(snapshot, plan, CreationRefinementOptions{MaxPasses: 3})

	if !report.Changed {
		t.Fatalf("Changed=false, want refinement changes")
	}
	if report.MissingAcceptanceFixed != 1 || report.MissingVerificationFixed != 1 {
		t.Fatalf("report=%+v, want missing sections fixed", report)
	}
	if len(refined.ProposedBeads[0].AcceptanceCriteria) == 0 || len(refined.ProposedBeads[0].VerificationCommands) == 0 {
		t.Fatalf("refined bead missing sections: %+v", refined.ProposedBeads[0])
	}
	if !strings.Contains(refined.ProposedBeads[0].Description, "Acceptance criteria:") ||
		!strings.Contains(refined.ProposedBeads[0].Description, "Verification commands:") {
		t.Fatalf("description missing added sections:\n%s", refined.ProposedBeads[0].Description)
	}
	if !containsCreationString(report.MissingDocs, "README.md") || !containsCreationString(report.GraphWarnings, "graph cycle count=2") {
		t.Fatalf("report=%+v, want doc and graph warnings", report)
	}
}

type recordingCreationRunner struct {
	outputs [][]byte
	errs    []error
	calls   []creationRunnerCall
}

type creationRunnerCall struct {
	name string
	args []string
}

func (runner *recordingCreationRunner) Run(ctx context.Context, workdir string, name string, args []string) ([]byte, error) {
	runner.calls = append(runner.calls, creationRunnerCall{name: name, args: append([]string{}, args...)})
	index := len(runner.calls) - 1
	if index < len(runner.errs) && runner.errs[index] != nil {
		return nil, runner.errs[index]
	}
	if index < len(runner.outputs) {
		return runner.outputs[index], nil
	}
	return []byte(`{"id":"bd-created"}`), nil
}

func fixtureCreationPlan() RoadmapPlan {
	return RoadmapPlan{
		PlanID: "queue-dry-ideation-dry-run",
		DryRun: true,
		ProposedBeads: []ProposedBead{
			{
				Ref:                  "${BEAD_ID_ONE}",
				CandidateID:          "one",
				Title:                "Create one",
				IssueType:            "task",
				Priority:             2,
				Labels:               []string{"queue-dry"},
				Parent:               "bd-e7xm1",
				Description:          "First candidate.",
				AcceptanceCriteria:   []string{"accepted"},
				VerificationCommands: []string{"rch exec -- go test -short ./internal/ideation/..."},
				Overlap:              OverlapVerdict{Kind: OverlapNovel},
			},
			{
				Ref:                  "${BEAD_ID_DUP}",
				CandidateID:          "dup",
				Title:                "Create duplicate",
				IssueType:            "task",
				Priority:             2,
				Labels:               []string{"queue-dry"},
				Description:          "Duplicate candidate.",
				AcceptanceCriteria:   []string{"accepted"},
				VerificationCommands: []string{"rch exec -- go test -short ./internal/ideation/..."},
				Overlap:              OverlapVerdict{Kind: OverlapNovel},
			},
		},
	}
}

func hasCreationError(report BeadCreationReport, code string) bool {
	for _, err := range report.Errors {
		if err.Code == code {
			return true
		}
	}
	return false
}

func containsCreationString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
