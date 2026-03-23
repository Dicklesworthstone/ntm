package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ProxyAdapter provides integration with rust_proxy.
// rust_proxy is a local HTTP proxy with optional failover support.
type ProxyAdapter struct {
	*BaseAdapter
}

// NewProxyAdapter creates a new rust_proxy adapter.
func NewProxyAdapter() *ProxyAdapter {
	return &ProxyAdapter{
		BaseAdapter: NewBaseAdapter(ToolProxy, "rust_proxy"),
	}
}

// Detect checks if rust_proxy is installed.
func (a *ProxyAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed rust_proxy version.
func (a *ProxyAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get rust_proxy version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns the list of rust_proxy capabilities.
func (a *ProxyAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{}

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
	_ = cmd.Run() // Best-effort capability probing.

	output := strings.ToLower(stdout.String())
	if strings.Contains(output, "--json") || strings.Contains(output, "status") {
		caps = append(caps, CapRobotMode)
	}
	if strings.Contains(output, "daemon") {
		caps = append(caps, CapDaemonMode)
	}

	return caps, nil
}

// Health checks if rust_proxy is functioning correctly.
func (a *ProxyAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	availability, err := a.GetAvailability(ctx)
	latency := time.Since(start)
	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     "rust_proxy availability check failed",
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	if availability == nil || !availability.Available {
		return &HealthStatus{
			Healthy:     false,
			Message:     "rust_proxy not installed",
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	if !availability.Compatible {
		return &HealthStatus{
			Healthy:     false,
			Message:     "rust_proxy version incompatible",
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	if !availability.Running {
		return &HealthStatus{
			Healthy:     false,
			Message:     "rust_proxy daemon not running",
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "rust_proxy is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if rust_proxy has a specific capability.
func (a *ProxyAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete rust_proxy tool information.
func (a *ProxyAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// ProxyAvailability summarizes binary/version/daemon status for rust_proxy.
type ProxyAvailability struct {
	Available   bool      `json:"available"`
	Compatible  bool      `json:"compatible"`
	Running     bool      `json:"running"`
	Version     Version   `json:"version,omitempty"`
	Path        string    `json:"path,omitempty"`
	LastChecked time.Time `json:"last_checked"`
	Error       string    `json:"error,omitempty"`
}

// ProxyStatus contains runtime status values returned by rust_proxy status.
type ProxyStatus struct {
	Running        bool                 `json:"running"`
	Version        string               `json:"version,omitempty"`
	ListenPort     int                  `json:"listen_port,omitempty"`
	UptimeSeconds  int64                `json:"uptime_seconds,omitempty"`
	Routes         int                  `json:"routes"`
	RouteStats     []ProxyRouteStatus   `json:"route_stats,omitempty"`
	FailoverEvents []ProxyFailoverEvent `json:"failover_events,omitempty"`
	Errors         int                  `json:"errors"`
}

// ProxyRouteStatus represents per-route traffic and error metrics.
type ProxyRouteStatus struct {
	Domain   string `json:"domain,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	Active   bool   `json:"active"`
	BytesIn  int64  `json:"bytes_in,omitempty"`
	BytesOut int64  `json:"bytes_out,omitempty"`
	Requests int64  `json:"requests,omitempty"`
	Errors   int64  `json:"errors,omitempty"`
}

// ProxyFailoverEvent represents a historical failover event reported by rust_proxy.
type ProxyFailoverEvent struct {
	Timestamp string `json:"timestamp,omitempty"`
	Domain    string `json:"domain,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

var (
	proxyAvailabilityCache  ProxyAvailability
	proxyAvailabilityExpiry time.Time
	proxyAvailabilityMutex  sync.RWMutex
	proxyAvailabilityTTL    = 30 * time.Second
	proxyMinVersion         = Version{Major: 0, Minor: 1, Patch: 0}
	proxyLogger             = slog.Default().With("component", "tools.proxy")
)

// GetAvailability returns whether rust_proxy is available and compatible, with caching.
func (a *ProxyAdapter) GetAvailability(ctx context.Context) (*ProxyAvailability, error) {
	proxyAvailabilityMutex.RLock()
	if time.Now().Before(proxyAvailabilityExpiry) {
		availability := proxyAvailabilityCache
		proxyAvailabilityMutex.RUnlock()
		return &availability, nil
	}
	proxyAvailabilityMutex.RUnlock()

	availability := a.fetchAvailability(ctx)

	proxyAvailabilityMutex.Lock()
	proxyAvailabilityCache = *availability
	proxyAvailabilityExpiry = time.Now().Add(proxyAvailabilityTTL)
	proxyAvailabilityMutex.Unlock()

	return availability, nil
}

// InvalidateAvailabilityCache forces the next GetAvailability call to re-check.
func (a *ProxyAdapter) InvalidateAvailabilityCache() {
	proxyAvailabilityMutex.Lock()
	proxyAvailabilityExpiry = time.Time{}
	proxyAvailabilityMutex.Unlock()
}

// IsAvailable returns true if rust_proxy is installed and version-compatible.
func (a *ProxyAdapter) IsAvailable(ctx context.Context) bool {
	availability, err := a.GetAvailability(ctx)
	if err != nil || availability == nil {
		return false
	}
	return availability.Available && availability.Compatible
}

// GetStatus queries runtime status from rust_proxy.
func (a *ProxyAdapter) GetStatus(ctx context.Context) (*ProxyStatus, error) {
	path, installed := a.Detect()
	if !installed {
		return nil, ErrToolNotInstalled
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	candidates := [][]string{
		{"status", "--json"},
		{"daemon", "status", "--json"},
		{"status"},
	}

	lastOutput := ""
	for _, args := range candidates {
		output, runErr := runProxyStatusCommand(ctx, path, args...)
		if runErr != nil && ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}

		if len(output) == 0 {
			continue
		}

		lastOutput = strings.TrimSpace(string(output))

		status, parseErr := parseProxyStatusOutput(output)
		if parseErr == nil && status != nil {
			return status, nil
		}

		if textStatus := parseProxyStatusText(lastOutput); textStatus != nil {
			return textStatus, nil
		}
	}

	if status := parseProxyStatusText(lastOutput); status != nil {
		return status, nil
	}

	return &ProxyStatus{Running: false, Routes: 0, Errors: 0}, nil
}

func (a *ProxyAdapter) fetchAvailability(ctx context.Context) *ProxyAvailability {
	availability := &ProxyAvailability{
		LastChecked: time.Now(),
	}

	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		availability.Error = err.Error()
		proxyLogger.Debug("rust_proxy binary not found", "error", err)
		return availability
	}

	availability.Available = true
	availability.Path = path

	version, err := a.Version(ctx)
	if err != nil {
		availability.Error = err.Error()
		proxyLogger.Warn("rust_proxy version check failed", "path", path, "error", err)
		return availability
	}

	availability.Version = version
	if !proxyCompatible(version) {
		proxyLogger.Warn("rust_proxy version incompatible", "path", path, "version", version.String(), "min_version", proxyMinVersion.String())
		return availability
	}

	availability.Compatible = true

	status, err := a.GetStatus(ctx)
	if err != nil {
		proxyLogger.Debug("rust_proxy status check failed", "error", err)
		return availability
	}
	if status != nil {
		availability.Running = status.Running
	}

	return availability
}

func proxyCompatible(version Version) bool {
	return version.AtLeast(proxyMinVersion)
}

func runProxyStatusCommand(ctx context.Context, binaryPath string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := strings.TrimSpace(strings.TrimSpace(stdout.String()) + "\n" + strings.TrimSpace(stderr.String()))
	if output == "" {
		return nil, err
	}
	return []byte(output), err
}

func parseProxyStatusOutput(output []byte) (*ProxyStatus, error) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil, fmt.Errorf("empty proxy status output")
	}
	if !json.Valid([]byte(trimmed)) {
		return nil, fmt.Errorf("proxy status output is not valid JSON")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("failed to parse proxy status JSON: %w", err)
	}
	if nested, ok := payload["proxy"].(map[string]interface{}); ok {
		payload = nested
	}

	status := &ProxyStatus{
		Routes: 0,
		Errors: 0,
	}

	if running, ok := jsonBoolField(payload, "running"); ok {
		status.Running = running
	} else if running, ok := jsonBoolField(payload, "daemon_running"); ok {
		status.Running = running
	} else if state, ok := jsonStringField(payload, "status"); ok {
		normalized := strings.ToLower(strings.TrimSpace(state))
		status.Running = normalized == "running" || normalized == "active"
	}

	if version, ok := jsonStringField(payload, "version"); ok {
		status.Version = version
	}
	if listenPort, ok := jsonIntField(payload, "listen_port"); ok {
		status.ListenPort = int(listenPort)
	} else if listenPort, ok := jsonIntField(payload, "port"); ok {
		status.ListenPort = int(listenPort)
	}

	if uptime, ok := jsonIntField(payload, "uptime_seconds"); ok {
		status.UptimeSeconds = uptime
	} else if uptime, ok := jsonIntField(payload, "uptime"); ok {
		status.UptimeSeconds = uptime
	} else if uptime, ok := jsonStringField(payload, "uptime"); ok {
		if dur, err := time.ParseDuration(strings.TrimSpace(uptime)); err == nil {
			status.UptimeSeconds = int64(dur.Seconds())
		}
	}

	if routes, ok := jsonIntField(payload, "routes"); ok {
		status.Routes = int(routes)
	} else if routeList, ok := payload["routes"].([]interface{}); ok {
		status.RouteStats = parseProxyRoutes(routeList)
		status.Routes = len(status.RouteStats)
	} else if routeList, ok := payload["route_stats"].([]interface{}); ok {
		status.RouteStats = parseProxyRoutes(routeList)
		status.Routes = len(status.RouteStats)
	} else if routeCount, ok := jsonIntField(payload, "route_count"); ok {
		status.Routes = int(routeCount)
	}

	if errorsCount, ok := jsonIntField(payload, "errors"); ok {
		status.Errors = int(errorsCount)
	} else if errorCount, ok := jsonIntField(payload, "error_count"); ok {
		status.Errors = int(errorCount)
	} else if errorList, ok := payload["errors"].([]interface{}); ok {
		status.Errors = len(errorList)
	}
	if failover, ok := payload["failover_events"].([]interface{}); ok {
		status.FailoverEvents = parseProxyFailoverEvents(failover)
	}

	return status, nil
}

func parseProxyStatusText(output string) *ProxyStatus {
	normalized := strings.ToLower(strings.TrimSpace(output))
	if normalized == "" {
		return nil
	}

	status := &ProxyStatus{}
	switch {
	case strings.Contains(normalized, "not running"),
		strings.Contains(normalized, "stopped"),
		strings.Contains(normalized, "connection refused"),
		strings.Contains(normalized, "no daemon"):
		status.Running = false
	case strings.Contains(normalized, "running"),
		strings.Contains(normalized, "active"):
		status.Running = true
	default:
		return nil
	}

	if version, err := ParseStandardVersion(output); err == nil && version.Raw != "" {
		status.Version = version.String()
	}

	return status
}

func jsonBoolField(payload map[string]interface{}, key string) (bool, bool) {
	value, ok := payload[key]
	if !ok {
		return false, false
	}
	booleanValue, ok := value.(bool)
	return booleanValue, ok
}

func jsonIntField(payload map[string]interface{}, key string) (int64, bool) {
	value, ok := payload[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func jsonStringField(payload map[string]interface{}, key string) (string, bool) {
	value, ok := payload[key]
	if !ok {
		return "", false
	}
	str, ok := value.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(str), true
}

func parseProxyRoutes(routes []interface{}) []ProxyRouteStatus {
	parsed := make([]ProxyRouteStatus, 0, len(routes))
	for _, route := range routes {
		asMap, ok := route.(map[string]interface{})
		if !ok {
			continue
		}
		entry := ProxyRouteStatus{}
		if domain, ok := jsonStringField(asMap, "domain"); ok {
			entry.Domain = domain
		}
		if upstream, ok := jsonStringField(asMap, "upstream"); ok {
			entry.Upstream = upstream
		}
		if active, ok := jsonBoolField(asMap, "active"); ok {
			entry.Active = active
		}
		if bytesIn, ok := jsonIntField(asMap, "bytes_in"); ok {
			entry.BytesIn = bytesIn
		}
		if bytesOut, ok := jsonIntField(asMap, "bytes_out"); ok {
			entry.BytesOut = bytesOut
		}
		if requests, ok := jsonIntField(asMap, "requests"); ok {
			entry.Requests = requests
		}
		if errorsCount, ok := jsonIntField(asMap, "errors"); ok {
			entry.Errors = errorsCount
		}
		parsed = append(parsed, entry)
	}
	return parsed
}

func parseProxyFailoverEvents(events []interface{}) []ProxyFailoverEvent {
	parsed := make([]ProxyFailoverEvent, 0, len(events))
	for _, event := range events {
		asMap, ok := event.(map[string]interface{})
		if !ok {
			continue
		}
		entry := ProxyFailoverEvent{}
		if timestamp, ok := jsonStringField(asMap, "timestamp"); ok {
			entry.Timestamp = timestamp
		}
		if domain, ok := jsonStringField(asMap, "domain"); ok {
			entry.Domain = domain
		}
		if from, ok := jsonStringField(asMap, "from"); ok {
			entry.From = from
		}
		if to, ok := jsonStringField(asMap, "to"); ok {
			entry.To = to
		}
		if reason, ok := jsonStringField(asMap, "reason"); ok {
			entry.Reason = reason
		}
		parsed = append(parsed, entry)
	}
	return parsed
}
