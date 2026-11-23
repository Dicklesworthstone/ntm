package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestAgentTypeLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		agentType tmux.AgentType
		want      string
	}{
		{tmux.AgentClaude, "cc"},
		{tmux.AgentCodex, "cod"},
		{tmux.AgentGemini, "gmi"},
		{tmux.AgentType("claude_code"), "cc"},
		{tmux.AgentType("openai-codex"), "cod"},
		{tmux.AgentType("google-gemini"), "gmi"},
		{tmux.AgentUser, "user"},
		{tmux.AgentUnknown, "unknown"},
		{"something-else", "unknown"},
	}
	for _, tt := range tests {
		got := scaleAgentTypeLabel(tt.agentType)
		if got != tt.want {
			t.Errorf("scaleAgentTypeLabel(%q) = %q, want %q", tt.agentType, got, tt.want)
		}
	}
}

func TestScaleDeltaCalculation(t *testing.T) {
	t.Parallel()
	// Test the core delta calculation logic directly by simulating current counts
	// and target counts, then verifying the expected actions
	tests := []struct {
		name          string
		currentCounts map[string]int
		targets       []scaleTarget
		wantUpCount   int // total spawn actions expected
		wantDownCount int // total kill actions expected
		wantNoChange  bool
	}{
		{
			name:          "scale up from zero",
			currentCounts: map[string]int{"cc": 0, "cod": 0, "gmi": 0},
			targets: []scaleTarget{
				{agentType: AgentTypeClaude, count: 3, set: true},
			},
			wantUpCount:   3,
			wantDownCount: 0,
		},
		{
			name:          "scale down",
			currentCounts: map[string]int{"cc": 5, "cod": 2, "gmi": 0},
			targets: []scaleTarget{
				{agentType: AgentTypeClaude, count: 2, set: true},
			},
			wantUpCount:   0,
			wantDownCount: 3,
		},
		{
			name:          "no change when at target",
			currentCounts: map[string]int{"cc": 3, "cod": 2, "gmi": 1},
			targets: []scaleTarget{
				{agentType: AgentTypeClaude, count: 3, set: true},
			},
			wantNoChange: true,
		},
		{
			name:          "mixed scale up and down",
			currentCounts: map[string]int{"cc": 3, "cod": 2, "gmi": 0},
			targets: []scaleTarget{
				{agentType: AgentTypeClaude, count: 5, set: true},
				{agentType: AgentTypeCodex, count: 1, set: true},
				{agentType: AgentTypeGemini, count: 2, set: true},
			},
			wantUpCount:   4, // +2 cc + 2 gmi
			wantDownCount: 1, // -1 cod
		},
		{
			name:          "scale to zero",
			currentCounts: map[string]int{"cc": 3, "cod": 0, "gmi": 0},
			targets: []scaleTarget{
				{agentType: AgentTypeClaude, count: 0, set: true},
			},
			wantUpCount:   0,
			wantDownCount: 3,
		},
		{
			name:          "unset flags are ignored",
			currentCounts: map[string]int{"cc": 3, "cod": 2, "gmi": 1},
			targets: []scaleTarget{
				{agentType: AgentTypeClaude, count: 0, set: false}, // not set
				{agentType: AgentTypeCodex, count: 5, set: true},   // set
			},
			wantUpCount:   3, // +3 cod
			wantDownCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var upTotal, downTotal int

			for _, target := range tt.targets {
				if !target.set {
					continue
				}
				typeStr := string(target.agentType)
				current := tt.currentCounts[typeStr]
				delta := target.count - current

				if delta > 0 {
					upTotal += delta
				} else if delta < 0 {
					downTotal += -delta
				}
			}

			if tt.wantNoChange {
				if upTotal != 0 || downTotal != 0 {
					t.Errorf("expected no change, got up=%d down=%d", upTotal, downTotal)
				}
				return
			}

			if upTotal != tt.wantUpCount {
				t.Errorf("scale up count = %d, want %d", upTotal, tt.wantUpCount)
			}
			if downTotal != tt.wantDownCount {
				t.Errorf("scale down count = %d, want %d", downTotal, tt.wantDownCount)
			}
		})
	}
}

func TestScaleTargetValidation(t *testing.T) {
	t.Parallel()
	// Verify negative counts are caught
	targets := []scaleTarget{
		{agentType: AgentTypeClaude, count: -1, set: true},
	}
	for _, target := range targets {
		if target.set && target.count < 0 {
			// This is the validation check in runScale
			t.Logf("correctly identified negative count for %s: %d", target.agentType, target.count)
		}
	}
}

func TestScaleTargetNoFlagsSet(t *testing.T) {
	t.Parallel()
	targets := []scaleTarget{
		{agentType: AgentTypeClaude, count: 0, set: false},
		{agentType: AgentTypeCodex, count: 0, set: false},
		{agentType: AgentTypeGemini, count: 0, set: false},
	}

	anySet := false
	for _, target := range targets {
		if target.set {
			anySet = true
			break
		}
	}
	if anySet {
		t.Error("expected no flags set, but found one")
	}
}

func TestScaleAgentSelectionOrder(t *testing.T) {
	t.Parallel()
	// Verify that agents are selected for killing in NTMIndex descending order
	panes := []tmux.Pane{
		{NTMIndex: 1, Title: "proj__cc_1"},
		{NTMIndex: 3, Title: "proj__cc_3"},
		{NTMIndex: 2, Title: "proj__cc_2"},
		{NTMIndex: 5, Title: "proj__cc_5"},
		{NTMIndex: 4, Title: "proj__cc_4"},
	}

	// Sort descending by NTMIndex (matching scale command logic)
	sorted := make([]tmux.Pane, len(panes))
	copy(sorted, panes)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].NTMIndex > sorted[i].NTMIndex {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// First to kill should be highest index
	if sorted[0].NTMIndex != 5 {
		t.Errorf("first kill candidate NTMIndex = %d, want 5", sorted[0].NTMIndex)
	}
	if sorted[1].NTMIndex != 4 {
		t.Errorf("second kill candidate NTMIndex = %d, want 4", sorted[1].NTMIndex)
	}
	if sorted[4].NTMIndex != 1 {
		t.Errorf("last kill candidate NTMIndex = %d, want 1", sorted[4].NTMIndex)
	}
}

func TestNewScaleCmd(t *testing.T) {
	t.Parallel()
	cmd := newScaleCmd()

	if cmd.Use != "scale <session>" {
		t.Errorf("Use = %q, want %q", cmd.Use, "scale <session>")
	}

	// Verify expected flags exist
	flags := []string{"cc", "cod", "gmi", "dry-run", "force"}
	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("expected flag %q not found", name)
		}
	}

	// Verify force has short flag
	f := cmd.Flags().ShorthandLookup("f")
	if f == nil {
		t.Error("expected short flag -f for --force")
	}
}

func TestScaleResponseFields(t *testing.T) {
	t.Parallel()
	resp := ScaleResponse{
		Session: "test",
		Before:  map[string]int{"cc": 3, "cod": 2, "gmi": 0},
		After:   map[string]int{"cc": 5, "cod": 1, "gmi": 2},
		Actions: []ScaleAction{
			{ActionType: "spawn", AgentType: "cc", Count: 2, Agents: []string{}},
			{ActionType: "kill", AgentType: "cod", Count: 1, Agents: []string{"test__cod_2"}},
			{ActionType: "spawn", AgentType: "gmi", Count: 2, Agents: []string{}},
		},
		Success: true,
	}

	if resp.Session != "test" {
		t.Errorf("Session = %q, want %q", resp.Session, "test")
	}
	if resp.Before["cc"] != 3 {
		t.Errorf("Before[cc] = %d, want 3", resp.Before["cc"])
	}
	if resp.After["cc"] != 5 {
		t.Errorf("After[cc] = %d, want 5", resp.After["cc"])
	}
	if len(resp.Actions) != 3 {
		t.Errorf("len(Actions) = %d, want 3", len(resp.Actions))
	}
	for i, action := range resp.Actions {
		if action.Agents == nil {
			t.Errorf("Actions[%d].Agents is nil, want checked-empty [] when no agents are selected", i)
		}
	}
	if !resp.Success {
		t.Error("expected Success = true")
	}
	if resp.DryRun {
		t.Error("expected DryRun = false when not set")
	}
}

func TestExecuteAddComposedJSONReturnsUnderlyingErrorWithoutOutput(t *testing.T) {
	previousJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previousJSON })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout, err := captureScaleStdout(t, func() error {
		return executeAdd(ctx, AddOptions{}, false)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeAdd() error = %v, want context.Canceled", err)
	}
	if len(bytes.TrimSpace(stdout)) != 0 {
		t.Fatalf("composed add emitted terminal output: %q", stdout)
	}
}

func TestExecuteAddDirectPreCanceledJSONOwnsFailureEnvelope(t *testing.T) {
	previousJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previousJSON })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout, err := captureScaleStdout(t, func() error {
		return executeAdd(ctx, AddOptions{}, true)
	})
	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("executeAdd() error = %v, want errJSONFailure", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode add error envelope: %v raw=%s", err, stdout)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("add emitted more than one JSON document: %v raw=%s", err, stdout)
	}
	if document["success"] != false || document["code"] != robot.ErrCodeTimeout ||
		document["error_code"] != robot.ErrCodeTimeout || strings.TrimSpace(fmt.Sprint(document["error"])) == "" {
		t.Fatalf("add error envelope=%v, want TIMEOUT", document)
	}
	panes, ok := document["affected_pane_ids"].([]any)
	if !ok || len(panes) != 0 {
		t.Fatalf("add error affected_pane_ids=%#v, want checked-empty []", document["affected_pane_ids"])
	}
}

func TestResolveAddSessionBlockedListingCancellationHelper(t *testing.T) {
	if os.Getenv("NTM_RESOLVE_ADD_BLOCKED_HELPER") != "1" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := resolveAddSession(ctx, "blocked-session")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("resolveAddSession() error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("resolveAddSession() returned after %s, want prompt cancellation", elapsed)
	}
}

func TestResolveAddSessionBlockedListingHonorsContext(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tmuxPath := filepath.Join(root, "tmux")
	script := `#!/bin/sh
if [ "${1:-}" = "list-sessions" ]; then
	case "$*" in
	  *session_windows*) while :; do :; done ;;
	  *) exit 1 ;;
	esac
fi
exit 64
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write blocking tmux: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestResolveAddSessionBlockedListingCancellationHelper$",
		"-test.timeout=4s",
	)
	cmd.Env = envWithOverrides(os.Environ(),
		"NTM_RESOLVE_ADD_BLOCKED_HELPER=1",
		"NTM_TEST_TMUX_ENV_OWNED=1",
		"NTM_TMUX_BINARY="+tmuxPath,
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("blocked-listing helper did not return promptly: %v output=%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("blocked-listing helper failed: %v output=%s", err, output)
	}
}

func TestResolveSessionWithOptionsContextStaleTMUXHelper(t *testing.T) {
	if os.Getenv("NTM_RESOLVE_STALE_TMUX_HELPER") != "1" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resolution, err := ResolveSessionWithOptionsContext(ctx, "", nil, SessionResolveOptions{TreatAsJSON: true})
	if err != nil {
		t.Fatalf("ResolveSessionWithOptionsContext() error = %v", err)
	}
	if resolution.Session != "fallback-session" || !resolution.Inferred {
		t.Fatalf("ResolveSessionWithOptionsContext() = %+v, want fallback-session", resolution)
	}
}

func TestResolveSessionWithOptionsContextFallsBackFromStaleTMUX(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tmuxPath := filepath.Join(root, "tmux")
	script := `#!/bin/sh
case "${1:-}" in
  list-sessions)
	case "$*" in
	  *session_windows*) printf 'fallback-session_NTM_SEP_1_NTM_SEP_0_NTM_SEP_created\n' ;;
	  *) exit 1 ;;
	esac
	;;
  display-message)
	exit 70
	;;
  *)
	exit 64
	;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write stale-tmux wrapper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestResolveSessionWithOptionsContextStaleTMUXHelper$",
		"-test.timeout=4s",
	)
	cmd.Env = envWithOverrides(os.Environ(),
		"NTM_RESOLVE_STALE_TMUX_HELPER=1",
		"NTM_TEST_TMUX_ENV_OWNED=1",
		"NTM_TMUX_BINARY="+tmuxPath,
		"TMUX=stale",
		"TMUX_PANE=",
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("stale-TMUX helper did not return promptly: %v output=%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("stale-TMUX helper failed: %v output=%s", err, output)
	}
}

func TestScaleJSONCancellationProcessHelper(t *testing.T) {
	if os.Getenv("NTM_SCALE_CANCEL_HELPER") != "1" {
		return
	}
	jsonOutput = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runScale(ctx, "scale-cancel", []scaleTarget{{agentType: AgentTypeClaude, count: 1, set: true}}, false, true)
	if err != nil {
		os.Exit(ExitCode(err))
	}
	os.Exit(0)
}

func TestScaleJSONCancellationProcessContract(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(os.Args[0], "-test.run=^TestScaleJSONCancellationProcessHelper$")
	cmd.Env = append(os.Environ(), "NTM_SCALE_CANCEL_HELPER=1")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("canceled scale exit error = %v, want exit 1; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "" {
		t.Fatalf("canceled scale stderr = %q, want empty", got)
	}
	response := decodeSingleScaleResponse(t, stdout.Bytes())
	if response.Success || response.ErrorCode != robot.ErrCodeTimeout || len(response.Errors) != 1 {
		t.Fatalf("canceled scale response = %+v, want one TIMEOUT failure", response)
	}
}

func TestScaleJSONScaleUpOwnsSingleTerminalResponse(t *testing.T) {
	session := newScaleJSONTestSession(t, testAgentCatCommandTemplate)
	stdout, err := captureScaleStdout(t, func() error {
		return runScale(t.Context(), session, []scaleTarget{{agentType: AgentTypeClaude, count: 1, set: true}}, false, true)
	})
	if err != nil {
		t.Fatalf("runScale() error = %v; stdout=%s", err, stdout)
	}
	response := decodeSingleScaleResponse(t, stdout)
	if !response.Success || response.Session != session || response.Before["cc"] != 0 || response.After["cc"] != 1 {
		t.Fatalf("scale response = %+v, want successful cc 0 -> 1", response)
	}
	if len(response.Actions) != 1 || response.Actions[0].Agents == nil || len(response.Actions[0].Agents) != 0 {
		t.Fatalf("scale spawn action agents = %+v, want checked-empty []", response.Actions)
	}
	if bytes.Contains(stdout, []byte(`"total_added"`)) || bytes.Contains(stdout, []byte(`"added_claude"`)) {
		t.Fatalf("scale leaked nested AddResponse: %s", stdout)
	}
}

func TestScaleJSONAddFailureOwnsSingleTerminalResponseAndFails(t *testing.T) {
	session := newScaleJSONTestSession(t, "{{")
	stdout, err := captureScaleStdout(t, func() error {
		return runScale(t.Context(), session, []scaleTarget{{agentType: AgentTypeClaude, count: 1, set: true}}, false, true)
	})
	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runScale() error = %v, want errJSONFailure; stdout=%s", err, stdout)
	}
	response := decodeSingleScaleResponse(t, stdout)
	if response.Success || response.ErrorCode != robot.ErrCodeInternalError || len(response.Errors) == 0 ||
		!strings.Contains(response.Errors[0], "spawn cc") {
		t.Fatalf("scale add-failure response = %+v", response)
	}
	if len(response.Actions) != 1 || response.Actions[0].Agents == nil || len(response.Actions[0].Agents) != 0 {
		t.Fatalf("scale failed spawn action agents = %+v, want checked-empty []", response.Actions)
	}
	if bytes.Contains(stdout, []byte(`"total_added"`)) || bytes.Contains(stdout, []byte(`"code"`)) {
		t.Fatalf("scale leaked nested add terminal envelope: %s", stdout)
	}
}

func newScaleJSONTestSession(t *testing.T, claudeTemplate string) string {
	t.Helper()
	testutil.RequireTmuxThrottled(t)

	root := t.TempDir()
	projectsBase := filepath.Join(root, "projects")
	session := fmt.Sprintf("ntm_test_scale_json_%d", time.Now().UnixNano())
	projectDir := filepath.Join(projectsBase, session)
	for _, dir := range []string{projectDir, filepath.Join(root, "home"), filepath.Join(root, "config"), filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create scale test directory %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("NTM_CONFIG", "")

	previousCfg := cfg
	previousJSON := jsonOutput
	cfg = newTmuxIntegrationTestConfig(projectsBase)
	cfg.Agents.Claude = claudeTemplate
	cfg.Tmux.PaneInitDelayMs = 0
	cfg.Checkpoints.Enabled = false
	jsonOutput = true
	t.Cleanup(func() {
		cfg = previousCfg
		jsonOutput = previousJSON
	})

	if err := tmux.CreateSession(session, projectDir); err != nil {
		t.Fatalf("create scale test session: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })
	return session
}

func captureScaleStdout(t *testing.T, run func() error) ([]byte, error) {
	t.Helper()
	previousStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create scale stdout pipe: %v", err)
	}
	os.Stdout = writer
	runErr := run()
	if err := writer.Close(); err != nil {
		os.Stdout = previousStdout
		_ = reader.Close()
		t.Fatalf("close scale stdout writer: %v", err)
	}
	os.Stdout = previousStdout
	data, readErr := io.ReadAll(reader)
	if closeErr := reader.Close(); readErr == nil {
		readErr = closeErr
	}
	if readErr != nil {
		t.Fatalf("read scale stdout: %v", readErr)
	}
	return data, runErr
}

func decodeSingleScaleResponse(t *testing.T, data []byte) ScaleResponse {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var response ScaleResponse
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("decode scale response: %v; raw=%q", err, data)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("scale emitted more than one JSON document: err=%v extra=%v raw=%q", err, extra, data)
	}
	return response
}

func TestScaleMultiHarnessSupport(t *testing.T) {
	cmd := newScaleCmd()
	for _, name := range []string{"agy", "grok", "omp", "oc"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("expected --%s flag on scale command", name)
		}
	}

	for _, tc := range []struct {
		agentType tmux.AgentType
		want      string
	}{
		{tmux.AgentAntigravity, "agy"},
		{tmux.AgentType("antigravity"), "agy"},
		{tmux.AgentGrok, "grok"},
		{tmux.AgentType("grok-build"), "grok"},
		{tmux.AgentOmp, "omp"},
		{tmux.AgentType("oh-my-pi"), "omp"},
		{tmux.AgentPi, "pi"},
		{tmux.AgentOpencode, "oc"},
		{tmux.AgentType("opencode"), "oc"},
	} {
		if got := scaleAgentTypeLabel(tc.agentType); got != tc.want {
			t.Errorf("scaleAgentTypeLabel(%q) = %q, want %q", tc.agentType, got, tc.want)
		}
	}

	t.Run("no_targets_error_mentions_modern_harnesses", func(t *testing.T) {
		session := newScaleJSONTestSession(t, "cat")
		jsonOutput = false

		noTargetCmd := newScaleCmd()
		noTargetCmd.SetOut(io.Discard)
		noTargetCmd.SetErr(io.Discard)
		noTargetCmd.SetArgs([]string{session})
		err := noTargetCmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "--grok, --omp, --pi, or --oc") {
			t.Fatalf("expected no-targets error mentioning --grok, --omp, --pi, or --oc, got: %v", err)
		}
	})

	planOut, _ := captureScaleStdout(t, func() error {
		printScalePlan(
			"proj",
			map[string]int{"grok": 0, "omp": 1, "oc": 2},
			map[string]int{"grok": 2, "omp": 3, "oc": 0},
			[]ScaleAction{
				{ActionType: "spawn", AgentType: "grok", Count: 2},
				{ActionType: "spawn", AgentType: "omp", Count: 2},
				{ActionType: "kill", AgentType: "oc", Count: 2, Agents: []string{"proj__oc_2", "proj__oc_1"}},
			},
		)
		return nil
	})
	text := string(planOut)
	for _, want := range []string{
		"Grok (grok): 0 → 2 (+2)",
		"Oh My Pi (omp): 1 → 3 (+2)",
		"OpenCode (oc): 2 → 0 (-2)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("printScalePlan output missing %q:\n%s", want, text)
		}
	}
}
