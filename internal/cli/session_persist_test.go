package cli

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/session"
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
