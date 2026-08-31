package process

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestCPUDeltaHelperProcess is the re-exec target for the busy/idle helper
// subprocesses below, following the same os.Args[0] re-exec pattern as
// TestLivenessHelperProcess in liveness_test.go.
func TestCPUDeltaHelperProcess(t *testing.T) {
	switch os.Getenv("NTM_CPU_DELTA_HELPER") {
	case "busy":
		// Spin a real CPU-bound loop for long enough that the parent test's
		// short sampling window reliably lands inside it.
		deadline := time.Now().Add(2 * time.Second)
		x := 0
		for time.Now().Before(deadline) {
			x++
		}
		_ = x
		os.Exit(0)
	case "idle":
		time.Sleep(2 * time.Second)
		os.Exit(0)
	default:
		return
	}
}

func startHelper(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestCPUDeltaHelperProcess")
	cmd.Env = append(os.Environ(), "NTM_CPU_DELTA_HELPER="+mode)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s helper: %v", mode, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestCPUDeltaPercent_BusyProcessReadsHigh(t *testing.T) {
	// Round-2 finding (SnowySparrow): a flat ">=50%" hard assertion over a 200ms
	// window failed at 30.0% on a contended CI/dev box -- this repo's own test
	// helpers routinely run on a machine with many concurrent builds/agents, and
	// a spinning child process is not guaranteed a full core for the whole
	// window even though it never blocks voluntarily. Fixed two ways: a longer
	// window (1s, not 200ms) to average out short scheduling gaps, and a lower,
	// contention-tolerant absolute floor -- what actually matters for
	// CPUDeltaWorking's real contract is clearing the production 5% threshold
	// by a wide, unambiguous margin, not approximating 100% on a shared box.
	cmd := startHelper(t, "busy")
	// Let the loop actually start spinning before the first sample.
	time.Sleep(50 * time.Millisecond)

	pct, err := CPUDeltaPercent(cmd.Process.Pid, time.Second)
	if err != nil {
		t.Fatalf("CPUDeltaPercent: %v", err)
	}
	if pct < 20.0 {
		t.Errorf("CPUDeltaPercent(busy) = %.1f%%, want >= 20%% (4x the 5%% production threshold) for a tight CPU loop, even on a contended box", pct)
	}
}

func TestCPUDeltaPercent_IdleProcessReadsLow(t *testing.T) {
	cmd := startHelper(t, "idle")
	time.Sleep(50 * time.Millisecond)

	pct, err := CPUDeltaPercent(cmd.Process.Pid, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("CPUDeltaPercent: %v", err)
	}
	if pct > 10.0 {
		t.Errorf("CPUDeltaPercent(idle/sleeping) = %.1f%%, want < 10%% for a process blocked in time.Sleep", pct)
	}
}

func TestCPUDeltaWorking_ThresholdSplitsBusyFromIdle(t *testing.T) {
	busy := startHelper(t, "busy")
	time.Sleep(50 * time.Millisecond)
	// 1s window, same contention-robustness reasoning as
	// TestCPUDeltaPercent_BusyProcessReadsHigh.
	working, pct, err := CPUDeltaWorking(busy.Process.Pid, time.Second, 5.0)
	if err != nil {
		t.Fatalf("CPUDeltaWorking(busy): %v", err)
	}
	if !working {
		t.Errorf("CPUDeltaWorking(busy) = false (cpu=%.1f%%), want true at a 5%% threshold", pct)
	}

	idle := startHelper(t, "idle")
	time.Sleep(50 * time.Millisecond)
	working, pct, err = CPUDeltaWorking(idle.Process.Pid, 200*time.Millisecond, 5.0)
	if err != nil {
		t.Fatalf("CPUDeltaWorking(idle): %v", err)
	}
	if working {
		t.Errorf("CPUDeltaWorking(idle) = true (cpu=%.1f%%), want false at a 5%% threshold", pct)
	}
}

func TestCPUDeltaPercent_InvalidPIDReturnsError(t *testing.T) {
	if _, err := CPUDeltaPercent(0, 10*time.Millisecond); err == nil {
		t.Error("CPUDeltaPercent(0, ...) = nil error, want an error for an invalid pid")
	}
	if _, err := CPUDeltaPercent(-1, 10*time.Millisecond); err == nil {
		t.Error("CPUDeltaPercent(-1, ...) = nil error, want an error for an invalid pid")
	}
}

func TestCPUDeltaPercent_NonexistentPIDReturnsError(t *testing.T) {
	// A pid essentially guaranteed not to exist. gopsutil.NewProcess fails
	// fast for a pid with no /proc entry (Linux) or process snapshot record.
	const unlikelyPID = 1 << 30
	if _, err := CPUDeltaPercent(unlikelyPID, 10*time.Millisecond); err == nil {
		t.Errorf("CPUDeltaPercent(%d, ...) = nil error, want an error for a pid that does not exist", unlikelyPID)
	}
}

func TestCPUDeltaPercent_DefaultsIntervalWhenNonPositive(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	// A non-positive interval must not hang the test forever or divide by
	// zero -- CPUDeltaPercent substitutes a 1s default. Use the current
	// (idle-during-test) process so this stays fast and deterministic-ish.
	start := time.Now()
	if _, err := CPUDeltaPercent(pid, 0); err != nil {
		t.Fatalf("CPUDeltaPercent(pid, 0): %v", err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("CPUDeltaPercent(pid, 0) returned after %v, want it to have honored the 1s default interval", elapsed)
	}
}
