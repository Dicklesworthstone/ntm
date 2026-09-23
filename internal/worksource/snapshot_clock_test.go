package worksource

import (
	"testing"
	"time"
)

func TestNextEligibilityChangeIncludesOmittedOpenWork(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snapshot := &Snapshot{issues: map[string]issue{
		"later":  {Status: "open", DeferUntil: now.Add(2 * time.Hour).Format(time.RFC3339)},
		"first":  {Status: "open", DeferUntil: now.Add(time.Hour).Format(time.RFC3339)},
		"past":   {Status: "open", DeferUntil: now.Add(-time.Hour).Format(time.RFC3339)},
		"closed": {Status: "closed", DeferUntil: now.Add(time.Minute).Format(time.RFC3339)},
		"broken": {Status: "open", DeferUntil: "not a timestamp"},
	}}
	if got := snapshot.NextEligibilityChange(now); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("first boundary=%v", got)
	}
	if got := snapshot.NextEligibilityChange(now.Add(time.Hour)); !got.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("next boundary=%v", got)
	}
	if got := snapshot.NextEligibilityChange(now.Add(3 * time.Hour)); !got.IsZero() {
		t.Fatalf("unexpected later boundary=%v", got)
	}
	if got := (*Snapshot)(nil).NextEligibilityChange(now); !got.IsZero() {
		t.Fatalf("nil snapshot boundary=%v", got)
	}
}
