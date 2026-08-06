package robot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func TestRCHWorkerCounts(t *testing.T) {
	workers := []tools.RCHWorker{
		{Name: "a", Available: true, Healthy: true, Load: 10, Queue: 0},
		{Name: "b", Available: true, Healthy: true, Load: 90, Queue: 0},
		{Name: "c", Available: true, Healthy: false, Load: 10, Queue: 0},
		{Name: "d", Available: false, Healthy: true, Load: 10, Queue: 0},
		{Name: "e", Available: true, Healthy: true, Load: 10, Queue: 0, CurrentBuild: "go test ./..."},
	}

	if got := countRCHHealthyWorkers(workers); got != 3 {
		t.Fatalf("expected 3 healthy workers, got %d", got)
	}
	if got := countRCHBusyWorkers(workers); got != 2 {
		t.Fatalf("expected 2 busy workers, got %d", got)
	}
}

func TestGetRCHStatusReportsDaemonFailure(t *testing.T) {
	dir := t.TempDir()
	fakeRCH := filepath.Join(dir, "rch")
	if err := os.WriteFile(fakeRCH, []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "rch 1.0.0"
  exit 0
fi
if [ "$1" = "status" ] && [ "$2" = "--json" ]; then
  echo "daemon socket unavailable" >&2
  exit 7
fi
exit 1
`), 0755); err != nil {
		t.Fatalf("write fake rch: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	adapter := tools.NewRCHAdapter()
	adapter.InvalidateAvailabilityCache()
	t.Cleanup(adapter.InvalidateAvailabilityCache)

	output, err := GetRCHStatus()
	if err != nil {
		t.Fatalf("GetRCHStatus() error: %v", err)
	}
	if output.RobotResponse.Success {
		t.Fatal("GetRCHStatus() success = true, want false when the daemon status probe fails")
	}
	// The binary and version probe succeeded; only daemon status is degraded.
	if !output.RCH.Available {
		t.Fatalf("GetRCHStatus() available = false, want true for installed rch")
	}
	if output.RCH.Version != "rch 1.0.0" {
		t.Fatalf("GetRCHStatus() version = %q, want rch 1.0.0", output.RCH.Version)
	}
	if output.RCH.Enabled {
		t.Fatal("GetRCHStatus() enabled = true, want false when daemon status is unavailable")
	}
	if !strings.Contains(output.RobotResponse.Error, "daemon socket unavailable") {
		t.Fatalf("GetRCHStatus() error = %q, want daemon diagnostic", output.RobotResponse.Error)
	}
}
