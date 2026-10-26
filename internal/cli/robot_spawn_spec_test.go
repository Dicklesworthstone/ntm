package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/startup"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestRobotPluginSpawnFlagsPreserveBuiltinControls(t *testing.T) {
	cmd := &cobra.Command{Use: "ntm"}
	var omp, timeout string
	cmd.Flags().StringVar(&omp, "spawn-omp", "", "")
	cmd.Flags().StringVar(&timeout, "spawn-timeout", "30s", "")
	values := registerRobotPluginSpawnFlags(cmd, []plugins.AgentPlugin{
		{Name: "pi", Alias: "pine"}, {Name: "omp", Alias: "om"}, {Name: "timeout", Alias: "pi-timeout"},
	})
	if err := cmd.ParseFlags([]string{"--spawn-pine=2:provider/test:high", "--spawn-omp=1", "--spawn-timeout=45s"}); err != nil {
		t.Fatal(err)
	}
	if values["pi"] == nil || *values["pi"] != "2:provider/test:high" || omp != "1" || timeout != "45s" || values["omp"] != nil || cmd.Flags().Lookup("spawn-om") != nil {
		t.Fatalf("plugin flags replaced a built-in or lost the canonical alias: values=%v omp=%q timeout=%q", values, omp, timeout)
	}
	old := robotSpawnPlugins
	robotSpawnPlugins = values
	t.Cleanup(func() { robotSpawnPlugins = old })
	specs, err := parseRobotSpawnAgentSpecs()
	if err != nil || specs[AgentType("pi")].Count != 2 || specs[AgentType("pi")].Model != "provider/test" || specs[AgentType("pi")].ReasoningEffort != "high" {
		t.Fatalf("dynamic flags did not use the shared model/effort grammar: %+v %v", specs, err)
	}
	*values["pi"] = "two:model"
	if _, err := parseRobotSpawnAgentSpecs(); err == nil || !strings.Contains(err.Error(), "--spawn-pi") {
		t.Fatalf("malformed plugin count was accepted: %v", err)
	}
}

// Re-execution installs the manifest before package initialization, so this
// drives the real root constructor, option parsing, robot engine, tmux launch,
// and native session binding together.
func TestRobotPiSpawnThroughRootCommand(t *testing.T) {
	const childEnv = "NTM_ROBOT_PI_ROOT_FIXTURE"
	if os.Getenv(childEnv) == "" {
		testutil.RequireTmuxThrottled(t)
		root := t.TempDir()
		for _, name := range []string{"agents", "sessions", "project", "home", "data"} {
			if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
				t.Fatal(err)
			}
		}
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		pi := filepath.Join(root, "pi")
		if err := os.Symlink(binary, pi); err != nil {
			t.Fatal(err)
		}
		command := tmux.ShellQuote(pi) + `{{if .Model}} --model {{shellQuote .Model}}{{end}}{{if .ReasoningEffort}} --thinking {{shellQuote .ReasoningEffort}}{{end}}`
		manifest := "[agent]\nname = 'pi'\nalias = 'pine'\ncommand = " + strconv.Quote(command) +
			"\n[agent.env]\nPI_CODING_AGENT_SESSION_DIR = " + strconv.Quote(filepath.Join(root, "sessions")) +
			"\n[agent.readiness]\nidle_patterns = ['^\\s*─{4,}\\s*$']\nworking_patterns = ['^\\s*─+\\s*[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏].*─+\\s*$']\n"
		if err := os.WriteFile(filepath.Join(root, "agents", "pi.toml"), []byte(manifest), 0600); err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(root, "config.toml")
		if err := os.WriteFile(configPath, []byte("[agent_mail]\nenabled = false\nauto_register = false\n[spawn_pacing]\nenabled = false\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(childEnv, root)
		t.Setenv("NTM_CONFIG", configPath)
		t.Setenv("HOME", filepath.Join(root, "home"))
		t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
		t.Setenv("NTM_PI_PANE_FIXTURE", "1")
		t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
		child := exec.CommandContext(t.Context(), binary, "-test.run=^TestRobotPiSpawnThroughRootCommand$", "-test.count=1", "-test.timeout=2m")
		child.Env = os.Environ()
		output, err := child.CombinedOutput()
		if err != nil {
			t.Fatalf("real root pi spawn failed: %v\n%s", err, output)
		}
		return
	}
	root := os.Getenv(childEnv)
	name := "ntm-robot-pi-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() { _ = tmux.KillSession(name) })
	run := func(args ...string) robot.SpawnOutput {
		resetFlags()
		rootCmd.Flags().VisitAll(func(f *pflag.Flag) {
			if slice, ok := f.Value.(pflag.SliceValue); ok {
				_ = slice.Replace(nil)
			} else {
				_ = f.Value.Set(f.DefValue)
			}
			f.Changed = false
		})
		cfg, cfgFile, robotProcessExit = nil, "", nil
		startup.ResetConfig()
		argv := append([]string{"--robot-spawn=" + name, "--spawn-dir=" + filepath.Join(root, "project"), "--spawn-no-user"}, args...)
		stdout, err := captureStdout(t, func() error {
			rootCmd.SetArgs(argv)
			rootCmd.SetContext(t.Context())
			return rootCmd.Execute()
		})
		if err != nil {
			t.Fatalf("root invocation failed: %v\n%s", err, stdout)
		}
		var output robot.SpawnOutput
		if err := json.Unmarshal([]byte(stdout), &output); err != nil || !output.Success {
			t.Fatalf("root spawn response failed: %v\n%s", err, stdout)
		}
		return output
	}
	preview := run("--spawn-pine=2:provider/reviewer:high", "--spawn-omp=1", "--dry-run")
	if len(preview.WouldCreate) != 3 || preview.WouldCreate[0].Type != "omp" || preview.WouldCreate[1].Type != "pi" || preview.WouldCreate[2].Variant != "provider/reviewer" {
		t.Fatalf("root dynamic alias did not produce the real mixed-agent plan: %+v", preview)
	}
	output := run("--spawn-pi=2:provider/reviewer:high", "--spawn-wait", "--spawn-timeout=20s")
	if len(output.Agents) != 2 || !output.Agents[0].Ready || !output.Agents[1].Ready {
		t.Fatalf("root spawn did not launch two ready pi agents: %+v", output)
	}
	captured, err := session.Capture(name)
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool)
	for _, pane := range captured.Panes {
		if pane.AgentType != "pi" || !pane.HasFreshSessionBinding() || pane.LaunchSpec == nil || ids[pane.SessionID] {
			t.Fatalf("robot pi pane has no distinct native transcript: %+v", pane)
		}
		ids[pane.SessionID] = true
		if pane.LaunchSpec.Model != "provider/reviewer" || pane.LaunchSpec.ReasoningEffort != "high" ||
			!strings.Contains(pane.LaunchSpec.Command, "--session-id ") || strings.Contains(pane.LaunchSpec.Command, filepath.Join(root, "sessions")) ||
			!reflect.DeepEqual(pane.LaunchSpec.OmittedEnv, []string{"PI_CODING_AGENT_SESSION_DIR"}) {
			t.Fatalf("robot launch lost settings or persisted plugin environment values: %+v", pane.LaunchSpec)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("native pi binding count = %d, want 2", len(ids))
	}
}

// TestParseRobotSpawnAgentFlag covers the count[:model[:effort]] grammar the
// robot spawn flags share with `ntm spawn --cc/--cod/...` (bd-rr8gn).
func TestParseRobotSpawnAgentFlag(t *testing.T) {
	tests := []struct {
		name       string
		flag       string
		value      string
		agentType  AgentType
		wantCount  int
		wantModel  string
		wantEffort string
		wantErr    string
	}{
		{name: "empty means unset", flag: "--spawn-cc", value: "", agentType: AgentTypeClaude, wantCount: 0},
		{name: "whitespace only means unset", flag: "--spawn-cc", value: "  ", agentType: AgentTypeClaude, wantCount: 0},
		{name: "plain int", flag: "--spawn-cc", value: "2", agentType: AgentTypeClaude, wantCount: 2},
		{name: "plain zero preserved", flag: "--spawn-cod", value: "0", agentType: AgentTypeCodex, wantCount: 0},
		{name: "negative int passes through for robot validation", flag: "--spawn-cc", value: "-1", agentType: AgentTypeClaude, wantCount: -1},
		{name: "count and model", flag: "--spawn-cod", value: "3:gpt-5.3-codex", agentType: AgentTypeCodex, wantCount: 3, wantModel: "gpt-5.3-codex"},
		{name: "count model colon effort", flag: "--spawn-cod", value: "8:gpt-5.6-terra:high", agentType: AgentTypeCodex, wantCount: 8, wantModel: "gpt-5.6-terra", wantEffort: "high"},
		{name: "count model at effort", flag: "--spawn-cod", value: "8:gpt-5.6-terra@high", agentType: AgentTypeCodex, wantCount: 8, wantModel: "gpt-5.6-terra", wantEffort: "high"},
		{name: "claude model and effort", flag: "--spawn-cc", value: "2:opus:high", agentType: AgentTypeClaude, wantCount: 2, wantModel: "opus", wantEffort: "high"},
		{name: "grok at effort", flag: "--spawn-grok", value: "1:grok-4@low", agentType: AgentTypeGrok, wantCount: 1, wantModel: "grok-4", wantEffort: "low"},
		{name: "gmi model only", flag: "--spawn-gmi", value: "1:gemini-3.1-pro", agentType: AgentTypeGemini, wantCount: 1, wantModel: "gemini-3.1-pro"},
		// gmi has no effort knob, so '@' stays a literal model character —
		// identical to `ntm spawn --gmi` (agentTypeSupportsEffortSuffix).
		{name: "gmi at stays in model", flag: "--spawn-gmi", value: "1:provider/model@tag", agentType: AgentTypeGemini, wantCount: 1, wantModel: "provider/model@tag"},
		// oc has no effort knob either; OpenCode models are provider/model.
		{name: "oc provider model", flag: "--spawn-oc", value: "1:opencode/space-bunny-free", agentType: AgentTypeOpencode, wantCount: 1, wantModel: "opencode/space-bunny-free"},
		{name: "oc at stays in model", flag: "--spawn-oc", value: "2:provider/model@tag", agentType: AgentTypeOpencode, wantCount: 2, wantModel: "provider/model@tag"},
		{name: "agy model override rejected", flag: "--spawn-agy", value: "1:gemini-3.1-pro-high", agentType: AgentTypeAntigravity, wantErr: "model is pinned"},
		{name: "invalid count", flag: "--spawn-cod", value: "x:gpt-5.3-codex", agentType: AgentTypeCodex, wantErr: "invalid count"},
		{name: "zero count with model rejected", flag: "--spawn-cod", value: "0:gpt-5.3-codex", agentType: AgentTypeCodex, wantErr: "count must be at least 1"},
		{name: "empty model", flag: "--spawn-cod", value: "2:", agentType: AgentTypeCodex, wantErr: "empty model"},
		{name: "empty effort", flag: "--spawn-cod", value: "2:model:", agentType: AgentTypeCodex, wantErr: "empty reasoning effort"},
		{name: "double effort rejected", flag: "--spawn-cod", value: "2:model@high:low", agentType: AgentTypeCodex, wantErr: "reasoning effort twice"},
		{name: "bad model charset", flag: "--spawn-cc", value: "2:bad model", agentType: AgentTypeClaude, wantErr: "invalid characters in model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := parseRobotSpawnAgentFlag(tt.flag, tt.value, tt.agentType)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parseRobotSpawnAgentFlag(%q)=%+v, want error containing %q", tt.value, spec, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.flag) && tt.value != "" {
					t.Fatalf("error %q does not name the offending flag %s", err.Error(), tt.flag)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRobotSpawnAgentFlag(%q): %v", tt.value, err)
			}
			if spec.Count != tt.wantCount || spec.Model != tt.wantModel || spec.ReasoningEffort != tt.wantEffort {
				t.Fatalf("parseRobotSpawnAgentFlag(%q)={Count:%d Model:%q Effort:%q}, want {%d %q %q}",
					tt.value, spec.Count, spec.Model, spec.ReasoningEffort, tt.wantCount, tt.wantModel, tt.wantEffort)
			}
		})
	}
}

// robotSpawnPromptFlagCmd registers the --robot-spawn prompt/stagger flags
// bound to the real globals, so Changed reflects exactly what a test sets.
func robotSpawnPromptFlagCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().StringVar(&robotSpawnPrompt, "spawn-prompt", "", "")
	cmd.Flags().StringVar(&robotSpawnPromptFile, "spawn-prompt-file", "", "")
	cmd.Flags().StringVar(&robotSpawnStaggerMode, "spawn-stagger-mode", "", "")
	cmd.Flags().StringVar(&robotSpawnStaggerDelay, "spawn-stagger-delay", "", "")
	return cmd
}

// TestApplyRobotSpawnPromptFlagsResolvesSpawnConfigDefaults pins how
// --robot-spawn resolves its prompt and stagger: [spawn] fills unset stagger
// flags, explicit flags win, and malformed input is an error.
func TestApplyRobotSpawnPromptFlagsResolvesSpawnConfigDefaults(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)
	spawnCfg := config.Default()
	spawnCfg.Spawn.StaggerMode = config.SpawnStaggerFixed
	spawnCfg.Spawn.StaggerDelay = 45 * time.Second
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("Read AGENTS.md first\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		cfg       *config.Config
		flags     map[string]string
		wantMode  string
		wantDelay time.Duration
		wantText  string
		wantErr   string
	}{
		{name: "config defaults apply when flags are absent", cfg: spawnCfg, flags: map[string]string{"spawn-prompt": "go"},
			wantMode: "fixed", wantDelay: 45 * time.Second, wantText: "go"},
		{name: "built-in defaults without a [spawn] table", cfg: config.Default(),
			wantMode: "none", wantDelay: 30 * time.Second},
		{name: "explicit flags override config", cfg: spawnCfg,
			flags:    map[string]string{"spawn-stagger-mode": "smart", "spawn-stagger-delay": "10s"},
			wantMode: "smart", wantDelay: 10 * time.Second},
		{name: "explicit none disables configured pacing", cfg: spawnCfg, flags: map[string]string{"spawn-stagger-mode": "none"},
			wantMode: "none", wantDelay: 45 * time.Second},
		{name: "prompt file", cfg: spawnCfg, flags: map[string]string{"spawn-prompt-file": promptFile},
			wantMode: "fixed", wantDelay: 45 * time.Second, wantText: "Read AGENTS.md first\n"},
		{name: "prompt and prompt file are exclusive", cfg: spawnCfg,
			flags:   map[string]string{"spawn-prompt": "go", "spawn-prompt-file": promptFile},
			wantErr: "use either --spawn-prompt or --spawn-prompt-file, not both"},
		{name: "unparseable delay", cfg: spawnCfg, flags: map[string]string{"spawn-stagger-delay": "soon"},
			wantErr: `invalid --spawn-stagger-delay "soon"`},
		{name: "invalid configured mode names the config key", cfg: func() *config.Config {
			bad := config.Default()
			bad.Spawn.StaggerMode = "adaptive"
			return bad
		}(), wantErr: "[spawn] stagger_mode must be one of none, fixed, or smart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetFlags()
			cmd := robotSpawnPromptFlagCmd()
			for name, value := range tc.flags {
				if err := cmd.Flags().Set(name, value); err != nil {
					t.Fatalf("set --%s: %v", name, err)
				}
			}
			var opts robot.SpawnOptions
			err := applyRobotSpawnPromptFlags(cmd, tc.cfg, &opts)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if opts.StaggerMode != tc.wantMode || opts.StaggerDelay != tc.wantDelay || opts.Prompt != tc.wantText {
				t.Fatalf("resolved opts mode=%q delay=%v prompt=%q, want %q %v %q",
					opts.StaggerMode, opts.StaggerDelay, opts.Prompt, tc.wantMode, tc.wantDelay, tc.wantText)
			}
		})
	}
}

// runRobotSpawnRoot executes `ntm --config CONFIG args...` in-process and
// returns the robot JSON envelope, restoring every flag it touched.
func runRobotSpawnRoot(t *testing.T, configTOML string, args ...string) map[string]any {
	t.Helper()
	resetFlags()
	oldCfg, oldCfgFile := cfg, cfgFile
	cfg, cfgFile = nil, ""
	robotProcessExit = nil
	startup.ResetConfig()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte(configTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	argv := append([]string{"--config=" + configPath}, args...)
	// pflag never resets values or Changed between parses, and earlier tests
	// in this package leave values behind (a stale --help=true makes cobra
	// print help instead of running). Start from a fresh-process view.
	rootCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Value.String() != f.DefValue {
			if slice, ok := f.Value.(pflag.SliceValue); ok {
				_ = slice.Replace(nil)
			} else {
				_ = f.Value.Set(f.DefValue)
			}
		}
		f.Changed = false
	})
	t.Cleanup(func() {
		for _, arg := range argv {
			name := strings.TrimPrefix(strings.SplitN(arg, "=", 2)[0], "--")
			if f := rootCmd.Flags().Lookup(name); f != nil {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			}
		}
		resetFlags()
		robotDryRun = false
		robotProcessExit = nil
		cfg, cfgFile = oldCfg, oldCfgFile
		startup.ResetConfig()
	})

	out, err := captureStdout(t, func() error {
		rootCmd.SetArgs(argv)
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute(%v): %v\n%s", argv, err, out)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		var changed []string
		rootCmd.Flags().Visit(func(f *pflag.Flag) { changed = append(changed, f.Name+"="+f.Value.String()) })
		t.Fatalf("robot spawn output is not one JSON envelope: %v\nstdout=%q\nrobotSpawn=%q exit=%v changed=%v", err, out, robotSpawn, robotProcessExit, changed)
	}
	return envelope
}

// TestRobotSpawnPromptStaggerThroughRootCommand drives `ntm --robot-spawn`
// itself: the [spawn] table's stagger defaults reach the reported schedule,
// explicit --spawn-stagger-* flags override them, and invalid values fail
// with INVALID_FLAG before any session is touched.
func TestRobotSpawnPromptStaggerThroughRootCommand(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("robot spawn dry runs require tmux on PATH")
	}
	const spawnTable = "[spawn]\nstagger_mode = \"fixed\"\nstagger_delay = \"45s\"\n"
	spawnArgs := func(session string, extra ...string) []string {
		return append([]string{
			"--robot-spawn=" + session, "--spawn-cc=2", "--spawn-no-user",
			"--spawn-dir=" + t.TempDir(), "--spawn-prompt=Read AGENTS.md first", "--dry-run",
		}, extra...)
	}
	scheduleDelays := func(t *testing.T, envelope map[string]any) (string, float64, []float64) {
		t.Helper()
		if envelope["success"] != true {
			t.Fatalf("robot spawn failed: %v", envelope)
		}
		stagger, ok := envelope["stagger"].(map[string]any)
		if !ok {
			t.Fatalf("envelope has no stagger plan: %v", envelope)
		}
		var delays []float64
		for _, slot := range stagger["schedule"].([]any) {
			delays = append(delays, slot.(map[string]any)["delay_ms"].(float64))
		}
		return stagger["mode"].(string), stagger["interval_ms"].(float64), delays
	}

	t.Run("spawn config defaults", func(t *testing.T) {
		mode, interval, delays := scheduleDelays(t, runRobotSpawnRoot(t, spawnTable, spawnArgs("cfgstagger")...))
		if mode != "fixed" || interval != 45000 || !reflect.DeepEqual(delays, []float64{0, 45000}) {
			t.Fatalf("config-default stagger = %s/%v %v, want fixed/45000 [0 45000]", mode, interval, delays)
		}
	})
	t.Run("delay flag overrides spawn config", func(t *testing.T) {
		mode, interval, delays := scheduleDelays(t, runRobotSpawnRoot(t, spawnTable, spawnArgs("flagstagger", "--spawn-stagger-delay=10s")...))
		if mode != "fixed" || interval != 10000 || !reflect.DeepEqual(delays, []float64{0, 10000}) {
			t.Fatalf("flag-override stagger = %s/%v %v, want fixed/10000 [0 10000]", mode, interval, delays)
		}
	})
	t.Run("mode flag overrides spawn config", func(t *testing.T) {
		mode, interval, delays := scheduleDelays(t, runRobotSpawnRoot(t, spawnTable, spawnArgs("nostagger", "--spawn-stagger-mode=none")...))
		if mode != "none" || interval != 0 || !reflect.DeepEqual(delays, []float64{0, 0}) {
			t.Fatalf("explicit none stagger = %s/%v %v, want none/0 [0 0]", mode, interval, delays)
		}
	})
	for _, tc := range []struct {
		name    string
		config  string
		extra   []string
		wantErr string
	}{
		{name: "unsupported mode", config: spawnTable, extra: []string{"--spawn-stagger-mode=adaptive"}, wantErr: "--spawn-stagger-mode must be one of none, fixed, or smart"},
		{name: "delay above maximum", config: spawnTable, extra: []string{"--spawn-stagger-delay=10m"}, wantErr: "--spawn-stagger-delay must be between 0 and 5m0s"},
		{name: "unparseable delay", config: spawnTable, extra: []string{"--spawn-stagger-delay=soon"}, wantErr: "invalid --spawn-stagger-delay"},
		{name: "invalid spawn config", config: "[spawn]\nstagger_mode = \"bogus\"\n", wantErr: "[spawn] stagger_mode must be one of none, fixed, or smart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := runRobotSpawnRoot(t, tc.config, spawnArgs("badstagger", tc.extra...)...)
			if envelope["success"] != false || envelope["error_code"] != "INVALID_FLAG" ||
				!strings.Contains(envelope["error"].(string), tc.wantErr) {
				t.Fatalf("envelope = %v, want INVALID_FLAG containing %q", envelope, tc.wantErr)
			}
			if _, ok := envelope["would_create"]; ok {
				t.Fatalf("invalid stagger still planned panes: %v", envelope)
			}
		})
	}
}
