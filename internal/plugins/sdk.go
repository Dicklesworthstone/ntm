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
	plugins map[string]*pluginEntry
	hostCtx PluginContext
	// closed is set when Shutdown has run; subsequent Register calls
	// fail-close, and any in-flight Init that completes after Shutdown
	// invokes its plugin's Shutdown to avoid leaking a freshly-init'd
	// orphan into a defunct registry (bd-dl6ek).
	closed bool
}

// pluginEntry tracks one slot in the registry. ready=false means
// Register has reserved the name slot but the plugin's Init has not
// yet returned successfully — the plugin is intentionally NOT
// visible via Get / List / FindByCapability so consumers cannot
// invoke it before Init completes (bd-dl6ek). Once Init returns
// nil, the registering goroutine flips ready=true under r.mu.
type pluginEntry struct {
	p     Plugin
	ready bool
}

// NewAdapterRegistry returns a registry seeded with the host context
// passed to every Plugin.Init.
func NewAdapterRegistry(ctx PluginContext) *AdapterRegistry {
	if ctx.SDKVersion == "" {
		ctx.SDKVersion = SDKVersion
	}
	return &AdapterRegistry{
		plugins: make(map[string]*pluginEntry),
		hostCtx: ctx,
	}
}

// ErrRegistryClosed is returned by Register when the registry's
// Shutdown has already been called. The closed state is permanent.
var ErrRegistryClosed = errors.New("plugin registry closed")

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
// caller and no partial state is retained. The plugin is NOT visible
// to Get/List/FindByCapability until Init returns nil, so concurrent
// observers cannot invoke a plugin whose Init is still in flight
// (bd-dl6ek). The name slot is reserved during Init so a concurrent
// Register call for the same name still gets ErrPluginAlreadyRegistered.
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
	if r.closed {
		r.mu.Unlock()
		return fmt.Errorf("%w: cannot register %s", ErrRegistryClosed, name)
	}
	if _, ok := r.plugins[name]; ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrPluginAlreadyRegistered, name)
	}
	entry := &pluginEntry{p: p, ready: false}
	r.plugins[name] = entry
	r.mu.Unlock()

	// Init runs OUTSIDE the lock so it does not block other registry
	// operations on unrelated plugins. The slot is reserved by the
	// non-ready entry; observers filter on ready=true.
	initErr := p.Init(r.hostCtx)

	r.mu.Lock()
	registryClosed := r.closed
	if initErr != nil {
		delete(r.plugins, name)
		r.mu.Unlock()
		return fmt.Errorf("%w: %s: %v", ErrPluginInitFailed, name, initErr)
	}
	if registryClosed {
		// Shutdown ran while our Init was in flight. Don't mark the
		// entry ready — drop it from the map and clean up the freshly
		// init'd plugin so the contract "After Shutdown returns, no
		// further calls into the plugin are made" still holds for the
		// registry as a whole.
		delete(r.plugins, name)
		r.mu.Unlock()
		_ = p.Shutdown()
		return fmt.Errorf("%w: %s init completed after shutdown", ErrRegistryClosed, name)
	}
	entry.ready = true
	r.mu.Unlock()
	return nil
}

// Unregister removes a plugin and calls its Shutdown. Returns
// ErrPluginNotFound if the plugin is not present OR is still
// initializing — only ready (Init-completed) plugins can be
// Unregistered. Shutdown errors are returned verbatim; the plugin is
// removed regardless so the caller can retry registration with a
// corrected implementation.
func (r *AdapterRegistry) Unregister(name string) error {
	r.mu.Lock()
	entry, ok := r.plugins[name]
	if !ok || !entry.ready {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrPluginNotFound, name)
	}
	delete(r.plugins, name)
	r.mu.Unlock()
	return entry.p.Shutdown()
}

// Get returns the plugin with the given name, or ErrPluginNotFound.
// In-flight registrations (Init not yet returned) are reported as
// not found so consumers cannot invoke a plugin before its Init
// contract is satisfied (bd-dl6ek).
func (r *AdapterRegistry) Get(name string) (Plugin, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.plugins[name]
	if !ok || !entry.ready {
		return nil, fmt.Errorf("%w: %s", ErrPluginNotFound, name)
	}
	return entry.p, nil
}

// List returns all registered plugins in deterministic alphabetical
// order. The returned slice is a snapshot — safe to mutate. Plugins
// whose Init has not yet completed are excluded.
func (r *AdapterRegistry) List() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.plugins))
	for n, entry := range r.plugins {
		if entry.ready {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]Plugin, 0, len(names))
	for _, n := range names {
		out = append(out, r.plugins[n].p)
	}
	return out
}

// FindByCapability returns plugins advertising the given capability,
// sorted alphabetically by Name. In-flight registrations are
// excluded — the host cannot dispatch work to a plugin whose Init
// has not yet completed.
func (r *AdapterRegistry) FindByCapability(c Capability) []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	type match struct {
		name string
		p    Plugin
	}
	var matches []match
	for n, entry := range r.plugins {
		if !entry.ready {
			continue
		}
		for _, cap := range entry.p.Capabilities() {
			if cap == c {
				matches = append(matches, match{name: n, p: entry.p})
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

// Shutdown calls Shutdown on every ready plugin in deterministic
// order, marks the registry closed, and clears the ready entries.
// Per-plugin Shutdown errors are collected via errors.Join so the
// caller sees them all even when one fails first.
//
// Plugins whose Init is still in flight are NOT invoked — their
// Init contract has not been satisfied, so the inverse "Shutdown
// after Init" promise has nothing to honor. The closed flag means
// any in-flight Init that returns successfully after this point
// will see registryClosed=true and clean up its own plugin via
// p.Shutdown(), so no orphan can leak past Shutdown's return.
// Subsequent Register calls fail with ErrRegistryClosed.
func (r *AdapterRegistry) Shutdown() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	names := make([]string, 0, len(r.plugins))
	for n, entry := range r.plugins {
		if entry.ready {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var joined error
	for _, n := range names {
		if err := r.plugins[n].p.Shutdown(); err != nil {
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
