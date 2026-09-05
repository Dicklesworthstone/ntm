package adapters

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// #283 (2026-09-04 report): a collection where both sections were unavailable
// surfaced only "work/coordination sources unavailable" in source health and
// the snapshot, while `br` itself was readable. The typed error must carry
// each section's own reason so the operator can see what actually failed.
func TestCollectUnavailableErrorNamesSectionReasons(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	// br fails every command (a locked or half-migrated database, say); bv
	// answers with nothing usable.
	brScript := "#!/bin/sh\necho 'database is locked' >&2\nexit 1\n"
	bvScript := "#!/bin/sh\nexit 1\n"
	for name, script := range map[string]string{"br": brScript, "bv": bvScript} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)

	// Agent Mail refuses every request, so coordination is unavailable too.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	cfg := DefaultWorkCoordinationAdapterConfig(projectDir)
	cfg.AgentMailClient = agentmail.NewClient(
		agentmail.WithBaseURL(srv.URL+"/"),
		agentmail.WithProjectKey(projectDir),
	)
	adapter := NewWorkCoordinationAdapter(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	batch, err := adapter.Collect(ctx)
	if err == nil {
		t.Fatalf("Collect succeeded with both sections unavailable: %+v", batch)
	}
	if batch == nil || batch.Work == nil || batch.Work.Available {
		t.Fatalf("work section = %+v, want an unavailable section carried on the batch", batch)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "work/coordination sources unavailable") {
		t.Fatalf("error = %q, want the typed sources-unavailable prefix", msg)
	}
	if !strings.Contains(msg, "work: "+strings.TrimSpace(batch.Work.Reason)) || !strings.Contains(batch.Work.Reason, "br stats failed") {
		t.Fatalf("error %q does not carry the work reason %q", msg, batch.Work.Reason)
	}
	if batch.Coordination == nil || batch.Coordination.Available {
		t.Fatalf("coordination section = %+v, want unavailable", batch.Coordination)
	}
	if !strings.Contains(msg, "coordination: "+strings.TrimSpace(batch.Coordination.Reason)) || !strings.Contains(batch.Coordination.Reason, "agent mail unavailable") {
		t.Fatalf("error %q does not carry the coordination reason %q", msg, batch.Coordination.Reason)
	}
	if last := adapter.LastError(); !errors.Is(err, last) {
		t.Fatalf("LastError() = %v, want the returned error", last)
	}
}

func TestSourcesUnavailableErrorFallbacks(t *testing.T) {
	err := sourcesUnavailableError(nil, nil)
	if got := err.Error(); got != "work/coordination sources unavailable (work: work section missing; coordination: coordination section missing)" {
		t.Fatalf("nil sections = %q", got)
	}
	err = sourcesUnavailableError(&WorkSection{}, &CoordinationSection{Reason: "  agent mail unavailable "})
	if got := err.Error(); got != "work/coordination sources unavailable (work: work data unavailable; coordination: agent mail unavailable)" {
		t.Fatalf("empty reasons = %q", got)
	}
}
