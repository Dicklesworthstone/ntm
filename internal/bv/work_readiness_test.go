package bv

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGetReadySnapshotContextUsesOneDirectReadyResult(t *testing.T) {
	dir := newWorkReadinessTestWorkspace(t)

	snapshot, err := GetReadySnapshotContext(context.Background(), dir, 2)
	if err != nil {
		t.Fatalf("GetReadySnapshotContext() error: %v", err)
	}
	if snapshot.Total != 3 {
		t.Fatalf("snapshot.Total = %d, want 3", snapshot.Total)
	}
	if got := beadPreviewIDs(snapshot.Preview); !reflect.DeepEqual(got, []string{"ready-1", "ready-2"}) {
		t.Fatalf("snapshot preview IDs = %v, want [ready-1 ready-2]", got)
	}

	unlimited, err := GetReadySnapshotContext(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("GetReadySnapshotContext(unlimited) error: %v", err)
	}
	if unlimited.Total != 3 || len(unlimited.Preview) != 3 {
		t.Fatalf("unlimited snapshot = %+v, want total and preview length 3", unlimited)
	}
}

func TestGetBlockedSnapshotContextRetainsRestrictiveEvidence(t *testing.T) {
	dir := newWorkReadinessTestWorkspace(t)

	items, err := GetBlockedSnapshotContext(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("GetBlockedSnapshotContext() error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("blocked item count = %d, want 2", len(items))
	}
	join := items[0]
	if join.ID != "join" || join.Priority != 1 || join.Type != "task" {
		t.Fatalf("join metadata = %+v", join)
	}
	if !reflect.DeepEqual(join.BlockedBy, []string{"blocker-a", "blocker-b"}) {
		t.Fatalf("join.BlockedBy = %v, want sorted unique blockers", join.BlockedBy)
	}
	if join.BlockedByCount != 2 {
		t.Fatalf("join.BlockedByCount = %d, want 2", join.BlockedByCount)
	}
	if items[1].BlockedByCount != 1 {
		t.Fatalf("blocked membership without details must retain count 1: %+v", items[1])
	}

	limited, err := GetBlockedSnapshotContext(context.Background(), dir, 1)
	if err != nil {
		t.Fatalf("GetBlockedSnapshotContext(limit=1) error: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != "join" {
		t.Fatalf("limited blocked items = %+v, want join only", limited)
	}
}

func TestWorkReadinessSnapshotsRejectNegativeLimits(t *testing.T) {
	dir := newWorkReadinessTestWorkspace(t)
	if _, err := GetReadySnapshotContext(context.Background(), dir, -1); err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("ready negative-limit error = %v", err)
	}
	if _, err := GetBlockedSnapshotContext(context.Background(), dir, -1); err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("blocked negative-limit error = %v", err)
	}
}

func newWorkReadinessTestWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{".git", ".beads"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "${1:-}" = "--lock-timeout" ]; then shift 2; fi
case "$1" in
  ready)
    printf '%s\n' '{"issues":[{"id":"ready-1","title":"first","priority":1},{"id":"ready-2","title":"second","priority":2},{"id":"ready-3","title":"third","priority":3}],"total":3}'
    ;;
  blocked)
    printf '%s\n' '[{"id":"join","title":"Join completed work","priority":1,"issue_type":"task","blocked_by":[" blocker-b ","blocker-a","blocker-a",""]},{"id":"blocked-without-details","title":"Legacy blocked row","priority":2}]'
    ;;
  *)
    printf 'unexpected br command: %s\n' "$*" >&2
    exit 64
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func beadPreviewIDs(items []BeadPreview) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}
