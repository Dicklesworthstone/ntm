package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

type stubCAAMResolver struct {
	mu    sync.Mutex
	calls []string
	seats map[string]*tools.CAAMRankedProfile
	errs  map[string]error
}

func TestCAAMSeatCompatibilityPreservesExplicitPinsAndDisabledPolicies(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selection bool
		isolation bool
		candidate CAAMSeatCandidate
		conflict  bool
		inherited string
	}{
		{"automatic Claude", true, true, CAAMSeatCandidate{Type: "claude"}, true, ""},
		{"CLI Claude alias", true, true, CAAMSeatCandidate{Type: "cc"}, true, ""},
		{"explicit Claude persona", true, true, CAAMSeatCandidate{Type: "claude", Pinned: true}, false, ""},
		{"selection disabled", false, true, CAAMSeatCandidate{Type: "claude"}, false, ""},
		{"isolation disabled", true, false, CAAMSeatCandidate{Type: "claude"}, false, ""},
		{"Codex unaffected", true, true, CAAMSeatCandidate{Type: "codex"}, false, ""},
		{"inherited Claude without selection", false, true, CAAMSeatCandidate{Type: "claude"}, true, "parent-profile"},
		{"inherited pinned Claude", true, true, CAAMSeatCandidate{Type: "claude", Pinned: true}, true, "parent-profile"},
		{"inherited Claude without isolation", false, false, CAAMSeatCandidate{Type: "claude"}, false, "parent-profile"},
		{"inherited Codex", false, true, CAAMSeatCandidate{Type: "codex"}, false, "parent-profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHALLOW_PROFILE", tc.inherited)
			cfg := config.Default()
			cfg.Integrations.CAAM.SeatSelection = tc.selection
			cfg.Agents.ClaudeIsolateCredentials = tc.isolation
			err := ValidateCAAMSeatCompatibility(cfg, []CAAMSeatCandidate{tc.candidate})
			if errors.Is(err, resilience.ErrCAAMClaudeIsolationConflict) != tc.conflict {
				t.Fatalf("compatibility error=%v, want conflict=%t", err, tc.conflict)
			}
		})
	}
}

func TestCAAMSeatLaunchCommandRunsWholeCommandUnderSelectedAccount(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "caam with spaces")
	script := `#!/bin/sh
set -eu
[ "$1" = 'shallow-spawn' ] && [ "$2" = '--tool' ] && [ "$3" = 'codex' ] && [ "$5" = '--' ] || exit 41
[ "$4" != 'missing' ] || exit 42
SHALLOW_PROFILE=$4
export SHALLOW_PROFILE
shift 5
exec "$@"
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	profile := "spend'me; reserve"
	decision := CAAMSeatDecision{Provider: "codex", Profile: profile}
	inner := `printf '%s\n' "$SHALLOW_PROFILE" && printf '%s\n' "$SHALLOW_PROFILE"`
	command, err := decision.LaunchCommand(binary, inner)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "SHALLOW_PROFILE=host-reserve")
	got, err := cmd.CombinedOutput()
	if err != nil || string(got) != profile+"\n"+profile+"\n" {
		t.Fatalf("selected-account launch output=%q err=%v command=%q", got, err, command)
	}
	marker := filepath.Join(root, "agent-started")
	decision.Profile = "missing"
	command, err = decision.LaunchCommand(binary, "touch "+tmux.ShellQuote(marker))
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", command).CombinedOutput(); err == nil {
		t.Fatalf("unavailable seat launched: %s", output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("agent ran despite unavailable selected profile: %v", err)
	}
}

func TestCAAMSeatLaunchCommandRefusesInvalidBinding(t *testing.T) {
	for _, decision := range []CAAMSeatDecision{
		{Provider: "codex", Profile: "seat", Skip: true, Reason: "pool exhausted"},
		{Provider: "codex"},
		{Profile: "seat"},
		{Provider: "codex", Profile: "seat\nother"},
	} {
		command, err := decision.LaunchCommand("caam", "agent")
		if err == nil || strings.TrimSpace(command) != "" {
			t.Fatalf("invalid decision %+v rendered %q: %v", decision, command, err)
		}
	}
}

func (s *stubCAAMResolver) RankedSeat(_ context.Context, provider, _ string) (*tools.CAAMRankedProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, provider)
	if err, ok := s.errs[provider]; ok {
		return nil, err
	}
	if seat, ok := s.seats[provider]; ok {
		return seat, nil
	}
	return nil, fmt.Errorf("no fixture for provider %q", provider)
}

func TestPlanCAAMSeats_SharedEngineCanonicalAliasesAndSkipIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	projectDir := t.TempDir()

	// Default config is off.
	if CAAMSeatSelectionEnabled(config.Default()) {
		t.Fatal("seat_selection must default to off")
	}

	cfg := config.Default()
	cfg.Integrations.CAAM.SeatSelection = true

	resolver := &stubCAAMResolver{
		seats: map[string]*tools.CAAMRankedProfile{
			"claude": {Provider: "claude", Profile: "early-seat", Eligible: true},
		},
		errs: map[string]error{
			"codex": fmt.Errorf("%w: all seats capped", tools.ErrCAAMNoSeatSelectable),
		},
	}

	candidates := []CAAMSeatCandidate{
		{Index: 0, Type: "claude", Pinned: false}, // Robot vocabulary
		{Index: 1, Type: "cc", Pinned: true},      // CLI vocabulary, pinned
		{Index: 2, Type: "codex", Pinned: false},  // Robot vocabulary, exhausted
		{Index: 3, Type: "gemini", Pinned: false}, // Unsupported by caam limits
	}
	plan := PlanCAAMSeatsWithResolver(context.Background(), cfg, projectDir, candidates, resolver)
	if plan == nil {
		t.Fatal("expected non-nil seat plan")
	}

	seat, ok := plan.Seat(0)
	if !ok || seat.Persona != "claude-early-seat" {
		t.Fatalf("slot 0 seat = %+v, ok=%v; want claude-early-seat", seat, ok)
	}
	if _, decided := plan.For(1); decided {
		t.Fatal("pinned slot 1 must not be re-ranked")
	}
	reason, skipped := plan.SkipReason(2)
	if !skipped || !strings.Contains(reason, "no codex seat has included headroom (all seats capped)") {
		t.Fatalf("slot 2 skip = (%q, %v), want exhausted headroom reason", reason, skipped)
	}
	if _, decided := plan.For(3); decided {
		t.Fatal("gemini slot 3 must not receive a CAAM seat decision")
	}
	if len(resolver.calls) != 2 || resolver.calls[0] != "claude" || resolver.calls[1] != "codex" {
		t.Fatalf("expected deterministic single-query-per-provider order [claude codex], got %v", resolver.calls)
	}
}

func TestWorktreeLifecycle_PreflightProvisionResolveAndEnv(t *testing.T) {
	ctx := context.Background()

	// Non-git directory fails closed.
	nonGit := t.TempDir()
	if err := PreflightWorktreeProject(ctx, nonGit); err == nil {
		t.Fatal("expected non-git directory to fail worktree preflight")
	}
	if err := ValidateSpawnWorktreeOptions(true, "custom", 2); err == nil {
		t.Fatal("expected --worktree-name with 2 agents to be rejected")
	}
	if err := ValidateSpawnWorktreeOptions(true, "custom", 1); err != nil {
		t.Fatalf("single-agent worktree-name unexpectedly rejected: %v", err)
	}

	// Real git repo succeeds end-to-end.
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
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial")

	names := []string{
		WorktreeAgentName("claude", 1, ""),
		WorktreeAgentName("codex", 1, ""),
	}
	if names[0] != "cc_1" || names[1] != "cod_1" {
		t.Fatalf("WorktreeAgentName normalized = %v, want [cc_1 cod_1]", names)
	}

	mgr, err := PreflightSpawnWorktrees(ctx, repo, "ori_wt_test", names)
	if err != nil {
		t.Fatalf("PreflightSpawnWorktrees failed: %v", err)
	}
	plan, affected, err := ProvisionSpawnWorktrees(ctx, mgr, names)
	if err != nil {
		t.Fatalf("ProvisionSpawnWorktrees failed: %v", err)
	}
	if len(affected) != 2 {
		t.Fatalf("affected paths = %v, want 2", affected)
	}
	for _, name := range names {
		dir, err := plan.ResolveLaunchDir(ctx, name)
		if err != nil {
			t.Fatalf("ResolveLaunchDir(%q) failed: %v", name, err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "README.md")); statErr != nil {
			t.Fatalf("provisioned worktree %q missing README.md: %v", dir, statErr)
		}
	}

	env, injected := ApplyWorktreeAgentMailProjectEnv(nil, nil, repo)
	if !injected || env["AGENT_MAIL_PROJECT"] != repo {
		t.Fatalf("ApplyWorktreeAgentMailProjectEnv = (%v, %v), want AGENT_MAIL_PROJECT=%q", env, injected, repo)
	}
	envOverride, injected := ApplyWorktreeAgentMailProjectEnv(nil, map[string]string{"AGENT_MAIL_PROJECT": "/custom"}, repo)
	if injected || envOverride != nil {
		t.Fatalf("expected user override to suppress injection, got (%v, %v)", envOverride, injected)
	}
}

func TestPersonaAndProfileSetResolution_SharedEngine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	projectDir := t.TempDir()

	spec, err := ParsePersonaSpec("reviewer:2")
	if err != nil || spec.Name != "reviewer" || spec.Count != 2 {
		t.Fatalf("ParsePersonaSpec = (%+v, %v), want reviewer:2", spec, err)
	}
	slots, err := ResolveSpawnPersonas([]PersonaSpec{spec}, projectDir)
	if err != nil {
		t.Fatalf("ResolveSpawnPersonas failed: %v", err)
	}
	if len(slots) != 2 || slots[0].Persona == nil || slots[0].Persona.Name != "reviewer" {
		t.Fatalf("unexpected resolved slots: %+v", slots)
	}

	promptFile, err := PrepareRequiredPersonaSystemPrompt(slots[0].Persona, projectDir)
	if err != nil {
		t.Fatalf("PrepareRequiredPersonaSystemPrompt failed: %v", err)
	}
	if promptFile == "" {
		t.Fatal("expected non-empty materialized system prompt path for builtin reviewer persona")
	}

	// Distribution validation accepts exact match and fails closed on mismatch.
	if err := ValidateProfileAgentDistribution(
		map[agent.AgentType]int{agent.AgentTypeClaudeCode: 2},
		map[agent.AgentType]int{agent.AgentType("claude"): 2},
	); err != nil {
		t.Fatalf("expected canonical alias match to pass, got %v", err)
	}
	if err := ValidateProfileAgentDistribution(
		map[agent.AgentType]int{agent.AgentTypeClaudeCode: 2},
		map[agent.AgentType]int{agent.AgentTypeCodex: 2},
	); err == nil {
		t.Fatal("expected type mismatch to fail closed")
	}
}

func TestSpawnHooks_PreAndPostExecutionAndEnvPropagation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	marker := filepath.Join(dir, "hook_trace.txt")

	hookCfg := &hooks.CommandHooksConfig{
		Hooks: []hooks.CommandHook{
			{
				Event:   hooks.EventPreSpawn,
				Name:    "record-pre",
				Command: fmt.Sprintf(`printf "pre:%%s:%%s\n" "$NTM_AGENT_COUNT_CC" "$NTM_AGENT_COUNT_TOTAL" >> %q`, marker),
			},
			{
				Event:   hooks.EventPostSpawn,
				Name:    "record-post",
				Command: fmt.Sprintf(`printf "post:%%s:%%s:%%s\n" "$NTM_PANE_COUNT" "$NTM_PANE_TITLES" "$NTM_SPAWN_SUCCESS" >> %q`, marker),
			},
		},
	}
	exec := hooks.NewExecutor(hookCfg)
	env := SpawnHookEnv(SpawnHookCounts{CC: 2, Total: 2})

	if _, err := RunPreSpawnHooks(ctx, exec, "sess_hook", dir, env); err != nil {
		t.Fatalf("RunPreSpawnHooks failed: %v", err)
	}
	if _, err := RunPostSpawnHooks(ctx, exec, "sess_hook", dir, env, 3, []string{"sess_hook__user", "sess_hook__cc_1", "sess_hook__cc_2"}); err != nil {
		t.Fatalf("RunPostSpawnHooks failed: %v", err)
	}

	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read hook trace: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "pre:2:2\n") {
		t.Fatalf("missing pre-spawn hook env line in %q", got)
	}
	if !strings.Contains(got, "post:3:sess_hook__user,sess_hook__cc_1,sess_hook__cc_2:true\n") {
		t.Fatalf("missing post-spawn hook env line in %q", got)
	}

	// Failing pre-spawn hook aborts spawn.
	failExec := hooks.NewExecutor(&hooks.CommandHooksConfig{
		Hooks: []hooks.CommandHook{{Event: hooks.EventPreSpawn, Name: "deny", Command: "exit 42"}},
	})
	if _, err := RunPreSpawnHooks(ctx, failExec, "sess_hook", dir, env); err == nil {
		t.Fatal("expected failing pre-spawn hook to return an error")
	}

	// Disabled Gemini setup is a clean no-op.
	disabledCfg := config.Default()
	disabledCfg.GeminiSetup.AutoSelectProModel = false
	if err := RunGeminiPostSpawnSetup(ctx, disabledCfg, "%99"); err != nil && !errors.Is(err, nil) {
		t.Fatalf("disabled Gemini setup returned error: %v", err)
	}
}
