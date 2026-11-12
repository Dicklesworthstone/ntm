package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGitTestCommand(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func assertStringEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestIsGitRepository(t *testing.T) {
	isolateGitConfig(t)

	repo := setupRepoWithInitialBranch(t, "main")
	ok, err := IsGitRepository(t.Context(), repo)
	if err != nil {
		t.Fatalf("IsGitRepository(repo): %v", err)
	}
	if !ok {
		t.Fatal("IsGitRepository(repo) = false, want true")
	}

	plain := t.TempDir()
	ok, err = IsGitRepository(t.Context(), plain)
	if err != nil {
		t.Fatalf("IsGitRepository(plain): %v", err)
	}
	if ok {
		t.Fatal("IsGitRepository(plain) = true, want false")
	}

	ok, err = IsGitRepository(t.Context(), "")
	if err != nil || ok {
		t.Fatalf("IsGitRepository(\"\") = (%v, %v), want (false, nil)", ok, err)
	}

	missing := filepath.Join(plain, "does-not-exist")
	ok, err = IsGitRepository(t.Context(), missing)
	if err != nil || ok {
		t.Fatalf("IsGitRepository(missing) = (%v, %v), want (false, nil)", ok, err)
	}

	filePath := filepath.Join(plain, "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err = IsGitRepository(t.Context(), filePath)
	if err != nil || ok {
		t.Fatalf("IsGitRepository(file) = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestIsGitRepository_ContextValidation(t *testing.T) {
	if _, err := IsGitRepository(nil, t.TempDir()); err == nil { //nolint:staticcheck // intentional nil-context contract test
		t.Fatal("IsGitRepository(nil) succeeded, want error")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := IsGitRepository(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("IsGitRepository(canceled) = %v, want context.Canceled", err)
	}
}
