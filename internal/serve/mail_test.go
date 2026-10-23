package serve

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestMailRequestProjectIsolation(t *testing.T) {
	projectA, err := ResolveProjectDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := ResolveProjectDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{ProjectDir: projectA})
	defer srv.Stop()
	var mu sync.Mutex
	sends := map[string]int{}
	srv.mailClient = newMockAgentMailMCPClient(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"send_message": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			project, _ := args["project_key"].(string)
			mu.Lock()
			sends[project]++
			mu.Unlock()
			return agentmail.SendResult{Count: 1, Deliveries: []agentmail.MessageDelivery{{Project: project, Payload: &agentmail.Message{ID: 7}}}}, nil
		},
		"fetch_inbox": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			project, _ := args["project_key"].(string)
			return []agentmail.InboxMessage{{ID: 7, From: "BlueLake", Subject: project}}, nil
		},
	})
	// A cached transport's original default must not replace an explicit key.
	srv.mailClient.SetProjectKey("/different-cached-project")

	var wg sync.WaitGroup
	for _, project := range []string{projectA, projectB} {
		for i := 0; i < 3; i++ {
			wg.Go(func() {
				path := "/api/v1/mail/messages/?project=" + url.QueryEscape(project)
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"sender_name":"BlueLake","to":["RedStone"],"subject":"task","body_md":"work"}`))
				req.Header.Set("Idempotency-Key", "shared-retry-key")
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, req)
				var sent struct {
					Deliveries []agentmail.MessageDelivery `json:"deliveries"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &sent); err != nil || rec.Code != http.StatusCreated || len(sent.Deliveries) != 1 || sent.Deliveries[0].Project != project {
					t.Errorf("send for %q returned %d %s: %v", project, rec.Code, rec.Body, err)
				}

				rec = httptest.NewRecorder()
				path = "/api/v1/mail/inbox?agent_name=BlueLake&project=" + url.QueryEscape(project)
				srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				var inbox struct {
					Messages []agentmail.InboxMessage `json:"messages"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &inbox); err != nil || rec.Code != http.StatusOK || len(inbox.Messages) != 1 || inbox.Messages[0].Subject != project {
					t.Errorf("inbox for %q returned %d %s: %v", project, rec.Code, rec.Body, err)
				}
			})
		}
	}
	wg.Wait()
	if len(sends) != 2 || sends[projectA] != 1 || sends[projectB] != 1 {
		t.Fatalf("cross-project retries executed unexpected sends: %v", sends)
	}
	if got := srv.projectDirSnapshot(); got != projectA {
		t.Fatalf("request overrides changed the server project to %q", got)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox?agent_name=BlueLake", nil))
	var inbox struct {
		Messages []agentmail.InboxMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &inbox); err != nil || rec.Code != http.StatusOK || len(inbox.Messages) != 1 || inbox.Messages[0].Subject != projectA {
		t.Fatalf("unqualified inbox lost the default project: %d %s, %v", rec.Code, rec.Body, err)
	}
}

func TestMailRequestProjectIdempotencySurvivesDefaultChanges(t *testing.T) {
	projectA, err := ResolveProjectDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := ResolveProjectDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{ProjectDir: projectA})
	defer srv.Stop()
	var calls atomic.Int32
	client := newMockAgentMailMCPClient(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"send_message": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			calls.Add(1)
			return agentmail.SendResult{Count: 1, Deliveries: []agentmail.MessageDelivery{{Project: args["project_key"].(string)}}}, nil
		},
	})
	srv.mailClient = client
	post := func(path, wantProject string, wantReplay bool) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"sender_name":"BlueLake","to":["RedStone"],"subject":"task","body_md":"work"}`))
		req.Header.Set("Idempotency-Key", "same-key")
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		var result agentmail.SendResult
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != http.StatusCreated || len(result.Deliveries) != 1 || result.Deliveries[0].Project != wantProject {
			t.Fatalf("send selected wrong project: %d %s, %v", rec.Code, rec.Body, err)
		}
		if replay := rec.Header().Get("X-Idempotent-Replay") == "true"; replay != wantReplay {
			t.Fatalf("send replay=%v, want %v", replay, wantReplay)
		}
	}
	patch := func(project string) {
		t.Helper()
		body, err := json.Marshal(map[string]string{"project_dir": project})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/v1/config", bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("config patch failed: %d %s", rec.Code, rec.Body)
		}
		// Keep the same upstream fixture after the real PATCH invalidates its
		// cached client. The request resolver and replay middleware stay real.
		srv.mailClient = client
	}
	defaultPath := "/api/v1/mail/messages/"
	explicitPath := defaultPath + "?project=" + url.QueryEscape(projectB)
	post(defaultPath, projectA, false)
	post(explicitPath, projectB, false)
	patch(projectB)
	post(defaultPath, projectB, false)
	post(defaultPath, projectB, true)
	post(explicitPath, projectB, true)
	patch(projectA)
	post(defaultPath, projectA, true)
	post(explicitPath, projectB, true)
	if got := calls.Load(); got != 3 {
		t.Fatalf("sent %d upstream messages; want exactly three distinct scoped requests", got)
	}
}

func TestMailRequestProjectFrozenDuringConfigPatch(t *testing.T) {
	projectA, err := ResolveProjectDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := ResolveProjectDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{ProjectDir: projectA})
	defer srv.Stop()
	entered := make(chan string, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	srv.mailClient = newMockAgentMailMCPClient(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"acknowledge_message": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			entered <- args["project_key"].(string)
			<-release
			return agentmail.MessageAckResult{MessageID: 7, Acknowledged: true}, nil
		},
	})
	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/7/ack?agent_name=BlueLake", nil))
		completed <- rec
	}()
	select {
	case project := <-entered:
		if project != projectA {
			t.Fatalf("acknowledgement reached the wrong project: %q", project)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acknowledgement did not reach Agent Mail")
	}
	body, err := json.Marshal(map[string]string{"project_dir": projectB})
	if err != nil {
		t.Fatal(err)
	}
	patch := httptest.NewRecorder()
	srv.Router().ServeHTTP(patch, httptest.NewRequest(http.MethodPatch, "/api/v1/config", bytes.NewReader(body)))
	if patch.Code != http.StatusOK {
		t.Fatalf("concurrent config patch failed: %d %s", patch.Code, patch.Body)
	}
	unblock()
	select {
	case rec := <-completed:
		if rec.Code != http.StatusOK {
			t.Fatalf("acknowledgement failed after config patch: %d %s", rec.Code, rec.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acknowledgement did not complete")
	}
	select {
	case event := <-srv.wsHub.broadcast:
		payload, ok := event.Data.(map[string]interface{})
		if !ok || event.EventType != "mail.acknowledged" || payload["project_key"] != projectA {
			t.Fatalf("acknowledgement event lost its original project: %+v", event)
		}
	default:
		t.Fatal("acknowledgement did not publish its project-scoped event")
	}
	if got := srv.projectDirSnapshot(); got != projectB {
		t.Fatalf("completed mail request overwrote the concurrent configuration: %q", got)
	}
}

func TestMailRequestProjectPermissionBeforeValidation(t *testing.T) {
	srv := New(Config{})
	defer srv.Stop()
	router := chi.NewRouter()
	router.Use(srv.mailProjectScopeMiddleware)
	router.Use(srv.idempotencyMiddleware)
	router.Route("/api/v1", srv.registerMailRoutes)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/?project=relative", strings.NewReader(`{}`))
	req = req.WithContext(withRoleContext(req.Context(), &RoleContext{Role: RoleViewer, UserID: "reader"}))
	req.Header.Set("Idempotency-Key", "invalid-scope")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("project validation bypassed route permission: %d %s", rec.Code, rec.Body)
	}
}

func TestMailRequestProjectValidation(t *testing.T) {
	project := t.TempDir()
	srv := New(Config{ProjectDir: project})
	defer srv.Stop()
	var calls atomic.Int32
	srv.mailClient = newMockAgentMailMCPClient(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"ensure_project": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			calls.Add(1)
			return agentmail.Project{ID: 1, HumanKey: args["human_key"].(string)}, nil
		},
	})
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("not a project directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{
		"empty":     "project=",
		"relative":  "project=relative/project",
		"missing":   "project=" + url.QueryEscape(filepath.Join(project, "missing")),
		"file":      "project=" + url.QueryEscape(file),
		"duplicate": "project=" + url.QueryEscape(project) + "&project=" + url.QueryEscape(project),
		"malformed": "project=%zz",
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/mail/projects?"+query, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("invalid project returned %d %s", rec.Code, rec.Body)
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("invalid selectors reached Agent Mail %d times", got)
	}
	alias := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(project, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/mail/projects?project="+url.QueryEscape(alias), nil))
	var response struct {
		Projects []agentmail.Project `json:"projects"`
	}
	want, err := ResolveProjectDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || len(response.Projects) != 1 || response.Projects[0].HumanKey != want {
		t.Fatalf("symlink selector did not canonicalize: %d %s, %v", rec.Code, rec.Body, err)
	}
}

// bd-kar03: redaction must not depend on which read path a caller uses.
// sanitizeInboxMessage/sanitizeMessage routed subject+body through
// redaction.ScanAndRedact, but thread-summary, reply, and search returned raw
// content under the SAME mail:read permission — so one secret came back
// REDACTED from GET /mail/inbox?include_bodies=true and VERBATIM from
// GET /mail/threads/{id}/summary?include_examples=true.
func TestMailReadPathsRedactUniformly(t *testing.T) {
	const secret = "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	// Establish that the shared redactor actually removes this token, so the
	// assertions below are meaningful rather than vacuous.
	redactedProbe, disclosure := adapters.NormalizeDisclosureText("key " + secret)
	if strings.Contains(redactedProbe, secret) || disclosure == nil || disclosure.DisclosureState != "redacted" {
		t.Skipf("shared redactor does not treat the probe token as a secret (state=%v); nothing to assert", disclosure)
	}

	t.Run("thread summary examples", func(t *testing.T) {
		examples := sanitizeInboxMessages([]agentmail.InboxMessage{{
			ID:      1,
			Subject: "leak " + secret,
			BodyMD:  "body " + secret,
		}}, true)
		if len(examples) != 1 {
			t.Fatalf("got %d examples, want 1", len(examples))
		}
		if strings.Contains(examples[0].Subject, secret) {
			t.Errorf("thread-summary example subject leaked the secret: %q", examples[0].Subject)
		}
		if examples[0].BodyMD != nil && strings.Contains(*examples[0].BodyMD, secret) {
			t.Errorf("thread-summary example body leaked the secret: %q", *examples[0].BodyMD)
		}
	})

	t.Run("thread summary text", func(t *testing.T) {
		safe := sanitizeThreadSummary(agentmail.ThreadSummary{
			ThreadID:     "br-1",
			Participants: []string{"BlueLake"},
			KeyPoints:    []string{"the key is " + secret},
			ActionItems:  []string{"rotate " + secret},
		})
		for _, line := range append(append([]string{}, safe.KeyPoints...), safe.ActionItems...) {
			if strings.Contains(line, secret) {
				t.Errorf("thread summary text leaked the secret: %q", line)
			}
		}
		if safe.ThreadID != "br-1" || len(safe.Participants) != 1 {
			t.Errorf("sanitization dropped non-sensitive fields: %+v", safe)
		}
	})

	t.Run("search results", func(t *testing.T) {
		safe := sanitizeSearchResults([]agentmail.SearchResult{{
			ID:      7,
			Subject: "found " + secret,
			From:    "BlueLake",
		}})
		if len(safe) != 1 {
			t.Fatalf("got %d results, want 1", len(safe))
		}
		if strings.Contains(safe[0].Subject, secret) {
			t.Errorf("search result subject leaked the secret: %q", safe[0].Subject)
		}
		if safe[0].ID != 7 || safe[0].From != "BlueLake" {
			t.Errorf("sanitization dropped non-sensitive fields: %+v", safe[0])
		}
	})

	t.Run("reply message", func(t *testing.T) {
		safe := sanitizeMessage(&agentmail.Message{
			ID:      9,
			Subject: "re: " + secret,
			BodyMD:  "body " + secret,
		})
		if safe == nil {
			t.Fatal("sanitizeMessage returned nil")
		}
		if strings.Contains(safe.Subject, secret) {
			t.Errorf("reply subject leaked the secret: %q", safe.Subject)
		}
		if safe.BodyMD != nil && strings.Contains(*safe.BodyMD, secret) {
			t.Errorf("reply body leaked the secret: %q", *safe.BodyMD)
		}
	})
}

type forceReleaseHTTPFixture struct {
	server      *Server
	store       *state.Store
	router      http.Handler
	calls       atomic.Int32
	inspections atomic.Int32
	malformed   atomic.Bool
}

func newForceReleaseHTTPFixture(t *testing.T) *forceReleaseHTTPFixture {
	t.Helper()
	setForceReleasePolicy(t, "approval")
	server, store := setupTestServer(t)
	fixture := &forceReleaseHTTPFixture{server: server, store: store}
	server.mailClient = newMockAgentMailMCPClient(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"list_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			fixture.inspections.Add(1)
			return []agentmail.FileReservation{{
				ID: 17, ProjectID: 4, AgentName: "BlueLake", PathPattern: "internal/serve/*",
				CreatedTS: agentmail.FlexTime{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
				ExpiresTS: agentmail.FlexTime{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
				Exclusive: true,
			}}, nil
		},
		"force_release_file_reservation": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			fixture.calls.Add(1)
			if args["project_key"] != agentmail.CanonicalProjectKey(server.projectDirSnapshot()) ||
				args["agent_name"] != "RedStone" || args["file_reservation_id"] != float64(17) {
				t.Errorf("release changed approved project/actor/reservation: %+v", args)
			}
			if fixture.malformed.Load() {
				// The server may have acted, but its receipt cannot be
				// decoded. A retry must not repeat this mutation.
				return map[string]interface{}{"success": "receipt lost"}, nil
			}
			return agentmail.ForceReleaseResult{
				Success: true, PreviousHolder: "BlueLake", PathPattern: "internal/serve/*",
			}, nil
		},
	})
	router := chi.NewRouter()
	router.Use(server.idempotencyMiddleware)
	router.With(server.RequirePermission(PermForceRelease)).Post("/api/v1/reservations/{id}/force-release", server.handleForceReleaseReservation)
	router.With(server.RequirePermission(PermApproveRequests)).Post("/api/v1/approvals/{id}/approve", server.handleApprovalApproveV1)
	router.With(server.RequirePermission(PermApproveRequests)).Post("/api/v1/approvals/{id}/deny", server.handleApprovalDenyV1)
	fixture.router = router
	return fixture
}

func (fixture *forceReleaseHTTPFixture) post(t *testing.T, actor, path, key string, body interface{}) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(http.MethodPost, path, reader)
	// Authentication's validated RBAC context is the only identity input.
	req = req.WithContext(withRoleContext(req.Context(), &RoleContext{Role: RoleAdmin, UserID: actor}))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	fixture.router.ServeHTTP(rec, req)
	var decoded map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode HTTP response: %v: %s", err, rec.Body.String())
	}
	return rec, decoded
}

func forceReleaseResponseApprovalID(t *testing.T, body map[string]interface{}) string {
	t.Helper()
	id, _ := body["approval_id"].(string)
	if id == "" {
		if details, ok := body["details"].(map[string]interface{}); ok {
			id, _ = details["approval_id"].(string)
		}
	}
	if id == "" {
		t.Fatalf("approval ID missing: %+v", body)
	}
	return id
}

func (fixture *forceReleaseHTTPFixture) approve(t *testing.T, id string) {
	t.Helper()
	rec, _ := fixture.post(t, "bob", "/api/v1/approvals/"+id+"/approve", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second-operator approval: %d: %s", rec.Code, rec.Body.String())
	}
}

func TestForceReleaseHTTPApprovalLifecycleAndIdempotentReplay(t *testing.T) {
	fixture := newForceReleaseHTTPFixture(t)
	const path = "/api/v1/reservations/17/force-release"
	body := ForceReleaseRequest{AgentName: "RedStone", Note: "holder crashed", NotifyPrevious: true}
	rec, decoded := fixture.post(t, "alice", path, "release-one", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("initial request: %d: %s", rec.Code, rec.Body.String())
	}
	id := forceReleaseResponseApprovalID(t, decoded)
	record, err := fixture.store.GetApproval(id)
	if err != nil || record == nil || record.RequestedBy != "alice" || !record.RequiresSLB ||
		!strings.Contains(record.Resource, "BlueLake") || !strings.Contains(record.Resource, "internal/serve/*") {
		t.Fatalf("approval omitted principal or lease review evidence: %+v, %v", record, err)
	}
	rec, decoded = fixture.post(t, "alice", path, "release-one", body)
	if rec.Code != http.StatusForbidden || forceReleaseResponseApprovalID(t, decoded) != id {
		t.Fatalf("pending replay changed approval: %d: %s", rec.Code, rec.Body.String())
	}
	if fixture.calls.Load() != 0 {
		t.Fatal("pending request reached external release")
	}
	rec, _ = fixture.post(t, "alice", "/api/v1/approvals/"+id+"/approve", "", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self-approval: %d: %s", rec.Code, rec.Body.String())
	}
	fixture.approve(t, id)
	rec, decoded = fixture.post(t, "alice", path, "release-one", body)
	if rec.Code != http.StatusOK || decoded["force_released"] != true || decoded["approval_status"] != "consumed" {
		t.Fatalf("approved release: %d: %s", rec.Code, rec.Body.String())
	}
	rec, _ = fixture.post(t, "alice", path, "release-one", body)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Idempotent-Replay") != "true" || fixture.calls.Load() != 1 {
		t.Fatalf("successful retry repeated mutation: %d, calls=%d: %s", rec.Code, fixture.calls.Load(), rec.Body.String())
	}
	inspections := fixture.inspections.Load()
	rec, decoded = fixture.post(t, "alice", path, "a-new-http-request", body)
	if rec.Code != http.StatusConflict || forceReleaseResponseApprovalID(t, decoded) != id ||
		fixture.calls.Load() != 1 || fixture.inspections.Load() != inspections {
		t.Fatalf("spent replay must refuse before another lease read/release: %d: %s", rec.Code, rec.Body.String())
	}
	history, err := fixture.server.approvalEngine().History(t.Context())
	if err != nil || len(history) != 1 {
		t.Fatalf("replay minted a new approval: %+v, %v", history, err)
	}
}

func TestForceReleaseHTTPUnconfirmedReceiptRequiresExplicitReauthorization(t *testing.T) {
	fixture := newForceReleaseHTTPFixture(t)
	fixture.malformed.Store(true)
	const path = "/api/v1/reservations/17/force-release"
	body := ForceReleaseRequest{AgentName: "RedStone", Note: "holder crashed"}
	_, decoded := fixture.post(t, "alice", path, "uncertain-release", body)
	id := forceReleaseResponseApprovalID(t, decoded)
	fixture.approve(t, id)
	rec, decoded := fixture.post(t, "alice", path, "uncertain-release", body)
	details, _ := decoded["details"].(map[string]interface{})
	if rec.Code < 400 || details["approval_status"] != "consumed" ||
		details["execution_status"] != "unconfirmed" || fixture.calls.Load() != 1 {
		t.Fatalf("malformed receipt lost spent/uncertain evidence: %d: %s", rec.Code, rec.Body.String())
	}
	rec, _ = fixture.post(t, "alice", path, "uncertain-release", body)
	if rec.Code != http.StatusConflict || fixture.calls.Load() != 1 {
		t.Fatalf("uncertain retry repeated release: %d, calls=%d", rec.Code, fixture.calls.Load())
	}

	body.NewApproval = id
	rec, decoded = fixture.post(t, "alice", path, "deliberate-new-attempt", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("explicit new attempt: %d: %s", rec.Code, rec.Body.String())
	}
	nextID := forceReleaseResponseApprovalID(t, decoded)
	if nextID == id {
		t.Fatal("explicit new attempt reused consumed grant")
	}
	rec, decoded = fixture.post(t, "alice", path, "deliberate-new-attempt", body)
	if rec.Code != http.StatusForbidden || forceReleaseResponseApprovalID(t, decoded) != nextID {
		t.Fatalf("renewal replay did not reuse successor: %d: %s", rec.Code, rec.Body.String())
	}
	fixture.approve(t, nextID)
	fixture.malformed.Store(false)
	rec, _ = fixture.post(t, "alice", path, "deliberate-new-attempt", body)
	if rec.Code != http.StatusOK || fixture.calls.Load() != 2 {
		t.Fatalf("approved new attempt: %d: %s", rec.Code, rec.Body.String())
	}
	rec, _ = fixture.post(t, "alice", path, "renewal-with-another-http-key", body)
	if rec.Code != http.StatusConflict || fixture.calls.Load() != 2 {
		t.Fatalf("renewal replay minted another execution: %d: %s", rec.Code, rec.Body.String())
	}
	history, err := fixture.server.approvalEngine().History(t.Context())
	if err != nil || len(history) != 2 {
		t.Fatalf("renewal replays minted extra approvals: %+v, %v", history, err)
	}
}

func TestForceReleaseHTTPApprovalCannotChangeRequesterOrMailActor(t *testing.T) {
	fixture := newForceReleaseHTTPFixture(t)
	const path = "/api/v1/reservations/17/force-release"
	body := ForceReleaseRequest{AgentName: "RedStone"}
	_, decoded := fixture.post(t, "alice", path, "", body)
	id := forceReleaseResponseApprovalID(t, decoded)
	fixture.approve(t, id)

	spoofed := map[string]interface{}{"agent_name": "RedStone", "requested_by": "alice", "requestor": "alice"}
	rec, decoded := fixture.post(t, "mallory", path, "", spoofed)
	if rec.Code != http.StatusForbidden || fixture.calls.Load() != 0 {
		t.Fatalf("body identity spoof dispatched release: %d: %s", rec.Code, rec.Body.String())
	}
	otherID := forceReleaseResponseApprovalID(t, decoded)
	record, err := fixture.store.GetApproval(otherID)
	if err != nil || record == nil || record.RequestedBy != "mallory" || otherID == id {
		t.Fatalf("body controlled trusted requester: %+v, %v", record, err)
	}
	body.NewApproval = id
	rec, _ = fixture.post(t, "mallory", path, "", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign predecessor accepted: %d: %s", rec.Code, rec.Body.String())
	}
	body.AgentName = "GreenCastle"
	rec, _ = fixture.post(t, "alice", path, "", body)
	if rec.Code != http.StatusForbidden || fixture.calls.Load() != 0 {
		t.Fatalf("changed mail actor reused approval: %d: %s", rec.Code, rec.Body.String())
	}
	record, _ = fixture.store.GetApproval(id)
	if record == nil || record.Status != state.ApprovalApproved {
		t.Fatalf("foreign attempts spent original approval: %+v", record)
	}
}
