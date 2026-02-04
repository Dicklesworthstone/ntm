package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewNotifier(t *testing.T) {
	cfg := NotifierConfig{
		Channels:      []string{"desktop", "webhook", "mail"},
		WebhookURL:    "https://example.com/webhook",
		MailRecipient: "Human",
	}

	n := NewNotifier(cfg)

	if len(n.channels) != 3 {
		t.Errorf("expected 3 channels, got %d", len(n.channels))
	}
	if n.webhookURL != "https://example.com/webhook" {
		t.Errorf("expected webhook URL, got %q", n.webhookURL)
	}
	if n.mailRecipient != "Human" {
		t.Errorf("expected mail recipient 'Human', got %q", n.mailRecipient)
	}
}

func TestNewNotifierIgnoresUnknownChannels(t *testing.T) {
	cfg := NotifierConfig{
		Channels: []string{"desktop", "slack", "unknown", "webhook"},
	}

	n := NewNotifier(cfg)

	// Only desktop and webhook should be recognized
	if len(n.channels) != 2 {
		t.Errorf("expected 2 valid channels (unknown ignored), got %d", len(n.channels))
	}
}

func TestNewNotifierFromSettings(t *testing.T) {
	settings := WorkflowSettings{
		NotifyOnComplete: true,
		NotifyOnError:    true,
		NotifyChannels:   []string{"desktop", "webhook"},
		WebhookURL:       "https://example.com/hook",
		MailRecipient:    "TestAgent",
	}

	n := NewNotifierFromSettings(settings, nil, "/test/project", "Coordinator")

	if len(n.channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(n.channels))
	}
	if n.projectKey != "/test/project" {
		t.Errorf("expected projectKey, got %q", n.projectKey)
	}
	if n.agentName != "Coordinator" {
		t.Errorf("expected agentName 'Coordinator', got %q", n.agentName)
	}
}

func TestShouldNotify(t *testing.T) {
	tests := []struct {
		name     string
		settings WorkflowSettings
		event    NotificationEvent
		want     bool
	}{
		{
			name:     "complete with notify on",
			settings: WorkflowSettings{NotifyOnComplete: true},
			event:    NotifyCompleted,
			want:     true,
		},
		{
			name:     "complete with notify off",
			settings: WorkflowSettings{NotifyOnComplete: false},
			event:    NotifyCompleted,
			want:     false,
		},
		{
			name:     "failed with notify on",
			settings: WorkflowSettings{NotifyOnError: true},
			event:    NotifyFailed,
			want:     true,
		},
		{
			name:     "failed with notify off",
			settings: WorkflowSettings{NotifyOnError: false},
			event:    NotifyFailed,
			want:     false,
		},
		{
			name:     "step error with notify on",
			settings: WorkflowSettings{NotifyOnError: true},
			event:    NotifyStepError,
			want:     true,
		},
		{
			name:     "started never notifies",
			settings: WorkflowSettings{NotifyOnComplete: true, NotifyOnError: true},
			event:    NotifyStarted,
			want:     false,
		},
		{
			name:     "cancelled never notifies (no setting yet)",
			settings: WorkflowSettings{NotifyOnComplete: true, NotifyOnError: true},
			event:    NotifyCancelled,
			want:     false,
		},
		{
			name:     "unknown event returns false",
			settings: WorkflowSettings{NotifyOnComplete: true, NotifyOnError: true},
			event:    NotificationEvent("unknown"),
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldNotify(tt.settings, tt.event)
			if got != tt.want {
				t.Errorf("ShouldNotify() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNotifyWebhook(t *testing.T) {
	var received NotificationPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode body: %v", err)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := NewNotifier(NotifierConfig{
		Channels:   []string{"webhook"},
		WebhookURL: server.URL,
	})

	payload := NotificationPayload{
		Event:        NotifyCompleted,
		WorkflowName: "test-workflow",
		RunID:        "run-123",
		Status:       StatusCompleted,
		StepsTotal:   5,
		StepsDone:    5,
		Timestamp:    time.Now(),
	}

	err := n.Notify(context.Background(), payload)
	if err != nil {
		t.Errorf("Notify() error = %v", err)
	}

	if received.Event != NotifyCompleted {
		t.Errorf("expected event NotifyCompleted, got %s", received.Event)
	}
	if received.WorkflowName != "test-workflow" {
		t.Errorf("expected workflow name 'test-workflow', got %q", received.WorkflowName)
	}
}

func TestNotifyWebhookError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	n := NewNotifier(NotifierConfig{
		Channels:   []string{"webhook"},
		WebhookURL: server.URL,
	})

	payload := NotificationPayload{
		Event:        NotifyFailed,
		WorkflowName: "test-workflow",
		Timestamp:    time.Now(),
	}

	err := n.Notify(context.Background(), payload)
	if err == nil {
		t.Error("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention status 500, got: %v", err)
	}
}

func TestFormatDesktopTitle(t *testing.T) {
	tests := []struct {
		event NotificationEvent
		want  string
	}{
		{NotifyCompleted, "Pipeline 'test' completed"},
		{NotifyFailed, "Pipeline 'test' failed"},
		{NotifyCancelled, "Pipeline 'test' cancelled"},
		{NotifyStarted, "Pipeline 'test' started"},
		{NotifyStepError, "Pipeline 'test' step error"},
	}

	for _, tt := range tests {
		t.Run(string(tt.event), func(t *testing.T) {
			p := NotificationPayload{Event: tt.event, WorkflowName: "test"}
			got := formatDesktopTitle(p)
			if got != tt.want {
				t.Errorf("formatDesktopTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatDesktopBody(t *testing.T) {
	tests := []struct {
		name    string
		payload NotificationPayload
		contain string
	}{
		{
			name: "completed",
			payload: NotificationPayload{
				Event:      NotifyCompleted,
				Duration:   5 * time.Minute,
				StepsDone:  10,
				StepsTotal: 10,
			},
			contain: "Duration: 5m",
		},
		{
			name: "failed with step",
			payload: NotificationPayload{
				Event:      NotifyFailed,
				FailedStep: "build",
				Error:      "compilation error",
			},
			contain: "step 'build'",
		},
		{
			name: "cancelled",
			payload: NotificationPayload{
				Event:    NotifyCancelled,
				Duration: 2 * time.Minute,
			},
			contain: "Cancelled after",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDesktopBody(tt.payload)
			if !strings.Contains(got, tt.contain) {
				t.Errorf("formatDesktopBody() = %q, want to contain %q", got, tt.contain)
			}
		})
	}
}

func TestFormatMailSubject(t *testing.T) {
	tests := []struct {
		event NotificationEvent
		want  string
	}{
		{NotifyCompleted, "Pipeline 'test' completed successfully"},
		{NotifyFailed, "Pipeline 'test' failed"},
		{NotifyCancelled, "Pipeline 'test' was cancelled"},
	}

	for _, tt := range tests {
		t.Run(string(tt.event), func(t *testing.T) {
			p := NotificationPayload{Event: tt.event, WorkflowName: "test"}
			got := formatMailSubject(p)
			if got != tt.want {
				t.Errorf("formatMailSubject() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatMailBody(t *testing.T) {
	payload := NotificationPayload{
		Event:        NotifyCompleted,
		WorkflowName: "test-workflow",
		RunID:        "run-abc",
		Session:      "my-session",
		Status:       StatusCompleted,
		Duration:     3*time.Minute + 45*time.Second,
		StepsTotal:   5,
		StepsDone:    5,
		Timestamp:    time.Now(),
	}

	body := formatMailBody(payload)

	if !strings.Contains(body, "# Pipeline: test-workflow") {
		t.Error("expected markdown header")
	}
	if !strings.Contains(body, "run-abc") {
		t.Error("expected run ID")
	}
	if !strings.Contains(body, "my-session") {
		t.Error("expected session")
	}
	if !strings.Contains(body, "3m45s") {
		t.Error("expected duration")
	}
	if !strings.Contains(body, "5/5") {
		t.Error("expected step count")
	}
}

func TestFormatMailBodyFailed(t *testing.T) {
	payload := NotificationPayload{
		Event:        NotifyFailed,
		WorkflowName: "test-workflow",
		RunID:        "run-xyz",
		Status:       StatusFailed,
		FailedStep:   "deploy",
		Error:        "connection refused",
		Duration:     1 * time.Minute,
		StepsTotal:   5,
		StepsDone:    3,
		Timestamp:    time.Now(),
	}

	body := formatMailBody(payload)

	if !strings.Contains(body, "## Error") {
		t.Error("expected error section")
	}
	if !strings.Contains(body, "deploy") {
		t.Error("expected failed step name")
	}
	if !strings.Contains(body, "connection refused") {
		t.Error("expected error message")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m30s"},
		{3600 * time.Second, "1h0m"},
		{3661 * time.Second, "1h1m"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatDuration(tt.d)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestTruncateMessage(t *testing.T) {
	tests := []struct {
		s    string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"this is a longer message", 10, "this is..."},
		{"abc", 3, "abc"},      // fits, no truncation needed
		{"ab", 2, "ab"},        // fits, no truncation needed
		{"abcdef", 3, "..."},   // doesn't fit, show ellipsis
		{"abcdef", 5, "ab..."}, // truncate with room for some content
		{"hello", 0, ""},       // zero length
		{"hello", -1, ""},      // negative length
		{"hello", 1, "."},      // n=1 returns "."
		{"hello", 2, ".."},     // n=2 returns ".."
		{"héllo wörld", 8, "héll..."},  // UTF-8 multibyte chars (counts bytes)
		{"", 5, ""},            // empty string
	}

	for _, tt := range tests {
		name := tt.s
		if len(name) > 5 {
			name = name[:5]
		}
		if name == "" {
			name = "empty"
		}
		t.Run(fmt.Sprintf("%s_n%d", name, tt.n), func(t *testing.T) {
			got := truncateMessage(tt.s, tt.n)
			if got != tt.want {
				t.Errorf("truncateMessage(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
			}
		})
	}
}

func TestTruncateMessage_ForLoopCompletion(t *testing.T) {
	t.Parallel()

	// Test case where the for loop completes without returning early (line 371)
	// This happens when all rune boundaries fit within targetLen
	// String "abc🌍" is 7 bytes (a=1, b=1, c=1, emoji=4)
	// With n=6, targetLen=3, all rune boundaries (0,1,2,3) are <= 3
	s := "abc🌍"
	got := truncateMessage(s, 6)
	want := "abc..."
	if got != want {
		t.Errorf("truncateMessage(%q, 6) = %q, want %q", s, got, want)
	}
}

func TestBuildPayloadFromState(t *testing.T) {
	now := time.Now()
	state := &ExecutionState{
		RunID:      "run-test",
		WorkflowID: "test-workflow",
		Session:    "test-session",
		Status:     StatusCompleted,
		StartedAt:  now.Add(-5 * time.Minute),
		FinishedAt: now,
		Steps: map[string]StepResult{
			"step1": {StepID: "step1", Status: StatusCompleted},
			"step2": {StepID: "step2", Status: StatusCompleted},
			"step3": {StepID: "step3", Status: StatusSkipped},
		},
	}

	workflow := &Workflow{
		Name: "test-workflow",
		Steps: []Step{
			{ID: "step1"},
			{ID: "step2"},
			{ID: "step3"},
		},
	}

	payload := BuildPayloadFromState(state, workflow, NotifyCompleted)

	if payload.Event != NotifyCompleted {
		t.Errorf("expected event NotifyCompleted, got %s", payload.Event)
	}
	if payload.WorkflowName != "test-workflow" {
		t.Errorf("expected workflow name, got %q", payload.WorkflowName)
	}
	if payload.RunID != "run-test" {
		t.Errorf("expected run ID, got %q", payload.RunID)
	}
	if payload.StepsTotal != 3 {
		t.Errorf("expected 3 steps total, got %d", payload.StepsTotal)
	}
	if payload.StepsDone != 3 { // 2 completed + 1 skipped
		t.Errorf("expected 3 steps done, got %d", payload.StepsDone)
	}
	if payload.Duration < 4*time.Minute || payload.Duration > 6*time.Minute {
		t.Errorf("expected duration ~5m, got %v", payload.Duration)
	}
}

func TestBuildPayloadFromStateWithFailure(t *testing.T) {
	now := time.Now()
	state := &ExecutionState{
		RunID:      "run-fail",
		WorkflowID: "fail-workflow",
		Status:     StatusFailed,
		StartedAt:  now.Add(-2 * time.Minute),
		FinishedAt: now,
		Steps: map[string]StepResult{
			"step1": {StepID: "step1", Status: StatusCompleted},
			"step2": {
				StepID: "step2",
				Status: StatusFailed,
				Error: &StepError{
					Message: "build failed",
				},
			},
		},
		Errors: []ExecutionError{
			{StepID: "step2", Message: "build failed", Fatal: true},
		},
	}

	workflow := &Workflow{
		Name: "fail-workflow",
		Steps: []Step{
			{ID: "step1"},
			{ID: "step2"},
		},
	}

	payload := BuildPayloadFromState(state, workflow, NotifyFailed)

	if payload.Event != NotifyFailed {
		t.Errorf("expected event NotifyFailed, got %s", payload.Event)
	}
	if payload.StepsFailed != 1 {
		t.Errorf("expected 1 step failed, got %d", payload.StepsFailed)
	}
	if payload.FailedStep != "step2" {
		t.Errorf("expected failed step 'step2', got %q", payload.FailedStep)
	}
	if payload.Error != "build failed" {
		t.Errorf("expected error 'build failed', got %q", payload.Error)
	}
}

func TestFormatDesktopBody_AllEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload NotificationPayload
		contain string
	}{
		{
			name: "started",
			payload: NotificationPayload{
				Event:      NotifyStarted,
				StepsTotal: 5,
			},
			contain: "5 steps",
		},
		{
			name: "step error",
			payload: NotificationPayload{
				Event:      NotifyStepError,
				FailedStep: "deploy",
				Error:      "connection refused",
			},
			contain: "deploy",
		},
		{
			name: "failed without step",
			payload: NotificationPayload{
				Event: NotifyFailed,
				Error: "catastrophic failure occurred here",
			},
			contain: "catastrophic failure",
		},
		{
			name: "unknown event",
			payload: NotificationPayload{
				Event: "custom_event",
			},
			contain: "", // empty body
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatDesktopBody(tt.payload)
			if tt.contain != "" && !strings.Contains(got, tt.contain) {
				t.Errorf("formatDesktopBody() = %q, want to contain %q", got, tt.contain)
			}
		})
	}
}

func TestFormatDesktopTitle_UnknownEvent(t *testing.T) {
	t.Parallel()

	p := NotificationPayload{Event: "custom", WorkflowName: "test"}
	got := formatDesktopTitle(p)
	if got != "Pipeline 'test'" {
		t.Errorf("formatDesktopTitle() = %q, want %q", got, "Pipeline 'test'")
	}
}

func TestFormatMailSubject_AllEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		event NotificationEvent
		want  string
	}{
		{NotifyStarted, "Pipeline 'wf' started"},
		{NotifyStepError, "Pipeline 'wf' step failed: deploy"},
		{"custom", "Pipeline 'wf' notification"},
	}

	for _, tt := range tests {
		t.Run(string(tt.event), func(t *testing.T) {
			t.Parallel()
			p := NotificationPayload{Event: tt.event, WorkflowName: "wf", FailedStep: "deploy"}
			got := formatMailSubject(p)
			if got != tt.want {
				t.Errorf("formatMailSubject() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatMailBody_AllEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload NotificationPayload
		contain string
	}{
		{
			name: "cancelled",
			payload: NotificationPayload{
				Event:      NotifyCancelled,
				Duration:   2 * time.Minute,
				StepsTotal: 10,
				StepsDone:  4,
				Timestamp:  time.Now(),
			},
			contain: "## Cancellation",
		},
		{
			name: "step error",
			payload: NotificationPayload{
				Event:      NotifyStepError,
				FailedStep: "build",
				Error:      "compile error",
				Timestamp:  time.Now(),
			},
			contain: "## Step Error",
		},
		{
			name: "completed with failed steps",
			payload: NotificationPayload{
				Event:       NotifyCompleted,
				Duration:    5 * time.Minute,
				StepsDone:   8,
				StepsTotal:  10,
				StepsFailed: 2,
				Timestamp:   time.Now(),
			},
			contain: "Steps failed",
		},
		{
			name: "started",
			payload: NotificationPayload{
				Event:      NotifyStarted,
				Timestamp:  time.Now(),
				StepsTotal: 3,
			},
			contain: "NTM Pipeline", // Contains footer but no special section
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatMailBody(tt.payload)
			if !strings.Contains(got, tt.contain) {
				t.Errorf("formatMailBody() should contain %q, got:\n%s", tt.contain, got)
			}
		})
	}
}

func TestBuildPayloadFromState_NoStartedTime(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		RunID:  "run-no-start",
		Status: StatusRunning,
		Steps:  map[string]StepResult{},
	}
	workflow := &Workflow{
		Name:  "wf",
		Steps: []Step{{ID: "s1"}},
	}

	payload := BuildPayloadFromState(state, workflow, NotifyStarted)
	if payload.Duration != 0 {
		t.Errorf("Duration = %v, want 0 for no started time", payload.Duration)
	}
}

func TestBuildPayloadFromState_OngoingExecution(t *testing.T) {
	t.Parallel()

	// Test case where StartedAt is set but FinishedAt is zero (ongoing execution)
	state := &ExecutionState{
		RunID:     "run-ongoing",
		Status:    StatusRunning,
		StartedAt: time.Now().Add(-30 * time.Second),
		// FinishedAt is zero
		Steps: map[string]StepResult{},
	}
	workflow := &Workflow{
		Name:  "wf",
		Steps: []Step{{ID: "s1"}},
	}

	payload := BuildPayloadFromState(state, workflow, NotifyStarted)
	// Duration should be approximately 30 seconds (time.Since(StartedAt))
	if payload.Duration < 25*time.Second || payload.Duration > 35*time.Second {
		t.Errorf("Duration = %v, expected ~30s for ongoing execution", payload.Duration)
	}
}

func TestBuildPayloadFromState_StateErrorsOnly(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		RunID:      "run-state-errs",
		Status:     StatusFailed,
		StartedAt:  time.Now().Add(-time.Minute),
		FinishedAt: time.Now(),
		Steps: map[string]StepResult{
			"step1": {StepID: "step1", Status: StatusCompleted},
		},
		Errors: []ExecutionError{
			{Message: "non-fatal warning", Fatal: false},
			{StepID: "step2", Message: "fatal error here", Fatal: true},
		},
	}
	workflow := &Workflow{
		Name:  "wf",
		Steps: []Step{{ID: "step1"}, {ID: "step2"}},
	}

	payload := BuildPayloadFromState(state, workflow, NotifyFailed)
	if payload.Error != "fatal error here" {
		t.Errorf("Error = %q, want %q", payload.Error, "fatal error here")
	}
	if payload.FailedStep != "step2" {
		t.Errorf("FailedStep = %q, want %q", payload.FailedStep, "step2")
	}
}

func TestNotifyWebhook_EmptyURL(t *testing.T) {
	t.Parallel()

	n := NewNotifier(NotifierConfig{
		Channels:   []string{"webhook"},
		WebhookURL: "", // empty URL
	})

	payload := NotificationPayload{
		Event:        NotifyCompleted,
		WorkflowName: "test",
		Timestamp:    time.Now(),
	}

	// Webhook with empty URL should be a no-op
	err := n.Notify(context.Background(), payload)
	if err != nil {
		t.Errorf("expected no error for empty webhook URL, got %v", err)
	}
}

func TestNotifyMail_NilClient(t *testing.T) {
	t.Parallel()

	n := NewNotifier(NotifierConfig{
		Channels:      []string{"mail"},
		MailRecipient: "agent",
		// mailClient is nil
	})

	payload := NotificationPayload{
		Event:     NotifyCompleted,
		Timestamp: time.Now(),
	}

	// Should be a no-op when client is nil
	err := n.Notify(context.Background(), payload)
	if err != nil {
		t.Errorf("expected no error for nil mail client, got %v", err)
	}
}

func TestNotifyNoChannels(t *testing.T) {
	n := NewNotifier(NotifierConfig{
		Channels: []string{},
	})

	payload := NotificationPayload{
		Event:        NotifyCompleted,
		WorkflowName: "test",
		Timestamp:    time.Now(),
	}

	err := n.Notify(context.Background(), payload)
	if err != nil {
		t.Errorf("expected no error for empty channels, got %v", err)
	}
}

func TestNotifyWebhook_ServerError(t *testing.T) {
	t.Parallel()

	// Create a test server that returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	n := NewNotifier(NotifierConfig{
		Channels:   []string{"webhook"},
		WebhookURL: server.URL,
	})

	payload := NotificationPayload{
		Event:        NotifyFailed,
		WorkflowName: "test",
		Timestamp:    time.Now(),
	}

	err := n.Notify(context.Background(), payload)
	if err == nil {
		t.Error("expected error for 500 status")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention status 500, got: %v", err)
	}
}

func TestNotifyWebhook_Success(t *testing.T) {
	t.Parallel()

	var receivedPayload NotificationPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify content type
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected Content-Type: application/json")
		}

		// Decode payload
		if err := json.NewDecoder(r.Body).Decode(&receivedPayload); err != nil {
			t.Errorf("failed to decode payload: %v", err)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := NewNotifier(NotifierConfig{
		Channels:   []string{"webhook"},
		WebhookURL: server.URL,
	})

	payload := NotificationPayload{
		Event:        NotifyCompleted,
		WorkflowName: "test-workflow",
		RunID:        "run-123",
		Timestamp:    time.Now(),
	}

	err := n.Notify(context.Background(), payload)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if receivedPayload.WorkflowName != "test-workflow" {
		t.Errorf("expected workflow 'test-workflow', got %q", receivedPayload.WorkflowName)
	}
}
