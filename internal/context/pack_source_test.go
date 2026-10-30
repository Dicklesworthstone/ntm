package context

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the same Build path used by ntm context build --files, with every
// external context provider absent. Source content must still be available.
func TestBuildSourceContextWithoutExternalTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := NewContextPackBuilder(nil)
	b.ClearCache()
	t.Cleanup(b.ClearCache)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package demo\n// native-source-proof\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, agentType := range []string{"cod", "cc"} {
		pack, err := b.Build(context.Background(), BuildOptions{
			AgentType: agentType, ProjectDir: dir, Files: []string{"*.go"},
		})
		if err != nil {
			t.Fatal(err)
		}
		source := pack.Components["s2p"]
		if source == nil || source.Error != "" {
			t.Fatalf("%s source component unavailable: %+v", agentType, source)
		}
		var text string
		if err := json.Unmarshal(source.Data, &text); err != nil {
			t.Fatalf("source component is not a JSON string: %v", err)
		}
		if !strings.Contains(text, "native-source-proof") || !strings.Contains(pack.RenderedPrompt, "native-source-proof") {
			t.Fatalf("%s prompt omitted real source: %s", agentType, pack.RenderedPrompt)
		}
		if _, err := json.Marshal(pack); err != nil {
			t.Fatalf("pack cannot be returned by --json: %v", err)
		}
		if pack.TokenCount > GetTokenBudget(agentType) {
			t.Fatalf("pack exceeded agent budget: %d", pack.TokenCount)
		}
	}
}

func TestSourceContextCannotEscapeProjectRoot(t *testing.T) {
	b := NewContextPackBuilder(nil)
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside-private-content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, file := range []string{"escape.txt", "../secret.txt", outside} {
		comp := b.buildS2PComponent(context.Background(), dir, []string{file}, 1000)
		if comp.Error == "" || strings.Contains(string(comp.Data), "outside-private-content") {
			t.Fatalf("escaped source root with %q: %+v", file, comp)
		}
	}
}

func TestBuildRefusesRemoteProjectBeforePreparingComponents(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("LOCAL_PROJECT_SOURCE_MUST_NOT_LEAK"), 0600); err != nil {
		t.Fatal(err)
	}
	b := NewContextPackBuilder(nil)
	b.ClearCache()
	t.Cleanup(b.ClearCache)
	for _, files := range [][]string{nil, {"README.md"}, {"*.md"}} {
		pack, err := b.Build(t.Context(), BuildOptions{
			AgentType: "cod", ProjectDir: dir, Remote: true, Files: files, Task: "remote task",
		})
		if !errors.Is(err, ErrRemoteContextPreparation) || pack != nil {
			t.Fatalf("remote project produced local context for %v: pack=%+v err=%v", files, pack, err)
		}
	}
	if size, _ := b.CacheStats(); size != 0 {
		t.Fatal("refused remote preparation published a cached pack")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if pack, err := b.Build(ctx, BuildOptions{Remote: true}); !errors.Is(err, context.Canceled) || pack != nil {
		t.Fatalf("remote refusal hid cancellation: pack=%+v err=%v", pack, err)
	}
}
