package process

import (
	"fmt"
	"time"

	gopsutil "github.com/shirou/gopsutil/v4/process"
)

// CPUDeltaWorking samples cumulative CPU time (user+system) for pid and its
// full descendant process tree, twice, `interval` apart, and reports whether
// the tree's CPU usage over that window is at or above thresholdPct.
//
// This exists as a liveness fallback for exactly the case a screen/title
// observation cannot resolve (ntm-tnh1, dea/agent-factory fleet, 2026-08-30):
// a working `claude` pane can render a frozen frame with an empty composer
// for over an hour while genuinely producing commits and mail the whole
// time -- the canonical observer correctly has no title/screen signal to
// read and reports StateUnknown, but the pane's process tree is not idle at
// all. A lifetime-average %CPU reading (e.g. via `ps`) is too diluted by
// process age to catch this; a short windowed delta is not.
//
// Uses gopsutil (already a dependency here, see StartTime above) rather than
// a hand-rolled /proc reader, so this is cross-platform for free -- Linux,
// macOS, and Windows all implement Process.Times().
//
// Cost: this function blocks for `interval`. It is deliberately NOT wired
// into any hot per-pane status path in this patch -- see the call site in
// is_working.go's applyCanonicalWorkSafety for the open question this needs
// resolved before merge (batch multiple panes' samples into one sleep, the
// way the Python interim probe does, or run on a slower/cached cadence
// instead of inline per unknown-pane).
func CPUDeltaWorking(pid int, interval time.Duration, thresholdPct float64) (bool, float64, error) {
	pct, err := CPUDeltaPercent(pid, interval)
	if err != nil {
		return false, 0, err
	}
	return pct >= thresholdPct, pct, nil
}

// CPUDeltaPercent returns the percentage of wall-clock time pid's process
// tree spent on CPU over the given interval. See CPUDeltaWorking for context.
func CPUDeltaPercent(pid int, interval time.Duration) (float64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d", pid)
	}
	if interval <= 0 {
		interval = time.Second
	}

	t0, err := cpuTreeTicks(pid)
	if err != nil {
		return 0, err
	}
	time.Sleep(interval)
	t1, err := cpuTreeTicks(pid)
	if err != nil {
		return 0, err
	}

	delta := t1 - t0
	if delta < 0 {
		// A respawned pid reusing the same number mid-sample can make t1 <
		// t0; floor at 0 rather than report a negative CPU percent.
		delta = 0
	}
	elapsed := interval.Seconds()
	if elapsed <= 0 {
		elapsed = 0.001
	}
	return (delta / elapsed) * 100.0, nil
}

// cpuTreeTicks sums User+System seconds (gopsutil reports these as floats,
// already normalized across platforms) across pid and every live descendant
// at the moment of the call. A descendant that exits between the tree walk
// and its own Times() call is skipped, not fatal -- the same race tolerance
// getChildPIDs already documents for its own callers.
func cpuTreeTicks(pid int) (float64, error) {
	root, err := gopsutil.NewProcess(int32(pid))
	if err != nil {
		return 0, fmt.Errorf("process %d: %w", pid, err)
	}

	total := 0.0
	seen := map[int]struct{}{pid: {}}
	frontier := []int{pid}
	addTimes := func(p *gopsutil.Process) {
		if times, terr := p.Times(); terr == nil {
			total += times.User + times.System
		}
	}
	addTimes(root)

	for len(frontier) > 0 {
		var next []int
		for _, parent := range frontier {
			for _, child := range GetChildPIDs(parent, 256) {
				if _, ok := seen[child]; ok {
					continue
				}
				seen[child] = struct{}{}
				proc, perr := gopsutil.NewProcess(int32(child))
				if perr != nil {
					continue // exited between the walk and here; not fatal
				}
				addTimes(proc)
				next = append(next, child)
			}
		}
		frontier = next
	}
	return total, nil
}
