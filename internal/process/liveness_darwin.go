//go:build darwin

package process

import (
	"golang.org/x/sys/unix"
)

// darwinStatZombie is the XNU p_stat value for a zombie (SZOMB).
const darwinStatZombie = 5

// darwinChildStates returns the direct children of parentPID together with
// their p_stat process states, from a SINGLE kern.proc.all sysctl snapshot.
//
// This exists because the portable fallback path spawns `pgrep -P` (and a
// `ps -o state=` per child inside IsAlive) for every liveness question. The
// robot status surfaces ask that question once per pane, serially, so on a
// loaded workstation with a wide swarm session each ~300ms+ subprocess adds
// up to tens of seconds of wall time (bd-4479y). One in-process sysctl call
// answers "which children, and are they zombies" in microseconds with no
// staleness: it is a fresh kernel snapshot per call, so restart/reap callers
// keep their exact semantics.
func darwinChildStates(parentPID int) (pids []int, states []int, err error) {
	kprocs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, nil, err
	}
	for i := range kprocs {
		kp := &kprocs[i]
		if int(kp.Eproc.Ppid) != parentPID {
			continue
		}
		pids = append(pids, int(kp.Proc.P_pid))
		states = append(states, int(kp.Proc.P_stat))
	}
	return pids, states, nil
}

// nativeProcessState returns the single-character state code for pid from a
// kern.proc.pid sysctl lookup, replacing the `ps -o state=` subprocess. ok is
// false when the caller should fall back to the portable path.
func nativeProcessState(pid int) (state string, ok bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return "", false
	}
	// XNU p_stat values (bsd/sys/proc.h).
	switch int(kp.Proc.P_stat) {
	case 1: // SIDL
		return "I", true
	case 2: // SRUN
		return "R", true
	case 3: // SSLEEP
		return "S", true
	case 4: // SSTOP
		return "T", true
	case darwinStatZombie: // SZOMB
		return "Z", true
	default:
		return "", false
	}
}

// nativeIsAlive reports whether pid exists and is not a zombie, from one
// kern.proc.pid sysctl lookup — replacing the kill(0)+`ps -o state=` probe.
// ok is false when the lookup failed for a reason other than nonexistence
// being knowable (i.e. caller should fall back to the portable path).
func nativeIsAlive(pid int) (alive bool, ok bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if err == unix.ESRCH || err == unix.ENOENT {
			return false, true
		}
		// Sysctl succeeds with an empty record on some races; be conservative
		// and let the portable path decide.
		return false, false
	}
	if int(kp.Proc.P_pid) != pid {
		// Kernel returned no matching record: the process does not exist.
		return false, true
	}
	return int(kp.Proc.P_stat) != darwinStatZombie, true
}

// nativeChildPIDs returns parentPID's direct children without spawning a
// subprocess. ok is false when the platform snapshot failed and the caller
// should fall back to pgrep.
func nativeChildPIDs(parentPID int) ([]int, bool) {
	pids, _, err := darwinChildStates(parentPID)
	if err != nil {
		return nil, false
	}
	return pids, true
}

// nativeHasChildAlive reports whether parentPID has at least one non-zombie
// direct child, without spawning a subprocess. ok is false when the platform
// snapshot failed and the caller should fall back to the portable path.
func nativeHasChildAlive(parentPID int) (alive bool, ok bool) {
	pids, states, err := darwinChildStates(parentPID)
	if err != nil {
		return false, false
	}
	for i := range pids {
		if states[i] != darwinStatZombie {
			return true, true
		}
	}
	return false, true
}
