package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func swarmRotationCommandFixture(t *testing.T) (string, string, *[]resilience.SpawnMonitorRequest) {
	t.Helper()
	project, dir := swarmCommandFixture(t)
	cfg.Agents = config.DefaultAgentTemplates()
	cfg.Models = config.DefaultModels()
	cfg.Integrations.CAAM = config.DefaultCAAMConfig()
	for _, key := range []string{"CODEX_HOME", "OPENAI_API_KEY", "OPENAI_BASE_URL", "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		t.Setenv(key, "")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	caam := filepath.Join(dir, "caam")
	if err := os.WriteFile(caam, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Integrations.CAAM.BinaryPath = caam
	oldAccount, oldPreflight, oldStart := swarmPreflightAccountRotation, swarmPreflightSessionMonitor, swarmStartSessionMonitor
	t.Cleanup(func() {
		swarmPreflightAccountRotation, swarmPreflightSessionMonitor, swarmStartSessionMonitor = oldAccount, oldPreflight, oldStart
	})
	swarmPreflightAccountRotation = func(ctx context.Context, policy config.CAAMConfig) error {
		if !policy.AutoFailover || !filepath.IsAbs(policy.BinaryPath) || len(policy.FailoverProviders) == 0 {
			t.Errorf("preflight policy did not carry resolved operator intent: %+v", policy)
		}
		return ctx.Err()
	}
	swarmPreflightSessionMonitor = func(req resilience.SpawnMonitorRequest) error {
		if !filepath.IsAbs(req.ProjectDir) || !filepath.IsAbs(req.ConfigPath) || req.AccountRotation == nil {
			t.Errorf("monitor preflight lacks durable scope: %+v", req)
		}
		return nil
	}
	requests := []resilience.SpawnMonitorRequest{}
	swarmStartSessionMonitor = func(ctx context.Context, req resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
		requests = append(requests, req)
		return &resilience.SpawnMonitorResult{MonitorStarted: true, MonitorPID: 4242, Generation: "confirmed-" + req.Session}, nil
	}
	return project, dir, &requests
}

func TestSwarmAccountRotationJSONRequiresConfirmedResidentReceipts(t *testing.T) {
	project, dir, requests := swarmRotationCommandFixture(t)
	cfg.Agents.Claude = "claude --model 'saved-opus' --effort high"
	cfg.Agents.Codex = "codex -m 'saved-codex' -c model_reasoning_effort=high"
	out, err := executeSwarmCommandJSON(t, context.Background(), "--projects", project, "--auto-rotate-accounts", "--force-global-auth-clobber")
	if err != nil || !out.Success || out.Execution == nil || out.AccountRotation == nil {
		t.Fatalf("rotation launch = %+v, %v", out, err)
	}
	rotation := out.AccountRotation
	if strings.Join(rotation.Providers, ",") != "claude,openai" || rotation.EligiblePanes != 2 || len(rotation.Monitors) != 2 || len(*requests) != 2 {
		t.Fatalf("resolved resident scope = %+v requests=%+v", rotation, *requests)
	}
	for i, receipt := range rotation.Monitors {
		if !receipt.Started || receipt.PID != 4242 || receipt.Generation != "confirmed-"+receipt.Session || len(receipt.PaneIDs) != 1 {
			t.Errorf("unconfirmed resident receipt: %+v", receipt)
		}
		req := (*requests)[i]
		if req.AutoRestart || req.AccountRotation == nil || !req.AccountRotation.ForceGlobalAuthClobber || len(req.Agents) != 1 || req.Agents[0].ProjectDir != project {
			t.Fatalf("monitor request lost scope/force/project: %+v", req)
		}
		encoded, err := os.ReadFile(filepath.Join(dir, req.Agents[0].PaneID+"-launch"))
		if err != nil {
			t.Fatal(err)
		}
		data, err := base64.StdEncoding.DecodeString(string(encoded))
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			Spec tmux.AgentLaunchSpec `json:"spec"`
		}
		if err := json.Unmarshal(data, &saved); err != nil || saved.Spec.Command != req.Agents[0].Command || !strings.Contains(saved.Spec.Command, "saved-") {
			t.Fatalf("launch and durable recovery settings differ: %s / %+v / %v", data, req.Agents[0], err)
		}
	}
	// Explicit model and effort arguments must reach the physical pane, as
	// well as the durable record consumed by unattended recovery.
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if !strings.Contains(string(calls), "&& claude --model 'saved-opus' --effort high") || !strings.Contains(string(calls), "&& codex -m 'saved-codex'") {
		t.Fatalf("configured agent commands were not launched: %s", calls)
	}
}

func TestSwarmAccountRotationReportsPartialMonitorStartup(t *testing.T) {
	project, _, _ := swarmRotationCommandFixture(t)
	swarmStartSessionMonitor = func(ctx context.Context, req resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
		if req.Session == "cc_agents_1" {
			return &resilience.SpawnMonitorResult{MonitorStarted: true, MonitorPID: 45, Generation: "live"}, nil
		}
		return &resilience.SpawnMonitorResult{MonitorPID: 46, Generation: "failed"}, errors.New("startup readiness timed out")
	}
	out, err := executeSwarmCommandJSON(t, context.Background(), "--projects", project, "--auto-rotate-accounts", "--force-global-auth-clobber")
	if err == nil || out.Success || out.Execution == nil || out.Execution.Launch.Successful != 2 || out.AccountRotation == nil {
		t.Fatalf("monitor failure erased partial launch or reported success: %+v, %v", out, err)
	}
	monitors := out.AccountRotation.Monitors
	if len(monitors) != 2 || !monitors[0].Started || monitors[1].Started || !strings.Contains(monitors[1].Error, "timed out") || len(out.Execution.Errors) == 0 {
		t.Fatalf("monitor outcomes lack actual readiness: %+v", out)
	}
}

func TestSwarmAccountRotationNeverReportsUnacknowledgedStart(t *testing.T) {
	project, _, _ := swarmRotationCommandFixture(t)
	swarmStartSessionMonitor = func(context.Context, resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
		return &resilience.SpawnMonitorResult{MonitorPID: 88}, nil
	}
	out, err := executeSwarmCommandJSON(t, context.Background(), "--projects", project, "--auto-rotate-accounts")
	if err == nil || out.Success || out.AccountRotation.Monitors[0].Started || !strings.Contains(out.AccountRotation.Monitors[0].Error, "acknowledge") {
		t.Fatalf("cmd.Start-style receipt was accepted: %+v, %v", out, err)
	}
}

func TestSwarmAccountRotationPreflightPreventsSideEffects(t *testing.T) {
	for _, cause := range []string{"remote", "global_remote", "profile", "isolation", "credential_env", "command_env", "command_wrapper", "command_quote", "command_template", "command_routing", "command_workdir", "accounts", "resident_disabled"} {
		t.Run(cause, func(t *testing.T) {
			project, dir, requests := swarmRotationCommandFixture(t)
			args := []string{"--projects", project, "--auto-rotate-accounts", "--force-global-auth-clobber"}
			switch cause {
			case "remote":
				args = append(args, "--remote", "test@host")
			case "global_remote":
				tmux.DefaultClient.Remote = "test@host"
			case "profile":
				t.Setenv("SHALLOW_PROFILE", "private-profile")
			case "isolation":
				cfg.Agents.ClaudeIsolateCredentials = true
			case "credential_env":
				t.Setenv("OPENAI_API_KEY", "must-never-appear-in-receipt")
			case "command_env":
				cfg.Agents.Codex = "OPENAI_API_KEY='must-never-appear-in-receipt' codex"
			case "command_wrapper":
				cfg.Agents.Codex = "env codex"
			case "command_quote":
				cfg.Agents.Codex = "codex --model 'unterminated"
			case "command_template":
				cfg.Agents.Codex = "codex --model {{ .MissingModel }}"
			case "command_routing":
				cfg.Agents.Codex = "codex -c model_provider='private'"
			case "command_workdir":
				cfg.Agents.Codex = "codex -C '/another-worktree'"
			case "accounts":
				swarmPreflightAccountRotation = func(context.Context, config.CAAMConfig) error { return errors.New("no verified alternate account") }
			case "resident_disabled":
				swarmPreflightSessionMonitor = func(resilience.SpawnMonitorRequest) error { return resilience.ErrInternalMonitorDisabled }
			}
			out, err := executeSwarmCommandJSON(t, context.Background(), args...)
			if err == nil || out.Success || out.Execution != nil || len(*requests) != 0 {
				t.Fatalf("failed preflight launched agents/monitors: %+v, %v, %+v", out, err, *requests)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Contains(string(calls), "new-session") || strings.Contains(string(calls), "send-keys") {
				t.Fatalf("preflight failure mutated tmux: %s", calls)
			}
			encoded, _ := json.Marshal(out)
			if strings.Contains(string(encoded), "must-never-appear-in-receipt") {
				t.Fatal("credential value leaked into receipt")
			}
			if cause == "command_env" && !strings.Contains(string(encoded), "OPENAI_API_KEY") {
				t.Fatal("environment restriction did not identify the offending key")
			}
		})
	}
}

func TestSwarmAccountRotationHonorsProviderRestrictionAndFailedLaunch(t *testing.T) {
	project, _, requests := swarmRotationCommandFixture(t)
	cfg.Integrations.CAAM.FailoverProviders = []string{"claude"}
	out, err := executeSwarmCommandJSON(t, context.Background(), "--projects", project, "--auto-rotate-accounts", "--force-global-auth-clobber")
	if err != nil || len(*requests) != 1 || (*requests)[0].Session != "cc_agents_1" || len(out.AccountRotation.Declined) != 1 || out.AccountRotation.Declined[0].AgentType != "cod" {
		t.Fatalf("provider policy was widened: %+v, %v, %+v", out, err, *requests)
	}
}

func TestSwarmAccountRotationDoesNotMonitorFailedPane(t *testing.T) {
	project, _, requests := swarmRotationCommandFixture(t)
	t.Setenv("NTM_SWARM_FAIL_METADATA", "%42")
	out, err := executeSwarmCommandJSON(t, context.Background(), "--projects", project, "--auto-rotate-accounts", "--force-global-auth-clobber")
	if err == nil || out.Success || len(*requests) != 1 || (*requests)[0].Session != "cc_agents_1" || len(out.AccountRotation.Monitors) != 1 {
		t.Fatalf("failed launch became a rotation target: %+v, %v, %+v", out, err, *requests)
	}
}

func TestSwarmAccountRotationPreviewDoesNotProbeOrStart(t *testing.T) {
	for _, preview := range []string{"plan", "--dry-run"} {
		t.Run(preview, func(t *testing.T) {
			project, _, requests := swarmRotationCommandFixture(t)
			swarmPreflightAccountRotation = func(context.Context, config.CAAMConfig) error { t.Fatal("preview probed CAAM"); return nil }
			swarmPreflightSessionMonitor = func(resilience.SpawnMonitorRequest) error { t.Fatal("preview preflighted resident"); return nil }
			out, err := executeSwarmCommandJSON(t, context.Background(), preview, "--projects", project, "--auto-rotate-accounts")
			if err != nil || !out.DryRun || out.Execution != nil || len(*requests) != 0 {
				t.Fatalf("preview had effects: %+v, %v", out, err)
			}
		})
	}
}

func TestSwarmAccountRotationRequiresExplicitCodexGlobalConsent(t *testing.T) {
	project, _, requests := swarmRotationCommandFixture(t)
	out, err := executeSwarmCommandJSON(t, context.Background(), "--projects", project, "--auto-rotate-accounts")
	if err != nil || !out.Success || out.AccountRotation == nil || out.AccountRotation.EligiblePanes != 1 || len(*requests) != 1 || (*requests)[0].Session != "cc_agents_1" {
		t.Fatalf("Claude rotation without Codex global consent = %+v, %v, %+v", out, err, *requests)
	}
	if len(out.AccountRotation.Declined) != 1 || out.AccountRotation.Declined[0].AgentType != "cod" || !strings.Contains(out.AccountRotation.Declined[0].Reason, "--force-global-auth-clobber") {
		t.Fatalf("Codex global credential boundary is missing: %+v", out.AccountRotation)
	}
}

func TestSwarmAccountRotationNoEligiblePanesPreventsLaunch(t *testing.T) {
	project, dir, requests := swarmRotationCommandFixture(t)
	cfg.Swarm.Tier3Allocation = config.AllocationSpec{Cod: 1}
	out, err := executeSwarmCommandJSON(t, context.Background(), "--projects", project, "--auto-rotate-accounts")
	if err == nil || out.Success || out.Execution != nil || len(*requests) != 0 || !strings.Contains(err.Error(), "no swarm panes support") {
		t.Fatalf("zero eligible rotation targets were accepted: %+v, %v", out, err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Contains(string(calls), "new-session") || strings.Contains(string(calls), "send-keys") {
		t.Fatalf("zero eligible rotation targets mutated tmux: %s", calls)
	}
}

func TestSwarmStopQuiescesResidentMonitorsBeforeAgentShutdown(t *testing.T) {
	for _, stopFailure := range []bool{false, true} {
		name := "joined"
		if stopFailure {
			name = "still_active"
		}
		t.Run(name, func(t *testing.T) {
			_, dir := swarmCommandFixture(t)
			t.Setenv("NTM_SWARM_EXISTING", "1")
			previousStop := swarmStopSessionMonitor
			t.Cleanup(func() { swarmStopSessionMonitor = previousStop })
			stopped := []string{}
			swarmStopSessionMonitor = func(ctx context.Context, session string) error {
				calls, err := os.ReadFile(filepath.Join(dir, "calls"))
				if err != nil {
					return err
				}
				if strings.Contains(string(calls), "kill-session") || strings.Contains(string(calls), "send-keys") {
					return errors.New("agents were interrupted before resident recovery stopped")
				}
				if stopFailure {
					return errors.New("recovery is still in flight")
				}
				stopped = append(stopped, session)
				return ctx.Err()
			}
			cmd := newSwarmStopCmd()
			cmd.SetArgs([]string{"--force"})
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			_, runErr := captureStdout(t, cmd.Execute)
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if stopFailure {
				if runErr == nil || !strings.Contains(runErr.Error(), "recovery is still in flight") || strings.Contains(string(calls), "kill-session") || strings.Contains(string(calls), "send-keys") {
					t.Fatalf("unjoined recovery did not prevent agent shutdown: %v\n%s", runErr, calls)
				}
			} else if runErr != nil || len(stopped) != 2 || strings.Count(string(calls), "kill-session") != 2 {
				t.Fatalf("joined shutdown did not stop both resident owners and sessions: stopped=%v err=%v\n%s", stopped, runErr, calls)
			}
		})
	}
}

func TestSwarmRemoteStatusAndStopLeaveLocalResidentsAlone(t *testing.T) {
	_, dir := swarmCommandFixture(t)
	t.Setenv("NTM_SWARM_EXISTING", "1")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	const ssh = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_SWARM_FIXTURE/ssh-calls"
for remote_command; do :; done
exec /bin/sh -c "$remote_command"
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(ssh), 0700); err != nil {
		t.Fatal(err)
	}
	tmux.DefaultClient.Remote = "recording@host"
	// A local status problem must not become the health of a same-named
	// remote session. If the CLI consults it, it would emit monitor_error.
	controlDir := filepath.Join(resilience.ManifestDir(), "monitors")
	if err := os.MkdirAll(controlDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "cc_agents_1-monitor.status.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	jsonOutput = true
	statusCmd := newSwarmStatusCmd()
	statusCmd.SetArgs(nil)
	statusCmd.SilenceErrors, statusCmd.SilenceUsage = true, true
	raw, err := captureStdout(t, statusCmd.Execute)
	if err != nil || !strings.Contains(raw, "cc_agents_1") || strings.Contains(raw, "account_rotation") || strings.Contains(raw, "monitor_error") {
		t.Fatalf("remote status used local resident state: %v\n%s", err, raw)
	}
	previousStop := swarmStopSessionMonitor
	t.Cleanup(func() { swarmStopSessionMonitor = previousStop })
	swarmStopSessionMonitor = func(context.Context, string) error {
		t.Error("remote shutdown attempted to stop a local resident")
		return errors.New("local resident must remain untouched")
	}
	stopCmd := newSwarmStopCmd()
	stopCmd.SetArgs([]string{"--force"})
	stopCmd.SilenceErrors, stopCmd.SilenceUsage = true, true
	if _, err := captureStdout(t, stopCmd.Execute); err != nil {
		t.Fatalf("recording remote shutdown failed: %v", err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "ssh-calls"))
	if strings.Count(string(calls), "kill-session") != 2 {
		t.Fatalf("remote shutdown did not destroy its own sessions: %s", calls)
	}
}
