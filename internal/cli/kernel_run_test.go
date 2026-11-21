package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
)

func TestKernelCatalogHonestExecutableFlagAndRunCommand(t *testing.T) {
	// 1. Registered in-process handlers report executable=true, catalog_only=false.
	meta, ok := kernel.Get("core.version")
	if !ok {
		t.Fatal("expected core.version to be registered in kernel")
	}
	if !meta.Executable || meta.CatalogOnly {
		t.Fatalf("core.version executable=%v catalog_only=%v, want true/false", meta.Executable, meta.CatalogOnly)
	}

	// 2. Execute core.version via runKernelCommand and verify valid JSON output.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	runErr := runKernelCommand(context.Background(), "core.version", "")
	_ = w.Close()
	os.Stdout = oldStdout
	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)
	_ = r.Close()

	if runErr != nil {
		t.Fatalf("runKernelCommand(core.version): %v", runErr)
	}
	var decoded map[string]any
	if err := json.Unmarshal(outBuf.Bytes(), &decoded); err != nil {
		t.Fatalf("core.version output was not valid JSON (%q): %v", outBuf.String(), err)
	}
	if _, hasVersion := decoded["version"]; !hasVersion {
		t.Fatalf("core.version JSON missing version field: %+v", decoded)
	}

	// 3. Catalog-only entries must fail closed honestly with an explicit catalog-only message.
	var catalogOnlyName string
	for _, c := range kernel.List() {
		if c.CatalogOnly && !c.Executable {
			catalogOnlyName = c.Name
			break
		}
	}
	if catalogOnlyName == "" {
		t.Fatal("expected at least one catalog_only command after EnsureKernelCatalogRegistered")
	}
	if err := runKernelCommand(context.Background(), catalogOnlyName, ""); err == nil || !strings.Contains(err.Error(), "catalog-only") {
		t.Fatalf("runKernelCommand(%q) err = %v, want catalog-only refusal", catalogOnlyName, err)
	}

	// 4. Unknown command returns a clean not-found error.
	if err := runKernelCommand(context.Background(), "nonexistent.kernel.command", ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("runKernelCommand(unknown) err = %v, want not found", err)
	}

	// 5. Invalid --input JSON returns a clean parse error.
	if err := runKernelCommand(context.Background(), "core.version", "{bad-json"); err == nil || !strings.Contains(err.Error(), "parse --input JSON") {
		t.Fatalf("runKernelCommand(bad-json) err = %v, want parse --input JSON", err)
	}
}
