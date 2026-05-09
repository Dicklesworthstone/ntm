package ideation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type RoadmapRenderOptions struct {
	PlanID               string   `json:"plan_id,omitempty"`
	ParentID             string   `json:"parent_id,omitempty"`
	DefaultIssueType     string   `json:"default_issue_type,omitempty"`
	DefaultPriority      *int     `json:"default_priority,omitempty"`
	IncludeNextBest      bool     `json:"include_next_best,omitempty"`
	AcceptanceCriteria   []string `json:"acceptance_criteria,omitempty"`
	VerificationCommands []string `json:"verification_commands,omitempty"`
	NonGoals             []string `json:"non_goals,omitempty"`
}

type RoadmapPlan struct {
	PlanID              string          `json:"plan_id"`
	DryRun              bool            `json:"dry_run"`
	Decision            RankingDecision `json:"decision"`
	Summary             string          `json:"summary"`
	ParentID            string          `json:"parent_id,omitempty"`
	RenderedCount       int             `json:"rendered_count"`
	ProposedBeads       []ProposedBead  `json:"proposed_beads"`
	CommandPreview      []string        `json:"command_preview"`
	DependencyPreview   []string        `json:"dependency_preview"`
	SuppressedCandidate []string        `json:"suppressed_candidates"`
}

type ProposedBead struct {
	Ref                  string           `json:"ref"`
	CandidateID          string           `json:"candidate_id"`
	Rank                 int              `json:"rank"`
	Score                float64          `json:"score"`
	Title                string           `json:"title"`
	IssueType            string           `json:"issue_type"`
	Priority             int              `json:"priority"`
	Labels               []string         `json:"labels"`
	Parent               string           `json:"parent,omitempty"`
	Dependencies         []BeadDependency `json:"dependencies"`
	Description          string           `json:"description"`
	AcceptanceCriteria   []string         `json:"acceptance_criteria"`
	VerificationCommands []string         `json:"verification_commands"`
	NonGoals             []string         `json:"non_goals"`
	DuplicateNotes       []string         `json:"duplicate_notes"`
	SourceEvidence       []string         `json:"source_evidence"`
	Overlap              OverlapVerdict   `json:"overlap"`
	CreateCommand        string           `json:"create_command"`
	DependencyCommands   []string         `json:"dependency_commands"`
}

type BeadDependency struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func RenderRoadmap(result RankingResult, opts RoadmapRenderOptions) RoadmapPlan {
	opts = normalizeRoadmapOptions(opts)
	items := append([]RankedCandidate{}, result.Selected...)
	if opts.IncludeNextBest {
		items = append(items, result.NextBest...)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Rank != items[j].Rank {
			return items[i].Rank < items[j].Rank
		}
		if items[i].Candidate.Title != items[j].Candidate.Title {
			return items[i].Candidate.Title < items[j].Candidate.Title
		}
		return items[i].Candidate.ID < items[j].Candidate.ID
	})

	plan := RoadmapPlan{
		PlanID:              opts.PlanID,
		DryRun:              true,
		Decision:            result.Decision,
		Summary:             result.Summary,
		ParentID:            opts.ParentID,
		ProposedBeads:       []ProposedBead{},
		CommandPreview:      []string{},
		DependencyPreview:   []string{},
		SuppressedCandidate: []string{},
	}
	for _, suppressed := range result.Suppressed {
		plan.SuppressedCandidate = append(plan.SuppressedCandidate, suppressed.Candidate.ID)
	}
	plan.SuppressedCandidate = stableStrings(plan.SuppressedCandidate)

	for _, item := range items {
		bead := proposedBead(item, opts)
		plan.ProposedBeads = append(plan.ProposedBeads, bead)
		plan.CommandPreview = append(plan.CommandPreview, bead.CreateCommand)
		plan.DependencyPreview = append(plan.DependencyPreview, bead.DependencyCommands...)
	}
	plan.RenderedCount = len(plan.ProposedBeads)
	return plan
}

func RenderRoadmapJSON(plan RoadmapPlan) ([]byte, error) {
	return json.MarshalIndent(plan, "", "  ")
}

func RenderRoadmapMarkdown(plan RoadmapPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", plan.PlanID)
	fmt.Fprintf(&b, "- Dry run: %t\n", plan.DryRun)
	fmt.Fprintf(&b, "- Decision: %s\n", plan.Decision)
	fmt.Fprintf(&b, "- Rendered candidates: %d\n", plan.RenderedCount)
	if plan.ParentID != "" {
		fmt.Fprintf(&b, "- Parent: %s\n", plan.ParentID)
	}
	if plan.Summary != "" {
		fmt.Fprintf(&b, "- Summary: %s\n", plan.Summary)
	}
	b.WriteString("\n## Proposed Beads\n")
	if len(plan.ProposedBeads) == 0 {
		b.WriteString("\nNo proposed beads.\n")
		return b.String()
	}
	for _, bead := range plan.ProposedBeads {
		fmt.Fprintf(&b, "\n### %d. %s\n\n", bead.Rank, bead.Title)
		fmt.Fprintf(&b, "- Ref: %s\n", bead.Ref)
		fmt.Fprintf(&b, "- Candidate: %s\n", bead.CandidateID)
		fmt.Fprintf(&b, "- Priority: P%d\n", bead.Priority)
		fmt.Fprintf(&b, "- Labels: %s\n", strings.Join(bead.Labels, ", "))
		if len(bead.Dependencies) > 0 {
			deps := make([]string, 0, len(bead.Dependencies))
			for _, dep := range bead.Dependencies {
				deps = append(deps, dep.Type+":"+dep.ID)
			}
			fmt.Fprintf(&b, "- Dependencies: %s\n", strings.Join(deps, ", "))
		}
		fmt.Fprintf(&b, "- Create: `%s`\n", bead.CreateCommand)
		for _, cmd := range bead.DependencyCommands {
			fmt.Fprintf(&b, "- Dependency: `%s`\n", cmd)
		}
		b.WriteString("\n")
		b.WriteString(bead.Description)
		b.WriteString("\n")
	}
	return b.String()
}

func proposedBead(item RankedCandidate, opts RoadmapRenderOptions) ProposedBead {
	candidate := item.Candidate
	ref := candidateRef(candidate.ID)
	labels := stableStrings(append(append([]string{}, candidate.Labels...), "idea-wizard", "queue-dry"))
	deps := candidateDependencies(candidate, opts)
	acceptance := stableStrings(append([]string{}, opts.AcceptanceCriteria...))
	verification := stableStrings(append([]string{}, opts.VerificationCommands...))
	nonGoals := stableStrings(append([]string{}, opts.NonGoals...))
	duplicateNotes := duplicateNotes(candidate)
	evidence := stableStrings(append(append([]string{}, candidate.Evidence...), candidate.Overlap.Evidence...))
	description := beadDescription(candidate, item, acceptance, verification, nonGoals, duplicateNotes, evidence)

	bead := ProposedBead{
		Ref:                  ref,
		CandidateID:          candidate.ID,
		Rank:                 item.Rank,
		Score:                item.Score,
		Title:                candidate.Title,
		IssueType:            opts.DefaultIssueType,
		Priority:             priorityForRank(item.Rank, opts.DefaultPriority),
		Labels:               labels,
		Parent:               opts.ParentID,
		Dependencies:         deps,
		Description:          description,
		AcceptanceCriteria:   acceptance,
		VerificationCommands: verification,
		NonGoals:             nonGoals,
		DuplicateNotes:       duplicateNotes,
		SourceEvidence:       evidence,
		Overlap:              candidate.Overlap,
	}
	bead.CreateCommand = brCreateCommand(bead)
	bead.DependencyCommands = brDependencyCommands(bead)
	return bead
}

func candidateDependencies(candidate IdeaCandidate, opts RoadmapRenderOptions) []BeadDependency {
	deps := make([]BeadDependency, 0, len(candidate.RelatedWork)+1)
	if opts.ParentID != "" {
		deps = append(deps, BeadDependency{Type: "parent-child", ID: opts.ParentID})
	}
	for _, related := range candidate.RelatedWork {
		if related.ID == "" {
			continue
		}
		depType := "related"
		if normalizeRelationship(related.Relationship) == RelationshipDuplicate {
			depType = "blocks"
		}
		deps = append(deps, BeadDependency{Type: depType, ID: related.ID})
	}
	if candidate.Overlap.Kind == OverlapAdjacentFollowUp && candidate.Overlap.WorkID != "" {
		deps = append(deps, BeadDependency{Type: "related", ID: candidate.Overlap.WorkID})
	}
	return uniqueDependencies(deps)
}

func brCreateCommand(bead ProposedBead) string {
	parts := []string{
		"br create --dry-run",
		"--title " + shellQuote(bead.Title),
		"--type " + shellQuote(bead.IssueType),
		"--priority " + fmt.Sprintf("%d", bead.Priority),
	}
	if len(bead.Labels) > 0 {
		parts = append(parts, "--labels "+shellQuote(strings.Join(bead.Labels, ",")))
	}
	if bead.Description != "" {
		parts = append(parts, "--description "+shellQuote(bead.Description))
	}
	if bead.Parent != "" {
		parts = append(parts, "--parent "+shellQuote(bead.Parent))
	}
	if len(bead.Dependencies) > 0 {
		deps := make([]string, 0, len(bead.Dependencies))
		for _, dep := range bead.Dependencies {
			deps = append(deps, dep.Type+":"+dep.ID)
		}
		parts = append(parts, "--deps "+shellQuote(strings.Join(deps, ",")))
	}
	return strings.Join(parts, " ")
}

func brDependencyCommands(bead ProposedBead) []string {
	commands := make([]string, 0, len(bead.Dependencies))
	for _, dep := range bead.Dependencies {
		commands = append(commands, fmt.Sprintf("br dep add --type %s %s %s", shellQuote(dep.Type), shellQuote(bead.Ref), shellQuote(dep.ID)))
	}
	return commands
}

func beadDescription(candidate IdeaCandidate, item RankedCandidate, acceptance, verification, nonGoals, duplicateNotes, evidence []string) string {
	sections := []string{}
	if candidate.Summary != "" {
		sections = append(sections, candidate.Summary)
	} else {
		sections = append(sections, "Implement the ranked queue-dry ideation candidate as a scoped task.")
	}
	sections = append(sections, fmt.Sprintf("Candidate %s ranked #%d with score %.4f.", candidate.ID, item.Rank, item.Score))
	sections = append(sections, "Overlap: "+string(candidate.Overlap.Kind))
	if candidate.Overlap.WorkID != "" {
		sections = append(sections, "Related work: "+candidate.Overlap.WorkID)
	}
	sections = appendListSection(sections, "Acceptance criteria", acceptance)
	sections = appendListSection(sections, "Verification commands", verification)
	sections = appendListSection(sections, "Non-goals", nonGoals)
	sections = appendListSection(sections, "Duplicate and overlap notes", duplicateNotes)
	sections = appendListSection(sections, "Source evidence", evidence)
	return strings.Join(sections, "\n\n")
}

func appendListSection(sections []string, title string, items []string) []string {
	if len(items) == 0 {
		return sections
	}
	var b strings.Builder
	b.WriteString(title)
	b.WriteString(":")
	for _, item := range items {
		b.WriteString("\n- ")
		b.WriteString(item)
	}
	return append(sections, b.String())
}

func duplicateNotes(candidate IdeaCandidate) []string {
	notes := []string{}
	if candidate.Overlap.Kind != "" {
		notes = append(notes, fmt.Sprintf("overlap verdict: %s", candidate.Overlap.Kind))
	}
	if candidate.Overlap.WorkID != "" {
		notes = append(notes, "overlap work: "+candidate.Overlap.WorkID)
	}
	if candidate.Overlap.FamilyID != "" {
		notes = append(notes, "overlap family: "+candidate.Overlap.FamilyID)
	}
	notes = append(notes, candidate.Overlap.Evidence...)
	return stableStrings(notes)
}

func priorityForRank(rank int, fallback *int) int {
	if fallback != nil && *fallback >= 0 && *fallback <= 4 {
		return *fallback
	}
	switch {
	case rank <= 3:
		return 1
	case rank <= 8:
		return 2
	default:
		return 3
	}
}

func normalizeRoadmapOptions(opts RoadmapRenderOptions) RoadmapRenderOptions {
	if opts.PlanID == "" {
		opts.PlanID = "queue-dry-roadmap"
	}
	if opts.DefaultIssueType == "" {
		opts.DefaultIssueType = "task"
	}
	if opts.DefaultPriority != nil && (*opts.DefaultPriority < 0 || *opts.DefaultPriority > 4) {
		opts.DefaultPriority = nil
	}
	if len(opts.AcceptanceCriteria) == 0 {
		opts.AcceptanceCriteria = []string{"candidate is implemented as a scoped, reviewable task", "output preserves duplicate and source evidence"}
	}
	if len(opts.VerificationCommands) == 0 {
		opts.VerificationCommands = []string{"gofmt -l <touched-go-files>", "go test -short ./internal/ideation/...", "git diff --check"}
	}
	if len(opts.NonGoals) == 0 {
		opts.NonGoals = []string{"do not mutate Beads during dry-run rendering", "do not require network or model calls"}
	}
	return opts
}

func uniqueDependencies(deps []BeadDependency) []BeadDependency {
	seen := make(map[string]struct{}, len(deps))
	out := make([]BeadDependency, 0, len(deps))
	for _, dep := range deps {
		dep.Type = strings.TrimSpace(dep.Type)
		dep.ID = strings.TrimSpace(dep.ID)
		if dep.Type == "" || dep.ID == "" {
			continue
		}
		key := dep.Type + ":" + dep.ID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, dep)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func candidateRef(candidateID string) string {
	return "${BEAD_ID_" + strings.ToUpper(strings.ReplaceAll(normalizeIDPart(candidateID), "-", "_")) + "}"
}

func shellQuote(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\n", `\n`)
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
