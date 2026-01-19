package handoff

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestNew(t *testing.T) {
	h := New("test-session")

	if h.Session != "test-session" {
		t.Errorf("expected session=test-session, got %s", h.Session)
	}
	if h.Version != HandoffVersion {
		t.Errorf("expected version=%s, got %s", HandoffVersion, h.Version)
	}
	if h.Date == "" {
		t.Error("expected date to be set")
	}
	if h.CreatedAt.IsZero() {
		t.Error("expected created_at to be set")
	}
}

func TestHandoffWithMethods(t *testing.T) {
	h := New("test").
		WithGoalAndNow("Implement feature X", "Write tests").
		WithStatus(StatusComplete, OutcomeSucceeded).
		AddTask("Created handler", "handler.go", "handler_test.go").
		AddBlocker("Waiting for API review").
		AddDecision("language", "Go").
		AddFinding("perf", "10x faster with caching").
		MarkCreated("new_file.go").
		MarkModified("existing.go").
		SetAgentInfo("cc_1", AgentTypeClaude, "%42").
		SetTokenInfo(50000, 100000)

	if h.Goal != "Implement feature X" {
		t.Errorf("unexpected goal: %s", h.Goal)
	}
	if h.Now != "Write tests" {
		t.Errorf("unexpected now: %s", h.Now)
	}
	if h.Status != StatusComplete {
		t.Errorf("unexpected status: %s", h.Status)
	}
	if h.Outcome != OutcomeSucceeded {
		t.Errorf("unexpected outcome: %s", h.Outcome)
	}
	if len(h.DoneThisSession) != 1 {
		t.Errorf("expected 1 task, got %d", len(h.DoneThisSession))
	}
	if len(h.DoneThisSession[0].Files) != 2 {
		t.Errorf("expected 2 files in task, got %d", len(h.DoneThisSession[0].Files))
	}
	if len(h.Blockers) != 1 {
		t.Errorf("expected 1 blocker, got %d", len(h.Blockers))
	}
	if h.Decisions["language"] != "Go" {
		t.Errorf("unexpected decision: %v", h.Decisions)
	}
	if h.Findings["perf"] != "10x faster with caching" {
		t.Errorf("unexpected finding: %v", h.Findings)
	}
	if len(h.Files.Created) != 1 {
		t.Errorf("expected 1 created file, got %d", len(h.Files.Created))
	}
	if len(h.Files.Modified) != 1 {
		t.Errorf("expected 1 modified file, got %d", len(h.Files.Modified))
	}
	if h.AgentID != "cc_1" {
		t.Errorf("unexpected agent_id: %s", h.AgentID)
	}
	if h.TokensUsed != 50000 {
		t.Errorf("unexpected tokens_used: %d", h.TokensUsed)
	}
	if h.TokensPct != 50.0 {
		t.Errorf("unexpected tokens_pct: %f", h.TokensPct)
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name        string
		handoff     *Handoff
		expectValid bool
		expectField string
	}{
		{
			name: "valid minimal",
			handoff: &Handoff{
				Goal: "Test goal",
				Now:  "Test now",
			},
			expectValid: true,
		},
		{
			name: "missing goal",
			handoff: &Handoff{
				Now: "Test now",
			},
			expectValid: false,
			expectField: "goal",
		},
		{
			name: "missing now",
			handoff: &Handoff{
				Goal: "Test goal",
			},
			expectValid: false,
			expectField: "now",
		},
		{
			name: "invalid session name",
			handoff: &Handoff{
				Goal:    "Test goal",
				Now:     "Test now",
				Session: "invalid session!",
			},
			expectValid: false,
			expectField: "session",
		},
		{
			name: "valid session name",
			handoff: &Handoff{
				Goal:    "Test goal",
				Now:     "Test now",
				Session: "valid-session_name123",
			},
			expectValid: true,
		},
		{
			name: "general session is valid",
			handoff: &Handoff{
				Goal:    "Test goal",
				Now:     "Test now",
				Session: "general",
			},
			expectValid: true,
		},
		{
			name: "invalid date format",
			handoff: &Handoff{
				Goal: "Test goal",
				Now:  "Test now",
				Date: "01-15-2026",
			},
			expectValid: false,
			expectField: "date",
		},
		{
			name: "valid date format",
			handoff: &Handoff{
				Goal: "Test goal",
				Now:  "Test now",
				Date: "2026-01-15",
			},
			expectValid: true,
		},
		{
			name: "invalid status",
			handoff: &Handoff{
				Goal:   "Test goal",
				Now:    "Test now",
				Status: "invalid",
			},
			expectValid: false,
			expectField: "status",
		},
		{
			name: "valid status",
			handoff: &Handoff{
				Goal:   "Test goal",
				Now:    "Test now",
				Status: StatusPartial,
			},
			expectValid: true,
		},
		{
			name: "invalid outcome",
			handoff: &Handoff{
				Goal:    "Test goal",
				Now:     "Test now",
				Outcome: "MAYBE",
			},
			expectValid: false,
			expectField: "outcome",
		},
		{
			name: "valid outcome",
			handoff: &Handoff{
				Goal:    "Test goal",
				Now:     "Test now",
				Outcome: OutcomePartialPlus,
			},
			expectValid: true,
		},
		{
			name: "invalid agent_type",
			handoff: &Handoff{
				Goal:      "Test goal",
				Now:       "Test now",
				AgentType: "gpt",
			},
			expectValid: false,
			expectField: "agent_type",
		},
		{
			name: "valid agent_type",
			handoff: &Handoff{
				Goal:      "Test goal",
				Now:       "Test now",
				AgentType: AgentTypeClaude,
			},
			expectValid: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := tc.handoff.Validate()
			valid := len(errs) == 0

			if valid != tc.expectValid {
				t.Errorf("expected valid=%v, got valid=%v, errors=%v", tc.expectValid, valid, errs)
			}

			if !tc.expectValid && tc.expectField != "" {
				found := false
				for _, err := range errs {
					if err.Field == tc.expectField {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error for field %s, got %v", tc.expectField, errs)
				}
			}
		})
	}
}

func TestSetDefaults(t *testing.T) {
	h := &Handoff{
		Goal: "Test",
		Now:  "Continue",
	}

	h.SetDefaults()

	if h.Version != HandoffVersion {
		t.Errorf("expected version=%s, got %s", HandoffVersion, h.Version)
	}
	if h.Date == "" {
		t.Error("expected date to be set")
	}
	if h.CreatedAt.IsZero() {
		t.Error("expected created_at to be set")
	}
	if h.UpdatedAt.IsZero() {
		t.Error("expected updated_at to be set")
	}
}

func TestValidateAndSetDefaults(t *testing.T) {
	h := &Handoff{
		Goal: "Test",
		Now:  "Continue",
	}

	errs := h.ValidateAndSetDefaults()
	if len(errs) > 0 {
		t.Errorf("expected no errors, got %v", errs)
	}

	if h.Version == "" {
		t.Error("expected version to be set")
	}
}

func TestValidateMinimal(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		h := &Handoff{Goal: "Test", Now: "Continue"}
		if err := h.ValidateMinimal(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("missing goal", func(t *testing.T) {
		h := &Handoff{Now: "Continue"}
		err := h.ValidateMinimal()
		if err == nil {
			t.Error("expected error for missing goal")
		}
		if ve, ok := err.(ValidationError); ok {
			if ve.Field != "goal" {
				t.Errorf("expected field=goal, got %s", ve.Field)
			}
		}
	})

	t.Run("missing now", func(t *testing.T) {
		h := &Handoff{Goal: "Test"}
		err := h.ValidateMinimal()
		if err == nil {
			t.Error("expected error for missing now")
		}
	})
}

func TestIsComplete(t *testing.T) {
	h := &Handoff{Status: StatusComplete}
	if !h.IsComplete() {
		t.Error("expected IsComplete() to return true")
	}

	h.Status = StatusPartial
	if h.IsComplete() {
		t.Error("expected IsComplete() to return false for partial")
	}
}

func TestIsBlocked(t *testing.T) {
	h := &Handoff{Status: StatusBlocked}
	if !h.IsBlocked() {
		t.Error("expected IsBlocked() to return true")
	}

	h.Status = StatusComplete
	if h.IsBlocked() {
		t.Error("expected IsBlocked() to return false for complete")
	}
}

func TestHasChanges(t *testing.T) {
	h := &Handoff{}
	if h.HasChanges() {
		t.Error("expected HasChanges() to return false for empty files")
	}

	h.MarkCreated("new.go")
	if !h.HasChanges() {
		t.Error("expected HasChanges() to return true after MarkCreated")
	}
}

func TestTotalFileChanges(t *testing.T) {
	h := &Handoff{}
	h.MarkCreated("a.go", "b.go")
	h.MarkModified("c.go")
	h.MarkDeleted("d.go")

	if h.TotalFileChanges() != 4 {
		t.Errorf("expected 4 total file changes, got %d", h.TotalFileChanges())
	}
}

func TestYAMLSerialization(t *testing.T) {
	original := New("test-session").
		WithGoalAndNow("Implement feature", "Write tests").
		WithStatus(StatusComplete, OutcomeSucceeded).
		AddTask("Created handler", "handler.go").
		AddDecision("approach", "TDD").
		MarkCreated("new.go").
		SetAgentInfo("cc_1", AgentTypeClaude, "%42")

	// Serialize
	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Deserialize
	var restored Handoff
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Verify key fields
	if restored.Session != original.Session {
		t.Errorf("session mismatch: got %s, want %s", restored.Session, original.Session)
	}
	if restored.Goal != original.Goal {
		t.Errorf("goal mismatch: got %s, want %s", restored.Goal, original.Goal)
	}
	if restored.Now != original.Now {
		t.Errorf("now mismatch: got %s, want %s", restored.Now, original.Now)
	}
	if restored.Status != original.Status {
		t.Errorf("status mismatch: got %s, want %s", restored.Status, original.Status)
	}
	if restored.Outcome != original.Outcome {
		t.Errorf("outcome mismatch: got %s, want %s", restored.Outcome, original.Outcome)
	}
	if len(restored.DoneThisSession) != len(original.DoneThisSession) {
		t.Errorf("tasks mismatch: got %d, want %d", len(restored.DoneThisSession), len(original.DoneThisSession))
	}
	if restored.Decisions["approach"] != original.Decisions["approach"] {
		t.Errorf("decisions mismatch: got %v, want %v", restored.Decisions, original.Decisions)
	}
	if restored.AgentType != original.AgentType {
		t.Errorf("agent_type mismatch: got %s, want %s", restored.AgentType, original.AgentType)
	}
}

func TestYAMLFieldNames(t *testing.T) {
	h := New("test").
		WithGoalAndNow("Test goal", "Test now").
		WithStatus(StatusComplete, OutcomeSucceeded)

	data, err := yaml.Marshal(h)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	yamlStr := string(data)

	// Verify YAML uses exact field names (not camelCase or snake_case variations)
	requiredFields := []string{
		"version:",
		"session:",
		"date:",
		"created_at:",
		"updated_at:",
		"status:",
		"outcome:",
		"goal:",
		"now:",
	}

	for _, field := range requiredFields {
		if !contains(yamlStr, field) {
			t.Errorf("YAML missing expected field: %s\nYAML:\n%s", field, yamlStr)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestValidationErrorsCollection(t *testing.T) {
	// Multiple errors
	h := &Handoff{
		Session: "invalid name!",
		Status:  "invalid",
		Outcome: "invalid",
		// Missing goal and now
	}

	errs := h.Validate()
	if len(errs) < 4 {
		t.Errorf("expected at least 4 errors, got %d: %v", len(errs), errs)
	}

	// Test FieldNames
	names := errs.FieldNames()
	if len(names) != len(errs) {
		t.Errorf("FieldNames length mismatch")
	}

	// Test ForField
	goalErrs := errs.ForField("goal")
	if len(goalErrs) != 1 {
		t.Errorf("expected 1 error for goal field, got %d", len(goalErrs))
	}
}

func TestValidationError(t *testing.T) {
	err := ValidationError{
		Field:   "test_field",
		Message: "test message",
		Value:   "test value",
	}

	errStr := err.Error()
	if errStr == "" {
		t.Error("expected non-empty error string")
	}

	// Test without value
	err2 := ValidationError{
		Field:   "field",
		Message: "msg",
	}
	if err2.Error() == "" {
		t.Error("expected non-empty error string without value")
	}
}

func TestValidationErrorsError(t *testing.T) {
	// Empty
	var errs ValidationErrors
	if errs.Error() != "no validation errors" {
		t.Errorf("unexpected error for empty: %s", errs.Error())
	}

	// Single
	errs = ValidationErrors{{Field: "f", Message: "m"}}
	if !errs.HasErrors() {
		t.Error("expected HasErrors() to return true")
	}

	// Multiple
	errs = append(errs, ValidationError{Field: "f2", Message: "m2"})
	errStr := errs.Error()
	if errStr == "" {
		t.Error("expected non-empty error string for multiple errors")
	}
}

func TestTokenPercentageCalculation(t *testing.T) {
	h := &Handoff{}
	h.SetTokenInfo(25000, 100000)

	if h.TokensPct != 25.0 {
		t.Errorf("expected 25%%, got %f%%", h.TokensPct)
	}

	// Test with zero max (should not divide by zero)
	h2 := &Handoff{}
	h2.SetTokenInfo(1000, 0)
	if h2.TokensPct != 0 {
		t.Errorf("expected 0%% for zero max, got %f%%", h2.TokensPct)
	}
}

func BenchmarkValidation(b *testing.B) {
	h := New("benchmark-session").
		WithGoalAndNow("Benchmark goal", "Benchmark now").
		WithStatus(StatusComplete, OutcomeSucceeded).
		AddTask("Task 1", "file1.go", "file2.go").
		AddDecision("key", "value").
		MarkCreated("new.go").
		SetAgentInfo("cc_1", AgentTypeClaude, "%42")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Validate()
	}
}

func BenchmarkYAMLMarshal(b *testing.B) {
	h := New("benchmark-session").
		WithGoalAndNow("Benchmark goal", "Benchmark now").
		WithStatus(StatusComplete, OutcomeSucceeded).
		AddTask("Task 1", "file1.go", "file2.go").
		AddDecision("key", "value").
		MarkCreated("new.go")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = yaml.Marshal(h)
	}
}

func BenchmarkYAMLUnmarshal(b *testing.B) {
	h := New("benchmark-session").
		WithGoalAndNow("Benchmark goal", "Benchmark now")

	data, _ := yaml.Marshal(h)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var restored Handoff
		_ = yaml.Unmarshal(data, &restored)
	}
}

func TestCreatedAtPreserved(t *testing.T) {
	// Test that SetDefaults doesn't overwrite existing CreatedAt
	originalTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	h := &Handoff{
		Goal:      "Test",
		Now:       "Continue",
		CreatedAt: originalTime,
	}

	h.SetDefaults()

	if !h.CreatedAt.Equal(originalTime) {
		t.Errorf("CreatedAt was overwritten: got %v, want %v", h.CreatedAt, originalTime)
	}
	if h.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
}
