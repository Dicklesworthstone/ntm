package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Use the spawn recovery entry point, a real on-disk handoff, and the real
// Agent Mail HTTP client. An empty successful acquisition reply must not be
// promoted into recovered file coverage or trigger further remote mutations.
func TestSpawnRecoveryTransferRejectsEmptyGrantReceipt(t *testing.T) {
	project := t.TempDir()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	h := handoff.New("recover").WithGoalAndNow("Preserve active work", "Continue the previous task")
	h.ReservationTransfer = &handoff.ReservationTransfer{
		FromAgent: "old", ProjectKey: project,
		Reservations: []handoff.ReservationSnapshot{
			{ID: 1001, ProjectID: 73, AgentName: "old", CreatedAt: created, ExpiresAt: expires, PathPattern: "a.go", Exclusive: true},
			{ID: 1002, ProjectID: 73, AgentName: "old", CreatedAt: created, ExpiresAt: expires, PathPattern: "b.go", Exclusive: true},
		},
	}
	data, err := yaml.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(project, ".ntm", "handoffs", "recover")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20260923-transfer.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var calls []string
	var reads int
	sourceReleased := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				URI       string         `json:"uri"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Agent Mail request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		if request.Method == "resources/read" {
			reads++
		} else {
			calls = append(calls, request.Params.Name)
		}
		defer mu.Unlock()
		var result any
		switch {
		case request.Method == "resources/read" && strings.HasPrefix(request.Params.URI, "resource://file_reservations/"):
			result = map[string]any{"contents": []map[string]any{{"text": `[{"id":1001,"project_id":73,"agent_name":"old","path_pattern":"a.go","exclusive":true,"created_ts":"2026-01-01T00:00:00Z","expires_ts":"2099-01-01T00:00:00Z"},{"id":1002,"project_id":73,"agent_name":"old","path_pattern":"b.go","exclusive":true,"created_ts":"2026-01-01T00:00:00Z","expires_ts":"2099-01-01T00:00:00Z"}]`}}}
			if sourceReleased {
				result = map[string]any{"contents": []map[string]any{{"text": "[]"}}}
			}
		case request.Method == "tools/call" && request.Params.Name == "release_file_reservations":
			if request.Params.Arguments["agent_name"] != "old" {
				t.Errorf("untrusted grant reply authorized a cleanup: %+v", request.Params.Arguments)
			}
			if _, hasPaths := request.Params.Arguments["paths"]; hasPaths || !reflect.DeepEqual(request.Params.Arguments["file_reservation_ids"], []any{float64(1001), float64(1002)}) {
				t.Errorf("spawn recovery lost captured source IDs: %+v", request.Params.Arguments)
			}
			sourceReleased = true
			result = map[string]any{"released": 2}
		case request.Method == "tools/call" && request.Params.Name == "file_reservation_paths":
			result = map[string]any{"granted": []any{}, "conflicts": []any{}}
		default:
			t.Errorf("unexpected recovery call: %s %s", request.Method, request.Params.Name)
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			t.Errorf("encode Agent Mail response: %v", err)
		}
	}))
	defer server.Close()
	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL), agentmail.WithToken(""))
	result, err := attemptReservationTransfer(context.Background(), client, "recover", "new", project)
	if !errors.Is(err, handoff.ErrTransferGrantEvidence) || result == nil || result.Success || result.RolledBack || !result.OutcomeUnknown {
		t.Fatalf("spawn accepted incomplete reservation coverage: result=%+v error=%v", result, err)
	}
	if !reflect.DeepEqual(result.ReleasedPaths, []string{"a.go", "b.go"}) || result.Attempts != 1 {
		t.Fatalf("spawn lost partial recovery evidence: %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(calls, []string{"release_file_reservations", "file_reservation_paths"}) {
		t.Fatalf("unexpected recovery mutations: %v", calls)
	}
	if reads != 2 {
		t.Fatalf("expected independent source preflight and release readback, got %d reads", reads)
	}
}

func TestSpawnRecoveryTransferRefusesStaleSourceWithoutChangingHandoff(t *testing.T) {
	for _, mode := range []string{"legacy", "replacement"} {
		for _, target := range []string{"new", "old"} {
			t.Run(mode+"/"+target, func(t *testing.T) {
				project := t.TempDir()
				lease := handoff.ReservationSnapshot{PathPattern: "a.go", Exclusive: true}
				if mode == "replacement" {
					lease.ID, lease.ProjectID, lease.AgentName = 1001, 73, "old"
					lease.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
					lease.ExpiresAt = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				h := handoff.New("recover").WithGoalAndNow("Preserve active work", "Continue the previous task")
				h.ReservationTransfer = &handoff.ReservationTransfer{FromAgent: "old", ProjectKey: project, Reservations: []handoff.ReservationSnapshot{lease}}
				path, err := handoff.NewWriter(project).Write(h, "source-identity")
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var mu sync.Mutex
				reads, mutations := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var rpc struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
						Params struct {
							URI string `json:"uri"`
						} `json:"params"`
					}
					if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
						t.Errorf("decode recovery preflight: %v", err)
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					var reply any = map[string]any{}
					mu.Lock()
					if rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://file_reservations/") {
						reads++
						reply = map[string]any{"contents": []map[string]any{{"text": `[{"id":2001,"project_id":73,"agent_name":"old","path_pattern":"a.go","exclusive":true,"created_ts":"2026-01-01T00:01:00Z","expires_ts":"2099-01-01T00:00:00Z"}]`}}}
					} else {
						mutations++
						t.Errorf("stale handoff authorized remote call: %s %s", rpc.Method, rpc.Params.URI)
					}
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": reply}); err != nil {
						t.Errorf("encode recovery preflight: %v", err)
					}
				}))
				defer server.Close()
				client := agentmail.NewClient(agentmail.WithBaseURL(server.URL), agentmail.WithToken(""))
				result, err := attemptReservationTransfer(context.Background(), client, "recover", target, project)
				if !errors.Is(err, handoff.ErrTransferSourceEvidence) || result == nil || result.Success || result.RolledBack || result.OutcomeUnknown || result.Attempts != 0 {
					t.Fatalf("recovery accepted or misclassified stale ownership: %+v %v", result, err)
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || string(before) != string(after) {
					t.Fatalf("refused recovery rewrote its handoff evidence: %v", readErr)
				}
				mu.Lock()
				defer mu.Unlock()
				wantReads := 1
				if mode == "legacy" {
					wantReads = 0
				}
				if mutations != 0 || reads != wantReads {
					t.Fatalf("unexpected recovery I/O: reads=%d mutations=%d", reads, mutations)
				}
			})
		}
	}
}

func TestSpawnRecoveryTransferRequiresObservedSourceRelease(t *testing.T) {
	for _, mode := range []string{"exclusive retained", "shared retained", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("NTM_CONFIG", "")
			project := t.TempDir()
			created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			expires := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
			source := agentmail.FileReservation{
				ID: 1001, ProjectID: 73, AgentName: "old", PathPattern: "a.go",
				Exclusive: mode != "shared retained", Reason: "original work",
				CreatedTS: agentmail.FlexTime{Time: created}, ExpiresTS: agentmail.FlexTime{Time: expires},
			}
			h := handoff.New("recover").WithGoalAndNow("Preserve active work", "Continue the previous task")
			h.ReservationTransfer = &handoff.ReservationTransfer{
				FromAgent: "old", ProjectKey: project,
				Reservations: []handoff.ReservationSnapshot{{
					ID: source.ID, ProjectID: source.ProjectID, AgentName: source.AgentName,
					PathPattern: source.PathPattern, Exclusive: source.Exclusive, Reason: source.Reason,
					CreatedAt: created, ExpiresAt: expires,
				}},
			}
			path, err := handoff.NewWriter(project).Write(h, "release-readback")
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			var mu sync.Mutex
			var mutations []string
			reads := 0
			releaseAcknowledged := false
			live := []agentmail.FileReservation{source}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var rpc struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						Name      string         `json:"name"`
						URI       string         `json:"uri"`
						Arguments map[string]any `json:"arguments"`
					} `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
					t.Errorf("decode recovery request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				var reply any
				switch {
				case rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://file_reservations/"):
					reads++
					payload, err := json.Marshal(live)
					if err != nil {
						t.Errorf("encode live reservations: %v", err)
					}
					if releaseAcknowledged && mode == "unreadable" {
						payload = []byte("null")
					}
					reply = map[string]any{"contents": []map[string]any{{"text": string(payload)}}}
				case rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://project/"):
					payload, err := json.Marshal(map[string]any{"id": 73, "slug": "project", "human_key": project})
					if err != nil {
						t.Errorf("encode project: %v", err)
					}
					reply = map[string]any{"contents": []map[string]any{{"text": string(payload)}}}
				case rpc.Method == "tools/call" && rpc.Params.Name == "release_file_reservations":
					args := rpc.Params.Arguments
					owner, _ := args["agent_name"].(string)
					mutations = append(mutations, "release:"+owner)
					if owner != "old" || args["project_key"] != project {
						t.Errorf("source mutation changed authority: %+v", args)
					}
					if _, paths := args["paths"]; paths || !reflect.DeepEqual(args["file_reservation_ids"], []any{float64(1001)}) {
						t.Errorf("source release broadened captured selectors: %+v", args)
					}
					// A successful count deliberately does not remove the source.
					releaseAcknowledged = true
					reply = map[string]any{"released": 1}
				case rpc.Method == "tools/call" && rpc.Params.Name == "file_reservation_paths":
					owner, _ := rpc.Params.Arguments["agent_name"].(string)
					mutations = append(mutations, "reserve:"+owner)
					grant := source
					grant.ID = 2001
					grant.AgentName = owner
					grant.Reason = "handoff transfer from old"
					live = append(live, grant)
					reply = &agentmail.ReservationResult{Granted: []agentmail.FileReservation{grant}}
				default:
					t.Errorf("unexpected recovery request: %s %s %s", rpc.Method, rpc.Params.Name, rpc.Params.URI)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": reply}); err != nil {
					t.Errorf("encode recovery response: %v", err)
				}
			}))
			defer server.Close()
			client := agentmail.NewClient(agentmail.WithBaseURL(server.URL), agentmail.WithToken(""))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := attemptReservationTransfer(ctx, client, "recover", "new", project)
			if !errors.Is(err, handoff.ErrTransferPostcondition) || result == nil || result.Success || result.RolledBack ||
				!result.OutcomeUnknown || result.Stage != "verify_release" || result.Attempts != 0 {
				t.Fatalf("spawn promoted an unverified release: %+v %v", result, err)
			}
			if !reflect.DeepEqual(result.RequestedIDs, []int{1001}) || len(result.ReleasedIDs) != 0 ||
				len(result.ReleasedPaths) != 0 || len(result.GrantedIDs) != 0 {
				t.Fatalf("spawn lost attempted scope or claimed confirmed coverage: %+v", result)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("uncertain recovery rewrote its handoff evidence: %v", readErr)
			}
			recovery := &RecoveryContext{ReservationTransfer: result}
			if summary := generateRecoverySummary(recovery); !strings.Contains(summary, "outcome unknown") {
				t.Fatalf("recovery summary hid the uncertain transfer: %q", summary)
			}
			if prompt := FormatRecoveryPrompt(recovery, AgentTypeClaude); !strings.Contains(prompt, "inspect live reservations before retrying") {
				t.Fatalf("recovery prompt omitted its only recovery item: %q", prompt)
			}
			mu.Lock()
			defer mu.Unlock()
			if reads != 2 || !reflect.DeepEqual(mutations, []string{"release:old"}) || !reflect.DeepEqual(live, []agentmail.FileReservation{source}) {
				t.Fatalf("unverified release authorized another mutation: reads=%d mutations=%v live=%+v", reads, mutations, live)
			}
		})
	}
}

func TestRecoveryPromptKeepsTransferUncertaintyAheadOfCompensation(t *testing.T) {
	for _, mode := range []string{"conflict", "restored source"} {
		t.Run(mode, func(t *testing.T) {
			result := &handoff.ReservationTransferResult{
				OutcomeUnknown: true, Error: "remote outcome could not be confirmed",
				Conflicts:  []agentmail.ReservationConflict{{Path: "a.go", Holders: []string{"peer"}}},
				RolledBack: mode == "restored source",
			}
			recovery := &RecoveryContext{ReservationTransfer: result}
			if summary := generateRecoverySummary(recovery); !strings.Contains(summary, "outcome unknown") {
				t.Fatalf("conflict or compensation hid uncertainty: %q", summary)
			}
			prompt := FormatRecoveryPrompt(recovery, AgentTypeClaude)
			if !strings.Contains(prompt, "outcome unknown") || !strings.Contains(prompt, "assuming file ownership") {
				t.Fatalf("injected recovery omitted the ownership warning: %q", prompt)
			}
		})
	}
}

func TestBuildRecoveryContextPreservesTimedOutTransferReceipt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("AGENT_MAIL_TOKEN", "test-recovery-token")
	oldCfg, oldJSON, oldClient := cfg, jsonOutput, tmux.DefaultClient
	t.Cleanup(func() {
		cfg, jsonOutput, tmux.DefaultClient = oldCfg, oldJSON, oldClient
	})
	cfg = config.Default()
	jsonOutput = true
	tmux.DefaultClient = tmux.NewClient("")

	project := t.TempDir()
	const session = "transfer-timeout"
	saveSessionAgentForTest(t, session, project, "new")
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	h := handoff.New(session).WithGoalAndNow("Preserve active work", "Inspect interrupted reservation recovery")
	h.ReservationTransfer = &handoff.ReservationTransfer{
		FromAgent: "old", ProjectKey: project,
		Reservations: []handoff.ReservationSnapshot{{
			ID: 1001, ProjectID: 73, AgentName: "old", PathPattern: "a.go", Exclusive: true,
			CreatedAt: created, ExpiresAt: expires,
		}},
	}
	path, err := handoff.NewWriter(project).Write(h, "transfer-timeout")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	live := map[int]agentmail.FileReservation{
		1001: {ID: 1001, ProjectID: 73, AgentName: "old", PathPattern: "a.go", Exclusive: true,
			CreatedTS: agentmail.FlexTime{Time: created}, ExpiresTS: agentmail.FlexTime{Time: expires}},
	}
	var mu sync.Mutex
	var effects []string
	reads := 0
	unblock := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var rpc struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				URI       string         `json:"uri"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			t.Errorf("decode recovery request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var reply any
		switch {
		case rpc.Method == "tools/call" && rpc.Params.Name == "ensure_project":
			reply = map[string]any{"id": 73, "slug": "project", "human_key": project}
		case rpc.Method == "tools/call" && rpc.Params.Name == "fetch_inbox":
			if rpc.Params.Arguments["agent_name"] != "new" {
				t.Errorf("recovery did not resolve the saved target identity: %+v", rpc.Params.Arguments)
			}
			reply = []any{}
		case rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://file_reservations/"):
			mu.Lock()
			reads++
			rows := make([]agentmail.FileReservation, 0, len(live))
			for _, row := range live {
				rows = append(rows, row)
			}
			mu.Unlock()
			data, err := json.Marshal(rows)
			if err != nil {
				t.Errorf("encode source reservations: %v", err)
				http.Error(w, "encoding failed", http.StatusInternalServerError)
				return
			}
			reply = map[string]any{"contents": []map[string]any{{"text": string(data)}}}
		case rpc.Method == "tools/call" && rpc.Params.Name == "release_file_reservations":
			args := rpc.Params.Arguments
			if _, hasPaths := args["paths"]; hasPaths || args["agent_name"] != "old" || args["project_key"] != project ||
				!reflect.DeepEqual(args["file_reservation_ids"], []any{float64(1001)}) {
				t.Errorf("release lost its captured source identity: %+v", args)
			}
			mu.Lock()
			effects = append(effects, "release:old")
			delete(live, 1001)
			mu.Unlock()
			// The server applied the mutation, but the caller cannot observe
			// an acknowledgement before its real request deadline expires.
			select {
			case <-r.Context().Done():
			case <-unblock:
			}
			return
		default:
			t.Errorf("unexpected recovery request: %s %s %s", rpc.Method, rpc.Params.Name, rpc.Params.URI)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": reply}); err != nil {
			t.Errorf("encode recovery response: %v", err)
		}
	}))
	defer func() {
		close(unblock)
		server.Close()
	}()
	t.Setenv("AGENT_MAIL_URL", server.URL)
	cfg.AgentMail.URL = server.URL
	settings := config.SessionRecoveryConfig{
		Enabled: true, IncludeAgentMail: true, MaxRecoveryTokens: 3000, TimeoutSeconds: 2,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rc, err := buildRecoveryContext(ctx, session, project, settings)
	if err != nil || rc == nil || rc.Error == nil || rc.Error.Code != "PARTIAL_RECOVERY" || !rc.Error.Recoverable || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("mail deadline did not remain partial recovery: rc=%+v err=%v context=%v", rc, err, ctx.Err())
	}
	transfer := rc.ReservationTransfer
	if transfer == nil || transfer.Success || !transfer.OutcomeUnknown || transfer.RolledBack || transfer.Stage != "release" || transfer.Attempts != 0 ||
		!reflect.DeepEqual(transfer.RequestedIDs, []int{1001}) || len(transfer.ReleasedIDs) != 0 || len(transfer.GrantedIDs) != 0 ||
		!strings.Contains(transfer.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("partial recovery discarded or overstated the timed-out mutation: %+v", transfer)
	}
	if details := strings.Join(rc.Error.Details, "\n"); !strings.Contains(details, "agent mail") || !strings.Contains(details, "timed out") {
		t.Fatalf("partial recovery lost the timed-out source: %+v", rc.Error)
	}
	if !strings.Contains(strings.ToLower(rc.Summary), "outcome unknown") || !strings.Contains(strings.ToLower(FormatRecoveryPrompt(rc, AgentTypeClaude)), "outcome unknown") {
		t.Fatalf("retained uncertainty did not reach the summary and agent prompt: summary=%q prompt=%q", rc.Summary, FormatRecoveryPrompt(rc, AgentTypeClaude))
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("timed-out transfer rewrote its captured handoff: %v", readErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(live) != 0 || reads != 1 || !reflect.DeepEqual(effects, []string{"release:old"}) {
		t.Fatalf("fixture effect was not observed or unknown outcome authorized more work: live=%v reads=%d effects=%v", live, reads, effects)
	}
}
