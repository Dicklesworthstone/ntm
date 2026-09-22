package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	processObserved := make(chan struct{})
	deps.processes = func(ctx context.Context) ([]productivityProcess, error) {
		observed = ctx
		close(processObserved)
		return nil, nil
	}
	var calls atomic.Int32
	deps.progressContext = func(ctx context.Context, _ PaneAddr, _ string, _ time.Duration, _ bool, _ time.Time) (*SemanticProgress, bool) {
		calls.Add(1)
		<-processObserved
		got, ok := ctx.Deadline()
		if ctx != observed || !ok || !got.Equal(deadline) {
			t.Errorf("pane received a fresh budget: got %s, want shared %s", got, deadline)
		}
		return &SemanticProgress{EvidenceComplete: true}, true
	}
	out, err := getProductivityWithContext(ctx, ProductivityOptions{Session: "s"}, deps)
	if err != nil || calls.Load() != 3 || !out.EvidenceComplete || out.Decision != ProductivityConverged {
		t.Fatalf("shared deadline observation: calls=%d output=%+v err=%v", calls.Load(), out, err)
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
	if count := strings.Count(string(starts), "start"); err != nil || count < 1 || count > 3 {
		t.Fatalf("expected at most one parallel read per pane: %q, %v", starts, err)
	}
}

func TestProductivityCollectsSwarmEvidenceConcurrentlyWithinBound(t *testing.T) {
	const paneCount = 25
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deps := productivityContextFixture()
	panes := make([]tmux.Pane, 0, paneCount+3)
	for i := paneCount; i > 0; i-- {
		panes = append(panes, tmux.Pane{ID: fmt.Sprintf("%%%d", i), Index: i % 5, WindowIndex: i / 5, Type: tmux.AgentClaude})
	}
	panes = append(panes,
		tmux.Pane{ID: "%100", Type: tmux.AgentUser},
		tmux.Pane{ID: "%101", Type: tmux.AgentUnknown},
		tmux.Pane{ID: "%102", Type: tmux.AgentClaude, Service: "cm"},
	)
	deps.getPanes = func(string) ([]tmux.Pane, error) { return panes, nil }
	var pathMu sync.Mutex
	pathCalls := make(map[string]int)
	deps.panePath = func(_ context.Context, id string) string {
		pathMu.Lock()
		pathCalls[id]++
		pathMu.Unlock()
		return "/workspace/" + id
	}
	started := make(chan string, paneCount+2)
	release := make(chan struct{})
	deps.processes = func(ctx context.Context) ([]productivityProcess, error) {
		started <- "processes"
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return []productivityProcess{{pid: 17, cwd: "/workspace/%17", command: "go test ./..."}}, nil
		}
	}
	deps.readyBeads = func(ctx context.Context, dir string) (int, error) {
		if dir != "/workspace/%25" {
			t.Errorf("ready beads project = %q, want first pane project", dir)
		}
		started <- "beads"
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-release:
			return 0, nil
		}
	}
	var active, peak, progressCalls atomic.Int32
	deps.progressContext = func(ctx context.Context, addr PaneAddr, path string, window time.Duration, _ bool, _ time.Time) (*SemanticProgress, bool) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		progressCalls.Add(1)
		started <- "pane"
		select {
		case <-ctx.Done():
			return &SemanticProgress{}, false
		case <-release:
		}
		wantPath := fmt.Sprintf("/workspace/%%%d", addr.Window*5+addr.Pane)
		if path != wantPath {
			t.Errorf("pane %+v got sibling path %q, want %q", addr, path, wantPath)
		}
		return &SemanticProgress{
			Source:           "token",
			Token:            PaneWorkToken(addr.Session, addr.Window, addr.Pane),
			WindowSeconds:    int(window / time.Second),
			EvidenceComplete: true,
		}, true
	}
	type result struct {
		output *ProductivityOutput
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		out, err := getProductivityWithContext(ctx, ProductivityOptions{Session: "swarm"}, deps)
		finished <- result{out, err}
	}()
	// Every independent source must enter before any source is released.
	// A serial collector deadlocks at the first source until ctx expires.
	seen := make(map[string]int)
	for i := 0; i < productivityPaneWorkers+2; i++ {
		select {
		case source := <-started:
			seen[source]++
		case <-ctx.Done():
			<-finished
			t.Fatalf("collection serialized independent sources: started=%v", seen)
		}
	}
	if seen["pane"] != productivityPaneWorkers || seen["processes"] != 1 || seen["beads"] != 1 {
		cancel()
		<-finished
		t.Fatalf("parallel sources = %v, want bounded pane workers plus process and Beads reads", seen)
	}
	close(release)
	got := <-finished
	if got.err != nil || got.output == nil {
		t.Fatalf("getProductivityWithContext: %v", got.err)
	}
	out := got.output
	if !out.EvidenceComplete || out.Decision != ProductivityContinue || len(out.Panes) != paneCount {
		t.Fatalf("swarm collection lost evidence: %+v", out)
	}
	if progressCalls.Load() != paneCount || peak.Load() != productivityPaneWorkers || active.Load() != 0 {
		t.Fatalf("progress calls=%d peak=%d active=%d; want %d calls, peak %d, all joined", progressCalls.Load(), peak.Load(), active.Load(), paneCount, productivityPaneWorkers)
	}
	if len(pathCalls) != paneCount {
		t.Fatalf("queried %d pane paths, want only %d agent panes", len(pathCalls), paneCount)
	}
	for id, count := range pathCalls {
		if count != 1 {
			t.Errorf("pane %s path queried %d times, want once", id, count)
		}
	}
	addresses := make([]string, len(out.Panes))
	for i, pane := range out.Panes {
		addresses[i] = pane.Pane
		if pane.Progress == nil || pane.Progress.Token != "NTM-Pane: swarm/"+pane.Pane {
			t.Errorf("pane %s received sibling evidence: %+v", pane.Pane, pane.Progress)
		}
		if pane.Pane == "3.2" {
			if len(pane.Builds) != 1 || pane.Builds[0].PID != 17 {
				t.Errorf("matching pane lost build: %+v", pane)
			}
		} else if len(pane.Builds) != 0 {
			t.Errorf("sibling pane received build: %+v", pane)
		}
	}
	if !sort.StringsAreSorted(addresses) {
		t.Errorf("pane output is not deterministic: %v", addresses)
	}
}

func TestProductivityCancellationStopsQueuedPaneReadsAndRetainsRows(t *testing.T) {
	const paneCount = 25
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deps := productivityContextFixture()
	deps.getPanes = func(string) ([]tmux.Pane, error) {
		panes := make([]tmux.Pane, paneCount)
		for i := range panes {
			panes[i] = tmux.Pane{ID: fmt.Sprintf("%%%d", i), Index: i, Type: tmux.AgentCodex}
		}
		return panes, nil
	}
	var paths, calls atomic.Int32
	deps.panePath = func(context.Context, string) string {
		paths.Add(1)
		return "/workspace"
	}
	started := make(chan struct{}, paneCount)
	deps.progressContext = func(ctx context.Context, addr PaneAddr, _ string, _ time.Duration, _ bool, _ time.Time) (*SemanticProgress, bool) {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return &SemanticProgress{Source: "none", Token: PaneWorkToken(addr.Session, addr.Window, addr.Pane)}, false
	}
	finished := make(chan *ProductivityOutput, 1)
	go func() {
		out, err := getProductivityWithContext(ctx, ProductivityOptions{Session: "swarm"}, deps)
		if err != nil {
			t.Errorf("getProductivityWithContext: %v", err)
		}
		finished <- out
	}()
	for i := 0; i < productivityPaneWorkers; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			<-finished
			t.Fatal("pane workers did not start concurrently")
		}
	}
	cancel()
	out := <-finished
	if out == nil || out.EvidenceComplete || out.Decision != ProductivityUnknown || len(out.Panes) != paneCount {
		t.Fatalf("cancellation lost panes or invented evidence: %+v", out)
	}
	if paths.Load() != productivityPaneWorkers || calls.Load() != productivityPaneWorkers {
		t.Fatalf("cancellation launched queued reads: paths=%d progress=%d, want %d each", paths.Load(), calls.Load(), productivityPaneWorkers)
	}
	for _, pane := range out.Panes {
		if pane.Progress == nil || pane.Progress.EvidenceComplete || pane.Progress.Token != "NTM-Pane: swarm/"+pane.Pane {
			t.Errorf("canceled pane lost its incomplete evidence: %+v", pane)
		}
	}
}

func TestProductivityUsesFirstUsableProjectDespitePathCompletionOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deps := productivityContextFixture()
	laterPathReady := make(chan struct{})
	deps.panePath = func(ctx context.Context, id string) string {
		switch id {
		case "%1":
			return "   "
		case "%2":
			select {
			case <-ctx.Done():
				return ""
			case <-laterPathReady:
				return "/workspace/first-usable"
			}
		case "%3":
			close(laterPathReady)
			return "/workspace/later"
		default:
			t.Errorf("unexpected pane path lookup: %s", id)
			return ""
		}
	}
	var projectDir string
	deps.readyBeads = func(_ context.Context, dir string) (int, error) {
		projectDir = dir
		return 2, nil
	}
	deps.progressContext = func(_ context.Context, addr PaneAddr, path string, _ time.Duration, _ bool, _ time.Time) (*SemanticProgress, bool) {
		available := strings.TrimSpace(path) != ""
		return &SemanticProgress{Token: PaneWorkToken(addr.Session, addr.Window, addr.Pane), EvidenceComplete: available}, available
	}
	out, err := getProductivityWithContext(ctx, ProductivityOptions{Session: "s"}, deps)
	if err != nil || out == nil {
		t.Fatalf("getProductivityWithContext: %v", err)
	}
	if projectDir != "/workspace/first-usable" || out.ReadyBeadCount != 2 {
		t.Fatalf("completion order changed project selection: dir=%q ready=%d", projectDir, out.ReadyBeadCount)
	}
	if out.EvidenceComplete || out.Decision != ProductivityUnknown || len(out.Panes) != 3 {
		t.Fatalf("missing pane path was hidden: %+v", out)
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
