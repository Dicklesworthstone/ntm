package health

import (
	"strings"
	"testing"
)

func TestDetectProgressStages(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		activity ActivityLevel
		issues   []Issue
		want     ProgressStage
	}{
		{
			name:     "Idle prompt",
			output:   "some output\n>",
			activity: ActivityIdle,
			want:     StageIdle,
		},
		{
			name:     "Rate limited",
			output:   "rate limit exceeded",
			activity: ActivityActive,
			issues:   []Issue{{Type: "rate_limit"}},
			want:     StageStuck,
		},
		{
			name:     "Error detected",
			output:   "fatal error occurred",
			activity: ActivityActive,
			issues:   []Issue{{Type: "crash"}},
			want:     StageStuck,
		},
		{
			name:     "Planning phase",
			output:   "I will start by analyzing the code structure.",
			activity: ActivityActive,
			want:     StageStarting,
		},
		{
			name:     "Working phase (file edits)",
			output:   "Editing internal/main.go...",
			activity: ActivityActive,
			want:     StageWorking,
		},
		{
			name:     "Working phase (code block)",
			output:   "Here is the code:\n```go\nfunc main() {}\n```",
			activity: ActivityActive,
			want:     StageWorking,
		},
		{
			name:     "Finishing phase (completion)",
			output:   "I have completed the task. All tests passed.",
			activity: ActivityActive,
			want:     StageFinishing,
		},
		{
			name:     "Stuck phase (confusion)",
			output:   "I am confused about the requirements. Can you clarify?",
			activity: ActivityActive,
			want:     StageStuck,
		},
		{
			name:     "Ambiguous/Unknown",
			output:   "Just some random text without clear indicators.",
			activity: ActivityActive,
			want:     StageUnknown, // Or whatever default is if no match
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectProgress(tt.output, tt.activity, tt.issues)
			if got.Stage != tt.want {
				t.Errorf("detectProgress() stage = %v, want %v", got.Stage, tt.want)
			}
		})
	}
}

func BenchmarkDetectProgress(b *testing.B) {
	output := strings.Repeat("some log line\n", 100) +
		"I will start by analyzing the code structure.\n" +
		"Editing internal/main.go...\n" +
		"```go\nfunc main() {}\n```\n" +
		"I have completed the task."

	for i := 0; i < b.N; i++ {
		detectProgress(output, ActivityActive, nil)
	}
}

func TestDetectProgressDeterministicTieBreak(t *testing.T) {
	t.Parallel()
	// Matches multiple stages with equal match counts; order must never vary.
	output := "Starting analysis\nEditing main.go\nAll done\nI'm stuck"
	first := detectProgress(output, ActivityActive, nil)
	for i := 0; i < 50; i++ {
		got := detectProgress(output, ActivityActive, nil)
		if got.Stage != first.Stage {
			t.Fatalf("iteration %d: detectProgress stage = %v, want deterministic %v", i, got.Stage, first.Stage)
		}
	}
}
