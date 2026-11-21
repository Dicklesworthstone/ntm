package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type bdBinaryProbeKey struct {
	path    string
	modUnix int64
	size    int64
}

type bdVersionCacheEntry struct {
	version   Version
	checkedAt time.Time
}

type bdDaemonCacheEntry struct {
	supported bool
	checkedAt time.Time
}

const (
	toolVersionCacheTTL = 15 * time.Second
	maxToolCacheEntries = 128
)

var (
	bdVersionCacheMu sync.RWMutex
	bdVersionCache   = map[bdBinaryProbeKey]bdVersionCacheEntry{}
	bdDaemonCache    = map[bdBinaryProbeKey]bdDaemonCacheEntry{}
)

// bdBinaryCacheKey returns a cache key for installed executables outside the
// temp directory so repeated Detect/Version/Capabilities/Health probes within
// one check do not re-exec slow binaries multiple times. Entries expire after
// toolVersionCacheTTL so long-lived daemons re-probe real binary liveness.
// Files under os.TempDir() (e.g., per-test stub scripts) are never cached.
func bdBinaryCacheKey(binary string) (bdBinaryProbeKey, bool) {
	cleanPath := filepath.Clean(binary)
	tmpRoot := filepath.Clean(os.TempDir())
	if strings.HasPrefix(cleanPath, tmpRoot+string(os.PathSeparator)) || cleanPath == tmpRoot {
		return bdBinaryProbeKey{}, false
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() {
		return bdBinaryProbeKey{}, false
	}
	return bdBinaryProbeKey{
		path:    cleanPath,
		modUnix: info.ModTime().UnixNano(),
		size:    info.Size(),
	}, true
}

// cachedToolVersion looks up or populates the TTL-bounded per-binary version
// cache for installed binaries outside os.TempDir(), honoring ctx cancellation.
func cachedToolVersion(ctx context.Context, binaryName string, fetch func(path string) (Version, error)) (Version, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Version{}, err
		}
	}
	if path, err := exec.LookPath(binaryName); err == nil {
		if key, cacheable := bdBinaryCacheKey(path); cacheable {
			bdVersionCacheMu.RLock()
			entry, ok := bdVersionCache[key]
			bdVersionCacheMu.RUnlock()
			if ok && time.Since(entry.checkedAt) < toolVersionCacheTTL {
				return entry.version, nil
			}
			v, err := fetch(path)
			if err == nil && (ctx == nil || ctx.Err() == nil) {
				bdVersionCacheMu.Lock()
				if len(bdVersionCache) >= maxToolCacheEntries {
					clear(bdVersionCache)
				}
				bdVersionCache[key] = bdVersionCacheEntry{version: v, checkedAt: time.Now()}
				bdVersionCacheMu.Unlock()
			}
			return v, err
		}
	}
	return fetch(binaryName)
}

// BDAdapter provides integration with the beads (bd) tool
type BDAdapter struct {
	*BaseAdapter
}

// NewBDAdapter creates a new BD adapter
func NewBDAdapter() *BDAdapter {
	return &BDAdapter{
		BaseAdapter: NewBaseAdapter(ToolBD, "bd"),
	}
}

func (a *BDAdapter) binaryCandidates() []string {
	seen := make(map[string]struct{}, 2)
	candidates := make([]string, 0, 2)

	for _, candidate := range []string{"br", a.BinaryName()} {
		if candidate == "" {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			candidates = append(candidates, path)
		}
	}
	return candidates
}

func (a *BDAdapter) versionFromBinary(ctx context.Context, binary string) (Version, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Version{}, err
		}
	}
	key, cacheable := bdBinaryCacheKey(binary)
	if cacheable {
		bdVersionCacheMu.RLock()
		entry, ok := bdVersionCache[key]
		bdVersionCacheMu.RUnlock()
		if ok && time.Since(entry.checkedAt) < toolVersionCacheTTL {
			return entry.version, nil
		}
	}

	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get beads_rust version from %s: %w", binary, err)
	}

	v, err := ParseStandardVersion(stdout.String())
	if err == nil && cacheable && (ctx == nil || ctx.Err() == nil) {
		bdVersionCacheMu.Lock()
		if len(bdVersionCache) >= maxToolCacheEntries {
			clear(bdVersionCache)
		}
		bdVersionCache[key] = bdVersionCacheEntry{version: v, checkedAt: time.Now()}
		bdVersionCacheMu.Unlock()
	}
	return v, err
}

func (a *BDAdapter) workingBinary(ctx context.Context, candidates []string) (string, Version, error) {
	if len(candidates) == 0 {
		return "", Version{}, ErrToolNotInstalled
	}

	var lastErr error
	for _, candidate := range candidates {
		version, err := a.versionFromBinary(ctx, candidate)
		if err == nil {
			return candidate, version, nil
		}
		lastErr = err
	}

	return candidates[0], Version{}, lastErr
}

// Detect checks if bd is installed
func (a *BDAdapter) Detect() (string, bool) {
	candidates := a.binaryCandidates()
	if len(candidates) == 0 {
		return "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), a.Timeout())
	defer cancel()

	if path, _, err := a.workingBinary(ctx, candidates); err == nil {
		return path, true
	}

	// No candidate responded to --version; report as not working.
	return candidates[0], false
}

// Version returns the installed bd version
func (a *BDAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	path, version, err := a.workingBinary(ctx, a.binaryCandidates())
	if err != nil {
		if path == "" {
			return Version{}, fmt.Errorf("failed to get beads_rust version: %w", err)
		}
		return Version{}, fmt.Errorf("failed to get beads_rust version: %w", err)
	}
	return version, nil
}

// Capabilities returns the list of bd capabilities
func (a *BDAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{CapRobotMode}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	binary, _, err := a.workingBinary(ctx, a.binaryCandidates())
	if err != nil {
		return caps, nil
	}

	if a.supportsDaemonMode(ctx, binary) {
		caps = append(caps, CapDaemonMode)
	}

	return caps, nil
}

func (a *BDAdapter) supportsDaemonMode(ctx context.Context, binary string) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	key, cacheable := bdBinaryCacheKey(binary)
	if cacheable {
		bdVersionCacheMu.RLock()
		entry, ok := bdDaemonCache[key]
		bdVersionCacheMu.RUnlock()
		if ok && time.Since(entry.checkedAt) < toolVersionCacheTTL {
			return entry.supported
		}
	}

	cmd := exec.CommandContext(ctx, binary, "help", "daemon")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	supported := cmd.Run() == nil
	if cacheable && (ctx == nil || ctx.Err() == nil) {
		bdVersionCacheMu.Lock()
		if len(bdDaemonCache) >= maxToolCacheEntries {
			clear(bdDaemonCache)
		}
		bdDaemonCache[key] = bdDaemonCacheEntry{supported: supported, checkedAt: time.Now()}
		bdVersionCacheMu.Unlock()
	}
	return supported
}

// Health checks if bd is functioning correctly
func (a *BDAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	candidates := a.binaryCandidates()
	if len(candidates) == 0 {
		return &HealthStatus{
			Healthy:     false,
			Message:     "beads_rust (br) not installed",
			LastChecked: time.Now(),
		}, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	path, _, err := a.workingBinary(probeCtx, candidates)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("beads_rust at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "beads_rust is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// Info returns complete bd tool information
func (a *BDAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}
