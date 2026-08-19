package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// CASSAdapter provides integration with Cross-Agent Semantic Search
type CASSAdapter struct {
	*BaseAdapter
}

// NewCASSAdapter creates a new CASS adapter
func NewCASSAdapter() *CASSAdapter {
	return &CASSAdapter{
		BaseAdapter: NewBaseAdapter(ToolCASS, "cass"),
	}
}

// Detect checks if cass is installed
func (a *CASSAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed cass version
func (a *CASSAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get cass version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns cass capabilities
func (a *CASSAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{CapRobotMode, CapSearch}
	return caps, nil
}

// Health checks if cass is functioning
//
// Distinguishes three failure modes that previously all collapsed
// into "not responding":
//
//   - cass not installed        — binary is not on PATH
//   - cass installed, not init  — binary is fine, but the user has
//     not run `cass index --full` yet, so search-backed code paths
//     (dedup, context injection) fail with exit 3. Reported with
//     a one-line install hint instead of a misleading "not
//     responding" message (acfs#266).
//   - cass at <path> not responding — anything else (process crash,
//     missing dependencies, etc.).
func (a *CASSAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "cass not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try health command in JSON mode so we can tell "uninitialized"
	// from "broken" via a structured field instead of guessing from
	// exit code alone.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "health", "--json")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err := cmd.Run()
	latency := time.Since(start)

	// `cass health --json` may emit a JSON body with `initialized:false`
	// AND set a non-zero exit code (3 = "no database — run cass index
	// --full"), so check the JSON before treating the exit code as
	// fatal.
	if body := stdout.Bytes(); len(body) > 0 {
		var parsed cassHealth
		if jerr := json.Unmarshal(body, &parsed); jerr == nil {
			if parsed.Initialized != nil && !*parsed.Initialized {
				return &HealthStatus{
					Healthy:     false,
					Message:     "cass installed but not initialized (run: cass index --full)",
					LastChecked: time.Now(),
					Latency:     latency,
				}, nil
			}
			if !cassHealthIsHealthy(parsed.Status, parsed.Healthy) {
				message := strings.TrimSpace(parsed.Status)
				if message == "" {
					message = "unhealthy"
				}
				if len(parsed.Errors) > 0 {
					message = fmt.Sprintf("%s (%s)", message, strings.Join(parsed.Errors, "; "))
				}
				if parsed.RecommendedAction != "" {
					message = fmt.Sprintf("%s; %s", message, parsed.RecommendedAction)
				}

				// A stale *derived/search* index is cass's own
				// "degraded-derived-assets" state: the canonical archive
				// DB is intact and lexical search still works from the
				// existing (older) index. cass itself frames this as a
				// refresh hint ("run cass index"), not a hard failure
				// (acfs#296). Treat a stale-only blocker as a healthy
				// degraded warning instead of a hard "unhealthy", but
				// only when the index exists and the database opened —
				// so genuine corruption, a missing index, or a DB open
				// failure still fail closed.
				if cassDegradedByStaleIndexOnly(parsed.Errors, parsed.State.indexStaleOnly()) {
					return &HealthStatus{
						Healthy:     true,
						Message:     fmt.Sprintf("cass degraded: %s", message),
						LastChecked: time.Now(),
						Latency:     latency,
					}, nil
				}

				return &HealthStatus{
					Healthy:     false,
					Message:     fmt.Sprintf("cass reports unhealthy: %s", message),
					LastChecked: time.Now(),
					Latency:     latency,
				}, nil
			}
		}
	}

	if err != nil {
		// Exit code 3 with no parseable JSON body still means
		// "uninitialized" per cass --help.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
			return &HealthStatus{
				Healthy:     false,
				Message:     "cass installed but not initialized (run: cass index --full)",
				LastChecked: time.Now(),
				Latency:     latency,
			}, nil
		}
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("cass at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "cass is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

func cassHealthIsHealthy(status string, healthy *bool) bool {
	if healthy != nil {
		return *healthy
	}

	normalized := strings.ToLower(strings.TrimSpace(status))
	switch normalized {
	case "healthy", "ok", "ready":
		return true
	case "unhealthy", "degraded", "error", "not_initialized", "not-initialized":
		return false
	}

	// When `healthy` is absent, fail closed for unknown non-empty status
	// values so future schema/status additions do not get treated as healthy.
	if normalized != "" {
		return false
	}

	// Empty status + missing healthy should fail closed. A schema-less
	// success body does not prove cass is healthy.
	return false
}

// cassIndexState mirrors the `state.index` block of `cass health --json`.
type cassIndexState struct {
	Exists *bool  `json:"exists"`
	Status string `json:"status"`
	Stale  *bool  `json:"stale"`
}

// cassDBState mirrors the `state.database` block of `cass health --json`.
type cassDBState struct {
	Exists *bool `json:"exists"`
	Opened *bool `json:"opened"`
}

// cassStateBlock mirrors the `state` block of `cass health --json`.
type cassStateBlock struct {
	Index    *cassIndexState `json:"index"`
	Database *cassDBState    `json:"database"`
}

// cassHealth is the subset of `cass health --json` that ntm relies on for its
// dependency-health verdict.
type cassHealth struct {
	Status            string          `json:"status"`
	Healthy           *bool           `json:"healthy"`
	Initialized       *bool           `json:"initialized"`
	Errors            []string        `json:"errors"`
	Warnings          []string        `json:"warnings"`
	RecommendedAction string          `json:"recommended_action"`
	State             *cassStateBlock `json:"state"`
}

// cassStaleErrorRe matches the soft "derived index needs a refresh" advisories
// that cass surfaces in its `errors` array when the only blocker is a stale or
// pending lexical/search index. These are degraded-but-functional conditions —
// the canonical archive is intact and lexical search still works — so they must
// not be conflated with hard failures (corruption, missing index, DB open
// failure). Matching is conservative: any error that is NOT one of these soft
// advisories keeps cass classified as unhealthy.
var cassStaleErrorRe = regexp.MustCompile(`(?i)\b(stale|out[ -]?of[ -]?date|needs?[ -]refresh|pending)\b`)

// cassDegradedByStaleIndexOnly reports whether cass's unhealthy verdict is
// caused solely by a stale derived/search index. It requires BOTH that every
// reported error is a recognized soft staleness advisory AND that the
// structured index/database state confirms the index exists and the DB opened
// (stateConfirmsStaleOnly). Either guard failing keeps the verdict as
// hard-unhealthy so genuine corruption never gets downgraded to a warning.
func cassDegradedByStaleIndexOnly(errs []string, stateConfirmsStaleOnly bool) bool {
	if !stateConfirmsStaleOnly {
		return false
	}
	if len(errs) == 0 {
		// No explicit errors but the structured state still says
		// "exists + stale + db open": a refresh-only degradation.
		return true
	}
	for _, e := range errs {
		trimmed := strings.TrimSpace(e)
		if trimmed == "" {
			continue
		}
		if !cassStaleErrorRe.MatchString(trimmed) {
			return false
		}
	}
	return true
}

// indexStaleOnly inspects cass's structured `state` block and returns true only
// when the lexical index physically exists, is flagged stale, and the canonical
// database both exists and opened. A nil/partial state, a missing index, or a
// database that failed to open all return false so we fail closed.
func (s *cassStateBlock) indexStaleOnly() bool {
	if s == nil || s.Index == nil || s.Database == nil {
		return false
	}
	if !boolDeref(s.Index.Exists) {
		return false
	}
	if !boolDeref(s.Index.Stale) {
		return false
	}
	if !boolDeref(s.Database.Exists) {
		return false
	}
	if !boolDeref(s.Database.Opened) {
		return false
	}
	return true
}

func boolDeref(b *bool) bool {
	return b != nil && *b
}

// HasCapability checks if cass has a specific capability
func (a *CASSAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete cass tool information
func (a *CASSAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// CASS-specific methods

// Search performs a semantic search across agent conversations
func (a *CASSAdapter) Search(ctx context.Context, query string, limit int) (json.RawMessage, error) {
	args := []string{"search", "--robot", fmt.Sprintf("--limit=%d", limit), "--", query}
	return a.runCommand(ctx, args...)
}

// GetCapabilities returns cass capabilities info
func (a *CASSAdapter) GetCapabilities(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "capabilities", "--json")
}

// runCommand executes a cass command and returns raw JSON
func (a *CASSAdapter) runCommand(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	cmd.WaitDelay = time.Second

	// Limit output to 10MB
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		if strings.Contains(err.Error(), ErrOutputLimitExceeded.Error()) {
			return nil, fmt.Errorf("cass output exceeded 10MB limit")
		}
		return nil, fmt.Errorf("cass failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if len(output) > 0 && !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from cass", ErrSchemaValidation)
	}

	return output, nil
}
