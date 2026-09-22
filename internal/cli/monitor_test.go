package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type recordingAccountRotationRunner struct {
	run   func(context.Context) []coordinator.AccountFailoverDecision
	close func() error
}

func (r recordingAccountRotationRunner) RunOnce(ctx context.Context) []coordinator.AccountFailoverDecision {
	if r.run == nil {
		return nil
	}
	return r.run(ctx)
}

func (r recordingAccountRotationRunner) Close() error {
	if r.close == nil {
		return nil
	}
	return r.close()
}

func accountRotationMonitorFixture(t *testing.T) (*resilience.SpawnManifest, string) {
	t.Helper()
	project, dir, _ := swarmRotationCommandFixture(t)
	t.Setenv("NTM_INTERNAL_MONITOR_GENERATION", "")
	for name, value := range map[string]string{"%41-title": "cc_agents_1__cc_1", "%41-command": "claude"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldRunner := newAccountRotationRunner
	t.Cleanup(func() { newAccountRotationRunner = oldRunner })
	manifest := &resilience.SpawnManifest{
		Session: "cc_agents_1", SessionIdentity: "700:$1:1770000000", ProjectDir: project,
		Agents: []resilience.AgentConfig{{PaneID: "%41", PaneIndex: 7, Type: "cc", ProjectDir: project}},
		AccountRotation: &resilience.RotationMonitorOptions{
			Providers: []string{"claude"}, CAAMBinary: cfg.Integrations.CAAM.BinaryPath, PollSeconds: 1,
		},
	}
	return manifest, dir
}

// Exercise the actual hidden Cobra surface and resident ownership protocol.
// Stop is not acknowledged while the canonical checker still has work in
// flight, even after its context has been canceled.
func TestAccountRotationMonitorStopJoinsInFlightRecovery(t *testing.T) {
	manifest, _ := accountRotationMonitorFixture(t)
	if err := resilience.SaveManifest(manifest); err != nil {
		t.Fatal(err)
	}
	entered, canceled, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseRecovery := func() { releaseOnce.Do(func() { close(release) }) }
	newAccountRotationRunner = func(session string, options coordinator.AccountFailoverOptions) (accountRotationRunner, error) {
		if session != manifest.Session || len(options.Targets) != 1 || options.Targets["%41"].ProjectDir != manifest.ProjectDir || options.Config == nil || !options.Config.Integrations.CAAM.AutoFailover {
			return nil, errors.New("resident lost its explicit launch scope")
		}
		return recordingAccountRotationRunner{
			run: func(ctx context.Context) []coordinator.AccountFailoverDecision {
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-release
				return nil
			},
			close: func() error { close(closed); return nil },
		}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := newMonitorCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{manifest.Session})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	t.Cleanup(func() {
		cancel()
		releaseRecovery()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Error("resident runner did not close")
		}
	})
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("resident exited before its first account check: %v", err)
	case <-ctx.Done():
		t.Fatal("resident did not begin account monitoring")
	}
	status, err := resilience.ReadSessionMonitorStatus(manifest.Session)
	if err != nil || status == nil || !status.Healthy || !status.AccountRotation {
		t.Fatalf("active resident status = %+v, %v", status, err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- resilience.StopSessionMonitor(ctx, manifest.Session) }()
	select {
	case <-canceled:
	case err := <-stopped:
		t.Fatalf("stop returned before cancellation reached the checker: %v", err)
	case <-ctx.Done():
		t.Fatal("stop did not cancel in-flight recovery")
	}
	select {
	case err := <-stopped:
		t.Fatalf("stop acknowledged while recovery was still in flight: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	releaseRecovery()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("quiescent stop failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stop did not acknowledge the joined runner")
	}
	select {
	case <-closed:
	default:
		t.Fatal("resident owner was released before its runner closed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("monitor command returned an error after a clean stop: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("monitor command did not return after stop")
	}
}

func TestAccountRotationMonitorRequiresAuthorizationBeforeChecks(t *testing.T) {
	manifest, _ := accountRotationMonitorFixture(t)
	ran, closed := false, false
	newAccountRotationRunner = func(string, coordinator.AccountFailoverOptions) (accountRotationRunner, error) {
		return recordingAccountRotationRunner{
			run:   func(context.Context) []coordinator.AccountFailoverDecision { ran = true; return nil },
			close: func() error { closed = true; return nil },
		}, nil
	}
	want := errors.New("parent authorization canceled")
	err := runAccountRotationMonitor(t.Context(), manifest, func(context.Context, io.ReadCloser) error { return want })
	if !errors.Is(err, want) || ran || !closed {
		t.Fatalf("authorization refusal = %v, ran=%t closed=%t", err, ran, closed)
	}
}

func TestAccountRotationMonitorRejectsChangedScopeBeforeAcknowledgment(t *testing.T) {
	for _, cause := range []string{"missing_pane", "wrong_provider", "replaced_session", "replaced_during_preflight", "empty_scope", "remote"} {
		t.Run(cause, func(t *testing.T) {
			manifest, dir := accountRotationMonitorFixture(t)
			acknowledged, ran := false, false
			newAccountRotationRunner = func(string, coordinator.AccountFailoverOptions) (accountRotationRunner, error) {
				return recordingAccountRotationRunner{run: func(context.Context) []coordinator.AccountFailoverDecision { ran = true; return nil }}, nil
			}
			switch cause {
			case "missing_pane":
				manifest.Agents[0].PaneID = "%99"
			case "wrong_provider":
				manifest.Agents[0].Type = "cod"
			case "replaced_session":
				manifest.SessionIdentity = "700:$2:1770000001"
			case "replaced_during_preflight":
				swarmPreflightAccountRotation = func(context.Context, config.CAAMConfig) error {
					return os.WriteFile(filepath.Join(dir, "identity"), []byte("701:$1:1770000000"), 0600)
				}
			case "empty_scope":
				manifest.Agents = nil
			case "remote":
				tmux.DefaultClient.Remote = "test@host"
			}
			err := runAccountRotationMonitor(t.Context(), manifest, func(context.Context, io.ReadCloser) error { acknowledged = true; return nil })
			if err == nil || acknowledged || ran {
				t.Fatalf("changed startup scope accepted: err=%v acknowledged=%t ran=%t", err, acknowledged, ran)
			}
		})
	}
}

func TestAccountRotationMonitorStopsBeforeCheckingReplacementSession(t *testing.T) {
	manifest, dir := accountRotationMonitorFixture(t)
	ran, closed := false, false
	newAccountRotationRunner = func(string, coordinator.AccountFailoverOptions) (accountRotationRunner, error) {
		return recordingAccountRotationRunner{
			run:   func(context.Context) []coordinator.AccountFailoverDecision { ran = true; return nil },
			close: func() error { closed = true; return nil },
		}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := runAccountRotationMonitor(ctx, manifest, func(context.Context, io.ReadCloser) error {
		return os.WriteFile(filepath.Join(dir, "identity"), []byte("700:$2:1770000001"), 0600)
	})
	if err == nil || !strings.Contains(err.Error(), "original session was replaced") || ran || !closed {
		t.Fatalf("replacement session handling = %v, ran=%t closed=%t", err, ran, closed)
	}
}

func TestMonitorSnapshotCancellationStopsCapture(t *testing.T) {
	manifest, dir := accountRotationMonitorFixture(t)
	t.Setenv("NTM_SWARM_BLOCK", "capture-pane")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	outputs := make(map[string]string)
	go func() {
		defer close(done)
		captureSessionOutputs(ctx, manifest.Session, outputs)
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "blocked")); err == nil {
			break
		}
		select {
		case <-done:
			t.Fatal("snapshot returned before capture began")
		case <-ctx.Done():
			t.Fatal("snapshot capture never started")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case <-done:
		if len(outputs) != 0 {
			t.Fatalf("canceled capture published output: %v", outputs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot kept the resident busy after owner cancellation")
	}
}

func TestMonitorSummarySkipsCanceledOwner(t *testing.T) {
	manifest, _ := accountRotationMonitorFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	generateEndSessionSummary(ctx, manifest.Session, map[string]string{"%41": "Finished implementation"}, manifest)
	if _, err := os.Stat(filepath.Join(manifest.ProjectDir, ".ntm", "summaries")); !os.IsNotExist(err) {
		t.Fatalf("explicit owner cancellation created summary work: %v", err)
	}
}

func TestMonitorRemoteInvocationDoesNotCreateOwnership(t *testing.T) {
	manifest, _ := accountRotationMonitorFixture(t)
	tmux.DefaultClient.Remote = "recording@host"
	cmd := newMonitorCmd()
	cmd.SetArgs([]string{manifest.Session})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "local tmux sessions") {
		t.Fatalf("remote monitor invocation was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(resilience.ManifestDir(), "monitors")); !os.IsNotExist(err) {
		t.Fatalf("remote monitor invocation touched local ownership files: %v", err)
	}
}
