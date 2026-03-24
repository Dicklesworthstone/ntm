package robot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
)

// TestMailCheckOptionsValidate tests validation of MailCheckOptions.
func TestMailCheckOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    MailCheckOptions
		wantErr bool
		errMsg  string
	}{
		{
			name:    "missing project",
			opts:    MailCheckOptions{},
			wantErr: true,
			errMsg:  "--mail-project is required",
		},
		{
			name: "valid minimal",
			opts: MailCheckOptions{
				Project: "/data/projects/test",
			},
			wantErr: false,
		},
		{
			name: "valid with all options",
			opts: MailCheckOptions{
				Project:       "/data/projects/test",
				Agent:         "cc_1",
				Thread:        "TKT-123",
				Status:        "unread",
				IncludeBodies: true,
				UrgentOnly:    true,
				Verbose:       true,
				Limit:         50,
				Offset:        10,
				Since:         "2025-01-01",
				Until:         "2025-12-31",
			},
			wantErr: false,
		},
		{
			name: "invalid status value",
			opts: MailCheckOptions{
				Project: "/data/projects/test",
				Status:  "invalid",
			},
			wantErr: true,
			errMsg:  "invalid --mail-status value",
		},
		{
			name: "valid status read",
			opts: MailCheckOptions{
				Project: "/data/projects/test",
				Status:  "read",
			},
			wantErr: false,
		},
		{
			name: "valid status unread",
			opts: MailCheckOptions{
				Project: "/data/projects/test",
				Status:  "unread",
			},
			wantErr: false,
		},
		{
			name: "valid status all",
			opts: MailCheckOptions{
				Project: "/data/projects/test",
				Status:  "all",
			},
			wantErr: false,
		},
		{
			name: "invalid since date format",
			opts: MailCheckOptions{
				Project: "/data/projects/test",
				Since:   "invalid-date",
				Until:   "2025-12-31",
			},
			wantErr: true,
			errMsg:  "invalid --cass-since date format",
		},
		{
			name: "invalid until date format",
			opts: MailCheckOptions{
				Project: "/data/projects/test",
				Since:   "2025-01-01",
				Until:   "invalid-date",
			},
			wantErr: true,
			errMsg:  "invalid --mail-until date format",
		},
		{
			name: "until before since",
			opts: MailCheckOptions{
				Project: "/data/projects/test",
				Since:   "2025-12-31",
				Until:   "2025-01-01",
			},
			wantErr: true,
			errMsg:  "--mail-until date cannot be before --cass-since date",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errMsg)
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestMailCheckOutputJSONSerialization tests that MailCheckOutput serializes correctly.
func TestMailCheckOutputJSONSerialization(t *testing.T) {
	// Test that all fields serialize correctly
	thread := "TKT-123"
	body := "Test body content"
	nextOffset := 20
	pagesRemaining := 2
	oldestUnread := "2025-01-15T10:00:00Z"

	output := MailCheckOutput{
		RobotResponse: NewRobotResponse(true),
		Project:       "/data/projects/test",
		Agent:         "cc_1",
		Filters: MailCheckFilters{
			Status:     "unread",
			UrgentOnly: true,
			Thread:     &thread,
		},
		Unread:        5,
		Urgent:        2,
		TotalMessages: 25,
		Offset:        0,
		Count:         10,
		Messages: []MailCheckMessage{
			{
				ID:                1,
				From:              "BlueLake",
				To:                "cc_1",
				Subject:           "Test message",
				SubjectDisclosure: &adapters.DisclosureMetadata{DisclosureState: "visible", Preview: "Test message", RedactionMode: "redact"},
				Preview:           "This is a preview...",
				PreviewDisclosure: &adapters.DisclosureMetadata{DisclosureState: "preview_only", Preview: "This is a preview...", RedactionMode: "redact"},
				Body:              &body,
				BodyDisclosure:    &adapters.DisclosureMetadata{DisclosureState: "visible", Preview: "Test body content", RedactionMode: "redact"},
				ThreadID:          &thread,
				Importance:        "high",
				AckRequired:       true,
				Read:              false,
				Timestamp:         "2025-01-20T14:30:00Z",
			},
		},
		HasMore: true,
		AgentHints: &MailCheckAgentHints{
			SuggestedAction: "Reply to BlueLake about: Test message",
			UnreadSummary:   "5 unread messages, 2 urgent",
			NextOffset:      &nextOffset,
			PagesRemaining:  &pagesRemaining,
			OldestUnread:    &oldestUnread,
		},
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal output: %v", err)
	}

	// Verify we can unmarshal back
	var decoded MailCheckOutput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	// Verify key fields
	if decoded.Project != output.Project {
		t.Errorf("project mismatch: got %q, want %q", decoded.Project, output.Project)
	}
	if decoded.Unread != output.Unread {
		t.Errorf("unread mismatch: got %d, want %d", decoded.Unread, output.Unread)
	}
	if decoded.Urgent != output.Urgent {
		t.Errorf("urgent mismatch: got %d, want %d", decoded.Urgent, output.Urgent)
	}
	if len(decoded.Messages) != len(output.Messages) {
		t.Errorf("messages count mismatch: got %d, want %d", len(decoded.Messages), len(output.Messages))
	}
	if len(decoded.Messages) > 0 && decoded.Messages[0].AckRequired != output.Messages[0].AckRequired {
		t.Errorf("ack_required mismatch: got %v, want %v", decoded.Messages[0].AckRequired, output.Messages[0].AckRequired)
	}
	if decoded.HasMore != output.HasMore {
		t.Errorf("has_more mismatch: got %v, want %v", decoded.HasMore, output.HasMore)
	}
	if decoded.AgentHints == nil {
		t.Error("agent hints should not be nil")
	} else {
		if decoded.AgentHints.SuggestedAction != output.AgentHints.SuggestedAction {
			t.Errorf("suggested_action mismatch: got %q, want %q",
				decoded.AgentHints.SuggestedAction, output.AgentHints.SuggestedAction)
		}
	}
}

func TestMailCheckMessageFromInboxAppliesDisclosureControl(t *testing.T) {
	thread := "bd-j9jo3.3.5"
	secret := strings.Repeat("s", 20)
	msg := agentmail.InboxMessage{
		ID:          7,
		From:        "BlueLake",
		Subject:     "Rotate credential",
		BodyMD:      "Need token=" + secret + " and " + strings.Repeat("harmless coordination detail ", 8),
		ThreadID:    &thread,
		Importance:  "urgent",
		AckRequired: true,
		CreatedTS:   agentmail.FlexTime{},
	}

	safe := mailCheckMessageFromInbox(msg, "GreenStone", true)

	if safe.Subject != "Rotate credential" {
		t.Fatalf("subject = %q, want %q", safe.Subject, "Rotate credential")
	}
	if safe.SubjectDisclosure == nil || safe.SubjectDisclosure.DisclosureState != "visible" {
		t.Fatalf("expected visible subject disclosure, got %+v", safe.SubjectDisclosure)
	}
	if !strings.Contains(safe.Preview, "[REDACTED:GENERIC_SECRET:") {
		t.Fatalf("expected redacted preview, got %q", safe.Preview)
	}
	if safe.PreviewDisclosure == nil || safe.PreviewDisclosure.DisclosureState != "redacted" {
		t.Fatalf("expected redacted preview disclosure, got %+v", safe.PreviewDisclosure)
	}
	if safe.Body == nil || !strings.Contains(*safe.Body, "[REDACTED:GENERIC_SECRET:") {
		t.Fatalf("expected redacted body, got %+v", safe.Body)
	}
	if safe.BodyDisclosure == nil || safe.BodyDisclosure.DisclosureState != "redacted" {
		t.Fatalf("expected redacted body disclosure, got %+v", safe.BodyDisclosure)
	}
	if !safe.AckRequired {
		t.Fatal("AckRequired = false, want true from inbox message")
	}
}

func TestMailCheckMessageFromInboxPreviewsLongSafeBody(t *testing.T) {
	msg := agentmail.InboxMessage{
		ID:        8,
		From:      "BlueLake",
		Subject:   "Coordination",
		BodyMD:    strings.Repeat("harmless coordination detail ", 8),
		CreatedTS: agentmail.FlexTime{},
	}

	safe := mailCheckMessageFromInbox(msg, "GreenStone", true)

	if safe.Body == nil {
		t.Fatal("expected body output")
	}
	if safe.BodyDisclosure == nil || safe.BodyDisclosure.DisclosureState != "preview_only" {
		t.Fatalf("expected preview_only body disclosure, got %+v", safe.BodyDisclosure)
	}
	if *safe.Body != safe.BodyDisclosure.Preview {
		t.Fatalf("expected preview-only body to match preview, got body=%q preview=%q", *safe.Body, safe.BodyDisclosure.Preview)
	}
	if !strings.HasSuffix(*safe.Body, "...") {
		t.Fatalf("expected preview-only body to truncate long content, got %q", *safe.Body)
	}
}

// TestMailCheckOutputValidationError tests error response handling.
func TestMailCheckOutputValidationError(t *testing.T) {
	// Test that validation errors return proper error response
	opts := MailCheckOptions{
		Project: "", // Missing required project
	}

	output, err := GetMailCheck(opts)
	if err != nil {
		t.Fatalf("GetMailCheck should not return Go error, got: %v", err)
	}

	if output.Success {
		t.Error("expected Success=false for validation error")
	}
	if output.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("expected error_code %s, got %s", ErrCodeInvalidFlag, output.ErrorCode)
	}
	if output.Error == "" {
		t.Error("expected non-empty error message")
	}
}

// TestTruncateStringMail tests the string truncation helper.
func TestTruncateStringMail(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short string unchanged",
			input:    "Hello world",
			maxLen:   20,
			expected: "Hello world",
		},
		{
			name:     "exact length",
			input:    "Hello world",
			maxLen:   11,
			expected: "Hello world",
		},
		{
			name:     "truncate at word boundary",
			input:    "Hello wonderful world",
			maxLen:   15,
			expected: "Hello wonderful...", // truncates at maxLen, then finds last space if past midpoint
		},
		{
			name:     "truncate long string",
			input:    "This is a very long message that needs to be truncated for preview purposes",
			maxLen:   30,
			expected: "This is a very long message...",
		},
		{
			name:     "trims whitespace",
			input:    "  Hello world  ",
			maxLen:   50,
			expected: "Hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateStringMail(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestMailCheckFiltersJSON tests filters serialization.
func TestMailCheckFiltersJSON(t *testing.T) {
	thread := "TKT-123"
	since := "2025-01-01"
	until := "2025-12-31"

	filters := MailCheckFilters{
		Status:     "unread",
		UrgentOnly: true,
		Thread:     &thread,
		Since:      &since,
		Until:      &until,
	}

	data, err := json.Marshal(filters)
	if err != nil {
		t.Fatalf("failed to marshal filters: %v", err)
	}

	// Verify all fields are present
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal filters: %v", err)
	}

	if decoded["status"] != "unread" {
		t.Errorf("status mismatch: got %v", decoded["status"])
	}
	if decoded["urgent_only"] != true {
		t.Errorf("urgent_only mismatch: got %v", decoded["urgent_only"])
	}
	if decoded["thread"] != "TKT-123" {
		t.Errorf("thread mismatch: got %v", decoded["thread"])
	}
}

// TestMailCheckAgentHintsOmitEmpty tests that empty hints are omitted.
func TestMailCheckAgentHintsOmitEmpty(t *testing.T) {
	output := MailCheckOutput{
		RobotResponse: NewRobotResponse(true),
		Project:       "/data/projects/test",
		Messages:      []MailCheckMessage{},
		AgentHints:    nil, // No hints
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal output: %v", err)
	}

	// _agent_hints should be omitted when nil
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if _, exists := decoded["_agent_hints"]; exists {
		t.Error("_agent_hints should be omitted when nil")
	}
}

// Note: contains() and containsHelper() are defined in diagnose_test.go
// and are reused here since we're in the same package
