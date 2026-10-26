package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestNormalizeAgentType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"cc", "claude"},
		{"CC", "claude"},
		{"claude", "claude"},
		{"Claude", "claude"},
		{"claude_code", "claude"},
		{"claude-code", "claude"},
		{"cod", "codex"},
		{"codex", "codex"},
		{"openai-codex", "codex"},
		{"codex-cli", "codex"},
		{"codex_cli", "codex"},
		{"gmi", "gemini"},
		{"gemini", "gemini"},
		{"google-gemini", "gemini"},
		{"gemini-cli", "gemini"},
		{"gemini_cli", "gemini"},
		{"grok-build", "grok"},
		{"unknown", "unknown"},
		{"aider", "aider"},
		{"  cc  ", "claude"}, // whitespace handling
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeAgentType(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeAgentType(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestRespawnHelpIncludesGrokTypeFilter(t *testing.T) {
	flag := newRespawnCmd().Flags().Lookup("type")
	if flag == nil {
		t.Fatal("respawn --type flag is not registered")
	}
	if !strings.Contains(flag.Usage, "grok") || !strings.Contains(flag.Usage, "grok-build") {
		t.Fatalf("respawn --type help = %q, want Grok aliases", flag.Usage)
	}
}

func TestRespawnCommandPropagatesCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := newRespawnCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"ignored", "--force"})

	if err := cmd.Execute(); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled respawn command error=%v, want context.Canceled", err)
	}
}

func TestRestartEnvironmentFlagsAvailableOnBothSurfaces(t *testing.T) {
	for name, flag := range map[string]*pflag.Flag{
		"respawn": newRespawnCmd().Flags().Lookup("restart-env-file"),
		"robot":   liveRootFlag("restart-env-file"),
	} {
		if flag == nil || flag.Value.Type() != "string" || !strings.Contains(flag.Usage, "private") && !strings.Contains(flag.Usage, "Private") {
			t.Fatalf("%s recovery input flag missing or does not describe private file input", name)
		}
	}
}

func TestReadRestartEnvironmentPrivateSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recovery file permissions require POSIX modes")
	}
	if values, err := readRestartEnvironment(""); err != nil || values != nil {
		t.Fatalf("absent input = %v, %v; want nil, nil", values, err)
	}
	path := filepath.Join(t.TempDir(), "recovery.json")
	want := map[string]string{"AGENT_MAIL_AGENT": "BlueLake", "BEADS_DIR": "/tmp/project/.beads", "BV_ROBOT_NOT_READY_LABELS": "", "PATH": "/usr/bin:/bin", "ZAI_SEAT_ROLE": "reviewer"}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0600, 0400} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		values, err := readRestartEnvironment(path)
		if err != nil || !reflect.DeepEqual(values, want) {
			t.Fatalf("mode %o: values = %v, err = %v", mode, values, err)
		}
	}
}

func TestRestartEnvironmentRejectsOtherRobotSurface(t *testing.T) {
	root := t.TempDir()
	args, err := json.Marshal([]string{"--robot-version", "--restart-env-file=" + filepath.Join(root, "unused")})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
	cmd.Dir = root
	cmd.Env = envWithOverrides(os.Environ(), "HOME="+root, "XDG_CONFIG_HOME="+root,
		"NTM_CONFIG=", "NTM_NO_COLOR=1", "NTM_ROBOT_FORMAT=", "NTM_OUTPUT_FORMAT=", "TOON_DEFAULT_FORMAT=", "NTM_ROBOT_VERBOSITY=",
		"NTM_ROBOT_CONTRACT_ARGS="+string(args))
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("unused recovery input exit=%v output=%s", err, output)
	}
	var response robot.RobotResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("invalid robot error: %v; output=%s", err, output)
	}
	if response.Success || response.ErrorCode != robot.ErrCodeInvalidFlag || !strings.Contains(response.Error, "--restart-env-file is only supported") {
		t.Fatalf("unused recovery input was not rejected: %s", output)
	}
}

func TestReadRestartEnvironmentRejectsUnsafeInputWithoutEcho(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recovery file permissions require POSIX modes")
	}
	const secret = "private-recovery-input-marker"
	tests := []struct {
		name string
		data string
		mode os.FileMode
	}{
		{name: "group readable", data: `{"TOKEN":"` + secret + `"}`, mode: 0640},
		{name: "world writable", data: `{"TOKEN":"` + secret + `"}`, mode: 0602},
		{name: "owner unreadable", data: `{"TOKEN":"` + secret + `"}`, mode: 0200},
		{name: "empty"},
		{name: "null", data: `null`},
		{name: "array", data: `["` + secret + `"]`},
		{name: "duplicate", data: `{"TOKEN":"` + secret + `","TOKEN":"second"}`},
		{name: "null value", data: `{"TOKEN":null}`},
		{name: "number", data: `{"TOKEN":123}`},
		{name: "object value", data: `{"TOKEN":{"nested":"` + secret + `"}}`},
		{name: "malformed", data: `{"TOKEN":` + secret + `}`},
		{name: "trailing", data: `{"TOKEN":"` + secret + `"} {}`},
		{name: "invalid name", data: `{"TOKEN=` + secret + `":"other"}`},
		{name: "NUL", data: `{"TOKEN":"` + secret + `\u0000"}`},
		{name: "shell startup", data: `{"BASH_ENV":"` + secret + `"}`},
		{name: "invalid utf8", data: "{\"TOKEN\":\"" + secret + "\xff\"}"},
		{name: "oversized", data: strings.Repeat(" ", maxRestartEnvironmentFileBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "recovery.json")
			mode := tt.mode
			if mode == 0 {
				mode = 0600
			}
			if err := os.WriteFile(path, []byte(tt.data), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			values, err := readRestartEnvironment(path)
			if err == nil || values != nil {
				t.Fatalf("unsafe input accepted: returned=%t err=%v", values != nil, err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), path) {
				t.Fatal("recovery diagnostic exposed input contents or path")
			}
		})
	}
	root := t.TempDir()
	file := filepath.Join(root, "private.json")
	link := filepath.Join(root, "linked.json")
	if err := os.WriteFile(file, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"symlink": link, "directory": root, "missing": filepath.Join(root, "missing")} {
		t.Run(name, func(t *testing.T) {
			if _, err := readRestartEnvironment(path); err == nil {
				t.Fatal("non-regular recovery input accepted")
			}
		})
	}
}

// Exercise both public command parsers and their real restart preflight against
// a recording tmux executable. The fixture refuses actuation so a valid actual
// request proves it got past preflight without needing a running agent or server.
func TestRestartEnvironmentCLIRecoveryPreflight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording tmux executable requires a POSIX shell")
	}
	const secret = "ntm-cli-recovery-secret-marker"
	for _, surface := range []string{"human", "robot"} {
		for _, mode := range []string{"ordinary-dry", "missing-dry", "missing-real", "partial-dry", "complete-dry", "complete-real"} {
			t.Run(surface+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				binary := filepath.Join(root, "tmux")
				const script = `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_RESTART_CLI_ROOT/calls"
case "$1" in
  -V) printf 'tmux 3.4\n' ;;
  has-session) ;;
  list-sessions) printf 'recovery_NTM_SEP_2_NTM_SEP_0_NTM_SEP_today\n' ;;
  list-panes)
    printf '%%0_NTM_SEP_0_NTM_SEP_recovery__user_0_NTM_SEP_zsh_NTM_SEP_100_NTM_SEP_30_NTM_SEP_0_NTM_SEP_111_NTM_SEP_0_NTM_SEP_user\n'
    printf '%%7_NTM_SEP_0_NTM_SEP_notes_NTM_SEP_zsh_NTM_SEP_100_NTM_SEP_30_NTM_SEP_1_NTM_SEP_222_NTM_SEP_2_NTM_SEP_codex\n'
    ;;
  display-message)
    case "$*" in
      *@ntm_agent_type*) printf '[cod]\n' ;;
      *pane_pid*) printf '222\n' ;;
      *) printf '%s\n' "$NTM_RESTART_CLI_ROOT" ;;
    esac
    ;;
  show-options)
    case "$*" in
      *@ntm_agent_launch*) cat "$NTM_RESTART_CLI_ROOT/spec" ;;
    esac
    ;;
  respawn-pane) : > "$NTM_RESTART_CLI_ROOT/mutation-boundary"; exit 71 ;;
  if-shell)
    case "${6:-}" in
      *respawn-pane*) : > "$NTM_RESTART_CLI_ROOT/mutation-boundary"; exit 71 ;;
      *) exit 72 ;;
    esac
    ;;
esac
`
				if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex, Command: "codex --model saved/model", Model: "saved/model"}
				if mode != "ordinary-dry" {
					spec.OmittedEnv = []string{"AGENT_MAIL_AGENT", "BEADS_DIR", "BV_ROBOT_NOT_READY_LABELS", "PATH", "ZAI_SEAT_ROLE"}
				}
				record, err := json.Marshal(map[string]any{"pane_id": "%7", "spec": spec})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "spec"), []byte(base64.StdEncoding.EncodeToString(record)), 0600); err != nil {
					t.Fatal(err)
				}
				configPath := filepath.Join(root, "config.toml")
				if err := os.WriteFile(configPath, []byte("[agent_mail]\nenabled = false\n[cleanup]\nauto_clean_on_startup = false\n"), 0600); err != nil {
					t.Fatal(err)
				}
				args := []string{"--config=" + configPath}
				if surface == "human" {
					args = append(args, "respawn", "recovery", "--force")
				} else {
					args = append(args, "--robot-restart-pane=recovery")
				}
				if mode != "ordinary-dry" {
					args = append(args, "--panes=%7")
				}
				if strings.HasSuffix(mode, "-dry") {
					args = append(args, "--dry-run")
				}
				if strings.HasPrefix(mode, "complete-") || mode == "partial-dry" {
					environment := map[string]string{"AGENT_MAIL_AGENT": "BlueLake", "BEADS_DIR": filepath.Join(root, ".beads"), "BV_ROBOT_NOT_READY_LABELS": "not-ready", "PATH": "/usr/bin:/bin", "ZAI_SEAT_ROLE": secret}
					if mode == "partial-dry" {
						delete(environment, "PATH")
					}
					data, err := json.Marshal(environment)
					if err != nil {
						t.Fatal(err)
					}
					inputPath := filepath.Join(root, "recovery.json")
					if err := os.WriteFile(inputPath, data, 0600); err != nil {
						t.Fatal(err)
					}
					args = append(args, "--restart-env-file="+inputPath)
				}
				rawArgs, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
				cmd.Dir = root
				cmd.Env = envWithOverrides(os.Environ(),
					"HOME="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"),
					"NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+binary, "NTM_RESTART_CLI_ROOT="+root,
					"NTM_NO_COLOR=1", "NTM_CONFIG=", "NTM_ROBOT_FORMAT=", "NTM_OUTPUT_FORMAT=", "TOON_DEFAULT_FORMAT=", "NTM_ROBOT_VERBOSITY=",
					"NTM_ROBOT_CONTRACT_ARGS="+string(rawArgs),
					// Ambient values must never satisfy a missing recovery input.
					"AGENT_MAIL_AGENT=ambient", "BEADS_DIR="+root, "BV_ROBOT_NOT_READY_LABELS=ambient", "ZAI_SEAT_ROLE="+secret)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				runErr := cmd.Run()
				wantSuccess := mode == "ordinary-dry" || mode == "complete-dry"
				if (runErr == nil) != wantSuccess {
					t.Fatalf("success=%t want=%t error=%v stdout=%s stderr=%s", runErr == nil, wantSuccess, runErr, stdout.String(), stderr.String())
				}
				calls, err := os.ReadFile(filepath.Join(root, "calls"))
				if err != nil {
					t.Fatal(err)
				}
				mutated := strings.Contains(string(calls), "respawn-pane")
				if mutated != (mode == "complete-real") {
					t.Fatalf("mutation=%t mode=%s calls=%s", mutated, mode, calls)
				}
				combined := stdout.String() + stderr.String() + string(calls)
				if strings.Contains(combined, secret) {
					t.Fatal("recovery value leaked to output or tmux argv")
				}
				if mode == "complete-real" {
					if _, err := os.Stat(filepath.Join(root, "mutation-boundary")); err != nil {
						t.Fatal("valid recovery request did not reach intended mutation boundary")
					}
				}
				if surface == "robot" {
					var output robot.RestartPaneOutput
					if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
						t.Fatalf("robot stdout is not JSON: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
					}
					if output.Success != wantSuccess {
						t.Fatalf("JSON success=%t want=%t", output.Success, wantSuccess)
					}
					if wantSuccess && !reflect.DeepEqual(output.WouldAffect, []string{"2.0"}) {
						t.Fatalf("wrong preview targets: %v", output.WouldAffect)
					}
				} else if wantSuccess && !strings.Contains(stdout.String(), "Would restart 1 pane(s)") {
					t.Fatalf("human preview omitted successful preflight: %s", stdout.String())
				}
			})
		}
	}
}

// The operator confirms a preflighted recovery, not whichever process a logical
// selector happens to name later. Change the live fixture while the real CLI is
// blocked at its confirmation prompt, then prove no stale recovery can mutate
// a pane or deliver the original pane's environment to a replacement.
func TestRespawnRevalidatesConfirmedRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording tmux executable requires a POSIX shell")
	}
	const secret = "ntm-confirmed-recovery-secret-marker"
	for _, withEnvironment := range []bool{false, true} {
		for _, change := range []string{"unchanged", "pane-replaced", "pane-moved", "process-changed", "launch-changed", "directory-changed"} {
			t.Run(fmt.Sprintf("environment=%t/%s", withEnvironment, change), func(t *testing.T) {
				root := t.TempDir()
				binary := filepath.Join(root, "tmux")
				const script = `#!/bin/sh
set -eu
root="$NTM_RESPAWN_CONFIRM_ROOT"
printf '%s\n' "$*" >> "$root/calls"
change=unchanged
if [ -f "$root/confirmed-change" ]; then
  change="$NTM_RESPAWN_CONFIRM_CHANGE"
fi
pane='%7'
pid=222
window=2
case "$change" in
  pane-replaced) pane='%8' ;;
  pane-moved) window=3 ;;
  process-changed) pid=333 ;;
esac
case "$1" in
  -V) printf 'tmux 3.4\n' ;;
  has-session) ;;
  list-sessions) printf 'recovery_NTM_SEP_2_NTM_SEP_0_NTM_SEP_today\n' ;;
  list-panes)
    printf '%%0_NTM_SEP_0_NTM_SEP_recovery__user_0_NTM_SEP_zsh_NTM_SEP_100_NTM_SEP_30_NTM_SEP_0_NTM_SEP_111_NTM_SEP_0_NTM_SEP_user\n'
    printf '%s_NTM_SEP_0_NTM_SEP_notes_NTM_SEP_zsh_NTM_SEP_100_NTM_SEP_30_NTM_SEP_1_NTM_SEP_%s_NTM_SEP_%s_NTM_SEP_codex\n' "$pane" "$pid" "$window"
    if [ "$change" = pane-moved ]; then
      printf '%%8_NTM_SEP_0_NTM_SEP_replacement_NTM_SEP_zsh_NTM_SEP_100_NTM_SEP_30_NTM_SEP_0_NTM_SEP_444_NTM_SEP_2_NTM_SEP_codex\n'
    fi
    ;;
  display-message)
    case "$*" in
      *@ntm_agent_type*) printf '[cod]\n' ;;
      *pane_pid*)
        case "$change:$*" in
          pane-moved:*%8*) printf '444\n' ;;
          *) printf '%s\n' "$pid" ;;
        esac
        ;;
      *pane_current_command*) printf 'zsh\n' ;;
      *)
        if [ "$change" = directory-changed ]; then
          printf '%s\n' "$root/moved"
        else
          printf '%s\n' "$root"
        fi
        ;;
    esac
    ;;
  show-options)
    case "$*" in
      *@ntm_agent_launch*)
        case "$*" in
          *%8*) cat "$root/spec-replacement" ;;
          *)
            if [ "$change" = launch-changed ]; then
              cat "$root/spec-changed"
            else
              cat "$root/spec"
            fi
            ;;
        esac
        ;;
    esac
    ;;
  respawn-pane) : > "$root/mutation-boundary"; exit 71 ;;
  if-shell)
    case "${6:-}" in
      *respawn-pane*) : > "$root/mutation-boundary"; exit 71 ;;
      *) exit 72 ;;
    esac
    ;;
esac
`
				if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, "moved"), 0700); err != nil {
					t.Fatal(err)
				}
				spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex, Command: "codex --model saved/model", Model: "saved/model"}
				if withEnvironment {
					spec.OmittedEnv = []string{"ZAI_SEAT_ROLE"}
				}
				for _, name := range []string{"spec", "spec-changed", "spec-replacement"} {
					launch := spec
					paneID := "%7"
					if name == "spec-changed" {
						launch.Command = "codex --model replacement/model"
						launch.Model = "replacement/model"
					}
					if name == "spec-replacement" {
						paneID = "%8"
					}
					record, err := json.Marshal(map[string]any{"pane_id": paneID, "spec": launch})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, name), []byte(base64.StdEncoding.EncodeToString(record)), 0600); err != nil {
						t.Fatal(err)
					}
				}
				configPath := filepath.Join(root, "config.toml")
				if err := os.WriteFile(configPath, []byte("[agent_mail]\nenabled = false\n[cleanup]\nauto_clean_on_startup = false\n"), 0600); err != nil {
					t.Fatal(err)
				}
				args := []string{"--config=" + configPath, "respawn", "recovery", "--panes=2.0"}
				if withEnvironment {
					data, err := json.Marshal(map[string]string{"ZAI_SEAT_ROLE": secret})
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(root, "recovery.json")
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
					args = append(args, "--restart-env-file="+path)
				}
				rawArgs, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
				cmd.Dir = root
				cmd.Env = envWithOverrides(os.Environ(),
					"HOME="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"),
					"NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+binary, "NTM_RESPAWN_CONFIRM_ROOT="+root,
					"NTM_RESPAWN_CONFIRM_CHANGE="+change, "NTM_ROBOT_CONTRACT_ARGS="+string(rawArgs),
					"NTM_NO_COLOR=1", "NTM_CONFIG=", "NTM_ROBOT_FORMAT=", "NTM_OUTPUT_FORMAT=", "TOON_DEFAULT_FORMAT=", "NTM_ROBOT_VERBOSITY=")
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer stdin.Close()
				stdoutPipe, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var stdout, stderr bytes.Buffer
				cmd.Stderr = &stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					cancel()
					_ = cmd.Wait()
				}()
				reader := bufio.NewReader(stdoutPipe)
				for !strings.Contains(stdout.String(), "[y/N]:") {
					part, readErr := reader.ReadString(':')
					stdout.WriteString(part)
					if readErr != nil {
						waitErr := cmd.Wait()
						t.Fatalf("respawn did not reach confirmation: read=%v process=%v stdout=%s stderr=%s", readErr, waitErr, stdout.String(), stderr.String())
					}
				}
				// The first shared preflight is complete and the CLI is blocked
				// reading confirmation. Change only the live target evidence.
				if err := os.WriteFile(filepath.Join(root, "confirmed-change"), []byte(change), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(stdin, "y\n"); err != nil {
					t.Fatal(err)
				}
				if err := stdin.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := io.Copy(&stdout, reader); err != nil {
					t.Fatal(err)
				}
				runErr := cmd.Wait()
				if runErr == nil {
					t.Fatalf("expected stale-preflight refusal or fixture mutation error; stdout=%s stderr=%s", stdout.String(), stderr.String())
				}
				if ctx.Err() != nil {
					t.Fatalf("confirmed recovery timed out: stdout=%s stderr=%s", stdout.String(), stderr.String())
				}
				calls, err := os.ReadFile(filepath.Join(root, "calls"))
				if err != nil {
					t.Fatal(err)
				}
				mutated := strings.Contains(string(calls), "respawn-pane")
				if mutated != (change == "unchanged") {
					t.Fatalf("mutation=%t for %s: calls=%s stdout=%s stderr=%s", mutated, change, calls, stdout.String(), stderr.String())
				}
				for _, operation := range []string{"send-keys", "load-buffer", "paste-buffer", "set-option"} {
					if strings.Contains(string(calls), operation) {
						t.Fatalf("unconfirmed recovery reached %q: %s", operation, calls)
					}
				}
				combined := stdout.String() + stderr.String() + string(calls)
				if strings.Contains(combined, secret) {
					t.Fatal("recovery value leaked across confirmation or into diagnostics")
				}
				if change == "unchanged" {
					if _, err := os.Stat(filepath.Join(root, "mutation-boundary")); err != nil {
						t.Fatal("stable preflight did not reach the intended mutation boundary")
					}
				}
			})
		}
	}
}

func TestRespawnResultErrorRejectsTerminalRobotResponse(t *testing.T) {
	out := &robot.RestartPaneOutput{
		RobotResponse: robot.NewErrorResponse(
			agent.ErrAutomatedRelaunchNotImplemented,
			robot.ErrCodeNotImplemented,
			agent.GrokPhaseOneCapabilityHint,
		),
	}
	err := respawnResultError(out)
	if err == nil || !strings.Contains(err.Error(), robot.ErrCodeNotImplemented) {
		t.Fatalf("respawnResultError() error = %v, want typed terminal failure", err)
	}
	if err := respawnResultError(&robot.RestartPaneOutput{RobotResponse: robot.NewRobotResponse(true)}); err != nil {
		t.Fatalf("successful response rejected: %v", err)
	}
}

func TestRespawnRelaunchDisplayStatusUsesTriStateBeforeLegacyBoolean(t *testing.T) {
	tests := []struct {
		name      string
		status    robot.RestartAgentRelaunchStatus
		alive     bool
		legacy    bool
		want      string
		wantExact bool
	}{
		{name: "ready", status: robot.RestartAgentRelaunchReady, want: "agent relaunched and ready", wantExact: true},
		{name: "not ready with child", status: robot.RestartAgentRelaunchNotReady, alive: true, want: "alive but not ready"},
		{name: "unknown with child", status: robot.RestartAgentRelaunchUnknown, alive: true, want: "UNKNOWN"},
		{name: "failed", status: robot.RestartAgentRelaunchFailed, want: "agent relaunch FAILED", wantExact: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := &robot.RestartPaneOutput{
				AgentRelaunched:     map[string]bool{"1": tt.legacy},
				AgentRelaunchStatus: map[string]robot.RestartAgentRelaunchStatus{"1": tt.status},
				ProcessAlive:        map[string]bool{"1": tt.alive},
			}
			got, ok := respawnRelaunchDisplayStatus(out, "1")
			if !ok || (tt.wantExact && got != tt.want) || (!tt.wantExact && !strings.Contains(got, tt.want)) {
				t.Fatalf("display status=%q ok=%t, want %q", got, ok, tt.want)
			}
			if tt.status == robot.RestartAgentRelaunchUnknown && strings.Contains(got, "pane left at a shell") {
				t.Fatalf("unknown live-child status used legacy false rendering: %q", got)
			}
		})
	}

	legacy, ok := respawnRelaunchDisplayStatus(&robot.RestartPaneOutput{
		AgentRelaunched: map[string]bool{"1": false},
	}, "1")
	if !ok || !strings.Contains(legacy, "pane left at a shell") {
		t.Fatalf("legacy fallback=%q ok=%t", legacy, ok)
	}
}

func TestRespawnRequiresSession(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Test respawning a non-existent session should fail
	err := runRespawn(t.Context(), "nonexistent-session-12345", true, "", "", false, false, "")
	if err == nil {
		t.Error("expected error for non-existent session")
	}
}

func TestRespawnDryRun(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "ntm-test-respawn")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = "printf ready; sleep 300"

	// Create unique session
	sessionName := fmt.Sprintf("ntm-test-respawn-%d", time.Now().UnixNano())

	// Pre-create project directory
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn a test session
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1},
	}
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		UserPane: true,
	}

	err = spawnSessionLogicContext(t.Context(), opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Wait for session to be ready
	time.Sleep(500 * time.Millisecond)

	// Test dry-run mode (should not error and not actually restart)
	err = runRespawn(t.Context(), sessionName, true, "", "", false, true, "")
	if err != nil {
		t.Errorf("dry-run respawn failed: %v", err)
	}
}

func TestRespawnWithPaneFilter(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "ntm-test-respawn-filter")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = "printf ready; sleep 300"

	// Create unique session
	sessionName := fmt.Sprintf("ntm-test-respawn-filter-%d", time.Now().UnixNano())

	// Pre-create project directory
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn a test session with 2 agents
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1},
		{Type: AgentTypeClaude, Index: 2},
	}
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  2,
		UserPane: true,
	}

	err = spawnSessionLogicContext(t.Context(), opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Wait for session to be ready
	time.Sleep(500 * time.Millisecond)

	// Test respawning specific pane with force flag
	err = runRespawn(t.Context(), sessionName, true, "1", "", false, false, "")
	if err != nil {
		t.Errorf("respawn with pane filter failed: %v", err)
	}
}

// A malformed --panes selector must fail before the confirmation prompt (and
// dry-run listing) instead of silently narrowing the restart to the
// well-formed part of the list.
func TestRespawnRejectsMalformedPaneSelector(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	sessionName := fmt.Sprintf("ntm-respawn-selector-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(sessionName, t.TempDir()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer func() { _ = tmux.KillSession(sessionName) }()

	for _, panes := range []string{"0,0.x", "all", "0,"} {
		err := runRespawn(t.Context(), sessionName, true, panes, "", false, true, "")
		if err == nil || !strings.Contains(err.Error(), "invalid pane selector") {
			t.Fatalf("runRespawn(--panes=%q) error = %v, want an invalid pane selector error", panes, err)
		}
	}
}
