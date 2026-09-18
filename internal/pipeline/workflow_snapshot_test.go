package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func snapshotTestWorkflow() *Workflow {
	return &Workflow{SchemaVersion: "2.0", Name: "snapshot-test", Steps: []Step{{ID: "work", Command: "echo original"}}}
}

func TestWorkflowSnapshotFreezesCallerAndIsOrdinaryWorkflowFile(t *testing.T) {
	original := snapshotTestWorkflow()
	frozen, path, err := SnapshotWorkflow(context.Background(), t.TempDir(), original)
	if err != nil {
		t.Fatal(err)
	}
	original.Name = "changed"
	original.Steps[0].Command = "echo changed"
	if frozen.Name != "snapshot-test" || frozen.Steps[0].Command != "echo original" {
		t.Fatalf("running workflow still aliases caller input: %+v", frozen)
	}
	for _, load := range []func(string) (*Workflow, ValidationResult, error){LoadResumeWorkflow, LoadAndValidate} {
		loaded, validation, err := load(path)
		if err != nil || !validation.Valid || loaded.Name != frozen.Name || loaded.Steps[0].Command != "echo original" {
			t.Fatalf("snapshot is not reloadable by normal/resume loader: %+v %+v %v", loaded, validation, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("snapshot exposes potentially sensitive workflow constants: %v", info.Mode())
	}
}

func TestWorkflowSnapshotReusesIdenticalArtifactWithoutRewriting(t *testing.T) {
	dir := t.TempDir()
	_, path, err := SnapshotWorkflow(context.Background(), dir, snapshotTestWorkflow())
	if err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1234567890, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	_, second, err := SnapshotWorkflow(context.Background(), dir, snapshotTestWorkflow())
	if err != nil || second != path {
		t.Fatalf("identical definition did not reuse snapshot: %q %v", second, err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("existing definition was rewritten: %v", err)
	}
}

func TestWorkflowSnapshotConcurrentPublishers(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	paths := make(chan string, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, path, err := SnapshotWorkflow(context.Background(), dir, snapshotTestWorkflow())
			if err != nil {
				t.Error(err)
				return
			}
			paths <- path
		}()
	}
	wg.Wait()
	close(paths)
	first := ""
	for path := range paths {
		if first != "" && first != path {
			t.Fatalf("same definition has different identities: %q %q", first, path)
		}
		first = path
	}
	if first == "" {
		t.Fatal("no snapshot published")
	}
	if _, validation, err := LoadResumeWorkflow(first); err != nil || !validation.Valid {
		t.Fatalf("concurrent publication produced corrupt artifact: %+v %v", validation, err)
	}
}

func TestWorkflowSnapshotCorruptionFailsClosedWithoutRepair(t *testing.T) {
	dir := t.TempDir()
	_, path, err := SnapshotWorkflow(context.Background(), dir, snapshotTestWorkflow())
	if err != nil {
		t.Fatal(err)
	}
	damaged := []byte(`{"schema_version":"2.0","name":"snapshot-test","steps":[{"id":"work","command":"echo substituted"}]}`)
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadResumeWorkflow(path); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("changed executable definition was accepted: %v", err)
	}
	if _, _, err := SnapshotWorkflow(context.Background(), dir, snapshotTestWorkflow()); err == nil {
		t.Fatal("corruption was silently repaired")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, damaged) {
		t.Fatalf("corrupt evidence was overwritten: %v", err)
	}
}

func TestWorkflowSnapshotRejectsInvalidInputBeforeWriting(t *testing.T) {
	for _, name := range []string{"nil", "invalid", "canceled"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			workflow := snapshotTestWorkflow()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "nil":
				workflow = nil
			case "invalid":
				workflow.Name = ""
			case "canceled":
				cancel()
			}
			if _, _, err := SnapshotWorkflow(ctx, dir, workflow); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected snapshot wrote files: %v %v", entries, err)
			}
		})
	}
}

func TestWorkflowSnapshotRejectsOversizedOrMalformedArtifact(t *testing.T) {
	dir := t.TempDir()
	_, path, err := SnapshotWorkflow(context.Background(), dir, snapshotTestWorkflow())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxWorkflowSnapshotBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadResumeWorkflow(path); err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("oversized snapshot accepted: %v", err)
	}
	if _, _, err := LoadResumeWorkflow(filepath.Join(filepath.Dir(path), "unexpected.yaml")); err == nil {
		t.Fatal("malformed snapshot identifier accepted")
	}
}
