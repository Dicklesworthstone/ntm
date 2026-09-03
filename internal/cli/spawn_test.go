package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cm"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestShouldStartInternalMonitor_IsDisabledUnderGoTest(t *testing.T) {
	if resilience.ShouldStartInternalMonitor() {
		t.Fatal("expected internal monitor to be disabled under go test")
	}
}

func TestShouldSuperviseAgentMailDaemon_DefaultsExternal(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })

	cfg = nil
	if shouldSuperviseAgentMailDaemon() {
		t.Fatal("nil config should not supervise Agent Mail")
	}

	cfg = config.Default()
	if shouldSuperviseAgentMailDaemon() {
		t.Fatal("default config should leave Agent Mail externally managed")
	}

	enabled := true
	cfg.AgentMail.SupervisorEnabled = &enabled
	if !shouldSuperviseAgentMailDaemon() {
		t.Fatal("explicit supervisor_enabled=true should supervise Agent Mail")
	}

	cfg.AgentMail.Enabled = false
	if shouldSuperviseAgentMailDaemon() {
		t.Fatal("agent_mail.enabled=false should disable Agent Mail supervision")
	}
}

func TestResolveSpawnProjectDirUsesOverride(t *testing.T) {
	projectDir := t.TempDir()

	got, err := resolveSpawnProjectDir(SpawnOptions{
		Session:            "resume-target",
		ProjectDirOverride: projectDir,
	})
	if err != nil {
		t.Fatalf("resolveSpawnProjectDir() error = %v", err)
	}
	if got != projectDir {
		t.Fatalf("project dir = %q, want %q", got, projectDir)
	}
}

func TestResolveSpawnProjectDirRejectsRelativeOverride(t *testing.T) {
	_, err := resolveSpawnProjectDir(SpawnOptions{
		Session:            "resume-target",
		ProjectDirOverride: "relative/project",
	})
	if err == nil {
		t.Fatal("expected relative override error")
	}
	if !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("expected absolute-path error, got %v", err)
	}
}

func TestSpawnPaneEnvExpansionAndValidation(t *testing.T) {
	env := map[string]string{
		"CARGO_TARGET_DIR": "/tmp/build_{project}_{role}_{pane}",
		"GOPATH":           "/tmp/go-{pane}",
	}
	if err := validateSpawnPaneEnv(env); err != nil {
		t.Fatalf("validateSpawnPaneEnv() error = %v", err)
	}
	expanded := expandSpawnPaneEnv(env, "demo", 2, AgentTypeCodex)
	if got, want := expanded["CARGO_TARGET_DIR"], "/tmp/build_demo_cod_2"; got != want {
		t.Errorf("CARGO_TARGET_DIR = %q, want %q", got, want)
	}
	if got, want := prependSpawnPaneEnv("codex", expanded), "CARGO_TARGET_DIR='/tmp/build_demo_cod_2' GOPATH='/tmp/go-2' codex"; got != want {
		t.Errorf("prependSpawnPaneEnv() = %q, want %q", got, want)
	}
	if err := validateSpawnPaneEnv(map[string]string{"BAD-NAME": "value"}); err == nil {
		t.Fatal("validateSpawnPaneEnv accepted invalid environment name")
	}
	if err := validateSpawnPaneEnv(map[string]string{"GOOD": "bad\x00value"}); err == nil {
		t.Fatal("validateSpawnPaneEnv accepted NUL value")
	}
}

func TestPreflightWorktreeProject(t *testing.T) {
	t.Run("nil context", func(t *testing.T) {
		err := preflightWorktreeProject(nil, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "requires a command context") {
			t.Fatalf("preflightWorktreeProject() error = %v", err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := preflightWorktreeProject(ctx, t.TempDir())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("preflightWorktreeProject() error = %v, want context.Canceled", err)
		}
	})

	t.Run("missing directory", func(t *testing.T) {
		err := preflightWorktreeProject(t.Context(), filepath.Join(t.TempDir(), "missing"))
		if err == nil || !strings.Contains(err.Error(), "inspect project") {
			t.Fatalf("preflightWorktreeProject() error = %v", err)
		}
	})

	t.Run("project path is a file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "project-file")
		if err := os.WriteFile(path, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("write project fixture: %v", err)
		}
		err := preflightWorktreeProject(t.Context(), path)
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("preflightWorktreeProject() error = %v", err)
		}
	})

	t.Run("ordinary directory", func(t *testing.T) {
		err := preflightWorktreeProject(t.Context(), t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "not an initialized Git working tree") {
			t.Fatalf("preflightWorktreeProject() error = %v", err)
		}
	})

	t.Run("repository without commit", func(t *testing.T) {
		repo := t.TempDir()
		cmd := exec.CommandContext(t.Context(), "git", "init", "-q", repo)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git init unavailable: %v: %s", err, output)
		}
		err := preflightWorktreeProject(t.Context(), repo)
		if err == nil || !strings.Contains(err.Error(), "no valid HEAD commit") {
			t.Fatalf("preflightWorktreeProject() error = %v", err)
		}
		if !strings.Contains(err.Error(), "fatal:") && !strings.Contains(err.Error(), "exit status") {
			t.Fatalf("preflightWorktreeProject() error omits Git diagnostic: %v", err)
		}
	})

	t.Run("repository with commit", func(t *testing.T) {
		repo := setupCLIWorktreeGitRepo(t)
		if err := preflightWorktreeProject(t.Context(), repo); err != nil {
			t.Fatalf("preflightWorktreeProject() error = %v", err)
		}
	})

	t.Run("repository with detached head", func(t *testing.T) {
		repo := setupCLIWorktreeGitRepo(t)
		cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "checkout", "--detach", "--quiet")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("detach fixture HEAD: %v: %s", err, output)
		}
		if err := preflightWorktreeProject(t.Context(), repo); err != nil {
			t.Fatalf("preflightWorktreeProject() detached HEAD error = %v", err)
		}
	})

	t.Run("cancellation during git command remains classifiable", func(t *testing.T) {
		fakeBin := t.TempDir()
		fakeGit := filepath.Join(fakeBin, "git")
		if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
			t.Fatalf("write blocking git fixture: %v", err)
		}
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := preflightWorktreeProject(ctx, t.TempDir())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("preflightWorktreeProject() error = %v, want context deadline", err)
		}
	})
}

func TestValidateSpawnWorktreeOptions(t *testing.T) {
	if err := validateSpawnWorktreeOptions(SpawnOptions{WorktreeName: "shared", Agents: []FlatAgent{{}, {}}}); err != nil {
		t.Fatalf("disabled worktrees should ignore worktree name: %v", err)
	}
	if err := validateSpawnWorktreeOptions(SpawnOptions{UseWorktrees: true, WorktreeName: "isolated", Agents: []FlatAgent{{}}}); err != nil {
		t.Fatalf("single-agent worktree name rejected: %v", err)
	}
	err := validateSpawnWorktreeOptions(SpawnOptions{UseWorktrees: true, WorktreeName: "shared", Agents: []FlatAgent{{}, {}}})
	if err == nil || !strings.Contains(err.Error(), "only valid for single-agent spawns") {
		t.Fatalf("multi-agent worktree name error = %v", err)
	}
}

func TestSpawnSafetySessionExistsErrorIsStable(t *testing.T) {
	want := "session 'race-session' already exists (--safety mode prevents reuse; use 'ntm kill race-session' first)"
	if got := spawnSafetySessionExistsError("race-session").Error(); got != want {
		t.Fatalf("spawn safety error = %q, want %q", got, want)
	}
}

func TestPreflightSpawnAssignmentFailsClosedInAdmissionOrder(t *testing.T) {
	policyErr := errors.New("policy unavailable")
	planErr := fmt.Errorf("%w: synthetic planner failure", bv.ErrActionablePlanUnverified)

	for _, test := range []struct {
		name       string
		policyErr  error
		planErr    error
		wantCalls  []string
		wantErr    error
		wantPhrase string
	}{
		{
			name:       "policy failure stops before planning",
			policyErr:  policyErr,
			wantCalls:  []string{"policy"},
			wantErr:    policyErr,
			wantPhrase: "policy preflight",
		},
		{
			name:       "unverified plan is terminal",
			planErr:    planErr,
			wantCalls:  []string{"policy", "actionable"},
			wantErr:    bv.ErrActionablePlanUnverified,
			wantPhrase: "could not be verified",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			admission, err := preflightSpawnAssignment(
				t.Context(),
				filepath.Join(t.TempDir(), "project"),
				time.Second,
				spawnAssignmentPreflightDependencies{
					configurePolicy: func(string) error {
						calls = append(calls, "policy")
						return test.policyErr
					},
					fetchActionable: func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
						calls = append(calls, "actionable")
						return nil, test.planErr
					},
				},
			)
			if admission != nil || !errors.Is(err, test.wantErr) || !strings.Contains(err.Error(), test.wantPhrase) {
				t.Fatalf("admission=%+v err=%v, want wrapped %v containing %q", admission, err, test.wantErr, test.wantPhrase)
			}
			if got := strings.Join(calls, ","); got != strings.Join(test.wantCalls, ",") {
				t.Fatalf("preflight calls=%q, want %q", got, strings.Join(test.wantCalls, ","))
			}
		})
	}
}

func TestPreflightSpawnAssignmentAcceptsVerifiedEmptyPlan(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project", "..", "project")
	var calls []string
	admission, err := preflightSpawnAssignment(
		t.Context(),
		projectDir,
		time.Second,
		spawnAssignmentPreflightDependencies{
			configurePolicy: func(gotProject string) error {
				calls = append(calls, "policy:"+gotProject)
				return nil
			},
			fetchActionable: func(_ context.Context, gotProject string, limit int) ([]bv.TriageRecommendation, error) {
				calls = append(calls, fmt.Sprintf("actionable:%s:%d", gotProject, limit))
				return nil, nil
			},
		},
	)
	cleanProject := filepath.Clean(projectDir)
	if err != nil || admission == nil {
		t.Fatalf("verified empty preflight admission=%+v err=%v", admission, err)
	}
	if admission.projectDir != cleanProject || admission.actionable == nil || len(admission.actionable) != 0 {
		t.Fatalf("verified empty admission=%+v, want project=%q and non-nil empty actionable set", admission, cleanProject)
	}
	wantCalls := []string{"policy:" + cleanProject, "actionable:" + cleanProject + ":100"}
	if got := strings.Join(calls, ","); got != strings.Join(wantCalls, ",") {
		t.Fatalf("preflight calls=%q, want %q", got, strings.Join(wantCalls, ","))
	}
}

func TestPreflightSpawnAssignmentTimeoutRemainsClassifiable(t *testing.T) {
	var calls []string
	admission, err := preflightSpawnAssignment(
		t.Context(),
		t.TempDir(),
		10*time.Millisecond,
		spawnAssignmentPreflightDependencies{
			configurePolicy: func(string) error {
				calls = append(calls, "policy")
				return nil
			},
			fetchActionable: func(ctx context.Context, _ string, _ int) ([]bv.TriageRecommendation, error) {
				calls = append(calls, "actionable")
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	)
	if admission != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed preflight admission=%+v err=%v, want deadline exceeded", admission, err)
	}
	if got := strings.Join(calls, ","); got != "policy,actionable" {
		t.Fatalf("timed preflight calls=%q, want policy,actionable", got)
	}
}

func TestSpawnAssignCommandOptionsReusesVerifiedAdmissionSnapshot(t *testing.T) {
	recommendation := bv.TriageRecommendation{
		ID:      "ntm-verified",
		Title:   "verified work",
		Labels:  []string{"backend"},
		Reasons: []string{"authorized by plan"},
	}
	spawnOpts := SpawnOptions{
		AssignStrategy:  "quality",
		AssignLimit:     2,
		AssignAgentType: "codex",
		assignAdmission: &spawnAssignmentAdmission{
			projectDir: "/tmp/verified-project",
			actionable: []bv.TriageRecommendation{recommendation},
		},
	}

	assignOpts := spawnAssignCommandOptions("verified-session", spawnOpts, true, false, 7*time.Second)
	if assignOpts.ProjectDir != spawnOpts.assignAdmission.projectDir ||
		assignOpts.policyProject != spawnOpts.assignAdmission.projectDir ||
		!assignOpts.actionablePreflightVerified || len(assignOpts.verifiedActionable) != 1 {
		t.Fatalf("spawn assignment options did not carry verified admission: %+v", assignOpts)
	}
	if assignOpts.Session != "verified-session" || assignOpts.Strategy != "quality" ||
		assignOpts.Limit != 2 || assignOpts.AgentTypeFilter != "codex" ||
		!assignOpts.Verbose || assignOpts.Quiet || assignOpts.Timeout != 7*time.Second {
		t.Fatalf("spawn assignment options lost command settings: %+v", assignOpts)
	}

	spawnOpts.assignAdmission.actionable[0].Labels[0] = "mutated"
	if assignOpts.verifiedActionable[0].Labels[0] != "backend" {
		t.Fatalf("verified admission was aliased across phases: %+v", assignOpts.verifiedActionable[0])
	}
}

func TestMonitorProcessPattern_MatchesExactSessionOnly(t *testing.T) {
	pattern := regexp.MustCompile(resilience.MonitorProcessPatternForExecutable("/usr/local/bin/ntm-dev", "proj"))

	if !pattern.MatchString("/usr/local/bin/ntm-dev internal-monitor proj") {
		t.Fatal("expected exact executable/session monitor command to match")
	}
	if pattern.MatchString("/usr/local/bin/ntm-dev internal-monitor proj2") {
		t.Fatal("expected prefix-sharing session name not to match")
	}
	if pattern.MatchString("/usr/local/bin/ntm-dev2 internal-monitor proj") {
		t.Fatal("expected prefix-sharing executable name not to match")
	}
}

func TestResolveSpawnPanePrompt_UsesZeroBasedAgentOrder(t *testing.T) {

	opts := SpawnOptions{
		Prompt:         "global fallback",
		MarchingOrders: map[int]string{0: "first agent task", 1: "second agent task"},
		DefaultPrompts: config.PromptsConfig{
			CCDefault:  "claude default",
			CodDefault: "codex default",
		},
	}

	firstPrompt, err := resolveSpawnPanePrompt(opts, AgentTypeClaude, 0)
	if err != nil {
		t.Fatalf("resolveSpawnPanePrompt(first) error = %v", err)
	}
	if firstPrompt != "claude default\n\nfirst agent task" {
		t.Fatalf("first prompt = %q, want %q", firstPrompt, "claude default\n\nfirst agent task")
	}

	secondPrompt, err := resolveSpawnPanePrompt(opts, AgentTypeCodex, 1)
	if err != nil {
		t.Fatalf("resolveSpawnPanePrompt(second) error = %v", err)
	}
	if secondPrompt != "codex default\n\nsecond agent task" {
		t.Fatalf("second prompt = %q, want %q", secondPrompt, "codex default\n\nsecond agent task")
	}
}

func TestResolveSpawnPanePromptReportsConfiguredDefaultFileFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-default-prompt.md")
	opts := SpawnOptions{
		Prompt: "explicit prompt must remain available for diagnostics",
		DefaultPrompts: config.PromptsConfig{
			CCDefaultFile: missing,
		},
	}

	prompt, err := resolveSpawnPanePrompt(opts, AgentTypeClaude, 0)
	if err == nil || !strings.Contains(err.Error(), "reading prompts.cc_default_file") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("default prompt resolution error = %v", err)
	}
	if prompt != opts.Prompt {
		t.Fatalf("prompt returned with resolution error = %q, want explicit prompt %q", prompt, opts.Prompt)
	}
}

func TestSpawnPromptSequenceCassAdvisoryPolicy(t *testing.T) {
	tests := []struct {
		name     string
		steps    []spawnPromptStep
		cassOnly bool
	}{
		{name: "empty is not a failed injection", steps: nil, cassOnly: false},
		{name: "cass only", steps: []spawnPromptStep{{Kind: "cass_context", Message: "history"}}, cassOnly: true},
		{name: "recovery is requested work", steps: []spawnPromptStep{{Kind: "recovery_context", Message: "recover"}}, cassOnly: false},
		{name: "user prompt is requested work", steps: []spawnPromptStep{{Kind: "user_prompt", Message: "build"}}, cassOnly: false},
		{name: "combined cass and user remains fatal", steps: []spawnPromptStep{{Kind: "cass_context"}, {Kind: "user_prompt"}}, cassOnly: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := spawnPromptSequenceIsCassOnly(tt.steps); got != tt.cassOnly {
				t.Fatalf("spawnPromptSequenceIsCassOnly() = %v, want %v", got, tt.cassOnly)
			}
		})
	}
}

func TestSpawnHasPromptDelivery_RecognizesDefaultAndMarchingPrompts(t *testing.T) {

	if !spawnHasPromptDelivery(SpawnOptions{
		Agents: []FlatAgent{{Type: AgentTypeClaude, Index: 1}},
		DefaultPrompts: config.PromptsConfig{
			CCDefault: "default instructions",
		},
	}) {
		t.Fatal("expected default prompt to count as prompt delivery")
	}

	if !spawnHasPromptDelivery(SpawnOptions{
		Agents:         []FlatAgent{{Type: AgentTypeClaude, Index: 1}, {Type: AgentTypeGemini, Index: 1}},
		MarchingOrders: map[int]string{1: "second agent only"},
	}) {
		t.Fatal("expected marching orders to count as prompt delivery")
	}

	if spawnHasPromptDelivery(SpawnOptions{
		Agents: []FlatAgent{{Type: AgentTypeClaude, Index: 1}},
	}) {
		t.Fatal("expected no prompt delivery when no prompt sources are configured")
	}
}

func TestValidateSpawnAgentTypes(t *testing.T) {
	builtins := []FlatAgent{
		{Type: AgentTypeClaude},
		{Type: AgentTypeCodex},
		{Type: AgentTypeGemini},
		{Type: AgentTypeAntigravity},
		{Type: AgentTypeGrok},
		{Type: AgentTypeOllama},
		{Type: AgentTypeCursor},
		{Type: AgentTypeWindsurf},
		{Type: AgentTypeAider},
		{Type: AgentTypeOpencode},
	}
	if err := validateSpawnAgentTypes(builtins, nil, nil); err != nil {
		t.Fatalf("built-in agent validation error = %v", err)
	}

	pluginMap := map[string]plugins.AgentPlugin{
		"custom": {Name: "custom", Command: "custom-agent"},
	}
	if err := validateSpawnAgentTypes([]FlatAgent{{Type: AgentType("custom")}}, pluginMap, nil); err != nil {
		t.Fatalf("plugin agent validation error = %v", err)
	}

	err := validateSpawnAgentTypes([]FlatAgent{{Type: AgentType("mystery")}}, pluginMap, nil)
	if err == nil || !strings.Contains(err.Error(), `unknown agent type "mystery"`) {
		t.Fatalf("unknown agent validation error = %v", err)
	}
}

// GH#251 phase 2: Grok Build spawn now supports prompt delivery, CASS
// context, marching orders, --assign, and --auto-restart. The only remaining
// deliberate refusal is persona injection — the Grok Build CLI has no
// system-prompt flag or env var.
func TestValidateGrokSpawnAcceptsPhaseTwoSurfacesRejectsPersona(t *testing.T) {
	base := SpawnOptions{Agents: []FlatAgent{{Type: AgentTypeGrok, Index: 1}}}
	effectiveConfig := config.Default()

	accepted := []SpawnOptions{
		base,
		{Agents: base.Agents, NoCassContext: true, CassContextQuery: "ignored"},
		{Agents: base.Agents, Prompt: "work"},
		{Agents: base.Agents, InitPrompt: "work"},
		{Agents: base.Agents, CassContextQuery: "history"},
		{Agents: base.Agents, MarchingOrders: map[int]string{0: "work"}},
		{Agents: base.Agents, Assign: true},
		{Agents: base.Agents, AutoRestart: true},
	}
	for _, opts := range accepted {
		if err := validateGrokPhaseOneSpawn(opts, effectiveConfig); err != nil {
			t.Fatalf("phase-2 Grok spawn options unexpectedly rejected: %+v: %v", opts, err)
		}
	}

	rejected := []SpawnOptions{
		{Agents: []FlatAgent{{Type: AgentTypeGrok, Index: 1, Persona: &persona.Persona{Name: "reviewer"}}}},
		{
			Agents:     []FlatAgent{{Type: AgentTypeGrok, Index: 1, Model: "reviewer"}},
			PersonaMap: map[string]*persona.Persona{"reviewer": {Name: "reviewer"}},
		},
	}
	for _, opts := range rejected {
		if err := validateGrokPhaseOneSpawn(opts, effectiveConfig); err == nil {
			t.Fatalf("Grok persona injection unexpectedly accepted: %+v", opts)
		}
	}

	effectiveConfig.Resilience.AutoRestart = true
	if err := validateGrokPhaseOneSpawn(base, effectiveConfig); err != nil {
		t.Fatalf("config-enabled automatic restart unexpectedly rejected for Grok: %v", err)
	}
}

// GH#251 phase 2: Grok panes are now automated prompt-delivery targets and
// receive the same spawn prompt sequence as claude/codex.
func TestGrokSpawnDeliversAmbientContextPrompts(t *testing.T) {
	grokOnly := []FlatAgent{{Type: AgentTypeGrok, Index: 1}}
	if !spawnHasAutomatedPromptDeliveryTarget(grokOnly) {
		t.Fatal("Grok-only spawn should have an automated prompt-delivery target")
	}
	steps := buildSpawnPromptSequenceForAgent(
		AgentTypeGrok,
		"ambient CASS context",
		"ambient recovery context",
		"defensive user prompt",
		time.Second,
	)
	if len(steps) != 2 || steps[0].Kind != "recovery_context" || steps[1].Kind != "user_prompt" {
		t.Fatalf("Grok prompt steps = %+v, want recovery plus CASS-enriched user prompt", steps)
	}

	mixed := append([]FlatAgent{{Type: AgentTypeClaude, Index: 1}}, grokOnly...)
	if !spawnHasAutomatedPromptDeliveryTarget(mixed) {
		t.Fatal("mixed spawn should retain automated context for supported agents")
	}
	claudeSteps := buildSpawnPromptSequenceForAgent(AgentTypeClaude, "cass", "recovery", "prompt", time.Second)
	if len(claudeSteps) != 2 || claudeSteps[0].Kind != "recovery_context" || claudeSteps[1].Kind != "user_prompt" {
		t.Fatalf("supported-agent prompt steps = %+v, want recovery plus CASS-enriched user prompt", claudeSteps)
	}
}

// GH#251 phase 2: Grok Build add accepts --prompt and CASS context; only
// persona injection remains refused (no system-prompt mechanism in the CLI).
func TestValidateGrokAddAcceptsPhaseTwoSurfacesRejectsPersona(t *testing.T) {
	base := AddOptions{Agents: AgentSpecs{{Type: AgentTypeGrok, Count: 1}}}

	accepted := []AddOptions{
		base,
		{Agents: base.Agents, NoCassContext: true, CassContextQuery: "ignored"},
		{Agents: base.Agents, Prompt: "work"},
		{Agents: base.Agents, CassContextQuery: "history"},
	}
	for _, opts := range accepted {
		if err := validateGrokPhaseOneAdd(opts); err != nil {
			t.Fatalf("phase-2 Grok add options unexpectedly rejected: %+v: %v", opts, err)
		}
	}

	personaOpts := AddOptions{
		Agents:     AgentSpecs{{Type: AgentTypeGrok, Count: 1, Model: "reviewer"}},
		PersonaMap: map[string]*persona.Persona{"reviewer": {Name: "reviewer"}},
	}
	if err := validateGrokPhaseOneAdd(personaOpts); err == nil {
		t.Fatalf("Grok persona injection unexpectedly accepted: %+v", personaOpts)
	}
}

func TestNormalizeSpawnOptionsGrok(t *testing.T) {
	opts := SpawnOptions{GrokCount: 2}
	normalizeSpawnOptions(&opts)
	if opts.GrokCount != 2 || len(opts.Agents) != 2 {
		t.Fatalf("normalized Grok spawn = count:%d agents:%+v", opts.GrokCount, opts.Agents)
	}
	for i, spec := range opts.Agents {
		if spec.Type != AgentTypeGrok || spec.Index != i+1 {
			t.Fatalf("normalized Grok agent[%d] = %+v", i, spec)
		}
	}
	if newSpawnCmd().Flags().Lookup("grok") == nil {
		t.Fatal("spawn command omits --grok")
	}
	if newAddCmd().Flags().Lookup("grok") == nil {
		t.Fatal("add command omits --grok")
	}
}

func TestValidateSpawnPaneCapacity(t *testing.T) {
	tests := []struct {
		name       string
		paneCount  int
		startIdx   int
		agentCount int
		wantErr    string
	}{
		{name: "exact agent panes", paneCount: 2, agentCount: 2},
		{name: "user pane plus exact agent panes", paneCount: 3, startIdx: 1, agentCount: 2},
		{name: "extra panes", paneCount: 4, startIdx: 1, agentCount: 2},
		{name: "one agent pane missing", paneCount: 2, startIdx: 1, agentCount: 2, wantErr: "requires 2 agent pane(s), but only 1 are available"},
		{name: "reserved offset exceeds topology", paneCount: 0, startIdx: 1, agentCount: 1, wantErr: "requires 1 agent pane(s), but only 0 are available"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panes := make([]tmux.Pane, tt.paneCount)
			err := validateSpawnPaneCapacity(panes, tt.startIdx, tt.agentCount)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSpawnPaneCapacity() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateSpawnPaneCapacity() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateSpawnPaneBaselinesPreflightsCompleteAssignment(t *testing.T) {
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1},
		{Type: AgentTypeGrok, Index: 1},
	}

	// #291: an existing session pane that is already running a process (an
	// agent CLI, `cat`, an editor) must refuse a launch for EVERY agent type,
	// not only Grok; send-keys would otherwise type the launch line into it.
	t.Run("occupied first target refuses the batch for a non-Grok agent", func(t *testing.T) {
		panes := []tmux.Pane{
			{ID: "%occupied", Index: 0, Command: "cat"},
			{ID: "%idle", Index: 1, Command: "zsh"},
		}
		err := validateSpawnPaneBaselines(context.Background(), "ntm-unit-no-such-session", panes, 0, agents)
		if err == nil {
			t.Fatal("validateSpawnPaneBaselines() error = nil, want occupied Claude pane rejected")
		}
		if !errors.Is(err, tmux.ErrPaneOccupied) {
			t.Fatalf("validateSpawnPaneBaselines() error = %v, want wrapped tmux.ErrPaneOccupied", err)
		}
		for _, want := range []string{"cc agent 1", "%occupied", `"cat"`, "non-shell", "ntm add"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("validateSpawnPaneBaselines() error = %q, want %q", err, want)
			}
		}
	})

	t.Run("live agent in a reused pane is refused", func(t *testing.T) {
		panes := []tmux.Pane{
			{ID: "%live", Index: 0, Command: "claude"},
			{ID: "%idle", Index: 1, Command: "zsh"},
		}
		err := validateSpawnPaneBaselines(context.Background(), "ntm-unit-no-such-session", panes, 0, []FlatAgent{{Type: AgentTypeCodex, Index: 1}})
		if err == nil || !errors.Is(err, tmux.ErrPaneOccupied) {
			t.Fatalf("validateSpawnPaneBaselines() error = %v, want occupied-pane rejection", err)
		}
	})

	t.Run("late occupied Grok target rejects mixed batch", func(t *testing.T) {
		panes := []tmux.Pane{
			{ID: "%idle", Index: 0, Command: "zsh"},
			{ID: "%occupied", Index: 1, Command: "sleep"},
		}
		err := validateSpawnPaneBaselines(context.Background(), "ntm-unit-no-such-session", panes, 0, agents)
		if err == nil {
			t.Fatal("validateSpawnPaneBaselines() error = nil")
		}
		for _, want := range []string{"grok agent 1", "%occupied", "sleep", "non-shell"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("validateSpawnPaneBaselines() error = %q, want %q", err, want)
			}
		}
	})

	t.Run("user pane offset reaches assigned shells", func(t *testing.T) {
		panes := []tmux.Pane{
			{ID: "%user", Index: 0, Command: "vim"}, // the reserved user pane may be busy
			{ID: "%claude", Index: 1, Command: "bash"},
			{ID: "%grok", Index: 2, Command: "fish"},
		}
		if err := validateSpawnPaneBaselines(context.Background(), "ntm-unit-no-such-session", panes, 1, agents); err != nil {
			t.Fatalf("validateSpawnPaneBaselines() error = %v", err)
		}
	})

	t.Run("empty observation is left to the post-launch stability wait", func(t *testing.T) {
		panes := []tmux.Pane{{ID: "%fresh", Index: 0}}
		if err := validateSpawnPaneBaselines(context.Background(), "ntm-unit-no-such-session", panes, 0, agents[:1]); err != nil {
			t.Fatalf("validateSpawnPaneBaselines() error = %v", err)
		}
	})

	t.Run("missing assigned pane fails closed", func(t *testing.T) {
		panes := []tmux.Pane{{ID: "%claude", Index: 0, Command: "zsh"}}
		err := validateSpawnPaneBaselines(context.Background(), "ntm-unit-no-such-session", panes, 0, agents)
		if err == nil || !strings.Contains(err.Error(), "no assigned pane for grok agent 1 at offset 1") {
			t.Fatalf("validateSpawnPaneBaselines() error = %v", err)
		}
	})
}

func TestSpawnUnknownAgentTextReturnsErrorWithoutWarning(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	stdout, err := captureStdout(t, func() error {
		return spawnSessionLogicContext(t.Context(), SpawnOptions{
			Session: "spawn-unknown-text",
			Agents:  []FlatAgent{{Type: AgentType("mystery"), Index: 1}},
		})
	})
	if err == nil || !strings.Contains(err.Error(), `unknown agent type "mystery"`) {
		t.Fatalf("spawn error = %v, want unknown agent failure", err)
	}
	if errors.Is(err, errJSONFailure) {
		t.Fatalf("text spawn error = %v, must not use JSON failure sentinel", err)
	}
	if stdout != "" {
		t.Fatalf("text spawn stdout = %q, want no warning or success output", stdout)
	}
}

func TestSpawnUnknownAgentJSONProcessHelper(t *testing.T) {
	if os.Getenv("NTM_SPAWN_UNKNOWN_JSON_HELPER") != "1" {
		return
	}
	jsonOutput = true
	err := spawnSessionLogicContext(t.Context(), SpawnOptions{
		Session: "spawn-unknown-json",
		Agents:  []FlatAgent{{Type: AgentType("mystery"), Index: 1}},
	})
	os.Exit(ExitCode(err))
}

func TestSpawnUnknownAgentJSONProcessContract(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSpawnUnknownAgentJSONProcessHelper$")
	cmd.Env = append(os.Environ(), "NTM_SPAWN_UNKNOWN_JSON_HELPER=1")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("unknown-agent process error = %v, want exit 1; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "" {
		t.Fatalf("unknown-agent process stderr = %q, want empty", got)
	}

	var envelope struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode single spawn failure envelope: %v; stdout=%q", err, stdout.String())
	}
	if envelope.Success || !strings.Contains(envelope.Error, `unknown agent type "mystery"`) {
		t.Fatalf("unknown-agent failure envelope = %+v", envelope)
	}
	if strings.Contains(stdout.String(), "Warning") || strings.Contains(stdout.String(), "⚠") {
		t.Fatalf("JSON stdout leaked human warning: %q", stdout.String())
	}
}

func TestRunSpawnAssignmentTextContextFailuresAreTerminal(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	newSuccessfulOps := func() spawnAssignmentOps {
		ops := defaultSpawnAssignmentOps()
		ops.waitForReady = func(context.Context, string, time.Duration, time.Duration, spawnSessionObserver) (int, error) {
			return 2, nil
		}
		ops.assign = func(context.Context, string, SpawnOptions) (*AssignOutputEnhanced, error) {
			return &AssignOutputEnhanced{Strategy: "balanced"}, nil
		}
		return ops
	}

	t.Run("ready failure stops before assignment", func(t *testing.T) {
		ops := newSuccessfulOps()
		assignCalled := false
		ops.waitForReady = func(context.Context, string, time.Duration, time.Duration, spawnSessionObserver) (int, error) {
			return 1, errors.New("readiness unavailable")
		}
		ops.assign = func(context.Context, string, SpawnOptions) (*AssignOutputEnhanced, error) {
			assignCalled = true
			return &AssignOutputEnhanced{}, nil
		}
		stdout, err := captureStdout(t, func() error {
			return runSpawnAssignmentTextContext(t.Context(), "demo", SpawnOptions{}, nil, nil, ops)
		})
		if err == nil || !strings.Contains(err.Error(), "ready wait failed: readiness unavailable") {
			t.Fatalf("ready failure = %v", err)
		}
		if assignCalled {
			t.Fatal("assignment ran after terminal readiness failure")
		}
		if !strings.Contains(stdout, "Waiting for agents to become ready") {
			t.Fatalf("ready failure output = %q", stdout)
		}
	})

	t.Run("init failure stops before assignment", func(t *testing.T) {
		ops := newSuccessfulOps()
		assignCalled := false
		ops.assign = func(context.Context, string, SpawnOptions) (*AssignOutputEnhanced, error) {
			assignCalled = true
			return &AssignOutputEnhanced{}, nil
		}
		stdout, err := captureStdout(t, func() error {
			return runSpawnAssignmentTextContext(
				t.Context(), "demo", SpawnOptions{InitPrompt: "initialize"},
				&scriptedSpawnObserver{}, &recordingSpawnDispatcher{}, ops,
			)
		})
		if err == nil || !strings.Contains(err.Error(), "init prompt failed: observe init prompt targets: no scripted observation") {
			t.Fatalf("init failure = %v", err)
		}
		if assignCalled {
			t.Fatal("assignment ran after terminal init failure")
		}
		if !strings.Contains(stdout, "Sending init prompt to ready agents") {
			t.Fatalf("init failure output = %q", stdout)
		}
	})

	t.Run("assignment failure returns error", func(t *testing.T) {
		ops := newSuccessfulOps()
		ops.assign = func(context.Context, string, SpawnOptions) (*AssignOutputEnhanced, error) {
			return nil, errors.New("planner unavailable")
		}
		stdout, err := captureStdout(t, func() error {
			return runSpawnAssignmentTextContext(t.Context(), "demo", SpawnOptions{}, nil, nil, ops)
		})
		if err == nil || !strings.Contains(err.Error(), "assignment failed: planner unavailable") {
			t.Fatalf("assignment failure = %v", err)
		}
		if !strings.Contains(stdout, "Assigning work to agents") {
			t.Fatalf("assignment failure output = %q", stdout)
		}
	})

	t.Run("cancellation remains classifiable", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		stdout, err := captureStdout(t, func() error {
			return runSpawnAssignmentTextContext(ctx, "demo", SpawnOptions{}, nil, nil, newSuccessfulOps())
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled assignment error = %v, want context cancellation", err)
		}
		if stdout != "" {
			t.Fatalf("canceled assignment stdout = %q, want no phase output", stdout)
		}
	})
}

func TestNewInternalMonitorCommand_ValidatesSessionAndExecutable(t *testing.T) {

	cmd, err := resilience.NewInternalMonitorCommand("proj")
	if err != nil {
		t.Fatalf("resilience.NewInternalMonitorCommand(valid) error = %v", err)
	}
	if !filepath.IsAbs(cmd.Path) {
		t.Fatalf("command path = %q, want absolute path", cmd.Path)
	}
	if got, want := cmd.Args, []string{cmd.Path, "internal-monitor", "proj"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("command args = %#v, want %#v", got, want)
	}

	if _, err := resilience.NewInternalMonitorCommand("bad:name"); err == nil {
		t.Fatal("expected invalid session name to be rejected")
	}
}

func TestWaitForSpawnSetupCompletion_Interrupted(t *testing.T) {

	setupDone := make(chan struct{})
	sigChan := make(chan os.Signal, 1)
	sigChan <- os.Interrupt

	err := waitForSpawnSetupCompletionContext(t.Context(), setupDone, sigChan, true)
	if err == nil {
		t.Fatal("expected interrupt error")
	}
	if !strings.Contains(err.Error(), "spawn interrupted") {
		t.Fatalf("error = %v, want interrupt context", err)
	}
}

func TestWaitForSpawnSetupCompletion_Completes(t *testing.T) {

	setupDone := make(chan struct{})
	close(setupDone)

	if err := waitForSpawnSetupCompletionContext(t.Context(), setupDone, make(chan os.Signal), true); err != nil {
		t.Fatalf("waitForSpawnSetupCompletionContext() error = %v, want nil", err)
	}
}

func TestSpawnSessionLogic(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir for projects
	tmpDir, err := os.MkdirTemp("", "ntm-test-projects")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize global cfg (unexported in cli package, but accessible here)
	// Save/Restore to prevent side effects
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true

	// Override templates to avoid dependency on actual agent binaries while
	// remaining compatible with explicit model overrides under test.
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCatCommandTemplate
	cfg.Agents.Gemini = testAgentCatCommandTemplate

	// Unique session name
	sessionName := fmt.Sprintf("ntm-test-spawn-%d", time.Now().UnixNano())

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Define agents
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "claude-3-5-sonnet-20241022"},
	}

	// Pre-create project directory to avoid interactive prompt
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Execute spawn
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

	// Validate session exists
	if !tmux.SessionExists(sessionName) {
		t.Errorf("session %s was not created", sessionName)
	}

	// Validate panes
	// Expected: 1 user pane + 1 claude pane = 2 panes
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}

	if len(panes) != 2 {
		t.Errorf("expected 2 panes, got %d", len(panes))
	}

	// Validate user pane and agent pane
	foundClaude := false
	claudePaneID := ""
	for _, p := range panes {
		if p.Type == tmux.AgentClaude {
			foundClaude = true
			claudePaneID = p.ID
			// Check title format: session__type_index_variant
			expectedTitle := fmt.Sprintf("%s__cc_1_claude-3-5-sonnet-20241022", sessionName)
			if p.Title != expectedTitle {
				t.Errorf("expected pane title %q, got %q", expectedTitle, p.Title)
			}
		}
	}

	if !foundClaude {
		t.Fatal("did not find Claude agent pane")
	}
	if err := tmux.SetPaneTitle(claudePaneID, "caam | title replaced by wrapper"); err != nil {
		t.Fatalf("rewrite spawned Claude title: %v", err)
	}
	panes, err = tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("get panes after title rewrite: %v", err)
	}
	foundClaude = false
	for _, p := range panes {
		if p.ID == claudePaneID && p.Type == tmux.AgentClaude {
			foundClaude = true
		}
	}
	if !foundClaude {
		t.Fatalf("spawned pane %s lost its durable Claude type after title rewrite: %+v", claudePaneID, panes)
	}

	// Verify project directory creation
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		t.Errorf("project directory %s was not created", projectDir)
	}
}

func TestAppendOllamaAgentSpecs(t *testing.T) {

	t.Run("no_agents_noop", func(t *testing.T) {
		var specs AgentSpecs
		model, err := appendOllamaAgentSpecs(&specs, 0, 0, "  codellama:latest  ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if model != "codellama:latest" {
			t.Fatalf("model=%q, want %q", model, "codellama:latest")
		}
		if len(specs) != 0 {
			t.Fatalf("specs len=%d, want 0", len(specs))
		}
	})

	t.Run("local_count_appends_ollama_spec", func(t *testing.T) {
		var specs AgentSpecs
		model, err := appendOllamaAgentSpecs(&specs, 2, 0, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if model != "codellama:latest" {
			t.Fatalf("model=%q, want %q", model, "codellama:latest")
		}
		if len(specs) != 1 {
			t.Fatalf("specs len=%d, want 1", len(specs))
		}
		if specs[0].Type != AgentTypeOllama || specs[0].Count != 2 || specs[0].Model != "codellama:latest" {
			t.Fatalf("spec=%+v, want type=%q count=2 model=%q", specs[0], AgentTypeOllama, "codellama:latest")
		}
	})

	t.Run("ollama_alias_appends_ollama_spec", func(t *testing.T) {
		var specs AgentSpecs
		model, err := appendOllamaAgentSpecs(&specs, 0, 3, "deepseek-coder:33b")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if model != "deepseek-coder:33b" {
			t.Fatalf("model=%q, want %q", model, "deepseek-coder:33b")
		}
		if len(specs) != 1 {
			t.Fatalf("specs len=%d, want 1", len(specs))
		}
		if specs[0].Type != AgentTypeOllama || specs[0].Count != 3 || specs[0].Model != "deepseek-coder:33b" {
			t.Fatalf("spec=%+v, want type=%q count=3 model=%q", specs[0], AgentTypeOllama, "deepseek-coder:33b")
		}
	})

	t.Run("cannot_use_local_and_ollama_together", func(t *testing.T) {
		var specs AgentSpecs
		if _, err := appendOllamaAgentSpecs(&specs, 1, 1, "codellama:latest"); err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("invalid_model_rejected", func(t *testing.T) {
		var specs AgentSpecs
		if _, err := appendOllamaAgentSpecs(&specs, 1, 0, "bad model!"); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestParseLocalFallbackProvider(t *testing.T) {

	testCases := []struct {
		name    string
		input   string
		want    AgentType
		wantErr bool
	}{
		{name: "default_empty", input: "", want: AgentTypeCodex},
		{name: "cod", input: "cod", want: AgentTypeCodex},
		{name: "codex", input: "codex", want: AgentTypeCodex},
		{name: "codex_cli", input: "codex_cli", want: AgentTypeCodex},
		{name: "codex_dash_cli", input: "codex-cli", want: AgentTypeCodex},
		{name: "openai_codex", input: "openai-codex", want: AgentTypeCodex},
		{name: "cc", input: "cc", want: AgentTypeClaude},
		{name: "claude", input: "claude", want: AgentTypeClaude},
		{name: "claude_code", input: "claude_code", want: AgentTypeClaude},
		{name: "claude_dash_code", input: "claude-code", want: AgentTypeClaude},
		{name: "gmi", input: "gmi", want: AgentTypeGemini},
		{name: "gemini", input: "gemini", want: AgentTypeGemini},
		{name: "gemini_cli", input: "gemini_cli", want: AgentTypeGemini},
		{name: "gemini_dash_cli", input: "gemini-cli", want: AgentTypeGemini},
		{name: "google_gemini", input: "google-gemini", want: AgentTypeGemini},
		{name: "mixed_case_spacing", input: "  CodEx-Cli  ", want: AgentTypeCodex},
		{name: "invalid", input: "ollama", wantErr: true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLocalFallbackProvider(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (provider=%q)", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("provider=%q => %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestHandleOllamaPreflightError_FallbackDisabled(t *testing.T) {

	opts := SpawnOptions{
		Agents: []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "codellama:latest"}},
	}
	expectedErr := fmt.Errorf("connect failed")
	applied, msg, err := handleOllamaPreflightError(&opts, expectedErr)
	if applied {
		t.Fatal("expected fallback not applied")
	}
	if msg != "" {
		t.Fatalf("msg=%q, want empty", msg)
	}
	if err == nil || !strings.Contains(err.Error(), "connect failed") {
		t.Fatalf("unexpected err=%v", err)
	}
}

func TestHandleOllamaPreflightError_FallbackEnabledReindexesAndRecounts(t *testing.T) {

	opts := SpawnOptions{
		Agents: []FlatAgent{
			{Type: AgentTypeClaude, Index: 1, Model: "opus"},
			{Type: AgentTypeOllama, Index: 1, Model: "codellama:latest"},
			{Type: AgentTypeCodex, Index: 1, Model: "o3"},
			{Type: AgentTypeOllama, Index: 2, Model: "deepseek-coder:6.7b"},
		},
		LocalHost:             "http://localhost:11434",
		LocalFallback:         true,
		LocalFallbackProvider: AgentTypeCodex,
		CCCount:               1,
		CodCount:              1,
		GmiCount:              0,
		CursorCount:           0,
		WindsurfCount:         0,
		AiderCount:            0,
	}

	applied, msg, err := handleOllamaPreflightError(&opts, fmt.Errorf("failed to connect to Ollama"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !applied {
		t.Fatal("expected fallback to be applied")
	}
	if !strings.Contains(msg, "falling back 2 local agent(s) to cod") {
		t.Fatalf("unexpected message: %q", msg)
	}
	if opts.LocalHost != "" {
		t.Fatalf("LocalHost=%q, want empty", opts.LocalHost)
	}

	if len(opts.Agents) != 4 {
		t.Fatalf("agents len=%d, want 4", len(opts.Agents))
	}
	if opts.Agents[1].Type != AgentTypeCodex || opts.Agents[1].Index != 1 || opts.Agents[1].Model != "" {
		t.Fatalf("agent[1]=%+v, want codex index=1 empty model", opts.Agents[1])
	}
	if opts.Agents[2].Type != AgentTypeCodex || opts.Agents[2].Index != 2 {
		t.Fatalf("agent[2]=%+v, want codex index=2", opts.Agents[2])
	}
	if opts.Agents[3].Type != AgentTypeCodex || opts.Agents[3].Index != 3 || opts.Agents[3].Model != "" {
		t.Fatalf("agent[3]=%+v, want codex index=3 empty model", opts.Agents[3])
	}

	if opts.CCCount != 1 || opts.CodCount != 3 || opts.GmiCount != 0 {
		t.Fatalf("counts cc=%d cod=%d gmi=%d, want 1/3/0", opts.CCCount, opts.CodCount, opts.GmiCount)
	}
}

func TestHandleOllamaPreflightError_NoOllamaAgentsStillFails(t *testing.T) {

	opts := SpawnOptions{
		Agents:                []FlatAgent{{Type: AgentTypeClaude, Index: 1, Model: "sonnet"}},
		LocalFallback:         true,
		LocalFallbackProvider: AgentTypeCodex,
	}

	applied, msg, err := handleOllamaPreflightError(&opts, fmt.Errorf("failed to connect to Ollama"))
	if applied {
		t.Fatal("expected fallback not applied")
	}
	if msg != "" {
		t.Fatalf("msg=%q, want empty", msg)
	}
	if err == nil || !strings.Contains(err.Error(), "failed to connect to Ollama") {
		t.Fatalf("unexpected err=%v", err)
	}
}

func withStdinInput(t *testing.T, input string, fn func()) {
	t.Helper()

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("failed to write stdin input: %v", err)
	}
	_ = w.Close()
	os.Stdin = r
	defer func() {
		os.Stdin = oldStdin
		_ = r.Close()
	}()

	fn()
}

func TestPreflightOllamaSpawn_ModelPresent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{
						"name":   "codellama:latest",
						"size":   0,
						"digest": "sha256:deadbeef",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldJSON := jsonOutput
	defer func() { jsonOutput = oldJSON }()
	jsonOutput = true

	host, err := preflightOllamaSpawnContext(t.Context(), SpawnOptions{
		Agents:     []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "codellama:latest"}},
		LocalHost:  server.URL,
		LocalModel: "codellama:latest",
	})
	if err != nil {
		t.Fatalf("preflightOllamaSpawn failed: %v", err)
	}
	if host != strings.TrimSuffix(server.URL, "/") {
		t.Fatalf("host=%q, want %q", host, strings.TrimSuffix(server.URL, "/"))
	}
}

func TestPreflightOllamaSpawn_MissingModel_JSONModeErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{
						"name": "llama3:latest",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldJSON := jsonOutput
	defer func() { jsonOutput = oldJSON }()
	jsonOutput = true

	_, err := preflightOllamaSpawnContext(t.Context(), SpawnOptions{
		Agents:    []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "codellama:latest"}},
		LocalHost: server.URL,
	})
	if err == nil {
		t.Fatal("expected missing-model error")
	}
	if !strings.Contains(err.Error(), "not found at") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflightOllamaSpawn_MissingModel_TextModePullsOnConfirm(t *testing.T) {
	var pullCalled atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{}})
		case "/api/pull":
			pullCalled.Store(true)
			flusher, _ := w.(http.Flusher)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "pulling"})
			flusher.Flush()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success"})
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldJSON := jsonOutput
	defer func() { jsonOutput = oldJSON }()
	jsonOutput = false

	withStdinInput(t, "y\n", func() {
		host, err := preflightOllamaSpawnContext(t.Context(), SpawnOptions{
			Agents:    []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "deepseek-coder:6.7b"}},
			LocalHost: server.URL,
		})
		if err != nil {
			t.Fatalf("preflightOllamaSpawn failed: %v", err)
		}
		if host != strings.TrimSuffix(server.URL, "/") {
			t.Fatalf("host=%q, want %q", host, strings.TrimSuffix(server.URL, "/"))
		}
	})

	if !pullCalled.Load() {
		t.Fatal("expected /api/pull to be called after confirmation")
	}
}

func TestPreflightOllamaSpawn_MissingModel_TextModeDecline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldJSON := jsonOutput
	defer func() { jsonOutput = oldJSON }()
	jsonOutput = false

	withStdinInput(t, "n\n", func() {
		_, err := preflightOllamaSpawnContext(t.Context(), SpawnOptions{
			Agents:    []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "deepseek-coder:6.7b"}},
			LocalHost: server.URL,
		})
		if err == nil {
			t.Fatal("expected decline error")
		}
		if !strings.Contains(err.Error(), "not found (try: ollama pull") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestSpawnSessionLogic_Ollama(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir for projects
	tmpDir, err := os.MkdirTemp("", "ntm-test-projects")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Mock Ollama server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{
						"name":        "codellama:latest",
						"size":        0,
						"digest":      "sha256:deadbeef",
						"modified_at": time.Now().UTC().Format(time.RFC3339),
						"details": map[string]any{
							"format": "gguf",
							"family": "llama",
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// Initialize global cfg (unexported in cli package, but accessible here)
	// Save/Restore to prevent side effects
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true

	// Override templates to avoid dependency on actual agent binaries while
	// remaining compatible with explicit model overrides under test.
	cfg.Agents.Ollama = testAgentCatCommandTemplate

	// Unique session name
	sessionName := fmt.Sprintf("ntm-test-spawn-ollama-%d", time.Now().UnixNano())

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Pre-create project directory to avoid interactive prompt
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	opts := SpawnOptions{
		Session:       sessionName,
		Agents:        []FlatAgent{{Type: AgentTypeOllama, Index: 1, Model: "codellama:latest"}},
		UserPane:      true,
		LocalHost:     server.URL,
		LocalModel:    "codellama:latest",
		CCCount:       0,
		CodCount:      0,
		GmiCount:      0,
		CursorCount:   0,
		WindsurfCount: 0,
		AiderCount:    0,
	}

	if err := spawnSessionLogicContext(t.Context(), opts); err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	if !tmux.SessionExists(sessionName) {
		t.Fatalf("session %s was not created", sessionName)
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes, got %d", len(panes))
	}

	foundOllama := false
	for _, p := range panes {
		if p.Type.String() != "ollama" {
			continue
		}
		foundOllama = true
		expectedTitle := fmt.Sprintf("%s__ollama_1_codellama:latest", sessionName)
		if p.Title != expectedTitle {
			t.Errorf("expected pane title %q, got %q", expectedTitle, p.Title)
		}
	}
	if !foundOllama {
		t.Fatal("did not find Ollama agent pane")
	}

	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		t.Fatalf("project directory %s was not created", projectDir)
	}
}

// bd-3f53: Tests for getMemoryContext and formatMemoryContext

func TestFormatMemoryContext_Nil(t *testing.T) {

	result := formatMemoryContext(nil)
	if result != "" {
		t.Errorf("formatMemoryContext(nil) = %q, want empty string", result)
	}
}

func TestFormatMemoryContext_EmptyResult(t *testing.T) {

	result := formatMemoryContext(&cm.CLIContextResponse{
		Success:         true,
		Task:            "test task",
		RelevantBullets: []cm.Rule{},
		AntiPatterns:    []cm.Rule{},
	})
	if result != "" {
		t.Errorf("formatMemoryContext(empty) = %q, want empty string", result)
	}
}

func TestFormatMemoryContext_RulesOnly(t *testing.T) {

	resp := &cm.CLIContextResponse{
		Success: true,
		Task:    "test task",
		RelevantBullets: []cm.Rule{
			{ID: "b-8f3a2c", Content: "Always use structured logging with log/slog", Category: "best-practice"},
			{ID: "b-4e1d7b", Content: "Database migrations must be idempotent", Category: "database"},
		},
		AntiPatterns: []cm.Rule{},
	}

	result := formatMemoryContext(resp)

	// Check header
	if !strings.Contains(result, "# Project Memory from Past Sessions") {
		t.Error("missing main header")
	}

	// Check rules section
	if !strings.Contains(result, "## Key Rules for This Project") {
		t.Error("missing Key Rules section header")
	}

	// Check rule formatting
	if !strings.Contains(result, "[b-8f3a2c] Always use structured logging with log/slog") {
		t.Error("missing first rule")
	}
	if !strings.Contains(result, "[b-4e1d7b] Database migrations must be idempotent") {
		t.Error("missing second rule")
	}

	// Should NOT have anti-patterns section
	if strings.Contains(result, "## Anti-Patterns to Avoid") {
		t.Error("should not have Anti-Patterns section when empty")
	}
}

func TestFormatMemoryContext_AntiPatternsOnly(t *testing.T) {

	resp := &cm.CLIContextResponse{
		Success:         true,
		Task:            "test task",
		RelevantBullets: []cm.Rule{},
		AntiPatterns: []cm.Rule{
			{ID: "b-7d3e8c", Content: "Don't add backwards-compatibility shims", Category: "anti-pattern"},
		},
	}

	result := formatMemoryContext(resp)

	// Check header
	if !strings.Contains(result, "# Project Memory from Past Sessions") {
		t.Error("missing main header")
	}

	// Should NOT have rules section
	if strings.Contains(result, "## Key Rules for This Project") {
		t.Error("should not have Key Rules section when empty")
	}

	// Check anti-patterns section
	if !strings.Contains(result, "## Anti-Patterns to Avoid") {
		t.Error("missing Anti-Patterns section header")
	}
	if !strings.Contains(result, "[b-7d3e8c] Don't add backwards-compatibility shims") {
		t.Error("missing anti-pattern")
	}
}

func TestFormatMemoryContext_BothSections(t *testing.T) {

	resp := &cm.CLIContextResponse{
		Success: true,
		Task:    "test task",
		RelevantBullets: []cm.Rule{
			{ID: "b-rule1", Content: "Use Go 1.25 features", Category: "best-practice"},
		},
		AntiPatterns: []cm.Rule{
			{ID: "b-anti1", Content: "Avoid using deprecated APIs", Category: "anti-pattern"},
		},
	}

	result := formatMemoryContext(resp)

	// Check both sections present
	if !strings.Contains(result, "## Key Rules for This Project") {
		t.Error("missing Key Rules section")
	}
	if !strings.Contains(result, "## Anti-Patterns to Avoid") {
		t.Error("missing Anti-Patterns section")
	}

	// Check both items present
	if !strings.Contains(result, "[b-rule1]") {
		t.Error("missing rule ID")
	}
	if !strings.Contains(result, "[b-anti1]") {
		t.Error("missing anti-pattern ID")
	}

	// Check order: rules should come before anti-patterns
	rulesIdx := strings.Index(result, "## Key Rules")
	antiIdx := strings.Index(result, "## Anti-Patterns")
	if rulesIdx > antiIdx {
		t.Error("Key Rules should appear before Anti-Patterns")
	}
}

func TestGetMemoryContext_ConfigDisabled(t *testing.T) {

	// Save and restore global config
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	// Create config with CM memories disabled
	cfg = config.Default()
	cfg.SessionRecovery.IncludeCMMemories = false

	result := getMemoryContext("test-project", "test task")
	if result != "" {
		t.Errorf("getMemoryContext with disabled config = %q, want empty string", result)
	}
}

func TestGetMemoryContext_NilConfig(t *testing.T) {

	// Save and restore global config
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	cfg = nil

	result := getMemoryContext("test-project", "test task")
	if result != "" {
		t.Errorf("getMemoryContext with nil config = %q, want empty string", result)
	}
}

func TestGetMemoryContext_EmptyTask(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	// Save and restore global config
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	cfg = config.Default()
	cfg.SessionRecovery.IncludeCMMemories = true

	// This test verifies the function handles empty task gracefully
	// Even if CM is not installed, it should return empty string without error
	result := getMemoryContext("test-project", "")

	// Result should be empty (CM likely not installed in test environment)
	// but the function should not panic
	_ = result // Just verify no panic
}

func TestLegacySpawnTotalAgentCount_IncludesModernTypes(t *testing.T) {

	opts := SpawnOptions{
		CCCount:       1,
		CodCount:      2,
		GmiCount:      3,
		GrokCount:     4,
		CursorCount:   5,
		WindsurfCount: 6,
		AiderCount:    7,
		OllamaCount:   8,
	}

	if got := legacySpawnTotalAgentCount(opts); got != 36 {
		t.Fatalf("legacySpawnTotalAgentCount() = %d, want 36", got)
	}
}

func TestSpawnHookCountEnv_IncludesGrok(t *testing.T) {

	env := spawnHookCountEnv(7, SpawnOptions{GrokCount: 2, OllamaCount: 2, CursorCount: 3})
	if env["NTM_AGENT_COUNT_GROK"] != "2" {
		t.Fatalf("NTM_AGENT_COUNT_GROK = %q, want 2", env["NTM_AGENT_COUNT_GROK"])
	}
	if env["NTM_AGENT_COUNT_OLLAMA"] != "2" {
		t.Fatalf("NTM_AGENT_COUNT_OLLAMA = %q, want 2", env["NTM_AGENT_COUNT_OLLAMA"])
	}
	if env["NTM_AGENT_COUNT_TOTAL"] != "7" {
		t.Fatalf("NTM_AGENT_COUNT_TOTAL = %q, want 7", env["NTM_AGENT_COUNT_TOTAL"])
	}
}

func TestSpawnSessionCreatedEventFields_IncludeModernTypes(t *testing.T) {

	fields := spawnSessionCreatedEventFields(SpawnOptions{
		RecipeName:    "default",
		CCCount:       1,
		GrokCount:     2,
		CursorCount:   3,
		WindsurfCount: 4,
		AiderCount:    5,
		OllamaCount:   6,
	}, "/tmp/project")

	if fields["agent_count"] != "21" {
		t.Fatalf("agent_count = %q, want 21", fields["agent_count"])
	}
	if fields["agent_grok"] != "2" || fields["agent_cursor"] != "3" || fields["agent_windsurf"] != "4" || fields["agent_aider"] != "5" || fields["agent_ollama"] != "6" {
		t.Fatalf("spawnSessionCreatedEventFields() missing modern counts: %+v", fields)
	}
}

func TestNormalizeSpawnOptions_ExpandsLegacyCountsIncludingOllama(t *testing.T) {

	opts := SpawnOptions{
		CursorCount: 2,
		OllamaCount: 1,
	}

	normalizeSpawnOptions(&opts)

	if len(opts.Agents) != 3 {
		t.Fatalf("len(opts.Agents) = %d, want 3", len(opts.Agents))
	}
	if opts.CursorCount != 2 {
		t.Fatalf("CursorCount = %d, want 2", opts.CursorCount)
	}
	if opts.OllamaCount != 1 {
		t.Fatalf("OllamaCount = %d, want 1", opts.OllamaCount)
	}
	if opts.Agents[0].Type != AgentTypeCursor || opts.Agents[0].Index != 1 {
		t.Fatalf("agent[0] = %+v, want cursor_1", opts.Agents[0])
	}
	if opts.Agents[1].Type != AgentTypeCursor || opts.Agents[1].Index != 2 {
		t.Fatalf("agent[1] = %+v, want cursor_2", opts.Agents[1])
	}
	if opts.Agents[2].Type != AgentTypeOllama || opts.Agents[2].Index != 1 {
		t.Fatalf("agent[2] = %+v, want ollama_1", opts.Agents[2])
	}
}

func TestNormalizeSpawnOptions_RecomputesModernCountsFromAgents(t *testing.T) {

	opts := SpawnOptions{
		Agents: []FlatAgent{
			{Type: AgentTypeCursor, Index: 1},
			{Type: AgentTypeWindsurf, Index: 1},
			{Type: AgentTypeOllama, Index: 1},
			{Type: AgentTypeOllama, Index: 2},
		},
	}

	normalizeSpawnOptions(&opts)

	if opts.CCCount != 0 || opts.CodCount != 0 || opts.GmiCount != 0 {
		t.Fatalf("legacy cloud counts changed unexpectedly: cc=%d cod=%d gmi=%d", opts.CCCount, opts.CodCount, opts.GmiCount)
	}
	if opts.CursorCount != 1 {
		t.Fatalf("CursorCount = %d, want 1", opts.CursorCount)
	}
	if opts.WindsurfCount != 1 {
		t.Fatalf("WindsurfCount = %d, want 1", opts.WindsurfCount)
	}
	if opts.OllamaCount != 2 {
		t.Fatalf("OllamaCount = %d, want 2", opts.OllamaCount)
	}
}

func TestExpandProfileAgents_PersonaSetDrivesOrderAndType(t *testing.T) {
	// architect=claude, developer=codex, auditor=codex — order and per-persona
	// agent_type must be preserved, with the persona attached to each agent.
	profiles := []*persona.Persona{
		{Name: "architect", AgentType: "claude", Model: "opus"},
		{Name: "developer", AgentType: "codex", Model: "gpt-5.5"},
		{Name: "auditor", AgentType: "codex"},
	}

	agents, err := expandProfileAgents(profiles, nil)
	if err != nil {
		t.Fatalf("expandProfileAgents: unexpected error: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("got %d agents, want 3", len(agents))
	}

	wantType := []AgentType{AgentTypeClaude, AgentTypeCodex, AgentTypeCodex}
	wantName := []string{"architect", "developer", "auditor"}
	wantIndex := []int{1, 1, 2} // per-type 1-based index
	for i, a := range agents {
		if a.Type != wantType[i] {
			t.Fatalf("agent[%d].Type = %q, want %q", i, a.Type, wantType[i])
		}
		if a.Persona == nil || a.Persona.Name != wantName[i] {
			t.Fatalf("agent[%d] persona = %v, want %q", i, a.Persona, wantName[i])
		}
		if a.Index != wantIndex[i] {
			t.Fatalf("agent[%d].Index = %d, want %d", i, a.Index, wantIndex[i])
		}
	}
	if agents[0].Model != "opus" || agents[1].Model != "gpt-5.5" {
		t.Fatalf("models not carried from personas: %q, %q", agents[0].Model, agents[1].Model)
	}
}

func TestExpandProfileAgents_SurvivesNormalizeSpawnOptions(t *testing.T) {
	// Regression guard: normalizeSpawnOptions -> populateSpawnAgentsFromCounts
	// must NOT regenerate Agents from the (zero) counts and strip the personas
	// off an already-expanded persona-driven agent list. recomputeSpawnAgentCounts
	// must then derive the counts from that list.
	profiles := []*persona.Persona{
		{Name: "architect", AgentType: "claude", Model: "opus"},
		{Name: "developer", AgentType: "codex"},
		{Name: "auditor", AgentType: "codex"},
	}
	expanded, err := expandProfileAgents(profiles, nil)
	if err != nil {
		t.Fatalf("expandProfileAgents: %v", err)
	}

	opts := SpawnOptions{Agents: expanded} // counts deliberately left at zero
	normalizeSpawnOptions(&opts)

	if len(opts.Agents) != 3 {
		t.Fatalf("agents were stripped by normalize: got %d, want 3", len(opts.Agents))
	}
	for i, want := range []string{"architect", "developer", "auditor"} {
		if opts.Agents[i].Persona == nil || opts.Agents[i].Persona.Name != want {
			t.Fatalf("agent[%d] persona lost after normalize: %+v", i, opts.Agents[i].Persona)
		}
	}
	if opts.CCCount != 1 || opts.CodCount != 2 {
		t.Fatalf("counts not recomputed from expanded agents: cc=%d cod=%d, want cc=1 cod=2", opts.CCCount, opts.CodCount)
	}
}

func TestExpandProfileAgents_MatchingRequestedCountsSucceeds(t *testing.T) {
	// --cod=3 with a 3-codex persona set is consistent and must succeed.
	profiles := []*persona.Persona{
		{Name: "p1", AgentType: "codex"},
		{Name: "p2", AgentType: "codex"},
		{Name: "p3", AgentType: "codex"},
	}
	requested := AgentSpecs{{Type: AgentTypeCodex, Count: 3}}
	agents, err := expandProfileAgents(profiles, requested)
	if err != nil {
		t.Fatalf("expandProfileAgents: unexpected error: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("got %d agents, want 3", len(agents))
	}
}

func TestExpandProfileAgents_CountMismatchFailsClosed(t *testing.T) {
	// --cod=3 but only 2 codex personas — must fail closed, not silently warn.
	profiles := []*persona.Persona{
		{Name: "p1", AgentType: "codex"},
		{Name: "p2", AgentType: "codex"},
	}
	requested := AgentSpecs{{Type: AgentTypeCodex, Count: 3}}
	_, err := expandProfileAgents(profiles, requested)
	if err == nil {
		t.Fatal("expected error for count mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "cod") {
		t.Fatalf("error should mention the conflicting type: %v", err)
	}
}

func TestExpandProfileAgents_AgentTypeConflictFailsClosed(t *testing.T) {
	// --cod=3 but the set mixes a claude persona — agent_type conflict.
	profiles := []*persona.Persona{
		{Name: "architect", AgentType: "claude"},
		{Name: "developer", AgentType: "codex"},
		{Name: "auditor", AgentType: "codex"},
	}
	requested := AgentSpecs{{Type: AgentTypeCodex, Count: 3}}
	_, err := expandProfileAgents(profiles, requested)
	if err == nil {
		t.Fatal("expected error for agent_type conflict, got nil")
	}
}

func TestExpandProfileAgents_EmptyReturnsNil(t *testing.T) {
	agents, err := expandProfileAgents(nil, nil)
	if err != nil || agents != nil {
		t.Fatalf("expandProfileAgents(nil,nil) = (%v, %v), want (nil, nil)", agents, err)
	}
}

func TestSortPanesForAssignment_DeterministicOrder(t *testing.T) {
	// Panes returned out of order (and across two windows) must sort to a
	// stable (window, index) order so persona→pane mapping is reproducible.
	panes := []tmux.Pane{
		{ID: "%3", Index: 2, WindowIndex: 0},
		{ID: "%1", Index: 0, WindowIndex: 0},
		{ID: "%5", Index: 0, WindowIndex: 1},
		{ID: "%2", Index: 1, WindowIndex: 0},
	}
	sortPanesForAssignment(panes)
	wantIDs := []string{"%1", "%2", "%3", "%5"}
	for i, p := range panes {
		if p.ID != wantIDs[i] {
			t.Fatalf("panes[%d].ID = %q, want %q (order=%v)", i, p.ID, wantIDs[i], panes)
		}
	}
}

func TestValidateProfileAgentDistribution(t *testing.T) {
	tests := []struct {
		name      string
		persona   map[AgentType]int
		requested map[AgentType]int
		wantErr   bool
	}{
		{"exact match", map[AgentType]int{AgentTypeCodex: 3}, map[AgentType]int{AgentTypeCodex: 3}, false},
		{"count mismatch", map[AgentType]int{AgentTypeCodex: 2}, map[AgentType]int{AgentTypeCodex: 3}, true},
		{"type missing in request", map[AgentType]int{AgentTypeClaude: 1, AgentTypeCodex: 2}, map[AgentType]int{AgentTypeCodex: 3}, true},
		{"type missing in persona", map[AgentType]int{AgentTypeCodex: 3}, map[AgentType]int{AgentTypeClaude: 1, AgentTypeCodex: 3}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProfileAgentDistribution(tc.persona, tc.requested)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateProfileAgentDistribution err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestWizardAgentSpecs_IncludesModernTypes(t *testing.T) {

	specs := wizardAgentSpecs(SpawnWizardResult{
		CCCount:       1,
		CursorCount:   2,
		WindsurfCount: 1,
		AiderCount:    1,
		OllamaCount:   3,
	})

	if len(specs) != 5 {
		t.Fatalf("len(specs) = %d, want 5", len(specs))
	}
	if specs[0] != (AgentSpec{Type: AgentTypeClaude, Count: 1}) {
		t.Fatalf("specs[0] = %+v, want Claude x1", specs[0])
	}
	if specs[1] != (AgentSpec{Type: AgentTypeCursor, Count: 2}) {
		t.Fatalf("specs[1] = %+v, want Cursor x2", specs[1])
	}
	if specs[2] != (AgentSpec{Type: AgentTypeWindsurf, Count: 1}) {
		t.Fatalf("specs[2] = %+v, want Windsurf x1", specs[2])
	}
	if specs[3] != (AgentSpec{Type: AgentTypeAider, Count: 1}) {
		t.Fatalf("specs[3] = %+v, want Aider x1", specs[3])
	}
	if specs[4] != (AgentSpec{Type: AgentTypeOllama, Count: 3}) {
		t.Fatalf("specs[4] = %+v, want Ollama x3", specs[4])
	}
}

func TestSpawnWizardResultFromCounts_IncludesModernTypes(t *testing.T) {

	result := spawnWizardResultFromCounts(map[string]int{
		"cc":       1,
		"cursor":   2,
		"windsurf": 3,
		"aider":    4,
		"ollama":   5,
	})

	if result.CCCount != 1 || result.CursorCount != 2 || result.WindsurfCount != 3 || result.AiderCount != 4 || result.OllamaCount != 5 {
		t.Fatalf("spawnWizardResultFromCounts() = %+v", result)
	}
}

func TestFormatWizardAgentCountSummary_IncludesModernTypes(t *testing.T) {

	got := formatWizardAgentCountSummary(map[string]int{
		"cc":       1,
		"cursor":   2,
		"windsurf": 1,
		"aider":    1,
		"ollama":   3,
	})
	want := "cc:1 cursor:2 windsurf:1 aider:1 ollama:3"
	if got != want {
		t.Fatalf("formatWizardAgentCountSummary() = %q, want %q", got, want)
	}
	if got := formatWizardAgentCountSummary(nil); got != "no agents" {
		t.Fatalf("formatWizardAgentCountSummary(nil) = %q, want %q", got, "no agents")
	}
}

func TestWizardLaunchAgentSpecs_ManualWizardKeepsCounts(t *testing.T) {

	specs := wizardLaunchAgentSpecs(SpawnWizardResult{
		CCCount:       1,
		CursorCount:   2,
		WindsurfCount: 1,
	})

	if len(specs) != 3 {
		t.Fatalf("len(specs) = %d, want 3", len(specs))
	}
	if specs[0] != (AgentSpec{Type: AgentTypeClaude, Count: 1}) {
		t.Fatalf("specs[0] = %+v, want Claude x1", specs[0])
	}
	if specs[1] != (AgentSpec{Type: AgentTypeCursor, Count: 2}) {
		t.Fatalf("specs[1] = %+v, want Cursor x2", specs[1])
	}
	if specs[2] != (AgentSpec{Type: AgentTypeWindsurf, Count: 1}) {
		t.Fatalf("specs[2] = %+v, want Windsurf x1", specs[2])
	}
}

func TestWizardLaunchAgentSpecs_RecipeAndTemplateSelectionsDeferCounts(t *testing.T) {

	if specs := wizardLaunchAgentSpecs(SpawnWizardResult{CCCount: 2, Recipe: "review-team"}); len(specs) != 0 {
		t.Fatalf("recipe selection specs = %+v, want empty", specs)
	}
	if specs := wizardLaunchAgentSpecs(SpawnWizardResult{CodCount: 1, Template: "red-green"}); len(specs) != 0 {
		t.Fatalf("template selection specs = %+v, want empty", specs)
	}
	if !wizardDeferredSelection(SpawnWizardResult{Recipe: "review-team"}) {
		t.Fatal("wizardDeferredSelection(recipe) = false, want true")
	}
	if !wizardDeferredSelection(SpawnWizardResult{Template: "red-green"}) {
		t.Fatal("wizardDeferredSelection(template) = false, want true")
	}
	if wizardDeferredSelection(SpawnWizardResult{CCCount: 1}) {
		t.Fatal("wizardDeferredSelection(manual) = true, want false")
	}
}
