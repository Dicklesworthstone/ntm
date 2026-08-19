package ensemble

import (
	"strings"
	"testing"
	"time"
)

func TestShouldAskQuestions_NilPack(t *testing.T) {
	if !ShouldAskQuestions(nil) {
		t.Error("ShouldAskQuestions(nil) should be true")
	}
}

func TestShouldAskQuestions_FullContext(t *testing.T) {
	pack := &ContextPack{
		ProjectBrief: &ProjectBrief{
			Description:    strings.Repeat("a", minDescriptionLen),
			RecentActivity: []CommitSummary{{Hash: "abc123", Date: time.Now()}},
			Structure: &ProjectStructure{
				EntryPoints: []string{"cmd/main.go"},
				TotalFiles:  42,
			},
		},
		UserContext: &UserContext{
			ProblemStatement: strings.Repeat("b", minProblemStatementLen),
			FocusAreas:       []string{"api"},
			Constraints:      []string{"no downtime"},
			Stakeholders:     []string{"platform"},
		},
	}

	if ShouldAskQuestions(pack) {
		t.Error("ShouldAskQuestions should be false for complete context")
	}
}

func TestSelectQuestions_NilPackReturnsDefaults(t *testing.T) {
	questions := SelectQuestions(nil)
	if len(questions) != len(DefaultQuestions) {
		t.Fatalf("expected %d questions, got %d", len(DefaultQuestions), len(questions))
	}
	if questions[0].ID != "goal" {
		t.Fatalf("expected first question to be goal, got %s", questions[0].ID)
	}
}

func TestSelectQuestions_PartialContext(t *testing.T) {
	pack := &ContextPack{
		ProjectBrief: &ProjectBrief{
			Description:    strings.Repeat("a", minDescriptionLen),
			RecentActivity: []CommitSummary{{Hash: "abc123", Date: time.Now()}},
			Structure: &ProjectStructure{
				EntryPoints: []string{"cmd/main.go"},
				TotalFiles:  10,
			},
		},
		UserContext: &UserContext{
			ProblemStatement: "short", // below threshold triggers goal
		},
	}

	questions := SelectQuestions(pack)
	if len(questions) == 0 {
		t.Fatal("expected questions for thin context")
	}

	var hasGoal, hasConstraints bool
	for _, q := range questions {
		if q.ID == "goal" {
			hasGoal = true
		}
		if q.ID == "constraints" {
			hasConstraints = true
		}
	}

	if !hasGoal {
		t.Error("expected goal question when problem statement is thin")
	}
	if !hasConstraints {
		t.Error("expected constraints question when constraints are missing")
	}
}

func TestSelectQuestions_PrioritizesRequired(t *testing.T) {
	pack := &ContextPack{
		ProjectBrief: &ProjectBrief{
			Description: strings.Repeat("a", minDescriptionLen),
			Structure: &ProjectStructure{
				EntryPoints: []string{"cmd/main.go"},
				TotalFiles:  10,
			},
		},
		UserContext: &UserContext{
			ProblemStatement: strings.Repeat("b", minProblemStatementLen),
			Constraints:      []string{"no downtime"},
		},
	}

	questions := SelectQuestions(pack)
	if len(questions) == 0 {
		t.Fatal("expected questions for thin context")
	}

	foundGoal := false
	for _, q := range questions {
		if q.ID == "goal" {
			foundGoal = true
			break
		}
	}
	if !foundGoal {
		t.Error("expected required goal question even when problem statement is present")
	}
}

func TestDefaultQuestions_AllHaveIDs(t *testing.T) {
	seen := make(map[string]bool, len(DefaultQuestions))
	for _, q := range DefaultQuestions {
		if strings.TrimSpace(q.ID) == "" {
			t.Fatalf("question with empty ID: %#v", q)
		}
		if seen[q.ID] {
			t.Fatalf("duplicate question ID: %s", q.ID)
		}
		seen[q.ID] = true
	}
}
