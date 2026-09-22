package robot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestLifecycleRelaunchPreservesSavedLaunch(t *testing.T) {
	for _, verb := range []string{"exit", "kill"} {
		for _, mode := range []string{"saved", "legacy", "corrupt", "batch-corrupt", "omitted-env", "changed-prompt", "metadata-failure", "shell-replaced", "shell-busy", "lookup-failure", "provider-changed", "service", "dead", "post-provider-changed", "post-service", "post-dead", "delivery-failure"} {
			t.Run(verb+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "tmux")
				script := `#!/bin/sh
set -eu
root="$NTM_LIFECYCLE_SPEC_TEST_ROOT"
mode="$NTM_LIFECYCLE_SPEC_TEST_MODE"
printf '%s\n' "$1" >> "$root/calls"
case "$1" in
  has-session) ;;
  list-panes)
    command=claude
    pid=2000000001
    agent=cc
    service=''
    dead=0
    if [ "$NTM_LIFECYCLE_SPEC_TEST_VERB" = kill ] || [ -f "$root/exited" ]; then command=bash; fi
    if [ -f "$root/preflighted" ]; then
      if [ "$mode" = shell-replaced ]; then pid=2000000002; fi
      if [ "$mode" = provider-changed ]; then agent=cod; fi
      if [ "$mode" = service ]; then agent=''; service=cass; fi
      if [ "$mode" = dead ]; then dead=1; fi
      if [ -f "$root/shell-observed" ]; then
        if [ "$mode" = shell-busy ]; then command=claude; fi
        if [ "$mode" = lookup-failure ]; then printf 'temporary transport error\n' >&2; exit 51; fi
      fi
      if [ "$command" = bash ]; then : > "$root/shell-observed"; fi
    fi
    if [ -f "$root/launched" ]; then
      command=claude
      if [ "$mode" = post-provider-changed ]; then agent=cod; fi
      if [ "$mode" = post-service ]; then agent=''; service=cass; fi
      if [ "$mode" = post-dead ]; then dead=1; fi
    fi
    printf '%%7_NTM_SEP_1_NTM_SEP_session__cc_1_NTM_SEP_%s_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_%s_NTM_SEP__NTM_SEP_%s_NTM_SEP_%s\n' "$command" "$pid" "$agent" "$service" "$dead"
    if [ "$mode" = batch-corrupt ]; then
      printf '%%8_NTM_SEP_2_NTM_SEP_session__cc_2_NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_0_NTM_SEP_2000000002_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n'
    fi
    ;;
  show-options)
    if [ "$mode" = batch-corrupt ] && [ "$5" = '%8' ]; then printf 'corrupt-metadata'; exit 0; fi
    [ "$5" = '%7' ] && [ "$6" = '@ntm_agent_launch' ] || exit 52
    : > "$root/preflighted"
    if [ "$mode" = legacy ]; then printf 'invalid option: @ntm_agent_launch\n' >&2; exit 1; fi
    cat "$root/original"
    ;;
  display-message)
    [ "$4" = '%7' ] && [ "$5" = '#{pane_current_path}' ] || exit 53
    printf '%s\n' "$root"
    ;;
  set-option)
    [ "$4" = '%7' ] && [ "$5" = '@ntm_agent_launch' ] || exit 54
    [ -f "$root/shell-observed" ] || exit 55
    if [ "$mode" = metadata-failure ]; then exit 56; fi
    printf '%s' "$6" > "$root/recorded"
    ;;
  send-keys)
    [ "$3" = '%7' ] || exit 57
    case "${4:-}" in
      C-c)
        [ -f "$root/preflighted" ] || exit 58
        : > "$root/exited"
        ;;
      -l)
        [ -f "$root/recorded" ] || exit 59
        printf '%s' "$6" > "$root/delivered"
        if [ "$mode" = delivery-failure ]; then printf '%s' "$6" >&2; exit 60; fi
        ;;
      Enter)
        [ -f "$root/delivered" ] || exit 61
        : > "$root/launched"
        ;;
      *) exit 62 ;;
    esac
    ;;
  *) exit 63 ;;
esac
`
				if err := os.WriteFile(path, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLifecycleLaunchSpecTransportHelper$")
				cmd.Env = append(os.Environ(), "NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+path,
					"NTM_LIFECYCLE_SPEC_TEST_ROOT="+root, "NTM_LIFECYCLE_SPEC_TEST_MODE="+mode,
					"NTM_LIFECYCLE_SPEC_TEST_VERB="+verb, "HOME="+root, "NTM_CONFIG="+filepath.Join(root, "config.toml"),
					"XDG_DATA_HOME="+filepath.Join(root, "data"))
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("lifecycle metadata transport: %v\n%s", err, output)
				}
			})
		}
	}
}

func TestLifecycleLaunchSpecTransportHelper(t *testing.T) {
	root := os.Getenv("NTM_LIFECYCLE_SPEC_TEST_ROOT")
	if root == "" {
		return
	}
	mode, verb := os.Getenv("NTM_LIFECYCLE_SPEC_TEST_MODE"), os.Getenv("NTM_LIFECYCLE_SPEC_TEST_VERB")
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("[agents]\nclaude = \"claude --model CURRENT_WRONG\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(root, "persona.md")
	prompt := []byte("Preserve this prepared persona and its expanded project context.")
	if err := os.WriteFile(promptPath, prompt, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(prompt)
	saved := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude,
		Command: "claude --model 'ORIGINAL_PIN' --effort high --custom 'private fixture argument' --append-system-prompt-file " + tmux.ShellQuote(promptPath),
		Model:   "ORIGINAL_PIN", Persona: "architect", ReasoningEffort: "high", SystemPromptFile: promptPath, SystemPromptSHA256: hex.EncodeToString(digest[:]),
	}
	if mode == "omitted-env" {
		saved.OmittedEnv = []string{"PRIVATE_TOKEN"}
	}
	if mode == "changed-prompt" {
		if err := os.WriteFile(promptPath, []byte("Changed after the original spawn."), 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(struct {
		PaneID string               `json:"pane_id"`
		Spec   tmux.AgentLaunchSpec `json:"spec"`
	}{PaneID: "%7", Spec: saved})
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	if mode == "corrupt" {
		encoded = "corrupt-metadata"
	}
	if err := os.WriteFile(filepath.Join(root, "original"), []byte(encoded), 0600); err != nil {
		t.Fatal(err)
	}
	opts := LifecycleOptions{Session: "session", Panes: []string{"%7"}, Relaunch: true}
	if mode == "batch-corrupt" {
		opts.Panes = []string{"%7", "%8"}
	}
	var success bool
	var results []LifecyclePaneResult
	if verb == "exit" {
		out, err := GetExitCLI(t.Context(), opts)
		if err != nil {
			t.Fatal(err)
		}
		success, results = out.Success, out.Results
	} else {
		out, err := GetKillAgent(t.Context(), opts)
		if err != nil {
			t.Fatal(err)
		}
		success, results = out.Success, out.Results
	}
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	calls, recorded, delivered := string(read("calls")), read("recorded"), read("delivered")
	if mode == "corrupt" || mode == "batch-corrupt" || mode == "omitted-env" || mode == "changed-prompt" {
		if success || len(results) != 0 || strings.Contains(calls, "send-keys") || len(recorded) != 0 {
			t.Fatalf("failed preflight crossed quit/kill boundary: success=%t results=%+v calls=%s", success, results, calls)
		}
		return
	}
	if mode == "metadata-failure" || mode == "shell-replaced" || mode == "shell-busy" || mode == "lookup-failure" || mode == "provider-changed" || mode == "service" || mode == "dead" {
		if success || len(recorded) != 0 || len(delivered) != 0 {
			t.Fatalf("unverified shell or failed metadata permitted relaunch: success=%t results=%+v calls=%s", success, results, calls)
		}
		return
	}
	if len(recorded) == 0 || len(delivered) == 0 {
		t.Fatalf("metadata was not recorded before launch: results=%+v calls=%s", results, calls)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(recorded))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		PaneID string               `json:"pane_id"`
		Spec   tmux.AgentLaunchSpec `json:"spec"`
	}
	if err := json.Unmarshal(decoded, &record); err != nil {
		t.Fatal(err)
	}
	if record.PaneID != "%7" {
		t.Fatalf("metadata recorded for another pane: %q", record.PaneID)
	}
	if mode == "legacy" {
		if record.Spec.Command != "claude --model CURRENT_WRONG" || string(delivered) != record.Spec.Command {
			t.Fatalf("legacy relaunch did not record the command actually sent: %+v", record.Spec)
		}
	} else if record.Spec.Command != saved.Command || record.Spec.SystemPromptSHA256 != saved.SystemPromptSHA256 || record.Spec.Persona != saved.Persona || !strings.Contains(string(delivered), saved.Command) || strings.Contains(string(delivered), "CURRENT_WRONG") {
		t.Fatal("relaunch lost the saved command, prepared persona, or model pin")
	}
	if mode == "delivery-failure" || strings.HasPrefix(mode, "post-") {
		if success || len(results) != 1 || results[0].Relaunched || strings.Contains(results[0].Detail, "private fixture argument") {
			t.Fatalf("failed delivery lost failure evidence or exposed command arguments: success=%t results=%+v", success, results)
		}
		return
	}
	if !success || len(results) != 1 || !results[0].Relaunched || !results[0].ShellPreserved {
		t.Fatalf("saved launch did not complete: success=%t results=%+v calls=%s", success, results, calls)
	}
}
