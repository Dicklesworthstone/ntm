package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFakeCassHealthScript(t *testing.T, healthJSON string, exitCode string) string {
	t.Helper()

	fakeDir := t.TempDir()
	fakeCass := filepath.Join(fakeDir, "cass")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then\n" +
		"  echo \"cass 0.3.7\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"health\" ] && [ \"$2\" = \"--json\" ]; then\n" +
		"  printf '%s\\n' '" + healthJSON + "'\n" +
		"  exit " + exitCode + "\n" +
		"fi\n" +
		"printf '%s\\n' '{}'\n"
	if err := os.WriteFile(fakeCass, []byte(script), 0755); err != nil {
		t.Fatalf("write fake cass: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+oldPath)
	return fakeCass
}

func TestCASSAdapter_HealthCurrentSchemaWithoutInitializedUsesUnhealthyDetails(t *testing.T) {
	writeFakeCassHealthScript(t,
		`{"status":"corrupt_index","healthy":false,"errors":["lexical index missing"],"recommended_action":"Run cass index --full --json"}`,
		"1",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Fatal("Health() Healthy = true, want false")
	}
	if strings.Contains(health.Message, "not initialized") {
		t.Fatalf("Health() message = %q, should not misclassify as uninitialized", health.Message)
	}
	for _, want := range []string{"cass reports unhealthy", "corrupt_index", "lexical index missing", "Run cass index --full --json"} {
		if !strings.Contains(health.Message, want) {
			t.Fatalf("Health() message = %q, want substring %q", health.Message, want)
		}
	}
}

func TestCASSAdapter_HealthInitializedFalseStillMapsToUninitialized(t *testing.T) {
	writeFakeCassHealthScript(t,
		`{"status":"not_initialized","healthy":false,"initialized":false}`,
		"1",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Fatal("Health() Healthy = true, want false")
	}
	if !strings.Contains(health.Message, "not initialized") {
		t.Fatalf("Health() message = %q, want uninitialized hint", health.Message)
	}
}
