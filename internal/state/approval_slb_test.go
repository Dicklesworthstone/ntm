package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestApprovalSLBLinkIsDurableImmutableAndKeepsCorrelation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	create := func(id, correlation string, slb bool) {
		t.Helper()
		if err := store.CreateApproval(&Approval{ID: id, Action: "force_release", Resource: "reservation 17", RequestedBy: "alice", CorrelationID: correlation, RequiresSLB: slb, Status: ApprovalPending, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	create("first", "operation:17", true)
	create("second", "", true)
	create("native", "", false)
	link := ApprovalSLBLink{ApprovalID: "first", RequestID: "req-1", ProjectDir: t.TempDir()}
	if err := store.CreateApprovalSLBLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApprovalSLBLink(ctx, link); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	for _, tc := range []struct {
		name string
		link ApprovalSLBLink
	}{
		{"request replacement", ApprovalSLBLink{"first", "req-2", link.ProjectDir}},
		{"project replacement", ApprovalSLBLink{"first", "req-1", t.TempDir()}},
		{"request reused by another approval", ApprovalSLBLink{"second", "req-1", link.ProjectDir}},
		{"missing approval", ApprovalSLBLink{"missing", "req-missing", link.ProjectDir}},
		{"native only approval", ApprovalSLBLink{"native", "req-native", link.ProjectDir}},
		{"empty request", ApprovalSLBLink{"second", "", link.ProjectDir}},
		{"relative project", ApprovalSLBLink{"second", "req-2", "relative"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.CreateApprovalSLBLink(ctx, tc.link); err == nil {
				t.Fatalf("accepted invalid/replacement link: %+v", tc.link)
			}
		})
	}
	row, err := store.GetApproval("first")
	if err != nil || row == nil || row.CorrelationID != "operation:17" || row.Status != ApprovalPending {
		t.Fatalf("link changed operation or decision: %+v %v", row, err)
	}
	// The failed duplicate did not annotate the other approval.
	row, err = store.GetApproval("second")
	if err != nil || row == nil || row.CorrelationID != "" {
		t.Fatalf("failed link was not atomic: %+v %v", row, err)
	}
	second := ApprovalSLBLink{"second", "req-2", link.ProjectDir}
	if err := store.CreateApprovalSLBLink(ctx, second); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetApproval("second")
	if err != nil || row == nil || row.CorrelationID != "slb:req-2" {
		t.Fatalf("uncorrelated display annotation missing: %+v %v", row, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetApprovalSLBLink(ctx, "first")
	if err != nil || got == nil || *got != link {
		t.Fatalf("reopened external identity=%+v error=%v", got, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.GetApprovalSLBLink(canceled, "first"); err == nil {
		t.Fatal("canceled read was ignored")
	}
	if err := store.CreateApprovalSLBLink(canceled, link); err == nil {
		t.Fatal("canceled write was ignored")
	}
}
