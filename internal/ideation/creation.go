package ideation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type BeadCreationOptions struct {
	ProjectDir      string
	PlanVersion     string
	CreateRequested bool
	Confirmed       bool
	AllowCreate     bool
	CommandTimeout  time.Duration
	Runner          CommandRunner
}

type BeadCreationReport struct {
	PlanID             string                 `json:"plan_id"`
	PlanVersion        string                 `json:"plan_version,omitempty"`
	CreateRequested    bool                   `json:"create_requested"`
	Confirmed          bool                   `json:"confirmed"`
	DryRun             bool                   `json:"dry_run"`
	Success            bool                   `json:"success"`
	PartialFailure     bool                   `json:"partial_failure,omitempty"`
	Created            []CreatedBead          `json:"created"`
	Mappings           []CandidateBeadMapping `json:"mappings"`
	SkippedCandidates  []string               `json:"skipped_candidates,omitempty"`
	RemainingCommands  []string               `json:"remaining_commands"`
	ExecutedCommands   []string               `json:"executed_commands,omitempty"`
	ValidationWarnings []ValidationNote       `json:"validation_warnings,omitempty"`
	Errors             []ValidationNote       `json:"errors,omitempty"`
}

type CreatedBead struct {
	CandidateID string `json:"candidate_id"`
	Ref         string `json:"ref"`
	BeadID      string `json:"bead_id"`
	Title       string `json:"title"`
	Command     string `json:"command"`
}

type CandidateBeadMapping struct {
	CandidateID string `json:"candidate_id"`
	Ref         string `json:"ref"`
	BeadID      string `json:"bead_id"`
}

type CreationRefinementOptions struct {
	MaxPasses int `json:"max_passes,omitempty"`
}

type CreationRefinementReport struct {
	Passes                   int              `json:"passes"`
	Changed                  bool             `json:"changed"`
	MissingAcceptanceFixed   int              `json:"missing_acceptance_fixed"`
	MissingVerificationFixed int              `json:"missing_verification_fixed"`
	MissingDocs              []string         `json:"missing_docs,omitempty"`
	GraphWarnings            []string         `json:"graph_warnings,omitempty"`
	Notes                    []ValidationNote `json:"notes,omitempty"`
}

func RefinePlanForCreation(snapshot IdeaEvidenceSnapshot, plan RoadmapPlan, opts CreationRefinementOptions) (RoadmapPlan, CreationRefinementReport) {
	passes := opts.MaxPasses
	if passes <= 0 {
		passes = 1
	}
	if passes > 5 {
		passes = 5
	}
	report := CreationRefinementReport{Passes: passes}
	for pass := 0; pass < passes; pass++ {
		changedThisPass := false
		for i := range plan.ProposedBeads {
			bead := &plan.ProposedBeads[i]
			if len(bead.AcceptanceCriteria) == 0 {
				bead.AcceptanceCriteria = []string{"candidate remains self-contained and duplicate evidence is preserved"}
				bead.Description = appendMissingSection(bead.Description, "Acceptance criteria", bead.AcceptanceCriteria)
				report.MissingAcceptanceFixed++
				changedThisPass = true
			}
			if len(bead.VerificationCommands) == 0 {
				bead.VerificationCommands = []string{"rch exec -- go test -short ./internal/ideation/...", "git diff --check"}
				bead.Description = appendMissingSection(bead.Description, "Verification commands", bead.VerificationCommands)
				report.MissingVerificationFixed++
				changedThisPass = true
			}
		}
		report.Changed = report.Changed || changedThisPass
		if !changedThisPass {
			break
		}
	}
	report.MissingDocs = missingRequiredDocs(snapshot)
	report.GraphWarnings = graphWarnings(snapshot.Triage.GraphHealth)
	for _, doc := range report.MissingDocs {
		report.Notes = append(report.Notes, ValidationNote{
			Code:     "missing_project_doc",
			Severity: ValidationWarning,
			Message:  "project document missing during refinement",
			Evidence: []string{doc},
		})
	}
	for _, warning := range report.GraphWarnings {
		report.Notes = append(report.Notes, ValidationNote{
			Code:     "graph_health_warning",
			Severity: ValidationWarning,
			Message:  "graph health should be checked before creation",
			Evidence: []string{warning},
		})
	}
	return plan, report
}

func RunBeadCreation(ctx context.Context, plan RoadmapPlan, opts BeadCreationOptions) BeadCreationReport {
	opts = normalizeBeadCreationOptions(opts, plan)
	report := BeadCreationReport{
		PlanID:            plan.PlanID,
		PlanVersion:       opts.PlanVersion,
		CreateRequested:   opts.CreateRequested,
		Confirmed:         opts.Confirmed,
		DryRun:            !opts.CreateRequested,
		Success:           true,
		Created:           []CreatedBead{},
		Mappings:          []CandidateBeadMapping{},
		RemainingCommands: []string{},
	}

	commands := beadCreateCommands(plan, true)
	if !opts.CreateRequested {
		report.RemainingCommands = commands
		return report
	}
	report.DryRun = false
	if !opts.Confirmed {
		return blockBeadCreation(report, "creation_confirmation_required", "pass the explicit confirmation flag before creating beads", commands)
	}
	if !opts.AllowCreate {
		return blockBeadCreation(report, "creation_blocked_by_guard", "novelty guard did not allow mutating bead creation", commands)
	}

	runner := opts.Runner
	if runner == nil {
		runner = ExecCommandRunner{OutputLimitBytes: defaultCollectorOutputLimit}
	}
	for i, bead := range plan.ProposedBeads {
		if isDuplicateCreationCandidate(bead) {
			report.SkippedCandidates = append(report.SkippedCandidates, bead.CandidateID)
			continue
		}
		args := beadCreateArgs(bead, false)
		command := commandString("br", args)
		output, err := runBeadCreateCommand(ctx, runner, opts, args)
		if err != nil {
			report.Success = false
			report.PartialFailure = len(report.Created) > 0
			report.Errors = append(report.Errors, ValidationNote{
				Code:     "bead_creation_failed",
				Severity: ValidationError,
				Message:  err.Error(),
				Evidence: []string{command},
			})
			report.RemainingCommands = append(report.RemainingCommands, command)
			report.RemainingCommands = append(report.RemainingCommands, beadCreateCommands(RoadmapPlan{ProposedBeads: plan.ProposedBeads[i+1:]}, false)...)
			return report
		}
		id, parseErr := parseCreatedBeadID(output)
		if parseErr != nil {
			report.Success = false
			report.PartialFailure = len(report.Created) > 0
			report.Errors = append(report.Errors, ValidationNote{
				Code:     "bead_creation_output_unparseable",
				Severity: ValidationError,
				Message:  parseErr.Error(),
				Evidence: []string{command, strings.TrimSpace(string(output))},
			})
			report.RemainingCommands = append(report.RemainingCommands, command)
			report.RemainingCommands = append(report.RemainingCommands, beadCreateCommands(RoadmapPlan{ProposedBeads: plan.ProposedBeads[i+1:]}, false)...)
			return report
		}
		report.ExecutedCommands = append(report.ExecutedCommands, command)
		report.Created = append(report.Created, CreatedBead{
			CandidateID: bead.CandidateID,
			Ref:         bead.Ref,
			BeadID:      id,
			Title:       bead.Title,
			Command:     command,
		})
		report.Mappings = append(report.Mappings, CandidateBeadMapping{CandidateID: bead.CandidateID, Ref: bead.Ref, BeadID: id})
	}
	report.SkippedCandidates = stableStrings(report.SkippedCandidates)
	return report
}

func runBeadCreateCommand(ctx context.Context, runner CommandRunner, opts BeadCreationOptions, args []string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, opts.CommandTimeout)
	defer cancel()
	return runner.Run(commandCtx, opts.ProjectDir, "br", args)
}

func normalizeBeadCreationOptions(opts BeadCreationOptions, plan RoadmapPlan) BeadCreationOptions {
	if opts.PlanVersion == "" {
		opts.PlanVersion = plan.PlanID
	}
	if opts.CommandTimeout <= 0 {
		opts.CommandTimeout = defaultCollectorTimeout
	}
	return opts
}

func blockBeadCreation(report BeadCreationReport, code, message string, commands []string) BeadCreationReport {
	report.Success = false
	report.RemainingCommands = append(report.RemainingCommands, commands...)
	report.Errors = append(report.Errors, ValidationNote{
		Code:     code,
		Severity: ValidationError,
		Message:  message,
		Evidence: commands,
	})
	return report
}

func beadCreateCommands(plan RoadmapPlan, dryRun bool) []string {
	commands := make([]string, 0, len(plan.ProposedBeads))
	for _, bead := range plan.ProposedBeads {
		commands = append(commands, commandString("br", beadCreateArgs(bead, dryRun)))
	}
	return commands
}

func beadCreateArgs(bead ProposedBead, dryRun bool) []string {
	args := []string{
		"create",
		"--json",
		"--title", bead.Title,
		"--type", valueOrDefault(bead.IssueType, "task"),
		"--priority", strconv.Itoa(bead.Priority),
	}
	if strings.TrimSpace(bead.Description) != "" {
		args = append(args, "--description", bead.Description)
	}
	if len(bead.Labels) > 0 {
		args = append(args, "--labels", strings.Join(stableStrings(bead.Labels), ","))
	}
	if strings.TrimSpace(bead.Parent) != "" {
		args = append(args, "--parent", bead.Parent)
	}
	if len(bead.Dependencies) > 0 {
		deps := make([]string, 0, len(bead.Dependencies))
		for _, dep := range bead.Dependencies {
			if strings.TrimSpace(dep.Type) == "" || strings.TrimSpace(dep.ID) == "" {
				continue
			}
			deps = append(deps, dep.Type+":"+dep.ID)
		}
		if len(deps) > 0 {
			args = append(args, "--deps", strings.Join(stableStrings(deps), ","))
		}
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	return args
}

func parseCreatedBeadID(output []byte) (string, error) {
	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		return "", fmt.Errorf("parse br create JSON: %w", err)
	}
	if strings.TrimSpace(parsed.ID) == "" {
		return "", fmt.Errorf("br create JSON did not include id")
	}
	return strings.TrimSpace(parsed.ID), nil
}

func isDuplicateCreationCandidate(bead ProposedBead) bool {
	switch bead.Overlap.Kind {
	case OverlapExactDuplicate, OverlapLikelyDuplicate:
		return true
	default:
		return false
	}
}

func commandString(name string, args []string) string {
	parts := []string{name}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func valueOrDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func appendMissingSection(description, title string, items []string) string {
	if strings.Contains(description, title+":") {
		return description
	}
	sections := appendListSection([]string{strings.TrimSpace(description)}, title, items)
	return strings.TrimSpace(strings.Join(sections, "\n\n"))
}

func missingRequiredDocs(snapshot IdeaEvidenceSnapshot) []string {
	missing := []string{}
	for _, doc := range snapshot.Documents {
		if !doc.Exists {
			missing = append(missing, doc.Path)
		}
	}
	return stableStrings(missing)
}

func graphWarnings(health GraphHealth) []string {
	warnings := []string{}
	if health.Metrics["has_cycles"] == "true" {
		warnings = append(warnings, "graph has cycles")
	}
	if health.Metrics["cycle_count"] != "" && health.Metrics["cycle_count"] != "0" {
		warnings = append(warnings, "graph cycle count="+health.Metrics["cycle_count"])
	}
	return stableStrings(warnings)
}
