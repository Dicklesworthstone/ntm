package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CapabilitySafeRestore is the caam capability that indicates caam's restore path
// is safe for the global ~/.codex/auth.json clobber path: it re-snapshots the
// outgoing profile's live (codex-rotated) tokens before restoring the incoming
// profile, avoiding refresh-token-reuse family revocation (caam #19). ntm refuses
// to perform a *global* caam switch unless caam advertises this capability.
const CapabilitySafeRestore = "safe-restore"

// caamRobotStatus is the subset of `caam robot status --json` we care about. caam
// exposes data.capabilities (e.g. ["safe-restore"]) since caam 0bdd715.
type caamRobotStatus struct {
	Data struct {
		Capabilities []string `json:"capabilities"`
	} `json:"data"`
	// Some caam builds may surface capabilities at the top level; accept both.
	Capabilities []string `json:"capabilities"`
}

// caamCapabilityProber probes caam for advertised capabilities. Injected so the
// gating logic is unit-testable without a live caam binary.
type caamCapabilityProber func(ctx context.Context) ([]string, error)

// defaultCaamCapabilityProber runs `caam robot status --json` and extracts the
// advertised capabilities. A non-zero exit or unparseable output yields an error
// (the caller fails closed).
func defaultCaamCapabilityProber(caamPath string, timeout time.Duration) caamCapabilityProber {
	return func(ctx context.Context) ([]string, error) {
		path := caamPath
		if path == "" {
			path = "caam"
		}
		stdout, stderr, err := runCmdCapture(ctx, timeout, path, "robot", "status", "--json")
		if err != nil {
			return nil, fmt.Errorf("caam robot status: %w (%s)", err, strings.TrimSpace(stderr))
		}
		caps, perr := parseCaamCapabilities(stdout)
		if perr != nil {
			return nil, perr
		}
		return caps, nil
	}
}

// parseCaamCapabilities extracts the capability list from caam robot status JSON,
// accepting either data.capabilities or a top-level capabilities array.
func parseCaamCapabilities(out string) ([]string, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, fmt.Errorf("caam robot status produced empty output")
	}
	if !json.Valid([]byte(trimmed)) {
		return nil, fmt.Errorf("caam robot status output is not valid JSON")
	}
	var st caamRobotStatus
	if err := json.Unmarshal([]byte(trimmed), &st); err != nil {
		return nil, fmt.Errorf("parse caam robot status: %w", err)
	}
	caps := st.Data.Capabilities
	if len(caps) == 0 {
		caps = st.Capabilities
	}
	return caps, nil
}

// hasCapability reports whether name is present in caps (case-insensitive).
func hasCapability(caps []string, name string) bool {
	for _, c := range caps {
		if strings.EqualFold(strings.TrimSpace(c), name) {
			return true
		}
	}
	return false
}

// CaamSupportsSafeRestore probes caam and reports whether it advertises the
// safe-restore capability. ok=false means caam either lacks the capability or
// could not be probed; err is non-nil only on a probe failure (so callers can
// distinguish "definitely unsafe" from "unknown"). Either way, a global clobber
// must be refused unless ok is true.
func (r *AccountRotator) CaamSupportsSafeRestore(ctx context.Context) (ok bool, err error) {
	r.mu.Lock()
	prober := r.caamCapProber
	caam := r.caamPath
	timeout := r.CommandTimeout
	r.mu.Unlock()

	if prober == nil {
		prober = defaultCaamCapabilityProber(caam, timeout)
	}
	caps, perr := prober(ctx)
	if perr != nil {
		return false, perr
	}
	return hasCapability(caps, CapabilitySafeRestore), nil
}
