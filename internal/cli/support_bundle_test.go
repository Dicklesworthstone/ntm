package cli

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bundle"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestSupportBundleCreateAndVerify(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	origCfg := cfg
	cfg = config.Default()
	t.Cleanup(func() { cfg = origCfg })

	outputPath := filepath.Join(tmpDir, "diag-bundle.zip")
	err := runSupportBundle(t.Context(), supportBundleOptions{
		Output:          outputPath,
		Lines:           50,
		MaxSize:         10 * 1024 * 1024,
		IncludeSessions: false,
		IncludeEvents:   true,
	})
	if err != nil {
		t.Fatalf("runSupportBundle(create) error = %v", err)
	}

	verRes, err := bundle.Verify(outputPath)
	if err != nil {
		t.Fatalf("bundle.Verify() error = %v", err)
	}
	if !verRes.Valid {
		t.Fatalf("bundle.Verify() = %+v, want valid bundle", verRes)
	}
	if verRes.Manifest == nil {
		t.Fatal("expected non-nil manifest")
	}
	if verRes.Manifest.FileCount() < 2 {
		t.Fatalf("FileCount() = %d, want >= 2 (versions.json + doctor.json)", verRes.Manifest.FileCount())
	}
	if verRes.Manifest.TotalSize() <= 0 {
		t.Fatalf("TotalSize() = %d, want > 0", verRes.Manifest.TotalSize())
	}

	// Verify via CLI --verify flag
	if err := runSupportBundle(t.Context(), supportBundleOptions{
		Verify: outputPath,
	}); err != nil {
		t.Fatalf("runSupportBundle(--verify) error = %v", err)
	}
}

func TestSupportBundleVerifyDetectsTamperedArchive(t *testing.T) {
	tmpDir := t.TempDir()
	tamperedPath := filepath.Join(tmpDir, "tampered.zip")

	f, err := os.Create(tamperedPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)

	m := bundle.NewManifest("v1.0.0")
	m.AddFile(bundle.FileEntry{
		Path:        "versions.json",
		SHA256:      bundle.HashBytes([]byte(`{"ntm":"v1.0.0"}`)),
		SizeBytes:   16,
		ContentType: bundle.ContentTypeMetadata,
	})
	mBytes, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	mw, _ := zw.Create(bundle.ManifestFileName)
	_, _ = mw.Write(mBytes)

	// Write tampered payload whose checksum does not match manifest
	vw, _ := zw.Create("versions.json")
	_, _ = vw.Write([]byte(`{"ntm":"tampered"}`))
	_ = zw.Close()
	_ = f.Close()

	err = runSupportBundle(t.Context(), supportBundleOptions{
		Verify:    tamperedPath,
		RobotMode: true,
	})
	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runSupportBundle(--verify tampered --json) error = %v, want errJSONFailure", err)
	}

	err = runSupportBundle(t.Context(), supportBundleOptions{
		Verify: tamperedPath,
	})
	if err == nil || !strings.Contains(err.Error(), "bundle verification failed") {
		t.Fatalf("runSupportBundle(--verify tampered) error = %v, want verification failure", err)
	}
}
