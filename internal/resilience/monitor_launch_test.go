package resilience

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestStartSessionMonitor_DisabledUnderGoTest pins the fork-bomb guard: under
// `go test` the shared path must refuse with ErrInternalMonitorDisabled and
// write nothing.
func TestStartSessionMonitor_DisabledUnderGoTest(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)

	result, err := StartSessionMonitor(t.Context(), SpawnMonitorRequest{
		Session:    "guardproj",
		ProjectDir: "/tmp/guardproj",
		Agents:     []AgentConfig{{PaneID: "0.1", PaneIndex: 1, Type: "claude"}},
	})
	if !errors.Is(err, ErrInternalMonitorDisabled) {
		t.Fatalf("err = %v, want ErrInternalMonitorDisabled", err)
	}
	if result != nil {
		t.Fatalf("result = %+v, want nil when disabled", result)
	}
	if _, statErr := os.Stat(filepath.Join(ManifestDir(), "guardproj.json")); !os.IsNotExist(statErr) {
		t.Fatalf("manifest must not be written when the monitor is disabled (stat err = %v)", statErr)
	}
}

func TestSessionMonitorRemotePreflightRefusesBeforeLocalSideEffects(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	oldClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("operator@remote.example")
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
	req := SpawnMonitorRequest{Session: "same-name", ProjectDir: dir}
	if err := PreflightSessionMonitor(req); err == nil || errors.Is(err, ErrInternalMonitorDisabled) || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("remote preflight = %v, want explicit unsupported remote resident", err)
	}
	if result, err := StartSessionMonitor(t.Context(), req); err == nil || result != nil {
		t.Fatalf("remote public launch result=%+v err=%v", result, err)
	}
	commands := 0
	if result, err := startSessionMonitor(t.Context(), req, func(string) (*exec.Cmd, error) {
		commands++
		return nil, errors.New("local child command must not be constructed")
	}); err == nil || result != nil || commands != 0 {
		t.Fatalf("remote launch result=%+v err=%v child commands=%d", result, err, commands)
	}
	for _, path := range []string{ManifestDir(), LogDir()} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remote monitor launch created local state at %s: %v", path, err)
		}
	}
}

// TestBuildSpawnManifest pins single-definition manifest construction: field
// mapping, agent passthrough, and the restart-unsupported (grok) filter.
func TestBuildSpawnManifest(t *testing.T) {
	req := SpawnMonitorRequest{
		Session:     "proj--lane",
		ProjectDir:  "/srv/projects/proj",
		AutoRestart: true,
		Agents: []AgentConfig{
			{PaneID: "0.1", PaneIndex: 1, Type: "claude", Model: "opus", Command: "claude --model opus"},
			{PaneID: "0.2", PaneIndex: 2, Type: "grok", Model: "", Command: "grok"},
			{PaneID: "0.3", PaneIndex: 3, Type: "codex", Model: "", Command: "codex"},
		},
	}
	m := buildSpawnManifest(req)
	if m.Session != "proj--lane" || m.ProjectDir != "/srv/projects/proj" || !m.AutoRestart {
		t.Fatalf("manifest header mismatch: %+v", m)
	}
	if len(m.Agents) != 2 {
		t.Fatalf("agents = %d, want 2 (grok filtered): %+v", len(m.Agents), m.Agents)
	}
	if m.Agents[0].PaneID != "0.1" || m.Agents[0].Type != "claude" || m.Agents[0].Command != "claude --model opus" {
		t.Fatalf("agent 0 mismatch: %+v", m.Agents[0])
	}
	if m.Agents[1].PaneID != "0.3" || m.Agents[1].Type != "codex" {
		t.Fatalf("agent 1 mismatch: %+v", m.Agents[1])
	}

	// Round-trip through the persisted form the monitor loads.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	loaded, err := LoadManifest("proj--lane")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.Session != m.Session || len(loaded.Agents) != len(m.Agents) || loaded.Agents[1].PaneID != "0.3" {
		t.Fatalf("persisted manifest mismatch: %+v", loaded)
	}
}

// The real subprocess runs only this helper, never the enclosing test suite.
// It uses the same ownership, readiness and cancellation paths as the CLI.
func TestSessionMonitorProcessHelper(t *testing.T) {
	if os.Getenv("NTM_MONITOR_TEST_HELPER") != "1" {
		return
	}
	if os.Getenv("NTM_MONITOR_TEST_MODE") == "launcher" {
		dir, _ := os.Getwd()
		session := os.Getenv("NTM_MONITOR_TEST_SESSION")
		marker := os.Getenv("NTM_MONITOR_TEST_MARKER")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := startSessionMonitor(ctx, SpawnMonitorRequest{Session: session, ProjectDir: dir},
			monitorHelperCommand(t, session, marker, ""))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(4)
		}
		os.Exit(0)
	}
	err := runSessionMonitorProcessHelper()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}

func runSessionMonitorProcessHelper() (retErr error) {
	session := os.Getenv("NTM_MONITOR_TEST_SESSION")
	lease, err := AcquireSessionMonitor(context.Background(), session)
	if err != nil {
		return err
	}
	defer lease.Close()
	defer func() { lease.Fail(retErr) }()
	marker := os.Getenv("NTM_MONITOR_TEST_MARKER")
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := writeMonitorJSON(marker+".initialized", map[string]any{"cwd": cwd, "args": os.Args}); err != nil {
		return err
	}
	switch os.Getenv("NTM_MONITOR_TEST_MODE") {
	case "fail":
		return errors.New("fixture initialization refused unavailable account source")
	case "delay":
		select {
		case <-lease.Context().Done():
			return lease.Context().Err()
		case <-time.After(10 * time.Second):
		}
	}
	if err := lease.ConfirmReady(lease.Context(), os.Stdin); err != nil {
		return err
	}
	if err := os.WriteFile(marker+".acted", []byte("authorized"), 0600); err != nil {
		return err
	}
	<-lease.Context().Done()
	// Model an in-flight loop joining. Stop must wait for this final write and
	// lease release, rather than merely observing cancellation was requested.
	time.Sleep(150 * time.Millisecond)
	return os.WriteFile(marker+".joined", []byte("joined"), 0600)
}

func monitorHelperCommand(t *testing.T, session, marker, mode string) func(string) (*exec.Cmd, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return func(got string) (*exec.Cmd, error) {
		if got != session {
			return nil, fmt.Errorf("unexpected session %q", got)
		}
		cmd := exec.Command(exe, "-test.run=^TestSessionMonitorProcessHelper$", "--", session)
		cmd.Env = append(os.Environ(), "NTM_MONITOR_TEST_HELPER=1", "NTM_MONITOR_TEST_SESSION="+session,
			"NTM_MONITOR_TEST_MARKER="+marker, "NTM_MONITOR_TEST_MODE="+mode)
		return cmd, nil
	}
}

func prepareMonitorTest(t *testing.T) string {
	t.Helper()
	if err := monitorPlatformSupported(); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv(monitorGenerationEnv, "")
	return dir
}

func TestSessionMonitorDetachedReadinessOwnershipAndStop(t *testing.T) {
	dir := prepareMonitorTest(t)
	marker := filepath.Join(dir, "monitor")
	req := SpawnMonitorRequest{Session: "resident", ProjectDir: dir, ConfigPath: filepath.Join(dir, "explicit.toml"),
		AccountRotation: &RotationMonitorOptions{Providers: []string{"claude"}, PollSeconds: 2}}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	result, err := startSessionMonitor(ctx, req, monitorHelperCommand(t, req.Session, marker, ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_ = StopSessionMonitor(ctx, req.Session)
	})
	if !result.MonitorStarted || result.MonitorPID <= 0 || result.Generation == "" {
		t.Fatalf("unacknowledged launch: %+v", result)
	}
	status, err := ReadSessionMonitorStatus(req.Session)
	if err != nil || !status.Healthy || !status.AccountRotation || status.Generation != result.Generation {
		t.Fatalf("resident status = %+v, err = %v", status, err)
	}
	var initialized struct {
		CWD  string   `json:"cwd"`
		Args []string `json:"args"`
	}
	if err := readMonitorJSON(marker+".initialized", &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.CWD != dir || !strings.Contains(strings.Join(initialized.Args, " "), "--config "+req.ConfigPath) {
		t.Fatalf("child lost launch context: %+v", initialized)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(marker + ".acted"); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("confirmed child did not execute: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	before, err := os.ReadFile(filepath.Join(ManifestDir(), req.Session+".json"))
	if err != nil {
		t.Fatal(err)
	}
	duplicate := req
	duplicate.AutoRestart = true
	if _, err := startSessionMonitor(ctx, duplicate, monitorHelperCommand(t, req.Session, marker, "")); !errors.Is(err, ErrSessionMonitorOwned) {
		t.Fatalf("duplicate startup = %v", err)
	}
	after, err := os.ReadFile(filepath.Join(ManifestDir(), req.Session+".json"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("duplicate overwrote owner manifest: %v", err)
	}
	// A stop left by a previous generation must not affect this process.
	prefix, _ := monitorPrefix(req.Session)
	if err := writeMonitorJSON(prefix+".stop.json", monitorStopRequest{Generation: "previous-generation"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * monitorControlPoll)
	status, err = ReadSessionMonitorStatus(req.Session)
	if err != nil || !status.Healthy {
		t.Fatalf("stale stop canceled owner: %+v, %v", status, err)
	}
	if err := StopSessionMonitor(ctx, req.Session); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker + ".joined"); err != nil {
		t.Fatalf("stop returned before loop joined: %v", err)
	}
	status, err = ReadSessionMonitorStatus(req.Session)
	if err != nil || status.State != "stopped" || status.Alive || status.Healthy {
		t.Fatalf("stopped status = %+v, %v", status, err)
	}
}

func TestSessionMonitorSurvivesLauncherProcessExit(t *testing.T) {
	dir := prepareMonitorTest(t)
	marker := filepath.Join(dir, "orphan")
	command, err := monitorHelperCommand(t, "survivor", marker, "launcher")("survivor")
	if err != nil {
		t.Fatal(err)
	}
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("launcher failed: %v\n%s", err, output)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_ = StopSessionMonitor(ctx, "survivor")
	})
	status, err := ReadSessionMonitorStatus("survivor")
	if err != nil || !status.Healthy || status.PID == command.ProcessState.Pid() {
		t.Fatalf("resident did not survive its launching process: %+v, %v", status, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	if err := StopSessionMonitor(ctx, "survivor"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker + ".joined"); err != nil {
		t.Fatalf("survivor was not joined: %v", err)
	}
}

func TestSessionMonitorStartupFailureAndCancellationDoNotAct(t *testing.T) {
	for _, mode := range []string{"fail", "delay"} {
		t.Run(mode, func(t *testing.T) {
			dir := prepareMonitorTest(t)
			marker := filepath.Join(dir, "monitor")
			req := SpawnMonitorRequest{Session: "refused", ProjectDir: dir}
			duration := 4 * time.Second
			if mode == "delay" {
				duration = 400 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), duration)
			defer cancel()
			result, err := startSessionMonitor(ctx, req, monitorHelperCommand(t, req.Session, marker, mode))
			if err == nil || result == nil || result.MonitorStarted {
				t.Fatalf("startup = %+v, %v", result, err)
			}
			if mode == "delay" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("startup cancellation not preserved: %v", err)
			}
			if _, err := os.Stat(marker + ".acted"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unready child acted: %v", err)
			}
			stopCtx, stopCancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer stopCancel()
			if err := StopSessionMonitor(stopCtx, req.Session); err != nil {
				t.Fatalf("failed startup retained ownership: %v", err)
			}
		})
	}
}

func TestSessionMonitorGenerationAndAuthorizationRefusal(t *testing.T) {
	dir := prepareMonitorTest(t)
	manifest := &SpawnManifest{Session: "authorization", ProjectDir: dir, MonitorGeneration: strings.Repeat("a", 32)}
	if err := SaveManifest(manifest); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monitorGenerationEnv, strings.Repeat("b", 32))
	if lease, err := AcquireSessionMonitor(t.Context(), manifest.Session); err == nil || lease != nil {
		t.Fatalf("stale child acquired manifest: %+v, %v", lease, err)
	}
	t.Setenv(monitorGenerationEnv, manifest.MonitorGeneration)
	lease, err := AcquireSessionMonitor(t.Context(), manifest.Session)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.ConfirmReady(t.Context(), input); err == nil {
		t.Fatal("EOF authorized detached monitor")
	}
	status, err := ReadSessionMonitorStatus(manifest.Session)
	if err != nil || status.Healthy || status.State == "running" {
		t.Fatalf("unauthorized status = %+v, %v", status, err)
	}
}

func TestSessionMonitorStatusRejectsReusedPIDAndStaleHeartbeat(t *testing.T) {
	dir := prepareMonitorTest(t)
	manifest := &SpawnManifest{Session: "status", ProjectDir: dir}
	if err := SaveManifest(manifest); err != nil {
		t.Fatal(err)
	}
	lease, err := AcquireSessionMonitor(t.Context(), manifest.Session)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := lease.ConfirmReady(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	lease.mu.Lock()
	status := lease.status
	lease.mu.Unlock()
	status.ProcessStarted = status.ProcessStarted.Add(-time.Hour)
	if err := writeMonitorJSON(lease.prefix+".status.json", status); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSessionMonitorStatus(manifest.Session)
	if err != nil || got.Alive || got.Healthy {
		t.Fatalf("reused PID blessed: %+v, %v", got, err)
	}
	lease.mu.Lock()
	status = lease.status
	lease.mu.Unlock()
	status.HeartbeatAt = time.Now().Add(-time.Minute)
	if err := writeMonitorJSON(lease.prefix+".status.json", status); err != nil {
		t.Fatal(err)
	}
	got, err = ReadSessionMonitorStatus(manifest.Session)
	if err != nil || !got.Alive || got.Healthy {
		t.Fatalf("stale heartbeat blessed: %+v, %v", got, err)
	}
}

func TestSessionMonitorMissingStatusAndStopDoNotCreateFiles(t *testing.T) {
	prepareMonitorTest(t)
	if _, err := ReadSessionMonitorStatus("absent"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing status = %v", err)
	}
	if err := StopSessionMonitor(t.Context(), "absent"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ManifestDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection/absent stop created state directory: %v", err)
	}
}

func TestSessionMonitorCanceledStopDoesNotCreateFiles(t *testing.T) {
	prepareMonitorTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := StopSessionMonitor(ctx, "absent"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stop = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(ManifestDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled stop created state directory: %v", err)
	}
}

func TestSessionMonitorDiscoveryFailureRefusesStopAndLaunch(t *testing.T) {
	dir := prepareMonitorTest(t)
	// An unavailable process query is not evidence that an older resident
	// does not exist. Neither stop nor start may silently accept that gap.
	t.Setenv("PATH", t.TempDir())
	const session = "discovery-unavailable"
	if err := StopSessionMonitor(t.Context(), session); err == nil {
		t.Fatal("stop accepted unavailable legacy process discovery")
	}
	if _, err := os.Stat(ManifestDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed stop created state directory: %v", err)
	}
	_, err := startSessionMonitor(t.Context(), SpawnMonitorRequest{Session: session, ProjectDir: dir}, func(string) (*exec.Cmd, error) {
		t.Fatal("child command must not be constructed without process ownership evidence")
		return nil, nil
	})
	if err == nil {
		t.Fatal("launch accepted unavailable legacy process discovery")
	}
	if _, err := LoadManifest(session); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed discovery wrote launch manifest: %v", err)
	}
}

func TestSessionMonitorRefusesManifestFromAnotherSession(t *testing.T) {
	prepareMonitorTest(t)
	manifest := buildSpawnManifest(SpawnMonitorRequest{Session: "owned-session", ProjectDir: t.TempDir()})
	if err := SaveManifest(manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Session = "different-session"
	if err := writeMonitorJSON(filepath.Join(ManifestDir(), "owned-session.json"), manifest); err != nil {
		t.Fatal(err)
	}
	lease, err := AcquireSessionMonitor(t.Context(), "owned-session")
	if err == nil {
		lease.Close()
		t.Fatal("monitor accepted a manifest from another session")
	}
	if _, err := ReadSessionMonitorStatus("owned-session"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid manifest published a monitor receipt: %v", err)
	}
}

func TestSessionMonitorRefusesLegacyUnownedProcess(t *testing.T) {
	dir := prepareMonitorTest(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("legacy process fixture needs bash exec -a")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("legacy process discovery needs pgrep")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const session = "legacy-unowned"
	command := exec.Command("bash", "-c", `exec -a "$0" sleep 60`, exe+" internal-monitor "+session)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	deadline := time.Now().Add(time.Second)
	for {
		alive, err := unownedMonitorAlive(t.Context(), session)
		if err != nil {
			t.Fatal(err)
		}
		if alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("legacy process fixture not discoverable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := StopSessionMonitor(t.Context(), session); err == nil {
		t.Fatal("stop claimed success for unowned legacy process")
	}
	_, err = startSessionMonitor(t.Context(), SpawnMonitorRequest{Session: session, ProjectDir: dir}, func(string) (*exec.Cmd, error) {
		t.Fatal("duplicate child command must not be constructed")
		return nil, nil
	})
	if !errors.Is(err, ErrSessionMonitorOwned) {
		t.Fatalf("legacy duplicate launch = %v", err)
	}
}
