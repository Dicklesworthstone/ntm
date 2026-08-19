// Package tmux provides a wrapper around tmux commands.
package tmux

import (
	"context"
)

// ============== Optimized Capture Helpers ==============
//
// These helpers provide semantic capture operations with appropriate line budgets
// to reduce latency and CPU usage. Use these instead of raw CapturePaneOutput
// with magic numbers.
//
// Line budget guidelines:
// - StatusDetection: 10-20 lines (fast, frequent polling for state changes)
// - HealthCheck: 20-50 lines (moderate analysis for health/error detection)
// - FullContext: 500-2000 lines (rare, expensive full capture for analysis)

// Capture line budget constants for consistent usage across codebase.
const (
	// LinesStatusDetection is the default for quick status/state detection.
	// Use for: agent ready checks, ack polling, interrupt detection.
	LinesStatusDetection = 20

	// LinesHealthCheck is the default for health and error analysis.
	// Use for: error detection, alert generation, process health.
	LinesHealthCheck = 50

	// LinesFullContext is the default for comprehensive context capture.
	// Use for: context estimation, grep, diff, save operations.
	LinesFullContext = 500

	// LinesCheckpoint is for session checkpoint/restore operations.
	// Use for: checkpoint capture, pipeline stages.
	LinesCheckpoint = 2000
)

// CaptureForStatusDetection captures a minimal amount of output for quick state detection.
// This is optimized for frequent polling operations (ack, ready checks, interrupt).
// Uses LinesStatusDetection (20 lines) by default.
func (c *Client) CaptureForStatusDetection(target string) (string, error) {
	return c.CapturePaneOutput(target, LinesStatusDetection)
}

// CaptureForStatusDetectionContext captures with context support for status detection.
func (c *Client) CaptureForStatusDetectionContext(ctx context.Context, target string) (string, error) {
	return c.CapturePaneOutputContext(ctx, target, LinesStatusDetection)
}

// CaptureForStatusDetection captures for status detection (default client).
func CaptureForStatusDetection(target string) (string, error) {
	return DefaultClient.CaptureForStatusDetection(target)
}

// CaptureForStatusDetectionContext captures with context support (default client).
func CaptureForStatusDetectionContext(ctx context.Context, target string) (string, error) {
	return DefaultClient.CaptureForStatusDetectionContext(ctx, target)
}

// CaptureForHealthCheck captures output for health analysis and error detection.
// Uses LinesHealthCheck (50 lines) to balance between detail and performance.
func (c *Client) CaptureForHealthCheck(target string) (string, error) {
	return c.CapturePaneOutput(target, LinesHealthCheck)
}

// CaptureForHealthCheckContext captures with context support for health checks.
func (c *Client) CaptureForHealthCheckContext(ctx context.Context, target string) (string, error) {
	return c.CapturePaneOutputContext(ctx, target, LinesHealthCheck)
}

// CaptureForHealthCheckContext captures with context support (default client).
func CaptureForHealthCheckContext(ctx context.Context, target string) (string, error) {
	return DefaultClient.CaptureForHealthCheckContext(ctx, target)
}

// CaptureForFullContext captures comprehensive output for analysis.
// Uses LinesFullContext (500 lines) for grep, diff, save, and context estimation.
func (c *Client) CaptureForFullContext(target string) (string, error) {
	return c.CapturePaneOutput(target, LinesFullContext)
}

// CaptureForFullContextContext captures with context support for full analysis.
func (c *Client) CaptureForFullContextContext(ctx context.Context, target string) (string, error) {
	return c.CapturePaneOutputContext(ctx, target, LinesFullContext)
}

// CaptureForFullContextContext captures with context support (default client).
func CaptureForFullContextContext(ctx context.Context, target string) (string, error) {
	return DefaultClient.CaptureForFullContextContext(ctx, target)
}

// CaptureForCheckpoint captures maximum output for checkpoint/pipeline operations.
// Uses LinesCheckpoint (2000 lines) for complete session state.
func (c *Client) CaptureForCheckpoint(target string) (string, error) {
	return c.CapturePaneOutput(target, LinesCheckpoint)
}

// CaptureForCheckpointContext captures with context support for checkpoints.
func (c *Client) CaptureForCheckpointContext(ctx context.Context, target string) (string, error) {
	return c.CapturePaneOutputContext(ctx, target, LinesCheckpoint)
}
