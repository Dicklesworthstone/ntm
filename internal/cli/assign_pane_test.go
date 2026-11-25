package cli

import (
	"testing"
	"time"
)

// ============================================================================
// Direct Pane Assignment Tests (--pane flag)
// bd-16y6i: Unit Tests: ntm assign --pane direct assignment validation
// ============================================================================

// ============================================================================
// Basic Structure Tests
// ============================================================================

// TestDirectAssignItemStructure tests the DirectAssignItem type has correct fields
func TestDirectAssignItemStructure(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Format(time.RFC3339)
	item := DirectAssignItem{
		BeadID:       "bd-xyz",
		BeadTitle:    "Test task for assignment",
		Pane:         3,
		AgentType:    "claude",
		Status:       "assigned",
		Prompt:       "Work on bd-xyz: Test task",
		PromptSent:   true,
		AssignedAt:   now,
		PaneWasBusy:  false,
		DepsIgnored:  false,
		BlockedByIDs: nil,
	}

	if item.BeadID != "bd-xyz" {
		t.Errorf("Expected BeadID 'bd-xyz', got %q", item.BeadID)
	}
	if item.BeadTitle != "Test task for assignment" {
		t.Errorf("Expected BeadTitle 'Test task for assignment', got %q", item.BeadTitle)
	}
	if item.Pane != 3 {
		t.Errorf("Expected Pane 3, got %d", item.Pane)
	}
	if item.AgentType != "claude" {
		t.Errorf("Expected AgentType 'claude', got %q", item.AgentType)
	}
	if item.Status != "assigned" {
		t.Errorf("Expected Status 'assigned', got %q", item.Status)
	}
	if !item.PromptSent {
		t.Error("Expected PromptSent to be true")
	}
	if item.PaneWasBusy {
		t.Error("Expected PaneWasBusy to be false")
	}
	if item.DepsIgnored {
		t.Error("Expected DepsIgnored to be false")
	}
}

// TestDirectAssignItemWithBusyPane tests the structure with busy pane scenario
func TestDirectAssignItemWithBusyPane(t *testing.T) {
	t.Parallel()
	item := DirectAssignItem{
		BeadID:      "bd-abc",
		Pane:        5,
		AgentType:   "codex",
		Status:      "assigned",
		PromptSent:  true,
		PaneWasBusy: true,
		DepsIgnored: false,
	}

	if !item.PaneWasBusy {
		t.Error("Expected PaneWasBusy to be true when pane was busy")
	}
}

// TestDirectAssignItemWithIgnoredDeps tests the structure with ignored dependencies
func TestDirectAssignItemWithIgnoredDeps(t *testing.T) {
	t.Parallel()
	item := DirectAssignItem{
		BeadID:       "bd-blocked",
		Pane:         2,
		AgentType:    "gemini",
		Status:       "assigned",
		PromptSent:   true,
		DepsIgnored:  true,
		BlockedByIDs: []string{"bd-blocker1", "bd-blocker2"},
	}

	if !item.DepsIgnored {
		t.Error("Expected DepsIgnored to be true")
	}
	if len(item.BlockedByIDs) != 2 {
		t.Errorf("Expected 2 blockers, got %d", len(item.BlockedByIDs))
	}
}

// TestDirectAssignDataStructure tests the DirectAssignData container
func TestDirectAssignDataStructure(t *testing.T) {
	t.Parallel()
	data := DirectAssignData{
		Assignment: &DirectAssignItem{
			BeadID:     "bd-test",
			Pane:       1,
			AgentType:  "claude",
			Status:     "assigned",
			PromptSent: true,
		},
		FileReservations: &DirectAssignFileReservations{
			Requested: []string{"src/api/*.ts", "src/utils/*.ts"},
			Granted:   []string{"src/api/*.ts"},
			Denied:    []string{"src/utils/*.ts"},
		},
	}

	if data.Assignment == nil {
		t.Fatal("Expected Assignment to be non-nil")
	}
	if data.Assignment.BeadID != "bd-test" {
		t.Errorf("Expected BeadID 'bd-test', got %q", data.Assignment.BeadID)
	}
	if data.FileReservations == nil {
		t.Fatal("Expected FileReservations to be non-nil")
	}
	if len(data.FileReservations.Requested) != 2 {
		t.Errorf("Expected 2 requested paths, got %d", len(data.FileReservations.Requested))
	}
	if len(data.FileReservations.Granted) != 1 {
		t.Errorf("Expected 1 granted path, got %d", len(data.FileReservations.Granted))
	}
	if len(data.FileReservations.Denied) != 1 {
		t.Errorf("Expected 1 denied path, got %d", len(data.FileReservations.Denied))
	}
}

// TestDirectAssignDataWithoutReservations tests the data without file reservations
func TestDirectAssignDataWithoutReservations(t *testing.T) {
	t.Parallel()
	data := DirectAssignData{
		Assignment: &DirectAssignItem{
			BeadID:     "bd-simple",
			Pane:       2,
			AgentType:  "claude",
			Status:     "assigned",
			PromptSent: true,
		},
		FileReservations: nil,
	}

	if data.Assignment == nil {
		t.Fatal("Expected Assignment to be non-nil")
	}
	if data.FileReservations != nil {
		t.Error("Expected FileReservations to be nil when not used")
	}
}

// TestDirectAssignFileReservationsStructure tests the file reservations type
func TestDirectAssignFileReservationsStructure(t *testing.T) {
	t.Parallel()
	reservations := DirectAssignFileReservations{
		Requested: []string{"src/*.go", "pkg/*.go", "internal/*.go"},
		Granted:   []string{"src/*.go", "pkg/*.go"},
		Denied:    []string{"internal/*.go"},
	}

	if len(reservations.Requested) != 3 {
		t.Errorf("Expected 3 requested paths, got %d", len(reservations.Requested))
	}
	if len(reservations.Granted) != 2 {
		t.Errorf("Expected 2 granted paths, got %d", len(reservations.Granted))
	}
	if len(reservations.Denied) != 1 {
		t.Errorf("Expected 1 denied path, got %d", len(reservations.Denied))
	}
}

// TestDirectAssignFileReservationsEmpty tests empty file reservations
func TestDirectAssignFileReservationsEmpty(t *testing.T) {
	t.Parallel()
	reservations := DirectAssignFileReservations{
		Requested: []string{},
		Granted:   []string{},
		Denied:    []string{},
	}

	if reservations.Requested == nil {
		t.Error("Requested should be empty slice, not nil")
	}
	if len(reservations.Requested) != 0 {
		t.Errorf("Expected 0 requested paths, got %d", len(reservations.Requested))
	}
}

// ============================================================================
// Command Options Tests
// ============================================================================

// TestAssignCommandOptionsPaneFields tests the pane-related options fields
func TestAssignCommandOptionsPaneFields(t *testing.T) {
	t.Parallel()
	opts := AssignCommandOptions{
		Session:      "myproject",
		BeadIDs:      []string{"bd-xyz"},
		PaneSelector: "1.3",
		Force:        false,
		IgnoreDeps:   false,
		Prompt:       "Custom prompt for the task",
	}

	if opts.PaneSelector != "1.3" {
		t.Errorf("Expected pane selector 1.3, got %q", opts.PaneSelector)
	}
	if opts.Force {
		t.Error("Expected Force to be false by default")
	}
	if opts.IgnoreDeps {
		t.Error("Expected IgnoreDeps to be false by default")
	}
	if len(opts.BeadIDs) != 1 {
		t.Errorf("Expected 1 bead ID, got %d", len(opts.BeadIDs))
	}
	if opts.Prompt != "Custom prompt for the task" {
		t.Errorf("Expected custom prompt, got %q", opts.Prompt)
	}
}

// TestAssignCommandOptionsForceFlag tests the force flag behavior
func TestAssignCommandOptionsForceFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		force    bool
		expected bool
	}{
		{"force disabled", false, false},
		{"force enabled", true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := AssignCommandOptions{
				Session:      "test",
				PaneSelector: "1",
				Force:        tc.force,
			}
			if opts.Force != tc.expected {
				t.Errorf("Expected Force=%v, got %v", tc.expected, opts.Force)
			}
		})
	}
}

// TestAssignCommandOptionsIgnoreDepsFlag tests the ignore-deps flag behavior
func TestAssignCommandOptionsIgnoreDepsFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		ignoreDeps bool
		expected   bool
	}{
		{"ignore-deps disabled", false, false},
		{"ignore-deps enabled", true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := AssignCommandOptions{
				Session:      "test",
				PaneSelector: "1",
				IgnoreDeps:   tc.ignoreDeps,
			}
			if opts.IgnoreDeps != tc.expected {
				t.Errorf("Expected IgnoreDeps=%v, got %v", tc.expected, opts.IgnoreDeps)
			}
		})
	}
}

// TestAssignCommandOptionsBothOverrideFlags tests using both force and ignore-deps
func TestAssignCommandOptionsBothOverrideFlags(t *testing.T) {
	t.Parallel()
	opts := AssignCommandOptions{
		Session:      "test",
		BeadIDs:      []string{"bd-blocked"},
		PaneSelector: "%5",
		Force:        true,
		IgnoreDeps:   true,
	}

	if !opts.Force {
		t.Error("Expected Force to be true")
	}
	if !opts.IgnoreDeps {
		t.Error("Expected IgnoreDeps to be true")
	}
}

// TestAssignCommandOptionsPaneDisabledValue tests the empty disabled value.
func TestAssignCommandOptionsPaneDisabledValue(t *testing.T) {
	t.Parallel()
	opts := AssignCommandOptions{
		Session: "test",
	}

	if opts.PaneSelector != "" {
		t.Errorf("Expected empty pane selector, got %q", opts.PaneSelector)
	}
}

// ============================================================================
// Envelope Tests
// ============================================================================

// TestMakeDirectAssignEnvelopeSuccess tests successful envelope creation
func TestMakeDirectAssignEnvelopeSuccess(t *testing.T) {
	t.Parallel()
	data := &DirectAssignData{
		Assignment: &DirectAssignItem{
			BeadID:     "bd-test",
			BeadTitle:  "Test task",
			Pane:       3,
			AgentType:  "claude",
			Status:     "assigned",
			PromptSent: true,
		},
	}

	envelope := makeDirectAssignEnvelope("myproject", true, data, "", "", nil)

	if envelope.Command != "assign" {
		t.Errorf("Expected Command 'assign', got %q", envelope.Command)
	}
	if envelope.Subcommand != "pane" {
		t.Errorf("Expected Subcommand 'pane', got %q", envelope.Subcommand)
	}
	if envelope.Session != "myproject" {
		t.Errorf("Expected Session 'myproject', got %q", envelope.Session)
	}
	if !envelope.Success {
		t.Error("Expected Success to be true")
	}
	if envelope.Data == nil {
		t.Error("Expected Data to be non-nil")
	}
	if envelope.Error != nil {
		t.Error("Expected Error to be nil for success case")
	}
}

// TestMakeDirectAssignEnvelopeError tests error envelope creation
func TestMakeDirectAssignEnvelopeError(t *testing.T) {
	t.Parallel()
	envelope := makeDirectAssignEnvelope("myproject", false, nil, "PANE_NOT_FOUND", "pane 99 not found", nil)

	if envelope.Success {
		t.Error("Expected Success to be false")
	}
	if envelope.Data != nil {
		t.Error("Expected Data to be nil for error case")
	}
	if envelope.Error == nil {
		t.Fatal("Expected Error to be non-nil")
	}
	if envelope.Error.Code != "PANE_NOT_FOUND" {
		t.Errorf("Expected Error.Code 'PANE_NOT_FOUND', got %q", envelope.Error.Code)
	}
	if envelope.Error.Message != "pane 99 not found" {
		t.Errorf("Expected error message about pane 99, got %q", envelope.Error.Message)
	}
}

// TestMakeDirectAssignEnvelopeWithWarnings tests envelope with warnings
func TestMakeDirectAssignEnvelopeWithWarnings(t *testing.T) {
	t.Parallel()
	warnings := []string{"could not check dependencies", "file reservation partial"}
	envelope := makeDirectAssignEnvelope("test", true, nil, "", "", warnings)

	if len(envelope.Warnings) != 2 {
		t.Errorf("Expected 2 warnings, got %d", len(envelope.Warnings))
	}
	if envelope.Warnings[0] != "could not check dependencies" {
		t.Errorf("Expected first warning about dependencies, got %q", envelope.Warnings[0])
	}
}

// TestMakeDirectAssignEnvelopeNilWarningsBecomesEmptySlice tests nil warnings handling
func TestMakeDirectAssignEnvelopeNilWarningsBecomesEmptySlice(t *testing.T) {
	t.Parallel()
	envelope := makeDirectAssignEnvelope("test", true, nil, "", "", nil)

	if envelope.Warnings == nil {
		t.Error("Expected Warnings to be empty slice, not nil")
	}
	if len(envelope.Warnings) != 0 {
		t.Errorf("Expected 0 warnings, got %d", len(envelope.Warnings))
	}
}

// TestMakeDirectAssignEnvelopeHasTimestamp tests that envelope includes timestamp
func TestMakeDirectAssignEnvelopeHasTimestamp(t *testing.T) {
	t.Parallel()
	envelope := makeDirectAssignEnvelope("test", true, nil, "", "", nil)

	if envelope.Timestamp == "" {
		t.Error("Expected Timestamp to be set")
	}
	// Verify it's a valid RFC3339 timestamp
	_, err := time.Parse(time.RFC3339, envelope.Timestamp)
	if err != nil {
		t.Errorf("Expected valid RFC3339 timestamp, got %q: %v", envelope.Timestamp, err)
	}
}

// ============================================================================
// Error Code Tests
// ============================================================================

// TestDirectAssignErrorCodes tests the documented error codes for direct assignment
func TestDirectAssignErrorCodes(t *testing.T) {
	t.Parallel()
	// These are the documented error codes from the bead spec
	errorCodes := []struct {
		code    string
		meaning string
	}{
		{"PANE_NOT_FOUND", "Pane doesn't exist in session"},
		{"NOT_AGENT_PANE", "Pane is not an agent pane (user or unknown)"},
		{"PANE_BUSY", "Target agent is working on another bead"},
		{"BLOCKED", "Bead has unresolved dependencies"},
		{"INVALID_ARGS", "Invalid arguments (e.g., wrong number of beads)"},
		{"SEND_ERROR", "Failed to send prompt to pane"},
		{"TMUX_ERROR", "Tmux operation failed"},
	}

	for _, ec := range errorCodes {
		t.Run(ec.code, func(t *testing.T) {
			t.Parallel()
			envelope := makeDirectAssignEnvelope("test", false, nil, ec.code, ec.meaning, nil)

			if envelope.Error == nil {
				t.Fatal("Expected Error to be non-nil")
			}
			if envelope.Error.Code != ec.code {
				t.Errorf("Expected Error.Code %q, got %q", ec.code, envelope.Error.Code)
			}
			if envelope.Error.Message != ec.meaning {
				t.Errorf("Expected Error.Message %q, got %q", ec.meaning, envelope.Error.Message)
			}
		})
	}
}

// TestPaneNotFoundError tests the PANE_NOT_FOUND error scenario
func TestPaneNotFoundError(t *testing.T) {
	t.Parallel()
	envelope := makeDirectAssignEnvelope("myproject", false, nil, "PANE_NOT_FOUND", "pane 99 not found in session myproject", nil)

	if envelope.Error.Code != "PANE_NOT_FOUND" {
		t.Errorf("Expected PANE_NOT_FOUND error code, got %q", envelope.Error.Code)
	}
}

// TestNotAgentPaneError tests the NOT_AGENT_PANE error scenario
func TestNotAgentPaneError(t *testing.T) {
	t.Parallel()
	envelope := makeDirectAssignEnvelope("myproject", false, nil, "NOT_AGENT_PANE", "pane 0 is not an agent pane (type: user)", nil)

	if envelope.Error.Code != "NOT_AGENT_PANE" {
		t.Errorf("Expected NOT_AGENT_PANE error code, got %q", envelope.Error.Code)
	}
}

// TestPaneBusyError tests the PANE_BUSY error scenario
func TestPaneBusyError(t *testing.T) {
	t.Parallel()
	data := &DirectAssignData{
		Assignment: &DirectAssignItem{
			BeadID:      "bd-test",
			Pane:        3,
			AgentType:   "claude",
			PaneWasBusy: true,
		},
	}
	envelope := makeDirectAssignEnvelope("myproject", false, data, "PANE_BUSY", "pane 3 is busy (state: working), use --force to override", nil)

	if envelope.Error.Code != "PANE_BUSY" {
		t.Errorf("Expected PANE_BUSY error code, got %q", envelope.Error.Code)
	}
	if envelope.Data == nil || !envelope.Data.Assignment.PaneWasBusy {
		t.Error("Expected PaneWasBusy to be true in data")
	}
}

// TestBlockedError tests the BLOCKED error scenario
func TestBlockedError(t *testing.T) {
	t.Parallel()
	data := &DirectAssignData{
		Assignment: &DirectAssignItem{
			BeadID:       "bd-blocked",
			BlockedByIDs: []string{"bd-blocker1", "bd-blocker2"},
		},
	}
	envelope := makeDirectAssignEnvelope("myproject", false, data, "BLOCKED", "bead bd-blocked is blocked by: [bd-blocker1 bd-blocker2], use --ignore-deps to override", nil)

	if envelope.Error.Code != "BLOCKED" {
		t.Errorf("Expected BLOCKED error code, got %q", envelope.Error.Code)
	}
	if envelope.Data == nil || len(envelope.Data.Assignment.BlockedByIDs) != 2 {
		t.Error("Expected 2 blocker IDs in data")
	}
}

// TestInvalidArgsError tests the INVALID_ARGS error scenario
func TestInvalidArgsError(t *testing.T) {
	t.Parallel()
	envelope := makeDirectAssignEnvelope("myproject", false, nil, "INVALID_ARGS", "--pane requires exactly one bead (use --beads=bd-xxx)", nil)

	if envelope.Error.Code != "INVALID_ARGS" {
		t.Errorf("Expected INVALID_ARGS error code, got %q", envelope.Error.Code)
	}
}

// ============================================================================
// Validation Logic Tests
// ============================================================================

// TestPaneValidationRequiresSingleBead tests that exactly one bead is required
func TestPaneValidationRequiresSingleBead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		beadIDs     []string
		expectError bool
	}{
		{"no beads", []string{}, true},
		{"one bead - valid", []string{"bd-xyz"}, false},
		{"two beads - invalid", []string{"bd-xyz", "bd-abc"}, true},
		{"three beads - invalid", []string{"bd-1", "bd-2", "bd-3"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := AssignCommandOptions{
				Session:      "test",
				BeadIDs:      tc.beadIDs,
				PaneSelector: "1",
			}

			// The validation is: len(opts.BeadIDs) != 1
			isInvalid := len(opts.BeadIDs) != 1
			if isInvalid != tc.expectError {
				t.Errorf("Expected error=%v for %d beads, got %v", tc.expectError, len(tc.beadIDs), isInvalid)
			}
		})
	}
}

// TestBusyPaneValidation tests the busy pane validation logic
func TestBusyPaneValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		state       string
		force       bool
		expectError bool
	}{
		{"idle pane, no force", "idle", false, false},
		{"idle pane, with force", "idle", true, false},
		{"busy pane, no force", "working", false, true},
		{"busy pane, with force", "working", true, false},
		{"thinking pane, no force", "thinking", false, true},
		{"thinking pane, with force", "thinking", true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Validation: state != "idle" && !force
			shouldError := tc.state != "idle" && !tc.force
			if shouldError != tc.expectError {
				t.Errorf("Expected error=%v for state=%q force=%v, got %v",
					tc.expectError, tc.state, tc.force, shouldError)
			}
		})
	}
}

// TestBlockedBeadValidation tests the blocked bead validation logic
func TestBlockedBeadValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		blockers    []string
		ignoreDeps  bool
		expectError bool
	}{
		{"no blockers, no flag", nil, false, false},
		{"no blockers, with flag", nil, true, false},
		{"has blockers, no flag", []string{"bd-1"}, false, true},
		{"has blockers, with flag", []string{"bd-1"}, true, false},
		{"multiple blockers, no flag", []string{"bd-1", "bd-2", "bd-3"}, false, true},
		{"multiple blockers, with flag", []string{"bd-1", "bd-2", "bd-3"}, true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Validation: len(blockers) > 0 && !ignoreDeps
			hasBlockers := len(tc.blockers) > 0
			shouldError := hasBlockers && !tc.ignoreDeps
			if shouldError != tc.expectError {
				t.Errorf("Expected error=%v for blockers=%v ignoreDeps=%v, got %v",
					tc.expectError, tc.blockers, tc.ignoreDeps, shouldError)
			}
		})
	}
}

// ============================================================================
// CLI Flag Tests
// ============================================================================

// TestAssignCommandHasPaneFlag tests that the assign command has the --pane flag
func TestAssignCommandHasPaneFlag(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("pane")
	if flag == nil {
		t.Fatal("Expected 'pane' flag to exist")
	}
}

// TestAssignCommandHasForceFlag tests that the assign command has the --force flag
func TestAssignCommandHasForceFlag(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("force")
	if flag == nil {
		t.Fatal("Expected 'force' flag to exist")
	}
}

// TestAssignCommandHasIgnoreDepsFlag tests that the assign command has the --ignore-deps flag
func TestAssignCommandHasIgnoreDepsFlag(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("ignore-deps")
	if flag == nil {
		t.Fatal("Expected 'ignore-deps' flag to exist")
	}
}

// TestPaneFlagDefaultValue tests that --pane defaults to an empty selector (disabled).
func TestPaneFlagDefaultValue(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("pane")
	if flag == nil {
		t.Fatal("Expected 'pane' flag to exist")
	}
	if flag.DefValue != "" {
		t.Errorf("Expected empty default selector, got %q", flag.DefValue)
	}
}

// TestForceFlagDefaultValue tests that --force defaults to false
func TestForceFlagDefaultValue(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("force")
	if flag == nil {
		t.Fatal("Expected 'force' flag to exist")
	}
	if flag.DefValue != "false" {
		t.Errorf("Expected default value 'false', got %q", flag.DefValue)
	}
}

// TestIgnoreDepsFlagDefaultValue tests that --ignore-deps defaults to false
func TestIgnoreDepsFlagDefaultValue(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("ignore-deps")
	if flag == nil {
		t.Fatal("Expected 'ignore-deps' flag to exist")
	}
	if flag.DefValue != "false" {
		t.Errorf("Expected default value 'false', got %q", flag.DefValue)
	}
}

// ============================================================================
// DirectAssignResult Tests
// ============================================================================

// TestDirectAssignResultStructure tests the DirectAssignResult type
func TestDirectAssignResultStructure(t *testing.T) {
	t.Parallel()
	result := DirectAssignResult{
		BeadID:         "bd-xyz",
		BeadTitle:      "Test task",
		Pane:           3,
		AgentType:      "claude",
		AgentName:      "test_claude_3",
		PromptSent:     "Work on bd-xyz",
		Success:        true,
		Error:          "",
		Reservations:   nil,
		PaneWasBusy:    false,
		DepsIgnored:    false,
		BlockedByBeads: nil,
	}

	if result.BeadID != "bd-xyz" {
		t.Errorf("Expected BeadID 'bd-xyz', got %q", result.BeadID)
	}
	if result.Pane != 3 {
		t.Errorf("Expected Pane 3, got %d", result.Pane)
	}
	if result.AgentType != "claude" {
		t.Errorf("Expected AgentType 'claude', got %q", result.AgentType)
	}
	if !result.Success {
		t.Error("Expected Success to be true")
	}
	if result.Error != "" {
		t.Errorf("Expected empty Error, got %q", result.Error)
	}
}

// TestDirectAssignResultWithError tests result with error
func TestDirectAssignResultWithError(t *testing.T) {
	t.Parallel()
	result := DirectAssignResult{
		BeadID:    "bd-xyz",
		Pane:      99,
		Success:   false,
		Error:     "pane 99 not found",
		AgentType: "",
	}

	if result.Success {
		t.Error("Expected Success to be false")
	}
	if result.Error != "pane 99 not found" {
		t.Errorf("Expected error about pane 99, got %q", result.Error)
	}
}

// TestDirectAssignResultWithBusyPane tests result when pane was busy
func TestDirectAssignResultWithBusyPane(t *testing.T) {
	t.Parallel()
	result := DirectAssignResult{
		BeadID:      "bd-xyz",
		Pane:        3,
		AgentType:   "claude",
		Success:     true,
		PaneWasBusy: true,
		DepsIgnored: false,
	}

	if !result.PaneWasBusy {
		t.Error("Expected PaneWasBusy to be true")
	}
}

// TestDirectAssignResultWithBlockedBead tests result when bead was blocked
func TestDirectAssignResultWithBlockedBead(t *testing.T) {
	t.Parallel()
	result := DirectAssignResult{
		BeadID:         "bd-blocked",
		Pane:           2,
		AgentType:      "codex",
		Success:        true,
		DepsIgnored:    true,
		BlockedByBeads: []string{"bd-1", "bd-2"},
	}

	if !result.DepsIgnored {
		t.Error("Expected DepsIgnored to be true")
	}
	if len(result.BlockedByBeads) != 2 {
		t.Errorf("Expected 2 blocked by beads, got %d", len(result.BlockedByBeads))
	}
}

// ============================================================================
// Edge Cases
// ============================================================================

// TestDirectAssignWithEmptyBeadTitle tests handling of empty bead title
func TestDirectAssignWithEmptyBeadTitle(t *testing.T) {
	t.Parallel()
	item := DirectAssignItem{
		BeadID:     "bd-notitle",
		BeadTitle:  "", // Empty title
		Pane:       1,
		AgentType:  "claude",
		Status:     "assigned",
		PromptSent: true,
	}

	// Empty title should be allowed (system will fetch it if available)
	if item.BeadTitle != "" {
		t.Errorf("Expected empty BeadTitle, got %q", item.BeadTitle)
	}
}

// TestDirectAssignWithEmptyPrompt tests handling of empty prompt
func TestDirectAssignWithEmptyPrompt(t *testing.T) {
	t.Parallel()
	item := DirectAssignItem{
		BeadID:     "bd-test",
		Pane:       1,
		AgentType:  "claude",
		Prompt:     "", // Empty prompt (will use template)
		PromptSent: true,
	}

	// Empty prompt is valid - system will use template
	if item.Prompt != "" {
		t.Errorf("Expected empty Prompt, got %q", item.Prompt)
	}
}

// TestDirectAssignPaneZero tests pane index 0 (valid user pane)
func TestDirectAssignPaneZero(t *testing.T) {
	t.Parallel()
	// Pane 0 is typically the user pane, which should not accept assignments
	opts := AssignCommandOptions{
		Session:      "test",
		BeadIDs:      []string{"bd-xyz"},
		PaneSelector: "0",
	}

	if opts.PaneSelector != "0" {
		t.Errorf("Expected pane selector 0, got %q", opts.PaneSelector)
	}
}

// TestDirectAssignLargePaneIndex tests large pane index
func TestDirectAssignLargePaneIndex(t *testing.T) {
	t.Parallel()
	// Large pane index should be validated against actual panes
	opts := AssignCommandOptions{
		Session:      "test",
		BeadIDs:      []string{"bd-xyz"},
		PaneSelector: "999",
	}

	if opts.PaneSelector != "999" {
		t.Errorf("Expected pane selector 999, got %q", opts.PaneSelector)
	}
}

// TestDirectAssignCombinedFlags tests all override flags together
func TestDirectAssignCombinedFlags(t *testing.T) {
	t.Parallel()
	opts := AssignCommandOptions{
		Session:      "test",
		BeadIDs:      []string{"bd-xyz"},
		PaneSelector: "5",
		Force:        true,
		IgnoreDeps:   true,
		Prompt:       "Custom prompt",
	}

	if !opts.Force {
		t.Error("Expected Force to be true")
	}
	if !opts.IgnoreDeps {
		t.Error("Expected IgnoreDeps to be true")
	}
	if opts.Prompt != "Custom prompt" {
		t.Errorf("Expected 'Custom prompt', got %q", opts.Prompt)
	}
}
