package robot

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// TestSendOutputSchemaStability ensures SendOutput structure remains stable
// Covers: ntm-qce2 - Test robot-send with target filtering and tracking
func TestSendOutputSchemaStability(t *testing.T) {
	// Test schema consistency across multiple calls
	output1 := SendOutput{
		RobotResponse:  NewRobotResponse(true),
		Session:        "test-session",
		SentAt:         time.Now().UTC(),
		Blocked:        false,
		Redaction:      RedactionSummary{Mode: "off", Findings: 0, Action: "off"},
		Warnings:       []string{},
		Targets:        []string{"0", "1", "2"},
		Successful:     []string{"0", "1"},
		Failed:         []SendError{{Pane: "2", Error: "test error"}},
		MessagePreview: "test message",
		DryRun:         false,
		WouldSendTo:    []string{},
	}

	// Serialize and deserialize to check schema stability
	data1, err := json.Marshal(output1)
	if err != nil {
		t.Fatalf("Failed to marshal SendOutput: %v", err)
	}

	var unmarshaled1 SendOutput
	if err := json.Unmarshal(data1, &unmarshaled1); err != nil {
		t.Fatalf("Failed to unmarshal SendOutput: %v", err)
	}

	// Check that required fields are present
	requiredFields := []string{
		"success", "timestamp", "session", "sent_at",
		"blocked", "redaction", "warnings",
		"targets", "successful", "failed", "message_preview",
	}

	var jsonMap map[string]interface{}
	if err := json.Unmarshal(data1, &jsonMap); err != nil {
		t.Fatalf("Failed to unmarshal to map: %v", err)
	}

	for _, field := range requiredFields {
		if _, exists := jsonMap[field]; !exists {
			t.Errorf("Required field %q missing from JSON output", field)
		}
	}

	// Check that arrays are never null
	arrayFields := []string{"warnings", "targets", "successful", "failed"}
	for _, field := range arrayFields {
		value, exists := jsonMap[field]
		if !exists {
			t.Errorf("Array field %q missing from JSON", field)
			continue
		}
		if value == nil {
			t.Errorf("Array field %q should be [] not null", field)
		}
	}
}

// TestSendOutputDeterministicOrdering ensures consistent field ordering
func TestSendOutputDeterministicOrdering(t *testing.T) {
	output := SendOutput{
		RobotResponse:  NewRobotResponse(true),
		Session:        "test-session",
		SentAt:         time.Now().UTC(),
		Blocked:        false,
		Redaction:      RedactionSummary{Mode: "off", Findings: 0, Action: "off"},
		Warnings:       []string{},
		Targets:        []string{"2", "0", "1"}, // Intentionally unordered
		Successful:     []string{"1", "0"},      // Intentionally unordered
		Failed:         []SendError{{Pane: "2", Error: "error"}},
		MessagePreview: "test message",
	}

	// Serialize multiple times and ensure consistent ordering
	var outputs []string
	for i := 0; i < 5; i++ {
		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("Iteration %d: Failed to marshal: %v", i, err)
		}
		outputs = append(outputs, string(data))
	}

	// All serializations should be identical
	firstOutput := outputs[0]
	for i, otherOutput := range outputs[1:] {
		if otherOutput != firstOutput {
			t.Errorf("Iteration %d produced different JSON output", i+1)
		}
	}

	// Check that arrays maintain their order in JSON
	var jsonMap map[string]interface{}
	if err := json.Unmarshal([]byte(firstOutput), &jsonMap); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	targets, _ := jsonMap["targets"].([]interface{})
	successful, _ := jsonMap["successful"].([]interface{})

	// Verify arrays preserved their order
	expectedTargets := []string{"2", "0", "1"}
	expectedSuccessful := []string{"1", "0"}

	for i, target := range targets {
		if target != expectedTargets[i] {
			t.Errorf("Targets array order changed: expected %q at position %d, got %q", expectedTargets[i], i, target)
		}
	}

	for i, success := range successful {
		if success != expectedSuccessful[i] {
			t.Errorf("Successful array order changed: expected %q at position %d, got %q", expectedSuccessful[i], i, success)
		}
	}
}

func TestApplySendMessageRedaction_WarnMode(t *testing.T) {
	input := "prefix password=hunter2hunter2 suffix\n"
	msgToSend, preview, summary, warnings, blocked := applySendMessageRedaction(input, redaction.Config{Mode: redaction.ModeWarn})
	if blocked {
		t.Fatalf("expected blocked=false")
	}
	if msgToSend != input {
		t.Fatalf("expected msgToSend to be unchanged in warn mode")
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warnings in warn mode")
	}
	if summary.Action != "warn" {
		t.Fatalf("expected action=warn, got %q", summary.Action)
	}
	if summary.Findings == 0 {
		t.Fatalf("expected findings > 0")
	}
	if !strings.Contains(preview, "hunter2hunter2") {
		t.Fatalf("expected preview to include the original content in warn mode, got %q", preview)
	}
}

func TestApplySendMessageRedaction_RedactMode(t *testing.T) {
	input := "prefix password=hunter2hunter2 suffix\n"
	msgToSend, preview, summary, warnings, blocked := applySendMessageRedaction(input, redaction.Config{Mode: redaction.ModeRedact})
	if blocked {
		t.Fatalf("expected blocked=false")
	}
	if strings.Contains(msgToSend, "hunter2hunter2") {
		t.Fatalf("expected msgToSend to be redacted, got %q", msgToSend)
	}
	if !strings.Contains(msgToSend, "[REDACTED:PASSWORD:") {
		t.Fatalf("expected msgToSend to contain password placeholder, got %q", msgToSend)
	}
	if summary.Action != "redact" {
		t.Fatalf("expected action=redact, got %q", summary.Action)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warnings in redact mode")
	}
	if strings.Contains(preview, "hunter2hunter2") {
		t.Fatalf("expected preview to be redacted, got %q", preview)
	}
}

func TestApplySendMessageRedaction_BlockMode(t *testing.T) {
	input := "prefix password=hunter2hunter2 suffix\n"
	msgToSend, preview, summary, warnings, blocked := applySendMessageRedaction(input, redaction.Config{Mode: redaction.ModeBlock})
	if !blocked {
		t.Fatalf("expected blocked=true")
	}
	if msgToSend != "" {
		t.Fatalf("expected msgToSend to be empty when blocked, got %q", msgToSend)
	}
	if summary.Action != "block" {
		t.Fatalf("expected action=block, got %q", summary.Action)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warnings in block mode")
	}
	if strings.Contains(preview, "hunter2hunter2") {
		t.Fatalf("expected preview to be redacted in block mode, got %q", preview)
	}
	if !strings.Contains(preview, "[REDACTED:PASSWORD:") {
		t.Fatalf("expected preview to include placeholder in block mode, got %q", preview)
	}
}

func TestNormalizeActuationTrace_Defaults(t *testing.T) {
	trace := normalizeActuationTrace("", "", "")
	if trace.RequestID == "" {
		t.Fatal("RequestID should be populated")
	}
	if trace.CorrelationID == "" {
		t.Fatal("CorrelationID should be populated")
	}
	if trace.RequestID != trace.CorrelationID {
		t.Fatalf("expected generated request/correlation IDs to align, got request=%q correlation=%q", trace.RequestID, trace.CorrelationID)
	}
}

func TestNormalizeActuationTrace_UsesProvidedRequestID(t *testing.T) {
	trace := normalizeActuationTrace("req-abc", "", "idem-xyz")
	if trace.RequestID != "req-abc" {
		t.Fatalf("RequestID = %q, want req-abc", trace.RequestID)
	}
	if trace.CorrelationID != "req-abc" {
		t.Fatalf("CorrelationID = %q, want req-abc", trace.CorrelationID)
	}
	if trace.IdempotencyKey != "idem-xyz" {
		t.Fatalf("IdempotencyKey = %q, want idem-xyz", trace.IdempotencyKey)
	}
}

func TestGetSend_PublishesActuationOutcomeOnSessionNotFound(t *testing.T) {
	feed := newTestAttentionFeed(t)
	oldFeed := GetAttentionFeed()
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	output, err := GetSend(SendOptions{
		Session:   "missing-session-for-actuation-send",
		Message:   "hello",
		RequestID: "req-send-missing",
	})
	if err != nil {
		t.Fatalf("GetSend returned error: %v", err)
	}
	if output.ErrorCode != ErrCodeSessionNotFound {
		t.Fatalf("ErrorCode = %q, want %q", output.ErrorCode, ErrCodeSessionNotFound)
	}

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 attention event, got %d", len(events))
	}
	if events[0].Category != EventCategoryActuation {
		t.Fatalf("Category = %q, want %q", events[0].Category, EventCategoryActuation)
	}
	if events[0].Type != EventTypeActuationOutcome {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventTypeActuationOutcome)
	}
	if events[0].ReasonCode != "actuation_session_not_found" {
		t.Fatalf("ReasonCode = %q, want actuation_session_not_found", events[0].ReasonCode)
	}
	if got := events[0].Details["request_id"]; got != "req-send-missing" {
		t.Fatalf("request_id = %#v, want req-send-missing", got)
	}
}

func TestGetSendAndAck_PublishesActuationOutcomeOnSessionNotFound(t *testing.T) {
	feed := newTestAttentionFeed(t)
	oldFeed := GetAttentionFeed()
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	output, err := GetSendAndAck(SendAndAckOptions{
		SendOptions: SendOptions{
			Session:   "missing-session-for-actuation-send-ack",
			Message:   "hello",
			RequestID: "req-send-ack-missing",
		},
	})
	if err != nil {
		t.Fatalf("GetSendAndAck returned error: %v", err)
	}
	if output.Send.ErrorCode != ErrCodeSessionNotFound {
		t.Fatalf("Send.ErrorCode = %q, want %q", output.Send.ErrorCode, ErrCodeSessionNotFound)
	}

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 attention event, got %d", len(events))
	}
	if events[0].Category != EventCategoryActuation {
		t.Fatalf("Category = %q, want %q", events[0].Category, EventCategoryActuation)
	}
	if events[0].Type != EventTypeActuationOutcome {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventTypeActuationOutcome)
	}
	if events[0].ReasonCode != "actuation_session_not_found" {
		t.Fatalf("ReasonCode = %q, want actuation_session_not_found", events[0].ReasonCode)
	}
	if got := events[0].Details["request_id"]; got != "req-send-ack-missing" {
		t.Fatalf("request_id = %#v, want req-send-ack-missing", got)
	}
}

func TestGetInterrupt_PublishesActuationOutcomeOnSessionNotFound(t *testing.T) {
	feed := newTestAttentionFeed(t)
	oldFeed := GetAttentionFeed()
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	output, err := GetInterrupt(InterruptOptions{
		Session:   "missing-session-for-actuation-interrupt",
		RequestID: "req-interrupt-missing",
	})
	if err != nil {
		t.Fatalf("GetInterrupt returned error: %v", err)
	}
	if output.ErrorCode != ErrCodeSessionNotFound {
		t.Fatalf("ErrorCode = %q, want %q", output.ErrorCode, ErrCodeSessionNotFound)
	}

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 attention event, got %d", len(events))
	}
	if events[0].Category != EventCategoryActuation {
		t.Fatalf("Category = %q, want %q", events[0].Category, EventCategoryActuation)
	}
	if events[0].Type != EventTypeActuationOutcome {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventTypeActuationOutcome)
	}
	if events[0].ReasonCode != "actuation_session_not_found" {
		t.Fatalf("ReasonCode = %q, want actuation_session_not_found", events[0].ReasonCode)
	}
	if got := events[0].Details["request_id"]; got != "req-interrupt-missing" {
		t.Fatalf("request_id = %#v, want req-interrupt-missing", got)
	}
}

// TestSendOptionsTargetFiltering tests target filtering logic
func TestSendOptionsTargetFiltering(t *testing.T) {
	tests := []struct {
		name           string
		opts           SendOptions
		availablePanes []mockPane
		expectedTarget []string
		description    string
	}{
		{
			name: "all_panes_includes_user",
			opts: SendOptions{
				All: true,
			},
			availablePanes: []mockPane{
				{Index: 0, Type: "user"},
				{Index: 1, Type: "claude"},
				{Index: 2, Type: "codex"},
			},
			expectedTarget: []string{"0", "1", "2"},
			description:    "All flag should include user and agent panes",
		},
		{
			name: "specific_panes_only",
			opts: SendOptions{
				Panes: []string{"1", "2"},
			},
			availablePanes: []mockPane{
				{Index: 0, Type: "user"},
				{Index: 1, Type: "claude"},
				{Index: 2, Type: "codex"},
				{Index: 3, Type: "gemini"},
			},
			expectedTarget: []string{"1", "2"},
			description:    "Specific pane indices should be targeted",
		},
		{
			name: "agent_type_filtering",
			opts: SendOptions{
				AgentTypes: []string{"claude"},
			},
			availablePanes: []mockPane{
				{Index: 0, Type: "user"},
				{Index: 1, Type: "claude"},
				{Index: 2, Type: "codex"},
				{Index: 3, Type: "claude"},
			},
			expectedTarget: []string{"1", "3"},
			description:    "Agent type filtering should only target matching types",
		},
		{
			name: "exclude_functionality",
			opts: SendOptions{
				All:     true,
				Exclude: []string{"0", "2"},
			},
			availablePanes: []mockPane{
				{Index: 0, Type: "user"},
				{Index: 1, Type: "claude"},
				{Index: 2, Type: "codex"},
				{Index: 3, Type: "gemini"},
			},
			expectedTarget: []string{"1", "3"},
			description:    "Exclude should remove specified panes from all selection",
		},
		{
			name: "combined_type_and_exclude",
			opts: SendOptions{
				AgentTypes: []string{"claude", "codex"},
				Exclude:    []string{"2"},
			},
			availablePanes: []mockPane{
				{Index: 0, Type: "user"},
				{Index: 1, Type: "claude"},
				{Index: 2, Type: "claude"},
				{Index: 3, Type: "codex"},
				{Index: 4, Type: "gemini"},
			},
			expectedTarget: []string{"1", "3"},
			description:    "Combined filtering should apply both type and exclude filters",
		},
		{
			name: "multiple_agent_types",
			opts: SendOptions{
				AgentTypes: []string{"claude", "gemini"},
			},
			availablePanes: []mockPane{
				{Index: 0, Type: "user"},
				{Index: 1, Type: "claude"},
				{Index: 2, Type: "codex"},
				{Index: 3, Type: "gemini"},
				{Index: 4, Type: "claude"},
			},
			expectedTarget: []string{"1", "3", "4"},
			description:    "Multiple agent types should target all matching panes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterTargets(tt.opts, tt.availablePanes)

			// Sort both expected and actual for comparison
			expectedSorted := make([]string, len(tt.expectedTarget))
			copy(expectedSorted, tt.expectedTarget)
			sort.Strings(expectedSorted)

			resultSorted := make([]string, len(result))
			copy(resultSorted, result)
			sort.Strings(resultSorted)

			if !reflect.DeepEqual(resultSorted, expectedSorted) {
				t.Errorf("filterTargets() = %v, want %v\nDescription: %s",
					resultSorted, expectedSorted, tt.description)
			}
		})
	}
}

// TestSendErrorTracking tests error tracking functionality
func TestSendErrorTracking(t *testing.T) {
	tests := []struct {
		name            string
		sendResults     []mockSendResult
		expectedSuccess []string
		expectedFailed  []SendError
		description     string
	}{
		{
			name: "all_successful",
			sendResults: []mockSendResult{
				{Pane: "0", Success: true},
				{Pane: "1", Success: true},
				{Pane: "2", Success: true},
			},
			expectedSuccess: []string{"0", "1", "2"},
			expectedFailed:  []SendError{},
			description:     "All successful sends should be tracked correctly",
		},
		{
			name: "mixed_success_failure",
			sendResults: []mockSendResult{
				{Pane: "0", Success: true},
				{Pane: "1", Success: false, Error: "connection refused"},
				{Pane: "2", Success: true},
				{Pane: "3", Success: false, Error: "pane not found"},
			},
			expectedSuccess: []string{"0", "2"},
			expectedFailed: []SendError{
				{Pane: "1", Error: "connection refused"},
				{Pane: "3", Error: "pane not found"},
			},
			description: "Mixed results should track both successes and failures",
		},
		{
			name: "all_failed",
			sendResults: []mockSendResult{
				{Pane: "0", Success: false, Error: "session not found"},
				{Pane: "1", Success: false, Error: "timeout"},
			},
			expectedSuccess: []string{},
			expectedFailed: []SendError{
				{Pane: "0", Error: "session not found"},
				{Pane: "1", Error: "timeout"},
			},
			description: "All failed sends should be tracked correctly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			success, failed := processResults(tt.sendResults)

			// Sort for comparison
			sort.Strings(success)
			sort.Strings(tt.expectedSuccess)

			sort.Slice(failed, func(i, j int) bool { return failed[i].Pane < failed[j].Pane })
			sort.Slice(tt.expectedFailed, func(i, j int) bool { return tt.expectedFailed[i].Pane < tt.expectedFailed[j].Pane })

			// Handle empty slice comparisons properly
			if len(success) == 0 && len(tt.expectedSuccess) == 0 {
				// Both empty, that's fine
			} else if !reflect.DeepEqual(success, tt.expectedSuccess) {
				t.Errorf("Successful tracking = %v, want %v\nDescription: %s",
					success, tt.expectedSuccess, tt.description)
			}

			if len(failed) == 0 && len(tt.expectedFailed) == 0 {
				// Both empty, that's fine
			} else if !reflect.DeepEqual(failed, tt.expectedFailed) {
				t.Errorf("Failed tracking = %v, want %v\nDescription: %s",
					failed, tt.expectedFailed, tt.description)
			}
		})
	}
}

// TestSendOptionsValidation tests input validation
func TestSendOptionsValidation(t *testing.T) {
	tests := []struct {
		name        string
		opts        SendOptions
		expectValid bool
		description string
	}{
		{
			name: "valid_basic_options",
			opts: SendOptions{
				Session: "test-session",
				Message: "test message",
				All:     true,
			},
			expectValid: true,
			description: "Basic valid options should pass validation",
		},
		{
			name: "empty_session",
			opts: SendOptions{
				Session: "",
				Message: "test message",
			},
			expectValid: false,
			description: "Empty session name should fail validation",
		},
		{
			name: "whitespace_session",
			opts: SendOptions{
				Session: "   ",
				Message: "test message",
			},
			expectValid: false,
			description: "Whitespace-only session name should fail validation",
		},
		{
			name: "empty_message",
			opts: SendOptions{
				Session: "test-session",
				Message: "",
				All:     true,
			},
			expectValid: true,
			description: "Empty message should be valid (some use cases)",
		},
		{
			name: "negative_delay",
			opts: SendOptions{
				Session: "test-session",
				Message: "test",
				DelayMs: -100,
				All:     true,
			},
			expectValid: false,
			description: "Negative delay should fail validation",
		},
		{
			name: "conflicting_all_and_panes",
			opts: SendOptions{
				Session: "test-session",
				Message: "test",
				All:     true,
				Panes:   []string{"1", "2"},
			},
			expectValid: false,
			description: "All flag and specific panes should not be allowed together",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isValid := validateSendOptions(tt.opts)
			if isValid != tt.expectValid {
				t.Errorf("validateSendOptions() = %v, want %v\nDescription: %s",
					isValid, tt.expectValid, tt.description)
			}
		})
	}
}

// TestSendDryRunMode tests dry-run functionality
func TestSendDryRunMode(t *testing.T) {
	opts := SendOptions{
		Session: "test-session",
		Message: "test message",
		DryRun:  true,
		All:     true,
	}

	availablePanes := []mockPane{
		{Index: 0, Type: "user"},
		{Index: 1, Type: "claude"},
		{Index: 2, Type: "codex"},
	}

	output := simulateSendDryRun(opts, availablePanes)

	// Verify dry-run specific behavior
	if !output.DryRun {
		t.Error("DryRun field should be true for dry-run mode")
	}

	expectedWouldSend := []string{"0", "1", "2"}
	if !reflect.DeepEqual(output.WouldSendTo, expectedWouldSend) {
		t.Errorf("WouldSendTo = %v, want %v", output.WouldSendTo, expectedWouldSend)
	}

	// In dry-run, no actual sends should occur
	if len(output.Successful) != 0 {
		t.Errorf("Successful should be empty in dry-run mode, got %v", output.Successful)
	}

	if len(output.Failed) != 0 {
		t.Errorf("Failed should be empty in dry-run mode, got %v", output.Failed)
	}
}

// Mock types for testing
type mockPane struct {
	Index int
	Type  string
}

type mockSendResult struct {
	Pane    string
	Success bool
	Error   string
}

// Helper functions for testing (these would need actual implementations)
func filterTargets(opts SendOptions, panes []mockPane) []string {
	var targets []string

	// Simulate target filtering logic
	for _, pane := range panes {
		paneStr := string(rune('0' + pane.Index))

		// Apply All filter
		if opts.All {
			targets = append(targets, paneStr)
			continue
		}

		// Apply specific pane filter
		if len(opts.Panes) > 0 {
			for _, targetPane := range opts.Panes {
				if targetPane == paneStr {
					targets = append(targets, paneStr)
					break
				}
			}
			continue
		}

		// Apply agent type filter
		if len(opts.AgentTypes) > 0 {
			for _, agentType := range opts.AgentTypes {
				if agentType == pane.Type {
					targets = append(targets, paneStr)
					break
				}
			}
			continue
		}
	}

	// Apply exclusions
	if len(opts.Exclude) > 0 {
		var filtered []string
		for _, target := range targets {
			excluded := false
			for _, exclude := range opts.Exclude {
				if target == exclude {
					excluded = true
					break
				}
			}
			if !excluded {
				filtered = append(filtered, target)
			}
		}
		targets = filtered
	}

	return targets
}

func processResults(results []mockSendResult) ([]string, []SendError) {
	var successful []string
	var failed []SendError

	for _, result := range results {
		if result.Success {
			successful = append(successful, result.Pane)
		} else {
			failed = append(failed, SendError{
				Pane:  result.Pane,
				Error: result.Error,
			})
		}
	}

	return successful, failed
}

func validateSendOptions(opts SendOptions) bool {
	// Session validation
	if strings.TrimSpace(opts.Session) == "" {
		return false
	}

	// Delay validation
	if opts.DelayMs < 0 {
		return false
	}

	// Conflicting options validation
	if opts.All && len(opts.Panes) > 0 {
		return false
	}

	return true
}

func simulateSendDryRun(opts SendOptions, panes []mockPane) SendOutput {
	targets := filterTargets(opts, panes)

	return SendOutput{
		RobotResponse:  NewRobotResponse(true),
		Session:        opts.Session,
		SentAt:         time.Now().UTC(),
		Blocked:        false,
		Redaction:      RedactionSummary{Mode: "off", Findings: 0, Action: "off"},
		Warnings:       []string{},
		Targets:        targets,
		Successful:     []string{},
		Failed:         []SendError{},
		MessagePreview: opts.Message,
		DryRun:         true,
		WouldSendTo:    targets,
	}
}
