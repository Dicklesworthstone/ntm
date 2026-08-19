package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCASSAdapter_HasCapability(t *testing.T) {
	t.Parallel()

	a := NewCASSAdapter()
	ctx := context.Background()

	if !a.HasCapability(ctx, CapSearch) {
		t.Fatalf("expected CapSearch capability")
	}
	if a.HasCapability(ctx, Capability("nope")) {
		t.Fatalf("expected unknown capability to be false")
	}
}

func TestCASSAdapter_HealthReportsStructuredUnhealthyDespiteNonZeroExit(t *testing.T) {
	fakeDir := t.TempDir()
	fakeCass := filepath.Join(fakeDir, "cass")
	if err := os.WriteFile(fakeCass, []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "cass 0.3.7"
  exit 0
fi
if [ "$1" = "health" ] && [ "$2" = "--json" ]; then
  printf '%s\n' '{"status":"unhealthy","healthy":false,"initialized":true,"errors":["index stale"],"recommended_action":"Run cass index"}'
  exit 1
fi
printf '%s\n' '{}'
`), 0755); err != nil {
		t.Fatalf("write fake cass: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+oldPath)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Fatal("Health() Healthy = true, want false")
	}
	if strings.Contains(health.Message, "not responding") {
		t.Fatalf("Health() collapsed structured cass health to transport failure: %q", health.Message)
	}
	for _, want := range []string{"cass reports unhealthy", "index stale", "Run cass index"} {
		if !strings.Contains(health.Message, want) {
			t.Fatalf("Health() message = %q, want substring %q", health.Message, want)
		}
	}
}
