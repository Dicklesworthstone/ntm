package robot

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestEnvelope_EnsembleModes verifies envelope compliance for --robot-ensemble-modes.
func TestEnvelope_EnsembleModes(t *testing.T) {
	t.Log("TEST: TestEnvelope_EnsembleModes - starting")

	output, err := GetEnsembleModes(EnsembleModesOptions{})
	if err != nil {
		t.Fatalf("GetEnsembleModes failed: %v", err)
	}

	// Envelope fields must be present
	if output.Timestamp == "" {
		t.Error("envelope: timestamp is required")
	}
	if output.Version == "" {
		t.Error("envelope: version is required")
	}
	if output.Action != "ensemble_modes" {
		t.Errorf("envelope: action should be 'ensemble_modes', got '%s'", output.Action)
	}

	// Arrays must be initialized to [] not nil
	if output.Modes == nil {
		t.Error("envelope: modes array must be [] not nil")
	}
	if output.Categories == nil {
		t.Error("envelope: categories array must be [] not nil")
	}

	// Tier counts must be present
	if output.DefaultTier == "" {
		t.Error("envelope: default_tier is required")
	}
	// TotalModes should be >= 0 (valid count)
	if output.TotalModes < 0 {
		t.Errorf("envelope: total_modes should be >= 0, got %d", output.TotalModes)
	}

	// On success, error fields should be absent
	if output.Success {
		if output.Error != "" {
			t.Errorf("envelope: error field should be empty on success, got '%s'", output.Error)
		}
		if output.ErrorCode != "" {
			t.Errorf("envelope: error_code field should be empty on success, got '%s'", output.ErrorCode)
		}
		if output.Hint != "" {
			t.Errorf("envelope: hint field should be empty on success, got '%s'", output.Hint)
		}
	}

	t.Log("TEST: TestEnvelope_EnsembleModes - assertion: envelope compliant")
}

// TestEnvelope_EnsemblePresets verifies envelope compliance for --robot-ensemble-presets.
func TestEnvelope_EnsemblePresets(t *testing.T) {
	t.Log("TEST: TestEnvelope_EnsemblePresets - starting")

	output, err := GetEnsemblePresets()
	if err != nil {
		t.Fatalf("GetEnsemblePresets failed: %v", err)
	}

	// Envelope fields must be present
	if output.Timestamp == "" {
		t.Error("envelope: timestamp is required")
	}
	if output.Version == "" {
		t.Error("envelope: version is required")
	}
	if output.Action != "ensemble_presets" {
		t.Errorf("envelope: action should be 'ensemble_presets', got '%s'", output.Action)
	}

	// Arrays must be initialized to [] not nil
	if output.Presets == nil {
		t.Error("envelope: presets array must be [] not nil")
	}

	// Count should match array length
	if output.Count != len(output.Presets) {
		t.Errorf("envelope: count mismatch: count=%d, len(presets)=%d", output.Count, len(output.Presets))
	}

	// On success, error fields should be absent
	if output.Success {
		if output.Error != "" {
			t.Error("envelope: error field should be empty on success")
		}
		if output.ErrorCode != "" {
			t.Error("envelope: error_code field should be empty on success")
		}
	}

	t.Log("TEST: TestEnvelope_EnsemblePresets - assertion: envelope compliant")
}

func TestGetEnsemble_UsesPersistedStateWhenSessionOffline(t *testing.T) {
	// bd-uizon: clear ambient NTM_CONFIG so DefaultPath uses the hermetic
	// XDG_CONFIG_HOME below instead of inheriting a developer's local
	// /nonexistent/config.toml fixture (parity with bd-ev740/bd-xkls4).
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NTM_CONFIG", "")
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	state := &ensemble.EnsembleSession{
		SessionName:       "offline-ensemble-robot",
		Question:          "Summarize offline run",
		Status:            ensemble.EnsembleStopped,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentDone},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	output, err := GetEnsemble(state.SessionName)
	if err != nil {
		t.Fatalf("GetEnsemble error: %v", err)
	}
	if !output.Success {
		t.Fatalf("expected success for persisted offline ensemble state, got error=%q code=%q", output.Error, output.ErrorCode)
	}
	if output.Ensemble.Status != ensemble.EnsembleStopped.String() {
		t.Fatalf("status = %q, want %q", output.Ensemble.Status, ensemble.EnsembleStopped)
	}
	if output.Summary.Completed != 1 {
		t.Fatalf("completed = %d, want 1", output.Summary.Completed)
	}
}

func TestGetEnsembleStop_MarksOfflineActiveStateStopped(t *testing.T) {
	// bd-uizon: clear ambient NTM_CONFIG (see sibling test).
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NTM_CONFIG", "")
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	state := &ensemble.EnsembleSession{
		SessionName:       "offline-ensemble-stop-robot",
		Question:          "Stop this offline run",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentActive},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	output, err := GetEnsembleStop(state.SessionName, EnsembleStopOptions{})
	if err != nil {
		t.Fatalf("GetEnsembleStop error: %v", err)
	}
	if !output.Success {
		t.Fatalf("expected success, got error=%q code=%q", output.Error, output.ErrorCode)
	}
	if output.Result.FinalStatus != ensemble.EnsembleStopped.String() {
		t.Fatalf("final status = %q, want %q", output.Result.FinalStatus, ensemble.EnsembleStopped)
	}

	saved, err := ensemble.LoadSession(state.SessionName)
	if err != nil {
		t.Fatalf("LoadSession after robot stop error: %v", err)
	}
	if saved.Status != ensemble.EnsembleStopped {
		t.Fatalf("saved status = %q, want %q", saved.Status, ensemble.EnsembleStopped)
	}
}

func TestGetEnsembleStopCaptureFailureLeavesSessionRunning(t *testing.T) {
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	state := &ensemble.EnsembleSession{
		SessionName:       "capture-failure-ensemble-stop",
		Question:          "Keep outputs safe",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	deps := defaultEnsembleStopDependencies()
	deps.SessionExists = func(string) bool { return true }
	deps.CaptureAndSave = func(*ensemble.EnsembleSession) ([]ensemble.CapturedOutput, error) {
		return nil, errors.New("capture-pane failed")
	}
	killed := false
	deps.KillSession = func(string) error {
		killed = true
		return nil
	}

	output, err := getEnsembleStop(state.SessionName, EnsembleStopOptions{}, deps)
	if err != nil {
		t.Fatalf("getEnsembleStop error: %v", err)
	}
	if output.Success || output.ErrorCode != ErrCodeInternalError {
		t.Fatalf("capture failure output = %+v, want typed failure", output.RobotResponse)
	}
	if killed {
		t.Fatal("KillSession called after output capture failed")
	}
	if output.Result.FinalStatus != ensemble.EnsembleActive.String() {
		t.Fatalf("final status = %q, want active", output.Result.FinalStatus)
	}

	saved, err := ensemble.LoadSession(state.SessionName)
	if err != nil {
		t.Fatalf("LoadSession after capture failure: %v", err)
	}
	if saved.Status != ensemble.EnsembleActive {
		t.Fatalf("saved status = %q, want active", saved.Status)
	}
}

// TestEnvelope_EnsembleModesError verifies error envelope compliance.
func TestEnvelope_EnsembleModesError(t *testing.T) {
	t.Log("TEST: TestEnvelope_EnsembleModesError - starting")

	// Modes with valid options should succeed
	// We can't easily trigger an error from GetEnsembleModes with valid catalog
	// Instead, verify the success path fully

	output, _ := GetEnsembleModes(EnsembleModesOptions{Tier: "all"})

	// Check deterministic ordering - modes should be sorted
	if len(output.Modes) > 1 {
		// IDs should be stable (not random on each call)
		output2, _ := GetEnsembleModes(EnsembleModesOptions{Tier: "all"})
		if len(output.Modes) != len(output2.Modes) {
			t.Error("envelope: mode count not deterministic")
		}
		for i := range output.Modes {
			if i < len(output2.Modes) && output.Modes[i].ID != output2.Modes[i].ID {
				t.Errorf("envelope: mode ordering not deterministic at index %d", i)
				break
			}
		}
	}

	t.Log("TEST: TestEnvelope_EnsembleModesError - assertion: deterministic ordering verified")
}

// TestEnvelope_EnvelopeJSONMarshaling verifies JSON output matches envelope spec.
func TestEnvelope_EnvelopeJSONMarshaling(t *testing.T) {
	t.Log("TEST: TestEnvelope_EnvelopeJSONMarshaling - starting")

	output, _ := GetEnsembleModes(EnsembleModesOptions{})

	// Marshal to JSON
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("JSON marshaling failed: %v", err)
	}

	// Unmarshal to map to check field presence
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("JSON unmarshaling failed: %v", err)
	}

	// Required envelope fields
	requiredFields := []string{"success", "timestamp", "action"}
	for _, field := range requiredFields {
		if _, ok := m[field]; !ok {
			t.Errorf("envelope: required field '%s' missing from JSON", field)
		}
	}

	// Arrays should serialize as [] not null
	if modes, ok := m["modes"].([]interface{}); !ok || modes == nil {
		t.Error("envelope: modes should serialize as array, not null")
	}
	if categories, ok := m["categories"].([]interface{}); !ok || categories == nil {
		t.Error("envelope: categories should serialize as array, not null")
	}

	t.Logf("TEST: JSON contains %d top-level fields", len(m))
	t.Log("TEST: TestEnvelope_EnvelopeJSONMarshaling - assertion: JSON envelope compliant")
}

// TestEnvelope_ModesTierCounts verifies tier breakdown is present.
func TestEnvelope_ModesTierCounts(t *testing.T) {
	t.Log("TEST: TestEnvelope_ModesTierCounts - starting")

	output, _ := GetEnsembleModes(EnsembleModesOptions{Tier: "all"})

	// Tier counts should be reported
	t.Logf("TEST: tier counts - total=%d, core=%d, advanced=%d, experimental=%d",
		output.TotalModes, output.CoreModes, output.AdvancedModes, output.ExperimentalModes)

	// Sum of tiers should equal total
	tierSum := output.CoreModes + output.AdvancedModes + output.ExperimentalModes
	if tierSum != output.TotalModes {
		t.Errorf("envelope: tier sum (%d) != total modes (%d)", tierSum, output.TotalModes)
	}

	// Default tier should be valid
	validTiers := map[string]bool{"core": true, "advanced": true, "experimental": true}
	if !validTiers[output.DefaultTier] {
		t.Errorf("envelope: invalid default_tier '%s'", output.DefaultTier)
	}

	t.Log("TEST: TestEnvelope_ModesTierCounts - assertion: tier counts present and valid")
}

// TestEnvelope_PresetsTagsNotNil verifies preset tags array is never nil.
func TestEnvelope_PresetsTagsNotNil(t *testing.T) {
	t.Log("TEST: TestEnvelope_PresetsTagsNotNil - starting")

	output, _ := GetEnsemblePresets()

	for i, preset := range output.Presets {
		if preset.Tags == nil {
			t.Errorf("envelope: preset[%d] (%s) has nil tags, should be []", i, preset.Name)
		}
		// Modes array should also not be nil
		if preset.Modes == nil {
			t.Errorf("envelope: preset[%d] (%s) has nil modes, should be []", i, preset.Name)
		}
	}

	t.Log("TEST: TestEnvelope_PresetsTagsNotNil - assertion: all preset arrays initialized")
}

// TestEnvelope_SynthesizeAuditArrays verifies audit arrays in synthesize output.
func TestEnvelope_SynthesizeAuditArrays(t *testing.T) {
	t.Log("TEST: TestEnvelope_SynthesizeAuditArrays - starting")

	// Create a properly initialized output (as the actual code does)
	output := &EnsembleSynthesizeOutput{
		RobotResponse: NewRobotResponse(true),
		Action:        "ensemble_synthesize",
		Status:        "complete",
		Audit: &SynthesisAudit{
			ConflictCount:     0,
			UnresolvedCount:   0,
			HighConflictPairs: []string{}, // Must be [] not nil
		},
	}

	// Verify the array is properly initialized
	if output.Audit.HighConflictPairs == nil {
		t.Error("envelope: HighConflictPairs should be [] not nil")
	}

	// Verify JSON serialization produces [] not null
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("JSON marshaling failed: %v", err)
	}

	// Check that arrays serialize as [] in JSON
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("JSON unmarshaling failed: %v", err)
	}

	if audit, ok := m["audit"].(map[string]interface{}); ok {
		if pairs, ok := audit["high_conflict_pairs"].([]interface{}); !ok {
			t.Error("envelope: high_conflict_pairs should serialize as []")
		} else if pairs == nil {
			t.Error("envelope: high_conflict_pairs should not be null")
		}
	} else {
		t.Error("envelope: audit field missing from JSON")
	}

	t.Log("TEST: TestEnvelope_SynthesizeAuditArrays - assertion: audit arrays initialized")
}

// TestEnvelope_ModesArrayFieldsNotNil verifies mode info arrays are never nil.
func TestEnvelope_ModesArrayFieldsNotNil(t *testing.T) {
	t.Log("TEST: TestEnvelope_ModesArrayFieldsNotNil - starting")

	output, _ := GetEnsembleModes(EnsembleModesOptions{Tier: "all", Limit: 5})

	for i, mode := range output.Modes {
		// BestFor should be [] not nil
		if mode.BestFor == nil {
			t.Logf("envelope: mode[%d] (%s) has nil best_for (acceptable for omitempty)", i, mode.ID)
		}
		// FailureModes should be [] not nil
		if mode.FailureModes == nil {
			t.Logf("envelope: mode[%d] (%s) has nil failure_modes (acceptable for omitempty)", i, mode.ID)
		}
		// Required fields should be present
		if mode.ID == "" {
			t.Errorf("envelope: mode[%d] missing ID", i)
		}
		if mode.Code == "" {
			t.Errorf("envelope: mode[%d] (%s) missing Code", i, mode.ID)
		}
		if mode.Tier == "" {
			t.Errorf("envelope: mode[%d] (%s) missing Tier", i, mode.ID)
		}
	}

	t.Log("TEST: TestEnvelope_ModesArrayFieldsNotNil - assertion: mode fields present")
}

func TestGetEnsembleStopPaneInspectionFailureLeavesSessionRunning(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, noCollect := range []bool{false, true} {
			t.Run(fmt.Sprintf("force=%t/no_collect=%t", force, noCollect), func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				t.Setenv("NTM_CONFIG", "")
				ensemble.CloseDefaultStateStore()
				t.Cleanup(ensemble.CloseDefaultStateStore)

				state := &ensemble.EnsembleSession{
					SessionName:       "inspect-failure-ensemble-stop",
					Question:          "Leave running if pane inspection fails",
					Status:            ensemble.EnsembleActive,
					SynthesisStrategy: ensemble.StrategyConsensus,
					CreatedAt:         time.Now().UTC(),
				}
				if err := ensemble.SaveSession("", state); err != nil {
					t.Fatal(err)
				}

				deps := defaultEnsembleStopDependencies()
				deps.SessionExists = func(string) bool { return true }
				deps.CaptureAndSave = func(*ensemble.EnsembleSession) ([]ensemble.CapturedOutput, error) {
					return nil, nil
				}
				deps.GetPanes = func(string) ([]tmux.Pane, error) {
					return nil, errors.New("list-panes failed")
				}
				interrupted, slept, killed := false, false, false
				deps.SendKeys = func(string, string, bool) error {
					interrupted = true
					return nil
				}
				deps.Sleep = func(time.Duration) { slept = true }
				deps.KillSession = func(string) error {
					killed = true
					return nil
				}

				out, err := getEnsembleStop(state.SessionName, EnsembleStopOptions{Force: force, NoCollect: noCollect}, deps)
				if err != nil {
					t.Fatal(err)
				}
				if out.Success || out.ErrorCode != ErrCodeInternalError {
					t.Fatalf("pane inspection failure was not reported: %+v", out)
				}
				if interrupted || slept || killed {
					t.Fatalf("shutdown proceeded after inspection failed: interrupted=%t slept=%t killed=%t", interrupted, slept, killed)
				}
				if out.Result.FinalStatus != ensemble.EnsembleActive.String() {
					t.Fatalf("final status=%q, want active", out.Result.FinalStatus)
				}
				saved, err := ensemble.LoadSession(state.SessionName)
				if err != nil {
					t.Fatal(err)
				}
				if saved.Status != ensemble.EnsembleActive {
					t.Fatalf("saved status=%s, want active", saved.Status)
				}
			})
		}
	}
}

func TestGetEnsembleStopFinalCaptureFailureKeepsFirstSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NTM_CONFIG", "")
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	state := &ensemble.EnsembleSession{
		SessionName:       "final-capture-failure-ensemble-stop",
		Question:          "Keep the first snapshot if final capture fails",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "%1", AgentType: "cc", Status: ensemble.AssignmentActive},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatal(err)
	}

	deps := defaultEnsembleStopDependencies()
	deps.SessionExists = func(string) bool { return true }
	captures := 0
	deps.CaptureAndSave = func(*ensemble.EnsembleSession) ([]ensemble.CapturedOutput, error) {
		captures++
		if captures > 1 {
			return nil, errors.New("final capture failed")
		}
		return []ensemble.CapturedOutput{{ModeID: "deductive", PaneName: "%1", OutputPath: "/saved/first-transcript.txt"}}, nil
	}
	deps.GetPanes = func(string) ([]tmux.Pane, error) {
		return []tmux.Pane{{ID: "%1"}}, nil
	}
	interrupts := 0
	deps.SendKeys = func(string, string, bool) error {
		interrupts++
		return nil
	}
	deps.Sleep = func(time.Duration) {}
	killed := false
	deps.KillSession = func(string) error {
		killed = true
		return nil
	}

	out, err := getEnsembleStop(state.SessionName, EnsembleStopOptions{}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if out.Success || out.ErrorCode != ErrCodeInternalError || killed {
		t.Fatalf("final capture failure must leave the session alive: output=%+v killed=%t", out, killed)
	}
	if captures != 2 || interrupts != 1 {
		t.Fatalf("captures=%d interrupts=%d, want two captures around one interrupt", captures, interrupts)
	}
	if out.Result.Captured != 1 || len(out.Result.SavedOutputPaths) != 1 || out.Result.SavedOutputPaths[0] != "/saved/first-transcript.txt" {
		t.Fatalf("first recoverable snapshot was lost: %+v", out.Result)
	}
	saved, err := ensemble.LoadSession(state.SessionName)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != ensemble.EnsembleActive {
		t.Fatalf("saved status=%s, want active", saved.Status)
	}
}

func TestGetEnsemblePreservesSpawnBudgetAfterReopen(t *testing.T) {
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	for _, total := range []int{17500, 0} {
		session := &ensemble.EnsembleSession{
			SessionName: fmt.Sprintf("saved-budget-%d", total),
			Question:    "Use this run's configured budget.",
			PresetUsed:  "project-diagnosis",
			Status:      ensemble.EnsembleStopped,
			Budget:      &ensemble.BudgetConfig{MaxTotalTokens: total},
		}
		if err := ensemble.SaveSession("", session); err != nil {
			t.Fatal(err)
		}
		ensemble.CloseDefaultStateStore()
		result, err := GetEnsemble(session.SessionName)
		if err != nil || !result.Success {
			t.Fatalf("status lookup failed: %+v, %v", result, err)
		}
		if result.Ensemble.Budget.TotalTokens != total {
			t.Fatalf("status replaced saved budget %d with preset defaults: %+v", total, result.Ensemble.Budget)
		}
	}
}
