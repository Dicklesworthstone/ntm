package pipeline

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestForeachFingerprintRejectsPartialProgressDrift(t *testing.T) {
	oldFingerprint := computeForeachItemsFingerprint([]interface{}{"old-item"})
	newFingerprint := computeForeachItemsFingerprint([]interface{}{"new-item"})
	for _, tc := range []struct {
		name string
		seed func(*ExecutionState)
	}{
		{"completed_iteration", func(s *ExecutionState) {
			loop := s.ForeachState["batch"]
			loop.CompletedIterationIDs = []string{"batch_iter0"}
			s.ForeachState["batch"] = loop
		}},
		{"completed_round_before_first_complete_iteration", func(s *ExecutionState) {
			loop := s.ForeachState["batch"]
			loop.CompletedRounds = map[string]int{"batch_iter0": 2}
			s.ForeachState["batch"] = loop
		}},
		{"collected_output_without_completion_marker", func(s *ExecutionState) {
			loop := s.ForeachState["batch"]
			loop.CollectedOutputs = []interface{}{"old-output"}
			s.ForeachState["batch"] = loop
		}},
		{"leaf_result", func(s *ExecutionState) {
			s.Steps = map[string]StepResult{"batch_iter0_work": {}}
		}},
		{"in_flight_leaf", func(s *ExecutionState) {
			s.InFlightSteps = map[string]InFlightStepState{"batch_iter0_work": {StepID: "batch_iter0_work"}}
		}},
		{"ambiguous_sending_receipt", func(s *ExecutionState) {
			s.AgentDeliveries = map[string]AgentDeliveryState{"batch_iter0_work": {Status: "sending"}}
		}},
		{"delivered_receipt_without_result", func(s *ExecutionState) {
			s.AgentDeliveries = map[string]AgentDeliveryState{"batch_iter0_work": {Status: "delivered"}}
		}},
		{"completed_receipt_without_iteration_marker", func(s *ExecutionState) {
			s.AgentDeliveries = map[string]AgentDeliveryState{"batch_iter0_work": {Status: "completed"}}
		}},
		{"nested_foreach_checkpoint", func(s *ExecutionState) {
			s.ForeachState["batch_iter0_nested"] = ForeachIterationState{StepID: "batch_iter0_nested", Total: 2}
		}},
		{"nested_parallel_checkpoint", func(s *ExecutionState) {
			s.ParallelState = map[string]ParallelGroupState{"batch_iter0_parallel": {Total: 2}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &ExecutionState{ForeachState: map[string]ForeachIterationState{
				"batch": {StepID: "batch", ItemsFingerprint: oldFingerprint},
			}}
			tc.seed(s)
			encoded, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			var restored ExecutionState
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			e := &Executor{state: &restored}
			if err := e.verifyForeachItemsFingerprint("batch", oldFingerprint); err != nil {
				t.Fatalf("unchanged items rejected: %v", err)
			}
			if err := e.verifyForeachItemsFingerprint("batch", newFingerprint); err == nil || !strings.Contains(err.Error(), "items changed") {
				t.Errorf("changed items with persisted progress must be rejected, got %v", err)
			}
			after, err := json.Marshal(e.state)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != string(after) {
				t.Error("fingerprint validation mutated persisted progress")
			}
		})
	}
}

func TestForeachFingerprintAllowsUnstartedAndLegacyState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state *ExecutionState
	}{
		{"nil_state", nil},
		{"missing_loop", &ExecutionState{}},
		{"unstarted", &ExecutionState{ForeachState: map[string]ForeachIterationState{
			"batch": {ItemsFingerprint: "old"},
		}}},
		{"legacy_without_fingerprint", &ExecutionState{ForeachState: map[string]ForeachIterationState{
			"batch": {CompletedIterationIDs: []string{"batch_iter0"}},
		}}},
		{"unrelated_progress", &ExecutionState{
			ForeachState:    map[string]ForeachIterationState{"batch": {ItemsFingerprint: "old"}, "other": {Total: 2}},
			Steps:           map[string]StepResult{"other_iter0_work": {}, "batch_iter0other": {}},
			AgentDeliveries: map[string]AgentDeliveryState{"batch_iter0.other": {}},
			InFlightSteps:   map[string]InFlightStepState{"batch_iter0-other": {}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &Executor{state: tc.state}
			if err := e.verifyForeachItemsFingerprint("batch", "new"); err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
		})
	}
}

func TestForeachFingerprintMalformedCheckpointReturnsError(t *testing.T) {
	for _, fingerprint := range []string{"", "b", strings.Repeat("b", 64)} {
		e := &Executor{state: &ExecutionState{ForeachState: map[string]ForeachIterationState{
			"batch": {ItemsFingerprint: "a", CompletedIterationIDs: []string{"batch_iter0"}},
		}}}
		if err := e.verifyForeachItemsFingerprint("batch", fingerprint); err == nil {
			t.Errorf("malformed mismatching fingerprint %q must return an error", fingerprint)
		}
	}
}

func TestForeachFingerprintForceRewindRetainsOnlyRequiredBinding(t *testing.T) {
	for _, iteration := range []int{0, 1} {
		e := &Executor{state: &ExecutionState{
			ForeachState: map[string]ForeachIterationState{"batch": {
				ItemsFingerprint: "old-fingerprint", CompletedIterationIDs: []string{"batch_iter0"},
				CompletedRounds: map[string]int{"batch_iter0": 2, "batch_iter1": 1},
			}},
			AgentDeliveries: map[string]AgentDeliveryState{"batch_iter1_work": {Status: "sending"}},
			InFlightSteps:   map[string]InFlightStepState{"batch_iter1_work": {}},
		}}
		e.forceResumeIteration("batch", iteration)
		err := e.verifyForeachItemsFingerprint("batch", "new-fingerprint")
		if iteration == 0 && err != nil {
			t.Errorf("full iteration rewind should allow new items after evidence is cleared: %v", err)
		}
		if iteration == 1 && err == nil {
			t.Error("partial rewind must retain the item binding for preserved iterations")
		}
	}
}

func TestForeachFingerprintConcurrentRewindAndValidation(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	e := &Executor{state: &ExecutionState{ForeachState: map[string]ForeachIterationState{
		"batch": {ItemsFingerprint: fingerprint, CompletedRounds: map[string]int{"batch_iter0": 2}},
	}}}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(writer bool) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if writer {
					e.forceResumeIteration("batch", 0)
				} else if err := e.verifyForeachItemsFingerprint("batch", fingerprint); err != nil {
					t.Errorf("unchanged items rejected: %v", err)
				}
			}
		}(worker%2 == 0)
	}
	wg.Wait()
}
