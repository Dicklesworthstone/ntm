package robot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func productivityContextFixture() productivityDependencies {
	return productivityDependencies{
		sessionExists: func(string) bool { return true },
		getPanes: func(string) ([]tmux.Pane, error) {
			return []tmux.Pane{
				{ID: "%1", Index: 1, Type: tmux.AgentClaude},
				{ID: "%2", Index: 2, Type: tmux.AgentCodex},
				{ID: "%3", Index: 3, Type: tmux.AgentClaude},
			}, nil
		},
		panePath:   func(context.Context, string) string { return "/workspace" },
		processes:  func(context.Context) ([]productivityProcess, error) { return nil, nil },
		readyBeads: func(context.Context, string) (int, error) { return 0, nil },
		now:        time.Now,
	}
}

func TestProductivitySharesObservationDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	deps := productivityContextFixture()
	var observed context.Context
	deps.processes = func(ctx context.Context) ([]productivityProcess, error) {
		observed = ctx
		return nil, nil
	}
	calls := 0
	deps.progressContext = func(ctx context.Context, _ PaneAddr, _ string, _ time.Duration, _ bool, _ time.Time) (*SemanticProgress, bool) {
		calls++
		got, ok := ctx.Deadline()
		if ctx != observed || !ok || !got.Equal(deadline) {
			t.Fatalf("pane received a fresh budget: got %s, want shared %s", got, deadline)
		}
		return &SemanticProgress{EvidenceComplete: true}, true
	}
	out, err := getProductivityWithContext(ctx, ProductivityOptions{Session: "s"}, deps)
	if err != nil || calls != 3 || !out.EvidenceComplete || out.Decision != ProductivityConverged {
		t.Fatalf("shared deadline observation: calls=%d output=%+v err=%v", calls, out, err)
	}
}

func TestProductivityCancellationCannotBecomeConvergence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := productivityContextFixture()
	deps.progressContext = func(context.Context, PaneAddr, string, time.Duration, bool, time.Time) (*SemanticProgress, bool) {
		cancel()
		return &SemanticProgress{EvidenceComplete: true}, true
	}
	out, err := getProductivityWithContext(ctx, ProductivityOptions{Session: "s"}, deps)
	if err != nil || out.EvidenceComplete || out.Decision != ProductivityUnknown {
		t.Fatalf("canceled observation falsely completed: %+v err=%v", out, err)
	}
	// Pre-canceled calls do not even enter metadata dependencies.
	out, err = getProductivityWithContext(ctx, ProductivityOptions{Session: "s"}, productivityDependencies{})
	if out != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled observation: %+v err=%v", out, err)
	}
}

func TestProductivityLiveCollectorDoesNotMultiplyTimeoutByPaneCount(t *testing.T) {
	dir := installSemanticSchemaCommands(t)
	t.Setenv("NTM_TEST_BR_JSON", `[]`)
	t.Setenv("NTM_TEST_GIT_STARTS", filepath.Join(dir, "git-starts"))
	script := "#!/bin/sh\nprintf 'start\\n' >> \"$NTM_TEST_GIT_STARTS\"\nsleep 2\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	deps := productivityContextFixture()
	deps.panePath = func(context.Context, string) string { return dir }
	production := defaultProductivityDependencies()
	if production.progress != nil || production.progressContext == nil {
		t.Fatal("production must select the context-aware attribution path")
	}
	deps.progressContext = production.progressContext
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, err := getProductivityWithContext(ctx, ProductivityOptions{Session: "s"}, deps)
	if err != nil || out.EvidenceComplete || out.Decision != ProductivityUnknown || len(out.Panes) != 3 {
		t.Fatalf("bounded incomplete observation: %+v err=%v", out, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("per-pane reads multiplied the deadline: %s", elapsed)
	}
	starts, err := os.ReadFile(filepath.Join(dir, "git-starts"))
	if err != nil || strings.Count(string(starts), "start") != 1 {
		t.Fatalf("expired panes must not launch more processes: %q, %v", starts, err)
	}
}

func TestReadyBeadCountRequiresRecognizedEvidence(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		count int
		valid bool
	}{
		{`[]`, 0, true},
		{`{"issues":[]}`, 0, true},
		{`[{"id":"bd-1"},{"id":"bd-2"}]`, 2, true},
		{`{"issues":[{"id":"bd-1","status":"open"}]}`, 1, true},
		{`null`, 0, false}, {`{}`, 0, false}, {`{"issues":null}`, 0, false},
		{`[null]`, 0, false}, {`[{}]`, 0, false}, {`[42]`, 0, false},
		{`[{"id":42}]`, 0, false}, {`{"issues":{}}`, 0, false},
		{`[] garbage`, 0, false}, {``, 0, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := decodeReadyBeadCount([]byte(tc.raw))
			if got != tc.count || (err == nil) != tc.valid {
				t.Fatalf("count=%d err=%v, want count=%d valid=%v", got, err, tc.count, tc.valid)
			}
		})
	}
}

func TestReadyBeadCollectorRejectsExitZeroNull(t *testing.T) {
	dir := installSemanticSchemaCommands(t)
	for _, raw := range []string{`null`, `[]`, `{"issues":[]}`} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("NTM_TEST_BR_JSON", raw)
			got, err := readyBeadCount(context.Background(), dir)
			if got != 0 || (err != nil) != (raw == `null`) {
				t.Fatalf("read %s: count=%d error=%v", raw, got, err)
			}
		})
	}
}
