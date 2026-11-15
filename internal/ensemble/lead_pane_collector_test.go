package ensemble

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeCapturer scripts successive pane captures.
type fakeCapturer struct {
	captures  []string
	idx       int
	onCapture func(context.Context) (string, error)
}

func (f *fakeCapturer) CapturePaneOutputContext(ctx context.Context, _ string, _ int) (string, error) {
	if f.onCapture != nil {
		return f.onCapture(ctx)
	}
	if f.idx < len(f.captures) {
		out := f.captures[f.idx]
		f.idx++
		return out, nil
	}
	if len(f.captures) == 0 {
		return "", nil
	}
	return f.captures[len(f.captures)-1], nil
}

func TestPaneSynthesisCollectorPollsUntilParseable(t *testing.T) {
	capturer := &fakeCapturer{captures: []string{
		"still thinking...",
		"still thinking...",
		leadAgentResponse,
	}}
	collector := &PaneSynthesisCollector{
		Client:       capturer,
		PollInterval: time.Millisecond,
		MaxLines:     100,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	raw, err := collector.CollectSynthesisResponse(ctx, "%7")
	if err != nil {
		t.Fatalf("CollectSynthesisResponse: %v", err)
	}
	if !strings.Contains(raw, "stage 2 coupling") {
		t.Errorf("returned raw does not contain response: %q", raw)
	}
	if capturer.idx < 3 {
		t.Errorf("expected at least 3 captures, got %d", capturer.idx)
	}
}

func TestPaneSynthesisCollectorTimesOut(t *testing.T) {
	collector := &PaneSynthesisCollector{
		Client:       &fakeCapturer{captures: []string{"never a synthesis"}},
		PollInterval: time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	_, err := collector.CollectSynthesisResponse(ctx, "%7")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "no parseable synthesis output") {
		t.Errorf("error should explain no parseable output: %v", err)
	}
}

func TestPaneSynthesisCollectorPreCanceledNeverCaptures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	capturer := &fakeCapturer{captures: []string{leadAgentResponse}}
	collector := &PaneSynthesisCollector{Client: capturer}
	raw, err := collector.CollectSynthesisResponse(ctx, "%1")
	if raw != "" || !errors.Is(err, context.Canceled) || capturer.idx != 0 {
		t.Fatalf("canceled collector read stale output: raw=%q, err=%v, captures=%d", raw, err, capturer.idx)
	}
}

func TestPaneSynthesisCollectorCancelsBlockedCapture(t *testing.T) {
	capturer := &fakeCapturer{onCapture: func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	collector := &PaneSynthesisCollector{Client: capturer}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	raw, err := collector.CollectSynthesisResponse(ctx, "%1")
	if raw != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked capture lost caller deadline: raw=%q, err=%v", raw, err)
	}
}

func TestPaneSynthesisCollectorIgnoresOutputReturnedAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	collector := &PaneSynthesisCollector{Client: &fakeCapturer{onCapture: func(context.Context) (string, error) {
		cancel()
		return leadAgentResponse, nil
	}}}
	raw, err := collector.CollectSynthesisResponse(ctx, "%1")
	if raw != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled capture reported success: raw=%q, err=%v", raw, err)
	}
}
