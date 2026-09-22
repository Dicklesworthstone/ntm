package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Exercise the real Cobra command, planner, orchestrator, and tmux subprocess
// transport. The recording executable models two sessions with deliberately
// non-default window/pane indices and records every mutation.
func swarmCommandFixture(t *testing.T) (string, string) {
	t.Helper()
	oldCfg, oldJSON, oldClient := cfg, jsonOutput, tmux.DefaultClient
	t.Cleanup(func() { cfg, jsonOutput, tmux.DefaultClient = oldCfg, oldJSON, oldClient })
	cfg = &config.Config{Swarm: config.DefaultSwarmConfig()}
	cfg.Swarm.Enabled = true
	cfg.Swarm.SessionsPerType = 1
	cfg.Swarm.StaggerDelayMs = 1
	cfg.Swarm.Tier3Allocation = config.AllocationSpec{CC: 1, Cod: 1}
	jsonOutput = false
	tmux.DefaultClient = tmux.NewClient("")
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NTM_SWARM_FIXTURE", dir)
	t.Setenv("NTM_SWARM_FAIL_CREATE", "")
	t.Setenv("NTM_SWARM_FAIL_LAUNCH", "")
	t.Setenv("NTM_SWARM_FAIL_METADATA", "")
	t.Setenv("NTM_SWARM_BLOCK", "")
	t.Setenv("NTM_SWARM_NOT_READY", "")
	t.Setenv("SHALLOW_PROFILE", "")
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_SWARM_FIXTURE/calls"
action=$1
shift
target= name= title= format= value= option=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -t) target=$2; shift 2 ;;
    -s) if [ "$action" = new-session ]; then name=$2; shift 2; else shift; fi ;;
    -T) title=$2; shift 2 ;;
    -F) format=$2; shift 2 ;;
    @ntm_agent_launch) option=$1; value=$2; shift 2 ;;
    *) value=$1; shift ;;
  esac
done
target=${target#=}
session=${target%%:*}
case "$session" in
  cc_agents_1|%41) pane=%41; session=cc_agents_1 ;;
  cod_agents_1|%42) pane=%42; session=cod_agents_1 ;;
  *) pane=%0 ;;
esac
if [ "$action" = "$NTM_SWARM_BLOCK" ]; then
  if [ "$action" != new-session ] || [ "$name" = cod_agents_1 ]; then
    echo ready > "$NTM_SWARM_FIXTURE/blocked"
    exec sleep 30
  fi
fi
case "$action" in
  has-session) echo "can't find session" >&2; exit 1 ;;
  new-session)
    if [ "$name" = "$NTM_SWARM_FAIL_CREATE" ]; then echo 'creation refused' >&2; exit 1; fi
    echo created > "$NTM_SWARM_FIXTURE/$name"
    ;;
  list-windows) echo 4 ;;
  list-panes)
    if [ "$format" = '#{pane_index}' ]; then echo 7; exit 0; fi
    title=
    if [ -f "$NTM_SWARM_FIXTURE/$pane-title" ]; then title=$(cat "$NTM_SWARM_FIXTURE/$pane-title"); fi
    command=bash
    if [ -f "$NTM_SWARM_FIXTURE/$pane-command" ]; then command=$(cat "$NTM_SWARM_FIXTURE/$pane-command"); fi
    printf '%s_NTM_SEP_7_NTM_SEP_%s_NTM_SEP_%s_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_4_NTM_SEP__NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$pane" "$title" "$command"
    ;;
  select-pane) printf '%s' "$title" > "$NTM_SWARM_FIXTURE/$pane-title" ;;
  set-option)
    if [ "$option" = '@ntm_agent_launch' ]; then
      if [ "$pane" = "$NTM_SWARM_FAIL_METADATA" ]; then echo 'metadata refused' >&2; exit 1; fi
      printf '%s' "$value" > "$NTM_SWARM_FIXTURE/$pane-launch"
    fi
    ;;
  send-keys)
    if [ -n "$NTM_SWARM_FAIL_LAUNCH" ] && [ "${value##* && }" = "$NTM_SWARM_FAIL_LAUNCH" ]; then echo 'launch refused' >&2; exit 1; fi
    case "$value" in
      *' && cc') echo claude > "$NTM_SWARM_FIXTURE/$pane-command" ;;
      *' && cod') echo codex > "$NTM_SWARM_FIXTURE/$pane-command" ;;
    esac
    ;;
  display-message)
    if [ "$value" = '#{session_name}' ]; then echo "$session"; else echo 0; fi
    ;;
  capture-pane)
    if [ -z "$NTM_SWARM_NOT_READY" ]; then
      case "$pane" in
        %41) echo 'claude>' ;;
        %42) echo 'codex>' ;;
      esac
    fi
    ;;
esac
`
	bin := filepath.Join(dir, "tmux")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	return project, dir
}

func executeSwarmCommandJSON(t *testing.T, ctx context.Context, args ...string) (SwarmPlanOutput, error) {
	t.Helper()
	cmd := &cobra.Command{Use: "ntm", SilenceErrors: true, SilenceUsage: true}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
	cmd.AddCommand(newSwarmCmd())
	cmd.SetArgs(append([]string{"swarm", "--json"}, args...))
	cmd.SetContext(ctx)
	cmd.SetErr(io.Discard)
	raw, runErr := captureStdout(t, cmd.Execute)
	var out SwarmPlanOutput
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&out); err != nil {
		t.Fatalf("expected JSON document, got %q (execute error %v): %v", raw, runErr, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout contains more than one JSON document: %q", raw)
	}
	return out, runErr
}

func TestSwarmCommandJSONLaunchExecutesAndReportsReadiness(t *testing.T) {
	project, dir := swarmCommandFixture(t)
	out, err := executeSwarmCommandJSON(t, context.Background(), "--projects", project, "--prompt", "implement the feature", "--wait-ready")
	if err != nil {
		t.Fatalf("launch failed: %v; response %+v", err, out)
	}
	if !out.Success || out.DryRun || out.Execution == nil {
		t.Fatalf("expected successful execution receipt, got %+v", out)
	}
	execution := out.Execution
	if len(execution.Sessions) != 2 || execution.SuccessfulPanes != 2 || execution.Launch == nil || execution.Launch.Successful != 2 {
		t.Fatalf("unexpected launch outcome: %+v", execution)
	}
	if execution.Injection == nil || execution.Injection.Successful != 2 || execution.Readiness == nil || execution.Readiness.Ready != 2 || execution.Readiness.Total != 2 {
		t.Fatalf("initial prompt and readiness were not completed: %+v", execution)
	}
	for _, sess := range execution.Sessions {
		if !sess.Created || len(sess.PaneIDs) != 1 {
			t.Fatalf("missing actual session creation: %+v", sess)
		}
		if _, err := os.Stat(filepath.Join(dir, sess.Name)); err != nil {
			t.Fatalf("reported uncreated session %s: %v", sess.Name, err)
		}
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, pane := range []string{"%41", "%42"} {
		if !strings.Contains(string(calls), "send-keys -t "+pane+" -l -- implement the feature") {
			t.Errorf("missing prompt delivery to physical pane %s:\n%s", pane, calls)
		}
		encoded, err := os.ReadFile(filepath.Join(dir, pane+"-launch"))
		if err != nil {
			t.Fatal(err)
		}
		data, err := base64.StdEncoding.DecodeString(string(encoded))
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			PaneID string               `json:"pane_id"`
			Spec   tmux.AgentLaunchSpec `json:"spec"`
		}
		if err := json.Unmarshal(data, &record); err != nil || record.PaneID != pane || record.Spec.Command == "" {
			t.Fatalf("missing durable pane-bound command: %s (%v)", data, err)
		}
	}
}

func TestSwarmCommandJSONPreviewsDoNotLaunch(t *testing.T) {
	for _, preview := range []string{"--dry-run", "plan"} {
		t.Run(preview, func(t *testing.T) {
			project, dir := swarmCommandFixture(t)
			cfg.Swarm.Enabled = false
			out, err := executeSwarmCommandJSON(t, context.Background(), preview, "--projects", project)
			if err != nil || !out.Success || !out.DryRun || out.Execution != nil || out.TotalAgents != 2 {
				t.Fatalf("preview response %+v, error %v", out, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "calls")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("preview invoked tmux: %v", err)
			}
		})
	}
}

func TestSwarmCommandJSONPartialFailurePreservesActualLaunches(t *testing.T) {
	for _, failure := range []string{"creation", "launch", "metadata"} {
		t.Run(failure, func(t *testing.T) {
			project, dir := swarmCommandFixture(t)
			switch failure {
			case "creation":
				t.Setenv("NTM_SWARM_FAIL_CREATE", "cod_agents_1")
			case "launch":
				t.Setenv("NTM_SWARM_FAIL_LAUNCH", "cod")
			case "metadata":
				t.Setenv("NTM_SWARM_FAIL_METADATA", "%42")
			}
			out, err := executeSwarmCommandJSON(t, context.Background(), "--projects", project, "--prompt", "implement the feature")
			if !errors.Is(err, errJSONFailure) || out.Success || out.Execution == nil {
				t.Fatalf("expected failed JSON receipt, got %+v, %v", out, err)
			}
			if out.Execution.Launch == nil || out.Execution.Launch.Successful != 1 || out.Execution.Injection == nil || out.Execution.Injection.Successful != 1 {
				t.Fatalf("successful launch lost or failed launch received prompt: %+v", out.Execution)
			}
			if failure == "creation" && out.Execution.FailedPanes != 1 {
				t.Fatalf("missing creation failure: %+v", out.Execution)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "kill-") || strings.Contains(string(calls), "send-keys -t %42 -l -- implement the feature") {
				t.Fatalf("partial failure destroyed state or sent prompt to failed agent:\n%s", calls)
			}
		})
	}
}

func TestSwarmCommandJSONCancellationPreservesPartialProgress(t *testing.T) {
	for _, block := range []string{"new-session", "capture-pane"} {
		t.Run(block, func(t *testing.T) {
			project, dir := swarmCommandFixture(t)
			t.Setenv("NTM_SWARM_BLOCK", block)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if _, err := os.Stat(filepath.Join(dir, "blocked")); err == nil {
							cancel()
							return
						}
					}
				}
			}()
			start := time.Now()
			out, err := executeSwarmCommandJSON(t, ctx, "--projects", project, "--prompt", "implement the feature")
			<-done
			if !errors.Is(err, context.Canceled) || out.Success || out.Execution == nil || !out.Execution.Interrupted {
				t.Fatalf("expected canceled receipt, got %+v, %v", out, err)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("cancellation did not stop blocked tmux operation")
			}
			if len(out.Execution.Sessions) == 0 || !out.Execution.Sessions[0].Created {
				t.Fatalf("lost completed session creation: %+v", out.Execution)
			}
			if block == "capture-pane" && (out.Execution.Launch == nil || out.Execution.Launch.Successful != 2 || out.Execution.Injection == nil || out.Execution.Injection.Successful != 0) {
				t.Fatalf("lost launches or sent prompt after cancellation: %+v", out.Execution)
			}
		})
	}
}

func TestSwarmCommandJSONReadinessTimeoutIsFailure(t *testing.T) {
	project, _ := swarmCommandFixture(t)
	t.Setenv("NTM_SWARM_NOT_READY", "true")
	out, err := executeSwarmCommandJSON(t, context.Background(), "--projects", project, "--wait-ready", "--ready-timeout", "1")
	if !errors.Is(err, context.DeadlineExceeded) || out.Success || out.ErrorCode != "TIMEOUT" || out.Execution == nil {
		t.Fatalf("readiness timeout was not reported: %+v, %v", out, err)
	}
	if out.Execution.Readiness == nil || out.Execution.Readiness.Ready != 0 || out.Execution.Readiness.Total != 2 || out.Execution.Launch.Successful != 2 {
		t.Fatalf("timeout lost launched panes or readiness counts: %+v", out.Execution)
	}
}

func TestWritePlanToFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "plans", "swarm_plan.json")

	createdAt := time.Now().UTC().Truncate(time.Second)
	plan := &swarm.SwarmPlan{
		CreatedAt:       createdAt,
		ScanDir:         "/tmp/projects",
		TotalCC:         1,
		TotalCod:        2,
		TotalGmi:        0,
		TotalAgents:     3,
		SessionsPerType: 2,
		PanesPerSession: 2,
	}

	if err := writePlanToFile(plan, path); err != nil {
		t.Fatalf("writePlanToFile error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}

	var got swarm.SwarmPlan
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}

	if got.ScanDir != plan.ScanDir {
		t.Errorf("ScanDir = %q, want %q", got.ScanDir, plan.ScanDir)
	}
	if got.TotalAgents != plan.TotalAgents {
		t.Errorf("TotalAgents = %d, want %d", got.TotalAgents, plan.TotalAgents)
	}
	if got.SessionsPerType != plan.SessionsPerType {
		t.Errorf("SessionsPerType = %d, want %d", got.SessionsPerType, plan.SessionsPerType)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, createdAt)
	}
}

func TestWritePlanToFileNilPlan(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "plan.json")

	if err := writePlanToFile(nil, path); err == nil {
		t.Fatal("expected error for nil plan, got nil")
	}
}

func TestSwarmCmd_AutoRotateAccountsFlag_DefaultFromConfig(t *testing.T) {
	prevCfg := cfg
	t.Cleanup(func() { cfg = prevCfg })

	cfg = &config.Config{
		Swarm: config.DefaultSwarmConfig(),
	}
	cfg.Swarm.AutoRotateAccounts = true

	cmd := newSwarmCmd()

	if cmd.PersistentFlags().Lookup("auto-rotate-accounts") == nil {
		t.Fatal("expected --auto-rotate-accounts flag to exist")
	}

	got, err := cmd.PersistentFlags().GetBool("auto-rotate-accounts")
	if err != nil {
		t.Fatalf("GetBool(auto-rotate-accounts) error: %v", err)
	}
	if got != true {
		t.Errorf("auto-rotate-accounts default = %v, want true", got)
	}
}

// TestSwarmCmd_ForceGlobalAuthClobberFlag_DefaultFromConfig is the bd-6otuk
// behavior proof for the wiring fix: swarm.force_global_auth_clobber used to
// be overwritten by the flag's hard-coded false default before any read.
// Now the config value seeds the flag default (flipping the knob changes the
// effective value), while an explicit flag still overrides.
func TestSwarmCmd_ForceGlobalAuthClobberFlag_DefaultFromConfig(t *testing.T) {
	prevCfg := cfg
	t.Cleanup(func() { cfg = prevCfg })

	cfg = &config.Config{
		Swarm: config.DefaultSwarmConfig(),
	}
	cfg.Swarm.ForceGlobalAuthClobber = true

	cmd := newSwarmCmd()

	flag := cmd.PersistentFlags().Lookup("force-global-auth-clobber")
	if flag == nil {
		t.Fatal("expected --force-global-auth-clobber flag to exist")
	}
	got, err := cmd.PersistentFlags().GetBool("force-global-auth-clobber")
	if err != nil {
		t.Fatalf("GetBool(force-global-auth-clobber) error: %v", err)
	}
	if got != true {
		t.Errorf("force-global-auth-clobber default = %v, want true (seeded from config)", got)
	}

	// With the knob off, the default stays false.
	cfg.Swarm.ForceGlobalAuthClobber = false
	cmd = newSwarmCmd()
	got, err = cmd.PersistentFlags().GetBool("force-global-auth-clobber")
	if err != nil {
		t.Fatalf("GetBool(force-global-auth-clobber) error: %v", err)
	}
	if got != false {
		t.Errorf("force-global-auth-clobber default = %v, want false", got)
	}
}

func TestSwarmCmd_PromptFlagsExist(t *testing.T) {
	cmd := newSwarmCmd()
	if cmd.Flags().Lookup("prompt") == nil {
		t.Fatal("expected --prompt flag to exist")
	}
	if cmd.Flags().Lookup("prompt-file") == nil {
		t.Fatal("expected --prompt-file flag to exist")
	}
}

func TestResolveSwarmInitialPrompt_MutuallyExclusive(t *testing.T) {
	_, _, _, err := resolveSwarmInitialPrompt("hi", "/tmp/prompt.txt")
	if err == nil {
		t.Fatal("expected error for mutually exclusive flags, got nil")
	}
}

func TestResolveSwarmInitialPrompt_PromptFlag(t *testing.T) {
	got, source, path, err := resolveSwarmInitialPrompt("hello", "")
	if err != nil {
		t.Fatalf("resolveSwarmInitialPrompt error: %v", err)
	}
	if got != "hello" {
		t.Errorf("prompt=%q, want %q", got, "hello")
	}
	if source != "flag" {
		t.Errorf("source=%q, want %q", source, "flag")
	}
	if path != "" {
		t.Errorf("path=%q, want empty", path)
	}
}

func TestResolveSwarmInitialPrompt_PromptFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "prompt.txt")
	if err := os.WriteFile(path, []byte("from-file"), 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	got, source, gotPath, err := resolveSwarmInitialPrompt("", path)
	if err != nil {
		t.Fatalf("resolveSwarmInitialPrompt error: %v", err)
	}
	if got != "from-file" {
		t.Errorf("prompt=%q, want %q", got, "from-file")
	}
	if source != "file" {
		t.Errorf("source=%q, want %q", source, "file")
	}
	if gotPath != path {
		t.Errorf("path=%q, want %q", gotPath, path)
	}
}

func TestResolveSwarmInitialPrompt_PromptFileReadError(t *testing.T) {
	_, _, _, err := resolveSwarmInitialPrompt("", "/definitely/does/not/exist.txt")
	if err == nil {
		t.Fatal("expected error for missing prompt file, got nil")
	}
}

func TestGlobToRegex(t *testing.T) {

	tests := []struct {
		name   string
		glob   string
		want   string
		match  string // Test string that should match
		reject string // Test string that should not match
	}{
		{"simple wildcard", "*.go", ".*\\.go", "main.go", "main.gox"},
		{"question mark", "a?c", "a.c", "abc", "abbc"},
		{"double star", "**/*.ts", ".*.*/.*\\.ts", "src/app.ts", ""},
		{"literal dots", "file.txt", "file\\.txt", "", ""},
		{"special chars escaped", "test[1].go", "test\\[1\\]\\.go", "", ""},
		{"complex pattern", "src/**/*.{js,ts}", "src/.*.*/.*\\.\\{js,ts\\}", "", ""},
		{"plain string", "hello", "hello", "hello", "world"},
		{"empty string", "", "", "", ""},
		{"parens escaped", "func()", "func\\(\\)", "", ""},
		{"caret escaped", "^start", "\\^start", "", ""},
		{"dollar escaped", "end$", "end\\$", "", ""},
		{"pipe escaped", "a|b", "a\\|b", "", ""},
		{"backslash escaped", "a\\b", "a\\\\b", "", ""},
		{"plus escaped", "a+b", "a\\+b", "", ""},
		{"braces escaped", "{a,b}", "\\{a,b\\}", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := globToRegex(tc.glob)
			if got != tc.want {
				t.Errorf("globToRegex(%q) = %q, want %q", tc.glob, got, tc.want)
			}
		})
	}
}

func TestBuildSwarmPlanOutput(t *testing.T) {

	plan := &swarm.SwarmPlan{
		ScanDir:         "/projects",
		TotalCC:         2,
		TotalCod:        3,
		TotalGmi:        1,
		TotalAgents:     6,
		SessionsPerType: 2,
		PanesPerSession: 4,
		Allocations: []swarm.ProjectAllocation{
			{
				Project: swarm.ProjectBeadCount{
					Name:      "proj1",
					Path:      "/projects/proj1",
					OpenBeads: 5,
					Tier:      1,
				},
				CCAgents:    1,
				CodAgents:   2,
				GmiAgents:   0,
				TotalAgents: 3,
			},
		},
		Sessions: []swarm.SessionSpec{
			{
				Name:      "cc_0",
				AgentType: "cc",
				PaneCount: 2,
				Panes: []swarm.PaneSpec{
					{Index: 0, Project: "proj1", AgentType: "cc"},
					{Index: 1, Project: "proj1", AgentType: "cc"},
				},
			},
		},
	}

	out := buildSwarmPlanOutput(plan, true)

	if !out.Success || out.Timestamp == "" {
		t.Fatalf("robot envelope = %+v, want successful timestamped response", out.RobotResponse)
	}
	if out.ScanDir != plan.ScanDir {
		t.Errorf("ScanDir = %q, want %q", out.ScanDir, plan.ScanDir)
	}
	if out.TotalCC != plan.TotalCC {
		t.Errorf("TotalCC = %d, want %d", out.TotalCC, plan.TotalCC)
	}
	if out.TotalCod != plan.TotalCod {
		t.Errorf("TotalCod = %d, want %d", out.TotalCod, plan.TotalCod)
	}
	if out.TotalGmi != plan.TotalGmi {
		t.Errorf("TotalGmi = %d, want %d", out.TotalGmi, plan.TotalGmi)
	}
	if out.TotalAgents != plan.TotalAgents {
		t.Errorf("TotalAgents = %d, want %d", out.TotalAgents, plan.TotalAgents)
	}
	if !out.DryRun {
		t.Error("DryRun should be true")
	}
	if len(out.Allocations) != 1 {
		t.Fatalf("Allocations length = %d, want 1", len(out.Allocations))
	}
	if out.Allocations[0].Project != "proj1" {
		t.Errorf("Allocations[0].Project = %q, want %q", out.Allocations[0].Project, "proj1")
	}
	if len(out.Sessions) != 1 {
		t.Fatalf("Sessions length = %d, want 1", len(out.Sessions))
	}
	if len(out.Sessions[0].Panes) != 2 {
		t.Errorf("Sessions[0].Panes length = %d, want 2", len(out.Sessions[0].Panes))
	}
}

func TestBuildSwarmPlanOutput_EmptyPlan(t *testing.T) {

	plan := &swarm.SwarmPlan{
		ScanDir:     "/empty",
		Allocations: []swarm.ProjectAllocation{},
		Sessions:    []swarm.SessionSpec{},
	}

	out := buildSwarmPlanOutput(plan, false)

	if out.ScanDir != "/empty" {
		t.Errorf("ScanDir = %q, want %q", out.ScanDir, "/empty")
	}
	if out.DryRun {
		t.Error("DryRun should be false")
	}
	if len(out.Allocations) != 0 {
		t.Errorf("Allocations length = %d, want 0", len(out.Allocations))
	}
	if len(out.Sessions) != 0 {
		t.Errorf("Sessions length = %d, want 0", len(out.Sessions))
	}
	if out.Allocations == nil || out.Sessions == nil {
		t.Fatalf("empty swarm arrays must encode as []: allocations=%#v sessions=%#v", out.Allocations, out.Sessions)
	}
}
