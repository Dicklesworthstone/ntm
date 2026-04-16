// Package watcher provides file watching with debouncing using fsnotify.
// config.go provides helper functions to create watchers from config.
package watcher

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// FileReservationConfigValues holds the values needed to configure a FileReservationWatcher.
// This struct avoids import cycles by using primitive types instead of config.FileReservationConfig.
type FileReservationConfigValues struct {
	Enabled               bool
	AutoReserve           bool
	AutoReleaseIdleMin    int
	NotifyOnConflict      bool
	ExtendOnActivity      bool
	DefaultTTLMin         int
	PollIntervalSec       int
	CaptureLinesForDetect int
	Debug                 bool
}

// NewFileReservationWatcherFromConfig creates a FileReservationWatcher configured
// from the provided config values.
//
// `agentName` is accepted only as a last-resort fallback; the watcher resolves
// the actual registered Agent Mail identity for each pane via
// agentmail.ResolveIdentity (see internal/agentmail/pane_identity.go and
// issue #107). Callers should pass an empty string unless they have a
// specific reason to override.
func NewFileReservationWatcherFromConfig(
	cfg FileReservationConfigValues,
	client *agentmail.Client,
	projectDir string,
	agentName string,
	sessionName string,
	conflictCallback ConflictCallback,
) *FileReservationWatcher {
	if !cfg.Enabled {
		return nil
	}

	// Default resolver: read the canonical pane-identity file written by
	// ntm spawn (the same contract documented in pane_identity.go and
	// mirrored by the mcp-agent-mail Rust reference implementation). If the
	// file does not exist yet — for example because the pane has not yet
	// finished registering with Agent Mail — the resolver returns "" and
	// the watcher skips the reservation attempt rather than sending an
	// unregistered agent_name.
	resolver := func(paneID string) string {
		name, _ := agentmail.ResolveIdentity(projectDir, paneID)
		return name
	}

	opts := []FileReservationWatcherOption{
		WithWatcherClient(client),
		WithProjectDir(projectDir),
		WithAgentName(agentName),
		WithAgentNameResolver(resolver),
		WithSessionFilter(sessionName),
		WithDebug(cfg.Debug),
	}

	// Apply poll interval from config
	if cfg.PollIntervalSec > 0 {
		opts = append(opts, WithReservationPollInterval(time.Duration(cfg.PollIntervalSec)*time.Second))
	}

	// Apply idle timeout from config
	if cfg.AutoReleaseIdleMin > 0 {
		opts = append(opts, WithIdleTimeout(time.Duration(cfg.AutoReleaseIdleMin)*time.Minute))
	}

	// Apply TTL from config
	if cfg.DefaultTTLMin > 0 {
		opts = append(opts, WithReservationTTL(time.Duration(cfg.DefaultTTLMin)*time.Minute))
	}

	// Apply capture lines from config
	if cfg.CaptureLinesForDetect > 0 {
		opts = append(opts, WithCaptureLines(cfg.CaptureLinesForDetect))
	}

	// Apply conflict callback if notification is enabled
	if cfg.NotifyOnConflict && conflictCallback != nil {
		opts = append(opts, WithConflictCallback(conflictCallback))
	}

	return NewFileReservationWatcher(opts...)
}

// DefaultFileReservationConfigValues returns the default values for file reservation config.
// Use this when config is not available or as a fallback.
func DefaultFileReservationConfigValues() FileReservationConfigValues {
	return FileReservationConfigValues{
		Enabled:               true,
		AutoReserve:           true,
		AutoReleaseIdleMin:    10,
		NotifyOnConflict:      true,
		ExtendOnActivity:      true,
		DefaultTTLMin:         15,
		PollIntervalSec:       10,
		CaptureLinesForDetect: 100,
		Debug:                 false,
	}
}
