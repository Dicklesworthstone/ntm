package robot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestAdoptTypesSummaryAndCounts(t *testing.T) {
	t.Parallel()

	var counts AdoptedAgentCounts
	for _, raw := range []string{
		"claude", "codex", "gemini", "antigravity", "xai-grok-build",
		"cursor", "ws", "aider", "opencode", "oh-my-pi", "ollama", "user",
	} {
		counts.Increment(agent.AgentType(raw))
	}
	if counts.Total() != 12 {
		t.Fatalf("counts.Total() = %d, want 12", counts.Total())
	}
	wantSummary := "cc:1, cod:1, gmi:1, agy:1, grok:1, cursor:1, windsurf:1, aider:1, oc:1, omp:1, ollama:1, user:1"
	if got := counts.Summary(); strings.Compare(got, wantSummary) != 0 {
		t.Fatalf("counts.Summary() = %q, want %q", got, wantSummary)
	}
}

func TestGetRedactPreviewNeverEmitsRawSecrets(t *testing.T) {
	t.Parallel()

	syntheticCred := "sk" + "-live-" + strings.Repeat("9a", 16)
	input := "line one clean\nAuthorization: Bearer " + syntheticCred + "\n"

	// Even if caller passes ModeOff, preview must force ModeRedact and never leak syntheticCred.
	offCfg := redaction.Config{Mode: redaction.ModeOff}
	out, err := GetRedactPreview(RedactOptions{
		Text:      input,
		RedactCfg: &offCfg,
	})
	if err != nil {
		t.Fatalf("GetRedactPreview returned error: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected success=true, got error=%q code=%q", out.Error, out.ErrorCode)
	}
	if out.FindingCount == 0 || len(out.Findings) == 0 {
		t.Fatalf("expected >=1 secret finding, got %+v", out)
	}
	if strings.Contains(out.Output, syntheticCred) {
		t.Fatalf("redacted output leaked synthetic credential: %q", out.Output)
	}
	for _, f := range out.Findings {
		if strings.Contains(f.Redacted, syntheticCred) {
			t.Fatalf("finding leaked synthetic credential: %+v", f)
		}
		if f.Line != 2 {
			t.Fatalf("finding line = %d, want 2 (%+v)", f.Line, f)
		}
	}

	// File mode
	tmpFile := filepath.Join(t.TempDir(), "secret_notes.txt")
	if err := os.WriteFile(tmpFile, []byte(input), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fileOut, err := GetRedactPreview(RedactOptions{File: tmpFile})
	if err != nil {
		t.Fatalf("GetRedactPreview(file): %v", err)
	}
	if !fileOut.Success || !strings.EqualFold(fileOut.Source, "file") || strings.Contains(fileOut.Output, syntheticCred) {
		t.Fatalf("unexpected file redact output: %+v", fileOut)
	}

	// Planted negatives: empty input and mutually exclusive flags
	emptyOut, err := GetRedactPreview(RedactOptions{})
	if err != nil {
		t.Fatalf("GetRedactPreview(empty): %v", err)
	}
	if emptyOut.Success || !strings.EqualFold(emptyOut.ErrorCode, ErrCodeInvalidFlag) {
		t.Fatalf("expected INVALID_FLAG for empty redact options, got %+v", emptyOut)
	}

	bothOut, err := GetRedactPreview(RedactOptions{Text: "hello", File: tmpFile})
	if err != nil {
		t.Fatalf("GetRedactPreview(both): %v", err)
	}
	if bothOut.Success || !strings.EqualFold(bothOut.ErrorCode, ErrCodeInvalidFlag) {
		t.Fatalf("expected INVALID_FLAG for mutually exclusive redact options, got %+v", bothOut)
	}
}

func TestGetHooksStatusAndRun(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	gitInit := exec.Command("git", "init", repoDir)
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v (%s)", err, string(out))
	}
	// Pin local hooksPath so global core.hooksPath (if any) cannot interfere.
	gitCfg := exec.Command("git", "-C", repoDir, "config", "core.hooksPath", ".git/hooks")
	if out, err := gitCfg.CombinedOutput(); err != nil {
		t.Fatalf("git config core.hooksPath failed: %v (%s)", err, string(out))
	}

	mgr, err := hooks.NewManager(repoDir)
	if err != nil {
		t.Fatalf("hooks.NewManager: %v", err)
	}
	if err := mgr.Install(hooks.HookPreCommit, true); err != nil {
		t.Fatalf("Install(pre-commit): %v", err)
	}

	statusOut, err := GetHooks(context.Background(), HooksOptions{
		Action:     "status",
		ProjectDir: repoDir,
	})
	if err != nil {
		t.Fatalf("GetHooks(status): %v", err)
	}
	if !statusOut.Success || statusOut.InstalledCount == nil || *statusOut.InstalledCount != 1 || len(statusOut.Hooks) != 5 {
		t.Fatalf("unexpected hooks status output: %+v", statusOut)
	}

	// Valid commit-msg check
	validMsg := filepath.Join(repoDir, "VALID_MSG")
	if err := os.WriteFile(validMsg, []byte("feat(robot): wire git hooks surface\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(validMsg): %v", err)
	}
	passOut, err := GetHooks(context.Background(), HooksOptions{
		Action:     "run",
		HookType:   "commit-msg",
		HookArgs:   []string{validMsg},
		ProjectDir: repoDir,
	})
	if err != nil {
		t.Fatalf("GetHooks(run commit-msg valid): %v", err)
	}
	if !passOut.Success || passOut.Passed == nil || !*passOut.Passed {
		t.Fatalf("expected commit-msg check to pass, got %+v", passOut)
	}

	// Planted negative: commit message with merge conflict marker fails with HOOK_CHECK_FAILED
	badMsg := filepath.Join(repoDir, "BAD_MSG")
	if err := os.WriteFile(badMsg, []byte("feat: broken\n<<<<<<< HEAD\n=======\n>>>>>>> other\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(badMsg): %v", err)
	}
	failOut, err := GetHooks(context.Background(), HooksOptions{
		Action:     "run",
		HookType:   "commit-msg",
		HookArgs:   []string{badMsg},
		ProjectDir: repoDir,
	})
	if err != nil {
		t.Fatalf("GetHooks(run commit-msg bad): %v", err)
	}
	if failOut.Success || failOut.Passed == nil || *failOut.Passed || !strings.EqualFold(failOut.ErrorCode, ErrCodeHookCheckFailed) {
		t.Fatalf("expected HOOK_CHECK_FAILED on conflict marker commit-msg, got %+v", failOut)
	}

	// Planted negative: non-git directory returns INVALID_FLAG
	nonGitOut, err := GetHooks(context.Background(), HooksOptions{
		Action:     "status",
		ProjectDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("GetHooks(non-git): %v", err)
	}
	if nonGitOut.Success || !strings.EqualFold(nonGitOut.ErrorCode, ErrCodeInvalidFlag) {
		t.Fatalf("expected INVALID_FLAG on non-git dir, got %+v", nonGitOut)
	}
}

func TestGetPersonasAllHarnessesAndFilters(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	ntmDir := filepath.Join(projectDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	tomlContent := `
[[personas]]
name = "agy-navigator"
agent_type = "antigravity"
description = "Antigravity deep planner"
tags = ["wave14", "planning"]

[[personas]]
name = "grok-scout"
agent_type = "grok"
description = "Grok Build fast scout"
tags = ["wave14"]

[[personas]]
name = "omp-surgeon"
agent_type = "omp"
description = "Oh My Pi code surgeon"
tags = ["wave14"]

[[personas]]
name = "oc-verifier"
agent_type = "oc"
description = "OpenCode verifier"
tags = ["wave14"]
`
	if err := os.WriteFile(filepath.Join(ntmDir, "personas.toml"), []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("WriteFile(personas.toml): %v", err)
	}

	for _, tc := range []struct {
		filter   string
		wantName string
	}{
		{filter: "google-antigravity", wantName: "agy-navigator"},
		{filter: "xai-grok-build", wantName: "grok-scout"},
		{filter: "oh-my-pi", wantName: "omp-surgeon"},
		{filter: "opencode-cli", wantName: "oc-verifier"},
	} {
		out, err := GetPersonas(PersonasOptions{
			ProjectDir: projectDir,
			Agent:      tc.filter,
			Tag:        "wave14",
		})
		if err != nil {
			t.Fatalf("GetPersonas(%s): %v", tc.filter, err)
		}
		if !out.Success || out.Count != 1 || len(out.Personas) != 1 || strings.Compare(out.Personas[0].Name, tc.wantName) != 0 {
			t.Fatalf("GetPersonas(%s) = %+v, want single persona %q", tc.filter, out, tc.wantName)
		}
		if out.ProfileSets == nil {
			t.Fatalf("ProfileSets must never be nil")
		}
	}
}
