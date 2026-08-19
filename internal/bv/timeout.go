package bv

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// BVTimeoutEnvVar overrides the bv command timeout (whole seconds, > 0).
// It wins over the configured [integrations.bv] timeout_seconds value so a
// caller can tune a single invocation without editing config.toml (GH#253).
const BVTimeoutEnvVar = "NTM_BV_TIMEOUT"

// configuredTimeoutSeconds holds the [integrations.bv] timeout_seconds value
// installed by ConfigureCommandTimeout. Zero means "not configured" and falls
// back to DefaultTimeout.
var configuredTimeoutSeconds atomic.Int64

// ConfigureCommandTimeout installs the configured bv command timeout
// ([integrations.bv] timeout_seconds) for every bv subprocess this package
// launches. Non-positive values are ignored so a typo'd config can never
// disable the timeout entirely.
func ConfigureCommandTimeout(seconds int) {
	if seconds > 0 {
		configuredTimeoutSeconds.Store(int64(seconds))
	}
}

// CommandTimeout resolves the effective bv subprocess timeout:
// NTM_BV_TIMEOUT (seconds) > [integrations.bv] timeout_seconds > DefaultTimeout.
// The environment is consulted on every call so wrapper scripts that export
// NTM_BV_TIMEOUT work even on code paths that never load config.
func CommandTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(BVTimeoutEnvVar)); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	if seconds := configuredTimeoutSeconds.Load(); seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return DefaultTimeout
}
