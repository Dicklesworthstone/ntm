package robot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			},
			wantErr: true,
			errMsg:  "invalid --since date format",
		},
		{
			name: "invalid until date format",
			opts: MailCheckOptions{
				Project: "/data/projects/test",
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
			errMsg:  "--mail-until date cannot be before --since date",
		},
		{
			name: "negative limit rejected",
			opts: MailCheckOptions{
				Project: "/data/projects/test",
				Limit:   -1,
			},
			wantErr: true,
			errMsg:  "--limit cannot be negative",
		},
		{
			name: "negative offset rejected",
			opts: MailCheckOptions{
				Project: "/data/projects/test",
				Offset:  -1,
			},
			wantErr: true,
			errMsg:  "--mail-offset cannot be negative",
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

func TestMailCheckValidationErrorRetainsCanonicalFilters(t *testing.T) {
	thread := "TKT-123"
	output, err := GetMailCheck(MailCheckOptions{
		Project: "/data/projects/test",
		Thread:  thread,
		Since:   "invalid-date",
	})
	if err != nil {
		t.Fatalf("GetMailCheck should not return Go error, got: %v", err)
	}

	if output.Success {
		t.Fatal("expected Success=false for validation error")
	}
	if output.Filters.Status != "all" {
		t.Fatalf("filters.status = %q, want %q", output.Filters.Status, "all")
	}
	if output.Filters.Thread == nil || *output.Filters.Thread != thread {
		t.Fatalf("filters.thread = %+v, want %q", output.Filters.Thread, thread)
	}
	if output.Filters.Since == nil || *output.Filters.Since != "invalid-date" {
		t.Fatalf("filters.since = %+v, want invalid-date", output.Filters.Since)
	}
	if !strings.Contains(output.Error, "--since") {
		t.Fatalf("error = %q, want canonical --since wording", output.Error)
	}
	if !strings.Contains(output.Hint, "--since") {
		t.Fatalf("hint = %q, want canonical --since guidance", output.Hint)
	}
}

func TestGetMailCheck_DefaultsZeroLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		switch req.Method {
		case "tools/call":
			params, ok := req.Params.(map[string]interface{})
			if !ok {
				t.Fatalf("expected params map, got %T", req.Params)
			}
			switch params["name"] {
			case "health_check":
				resp := agentmail.JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  json.RawMessage(`{"status":"ok"}`),
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			case "fetch_inbox":
				args, _ := params["arguments"].(map[string]interface{})
				if got := int(args["limit"].(float64)); got != 21 {
					t.Fatalf("fetch_inbox limit = %d, want 21 default (20 + 1 overfetch)", got)
				}
				resp := agentmail.JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result: json.RawMessage(`{"result":[
						{"id":1,"subject":"One","from":"BlueLake","created_ts":"2026-01-01T00:00:00Z","importance":"normal","ack_required":false,"kind":"to","body_md":"body one"},
						{"id":2,"subject":"Two","from":"GreenStone","created_ts":"2026-01-02T00:00:00Z","importance":"urgent","ack_required":true,"kind":"to","body_md":"body two"}
					]}`),
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			default:
				t.Fatalf("unexpected tool name: %v", params["name"])
			}
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
	}))
	defer server.Close()

	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	output, err := GetMailCheck(MailCheckOptions{
		Project: "/data/projects/test",
		Agent:   "BlueLake",
		Limit:   0,
	})
	if err != nil {
		t.Fatalf("GetMailCheck returned error: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetMailCheck success = false, error=%q", output.Error)
	}
	if output.Count != 2 {
		t.Fatalf("Count = %d, want 2", output.Count)
	}
	if len(output.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(output.Messages))
	}
	if output.HasMore {
		t.Fatal("HasMore = true, want false for two-message result under default limit")
	}
}

func TestGetMailCheck_HasMoreUsesOverfetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		switch req.Method {
		case "tools/call":
			params, ok := req.Params.(map[string]interface{})
			if !ok {
				t.Fatalf("expected params map, got %T", req.Params)
			}
			switch params["name"] {
			case "health_check":
				resp := agentmail.JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  json.RawMessage(`{"status":"ok"}`),
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			case "fetch_inbox":
				args, _ := params["arguments"].(map[string]interface{})
				if got := int(args["limit"].(float64)); got != 2 {
					t.Fatalf("fetch_inbox limit = %d, want 2 (1 + 1 overfetch)", got)
				}
				resp := agentmail.JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result: json.RawMessage(`{"result":[
						{"id":1,"subject":"One","from":"BlueLake","created_ts":"2026-01-02T00:00:00Z","importance":"normal","ack_required":false,"kind":"to","body_md":"body one"},
						{"id":2,"subject":"Two","from":"GreenStone","created_ts":"2026-01-01T00:00:00Z","importance":"normal","ack_required":false,"kind":"to","body_md":"body two"}
					]}`),
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			default:
				t.Fatalf("unexpected tool name: %v", params["name"])
			}
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
	}))
	defer server.Close()

	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	output, err := GetMailCheck(MailCheckOptions{
		Project: "/data/projects/test",
		Agent:   "BlueLake",
		Limit:   1,
	})
	if err != nil {
		t.Fatalf("GetMailCheck returned error: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetMailCheck success = false, error=%q", output.Error)
	}
	if output.Count != 1 {
		t.Fatalf("Count = %d, want 1", output.Count)
	}
	if !output.HasMore {
		t.Fatal("HasMore = false, want true when overfetch found another message")
	}
	if output.AgentHints == nil || output.AgentHints.NextOffset == nil || *output.AgentHints.NextOffset != 1 {
		t.Fatalf("NextOffset = %+v, want 1", output.AgentHints)
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

func TestMailCheckFiltersOmitUnsetOptionalFields(t *testing.T) {
	filters := MailCheckFilters{
		Status:     "all",
		UrgentOnly: false,
	}

	data, err := json.Marshal(filters)
	if err != nil {
		t.Fatalf("failed to marshal filters: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal filters: %v", err)
	}

	for _, field := range []string{"thread", "since", "until"} {
		if _, exists := decoded[field]; exists {
			t.Fatalf("%s should be omitted when unset: %s", field, string(data))
		}
	}
}

func TestMailCheckMessageBodyOmittedWhenBodiesDisabled(t *testing.T) {
	msg := agentmail.InboxMessage{
		ID:        9,
		From:      "BlueLake",
		Subject:   "Coordination",
		BodyMD:    "some detailed body",
		CreatedTS: agentmail.FlexTime{},
	}

	output := mailCheckMessageFromInbox(msg, "GreenStone", false)
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal message: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}
	if _, exists := decoded["body"]; exists {
		t.Fatalf("body should be omitted when includeBodies=false: %s", string(data))
	}
}

func TestGetMailCheck_AggregatesProjectAgentsWhenAgentOmitted(t *testing.T) {
	const wantSince = "2026-01-01T00:00:00Z"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		switch req.Method {
		case "tools/call":
			params, ok := req.Params.(map[string]interface{})
			if !ok {
				t.Fatalf("expected params map, got %T", req.Params)
			}
			switch params["name"] {
			case "health_check":
				resp := agentmail.JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  json.RawMessage(`{"status":"ok"}`),
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			case "fetch_inbox":
				args, _ := params["arguments"].(map[string]interface{})
				agentName, _ := args["agent_name"].(string)
				if got, _ := args["since_ts"].(string); got != wantSince {
					t.Fatalf("fetch_inbox since_ts = %q, want %q", got, wantSince)
				}
				var result json.RawMessage
				switch agentName {
				case "BlueLake":
					result = json.RawMessage(`{"result":[
						{"id":1,"subject":"Shared","from":"Alice","created_ts":"2026-01-02T00:00:00Z","importance":"normal","ack_required":false,"kind":"to","body_md":"shared body"},
						{"id":2,"subject":"Blue only","from":"Bob","created_ts":"2026-01-03T00:00:00Z","importance":"urgent","ack_required":true,"kind":"to","body_md":"blue body","read_at":"2026-01-03T01:00:00Z"}
					]}`)
				case "GreenStone":
					result = json.RawMessage(`{"result":[
						{"id":1,"subject":"Shared","from":"Alice","created_ts":"2026-01-02T00:00:00Z","importance":"normal","ack_required":false,"kind":"to","body_md":"shared body","read_at":"2026-01-02T01:00:00Z"},
						{"id":3,"subject":"Green only","from":"Carol","created_ts":"2026-01-01T00:00:00Z","importance":"normal","ack_required":false,"kind":"to","body_md":"green body"}
					]}`)
				default:
					t.Fatalf("unexpected fetch_inbox agent_name: %q", agentName)
				}
				resp := agentmail.JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  result,
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			default:
				t.Fatalf("unexpected tool name: %v", params["name"])
			}
		case "resources/read":
			resp := agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"contents":[{"text":"[{\"id\":1,\"name\":\"BlueLake\",\"program\":\"codex-cli\",\"model\":\"gpt-5\",\"task_description\":\"blue\",\"inception_ts\":\"2026-01-01T00:00:00Z\",\"last_active_ts\":\"2026-01-01T00:00:00Z\",\"project_id\":1},{\"id\":2,\"name\":\"GreenStone\",\"program\":\"claude-code\",\"model\":\"opus\",\"task_description\":\"green\",\"inception_ts\":\"2026-01-01T00:00:00Z\",\"last_active_ts\":\"2026-01-01T00:00:00Z\",\"project_id\":1},{\"id\":3,\"name\":\"HumanOverseer\",\"program\":\"human\",\"model\":\"human\",\"task_description\":\"human\",\"inception_ts\":\"2026-01-01T00:00:00Z\",\"last_active_ts\":\"2026-01-01T00:00:00Z\",\"project_id\":1}]"}]
				}`),
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
	}))
	defer server.Close()

	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	output, err := GetMailCheck(MailCheckOptions{
		Project: "/data/projects/test",
		Since:   "2026-01-01",
	})
	if err != nil {
		t.Fatalf("GetMailCheck returned error: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetMailCheck success = false, error=%q", output.Error)
	}
	if output.Agent != "" {
		t.Fatalf("Agent = %q, want project-wide aggregate with empty agent", output.Agent)
	}
	if output.TotalMessages != 3 || output.Count != 3 {
		t.Fatalf("total/count = %d/%d, want 3/3", output.TotalMessages, output.Count)
	}
	if output.Unread != 2 {
		t.Fatalf("Unread = %d, want 2", output.Unread)
	}
	if output.Urgent != 1 {
		t.Fatalf("Urgent = %d, want 1", output.Urgent)
	}
	if len(output.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(output.Messages))
	}
	if output.Messages[0].ID != 2 || output.Messages[1].ID != 1 || output.Messages[2].ID != 3 {
		t.Fatalf("unexpected message order: %+v", output.Messages)
	}
	if output.Messages[1].To != "BlueLake, GreenStone" {
		t.Fatalf("shared message recipients = %q, want %q", output.Messages[1].To, "BlueLake, GreenStone")
	}
	if output.Messages[1].Read {
		t.Fatal("shared aggregate message should remain unread when any recipient is unread")
	}
}

func TestGetMailCheck_BackfillsWhenThreadFilterNeedsOlderMatches(t *testing.T) {
	fetchLimits := []int{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		switch req.Method {
		case "tools/call":
			params, ok := req.Params.(map[string]interface{})
			if !ok {
				t.Fatalf("expected params map, got %T", req.Params)
			}
			switch params["name"] {
			case "health_check":
				resp := agentmail.JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  json.RawMessage(`{"status":"ok"}`),
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			case "fetch_inbox":
				args, _ := params["arguments"].(map[string]interface{})
				limit := int(args["limit"].(float64))
				fetchLimits = append(fetchLimits, limit)
				var result json.RawMessage
				if limit <= 1 {
					result = json.RawMessage(`{"result":[
						{"id":11,"subject":"Newest other thread","from":"BlueLake","created_ts":"2026-01-03T00:00:00Z","thread_id":"OTHER","importance":"normal","ack_required":false,"kind":"to","body_md":"other"}
					]}`)
				} else {
					result = json.RawMessage(`{"result":[
						{"id":11,"subject":"Newest other thread","from":"BlueLake","created_ts":"2026-01-03T00:00:00Z","thread_id":"OTHER","importance":"normal","ack_required":false,"kind":"to","body_md":"other"},
						{"id":10,"subject":"Target thread","from":"GreenStone","created_ts":"2026-01-02T00:00:00Z","thread_id":"TKT-123","importance":"urgent","ack_required":true,"kind":"to","body_md":"target"}
					]}`)
				}
				resp := agentmail.JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  result,
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			default:
				t.Fatalf("unexpected tool name: %v", params["name"])
			}
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
	}))
	defer server.Close()

	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	output, err := GetMailCheck(MailCheckOptions{
		Project: "/data/projects/test",
		Agent:   "BlueLake",
		Thread:  "TKT-123",
		Limit:   1,
	})
	if err != nil {
		t.Fatalf("GetMailCheck returned error: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetMailCheck success = false, error=%q", output.Error)
	}
	if len(fetchLimits) < 2 {
		t.Fatalf("expected backfill fetches, got limits %v", fetchLimits)
	}
	if fetchLimits[0] != 1 {
		t.Fatalf("first fetch limit = %d, want 1", fetchLimits[0])
	}
	if fetchLimits[1] <= fetchLimits[0] {
		t.Fatalf("backfill should increase fetch limit, got %v", fetchLimits)
	}
	if output.Count != 1 || len(output.Messages) != 1 {
		t.Fatalf("count/messages = %d/%d, want 1/1", output.Count, len(output.Messages))
	}
	if output.Messages[0].ID != 10 {
		t.Fatalf("message ID = %d, want 10 after thread-filter backfill", output.Messages[0].ID)
	}
	if output.TotalMessages != 1 {
		t.Fatalf("total_messages = %d, want 1", output.TotalMessages)
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
