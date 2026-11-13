package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type stubStatusDetector struct {
	statuses []statuspkg.AgentStatus
}

func (d stubStatusDetector) Detect(_ string) (statuspkg.AgentStatus, error) {
	return statuspkg.AgentStatus{}, nil
}

func (d stubStatusDetector) DetectAll(_ string) ([]statuspkg.AgentStatus, error) {
	return d.statuses, nil
}

func TestContextStatusCmd_EstimatesRobotAndTranscriptAndStatus(t *testing.T) {
	transcriptPath := filepath.Join(t.TempDir(), "gemini-session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(strings.Repeat("transcript line payload\n", 1500)), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	deps := &contextStatusDeps{
		resolve: func(_ context.Context, name string) (string, error) {
			if name == "" {
				return "demo", nil
			}
			return name, nil
		},
		panes: func(_ string) ([]tmux.Pane, error) {
			return []tmux.Pane{
				{ID: "%0", Index: 0, Title: "shell", Type: tmux.AgentUser},
				{ID: "%1", Index: 1, Title: "demo__cc_1", Type: tmux.AgentClaude, Variant: "claude-opus-4", PID: 101},
				{ID: "%2", Index: 2, Title: "demo__cod_1", Type: tmux.AgentCodex, Variant: "gpt-4", PID: 102},
				{ID: "%3", Index: 3, Title: "demo__gmi_1", Type: tmux.AgentGemini, Variant: "gemini-2.0-flash", PID: 103},
			}, nil
		},
		capture: func(_ context.Context, target string, _ int) (string, error) {
			if target == "%1" {
				return `status line {"context_used": 156000, "context_limit": 200000}`, nil
			}
			return "normal agent output", nil
		},
		usage: func(_ context.Context, pane tmux.Pane) (*ntmctx.TranscriptUsage, bool) {
			switch pane.ID {
			case "%2":
				return &ntmctx.TranscriptUsage{
					InputTokens:  80000,
					OutputTokens: 20000,
					Model:        "gpt-4",
				}, true
			case "%3":
				return &ntmctx.TranscriptUsage{
					Path:  transcriptPath,
					Model: "gemini-2.0-flash",
				}, true
			default:
				return nil, false
			}
		},
		detector: stubStatusDetector{
			statuses: []statuspkg.AgentStatus{
				{PaneID: "%1", PaneName: "demo__cc_1", State: statuspkg.StateIdle, LastActive: now.Add(-45 * time.Second)},
				{PaneID: "%2", PaneName: "demo__cod_2", State: statuspkg.StateError, LastActive: now, ErrorType: statuspkg.ErrorRateLimit},
				{PaneID: "%3", PaneName: "demo__gmi_3", State: statuspkg.StateWorking, LastActive: now},
			},
		},
	}

	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	var jsonBuf bytes.Buffer
	cmd := newContextStatusCmdWithDeps(deps)
	cmd.SetOut(&jsonBuf)
	cmd.SetArgs([]string{"demo", "--warn-threshold=70", "--rotate-threshold=75"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("context status --json failed: %v", err)
	}

	var result ContextStatusResult
	if err := json.Unmarshal(jsonBuf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal ContextStatusResult: %v\n%s", err, jsonBuf.String())
	}
	if !result.Success || result.Session != "demo" || result.MonitoredCount != 3 || len(result.Agents) != 3 {
		t.Fatalf("unexpected ContextStatusResult summary: %+v", result)
	}
	if len(result.AboveThreshold) != 1 || result.AboveThreshold[0].AgentID != "demo__cc_1" {
		t.Fatalf("AboveThreshold = %+v, want [demo__cc_1]", result.AboveThreshold)
	}

	cc := result.Agents[0]
	if cc.Estimate == nil || cc.Estimate.Method != ntmctx.MethodRobotMode || cc.Estimate.TokensUsed != 156000 {
		t.Fatalf("cc estimate = %+v, want robot_mode 156000 tokens", cc.Estimate)
	}
	if cc.Handoff == nil || !cc.Handoff.ShouldTrigger {
		t.Fatalf("cc handoff = %+v, want ShouldTrigger=true above 75%%", cc.Handoff)
	}
	if cc.State != "idle" || cc.IdleSeconds < 40 {
		t.Fatalf("cc status = state:%q idle:%ds, want idle >=40s", cc.State, cc.IdleSeconds)
	}

	cod := result.Agents[1]
	if cod.Estimate == nil || cod.Estimate.Method != ntmctx.MethodCumulativeTokens || cod.Estimate.TokensUsed != 70000 {
		t.Fatalf("cod estimate = %+v, want cumulative_tokens 70000", cod.Estimate)
	}
	if !strings.Contains(cod.ErrorMessage, "Rate limited") {
		t.Fatalf("cod ErrorMessage = %q, want rate limit message", cod.ErrorMessage)
	}

	gmi := result.Agents[2]
	if gmi.Estimate == nil || gmi.Estimate.TokensUsed <= 0 {
		t.Fatalf("gmi transcript fallback estimate = %+v, want > 0 tokens", gmi.Estimate)
	}

	jsonOutput = false
	var textBuf bytes.Buffer
	textCmd := newContextStatusCmdWithDeps(deps)
	textCmd.SetOut(&textBuf)
	textCmd.SetArgs([]string{"demo"})
	if err := textCmd.Execute(); err != nil {
		t.Fatalf("context status text failed: %v", err)
	}
	rendered := textBuf.String()
	for _, want := range []string{"Context Status: session=demo monitored=3", "demo__cc_1", "Rate limited", "exceeds 75.0% threshold"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("text output missing %q:\n%s", want, rendered)
		}
	}
}

func TestContextRecoverCmd_SendsByIDAndIndexWithCooldown(t *testing.T) {
	fakeDir := t.TempDir()
	logFile := filepath.Join(fakeDir, "tmux.log")
	tmuxScript := filepath.Join(fakeDir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + logFile + `"
case "$1" in
  -V) printf 'tmux 3.4\n' ;;
  has-session) exit 0 ;;
  list-sessions) printf 'demo_NTM_SEP_1_NTM_SEP_0_NTM_SEP_1700000000\n' ;;
  list-panes)
    printf '%%17_NTM_SEP_1_NTM_SEP_demo__cc_1_NTM_SEP_node_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_4242_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]\n'
    ;;
  capture-pane)
    printf 'Welcome to Claude Code\n────────────────────────\n❯ \n────────────────────────\n  ⏵⏵ bypass permissions on\n'
    ;;
  load-buffer) cat >/dev/null ;;
  paste-buffer|delete-buffer|send-keys|display-message) exit 0 ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(tmuxScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", tmuxScript)
	t.Setenv("TMUX", "")

	prevJSON := jsonOutput
	prevClient := tmux.DefaultClient
	jsonOutput = true
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() {
		jsonOutput = prevJSON
		tmux.DefaultClient = prevClient
	})

	sharedManager := statuspkg.NewRecoveryManager(statuspkg.RecoveryConfig{
		Cooldown:           time.Hour,
		Prompt:             "Default prompt",
		MaxRecoveries:      3,
		IncludeBeadContext: false,
	})
	deps := &contextRecoverDeps{
		resolve: func(_ context.Context, name string) (string, error) { return name, nil },
		panes:   tmux.GetPanes,
		manager: sharedManager,
	}

	var firstOut bytes.Buffer
	cmd1 := newContextRecoverCmdWithDeps(deps)
	cmd1.SetOut(&firstOut)
	cmd1.SetArgs([]string{"demo", "--pane-id=%17", "--prompt=Reread AGENTS.md carefully", "--cooldown=1h", "--no-beads"})
	if err := cmd1.Execute(); err != nil {
		t.Fatalf("first context recover failed: %v\n%s", err, firstOut.String())
	}

	var firstResult ContextRecoverResult
	if err := json.Unmarshal(firstOut.Bytes(), &firstResult); err != nil {
		t.Fatalf("unmarshal first ContextRecoverResult: %v", err)
	}
	if !firstResult.Success || len(firstResult.Outcomes) != 1 || !firstResult.Outcomes[0].Sent || firstResult.Outcomes[0].RecoveryCount != 1 {
		t.Fatalf("unexpected first recover result: %+v", firstResult)
	}
	if len(firstResult.Events) != 1 || firstResult.Events[0].Prompt != "Reread AGENTS.md carefully" {
		t.Fatalf("unexpected recovery events: %+v", firstResult.Events)
	}

	// Second immediate invocation against the same pane ID must hit cooldown and skip cleanly.
	var secondOut bytes.Buffer
	cmd2 := newContextRecoverCmdWithDeps(deps)
	cmd2.SetOut(&secondOut)
	cmd2.SetArgs([]string{"demo", "--pane-id=%17", "--no-beads"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("cooldown context recover failed: %v", err)
	}
	var secondResult ContextRecoverResult
	if err := json.Unmarshal(secondOut.Bytes(), &secondResult); err != nil {
		t.Fatalf("unmarshal second ContextRecoverResult: %v", err)
	}
	if len(secondResult.Outcomes) != 1 || secondResult.Outcomes[0].Sent || !strings.Contains(secondResult.Outcomes[0].SkippedReason, "cooldown") {
		t.Fatalf("expected cooldown skip on second recover, got: %+v", secondResult)
	}

	// Exercise index-targeted SendRecoveryPrompt path as well.
	indexDeps := &contextRecoverDeps{
		resolve: func(_ context.Context, name string) (string, error) { return name, nil },
		panes:   tmux.GetPanes,
	}
	var thirdOut bytes.Buffer
	cmd3 := newContextRecoverCmdWithDeps(indexDeps)
	cmd3.SetOut(&thirdOut)
	cmd3.SetArgs([]string{"demo", "--pane=1", "--no-beads"})
	if err := cmd3.Execute(); err != nil {
		t.Fatalf("index context recover failed: %v\n%s", err, thirdOut.String())
	}
	var thirdResult ContextRecoverResult
	if err := json.Unmarshal(thirdOut.Bytes(), &thirdResult); err != nil {
		t.Fatalf("unmarshal third ContextRecoverResult: %v", err)
	}
	if !thirdResult.Success || len(thirdResult.Outcomes) != 1 || !thirdResult.Outcomes[0].Sent {
		t.Fatalf("expected index recover sent=true, got: %+v", thirdResult)
	}
}
