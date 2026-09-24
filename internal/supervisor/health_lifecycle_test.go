//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func healthSupervisor(t *testing.T, dir, owner string, restarts int, delay time.Duration) *Supervisor {
	t.Helper()
	s, err := New(Config{ProjectDir: dir, SessionID: owner, MaxRestarts: restarts,
		HealthInterval: 100 * time.Millisecond, RestartBackoffMax: delay})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	return s
}

func TestDaemonHealthCommandUsesLaunchScope(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "daemon-scope")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_HEALTH_PARENT", "at-launch")
	t.Setenv("NTM_HEALTH_LATE", "")
	s := healthSupervisor(t, dir, "health-scope", 1, time.Second)
	spec := ownershipSpec(t, dir, "service", "wait")
	spec.WorkDir = work
	spec.Env = append(spec.Env, "NTM_HEALTH_SCOPE=correct")
	spec.HealthCmd = []string{"sh", "-c", `test "$NTM_HEALTH_SCOPE" = correct && test "$PWD" = "$1" && test "$NTM_HEALTH_PARENT" = at-launch && test -z "$NTM_HEALTH_LATE"`, "sh", work}
	if err := s.Start(spec); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_HEALTH_PARENT", "changed-later")
	t.Setenv("NTM_HEALTH_LATE", "must-not-leak-into-probe")
	ownershipWait(t, "scoped health command", func() bool { d, _ := s.GetDaemon("service"); return d.State == StateRunning })
	d, _ := s.GetDaemon("service")
	if d.HealthMode != "command" || d.LastHealth.IsZero() || d.HealthError != "" {
		t.Fatalf("health evidence: %+v", d)
	}
}

func TestDaemonHealthProcessOnlyDoesNotInventReadiness(t *testing.T) {
	dir := t.TempDir()
	s := healthSupervisor(t, dir, "process-only", 1, time.Second)
	if err := s.Start(ownershipSpec(t, dir, "service", "wait")); err != nil {
		t.Fatal(err)
	}
	ownershipWait(t, "process liveness", func() bool { d, _ := s.GetDaemon("service"); return d.State == StateRunning })
	d, _ := s.GetDaemon("service")
	if d.HealthMode != "process" || !d.LastHealth.IsZero() {
		t.Fatalf("fabricated readiness evidence: %+v", d)
	}
}

// A real HTTP response whose body is held open makes cancellation observable
// independently of daemon termination. The daemon itself delays graceful exit.
func TestDaemonHealthShutdownCancelsInFlightResponse(t *testing.T) {
	dir := t.TempDir()
	started, cancelled := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		once.Do(func() { close(started) })
		<-r.Context().Done()
		close(cancelled)
	}))
	t.Cleanup(server.Close)
	s := healthSupervisor(t, dir, "cancel-health", 1, time.Second)
	spec := ownershipSpec(t, dir, "service", "slow-stop")
	if err := s.Start(spec); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	d := s.daemons["service"]
	s.mu.RUnlock()
	d.mu.Lock()
	d.Spec.HealthURL = server.URL // real endpoint; bypass port allocation only
	d.mu.Unlock()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("health request never started")
	}
	done := make(chan error, 1)
	go func() { done <- s.Shutdown() }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the in-flight body read")
	}
	// Observation stopped while graceful daemon shutdown is still in progress.
	select {
	case <-done:
		t.Fatal("daemon did not retain its graceful shutdown window")
	default:
	}
	if err := os.WriteFile(filepath.Join(dir, "finish-stop"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	snapshot, _ := s.GetDaemon("service")
	if !snapshot.LastHealth.IsZero() || snapshot.State != StateStopped {
		t.Fatalf("late cancelled health published: %+v", snapshot)
	}
	select {
	case <-d.monitorDone:
	default:
		t.Fatal("ownership released before monitor completed")
	}
}

func TestDaemonHealthExitCancelsBeforeRecovery(t *testing.T) {
	dir := t.TempDir()
	started, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	t.Cleanup(server.Close)
	s := healthSupervisor(t, dir, "exit-health", -1, time.Second)
	if err := s.Start(ownershipSpec(t, dir, "service", "wait")); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	d := s.daemons["service"]
	s.mu.RUnlock()
	d.mu.Lock()
	d.Spec.HealthURL = server.URL
	d.mu.Unlock()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("health request never started")
	}
	if err := d.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("process exit did not cancel health request")
	}
	ownershipWait(t, "finished recovery", func() bool { d, _ := s.GetDaemon("service"); return d.State == StateFailed })
	select {
	case <-d.monitorDone:
	default:
		t.Fatal("recovery left the old monitor active")
	}
}

func TestDaemonHealthRejectsIncompleteProtocolEvidence(t *testing.T) {
	const good = `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"test"}}}`
	for _, tc := range []struct {
		name, body          string
		mcp, good, truncate bool
	}{
		{"http ok", "OK", false, true, false},
		{"http truncated", "OK", false, false, true},
		{"http oversized", strings.Repeat("x", maxDaemonHealthResponse+1), false, false, false},
		{"mcp ok", good, true, true, false},
		{"mcp truncated", good, true, false, true},
		{"mcp trailing object", good + " {}", true, false, false},
		{"mcp missing response ID", `{"jsonrpc":"2.0","result":{"serverInfo":{"name":"test"}}}`, true, false, false},
		{"mcp wrong response ID", strings.Replace(good, `"id":1`, `"id":2`, 1), true, false, false},
		{"mcp serverInfo scalar", `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":true}}`, true, false, false},
		{"mcp error", `{"jsonrpc":"2.0","id":1,"error":{"code":-1},"result":{"serverInfo":{"name":"test"}}}`, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.truncate {
					w.Header().Set("Content-Length", fmt.Sprint(len(tc.body)+20))
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			s := &Supervisor{}
			got := s.checkHealthHTTP(server.URL)
			if tc.mcp {
				got = s.checkHealthMCP(server.URL)
			}
			if got != tc.good {
				t.Fatalf("healthy=%v, want %v", got, tc.good)
			}
		})
	}
}

func TestDaemonHealthCommandCancellationIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := probeDaemonHealth(ctx, DaemonSpec{HealthCmd: []string{"sh", "-c", "exec sleep 30"}}, t.TempDir())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost deadline cause: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("health command did not stop promptly")
	}
}

func TestDaemonHealthFailureAndRecoveryRetainTruthfulEvidence(t *testing.T) {
	dir := t.TempDir()
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, "OK")
	}))
	t.Cleanup(server.Close)
	s, err := New(Config{ProjectDir: dir, SessionID: "health-recovery", HealthInterval: 100 * time.Millisecond,
		StartupHealthTimeout: 100 * time.Millisecond, MaxRestarts: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	if err := s.Start(ownershipSpec(t, dir, "service", "wait")); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	d := s.daemons["service"]
	s.mu.RUnlock()
	d.mu.Lock()
	d.Spec.HealthURL = server.URL
	d.mu.Unlock()
	ownershipWait(t, "initial unhealthy status", func() bool { d, _ := s.GetDaemon("service"); return d.State == StateUnhealthy })
	initial, _ := s.GetDaemon("service")
	if !initial.LastHealth.IsZero() || initial.HealthMode != "http" || !strings.Contains(initial.HealthError, "503") {
		t.Fatalf("failed startup fabricated health: %+v", initial)
	}
	healthy.Store(true)
	ownershipWait(t, "health recovery", func() bool { d, _ := s.GetDaemon("service"); return d.State == StateRunning })
	recovered, _ := s.GetDaemon("service")
	if recovered.LastHealth.IsZero() || recovered.HealthError != "" || recovered.PID != initial.PID {
		t.Fatalf("recovery evidence: %+v", recovered)
	}
	healthy.Store(false)
	ownershipWait(t, "lost health", func() bool { d, _ := s.GetDaemon("service"); return d.State == StateUnhealthy })
	failed, _ := s.GetDaemon("service")
	if failed.LastHealth.Before(recovered.LastHealth) || failed.HealthError == "" || failed.Restarts != 0 || failed.PID != initial.PID {
		t.Fatalf("health failure replaced process or lost evidence: %+v", failed)
	}
}

func TestDaemonOwnershipExhaustionRetiresPID(t *testing.T) {
	dir := t.TempDir()
	s := healthSupervisor(t, dir, "exhaustion-cleanup", -1, time.Second)
	if err := s.Start(ownershipSpec(t, dir, "service", "crash")); err != nil {
		t.Fatal(err)
	}
	ownershipWait(t, "exhausted recovery", func() bool { d, _ := s.GetDaemon("service"); return d.State == StateFailed })
	if _, err := os.Stat(s.pidPath("service")); !os.IsNotExist(err) {
		t.Fatalf("exhausted recovery retained a stale compatibility PID file: %v", err)
	}
}
