package worktrees

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// WorktreeAdvisorSchemaVersion identifies the stable proof-mode
	// worktree risk report shape.
	WorktreeAdvisorSchemaVersion = "ntm.worktree_risk_advisor.v1"

	WorktreeActionInspect  = "inspect_worktree"
	WorktreeActionAskHuman = "ask_human"
)

// WorktreeRiskInput is a stable, source-agnostic worktree snapshot for
// proof-mode risk scoring.
type WorktreeRiskInput struct {
	AgentName           string    `json:"agent_name"`
	Path                string    `json:"path"`
	BranchName          string    `json:"branch_name"`
	SessionID           string    `json:"session_id"`
	Error               string    `json:"error,omitempty"`
	Dirty               bool      `json:"dirty"`
	PrimaryCheckout     bool      `json:"primary_checkout"`
	SymlinkedRepo       bool      `json:"symlinked_repo"`
	LastUsed            time.Time `json:"last_used,omitempty"`
	OwnershipConfidence float64   `json:"ownership_confidence"`
}

// WorktreeAdvisorOptions configures proof-mode worktree advice.
type WorktreeAdvisorOptions struct {
	Now time.Time
}

// WorktreeAdvisorReport is the JSON-friendly advisor result.
type WorktreeAdvisorReport struct {
	SchemaVersion   string                   `json:"schema_version"`
	GeneratedAt     time.Time                `json:"generated_at"`
	Mode            string                   `json:"mode"`
	Recommendations []WorktreeRecommendation `json:"recommendations"`
	LogRows         []WorktreeAdvisorLogRow  `json:"log_rows"`
}

// WorktreeRecommendation describes one proof-mode safe action.
type WorktreeRecommendation struct {
	AgentName    string   `json:"agent_name"`
	WorktreePath string   `json:"worktree_path"`
	BranchName   string   `json:"branch_name"`
	SessionID    string   `json:"session_id"`
	RiskScore    int      `json:"risk_score"`
	Risk         string   `json:"risk"`
	Confidence   float64  `json:"confidence"`
	Action       string   `json:"action"`
	Evidence     []string `json:"evidence"`
	ReasonCodes  []string `json:"reason_codes"`
}

// WorktreeAdvisorLogRow contains the audit fields operators need.
type WorktreeAdvisorLogRow struct {
	ReservationID int     `json:"reservation_id"`
	PathPattern   string  `json:"path_pattern"`
	Holder        string  `json:"holder"`
	WorktreePath  string  `json:"worktree_path"`
	RiskScore     int     `json:"risk_score"`
	Confidence    float64 `json:"confidence"`
	Action        string  `json:"action"`
}

// InspectRiskInput enriches WorktreeInfo with read-only filesystem/git checks.
func InspectRiskInput(info *WorktreeInfo, projectPath string) WorktreeRiskInput {
	if info == nil {
		return WorktreeRiskInput{Error: "missing worktree info", OwnershipConfidence: 0.2}
	}
	input := WorktreeRiskInput{
		AgentName:           info.AgentName,
		Path:                info.Path,
		BranchName:          info.BranchName,
		SessionID:           info.SessionID,
		Error:               info.Error,
		PrimaryCheckout:     sameLocation(info.Path, projectPath),
		SymlinkedRepo:       pathIsSymlink(info.Path) || pathIsSymlink(projectPath),
		OwnershipConfidence: expectedOwnershipConfidence(*info),
	}
	if stat, err := os.Stat(info.Path); err == nil {
		input.LastUsed = stat.ModTime()
	}
	if worktreeTextEmpty(info.Error) {
		input.Dirty = worktreeHasDirtyState(info.Path)
	}
	return input
}

// AdviseWorktrees scores worktrees and emits proof-mode actions.
func AdviseWorktrees(inputs []WorktreeRiskInput, opts WorktreeAdvisorOptions) WorktreeAdvisorReport {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	report := WorktreeAdvisorReport{
		SchemaVersion:   WorktreeAdvisorSchemaVersion,
		GeneratedAt:     now.UTC(),
		Mode:            "proof",
		Recommendations: []WorktreeRecommendation{},
		LogRows:         []WorktreeAdvisorLogRow{},
	}
	for _, input := range inputs {
		rec := scoreWorktree(input, now)
		report.Recommendations = append(report.Recommendations, rec)
		report.LogRows = append(report.LogRows, WorktreeAdvisorLogRow{
			Holder:       rec.AgentName,
			WorktreePath: rec.WorktreePath,
			RiskScore:    rec.RiskScore,
			Confidence:   rec.Confidence,
			Action:       rec.Action,
		})
	}
	sort.SliceStable(report.Recommendations, func(i, j int) bool {
		a, b := report.Recommendations[i], report.Recommendations[j]
		if a.RiskScore != b.RiskScore {
			return a.RiskScore > b.RiskScore
		}
		if cmp := strings.Compare(a.WorktreePath, b.WorktreePath); cmp != 0 {
			return cmp < 0
		}
		return strings.Compare(a.AgentName, b.AgentName) < 0
	})
	sort.SliceStable(report.LogRows, func(i, j int) bool {
		a, b := report.LogRows[i], report.LogRows[j]
		if a.RiskScore != b.RiskScore {
			return a.RiskScore > b.RiskScore
		}
		if cmp := strings.Compare(a.WorktreePath, b.WorktreePath); cmp != 0 {
			return cmp < 0
		}
		return strings.Compare(a.Holder, b.Holder) < 0
	})
	return report
}

func scoreWorktree(input WorktreeRiskInput, now time.Time) WorktreeRecommendation {
	score := 5
	confidence := input.OwnershipConfidence
	if confidence == 0 {
		confidence = 0.7
	}
	action := WorktreeActionInspect
	evidence := []string{}
	reasons := []string{}

	if !worktreeTextEmpty(input.Error) {
		score += 30
		evidence = append(evidence, "error="+input.Error)
		reasons = append(reasons, "worktree_error")
	}
	if input.Dirty {
		score += 35
		evidence = append(evidence, "dirty_state=true")
		reasons = append(reasons, "dirty_worktree")
		action = WorktreeActionInspect
	}
	if input.PrimaryCheckout {
		score += 60
		evidence = append(evidence, "primary_checkout=true")
		reasons = append(reasons, "primary_checkout")
		action = WorktreeActionAskHuman
	}
	if input.SymlinkedRepo {
		score += 35
		evidence = append(evidence, "symlinked_repo=true")
		reasons = append(reasons, "symlinked_repo")
		action = WorktreeActionAskHuman
	}
	if !branchMatchesSession(input) {
		score += 25
		confidence -= 0.2
		evidence = append(evidence, fmt.Sprintf("branch=%s", input.BranchName))
		reasons = append(reasons, "branch_name_mismatch")
	}
	if confidence < 0.5 {
		score += 20
		reasons = append(reasons, "low_ownership_confidence")
		action = WorktreeActionAskHuman
	}
	if !input.LastUsed.IsZero() {
		idle := now.Sub(input.LastUsed)
		if idle < 0 {
			idle = 0
		}
		evidence = append(evidence, fmt.Sprintf("last_used_minutes=%d", int(idle.Minutes())))
	}

	score = clampWorktreeInt(score, 0, 100)
	confidence = clampWorktreeFloat(confidence, 0, 1)
	if len(reasons) == 0 {
		reasons = append(reasons, "low_risk")
		evidence = append(evidence, "no_risk_threshold_crossed")
	}

	return WorktreeRecommendation{
		AgentName:    input.AgentName,
		WorktreePath: input.Path,
		BranchName:   input.BranchName,
		SessionID:    input.SessionID,
		RiskScore:    score,
		Risk:         worktreeRiskLevel(score),
		Confidence:   confidence,
		Action:       action,
		Evidence:     evidence,
		ReasonCodes:  uniqueWorktreeStrings(reasons),
	}
}

func expectedOwnershipConfidence(info WorktreeInfo) float64 {
	if worktreeTextEmpty(info.AgentName) || worktreeTextEmpty(info.SessionID) {
		return 0.35
	}
	if worktreeTextEqual(info.BranchName, fmt.Sprintf("ntm/%s/%s", info.SessionID, info.AgentName)) {
		return 0.95
	}
	return 0.55
}

func branchMatchesSession(input WorktreeRiskInput) bool {
	if worktreeTextEmpty(input.AgentName) || worktreeTextEmpty(input.SessionID) {
		return false
	}
	return worktreeTextEqual(input.BranchName, fmt.Sprintf("ntm/%s/%s", input.SessionID, input.AgentName))
}

func sameLocation(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if worktreeTextEmpty(a) || worktreeTextEmpty(b) {
		return false
	}
	if worktreeTextEqual(filepath.Clean(a), filepath.Clean(b)) {
		return true
	}
	statA, errA := os.Stat(a)
	statB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(statA, statB)
}

func pathIsSymlink(p string) bool {
	if worktreeTextEmpty(p) {
		return false
	}
	info, err := os.Lstat(p)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

func worktreeHasDirtyState(path string) bool {
	if worktreeTextEmpty(path) {
		return false
	}
	out, err := gitOutput(path, "status", "--porcelain")
	return err == nil && !worktreeTextEmpty(string(out))
}

func worktreeRiskLevel(score int) string {
	switch {
	case score >= 80:
		return "critical"
	case score >= 55:
		return "high"
	case score >= 30:
		return "medium"
	default:
		return "low"
	}
}

func clampWorktreeInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func clampWorktreeFloat(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func uniqueWorktreeStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if worktreeTextEmpty(value) {
			continue
		}
		set[value] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func worktreeTextEmpty(value string) bool {
	return strings.Compare(strings.TrimSpace(value), "") == 0
}

func worktreeTextEqual(a, b string) bool {
	return strings.Compare(a, b) == 0
}
