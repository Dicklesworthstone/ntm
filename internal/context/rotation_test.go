package context

import (
	stdcontext "context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const rotationReadyClaudeScreen = "Welcome to Claude Code\n────────────────────────\n❯ \n────────────────────────\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"

func TestExpiredProductionConfirmationSharesDurableOwnership(t *testing.T) {
	for _, mode := range []string{"already_acknowledged", "failed_legacy_identity"} {
		t.Run(mode, func(t *testing.T) {
			originalStore := DefaultPendingRotationStore
			DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
			t.Cleanup(func() { DefaultPendingRotationStore = originalStore })
			pending := &PendingRotation{
				AgentID: "test__cc_1", SessionName: "test", PaneID: "%1",
				CreatedAt: time.Now().Add(-time.Minute), TimeoutAt: time.Now().Add(time.Minute),
				DefaultAction: ConfirmIgnore,
			}
			if mode == "failed_legacy_identity" {
				pending.DefaultAction = ConfirmRotate
				pending.TimeoutAt = time.Now().Add(-time.Second)
			}
			if err := AddPendingRotation(pending); err != nil {
				t.Fatal(err)
			}
			r := NewRotator(RotatorConfig{
				Monitor: NewContextMonitor(DefaultMonitorConfig()),
				Spawner: NewDefaultPaneSpawner(config.Default()),
				Config:  config.DefaultContextRotationConfig(),
			})
			stale := clonePendingRotation(pending)
			stale.TimeoutAt = time.Now().Add(-time.Second)
			r.pending[pending.AgentID] = stale
			if mode == "already_acknowledged" {
				result := ConfirmPendingRotationContext(stdcontext.Background(), pending.AgentID, ConfirmIgnore, 0, false, config.Default())
				if !result.Success {
					t.Fatalf("manual acknowledgment: %+v", result)
				}
			}
			r.processExpiredPending("test", t.TempDir())
			if mode == "already_acknowledged" {
				if r.HasPendingRotation(pending.AgentID) {
					t.Fatal("expiry retained stale in-memory request after completed receipt replay")
				}
				return
			}
			stored, err := GetPendingRotationByID(pending.AgentID)
			if err != nil || stored == nil || stored.ExecutionState != RotationStateFailed || stored.Result == nil || !strings.Contains(stored.Result.Error, "process identity") {
				t.Fatalf("expiry lost failed choice: %+v, %v", stored, err)
			}
			executionID := stored.ExecutionID
			r.processExpiredPending("test", t.TempDir())
			stored, err = GetPendingRotationByID(pending.AgentID)
			if err != nil || stored == nil || stored.ExecutionID != executionID {
				t.Fatalf("expiry retried unresolved execution: %+v, %v", stored, err)
			}
		})
	}
}

func TestNativeCompactionRequiresFreshAccountingAndCompletedInputState(t *testing.T) {
	for _, mode := range []string{"reduced", "busy_then_reduced", "stale", "same_usage", "different_transcript", "identity_changed", "canceled", "missing_baseline"} {
		t.Run(mode, func(t *testing.T) {
			expected := tmux.Pane{ID: "%1", PID: 123, Type: tmux.AgentClaude, Command: "node", Width: 120}
			ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
			defer cancel()
			sent, afterCaptures := 0, 0
			before := &TranscriptUsage{Path: "/project/session.jsonl", Model: "claude-opus-4", Tokens: 150000, ContextWindow: 200000, UpdatedAt: time.Now().Add(-time.Second)}
			list := func(stdcontext.Context, string) ([]tmux.Pane, error) {
				pane := expected
				if sent > 0 && mode == "identity_changed" {
					pane.PID++
				}
				return []tmux.Pane{pane}, nil
			}
			capture := func(stdcontext.Context, string) (string, error) {
				if sent > 0 {
					afterCaptures++
					if mode == "busy_then_reduced" && afterCaptures <= 2 {
						return "✻ Sautéing… (ctrl+c to interrupt · 12s · thinking)\n" + rotationReadyClaudeScreen, nil
					}
				}
				return rotationReadyClaudeScreen, nil
			}
			usage := func(_ stdcontext.Context, _ string, prior *TranscriptUsage) (*TranscriptUsage, error) {
				if prior == nil {
					if mode == "missing_baseline" {
						return nil, errors.New("no trustworthy usage")
					}
					copy := *before
					return &copy, nil
				}
				if prior.Path != before.Path {
					t.Fatalf("lost original transcript identity: %+v", prior)
				}
				after := *before
				after.Tokens, after.UpdatedAt = 30000, time.Now()
				switch mode {
				case "stale":
					after.UpdatedAt = before.UpdatedAt
				case "same_usage":
					after.Tokens = before.Tokens
				case "different_transcript":
					after.Path = "/project/other-agent.jsonl"
				}
				return &after, nil
			}
			send := func(_ stdcontext.Context, pane, text string, enter bool) error {
				if pane != expected.ID || text != "/compact" || !enter {
					t.Fatalf("unsafe native command: %q %q enter=%v", pane, text, enter)
				}
				sent++
				if mode == "canceled" {
					cancel()
				}
				return nil
			}
			monitor := NewContextMonitor(DefaultMonitorConfig())
			monitor.RegisterAgent("test__cc_1", expected.ID, before.Model)
			monitor.UpdateFromRobotMode("test__cc_1", `{"context_used":199000,"context_limit":200000}`)
			compactor := NewCompactor(monitor, DefaultCompactorConfig())
			result := runNativeCompaction(ctx, "test", expected, CompactionCommand{Command: "/compact"}, compactor,
				25*time.Millisecond, time.Millisecond, list, capture, send, usage)
			wantSuccess := mode == "reduced" || mode == "busy_then_reduced"
			if result.Success != wantSuccess {
				t.Fatalf("native compaction = %+v, want success=%v", result, wantSuccess)
			}
			if wantSuccess && (result.TokensBefore != 150000 || result.TokensAfter != 30000 || result.UsageAfter != 15 || result.Method != CompactionBuiltin) {
				t.Fatalf("used cached monitor data instead of live accounting: %+v", result)
			}
			if mode == "busy_then_reduced" && afterCaptures < 4 {
				t.Fatalf("accepted compaction before stable idle observations: %d", afterCaptures)
			}
			wantSends := 1
			if mode == "missing_baseline" {
				wantSends = 0
			}
			if sent != wantSends {
				t.Fatalf("sent %d native commands, want %d; %+v", sent, wantSends, result)
			}
		})
	}
}

func TestRotationSummaryWaitsForFreshCompletedResponse(t *testing.T) {
	generator := NewSummaryGenerator(SummaryGeneratorConfig{PromptTimeout: time.Second})
	prompt, startMarker, endMarker := rotationSummaryRequest(generator)
	body := "## CURRENT TASK\nImplement durable scheduling.\n\n## PROGRESS\nQueue admission is complete; recovery remains.\n\n## ACTIVE FILES\n- internal/serve/jobs.go"
	expected := tmux.Pane{ID: "%1", PID: 123, Type: tmux.AgentClaude, Command: "node", Width: 120}
	list := func(stdcontext.Context, string) ([]tmux.Pane, error) { return []tmux.Pane{expected}, nil }
	captures, visibleCaptures := 0, 0
	capture := func(stdcontext.Context, string, int) (string, error) {
		captures++
		switch captures {
		case 1:
			return prompt, nil // The request echo has the same section headers.
		case 2:
			return prompt + "\n" + startMarker + "\n" + body, nil // Still streaming.
		default:
			return prompt + "\n" + startMarker + "\n" + body + "\n" + endMarker, nil
		}
	}
	visible := func(stdcontext.Context, string) (string, error) {
		visibleCaptures++
		if visibleCaptures == 1 {
			return "✻ Sautéing… (ctrl+c to interrupt · 12s · thinking)\n" + rotationReadyClaudeScreen, nil
		}
		return rotationReadyClaudeScreen, nil
	}
	summary, err := waitForRotationSummary(stdcontext.Background(), "test", "test__cc_1", expected, generator, startMarker, endMarker, time.Millisecond, list, capture, visible)
	if err != nil || summary == nil || summary.CurrentTask != "Implement durable scheduling." || summary.Progress != "Queue admission is complete; recovery remains." {
		t.Fatalf("completed summary = %+v, %v", summary, err)
	}
	if captures < 3 || visibleCaptures < 3 || strings.Contains(summary.RawSummary, "What task") {
		t.Fatalf("accepted request/partial/busy response: captures=%d visible=%d summary=%+v", captures, visibleCaptures, summary)
	}
}

func TestRotationSummaryRejectsEchoPartialAndUnrelatedResponses(t *testing.T) {
	for _, kind := range []string{"echo", "wrapped_echo", "partial", "unrelated"} {
		t.Run(kind, func(t *testing.T) {
			generator := NewSummaryGenerator(SummaryGeneratorConfig{PromptTimeout: 20 * time.Millisecond})
			prompt, startMarker, endMarker := rotationSummaryRequest(generator)
			captured := prompt
			switch kind {
			case "wrapped_echo":
				captured = startMarker + "\n" + SummaryPromptTemplate + "\n" + endMarker
			case "partial":
				captured = startMarker + "\n## CURRENT TASK\nBuild scheduling.\n## PROGRESS\nAdmission complete."
			case "unrelated":
				captured = "NTM_START_old\n## CURRENT TASK\nBuild scheduling.\n## PROGRESS\nAdmission complete.\nNTM_END_old"
			}
			expected := tmux.Pane{ID: "%1", PID: 123, Type: tmux.AgentClaude, Command: "node"}
			list := func(stdcontext.Context, string) ([]tmux.Pane, error) { return []tmux.Pane{expected}, nil }
			capture := func(stdcontext.Context, string, int) (string, error) { return captured, nil }
			visible := func(stdcontext.Context, string) (string, error) { return rotationReadyClaudeScreen, nil }
			summary, err := waitForRotationSummary(stdcontext.Background(), "test", "test__cc_1", expected, generator, startMarker, endMarker, time.Millisecond, list, capture, visible)
			if summary != nil || !errors.Is(err, stdcontext.DeadlineExceeded) {
				t.Fatalf("invalid summary accepted: %+v, %v", summary, err)
			}
		})
	}
}

func TestRotationReadinessRequiresStableLivePrompt(t *testing.T) {
	for _, initial := range []struct {
		name    string
		command string
		screen  string
	}{
		{"shell_with_stale_prompt", "bash", rotationReadyClaudeScreen},
		{"blank_startup", "node", ""},
		{"banner_without_composer", "node", "Welcome to Claude Code"},
		{"version_banner_without_composer", "node", "Claude Code v1.0.0\nWelcome back"},
		{"interpreter_prompt", "node", "Welcome to Node.js\n> "},
		{"unsubmitted_draft", "node", strings.Replace(rotationReadyClaudeScreen, "❯ ", "❯ unfinished instructions", 1)},
	} {
		t.Run(initial.name, func(t *testing.T) {
			expected := tmux.Pane{ID: "%99", PID: 321, Type: tmux.AgentClaude, Command: "node"}
			observations, captures := 0, 0
			list := func(ctx stdcontext.Context, session string) ([]tmux.Pane, error) {
				if session != "rotation" {
					t.Fatalf("unexpected session %q", session)
				}
				observations++
				pane := expected
				if observations == 1 {
					pane.Command = initial.command
				}
				// A title rewrite and logical-index change cannot move the target.
				pane.Title, pane.Index = "agent changed its title", 8
				return []tmux.Pane{{ID: "%2", PID: 2, Title: "rotation__cc_1"}, pane}, nil
			}
			capture := func(ctx stdcontext.Context, paneID string) (string, error) {
				if paneID != "%99" {
					t.Fatalf("capture target = %q, want physical replacement", paneID)
				}
				captures++
				if observations == 1 {
					return initial.screen, nil
				}
				return rotationReadyClaudeScreen, nil
			}
			pane, err := waitForRotationReady(stdcontext.Background(), "rotation", expected, time.Second, time.Millisecond, list, capture)
			if err != nil || pane.ID != expected.ID || observations < 4 || captures < 2 {
				t.Fatalf("readiness = %+v, %v; observations=%d captures=%d", pane, err, observations, captures)
			}
		})
	}
}

func TestRotationReadinessRejectsBusyComposerChrome(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agentType tmux.AgentType
		output    string
	}{
		{"codex", tmux.AgentCodex, "OpenAI Codex\n• Working (4m 51s • esc to interrupt)\n› \n47% context left · ? for shortcuts"},
		{"claude", tmux.AgentClaude, "Welcome to Claude Code\n✻ Sautéing… (ctrl+c to interrupt · 12s · thinking)\n────────────────────────\n❯ \n────────────────────────\n  ⏵⏵ bypass permissions on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane := tmux.Pane{ID: "%99", PID: 321, Type: tc.agentType, Command: "node", Width: 120}
			if ready, reason := rotationPromptReady(tc.output, pane); ready || !strings.Contains(reason, "processing") {
				t.Fatalf("busy composer readiness = %v, %q", ready, reason)
			}
		})
	}
}

func TestRotationReadinessRejectsChangedOrUnobservableReplacement(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*tmux.Pane)
		listErr    error
		captureErr error
		missing    bool
		want       string
	}{
		{name: "respawned", mutate: func(p *tmux.Pane) { p.PID++ }, want: "identity changed"},
		{name: "wrong_agent", mutate: func(p *tmux.Pane) { p.Type = tmux.AgentCodex }, want: "identity changed"},
		{name: "service", mutate: func(p *tmux.Pane) { p.Service = "cm" }, want: "identity changed"},
		{name: "dead", mutate: func(p *tmux.Pane) { p.Dead = true }, want: "process exited"},
		{name: "missing_same_title", missing: true, want: "not found"},
		{name: "pane_read_failure", listErr: errors.New("topology unavailable"), want: "topology unavailable"},
		{name: "capture_failure", captureErr: errors.New("capture unavailable"), want: "capture unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := tmux.Pane{ID: "%99", PID: 321, Type: tmux.AgentClaude, Command: "node"}
			list := func(stdcontext.Context, string) ([]tmux.Pane, error) {
				pane := expected
				if tc.mutate != nil {
					tc.mutate(&pane)
				}
				if tc.missing {
					pane.ID, pane.Title = "%100", expected.Title
				}
				return []tmux.Pane{pane}, tc.listErr
			}
			capture := func(stdcontext.Context, string) (string, error) { return rotationReadyClaudeScreen, tc.captureErr }
			_, err := waitForRotationReady(stdcontext.Background(), "rotation", expected, time.Second, time.Millisecond, list, capture)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("readiness error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRotationReadinessRechecksIdentityAfterCapture(t *testing.T) {
	expected := tmux.Pane{ID: "%99", PID: 321, Type: tmux.AgentClaude, Command: "node"}
	observed := expected
	captures := 0
	list := func(stdcontext.Context, string) ([]tmux.Pane, error) { return []tmux.Pane{observed}, nil }
	capture := func(stdcontext.Context, string) (string, error) {
		captures++
		if captures == 2 {
			observed.PID++
		}
		return rotationReadyClaudeScreen, nil
	}
	_, err := waitForRotationReady(stdcontext.Background(), "rotation", expected, time.Second, time.Millisecond, list, capture)
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("readiness after capture-time respawn = %v", err)
	}
}

func TestRotationReadinessBoundsShellAndBlockingCapture(t *testing.T) {
	for _, blockingCapture := range []bool{false, true} {
		t.Run(strconv.FormatBool(blockingCapture), func(t *testing.T) {
			expected := tmux.Pane{ID: "%99", PID: 321, Type: tmux.AgentClaude, Command: "bash"}
			if blockingCapture {
				expected.Command = "node"
			}
			list := func(stdcontext.Context, string) ([]tmux.Pane, error) { return []tmux.Pane{expected}, nil }
			capture := func(ctx stdcontext.Context, _ string) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			}
			started := time.Now()
			_, err := waitForRotationReady(stdcontext.Background(), "rotation", expected, 20*time.Millisecond, time.Millisecond, list, capture)
			if !errors.Is(err, stdcontext.DeadlineExceeded) || time.Since(started) > time.Second {
				t.Fatalf("bounded readiness = %v, elapsed=%s", err, time.Since(started))
			}
		})
	}
}

type observedRotationSpawner struct {
	*MockPaneSpawner
	replacement tmux.Pane
	deliveryErr error
	cleanupErr  error
	delivered   bool
}

func (s *observedRotationSpawner) SpawnReplacementContext(stdcontext.Context, string, tmux.Pane, int, string) (tmux.Pane, error) {
	s.spawnedPanes = append(s.spawnedPanes, s.replacement.ID)
	return s.replacement, nil
}

func (s *observedRotationSpawner) DeliverHandoffContext(ctx stdcontext.Context, _ string, pane tmux.Pane, prompt string) error {
	if pane.ID != s.replacement.ID || pane.PID != s.replacement.PID || !strings.Contains(prompt, "handoff") && !strings.Contains(strings.ToLower(prompt), "context") {
		return errors.New("incorrect replacement or missing handoff")
	}
	s.delivered = true
	return s.deliveryErr
}

func (s *observedRotationSpawner) KillRotationPaneContext(_ stdcontext.Context, _ string, pane tmux.Pane) error {
	s.killAttempts = append(s.killAttempts, pane.ID)
	if s.cleanupErr != nil {
		return s.cleanupErr
	}
	s.killedPanes = append(s.killedPanes, pane.ID)
	return nil
}

func TestConfirmRotationReadinessFailurePreservesOriginalMonitor(t *testing.T) {
	workDir, _ := setupRotationTmux(t)
	t.Setenv("ROTATION_CAPTURE", "Work in progress: implement the durable scheduler and preserve pending jobs.")
	monitor := NewContextMonitor(DefaultMonitorConfig())
	const agentID = "test__cc_1"
	monitor.RegisterAgent(agentID, "%1", "claude-opus-4")
	monitor.RecordMessage(agentID, 1000, 1000)
	original := *monitor.GetState(agentID)
	spawner := &observedRotationSpawner{
		MockPaneSpawner: NewMockPaneSpawner(),
		replacement:     tmux.Pane{ID: "%99", PID: 321, Type: tmux.AgentClaude, Command: "node"},
		deliveryErr:     fmt.Errorf("replacement never drew a composer: %w", stdcontext.DeadlineExceeded),
		cleanupErr:      errors.New("replacement process identity changed"),
	}
	spawner.panes = []tmux.Pane{{ID: "%1", PID: 123, Title: agentID, Type: tmux.AgentClaude, Command: "node"}}
	cfg := config.DefaultContextRotationConfig()
	cfg.TryCompactFirst = false
	r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: spawner, Config: cfg})
	r.EnqueuePendingRotation("test", agentID, "%1", 95, workDir)
	result := r.ConfirmRotationContext(stdcontext.Background(), agentID, ConfirmRotate, 0)
	if result.Success || result.State != RotationStateFailed || !spawner.delivered || !strings.Contains(result.Error, "original agent preserved") {
		t.Fatalf("failed readiness result = %+v, delivered=%v", result, spawner.delivered)
	}
	if !strings.Contains(result.Error, "identity changed") || len(spawner.killedPanes) != 0 || len(spawner.killAttempts) != 1 || spawner.killAttempts[0] != "%99" {
		t.Fatalf("cleanup did not preserve uncertain process: %+v, killed=%v attempts=%v", result, spawner.killedPanes, spawner.killAttempts)
	}
	if current := monitor.GetState(agentID); current == nil || *current != original {
		t.Fatalf("original monitor changed: %+v, want %+v", current, original)
	}
	if !r.HasPendingRotation(agentID) {
		t.Fatal("failed handoff consumed the pending confirmation")
	}
}

func TestConfirmRotationCancellationPreservesPendingAndPredecessor(t *testing.T) {
	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1", "%1", "claude-opus-4")
	spawner := NewMockPaneSpawner()
	r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: spawner})
	r.EnqueuePendingRotation("test", "test__cc_1", "%1", 95, t.TempDir())
	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	cancel()
	result := r.ConfirmRotationContext(ctx, "test__cc_1", ConfirmRotate, 0)
	if result.Success || !strings.Contains(result.Error, "canceled") || !r.HasPendingRotation("test__cc_1") || len(spawner.spawnedPanes) != 0 || len(spawner.sentBuffers) != 0 {
		t.Fatalf("canceled confirmation mutated pending work: %+v", result)
	}
}

func TestConfirmRotationContextCancelsCompactionBeforeFurtherCommands(t *testing.T) {
	for _, action := range []ConfirmAction{ConfirmCompact, ConfirmRotate} {
		t.Run(string(action), func(t *testing.T) {
			monitor := NewContextMonitor(DefaultMonitorConfig())
			monitor.RegisterAgent("test__cc_1", "%1", "claude-opus-4")
			monitor.UpdateFromRobotMode("test__cc_1", `{"context_used":180000,"context_limit":200000}`)
			spawner := NewMockPaneSpawner()
			spawner.panes = []tmux.Pane{{ID: "%1", Title: "test__cc_1", Type: tmux.AgentClaude}}
			r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: spawner, Config: config.DefaultContextRotationConfig()})
			r.EnqueuePendingRotation("test", "test__cc_1", "%1", 95, t.TempDir())
			ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 20*time.Millisecond)
			defer cancel()
			started := time.Now()
			result := r.ConfirmRotationContext(ctx, "test__cc_1", action, 0)
			if result.Success || !strings.Contains(result.Error, "deadline exceeded") || time.Since(started) > time.Second {
				t.Fatalf("compaction cancellation = %+v, elapsed=%s", result, time.Since(started))
			}
			if len(spawner.sentKeys["%1"])+len(spawner.sentBuffers["%1"]) != 1 || len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
				t.Fatalf("commands continued after cancellation: keys=%v buffers=%v spawned=%v killed=%v", spawner.sentKeys, spawner.sentBuffers, spawner.spawnedPanes, spawner.killedPanes)
			}
			if !r.HasPendingRotation("test__cc_1") {
				t.Fatal("canceled execution consumed the pending confirmation")
			}
		})
	}
}

// MockPaneSpawner is a test double for PaneSpawner.
type MockPaneSpawner struct {
	spawnedPanes  []string
	killedPanes   []string
	killAttempts  []string
	sentKeys      map[string][]string
	sentBuffers   map[string][]string
	getPanesFor   []string
	panes         []tmux.Pane
	spawnError    error
	killError     error
	sendError     error
	sendPaneError map[string]error
	panesError    error
	getPanesFunc  func(string) ([]tmux.Pane, error)
}

func NewMockPaneSpawner() *MockPaneSpawner {
	return &MockPaneSpawner{
		sentKeys:    make(map[string][]string),
		sentBuffers: make(map[string][]string),
		panes:       []tmux.Pane{},
	}
}

func (m *MockPaneSpawner) SpawnAgent(session, agentType string, index int, variant string, workDir string) (string, error) {
	if m.spawnError != nil {
		return "", m.spawnError
	}
	paneID := "%new-pane"
	m.spawnedPanes = append(m.spawnedPanes, paneID)
	return paneID, nil
}

func (m *MockPaneSpawner) KillPane(paneID string) error {
	m.killAttempts = append(m.killAttempts, paneID)
	if m.killError != nil {
		return m.killError
	}
	m.killedPanes = append(m.killedPanes, paneID)
	return nil
}

func (m *MockPaneSpawner) SendKeys(paneID, text string, enter bool) error {
	if err := m.sendPaneError[paneID]; err != nil {
		return err
	}
	if m.sendError != nil {
		return m.sendError
	}
	m.sentKeys[paneID] = append(m.sentKeys[paneID], text)
	return nil
}

func (m *MockPaneSpawner) SendBuffer(paneID, text string, enter bool) error {
	if err := m.sendPaneError[paneID]; err != nil {
		return err
	}
	if m.sendError != nil {
		return m.sendError
	}
	m.sentBuffers[paneID] = append(m.sentBuffers[paneID], text)
	return nil
}

func (m *MockPaneSpawner) GetPanes(session string) ([]tmux.Pane, error) {
	m.getPanesFor = append(m.getPanesFor, session)
	if m.getPanesFunc != nil {
		return m.getPanesFunc(session)
	}
	if m.panesError != nil {
		return nil, m.panesError
	}
	return m.panes, nil
}

func TestNewRotator(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()

	cfg := RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	}

	r := NewRotator(cfg)

	if r.monitor != monitor {
		t.Error("monitor not set correctly")
	}
	if r.spawner != spawner {
		t.Error("spawner not set correctly")
	}
	if r.compactor == nil {
		t.Error("compactor should be created automatically when monitor is provided")
	}
	if r.summary == nil {
		t.Error("summary generator should be created automatically")
	}
}

func TestCheckAndRotate_NoMonitor(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{
		Config: config.DefaultContextRotationConfig(),
	})

	_, err := r.CheckAndRotate("test-session", "/tmp")
	if err == nil || !strings.Contains(err.Error(), "no monitor") {
		t.Errorf("expected 'no monitor' error, got: %v", err)
	}
}

func TestCheckAndRotate_NoSpawner(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Config:  config.DefaultContextRotationConfig(),
	})

	_, err := r.CheckAndRotate("test-session", "/tmp")
	if err == nil || !strings.Contains(err.Error(), "no spawner") {
		t.Errorf("expected 'no spawner' error, got: %v", err)
	}
}

func TestCheckAndRotate_Disabled(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()

	cfg := config.DefaultContextRotationConfig()
	cfg.Enabled = false

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  cfg,
	})

	results, err := r.CheckAndRotate("test-session", "/tmp")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if results != nil {
		t.Error("expected nil results when disabled")
	}
}

func TestCheckAndRotate_NoAgentsAboveThreshold(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()

	// Register an agent but don't add enough messages to exceed threshold
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	monitor.RecordMessage("test__cc_1", 100, 100)

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	})

	results, err := r.CheckAndRotate("test", "/tmp")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestCheckAndRotateEmitsWarningAtConfiguredThreshold(t *testing.T) {
	tracker := alerts.GetGlobalTracker()
	clearAlertTracker(tracker)
	t.Cleanup(func() { clearAlertTracker(tracker) })

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	for i := 0; i < 100; i++ {
		monitor.RecordMessage("test__cc_1", 1000, 1000)
	}

	cfg := config.DefaultContextRotationConfig()
	cfg.WarningThreshold = 0.30
	cfg.RotateThreshold = 0.90
	cfg.MinSessionAgeSec = 0
	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: NewMockPaneSpawner(),
		Config:  cfg,
	})

	results, err := r.CheckAndRotate("test-session", "/tmp")
	if err != nil {
		t.Fatalf("CheckAndRotate() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("rotation results = %d, want none below rotate threshold", len(results))
	}

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("active alerts = %d, want 1", len(active))
	}
	if active[0].Type != alerts.AlertContextWarning {
		t.Errorf("alert type = %s, want %s", active[0].Type, alerts.AlertContextWarning)
	}
	if active[0].Session != "test-session" {
		t.Errorf("alert session = %q, want test-session", active[0].Session)
	}
}

func TestCheckAndRotateRespectsMinimumSessionAge(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	tracker := alerts.GetGlobalTracker()
	clearAlertTracker(tracker)
	t.Cleanup(func() { clearAlertTracker(tracker) })

	monitor := NewContextMonitor(DefaultMonitorConfig())
	const agentID = "test__cc_1"
	monitor.RegisterAgent(agentID, "%0", "claude-opus-4")
	for i := 0; i < 200; i++ {
		monitor.RecordMessage(agentID, 1000, 1000)
	}

	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{{ID: "%0", Title: agentID, Type: tmux.AgentClaude}}
	cfg := config.DefaultContextRotationConfig()
	cfg.WarningThreshold = 0.30
	cfg.RotateThreshold = 0.50
	cfg.MinSessionAgeSec = 60
	cfg.RequireConfirm = true
	r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: spawner, Config: cfg})

	results, err := r.CheckAndRotate("test-session", t.TempDir())
	if err != nil {
		t.Fatalf("CheckAndRotate() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("under-age CheckAndRotate() results = %+v, want none", results)
	}
	if len(spawner.getPanesFor) != 0 {
		t.Fatalf("under-age CheckAndRotate() fetched panes %v, want none", spawner.getPanesFor)
	}
	if active := tracker.GetActive(); len(active) != 0 {
		t.Fatalf("under-age CheckAndRotate() emitted alerts %#v, want none", active)
	}

	state := monitor.GetState(agentID)
	if state == nil {
		t.Fatal("registered agent is missing from monitor")
	}
	state.SessionStart = time.Now().Add(-61 * time.Second)

	results, err = r.CheckAndRotate("test-session", t.TempDir())
	if err != nil {
		t.Fatalf("eligible CheckAndRotate() error = %v", err)
	}
	if len(results) != 1 || results[0].State != RotationStatePending {
		t.Fatalf("eligible CheckAndRotate() results = %+v, want one pending rotation", results)
	}
	if active := tracker.GetActive(); len(active) != 1 || active[0].Type != alerts.AlertContextWarning {
		t.Fatalf("eligible CheckAndRotate() alerts = %#v, want one context warning", active)
	}
}

func TestRotateAgentFailureEmitsRotationAlert(t *testing.T) {
	tracker := alerts.GetGlobalTracker()
	clearAlertTracker(tracker)
	t.Cleanup(func() { clearAlertTracker(tracker) })

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	spawner := NewMockPaneSpawner()
	spawner.panesError = errors.New("tmux unavailable")
	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	})

	result := r.rotateAgent("test-session", "test__cc_1", "/tmp")
	if result.State != RotationStateFailed {
		t.Fatalf("rotation state = %s, want failed", result.State)
	}

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("active alerts = %d, want 1", len(active))
	}
	if active[0].Type != alerts.AlertRotationFailed {
		t.Errorf("alert type = %s, want %s", active[0].Type, alerts.AlertRotationFailed)
	}
	if active[0].Session != "test-session" {
		t.Errorf("alert session = %q, want test-session", active[0].Session)
	}
}

func TestRotateAgentSuccessEmitsCompletionAlert(t *testing.T) {
	tracker := alerts.GetGlobalTracker()
	clearAlertTracker(tracker)
	t.Cleanup(func() { clearAlertTracker(tracker) })

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	monitor.RecordMessage("test__cc_1", 1000, 1000)
	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{{
		ID:    "%0",
		Title: "test__cc_1",
		Type:  tmux.AgentClaude,
	}}
	cfg := config.DefaultContextRotationConfig()
	cfg.TryCompactFirst = false
	r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: spawner, Config: cfg})

	result := r.rotateAgent("test-session", "test__cc_1", "/tmp")
	if !result.Success || result.State != RotationStateCompleted {
		t.Fatalf("rotation result = %+v, want completed success", result)
	}

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("active alerts = %d, want 1", len(active))
	}
	if active[0].Type != alerts.AlertRotationComplete {
		t.Errorf("alert type = %s, want %s", active[0].Type, alerts.AlertRotationComplete)
	}
	if active[0].Context["old_agent_id"] != "test__cc_1" {
		t.Errorf("old agent = %v, want test__cc_1", active[0].Context["old_agent_id"])
	}
	if active[0].Context["new_agent_id"] != result.NewAgentID {
		t.Errorf("new agent = %v, want %s", active[0].Context["new_agent_id"], result.NewAgentID)
	}
}

func TestCheckAndRotate_LongSessionResetsReplacementMonitorState(t *testing.T) {
	monitor := NewContextMonitor(DefaultMonitorConfig())
	const agentID = "long-session__cc_1"
	monitor.RegisterAgent(agentID, "%0", "claude-opus-4")
	for i := 0; i < 200; i++ {
		monitor.RecordMessage(agentID, 1000, 1000)
	}

	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{{
		ID:    "%0",
		Title: agentID,
		Type:  tmux.AgentClaude,
	}}
	cfg := config.DefaultContextRotationConfig()
	cfg.RotateThreshold = 0.50
	cfg.TryCompactFirst = false
	cfg.MinSessionAgeSec = 0
	r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: spawner, Config: cfg})

	results, err := r.CheckAndRotate("long-session", t.TempDir())
	if err != nil {
		t.Fatalf("CheckAndRotate() error = %v", err)
	}
	if len(results) != 1 || !results[0].Success || results[0].State != RotationStateCompleted {
		t.Fatalf("CheckAndRotate() results = %+v, want one completed rotation", results)
	}

	replacement := monitor.GetState(results[0].NewAgentID)
	if replacement == nil {
		t.Fatal("replacement agent was not registered with the monitor")
	}
	if replacement.PaneID != results[0].NewPaneID {
		t.Errorf("replacement pane = %q, want %q", replacement.PaneID, results[0].NewPaneID)
	}
	if replacement.MessageCount != 0 || replacement.Estimate != nil {
		t.Errorf("replacement monitor state = %+v, want fresh context state", replacement)
	}

	results, err = r.CheckAndRotate("long-session", t.TempDir())
	if err != nil {
		t.Fatalf("second CheckAndRotate() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("second CheckAndRotate() results = %+v, want no immediate re-rotation", results)
	}
}

func TestManualRotate_HandoffFailurePreservesOriginalAgent(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		name := "replacement_removed"
		if cleanupFails {
			name = "replacement_cleanup_failed"
		}
		t.Run(name, func(t *testing.T) {
			monitor := NewContextMonitor(DefaultMonitorConfig())
			const agentID = "test__cc_1"
			monitor.RegisterAgent(agentID, "%0", "claude-opus-4")
			monitor.RecordMessage(agentID, 1000, 1000)
			monitor.UpdateFromRobotMode(agentID, `{"context_used":180000,"context_limit":200000}`)
			original := *monitor.GetState(agentID)

			spawner := NewMockPaneSpawner()
			spawner.panes = []tmux.Pane{{ID: "%0", Title: agentID, Type: tmux.AgentClaude}}
			spawner.sendPaneError = map[string]error{"%new-pane": errors.New("handoff paste failed")}
			if cleanupFails {
				spawner.killError = errors.New("replacement still running")
			}
			cfg := config.DefaultContextRotationConfig()
			cfg.TryCompactFirst = false
			r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: spawner, Config: cfg})

			result := r.ManualRotate("test", agentID, t.TempDir())
			if result.Success || result.State != RotationStateFailed || result.Method != RotationManual {
				t.Fatalf("ManualRotate() = %+v, want failed manual rotation", result)
			}
			if !strings.Contains(result.Error, "handoff paste failed") || !strings.Contains(result.Error, "original agent preserved") {
				t.Errorf("error = %q, want handoff failure and recovery information", result.Error)
			}
			if len(spawner.killAttempts) != 1 || spawner.killAttempts[0] != "%new-pane" {
				t.Errorf("kill attempts = %v, only the replacement may be removed", spawner.killAttempts)
			}
			if cleanupFails && !strings.Contains(result.Error, "replacement still running") {
				t.Errorf("error = %q, want replacement cleanup failure", result.Error)
			}
			if current := monitor.GetState(agentID); current == nil || *current != original {
				t.Errorf("original monitor state changed after failed handoff: %+v, want %+v", current, original)
			}
			if history := r.GetHistory(); len(history) != 0 {
				t.Errorf("successful rotation history = %+v, want none", history)
			}
		})
	}
}

// GH#251 phase 2: grok relaunch/prompt delivery is first-class, so a mixed
// claude+grok batch now passes rotation preflight and both agents are
// scheduled — the grok member no longer vetoes the batch.
func TestCheckAndRotate_MixedGrokBatchSchedulesBothAgents(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("custom-claude-pane", "%1", "claude-opus-4")
	monitor.RegisterAgent("custom-grok-pane", "%2", "grok-build")
	for i := 0; i < 200; i++ {
		monitor.RecordMessage("custom-claude-pane", 1000, 1000)
		monitor.RecordMessage("custom-grok-pane", 1000, 1000)
	}

	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{
		{ID: "%1", Index: 1, Title: "custom-claude-pane", Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Title: "custom-grok-pane", Type: tmux.AgentGrok},
	}
	cfg := config.DefaultContextRotationConfig()
	cfg.RotateThreshold = 0.50
	cfg.MinSessionAgeSec = 0
	// RequireConfirm keeps the test on the fast pending-rotation path while
	// still driving the batch preflight that used to reject grok.
	cfg.RequireConfirm = true
	r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: spawner, Config: cfg})

	results, err := r.CheckAndRotate("test", "/tmp")
	if err != nil {
		t.Fatalf("CheckAndRotate() error = %v, want mixed grok batch accepted", err)
	}
	if len(results) != 2 {
		t.Fatalf("CheckAndRotate() results = %+v, want both claude and grok scheduled", results)
	}
	for _, result := range results {
		if result.State != RotationStatePending {
			t.Fatalf("result %+v state = %s, want %s", result, result.State, RotationStatePending)
		}
	}
	for _, agentID := range []string{"custom-claude-pane", "custom-grok-pane"} {
		if !r.HasPendingRotation(agentID) {
			t.Fatalf("no pending rotation created for %s", agentID)
		}
	}
	if len(spawner.sentKeys) != 0 || len(spawner.sentBuffers) != 0 || len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
		t.Fatalf("confirmation-gated batch mutated panes: keys=%v buffers=%v spawned=%v killed=%v", spawner.sentKeys, spawner.sentBuffers, spawner.spawnedPanes, spawner.killedPanes)
	}
}

func TestNeedsRotation(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()

	// Register an agent and add enough messages to exceed threshold
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	for i := 0; i < 200; i++ {
		monitor.RecordMessage("test__cc_1", 1000, 1000)
	}

	cfg := config.DefaultContextRotationConfig()
	cfg.RotateThreshold = 0.50 // 50%

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  cfg,
	})

	agents, reason := r.NeedsRotation()
	if len(agents) == 0 {
		t.Errorf("expected agents needing rotation, got none. Reason: %s", reason)
	}
	if !strings.Contains(reason, "above") && !strings.Contains(reason, "threshold") {
		t.Errorf("expected threshold reason, got: %s", reason)
	}
}

func TestNeedsWarning(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()

	// Register an agent and add enough messages to exceed warning threshold
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	for i := 0; i < 100; i++ {
		monitor.RecordMessage("test__cc_1", 1000, 1000)
	}

	cfg := config.DefaultContextRotationConfig()
	cfg.WarningThreshold = 0.30 // 30%

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  cfg,
	})

	agents, reason := r.NeedsWarning()
	if len(agents) == 0 {
		t.Errorf("expected agents needing warning, got none. Reason: %s", reason)
	}
}

func TestNeedsRotation_Disabled(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	cfg := config.DefaultContextRotationConfig()
	cfg.Enabled = false

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Config:  cfg,
	})

	agents, reason := r.NeedsRotation()
	if len(agents) != 0 {
		t.Error("expected no agents when rotation disabled")
	}
	if !strings.Contains(reason, "disabled") {
		t.Errorf("expected disabled reason, got: %s", reason)
	}
}

func TestNeedsRotation_NoMonitor(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{
		Config: config.DefaultContextRotationConfig(),
	})

	agents, reason := r.NeedsRotation()
	if len(agents) != 0 {
		t.Error("expected no agents when no monitor")
	}
	if !strings.Contains(reason, "no monitor") {
		t.Errorf("expected 'no monitor' reason, got: %s", reason)
	}
}

func TestGetHistory(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{
		Config: config.DefaultContextRotationConfig(),
	})

	history := r.GetHistory()
	if len(history) != 0 {
		t.Error("expected empty history initially")
	}
}

func TestClearHistory(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{
		Config: config.DefaultContextRotationConfig(),
	})

	// Manually add an event to history
	r.history = append(r.history, RotationEvent{
		SessionName: "test",
		OldAgentID:  "cc_1",
		NewAgentID:  "cc_1",
		Timestamp:   time.Now(),
	})

	if len(r.GetHistory()) != 1 {
		t.Error("expected 1 event in history")
	}

	r.ClearHistory()

	if len(r.GetHistory()) != 0 {
		t.Error("expected empty history after clear")
	}
}

func TestExtractAgentIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentID string
		want    int
	}{
		{"myproject__cc_1", 1},
		{"myproject__cc_2", 2},
		{"myproject__cod_10", 10},
		{"myproject__gmi_3_variant", 3},
		{"myproject__cod_1_gpt_custom_2026@high", 1},
		{"myproject__cc_2_reviewer_42", 2},
		{"myproject__cc_2oops", 1},
		{"session__nested__cod_7_gpt_2026", 7},
		{"myproject__cc_-1", 1},
		{"invalid", 1},
		{"", 1},
	}

	for _, tt := range tests {
		t.Run(tt.agentID, func(t *testing.T) {
			t.Parallel()
			got := extractAgentIndex(tt.agentID)
			if got != tt.want {
				t.Errorf("extractAgentIndex(%q) = %d, want %d", tt.agentID, got, tt.want)
			}
		})
	}
}

func TestDeriveAgentTypeFromID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentID string
		want    string
	}{
		{"myproject__cc_1", "claude"},
		{"myproject__cod_2", "codex"},
		{"myproject__gmi_3", "gemini"},
		{"myproject__cc_1_opus", "claude"},
		{"my__project__cursor_4", "cursor"},
		{"invalid", "unknown"},
		{"", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.agentID, func(t *testing.T) {
			t.Parallel()
			got := deriveAgentTypeFromID(tt.agentID)
			if got != tt.want {
				t.Errorf("deriveAgentTypeFromID(%q) = %q, want %q", tt.agentID, got, tt.want)
			}
		})
	}
}

func TestAgentTypeShort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentType string
		want      string
	}{
		{"claude", "cc"},
		{"Claude", "cc"},
		{"cc", "cc"},
		{"claude_code", "cc"},
		{"codex", "cod"},
		{"cod", "cod"},
		{"openai-codex", "cod"},
		{"gemini", "gmi"},
		{"gmi", "gmi"},
		{"google-gemini", "gmi"},
		{"ws", "windsurf"},
		{"ollama", "ollama"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			t.Parallel()
			got := agentTypeShort(tt.agentType)
			if got != tt.want {
				t.Errorf("agentTypeShort(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestAgentTypeLong(t *testing.T) {
	t.Parallel()

	tests := []struct {
		shortType string
		want      string
	}{
		{"cc", "claude"},
		{"claude_code", "claude"},
		{"cod", "codex"},
		{"openai-codex", "codex"},
		{"gmi", "gemini"},
		{"google-gemini", "gemini"},
		{"ws", "windsurf"},
		{"ollama", "ollama"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.shortType, func(t *testing.T) {
			t.Parallel()
			got := agentTypeLong(tt.shortType)
			if got != tt.want {
				t.Errorf("agentTypeLong(%q) = %q, want %q", tt.shortType, got, tt.want)
			}
		})
	}
}

func TestRotationResultFormatForDisplay(t *testing.T) {
	t.Parallel()

	successResult := &RotationResult{
		Success:       true,
		OldAgentID:    "test__cc_1",
		NewAgentID:    "test__cc_1",
		Method:        RotationThresholdExceeded,
		State:         RotationStateCompleted,
		SummaryTokens: 500,
		Duration:      5 * time.Second,
	}

	output := successResult.FormatForDisplay()
	if !strings.Contains(output, "✓") {
		t.Error("success output should contain checkmark")
	}
	if !strings.Contains(output, "test__cc_1") {
		t.Error("output should contain agent ID")
	}
	if !strings.Contains(output, "completed") {
		t.Error("output should contain state")
	}

	failResult := &RotationResult{
		Success:    false,
		OldAgentID: "test__cc_1",
		State:      RotationStateFailed,
		Error:      "test error",
	}

	output = failResult.FormatForDisplay()
	if !strings.Contains(output, "✗") {
		t.Error("failure output should contain X mark")
	}
	if !strings.Contains(output, "test error") {
		t.Error("output should contain error message")
	}
}

func TestManualRotate_NoMonitor(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{
		Config: config.DefaultContextRotationConfig(),
	})

	result := r.ManualRotate("test-session", "test__cc_1", "/tmp")
	if result.Success {
		t.Error("expected failure when no monitor")
	}
	if !strings.Contains(result.Error, "no monitor") {
		t.Errorf("expected 'no monitor' error, got: %s", result.Error)
	}
	if result.Method != RotationManual {
		t.Errorf("expected RotationManual method, got: %s", result.Method)
	}
}

func TestManualRotate_NoSpawner(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Config:  config.DefaultContextRotationConfig(),
	})

	result := r.ManualRotate("test-session", "test__cc_1", "/tmp")
	if result.Success {
		t.Error("expected failure when no spawner")
	}
	if !strings.Contains(result.Error, "no spawner") {
		t.Errorf("expected 'no spawner' error, got: %s", result.Error)
	}
	if result.Method != RotationManual {
		t.Errorf("expected RotationManual method, got: %s", result.Method)
	}
}

// GH#251 phase 2: grok passes the rotation capability gate. The mock spawner's
// sendError makes the flow fail at the first post-admission lifecycle step
// (requesting the handoff summary), proving grok reaches the same rotation
// path claude does instead of being refused up front — while keeping the test
// fast and off any real tmux server.
func TestManualRotate_GrokPassesCapabilityGate(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__grok_1", "%7", "grok-build")
	monitor.RecordMessage("test__grok_1", 1000, 1000)

	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{{
		ID:       "%7",
		Title:    "test__grok_1",
		Type:     agent.AgentTypeGrok,
		NTMIndex: 1,
	}}
	spawner.sendError = errors.New("pane input unavailable in test")

	cfg := config.DefaultContextRotationConfig()
	cfg.TryCompactFirst = false // skip compaction wait loops; rotation path only
	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  cfg,
	})
	result := r.ManualRotate("test", "test__grok_1", "/tmp")

	if result.Success || result.State != RotationStateFailed {
		t.Fatalf("ManualRotate() result = %+v, want post-admission failure", result)
	}
	if strings.Contains(result.Error, agent.GrokPhaseOneCapabilityHint) {
		t.Fatalf("ManualRotate() error = %q, grok must not be refused by the capability gate", result.Error)
	}
	if !strings.Contains(result.Error, "failed to request summary") {
		t.Fatalf("ManualRotate() error = %q, want summary-request failure past the capability gate", result.Error)
	}
	if len(spawner.spawnedPanes) != 0 {
		t.Fatalf("failed rotation spawned panes: %v", spawner.spawnedPanes)
	}
	if len(spawner.killedPanes) != 0 {
		t.Fatalf("failed rotation killed panes: %v", spawner.killedPanes)
	}
}

// GH#251 phase 2: grok relaunch is first-class — the spawner resolves a real
// launch command (config override or the official autonomous default) instead
// of failing closed with an empty command. SpawnAgent itself is not driven
// here because it would create a real tmux pane.
func TestDefaultPaneSpawnerGrokCommand(t *testing.T) {
	t.Parallel()

	spawner := NewDefaultPaneSpawner(nil)
	if got := spawner.getAgentCommand("grok-build"); got != config.DefaultAgentTemplates().Grok {
		t.Fatalf("getAgentCommand(grok-build) = %q, want default Grok template", got)
	}

	cfg := &config.Config{}
	cfg.Agents.Grok = "/opt/bin/grok --always-approve --model grok-4"
	custom := NewDefaultPaneSpawner(cfg)
	if got := custom.getAgentCommand("xai_grok_build"); got != cfg.Agents.Grok {
		t.Fatalf("getAgentCommand(xai_grok_build) = %q, want configured %q", got, cfg.Agents.Grok)
	}
}

func TestDefaultPaneSpawnerGetAgentCommand(t *testing.T) {
	t.Parallel()

	// Without config
	spawner := NewDefaultPaneSpawner(nil)
	defaults := config.DefaultAgentTemplates()

	tests := []struct {
		agentType string
		want      string
	}{
		{"claude", defaults.Claude},
		{"claude_code", defaults.Claude},
		{"codex", defaults.Codex},
		{"openai-codex", defaults.Codex},
		{"gemini", defaults.Gemini},
		{"google-gemini", defaults.Gemini},
		{"ws", defaults.Windsurf},
		{"ollama", defaults.Ollama},
		{"oc", defaults.Opencode},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := spawner.getAgentCommand(tt.agentType)
			if got != tt.want {
				t.Errorf("getAgentCommand(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}

	// With custom config
	cfg := &config.Config{}
	cfg.Agents.Claude = "custom-claude"
	cfg.Agents.Codex = "custom-codex"
	cfg.Agents.Gemini = "custom-gemini"
	cfg.Agents.Cursor = "custom-cursor"
	cfg.Agents.Windsurf = "custom-windsurf"
	cfg.Agents.Aider = "custom-aider"
	cfg.Agents.Ollama = "custom-ollama"

	spawner2 := NewDefaultPaneSpawner(cfg)

	if got := spawner2.getAgentCommand("claude"); got != "custom-claude" {
		t.Errorf("expected custom-claude, got %q", got)
	}
	if got := spawner2.getAgentCommand("codex"); got != "custom-codex" {
		t.Errorf("expected custom-codex, got %q", got)
	}
	if got := spawner2.getAgentCommand("gemini"); got != "custom-gemini" {
		t.Errorf("expected custom-gemini, got %q", got)
	}
	if got := spawner2.getAgentCommand("cursor"); got != "custom-cursor" {
		t.Errorf("expected custom-cursor, got %q", got)
	}
	if got := spawner2.getAgentCommand("ws"); got != "custom-windsurf" {
		t.Errorf("expected custom-windsurf, got %q", got)
	}
	if got := spawner2.getAgentCommand("aider"); got != "custom-aider" {
		t.Errorf("expected custom-aider, got %q", got)
	}
	if got := spawner2.getAgentCommand("ollama"); got != "custom-ollama" {
		t.Errorf("expected custom-ollama, got %q", got)
	}
}

func TestSendCompactionCommandToPane_UsesBufferForPrompts(t *testing.T) {
	t.Parallel()

	spawner := NewMockPaneSpawner()
	cmd := CompactionCommand{
		Command:  CompactionPromptTemplate,
		IsPrompt: true,
	}

	if err := sendCompactionCommandToPane(spawner, "%1", cmd); err != nil {
		t.Fatalf("sendCompactionCommandToPane() error = %v", err)
	}

	if got := len(spawner.sentBuffers["%1"]); got != 1 {
		t.Fatalf("buffer sends = %d, want 1", got)
	}
	if got := len(spawner.sentKeys["%1"]); got != 0 {
		t.Fatalf("key sends = %d, want 0", got)
	}
	if got := spawner.sentBuffers["%1"][0]; got != CompactionPromptTemplate {
		t.Fatalf("buffer payload mismatch: got %q", got)
	}
}

func TestSendCompactionCommandToPane_UsesKeysForCommands(t *testing.T) {
	t.Parallel()

	spawner := NewMockPaneSpawner()
	cmd := CompactionCommand{
		Command: "/compact",
	}

	if err := sendCompactionCommandToPane(spawner, "%2", cmd); err != nil {
		t.Fatalf("sendCompactionCommandToPane() error = %v", err)
	}

	if got := len(spawner.sentKeys["%2"]); got != 1 {
		t.Fatalf("key sends = %d, want 1", got)
	}
	if got := len(spawner.sentBuffers["%2"]); got != 0 {
		t.Fatalf("buffer sends = %d, want 0", got)
	}
	if got := spawner.sentKeys["%2"][0]; got != "/compact" {
		t.Fatalf("key payload mismatch: got %q", got)
	}
}

func TestSendRotationPrompt_UsesBuffer(t *testing.T) {
	t.Parallel()

	spawner := NewMockPaneSpawner()
	prompt := SummaryPromptTemplate

	if err := sendRotationPrompt(spawner, "%3", prompt); err != nil {
		t.Fatalf("sendRotationPrompt() error = %v", err)
	}

	if got := len(spawner.sentBuffers["%3"]); got != 1 {
		t.Fatalf("buffer sends = %d, want 1", got)
	}
	if got := len(spawner.sentKeys["%3"]); got != 0 {
		t.Fatalf("key sends = %d, want 0", got)
	}
	if got := spawner.sentBuffers["%3"][0]; got != prompt {
		t.Fatalf("buffer payload mismatch: got %q", got)
	}
}

func TestTryCompaction_PreservesHistoryWhenNativeCommandDoesNotReduceUsage(t *testing.T) {
	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	monitor.RecordMessage("test__cc_1", 500, 500)

	spawner := NewMockPaneSpawner()
	compactor := NewCompactor(monitor, CompactorConfig{
		MinReduction:     0.10,
		BuiltinTimeout:   time.Millisecond,
		SummarizeTimeout: time.Millisecond,
	})

	r := NewRotator(RotatorConfig{
		Monitor:   monitor,
		Spawner:   spawner,
		Compactor: compactor,
		Config:    config.DefaultContextRotationConfig(),
	})

	result := r.tryCompaction("test__cc_1", "%0", tmux.AgentClaude)
	if result == nil {
		t.Fatal("expected compaction result, got nil")
	}
	if result.Success {
		t.Fatal("expected compaction to fail without any context reduction")
	}
	if result.Method != CompactionFailed {
		t.Fatalf("Method = %s, want %s", result.Method, CompactionFailed)
	}
	if !strings.Contains(result.Error, "exhausted") {
		t.Fatalf("Error = %q, want exhausted message", result.Error)
	}

	if got := spawner.sentKeys["%0"]; len(got) != 1 || got[0] != "/compact" {
		t.Fatalf("sentKeys = %#v, want only native /compact", got)
	}
	if got := len(spawner.sentBuffers["%0"]); got != 0 {
		t.Fatalf("buffer sends = %d, want no extra summary turn", got)
	}
}

// =============================================================================
// ToPendingRotation / FromPendingRotation
// =============================================================================

func TestToPendingRotation(t *testing.T) {
	t.Parallel()

	now := time.Now()
	timeout := now.Add(5 * time.Minute)

	stored := &StoredPendingRotation{
		AgentID:        "agent-1",
		SessionName:    "session-1",
		PaneID:         "pane-1",
		ContextPercent: 85.5,
		CreatedAt:      now,
		TimeoutAt:      timeout,
		DefaultAction:  ConfirmRotate,
		WorkDir:        "/data/project",
	}

	pending := stored.ToPendingRotation()

	if pending.AgentID != stored.AgentID {
		t.Errorf("AgentID = %q, want %q", pending.AgentID, stored.AgentID)
	}
	if pending.SessionName != stored.SessionName {
		t.Errorf("SessionName = %q, want %q", pending.SessionName, stored.SessionName)
	}
	if pending.PaneID != stored.PaneID {
		t.Errorf("PaneID = %q, want %q", pending.PaneID, stored.PaneID)
	}
	if pending.ContextPercent != stored.ContextPercent {
		t.Errorf("ContextPercent = %f, want %f", pending.ContextPercent, stored.ContextPercent)
	}
	if !pending.CreatedAt.Equal(stored.CreatedAt) {
		t.Errorf("CreatedAt mismatch")
	}
	if !pending.TimeoutAt.Equal(stored.TimeoutAt) {
		t.Errorf("TimeoutAt mismatch")
	}
	if pending.DefaultAction != stored.DefaultAction {
		t.Errorf("DefaultAction = %q, want %q", pending.DefaultAction, stored.DefaultAction)
	}
	if pending.WorkDir != stored.WorkDir {
		t.Errorf("WorkDir = %q, want %q", pending.WorkDir, stored.WorkDir)
	}
}

func TestFromPendingRotation(t *testing.T) {
	t.Parallel()

	now := time.Now()
	timeout := now.Add(10 * time.Minute)

	pending := &PendingRotation{
		AgentID:        "agent-2",
		SessionName:    "session-2",
		PaneID:         "pane-2",
		ContextPercent: 92.0,
		CreatedAt:      now,
		TimeoutAt:      timeout,
		DefaultAction:  ConfirmCompact,
		WorkDir:        "/home/user/project",
	}

	stored := FromPendingRotation(pending)

	if stored.AgentID != pending.AgentID {
		t.Errorf("AgentID = %q, want %q", stored.AgentID, pending.AgentID)
	}
	if stored.ContextPercent != pending.ContextPercent {
		t.Errorf("ContextPercent = %f, want %f", stored.ContextPercent, pending.ContextPercent)
	}
	if stored.DefaultAction != pending.DefaultAction {
		t.Errorf("DefaultAction = %q, want %q", stored.DefaultAction, pending.DefaultAction)
	}
}

func TestCheckAndRotate_RequireConfirmCreatesPendingRotation(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{{ID: "%0", Title: "test__cc_1", Type: tmux.AgentClaude}}

	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	for i := 0; i < 200; i++ {
		monitor.RecordMessage("test__cc_1", 1000, 1000)
	}

	cfg := config.DefaultContextRotationConfig()
	cfg.RotateThreshold = 0.50
	cfg.RequireConfirm = true
	cfg.MinSessionAgeSec = 0

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  cfg,
	})

	results, err := r.CheckAndRotate("test-session", "/tmp/project")
	if err != nil {
		t.Fatalf("CheckAndRotate() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("CheckAndRotate() returned %d results, want 1", len(results))
	}
	if results[0].State != RotationStatePending {
		t.Fatalf("rotation state = %s, want %s", results[0].State, RotationStatePending)
	}
	if !r.HasPendingRotation("test__cc_1") {
		t.Fatal("expected pending rotation to be tracked in memory")
	}

	pending := r.GetPendingRotation("test__cc_1")
	if pending == nil {
		t.Fatal("expected pending rotation to be retrievable")
	}
	if pending.SessionName != "test-session" {
		t.Fatalf("pending session = %q, want %q", pending.SessionName, "test-session")
	}
	if pending.WorkDir != "/tmp/project" {
		t.Fatalf("pending workdir = %q, want %q", pending.WorkDir, "/tmp/project")
	}
}

func TestProcessExpiredPending_UsesStoredSession(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")

	spawner := NewMockPaneSpawner()
	spawner.panesError = errors.New("boom")

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	})

	pending := &PendingRotation{
		AgentID:       "test__cc_1",
		SessionName:   "stored-session",
		PaneID:        "%0",
		TimeoutAt:     time.Now().Add(-time.Minute),
		DefaultAction: ConfirmRotate,
		WorkDir:       "/stored/workdir",
	}
	r.pending[pending.AgentID] = pending

	r.processExpiredPending("caller-session", "/caller/workdir")

	if len(spawner.getPanesFor) != 1 {
		t.Fatalf("GetPanes called %d times, want 1", len(spawner.getPanesFor))
	}
	if spawner.getPanesFor[0] != "stored-session" {
		t.Fatalf("GetPanes session = %q, want %q", spawner.getPanesFor[0], "stored-session")
	}
	if !r.HasPendingRotation(pending.AgentID) {
		t.Fatal("failed live-pane preflight removed the expired pending rotation")
	}
}

// Prompt delivery support does not establish native compaction support.
// A mixed batch must preserve its choices when one provider cannot compact;
// sending a summarization prompt would not reclaim that provider's context.
func TestProcessExpiredPending_MixedGrokBatchRejectsUnsupportedCompaction(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("custom-claude-pane", "%1", "claude-opus-4")
	monitor.RegisterAgent("custom-grok-pane", "%2", "grok-3")
	monitor.RecordMessage("custom-claude-pane", 1000, 1000)
	monitor.RecordMessage("custom-grok-pane", 1000, 1000)
	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{
		{ID: "%1", Index: 1, Title: "custom-claude-pane", Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Title: "custom-grok-pane", Type: tmux.AgentGrok},
	}
	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Compactor: NewCompactor(monitor, CompactorConfig{
			MinReduction:     0.10,
			BuiltinTimeout:   time.Millisecond,
			SummarizeTimeout: time.Millisecond,
		}),
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	})
	now := time.Now()
	pending := []*PendingRotation{
		{
			AgentID:       "custom-claude-pane",
			SessionName:   "test",
			PaneID:        "%1",
			TimeoutAt:     now.Add(-2 * time.Minute),
			DefaultAction: ConfirmCompact,
			WorkDir:       "/tmp",
		},
		{
			AgentID:       "custom-grok-pane",
			SessionName:   "test",
			PaneID:        "%2",
			TimeoutAt:     now.Add(-time.Minute),
			DefaultAction: ConfirmCompact,
			WorkDir:       "/tmp",
		},
	}
	for _, item := range pending {
		r.pending[item.AgentID] = item
		// Persist with a future timeout (the store's Get filters expired
		// entries) so removal after commit is actually observable.
		stored := clonePendingRotation(item)
		stored.TimeoutAt = now.Add(time.Hour)
		if err := AddPendingRotation(stored); err != nil {
			t.Fatalf("AddPendingRotation(%s) error = %v", item.AgentID, err)
		}
	}

	r.processExpiredPending("caller-session", "/caller/workdir")

	for _, item := range pending {
		if !r.HasPendingRotation(item.AgentID) {
			t.Fatalf("unsupported compaction consumed pending rotation %s", item.AgentID)
		}
		stored, err := GetPendingRotationByID(item.AgentID)
		if err != nil {
			t.Fatalf("GetPendingRotationByID(%s) error = %v", item.AgentID, err)
		}
		if stored == nil {
			t.Fatalf("unsupported compaction removed persisted request %s", item.AgentID)
		}
	}
	if len(spawner.sentKeys) != 0 || len(spawner.sentBuffers) != 0 {
		t.Fatalf("unsupported compaction batch delivered input: keys=%v buffers=%v", spawner.sentKeys, spawner.sentBuffers)
	}
	if len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
		t.Fatalf("compaction batch spawned/killed panes: spawned=%v killed=%v", spawner.spawnedPanes, spawner.killedPanes)
	}
}

func TestProcessExpiredPendingDoesNotActAfterConcurrentPostpone(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	const agentID = "test__cc_1"
	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent(agentID, "%1", "claude-opus-4")

	preflightStarted := make(chan struct{})
	releasePreflight := make(chan struct{})
	var getPanesCalls atomic.Int32
	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{{
		ID:    "%1",
		Index: 1,
		Title: agentID,
		Type:  tmux.AgentClaude,
	}}
	spawner.sendError = errors.New("stale rotation reached pane mutation")
	spawner.getPanesFunc = func(string) ([]tmux.Pane, error) {
		if getPanesCalls.Add(1) == 1 {
			close(preflightStarted)
			<-releasePreflight
		}
		return spawner.panes, nil
	}

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	})
	pending := &PendingRotation{
		AgentID:       agentID,
		SessionName:   "test-session",
		PaneID:        "%1",
		TimeoutAt:     time.Now().Add(-time.Minute),
		DefaultAction: ConfirmRotate,
		WorkDir:       "/tmp",
	}
	r.pending[agentID] = pending
	if err := AddPendingRotation(clonePendingRotation(pending)); err != nil {
		t.Fatalf("AddPendingRotation error = %v", err)
	}

	expiredDone := make(chan struct{})
	go func() {
		defer close(expiredDone)
		r.processExpiredPending("caller-session", "/caller/workdir")
	}()

	select {
	case <-preflightStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("expired rotation preflight did not start")
	}
	result := r.ConfirmRotation(agentID, ConfirmPostpone, 10)
	if !result.Success || result.State != RotationStatePending {
		t.Fatalf("ConfirmRotation(postpone) result = %+v", result)
	}
	close(releasePreflight)
	select {
	case <-expiredDone:
	case <-time.After(2 * time.Second):
		t.Fatal("expired rotation processing did not finish")
	}

	if got := getPanesCalls.Load(); got != 1 {
		t.Fatalf("GetPanes calls = %d, want only the blocked preflight", got)
	}
	if len(spawner.sentKeys) != 0 || len(spawner.sentBuffers) != 0 || len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
		t.Fatalf("stale expired action mutated panes: keys=%v buffers=%v spawned=%v killed=%v",
			spawner.sentKeys, spawner.sentBuffers, spawner.spawnedPanes, spawner.killedPanes)
	}
	inMemory := r.GetPendingRotation(agentID)
	if inMemory == nil || !inMemory.TimeoutAt.After(time.Now().Add(9*time.Minute)) {
		t.Fatalf("postponed in-memory rotation = %+v, want timeout about 10 minutes ahead", inMemory)
	}
	stored, err := GetPendingRotationByID(agentID)
	if err != nil {
		t.Fatalf("GetPendingRotationByID error = %v", err)
	}
	if stored == nil || !stored.TimeoutAt.Equal(inMemory.TimeoutAt) {
		t.Fatalf("persisted postponed rotation = %+v, want timeout %s", stored, inMemory.TimeoutAt)
	}
}

func TestProcessExpiredPending_AllowsIgnoreAndPostpone(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	spawner := NewMockPaneSpawner()
	r := NewRotator(RotatorConfig{Spawner: spawner, Config: config.DefaultContextRotationConfig()})
	now := time.Now()
	r.pending["test__grok_1"] = &PendingRotation{
		AgentID:       "test__grok_1",
		SessionName:   "test",
		TimeoutAt:     now.Add(-time.Minute),
		DefaultAction: ConfirmIgnore,
	}
	r.pending["test__grok_2"] = &PendingRotation{
		AgentID:       "test__grok_2",
		SessionName:   "test",
		TimeoutAt:     now.Add(-time.Minute),
		DefaultAction: ConfirmPostpone,
	}

	r.processExpiredPending("test", "/tmp")

	if r.HasPendingRotation("test__grok_1") {
		t.Fatal("expired ignore action should remove the pending rotation")
	}
	postponed := r.GetPendingRotation("test__grok_2")
	if postponed == nil {
		t.Fatal("expired postpone action should retain the pending rotation")
	}
	if !postponed.TimeoutAt.After(now.Add(29 * time.Minute)) {
		t.Fatalf("postponed timeout = %s, want about 30 minutes in the future", postponed.TimeoutAt)
	}
	if len(spawner.sentKeys) != 0 || len(spawner.sentBuffers) != 0 || len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
		t.Fatalf("ignore/postpone mutated panes: keys=%v buffers=%v spawned=%v killed=%v", spawner.sentKeys, spawner.sentBuffers, spawner.spawnedPanes, spawner.killedPanes)
	}
}

func TestConfirmRotation_PostponeUpdatesTimeout(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	r := NewRotator(RotatorConfig{})
	originalTimeout := time.Now().Add(2 * time.Minute)
	r.pending["agent-1"] = &PendingRotation{
		AgentID:       "agent-1",
		SessionName:   "test-session",
		TimeoutAt:     originalTimeout,
		DefaultAction: ConfirmRotate,
	}

	result := r.ConfirmRotation("agent-1", ConfirmPostpone, 10)
	if !result.Success {
		t.Fatalf("ConfirmRotation(postpone) success = false, error = %q", result.Error)
	}
	if result.State != RotationStatePending {
		t.Fatalf("ConfirmRotation(postpone) state = %s, want %s", result.State, RotationStatePending)
	}

	pending := r.GetPendingRotation("agent-1")
	if pending == nil {
		t.Fatal("expected pending rotation to remain after postpone")
	}
	if !pending.TimeoutAt.After(originalTimeout) {
		t.Fatalf("postponed timeout = %s, want after %s", pending.TimeoutAt, originalTimeout)
	}
}

func TestConfirmRotation_CompactWithoutPaneKeepsPending(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{})
	r.pending["agent-1"] = &PendingRotation{
		AgentID:     "agent-1",
		SessionName: "test-session",
	}

	result := r.ConfirmRotation("agent-1", ConfirmCompact, 0)
	if result.State != RotationStateFailed {
		t.Fatalf("ConfirmRotation(compact) state = %s, want %s", result.State, RotationStateFailed)
	}
	if !strings.Contains(result.Error, "pane ID unknown") {
		t.Fatalf("ConfirmRotation(compact) error = %q", result.Error)
	}
	if !r.HasPendingRotation("agent-1") {
		t.Fatal("expected pending rotation to remain when compaction cannot run")
	}
}

// GH#251 phase 2: a grok pane behind an operator-selected custom title now
// passes the ConfirmRotation capability admission for both rotate and compact,
// so the pending entry is consumed and the action proceeds — the same path a
// claude pane takes. The rotate case fails afterwards for a non-capability
// reason (agent not registered in the monitor) so it stays fast and mutation
// free; the compact case runs to a real compaction attempt against the fakes.
func TestConfirmRotation_CustomTitleGrokAdmitted(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	t.Run("rotate", func(t *testing.T) {
		spawner := NewMockPaneSpawner()
		spawner.panes = []tmux.Pane{{
			ID: "%7", Index: 7, Title: "operator-selected-title", Type: tmux.AgentGrok,
		}}
		r := NewRotator(RotatorConfig{
			Monitor: NewContextMonitor(DefaultMonitorConfig()),
			Spawner: spawner,
			Config:  config.DefaultContextRotationConfig(),
		})
		r.pending["operator-selected-title"] = &PendingRotation{
			AgentID: "operator-selected-title", SessionName: "test-session", PaneID: "%7",
		}

		result := r.ConfirmRotation("operator-selected-title", ConfirmRotate, 0)
		if strings.Contains(result.Error, agent.GrokPhaseOneCapabilityHint) {
			t.Fatalf("ConfirmRotation(rotate) error = %q, grok must not be refused by the capability gate", result.Error)
		}
		if result.State != RotationStateFailed || !strings.Contains(result.Error, "agent not found in monitor") {
			t.Fatalf("ConfirmRotation(rotate) result = %+v, want post-admission monitor failure", result)
		}
		if !r.HasPendingRotation("operator-selected-title") {
			t.Fatal("ConfirmRotation(rotate) lost pending state after failed execution")
		}
		if len(spawner.sentKeys) != 0 || len(spawner.sentBuffers) != 0 || len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
			t.Fatalf("ConfirmRotation(rotate) mutated panes on early failure: %+v", spawner)
		}
	})

	t.Run("compact", func(t *testing.T) {
		monitor := NewContextMonitor(DefaultMonitorConfig())
		monitor.RegisterAgent("operator-selected-title", "%7", "grok-3")
		monitor.RecordMessage("operator-selected-title", 1000, 1000)
		spawner := NewMockPaneSpawner()
		spawner.panes = []tmux.Pane{{
			ID: "%7", Index: 7, Title: "operator-selected-title", Type: tmux.AgentGrok,
		}}
		r := NewRotator(RotatorConfig{
			Monitor: monitor,
			Compactor: NewCompactor(monitor, CompactorConfig{
				MinReduction:     0.10,
				BuiltinTimeout:   time.Millisecond,
				SummarizeTimeout: time.Millisecond,
			}),
			Spawner: spawner,
			Config:  config.DefaultContextRotationConfig(),
		})
		r.pending["operator-selected-title"] = &PendingRotation{
			AgentID: "operator-selected-title", SessionName: "test-session", PaneID: "%7",
		}

		result := r.ConfirmRotation("operator-selected-title", ConfirmCompact, 0)
		if strings.Contains(result.Error, agent.GrokPromptDeliveryCapabilityHint) {
			t.Fatalf("ConfirmRotation(compact) error = %q, grok must not be refused by the capability gate", result.Error)
		}
		if !r.HasPendingRotation("operator-selected-title") {
			t.Fatal("ConfirmRotation(compact) lost pending state for unsupported native compaction")
		}
		if result.Success || !strings.Contains(result.Error, "no compaction commands") || len(spawner.sentBuffers["%7"]) != 0 {
			t.Fatalf("unsupported native compaction changed the conversation: result=%+v buffers=%v", result, spawner.sentBuffers)
		}
		if len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
			t.Fatalf("ConfirmRotation(compact) spawned/killed panes: %+v", spawner)
		}
	})
}

func TestPendingRotationRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Now().Truncate(time.Second) // Truncate for comparison safety
	timeout := now.Add(30 * time.Minute)

	original := &PendingRotation{
		AgentID:        "round-trip-agent",
		SessionName:    "round-trip-session",
		PaneID:         "round-trip-pane",
		ContextPercent: 77.3,
		CreatedAt:      now,
		TimeoutAt:      timeout,
		DefaultAction:  ConfirmIgnore,
		WorkDir:        "/tmp/round-trip",
	}

	stored := FromPendingRotation(original)
	restored := stored.ToPendingRotation()

	if restored.AgentID != original.AgentID {
		t.Errorf("AgentID mismatch after round trip")
	}
	if restored.SessionName != original.SessionName {
		t.Errorf("SessionName mismatch after round trip")
	}
	if restored.ContextPercent != original.ContextPercent {
		t.Errorf("ContextPercent mismatch after round trip")
	}
	if restored.DefaultAction != original.DefaultAction {
		t.Errorf("DefaultAction mismatch after round trip")
	}
	if restored.WorkDir != original.WorkDir {
		t.Errorf("WorkDir mismatch after round trip")
	}
}
