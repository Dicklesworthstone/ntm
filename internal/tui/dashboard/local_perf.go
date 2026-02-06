package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

type tokenEvent struct {
	At     time.Time
	Tokens int
}

// localPerfTracker tracks best-effort performance metrics for "local" agents
// (currently: Ollama panes). The inputs are pane output deltas and prompt history
// timestamps, so these metrics are estimates rather than ground truth.
type localPerfTracker struct {
	window time.Duration

	events []tokenEvent
	total  int

	pendingPromptTimes []time.Time

	latencySum   time.Duration
	latencyCount int

	lastTPS     float64
	lastLatency time.Duration
	avgLatency  time.Duration
}

func newLocalPerfTracker(window time.Duration) *localPerfTracker {
	if window <= 0 {
		window = 10 * time.Second
	}
	return &localPerfTracker{window: window}
}

func (t *localPerfTracker) addPrompt(ts time.Time) {
	if ts.IsZero() {
		return
	}
	t.pendingPromptTimes = append(t.pendingPromptTimes, ts)
}

func (t *localPerfTracker) addOutputDelta(at time.Time, deltaTokens int) {
	if at.IsZero() || deltaTokens <= 0 {
		return
	}

	t.events = append(t.events, tokenEvent{At: at, Tokens: deltaTokens})
	t.total += deltaTokens
	t.prune(at)
	t.lastTPS = t.tokensPerSecond(at)

	// First-token latency: when we see the first output delta after a prompt timestamp.
	if len(t.pendingPromptTimes) > 0 {
		ts := t.pendingPromptTimes[0]
		if !ts.IsZero() && at.After(ts) {
			lat := at.Sub(ts)
			t.pendingPromptTimes = t.pendingPromptTimes[1:]
			t.lastLatency = lat
			t.latencySum += lat
			t.latencyCount++
			t.avgLatency = time.Duration(int64(t.latencySum) / int64(t.latencyCount))
		}
	}
}

func (t *localPerfTracker) prune(now time.Time) {
	if len(t.events) == 0 {
		return
	}
	cutoff := now.Add(-t.window)

	keep := 0
	for ; keep < len(t.events); keep++ {
		if t.events[keep].At.After(cutoff) {
			break
		}
	}
	if keep == 0 {
		return
	}
	t.events = append([]tokenEvent(nil), t.events[keep:]...)
}

func (t *localPerfTracker) tokensPerSecond(at time.Time) float64 {
	if len(t.events) == 0 {
		return 0
	}

	sum := 0
	oldest := t.events[0].At
	for _, ev := range t.events {
		sum += ev.Tokens
		if ev.At.Before(oldest) {
			oldest = ev.At
		}
	}

	span := at.Sub(oldest)
	if span <= 0 {
		span = t.window
	}
	if span > t.window {
		span = t.window
	}

	sec := span.Seconds()
	if sec <= 0.25 {
		sec = 0.25
	}
	return float64(sum) / sec
}

func (t *localPerfTracker) snapshot() (tps float64, total int, lastLatency time.Duration, avgLatency time.Duration) {
	return t.lastTPS, t.total, t.lastLatency, t.avgLatency
}

func (m *Model) ensureLocalPerfTracker(paneID string) *localPerfTracker {
	if paneID == "" {
		return nil
	}
	if m.localPerfByPaneID == nil {
		m.localPerfByPaneID = make(map[string]*localPerfTracker)
	}
	tr := m.localPerfByPaneID[paneID]
	if tr == nil {
		tr = newLocalPerfTracker(10 * time.Second)
		m.localPerfByPaneID[paneID] = tr
	}
	return tr
}

type ollamaPSResponse struct {
	Models []ollamaPSModel `json:"models"`
}

type ollamaPSModel struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	SizeVRAM int64  `json:"size_vram"`
}

func ollamaHostFromEnv() string {
	host := strings.TrimSpace(os.Getenv("NTM_OLLAMA_HOST"))
	if host == "" {
		host = strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	}
	if host == "" {
		host = "http://127.0.0.1:11434"
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}
	return strings.TrimRight(host, "/")
}

func fetchOllamaPS(ctx context.Context, host string) (map[string]int64, error) {
	if host == "" {
		return nil, fmt.Errorf("missing host")
	}

	url := host + "/api/ps"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 750 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("ollama /api/ps: status %s", resp.Status)
	}

	var ps ollamaPSResponse
	if err := json.NewDecoder(resp.Body).Decode(&ps); err != nil {
		return nil, err
	}

	out := make(map[string]int64, len(ps.Models))
	for _, model := range ps.Models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			continue
		}
		mem := model.SizeVRAM
		if mem <= 0 {
			mem = model.Size
		}
		out[name] = mem
	}
	return out, nil
}

func isLocalAgentType(agentType string) bool {
	return strings.EqualFold(agentType, string(agent.AgentTypeOllama))
}

func (m *Model) refreshOllamaPSIfNeeded(now time.Time) {
	// Only refresh occasionally; the dashboard is a TUI and should avoid blocking.
	if !m.lastOllamaPSFetch.IsZero() && now.Sub(m.lastOllamaPSFetch) < 5*time.Second {
		return
	}

	host := ollamaHostFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()

	mem, err := fetchOllamaPS(ctx, host)
	m.lastOllamaPSFetch = now
	if err != nil {
		m.ollamaPSError = err
		return
	}
	m.ollamaPSError = nil
	m.ollamaModelMemory = mem
}
