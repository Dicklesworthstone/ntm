package approval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
