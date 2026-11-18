package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestScale11HarnessParityAndKernelHandler(t *testing.T) {
	scaleCmd := newScaleCmd()
	wantFlags := []string{
		"cc", "cod", "gmi", "agy",
		"ollama", "cursor", "windsurf", "aider",
		"grok", "omp", "oc",
	}
	for _, flagName := range wantFlags {
		if scaleCmd.Flags().Lookup(flagName) == nil {
			t.Fatalf("newScaleCmd() missing --%s flag", flagName)
		}
	}

	labelCases := []struct {
		agentType tmux.AgentType
		wantLabel string
	}{
		{tmux.AgentClaude, "cc"},
		{tmux.AgentCodex, "cod"},
		{tmux.AgentGemini, "gmi"},
		{tmux.AgentAntigravity, "agy"},
		{tmux.AgentOllama, "ollama"},
		{tmux.AgentCursor, "cursor"},
		{tmux.AgentWindsurf, "windsurf"},
		{tmux.AgentAider, "aider"},
		{tmux.AgentGrok, "grok"},
		{tmux.AgentOmp, "omp"},
		{tmux.AgentOpencode, "oc"},
	}
	for _, tc := range labelCases {
		if got := scaleAgentTypeLabel(tc.agentType); got != tc.wantLabel {
			t.Fatalf("scaleAgentTypeLabel(%q) = %q, want %q", tc.agentType, got, tc.wantLabel)
		}
	}

	wantBindings := map[string]struct {
		method string
		path   string
	}{
		"sessions.scale":                 {method: "POST", path: "/api/v1/sessions/{id}/scale"},
		"sessions.rebalance":             {method: "POST", path: "/api/v1/sessions/{id}/rebalance"},
		"sessions.coordinator":           {method: "GET", path: "/api/v1/sessions/{id}/coordinator"},
		"sessions.coordinator_digest":    {method: "POST", path: "/api/v1/sessions/{id}/coordinator/digest"},
		"sessions.coordinator_conflicts": {method: "GET", path: "/api/v1/sessions/{id}/coordinator/conflicts"},
	}
	byName := make(map[string]kernel.Command)
	for _, c := range kernel.List() {
		byName[c.Name] = c
	}
	for name, want := range wantBindings {
		cmdMeta, ok := byName[name]
		if !ok || cmdMeta.REST == nil {
			t.Fatalf("missing kernel command or REST binding for %s: %+v", name, cmdMeta)
		}
		if cmdMeta.REST.Method != want.method || cmdMeta.REST.Path != want.path {
			t.Fatalf("kernel command %s REST = %s %s, want %s %s", name, cmdMeta.REST.Method, cmdMeta.REST.Path, want.method, want.path)
		}
	}

	ctx := context.Background()

	// sessions.scale validation & missing-session classification
	if _, err := kernel.Run(ctx, "sessions.scale", map[string]any{"session": "proj"}); err == nil || !strings.Contains(err.Error(), "no target counts specified") {
		t.Fatalf("expected no-targets validation error from sessions.scale, got %v", err)
	}
	if _, err := kernel.Run(ctx, "sessions.scale", map[string]any{
		"session": "proj",
		"targets": map[string]any{"ollama": -1},
	}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("expected negative-target validation error from sessions.scale, got %v", err)
	}
	if _, err := kernel.Run(ctx, "sessions.scale", map[string]any{
		"session": "proj",
		"targets": map[string]any{"unknown_harness": 2},
	}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("expected unknown-harness validation error from sessions.scale, got %v", err)
	}
	if _, err := kernel.Run(ctx, "sessions.scale", map[string]any{
		"session": "__nonexistent_wave13_scale_session__",
		"targets": map[string]any{"ollama": 1, "cursor": 1, "windsurf": 1, "aider": 1},
		"dry_run": true,
	}); err == nil || (!strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "does not exist")) {
		t.Fatalf("expected missing-session error from sessions.scale, got %v", err)
	}

	// sessions.rebalance validation & missing-session classification
	if _, err := kernel.Run(ctx, "sessions.rebalance", map[string]any{
		"session": "proj",
		"filter":  "bogus",
	}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("expected invalid-filter error from sessions.rebalance, got %v", err)
	}
	if _, err := kernel.Run(ctx, "sessions.rebalance", map[string]any{
		"session": "__nonexistent_wave13_rebalance_session__",
		"dry_run": true,
	}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected does-not-exist error from sessions.rebalance, got %v", err)
	}

	// sessions.coordinator* validation & missing-session classification
	for _, cmdName := range []string{"sessions.coordinator", "sessions.coordinator_digest", "sessions.coordinator_conflicts"} {
		if _, err := kernel.Run(ctx, cmdName, map[string]any{
			"session": "__nonexistent_wave13_coord_session__",
		}); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("expected does-not-exist error from %s, got %v", cmdName, err)
		}
	}
}

func TestRobotSwarmOpsFlagsAndDispatch(t *testing.T) {
	for _, schemaType := range []string{"scale", "rebalance", "review_queue", "coordinator", "webhook_outbox"} {
		schemaOut, err := robot.GetSchema(schemaType)
		if err != nil {
			t.Fatalf("GetSchema(%q): %v", schemaType, err)
		}
		if !schemaOut.Success || schemaOut.Schema == nil {
			t.Fatalf("GetSchema(%q) unexpected output: %+v", schemaType, schemaOut)
		}
	}

	homeDir := t.TempDir()
	configHome := t.TempDir()
	dataHome := t.TempDir()
	projectDir := t.TempDir()

	runRobotSubprocess := func(args ...string) ([]byte, int) {
		t.Helper()
		rawArgs, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
		cmd.Dir = projectDir
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

	// 1. --robot-webhook-outbox list -> exit 0
	outboxRaw, code := runRobotSubprocess("--robot-webhook-outbox", "--outbox-action=list", "--project="+projectDir)
	if code != 0 {
		t.Fatalf("--robot-webhook-outbox list exit=%d, out=%s", code, string(outboxRaw))
	}
	var outboxOut robot.WebhookOutboxOutput
	if err := json.Unmarshal(outboxRaw, &outboxOut); err != nil {
		t.Fatalf("decode --robot-webhook-outbox list: %v (raw=%s)", err, string(outboxRaw))
	}
	if !outboxOut.Success || outboxOut.SchemaID != "ntm:robot:webhook-outbox:v1" || outboxOut.Action != "list" || outboxOut.Snapshot == nil {
		t.Fatalf("unexpected --robot-webhook-outbox list output: %+v", outboxOut)
	}

	// 2. --robot-webhook-outbox retry without record/revision -> exit 1 + INVALID_FLAG
	outboxErrRaw, code := runRobotSubprocess("--robot-webhook-outbox", "--outbox-action=retry", "--project="+projectDir)
	if code == 0 {
		t.Fatalf("--robot-webhook-outbox retry without record should exit non-zero: %s", string(outboxErrRaw))
	}
	var outboxErrOut robot.WebhookOutboxOutput
	if err := json.Unmarshal(outboxErrRaw, &outboxErrOut); err != nil {
		t.Fatalf("decode --robot-webhook-outbox retry error: %v", err)
	}
	if outboxErrOut.Success || outboxErrOut.ErrorCode != robot.ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG on retry without record, got %+v", outboxErrOut)
	}

	// 3. --robot-scale validation (no targets -> INVALID_FLAG; missing session -> SESSION_NOT_FOUND)
	scaleNoTargetRaw, code := runRobotSubprocess("--robot-scale=myproject")
	if code == 0 {
		t.Fatalf("--robot-scale without targets should exit non-zero: %s", string(scaleNoTargetRaw))
	}
	var scaleNoTargetOut robot.ScaleOutput
	if err := json.Unmarshal(scaleNoTargetRaw, &scaleNoTargetOut); err != nil {
		t.Fatalf("decode --robot-scale error: %v", err)
	}
	if scaleNoTargetOut.Success || scaleNoTargetOut.ErrorCode != robot.ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG on --robot-scale without targets, got %+v", scaleNoTargetOut)
	}

	scaleMissingRaw, code := runRobotSubprocess(
		"--robot-scale=__missing_wave13_session__",
		"--scale-ollama=1",
		"--scale-cursor=1",
		"--scale-windsurf=1",
		"--scale-aider=1",
		"--dry-run",
	)
	if code == 0 {
		t.Fatalf("--robot-scale missing session should exit non-zero: %s", string(scaleMissingRaw))
	}
	var scaleMissingOut robot.ScaleOutput
	if err := json.Unmarshal(scaleMissingRaw, &scaleMissingOut); err != nil {
		t.Fatalf("decode --robot-scale missing session: %v", err)
	}
	if scaleMissingOut.Success || scaleMissingOut.ErrorCode != robot.ErrCodeSessionNotFound {
		t.Fatalf("expected SESSION_NOT_FOUND on --robot-scale missing session, got %+v", scaleMissingOut)
	}

	// 4. --robot-rebalance validation (invalid --type -> INVALID_FLAG; missing session -> SESSION_NOT_FOUND)
	rebalBadTypeRaw, code := runRobotSubprocess("--robot-rebalance=myproject", "--type=bogus")
	if code == 0 {
		t.Fatalf("--robot-rebalance invalid type should exit non-zero: %s", string(rebalBadTypeRaw))
	}
	var rebalBadTypeOut robot.RebalanceOutput
	if err := json.Unmarshal(rebalBadTypeRaw, &rebalBadTypeOut); err != nil {
		t.Fatalf("decode --robot-rebalance invalid type: %v", err)
	}
	if rebalBadTypeOut.Success || rebalBadTypeOut.ErrorCode != robot.ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG on --robot-rebalance invalid type, got %+v", rebalBadTypeOut)
	}

	rebalMissingRaw, code := runRobotSubprocess("--robot-rebalance=__missing_wave13_session__", "--dry-run")
	if code == 0 {
		t.Fatalf("--robot-rebalance missing session should exit non-zero: %s", string(rebalMissingRaw))
	}
	var rebalMissingOut robot.RebalanceOutput
	if err := json.Unmarshal(rebalMissingRaw, &rebalMissingOut); err != nil {
		t.Fatalf("decode --robot-rebalance missing session: %v", err)
	}
	if rebalMissingOut.Success || rebalMissingOut.ErrorCode != robot.ErrCodeSessionNotFound {
		t.Fatalf("expected SESSION_NOT_FOUND on --robot-rebalance missing session, got %+v", rebalMissingOut)
	}

	// 5. --robot-review-queue validation (invalid --type -> INVALID_FLAG; missing session -> SESSION_NOT_FOUND)
	rqBadTypeRaw, code := runRobotSubprocess("--robot-review-queue=myproject", "--type=bogus")
	if code == 0 {
		t.Fatalf("--robot-review-queue invalid type should exit non-zero: %s", string(rqBadTypeRaw))
	}
	var rqBadTypeOut robot.ReviewQueueOutput
	if err := json.Unmarshal(rqBadTypeRaw, &rqBadTypeOut); err != nil {
		t.Fatalf("decode --robot-review-queue invalid type: %v", err)
	}
	if rqBadTypeOut.Success || rqBadTypeOut.ErrorCode != robot.ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG on --robot-review-queue invalid type, got %+v", rqBadTypeOut)
	}

	rqMissingRaw, code := runRobotSubprocess("--robot-review-queue=__missing_wave13_session__")
	if code == 0 {
		t.Fatalf("--robot-review-queue missing session should exit non-zero: %s", string(rqMissingRaw))
	}
	var rqMissingOut robot.ReviewQueueOutput
	if err := json.Unmarshal(rqMissingRaw, &rqMissingOut); err != nil {
		t.Fatalf("decode --robot-review-queue missing session: %v", err)
	}
	if rqMissingOut.Success || rqMissingOut.ErrorCode != robot.ErrCodeSessionNotFound {
		t.Fatalf("expected SESSION_NOT_FOUND on --robot-review-queue missing session, got %+v", rqMissingOut)
	}

	// 6. --robot-coordinator validation (invalid action -> INVALID_FLAG; missing session -> SESSION_NOT_FOUND)
	coordBadActionRaw, code := runRobotSubprocess("--robot-coordinator=myproject", "--coordinator-action=bogus")
	if code == 0 {
		t.Fatalf("--robot-coordinator invalid action should exit non-zero: %s", string(coordBadActionRaw))
	}
	var coordBadActionOut robot.CoordinatorOutput
	if err := json.Unmarshal(coordBadActionRaw, &coordBadActionOut); err != nil {
		t.Fatalf("decode --robot-coordinator invalid action: %v", err)
	}
	if coordBadActionOut.Success || coordBadActionOut.ErrorCode != robot.ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG on --robot-coordinator invalid action, got %+v", coordBadActionOut)
	}

	coordMissingRaw, code := runRobotSubprocess("--robot-coordinator=__missing_wave13_session__", "--coordinator-action=status")
	if code == 0 {
		t.Fatalf("--robot-coordinator missing session should exit non-zero: %s", string(coordMissingRaw))
	}
	var coordMissingOut robot.CoordinatorOutput
	if err := json.Unmarshal(coordMissingRaw, &coordMissingOut); err != nil {
		t.Fatalf("decode --robot-coordinator missing session: %v", err)
	}
	if coordMissingOut.Success || coordMissingOut.ErrorCode != robot.ErrCodeSessionNotFound {
		t.Fatalf("expected SESSION_NOT_FOUND on --robot-coordinator missing session, got %+v", coordMissingOut)
	}
}
