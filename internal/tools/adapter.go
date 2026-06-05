// Package tools provides a unified adapter framework for external ecosystem tools.
// It standardizes tool detection, version checking, capability discovery, and health monitoring
// across all tools in the NTM ecosystem (bv, bd, am, cm, cass, s2p).
package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Common errors returned by tool adapters
var (
	ErrToolNotInstalled    = errors.New("tool not installed")
	ErrToolNotHealthy      = errors.New("tool not healthy")
	ErrTimeout             = errors.New("operation timed out")
	ErrSchemaValidation    = errors.New("schema validation failed")
	ErrCapabilityMissing   = errors.New("capability not available")
	ErrOutputLimitExceeded = errors.New("output limit exceeded")
)

// VersionRegex matches semantic version strings like "0.31.0"
var VersionRegex = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ParseStandardVersion extracts version from command output using VersionRegex.
// It handles standard semantic version formats (X.Y.Z).
func ParseStandardVersion(output string) (Version, error) {
	output = strings.TrimSpace(output)
	matches := VersionRegex.FindStringSubmatch(output)
	if len(matches) < 4 {
		return Version{Raw: output}, nil
	}

	var major, minor, patch int
	fmt.Sscanf(matches[1], "%d", &major)
	fmt.Sscanf(matches[2], "%d", &minor)
	fmt.Sscanf(matches[3], "%d", &patch)

	return Version{
		Major: major,
		Minor: minor,
		Patch: patch,
		Raw:   output,
	}, nil
}

// parseVersion is the generic version parser that extracts X.Y.Z from any string.
// It is used by tools with simple version output formats (GIIL, RU, XF, DCG, UBS, etc.).
func parseVersion(output string) (Version, error) {
	return ParseStandardVersion(output)
}

// parseACFSVersion extracts version from ACFS output.
// ACFS uses simple "X.Y.Z" format, so we delegate to the standard parser.
func parseACFSVersion(output string) (Version, error) {
	return ParseStandardVersion(output)
}

// LimitedBuffer is a bytes.Buffer that errors on overflow.
// It is used to prevent OOM when capturing command output.
type LimitedBuffer struct {
	bytes.Buffer
	Limit int
}

// NewLimitedBuffer creates a new LimitedBuffer with the specified limit.
func NewLimitedBuffer(limit int) *LimitedBuffer {
	return &LimitedBuffer{Limit: limit}
}

func (b *LimitedBuffer) Write(p []byte) (n int, err error) {
	if b.Len()+len(p) > b.Limit {
		allowed := b.Limit - b.Len()
		if allowed > 0 {
			n, _ = b.Buffer.Write(p[:allowed])
		}
		return n, ErrOutputLimitExceeded
	}
	return b.Buffer.Write(p)
}

// ToolName identifies a specific tool in the ecosystem
type ToolName string

const (
	ToolBV    ToolName = "bv"         // Beads Viewer - graph-aware triage
	ToolBD    ToolName = "bd"         // Beads - issue tracking
	ToolAM    ToolName = "am"         // Agent Mail MCP server
	ToolCM    ToolName = "cm"         // Cass Memory system
	ToolCASS  ToolName = "cass"       // Cross-Agent Semantic Search
	ToolS2P   ToolName = "s2p"        // Source to Prompt
	ToolJFP   ToolName = "jfp"        // JeffreysPrompts CLI - prompt library
	ToolDCG   ToolName = "dcg"        // Destructive Command Guard - blocks dangerous commands
	ToolSLB   ToolName = "slb"        // Simultaneous Launch Button - two-person authorization
	ToolACFS  ToolName = "acfs"       // Agentic Coding Flywheel Setup - system configuration
	ToolRU    ToolName = "ru"         // Repo Updater - multi-repo sync and management
	ToolMS    ToolName = "ms"         // Meta Skill - skill search and suggestion
	ToolXF    ToolName = "xf"         // X Find - X/Twitter archive search
	ToolGIIL  ToolName = "giil"       // Get Image from Internet Link - cloud photo downloader
	ToolUBS   ToolName = "ubs"        // Ultimate Bug Scanner - code review and bug detection
	ToolCAAM  ToolName = "caam"       // CAAM - Coding Agent Account Manager for rate limit recovery
	ToolRCH   ToolName = "rch"        // RCH - Remote Compilation Helper for build offloading
	ToolRano  ToolName = "rano"       // rano - Network observer for per-agent API tracking
	ToolCaut  ToolName = "caut"       // caut - Cloud API Usage Tracker for quota monitoring
	ToolPT    ToolName = "pt"         // pt - process_triage - Bayesian agent health classification
	ToolProxy ToolName = "rust_proxy" // rust_proxy - local HTTP proxy with failover
)

// AllTools returns a list of all supported tools
func AllTools() []ToolName {
	return []ToolName{ToolBV, ToolBD, ToolAM, ToolCM, ToolCASS, ToolS2P, ToolJFP, ToolDCG, ToolSLB, ToolACFS, ToolRU, ToolMS, ToolXF, ToolGIIL, ToolUBS, ToolCAAM, ToolRCH, ToolRano, ToolCaut, ToolPT, ToolProxy}
}

// HealthStatus represents the health state of a tool
type HealthStatus struct {
	Healthy     bool          `json:"healthy"`
	Message     string        `json:"message,omitempty"`
	LastChecked time.Time     `json:"last_checked"`
	Latency     time.Duration `json:"latency_ms,omitempty"`
	Error       string        `json:"error,omitempty"`
}

// Capability represents a feature provided by a tool
type Capability string

// Common capabilities across tools
const (
	CapRobotMode   Capability = "robot_mode"   // JSON output for automation
	CapDaemonMode  Capability = "daemon_mode"  // Can run as daemon
	CapMacros      Capability = "macros"       // Supports macro commands
	CapSearch      Capability = "search"       // Can search content
	CapContextPack Capability = "context_pack" // Context preparation
)

// Version represents a parsed semantic version
type Version struct {
	Major int    `json:"major"`
	Minor int    `json:"minor"`
	Patch int    `json:"patch"`
	Raw   string `json:"raw"`
}

// String returns the version string
func (v Version) String() string {
	if v.Raw != "" {
		return v.Raw
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare compares two versions. Returns -1 if v < other, 0 if equal, 1 if v > other
func (v Version) Compare(other Version) int {
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}
	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}
	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}
		return 1
	}
	return 0
}

// AtLeast returns true if v >= other
func (v Version) AtLeast(other Version) bool {
	return v.Compare(other) >= 0
}

// ToolInfo contains metadata about a detected tool
type ToolInfo struct {
	Name         ToolName     `json:"name"`
	Installed    bool         `json:"installed"`
	Version      Version      `json:"version,omitempty"`
	Capabilities []Capability `json:"capabilities,omitempty"`
	Path         string       `json:"path,omitempty"`
	Health       HealthStatus `json:"health"`
}

// Adapter defines the interface that all tool adapters must implement
type Adapter interface {
	// Name returns the tool identifier
	Name() ToolName

	// Detect checks if the tool is installed and returns its path
	Detect() (path string, installed bool)

	// Version returns the installed version
	Version(ctx context.Context) (Version, error)

	// Capabilities returns the list of supported features
	Capabilities(ctx context.Context) ([]Capability, error)

	// Health checks if the tool is functioning correctly
	Health(ctx context.Context) (*HealthStatus, error)

	// HasCapability checks if a specific capability is available
	HasCapability(ctx context.Context, cap Capability) bool

	// Info returns complete tool information
	Info(ctx context.Context) (*ToolInfo, error)
}

// BaseAdapter provides common functionality for adapters
type BaseAdapter struct {
	name       ToolName
	binaryName string
	timeout    time.Duration
}

// NewBaseAdapter creates a new base adapter
func NewBaseAdapter(name ToolName, binaryName string) *BaseAdapter {
	return &BaseAdapter{
		name:       name,
		binaryName: binaryName,
		timeout:    30 * time.Second,
	}
}

// Name returns the tool identifier
func (a *BaseAdapter) Name() ToolName {
	return a.name
}

// BinaryName returns the executable name
func (a *BaseAdapter) BinaryName() string {
	return a.binaryName
}

// Timeout returns the default operation timeout
func (a *BaseAdapter) Timeout() time.Duration {
	return a.timeout
}

// SetTimeout sets the default operation timeout
func (a *BaseAdapter) SetTimeout(t time.Duration) {
	a.timeout = t
}

// Info returns complete tool information using the adapter methods
func (a *BaseAdapter) Info(ctx context.Context, adapter Adapter) (*ToolInfo, error) {
	info := &ToolInfo{
		Name: a.name,
	}

	// Check if installed
	path, installed := adapter.Detect()
	info.Installed = installed
	info.Path = path

	if !installed {
		info.Health = HealthStatus{
			Healthy:     false,
			Message:     "Tool not installed",
			LastChecked: time.Now(),
		}
		return info, nil
	}

	// Get version
	version, err := adapter.Version(ctx)
	if err == nil {
		info.Version = version
	}

	// Get capabilities
	caps, err := adapter.Capabilities(ctx)
	if err == nil {
		info.Capabilities = caps
	}

	// Check health
	health, err := adapter.Health(ctx)
	if err != nil {
		info.Health = HealthStatus{
			Healthy:     false,
			Error:       err.Error(),
			LastChecked: time.Now(),
		}
	} else {
		info.Health = *health
	}

	return info, nil
}
