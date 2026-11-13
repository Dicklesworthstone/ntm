package main

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReflectDeadCanaryReportsInterfaceOnlyMethodAndKeepsCalledMethod(t *testing.T) {
	canaryDir := filepath.Join("..", "testdata", "deadcode_canary")
	lines, err := analyze(canaryDir, "", "deadcodecanary/", "./cmd/canary")
	if err != nil {
		t.Fatalf("analyze(%s): %v", canaryDir, err)
	}
	if !slices.Contains(lines, "lib/lib.go:Reflected.ReflectOnly") {
		t.Fatalf("expected lib/lib.go:Reflected.ReflectOnly in reflectdead output, got %v", lines)
	}
	for _, line := range lines {
		if strings.Contains(line, "Reflected.Describe") {
			t.Fatalf("interface-dispatched method Reflected.Describe falsely reported dead: %v", lines)
		}
	}
}

func TestTaggedVetCanaryCatchesRottedTaggedTest(t *testing.T) {
	canaryDir := filepath.Join("..", "testdata", "tagged_vet_canary")

	untagged := exec.CommandContext(t.Context(), "go", "vet", "-C", canaryDir, "./...")
	if out, err := untagged.CombinedOutput(); err != nil {
		t.Fatalf("untagged go vet on %s should pass, got err=%v\n%s", canaryDir, err, string(out))
	}

	tagged := exec.CommandContext(t.Context(), "go", "vet", "-C", canaryDir, "-tags", "integration", "./...")
	if out, err := tagged.CombinedOutput(); err == nil {
		t.Fatalf("go vet -tags integration on %s should fail on rotted tagged test, got success:\n%s", canaryDir, string(out))
	}
}
