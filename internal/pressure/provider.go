package pressure

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Provider returns one or more current Readings. Implementations are
// expected to be cheap (cached/probed in the background) so Refresh can
// be called from hot paths.
type Provider interface {
	Name() string
	Read(ctx context.Context) ([]Reading, error)
}

// SystemProvider reads cheap host pressure signals from /proc when
// available. On non-Linux hosts, missing /proc files simply produce no
// readings so callers keep their deterministic low-pressure default.
type SystemProvider struct {
	CPUSampleInterval time.Duration
}

const (
	processCountPerCPU   = 256
	minProcessCountLimit = 4096
)

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
	if v, ok := readProcessCountRatio(); ok {
		readings = append(readings, Reading{Source: SourceProcCount, Value: v, Unit: "ratio"})
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

func readProcessCountRatio() (float64, bool) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	total, ok := parseProcessTotalFromLoadavg(string(raw))
	if !ok {
		return 0, false
	}
	return processCountRatio(total, defaultProcessCountLimit())
}

func parseProcessTotalFromLoadavg(raw string) (int, bool) {
	fields := strings.Fields(raw)
	if len(fields) < 4 {
		return 0, false
	}
	parts := strings.Split(fields[3], "/")
	if len(parts) != 2 {
		return 0, false
	}
	total, err := strconv.Atoi(parts[1])
	if err != nil || total < 0 {
		return 0, false
	}
	return total, true
}

func processCountRatio(total, limit int) (float64, bool) {
	if total < 0 || limit <= 0 {
		return 0, false
	}
	ratio := float64(total) / float64(limit)
	if ratio > 1 {
		ratio = 1
	}
	return ratio, true
}

func defaultProcessCountLimit() int {
	cpus := runtime.NumCPU()
	if cpus < 1 {
		cpus = 1
	}
	limit := cpus * processCountPerCPU
	if limit < minProcessCountLimit {
		return minProcessCountLimit
	}
	return limit
}

func readMemoryRatio() (float64, bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	return parseMemoryRatio(string(raw))
}

// parseMemoryRatio is the pure /proc/meminfo parser separated from the
// file-reading wrapper so unit tests can exercise the missing-field
// shapes that show up on stripped /proc mounts (containers, sandboxes,
// pre-3.14 kernels) without touching the real filesystem (bd-mmzvs).
//
// Returns (used, true) only when BOTH MemTotal and MemAvailable were
// observed and total>0. A missing MemAvailable line — common on old
// kernels and minimal containers — must return (0, false), NOT silently
// produce used=1.0 from an absent-equals-zero numerator.
func parseMemoryRatio(raw string) (float64, bool) {
	var totalKB, availableKB float64
	var haveAvailable bool
	for _, line := range strings.Split(raw, "\n") {
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
			haveAvailable = true
		}
	}
	if totalKB <= 0 || !haveAvailable {
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
