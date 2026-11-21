package robot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

type stubRobotCAAMResolver struct {
	seats map[string]*tools.CAAMRankedProfile
	errs  map[string]error
	calls []string
}

func (s *stubRobotCAAMResolver) RankedSeat(_ context.Context, provider, _ string) (*tools.CAAMRankedProfile, error) {
	s.calls = append(s.calls, provider)
	if err, ok := s.errs[provider]; ok {
		return nil, err
	}
	if seat, ok := s.seats[provider]; ok {
		return seat, nil
	}
	return nil, fmt.Errorf("no stub seat for %q", provider)
}

func TestRobotSpawnORI_CAAMSeatSelectionParity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("SHALLOW_PROFILE", "host-reserve")
	projectDir := t.TempDir()

	cfg := config.Default()
	cfg.Integrations.CAAM.SeatSelection = true
	cfg.Agents.Claude = `claude --persona {{.PersonaName}} {{if .Model}}--model {{.Model}} {{end}}{{if .SystemPromptFile}}--system-prompt-file {{.SystemPromptFile}}{{end}}`
	cfg.Agents.Codex = `codex --persona {{.PersonaName}}`

	resolver := &stubRobotCAAMResolver{
		seats: map[string]*tools.CAAMRankedProfile{
			"claude": {Provider: "claude", Profile: "soonest-reset", Eligible: true},
		},
		errs: map[string]error{
			"codex": fmt.Errorf("%w: codex pool exhausted", tools.ErrCAAMNoSeatSelectable),
		},
	}

	type launchedCall struct {
		agentType string
		dir       string
		command   string
	}
	var launched []launchedCall
	var manifest resilience.SpawnMonitorRequest

	deps := &SpawnLifecycleDependencies{
		IsTMUXInstalled: func() bool { return true },
		GetAllPanes:     func(context.Context) (map[string][]tmux.Pane, error) { return map[string][]tmux.Pane{}, nil },
		SessionExists:   func(context.Context, string) (bool, error) { return false, nil },
		CreateSession:   func(context.Context, string, string, int) error { return nil },
		GetPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return []tmux.Pane{
				{ID: "%1", WindowIndex: 0, Index: 0, Command: "bash", PID: 101},
				{ID: "%2", WindowIndex: 0, Index: 1, Command: "bash", PID: 102},
				{ID: "%3", WindowIndex: 0, Index: 2, Command: "bash", PID: 103},
			}, nil
		},
		SplitWindow:      func(context.Context, string, string) (string, error) { return "%x", nil },
		ApplyTiledLayout: func(context.Context, string) error { return nil },
		LaunchAgent: func(_ context.Context, pane tmux.Pane, sessionName, agentType string, num int, dir, command string) (SpawnedAgent, error) {
			launched = append(launched, launchedCall{agentType: agentType, dir: dir, command: command})
			return SpawnedAgent{
				Pane:   fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index),
				PaneID: pane.ID,
				Type:   agentType,
				Title:  fmt.Sprintf("%s__%s_%d", sessionName, agentTypeShort(agentType), num),
			}, nil
		},
		CAAMSeatResolver: resolver,
		HookExecutor:     hooks.NewExecutor(nil),
		StartSessionMonitor: func(_ context.Context, request resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
			manifest = request
			return &resilience.SpawnMonitorResult{MonitorStarted: true}, nil
		},
	}

	// 1 unpinned Claude + 1 pinned reviewer persona (Claude) + 1 unpinned Codex (exhausted).
	out, err := GetSpawn(context.Background(), SpawnOptions{
		Session:       "ori_caam",
		WorkingDir:    projectDir,
		CCCount:       1,
		CodCount:      1,
		Personas:      []string{"reviewer"},
		NoUserPane:    true,
		LifecycleDeps: deps,
	}, cfg)
	if err != nil {
		t.Fatalf("GetSpawn returned error: %v", err)
	}
	if !out.Success {
		t.Fatalf("GetSpawn failed: %s", out.Error)
	}

	// Codex pane must be skipped with structured SeatSkips entry; both Claude panes must launch.
	if len(out.SeatSkips) != 1 {
		t.Fatalf("seat_skips = %+v, want 1 codex skip", out.SeatSkips)
	}
	if out.SeatSkips[0].Provider != "codex" || !strings.Contains(out.SeatSkips[0].Reason, "codex pool exhausted") {
		t.Fatalf("unexpected seat_skips[0]: %+v", out.SeatSkips[0])
	}
	if len(launched) != 2 {
		t.Fatalf("launched = %+v, want 2 claude panes (codex skipped)", launched)
	}
	// First Claude pane is unpinned -> ranked seat claude-soonest-reset.
	if !strings.Contains(launched[0].command, "--persona claude-soonest-reset") {
		t.Fatalf("unpinned claude command = %q, want --persona claude-soonest-reset", launched[0].command)
	}
	// Second Claude pane is pinned to reviewer -> pin wins, model & prompt file rendered.
	if !strings.Contains(launched[1].command, "--persona reviewer") || !strings.Contains(launched[1].command, "--model ") || !strings.Contains(launched[1].command, "--system-prompt-file") {
		t.Fatalf("pinned reviewer command = %q, want --persona reviewer, --model, and --system-prompt-file", launched[1].command)
	}
	if out.Agents[0].Persona != "claude-soonest-reset" {
		t.Fatalf("agents[0].persona = %q, want claude-soonest-reset", out.Agents[0].Persona)
	}
	if out.Agents[1].Persona != "reviewer" || out.Agents[1].PersonaPromptSource == "" {
		t.Fatalf("agents[1] missing persona/prompt source: %+v", out.Agents[1])
	}
	if len(manifest.Agents) != 2 || manifest.Agents[0].LaunchBinding == nil || manifest.Agents[0].LaunchBinding.Identifier != "soonest-reset" {
		t.Fatalf("resilience retained host account instead of selected profile: %+v", manifest.Agents)
	}
}

func writeSpawnSeatPersona(t *testing.T, project string) {
	t.Helper()
	dir := filepath.Join(project, ".ntm")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "personas.toml"), []byte(`[[personas]]
name = "codex-spend-me"
agent_type = "codex"
model = "seat-default-model"
reasoning_effort = "high"
system_prompt = "Use the ranked account while preserving the requested work."
`), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRobotSpawnORI_CAAMIsolationConflictBeforeMutation(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		for _, inherited := range []bool{false, true} {
			t.Run(fmt.Sprintf("dry_run=%t/inherited=%t", dryRun, inherited), func(t *testing.T) {
				profile := ""
				if inherited {
					profile = "parent-profile"
				}
				t.Setenv("SHALLOW_PROFILE", profile)
				cfg := testSpawnConfig()
				cfg.Integrations.CAAM.SeatSelection = !inherited
				cfg.Agents.ClaudeIsolateCredentials = true
				cfg.Agents.ClaudeTokenFile = filepath.Join(t.TempDir(), "host-account-token")
				deps := testSpawnLifecycleDependencies(nil)
				resolver := &stubRobotCAAMResolver{}
				deps.CAAMSeatResolver = resolver
				hookFile := filepath.Join(t.TempDir(), "hook-ran")
				deps.HookExecutor = hooks.NewExecutor(&hooks.CommandHooksConfig{Hooks: []hooks.CommandHook{
					{Event: hooks.EventPreSpawn, Name: "must-not-run", Command: fmt.Sprintf("touch %q", hookFile)},
				}})
				mutations := 0
				deps.CreateSession = func(context.Context, string, string, int) error { mutations++; return nil }
				deps.SplitWindow = func(context.Context, string, string) (string, error) { mutations++; return "%1", nil }
				deps.LaunchAgent = func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
					mutations++
					return SpawnedAgent{}, nil
				}
				out, err := GetSpawn(t.Context(), SpawnOptions{
					Session: "seat-isolation-conflict", CCCount: 1, NoUserPane: true,
					// A non-repository path proves compatibility is checked before
					// even worktree preflight, as well as hooks and tmux mutations.
					WorkingDir: t.TempDir(), UseWorktrees: true, DryRun: dryRun, LifecycleDeps: deps,
				}, cfg)
				if err != nil || out == nil || out.Success || out.ErrorCode != ErrCodeInvalidFlag ||
					!strings.Contains(out.Error, "claude_isolate_credentials") || mutations != 0 || len(resolver.calls) != 0 {
					t.Fatalf("conflicting account owners reached launch: output=%+v err=%v mutations=%d ranks=%v", out, err, mutations, resolver.calls)
				}
				if _, err := os.Stat(hookFile); !os.IsNotExist(err) {
					t.Fatalf("conflicting account owners reached pre-spawn hook: %v", err)
				}
			})
		}
	}
}

func TestRobotSpawnORI_PersonaPromptCannotBeSilentlyDropped(t *testing.T) {
	t.Parallel()
	cfg := testSpawnConfig()
	cfg.Agents.Codex = "custom-codex"
	_, err := renderAgentCommandForSlot(cfg, SpawnOptions{Session: "persona-guard"}, "codex", 1, t.TempDir(), spawnLaunchModel{}, false, "reviewer", "/prepared/prompt")
	if err == nil || !strings.Contains(err.Error(), "SystemPromptFile") {
		t.Fatalf("persona prompt was silently discarded: %v", err)
	}
}

func TestRobotSpawnORI_WorktreesHooksAndGeminiSetupParity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// Create real git repo for worktree provisioning.
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ntm-test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=ntm-test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}
	runGit("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("ori\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "init")

	hookLog := filepath.Join(t.TempDir(), "hooks.log")
	hookExec := hooks.NewExecutor(&hooks.CommandHooksConfig{
		Hooks: []hooks.CommandHook{
			{Event: hooks.EventPreSpawn, Name: "pre", Command: fmt.Sprintf(`echo "pre:$NTM_AGENT_COUNT_GMI" >> %q`, hookLog)},
			{Event: hooks.EventPostSpawn, Name: "post", Command: fmt.Sprintf(`echo "post:$NTM_SPAWN_SUCCESS:$NTM_PANE_COUNT" >> %q`, hookLog)},
		},
	})

	var launchDir string
	var launchEnv map[string]string
	var geminiSetupPanes []string

	cfg := config.Default()
	cfg.GeminiSetup.AutoSelectProModel = true

	deps := &SpawnLifecycleDependencies{
		IsTMUXInstalled: func() bool { return true },
		GetAllPanes:     func(context.Context) (map[string][]tmux.Pane, error) { return map[string][]tmux.Pane{}, nil },
		SessionExists:   func(context.Context, string) (bool, error) { return false, nil },
		CreateSession:   func(context.Context, string, string, int) error { return nil },
		GetPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return []tmux.Pane{{ID: "%7", WindowIndex: 0, Index: 0, Command: "bash", PID: 201}}, nil
		},
		SplitWindow:      func(context.Context, string, string) (string, error) { return "%7", nil },
		ApplyTiledLayout: func(context.Context, string) error { return nil },
		LaunchAgent: func(ctx context.Context, pane tmux.Pane, sessionName, agentType string, num int, dir, command string) (SpawnedAgent, error) {
			launchDir = dir
			launchEnv, _ = ctx.Value(spawnPluginEnvironmentContextKey{}).(map[string]string)
			return SpawnedAgent{
				Pane:   fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index),
				PaneID: pane.ID,
				Type:   agentType,
				Title:  fmt.Sprintf("%s__%s_%d", sessionName, agentTypeShort(agentType), num),
			}, nil
		},
		HookExecutor: hookExec,
		GeminiPostSpawnSetup: func(_ context.Context, _ *config.Config, paneID string) error {
			geminiSetupPanes = append(geminiSetupPanes, paneID)
			return nil
		},
	}

	out, err := GetSpawn(context.Background(), SpawnOptions{
		Session:       "ori_full",
		WorkingDir:    repo,
		GmiCount:      1,
		UseWorktrees:  true,
		WorktreeName:  "gmi_isolated",
		NoUserPane:    true,
		LifecycleDeps: deps,
	}, cfg)
	if err != nil {
		t.Fatalf("GetSpawn error: %v", err)
	}
	if !out.Success {
		t.Fatalf("GetSpawn failed: %s", out.Error)
	}
	if launchDir == "" || launchDir == repo || !strings.Contains(launchDir, "gmi_isolated") {
		t.Fatalf("expected isolated worktree launchDir, got %q (repo=%q)", launchDir, repo)
	}
	if out.Agents[0].WorktreePath != launchDir {
		t.Fatalf("agent.worktree_path = %q, want %q", out.Agents[0].WorktreePath, launchDir)
	}
	if launchEnv["AGENT_MAIL_PROJECT"] != repo {
		t.Fatalf("AGENT_MAIL_PROJECT env = %q, want %q", launchEnv["AGENT_MAIL_PROJECT"], repo)
	}
	if len(geminiSetupPanes) != 1 || geminiSetupPanes[0] != "%7" {
		t.Fatalf("GeminiPostSpawnSetup panes = %v, want [%%7]", geminiSetupPanes)
	}

	rawHooks, err := os.ReadFile(hookLog)
	if err != nil {
		t.Fatalf("read hook log: %v", err)
	}
	if !strings.Contains(string(rawHooks), "pre:1\n") || !strings.Contains(string(rawHooks), "post:true:1\n") {
		t.Fatalf("hook log = %q, want pre:1 and post:true:1", string(rawHooks))
	}

	// Failing pre-spawn hook aborts before CreateSession is ever called.
	sessionCreated := false
	deps.CreateSession = func(context.Context, string, string, int) error {
		sessionCreated = true
		return nil
	}
	deps.HookExecutor = hooks.NewExecutor(&hooks.CommandHooksConfig{
		Hooks: []hooks.CommandHook{{Event: hooks.EventPreSpawn, Name: "block", Command: "exit 7"}},
	})
	blockedOut, err := GetSpawn(context.Background(), SpawnOptions{
		Session:       "ori_blocked",
		WorkingDir:    repo,
		GmiCount:      1,
		NoUserPane:    true,
		LifecycleDeps: deps,
	}, cfg)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if blockedOut.Success || sessionCreated {
		t.Fatalf("expected failing pre-spawn hook to block spawn before session creation: success=%v created=%v", blockedOut.Success, sessionCreated)
	}
}

func TestSpawnRecipeResolvesPersonaPins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	r := &recipe.Recipe{
		Name:   "review-preset",
		Agents: []recipe.AgentSpec{{Type: "cc", Count: 1, Persona: "reviewer"}},
	}
	got, err := expandSpawnRecipe(SpawnOptions{Session: "s", WorkingDir: t.TempDir(), Preset: "review-preset"}, r, nil)
	if err != nil {
		t.Fatalf("expandSpawnRecipe with valid persona failed: %v", err)
	}
	if got.CCCount != 1 {
		t.Fatalf("CCCount = %d, want 1", got.CCCount)
	}
	p := got.slotPersonas[spawnRecipeKey{agentType: "claude", number: 1}]
	if p == nil || p.Name != "reviewer" {
		t.Fatalf("slotPersonas[claude,1] = %+v, want reviewer", p)
	}
}
