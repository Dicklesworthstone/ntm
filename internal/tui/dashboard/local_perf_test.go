package dashboard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestLocalPerfTracker_TokensPerSecond(t *testing.T) {
	t.Parallel()

	tr := newLocalPerfTracker(10 * time.Second)
	now := time.Unix(1000, 0)

	// 50 tokens over ~2 seconds => ~25 tok/s (within some tolerance).
	tr.addOutputDelta(now.Add(1*time.Second), 20)
	tr.addOutputDelta(now.Add(3*time.Second), 30)

	tps, total, _, _ := tr.snapshot()
	if total != 50 {
		t.Fatalf("total=%d, want 50", total)
	}
	if tps <= 10 || tps >= 60 {
		t.Fatalf("tps=%f, want a reasonable value between 10 and 60", tps)
	}
}

func TestLocalPerfTracker_FirstTokenLatency(t *testing.T) {
	t.Parallel()

	tr := newLocalPerfTracker(10 * time.Second)
	sendAt := time.Unix(2000, 0)
	tr.addPrompt(sendAt)

	tr.addOutputDelta(sendAt.Add(1500*time.Millisecond), 1)
	_, _, last, avg := tr.snapshot()

	if last < 1400*time.Millisecond || last > 1600*time.Millisecond {
		t.Fatalf("last=%s, want ~1.5s", last)
	}
	if avg != last {
		t.Fatalf("avg=%s, want %s", avg, last)
	}
}

func TestFetchOllamaPS(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "models": [
    {"name":"codellama:latest","size":123,"size_vram":456},
    {"name":"cpu-model:1b","size":789,"size_vram":0}
  ]
}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mem, err := fetchOllamaPS(ctx, srv.URL)
	if err != nil {
		t.Fatalf("fetchOllamaPS err=%v", err)
	}
	if mem["codellama:latest"] != 456 {
		t.Fatalf("codellama mem=%d, want 456", mem["codellama:latest"])
	}
	if mem["cpu-model:1b"] != 789 {
		t.Fatalf("cpu-model mem=%d, want 789", mem["cpu-model:1b"])
	}
}

func TestNewLocalPerfTracker_DefaultWindow(t *testing.T) {
	t.Parallel()

	tr := newLocalPerfTracker(0)
	if tr.window != 10*time.Second {
		t.Fatalf("window=%s, want 10s", tr.window)
	}
}

func TestLocalPerfTracker_IgnoresInvalidDeltas(t *testing.T) {
	t.Parallel()

	tr := newLocalPerfTracker(5 * time.Second)
	base := time.Unix(3000, 0)

	tr.addOutputDelta(time.Time{}, 10)
	tr.addOutputDelta(base, 0)
	tr.addOutputDelta(base, -1)

	tps, total, _, _ := tr.snapshot()
	if total != 0 || tps != 0 {
		t.Fatalf("expected zero totals/tps, got total=%d tps=%f", total, tps)
	}
}

func TestOllamaHostFromEnv_PriorityAndNormalization(t *testing.T) {
	t.Setenv("NTM_OLLAMA_HOST", "localhost:11434/")
	t.Setenv("OLLAMA_HOST", "http://ignored:11434")
	host := ollamaHostFromEnv()
	if host != "http://localhost:11434" {
		t.Fatalf("host=%q, want http://localhost:11434", host)
	}
}

func TestOllamaHostFromEnv_FallbackToOllamaHost(t *testing.T) {
	t.Setenv("NTM_OLLAMA_HOST", "")
	t.Setenv("OLLAMA_HOST", "https://example.test:1234/")
	host := ollamaHostFromEnv()
	if host != "https://example.test:1234" {
		t.Fatalf("host=%q, want https://example.test:1234", host)
	}
}

func TestFetchOllamaPS_ErrorPaths(t *testing.T) {
	t.Parallel()

	if _, err := fetchOllamaPS(context.Background(), ""); err == nil {
		t.Fatal("expected missing-host error")
	}

	statusSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer statusSrv.Close()
	if _, err := fetchOllamaPS(context.Background(), statusSrv.URL); err == nil {
		t.Fatal("expected non-2xx error")
	}

	jsonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer jsonSrv.Close()
	if _, err := fetchOllamaPS(context.Background(), jsonSrv.URL); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestIsLocalAgentType(t *testing.T) {
	t.Parallel()

	if !isLocalAgentType("ollama") {
		t.Fatal("expected ollama to be local agent type")
	}
	if isLocalAgentType("claude") {
		t.Fatal("did not expect claude to be local agent type")
	}
}

func TestModelEnsureLocalPerfTracker(t *testing.T) {
	t.Parallel()

	m := &Model{}
	if got := m.ensureLocalPerfTracker(""); got != nil {
		t.Fatal("expected nil tracker for empty pane ID")
	}
	first := m.ensureLocalPerfTracker("pane-1")
	second := m.ensureLocalPerfTracker("pane-1")
	if first == nil || second == nil {
		t.Fatal("expected tracker to be created")
	}
	if first != second {
		t.Fatal("expected same tracker instance for same pane ID")
	}
}

func TestModelRefreshOllamaPSIfNeeded_SkipWhenFresh(t *testing.T) {
	t.Parallel()

	m := &Model{lastOllamaPSFetch: time.Now()}
	prev := m.lastOllamaPSFetch

	cmd := m.refreshOllamaPSIfNeeded(time.Now())
	if cmd != nil {
		t.Fatal("expected refresh to be skipped when cache is fresh")
	}
	if !m.lastOllamaPSFetch.Equal(prev) {
		t.Fatal("expected refresh to be skipped when cache is fresh")
	}
}

func TestModelRefreshOllamaPSIfNeeded_SetsErrorOnFailure(t *testing.T) {
	t.Setenv("NTM_OLLAMA_HOST", "http://127.0.0.1:1")
	m := &Model{}
	now := time.Now()
	cmd := m.refreshOllamaPSIfNeeded(now)
	if cmd == nil {
		t.Fatal("expected tea.Cmd")
	}
	msg := cmd()

	switch result := msg.(type) {
	case OllamaPSResultMsg:
		m.ollamaPSError = result.Err
		m.fetchingOllamaPS = false
	default:
		t.Fatalf("unexpected msg type: %T", msg)
	}

	if m.ollamaPSError == nil {
		t.Fatal("expected fetch error")
	}
	if m.lastOllamaPSFetch.IsZero() {
		t.Fatal("expected last fetch timestamp to be set")
	}
}

func TestModelRefreshOllamaPSIfNeeded_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"mistral:7b","size_vram":1234}]}`))
	}))
	defer srv.Close()

	t.Setenv("NTM_OLLAMA_HOST", srv.URL)
	m := &Model{}
	cmd := m.refreshOllamaPSIfNeeded(time.Now())
	if cmd == nil {
		t.Fatal("expected tea.Cmd")
	}
	msg := cmd()

	switch result := msg.(type) {
	case OllamaPSResultMsg:
		m.ollamaPSError = result.Err
		m.fetchingOllamaPS = false
		if result.Err == nil {
			m.ollamaModelMemory = result.Memory
		}
	default:
		t.Fatalf("unexpected msg type: %T", msg)
	}

	if m.ollamaPSError != nil {
		t.Fatalf("unexpected fetch error: %v", m.ollamaPSError)
	}
	if m.ollamaModelMemory == nil || m.ollamaModelMemory["mistral:7b"] != 1234 {
		t.Fatalf("unexpected memory map: %#v", m.ollamaModelMemory)
	}
}

func TestDashboardOllamaPSErrorClearsStalePaneMemory(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.panes = []tmux.Pane{
		{ID: "%1", Index: 0, Title: "session__ollama_1_mistral", Type: tmux.AgentOllama, Variant: "mistral:7b"},
	}
	m.ollamaModelMemory = map[string]int64{"mistral:7b": 1234}
	m.paneStatus[0] = PaneStatus{LocalMemoryBytes: 1234}

	updated, _ := m.Update(OllamaPSResultMsg{Err: errors.New("ollama /api/ps unavailable")})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if next.ollamaPSError == nil {
		t.Fatal("expected ollama ps error to be recorded")
	}
	if next.ollamaModelMemory != nil {
		t.Fatalf("expected stale ollama memory cache to clear, got %#v", next.ollamaModelMemory)
	}
	if next.paneStatus[0].LocalMemoryBytes != 0 {
		t.Fatalf("expected stale pane memory to clear, got %d", next.paneStatus[0].LocalMemoryBytes)
	}
}
