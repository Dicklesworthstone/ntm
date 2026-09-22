package tmux

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

func TestAgentLaunchSpecValidation(t *testing.T) {
	valid := AgentLaunchSpec{
		Version: AgentLaunchSpecVersion, AgentType: AgentClaude,
		Command: `claude --model 'custom/model' --effort high --append-system-prompt-file '/work/persona.md'`,
		Model:   "custom/model", ModelAlias: "architect", Persona: "architect", ReasoningEffort: "high",
	}
	if err := valid.ValidateReplay(AgentType("claude")); err != nil {
		t.Fatalf("valid launch: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*AgentLaunchSpec)
	}{
		{"unknown version", func(s *AgentLaunchSpec) { s.Version++ }},
		{"empty command", func(s *AgentLaunchSpec) { s.Command = " " }},
		{"newline command", func(s *AgentLaunchSpec) { s.Command += "\nwhoami" }},
		{"oversize command", func(s *AgentLaunchSpec) { s.Command = strings.Repeat("x", maxAgentLaunchSpecBytes) }},
		{"unknown type", func(s *AgentLaunchSpec) { s.AgentType = "unknown-agent" }},
		{"user type", func(s *AgentLaunchSpec) { s.AgentType = AgentUser }},
		{"invalid env key", func(s *AgentLaunchSpec) { s.OmittedEnv = []string{"BAD-NAME"} }},
		{"control in profile", func(s *AgentLaunchSpec) { s.CAAMProfile = "name\nother" }},
		{"unbound token file", func(s *AgentLaunchSpec) { s.ClaudeTokenFile = "/token" }},
		{"relative token file", func(s *AgentLaunchSpec) { s.ClaudeIsolateCredentials = true; s.ClaudeTokenFile = "token" }},
		{"relative project", func(s *AgentLaunchSpec) { s.AgentMailProject = "project" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := valid
			tc.change(&spec)
			if err := spec.Validate(AgentClaude); err == nil {
				t.Fatal("invalid specification accepted")
			}
		})
	}
	if err := valid.Validate(AgentCodex); err == nil {
		t.Fatal("provider mismatch accepted")
	}
	incomplete := valid
	incomplete.OmittedEnv = []string{"API_TOKEN", "CUSTOM_PROFILE"}
	if err := incomplete.Validate(AgentClaude); err != nil {
		t.Fatalf("incomplete specification must remain capturable: %v", err)
	}
	if err := incomplete.ValidateReplay(AgentClaude); err == nil || !strings.Contains(err.Error(), "API_TOKEN") {
		t.Fatalf("incomplete replay error=%v, want required environment names", err)
	}
	var missing *AgentLaunchSpec
	if err := missing.ValidateReplay(AgentClaude); err == nil {
		t.Fatal("nil specification accepted")
	}
}

func TestAgentLaunchSpecAcceptsRegisteredPlugin(t *testing.T) {
	const plugin = "launch-spec-plugin"
	if err := agent.RegisterPlugin(plugin, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	spec := AgentLaunchSpec{Version: AgentLaunchSpecVersion, AgentType: plugin, Command: "custom-agent --profile retained"}
	if err := spec.ValidateReplay(AgentType(plugin)); err != nil {
		t.Fatalf("registered plugin launch refused: %v", err)
	}
	if err := spec.Validate(AgentClaude); err == nil {
		t.Fatal("plugin metadata accepted as a different provider")
	}
}

func TestPaneLaunchSpecTransport(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tmux")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$1" >> "$NTM_LAUNCH_SPEC_TEST_ROOT/calls"
case "$1" in
  set-option)
    [ "$2" = '-p' ] && [ "$3" = '-t' ] && [ "$4" = '%7' ] && [ "$5" = '@ntm_agent_launch' ] || exit 61
    if [ -f "$NTM_LAUNCH_SPEC_TEST_ROOT/fail" ]; then
      printf '%s' "$6" >&2
      exit 62
    fi
    printf '%s' "$6" > "$NTM_LAUNCH_SPEC_TEST_ROOT/value"
    ;;
  show-options)
    [ "$2" = '-p' ] && [ "$3" = '-v' ] && [ "$4" = '-t' ] && [ "$5" = '%7' ] && [ "$6" = '@ntm_agent_launch' ] || exit 63
    if [ -f "$NTM_LAUNCH_SPEC_TEST_ROOT/fail" ]; then exit 64; fi
    if [ -f "$NTM_LAUNCH_SPEC_TEST_ROOT/value" ]; then
      cat "$NTM_LAUNCH_SPEC_TEST_ROOT/value"
    else
      printf 'invalid option: @ntm_agent_launch\n' >&2
      exit 1
    fi
    ;;
  *) exit 65 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPaneLaunchSpecTransportHelper$")
	cmd.Env = append(os.Environ(), "NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+path, "NTM_LAUNCH_SPEC_TEST_ROOT="+root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launch metadata transport: %v\n%s", err, output)
	}
}

func TestPaneLaunchSpecTransportHelper(t *testing.T) {
	root := os.Getenv("NTM_LAUNCH_SPEC_TEST_ROOT")
	if root == "" {
		return
	}
	client := NewClient("")
	spec := AgentLaunchSpec{
		Version: AgentLaunchSpecVersion, AgentType: AgentCodex,
		Command: `CODEX_SYSTEM_PROMPT="$(cat '/work/a prompt.md')" codex --model 'custom/model' -c 'model_reasoning_effort="high"' --custom 'literal;$(payload)'`,
		Model:   "custom/model", ModelAlias: "architect", Persona: "architect", ReasoningEffort: "high", CAAMProfile: "team-profile",
	}
	got, err := client.ReadPaneLaunchSpecContext(t.Context(), "%7")
	if err != nil || got != nil {
		t.Fatalf("absent record=(%+v,%v)", got, err)
	}
	if err := client.SetPaneLaunchSpecContext(t.Context(), "%7", spec); err != nil {
		t.Fatal(err)
	}
	got, err = client.ReadPaneLaunchSpecContext(t.Context(), "%7")
	if err != nil || !reflect.DeepEqual(got, &spec) {
		t.Fatalf("round trip=(%+v,%v), want %+v", got, err, spec)
	}
	encoded, err := os.ReadFile(filepath.Join(root, "value"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(string(encoded), "\n\r;\t") || strings.Contains(string(encoded), spec.Command) {
		t.Fatal("record contains unencoded command or line separators")
	}
	write := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "value"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	wrongPane, _ := json.Marshal(paneLaunchRecord{PaneID: "%9", Spec: spec})
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"wrong pane", []byte(base64.StdEncoding.EncodeToString(wrongPane))},
		{"invalid base64", []byte("not-base64")},
		{"empty stored value", nil},
		{"invalid json", []byte(base64.StdEncoding.EncodeToString([]byte("{}secret")))},
		{"oversized", []byte(strings.Repeat("A", 100000))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(tc.data)
			if _, err := client.ReadPaneLaunchSpecContext(t.Context(), "%7"); err == nil {
				t.Fatal("invalid launch record accepted")
			}
		})
	}
	write(encoded)
	for _, target := range []string{"", "sess:0.1", "%7;kill-pane", "%7\n"} {
		if err := client.SetPaneLaunchSpecContext(t.Context(), target, spec); err == nil {
			t.Fatalf("nonphysical target %q accepted", target)
		}
		if _, err := client.ReadPaneLaunchSpecContext(t.Context(), target); err == nil {
			t.Fatalf("nonphysical read target %q accepted", target)
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := client.SetPaneLaunchSpecContext(canceled, "%7", spec); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
	if _, err := client.ReadPaneLaunchSpecContext(canceled, "%7"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "fail"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadPaneLaunchSpecContext(t.Context(), "%7"); err == nil {
		t.Fatal("read transport failure treated as absent metadata")
	}
	if err := client.SetPaneLaunchSpecContext(t.Context(), "%7", spec); err == nil || strings.Contains(err.Error(), "literal") || strings.Contains(err.Error(), string(encoded)) {
		t.Fatalf("write error must be present without exposing command: %v", err)
	}
}
