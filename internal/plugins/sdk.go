package plugins

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// SDKVersion is the current Plugin SDK contract version. Bump major
// when a backward-incompatible change is made (signature change,
// removed capability key, lifecycle reordering); bump minor when a
// purely additive change is made (new optional capability,
// non-breaking field). Plugins declare the minimum SDK version they
// require via Plugin.MinSDKVersion.
const SDKVersion = "1.0"

// Capability is a string identifier advertised by a plugin to declare
// what it can do. Capability strings are namespaced with `:` so the
// host can route work without instance knowledge.
//
// Canonical capabilities are defined here; plugins are free to add
// custom capabilities prefixed with `x:` for experimentation.
type Capability string

const (
	CapabilityAgentLauncher  Capability = "agent.launcher"
	CapabilityHandoffSink    Capability = "handoff.sink"
	CapabilityRobotProvider  Capability = "robot.provider"
	CapabilityPipelineHook   Capability = "pipeline.hook"
	CapabilityActivitySource Capability = "activity.source"
)

// Plugin is the SDK-facing contract for an external integration. The
// fields are inspected during registration; SDK methods drive the
// lifecycle. Implementations must be safe to call from multiple
// goroutines after Init returns nil.
type Plugin interface {
	// Name returns a stable, unique plugin name. The same plugin must
	// always return the same Name; the registry uses it as a key.
	Name() string

	// Version returns the plugin's own version, independent of the
	// SDKVersion it targets.
	Version() string

	// MinSDKVersion returns the lowest SDKVersion the plugin is
	// compatible with. Registry rejects plugins whose MinSDKVersion is
	// newer than the host SDKVersion.
	MinSDKVersion() string

	// Capabilities returns the capability set this plugin advertises.
	// The returned slice is read by the registry; implementations
	// should return a stable list (no allocation per call when
	// possible).
	Capabilities() []Capability

	// Init is the lifecycle entry point. Called exactly once before
	// any capability dispatch. Init must return promptly; long-running
	// background work belongs in a goroutine started here.
	Init(ctx PluginContext) error

	// Shutdown is the lifecycle exit. Called exactly once. After
	// Shutdown returns, no further calls into the plugin are made.
	Shutdown() error
}

// PluginContext is the host-supplied environment passed to Init. It
// is intentionally narrow so plugins do not couple to internal types.
type PluginContext struct {
	// HostName is the host program name (e.g. "ntm").
	HostName string
	// HostVersion is the host program version.
	HostVersion string
	// SDKVersion is the SDKVersion advertised by the host.
	SDKVersion string
	// ProjectKey is the canonical project path the plugin is scoped to.
	ProjectKey string
}

// Sentinel errors. Plugins and host code should `errors.Is` these
// rather than matching on string content; messages may evolve.
var (
	// ErrPluginIncompatible is returned when a plugin's MinSDKVersion
	// is newer than the host SDKVersion.
	ErrPluginIncompatible = errors.New("plugin incompatible with host SDK version")

	// ErrPluginMalformed is returned when a plugin's metadata fails
	// validation (empty name, missing capability, invalid version
	// string, etc.).
	ErrPluginMalformed = errors.New("plugin metadata malformed")

	// ErrPluginAlreadyRegistered is returned when Register is called
	// for a name already present in the registry.
	ErrPluginAlreadyRegistered = errors.New("plugin already registered")

	// ErrPluginNotFound is returned when Get or Unregister is called
	// for an unknown name.
	ErrPluginNotFound = errors.New("plugin not found")

	// ErrPluginInitFailed wraps any error returned from Plugin.Init.
	// The underlying error is preserved via errors.Unwrap.
	ErrPluginInitFailed = errors.New("plugin init failed")
)

// AdapterRegistry is the host-side surface used to register, look up,
// and shut down Plugin instances. The registry is safe for concurrent
// use; writers are serialized so deterministic order is preserved
// across List calls.
type AdapterRegistry struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
	hostCtx PluginContext
}

// NewAdapterRegistry returns a registry seeded with the host context
// passed to every Plugin.Init.
func NewAdapterRegistry(ctx PluginContext) *AdapterRegistry {
	if ctx.SDKVersion == "" {
		ctx.SDKVersion = SDKVersion
	}
	return &AdapterRegistry{
		plugins: make(map[string]Plugin),
		hostCtx: ctx,
	}
}

// HostContext returns the PluginContext the registry will pass into
// future Init calls. Useful for diagnostics.
func (r *AdapterRegistry) HostContext() PluginContext {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hostCtx
}

// Register validates and stores a plugin. On success the plugin's
// Init is called with the registry's host context; on Init failure
// the plugin is removed from the registry and ErrPluginInitFailed is
// returned (with the original error wrapped).
//
// Register is fail-closed: any validation failure is surfaced to the
// caller and no partial state is retained.
func (r *AdapterRegistry) Register(p Plugin) error {
	if p == nil {
		return fmt.Errorf("%w: nil plugin", ErrPluginMalformed)
	}
	if err := validatePluginMetadata(p); err != nil {
		return err
	}
	if err := checkSDKCompatibility(p, r.hostCtx.SDKVersion); err != nil {
		return err
	}
	name := p.Name()

	r.mu.Lock()
	if _, ok := r.plugins[name]; ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrPluginAlreadyRegistered, name)
	}
	r.plugins[name] = p
	r.mu.Unlock()

	if err := p.Init(r.hostCtx); err != nil {
		r.mu.Lock()
		delete(r.plugins, name)
		r.mu.Unlock()
		return fmt.Errorf("%w: %s: %v", ErrPluginInitFailed, name, err)
	}
	return nil
}

// Unregister removes a plugin and calls its Shutdown. Returns
// ErrPluginNotFound if the plugin is not present. Shutdown errors are
// returned verbatim; the plugin is removed regardless so the caller
// can retry registration with a corrected implementation.
func (r *AdapterRegistry) Unregister(name string) error {
	r.mu.Lock()
	p, ok := r.plugins[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrPluginNotFound, name)
	}
	delete(r.plugins, name)
	r.mu.Unlock()
	return p.Shutdown()
}

// Get returns the plugin with the given name, or ErrPluginNotFound.
func (r *AdapterRegistry) Get(name string) (Plugin, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrPluginNotFound, name)
	}
	return p, nil
}

// List returns all registered plugins in deterministic alphabetical
// order. The returned slice is a snapshot — safe to mutate.
func (r *AdapterRegistry) List() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Plugin, 0, len(r.plugins))
	names := make([]string, 0, len(r.plugins))
	for n := range r.plugins {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, r.plugins[n])
	}
	return out
}

// FindByCapability returns plugins advertising the given capability,
// sorted alphabetically by Name. The host can use this to dispatch
// work without naming individual plugins.
func (r *AdapterRegistry) FindByCapability(c Capability) []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	type entry struct {
		name string
		p    Plugin
	}
	var matches []entry
	for n, p := range r.plugins {
		for _, cap := range p.Capabilities() {
			if cap == c {
				matches = append(matches, entry{name: n, p: p})
				break
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].name < matches[j].name })
	out := make([]Plugin, len(matches))
	for i, m := range matches {
		out[i] = m.p
	}
	return out
}

// Shutdown calls Shutdown on every registered plugin in deterministic
// order and clears the registry. Per-plugin Shutdown errors are
// collected via errors.Join so the caller sees them all even when one
// fails first.
func (r *AdapterRegistry) Shutdown() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.plugins))
	for n := range r.plugins {
		names = append(names, n)
	}
	sort.Strings(names)
	var joined error
	for _, n := range names {
		if err := r.plugins[n].Shutdown(); err != nil {
			joined = errors.Join(joined, fmt.Errorf("plugin %s: %w", n, err))
		}
		delete(r.plugins, n)
	}
	return joined
}

// validatePluginMetadata enforces the structural contract every plugin
// must satisfy independent of SDK version.
func validatePluginMetadata(p Plugin) error {
	name := strings.TrimSpace(p.Name())
	if name == "" {
		return fmt.Errorf("%w: empty name", ErrPluginMalformed)
	}
	if !pluginNameRegex.MatchString(name) {
		return fmt.Errorf("%w: invalid name %q (allowed: a-z, 0-9, _, -)", ErrPluginMalformed, name)
	}
	if strings.TrimSpace(p.Version()) == "" {
		return fmt.Errorf("%w: %s: empty version", ErrPluginMalformed, name)
	}
	if _, err := parseVersion(p.Version()); err != nil {
		return fmt.Errorf("%w: %s: invalid version %q: %v", ErrPluginMalformed, name, p.Version(), err)
	}
	if strings.TrimSpace(p.MinSDKVersion()) == "" {
		return fmt.Errorf("%w: %s: empty MinSDKVersion", ErrPluginMalformed, name)
	}
	if _, err := parseVersion(p.MinSDKVersion()); err != nil {
		return fmt.Errorf("%w: %s: invalid MinSDKVersion %q: %v", ErrPluginMalformed, name, p.MinSDKVersion(), err)
	}
	if len(p.Capabilities()) == 0 {
		return fmt.Errorf("%w: %s: declares no capabilities", ErrPluginMalformed, name)
	}
	for _, c := range p.Capabilities() {
		if strings.TrimSpace(string(c)) == "" {
			return fmt.Errorf("%w: %s: empty capability", ErrPluginMalformed, name)
		}
	}
	return nil
}

// checkSDKCompatibility returns ErrPluginIncompatible if the plugin's
// MinSDKVersion is newer than the host's SDKVersion. Comparison is
// purely numeric on (major, minor); patch is ignored.
func checkSDKCompatibility(p Plugin, hostSDK string) error {
	host, err := parseVersion(hostSDK)
	if err != nil {
		return fmt.Errorf("%w: host SDKVersion invalid %q", ErrPluginMalformed, hostSDK)
	}
	min, err := parseVersion(p.MinSDKVersion())
	if err != nil {
		return fmt.Errorf("%w: %s: invalid MinSDKVersion %q", ErrPluginMalformed, p.Name(), p.MinSDKVersion())
	}
	if min.major > host.major || (min.major == host.major && min.minor > host.minor) {
		return fmt.Errorf("%w: plugin %s requires SDK >= %s, host has %s",
			ErrPluginIncompatible, p.Name(), p.MinSDKVersion(), hostSDK)
	}
	return nil
}

// version is the parsed (major, minor) form of a "M.m" or "M.m.p"
// string. Patch is parsed but not compared.
type version struct {
	major int
	minor int
	patch int
}

// parseVersion accepts "M", "M.m", or "M.m.p"; rejects anything else.
func parseVersion(s string) (version, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) == 0 || len(parts) > 3 {
		return version{}, fmt.Errorf("expected M / M.m / M.m.p, got %q", s)
	}
	var v version
	dst := []*int{&v.major, &v.minor, &v.patch}
	for i, p := range parts {
		if p == "" {
			return version{}, fmt.Errorf("empty segment in %q", s)
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, fmt.Errorf("non-numeric segment %q in %q", p, s)
		}
		if n < 0 {
			return version{}, fmt.Errorf("negative segment %d in %q", n, s)
		}
		*dst[i] = n
	}
	return v, nil
}
