package ensemble

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// PaneCapturer captures scrollback from a tmux pane.
// *tmux.Client satisfies this interface.
type PaneCapturer interface {
	CapturePaneOutput(pane string, lines int) (string, error)
}

// PaneSynthesisCollector polls a tmux pane until it contains a parseable
// lead synthesis response or the context expires.
type PaneSynthesisCollector struct {
	Client       PaneCapturer
	PollInterval time.Duration
	MaxLines     int
	Baseline     string
}

// NewPaneSynthesisCollector creates a collector backed by a tmux client.
func NewPaneSynthesisCollector(client *tmux.Client) *PaneSynthesisCollector {
	if client == nil {
		client = tmux.DefaultClient
	}
	return &PaneSynthesisCollector{
		Client:       client,
		PollInterval: DefaultLeadPollInterval,
		MaxLines:     defaultLeadCaptureLines,
	}
}

// CollectSynthesisResponse polls the pane until a parseable synthesis
// response appears, returning the raw pane content that contained it.
func (c *PaneSynthesisCollector) CollectSynthesisResponse(ctx context.Context, paneTarget string) (string, error) {
	if c == nil || c.Client == nil {
		return "", errors.New("pane collector is not configured")
	}
	if strings.TrimSpace(paneTarget) == "" {
		return "", errors.New("pane target is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	interval := c.PollInterval
	if interval <= 0 {
		interval = DefaultLeadPollInterval
	}
	maxLines := c.MaxLines
	if maxLines <= 0 {
		maxLines = defaultLeadCaptureLines
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastErr error
	for {
		raw, err := c.Client.CapturePaneOutput(paneTarget, maxLines)
		if err != nil {
			lastErr = err
		} else {
			candidate := stripBaselineScrollback(raw, c.Baseline)
			if strings.TrimSpace(candidate) == "" {
				lastErr = errors.New("waiting for new synthesis output after baseline")
			} else if _, parseErr := ParseLeadSynthesisResponse(candidate); parseErr == nil {
				return candidate, nil
			} else {
				lastErr = parseErr
			}
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return "", fmt.Errorf("lead pane produced no parseable synthesis output: %w (last: %v)", ctx.Err(), lastErr)
			}
			return "", fmt.Errorf("lead pane produced no parseable synthesis output: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func stripBaselineScrollback(raw, baseline string) string {
	base := strings.TrimRight(baseline, " \t\r\n")
	if base == "" {
		return raw
	}
	rawTrim := strings.TrimRight(raw, " \t\r\n")
	if rawTrim == base {
		return ""
	}
	if strings.HasPrefix(raw, base) {
		return raw[len(base):]
	}
	anchor := base
	if len(anchor) > 200 {
		anchor = anchor[len(anchor)-200:]
	}
	if idx := strings.LastIndex(raw, anchor); idx >= 0 {
		return raw[idx+len(anchor):]
	}
	return raw
}
