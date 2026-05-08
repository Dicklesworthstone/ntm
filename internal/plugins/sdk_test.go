package plugins

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakePlugin is a small Plugin implementation that lets tests control
// each call's outcome via atomic counters and overrides.
type fakePlugin struct {
	name          string
	version       string
	minSDK        string
	caps          []Capability
	initErr       error
	shutdownErr   error
	initCount     atomic.Int32
	shutdownCount atomic.Int32
	gotCtx        atomic.Pointer[PluginContext]
}

func (p *fakePlugin) Name() string             { return p.name }
func (p *fakePlugin) Version() string          { return p.version }
func (p *fakePlugin) MinSDKVersion() string    { return p.minSDK }
func (p *fakePlugin) Capabilities() []Capability { return p.caps }
func (p *fakePlugin) Init(ctx PluginContext) error {
	p.initCount.Add(1)
	c := ctx
	p.gotCtx.Store(&c)
	return p.initErr
}
func (p *fakePlugin) Shutdown() error {
	p.shutdownCount.Add(1)
	return p.shutdownErr
}

func newGoodPlugin(name string, caps ...Capability) *fakePlugin {
	if len(caps) == 0 {
		caps = []Capability{CapabilityAgentLauncher}
	}
	return &fakePlugin{
		name:    name,
		version: "1.0.0",
		minSDK:  "1.0",
		caps:    caps,
	}
}

func newRegistry() *AdapterRegistry {
	return NewAdapterRegistry(PluginContext{
		HostName:    "ntm",
		HostVersion: "test",
		ProjectKey:  "/data/projects/ntm",
	})
}

func TestRegister_HappyPathInitsAndStores(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	p := newGoodPlugin("alpha")
	if err := r.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if p.initCount.Load() != 1 {
		t.Errorf("initCount = %d, want 1", p.initCount.Load())
	}
	got, err := r.Get("alpha")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name() != "alpha" {
		t.Errorf("Get returned %s, want alpha", got.Name())
	}
	// Init received the host context.
	c := p.gotCtx.Load()
	if c == nil || c.HostName != "ntm" {
		t.Errorf("Init context = %+v, want HostName=ntm", c)
	}
}

func TestRegister_DuplicateRejected(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	if err := r.Register(newGoodPlugin("alpha")); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := r.Register(newGoodPlugin("alpha"))
	if !errors.Is(err, ErrPluginAlreadyRegistered) {
		t.Errorf("err = %v, want ErrPluginAlreadyRegistered", err)
	}
}

func TestRegister_RejectsMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    *fakePlugin
	}{
		{"empty name", &fakePlugin{name: "", version: "1.0", minSDK: "1.0", caps: []Capability{CapabilityAgentLauncher}}},
		{"bad name char", &fakePlugin{name: "a/b", version: "1.0", minSDK: "1.0", caps: []Capability{CapabilityAgentLauncher}}},
		{"empty version", &fakePlugin{name: "alpha", version: "", minSDK: "1.0", caps: []Capability{CapabilityAgentLauncher}}},
		{"bad version", &fakePlugin{name: "alpha", version: "abc", minSDK: "1.0", caps: []Capability{CapabilityAgentLauncher}}},
		{"empty min sdk", &fakePlugin{name: "alpha", version: "1.0", minSDK: "", caps: []Capability{CapabilityAgentLauncher}}},
		{"no caps", &fakePlugin{name: "alpha", version: "1.0", minSDK: "1.0", caps: nil}},
		{"empty cap string", &fakePlugin{name: "alpha", version: "1.0", minSDK: "1.0", caps: []Capability{""}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRegistry()
			err := r.Register(c.p)
			if !errors.Is(err, ErrPluginMalformed) {
				t.Errorf("err = %v, want ErrPluginMalformed", err)
			}
		})
	}
}

func TestRegister_RejectsNilPlugin(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	if err := r.Register(nil); !errors.Is(err, ErrPluginMalformed) {
		t.Errorf("err = %v, want ErrPluginMalformed", err)
	}
}

func TestRegister_IncompatibleSDKVersion(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	p := newGoodPlugin("future")
	p.minSDK = "9.9"
	err := r.Register(p)
	if !errors.Is(err, ErrPluginIncompatible) {
		t.Errorf("err = %v, want ErrPluginIncompatible", err)
	}
	if _, ok := r.plugins["future"]; ok {
		t.Error("incompatible plugin retained in registry")
	}
}

func TestRegister_InitFailureRollsBack(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	p := newGoodPlugin("flaky")
	p.initErr = errors.New("boom")
	err := r.Register(p)
	if !errors.Is(err, ErrPluginInitFailed) {
		t.Errorf("err = %v, want ErrPluginInitFailed", err)
	}
	if _, getErr := r.Get("flaky"); !errors.Is(getErr, ErrPluginNotFound) {
		t.Error("flaky plugin still in registry after Init failure")
	}
}

func TestUnregister_CallsShutdownAndRemoves(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	p := newGoodPlugin("alpha")
	if err := r.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Unregister("alpha"); err != nil {
		t.Errorf("Unregister: %v", err)
	}
	if p.shutdownCount.Load() != 1 {
		t.Errorf("shutdownCount = %d, want 1", p.shutdownCount.Load())
	}
	if _, err := r.Get("alpha"); !errors.Is(err, ErrPluginNotFound) {
		t.Errorf("Get after Unregister: err = %v, want ErrPluginNotFound", err)
	}
}

func TestUnregister_NotFound(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	err := r.Unregister("nope")
	if !errors.Is(err, ErrPluginNotFound) {
		t.Errorf("err = %v, want ErrPluginNotFound", err)
	}
}

func TestList_DeterministicOrdering(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	for _, n := range []string{"zebra", "alpha", "mike"} {
		if err := r.Register(newGoodPlugin(n)); err != nil {
			t.Fatalf("Register %s: %v", n, err)
		}
	}
	got := r.List()
	want := []string{"alpha", "mike", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("got %d, want %d plugins", len(got), len(want))
	}
	for i, p := range got {
		if p.Name() != want[i] {
			t.Errorf("got[%d] = %s, want %s", i, p.Name(), want[i])
		}
	}
}

func TestFindByCapability(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	if err := r.Register(newGoodPlugin("alpha", CapabilityAgentLauncher, CapabilityRobotProvider)); err != nil {
		t.Fatalf("Register alpha: %v", err)
	}
	if err := r.Register(newGoodPlugin("bravo", CapabilityRobotProvider)); err != nil {
		t.Fatalf("Register bravo: %v", err)
	}
	if err := r.Register(newGoodPlugin("charlie", CapabilityHandoffSink)); err != nil {
		t.Fatalf("Register charlie: %v", err)
	}

	got := r.FindByCapability(CapabilityRobotProvider)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 plugins for RobotProvider", len(got))
	}
	if got[0].Name() != "alpha" || got[1].Name() != "bravo" {
		t.Errorf("got order = %s,%s, want alpha,bravo", got[0].Name(), got[1].Name())
	}
	if empty := r.FindByCapability(CapabilityPipelineHook); len(empty) != 0 {
		t.Errorf("expected empty result for unused capability, got %d", len(empty))
	}
}

func TestRegistry_ShutdownAllClears(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	a := newGoodPlugin("alpha")
	b := newGoodPlugin("bravo")
	if err := r.Register(a); err != nil {
		t.Fatalf("Register alpha: %v", err)
	}
	if err := r.Register(b); err != nil {
		t.Fatalf("Register bravo: %v", err)
	}
	if err := r.Shutdown(); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if a.shutdownCount.Load() != 1 || b.shutdownCount.Load() != 1 {
		t.Errorf("shutdown counts = %d/%d, want 1/1",
			a.shutdownCount.Load(), b.shutdownCount.Load())
	}
	if got := r.List(); len(got) != 0 {
		t.Errorf("List after Shutdown = %d, want 0", len(got))
	}
}

func TestRegistry_ShutdownJoinsErrors(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	a := newGoodPlugin("alpha")
	a.shutdownErr = errors.New("alpha-down")
	b := newGoodPlugin("bravo")
	b.shutdownErr = errors.New("bravo-down")
	c := newGoodPlugin("charlie")
	for _, p := range []*fakePlugin{a, b, c} {
		if err := r.Register(p); err != nil {
			t.Fatalf("Register %s: %v", p.name, err)
		}
	}
	err := r.Shutdown()
	if err == nil {
		t.Fatal("Shutdown returned nil; expected joined error")
	}
	// Both errors must be present.
	if !errors.Is(err, a.shutdownErr) || !errors.Is(err, b.shutdownErr) {
		t.Errorf("joined err = %v, missing one of the per-plugin errors", err)
	}
}

func TestParseVersion_Forms(t *testing.T) {
	t.Parallel()
	good := []string{"1", "1.0", "1.2.3", "0.0.0"}
	for _, s := range good {
		if _, err := parseVersion(s); err != nil {
			t.Errorf("parseVersion(%q) err = %v", s, err)
		}
	}
	bad := []string{"", "v1", "1..2", "abc", "1.2.3.4", "-1.0"}
	for _, s := range bad {
		if _, err := parseVersion(s); err == nil {
			t.Errorf("parseVersion(%q) accepted, want error", s)
		}
	}
}

// Failure isolation: a single broken plugin must not block registration
// of other valid plugins through the host.
func TestRegistry_FailureIsolation(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	if err := r.Register(newGoodPlugin("alpha")); err != nil {
		t.Fatalf("alpha: %v", err)
	}
	bad := newGoodPlugin("bravo")
	bad.minSDK = "9.9" // incompatible
	if err := r.Register(bad); !errors.Is(err, ErrPluginIncompatible) {
		t.Errorf("bravo: err = %v, want ErrPluginIncompatible", err)
	}
	if err := r.Register(newGoodPlugin("charlie")); err != nil {
		t.Fatalf("charlie: %v", err)
	}
	if got := len(r.List()); got != 2 {
		t.Errorf("registry size = %d, want 2 (alpha + charlie, bravo rejected)", got)
	}
}

// slowInitPlugin blocks Init on a release channel so tests can hold
// a registration in-flight and exercise the bd-dl6ek windows where
// the plugin is reserved in the map but not yet ready for callers.
type slowInitPlugin struct {
	fakePlugin
	release <-chan struct{}
}

func (p *slowInitPlugin) Init(ctx PluginContext) error {
	// Mark entry FIRST so tests can detect Init has started, THEN
	// block. Reversing this order deadlocks any test that gates the
	// channel-close on initCount > 0.
	p.fakePlugin.initCount.Add(1)
	c := ctx
	p.fakePlugin.gotCtx.Store(&c)
	if p.release != nil {
		<-p.release
	}
	return p.fakePlugin.initErr
}

func newSlowPlugin(name string, release <-chan struct{}) *slowInitPlugin {
	return &slowInitPlugin{
		fakePlugin: fakePlugin{
			name:    name,
			version: "1.0.0",
			minSDK:  "1.0",
			caps:    []Capability{CapabilityAgentLauncher},
		},
		release: release,
	}
}

// bd-dl6ek: Get / List / FindByCapability must NOT surface a plugin
// whose Init is still in flight. The pre-fix code stored the plugin
// in the map before Init returned, so observers could fetch and
// invoke a not-yet-initialized plugin.
func TestRegister_PluginIsInvisibleUntilInitReturns(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	release := make(chan struct{})
	p := newSlowPlugin("slow", release)

	registerDone := make(chan error, 1)
	go func() { registerDone <- r.Register(p) }()

	// Wait until Register has reserved the slot but is blocked on Init.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if p.initCount.Load() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if p.initCount.Load() == 0 {
		t.Fatalf("Init never started; race window not opened")
	}

	// While Init is blocked: Get / List / FindByCapability must all
	// behave as if the plugin doesn't exist.
	if got, err := r.Get("slow"); err == nil || got != nil {
		t.Errorf("Get returned in-flight plugin: got=%v err=%v", got, err)
	} else if !errors.Is(err, ErrPluginNotFound) {
		t.Errorf("Get err = %v, want ErrPluginNotFound", err)
	}
	if list := r.List(); len(list) != 0 {
		t.Errorf("List = %v, want empty (in-flight plugin must not surface)", list)
	}
	if matches := r.FindByCapability(CapabilityAgentLauncher); len(matches) != 0 {
		t.Errorf("FindByCapability = %v, want empty during in-flight Init", matches)
	}

	// Slot is reserved: a second Register for the same name must
	// see ErrPluginAlreadyRegistered, not silently double-init.
	if err := r.Register(newGoodPlugin("slow")); !errors.Is(err, ErrPluginAlreadyRegistered) {
		t.Errorf("concurrent Register err = %v, want ErrPluginAlreadyRegistered", err)
	}

	close(release)
	if err := <-registerDone; err != nil {
		t.Fatalf("Register: %v", err)
	}

	// After Init returns, the plugin becomes visible.
	if got, err := r.Get("slow"); err != nil || got == nil {
		t.Errorf("Get after Init: got=%v err=%v", got, err)
	}
}

// bd-dl6ek: Shutdown must NOT invoke Shutdown on a plugin whose Init
// is still in flight. The pre-fix code iterated the map and called
// Shutdown on every entry, including not-yet-init'd plugins.
func TestShutdown_DoesNotInvokeNotReadyPlugin(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	release := make(chan struct{})
	p := newSlowPlugin("slow", release)

	registerDone := make(chan error, 1)
	go func() { registerDone <- r.Register(p) }()

	// Wait for Init to be in flight.
	for p.initCount.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	// Shutdown the registry while the slow plugin's Init is blocked.
	// The not-yet-ready plugin must NOT have its Shutdown invoked.
	if err := r.Shutdown(); err != nil {
		t.Errorf("Shutdown err = %v, want nil (no ready plugins)", err)
	}
	if got := p.shutdownCount.Load(); got != 0 {
		t.Errorf("not-ready plugin Shutdown call count = %d, want 0", got)
	}

	// Now release Init. Since the registry is closed, Register's
	// completion path must call Shutdown on the just-init'd plugin
	// (so it doesn't leak past Shutdown's return) and surface
	// ErrRegistryClosed.
	close(release)
	regErr := <-registerDone
	if !errors.Is(regErr, ErrRegistryClosed) {
		t.Errorf("Register after Shutdown err = %v, want ErrRegistryClosed", regErr)
	}
	if got := p.shutdownCount.Load(); got != 1 {
		t.Errorf("post-shutdown Init plugin Shutdown call count = %d, want 1", got)
	}
}

// Subsequent Register calls fail-close after Shutdown.
func TestRegister_AfterShutdownFailsClosed(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	if err := r.Shutdown(); err != nil {
		t.Fatalf("initial Shutdown: %v", err)
	}
	err := r.Register(newGoodPlugin("p"))
	if !errors.Is(err, ErrRegistryClosed) {
		t.Errorf("Register err = %v, want ErrRegistryClosed", err)
	}
}

// Unregister of an in-flight plugin returns ErrPluginNotFound — the
// plugin is reserved but not yet usable, so the contract treats it
// as "not present" from the consumer's perspective.
func TestUnregister_InFlightPluginIsNotFound(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	release := make(chan struct{})
	p := newSlowPlugin("slow", release)

	registerDone := make(chan error, 1)
	go func() { registerDone <- r.Register(p) }()

	for p.initCount.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	// Unregister must not see the in-flight plugin.
	if err := r.Unregister("slow"); !errors.Is(err, ErrPluginNotFound) {
		t.Errorf("Unregister of in-flight plugin err = %v, want ErrPluginNotFound", err)
	}
	if got := p.shutdownCount.Load(); got != 0 {
		t.Errorf("Unregister leaked Shutdown call to not-ready plugin: count = %d", got)
	}

	close(release)
	if err := <-registerDone; err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Now the plugin is ready and Unregister works normally.
	if err := r.Unregister("slow"); err != nil {
		t.Errorf("Unregister after Init: %v", err)
	}
}

// Race-detector test: many goroutines registering, listing, and
// shutting down concurrently must not produce data races.
func TestRegistry_RaceFreeUnderConcurrentRegisterListShutdown(t *testing.T) {
	t.Parallel()
	r := newRegistry()

	const writers = 8
	const readers = 8
	const iterations = 50

	var wg sync.WaitGroup

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				name := "p_" + string(rune('a'+seed)) + "_" + string(rune('0'+(j%10)))
				_ = r.Register(newGoodPlugin(name))
			}
		}(i)
	}
	for i := 0; i < readers; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = r.List()
				_ = r.FindByCapability(CapabilityAgentLauncher)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_, _ = r.Get("p_a_0")
			}
		}()
	}
	wg.Wait()

	// Shutdown should not panic regardless of the final state.
	if err := r.Shutdown(); err != nil {
		t.Logf("Shutdown returned %v (acceptable — joined plugin errors)", err)
	}
}

// Concurrent Register + List must not panic and must produce a stable
// snapshot at each List call.
func TestRegistry_ConcurrentSafety(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(i int) {
			_ = r.Register(newGoodPlugin("p" + string(rune('0'+i))))
			done <- struct{}{}
		}(i)
	}
	// Reader goroutine.
	go func() {
		for i := 0; i < 100; i++ {
			_ = r.List()
		}
		done <- struct{}{}
	}()
	// 10 writers + 1 reader = 11 sends.
	for i := 0; i < 11; i++ {
		<-done
	}
	if got := len(r.List()); got != 10 {
		t.Errorf("List size = %d, want 10", got)
	}
}
