package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

// XFAdapter provides integration with the XF (X Find) tool.
// XF is a CLI for indexing and searching X/Twitter data archives,
// supporting full-text search with BM25 ranking via Tantivy.
type XFAdapter struct {
	*BaseAdapter
}

// NewXFAdapter creates a new XF adapter
func NewXFAdapter() *XFAdapter {
	return &XFAdapter{
		BaseAdapter: NewBaseAdapter(ToolXF, "xf"),
	}
}

const defaultXFArchivePath = "~/.xf/archive"

// Detect checks if xf is installed
func (a *XFAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed xf version
func (a *XFAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get xf version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns the list of xf capabilities
func (a *XFAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{}

	// Check if xf has specific capabilities by examining help output
	path, installed := a.Detect()
	if !installed {
		return caps, nil
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--help")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run() // Ignore error, just check output

	output := stdout.String()

	// Check for known capabilities
	if strings.Contains(output, "search") {
		caps = append(caps, CapSearch)
	}
	// XF supports JSON output via --output json
	if strings.Contains(output, "output") || strings.Contains(output, "json") {
		caps = append(caps, CapRobotMode)
	}

	return caps, nil
}

// Health checks if xf is functioning correctly
func (a *XFAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "xf not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a basic health check
	ver, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("xf at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	// Tool health = xf is installed and responds with a parseable version.
	// Whether an archive is *indexed* is operational readiness, NOT tool health:
	// a freshly-installed xf, or one whose archive lives at a non-default path
	// (XF_DB / XF_INDEX), is perfectly healthy but has nothing at the canonical
	// ~/.xf/archive location. Previously `ntm doctor` HARD-FAILED on a missing
	// default archive AND probed an index via the removed `xf stats --output json`
	// flag, so a healthy xf permanently reported "health check failed" (#202).
	// Archive/index state is now surfaced as advisory context only.
	versionOK := VersionRegex.MatchString(ver.Raw)

	// Archive presence is a cheap filesystem stat (no exec). Advisory only.
	archivePath := util.ExpandPath(defaultXFArchivePath)
	archiveOK, archiveErr := isDir(archivePath)

	// indexValid/indexStatus/tweetCount/statsErr are not probed here anymore
	// (they required the removed stats JSON flag); pass zero-values so the
	// advisory message reflects "archive presence" without a stale exec.
	msg := xfHealthMessage(ver, versionOK, archivePath, archiveOK, archiveErr, false, "", 0, nil)

	return &HealthStatus{
		Healthy:     versionOK,
		Message:     msg,
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if xf has a specific capability
func (a *XFAdapter) HasCapability(ctx context.Context, cap Capability) bool {
	caps, err := a.Capabilities(ctx)
	if err != nil {
		return false
	}
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}

// Info returns complete xf tool information
func (a *XFAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// XF-specific methods

// XFStats represents archive statistics
type XFStats struct {
	TweetCount   int    `json:"tweet_count,omitempty"`
	LikeCount    int    `json:"like_count,omitempty"`
	DMCount      int    `json:"dm_count,omitempty"`
	GrokCount    int    `json:"grok_count,omitempty"`
	IndexStatus  string `json:"index_status,omitempty"`
	LastIndexed  string `json:"last_indexed,omitempty"`
	DatabasePath string `json:"database_path,omitempty"`
}

// XFSearchResult represents a search result
type XFSearchResult struct {
	ID        string  `json:"id"`
	Content   string  `json:"content"`
	CreatedAt string  `json:"created_at,omitempty"`
	Type      string  `json:"type,omitempty"` // tweet, like, dm, grok
	Score     float64 `json:"score,omitempty"`
}

// GetStats returns archive statistics
func (a *XFAdapter) GetStats(ctx context.Context) (*XFStats, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "stats", "--output", "json")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		// Return empty stats if command fails (no archive indexed)
		return &XFStats{}, nil
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		return &XFStats{}, nil
	}

	var stats XFStats
	if err := json.Unmarshal(output, &stats); err != nil {
		return nil, fmt.Errorf("failed to parse xf stats: %w", err)
	}

	return &stats, nil
}

// Search performs a full-text search on the indexed archive
func (a *XFAdapter) Search(ctx context.Context, query string, limit int) ([]XFSearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	args := []string{"search", query, "--output", "json"}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", limit))
	}

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("xf search failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		return []XFSearchResult{}, nil
	}

	var results []XFSearchResult
	if err := json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("failed to parse xf search results: %w", err)
	}

	return results, nil
}

// Doctor runs xf doctor diagnostics
func (a *XFAdapter) Doctor(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "doctor")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", ErrTimeout
		}
		return "", fmt.Errorf("xf doctor failed: %w: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

func isDir(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("empty path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func xfHealthMessage(
	ver Version,
	versionOK bool,
	archivePath string,
	archiveOK bool,
	archiveErr error,
	indexValid bool,
	indexStatus string,
	tweetCount int,
	statsErr error,
) string {
	parts := []string{"xf"}
	if ver.Raw != "" {
		parts = append(parts, strings.TrimSpace(ver.Raw))
	} else {
		parts = append(parts, ver.String())
	}
	parts = append(parts, fmt.Sprintf("version_ok=%t", versionOK))

	parts = append(parts, fmt.Sprintf("archive=%s", archivePath))
	if !archiveOK {
		if archiveErr != nil {
			parts = append(parts, fmt.Sprintf("archive_ok=false(%s)", archiveErr.Error()))
		} else {
			parts = append(parts, "archive_ok=false")
		}
	} else {
		parts = append(parts, "archive_ok=true")
	}

	parts = append(parts, fmt.Sprintf("index_valid=%t", indexValid))
	if strings.TrimSpace(indexStatus) != "" {
		parts = append(parts, fmt.Sprintf("index_status=%q", strings.TrimSpace(indexStatus)))
	}
	if tweetCount > 0 {
		parts = append(parts, fmt.Sprintf("tweet_count=%d", tweetCount))
	}
	if statsErr != nil {
		parts = append(parts, fmt.Sprintf("stats_err=%q", statsErr.Error()))
	}

	return strings.Join(parts, " ")
}
