package agentmail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeBuildSlotLease writes a lease file the way the Agent Mail server does:
// <archive>/projects/<slug>/build_slots/<slot>/<agent>__<branch>.json
func writeBuildSlotLease(t *testing.T, archiveRoot, slug, slot, agent, branch string, payload map[string]interface{}) {
	t.Helper()
	dir := filepath.Join(archiveRoot, "projects", slug, "build_slots", slot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir slot dir: %v", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal lease: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, agent+"__"+branch+".json"), raw, 0o644); err != nil {
		t.Fatalf("write lease: %v", err)
	}
}

func TestListActiveBuildSlotLeases(t *testing.T) {
	t.Parallel()

	archiveRoot := t.TempDir()
	projectKey := "/tmp/ntm-buildslot-test"
	slug := ProjectSlugFromPath(projectKey)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	// Active lease: not released, expiry in the future.
	writeBuildSlotLease(t, archiveRoot, slug, "frontend", "GreenCastle", "main", map[string]interface{}{
		"slot": "frontend", "agent": "GreenCastle", "branch": "main", "exclusive": true,
		"acquired_ts": "2026-08-14T11:00:00Z", "expires_ts": "2026-08-14T13:00:00Z",
	})
	// Expired lease: expiry in the past.
	writeBuildSlotLease(t, archiveRoot, slug, "frontend", "BlueLake", "main", map[string]interface{}{
		"slot": "frontend", "agent": "BlueLake", "branch": "main",
		"acquired_ts": "2026-08-14T09:00:00Z", "expires_ts": "2026-08-14T10:00:00Z",
	})
	// Released lease.
	writeBuildSlotLease(t, archiveRoot, slug, "backend", "RedFox", "dev", map[string]interface{}{
		"slot": "backend", "agent": "RedFox", "branch": "dev",
		"acquired_ts": "2026-08-14T11:00:00Z", "expires_ts": "2026-08-14T13:00:00Z",
		"released_ts": "2026-08-14T11:30:00Z",
	})
	// Active lease missing its "slot" field: falls back to the dir name,
	// mirroring the server's tolerant reader.
	writeBuildSlotLease(t, archiveRoot, slug, "docs-build", "AmberOwl", "main", map[string]interface{}{
		"agent": "AmberOwl", "branch": "main", "expires_ts": "2026-08-14T13:00:00Z",
	})
	// Garbage file: skipped without failing the listing.
	garbageDir := filepath.Join(archiveRoot, "projects", slug, "build_slots", "frontend")
	if err := os.WriteFile(filepath.Join(garbageDir, "corrupt.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	leases, err := ListActiveBuildSlotLeases(archiveRoot, projectKey, now)
	if err != nil {
		t.Fatalf("ListActiveBuildSlotLeases: %v", err)
	}
	t.Logf("decision: archive scan is the ONLY listing surface (server has no list_build_slots tool or resource); got %d active leases", len(leases))
	if len(leases) != 2 {
		t.Fatalf("active leases = %d, want 2 (GreenCastle + AmberOwl): %+v", len(leases), leases)
	}
	bySlotAgent := map[string]BuildSlotLease{}
	for _, l := range leases {
		bySlotAgent[l.Slot+"/"+l.Agent] = l
	}
	if _, ok := bySlotAgent["frontend/GreenCastle"]; !ok {
		t.Errorf("missing active lease frontend/GreenCastle: %+v", leases)
	}
	if l, ok := bySlotAgent["docs-build/AmberOwl"]; !ok || l.Slot != "docs-build" {
		t.Errorf("slot-field fallback from dir name failed: %+v", leases)
	}
}

func TestListActiveBuildSlotLeasesDegradation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()

	// Archive root does not exist at all → the listing surface is
	// unavailable and callers must degrade, not fabricate.
	_, err := ListActiveBuildSlotLeases(filepath.Join(t.TempDir(), "nope"), "/tmp/p", now)
	if !errors.Is(err, ErrBuildSlotListingUnavailable) {
		t.Fatalf("missing archive: err = %v, want ErrBuildSlotListingUnavailable", err)
	}
	t.Logf("decision: missing archive reports ErrBuildSlotListingUnavailable so diagnose surfaces a degraded source instead of an empty (falsely clean) result")

	// Empty archive root string (unresolvable home) degrades the same way.
	if _, err := ListActiveBuildSlotLeases("", "/tmp/p", now); !errors.Is(err, ErrBuildSlotListingUnavailable) {
		t.Fatalf("empty root: err = %v, want ErrBuildSlotListingUnavailable", err)
	}

	// Archive exists but the project has no build_slots subtree → the
	// project simply has no leases: empty result, no error.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	leases, err := ListActiveBuildSlotLeases(root, "/tmp/never-used", now)
	if err != nil || len(leases) != 0 {
		t.Fatalf("project without leases: leases=%v err=%v, want empty and nil", leases, err)
	}
}

func TestBuildSlotLeaseActiveAt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	released := FlexTime{Time: now.Add(-time.Hour)}

	cases := []struct {
		name  string
		lease BuildSlotLease
		want  bool
	}{
		{"future expiry", BuildSlotLease{ExpiresTS: FlexTime{Time: now.Add(time.Hour)}}, true},
		{"past expiry", BuildSlotLease{ExpiresTS: FlexTime{Time: now.Add(-time.Minute)}}, false},
		{"released", BuildSlotLease{ExpiresTS: FlexTime{Time: now.Add(time.Hour)}, ReleasedTS: &released}, false},
		// Server parity: _is_active_build_slot_lease treats a missing
		// expiry as active.
		{"zero expiry", BuildSlotLease{}, true},
	}
	for _, tc := range cases {
		if got := tc.lease.ActiveAt(now); got != tc.want {
			t.Errorf("%s: ActiveAt = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestReleaseBuildSlot(t *testing.T) {
	t.Parallel()

	var gotArgs map[string]interface{}
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"release_build_slot": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			gotArgs = args
			return map[string]interface{}{"released": true, "released_at": "2026-08-14T12:00:00Z"}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	c.SetRegistrationToken("/test/project", "GreenCastle", "tok-123")

	result, err := c.ReleaseBuildSlot(context.Background(), "/test/project", "GreenCastle", "frontend", "main")
	if err != nil {
		t.Fatalf("ReleaseBuildSlot: %v", err)
	}
	if !result.Released {
		t.Fatalf("released = false, want true: %+v", result)
	}
	if gotArgs["project_key"] != "/test/project" || gotArgs["agent_name"] != "GreenCastle" ||
		gotArgs["slot"] != "frontend" || gotArgs["branch"] != "main" {
		t.Fatalf("release args = %+v", gotArgs)
	}
	// The server authenticates release_build_slot as the holder, so the
	// cached registration token must ride along automatically.
	if gotArgs["registration_token"] != "tok-123" {
		t.Fatalf("registration_token = %v, want tok-123 (holder auth is mandatory server-side)", gotArgs["registration_token"])
	}
	t.Logf("decision: release goes through the real release_build_slot MCP tool with the holder's persisted token; branch is forwarded from the lease so the server rebuilds the same holder id")
}

func TestReleaseBuildSlotServerDown(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, nil))
	server.Close() // immediately down

	c := NewClient(WithBaseURL(server.URL + "/"))
	if _, err := c.ReleaseBuildSlot(context.Background(), "/test", "A", "s", ""); err == nil {
		t.Fatal("expected error when Agent Mail server is unreachable")
	}
	t.Logf("decision: transport failure surfaces as an error for callers to degrade on — spawn/diagnose fold it into warnings, never a hard failure")
}

func TestReleaseBuildSlotValidation(t *testing.T) {
	t.Parallel()
	c := NewClient(WithBaseURL("http://127.0.0.1:1/"))
	for _, tc := range [][3]string{{"", "A", "s"}, {"/p", "", "s"}, {"/p", "A", ""}} {
		if _, err := c.ReleaseBuildSlot(context.Background(), tc[0], tc[1], tc[2], ""); err == nil {
			t.Fatalf("expected validation error for %v", tc)
		}
	}
}
