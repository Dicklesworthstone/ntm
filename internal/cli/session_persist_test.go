package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestBuildAgentCommands_RendersTemplates covers the #175 unrendered-template
// launch bug: resume/restore used to pass cfg.Agents.* (raw Go templates, e.g.
// `{{memLimitPrefix}} claude ...`) straight into AgentCommands, so the shell
// tried to exec a literal command named `{{memLimitPrefix}}` and the agent
// never launched. buildAgentCommands must render the templates so a concrete
// command (no `{{`/`}}` markers) reaches the pane.
func TestBuildAgentCommands_RendersTemplates(t *testing.T) {
	prevCfg := cfg
	cfg = config.Default()
	t.Cleanup(func() { cfg = prevCfg })

	// Sanity: the configured templates really do contain template syntax,
	// otherwise this test would pass vacuously.
	if !strings.Contains(cfg.Agents.Claude, "{{") {
		t.Fatalf("precondition failed: default Claude template has no template syntax: %q", cfg.Agents.Claude)
	}

	state := &session.SessionState{Name: "demo", WorkDir: "/data/projects/demo"}
	cmds := buildAgentCommands(state)

	check := func(name, got string) {
		if got == "" {
			return // empty is fine (agent not configured / render skipped)
		}
		if strings.Contains(got, "{{") || strings.Contains(got, "}}") {
			t.Errorf("%s command still contains unrendered template markers: %q", name, got)
		}
	}
	check("claude", cmds.Claude)
	check("codex", cmds.Codex)
	check("gemini", cmds.Gemini)
	check("cursor", cmds.Cursor)
	check("windsurf", cmds.Windsurf)
	check("aider", cmds.Aider)
	check("opencode", cmds.Opencode)
	check("ollama", cmds.Ollama)

	// The rendered Claude command must actually invoke `claude` (proving the
	// template body survived rendering, not just that markers were stripped).
	if cmds.Claude == "" || !strings.Contains(cmds.Claude, "claude") {
		t.Errorf("rendered Claude command = %q, want a concrete `claude ...` invocation", cmds.Claude)
	}
}

// TestBuildAgentCommands_NilConfig verifies the helper is safe when cfg is nil
// (no config loaded): it must return empty commands rather than panicking, so
// the launch path simply skips agents.
func TestBuildAgentCommands_NilConfig(t *testing.T) {
	prevCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = prevCfg })

	cmds := buildAgentCommands(&session.SessionState{Name: "x", WorkDir: "/tmp/x"})
	if cmds.Claude != "" || cmds.Codex != "" || cmds.Gemini != "" {
		t.Errorf("expected empty commands with nil cfg, got %+v", cmds)
	}
}

// TestApplyModelCommands_HonorsCapturedModel covers ntm-boi0: resume/restore
// must relaunch each agent on its captured model, not the account default. The
// helper renders the pane's model into PaneState.Command (which the session
// launch path prefers). Panes without a captured model keep Command empty and
// fall back to the no-model type-default command.
func TestApplyModelCommands_HonorsCapturedModel(t *testing.T) {
	prevCfg := cfg
	cfg = config.Default()
	t.Cleanup(func() { cfg = prevCfg })

	state := &session.SessionState{
		Name:    "demo",
		WorkDir: "/data/projects/demo",
		Panes: []session.PaneState{
			{Index: 1, AgentType: "cc", Model: "opus"},
			{Index: 2, AgentType: "cc"}, // no captured model
		},
	}
	applyModelCommands(state)

	withModel := state.Panes[0].Command
	if withModel == "" {
		t.Fatalf("model pane Command is empty; expected a rendered launch command")
	}
	if strings.Contains(withModel, "{{") || strings.Contains(withModel, "}}") {
		t.Errorf("model pane Command still has unrendered template markers: %q", withModel)
	}
	if !strings.Contains(withModel, "--model") {
		t.Errorf("model pane Command missing --model flag: %q", withModel)
	}
	if !strings.Contains(withModel, "claude") {
		t.Errorf("model pane Command missing claude invocation: %q", withModel)
	}
	if state.Panes[1].Command != "" {
		t.Errorf("no-model pane Command should stay empty (type-default fallback), got %q", state.Panes[1].Command)
	}
}

// TestApplyModelCommands_NilConfigSafe verifies the helper is a no-op (no panic)
// when no config is loaded, leaving Command empty so launch falls back cleanly.
func TestApplyModelCommands_NilConfigSafe(t *testing.T) {
	prevCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = prevCfg })

	state := &session.SessionState{
		Panes: []session.PaneState{{Index: 1, AgentType: "cc", Model: "opus"}},
	}
	applyModelCommands(state) // must not panic
	if state.Panes[0].Command != "" {
		t.Errorf("nil cfg should leave Command empty, got %q", state.Panes[0].Command)
	}
}

func TestApplyModelCommandsPreservesRecordedLaunches(t *testing.T) {
	previous := cfg
	cfg = config.Default()
	t.Cleanup(func() { cfg = previous })
	spec := &tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex, Command: "codex --model exact -c 'model_reasoning_effort=high'"}
	state := &session.SessionState{Panes: []session.PaneState{
		{AgentType: "cod", Model: "stale-title", LaunchSpec: spec},
		{AgentType: "cc", Model: "stale-title", Command: "/opt/claude --model recorded --effort high"},
	}}
	applyModelCommands(state)
	if state.Panes[0].Command != "" || state.Panes[0].LaunchSpec != spec || state.Panes[1].Command != "/opt/claude --model recorded --effort high" {
		t.Fatalf("current configuration overwrote recorded launch settings: %+v", state.Panes)
	}
}

func TestSessionsCommandRestoresDurableSettingsAndNativeContext(t *testing.T) {
	for _, operation := range []string{"restore", "resume"} {
		t.Run(operation, func(t *testing.T) {
			logPath := sessionRecoveryCommandFixture(t)
			worktree := t.TempDir()
			baseCommand := "/opt/codex --model saved-model -c 'model_reasoning_effort=high'"
			state := &session.SessionState{Name: "fidelity", WorkDir: t.TempDir(), Panes: []session.PaneState{{
				Index: 0, AgentType: "cod", Model: "stale-title", WorkDir: worktree,
				LaunchSpec: &tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex, Command: baseCommand},
				SessionID:  "native-context", SessionProvider: "codex", SessionFreshness: agentsession.BindingFresh, SessionConfidence: 1,
			}}}
			if _, err := session.Save(state, session.SaveOptions{}); err != nil {
				t.Fatal(err)
			}
			cmd := newSessionPersistCmd()
			args := []string{operation, "fidelity", "--force"}
			if operation == "restore" {
				args = append(args, "--launch")
			}
			cmd.SetArgs(args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			stdout, err := captureStdout(t, cmd.Execute)
			if err != nil {
				t.Fatalf("command failed: %v; %s", err, stdout)
			}
			var result struct {
				Success    bool `json:"success"`
				Resumed    int  `json:"resumed"`
				AgentCount int  `json:"agent_count"`
			}
			if err := json.Unmarshal([]byte(stdout), &result); err != nil || !result.Success {
				t.Fatalf("invalid recovery result: %s, %v", stdout, err)
			}
			expected := baseCommand
			if operation == "resume" {
				expected = "/opt/codex resume 'native-context' --model saved-model -c 'model_reasoning_effort=high'"
				if result.Resumed != 1 {
					t.Fatalf("native resume was not reported: %s", stdout)
				}
			}
			calls, _ := os.ReadFile(logPath)
			if !strings.Contains(string(calls), "cd "+tmux.ShellQuote(worktree)+" && "+expected) || strings.Contains(string(calls), "stale-title") {
				t.Fatalf("public command dropped settings or worktree: %s", calls)
			}
		})
	}
}

// GH#251 phase 2: Grok Build relaunch is implemented, so launching restores
// of saved grok panes now validate like topology-only restores.
func TestValidateSessionsAutomatedRelaunchAcceptsGrok(t *testing.T) {
	state := &session.SessionState{Panes: []session.PaneState{{Index: 1, AgentType: "grok"}}}

	if err := validateSessionsAutomatedRelaunch(state, false); err != nil {
		t.Fatalf("topology-only restore validation error = %v, want nil", err)
	}
	if err := validateSessionsAutomatedRelaunch(state, true); err != nil {
		t.Fatalf("launching restore validation error = %v, want nil", err)
	}
}

func TestSessionsRestoreOperationErrorIncludesAgentLaunchFailure(t *testing.T) {
	launchErr := errors.New("agent relaunch failed")
	if got := sessionsRestoreOperationError(launchErr, nil); !errors.Is(got, launchErr) {
		t.Fatalf("restore operation error = %v, want launch error", got)
	}
	promptErr := errors.New("prompt dispatch failed")
	if got := sessionsRestoreOperationError(launchErr, promptErr); !errors.Is(got, promptErr) {
		t.Fatalf("restore operation error = %v, want prompt error", got)
	}
	if got := sessionsRestoreOperationError(nil, nil); got != nil {
		t.Fatalf("successful restore operation error = %v, want nil", got)
	}
}

func sessionRecoveryCommandFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("tmux fixture requires a POSIX shell")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	logPath := filepath.Join(dir, "tmux-calls")
	t.Setenv("NTM_SESSION_CLI_LOG", logPath)
	t.Setenv("NTM_SESSION_CLI_FAIL_PANE", "")
	bin := filepath.Join(dir, "tmux")
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_SESSION_CLI_LOG"
case "$1" in
  -V) echo 'tmux 3.4' ;;
  list-windows) echo 0 ;;
  list-panes)
    for i in 0 1; do
      printf '%%%s_NTM_SEP_%s_NTM_SEP__NTM_SEP_bash_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_9999998_NTM_SEP_0_NTM_SEP_cod_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$i" "$i"
    done ;;
  send-keys)
    if [ "$3" = "$NTM_SESSION_CLI_FAIL_PANE" ]; then
      echo 'fixture pane dispatch failed' >&2
      exit 2
    fi ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	previousClient, previousCfg, previousJSON := tmux.DefaultClient, cfg, jsonOutput
	tmux.DefaultClient, cfg, jsonOutput = tmux.NewClient(""), config.Default(), true
	t.Cleanup(func() { tmux.DefaultClient, cfg, jsonOutput = previousClient, previousCfg, previousJSON })
	return logPath
}

func TestSessionsRecoveryCommandPreflightPreservesLiveSession(t *testing.T) {
	for _, command := range []string{"restore", "resume"} {
		for _, malformed := range []string{"command", "directory"} {
			t.Run(command+"_"+malformed, func(t *testing.T) {
				logPath := sessionRecoveryCommandFixture(t)
				state := &session.SessionState{Name: "recovery", WorkDir: t.TempDir(), Panes: []session.PaneState{
					{Index: 0, AgentType: "cod", Command: "codex"},
					{Index: 1, AgentType: "cod", Command: "codex"},
				}}
				if malformed == "command" {
					state.Panes[1].Command = "codex\ninvalid"
				} else {
					state.WorkDir = filepath.Join(t.TempDir(), "file")
					if err := os.WriteFile(state.WorkDir, []byte("not a directory"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := session.Save(state, session.SaveOptions{}); err != nil {
					t.Fatal(err)
				}
				cmd := newSessionPersistCmd()
				args := []string{command, "recovery", "--force"}
				if command == "restore" {
					args = append(args, "--launch")
				}
				cmd.SetArgs(args)
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				stdout, err := captureStdout(t, cmd.Execute)
				if !errors.Is(err, errJSONFailure) {
					t.Fatalf("invalid forced %s did not exit with failure: %v; %s", command, err, stdout)
				}
				var envelope struct {
					Success bool   `json:"success"`
					Error   string `json:"error"`
				}
				if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || envelope.Success || envelope.Error == "" {
					t.Fatalf("invalid recovery response: %s; %v", stdout, err)
				}
				data, err := os.ReadFile(logPath)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "kill-session") || strings.Contains(string(data), "new-session") || strings.Contains(string(data), "send-keys") {
					t.Fatalf("failed preflight changed the live session: %s", data)
				}
			})
		}
	}
}

func TestSessionsRecoveryCommandReportsPartialLaunchFailure(t *testing.T) {
	for _, command := range []string{"restore", "resume"} {
		t.Run(command, func(t *testing.T) {
			logPath := sessionRecoveryCommandFixture(t)
			t.Setenv("NTM_SESSION_CLI_FAIL_PANE", "%1")
			state := &session.SessionState{Name: "recovery", WorkDir: t.TempDir(), Agents: session.AgentConfig{Codex: 2}, Panes: []session.PaneState{
				{Index: 0, AgentType: "cod", Command: "codex"},
				{Index: 1, AgentType: "cod", Command: "codex"},
			}}
			if _, err := session.Save(state, session.SaveOptions{}); err != nil {
				t.Fatal(err)
			}
			cmd := newSessionPersistCmd()
			args := []string{command, "recovery", "--force"}
			if command == "restore" {
				args = append(args, "--launch")
			}
			cmd.SetArgs(args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			stdout, err := captureStdout(t, cmd.Execute)
			if !errors.Is(err, errJSONFailure) {
				t.Fatalf("partial %s reported success: %v; %s", command, err, stdout)
			}
			var envelope SessionsResumeResult
			if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || envelope.Success || !strings.Contains(envelope.Error, "0.1") {
				t.Fatalf("partial recovery response lost its failure: %s; %v", stdout, err)
			}
			if command == "resume" {
				if envelope.ResumedAs != "recovery" || envelope.Launched != 1 || envelope.Failed != 1 || envelope.Skipped != 0 || len(envelope.Panes) != 2 {
					t.Fatalf("partial resume lost pane outcomes: %+v", envelope)
				}
				var text bytes.Buffer
				if err := envelope.Text(&text); err != nil || !strings.Contains(text.String(), "1 launched fresh") || !strings.Contains(text.String(), "1 failed") || !strings.Contains(text.String(), "available for inspection") {
					t.Fatalf("partial resume text lost recovery progress: %s; %v", text.String(), err)
				}
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(data), "kill-session") != 1 || !strings.Contains(string(data), "send-keys -t %0") {
				t.Fatalf("partial failure destroyed successful panes: %s", data)
			}
		})
	}
}

// TestRunSessionsShow_LoadFailureRoutesThroughJSONEnvelope covers bd-1yws7:
// when --json is set, runSessionsShow's session.Load failure path must emit
// a parseable JSON envelope and propagate errJSONFailure so automation
// gating on `$?` no longer treats a missing/corrupt saved-session as
// success. Pre-fix the function returned the raw err, which under --json
// surfaced as a stderr "Error:" line and empty stdin to jq.
func TestRunSessionsShow_LoadFailureRoutesThroughJSONEnvelope(t *testing.T) {
	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	origStdout := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe error = %v", pipeErr)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(done)
	}()

	// Empty name trips normalizeSavedSessionName inside session.Load,
	// which is the deterministic failure surface for runSessionsShow.
	err := runSessionsShow("")
	_ = w.Close()
	<-done

	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runSessionsShow returned %v, want errJSONFailure (load failure must route through emitJSONFailureEnvelope under --json)", err)
	}
}

// TestRunSessionsDelete_NotFoundRoutesThroughJSONEnvelope covers bd-1yws7:
// runSessionsDelete previously returned a raw fmt.Errorf for the missing-
// session path, which bypassed --json and forced automation to parse
// stderr text. The fix routes the error through emitJSONFailureEnvelope so
// `ntm sessions delete --json | jq` sees a parseable failure on stdout and
// the process exits non-zero via errJSONFailure.
func TestRunSessionsDelete_NotFoundRoutesThroughJSONEnvelope(t *testing.T) {
	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	origStdout := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe error = %v", pipeErr)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(done)
	}()

	err := runSessionsDelete("ntm-bd-1yws7-nonexistent-12345-do-not-exist", false)
	_ = w.Close()
	<-done

	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runSessionsDelete returned %v, want errJSONFailure (not-found path must emit JSON envelope under --json)", err)
	}
}
