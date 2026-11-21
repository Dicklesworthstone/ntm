package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

type parityCAAMResolver struct {
	seats map[string]*tools.CAAMRankedProfile
	errs  map[string]error
}

func (r *parityCAAMResolver) RankedSeat(_ context.Context, provider, _ string) (*tools.CAAMRankedProfile, error) {
	if err, ok := r.errs[provider]; ok {
		return nil, err
	}
	if seat, ok := r.seats[provider]; ok {
		return seat, nil
	}
	return nil, fmt.Errorf("no parity seat for %q", provider)
}

func initParityGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ntm-parity",
			"GIT_AUTHOR_EMAIL=parity@example.com",
			"GIT_COMMITTER_NAME=ntm-parity",
			"GIT_COMMITTER_EMAIL=parity@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}
	runGit("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("ori parity\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial commit")
	return repo
}

// TestORISpawnParityAcrossCLIRobotAndREST proves that CAAM seat_selection=true,
// --worktrees, --persona, and lifecycle hooks produce identical outcomes across:
//   - CLI shared engine (internal/session.PlanCAAMSeatsWithResolver + PreflightSpawnWorktrees + ResolveSpawnPersonas + RunPreSpawnHooks)
//   - Robot spawn (internal/robot.GetSpawn)
//   - REST HTTP spawn (POST /api/v1/sessions/{id}/agents/spawn)
func TestORISpawnParityAcrossCLIRobotAndREST(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("SHALLOW_PROFILE", "")

	repo := initParityGitRepo(t)

	cfg := config.Default()
	cfg.Integrations.CAAM.SeatSelection = true
	cfg.Agents.Claude = `claude --persona {{.PersonaName}} {{if .Model}}--model {{.Model}} {{end}}{{if .SystemPromptFile}}--system-prompt-file {{.SystemPromptFile}}{{end}}`
	cfg.Agents.Codex = `codex --persona {{.PersonaName}}`

	resolver := &parityCAAMResolver{
		seats: map[string]*tools.CAAMRankedProfile{
			"claude": {Provider: "claude", Profile: "alpha-seat", Eligible: true},
		},
		errs: map[string]error{
			"codex": fmt.Errorf("%w: codex exhausted", tools.ErrCAAMNoSeatSelectable),
		},
	}

	// 1. CLI / shared internal/session ORI engine outcome.
	cliCandidates := []session.CAAMSeatCandidate{
		{Index: 0, Type: "cc", Pinned: false},
		{Index: 1, Type: "cc", Pinned: true}, // pinned reviewer persona
		{Index: 2, Type: "cod", Pinned: false},
	}
	cliSeatPlan := session.PlanCAAMSeatsWithResolver(context.Background(), cfg, repo, cliCandidates, resolver)
	cliSeat0, ok0 := cliSeatPlan.Seat(0)
	if !ok0 || cliSeat0.Profile != "alpha-seat" || cliSeat0.Persona != "claude-alpha-seat" {
		t.Fatalf("CLI seat[0] = (%+v, %v), want alpha-seat / claude-alpha-seat", cliSeat0, ok0)
	}
	if _, pinnedSeat := cliSeatPlan.Seat(1); pinnedSeat {
		t.Fatal("CLI seat planner overrode pinned reviewer persona")
	}
	cliSkipReason, cliSkipped := cliSeatPlan.SkipReason(2)
	if !cliSkipped || !strings.Contains(cliSkipReason, "codex exhausted") {
		t.Fatalf("CLI seat[2] skip = (%q, %v), want codex exhausted", cliSkipReason, cliSkipped)
	}
	wtMgr, err := session.PreflightSpawnWorktrees(context.Background(), repo, "ori_cli", []string{
		session.WorktreeAgentName("claude", 1, ""),
		session.WorktreeAgentName("claude", 2, ""),
	})
	if err != nil || wtMgr == nil {
		t.Fatalf("CLI PreflightSpawnWorktrees failed: %v", err)
	}

	// Helper building hermetic lifecycle deps for Robot and REST so both run the
	// exact production robot.GetSpawn -> internal/session ORI path without live tmux.
	runSpawnSurface := func(sessionName string, hookLogPath string) (*robot.SpawnLifecycleDependencies, *[]string) {
		var launchDirs []string
		hookExec := hooks.NewExecutor(&hooks.CommandHooksConfig{
			Hooks: []hooks.CommandHook{
				{Event: hooks.EventPreSpawn, Name: "pre", Command: fmt.Sprintf(`echo "pre:$NTM_AGENT_COUNT_CC:$NTM_AGENT_COUNT_COD" >> %q`, hookLogPath)},
				{Event: hooks.EventPostSpawn, Name: "post", Command: fmt.Sprintf(`echo "post:$NTM_SPAWN_SUCCESS" >> %q`, hookLogPath)},
			},
		})
		deps := &robot.SpawnLifecycleDependencies{
			IsTMUXInstalled: func() bool { return true },
			GetAllPanes:     func(context.Context) (map[string][]tmux.Pane, error) { return map[string][]tmux.Pane{}, nil },
			SessionExists:   func(context.Context, string) (bool, error) { return false, nil },
			CreateSession:   func(context.Context, string, string, int) error { return nil },
			GetPanes: func(context.Context, string) ([]tmux.Pane, error) {
				return []tmux.Pane{
					{ID: "%1", WindowIndex: 0, Index: 0, Command: "bash", PID: 301},
					{ID: "%2", WindowIndex: 0, Index: 1, Command: "bash", PID: 302},
					{ID: "%3", WindowIndex: 0, Index: 2, Command: "bash", PID: 303},
				}, nil
			},
			SplitWindow:      func(context.Context, string, string) (string, error) { return "%x", nil },
			ApplyTiledLayout: func(context.Context, string) error { return nil },
			LaunchAgent: func(_ context.Context, pane tmux.Pane, sess, agentType string, num int, dir, _ string) (robot.SpawnedAgent, error) {
				launchDirs = append(launchDirs, dir)
				return robot.SpawnedAgent{
					Pane:   fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index),
					PaneID: pane.ID,
					Type:   agentType,
					Title:  fmt.Sprintf("%s__%s_%d", sess, agentType, num),
				}, nil
			},
			CAAMSeatResolver: resolver,
			HookExecutor:     hookExec,
		}
		return deps, &launchDirs
	}

	// 2. Robot surface execution.
	robotHookLog := filepath.Join(t.TempDir(), "robot-hooks.log")
	robotDeps, robotDirs := runSpawnSurface("ori_robot", robotHookLog)
	robotOut, err := robot.GetSpawn(context.Background(), robot.SpawnOptions{
		Session:       "ori_robot",
		WorkingDir:    repo,
		CCCount:       1,
		CodCount:      1,
		Personas:      []string{"reviewer"},
		UseWorktrees:  true,
		NoUserPane:    true,
		LifecycleDeps: robotDeps,
	}, cfg)
	if err != nil || !robotOut.Success {
		t.Fatalf("Robot GetSpawn failed: out=%+v err=%v", robotOut, err)
	}

	// 3. REST surface execution through full HTTP router -> handleAgentSpawnV1 -> robot.GetSpawn.
	restHookLog := filepath.Join(t.TempDir(), "rest-hooks.log")
	restDeps, restDirs := runSpawnSurface("ori_rest", restHookLog)
	srv := NewHermeticServer("ori_rest")
	defer srv.Stop()
	srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		opts.WorkingDir = repo
		opts.NoUserPane = true
		opts.LifecycleDeps = restDeps
		return robot.GetSpawn(ctx, opts, cfg)
	}

	reqBody := `{"cc_count":1,"cod_count":1,"personas":["reviewer"],"worktrees":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/ori_rest/agents/spawn", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("REST spawn HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var restResp struct {
		Success   bool                   `json:"success"`
		Agents    []robot.SpawnedAgent   `json:"agents"`
		SeatSkips []session.CAAMSeatSkip `json:"seat_skips"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &restResp); err != nil {
		t.Fatalf("decode REST response: %v body=%s", err, rec.Body.String())
	}

	// Cross-surface ORI parity assertions: CLI == Robot == REST.
	if len(robotOut.Agents) != 2 || len(restResp.Agents) != 2 {
		t.Fatalf("agent counts diverged: robot=%d rest=%d", len(robotOut.Agents), len(restResp.Agents))
	}
	if robotOut.Agents[0].Persona != cliSeat0.Persona || restResp.Agents[0].Persona != cliSeat0.Persona {
		t.Fatalf("CAAM seat persona diverged: cli=%q robot=%q rest=%q", cliSeat0.Persona, robotOut.Agents[0].Persona, restResp.Agents[0].Persona)
	}
	if robotOut.Agents[1].Persona != "reviewer" || restResp.Agents[1].Persona != "reviewer" {
		t.Fatalf("pinned persona diverged: robot=%q rest=%q", robotOut.Agents[1].Persona, restResp.Agents[1].Persona)
	}
	if len(robotOut.SeatSkips) != 1 || len(restResp.SeatSkips) != 1 ||
		robotOut.SeatSkips[0].Provider != "codex" || restResp.SeatSkips[0].Provider != "codex" {
		t.Fatalf("CAAM seat skip parity diverged: robot=%+v rest=%+v", robotOut.SeatSkips, restResp.SeatSkips)
	}
	if len(*robotDirs) != 2 || len(*restDirs) != 2 || (*robotDirs)[0] == repo || (*restDirs)[0] == repo {
		t.Fatalf("worktree isolation diverged: robotDirs=%v restDirs=%v repo=%s", *robotDirs, *restDirs, repo)
	}

	robotHooks, err := os.ReadFile(robotHookLog)
	if err != nil {
		t.Fatalf("read robot hook log: %v", err)
	}
	restHooks, err := os.ReadFile(restHookLog)
	if err != nil {
		t.Fatalf("read rest hook log: %v", err)
	}
	if string(robotHooks) != string(restHooks) || !strings.Contains(string(robotHooks), "pre:2:1\n") || !strings.Contains(string(robotHooks), "post:true\n") {
		t.Fatalf("hook parity diverged:\nrobot=%q\nrest=%q", string(robotHooks), string(restHooks))
	}
}
