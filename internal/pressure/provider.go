package pressure

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Provider returns one or more current Readings. Implementations are
// expected to be cheap (cached/probed in the background) so Refresh can
// be called from hot paths.
type Provider interface {
	Name() string
	Read(ctx context.Context) ([]Reading, error)
}

// FakeProvider is the deterministic provider used by tests and by the
// observe-only mode when a real probe is not yet wired. Readings can be
// updated atomically with Set.
type FakeProvider struct {
	name string

	mu       sync.RWMutex
	readings []Reading
	err      error
}

// NewFakeProvider builds a fake provider with an initial reading set.
func NewFakeProvider(name string, readings ...Reading) *FakeProvider {
	rs := append([]Reading(nil), readings...)
	return &FakeProvider{name: name, readings: rs}
}

// Name returns the provider name.
func (f *FakeProvider) Name() string { return f.name }

// Read returns the most recent set of readings.
func (f *FakeProvider) Read(ctx context.Context) ([]Reading, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([]Reading, len(f.readings))
	copy(out, f.readings)
	return out, nil
}

// Set replaces the provider's readings.
func (f *FakeProvider) Set(readings ...Reading) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readings = append([]Reading(nil), readings...)
}

// SetError stores an error to be returned by the next Read call. Pass
// nil to clear.
func (f *FakeProvider) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// SystemProvider reads cheap host pressure signals from /proc when
// available. On non-Linux hosts, missing /proc files simply produce no
// readings so callers keep their deterministic low-pressure default.
type SystemProvider struct {
	CPUSampleInterval time.Duration
}

// NewSystemProvider creates the default host-pressure provider.
func NewSystemProvider() *SystemProvider {
	return &SystemProvider{CPUSampleInterval: 50 * time.Millisecond}
}

// Name returns the provider name.
func (p *SystemProvider) Name() string { return "system" }

// Read returns current CPU, load, and memory readings when the host
// exposes the needed procfs counters.
func (p *SystemProvider) Read(ctx context.Context) ([]Reading, error) {
	var readings []Reading
	if v, ok := readLoadRatio(); ok {
		readings = append(readings, Reading{Source: SourceLoad, Value: v, Unit: "ratio"})
	}
	if v, ok := readMemoryRatio(); ok {
		readings = append(readings, Reading{Source: SourceMemory, Value: v, Unit: "ratio"})
	}
	if v, ok := sampleCPURatio(ctx, p.CPUSampleInterval); ok {
		readings = append(readings, Reading{Source: SourceCPU, Value: v, Unit: "ratio"})
	}
	return readings, nil
}

func readLoadRatio() (float64, bool) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	load1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	cpus := runtime.NumCPU()
	if cpus <= 0 {
		return 0, false
	}
	return load1 / float64(cpus), true
}

func readMemoryRatio() (float64, bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	var totalKB, availableKB float64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			totalKB = value
		case "MemAvailable":
			availableKB = value
		}
	}
	if totalKB <= 0 || availableKB < 0 {
		return 0, false
	}
	used := 1 - (availableKB / totalKB)
	if used < 0 {
		used = 0
	}
	if used > 1 {
		used = 1
	}
	return used, true
}

func sampleCPURatio(ctx context.Context, interval time.Duration) (float64, bool) {
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	totalA, idleA, ok := readCPUStat()
	if !ok {
		return 0, false
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, false
	case <-timer.C:
	}
	totalB, idleB, ok := readCPUStat()
	if !ok {
		return 0, false
	}
	if totalB <= totalA || idleB < idleA {
		return 0, false
	}
	totalDelta := totalB - totalA
	idleDelta := idleB - idleA
	used := 1 - (float64(idleDelta) / float64(totalDelta))
	if used < 0 {
		used = 0
	}
	if used > 1 {
		used = 1
	}
	return used, true
}

func readCPUStat() (uint64, uint64, bool) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	lines := strings.SplitN(string(raw), "\n", 2)
	fields := strings.Fields(lines[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var total uint64
	var idle uint64
	for i, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		total += value
		if i == 3 || i == 4 {
			idle += value
		}
	}
	return total, idle, true
}
