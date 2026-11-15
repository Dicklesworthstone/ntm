package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestNewResumeCmd_MultiHarnessFlags(t *testing.T) {
	cmd := newResumeCmd()
	for _, flagName := range []string{"cc", "cod", "gmi", "agy", "grok", "omp", "oc"} {
		if cmd.Flags().Lookup(flagName) == nil {
			t.Fatalf("expected --%s flag on resume command", flagName)
		}
	}

	dir := t.TempDir()
	projectDir := filepath.Join(dir, "resume-parity")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir projectDir: %v", err)
	}
	prevCfg := cfg
	cfg = config.Default()
	cfg.ProjectsBase = dir
	t.Cleanup(func() { cfg = prevCfg })

	handoffPath := filepath.Join(dir, "handoff.yaml")
	handoffBody := "version: \"1.0\"\nsession: resume-parity\ngoal: Verify parity\nnow: Run tests\n"
	if err := os.WriteFile(handoffPath, []byte(handoffBody), 0o600); err != nil {
		t.Fatalf("WriteFile handoff: %v", err)
	}

	cmd.SetArgs([]string{"resume-parity", "--from", handoffPath, "--spawn"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--grok") || !strings.Contains(err.Error(), "--oc") {
		t.Fatalf("expected zero-count resume --spawn error to mention --grok and --oc, got: %v", err)
	}
}

func TestResumeSpawnCounts_GrokAndOpencode(t *testing.T) {
	t.Parallel()

	counts := resumeSpawnCounts{
		CC:   1,
		Grok: 2,
		Omp:  1,
		Oc:   3,
	}
	if got := counts.total(); got != 7 {
		t.Fatalf("counts.total() = %d, want 7", got)
	}

	opts := counts.spawnOptions("proj", "/tmp/proj")
	if opts.GrokCount != 2 {
		t.Fatalf("opts.GrokCount = %d, want 2", opts.GrokCount)
	}
	if opts.OpencodeCount != 3 {
		t.Fatalf("opts.OpencodeCount = %d, want 3", opts.OpencodeCount)
	}
	if opts.OmpCount != 1 {
		t.Fatalf("opts.OmpCount = %d, want 1", opts.OmpCount)
	}

	countsWithPlugin := resumeSpawnCounts{
		Grok: 2,
		Oc:   1,
		Plugins: AgentSpecs{
			{Type: AgentType("custom"), Count: 1, Model: "m1"},
		},
	}
	if got := countsWithPlugin.total(); got != 4 {
		t.Fatalf("countsWithPlugin.total() = %d, want 4", got)
	}
	pluginOpts := countsWithPlugin.spawnOptions("proj", "/tmp/proj")
	foundGrok, foundOc := 0, 0
	for _, spec := range pluginOpts.Agents {
		switch spec.Type {
		case AgentTypeGrok:
			foundGrok++
		case AgentTypeOpencode:
			foundOc++
		}
	}
	if foundGrok != 2 || foundOc != 1 {
		t.Fatalf("plugin spawnOptions Agents = %+v, want 2 grok and 1 oc", pluginOpts.Agents)
	}
}
