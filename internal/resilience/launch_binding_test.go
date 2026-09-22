package resilience

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestCaptureLaunchBindingPersistsOnlyOpaqueProfile(t *testing.T) {
	t.Setenv("SHALLOW_PROFILE", "profile-a")
	t.Setenv("HOME", "/tmp/secret-home")
	t.Setenv("ANTHROPIC_API_KEY", "secret-token")

	binding := CaptureLaunchBinding("claude")
	if binding == nil {
		t.Fatal("CaptureLaunchBinding returned nil")
	}
	data, err := json.Marshal(AgentConfig{
		PaneID:        "%1",
		PaneIndex:     1,
		Type:          "cc",
		Command:       "claude",
		LaunchBinding: binding,
	})
	if err != nil {
		t.Fatalf("marshal agent config: %v", err)
	}
	text := string(data)
	for _, want := range []string{"profile-a", `"provider":"cc"`, `"launcher":"caam"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest row %s does not contain %q", text, want)
		}
	}
	for _, forbidden := range []string{"HOME", "secret-home", "ANTHROPIC_API_KEY", "secret-token", `"environment"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("manifest row persisted forbidden environment data %q: %s", forbidden, text)
		}
	}
}

func TestPrepareLaunchCommandPreservesCreationProfile(t *testing.T) {
	t.Setenv("SHALLOW_PROFILE", "profile-b")
	binding := &LaunchBinding{Provider: "cc", Launcher: "caam", Identifier: "profile-a"}
	var gotBinary, gotIdentifier string

	command, affinity, err := prepareLaunchCommand(
		context.Background(),
		"claude",
		"/opt/caam",
		binding,
		"claude --model opus",
		func(_ context.Context, binary string, got *LaunchBinding) error {
			gotBinary = binary
			gotIdentifier = got.Identifier
			return nil
		},
	)
	if err != nil {
		t.Fatalf("prepareLaunchCommand: %v", err)
	}
	if affinity != LaunchAffinityPreserved {
		t.Fatalf("affinity = %q, want %q", affinity, LaunchAffinityPreserved)
	}
	if gotBinary != "/opt/caam" || gotIdentifier != "profile-a" {
		t.Fatalf("preflight used binary=%q profile=%q, want /opt/caam profile-a", gotBinary, gotIdentifier)
	}
	if !strings.Contains(command, "shallow-spawn") || !strings.Contains(command, "profile-a") ||
		strings.Contains(command, "profile-b") || !strings.HasSuffix(command, " -- sh -c 'claude --model opus'") {
		t.Fatalf("prepared command did not preserve profile-a: %q", command)
	}
}

// caam exec()s the argv after "--" without a shell, but agent commands are
// shell strings (env assignments, systemd-run prefixes, `a && b`). The whole
// string must reach one shell running under the profile; a bare `a && b`
// would run b outside the profile while the restart reported it preserved.
func TestPrepareLaunchCommandRunsWholeShellCommandInsideProfile(t *testing.T) {
	binding := &LaunchBinding{Provider: "cod", Launcher: "caam", Identifier: "profile-a"}
	const original = `CODEX_SYSTEM_PROMPT="$(cat '/tmp/p.md')" codex --search && echo it's done`
	command, affinity, err := prepareLaunchCommand(
		context.Background(),
		"codex",
		"",
		binding,
		original,
		func(context.Context, string, *LaunchBinding) error { return nil },
	)
	if err != nil {
		t.Fatalf("prepareLaunchCommand: %v", err)
	}
	if affinity != LaunchAffinityPreserved {
		t.Fatalf("affinity = %q, want %q", affinity, LaunchAffinityPreserved)
	}
	want := "'caam' shallow-spawn 'profile-a' -- sh -c " + tmux.ShellQuote(original)
	if command != want {
		t.Fatalf("prepared command = %q, want %q", command, want)
	}
	// The command must be the single argument of sh -c, so nothing after the
	// binding's "--" can be parsed by the pane shell as a separate command.
	if idx := strings.Index(command, " -- sh -c "); idx < 0 || command[idx+len(" -- sh -c "):] != tmux.ShellQuote(original) {
		t.Fatalf("shell command is not a single quoted sh -c argument: %q", command)
	}
}

func TestPrepareLaunchCommandLegacyAffinityIsExplicit(t *testing.T) {
	const original = "codex --model gpt-5"
	command, affinity, err := prepareLaunchCommand(
		context.Background(),
		"cod",
		"",
		nil,
		original,
		func(context.Context, string, *LaunchBinding) error {
			t.Fatal("legacy unknown affinity must not invoke a launcher")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("prepareLaunchCommand: %v", err)
	}
	if command != original || affinity != LaunchAffinityUnknown {
		t.Fatalf("command=%q affinity=%q, want original/%q", command, affinity, LaunchAffinityUnknown)
	}
}

func TestPrepareLaunchCommandResolutionFailureNamesBinding(t *testing.T) {
	binding := &LaunchBinding{Provider: "cc", Launcher: "caam", Identifier: "missing-profile"}
	_, _, err := prepareLaunchCommand(
		context.Background(),
		"cc",
		"",
		binding,
		"claude",
		func(context.Context, string, *LaunchBinding) error {
			return errors.New("profile does not exist")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "missing-profile") || !strings.Contains(err.Error(), "profile does not exist") {
		t.Fatalf("error = %v, want named resolver failure", err)
	}
}

func TestPrepareLaunchCommandRejectsProviderMismatchBeforePreflight(t *testing.T) {
	calls := 0
	binding := &LaunchBinding{Provider: "cod", Launcher: "caam", Identifier: "profile-a"}
	_, _, err := prepareLaunchCommand(
		context.Background(),
		"cc",
		"",
		binding,
		"claude",
		func(context.Context, string, *LaunchBinding) error {
			calls++
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "profile-a") || calls != 0 {
		t.Fatalf("error=%v preflight calls=%d, want named mismatch before preflight", err, calls)
	}
}

func TestUpsertAgentConfigPreservesExistingManifestWithoutEnvironment(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := SaveManifest(&SpawnManifest{
		Session:     "add-binding",
		ProjectDir:  "/tmp/project",
		AutoRestart: true,
		Agents: []AgentConfig{{
			PaneID: "%1", PaneIndex: 1, Type: "cod", Command: "codex",
		}},
	}); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	t.Setenv("HOME", "/tmp/secret-home")
	t.Setenv("ANTHROPIC_API_KEY", "secret-token")
	if err := UpsertAgentConfig("add-binding", "/tmp/project", AgentConfig{
		PaneID:    "%2",
		PaneIndex: 2,
		Type:      "cc",
		Command:   "claude",
		LaunchBinding: &LaunchBinding{
			Provider: "cc", Launcher: "caam", Identifier: "profile-a",
		},
	}); err != nil {
		t.Fatalf("UpsertAgentConfig: %v", err)
	}
	loaded, err := LoadManifest("add-binding")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if !loaded.AutoRestart || len(loaded.Agents) != 2 ||
		loaded.Agents[1].LaunchBinding == nil ||
		loaded.Agents[1].LaunchBinding.Identifier != "profile-a" {
		t.Fatalf("unexpected manifest after upsert: %+v", loaded)
	}
	raw, err := os.ReadFile(filepath.Join(ManifestDir(), "add-binding.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	for _, forbidden := range []string{"secret-home", "secret-token", `"environment"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("manifest contains forbidden data %q: %s", forbidden, raw)
		}
	}
}

func TestAgentLaunchSpecReplayPreservesConfiguredCommandAndProfile(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "caam-calls")
	t.Setenv("NTM_LAUNCH_TEST_CAAM_LOG", log)
	t.Setenv("SHALLOW_PROFILE", "current-profile-must-not-replace-saved")
	caam := filepath.Join(dir, "caam")
	if err := os.WriteFile(caam, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NTM_LAUNCH_TEST_CAAM_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Integrations.CAAM.BinaryPath = caam
	spec := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex,
		Command: `CUSTOM_FLAG='two words' /opt/wrapper --model saved-model && printf 'started'`,
		Model:   "saved-model", Persona: "reviewer", ReasoningEffort: "high", CAAMProfile: "original-profile",
		AgentMailProject: "/original project",
	}
	original := spec
	plan, err := PreflightAgentLaunchSpec(context.Background(), cfg, spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the source inputs after preflight cannot change the saved plan.
	spec.Command = "different-agent"
	cfg.Integrations.CAAM.BinaryPath = "/missing-caam"
	prepared, err := plan.Prepare(context.Background(), dir, "recovered", 2)
	if err != nil {
		t.Fatal(err)
	}
	inside := "AGENT_MAIL_PROJECT=" + tmux.ShellQuote(original.AgentMailProject) + " " + original.Command
	want := tmux.ShellQuote(caam) + " shallow-spawn 'original-profile' -- sh -c " + tmux.ShellQuote(inside)
	if prepared != want {
		t.Fatalf("saved launch settings changed:\n got %s\nwant %s", prepared, want)
	}
	calls, err := os.ReadFile(log)
	if err != nil || strings.TrimSpace(string(calls)) != "shallow-spawn original-profile --print-env" {
		t.Fatalf("expected exactly one saved-profile resolver call: %s, %v", calls, err)
	}
}

func TestAgentLaunchSpecPreflightDoesNotProvisionOrLeakCredentials(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("Claude credential isolation intentionally rejects macOS Keychain credentials")
	}
	projectDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"rotating":"must-not-copy"}`), 0600); err != nil {
		t.Fatal(err)
	}
	const token = "test-setup-token-value-must-not-appear"
	tokenFile := filepath.Join(t.TempDir(), "setup-token")
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Agents.ClaudeIsolateCredentials = false
	cfg.Agents.ClaudeTokenFile = "/wrong-current-token-file"
	originalCfg := cfg.Agents
	spec := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude, Command: "claude --model saved-model",
		ClaudeIsolateCredentials: true, ClaudeTokenFile: tokenFile,
	}
	originalSpec := spec
	plan, err := PreflightAgentLaunchSpec(context.Background(), cfg, spec, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	privateDir := swarm.NewClaudeConfigProvisioner(projectDir).ConfigPath("restored", "2")
	if _, err := os.Stat(privateDir); !os.IsNotExist(err) {
		t.Fatalf("read-only preflight provisioned credential state: %v", err)
	}
	prepared, err := plan.Prepare(context.Background(), projectDir, "restored", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prepared, "CLAUDE_CONFIG_DIR="+tmux.ShellQuote(privateDir)) ||
		!strings.Contains(prepared, tmux.ShellQuote(tokenFile)) || !strings.HasSuffix(prepared, spec.Command) {
		t.Fatalf("saved credential isolation was not reprovisioned for destination: %s", prepared)
	}
	if strings.Contains(prepared, token) || strings.Contains(prepared, "wrong-current-token-file") {
		t.Fatal("credential value or current default leaked into replay command")
	}
	if _, err := os.Lstat(filepath.Join(privateDir, ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("replacement can reach a rotating shared credential: %v", err)
	}
	if !reflect.DeepEqual(cfg.Agents, originalCfg) || !reflect.DeepEqual(spec, originalSpec) {
		t.Fatal("replay mutated caller configuration or durable specification")
	}
}

func TestAgentLaunchSpecPreflightRejectsUnavailableInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*tmux.AgentLaunchSpec)
		want   string
	}{
		{"omitted environment", func(spec *tmux.AgentLaunchSpec) { spec.OmittedEnv = []string{"AUTH_TOKEN"} }, "AUTH_TOKEN"},
		{"unavailable token file", func(spec *tmux.AgentLaunchSpec) {
			spec.ClaudeIsolateCredentials = true
			spec.ClaudeTokenFile = filepath.Join(t.TempDir(), "unavailable-token")
		}, "saved Claude token file"},
		{"unavailable profile", func(spec *tmux.AgentLaunchSpec) { spec.CAAMProfile = "unavailable-profile" }, "unavailable-profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude, Command: "claude"}
			tc.mutate(&spec)
			cfg := config.Default()
			cfg.Integrations.CAAM.BinaryPath = "/bin/false"
			if _, err := PreflightAgentLaunchSpec(context.Background(), cfg, spec, t.TempDir()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("preflight error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAgentLaunchSpecPreflightVerifiesOriginalPersonaPrompt(t *testing.T) {
	prompt := filepath.Join(t.TempDir(), "persona.md")
	const original = "Use the original architecture and constraints."
	if err := os.WriteFile(prompt, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	spec := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude,
		Command: "claude --append-system-prompt-file " + tmux.ShellQuote(prompt), Persona: "architect",
		SystemPromptFile: prompt, SystemPromptSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(original))),
	}
	if _, err := PreflightAgentLaunchSpec(context.Background(), nil, spec, t.TempDir()); err != nil {
		t.Fatalf("original prompt rejected: %v", err)
	}
	if err := os.WriteFile(prompt, []byte("different instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PreflightAgentLaunchSpec(context.Background(), nil, spec, t.TempDir()); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("changed persona was silently replayed: %v", err)
	}
	spec.SystemPromptFile = filepath.Join(t.TempDir(), "missing-persona.md")
	if _, err := PreflightAgentLaunchSpec(context.Background(), nil, spec, t.TempDir()); err == nil {
		t.Fatal("missing original persona prompt was silently reconstructed")
	}
}

func TestAgentLaunchSpecCancellationPreventsProvisioning(t *testing.T) {
	spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude, Command: "claude"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PreflightAgentLaunchSpec(ctx, nil, spec, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preflight = %v", err)
	}
	plan, err := PreflightAgentLaunchSpec(context.Background(), nil, spec, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Prepare(ctx, t.TempDir(), "restored", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation = %v", err)
	}
}

func TestAgentLaunchSpecPreflightRejectsNonIsolableCredentialStore(t *testing.T) {
	projectDir := t.TempDir()
	spec := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude, Command: "claude",
		ClaudeIsolateCredentials: true, CAAMProfile: "profile-must-not-be-resolved",
	}
	checks := 0
	_, err := preflightAgentLaunchSpec(context.Background(), nil, spec, projectDir, func() error {
		checks++
		return swarm.ErrCredentialStoreNotIsolable
	})
	if !errors.Is(err, swarm.ErrCredentialStoreNotIsolable) || checks != 1 {
		t.Fatalf("unsupported credential store reached later launch stages: %v (checks=%d)", err, checks)
	}
	entries, err := os.ReadDir(projectDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed isolation preflight provisioned files: %v, %v", entries, err)
	}
}

func TestAgentLaunchSpecPreparationRechecksSavedPersonaPrompt(t *testing.T) {
	prompt := filepath.Join(t.TempDir(), "persona.md")
	const original = "Keep the original review constraints."
	if err := os.WriteFile(prompt, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	spec := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude, Command: "claude --append-system-prompt-file " + tmux.ShellQuote(prompt),
		SystemPromptFile: prompt, SystemPromptSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(original))),
	}
	plan, err := PreflightAgentLaunchSpec(context.Background(), nil, spec, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prompt, []byte("replaced between preflight and pane launch"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Prepare(context.Background(), t.TempDir(), "restored", 1); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("launch accepted changed prompt after preflight: %v", err)
	}
}
