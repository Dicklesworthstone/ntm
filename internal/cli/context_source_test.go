package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
)

// Test the shipped command, not just a file-preparation helper. In particular,
// --files must work when s2p and every optional context tool are absent.
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

	capture, err := os.CreateTemp(t.TempDir(), "context-output-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	oldStdout := os.Stdout
	os.Stdout = capture
	t.Cleanup(func() { os.Stdout = oldStdout })
	cmd := newContextCmd()
	cmd.SetArgs([]string{"build", "--agent", "cod", "--files", "main.go"})
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
}
