package approval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func TestSLBDecisionRequiresBoundReviewerEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requester string
		status    *tools.SLBRequestStatus
		want      bool
		reviewer  string
	}{
		{name: "missing status"},
		{name: "missing request identity", status: &tools.SLBRequestStatus{Status: "approved", Reviews: []tools.SLBReview{{Reviewer: "bob", Decision: "approve"}}}},
		{name: "another request", status: &tools.SLBRequestStatus{RequestID: "other", Status: "approved", Reviews: []tools.SLBReview{{Reviewer: "bob", Decision: "approve"}}}},
		{name: "no reviews", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "approved"}},
		{name: "wrong decision", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "approved", Reviews: []tools.SLBReview{{Reviewer: "bob", Decision: "reject"}}}},
		{name: "unknown decision is not rejection", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "rejected", Reviews: []tools.SLBReview{{Reviewer: "bob", Decision: "abstain"}}}},
		{name: "empty reviewer", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "approved", Reviews: []tools.SLBReview{{Decision: "approve"}}}},
		{name: "whitespace reviewer", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "approved", Reviews: []tools.SLBReview{{Reviewer: "  ", Decision: "approve"}}}},
		{name: "padded reviewer", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "approved", Reviews: []tools.SLBReview{{Reviewer: " alice ", Decision: "approve"}}}},
		{name: "self review", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "approved", Reviews: []tools.SLBReview{{Reviewer: "alice", Decision: "approve"}}}},
		{name: "namespaced requester", requester: "slb:alice", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "approved", Reviews: []tools.SLBReview{{Reviewer: "alice", Decision: "approve"}}}},
		{name: "namespaced reviewer", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "approved", Reviews: []tools.SLBReview{{Reviewer: "slb:alice", Decision: "approve"}}}},
		{name: "pending despite reviews", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "pending", Reviews: []tools.SLBReview{{Reviewer: "bob", Decision: "approve"}}}},
		{name: "last matching review malformed", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "approved", Reviews: []tools.SLBReview{{Reviewer: "bob", Decision: "approve"}, {Decision: "approve"}}}},
		{name: "approved", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "approved", Reviews: []tools.SLBReview{{Reviewer: "bob", Decision: "approve"}}}, want: true, reviewer: "bob"},
		{name: "rejected", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "rejected", Reviews: []tools.SLBReview{{Reviewer: "bob", Decision: "reject", Comments: "still active"}}}, want: true, reviewer: "bob"},
		{name: "last matching review", status: &tools.SLBRequestStatus{RequestID: "req-1", Status: "approved", Reviews: []tools.SLBReview{{Reviewer: "bob", Decision: "approve"}, {Reviewer: "carol", Decision: "approve"}, {Reviewer: "dave", Decision: "abstain"}}}, want: true, reviewer: "carol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requester := tc.requester
			if requester == "" {
				requester = "alice"
			}
			review, valid := slbDecisionReview("req-1", requester, tc.status)
			if valid != tc.want || (valid && review.Reviewer != tc.reviewer) {
				t.Fatalf("review=%+v valid=%t, want reviewer=%q valid=%t", review, valid, tc.reviewer, tc.want)
			}
		})
	}
}

// Exercise the public engine used by approval CLI/HTTP handlers with real
// SQLite state and a controlled external SLB executable, not a fake decision
// callback. An invalid external receipt must leave the durable row pending.
func TestCheckSLBDecisionPersistsOnlyVerifiedReview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SLB executable fixture requires /bin/sh")
	}
	dir := t.TempDir()
	responsePath := filepath.Join(dir, "response.json")
	callsPath := filepath.Join(dir, "calls")
	script := `#!/bin/sh
case "$1" in
  status)
    printf '%s\n' "$*" >> "$NTM_SLB_REVIEW_CALLS"
    cat "$NTM_SLB_REVIEW_RESPONSE"
    ;;
  *) exit 91 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "slb"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NTM_SLB_REVIEW_RESPONSE", responsePath)
	t.Setenv("NTM_SLB_REVIEW_CALLS", callsPath)
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	engine := New(store, nil, nil, Config{EnableSLB: true})
	for _, tc := range []struct {
		name, requestID, status, reviewer, decision string
		want                                        state.ApprovalStatus
		allowlist                                   []string
	}{
		{"no reviewer", "req-1", "approved", "", "approve", state.ApprovalPending, nil},
		{"self review", "req-1", "approved", "alice", "approve", state.ApprovalPending, nil},
		{"wrong request", "req-other", "approved", "bob", "approve", state.ApprovalPending, nil},
		{"unknown rejection", "req-1", "rejected", "bob", "comment", state.ApprovalPending, nil},
		{"unlisted approver", "req-1", "approved", "bob", "approve", state.ApprovalPending, []string{"slb:carol"}},
		{"valid approval", "req-1", "approved", "bob", "approve", state.ApprovalApproved, []string{"slb:bob"}},
		{"valid rejection", "req-1", "rejected", "bob", "reject", state.ApprovalDenied, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine.config.ApproverList = tc.allowlist
			row := &state.Approval{
				ID: tc.name, Action: "force_release", Resource: "reservation 17", RequestedBy: "alice",
				CorrelationID: "slb:req-1", RequiresSLB: true, Status: state.ApprovalPending,
				CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(),
			}
			if err := store.CreateApproval(row); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateApprovalSLBLink(context.Background(), state.ApprovalSLBLink{
				ApprovalID: row.ID, RequestID: "req-1", ProjectDir: t.TempDir(),
			}); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(tools.SLBRequestStatus{
				RequestID: tc.requestID, Status: tc.status,
				Reviews: []tools.SLBReview{{Reviewer: tc.reviewer, Decision: tc.decision, Comments: "reviewed lease"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(responsePath, payload, 0600); err != nil {
				t.Fatal(err)
			}
			observed, err := engine.Check(context.Background(), row.ID)
			if err != nil || observed == nil || observed.Status != tc.want {
				t.Fatalf("Check()=%+v err=%v, want %s", observed, err, tc.want)
			}
			durable, err := store.GetApproval(row.ID)
			if err != nil || durable == nil || durable.Status != tc.want {
				t.Fatalf("durable row=%+v err=%v, want %s", durable, err, tc.want)
			}
			if tc.want == state.ApprovalPending && (durable.ApprovedBy != "" || durable.ApprovedAt != nil) {
				t.Fatalf("invalid receipt stamped reviewer evidence: %+v", durable)
			}
			if tc.want != state.ApprovalPending && durable.ApprovedBy != "slb:bob" {
				t.Fatalf("valid receipt lost reviewer attribution: %+v", durable)
			}
			if tc.want == state.ApprovalDenied && durable.DeniedReason != "reviewed lease" {
				t.Fatalf("rejection lost review reason: %+v", durable)
			}
		})
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil || !strings.Contains(string(calls), "status --json -- req-1") {
		t.Fatalf("Check did not use the external SLB adapter: calls=%q error=%v", calls, err)
	}
}

func installSLBLinkFixture(t *testing.T) (projectDir, callsPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("SLB executable fixture requires /bin/sh")
	}
	binDir, projectDir := t.TempDir(), t.TempDir()
	var err error
	projectDir, err = filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	callsPath = filepath.Join(binDir, "calls")
	script := `#!/bin/sh
case "$1" in
  request)
    printf 'request|%s\n' "$PWD" >> "$NTM_SLB_LINK_CALLS"
    cat request-response.json
    ;;
  status)
    printf 'status|%s\n' "$PWD" >> "$NTM_SLB_LINK_CALLS"
    cat status-response.json
    ;;
  *) exit 91 ;;
esac
`
	for path, content := range map[string]string{
		filepath.Join(binDir, "slb"):                       script,
		filepath.Join(projectDir, "request-response.json"): `{"request_id":"req-1"}`,
		filepath.Join(projectDir, "status-response.json"):  `{"request_id":"req-1","status":"pending","reviews":[]}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SLB_SESSION_ID", "requester-session")
	t.Setenv("SLB_SESSION_KEY", "test-key-never-persisted")
	t.Setenv("NTM_SLB_LINK_CALLS", callsPath)
	return projectDir, callsPath
}

func openSLBLinkStore(t *testing.T, path string) *state.Store {
	t.Helper()
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestForceReleaseSLBLinkSurvivesRestartAndDifferentWorkingDirectory(t *testing.T) {
	projectDir, callsPath := installSLBLinkFixture(t)
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldDir) })
	path := filepath.Join(t.TempDir(), "state.db")
	store := openSLBLinkStore(t, path)
	engine := New(store, nil, nil, Config{EnableSLB: true})
	op := ForceReleaseOperation{ProjectKey: projectDir, ReservationID: 17, RequestedBy: "alice", AgentName: "RedStone", Note: "holder crashed"}
	load := func(context.Context) (ForceReleaseLease, error) {
		return ForceReleaseLease{Holder: "BlueLake", Path: "internal/auth/*", ProjectID: 4, CreatedAt: "2026-01-01", Exclusive: true}, nil
	}
	first, err := engine.EvaluateForceRelease(context.Background(), "approval", op, "", load)
	if err != nil || !first.Created || first.Allowed || first.ApprovalStatus != "pending" {
		t.Fatalf("initial request=%+v error=%v", first, err)
	}
	link, err := store.GetApprovalSLBLink(context.Background(), first.ApprovalID)
	if err != nil || link == nil || link.ProjectDir != projectDir || link.RequestID != "req-1" {
		t.Fatalf("durable external identity=%+v error=%v", link, err)
	}
	row, err := store.GetApproval(first.ApprovalID)
	if err != nil || row.CorrelationID != op.CorrelationKey() {
		t.Fatalf("external link overwrote the operation key: %+v %v", row, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openSLBLinkStore(t, path)
	engine = New(store, nil, nil, Config{EnableSLB: true})
	if err := os.WriteFile(filepath.Join(projectDir, "status-response.json"), []byte(`{"request_id":"req-1","status":"approved","reviews":[{"reviewer":"bob","decision":"approve"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	approved, err := engine.EvaluateForceRelease(context.Background(), "approval", op, "", load)
	if err != nil || !approved.Allowed || approved.Created || approved.ApprovalStatus != "consumed" || approved.ApprovalID != first.ApprovalID {
		t.Fatalf("external approval did not authorize the original operation: %+v %v", approved, err)
	}
	spent, err := engine.EvaluateForceRelease(context.Background(), "approval", op, "", load)
	if err != nil || spent.Allowed || spent.Created || spent.ApprovalStatus != "consumed" {
		t.Fatalf("consumed external grant authorized another attempt: %+v %v", spent, err)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil || strings.Count(string(calls), "request|") != 1 || !strings.Contains(string(calls), "status|"+projectDir) {
		t.Fatalf("recovery remirrored the request or lost project scope: %q %v", calls, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if !strings.HasSuffix(line, "|"+projectDir) {
			t.Fatalf("SLB ran outside the bound project: %q", line)
		}
	}
}

func TestSLBLinkConcurrentAdmissionMirrorsOnlyOnce(t *testing.T) {
	projectDir, callsPath := installSLBLinkFixture(t)
	path := filepath.Join(t.TempDir(), "state.db")
	engines := []*Engine{
		New(openSLBLinkStore(t, path), nil, nil, Config{EnableSLB: true}),
		New(openSLBLinkStore(t, path), nil, nil, Config{EnableSLB: true}),
	}
	params := RequestParams{Action: "force_release", Resource: "reservation 17", RequestedBy: "alice", CorrelationID: "operation:17", RequiresSLB: true, ProjectDir: projectDir}
	start := make(chan struct{})
	var wg sync.WaitGroup
	created := make([]bool, len(engines))
	errs := make([]error, len(engines))
	for i, engine := range engines {
		wg.Add(1)
		go func(i int, engine *Engine) {
			defer wg.Done()
			<-start
			_, created[i], errs[i] = engine.requestWithID(context.Background(), params, "one-admission", true)
		}(i, engine)
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || created[0] == created[1] {
		t.Fatalf("admission winners=%v errors=%v", created, errs)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil || strings.Count(string(calls), "request|") != 1 {
		t.Fatalf("concurrent admission created duplicate external grants: %q %v", calls, err)
	}
	for _, engine := range engines {
		link, err := engine.store.GetApprovalSLBLink(context.Background(), "one-admission")
		if err != nil || link == nil || link.RequestID != "req-1" || link.ProjectDir != projectDir {
			t.Fatalf("connections disagree about external identity: %+v %v", link, err)
		}
	}
}

func TestSLBLinkFailureDoesNotRemirrorOrInferAuthority(t *testing.T) {
	projectDir, callsPath := installSLBLinkFixture(t)
	store := openSLBLinkStore(t, filepath.Join(t.TempDir(), "state.db"))
	if _, err := store.DB().Exec(`CREATE TRIGGER refuse_slb_link BEFORE INSERT ON approval_slb_links
		BEGIN SELECT RAISE(ABORT, 'link storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	engine := New(store, nil, nil, Config{EnableSLB: true})
	params := RequestParams{Action: "force_release", Resource: "reservation 17", RequestedBy: "alice", CorrelationID: "slb:req-1", RequiresSLB: true, ProjectDir: projectDir}
	row, created, err := engine.requestWithID(context.Background(), params, "unlinked", true)
	if err == nil || row == nil || !created || !strings.Contains(err.Error(), "could not be linked") {
		t.Fatalf("link failure lost native admission: row=%+v created=%t err=%v", row, created, err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "status-response.json"), []byte(`{"request_id":"req-1","status":"approved","reviews":[{"reviewer":"bob","decision":"approve"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	row, created, err = engine.requestWithID(context.Background(), params, "unlinked", true)
	if err != nil || created || row == nil || row.Status != state.ApprovalPending {
		t.Fatalf("interrupted mirror replayed instead of retaining native review: %+v %t %v", row, created, err)
	}
	row, err = engine.Check(context.Background(), "unlinked")
	if err != nil || row == nil || row.Status != state.ApprovalPending {
		t.Fatalf("correlation string fabricated authority without a saved link: %+v %v", row, err)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil || strings.Count(string(calls), "request|") != 1 || strings.Contains(string(calls), "status|") {
		t.Fatalf("uncertain link caused resubmission or unbound lookup: %q %v", calls, err)
	}
	if err := engine.Approve(context.Background(), "unlinked", "bob"); err != nil {
		t.Fatalf("native two-person fallback became unavailable: %v", err)
	}
}

func TestSLBLinkRefusesRedirectedProject(t *testing.T) {
	projectDir, callsPath := installSLBLinkFixture(t)
	store := openSLBLinkStore(t, filepath.Join(t.TempDir(), "state.db"))
	engine := New(store, nil, nil, Config{EnableSLB: true})
	row, err := engine.Request(context.Background(), RequestParams{
		Action: "force_release", Resource: "reservation 17", RequestedBy: "alice",
		RequiresSLB: true, ProjectDir: projectDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "status-response.json"), []byte(`{"request_id":"req-1","status":"approved","reviews":[{"reviewer":"bob","decision":"approve"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(projectDir, projectDir+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, projectDir); err != nil {
		t.Fatal(err)
	}
	got, err := engine.Check(context.Background(), row.ID)
	if err != nil || got == nil || got.Status != state.ApprovalPending {
		t.Fatalf("redirected project supplied approval evidence: %+v %v", got, err)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil || strings.Contains(string(calls), "status|") {
		t.Fatalf("queried substituted project: %q %v", calls, err)
	}
}
