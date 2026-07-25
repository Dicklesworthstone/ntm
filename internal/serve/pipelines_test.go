package serve

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Workflow paths must be confined to the project directory. pipeline.ParseFile
// os.ReadFile's the path before checking its extension, so an unconfined path let
// /pipelines/validate — which needs only pipelines:read, a permission RoleViewer
// holds — read arbitrary files and distinguish "permission denied" from "no such
// file" through the echoed load error.
func TestResolveWorkflowPathConfinesToProjectDir(t *testing.T) {
	projectDir := t.TempDir()
	outside := t.TempDir()

	inside := filepath.Join(projectDir, ".ntm", "pipelines", "review.yaml")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(inside, []byte("name: review\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	outsideFile := filepath.Join(outside, "secret.yaml")
	if err := os.WriteFile(outsideFile, []byte("name: secret\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	srv := &Server{projectDir: projectDir}

	t.Run("accepts a path inside the project", func(t *testing.T) {
		got, err := srv.resolveWorkflowPath(inside)
		if err != nil {
			t.Fatalf("resolveWorkflowPath: %v", err)
		}
		if !pathWithinRoot(mustEval(t, projectDir), got) {
			t.Fatalf("resolved %q is not within %q", got, projectDir)
		}
	})

	t.Run("accepts a relative path", func(t *testing.T) {
		got, err := srv.resolveWorkflowPath(".ntm/pipelines/review.yaml")
		if err != nil {
			t.Fatalf("resolveWorkflowPath: %v", err)
		}
		if filepath.Base(got) != "review.yaml" {
			t.Fatalf("resolved %q, want the review.yaml fixture", got)
		}
	})

	for name, path := range map[string]string{
		"absolute path outside": outsideFile,
		"parent traversal":      "../../etc/passwd",
		"ssh key":               "/root/.ssh/id_rsa",
		"empty":                 "   ",
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			if got, err := srv.resolveWorkflowPath(path); err == nil {
				t.Fatalf("resolveWorkflowPath(%q) = %q, want an error", path, got)
			}
		})
	}

	t.Run("rejects a symlink escaping the project", func(t *testing.T) {
		link := filepath.Join(projectDir, "escape.yaml")
		if err := os.Symlink(outsideFile, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got, err := srv.resolveWorkflowPath(link); err == nil {
			t.Fatalf("resolveWorkflowPath(symlink) = %q, want an error", got)
		}
	})

	t.Run("rejects when no project dir is configured", func(t *testing.T) {
		bare := &Server{}
		if _, err := bare.resolveWorkflowPath(inside); err == nil {
			t.Fatal("a server with no project directory must not resolve a workflow path")
		}
	})
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// getMailClient must not hold the server write lock across the availability
// probe. redactionMiddleware takes s.mu for reading on every request and Go's
// RWMutex is writer-preferring, so a probe under the write lock stalled the whole
// HTTP server — continuously, because only successful probes were cached and every
// mail request therefore re-probed.
func TestGetMailClientDoesNotBlockReadersDuringProbe(t *testing.T) {
	srv := &Server{projectDir: t.TempDir()}

	// Agent Mail is not reachable in tests, so the probe takes the unavailable
	// path. While it runs, a reader must still be able to acquire s.mu.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for i := 0; i < 50; i++ {
			srv.mu.RLock()
			_ = srv.projectDir
			srv.mu.RUnlock()
			time.Sleep(time.Millisecond)
		}
	}()

	client, err := srv.getMailClient()
	if err != nil {
		t.Fatalf("getMailClient: %v", err)
	}
	if client != nil {
		t.Skip("Agent Mail is reachable in this environment; the unavailable path is not exercised")
	}

	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("readers were starved while the availability probe ran")
	}

	// The negative verdict is cached, so a second call returns immediately without
	// re-probing.
	srv.mu.RLock()
	until := srv.mailUnavailableUntil
	srv.mu.RUnlock()
	if until.IsZero() {
		t.Fatal("an unavailable verdict was not cached; every mail request would re-probe")
	}

	start := time.Now()
	if _, err := srv.getMailClient(); err != nil {
		t.Fatalf("second getMailClient: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("second call took %s; the cached verdict was not used", elapsed)
	}
}
