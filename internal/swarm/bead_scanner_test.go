package swarm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewBeadScanner(t *testing.T) {
	bs := NewBeadScanner("/test/dir")

	if bs.BaseDir != "/test/dir" {
		t.Errorf("expected BaseDir /test/dir, got %s", bs.BaseDir)
	}
	if bs.brPath != "br" {
		t.Errorf("expected brPath br, got %s", bs.brPath)
	}
	if bs.Parallelism != 4 {
		t.Errorf("expected Parallelism 4, got %d", bs.Parallelism)
	}
}

func TestNewBeadScannerWithOptions(t *testing.T) {
	bs := NewBeadScanner("/test/dir",
		WithBrPath("/custom/br"),
		WithParallelism(8),
		WithExplicitProjects([]string{"proj1", "proj2"}),
	)

	if bs.brPath != "/custom/br" {
		t.Errorf("expected brPath /custom/br, got %s", bs.brPath)
	}
	if bs.Parallelism != 8 {
		t.Errorf("expected Parallelism 8, got %d", bs.Parallelism)
	}
	if len(bs.ExplicitProjects) != 2 {
		t.Errorf("expected 2 explicit projects, got %d", len(bs.ExplicitProjects))
	}
}

func TestIsProject(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Test directory without .git or .beads
	emptyDir := filepath.Join(tmpDir, "empty")
	if err := os.Mkdir(emptyDir, 0755); err != nil {
		t.Fatal(err)
	}
	if isProject(emptyDir) {
		t.Error("empty directory should not be a project")
	}

	// Test directory with .git
	gitDir := filepath.Join(tmpDir, "gitproject")
	if err := os.MkdirAll(filepath.Join(gitDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if !isProject(gitDir) {
		t.Error("directory with .git should be a project")
	}

	// Test directory with .beads
	beadsDir := filepath.Join(tmpDir, "beadsproject")
	if err := os.MkdirAll(filepath.Join(beadsDir, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if !isProject(beadsDir) {
		t.Error("directory with .beads should be a project")
	}
}

func TestDiscoverProjects(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()

	// Create some project directories
	proj1 := filepath.Join(tmpDir, "project1")
	if err := os.MkdirAll(filepath.Join(proj1, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	proj2 := filepath.Join(tmpDir, "project2")
	if err := os.MkdirAll(filepath.Join(proj2, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a non-project directory
	nonProj := filepath.Join(tmpDir, "notaproject")
	if err := os.Mkdir(nonProj, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a hidden directory (should be skipped)
	hiddenDir := filepath.Join(tmpDir, ".hidden")
	if err := os.MkdirAll(filepath.Join(hiddenDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a file (should be skipped)
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	bs := NewBeadScanner(tmpDir)
	paths, err := bs.discoverProjects()
	if err != nil {
		t.Fatal(err)
	}

	if len(paths) != 2 {
		t.Errorf("expected 2 projects, got %d: %v", len(paths), paths)
	}

	// Check that both projects were found
	foundProj1, foundProj2 := false, false
	for _, p := range paths {
		if p == proj1 {
			foundProj1 = true
		}
		if p == proj2 {
			foundProj2 = true
		}
	}

	if !foundProj1 || !foundProj2 {
		t.Errorf("did not find expected projects: foundProj1=%v, foundProj2=%v", foundProj1, foundProj2)
	}
}

func TestDiscoverProjectsExplicit(t *testing.T) {
	tmpDir := t.TempDir()

	// Create projects
	proj1 := filepath.Join(tmpDir, "project1")
	if err := os.MkdirAll(filepath.Join(proj1, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	proj2 := filepath.Join(tmpDir, "project2")
	if err := os.MkdirAll(filepath.Join(proj2, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Test with explicit project paths
	bs := NewBeadScanner(tmpDir, WithExplicitProjects([]string{"project1"}))
	paths, err := bs.discoverProjects()
	if err != nil {
		t.Fatal(err)
	}

	if len(paths) != 1 {
		t.Errorf("expected 1 project, got %d", len(paths))
	}
	if len(paths) > 0 && paths[0] != proj1 {
		t.Errorf("expected %s, got %s", proj1, paths[0])
	}

	// Test with absolute path
	bs2 := NewBeadScanner(tmpDir, WithExplicitProjects([]string{proj2}))
	paths2, err := bs2.discoverProjects()
	if err != nil {
		t.Fatal(err)
	}

	if len(paths2) != 1 {
		t.Errorf("expected 1 project, got %d", len(paths2))
	}
}

func TestScanEmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	bs := NewBeadScanner(tmpDir)
	result, err := bs.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if result.TotalProjects != 0 {
		t.Errorf("expected 0 projects, got %d", result.TotalProjects)
	}
	if result.TotalBeads != 0 {
		t.Errorf("expected 0 beads, got %d", result.TotalBeads)
	}
}

func TestScanWithBeads(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a project with beads
	proj := filepath.Join(tmpDir, "project")
	beadsDir := filepath.Join(proj, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a mock issues.jsonl file
	issues := []map[string]interface{}{
		{"id": "bd-1", "status": "open", "title": "Issue 1"},
		{"id": "bd-2", "status": "open", "title": "Issue 2"},
		{"id": "bd-3", "status": "closed", "title": "Issue 3"},
	}
	issuesJSON, _ := json.Marshal(issues)
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), issuesJSON, 0644); err != nil {
		t.Fatal(err)
	}

	bs := NewBeadScanner(tmpDir)

	// ScanProject should return 0 if br binary is not available
	// (which is expected in test environment)
	result, err := bs.ScanProject(context.Background(), proj)
	if err != nil {
		t.Fatal(err)
	}

	// In test environment without br binary, we expect 0 beads
	// The function gracefully handles missing br
	if result.Path != proj {
		t.Errorf("expected path %s, got %s", proj, result.Path)
	}
	if result.Name != "project" {
		t.Errorf("expected name 'project', got %s", result.Name)
	}
}

func TestScanContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple projects
	for i := 0; i < 10; i++ {
		projDir := filepath.Join(tmpDir, "project"+string(rune('0'+i)))
		if err := os.MkdirAll(filepath.Join(projDir, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	bs := NewBeadScanner(tmpDir, WithParallelism(2))

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := bs.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// With cancelled context, we may get partial results
	// The key is that it doesn't hang
	_ = result
}

func TestScanResultDuration(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a project
	projDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(filepath.Join(projDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	bs := NewBeadScanner(tmpDir)
	result, err := bs.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Verify duration is set
	if result.ScanDuration == 0 {
		t.Error("expected non-zero scan duration")
	}
}

func TestBeadScanner_ProjectBeadCountFromPath(t *testing.T) {
	pbc := ProjectBeadCountFromPath("/path/to/project", 42)

	if pbc.Path != "/path/to/project" {
		t.Errorf("expected path /path/to/project, got %s", pbc.Path)
	}
	if pbc.Name != "project" {
		t.Errorf("expected name 'project', got %s", pbc.Name)
	}
	if pbc.OpenBeads != 42 {
		t.Errorf("expected 42 beads, got %d", pbc.OpenBeads)
	}
}

func TestScanWithTimeout(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a project
	projDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(filepath.Join(projDir, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	bs := NewBeadScanner(tmpDir)

	// Use a timeout context
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := bs.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Verify we got a result within timeout
	if result.TotalProjects != 1 {
		t.Errorf("expected 1 project, got %d", result.TotalProjects)
	}
}
