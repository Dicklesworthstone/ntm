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

	// Version compatibility: require a parseable X.Y.Z substring.
	// This avoids silently treating unparseable versions as "compatible".
	versionOK := VersionRegex.MatchString(ver.Raw)

	// Validate default archive location existence. This isn't guaranteed to be
	// the only possible archive location, but it is the canonical default and
	// the integration won't function usefully without *some* indexed archive.
	archivePath := util.ExpandPath(defaultXFArchivePath)
	archiveOK, archiveErr := isDir(archivePath)

	// Check index validity via xf stats (best-effort).
	// If stats returns empty, treat as "not indexed" rather than hard error.
	stats, statsErr := a.GetStats(ctx)
	indexValid := false
	tweetCount := 0
	indexStatus := ""
	dbPath := ""
	if stats != nil {
		tweetCount = stats.TweetCount
		indexStatus = stats.IndexStatus
		dbPath = stats.DatabasePath
		indexValid = xfIndexValid(*stats)
	}

	// If we have a database path, ensure it exists (tighten index validity).
	if indexValid && dbPath != "" {
		if _, err := os.Stat(util.ExpandPath(dbPath)); err != nil {
			indexValid = false
		}
	}

	healthy := versionOK && archiveOK && indexValid
	msg := xfHealthMessage(ver, versionOK, archivePath, archiveOK, archiveErr, indexValid, indexStatus, tweetCount, statsErr)

	return &HealthStatus{
		Healthy:     healthy,
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

func xfIndexValid(stats XFStats) bool {
	// Conservative: require at least one strong signal of an index.
	if stats.TweetCount > 0 || stats.LikeCount > 0 || stats.DMCount > 0 || stats.GrokCount > 0 {
		return true
	}
	if stats.DatabasePath != "" {
		return true
	}
	return xfIndexStatusHealthy(stats.IndexStatus)
}

func xfIndexStatusHealthy(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	if s == "" {
		return false
	}

	// Explicitly unhealthy signals.
	switch {
	case strings.Contains(s, "missing"),
		strings.Contains(s, "not indexed"),
		strings.Contains(s, "invalid"),
		strings.Contains(s, "corrupt"),
		strings.Contains(s, "error"),
		strings.Contains(s, "failed"):
		return false
	}

	// Explicit healthy signals.
	switch {
	case strings.Contains(s, "ok"),
		strings.Contains(s, "ready"),
		strings.Contains(s, "indexed"),
		strings.Contains(s, "healthy"),
		strings.Contains(s, "up-to-date"):
		return true
	}

	// Unknown status: keep conservative.
	return false
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
