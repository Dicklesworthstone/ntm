package ideation

import (
	"fmt"
	"sort"
	"strings"
)

type SourceKind string

const (
	SourceBR            SourceKind = "br"
	SourceBV            SourceKind = "bv"
	SourceProjectDoc    SourceKind = "project_doc"
	SourceCASS          SourceKind = "cass"
	SourceCM            SourceKind = "cm"
	SourceAgentMail     SourceKind = "agent_mail"
	SourceSupportBundle SourceKind = "support_bundle"
	SourceGit           SourceKind = "git"
	SourceManual        SourceKind = "manual"
)

type WorkStatus string

const (
	WorkStatusOpen       WorkStatus = "open"
	WorkStatusInProgress WorkStatus = "in_progress"
	WorkStatusClosed     WorkStatus = "closed"
)

type OverlapKind string

const (
	OverlapExactDuplicate   OverlapKind = "exact_duplicate"
	OverlapLikelyDuplicate  OverlapKind = "likely_duplicate"
	OverlapAdjacentFollowUp OverlapKind = "adjacent_follow_up"
	OverlapNovel            OverlapKind = "novel"
)

type NoveltyLevel string

const (
	NoveltyLow     NoveltyLevel = "low"
	NoveltyMedium  NoveltyLevel = "medium"
	NoveltyHigh    NoveltyLevel = "high"
	NoveltyUnknown NoveltyLevel = "unknown"
)

type ValidationSeverity string

const (
	ValidationInfo    ValidationSeverity = "info"
	ValidationWarning ValidationSeverity = "warning"
	ValidationError   ValidationSeverity = "error"
)

const (
	RelationshipDuplicate = "duplicate"
	RelationshipFollowUp  = "follow_up"
	RelationshipAdjacent  = "adjacent"
)

var knownClosedIdeaWizardFamilies = []string{"bd-2mb03", "bd-3v1gs", "bd-fxj4f", "bd-8kglp"}

// IdeaEvidenceSnapshot is the pure, serializable input contract for queue-dry
// ideation. Collectors populate this shape; rankers and renderers consume it.
type IdeaEvidenceSnapshot struct {
	Project         string                    `json:"project,omitempty"`
	GeneratedAt     string                    `json:"generated_at,omitempty"`
	Queue           WorkQueueSummary          `json:"queue"`
	Triage          TriageEvidence            `json:"triage"`
	Documents       []ProjectDocumentMarker   `json:"documents"`
	CloseoutProof   []CloseoutProofEvidence   `json:"closeout_proof"`
	Git             []GitTouchSummary         `json:"git"`
	Sources         []CandidateSource         `json:"sources"`
	ExistingWork    []ExistingWorkFingerprint `json:"existing_work"`
	Candidates      []IdeaCandidate           `json:"candidates"`
	OptionalSignals []OptionalSignal          `json:"optional_signals"`
	DegradedSources []ValidationNote          `json:"degraded_sources"`
	ValidationNotes []ValidationNote          `json:"validation_notes"`
}

type WorkQueueSummary struct {
	OpenCount       int      `json:"open_count"`
	ActionableCount int      `json:"actionable_count"`
	BlockedCount    int      `json:"blocked_count"`
	InProgressCount int      `json:"in_progress_count"`
	ReadyCount      int      `json:"ready_count"`
	CountsVerified  bool     `json:"counts_verified"`
	SourceIDs       []string `json:"source_ids"`
}

type TriageEvidence struct {
	TopIDs        []string      `json:"top_ids"`
	GraphHealth   GraphHealth   `json:"graph_health"`
	StatusSummary []SourceState `json:"status_summary"`
	Warnings      []string      `json:"warnings"`
	SourceIDs     []string      `json:"source_ids"`
}

type GraphHealth struct {
	TotalNodes int               `json:"total_nodes"`
	TotalEdges int               `json:"total_edges"`
	Density    float64           `json:"density"`
	Metrics    map[string]string `json:"metrics"`
	Evidence   []string          `json:"evidence"`
}

type SourceState struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Elapsed string `json:"elapsed,omitempty"`
}

type ProjectDocumentMarker struct {
	Path      string   `json:"path"`
	Exists    bool     `json:"exists"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	Digest    string   `json:"digest,omitempty"`
	Evidence  []string `json:"evidence"`
	SourceID  string   `json:"source_id,omitempty"`
}

type CloseoutProofEvidence struct {
	ID       string   `json:"id"`
	Path     string   `json:"path,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Signals  []string `json:"signals"`
	SourceID string   `json:"source_id,omitempty"`
}

type GitTouchSummary struct {
	Commit   string   `json:"commit,omitempty"`
	Subject  string   `json:"subject,omitempty"`
	Paths    []string `json:"paths"`
	SourceID string   `json:"source_id,omitempty"`
}

type CandidateSource struct {
	ID        string     `json:"id"`
	Kind      SourceKind `json:"kind"`
	Name      string     `json:"name,omitempty"`
	Available bool       `json:"available"`
	Required  bool       `json:"required,omitempty"`
	Error     string     `json:"error,omitempty"`
	Evidence  []string   `json:"evidence"`
}

type ExistingWorkFingerprint struct {
	ID        string     `json:"id"`
	FamilyID  string     `json:"family_id,omitempty"`
	Title     string     `json:"title"`
	Summary   string     `json:"summary,omitempty"`
	Status    WorkStatus `json:"status"`
	Labels    []string   `json:"labels"`
	Keywords  []string   `json:"keywords"`
	Paths     []string   `json:"paths"`
	SourceIDs []string   `json:"source_ids"`
	Evidence  []string   `json:"evidence"`
	UpdatedAt string     `json:"updated_at,omitempty"`
}

type IdeaCandidate struct {
	ID              string                 `json:"id"`
	Title           string                 `json:"title"`
	Summary         string                 `json:"summary,omitempty"`
	Labels          []string               `json:"labels"`
	Keywords        []string               `json:"keywords"`
	Paths           []string               `json:"paths"`
	SourceIDs       []string               `json:"source_ids"`
	Evidence        []string               `json:"evidence"`
	RelatedWork     []RelatedWorkReference `json:"related_work"`
	Overlap         OverlapVerdict         `json:"overlap"`
	Novelty         NoveltySignal          `json:"novelty"`
	ValidationNotes []ValidationNote       `json:"validation_notes"`
}

type RelatedWorkReference struct {
	ID           string   `json:"id"`
	FamilyID     string   `json:"family_id,omitempty"`
	Relationship string   `json:"relationship"`
	Evidence     []string `json:"evidence"`
}

type OverlapVerdict struct {
	Kind       OverlapKind `json:"kind"`
	WorkID     string      `json:"work_id,omitempty"`
	FamilyID   string      `json:"family_id,omitempty"`
	Confidence float64     `json:"confidence"`
	Evidence   []string    `json:"evidence"`
}

type NoveltySignal struct {
	Level    NoveltyLevel `json:"level"`
	Score    float64      `json:"score"`
	Evidence []string     `json:"evidence"`
}

type ValidationNote struct {
	Code     string             `json:"code"`
	Severity ValidationSeverity `json:"severity"`
	Message  string             `json:"message"`
	SourceID string             `json:"source_id,omitempty"`
	Evidence []string           `json:"evidence"`
}

// OptionalSignal carries compact, redacted, source-attributed prior-knowledge
// hints from optional adapters (CASS, CM, handoff summaries, assurance digests,
// closed work history). It deliberately stores summaries and short snippets
// rather than raw long conversation text, and never mutates beads or rankings.
type OptionalSignal struct {
	ID        string   `json:"id"`
	SourceID  string   `json:"source_id"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title,omitempty"`
	Summary   string   `json:"summary,omitempty"`
	Snippet   string   `json:"snippet,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
	Evidence  []string `json:"evidence"`
}

func NewIdeaEvidenceSnapshot(project string) IdeaEvidenceSnapshot {
	return IdeaEvidenceSnapshot{
		Project: project,
		Queue: WorkQueueSummary{
			SourceIDs: []string{},
		},
		Triage: TriageEvidence{
			TopIDs:        []string{},
			GraphHealth:   GraphHealth{Metrics: map[string]string{}, Evidence: []string{}},
			StatusSummary: []SourceState{},
			Warnings:      []string{},
			SourceIDs:     []string{},
		},
		Documents:       []ProjectDocumentMarker{},
		CloseoutProof:   []CloseoutProofEvidence{},
		Git:             []GitTouchSummary{},
		Sources:         []CandidateSource{},
		ExistingWork:    []ExistingWorkFingerprint{},
		Candidates:      []IdeaCandidate{},
		OptionalSignals: []OptionalSignal{},
		DegradedSources: []ValidationNote{},
		ValidationNotes: []ValidationNote{},
	}
}

func (snapshot *IdeaEvidenceSnapshot) RecordSource(source CandidateSource) {
	if snapshot == nil {
		return
	}
	source.Evidence = stableStrings(source.Evidence)
	snapshot.Sources = append(snapshot.Sources, source)
	if source.Available {
		return
	}

	severity := ValidationWarning
	if source.Required {
		severity = ValidationError
	}
	message := strings.TrimSpace(source.Error)
	if message == "" {
		message = "source unavailable"
	}
	note := ValidationNote{
		Code:     "source_degraded",
		Severity: severity,
		Message:  message,
		SourceID: source.ID,
		Evidence: stableStrings(append([]string{}, source.Evidence...)),
	}
	if note.Evidence == nil {
		note.Evidence = []string{}
	}
	snapshot.DegradedSources = append(snapshot.DegradedSources, note)
}

func AttachOverlap(candidate IdeaCandidate, existing []ExistingWorkFingerprint) IdeaCandidate {
	candidate.Overlap = EvaluateOverlap(candidate, existing)
	candidate.Novelty = NoveltyFromOverlap(candidate.Overlap)
	return candidate
}

func EvaluateOverlap(candidate IdeaCandidate, existing []ExistingWorkFingerprint) OverlapVerdict {
	candidate = normalizeCandidate(candidate)
	existing = normalizeExistingWork(existing)

	if verdict, ok := explicitRelationshipVerdict(candidate, existing); ok {
		return verdict
	}

	best := OverlapVerdict{Kind: OverlapNovel, Confidence: 0, Evidence: []string{"no matching open, in_progress, or recently closed work fingerprints"}}
	bestScore := 0.0
	candidateTitle := normalizeText(candidate.Title)
	candidateTokens := tokenSet(candidate.Title + " " + candidate.Summary + " " + strings.Join(candidate.Keywords, " "))

	for _, work := range existing {
		titleMatch := candidateTitle != "" && candidateTitle == normalizeText(work.Title)
		sharedKeywords := intersectStrings(candidate.Keywords, work.Keywords)
		sharedLabels := intersectStrings(candidate.Labels, work.Labels)
		sharedPaths := intersectStrings(candidate.Paths, work.Paths)
		tokenOverlap := jaccard(candidateTokens, tokenSet(work.Title+" "+work.Summary+" "+strings.Join(work.Keywords, " ")))

		evidence := overlapEvidence(candidate, work, sharedKeywords, sharedLabels, sharedPaths, tokenOverlap)
		switch {
		case titleMatch:
			evidence = append(evidence, fmt.Sprintf("normalized title matches existing work %s", work.ID))
			return OverlapVerdict{
				Kind:       OverlapExactDuplicate,
				WorkID:     work.ID,
				FamilyID:   work.FamilyID,
				Confidence: 1,
				Evidence:   stableStrings(evidence),
			}
		case tokenOverlap >= 0.72 ||
			(tokenOverlap >= 0.55 && len(sharedKeywords)+len(sharedLabels)+len(sharedPaths) > 0) ||
			(len(sharedKeywords) >= 2 && len(sharedPaths)+len(sharedLabels) > 0):
			score := tokenOverlap + 0.05*float64(len(sharedKeywords)+len(sharedLabels)+len(sharedPaths))
			if score > bestScore {
				bestScore = score
				best = OverlapVerdict{
					Kind:       OverlapLikelyDuplicate,
					WorkID:     work.ID,
					FamilyID:   work.FamilyID,
					Confidence: clamp(score, 0.6, 0.95),
					Evidence:   stableStrings(evidence),
				}
			}
		}
	}

	return best
}

func NoveltyFromOverlap(overlap OverlapVerdict) NoveltySignal {
	switch overlap.Kind {
	case OverlapExactDuplicate:
		return NoveltySignal{Level: NoveltyLow, Score: 0, Evidence: []string{"exact duplicate overlap verdict"}}
	case OverlapLikelyDuplicate:
		return NoveltySignal{Level: NoveltyLow, Score: 0.2, Evidence: []string{"likely duplicate overlap verdict"}}
	case OverlapAdjacentFollowUp:
		return NoveltySignal{Level: NoveltyMedium, Score: 0.6, Evidence: []string{"adjacent follow-up overlap verdict"}}
	case OverlapNovel:
		return NoveltySignal{Level: NoveltyHigh, Score: 1, Evidence: []string{"novel overlap verdict"}}
	default:
		return NoveltySignal{Level: NoveltyUnknown, Score: 0, Evidence: []string{"unknown overlap verdict"}}
	}
}

func KnownClosedIdeaWizardFamilies() []string {
	return append([]string(nil), knownClosedIdeaWizardFamilies...)
}

func IsKnownClosedIdeaWizardFamily(id string) bool {
	id = strings.TrimSpace(id)
	for _, family := range knownClosedIdeaWizardFamilies {
		if id == family {
			return true
		}
	}
	return false
}

func explicitRelationshipVerdict(candidate IdeaCandidate, existing []ExistingWorkFingerprint) (OverlapVerdict, bool) {
	for _, related := range candidate.RelatedWork {
		relation := normalizeRelationship(related.Relationship)
		if relation == "" {
			continue
		}
		for _, work := range existing {
			if related.ID != "" && related.ID != work.ID {
				continue
			}
			if related.ID == "" && related.FamilyID != "" && related.FamilyID != work.FamilyID {
				continue
			}
			if related.ID == "" && related.FamilyID == "" {
				continue
			}
			evidence := append([]string{}, related.Evidence...)
			evidence = append(evidence, fmt.Sprintf("candidate explicitly references %s as %s", work.ID, relation))
			if IsKnownClosedIdeaWizardFamily(work.FamilyID) {
				evidence = append(evidence, fmt.Sprintf("matched recently closed idea-wizard family %s", work.FamilyID))
			}
			kind := OverlapAdjacentFollowUp
			confidence := 0.85
			if relation == RelationshipDuplicate {
				kind = OverlapExactDuplicate
				confidence = 1
			}
			return OverlapVerdict{
				Kind:       kind,
				WorkID:     work.ID,
				FamilyID:   work.FamilyID,
				Confidence: confidence,
				Evidence:   stableStrings(evidence),
			}, true
		}
	}
	return OverlapVerdict{}, false
}

func overlapEvidence(candidate IdeaCandidate, work ExistingWorkFingerprint, sharedKeywords, sharedLabels, sharedPaths []string, tokenOverlap float64) []string {
	evidence := append([]string{}, candidate.Evidence...)
	evidence = append(evidence, work.Evidence...)
	if len(sharedKeywords) > 0 {
		evidence = append(evidence, "shared keywords: "+strings.Join(sharedKeywords, ","))
	}
	if len(sharedLabels) > 0 {
		evidence = append(evidence, "shared labels: "+strings.Join(sharedLabels, ","))
	}
	if len(sharedPaths) > 0 {
		evidence = append(evidence, "shared paths: "+strings.Join(sharedPaths, ","))
	}
	if tokenOverlap > 0 {
		evidence = append(evidence, fmt.Sprintf("token overlap %.2f with %s", tokenOverlap, work.ID))
	}
	if IsKnownClosedIdeaWizardFamily(work.FamilyID) {
		evidence = append(evidence, fmt.Sprintf("matched recently closed idea-wizard family %s", work.FamilyID))
	}
	return stableStrings(evidence)
}

func normalizeCandidate(candidate IdeaCandidate) IdeaCandidate {
	candidate.Labels = stableStrings(candidate.Labels)
	candidate.Keywords = stableStrings(candidate.Keywords)
	candidate.Paths = stableStrings(candidate.Paths)
	candidate.SourceIDs = stableStrings(candidate.SourceIDs)
	candidate.Evidence = stableStrings(candidate.Evidence)
	for i := range candidate.RelatedWork {
		candidate.RelatedWork[i].Evidence = stableStrings(candidate.RelatedWork[i].Evidence)
	}
	return candidate
}

func normalizeExistingWork(existing []ExistingWorkFingerprint) []ExistingWorkFingerprint {
	out := append([]ExistingWorkFingerprint(nil), existing...)
	for i := range out {
		out[i].Labels = stableStrings(out[i].Labels)
		out[i].Keywords = stableStrings(out[i].Keywords)
		out[i].Paths = stableStrings(out[i].Paths)
		out[i].SourceIDs = stableStrings(out[i].SourceIDs)
		out[i].Evidence = stableStrings(out[i].Evidence)
	}
	return out
}

func normalizeRelationship(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "duplicate", "duplicates", "exact_duplicate", "likely_duplicate":
		return RelationshipDuplicate
	case "follow_up", "follow-up", "followup":
		return RelationshipFollowUp
	case "adjacent", "adjacent_follow_up":
		return RelationshipAdjacent
	default:
		return ""
	}
}

func normalizeText(value string) string {
	fields := tokenSlice(value)
	return strings.Join(fields, " ")
}

func tokenSet(value string) map[string]struct{} {
	tokens := tokenSlice(value)
	set := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		set[token] = struct{}{}
	}
	return set
}

func tokenSlice(value string) []string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	fields := strings.Fields(b.String())
	sort.Strings(fields)
	return stableStrings(fields)
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	intersection := 0
	for item := range a {
		if _, ok := b[item]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func intersectStrings(a, b []string) []string {
	left := make(map[string]struct{}, len(a))
	for _, item := range a {
		item = strings.ToLower(strings.TrimSpace(item))
		if item != "" {
			left[item] = struct{}{}
		}
	}
	out := make([]string, 0)
	for _, item := range b {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if _, ok := left[item]; ok {
			out = append(out, item)
		}
	}
	return stableStrings(out)
}

func stableStrings(items []string) []string {
	if items == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func clamp(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
