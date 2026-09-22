package resilience

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestRestartAgentReplaysLatestPaneLaunchSpec(t *testing.T) {
	restore := saveHooks()
	defer restore()
	project := t.TempDir()
	worktree := t.TempDir()
	caam := filepath.Join(project, "caam")
	if err := os.WriteFile(caam, []byte("#!/bin/sh\n[ \"$1\" = shallow-spawn ] && [ \"$3\" = --print-env ] || exit 2\ncase \"$2\" in profile-current|profile-next) exit 0;; *) exit 3;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0
	cfg.Integrations.CAAM.BinaryPath = caam
	monitor := NewMonitor("session", project, cfg, true)
	monitor.RegisterAgentWithBinding("%71", 1, 0, "cc", "sonnet", "claude --model sonnet", &LaunchBinding{
		Provider: "cc", Launcher: "caam", Identifier: "old-profile",
	})
	state := monitor.agents["%71"]
	state.Healthy = false
	spec := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude,
		Command: "claude --model opus --permission-mode plan", Model: "opus", Persona: "reviewer",
		CAAMProfile: "profile-current", AgentMailProject: project,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var commands []string
	readCalls, replayCalls := 0, 0
	livePID := 111
	setHooksLocked(func() {
		isChildAliveFn = func(int) bool { return false }
		paneWorkingDirFn = func(gotCtx context.Context, paneID string) (string, error) {
			if gotCtx != ctx || paneID != "%71" {
				t.Fatalf("unexpected working directory lookup: %s", paneID)
			}
			return worktree, nil
		}
		findPaneFn = func(gotCtx context.Context, session, paneID string) (*tmux.Pane, error) {
			if gotCtx != ctx || session != "session" || paneID != "%71" {
				t.Fatalf("unexpected destination lookup: %s/%s", session, paneID)
			}
			return &tmux.Pane{ID: paneID, Index: 9, PID: livePID, Type: tmux.AgentClaude}, nil
		}
		readPaneLaunchSpecFn = func(gotCtx context.Context, paneID string) (*tmux.AgentLaunchSpec, error) {
			if gotCtx != ctx || paneID != "%71" {
				t.Fatalf("unexpected launch read context or pane: %s", paneID)
			}
			readCalls++
			copy := spec
			return &copy, nil
		}
		prepareAgentLaunchSpecFn = func(gotCtx context.Context, gotCfg *config.Config, saved tmux.AgentLaunchSpec, gotProject, session string, paneIndex int) (string, error) {
			replayCalls++
			if gotCtx != ctx || gotCfg != cfg || gotProject != worktree || session != "session" || paneIndex != 9 {
				t.Fatalf("replay did not use the live destination: %s/%d in %s", session, paneIndex, gotProject)
			}
			return PrepareAgentLaunchSpec(gotCtx, gotCfg, saved, gotProject, session, paneIndex)
		}
		buildPaneCmdFn = func(gotDir, command string) (string, error) {
			if gotDir != worktree {
				t.Fatalf("worktree agent restarted in shared root %q instead of %q", gotDir, worktree)
			}
			return tmux.BuildPaneCommand(gotDir, command)
		}
		prepareLaunchCommandFn = func(context.Context, string, string, *LaunchBinding, string) (string, LaunchAffinity, error) {
			t.Fatal("recorded launch fell back to stale manifest settings")
			return "", "", errors.New("unexpected legacy replay")
		}
		sendKeysFn = func(gotCtx context.Context, paneID, command string, enter bool) error {
			if gotCtx != ctx || paneID != "%71" || !enter {
				t.Fatalf("unexpected launch delivery: %s, enter=%v", paneID, enter)
			}
			commands = append(commands, command)
			return nil
		}
	})
	monitor.restartAgent(ctx, state)
	if len(commands) != 1 || !strings.Contains(commands[0], "--model opus --permission-mode plan") ||
		!strings.Contains(commands[0], "profile-current") || !strings.Contains(commands[0], "AGENT_MAIL_PROJECT=") ||
		strings.Contains(commands[0], "old-profile") || strings.Contains(commands[0], "--model sonnet") {
		t.Fatalf("restart did not preserve the latest model, persona command, project, and account: %v", commands)
	}
	if state.Model != "opus" || state.Command != spec.Command || state.PaneIndex != 9 || state.ShellPID != 111 ||
		state.LaunchBinding == nil || state.LaunchBinding.Identifier != "profile-current" || !state.Healthy {
		t.Fatalf("monitor did not adopt the replayed launch: %+v", state)
	}

	// The daemon remains alive while another manual restart changes the pane.
	// The next crash must read that update instead of caching the first replay.
	spec.Model = "haiku"
	spec.Command = "claude --model haiku --permission-mode plan"
	spec.CAAMProfile = "profile-next"
	livePID = 222 // Explicit robot restart also replaced the pane's shell.
	state.Healthy = false
	monitor.restartAgent(ctx, state)
	if len(commands) != 2 || !strings.Contains(commands[1], "--model haiku") || !strings.Contains(commands[1], "profile-next") {
		t.Fatalf("subsequent crash reused a stale launch: %v", commands)
	}
	if state.RestartCount != 2 || state.Model != "haiku" || state.ShellPID != 222 || state.LaunchBinding.Identifier != "profile-next" || replayCalls != 2 || readCalls != 4 {
		t.Fatalf("unexpected replay state: agent=%+v replays=%d reads=%d", state, replayCalls, readCalls)
	}
}

func TestRestartAgentRejectsUnavailableLaunchInputsWithoutLegacyFallback(t *testing.T) {
	for _, failure := range []string{"read error", "tracked provider mismatch", "live provider mismatch", "invalid version", "omitted environment", "missing prompt", "missing account", "cwd read error", "relative cwd", "missing cwd", "file cwd", "legacy changed shell", "service pane", "dead pane"} {
		t.Run(failure, func(t *testing.T) {
			restore := saveHooks()
			defer restore()
			project := t.TempDir()
			cfg := config.Default()
			cfg.Resilience.RestartDelaySeconds = 0
			cfg.Integrations.CAAM.BinaryPath = filepath.Join(project, "missing-caam")
			monitor := NewMonitor("session", project, cfg, true)
			monitor.RegisterAgent("%72", 1, 0, "cc", "sonnet", "claude --model sonnet")
			state := monitor.agents["%72"]
			state.Healthy = false
			if failure == "legacy changed shell" {
				state.ShellPID = 111
			}
			spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude, Command: "claude --model opus"}
			liveType := tmux.AgentClaude
			var readErr error
			switch failure {
			case "read error":
				readErr = errors.New("malformed pane option")
			case "tracked provider mismatch":
				spec.AgentType = tmux.AgentGemini
				liveType = tmux.AgentGemini
			case "live provider mismatch":
				liveType = tmux.AgentCodex
			case "invalid version":
				spec.Version++
			case "omitted environment":
				spec.OmittedEnv = []string{"CUSTOM_PROVIDER_TOKEN"}
				t.Setenv("CUSTOM_PROVIDER_TOKEN", "ambient-value-must-not-be-used")
			case "missing prompt":
				spec.SystemPromptFile = filepath.Join(project, "missing-prompt.md")
				spec.SystemPromptSHA256 = strings.Repeat("0", 64)
			case "missing account":
				spec.CAAMProfile = "unavailable-profile"
			}
			builds, sends, legacyCalls := 0, 0, 0
			setHooksLocked(func() {
				isChildAliveFn = func(int) bool { return false }
				paneWorkingDirFn = func(context.Context, string) (string, error) {
					switch failure {
					case "cwd read error":
						return "", errors.New("tmux unreachable")
					case "relative cwd":
						return "relative/worktree", nil
					case "missing cwd":
						return filepath.Join(project, "missing-worktree"), nil
					case "file cwd":
						file := filepath.Join(project, "file")
						if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
							t.Fatal(err)
						}
						return file, nil
					}
					return project, nil
				}
				findPaneFn = func(context.Context, string, string) (*tmux.Pane, error) {
					pane := &tmux.Pane{ID: "%72", Index: 1, PID: 222, Type: liveType}
					if failure == "service pane" {
						pane.Service = "cm"
					}
					pane.Dead = failure == "dead pane"
					return pane, nil
				}
				readPaneLaunchSpecFn = func(context.Context, string) (*tmux.AgentLaunchSpec, error) {
					if failure == "legacy changed shell" {
						return nil, nil
					}
					return &spec, readErr
				}
				prepareLaunchCommandFn = func(context.Context, string, string, *LaunchBinding, string) (string, LaunchAffinity, error) {
					legacyCalls++
					return "claude --model sonnet", LaunchAffinityUnknown, nil
				}
				buildPaneCmdFn = func(_, command string) (string, error) { builds++; return command, nil }
				sendKeysFn = func(context.Context, string, string, bool) error { sends++; return nil }
			})
			monitor.restartAgent(context.Background(), state)
			if builds != 0 || sends != 0 || legacyCalls != 0 || state.RestartCount != 0 || state.Healthy || !state.LastRestart.IsZero() {
				t.Fatalf("failed launch consumed an attempt or fell back: builds=%d sends=%d legacy=%d agent=%+v", builds, sends, legacyCalls, state)
			}
		})
	}
}

func TestRestartAgentConcurrentRecoveryWinsDuringPreflight(t *testing.T) {
	for _, change := range []string{"pane disappears", "shell changes", "index changes", "provider changes", "launch changes", "launch disappears", "legacy gains launch", "launch read fails", "cwd changes", "cwd read fails", "pane becomes service", "pane dies", "process recovers", "agent re-registered", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			restore := saveHooks()
			defer restore()
			cfg := config.Default()
			cfg.Resilience.RestartDelaySeconds = 0
			project := t.TempDir()
			worktree := t.TempDir()
			monitor := NewMonitor("session", project, cfg, true)
			monitor.RegisterAgent("%73", 1, 111, "cc", "sonnet", "claude --model sonnet")
			state := monitor.agents["%73"]
			state.Healthy = false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			preflightComplete, sends := false, 0
			setHooksLocked(func() {
				paneWorkingDirFn = func(context.Context, string) (string, error) {
					if preflightComplete {
						switch change {
						case "cwd changes":
							return project, nil
						case "cwd read fails":
							return "", errors.New("tmux unreachable")
						}
					}
					return worktree, nil
				}
				findPaneFn = func(context.Context, string, string) (*tmux.Pane, error) {
					pane := &tmux.Pane{ID: "%73", Index: 1, PID: 111, Type: tmux.AgentClaude}
					if preflightComplete {
						switch change {
						case "pane disappears":
							return nil, nil
						case "shell changes":
							pane.PID = 222
						case "index changes":
							pane.Index = 2
						case "provider changes":
							pane.Type = tmux.AgentCodex
						case "pane becomes service":
							pane.Service = "cm"
						case "pane dies":
							pane.Dead = true
						}
					}
					return pane, nil
				}
				readPaneLaunchSpecFn = func(context.Context, string) (*tmux.AgentLaunchSpec, error) {
					spec := &tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude, Command: "claude --model opus"}
					if change == "legacy gains launch" && !preflightComplete {
						return nil, nil
					}
					if preflightComplete {
						switch change {
						case "launch changes":
							spec.Command = "claude --model haiku"
						case "launch disappears":
							return nil, nil
						case "launch read fails":
							return nil, errors.New("tmux unreachable")
						}
					}
					return spec, nil
				}
				prepareAgentLaunchSpecFn = func(context.Context, *config.Config, tmux.AgentLaunchSpec, string, string, int) (string, error) {
					preflightComplete = true
					if change == "agent re-registered" {
						monitor.RegisterAgent("%73", 1, 111, "cc", "haiku", "claude --model haiku")
					}
					if change == "cancelled" {
						cancel()
					}
					return "claude --model opus", nil
				}
				prepareLaunchCommandFn = func(context.Context, string, string, *LaunchBinding, string) (string, LaunchAffinity, error) {
					preflightComplete = true
					return "claude --model sonnet", LaunchAffinityUnknown, nil
				}
				isChildAliveFn = func(int) bool { return preflightComplete && change == "process recovers" }
				buildPaneCmdFn = func(_, command string) (string, error) { return command, nil }
				sendKeysFn = func(context.Context, string, string, bool) error { sends++; return nil }
			})
			monitor.restartAgent(ctx, state)
			if !preflightComplete || sends != 0 || state.RestartCount != 0 || !state.LastRestart.IsZero() {
				t.Fatalf("pending restart overrode concurrent recovery: prepared=%v sends=%d agent=%+v", preflightComplete, sends, state)
			}
			switch change {
			case "pane disappears":
				if _, exists := monitor.agents["%73"]; exists {
					t.Fatal("pane that disappeared during preflight was not retired")
				}
			case "process recovers":
				if !state.Healthy {
					t.Fatal("recovered process remained marked crashed")
				}
			case "agent re-registered":
				current := monitor.agents["%73"]
				if current == state || current.Model != "haiku" || !current.Healthy || current.RestartCount != 0 {
					t.Fatalf("new registration was changed by pending restart: %+v", current)
				}
			}
		})
	}
}
