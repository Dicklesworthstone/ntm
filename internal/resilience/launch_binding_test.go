package resilience

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	if !strings.Contains(command, "shallow-spawn --tool 'claude'") || !strings.Contains(command, "profile-a") ||
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
	want := "'caam' shallow-spawn --tool 'codex' 'profile-a' -- sh -c " + tmux.ShellQuote(original)
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

func TestAgentLaunchSpecCAAMProviderIsCheckedAtEveryLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording CAAM executable requires a POSIX shell")
	}
	for _, provider := range []struct{ alias, tool string }{
		{"claude-code", "claude"},
		{"codex-cli", "codex"},
		{"gmi", "gemini"},
		{"antigravity", "agy"},
	} {
		for _, mode := range []string{"matching", "mismatch-before-preflight", "changed-after-preflight"} {
			t.Run(provider.alias+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				profile := filepath.Join(root, "profile-provider")
				executed := filepath.Join(root, "executed")
				toolLog := filepath.Join(root, "tools")
				t.Setenv("NTM_CAAM_TEST_PROFILE", profile)
				t.Setenv("NTM_CAAM_TEST_EXECUTED", executed)
				t.Setenv("NTM_CAAM_TEST_TOOLS", toolLog)
				otherProvider := "claude"
				if provider.tool == otherProvider {
					otherProvider = "codex"
				}
				actualProvider := provider.tool
				if mode == "mismatch-before-preflight" {
					actualProvider = otherProvider
				}
				if err := os.WriteFile(profile, []byte(actualProvider+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				// Model CAAM's existing-profile contract: --tool is optional,
				// but when present its provider must match before --print-env
				// or exec. Accepting omission makes the old bug observable.
				const script = `#!/bin/sh
set -eu
[ "$1" = shallow-spawn ] || exit 51
shift
want=''
if [ "$1" = --tool ]; then want="$2"; shift 2; fi
[ "$1" = saved-profile ] || exit 52
shift
IFS= read -r actual < "$NTM_CAAM_TEST_PROFILE"
printf '%s\n' "$want" >> "$NTM_CAAM_TEST_TOOLS"
if [ -n "$want" ] && [ "$want" != "$actual" ]; then exit 53; fi
if [ "$1" = --print-env ]; then
  printf 'HOME=/private-profile-home\n'
  exit 0
fi
[ "$1" = -- ] || exit 54
shift
: > "$NTM_CAAM_TEST_EXECUTED"
export NTM_CAAM_TEST_ACTUAL="$actual"
exec "$@"
`
				caam := filepath.Join(root, "caam with spaces")
				if err := os.WriteFile(caam, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				cfg := config.Default()
				cfg.Integrations.CAAM.BinaryPath = caam
				spec := tmux.AgentLaunchSpec{
					Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentType(provider.alias),
					Command: `printf '%s' "$NTM_CAAM_TEST_ACTUAL"; printf '|%s' 'quoted value'`, CAAMProfile: "saved-profile",
				}
				plan, err := PreflightAgentLaunchSpec(t.Context(), cfg, spec, root)
				if mode == "mismatch-before-preflight" {
					if err == nil || plan != nil {
						t.Fatal("a different provider's profile passed saved-launch preflight")
					}
					if _, err := os.Stat(executed); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("failed profile preflight launched the configured command")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if mode == "changed-after-preflight" {
					if err := os.WriteFile(profile, []byte(otherProvider+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				command, err := plan.Prepare(t.Context(), root, "recovered", 2)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(plan.spec, spec) {
					t.Fatal("launcher provider mapping changed the durable saved specification")
				}
				output, runErr := exec.CommandContext(t.Context(), "/bin/sh", "-c", command).CombinedOutput()
				if mode == "changed-after-preflight" {
					if runErr == nil {
						t.Fatal("a changed profile provider launched the saved command")
					}
					if _, err := os.Stat(executed); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("saved command ran under the replacement provider's profile")
					}
				} else if runErr != nil || string(output) != provider.tool+"|quoted value" {
					t.Fatalf("matching profile lost its provider or command quoting: output=%q error=%v", output, runErr)
				}
				if calls, err := os.ReadFile(toolLog); err != nil || string(calls) != strings.Repeat(provider.tool+"\n", 2) {
					t.Fatalf("preflight and execution did not enforce the same provider: calls=%q error=%v", calls, err)
				}
			})
		}
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
	preview, err := plan.Preview(t.Context(), dir, "recovered", 2)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := plan.Prepare(context.Background(), dir, "recovered", 2)
	if err != nil {
		t.Fatal(err)
	}
	inside := "AGENT_MAIL_PROJECT=" + tmux.ShellQuote(original.AgentMailProject) + " " + original.Command
	want := tmux.ShellQuote(caam) + " shallow-spawn --tool 'codex' 'original-profile' -- /bin/sh -c " + tmux.ShellQuote(inside)
	if prepared != want {
		t.Fatalf("saved launch settings changed:\n got %s\nwant %s", prepared, want)
	}
	if preview != prepared {
		t.Fatal("CAAM launch preview differs from the prepared command")
	}
	calls, err := os.ReadFile(log)
	if err != nil || strings.TrimSpace(string(calls)) != "shallow-spawn --tool codex original-profile --print-env" {
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
	preview, err := plan.Preview(t.Context(), projectDir, "restored", 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(preview, token) {
		t.Fatal("launch preview exposed the credential value")
	}
	if _, err := os.Stat(privateDir); !os.IsNotExist(err) {
		t.Fatalf("launch preview provisioned credential state: %v", err)
	}
	prepared, err := plan.Prepare(context.Background(), projectDir, "restored", 2)
	if err != nil {
		t.Fatal(err)
	}
	if preview != prepared {
		t.Fatal("Claude launch preview differs from the prepared command")
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

func TestAgentLaunchSpecPreflightValidatesWorkingDirectoryBeforeProvisioning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux launch directories require a POSIX filesystem")
	}
	for _, name := range []string{"ordinary path with 'quotes' and $literal", "private\ndirectory", "private\rdirectory", "private\tdirectory", "private\x1bdirectory", "private\x7fdirectory", "private\u0085directory"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex, Command: "codex --model saved"}
			_, err := PreflightAgentLaunchSpec(t.Context(), nil, spec, dir)
			if name == "ordinary path with 'quotes' and $literal" {
				if err != nil {
					t.Fatalf("safe quoted directory was rejected: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "preflight agent launch command") {
					t.Fatalf("unsafe directory was not rejected before launch: %v", err)
				}
				if strings.Contains(err.Error(), name) {
					t.Fatalf("directory contents leaked into preflight diagnostic: %q", err)
				}
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("read-only preflight provisioned files: %v, %v", entries, readErr)
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

func TestAgentLaunchSpecPersonaReaderIgnoresRecoveryPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed persona shell launch requires a Unix shell")
	}
	for _, provider := range []struct {
		name     string
		kind     tmux.AgentType
		template func(config.AgentConfig) string
	}{
		{"codex", tmux.AgentCodex, func(a config.AgentConfig) string { return a.Codex }},
		{"agy", tmux.AgentAntigravity, func(a config.AgentConfig) string { return a.Antigravity }},
	} {
		for _, legacy := range []bool{false, true} {
			for _, counterfeit := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/legacy=%v/counterfeit=%v", provider.name, legacy, counterfeit), func(t *testing.T) {
					root := t.TempDir()
					bin := filepath.Join(root, "bin")
					if err := os.Mkdir(bin, 0700); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", bin)
					const original = "review this code\nliteral 'quotes' $() ; remains prompt text"
					prompt := filepath.Join(root, "reviewer's prompt.md")
					if err := os.WriteFile(prompt, []byte(original), 0600); err != nil {
						t.Fatal(err)
					}
					counterfeitLog := filepath.Join(root, "counterfeit-executed")
					if counterfeit {
						script := "#!/bin/sh\nprintf used > " + tmux.ShellQuote(counterfeitLog) + "\nprintf counterfeit-persona\n"
						if err := os.WriteFile(filepath.Join(bin, "cat"), []byte(script), 0700); err != nil {
							t.Fatal(err)
						}
					}
					providerScript := "#!/bin/sh\nprintf '%s' \"$CODEX_SYSTEM_PROMPT\"\n"
					if provider.name == "agy" {
						providerScript = "#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do if [ \"$1\" = --prompt-interactive ]; then shift; printf '%s' \"$1\"; exit; fi; shift; done\n"
					}
					if err := os.WriteFile(filepath.Join(bin, provider.name), []byte(providerScript), 0700); err != nil {
						t.Fatal(err)
					}
					command, err := config.GenerateAgentCommand(provider.template(config.DefaultAgentTemplates()), config.AgentTemplateVars{
						AgentType: string(provider.kind), Model: "retained-model", SystemPromptFile: prompt,
					})
					if err != nil {
						t.Fatal(err)
					}
					if legacy {
						command = strings.Replace(command, "$(/bin/cat ", "$(cat ", 1)
					}
					saved := tmux.AgentLaunchSpec{
						Version: tmux.AgentLaunchSpecVersion, AgentType: provider.kind, Command: command,
						Persona: "reviewer", SystemPromptFile: prompt, SystemPromptSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(original))),
						OmittedEnv: []string{"PATH"},
					}
					before, err := json.Marshal(saved)
					if err != nil {
						t.Fatal(err)
					}
					if err := saved.ValidateReplayWithEnvironment(provider.kind, map[string]string{"PATH": bin}); err != nil {
						t.Fatal(err)
					}
					// The restart engine proves explicit inputs first, then
					// preflights a private copy without clearing the durable marker.
					replaySpec := saved
					replaySpec.OmittedEnv = nil
					plan, err := PreflightAgentLaunchSpec(t.Context(), nil, replaySpec, root)
					if err != nil {
						t.Fatal(err)
					}
					preview, err := plan.Preview(t.Context(), root, "recovered", 1)
					if err != nil {
						t.Fatal(err)
					}
					prepared, err := plan.Prepare(t.Context(), root, "recovered", 1)
					if err != nil || prepared != preview || !strings.Contains(prepared, "$(/bin/cat ") {
						t.Fatalf("preview and preparation did not pin the same reader: %q, %q, %v", preview, prepared, err)
					}
					after, err := json.Marshal(saved)
					if err != nil || string(before) != string(after) || plan.spec.Command != command {
						t.Fatal("reader preparation changed the durable launch specification")
					}
					cmd := exec.CommandContext(t.Context(), "/bin/sh", "-p", "-c", prepared)
					cmd.Env = []string{"PATH=" + bin}
					output, err := cmd.CombinedOutput()
					if err != nil || string(output) != original {
						t.Fatalf("recovered PATH changed the delivered persona: %q, %v", output, err)
					}
					if _, err := os.Stat(counterfeitLog); !os.IsNotExist(err) {
						t.Fatalf("PATH-selected persona reader executed: %v", err)
					}
				})
			}
		}
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

func TestAgentLaunchSpecPinsCAAMAndShellBeforeRecoveryPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("saved CAAM replay requires a POSIX shell")
	}
	for _, configured := range []string{"caam", "./trusted/caam"} {
		t.Run(configured, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			trusted := filepath.Join(root, "trusted")
			replacement := filepath.Join(root, "replacement")
			for _, directory := range []string{trusted, replacement} {
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			preflightLog := filepath.Join(root, "preflight")
			executedLog := filepath.Join(root, "executed")
			t.Setenv("NTM_PINNED_CAAM_PREFLIGHT", preflightLog)
			t.Setenv("NTM_PINNED_CAAM_EXECUTED", executedLog)
			const trustedScript = `#!/bin/sh
set -eu
[ "$1" = shallow-spawn ] && [ "$2" = --tool ] && [ "$3" = codex ] && [ "$4" = saved-profile ] || exit 51
if [ "$5" = --print-env ]; then
  printf trusted-preflight > "$NTM_PINNED_CAAM_PREFLIGHT"
  exit 0
fi
[ "$5" = -- ] || exit 52
printf trusted-launch > "$NTM_PINNED_CAAM_EXECUTED"
shift 5
exec "$@"
`
			const replacementScript = "#!/bin/sh\nprintf replacement-selected > \"$NTM_PINNED_CAAM_EXECUTED\"\nexit 53\n"
			for path, script := range map[string]string{
				filepath.Join(trusted, "caam"):     trustedScript,
				filepath.Join(replacement, "caam"): replacementScript,
				filepath.Join(replacement, "sh"):   replacementScript,
			} {
				if err := os.WriteFile(path, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", trusted)
			cfg := config.Default()
			cfg.Integrations.CAAM.BinaryPath = configured
			spec := tmux.AgentLaunchSpec{
				Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex,
				Command: "printf agent-launched", CAAMProfile: "saved-profile",
			}
			plan, err := PreflightAgentLaunchSpec(t.Context(), cfg, spec, root)
			if err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(preflightLog); err != nil || string(data) != "trusted-preflight" {
				t.Fatalf("expected preflight of the original CAAM executable: %s, %v", data, err)
			}
			// Recovery supplies a different PATH and the destination cwd may
			// differ too. Neither may change the preflighted launch tools.
			t.Setenv("PATH", replacement)
			t.Chdir(replacement)
			command, err := plan.Prepare(t.Context(), root, "recovered", 2)
			if err != nil {
				t.Fatal(err)
			}
			output, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", command).CombinedOutput()
			if err != nil || string(output) != "agent-launched" {
				t.Fatalf("recovery PATH redirected the saved wrapper: %v, %s", err, output)
			}
			if data, err := os.ReadFile(executedLog); err != nil || string(data) != "trusted-launch" {
				t.Fatalf("executed a different CAAM or shell after preflight: %s, %v", data, err)
			}
		})
	}
}
