//go:build ensemble_experimental
// +build ensemble_experimental

package ensemble

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakeCapturer scripts successive pane captures.
type fakeCapturer struct {
	captures []string
	idx      int
}

func (f *fakeCapturer) CapturePaneOutput(_ string, _ int) (string, error) {
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
