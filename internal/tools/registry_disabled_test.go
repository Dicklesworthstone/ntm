package tools

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// probeCountingAdapter records every call that would shell out to the real
// binary, so a test can assert that a disabled tool is never executed.
type probeCountingAdapter struct {
	name ToolName

	detects  atomic.Int64
	versions atomic.Int64
	healths  atomic.Int64
	infos    atomic.Int64
}

func newProbeCountingAdapter(name ToolName) *probeCountingAdapter {
	return &probeCountingAdapter{name: name}
}

func (a *probeCountingAdapter) Name() ToolName { return a.name }

func (a *probeCountingAdapter) Detect() (string, bool) {
	a.detects.Add(1)
	return "/usr/local/bin/" + string(a.name), true
}

func (a *probeCountingAdapter) Version(context.Context) (Version, error) {
	a.versions.Add(1)
	return Version{Major: 1}, nil
}

func (a *probeCountingAdapter) Capabilities(context.Context) ([]Capability, error) {
	return []Capability{CapRobotMode}, nil
}

func (a *probeCountingAdapter) Health(context.Context) (*HealthStatus, error) {
	a.healths.Add(1)
	return &HealthStatus{Healthy: true, Message: "ok", LastChecked: time.Now()}, nil
}

func (a *probeCountingAdapter) HasCapability(context.Context, Capability) bool { return true }

func (a *probeCountingAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	a.infos.Add(1)
	path, installed := a.Detect()
	version, _ := a.Version(ctx)
	caps, _ := a.Capabilities(ctx)
	health, _ := a.Health(ctx)
	return &ToolInfo{
		Name:         a.name,
		Installed:    installed,
		Path:         path,
		Version:      version,
		Capabilities: caps,
		Health:       *health,
	}, nil
}

// totalProbes is the number of calls that would have touched the real binary.
func (a *probeCountingAdapter) totalProbes() int64 {
	return a.detects.Load() + a.versions.Load() + a.healths.Load() + a.infos.Load()
}

// TestGetAllInfoExceptNeverProbesDisabledTools is the ntm#313 regression: a
// tool the operator disabled in configuration must be reported as disabled
// without executing its binary, while its enabled siblings still probe
// normally.
func TestGetAllInfoExceptNeverProbesDisabledTools(t *testing.T) {
	r := &Registry{adapters: make(map[ToolName]Adapter)}
	off := newProbeCountingAdapter(ToolCASS)
	on := newProbeCountingAdapter(ToolBV)
	r.Register(off)
	r.Register(on)

	infos := r.GetAllInfoExcept(context.Background(), map[ToolName]bool{ToolCASS: true})

	if got := off.totalProbes(); got != 0 {
		t.Errorf("disabled tool %s was probed %d times, want 0", ToolCASS, got)
	}
	if got := on.totalProbes(); got == 0 {
		t.Errorf("enabled tool %s was never probed; the disable set leaked across tools", ToolBV)
	}

	if len(infos) != 2 {
		t.Fatalf("got %d tool infos, want 2 (a disabled tool is reported, not dropped)", len(infos))
	}

	byName := make(map[ToolName]*ToolInfo, len(infos))
	for _, info := range infos {
		byName[info.Name] = info
	}

	disabled, ok := byName[ToolCASS]
	if !ok {
		t.Fatalf("disabled tool %s missing from inventory", ToolCASS)
	}
	if !disabled.Disabled {
		t.Errorf("%s.Disabled = false, want true", ToolCASS)
	}
	// Nothing looked, so nothing may be claimed about installation.
	if disabled.Installed {
		t.Errorf("%s.Installed = true on an unprobed tool", ToolCASS)
	}
	if disabled.Health.Healthy {
		t.Errorf("%s.Health.Healthy = true on an unprobed tool", ToolCASS)
	}
	if disabled.Health.Message == "" {
		t.Errorf("%s health message is empty; a disabled tool must say why it was not probed", ToolCASS)
	}

	enabled, ok := byName[ToolBV]
	if !ok {
		t.Fatalf("enabled tool %s missing from inventory", ToolBV)
	}
	if enabled.Disabled {
		t.Errorf("%s.Disabled = true, want false", ToolBV)
	}
	if !enabled.Installed {
		t.Errorf("%s.Installed = false, want true", ToolBV)
	}
}

// TestGetAllInfoExceptNilProbesEverything guards the default: callers that
// pass no disable set (every pre-ntm#313 call site) must see unchanged
// behaviour.
func TestGetAllInfoExceptNilProbesEverything(t *testing.T) {
	r := &Registry{adapters: make(map[ToolName]Adapter)}
	a := newProbeCountingAdapter(ToolCASS)
	r.Register(a)

	infos := r.GetAllInfoExcept(context.Background(), nil)

	if a.totalProbes() == 0 {
		t.Error("nil disable set skipped a probe; probing is the default")
	}
	if len(infos) != 1 || infos[0].Disabled {
		t.Errorf("nil disable set produced %d infos with Disabled=%v, want 1 probed entry",
			len(infos), len(infos) == 1 && infos[0].Disabled)
	}
}

// slowAdapter probes slowly, so its goroutine is guaranteed to still be
// running while the caller records the disabled tools.
type slowAdapter struct {
	*probeCountingAdapter
	delay time.Duration
}

func (a *slowAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	time.Sleep(a.delay)
	return a.probeCountingAdapter.Info(ctx)
}

func (a *slowAdapter) Detect() (string, bool) {
	time.Sleep(a.delay)
	return a.probeCountingAdapter.Detect()
}

// TestDisabledToolsDoNotRaceWithProbes must be run under -race. Recording a
// disabled tool used to happen inside the goroutine-spawning loop, so this
// goroutine appended to the shared slice — and wrote the shared
// report.Tools map — while probe goroutines did the same. The map write in
// particular is a fatal concurrent map write, not merely a race, and it would
// fire on any real snapshot with one integration disabled.
func TestDisabledToolsDoNotRaceWithProbes(t *testing.T) {
	build := func() (*Registry, map[ToolName]bool) {
		r := &Registry{adapters: make(map[ToolName]Adapter)}
		// Several slow probes and several disabled tools, so the two kinds of
		// write overlap in time.
		for _, name := range []ToolName{ToolBV, ToolBD, ToolRU, ToolMS} {
			r.Register(&slowAdapter{probeCountingAdapter: newProbeCountingAdapter(name), delay: 20 * time.Millisecond})
		}
		disabled := map[ToolName]bool{}
		for _, name := range []ToolName{ToolCASS, ToolCM, ToolAM, ToolXF} {
			r.Register(newProbeCountingAdapter(name))
			disabled[name] = true
		}
		return r, disabled
	}

	t.Run("GetAllInfoExcept", func(t *testing.T) {
		r, disabled := build()
		infos := r.GetAllInfoExcept(context.Background(), disabled)
		if len(infos) != 8 {
			t.Errorf("got %d infos, want 8 (4 probed + 4 disabled)", len(infos))
		}
	})

	t.Run("GetHealthReportExcept", func(t *testing.T) {
		r, disabled := build()
		report := r.GetHealthReportExcept(context.Background(), disabled)
		if report.Total != 8 {
			t.Errorf("Total = %d, want 8", report.Total)
		}
		if report.Disabled != 4 {
			t.Errorf("Disabled = %d, want 4", report.Disabled)
		}
		if len(report.Tools) != 8 {
			t.Errorf("Tools has %d entries, want 8", len(report.Tools))
		}
		if sum := report.Healthy + report.Unhealthy + report.Missing + report.Disabled; sum != report.Total {
			t.Errorf("counts do not add up: %d != %d", sum, report.Total)
		}
	})
}

// TestGetHealthReportExceptCountsDisabledSeparately verifies the summary
// arithmetic still covers the whole registry: a disabled tool is neither
// healthy, unhealthy, nor known-missing.
func TestGetHealthReportExceptCountsDisabledSeparately(t *testing.T) {
	r := &Registry{adapters: make(map[ToolName]Adapter)}
	off := newProbeCountingAdapter(ToolCASS)
	r.Register(off)
	r.Register(newMockAdapter(ToolBV, true))
	r.Register(newMockAdapter(ToolCM, false))

	report := r.GetHealthReportExcept(context.Background(), map[ToolName]bool{ToolCASS: true})

	if got := off.totalProbes(); got != 0 {
		t.Errorf("health report probed disabled tool %d times, want 0", got)
	}
	if report.Disabled != 1 {
		t.Errorf("Disabled = %d, want 1", report.Disabled)
	}
	if report.Healthy != 1 {
		t.Errorf("Healthy = %d, want 1", report.Healthy)
	}
	if report.Missing != 1 {
		t.Errorf("Missing = %d, want 1", report.Missing)
	}
	if report.Total != 3 {
		t.Errorf("Total = %d, want 3", report.Total)
	}
	if sum := report.Healthy + report.Unhealthy + report.Missing + report.Disabled; sum != report.Total {
		t.Errorf("Healthy+Unhealthy+Missing+Disabled = %d, want Total = %d", sum, report.Total)
	}
}
