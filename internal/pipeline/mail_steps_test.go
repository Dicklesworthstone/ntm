package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMailSteps_ParseYAMLAndValidate(t *testing.T) {
	content := `
schema_version: "2.0"
name: mail-steps
steps:
  - id: notify
    mail_send:
      project_key: /data/projects/ntm
      agent_name: TealCrane
      to: [SageFern, OrangeFalcon]
      subject: "[bd-b5l8d] status"
      body: "Done"
      thread_id: bd-b5l8d
      ack_required: true
  - id: reserve
    file_reservation_paths:
      project_key: /data/projects/ntm
      agent_name: TealCrane
      paths: [internal/pipeline/schema.go, internal/pipeline/mail_steps.go]
      ttl_seconds: 3600
      exclusive: true
      reason: bd-b5l8d
  - id: inbox
    mail_inbox_check:
      project_key: /data/projects/ntm
      agent_name: TealCrane
      until_ack_count: 2
  - id: release
    file_reservation_release:
      project_key: /data/projects/ntm
      agent_name: TealCrane
      paths: internal/pipeline/schema.go
`

	workflow, err := ParseString(content, "yaml")
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}
	if result := Validate(workflow); !result.Valid {
		t.Fatalf("Validate() failed: %+v", result.Errors)
	}

	send := workflow.Steps[0].MailSend
	if send == nil {
		t.Fatal("MailSend = nil")
	}
	if !reflect.DeepEqual(send.To, StringOrList{"SageFern", "OrangeFalcon"}) {
		t.Fatalf("MailSend.To = %#v, want two recipients", send.To)
	}
	if !send.AckRequired || send.ThreadID != "bd-b5l8d" {
		t.Fatalf("MailSend metadata = %#v", send)
	}

	reserve := workflow.Steps[1].FileReservationPaths
	if reserve == nil {
		t.Fatal("FileReservationPaths = nil")
	}
	if reserve.TTLSeconds != 3600 || !reserve.Exclusive || reserve.Reason != "bd-b5l8d" {
		t.Fatalf("FileReservationPaths = %#v", reserve)
	}

	inbox := workflow.Steps[2].MailInboxCheck
	if inbox == nil || inbox.UntilAckCount != 2 {
		t.Fatalf("MailInboxCheck = %#v, want until_ack_count=2", inbox)
	}

	release := workflow.Steps[3].FileReservationRelease
	if release == nil {
		t.Fatal("FileReservationRelease = nil")
	}
	if !reflect.DeepEqual(release.Paths, StringOrList{"internal/pipeline/schema.go"}) {
		t.Fatalf("FileReservationRelease.Paths = %#v, want scalar path as one-item list", release.Paths)
	}
}

func TestMailSteps_ParseTOMLKnownFields(t *testing.T) {
	content := `
schema_version = "2.0"
name = "mail-steps-toml"

[[steps]]
id = "notify"

[steps.mail_send]
project_key = "/data/projects/ntm"
agent_name = "TealCrane"
to = ["SageFern"]
subject = "[bd-b5l8d] status"
body = "Done"
thread_id = "bd-b5l8d"
ack_required = true
`

	workflow, err := ParseString(content, "toml")
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}
	if result := Validate(workflow); !result.Valid {
		t.Fatalf("Validate() failed: %+v", result.Errors)
	}
	if got := workflow.Steps[0].MailSend.To; !reflect.DeepEqual(got, StringOrList{"SageFern"}) {
		t.Fatalf("MailSend.To = %#v, want SageFern", got)
	}
}

func TestMailSteps_JSONRoundTrip(t *testing.T) {
	step := Step{
		ID: "reserve",
		FileReservationPaths: &FileReservationPathsStep{
			ProjectKey: "/data/projects/ntm",
			AgentName:  "TealCrane",
			Paths:      StringOrList{"internal/pipeline/schema.go"},
			TTLSeconds: 3600,
			Exclusive:  true,
			Reason:     "bd-b5l8d",
		},
	}

	data, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got Step
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v\nJSON:\n%s", err, data)
	}
	if !reflect.DeepEqual(got, step) {
		t.Fatalf("JSON round trip mismatch\nwant: %#v\n got: %#v\nJSON:\n%s", step, got, data)
	}
}

func TestMailSteps_ValidationConflicts(t *testing.T) {
	tests := []struct {
		name    string
		step    Step
		wantErr string
	}{
		{
			name: "mail send with command",
			step: Step{
				ID:       "bad",
				Command:  "echo should-not-run",
				MailSend: validMailSendStep(),
			},
			wantErr: "cannot combine Agent Mail step kind",
		},
		{
			name: "two mail kinds",
			step: Step{
				ID:             "bad",
				MailSend:       validMailSendStep(),
				MailInboxCheck: &MailInboxCheckStep{ProjectKey: "/data/projects/ntm", AgentName: "TealCrane"},
			},
			wantErr: "can only use one Agent Mail step kind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Validate(&Workflow{
				SchemaVersion: SchemaVersion,
				Name:          "mail-step-conflict",
				Steps:         []Step{tt.step},
			})
			if result.Valid {
				t.Fatal("Validate() succeeded, want conflict")
			}
			for _, err := range result.Errors {
				if strings.Contains(err.Message, tt.wantErr) {
					return
				}
			}
			t.Fatalf("Validate() errors = %+v, want message containing %q", result.Errors, tt.wantErr)
		})
	}
}

func validMailSendStep() *MailSendStep {
	return &MailSendStep{
		ProjectKey: "/data/projects/ntm",
		AgentName:  "TealCrane",
		To:         StringOrList{"SageFern"},
		Subject:    "[bd-b5l8d] status",
		Body:       "Done",
		ThreadID:   "bd-b5l8d",
	}
}

func TestMailSteps_ValidationRequiredFields(t *testing.T) {
	// bd-vv7ij: each Agent Mail step kind must surface required-field
	// errors at parse time. Before this fix, mail_send: {} validated
	// successfully, file_reservation_paths with no paths validated, and
	// mail_inbox_check / file_reservation_release with no project_key /
	// agent_name validated.
	tests := []struct {
		name    string
		step    Step
		wantErr string
	}{
		{
			name:    "mail_send empty",
			step:    Step{ID: "send", MailSend: &MailSendStep{}},
			wantErr: "mail_send requires project_key",
		},
		{
			name:    "mail_send missing recipients",
			step:    Step{ID: "send", MailSend: &MailSendStep{ProjectKey: "/p", AgentName: "A", Subject: "s", Body: "b"}},
			wantErr: "mail_send requires at least one recipient in to",
		},
		{
			name:    "mail_send missing subject and body",
			step:    Step{ID: "send", MailSend: &MailSendStep{ProjectKey: "/p", AgentName: "A", To: StringOrList{"B"}}},
			wantErr: "mail_send requires subject or body",
		},
		{
			name:    "file_reservation_paths missing paths",
			step:    Step{ID: "lock", FileReservationPaths: &FileReservationPathsStep{ProjectKey: "/p", AgentName: "A"}},
			wantErr: "file_reservation_paths requires at least one path",
		},
		{
			name:    "file_reservation_paths negative ttl",
			step:    Step{ID: "lock", FileReservationPaths: &FileReservationPathsStep{ProjectKey: "/p", AgentName: "A", Paths: StringOrList{"a.go"}, TTLSeconds: -1}},
			wantErr: "ttl_seconds must be non-negative",
		},
		{
			name:    "mail_inbox_check empty",
			step:    Step{ID: "inbox", MailInboxCheck: &MailInboxCheckStep{}},
			wantErr: "mail_inbox_check requires project_key",
		},
		{
			name:    "mail_inbox_check missing agent",
			step:    Step{ID: "inbox", MailInboxCheck: &MailInboxCheckStep{ProjectKey: "/p"}},
			wantErr: "mail_inbox_check requires agent_name",
		},
		{
			name:    "file_reservation_release missing paths",
			step:    Step{ID: "release", FileReservationRelease: &FileReservationReleaseStep{ProjectKey: "/p", AgentName: "A"}},
			wantErr: "file_reservation_release requires at least one path",
		},
		{
			name:    "file_reservation_release blank path",
			step:    Step{ID: "release", FileReservationRelease: &FileReservationReleaseStep{ProjectKey: "/p", AgentName: "A", Paths: StringOrList{"   "}}},
			wantErr: "file_reservation_release path cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Validate(&Workflow{
				SchemaVersion: SchemaVersion,
				Name:          "mail-step-required",
				Steps:         []Step{tt.step},
			})
			if result.Valid {
				t.Fatal("Validate() succeeded, want required-field error")
			}
			for _, err := range result.Errors {
				if strings.Contains(err.Message, tt.wantErr) {
					return
				}
			}
			t.Fatalf("Validate() errors = %+v, want message containing %q", result.Errors, tt.wantErr)
		})
	}
}

func TestMailSteps_ValidationAcceptsValid(t *testing.T) {
	// bd-vv7ij: a fully populated step of each Agent Mail kind must remain
	// valid after the new required-field checks land.
	steps := []Step{
		{ID: "send", MailSend: validMailSendStep()},
		{ID: "lock", FileReservationPaths: &FileReservationPathsStep{ProjectKey: "/p", AgentName: "A", Paths: StringOrList{"a.go", "b.go"}, TTLSeconds: 60}},
		{ID: "inbox", MailInboxCheck: &MailInboxCheckStep{ProjectKey: "/p", AgentName: "A", UntilAckCount: 1}},
		{ID: "release", FileReservationRelease: &FileReservationReleaseStep{ProjectKey: "/p", AgentName: "A", Paths: StringOrList{"a.go"}}},
	}
	result := Validate(&Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "mail-step-valid",
		Steps:         steps,
	})
	if !result.Valid {
		t.Fatalf("Validate() errors = %+v, want all four mail steps to validate", result.Errors)
	}
}

func TestExecuteMailStep_DispatchesThroughAgentMail(t *testing.T) {
	type mailStepRequest struct {
		name string
		args map[string]interface{}
	}
	var requestsMu sync.Mutex
	requests := make([]mailStepRequest, 0, 4)
	recordRequest := func(req mailStepRequest) {
		requestsMu.Lock()
		defer requestsMu.Unlock()
		requests = append(requests, req)
	}
	requestCount := func() int {
		requestsMu.Lock()
		defer requestsMu.Unlock()
		return len(requests)
	}
	requestAt := func(idx int) mailStepRequest {
		requestsMu.Lock()
		defer requestsMu.Unlock()
		return requests[idx]
	}
	ledger := &reservationLedger{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		var req struct {
			JSONRPC string                 `json:"jsonrpc"`
			ID      interface{}            `json:"id"`
			Method  string                 `json:"method"`
			Params  map[string]interface{} `json:"params"`
		}
		body := http.MaxBytesReader(w, r.Body, 1<<20)
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if ledger.serveReadback(t, w, req.ID, req.Method, req.Params) {
			return
		}
		if req.Method != "tools/call" {
			t.Fatalf("method = %s, want tools/call", req.Method)
		}
		toolName, _ := req.Params["name"].(string)
		args, _ := req.Params["arguments"].(map[string]interface{})
		recordRequest(mailStepRequest{name: toolName, args: args})

		var result interface{}
		switch toolName {
		case "send_message":
			result = map[string]interface{}{
				"count": 1,
				"deliveries": []map[string]interface{}{
					{
						"project": "/data/projects/ntm",
						"payload": map[string]interface{}{
							"id":         77,
							"subject":    args["subject"],
							"body_md":    args["body_md"],
							"from":       args["sender_name"],
							"to":         args["to"],
							"created_ts": "2026-05-08T00:00:00Z",
						},
					},
				},
			}
		case "file_reservation_paths":
			grant := ledger.grant(88, "internal/pipeline/mail_steps.go", args)
			grant["agent_name"], grant["project_id"], grant["created_ts"] = args["agent_name"], 1, reservationLedgerCreated
			result = map[string]interface{}{
				"granted":   []map[string]interface{}{grant},
				"conflicts": []interface{}{},
			}
		case "fetch_inbox":
			result = []map[string]interface{}{
				{
					"id":           99,
					"subject":      "please ack",
					"from":         "SageFern",
					"created_ts":   "2026-05-08T00:00:00Z",
					"importance":   "normal",
					"ack_required": true,
					"kind":         "message",
				},
			}
		case "release_file_reservations":
			result = map[string]interface{}{
				"released":    ledger.release(args),
				"released_at": "2026-05-08T00:00:00Z",
			}
		default:
			t.Fatalf("unexpected tool %q", toolName)
		}

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	executor := newMailStepTestExecutor(t)
	cases := []struct {
		name   string
		step   *Step
		expect string
	}{
		{
			name:   "mail_send",
			step:   &Step{ID: "notify", MailSend: &MailSendStep{ProjectKey: "${vars.project}", AgentName: "${vars.agent}", To: StringOrList{"${vars.recipient}"}, Subject: "[${vars.thread}] status", Body: "${vars.body}", ThreadID: "${vars.thread}", AckRequired: true}, OutputParse: OutputParse{Type: "json"}},
			expect: "send_message",
		},
		{
			name:   "file_reservation_paths",
			step:   &Step{ID: "lock", FileReservationPaths: &FileReservationPathsStep{ProjectKey: "${vars.project}", AgentName: "${vars.agent}", Paths: StringOrList{"internal/pipeline/mail_steps.go"}, TTLSeconds: 60, Exclusive: true, Reason: "${vars.thread}"}},
			expect: "file_reservation_paths",
		},
		{
			name:   "mail_inbox_check",
			step:   &Step{ID: "inbox", MailInboxCheck: &MailInboxCheckStep{ProjectKey: "${vars.project}", AgentName: "${vars.agent}", UntilAckCount: 1}},
			expect: "fetch_inbox",
		},
		{
			name:   "file_reservation_release",
			step:   &Step{ID: "release", FileReservationRelease: &FileReservationReleaseStep{ProjectKey: "${vars.project}", AgentName: "${vars.agent}", Paths: StringOrList{"internal/pipeline/mail_steps.go"}}},
			expect: "release_file_reservations",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.step.hasMailStep() {
				t.Fatal("hasMailStep() = false; want true")
			}
			before := requestCount()
			result := executor.executeMailStep(context.Background(), tc.step)
			if result.Status != StatusCompleted {
				t.Fatalf("Status = %q, want StatusCompleted; error=%+v", result.Status, result.Error)
			}
			if result.Output == "" {
				t.Fatal("Output is empty")
			}
			if result.StartedAt.IsZero() || result.FinishedAt.IsZero() {
				t.Fatalf("StartedAt/FinishedAt should be populated; got %v / %v", result.StartedAt, result.FinishedAt)
			}
			if result.Error != nil {
				t.Fatalf("Error = %v, want nil", result.Error)
			}
			gotRequests := requestCount()
			if gotRequests != before+1 {
				t.Fatalf("requests = %d, want %d", gotRequests, before+1)
			}
			gotRequest := requestAt(before)
			if gotRequest.name != tc.expect {
				t.Fatalf("tool = %q, want %q", gotRequest.name, tc.expect)
			}
			if got := gotRequest.args["project_key"]; got != "/data/projects/ntm" {
				t.Fatalf("project_key = %#v, want substituted project", got)
			}
			if got := gotRequest.args["agent_name"]; got != "YellowBluff" && gotRequest.name != "send_message" {
				t.Fatalf("agent_name = %#v, want YellowBluff", got)
			}
			if got := gotRequest.args["sender_name"]; got != nil && got != "YellowBluff" {
				t.Fatalf("sender_name = %#v, want YellowBluff", got)
			}
			var output map[string]interface{}
			if err := json.Unmarshal([]byte(result.Output), &output); err != nil {
				t.Fatalf("unmarshal output: %v", err)
			}
			if output["action"] == "" {
				t.Fatalf("output action missing: %s", result.Output)
			}
		})
	}
}

func TestExecutorRun_AgentMailStepsUseRuntimeFixture(t *testing.T) {
	server, requests := newAgentMailRuntimeFixture(t, "")
	defer server.Close()
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "agent-mail-runtime",
		Steps: []Step{
			{
				ID: "send",
				MailSend: &MailSendStep{
					ProjectKey:  "${vars.project}",
					AgentName:   "${vars.agent}",
					To:          StringOrList{"${vars.recipient}"},
					Subject:     "[${vars.thread}] status",
					Body:        "ready for ${vars.thread}",
					ThreadID:    "${vars.thread}",
					AckRequired: true,
				},
				OutputVar:   "sent",
				OutputParse: OutputParse{Type: "json"},
			},
			{
				ID:        "reserve",
				DependsOn: []string{"send"},
				FileReservationPaths: &FileReservationPathsStep{
					ProjectKey: "${vars.project}",
					AgentName:  "${vars.agent}",
					Paths:      StringOrList{"${vars.path}"},
					TTLSeconds: 60,
					Exclusive:  true,
					Reason:     "${vars.sent_parsed.thread_id}",
				},
				OutputVar:   "reservation",
				OutputParse: OutputParse{Type: "json"},
			},
			{
				ID:        "inbox",
				DependsOn: []string{"reserve"},
				MailInboxCheck: &MailInboxCheckStep{
					ProjectKey:    "${vars.project}",
					AgentName:     "${vars.agent}",
					UntilAckCount: 1,
				},
				OutputVar:   "inbox",
				OutputParse: OutputParse{Type: "json"},
			},
			{
				ID:        "release",
				DependsOn: []string{"inbox"},
				FileReservationRelease: &FileReservationReleaseStep{
					ProjectKey: "${vars.project}",
					AgentName:  "${vars.agent}",
					Paths:      StringOrList{"${vars.path}"},
				},
				OutputVar:   "release",
				OutputParse: OutputParse{Type: "json"},
			},
		},
	}

	cfg := DefaultExecutorConfig("agent-mail-runtime")
	cfg.ProjectDir = t.TempDir()
	executor := NewExecutor(cfg)
	state, err := executor.Run(t.Context(), workflow, map[string]interface{}{
		"project":   "/data/projects/ntm",
		"agent":     "YellowBluff",
		"recipient": "SageFern",
		"thread":    "bd-fxj4f.2",
		"path":      "internal/pipeline/mail_steps_test.go",
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("workflow Status = %q, want %q; errors=%+v", state.Status, StatusCompleted, state.Errors)
	}
	for _, stepID := range []string{"send", "reserve", "inbox", "release"} {
		if got := state.Steps[stepID].Status; got != StatusCompleted {
			t.Fatalf("step %s Status = %q, want %q; result=%+v", stepID, got, StatusCompleted, state.Steps[stepID])
		}
	}

	gotRequests := requests()
	wantTools := []string{"send_message", "file_reservation_paths", "fetch_inbox", "release_file_reservations"}
	if len(gotRequests) != len(wantTools) {
		t.Fatalf("request count = %d, want %d; requests=%+v", len(gotRequests), len(wantTools), gotRequests)
	}
	for i, want := range wantTools {
		if gotRequests[i].name != want {
			t.Fatalf("request[%d] tool = %q, want %q; requests=%+v", i, gotRequests[i].name, want, gotRequests)
		}
	}
	cachedClients, cachedProject := func() (int, bool) {
		executor.mailClients.mu.Lock()
		defer executor.mailClients.mu.Unlock()
		_, cachedProject := executor.mailClients.byProject["/data/projects/ntm"]
		return len(executor.mailClients.byProject), cachedProject
	}()
	if cachedClients != 1 || !cachedProject {
		t.Fatalf("cached Agent Mail clients = %d, has project cache = %v; want one client for /data/projects/ntm", cachedClients, cachedProject)
	}
	if got := gotRequests[1].args["reason"]; got != "bd-fxj4f.2" {
		t.Fatalf("reservation reason = %#v, want thread from sent output_var parsed data", got)
	}
	if got := gotRequests[3].args["file_reservation_ids"]; !reflect.DeepEqual(got, []interface{}{float64(202)}) || gotRequests[3].args["paths"] != nil {
		t.Fatalf("release selectors = %#v, want only this run's captured ID", gotRequests[3].args)
	}

	sentParsed, ok := state.Variables["sent_parsed"].(map[string]interface{})
	if !ok {
		t.Fatalf("sent_parsed = %#v, want parsed JSON map", state.Variables["sent_parsed"])
	}
	if got := sentParsed["thread_id"]; got != "bd-fxj4f.2" {
		t.Fatalf("sent_parsed.thread_id = %#v, want bd-fxj4f.2", got)
	}
	inboxParsed, ok := state.Variables["inbox_parsed"].(map[string]interface{})
	if !ok {
		t.Fatalf("inbox_parsed = %#v, want parsed JSON map", state.Variables["inbox_parsed"])
	}
	if got := inboxParsed["ack_required_count"]; got != float64(1) {
		t.Fatalf("inbox_parsed.ack_required_count = %#v, want 1", got)
	}
}

func TestExecutorRun_AgentMailDryRunDoesNotCallFixture(t *testing.T) {
	var calledMu sync.Mutex
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledMu.Lock()
		called = true
		calledMu.Unlock()
		http.Error(w, "dry-run should not call Agent Mail", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "agent-mail-dry-run",
		Steps: []Step{
			{
				ID:        "send",
				MailSend:  validMailSendStep(),
				OutputVar: "dry_result",
			},
		},
	}

	cfg := DefaultExecutorConfig("agent-mail-dry-run")
	cfg.ProjectDir = t.TempDir()
	cfg.DryRun = true
	executor := NewExecutor(cfg)
	state, err := executor.Run(t.Context(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	calledMu.Lock()
	wasCalled := called
	calledMu.Unlock()
	if wasCalled {
		t.Fatal("dry-run workflow called Agent Mail fixture")
	}
	if state.Status != StatusCompleted {
		t.Fatalf("workflow Status = %q, want completed; errors=%+v", state.Status, state.Errors)
	}
	output, ok := state.Variables["dry_result"].(string)
	if !ok || !strings.Contains(output, "dry_run") {
		t.Fatalf("dry_result = %#v, want dry_run output string", state.Variables["dry_result"])
	}
}

func TestExecutorRun_AgentMailStepErrorPath(t *testing.T) {
	server, _ := newAgentMailRuntimeFixture(t, "send_message")
	defer server.Close()
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "agent-mail-error",
		Steps: []Step{
			{
				ID:       "send",
				MailSend: validMailSendStep(),
			},
		},
	}

	cfg := DefaultExecutorConfig("agent-mail-error")
	cfg.ProjectDir = t.TempDir()
	executor := NewExecutor(cfg)
	state, err := executor.Run(t.Context(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want Agent Mail fixture failure")
	}
	if state.Status != StatusFailed {
		t.Fatalf("workflow Status = %q, want failed", state.Status)
	}
	result := state.Steps["send"]
	if result.Status != StatusFailed {
		t.Fatalf("send Status = %q, want failed; result=%+v", result.Status, result)
	}
	if result.Error == nil || result.Error.Type != "delivery_unknown" || !strings.Contains(result.Error.Message, "fixture failure for send_message") {
		t.Fatalf("send error = %+v, want uncertain delivery with original Agent Mail failure", result.Error)
	}
}

func TestMailSendJournalLostResponseDoesNotRetryOrResume(t *testing.T) {
	server, counts := newMailSendJournalFixture(t, "lost")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "mail-lost-response", Steps: []Step{
		{ID: "send", MailSend: validMailSendStep(), OnError: ErrorActionRetry, RetryCount: 2, RetryDelay: Duration{Duration: time.Millisecond}},
	}}
	cfg := DefaultExecutorConfig("mail-journal")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "mail-lost-response"
	state, err := RunControlledPipeline(t.Context(), workflow, nil, cfg, nil)
	if err == nil || state == nil || state.Status != StatusFailed {
		t.Fatalf("lost response should fail the workflow: state=%+v err=%v", state, err)
	}
	result := state.Steps["send"]
	if result.Error == nil || result.Error.Type != "delivery_unknown" || result.Attempts != 1 {
		t.Fatalf("unsafe automatic retry or missing recovery diagnostic: %+v", result)
	}
	if calls, delivered := counts(); calls != 1 || delivered != 1 {
		t.Fatalf("accepted message was retried: calls=%d delivered=%d", calls, delivered)
	}
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil || prior.MailSends["send"].Status != mailSendSending {
		t.Fatalf("missing durable uncertainty: state=%+v err=%v", prior, err)
	}
	before, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resumeMailSendJournal(t, cfg, ResumeOptions{}); err == nil || !strings.Contains(err.Error(), "unresolved delivery") {
		t.Fatalf("ordinary resume must refuse ambiguous delivery: %v", err)
	}
	after, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID))
	if err != nil || string(after) != string(before) {
		t.Fatalf("rejected resume rewrote recovery evidence: %v", err)
	}
	if calls, delivered := counts(); calls != 1 || delivered != 1 {
		t.Fatalf("ordinary resume duplicated mail: calls=%d delivered=%d", calls, delivered)
	}
	// Deliberate replay is still available after the operator inspects the
	// recipient's inbox. Only this explicit choice permits another send.
	resumed, err := resumeMailSendJournal(t, cfg, ResumeOptions{Mode: ResumeModeRestartFailed})
	if err != nil || resumed == nil || resumed.Status != StatusCompleted || resumed.MailSends["send"].Status != mailSendSent {
		t.Fatalf("explicit replay failed: state=%+v err=%v", resumed, err)
	}
	if calls, delivered := counts(); calls != 2 || delivered != 2 {
		t.Fatalf("explicit replay sent an unexpected number of messages: calls=%d delivered=%d", calls, delivered)
	}
}

func TestMailSendJournalRestoresReceiptBeforeSchedulerCheckpoint(t *testing.T) {
	server, requests := newAgentMailRuntimeFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "mail-receipt-recovery", Steps: []Step{
		{ID: "send", MailSend: validMailSendStep(), OutputVar: "sent", OutputParse: OutputParse{Type: "json"}},
		{ID: "after", DependsOn: []string{"send"}, MailInboxCheck: &MailInboxCheckStep{ProjectKey: "/data/projects/ntm", AgentName: "${vars.sent_parsed.thread_id}"}},
	}}
	e := newMailStepTestExecutor(t)
	e.config.RunID = e.state.RunID
	frozen, path, err := SnapshotWorkflow(t.Context(), e.config.ProjectDir, workflow, "")
	if err != nil {
		t.Fatal(err)
	}
	e.config.WorkflowFile = path
	e.state.WorkflowID, e.state.WorkflowFile, e.state.Session = workflow.Name, path, e.config.Session
	e.state.Status, e.state.StartedAt = StatusRunning, time.Now()
	e.graph = NewDependencyGraph(frozen)
	// Execute the real leaf lifecycle, stopping at the boundary where its
	// caller would normally checkpoint Steps and named output variables.
	first := e.executeStep(t.Context(), &frozen.Steps[0], frozen)
	if first.Status != StatusCompleted {
		t.Fatalf("initial send failed: %+v", first)
	}
	prior, err := LoadState(e.config.ProjectDir, e.config.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, recorded := prior.Steps["send"]; recorded || prior.MailSends["send"].Status != mailSendSent || prior.InFlightSteps["send"].Kind != StepKindMailSend {
		t.Fatalf("fixture missed the receipt/scheduler crash window: %+v", prior)
	}
	resumed, err := resumeMailSendJournal(t, e.config, ResumeOptions{})
	if err != nil || resumed == nil || resumed.Status != StatusCompleted {
		t.Fatalf("receipt recovery failed: state=%+v err=%v", resumed, err)
	}
	got := requests()
	if len(got) != 2 || got[0].name != "send_message" || got[1].name != "fetch_inbox" || got[1].args["agent_name"] != "bd-b5l8d" {
		t.Fatalf("recovery repeated send or lost its parsed output: %+v", got)
	}
	if resumed.Steps["send"].Output != first.Output || resumed.Variables["sent"] != first.Output || !reflect.DeepEqual(resumed.Steps["send"].ParsedData, first.ParsedData) {
		t.Fatalf("recovery changed original output: before=%+v after=%+v", first, resumed.Steps["send"])
	}
}

func TestMailSendJournalCrashIntentAndLegacyStateRefuseReplay(t *testing.T) {
	for _, journaled := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "journaled"}[journaled], func(t *testing.T) {
			server, counts := newMailSendJournalFixture(t)
			t.Cleanup(server.Close)
			t.Setenv("AGENT_MAIL_URL", server.URL+"/")
			t.Setenv("AGENT_MAIL_TOKEN", "")
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "mail-crash", Steps: []Step{{ID: "send", MailSend: validMailSendStep()}}}
			e := newMailStepTestExecutor(t)
			e.config.RunID = e.state.RunID
			e.state.WorkflowID, e.state.Session = workflow.Name, e.config.Session
			e.state.Status = StatusRunning
			e.graph = NewDependencyGraph(workflow)
			e.markStepInFlight("send", StepKindMailSend, -1)
			if journaled {
				hash, err := agentStepHash(&workflow.Steps[0])
				if err != nil {
					t.Fatal(err)
				}
				if err := e.saveMailSend(MailSendState{Version: mailSendVersion, StepID: "send", StepHash: hash, Status: mailSendSending, StartedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			} else if err := e.persistState(); err != nil {
				t.Fatal(err)
			}
			prior, err := LoadState(e.config.ProjectDir, e.config.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewExecutor(e.config).Resume(t.Context(), workflow, prior, nil); err == nil || !strings.Contains(err.Error(), "unresolved delivery") {
				t.Fatalf("crash checkpoint silently replayed: %v", err)
			}
			if calls, _ := counts(); calls != 0 {
				t.Fatalf("crash recovery dispatched %d messages", calls)
			}
		})
	}
}

func TestMailSendJournalDefinitiveRejectionAllowsRetry(t *testing.T) {
	server, counts := newMailSendJournalFixture(t, "rejected")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "mail-rejection", Steps: []Step{
		{ID: "send", MailSend: validMailSendStep(), OnError: ErrorActionRetry, RetryCount: 2, RetryDelay: Duration{Duration: time.Millisecond}},
	}}
	cfg := DefaultExecutorConfig("mail-journal")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "mail-rejection"
	state, err := RunControlledPipeline(t.Context(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted || state.Steps["send"].Attempts != 2 {
		t.Fatalf("definitive refusal did not permit retry: state=%+v err=%v", state, err)
	}
	if calls, delivered := counts(); calls != 2 || delivered != 1 {
		t.Fatalf("rejection retry duplicated delivery: calls=%d delivered=%d", calls, delivered)
	}
}

func TestMailSendJournalParsingFailureNeverResends(t *testing.T) {
	server, counts := newMailSendJournalFixture(t)
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "mail-parse-failure", Steps: []Step{
		{ID: "send", MailSend: validMailSendStep(), OutputParse: OutputParse{Type: "regex", Pattern: "["}, OnError: ErrorActionRetry, RetryCount: 2, RetryDelay: Duration{Duration: time.Millisecond}},
	}}
	cfg := DefaultExecutorConfig("mail-journal")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "mail-parse-failure"
	state, err := NewExecutor(cfg).Run(t.Context(), workflow, nil, nil)
	if err == nil || state == nil || state.Steps["send"].Error == nil || state.Steps["send"].Error.Type != "output_parse" || state.Steps["send"].Attempts != 1 || state.MailSends["send"].Status != mailSendSent {
		t.Fatalf("parsing failure lost delivery evidence: state=%+v err=%v", state, err)
	}
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ResumeOptions.Mode = ResumeModeRestartFailed
	if _, err := NewExecutor(cfg).Resume(t.Context(), workflow, prior, nil); err == nil {
		t.Fatal("resume unexpectedly repaired the invalid parser")
	}
	if calls, delivered := counts(); calls != 1 || delivered != 1 {
		t.Fatalf("parsing failure resent mail: calls=%d delivered=%d", calls, delivered)
	}
}

func resumeMailSendJournal(t *testing.T, cfg ExecutorConfig, opts ResumeOptions) (*ExecutionState, error) {
	t.Helper()
	owner, err := AcquireRunControl(t.Context(), cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, valid, err := LoadResumeWorkflow(prior.WorkflowFile)
	if err != nil || !valid.Valid {
		t.Fatalf("load saved workflow: err=%v validation=%+v", err, valid)
	}
	cfg.WorkflowFile, cfg.ResumeOptions = prior.WorkflowFile, opts
	return NewExecutor(cfg).Resume(owner.Context(), frozen, prior, nil)
}

// A lost response occurs after the server has accepted the side effect. An
// explicit MCP isError result is the only failure modeled as a known refusal.
func newMailSendJournalFixture(t *testing.T, replies ...string) (*httptest.Server, func() (int, int)) {
	t.Helper()
	var mu sync.Mutex
	calls, delivered := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     interface{} `json:"id"`
			Params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Params.Name != "send_message" {
			t.Errorf("unexpected mail fixture request: %+v err=%v", request, err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		reply := "success"
		if calls < len(replies) {
			reply = replies[calls]
		}
		calls++
		if reply != "rejected" {
			delivered++
		}
		mu.Unlock()
		if reply == "lost" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		var result interface{}
		if reply == "rejected" {
			result = map[string]interface{}{"isError": true, "content": []map[string]interface{}{{"type": "text", "text": "sender is not registered"}}}
		} else {
			args := request.Params.Arguments
			result = map[string]interface{}{"count": 1, "deliveries": []map[string]interface{}{{
				"project": args["project_key"], "payload": map[string]interface{}{
					"id": 101, "from": args["sender_name"], "to": args["to"], "subject": args["subject"], "body_md": args["body_md"], "thread_id": args["thread_id"],
				},
			}}}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			t.Error(err)
		}
	}))
	return server, func() (int, int) {
		mu.Lock()
		defer mu.Unlock()
		return calls, delivered
	}
}

func TestExecuteMailStep_DryRunDoesNotCallAgentMail(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	executor := newMailStepTestExecutor(t)
	executor.config.DryRun = true
	result := executor.executeMailStep(context.Background(), &Step{
		ID:       "notify",
		MailSend: validMailSendStep(),
	})

	if called {
		t.Fatal("dry-run mail step called Agent Mail server")
	}
	if result.Status != StatusCompleted {
		t.Fatalf("Status = %q, want completed", result.Status)
	}
	if !strings.Contains(result.Output, "dry_run") {
		t.Fatalf("Output = %q, want dry_run marker", result.Output)
	}
}

type mailRuntimeFixtureRequest struct {
	name string
	args map[string]interface{}
}

func newAgentMailRuntimeFixture(t *testing.T, failTool string) (*httptest.Server, func() []mailRuntimeFixtureRequest) {
	t.Helper()

	var requestsMu sync.Mutex
	requests := make([]mailRuntimeFixtureRequest, 0, 4)
	record := func(name string, args map[string]interface{}) {
		requestsMu.Lock()
		defer requestsMu.Unlock()
		requests = append(requests, mailRuntimeFixtureRequest{name: name, args: args})
	}
	snapshot := func() []mailRuntimeFixtureRequest {
		requestsMu.Lock()
		defer requestsMu.Unlock()
		return append([]mailRuntimeFixtureRequest(nil), requests...)
	}

	ledger := &reservationLedger{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		var req struct {
			JSONRPC string                 `json:"jsonrpc"`
			ID      interface{}            `json:"id"`
			Method  string                 `json:"method"`
			Params  map[string]interface{} `json:"params"`
		}
		body := http.MaxBytesReader(w, r.Body, 1<<20)
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if ledger.serveReadback(t, w, req.ID, req.Method, req.Params) {
			return
		}
		if req.Method != "tools/call" {
			t.Fatalf("method = %s, want tools/call", req.Method)
		}
		toolName, _ := req.Params["name"].(string)
		rawArgs, _ := req.Params["arguments"].(map[string]interface{})
		args := make(map[string]interface{}, len(rawArgs))
		for k, v := range rawArgs {
			args[k] = v
		}
		record(toolName, args)

		w.Header().Set("Content-Type", "application/json")
		if failTool != "" && toolName == failTool {
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error": map[string]interface{}{
					"code":    -32000,
					"message": "fixture failure for " + toolName,
				},
			}); err != nil {
				t.Fatalf("encode error response: %v", err)
			}
			return
		}

		var result interface{}
		switch toolName {
		case "send_message":
			result = map[string]interface{}{
				"count": 1,
				"deliveries": []map[string]interface{}{
					{
						"project": args["project_key"],
						"payload": map[string]interface{}{
							"id":         101,
							"subject":    args["subject"],
							"body_md":    args["body_md"],
							"from":       args["sender_name"],
							"to":         args["to"],
							"thread_id":  args["thread_id"],
							"created_ts": "2026-05-08T00:00:00Z",
						},
					},
				},
			}
		case "file_reservation_paths":
			// Real Agent Mail grant receipts carry no project_id/agent_name;
			// ownership comes from the ledger readback.
			result = map[string]interface{}{
				"granted":   []map[string]interface{}{ledger.grant(202, "internal/pipeline/mail_steps_test.go", args)},
				"conflicts": []interface{}{},
			}
		case "fetch_inbox":
			result = []map[string]interface{}{
				{
					"id":           303,
					"subject":      "please ack",
					"from":         "SageFern",
					"created_ts":   "2026-05-08T00:00:00Z",
					"importance":   "normal",
					"ack_required": true,
					"kind":         "message",
				},
			}
		case "release_file_reservations":
			result = map[string]interface{}{
				"released":    ledger.release(args),
				"released_at": "2026-05-08T00:00:00Z",
			}
		default:
			t.Fatalf("unexpected tool %q", toolName)
		}

		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))

	return server, snapshot
}

func newMailStepTestExecutor(t *testing.T) *Executor {
	t.Helper()
	cfg := DefaultExecutorConfig("mail-step-test")
	cfg.ProjectDir = t.TempDir()
	executor := NewExecutor(cfg)
	executor.state = &ExecutionState{
		RunID:      "run-mail-step",
		WorkflowID: "workflow-mail-step",
		Variables: map[string]interface{}{
			"project":   "/data/projects/ntm",
			"agent":     "YellowBluff",
			"recipient": "SageFern",
			"thread":    "bd-tyfli",
			"body":      "Done",
		},
		Steps: map[string]StepResult{},
	}
	return executor
}

func TestStep_HasMailStep_FalseWhenAbsent(t *testing.T) {
	if (&Step{ID: "x", Command: "/bin/true"}).hasMailStep() {
		t.Errorf("hasMailStep() returned true for command step")
	}
	if (&Step{ID: "x", Prompt: "hello"}).hasMailStep() {
		t.Errorf("hasMailStep() returned true for prompt step")
	}
	if (*Step)(nil).hasMailStep() {
		t.Errorf("hasMailStep() on nil receiver returned true")
	}
}

const (
	reservationLedgerCreated = "2026-05-08T09:00:00Z"
	reservationLedgerExpires = "2099-05-08T10:00:00Z"
)

// reservationLedger models the live reservation rows behind an Agent Mail
// fixture. agentmail.ReservePaths never trusts a grant receipt on its own: it
// reads the project identity (resource://project/{key}) and the active
// reservations (resource://file_reservations/{key}) back from the server,
// read-only, and matches the granted IDs against them.
type reservationLedger struct {
	mu   sync.Mutex
	rows []map[string]interface{}
}

func (l *reservationLedger) release(args map[string]interface{}) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	ids, paths := make(map[int]bool), make(map[string]bool)
	if selected, ok := args["file_reservation_ids"].([]interface{}); ok {
		for _, value := range selected {
			if id, ok := value.(float64); ok {
				ids[int(id)] = true
			}
		}
	}
	if selected, ok := args["paths"].([]interface{}); ok {
		for _, value := range selected {
			if path, ok := value.(string); ok {
				paths[path] = true
			}
		}
	}
	kept := make([]map[string]interface{}, 0, len(l.rows))
	released := 0
	for _, row := range l.rows {
		id, _ := row["id"].(int)
		path, _ := row["path_pattern"].(string)
		if row["agent"] == args["agent_name"] && (ids[id] || paths[path]) {
			released++
		} else {
			kept = append(kept, row)
		}
	}
	l.rows = kept
	return released
}

// grant records a live row for the request and returns its receipt.
func (l *reservationLedger) grant(id int, path string, args map[string]interface{}) map[string]interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rows = append(l.rows, map[string]interface{}{
		"id": id, "agent": args["agent_name"], "path_pattern": path,
		"exclusive": args["exclusive"], "reason": args["reason"],
		"created_ts": reservationLedgerCreated, "expires_ts": reservationLedgerExpires,
	})
	return map[string]interface{}{
		"id": id, "path_pattern": path, "exclusive": args["exclusive"],
		"reason": args["reason"], "expires_ts": reservationLedgerExpires,
	}
}

// serveReadback answers the ownership readback resources and reports whether
// the request was one of them.
func (l *reservationLedger) serveReadback(t *testing.T, w http.ResponseWriter, id interface{}, method string, params map[string]interface{}) bool {
	t.Helper()
	if method != "resources/read" {
		return false
	}
	uri, _ := params["uri"].(string)
	var payload interface{}
	switch {
	case strings.HasPrefix(uri, "resource://project/"):
		key, err := url.PathUnescape(strings.TrimPrefix(uri, "resource://project/"))
		if err != nil {
			t.Fatalf("project resource key %q: %v", uri, err)
		}
		payload = map[string]interface{}{"id": 1, "slug": "ntm", "human_key": key}
	case strings.HasPrefix(uri, "resource://file_reservations/"):
		l.mu.Lock()
		payload = append([]map[string]interface{}{}, l.rows...)
		l.mu.Unlock()
	default:
		t.Fatalf("unexpected resource read %q", uri)
	}
	text, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode resource: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0", "id": id,
		"result": map[string]interface{}{"contents": []map[string]interface{}{{"uri": uri, "text": string(text)}}},
	}); err != nil {
		t.Fatalf("encode resource response: %v", err)
	}
	return true
}

func TestReservationJournalLostAcquisitionCannotRetryOrResume(t *testing.T) {
	server, fixture := newReservationJournalFixture(t, "lost_acquisition")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := reservationJournalWorkflow(false)
	workflow.Steps[0].OnError, workflow.Steps[0].RetryCount = ErrorActionRetry, 2
	workflow.Steps[0].RetryDelay = Duration{Duration: time.Millisecond}
	cfg := DefaultExecutorConfig("reservation-journal")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "lost-acquisition"
	state, err := RunControlledPipeline(t.Context(), workflow, nil, cfg, nil)
	if err == nil || state == nil || state.Steps["reserve"].Attempts != 1 || state.ReservationSteps["reserve"].Status != reservationUncertain {
		t.Fatalf("lost acquisition did not retain its replay barrier: state=%+v err=%v", state, err)
	}
	if _, err := resumeMailSendJournal(t, cfg, ResumeOptions{}); err == nil || !strings.Contains(err.Error(), "acquisition evidence") {
		t.Fatalf("ordinary resume accepted an ambiguous acquisition: %v", err)
	}
	if got := fixture.count("file_reservation_paths"); got != 1 {
		t.Fatalf("lost response caused %d acquisitions, want one", got)
	}
}

func TestReservationJournalRetainsPartialAndUnverifiedGrants(t *testing.T) {
	for _, mode := range []string{"partial", "unverified"} {
		t.Run(mode, func(t *testing.T) {
			server, fixture := newReservationJournalFixture(t, mode)
			t.Cleanup(server.Close)
			t.Setenv("AGENT_MAIL_URL", server.URL+"/")
			t.Setenv("AGENT_MAIL_TOKEN", "")
			workflow := reservationJournalWorkflow(false)
			workflow.Steps[0].FileReservationPaths.Paths = StringOrList{"a.go", "b.go"}
			workflow.Steps[0].OnError, workflow.Steps[0].RetryCount = ErrorActionRetry, 2
			workflow.Steps[0].RetryDelay = Duration{Duration: time.Millisecond}
			cfg := DefaultExecutorConfig("reservation-journal")
			cfg.ProjectDir, cfg.RunID = t.TempDir(), "partial-acquisition"
			state, err := RunControlledPipeline(t.Context(), workflow, nil, cfg, nil)
			if err == nil || state == nil || len(state.ReservationSteps["reserve"].Granted) == 0 || fixture.count("file_reservation_paths") != 1 {
				t.Fatalf("partial grant was discarded or reacquired: state=%+v err=%v", state, err)
			}
			if !strings.Contains(state.Steps["reserve"].Output, "601") {
				t.Fatalf("failed step output lost inspection handles: %+v", state.Steps["reserve"])
			}
			fixture.mu.Lock()
			fixture.failReadback = false
			fixture.mu.Unlock()
			e := NewExecutor(cfg)
			e.state = state
			release := Step{ID: "release", FileReservationRelease: &FileReservationReleaseStep{ProjectKey: "/data/projects/ntm", AgentName: "TealCrane", Paths: StringOrList{"a.go"}}}
			result := e.executeMailStep(t.Context(), &release)
			if mode == "unverified" {
				if state.ReservationSteps["reserve"].Status != reservationUncertain || result.Status != StatusFailed || fixture.count("release_file_reservations") != 0 {
					t.Fatalf("unverified handles authorized release: %+v record=%+v", result, state.ReservationSteps["reserve"])
				}
			} else if result.Status != StatusCompleted || state.ReservationSteps["reserve"].Status != reservationReleased || fixture.count("release_file_reservations") != 1 {
				t.Fatalf("verified partial grants could not be released: %+v record=%+v", result, state.ReservationSteps["reserve"])
			}
		})
	}
}

func TestReservationJournalResumeRejectsLostProtectionBeforeWork(t *testing.T) {
	for _, change := range []string{"expired", "replacement", "owner", "mode", "reason", "created", "unavailable"} {
		t.Run(change, func(t *testing.T) {
			server, fixture := newReservationJournalFixture(t, "")
			t.Cleanup(server.Close)
			t.Setenv("AGENT_MAIL_URL", server.URL+"/")
			t.Setenv("AGENT_MAIL_TOKEN", "")
			fixture.failInbox = true
			cfg := DefaultExecutorConfig("reservation-journal")
			cfg.ProjectDir, cfg.RunID = t.TempDir(), "protection-check"
			state, err := RunControlledPipeline(t.Context(), reservationJournalWorkflow(true), nil, cfg, nil)
			if err == nil || state == nil || state.Steps["reserve"].Status != StatusCompleted {
				t.Fatalf("initial protected workflow failed incorrectly: state=%+v err=%v", state, err)
			}
			fixture.ledger.mu.Lock()
			row := fixture.ledger.rows[0]
			switch change {
			case "expired":
				row["expires_ts"] = "2026-05-08T09:01:00Z"
			case "replacement":
				row["id"] = 999
			case "owner":
				row["agent"] = "OrangeFalcon"
			case "mode":
				row["exclusive"] = false
			case "reason":
				row["reason"] = "another workflow"
			case "created":
				row["created_ts"] = "2026-05-09T09:00:00Z"
			}
			fixture.ledger.mu.Unlock()
			fixture.mu.Lock()
			fixture.failInbox, fixture.failReadback = false, change == "unavailable"
			fixture.mu.Unlock()
			before, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := resumeMailSendJournal(t, cfg, ResumeOptions{}); err == nil || !strings.Contains(err.Error(), "reservation") {
				t.Fatalf("resume dispatched without original live protection: %v", err)
			}
			after, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID))
			if err != nil || string(after) != string(before) || fixture.count("fetch_inbox") != 1 || fixture.count("file_reservation_paths") != 1 {
				t.Fatalf("rejected resume changed checkpoint or dispatched work: err=%v requests=%+v", err, fixture.requests())
			}
		})
	}
}

func TestReservationJournalRestoresReceiptWithoutReacquiring(t *testing.T) {
	server, fixture := newReservationJournalFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := reservationJournalWorkflow(true)
	e := newMailStepTestExecutor(t)
	e.config.RunID = e.state.RunID
	frozen, path, err := SnapshotWorkflow(t.Context(), e.config.ProjectDir, workflow, "")
	if err != nil {
		t.Fatal(err)
	}
	e.config.WorkflowFile = path
	e.state.WorkflowID, e.state.WorkflowFile, e.state.Session = workflow.Name, path, e.config.Session
	e.state.Status, e.state.StartedAt = StatusRunning, time.Now()
	e.graph = NewDependencyGraph(frozen)
	first := e.executeStep(t.Context(), &frozen.Steps[0], frozen)
	if first.Status != StatusCompleted {
		t.Fatalf("initial acquisition: %+v", first)
	}
	prior, err := LoadState(e.config.ProjectDir, e.config.RunID)
	if err != nil || prior.ReservationSteps["reserve"].Status != reservationHeld || prior.Steps["reserve"].Status != "" {
		t.Fatalf("missing receipt/scheduler crash window: state=%+v err=%v", prior, err)
	}
	// Renewal changes expiry, not ownership. Recovery should accept it without
	// extending the lease again or claiming the old saved TTL is authoritative.
	fixture.ledger.mu.Lock()
	fixture.ledger.rows[0]["expires_ts"] = "2100-05-08T10:00:00Z"
	fixture.ledger.mu.Unlock()
	resumed, err := resumeMailSendJournal(t, e.config, ResumeOptions{})
	if err != nil || resumed == nil || resumed.Status != StatusCompleted || resumed.Steps["reserve"].Output != first.Output || resumed.Variables["lease"] != first.Output {
		t.Fatalf("receipt recovery lost original output: state=%+v err=%v", resumed, err)
	}
	if fixture.count("file_reservation_paths") != 1 || fixture.count("fetch_inbox") != 1 {
		t.Fatalf("receipt recovery reacquired leases: %+v", fixture.requests())
	}
}

func TestReservationJournalReleaseUsesOnlyRunOwnedIDs(t *testing.T) {
	server, fixture := newReservationJournalFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := reservationJournalWorkflow(false)
	e := newMailStepTestExecutor(t)
	if result := e.executeMailStep(t.Context(), &workflow.Steps[0]); result.Status != StatusCompleted {
		t.Fatalf("acquire: %+v", result)
	}
	fixture.ledger.grant(999, "a.go", map[string]interface{}{"agent_name": "TealCrane", "exclusive": true, "reason": "other run"})
	release := Step{ID: "release", FileReservationRelease: &FileReservationReleaseStep{ProjectKey: "/data/projects/ntm", AgentName: "TealCrane", Paths: StringOrList{"a.go"}}}
	if result := e.executeMailStep(t.Context(), &release); result.Status != StatusCompleted {
		t.Fatalf("scoped release: %+v", result)
	}
	requests := fixture.requests()
	args := requests[len(requests)-1].args
	if args["paths"] != nil || !reflect.DeepEqual(args["file_reservation_ids"], []interface{}{float64(601)}) {
		t.Fatalf("release used path or foreign selectors: %+v", args)
	}
	fixture.ledger.mu.Lock()
	remaining := append([]map[string]interface{}(nil), fixture.ledger.rows...)
	fixture.ledger.mu.Unlock()
	if len(remaining) != 1 || remaining[0]["id"] != 999 || e.state.ReservationSteps["reserve"].Status != reservationReleased {
		t.Fatalf("scoped release removed another run's lease or lost settlement: remaining=%+v state=%+v", remaining, e.state.ReservationSteps)
	}
	release.ID, release.FileReservationRelease.Paths = "foreign-release", StringOrList{"unowned.go"}
	if result := e.executeMailStep(t.Context(), &release); result.Status != StatusFailed || fixture.count("release_file_reservations") != 1 {
		t.Fatalf("unmatched paths fell back to a broad release: %+v", result)
	}
	release.ID, release.FileReservationRelease.ProjectKey, release.FileReservationRelease.Paths = "alias-release", "ntm", StringOrList{"a.go"}
	if result := e.executeMailStep(t.Context(), &release); result.Status != StatusFailed || fixture.count("release_file_reservations") != 1 {
		t.Fatalf("project alias bypassed this run's ownership boundary: %+v", result)
	}
}

func TestReservationJournalStandaloneReleaseRetainsExplicitPaths(t *testing.T) {
	server, fixture := newReservationJournalFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	fixture.ledger.grant(999, "a.go", map[string]interface{}{"agent_name": "TealCrane", "exclusive": true, "reason": "external reservation"})
	e := newMailStepTestExecutor(t)
	release := Step{ID: "release", FileReservationRelease: &FileReservationReleaseStep{ProjectKey: "/data/projects/ntm", AgentName: "TealCrane", Paths: StringOrList{"a.go"}}}
	if result := e.executeMailStep(t.Context(), &release); result.Status != StatusCompleted {
		t.Fatalf("explicit standalone release failed: %+v", result)
	}
	requests := fixture.requests()
	if len(requests) != 1 || requests[0].name != "release_file_reservations" || requests[0].args["file_reservation_ids"] != nil || !reflect.DeepEqual(requests[0].args["paths"], []interface{}{"a.go"}) {
		t.Fatalf("standalone release changed selectors: %+v", requests)
	}
}

func TestReservationJournalReleaseRecoversAfterLostReply(t *testing.T) {
	server, fixture := newReservationJournalFixture(t, "lost_release")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := reservationJournalWorkflow(false)
	workflow.Steps = append(workflow.Steps, Step{ID: "release", DependsOn: []string{"reserve"}, FileReservationRelease: &FileReservationReleaseStep{
		ProjectKey: "/data/projects/ntm", AgentName: "TealCrane", Paths: StringOrList{"a.go"},
	}})
	cfg := DefaultExecutorConfig("reservation-journal")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "release-recovery"
	state, err := RunControlledPipeline(t.Context(), workflow, nil, cfg, nil)
	if err == nil || state == nil || state.ReservationSteps["release"].Status != reservationReleasing || len(state.ReservationSteps["release"].Granted) != 1 {
		t.Fatalf("lost release did not retain exact-ID intent: state=%+v err=%v", state, err)
	}
	fixture.mu.Lock()
	fixture.failReadback = false
	fixture.mu.Unlock()
	resumed, err := resumeMailSendJournal(t, cfg, ResumeOptions{})
	if err != nil || resumed == nil || resumed.Status != StatusCompleted || resumed.ReservationSteps["reserve"].Status != reservationReleased || !strings.Contains(resumed.Steps["release"].Output, `"recovered":true`) {
		t.Fatalf("release readback recovery failed: state=%+v err=%v", resumed, err)
	}
	if fixture.count("file_reservation_paths") != 1 || fixture.count("release_file_reservations") != 1 {
		t.Fatalf("recovery repeated a settled lease mutation: %+v", fixture.requests())
	}
}

func TestReservationJournalResetCannotDiscardLiveLeases(t *testing.T) {
	server, fixture := newReservationJournalFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	fixture.failInbox = true
	cfg := DefaultExecutorConfig("reservation-journal")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "reset-protection"
	if _, err := RunControlledPipeline(t.Context(), reservationJournalWorkflow(true), nil, cfg, nil); err == nil {
		t.Fatal("fixture should stop at the inbox gate")
	}
	if _, err := resumeMailSendJournal(t, cfg, ResumeOptions{Reset: true}); err == nil || !strings.Contains(err.Error(), "active lease IDs") {
		t.Fatalf("reset discarded live lease ownership: %v", err)
	}
	fixture.mu.Lock()
	fixture.failInbox = false
	fixture.mu.Unlock()
	resumed, err := resumeMailSendJournal(t, cfg, ResumeOptions{Mode: ResumeModeRestartFailed})
	if err != nil || resumed == nil || resumed.Status != StatusCompleted || fixture.count("file_reservation_paths") != 1 {
		t.Fatalf("safe retry failed to retain live protection: state=%+v err=%v", resumed, err)
	}
}

func TestReservationJournalCancellationCleanupCannotResumeUnprotected(t *testing.T) {
	server, fixture := newReservationJournalFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := reservationJournalWorkflow(true)
	workflow.Settings.OnCancel = []Step{{ID: "cancel-release", FileReservationRelease: &FileReservationReleaseStep{
		ProjectKey: "/data/projects/ntm", AgentName: "TealCrane", Paths: StringOrList{"a.go"},
	}}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fixture.cancelInbox = cancel
	cfg := DefaultExecutorConfig("reservation-journal")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "cancelled-protection"
	state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
	if err == nil || state == nil || state.Status != StatusCancelled || state.ReservationSteps["reserve"].Status != reservationReleased {
		t.Fatalf("authored cancellation cleanup failed: state=%+v err=%v", state, err)
	}
	fixture.mu.Lock()
	fixture.cancelInbox = nil
	fixture.mu.Unlock()
	if _, err := resumeMailSendJournal(t, cfg, ResumeOptions{}); err == nil || !strings.Contains(err.Error(), "released by cleanup") {
		t.Fatalf("resume continued without protection released during cancellation: %v", err)
	}
	resumed, err := resumeMailSendJournal(t, cfg, ResumeOptions{Reset: true})
	if err != nil || resumed == nil || resumed.Status != StatusCompleted || resumed.ReservationSteps["reserve"].Status != reservationHeld || fixture.count("file_reservation_paths") != 2 {
		t.Fatalf("explicit reset did not reacquire protection: state=%+v err=%v", resumed, err)
	}
}

func TestReservationJournalCancelledResumeRetainsReceiptsForCleanup(t *testing.T) {
	server, fixture := newReservationJournalFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	fixture.failInbox = true
	workflow := reservationJournalWorkflow(true)
	workflow.Settings.OnCancel = []Step{{ID: "cancel-release", FileReservationRelease: &FileReservationReleaseStep{
		ProjectKey: "/data/projects/ntm", AgentName: "TealCrane", Paths: StringOrList{"a.go"},
	}}}
	cfg := DefaultExecutorConfig("reservation-journal")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "cancelled-resume-cleanup"
	if _, err := RunControlledPipeline(t.Context(), workflow, nil, cfg, nil); err == nil {
		t.Fatal("fixture should stop at the inbox gate while retaining its lease")
	}
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, valid, err := LoadResumeWorkflow(prior.WorkflowFile)
	if err != nil || !valid.Valid {
		t.Fatalf("load saved workflow: %v %+v", err, valid)
	}
	cfg.WorkflowFile = prior.WorkflowFile
	// A cancelled reset must not discard the IDs that authored cleanup needs.
	cfg.ResumeOptions.Reset = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	state, err := NewExecutor(cfg).Resume(ctx, frozen, prior, nil)
	if !errors.Is(err, context.Canceled) || state == nil || state.Status != StatusCancelled || state.ReservationSteps["reserve"].Status != reservationReleased {
		t.Fatalf("cancelled admission skipped cleanup or erased lease identity: state=%+v err=%v", state, err)
	}
	if fixture.count("file_reservation_paths") != 1 || fixture.count("fetch_inbox") != 1 || fixture.count("release_file_reservations") != 1 {
		t.Fatalf("cancelled resume dispatched normal work or skipped cleanup: %+v", fixture.requests())
	}
}

func TestReservationJournalForceIterationCannotRetainReleasedProtection(t *testing.T) {
	server, fixture := newReservationJournalFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := reservationJournalWorkflow(false)
	workflow.Steps = append(workflow.Steps, Step{ID: "work", DependsOn: []string{"reserve"}, Loop: &LoopConfig{Times: 1, Steps: []Step{{
		ID: "check", MailInboxCheck: &MailInboxCheckStep{ProjectKey: "/data/projects/ntm", AgentName: "TealCrane"},
	}}}})
	workflow.PostPipelineSteps = []Step{{ID: "finish-release", FileReservationRelease: &FileReservationReleaseStep{
		ProjectKey: "/data/projects/ntm", AgentName: "TealCrane", Paths: StringOrList{"a.go"},
	}}}
	cfg := DefaultExecutorConfig("reservation-journal")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "iteration-protection"
	state, err := RunControlledPipeline(t.Context(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted || state.ReservationSteps["reserve"].Status != reservationReleased {
		t.Fatalf("initial protected loop: state=%+v err=%v", state, err)
	}
	if _, err := resumeMailSendJournal(t, cfg, ResumeOptions{Mode: ResumeModeForceIter, StepID: "work", Iteration: 0}); err == nil || !strings.Contains(err.Error(), "released protection") {
		t.Fatalf("forced iteration reused released protection: %v", err)
	}
	if fixture.count("fetch_inbox") != 1 || fixture.count("file_reservation_paths") != 1 {
		t.Fatalf("refused rewind dispatched new work: %+v", fixture.requests())
	}
}

func TestReservationJournalCleanupOnlyResumeToleratesAbsentIDs(t *testing.T) {
	server, fixture := newReservationJournalFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := reservationJournalWorkflow(false)
	workflow.PostPipelineSteps = []Step{{ID: "finish-release", FileReservationRelease: &FileReservationReleaseStep{
		ProjectKey: "/data/projects/ntm", AgentName: "TealCrane", Paths: StringOrList{"a.go"},
	}}}
	e := newMailStepTestExecutor(t)
	e.config.RunID = e.state.RunID
	frozen, path, err := SnapshotWorkflow(t.Context(), e.config.ProjectDir, workflow, "")
	if err != nil {
		t.Fatal(err)
	}
	e.config.WorkflowFile = path
	e.state.WorkflowID, e.state.WorkflowFile, e.state.Session = workflow.Name, path, e.config.Session
	e.state.Status, e.state.StartedAt = StatusRunning, time.Now()
	e.graph = NewDependencyGraph(frozen)
	result := e.executeStep(t.Context(), &frozen.Steps[0], frozen)
	if result.Status != StatusCompleted {
		t.Fatalf("acquire: %+v", result)
	}
	e.state.Steps["reserve"] = result
	if err := e.persistState(); err != nil {
		t.Fatal(err)
	}
	// Main work finished; the process died before its release hook started.
	// Expired leases must not prevent that cleanup-only attempt from finishing.
	fixture.ledger.mu.Lock()
	fixture.ledger.rows = nil
	fixture.ledger.mu.Unlock()
	resumed, err := resumeMailSendJournal(t, e.config, ResumeOptions{})
	if err != nil || resumed == nil || resumed.Status != StatusCompleted || resumed.ReservationSteps["reserve"].Status != reservationReleased {
		t.Fatalf("cleanup-only recovery was blocked by absent leases: state=%+v err=%v", resumed, err)
	}
	if fixture.count("file_reservation_paths") != 1 || fixture.count("release_file_reservations") != 0 {
		t.Fatalf("cleanup-only recovery mutated nonexistent leases: %+v", fixture.requests())
	}
}

func TestReservationJournalConflictingReleaseIdentityCannotHideLostLease(t *testing.T) {
	server, fixture := newReservationJournalFixture(t, "lost_release")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	workflow := reservationJournalWorkflow(false)
	workflow.Steps = append(workflow.Steps, Step{ID: "release", DependsOn: []string{"reserve"}, FileReservationRelease: &FileReservationReleaseStep{
		ProjectKey: "/data/projects/ntm", AgentName: "TealCrane", Paths: StringOrList{"a.go"},
	}})
	cfg := DefaultExecutorConfig("reservation-journal")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "conflicting-lease-history"
	if _, err := RunControlledPipeline(t.Context(), workflow, nil, cfg, nil); err == nil {
		t.Fatal("fixture should stop after the lost release response")
	}
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	release := prior.ReservationSteps["release"]
	release.Granted[0].Reason = "different lease history"
	prior.ReservationSteps["release"] = release
	if err := SaveState(cfg.ProjectDir, prior); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.failReadback = false
	fixture.mu.Unlock()
	for _, opts := range []ResumeOptions{{}, {Reset: true}} {
		if _, err := resumeMailSendJournal(t, cfg, opts); err == nil || !strings.Contains(err.Error(), "conflicting captured identity") {
			t.Fatalf("contradictory release history authorized resume/reset: %v", err)
		}
	}
	if fixture.count("file_reservation_paths") != 1 || fixture.count("release_file_reservations") != 1 {
		t.Fatalf("invalid release history dispatched mutations: %+v", fixture.requests())
	}
}

func reservationJournalWorkflow(withGate bool) *Workflow {
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "reservation-journal", Steps: []Step{{
		ID: "reserve", FileReservationPaths: &FileReservationPathsStep{ProjectKey: "/data/projects/ntm", AgentName: "TealCrane", Paths: StringOrList{"a.go"}, Exclusive: true, Reason: "pipeline work"},
		OutputVar: "lease", OutputParse: OutputParse{Type: "json"},
	}}}
	if withGate {
		workflow.Steps = append(workflow.Steps, Step{ID: "gate", DependsOn: []string{"reserve"}, MailInboxCheck: &MailInboxCheckStep{
			ProjectKey: "/data/projects/ntm", AgentName: "${vars.lease_parsed.action}",
		}})
	}
	return workflow
}

type reservationJournalFixture struct {
	mu           sync.Mutex
	ledger       reservationLedger
	recorded     []mailRuntimeFixtureRequest
	mode         string
	failInbox    bool
	failReadback bool
	nextID       int
	cancelInbox  context.CancelFunc
}

func (f *reservationJournalFixture) requests() []mailRuntimeFixtureRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mailRuntimeFixtureRequest(nil), f.recorded...)
}

func (f *reservationJournalFixture) count(tool string) int {
	count := 0
	for _, request := range f.requests() {
		if request.name == tool {
			count++
		}
	}
	return count
}

func newReservationJournalFixture(t *testing.T, mode string) (*httptest.Server, *reservationJournalFixture) {
	t.Helper()
	fixture := &reservationJournalFixture{mode: mode, nextID: 600, failReadback: mode == "unverified"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     interface{}            `json:"id"`
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fixture.mu.Lock()
		failReadback, failInbox, replyMode, cancelInbox := fixture.failReadback, fixture.failInbox, fixture.mode, fixture.cancelInbox
		fixture.mu.Unlock()
		if req.Method == "resources/read" {
			if failReadback {
				http.Error(w, "readback temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			fixture.ledger.serveReadback(t, w, req.ID, req.Method, req.Params)
			return
		}
		name, _ := req.Params["name"].(string)
		args, _ := req.Params["arguments"].(map[string]interface{})
		fixture.mu.Lock()
		fixture.recorded = append(fixture.recorded, mailRuntimeFixtureRequest{name: name, args: args})
		fixture.mu.Unlock()
		var result interface{}
		switch name {
		case "file_reservation_paths":
			paths, _ := args["paths"].([]interface{})
			granted := make([]map[string]interface{}, 0, len(paths))
			conflicts := make([]map[string]interface{}, 0)
			for i, value := range paths {
				path, _ := value.(string)
				if replyMode == "partial" && i > 0 {
					conflicts = append(conflicts, map[string]interface{}{"path": path, "holders": []string{"OrangeFalcon"}})
					continue
				}
				fixture.mu.Lock()
				fixture.nextID++
				id := fixture.nextID
				fixture.mu.Unlock()
				granted = append(granted, fixture.ledger.grant(id, path, args))
			}
			result = map[string]interface{}{"granted": granted, "conflicts": conflicts}
		case "release_file_reservations":
			result = map[string]interface{}{"released": fixture.ledger.release(args)}
			if replyMode == "lost_release" {
				fixture.mu.Lock()
				fixture.failReadback = true
				fixture.mu.Unlock()
			}
		case "fetch_inbox":
			if cancelInbox != nil {
				cancelInbox()
			}
			if failInbox {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "error": map[string]interface{}{"code": -32000, "message": "gate not ready"}})
				return
			}
			result = []interface{}{}
		default:
			t.Errorf("unexpected fixture tool %q", name)
			http.Error(w, "unexpected tool", http.StatusBadRequest)
			return
		}
		if name == "file_reservation_paths" && replyMode == "lost_acquisition" || name == "release_file_reservations" && replyMode == "lost_release" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			t.Error(err)
		}
	}))
	return server, fixture
}
