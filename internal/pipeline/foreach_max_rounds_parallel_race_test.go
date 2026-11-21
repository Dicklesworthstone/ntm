package pipeline

import (
	"context"
	"fmt"
	"testing"
)

// Demonstrates round-counter pollution in parallel foreach + max_rounds.
// Two iterations run concurrently. Each round writes the round variable
// and reads it back. With a global e.state.Variables["round"] mutated by
// both goroutines, the body steps observe values from the wrong iteration.
func TestForeachMaxRounds_ParallelRaceOnRoundVar(t *testing.T) {
	t.Parallel()
	executor := NewExecutor(DefaultExecutorConfig("max-rounds-parallel-race"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "max-rounds-parallel-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "fanout",
			Foreach: &ForeachConfig{
				Items:     `["A","B","C","D"]`,
				As:        "item",
				Parallel:  true,
				MaxRounds: IntOrExpr{Value: 3},
				Steps: []Step{
					{
						ID: "echo_round",
						// Sleep then echo — gives other iterations time to advance round.
						Command: `sh -c 'sleep 0.05; echo round=${round} item=${item}'`,
					},
				},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	// Each (iter, round) cell should have a distinct step result, and the
	// captured output's "round=N" must match the round suffix in the key.
	var mismatches []string
	for iter := 0; iter < 4; iter++ {
		for round := 1; round <= 3; round++ {
			key := fmt.Sprintf("fanout_iter%d_echo_round_round%d", iter, round)
			got, ok := state.Steps[key]
			if !ok {
				mismatches = append(mismatches, fmt.Sprintf("missing %s", key))
				continue
			}
			expected := fmt.Sprintf("round=%d", round)
			if !contains(got.Output, expected) {
				mismatches = append(mismatches, fmt.Sprintf("%s output=%q expected to contain %q", key, got.Output, expected))
			}
		}
	}
	if len(mismatches) > 0 {
		t.Fatalf("parallel max_rounds race produced wrong round bindings:\n%v", mismatches)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
