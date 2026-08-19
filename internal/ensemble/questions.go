package ensemble

import "strings"

const (
	minProblemStatementLen = 20
	minDescriptionLen      = 100
)

// TargetedQuestion represents a clarifying question used when context is thin.
type TargetedQuestion struct {
	ID          string `json:"id"`
	Question    string `json:"question"`
	WhyAsked    string `json:"why_asked,omitempty"`
	Required    bool   `json:"required,omitempty"`
	DefaultHint string `json:"default_hint,omitempty"`
}

// DefaultQuestions is the baseline set of questions for thin context.
var DefaultQuestions = []TargetedQuestion{
	{
		ID:       "goal",
		Question: "What is the primary goal of this analysis?",
		Required: true,
	},
	{
		ID:       "concerns",
		Question: "Are there specific areas of concern?",
	},
	{
		ID:       "constraints",
		Question: "What constraints should we respect (time, scope, tech)?",
	},
	{
		ID:       "stakeholders",
		Question: "Who are the stakeholders for this output?",
	},
	{
		ID:       "decisions",
		Question: "What decisions will this analysis inform?",
	},
	{
		ID:       "history",
		Question: "Any relevant history or previous attempts?",
	},
	{
		ID:       "success",
		Question: "What does success look like?",
	},
}

// ShouldAskQuestions determines if context is thin enough to request targeted questions.
func ShouldAskQuestions(pack *ContextPack) bool {
	return len(thinContextReasons(pack)) > 0
}

// thinContextReasons returns machine-readable reasons for thin context detection.
func thinContextReasons(pack *ContextPack) []string {
	var reasons []string
	if pack == nil {
		return []string{"pack_missing"}
	}

	pb := pack.ProjectBrief
	if pb == nil {
		reasons = append(reasons, "project_brief_missing")
	} else {
		if len(strings.TrimSpace(pb.Description)) < minDescriptionLen {
			reasons = append(reasons, "description_short")
		}
		if len(pb.RecentActivity) == 0 {
			reasons = append(reasons, "no_recent_commits")
		}
		if pb.Structure == nil {
			reasons = append(reasons, "structure_missing")
		} else if len(pb.Structure.EntryPoints) == 0 && len(pb.Structure.CorePackages) == 0 && pb.Structure.TotalFiles == 0 {
			reasons = append(reasons, "structure_empty")
		}
	}

	uc := pack.UserContext
	if uc == nil {
		reasons = append(reasons, "user_context_missing")
	} else if len(strings.TrimSpace(uc.ProblemStatement)) < minProblemStatementLen {
		reasons = append(reasons, "problem_statement_short")
	}

	return reasons
}

// SelectQuestions returns the subset of DefaultQuestions relevant to missing context.
func SelectQuestions(pack *ContextPack) []TargetedQuestion {
	if pack == nil {
		return append([]TargetedQuestion(nil), DefaultQuestions...)
	}
	if !ShouldAskQuestions(pack) {
		return nil
	}

	pb := pack.ProjectBrief
	uc := pack.UserContext

	missing := map[string]bool{
		"goal":         uc == nil || len(strings.TrimSpace(uc.ProblemStatement)) < minProblemStatementLen,
		"concerns":     uc == nil || len(uc.FocusAreas) == 0,
		"constraints":  uc == nil || len(uc.Constraints) == 0,
		"stakeholders": uc == nil || len(uc.Stakeholders) == 0,
		"decisions":    uc == nil || len(uc.Decisions) == 0,
		"success":      uc == nil || len(uc.SuccessCriteria) == 0,
		"history":      pb == nil || len(pb.RecentActivity) == 0,
	}

	questions := make([]TargetedQuestion, 0, len(DefaultQuestions))
	for _, q := range DefaultQuestions {
		if q.Required || missing[q.ID] {
			questions = append(questions, q)
		}
	}

	return questions
}
