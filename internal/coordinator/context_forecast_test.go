package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type forecastTickFixture struct {
	rc          *rotationChecker
	now         time.Time
	panes       []tmux.Pane
	usages      map[int]*ntmctx.TranscriptUsage
	events      []robot.AttentionEvent
	listErr     error
	processRead bool
	cwdReads    int
	captures    int
	mutations   int
}

func newForecastTickFixture() *forecastTickFixture {
	f := &forecastTickFixture{
		now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC),
		panes: []tmux.Pane{{
			ID: "%1", PID: 101, Index: 1, WindowIndex: 0,
			Title: "forecast__cc_1", Type: tmux.AgentClaude,
			Command: "claude", Width: 120,
		}},
		usages:      make(map[int]*ntmctx.TranscriptUsage),
		processRead: true,
	}
	f.rc = &rotationChecker{
		session: "forecast", workDir: "/work", threshold: 95, autoConfirm: true,
		getPanes: func(string) ([]tmux.Pane, error) { return f.panes, f.listErr },
		paneCwd:  func(string) (string, bool) { return "/shared/project", true },
		processUsage: func(_ string, pid int) (*ntmctx.TranscriptUsage, bool) {
			u := f.usages[pid]
			return u, f.processRead && u != nil
		},
		transcriptUsage: func(string, string) (*ntmctx.TranscriptUsage, bool) {
			f.cwdReads++
			return f.usages[101], f.usages[101] != nil
		},
		capturePane: func(string, int) (string, error) {
			f.captures++
			return "✻ Simmering… (esc to interrupt · 12s)\n\n ❯\n", nil
		},
		contextLimit: func(string) (int, bool) { return 100000, true },
		enqueue: func(string, string, float64) *ntmctx.PendingRotation {
			f.mutations++
			return nil
		},
		confirm: func(context.Context, string) ntmctx.RotationResult {
			f.mutations++
			return ntmctx.RotationResult{}
		},
		publishForecast: func(event robot.AttentionEvent) { f.events = append(f.events, event) },
		now:             func() time.Time { return f.now },
	}
	return f
}

func (f *forecastTickFixture) tick(t *testing.T, tokens int) {
	t.Helper()
	for _, pane := range f.panes {
		f.usages[pane.PID] = &ntmctx.TranscriptUsage{
			Path: "/transcripts/" + pane.ID, Model: "test-model", Tokens: tokens,
			ContextWindow: 100000, UpdatedAt: f.now,
		}
	}
	if got := f.rc.runOnce(context.Background()); len(got) != 0 {
		t.Fatalf("advisory made rotation decisions: %+v", got)
	}
	f.now = f.now.Add(30 * time.Second)
}

func TestRotationCheckerForecastThroughTick(t *testing.T) {
	f := newForecastTickFixture()
	f.tick(t, 70000)
	f.tick(t, 72000)
	if len(f.events) != 0 {
		t.Fatal("forecast emitted without sufficient evidence")
	}
	f.tick(t, 74000)
	if len(f.events) != 1 {
		t.Fatalf("got %d events, want one pre-threshold warning", len(f.events))
	}
	event := f.events[0]
	if event.Type != robot.EventTypeAlertWarning || event.Category != robot.EventCategoryAlert ||
		event.ReasonCode != "context_exhaustion_forecast" || event.Source != "coordinator.context_forecast" ||
		event.Session != "forecast" || event.Pane != 1 || event.Severity != robot.SeverityWarning {
		t.Fatalf("wrong event envelope: %+v", event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{
		"current_tokens": 74000, "context_limit": 100000, "usage_percent": 74,
		"tokens_per_minute": 4000, "minutes_to_exhaustion": 6.5,
		"sample_count": 3, "window_minutes": 1, "pane_pid": 101, "window_index": 0,
	} {
		got, ok := wire.Details[key].(float64)
		if !ok || math.Abs(got-want) > 1e-8 {
			t.Errorf("details[%s] = %v, want %v", key, wire.Details[key], want)
		}
	}
	if wire.Details["advisory"] != true || wire.Details["forecast_level"] != "warning" ||
		wire.Details["pane_id"] != "%1" || wire.Details["context_limit_source"] != "transcript" {
		t.Fatalf("missing evidence/provenance: %s", encoded)
	}
	if !strings.Contains(event.Summary, "if observed growth continues") {
		t.Fatalf("forecast presented as certain: %q", event.Summary)
	}
	f.tick(t, 78000)
	if len(f.events) != 2 || f.events[1].Details["forecast_level"] != "urgent" {
		t.Fatalf("urgent forecast missing: %+v", f.events)
	}
	f.tick(t, 80000)
	if len(f.events) != 2 || f.mutations != 0 || f.captures != 0 || f.cwdReads != 0 {
		t.Fatalf("warning duplicated, actuated, or reprobed: events=%d mutations=%d captures=%d cwd=%d",
			len(f.events), f.mutations, f.captures, f.cwdReads)
	}
}

func TestRotationCheckerForecastAttribution(t *testing.T) {
	f := newForecastTickFixture()
	second := f.panes[0]
	second.ID, second.PID, second.Index, second.Title = "%2", 102, 2, "forecast__cc_2"
	f.panes = append(f.panes, second)
	f.processRead = false
	for _, tokens := range []int{70000, 72000, 74000} {
		f.tick(t, tokens)
	}
	if len(f.events) != 0 || f.cwdReads != 0 {
		t.Fatal("ambiguous shared-directory transcripts produced forecasts")
	}
	f.processRead = true
	for _, tokens := range []int{70000, 72000, 74000} {
		f.tick(t, tokens)
	}
	if len(f.events) != 2 || f.events[0].Details["pane_id"] != "%1" || f.events[1].Details["pane_id"] != "%2" {
		t.Fatalf("per-process forecasts were not independent: %+v", f.events)
	}
}

func TestRotationCheckerForecastInvalidatesHistory(t *testing.T) {
	for _, kind := range []string{"list_error", "missing", "dead", "shell", "service", "unknown_pid", "process_change", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			f := newForecastTickFixture()
			f.tick(t, 70000)
			f.tick(t, 72000)
			original := f.panes[0]
			switch kind {
			case "list_error":
				f.listErr = errors.New("tmux unavailable")
			case "missing":
				f.panes = nil
			case "dead":
				f.panes[0].Dead = true
			case "shell":
				f.panes[0].Command = "bash"
			case "service":
				f.panes[0].Service = "coordinator"
			case "unknown_pid":
				f.panes[0].PID = 0
			case "process_change":
				f.panes[0].PID++
			case "canceled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				f.usages[101].Tokens, f.usages[101].UpdatedAt = 74000, f.now
				f.rc.runOnce(ctx)
				if len(f.events) != 0 {
					t.Fatal("canceled tick published a forecast")
				}
				return
			}
			f.tick(t, 74000)
			if len(f.events) != 0 {
				t.Fatalf("invalid/replaced pane produced a forecast: %+v", f.events)
			}
			f.listErr = nil
			f.panes = []tmux.Pane{original}
			f.tick(t, 76000)
			if len(f.events) != 0 || f.mutations != 0 {
				t.Fatal("reappearing pane reused invalidated history")
			}
		})
	}
}

func TestRotationCheckerForecastWiringAndDisabled(t *testing.T) {
	if got := newRotationChecker("forecast", "/work", CoordinatorConfig{}, nil); got != nil {
		t.Fatal("disabled checker created forecast observer")
	}
	rc := newRotationChecker("forecast", "/work", CoordinatorConfig{RotationUsageThreshold: 95}, nil)
	if rc == nil || rc.publishForecast == nil {
		t.Fatal("production checker has no attention-feed forecast publisher")
	}
	f := newForecastTickFixture()
	f.rc.threshold = 0
	for _, tokens := range []int{70000, 72000, 74000} {
		f.tick(t, tokens)
	}
	if len(f.events) != 0 || f.rc.forecaster != nil {
		t.Fatal("disabled checker retained forecasting state")
	}
}

func TestRotationCheckerForecastRequiresRecognizedOrReportedCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		window      int
		start, step int
		want        bool
	}{
		{name: "unknown model cannot borrow display fallback", model: "private-provider/new-model", start: 90000, step: 4000},
		{name: "known model supplies registry capacity", model: "claude-opus-4", start: 140000, step: 4000, want: true},
		{name: "unknown model supplies native capacity", model: "private-provider/new-model", window: 100000, start: 70000, step: 2000, want: true},
		{name: "negative native window is invalid", model: "claude-opus-4", window: -1, start: 140000, step: 4000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newForecastTickFixture()
			production := newRotationChecker("forecast", "/work", CoordinatorConfig{RotationUsageThreshold: 95}, nil)
			f.rc.contextLimit = production.contextLimit
			for i := 0; i < 3; i++ {
				f.usages[101] = &ntmctx.TranscriptUsage{
					Path: "/transcripts/native.jsonl", Model: tc.model,
					Tokens: tc.start + i*tc.step, ContextWindow: tc.window, UpdatedAt: f.now,
				}
				if decisions := f.rc.runOnce(context.Background()); len(decisions) != 0 {
					t.Fatalf("forecast evidence triggered rotation: %+v", decisions)
				}
				f.now = f.now.Add(30 * time.Second)
			}
			if tc.want {
				if len(f.events) != 1 {
					t.Fatalf("verified capacity produced %d forecasts, want one", len(f.events))
				}
				source := "model_registry"
				if tc.window > 0 {
					source = "transcript"
				}
				if f.events[0].Details["context_limit_source"] != source {
					t.Fatalf("capacity source = %v, want %s", f.events[0].Details["context_limit_source"], source)
				}
			} else if len(f.events) != 0 {
				t.Fatalf("unknown capacity produced an exhaustion forecast: %+v", f.events)
			}
			if f.mutations != 0 || f.captures != 0 {
				t.Fatalf("capacity evaluation mutated or captured the pane: mutations=%d captures=%d", f.mutations, f.captures)
			}
		})
	}
}
