package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Exercise the shipped build -> SQLite -> inject --pack workflow. Only live
// tmux resolution/delivery is substituted; source preparation and storage are
// real, and no optional context tool is available.
func TestContextBuildCommandNativeSource(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("NTM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	oldJSON, oldCfg := jsonOutput, cfg
	jsonOutput, cfg = true, nil
	t.Cleanup(func() { jsonOutput, cfg = oldJSON, oldCfg })
	builder := ntmctx.NewContextPackBuilder(nil)
	builder.ClearCache()
	t.Cleanup(builder.ClearCache)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// cli-native-source-proof\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Directories and ** globs must survive the real command, pack rendering,
	// SQLite round-trip, and the stored-artifact delivery path below.
	for file, content := range map[string]string{
		"src/nested/worker.go": "package worker\n// cli-directory-source-proof\n",
		"lib/root.go":          "package lib\n// cli-zero-depth-glob-proof\n",
		"lib/deep/helper.go":   "package helper\n// cli-recursive-glob-proof\n",
		"lib/deep/ignored.txt": "cli-nonmatching-file-must-not-be-included",
	} {
		name := filepath.Join(dir, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	capture, err := os.CreateTemp(t.TempDir(), "context-output-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	oldStdout := os.Stdout
	os.Stdout = capture
	t.Cleanup(func() { os.Stdout = oldStdout })
	cmd := newContextCmd()
	cmd.SetArgs([]string{"build", "--agent", "cod", "--files", "main.go,src,lib/**/*.go"})
	err = cmd.Execute()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	var pack ntmctx.ContextPackFull
	if err := json.Unmarshal(raw, &pack); err != nil {
		t.Fatalf("context build did not emit a JSON pack: %v: %s", err, raw)
	}
	source := pack.Components["s2p"]
	if source == nil || source.Error != "" || !strings.Contains(pack.RenderedPrompt, "cli-native-source-proof") {
		t.Fatalf("context build omitted native source: %s", raw)
	}
	for _, marker := range []string{"cli-directory-source-proof", "cli-zero-depth-glob-proof", "cli-recursive-glob-proof"} {
		if !strings.Contains(pack.RenderedPrompt, marker) {
			t.Fatalf("context build omitted %s: %s", marker, raw)
		}
	}
	if strings.Contains(pack.RenderedPrompt, "cli-nonmatching-file-must-not-be-included") {
		t.Fatalf("context build included a nonmatching file: %s", raw)
	}

	store, err := state.Open("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	stored, err := store.GetContextPack(pack.ID)
	if err != nil || stored == nil || stored.RenderedPrompt != pack.RenderedPrompt {
		t.Fatalf("build printed an unusable pack ID: %+v, %v", stored, err)
	}
	// Pin database timestamp round-tripping as well as same-store idempotency.
	if err := persistContextPack(context.Background(), store, &pack.ContextPack); err != nil {
		t.Fatalf("persisting the identical stored artifact must be a no-op: %v", err)
	}
	calls := 0
	deps := contextInjectDeps{
		resolve: func(context.Context, string) (string, error) { return "demo", nil },
		panes: func(string) ([]tmux.Pane, error) {
			return []tmux.Pane{{ID: "%2", Index: 2, Type: tmux.AgentCodex}}, nil
		},
		load: store.GetContextPack,
		send: func(_ context.Context, pane tmux.Pane, text string) error {
			calls++
			if pane.ID != "%2" || text != pack.RenderedPrompt {
				t.Fatalf("stored source artifact changed during delivery to %+v", pane)
			}
			return nil
		},
	}
	var out bytes.Buffer
	inject := newContextInjectCmdWithDeps(&deps)
	inject.SetOut(&out)
	inject.SetArgs([]string{"demo", "--pack", pack.ID, "--pane", "2"})
	if err := inject.Execute(); err != nil {
		t.Fatal(err)
	}
	var result ContextInjectResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("inject did not emit one JSON envelope: %v: %s", err, out.String())
	}
	if calls != 1 || !result.Success || result.Mode != "pack" || len(result.Deliveries) != 1 || result.Deliveries[0].PackID != pack.ID {
		t.Fatalf("build/store/inject workflow failed: calls=%d result=%+v", calls, result)
	}
}
