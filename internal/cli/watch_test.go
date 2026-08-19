package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestAncillaryDispatchCancellationPreventsPostReturnEnter(t *testing.T) {
	pane := tmux.Pane{ID: "%9101", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude, Title: "cancel_cc_1"}
	tests := []struct {
		name     string
		dispatch func(context.Context, *dispatchsvc.Service, string, []tmux.Pane, []tmux.Pane, string) (dispatchsvc.Result, error)
	}{
		{name: "file watch", dispatch: dispatchWatchCommand},
		{name: "replay", dispatch: dispatchReplayPrompt},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			staged := make(chan struct{})
			entered := make(chan struct{}, 1)
			service, err := dispatchsvc.NewService(dispatchsvc.Ports{
				Redactor:  dispatchsvc.AllowAllRedactor{},
				Protocols: shellDispatchProtocolPlanner{},
				Deliverer: dispatchsvc.DelivererFunc(func(ctx context.Context, delivery dispatchsvc.Delivery) error {
					if delivery.Protocol != dispatchsvc.ProtocolDoubleEnter {
						return errors.New("expected double-enter protocol")
					}
					close(staged)
					timer := time.NewTimer(80 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-timer.C:
						entered <- struct{}{}
						return nil
					}
				}),
			})
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				_, dispatchErr := tc.dispatch(ctx, service, "cancel", []tmux.Pane{pane}, []tmux.Pane{pane}, "staged prompt")
				done <- dispatchErr
			}()

			select {
			case <-staged:
			case <-time.After(time.Second):
				t.Fatal("delivery never staged the prompt")
			}
			cancel()

			select {
			case dispatchErr := <-done:
				if !errors.Is(dispatchErr, context.Canceled) {
					t.Fatalf("dispatch error = %v, want context.Canceled", dispatchErr)
				}
			case <-time.After(time.Second):
				t.Fatal("dispatch did not return after cancellation")
			}

			select {
			case <-entered:
				t.Fatal("Enter was submitted after canceled dispatch returned")
			case <-time.After(120 * time.Millisecond):
			}
		})
	}
}

func TestParseWatchInterval(t *testing.T) {

	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", input: "", want: 250 * time.Millisecond},
		{name: "duration", input: "2s", want: 2 * time.Second},
		{name: "milliseconds integer", input: "500", want: 500 * time.Millisecond},
		{name: "invalid", input: "abc", wantErr: true},
		{name: "zero invalid", input: "0", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseWatchInterval(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWatchInterval returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("duration = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtractBeadMentions(t *testing.T) {

	re, err := beadMentionRegexp("bd-123")
	if err != nil {
		t.Fatalf("beadMentionRegexp error: %v", err)
	}

	input := "working on bd-123 now\nnoise line\nbd-1234 should not match\nDone with BD-123"
	got := extractBeadMentions(input, re)

	if len(got) != 2 {
		t.Fatalf("mentions count = %d, want 2", len(got))
	}
	if got[0] != "working on bd-123 now" {
		t.Fatalf("first mention = %q", got[0])
	}
	if got[1] != "Done with BD-123" {
		t.Fatalf("second mention = %q", got[1])
	}
}

func TestFilterPanesCanonicalizesAliases(t *testing.T) {

	panes := []tmux.Pane{
		{Index: 0, Type: tmux.AgentUser, Title: "user_0"},
		{Index: 1, Type: tmux.AgentType("claude_code"), Title: "cc_1"},
		{Index: 2, Type: tmux.AgentType("openai-codex"), Title: "cod_2"},
		{Index: 3, Type: tmux.AgentType("google-gemini"), Title: "gmi_3"},
	}

	tests := []struct {
		name string
		opts watchOptions
		want []int
	}{
		{name: "claude alias", opts: watchOptions{filterClaude: true}, want: []int{1}},
		{name: "codex alias", opts: watchOptions{filterCodex: true}, want: []int{2}},
		{name: "gemini alias", opts: watchOptions{filterGemini: true}, want: []int{3}},
		{name: "multiple aliases", opts: watchOptions{filterClaude: true, filterGemini: true}, want: []int{1, 3}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := filterPanes(panes, tc.opts)
			if len(got) != len(tc.want) {
				t.Fatalf("filterPanes(%+v) len = %d, want %d", tc.opts, len(got), len(tc.want))
			}
			for i, wantIdx := range tc.want {
				if got[i].Index != wantIdx {
					t.Fatalf("filterPanes(%+v)[%d].Index = %d, want %d", tc.opts, i, got[i].Index, wantIdx)
				}
			}
		})
	}
}

func TestResolveWatchProjectDir_ExplicitUsesSavedSessionProject(t *testing.T) {
	isolateSessionAgentStorage(t)
	session := fmt.Sprintf("saved-watch-project-%d", time.Now().UnixNano())

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	cfg = &config.Config{ProjectsBase: t.TempDir()}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	actualProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	saveSessionAgentForTest(t, session, actualProject, "GreenCastle")

	got, err := resolveWatchProjectDir(t.Context(), session, false)
	if err != nil {
		t.Fatalf("resolveWatchProjectDir() error = %v", err)
	}
	if got != actualProject {
		t.Fatalf("resolveWatchProjectDir() = %q, want %q", got, actualProject)
	}
}

func TestResolveWatchProjectDir_ExplicitRejectsWorkspaceFallback(t *testing.T) {
	isolateSessionAgentStorage(t)
	session := fmt.Sprintf("missing-watch-project-%d", time.Now().UnixNano())

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	cfg = &config.Config{ProjectsBase: t.TempDir()}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveWatchProjectDir(t.Context(), session, false); err == nil {
		t.Fatal("expected missing project root error")
	}
}

func TestWatchEventMatchesPattern_UsesWatchRootRelativePath(t *testing.T) {

	watchRoot := filepath.Join(string(filepath.Separator), "tmp", "actual-project")
	eventPath := filepath.Join(watchRoot, "internal", "cli", "watch.go")
	if !watchEventMatchesPattern("internal/cli/*.go", watchRoot, eventPath) {
		t.Fatalf("watchEventMatchesPattern should match nested path relative to watch root")
	}
}

func TestWatchEventMatchesPattern_RejectsOutsideWatchRoot(t *testing.T) {

	watchRoot := filepath.Join(string(filepath.Separator), "tmp", "actual-project")
	eventPath := filepath.Join(string(filepath.Separator), "tmp", "other-project", "internal", "cli", "watch.go")
	if watchEventMatchesPattern("internal/cli/*.go", watchRoot, eventPath) {
		t.Fatalf("watchEventMatchesPattern should reject paths outside the watch root")
	}
}

// ============================================================================
// FIX B: Periodic ready-work scan in the watch loop
// ============================================================================

// TestWatchLoop_PeriodicScanFiresWithoutCompletionEvents proves the regression
// fix: a freshly-started watch loop that dispatched nothing at startup (no idle
// agents OR no ready work) must NOT sit inert forever. The periodic ready-work
// scan ticker re-runs the plan/dispatch pass so work that becomes ready later
// (a gate unblocking, new beads, a startup-busy agent going idle) gets picked
// up even though no completion event ever fires.
//
// We inject scanFn to observe the ticker-driven scan without standing up tmux
// or bv: the empty assignment store guarantees the completion detector emits
// nothing, so the scan firing is solely the new ticker path.
func TestWatchLoop_PeriodicScanFiresWithoutCompletionEvents(t *testing.T) {
	isolateSessionAgentStorage(t)

	const session = "fixb"
	store := assignment.NewStore(session) // empty store ⇒ no completion events

	opts := &AutoReassignOptions{Session: session, Quiet: true}
	w := NewWatchLoop(session, store, opts)

	// Tight scan cadence so the test is fast; default completion poll interval
	// is whatever assignWatchInterval is, but the empty store means it never
	// produces an event regardless.
	w.scanInterval = 20 * time.Millisecond

	scanned := make(chan struct{}, 1)
	w.scanFn = func(context.Context) error {
		select {
		case scanned <- struct{}{}:
		default:
		}
		return nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- w.Run(ctx) }()

	select {
	case <-scanned:
		// Ticker-driven scan fired with zero completion events — the fix works.
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("periodic ready-work scan never fired; watch loop is inert without completion events")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("watch loop did not shut down after context cancel")
	}
}

// TestWatchLoop_ScanReadyWorkNilOptsIsNoop guards that scanReadyWork degrades
// safely when no scan options are configured (it must not panic or dispatch).
func TestWatchLoop_ScanReadyWorkNilOptsIsNoop(t *testing.T) {
	isolateSessionAgentStorage(t)
	store := assignment.NewStore("fixb-nil")
	w := NewWatchLoop("fixb-nil", store, &AutoReassignOptions{Session: "fixb-nil", Quiet: true})
	w.scanOpts = nil
	if err := w.scanReadyWork(t.Context()); err != nil {
		t.Fatalf("scanReadyWork with nil scanOpts should be a no-op, got error: %v", err)
	}
}
