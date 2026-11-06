package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Exercise the public executor and real Agent Mail JSON-RPC client. Only the
// br process boundary is supplied by the test; no step executor is replaced.
func TestForeachNativeFieldsRunUsesPerItemRequests(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(fmt.Sprintf("parallel=%t", parallel), func(t *testing.T) {
			server, requests := newForeachNativeMailFixture(t)
			defer server.Close()
			t.Setenv("AGENT_MAIL_URL", server.URL)
			t.Setenv("NTM_FOREACH_NATIVE_LITERAL", "${vars.secret}")

			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "native-fanout", Settings: DefaultWorkflowSettings(), Steps: []Step{{
				ID: "fanout", Foreach: &ForeachConfig{
					Items: `[{"id":"red","project":"/data/projects/red","agent":"TealCrane","recipient":"SageFern","status":"open","kind":"task","priority":"1"},{"id":"blue","project":"/data/projects/blue","agent":"YellowBluff","recipient":"CopperPeak","status":"closed","kind":"bug","priority":"2"}]`,
					As:    "task", Parallel: parallel, MaxConcurrent: 2,
					Steps: []Step{
						{ID: "query", BeadQuery: &BeadQueryStep{
							Label: StringOrList{"${task.id}", "${env.NTM_FOREACH_NATIVE_LITERAL}"},
							State: "${task.status}", Type: "${task.kind}", Priority: "${task.priority}", Assignee: "${task.agent}", Filter: "id==bd-${task.id}",
						}},
						{ID: "send", MailSend: &MailSendStep{
							ProjectKey: "${task.project}", AgentName: "${task.agent}", To: StringOrList{"${task.recipient}", "${vars.audit}"},
							Subject: "Work ${task.id}", Body: "${env.NTM_FOREACH_NATIVE_LITERAL} / ${task.id}", ThreadID: "bd-${task.id}", AckRequired: true,
						}},
						{ID: "reserve", FileReservationPaths: &FileReservationPathsStep{
							ProjectKey: "${task.project}", AgentName: "${task.agent}", Paths: StringOrList{"internal/${task.id}.go", "${task.id}/**"},
							Reason: "Prepare bd-${task.id}", TTLSeconds: 60, Exclusive: true,
						}},
						{ID: "inbox", MailInboxCheck: &MailInboxCheckStep{ProjectKey: "${task.project}", AgentName: "${task.agent}", UntilAckCount: 1}},
						{ID: "release", FileReservationRelease: &FileReservationReleaseStep{
							ProjectKey: "${task.project}", AgentName: "${task.agent}", Paths: StringOrList{"internal/${task.id}.go", "${task.id}/**"},
						}},
					},
				},
			}}}
			original := foreachNativeWorkflowJSON(t, workflow)
			var queryMu sync.Mutex
			var queryCalls [][]string
			cfg := foreachNativeConfig(t)
			cfg.BeadQueryRunBr = func(_ context.Context, args []string) ([]byte, error) {
				queryMu.Lock()
				queryCalls = append(queryCalls, append([]string(nil), args...))
				queryMu.Unlock()
				return []byte(`{"issues":[{"id":"bd-red"},{"id":"bd-blue"},{"id":"bd-unrelated"}]}`), nil
			}
			state, err := NewExecutor(cfg).Run(t.Context(), workflow, map[string]interface{}{"audit": "SilverMeadow", "secret": "must-not-expand"}, nil)
			if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("native fanout failed: state=%+v error=%v", state, err)
			}
			if got := foreachNativeWorkflowJSON(t, workflow); got != original {
				t.Fatal("foreach execution mutated the authored workflow")
			}

			wantQueries := [][]string{
				{"list", "--json", "--limit", "0", "--label", "red", "--label", "${vars.secret}", "--status", "open", "--type", "task", "--priority", "1", "--assignee", "TealCrane"},
				{"list", "--json", "--limit", "0", "--label", "blue", "--label", "${vars.secret}", "--status", "closed", "--type", "bug", "--priority", "2", "--assignee", "YellowBluff"},
			}
			queryMu.Lock()
			gotQueries := append([][]string(nil), queryCalls...)
			queryMu.Unlock()
			foreachNativeAssertCalls(t, gotQueries, wantQueries)
			gotRequests := requests()
			if len(gotRequests) != 8 {
				t.Fatalf("Agent Mail calls = %d, want 8: %+v", len(gotRequests), gotRequests)
			}
			for i, item := range []struct{ id, agent, recipient string }{{"red", "TealCrane", "SageFern"}, {"blue", "YellowBluff", "CopperPeak"}} {
				project := "/data/projects/" + item.id
				var calls []mailRuntimeFixtureRequest
				for _, call := range gotRequests {
					if call.args["project_key"] == project {
						calls = append(calls, call)
					}
				}
				if len(calls) != 4 {
					t.Fatalf("project %s calls = %+v, want send/reserve/inbox/release", project, calls)
				}
				for j, tool := range []string{"send_message", "file_reservation_paths", "fetch_inbox", "release_file_reservations"} {
					if calls[j].name != tool {
						t.Fatalf("project %s call %d = %s, want %s", project, j, calls[j].name, tool)
					}
				}
				send := calls[0].args
				if send["sender_name"] != item.agent || send["subject"] != "Work "+item.id || send["body_md"] != "${vars.secret} / "+item.id || send["thread_id"] != "bd-"+item.id || send["ack_required"] != true || !reflect.DeepEqual(send["to"], []interface{}{item.recipient, "SilverMeadow"}) {
					t.Fatalf("project %s substituted send = %#v", project, send)
				}
				reserve := calls[1].args
				if reserve["agent_name"] != item.agent || reserve["reason"] != "Prepare bd-"+item.id || reserve["ttl_seconds"] != float64(60) || reserve["exclusive"] != true || !reflect.DeepEqual(reserve["paths"], []interface{}{"internal/" + item.id + ".go", item.id + "/**"}) {
					t.Fatalf("project %s substituted reservation = %#v", project, reserve)
				}
				if calls[2].args["agent_name"] != item.agent {
					t.Fatalf("project %s inbox = %#v", project, calls[2].args)
				}
				release := calls[3].args
				record := state.ReservationSteps[fmt.Sprintf("fanout_iter%d_reserve", i)]
				if len(record.Granted) != 2 || record.Status != reservationReleased {
					t.Fatalf("project %s reservation was not released: %+v", project, record)
				}
				wantIDs := []interface{}{float64(record.Granted[0].ID), float64(record.Granted[1].ID)}
				if release["agent_name"] != item.agent || release["paths"] != nil || !reflect.DeepEqual(release["file_reservation_ids"], wantIDs) {
					t.Fatalf("project %s release = %#v, want captured IDs %#v", project, release, wantIDs)
				}
				var records []BeadRecord
				result := state.Steps[fmt.Sprintf("fanout_iter%d_query", i)]
				if err := json.Unmarshal([]byte(result.Output), &records); err != nil || len(records) != 1 || records[0].ID != "bd-"+item.id {
					t.Fatalf("per-item filter returned %q: records=%+v error=%v", result.Output, records, err)
				}
			}
		})
	}
}

func TestForeachNativeFieldsRunNestedBranches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		source  string
		queries [][]string
	}{
		{
			name: "outer alias and per-outer round counts",
			source: `
schema_version: "2.0"
name: native-nested-rounds
steps:
  - id: outer
    foreach:
      as: batch
      parallel: true
      max_concurrent: 2
      items: '[{"id":"A","rounds":2,"children":[{"id":"a"}]},{"id":"B","rounds":3,"children":[{"id":"b"}]}]'
      steps:
        - id: route
          branch: selected
          branches:
            selected:
              - id: inner
                foreach:
                  items: '${batch.children}'
                  max_rounds: '${batch.rounds}'
                  steps:
                    - id: query
                      bead_query:
                        label: ['${batch.id}', '${item.id}', '${loop.round}', '${rounds_remaining}']
                        status: open
`,
			queries: [][]string{
				{"list", "--json", "--limit", "0", "--label", "A", "--label", "a", "--label", "1", "--label", "1", "--status", "open"},
				{"list", "--json", "--limit", "0", "--label", "A", "--label", "a", "--label", "2", "--label", "0", "--status", "open"},
				{"list", "--json", "--limit", "0", "--label", "B", "--label", "b", "--label", "1", "--label", "2", "--status", "open"},
				{"list", "--json", "--limit", "0", "--label", "B", "--label", "b", "--label", "2", "--label", "1", "--status", "open"},
				{"list", "--json", "--limit", "0", "--label", "B", "--label", "b", "--label", "3", "--label", "0", "--status", "open"},
			},
		},
		{
			name: "inner default item shadows outer across a branch",
			source: `
schema_version: "2.0"
name: native-nested-shadow
steps:
  - id: outer
    foreach:
      items: '[{"id":"outer","children":[{"id":"inner-A"},{"id":"inner-B"}]}]'
      steps:
        - id: route
          branch: selected
          branches:
            selected:
              id: inner
              foreach:
                items: '${item.children}'
                steps:
                  - id: query
                    bead_query:
                      label: ['${item.id}']
`,
			queries: [][]string{
				{"list", "--json", "--limit", "0", "--label", "inner-A"},
				{"list", "--json", "--limit", "0", "--label", "inner-B"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflow, err := ParseString(tc.source, "yaml")
			if err != nil {
				t.Fatal(err)
			}
			if result := Validate(workflow); !result.Valid {
				t.Fatalf("invalid fixture: %+v", result.Errors)
			}
			original := foreachNativeWorkflowJSON(t, workflow)
			var mu sync.Mutex
			var calls [][]string
			cfg := foreachNativeConfig(t)
			cfg.BeadQueryRunBr = func(_ context.Context, args []string) ([]byte, error) {
				mu.Lock()
				calls = append(calls, append([]string(nil), args...))
				mu.Unlock()
				return []byte(`{"issues":[]}`), nil
			}
			state, err := NewExecutor(cfg).Run(t.Context(), workflow, nil, nil)
			if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("nested branch fanout failed: state=%+v error=%v", state, err)
			}
			mu.Lock()
			got := append([][]string(nil), calls...)
			mu.Unlock()
			foreachNativeAssertCalls(t, got, tc.queries)
			if got := foreachNativeWorkflowJSON(t, workflow); got != original {
				t.Fatal("nested branch materialization mutated the authored workflow")
			}
		})
	}
}

func TestForeachNativeFieldsRunOnFailure(t *testing.T) {
	cfg := foreachNativeConfig(t)
	for _, name := range []string{"alpha", "beta"} {
		if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "recover-"+name+".md"), []byte("Recover "+name+" <WORK>"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var calls [][]string
	cfg.BeadQueryRunBr = func(_ context.Context, args []string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return nil, errors.New("query service unavailable")
	}
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "native-fanout-recovery", Settings: DefaultWorkflowSettings(), Steps: []Step{{
		ID: "fanout", Foreach: &ForeachConfig{
			As: "task", Items: `[{"id":"alpha","pane":1},{"id":"beta","pane":2}]`, Steps: []Step{
				{ID: "query", BeadQuery: &BeadQueryStep{Label: StringOrList{"${task.id}"}}, OnFailure: OnFailureSpec{Fallback: map[string]interface{}{
					"pane": "${task.pane}", "template": "recover-${task.id}.md", "suppress_failure": true,
					"params": map[string]interface{}{"WORK": "${task.id}"},
				}}},
				{ID: "action", BeadQuery: &BeadQueryStep{Label: StringOrList{"action-${task.id}"}}, OnFailure: OnFailureSpec{Action: "fallback_${task.id}"}},
			},
		},
	}}}
	original := foreachNativeWorkflowJSON(t, workflow)
	client := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex}, tmux.Pane{ID: "%2", Index: 2, Type: tmux.AgentCodex})
	t.Cleanup(client.Reset)
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(client)
	state, err := executor.Run(t.Context(), workflow, nil, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("per-item failure recovery failed: state=%+v error=%v", state, err)
	}
	foreachNativeAssertCalls(t, calls, [][]string{
		{"list", "--json", "--limit", "0", "--label", "alpha"},
		{"list", "--json", "--limit", "0", "--label", "action-alpha"},
		{"list", "--json", "--limit", "0", "--label", "beta"},
		{"list", "--json", "--limit", "0", "--label", "action-beta"},
	})
	for i, name := range []string{"alpha", "beta"} {
		history, err := client.PasteHistory(fmt.Sprintf("%%%d", i+1))
		if err != nil || len(history) != 1 || history[0].Content != "Recover "+name+" "+name {
			t.Fatalf("recovery for %s: history=%+v error=%v", name, history, err)
		}
		id := fmt.Sprintf("fanout_iter%d_query.on_failure", i)
		if state.Steps[id].Status != StatusCompleted {
			t.Fatalf("recovery %s did not complete: %+v", id, state.Steps[id])
		}
		actionID := fmt.Sprintf("fanout_iter%d_action", i)
		if result := state.Steps[actionID]; result.Status != StatusSkipped || result.SkipKind != SkipKindOnFailureAction {
			t.Fatalf("per-item recovery action %s = %+v", actionID, result)
		}
		if got := state.Variables["runtime."+actionID+"_failure_action"]; got != "fallback_"+name {
			t.Fatalf("per-item failure action = %#v, want fallback_%s", got, name)
		}
	}
	if got := foreachNativeWorkflowJSON(t, workflow); got != original {
		t.Fatal("on_failure materialization mutated the authored workflow")
	}
}

func foreachNativeConfig(t *testing.T) ExecutorConfig {
	t.Helper()
	cfg := DefaultExecutorConfig("foreach-native")
	cfg.ProjectDir = t.TempDir()
	cfg.DefaultTimeout, cfg.GlobalTimeout = 3*time.Second, 15*time.Second
	return cfg
}

func foreachNativeWorkflowJSON(t *testing.T, workflow *Workflow) string {
	t.Helper()
	data, err := json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func foreachNativeAssertCalls(t *testing.T, got, want [][]string) {
	t.Helper()
	keys := func(calls [][]string) []string {
		out := make([]string, 0, len(calls))
		for _, call := range calls {
			data, _ := json.Marshal(call)
			out = append(out, string(data))
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(keys(got), keys(want)) {
		t.Fatalf("br invocations = %#v, want %#v", got, want)
	}
}

// Each project owns a separate live reservation ledger, as in Agent Mail.
// Captured requests are the externally observable effects under assertion.
func newForeachNativeMailFixture(t *testing.T) (*httptest.Server, func() []mailRuntimeFixtureRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []mailRuntimeFixtureRequest
	ledgers := make(map[string]*reservationLedger)
	nextID := 100
	ledgerFor := func(project string) *reservationLedger {
		mu.Lock()
		defer mu.Unlock()
		if ledgers[project] == nil {
			ledgers[project] = &reservationLedger{}
		}
		return ledgers[project]
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     interface{}            `json:"id"`
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		if r.Method != http.MethodPost {
			t.Errorf("Agent Mail HTTP method = %s", r.Method)
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			t.Errorf("decode Agent Mail request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if req.Method == "resources/read" {
			uri, _ := req.Params["uri"].(string)
			resource, _, _ := strings.Cut(uri, "?")
			project := strings.TrimPrefix(strings.TrimPrefix(resource, "resource://project/"), "resource://file_reservations/")
			project, err := url.PathUnescape(project)
			if err != nil {
				t.Errorf("decode resource key: %v", err)
				http.Error(w, "invalid project", http.StatusBadRequest)
				return
			}
			ledgerFor(project).serveReadback(t, w, req.ID, req.Method, req.Params)
			return
		}
		if req.Method != "tools/call" {
			t.Errorf("Agent Mail method = %s", req.Method)
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		name, _ := req.Params["name"].(string)
		args, _ := req.Params["arguments"].(map[string]interface{})
		mu.Lock()
		requests = append(requests, mailRuntimeFixtureRequest{name: name, args: args})
		nextID += 10
		id := nextID
		mu.Unlock()
		project, _ := args["project_key"].(string)
		var result interface{}
		switch name {
		case "send_message":
			result = map[string]interface{}{"count": 1, "deliveries": []interface{}{map[string]interface{}{
				"project": project, "payload": map[string]interface{}{"id": id, "from": args["sender_name"], "to": args["to"], "subject": args["subject"], "body_md": args["body_md"], "thread_id": args["thread_id"], "created_ts": reservationLedgerCreated},
			}}}
		case "file_reservation_paths":
			var grants []map[string]interface{}
			for i, path := range args["paths"].([]interface{}) {
				grants = append(grants, ledgerFor(project).grant(id+i, path.(string), args))
			}
			result = map[string]interface{}{"granted": grants, "conflicts": []interface{}{}}
		case "fetch_inbox":
			result = []interface{}{map[string]interface{}{"id": id, "from": "SilverMeadow", "subject": "ack requested", "ack_required": true, "created_ts": reservationLedgerCreated, "kind": "message"}}
		case "release_file_reservations":
			result = map[string]interface{}{"released": ledgerFor(project).release(args), "released_at": reservationLedgerCreated}
		default:
			t.Errorf("unexpected Agent Mail tool %s", name)
			http.Error(w, "unexpected tool", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			t.Errorf("encode Agent Mail response: %v", err)
		}
	}))
	return server, func() []mailRuntimeFixtureRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]mailRuntimeFixtureRequest(nil), requests...)
	}
}
