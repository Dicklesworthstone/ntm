package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func TestPersonaAgentPresentationAll11Harnesses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		alias     string
		wantLabel string
	}{
		{alias: "claude_code", wantLabel: "cc"},
		{alias: "openai-codex", wantLabel: "cod"},
		{alias: "google-gemini", wantLabel: "gmi"},
		{alias: "google-antigravity", wantLabel: "agy"},
		{alias: "xai-grok-build", wantLabel: "grok"},
		{alias: "cursor", wantLabel: "cursor"},
		{alias: "ws", wantLabel: "windsurf"},
		{alias: "aider", wantLabel: "aider"},
		{alias: "opencode-cli", wantLabel: "oc"},
		{alias: "oh-my-pi", wantLabel: "omp"},
		{alias: "ollama", wantLabel: "ollama"},
		{alias: "user", wantLabel: "user"},
	}

	for _, tc := range cases {
		_, _, label := personaAgentPresentation(tc.alias, theme.Plain, icons.ASCII)
		if strings.Compare(label, tc.wantLabel) != 0 {
			t.Fatalf("personaAgentPresentation(%q) label = %q, want %q", tc.alias, label, tc.wantLabel)
		}
		rendered := formatAgentType(tc.alias, theme.Plain, icons.ASCII)
		if !strings.HasSuffix(rendered, " "+tc.wantLabel) {
			t.Fatalf("formatAgentType(%q) = %q, want suffix %q", tc.alias, rendered, " "+tc.wantLabel)
		}
	}
}

func TestKernelSessionsAdoptRegistrationAndValidation(t *testing.T) {
	t.Parallel()

	var (
		cmd kernel.Command
		ok  bool
	)
	for _, candidate := range kernel.List() {
		if strings.Compare(candidate.Name, "sessions.adopt") == 0 {
			cmd = candidate
			ok = true
			break
		}
	}
	if !ok {
		t.Fatal("expected sessions.adopt registered in kernel")
	}
	if cmd.REST == nil || strings.Compare(cmd.REST.Method, "POST") != 0 || strings.Compare(cmd.REST.Path, "/api/v1/sessions/{id}/adopt") != 0 {
		t.Fatalf("unexpected REST binding for sessions.adopt: %+v", cmd.REST)
	}

	// Missing pane assignments must fail closed with invalid input error.
	_, err := kernel.Run(context.Background(), "sessions.adopt", map[string]any{
		"session": "wave14_nonexistent_session",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "invalid") {
		t.Fatalf("expected invalid adopt input error when panes omitted, got %v", err)
	}

	// Valid pane spec against nonexistent tmux session returns not-found or dependency-missing error.
	_, err = kernel.Run(context.Background(), "sessions.adopt", map[string]any{
		"session": "wave14_nonexistent_session_99999",
		"panes":   map[string]string{"cc": "0,1", "agy": "2"},
		"dry_run": true,
	})
	if err == nil {
		t.Fatal("expected error adopting nonexistent session")
	}
}

func TestWave14RobotCLIFlagsAndSubprocessDispatch(t *testing.T) {
	t.Parallel()
	homeDir := t.TempDir()
	configHome := t.TempDir()
	dataHome := t.TempDir()
	repoDir := t.TempDir()

	gitInit := exec.Command("git", "init", repoDir)
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, string(out))
	}
	gitCfg := exec.Command("git", "-C", repoDir, "config", "core.hooksPath", ".git/hooks")
	if out, err := gitCfg.CombinedOutput(); err != nil {
		t.Fatalf("git config core.hooksPath: %v (%s)", err, string(out))
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.ntm): %v", err)
	}
	personasToml := `
[[personas]]
name = "wave14-agy-architect"
agent_type = "agy"
description = "Antigravity system architect"
tags = ["wave14"]
`
	if err := os.WriteFile(filepath.Join(repoDir, ".ntm", "personas.toml"), []byte(personasToml), 0o644); err != nil {
		t.Fatalf("WriteFile(personas.toml): %v", err)
	}

	runRobotSubprocess := func(args ...string) ([]byte, int) {
		t.Helper()
		rawArgs, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
		cmd.Dir = repoDir
		cmd.Env = envWithOverrides(os.Environ(),
			"HOME="+homeDir,
			"XDG_CONFIG_HOME="+configHome,
			"XDG_DATA_HOME="+dataHome,
			"NTM_NO_COLOR=1",
			"NTM_CONFIG=",
			"NTM_ROBOT_FORMAT=json",
			"NTM_OUTPUT_FORMAT=",
			"TOON_DEFAULT_FORMAT=",
			"NTM_ROBOT_VERBOSITY=",
			"NTM_ROBOT_CONTRACT_ARGS="+string(rawArgs),
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		exitCode := 0
		if runErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(runErr, &exitErr) {
				t.Fatalf("run subprocess %v: %v (stderr=%s)", args, runErr, stderr.String())
			}
			exitCode = exitErr.ExitCode()
		}
		return stdout.Bytes(), exitCode
	}

	// 1. --robot-redact inline secret redaction (never leaks synthetic credential)
	syntheticCred := "sk" + "-live-" + strings.Repeat("b2", 16)
	redactRaw, code := runRobotSubprocess("--robot-redact=Authorization: Bearer " + syntheticCred)
	if code != 0 {
		t.Fatalf("--robot-redact exit=%d, out=%s", code, string(redactRaw))
	}
	var redactOut robot.RedactOutput
	if err := json.Unmarshal(redactRaw, &redactOut); err != nil {
		t.Fatalf("decode --robot-redact: %v (%s)", err, string(redactRaw))
	}
	if !redactOut.Success || redactOut.FindingCount == 0 || strings.Contains(redactOut.Output, syntheticCred) {
		t.Fatalf("unexpected --robot-redact output: %+v", redactOut)
	}

	// 2. --robot-hooks status against repoDir
	hooksRaw, code := runRobotSubprocess("--robot-hooks", "--hooks-action=status", "--project="+repoDir)
	if code != 0 {
		t.Fatalf("--robot-hooks status exit=%d, out=%s", code, string(hooksRaw))
	}
	var hooksOut robot.HooksOutput
	if err := json.Unmarshal(hooksRaw, &hooksOut); err != nil {
		t.Fatalf("decode --robot-hooks: %v (%s)", err, string(hooksRaw))
	}
	if !hooksOut.Success || len(hooksOut.Hooks) != 5 {
		t.Fatalf("unexpected --robot-hooks status output: %+v", hooksOut)
	}

	// 3. --robot-personas filtering by 11-harness alias (antigravity -> agy)
	personasRaw, code := runRobotSubprocess("--robot-personas", "--personas-agent=antigravity", "--personas-tag=wave14", "--project="+repoDir)
	if code != 0 {
		t.Fatalf("--robot-personas exit=%d, out=%s", code, string(personasRaw))
	}
	var personasOut robot.PersonasOutput
	if err := json.Unmarshal(personasRaw, &personasOut); err != nil {
		t.Fatalf("decode --robot-personas: %v (%s)", err, string(personasRaw))
	}
	if !personasOut.Success || personasOut.Count != 1 || strings.Compare(personasOut.Personas[0].Name, "wave14-agy-architect") != 0 {
		t.Fatalf("unexpected --robot-personas output: %+v", personasOut)
	}

	// 4. --robot-adopt missing pane flags -> exit 1 + INVALID_FLAG
	adoptBadRaw, code := runRobotSubprocess("--robot-adopt=wave14_missing_panes", "--dry-run")
	if code == 0 {
		t.Fatalf("expected non-zero exit for --robot-adopt without pane flags: %s", string(adoptBadRaw))
	}
	var adoptBadOut robot.AdoptOutput
	if err := json.Unmarshal(adoptBadRaw, &adoptBadOut); err != nil {
		t.Fatalf("decode --robot-adopt error: %v (%s)", err, string(adoptBadRaw))
	}
	if adoptBadOut.Success || !strings.EqualFold(adoptBadOut.ErrorCode, robot.ErrCodeInvalidFlag) {
		t.Fatalf("expected INVALID_FLAG from --robot-adopt without pane flags, got %+v", adoptBadOut)
	}

	// 5. Live non-dry-run --robot-adopt against the isolated package tmux server must emit
	// pure JSON stdout with zero human Agent Mail registration chatter prepended.
	if err := tmux.EnsureInstalled(); err == nil {
		sessName := "wave14_live_adopt"
		if createErr := tmux.CreateSession(sessName, repoDir); createErr == nil {
			t.Cleanup(func() {
				_ = tmux.KillSession(sessName)
			})
			adoptLiveRaw, capErr := captureStdout(t, func() error {
				out, execErr := executeAdoptRobot(context.Background(), robot.AdoptInput{
					Session:      sessName,
					ByWindow:     true,
					ByWindowType: "agy",
				})
				if execErr != nil {
					return execErr
				}
				return robot.PrintAdoptOutput(out)
			})
			if capErr != nil {
				t.Fatalf("live --robot-adopt failed: %v", capErr)
			}
			var adoptLiveOut robot.AdoptOutput
			if err := json.Unmarshal([]byte(adoptLiveRaw), &adoptLiveOut); err != nil {
				t.Fatalf("expected pure JSON stdout from live --robot-adopt, got %v (%q)", err, adoptLiveRaw)
			}
			if !adoptLiveOut.Success || len(adoptLiveOut.AdoptedPanes) != 1 || adoptLiveOut.Agents.Agy != 1 {
				t.Fatalf("unexpected live --robot-adopt output: %+v", adoptLiveOut)
			}
		}
	}
}
