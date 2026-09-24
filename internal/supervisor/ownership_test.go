//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func ownershipSupervisor(t *testing.T, dir, owner string, restarts int, delay time.Duration) *Supervisor {
	t.Helper()
	s, err := New(Config{ProjectDir: dir, SessionID: owner, MaxRestarts: restarts, RestartBackoffMax: delay})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	return s
}

func ownershipSpec(t *testing.T, dir, name, behavior string) DaemonSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return DaemonSpec{Name: name, Command: exe,
		Args: []string{"-test.run=^TestDaemonOwnershipHelper$"},
		Env: []string{"NTM_OWNERSHIP_HELPER=daemon", "NTM_OWNERSHIP_DIR=" + dir,
			"NTM_OWNERSHIP_BEHAVIOR=" + behavior, "NTM_OWNERSHIP_NAME=" + name}}
}

func ownershipWait(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func ownershipReadRecord(t *testing.T, dir string) (*daemonOwnershipRecord, string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, ".ntm", "pids", ".daemon-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("ownership records = %v, %v", paths, err)
	}
	record, err := readDaemonOwnership(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	return record, paths[0]
}

// The subprocess is the actual launched daemon, not a mocked exec port.
// Supervisor mode also allows the parent test to kill the controller while
// leaving its separately grouped daemon alive, reproducing an orphaned launch.
func TestDaemonOwnershipHelper(t *testing.T) {
	mode := os.Getenv("NTM_OWNERSHIP_HELPER")
	if mode == "" {
		return
	}
	dir := os.Getenv("NTM_OWNERSHIP_DIR")
	if mode == "prepared" {
		s := ownershipSupervisor(t, dir, "crashed", 1, time.Second)
		o, err := acquireDaemonOwnership(s.ntmDir, "service", "crashed")
		if err != nil {
			t.Fatal(err)
		}
		if err := o.prepare(); err != nil {
			t.Fatal(err)
		}
		os.Exit(23) // no defers: simulate a crash in the exec/PID-save window
	}
	if mode == "supervisor" {
		s := ownershipSupervisor(t, dir, "crashed", 1, time.Second)
		if err := s.Start(ownershipSpec(t, dir, "service", "wait")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "controller-ready"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		select {} // killed by the parent; Shutdown deliberately does not run
	}
	if mode != "daemon" {
		t.Fatalf("unknown helper mode %q", mode)
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM, os.Interrupt)
	name := os.Getenv("NTM_OWNERSHIP_NAME")
	f, err := os.OpenFile(filepath.Join(dir, name+"-launches"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(f, os.Getpid())
	_ = f.Close()
	if os.Getenv("NTM_OWNERSHIP_BEHAVIOR") == "crash" {
		os.Exit(7)
	}
	if os.Getenv("NTM_OWNERSHIP_BEHAVIOR") == "slow-stop" {
		<-term
		_ = os.WriteFile(filepath.Join(dir, "terminating"), []byte("ready"), 0600)
		for {
			if _, err := os.Stat(filepath.Join(dir, "finish-stop")); err == nil {
				os.Exit(0)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	<-term
	os.Exit(0)
}

func TestDaemonOwnershipConcurrentProjectLaunches(t *testing.T) {
	dir := t.TempDir()
	const contenders = 16
	all := make([]*Supervisor, contenders)
	for i := range all {
		all[i] = ownershipSupervisor(t, dir, fmt.Sprintf("session-%d", i), 1, time.Second)
	}
	spec := ownershipSpec(t, dir, "service", "wait") // deliberately no listening port
	start := make(chan struct{})
	results := make(chan error, contenders)
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func(s *Supervisor) { defer wg.Done(); <-start; results <- s.Start(spec) }(s)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrDaemonOwned) {
			t.Fatalf("unexpected admission error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("launched %d competing daemons, want one", wins)
	}
	ownershipWait(t, "daemon launch", func() bool { _, e := os.Stat(filepath.Join(dir, "service-launches")); return e == nil })
	data, err := os.ReadFile(filepath.Join(dir, "service-launches"))
	if err != nil || len(strings.Fields(string(data))) != 1 {
		t.Fatalf("actual child launches: %q, %v", data, err)
	}
}

func TestDaemonOwnershipAliasesAndIndependentScopes(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	a := ownershipSupervisor(t, dir, "A", 1, time.Second)
	b := ownershipSupervisor(t, alias, "B", 1, time.Second)
	if err := a.Start(ownershipSpec(t, dir, "service", "wait")); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ownershipSpec(t, dir, "service", "wait")); !errors.Is(err, ErrDaemonOwned) {
		t.Fatalf("alias bypass: %v", err)
	}
	if err := b.Start(ownershipSpec(t, dir, "different", "wait")); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	c := ownershipSupervisor(t, other, "C", 1, time.Second)
	if err := c.Start(ownershipSpec(t, other, "service", "wait")); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonOwnershipRetainedThroughRestartAndStop(t *testing.T) {
	dir := t.TempDir()
	a := ownershipSupervisor(t, dir, "A", 1, 250*time.Millisecond)
	b := ownershipSupervisor(t, dir, "B", 1, time.Second)
	if err := a.Start(ownershipSpec(t, dir, "service", "crash")); err != nil {
		t.Fatal(err)
	}
	ownershipWait(t, "restart backoff", func() bool { d, _ := a.GetDaemon("service"); return d.State == StateRestarting })
	if err := b.Start(ownershipSpec(t, dir, "service", "wait")); !errors.Is(err, ErrDaemonOwned) {
		t.Fatalf("lost ownership during backoff: %v", err)
	}
	ownershipWait(t, "restart exhaustion", func() bool { d, _ := a.GetDaemon("service"); return d.State == StateFailed })
	if err := b.Start(ownershipSpec(t, dir, "service", "wait")); err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := b.Shutdown(); err != nil {
		t.Fatal(err)
	}
	record, _ := ownershipReadRecord(t, dir)
	if record.Phase != "stopped" || record.PID != 0 {
		t.Fatalf("final record: %+v", record)
	}
}

func TestDaemonOwnershipStopKeepsFenceUntilChildExits(t *testing.T) {
	dir := t.TempDir()
	a := ownershipSupervisor(t, dir, "A", 1, time.Second)
	b := ownershipSupervisor(t, dir, "B", 1, time.Second)
	if err := a.Start(ownershipSpec(t, dir, "service", "slow-stop")); err != nil {
		t.Fatal(err)
	}
	ownershipWait(t, "daemon readiness", func() bool { _, e := os.Stat(filepath.Join(dir, "service-launches")); return e == nil })
	done := make(chan error, 1)
	go func() { done <- a.Shutdown() }()
	ownershipWait(t, "graceful stop", func() bool { _, e := os.Stat(filepath.Join(dir, "terminating")); return e == nil })
	if err := b.Start(ownershipSpec(t, dir, "service", "wait")); !errors.Is(err, ErrDaemonOwned) {
		t.Fatalf("lost ownership while stopping: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "finish-stop"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ownershipSpec(t, dir, "service", "wait")); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonOwnershipFailedExecReleasesFence(t *testing.T) {
	dir := t.TempDir()
	a := ownershipSupervisor(t, dir, "A", 1, time.Second)
	badBinary := filepath.Join(dir, "bad-executable")
	if err := os.WriteFile(badBinary, []byte("not an executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(DaemonSpec{Name: "service", Command: badBinary}); err == nil {
		t.Fatal("invalid binary launched")
	}
	b := ownershipSupervisor(t, dir, "B", 1, time.Second)
	if err := b.Start(ownershipSpec(t, dir, "service", "wait")); err != nil {
		t.Fatalf("failed exec leaked owner: %v", err)
	}
}

func TestDaemonOwnershipFailedJournalPreventsExec(t *testing.T) {
	dir := t.TempDir()
	s := ownershipSupervisor(t, dir, "A", 1, time.Second)
	o, err := acquireDaemonOwnership(s.ntmDir, "service", "A")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(o.path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := o.closeLocked(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ownershipSpec(t, dir, "service", "wait")); !errors.Is(err, ErrDaemonRecoveryRequired) {
		t.Fatalf("unwritable journal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "service-launches")); !os.IsNotExist(err) {
		t.Fatalf("child launched: %v", err)
	}
}

func TestDaemonOwnershipPreparedCrashRequiresInspection(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestDaemonOwnershipHelper$")
	cmd.Env = append(os.Environ(), "NTM_OWNERSHIP_HELPER=prepared", "NTM_OWNERSHIP_DIR="+dir)
	if err := cmd.Run(); err == nil {
		t.Fatal("helper did not crash")
	}
	b := ownershipSupervisor(t, dir, "B", 1, time.Second)
	if err := b.Start(ownershipSpec(t, dir, "service", "wait")); !errors.Is(err, ErrDaemonRecoveryRequired) {
		t.Fatalf("uncertain launch replayed: %v", err)
	}
	record, _ := ownershipReadRecord(t, dir)
	if record.Phase != "launching" || record.OwnerID != "crashed" {
		t.Fatalf("discarded crash evidence: %+v", record)
	}
}

func TestDaemonOwnershipControllerCrashDoesNotDuplicateOrKillOrphan(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestDaemonOwnershipHelper$")
	cmd.Env = append(os.Environ(), "NTM_OWNERSHIP_HELPER=supervisor", "NTM_OWNERSHIP_DIR="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	ownershipWait(t, "controller startup", func() bool { _, e := os.Stat(filepath.Join(dir, "controller-ready")); return e == nil })
	record, path := ownershipReadRecord(t, dir)
	pid := record.PID
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	b := ownershipSupervisor(t, dir, "B", 1, time.Second)
	if err := b.Start(ownershipSpec(t, dir, "service", "wait")); !errors.Is(err, ErrDaemonOwned) {
		t.Fatalf("independent live controller lost its fence: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	err = b.Start(ownershipSpec(t, dir, "service", "wait"))
	var ownershipErr *DaemonOwnershipError
	if !errors.Is(err, ErrDaemonRecoveryRequired) || !errors.As(err, &ownershipErr) || ownershipErr.PID != pid || ownershipErr.Path != path {
		t.Fatalf("orphan was not identified: %v", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("recovery signaled orphan: %v", err)
	}
}

func TestDaemonOwnershipDeadRecordedProcessCanRestart(t *testing.T) {
	dir := t.TempDir()
	s := ownershipSupervisor(t, dir, "A", 1, time.Second)
	o, err := acquireDaemonOwnership(s.ntmDir, "service", "old")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := o.started(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := o.closeLocked(); err != nil {
		t.Fatal(err)
	} // no stopped record; mimic controller exit
	if err := s.Start(ownershipSpec(t, dir, "service", "wait")); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonOwnershipOldControllerCannotRemoveNewPIDRecord(t *testing.T) {
	dir := t.TempDir()
	a := ownershipSupervisor(t, dir, "same-session", -1, time.Second)
	if err := a.Start(ownershipSpec(t, dir, "service", "crash")); err != nil {
		t.Fatal(err)
	}
	ownershipWait(t, "failed generation", func() bool { d, _ := a.GetDaemon("service"); return d.State == StateFailed })
	b := ownershipSupervisor(t, dir, "same-session", 1, time.Second)
	if err := b.Start(ownershipSpec(t, dir, "service", "wait")); err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(b.pidPath("service"))
	if err != nil {
		t.Fatalf("old controller deleted new PID record: %v", err)
	}
	var info PIDFileInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	d, _ := b.GetDaemon("service")
	if info.PID != d.PID {
		t.Fatalf("PID record=%d current=%d", info.PID, d.PID)
	}
}

func TestDaemonOwnershipRefusesInvalidRecordAndSymlink(t *testing.T) {
	for _, content := range []string{"{}", "null", `{"version":1,"name":"service","owner_id":"old","phase":"stopped","pid":42}`, "{} {}"} {
		t.Run(content, func(t *testing.T) {
			dir := t.TempDir()
			s := ownershipSupervisor(t, dir, "A", 1, time.Second)
			o, err := acquireDaemonOwnership(s.ntmDir, "service", "A")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(o.path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			_ = o.closeLocked()
			if err := s.Start(ownershipSpec(t, dir, "service", "wait")); !errors.Is(err, ErrDaemonRecoveryRequired) {
				t.Fatalf("invalid evidence accepted: %v", err)
			}
		})
	}
	dir := t.TempDir()
	s := ownershipSupervisor(t, dir, "A", 1, time.Second)
	o, err := acquireDaemonOwnership(s.ntmDir, "service", "A")
	if err != nil {
		t.Fatal(err)
	}
	_ = o.closeLocked()
	outside := filepath.Join(t.TempDir(), "record")
	if err := os.WriteFile(outside, []byte("do not change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, o.path); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ownershipSpec(t, dir, "service", "wait")); !errors.Is(err, ErrDaemonRecoveryRequired) {
		t.Fatalf("symlink evidence accepted: %v", err)
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "do not change" {
		t.Fatal("modified symlink target")
	}
}
