//go:build !windows

package cass

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func contextSearchShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func TestContextSearchProductionExecutorRetrievesBothScopes(t *testing.T) {
	project := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "queries")
	local := fmt.Sprintf(`{"hits":[{"source_path":"opaque-local","workspace":%q,"created_at":1792000000000,"snippet":"LOCAL_HISTORY","score":0.9}],"total_matches":1}`, project)
	global := `{"hits":[{"source_path":"global","content":"GLOBAL_HISTORY","score":1}],"total_matches":99}`
	body := "#!/bin/sh\nscope=global\nfor arg in \"$@\"; do\n case \"$arg\" in\n --) break ;;\n --workspace=*) scope=local ;;\n esac\ndone\n"
	body += "printf '%s\\n' \"$scope\" >> " + contextSearchShellQuote(logPath) + "\n"
	body += "case \"$scope\" in\n local) printf '%s' " + contextSearchShellQuote(local) + ";;\n *) printf '%s' " + contextSearchShellQuote(global) + ";;\nesac\n"
	binary := filepath.Join(t.TempDir(), "cass")
	if err := os.WriteFile(binary, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	client := NewClient(WithBinaryPath(binary), WithTimeout(3*time.Second))
	// The old single global search demonstrably cannot return this history.
	before, err := client.Search(context.Background(), SearchOptions{Query: "database transactions", Limit: 1})
	if err != nil || len(before.Hits) != 1 || before.Hits[0].SourcePath != "global" {
		t.Fatalf("invalid baseline fixture: %+v %v", before, err)
	}
	out, err := client.SearchContext(context.Background(), ContextSearchOptions{Query: "database transactions", Limit: 1, Workspace: project})
	if err != nil || len(out.Hits) != 2 || out.Hits[0].SourcePath != "global" || out.Hits[1].Workspace != project || out.Hits[1].Snippet != "LOCAL_HISTORY" || out.Hits[1].CreatedAt == nil || out.TotalMatches != 99 {
		t.Fatalf("real executor lost project candidates or indexed evidence: %+v %v", out, err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil || strings.Count(string(log), "global\n") != 2 || strings.Count(string(log), "local\n") != 1 {
		t.Fatalf("unexpected actual invocation count: %q %v", log, err)
	}
}

func TestContextSearchProductionCancellationStopsBothChildren(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "cass")
	body := "#!/bin/sh\nscope=global\nfor arg in \"$@\"; do\n case \"$arg\" in\n --) break ;;\n --workspace=*) scope=local ;;\n esac\ndone\n"
	body += "printf started > " + contextSearchShellQuote(root) + "/\"$scope\"\nexec sleep 30\n"
	if err := os.WriteFile(binary, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	client := NewClient(WithBinaryPath(binary), WithTimeout(30*time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.SearchContext(ctx, ContextSearchOptions{Query: "database transactions", Workspace: root})
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, a := os.Stat(filepath.Join(root, "global"))
		_, b := os.Stat(filepath.Join(root, "local"))
		if a == nil && b == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("both subprocesses did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("lost parent cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation retained a 30s child search")
	}
}
