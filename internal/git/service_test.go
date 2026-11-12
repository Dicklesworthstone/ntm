package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitRepositoryAndProjectRootParity(t *testing.T) {
	isolateGitConfig(t)

	repo := setupRepoWithInitialBranch(t, "main")
	nested := filepath.Join(repo, "pkg", "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	isRepo, err := IsGitRepository(t.Context(), nested)
	if err != nil {
		t.Fatalf("IsGitRepository(nested): %v", err)
	}
	if !isRepo {
		t.Fatal("nested directory inside git repo should report IsGitRepository=true")
	}

	root, err := FindProjectRoot(nested)
	if err != nil {
		t.Fatalf("FindProjectRoot(nested): %v", err)
	}
	realRepo, _ := filepath.EvalSymlinks(repo)
	realRoot, _ := filepath.EvalSymlinks(root)
	assertStringEqual(t, realRoot, realRepo)
}
