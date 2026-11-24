package cli

// Behavioral tests for the real `ntm memory serve` (bd-ws1-truth-safety-l5ddi.2):
//   - missing cm binary -> fail fast and loud (non-nil error naming the binary
//     and the install hint), never a silent retry loop;
//   - a stub cm binary on PATH is actually launched and supervised: the
//     process is observed alive, health transitions are logged, and Ctrl-C
//     (context cancel) tears the daemon down;
//   - grep-gate: the false "spawn auto-starts the memory daemon" advice is
//     gone from code and docs (WS0-G3 only parses fenced examples and cannot
//     read prose, so the sentence's absence needs its own assertion).

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/supervisor"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestMemoryServeMissingBinaryFailsFastAndLoud(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH semantics differ on windows")
	}
	// An empty PATH dir: cm is definitively not installed.
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())

	start := time.Now()
	err := runMemoryServe(context.Background(), io.Discard, 0)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("runMemoryServe() must fail when cm is not on PATH")
	}
	if elapsed > 3*time.Second {
		t.Errorf("runMemoryServe() took %v; missing cm must fail fast", elapsed)
	}
	msg := err.Error()
	if !strings.Contains(msg, "cm binary not found in PATH") {
		t.Errorf("error must name the missing cm binary; got: %v", err)
	}
	if !strings.Contains(msg, "install") {
		t.Errorf("error must carry an install hint; got: %v", err)
	}
}

// syncBuffer is a goroutine-safe writer for capturing serve output while the
// test polls it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestMemoryServeStubCMHelper implements the stub cm 0.2.x MCP JSON-RPC server
// inside the already-compiled test binary so tests avoid shelling out to a cold
// `go build` while still supervising a real separate OS process.
func TestMemoryServeStubCMHelper(t *testing.T) {
	if os.Getenv("STUB_CM_HELPER") != "1" {
		return
	}
	if os.Getenv("STUB_CM_DISABLE_RESTARTS") == "1" {
		target := os.Getenv("STUB_CM_SELF")
		if target == "" {
			fmt.Fprintln(os.Stderr, "missing STUB_CM_SELF")
			os.Exit(2)
		}
		if err := os.Chmod(target, 0644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(1)
	}

	var args []string
	for i, a := range os.Args[1:] {
		if a == "--" {
			args = os.Args[i+2:]
			break
		}
	}
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("stubcm", flag.ExitOnError)
	port := fs.Int("port", 8200, "")
	_ = fs.Parse(args)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize", "tools/list":
			// serverInfo is mandatory in a real MCP initialize result; the
			// probe requires it (bd-2c0yh.3), so the stub must send it too.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]string{"name": "stub-cm"}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "Unsupported method"},
			})
		}
	})
	if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func buildStubCMBinary(t *testing.T, binDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub daemon behavioral test requires unix process semantics")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	script := fmt.Sprintf("#!/bin/sh\nSTUB_CM_HELPER=1 STUB_CM_SELF=\"$0\" exec %s -test.run='^TestMemoryServeStubCMHelper$' -- \"$@\"\n", tmux.ShellQuote(exe))
	if err := os.WriteFile(filepath.Join(binDir, "cm"), []byte(script), 0755); err != nil {
		t.Fatalf("install stub cm: %v", err)
	}
}

func TestMemoryServeSupervisesStubCM(t *testing.T) {
	binDir := t.TempDir()
	buildStubCMBinary(t, binDir)

	projDir := t.TempDir()
	t.Chdir(projDir)
	// PATH contains ONLY the stub: proves memory serve launched exactly this
	// binary, not some ambient cm install.
	t.Setenv("PATH", binDir)

	port, err := freeLocalPort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}

	origPoll := memoryServePollInterval
	origHealth := memoryServeHealthInterval
	origStartup := memoryServeStartupDelay
	memoryServePollInterval = 25 * time.Millisecond
	memoryServeHealthInterval = 100 * time.Millisecond
	memoryServeStartupDelay = 25 * time.Millisecond
	defer func() {
		memoryServePollInterval = origPoll
		memoryServeHealthInterval = origHealth
		memoryServeStartupDelay = origStartup
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := &syncBuffer{}
	errCh := make(chan error, 1)
	go func() { errCh <- runMemoryServe(ctx, out, port) }()

	// The daemon process must be observable: PID file written, process alive.
	sessionID := fmt.Sprintf("memory-serve-%d", os.Getpid())
	pidPath := filepath.Join(projDir, ".ntm", "pids", fmt.Sprintf("cm-%s.pid", sessionID))
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidPath)
		if err == nil {
			var info supervisor.PIDFileInfo
			if json.Unmarshal(data, &info) == nil && info.PID > 0 {
				pid = info.PID
				break
			}
		}
		select {
		case err := <-errCh:
			t.Fatalf("runMemoryServe() exited early: %v\noutput:\n%s", err, out.String())
		case <-time.After(15 * time.Millisecond):
		}
	}
	if pid == 0 {
		t.Fatalf("cm PID file never appeared at %s\noutput:\n%s", pidPath, out.String())
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("supervised cm process %d not alive: %v", pid, err)
	}

	// Health transitions must be visible in both the foreground output and
	// the supervisor log.
	logPath := filepath.Join(projDir, ".ntm", "logs", fmt.Sprintf("cm-%s.log", sessionID))
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "starting -> running") {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "starting -> running") {
		logData, _ := os.ReadFile(logPath)
		t.Fatalf("foreground output never reported the starting -> running transition\noutput:\n%s\nlog:\n%s", out.String(), logData)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read supervisor log: %v", err)
	}
	if !strings.Contains(string(logData), "launched daemon cm") {
		t.Errorf("supervisor log missing launch line:\n%s", logData)
	}
	if !strings.Contains(string(logData), "state starting -> running") {
		t.Errorf("supervisor log missing health transition:\n%s", logData)
	}

	// Ctrl-C (context cancel) must stop the supervised process.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runMemoryServe() returned error on shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runMemoryServe() did not return after context cancel")
	}
	stopped := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			stopped = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !stopped {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("supervised cm process %d still alive after shutdown", pid)
	}
}

// TestMemoryServeRefusesOccupiedPort (bd-2c0yh.1): when the requested port is
// already held — almost always by another cm serving the same store —
// `ntm memory serve` must FAIL loudly instead of silently falling back to a
// random port and starting a second cm against the same database.
func TestMemoryServeRefusesOccupiedPort(t *testing.T) {
	binDir := t.TempDir()
	buildStubCMBinary(t, binDir)

	projDir := t.TempDir()
	t.Chdir(projDir)
	t.Setenv("PATH", binDir)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	out := &syncBuffer{}
	err = runMemoryServe(context.Background(), out, port)
	if err == nil {
		t.Fatalf("runMemoryServe must fail when the requested port is occupied\noutput:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("error must say the port is already in use, got: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", port)) {
		t.Errorf("error must name the contested port %d, got: %v", port, err)
	}
}

func TestMemoryServeReportsExhaustedRestartLaunchFailures(t *testing.T) {
	binDir := t.TempDir()
	buildStubCMBinary(t, binDir)
	projDir := t.TempDir()
	t.Chdir(projDir)
	t.Setenv("PATH", binDir)
	t.Setenv("STUB_CM_DISABLE_RESTARTS", "1")
	port, err := freeLocalPort()
	if err != nil {
		t.Fatal(err)
	}

	// Exercise the actual Cobra surface with the full production restart budget
	// (DefaultMaxRestarts=5) while capping restart backoff for fast hermetic execution.
	// A wedged StateRestarting used to keep this command alive until interrupted.
	origPoll := memoryServePollInterval
	origBackoff := memoryServeRestartBackoffMax
	memoryServePollInterval = 10 * time.Millisecond
	memoryServeRestartBackoffMax = 20 * time.Millisecond
	defer func() {
		memoryServePollInterval = origPoll
		memoryServeRestartBackoffMax = origBackoff
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := newMemoryServeCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--port", fmt.Sprint(port)})
	err = cmd.ExecuteContext(ctx)
	if ctx.Err() != nil {
		t.Fatalf("memory serve hung after restart launch failure: %v\n%s", ctx.Err(), out.String())
	}
	if err == nil || !strings.Contains(err.Error(), "failed permanently after 5 restart attempts") {
		t.Fatalf("memory serve must report exhausted recovery, got %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "restarting -> failed") {
		t.Fatalf("terminal recovery state missing from command output:\n%s", out.String())
	}

	sessionID := fmt.Sprintf("memory-serve-%d", os.Getpid())
	logPath := filepath.Join(projDir, ".ntm", "logs", fmt.Sprintf("cm-%s.log", sessionID))
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(logData), "restart failed:") != supervisor.DefaultMaxRestarts || !strings.Contains(string(logData), "max restarts (5) exceeded") {
		t.Fatalf("supervisor log must explain every failed launch and exhaustion:\n%s", logData)
	}
	pidPath := filepath.Join(projDir, ".ntm", "pids", fmt.Sprintf("cm-%s.pid", sessionID))
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("memory serve left a PID file after permanent failure: %v", err)
	}
}

// TestNoFalseSpawnAutoStartAdvice is the grep-gate for the corrected docs/help
// text: `ntm spawn` does NOT auto-start the memory daemon (only `ntm monitor`
// and `ntm memory serve` run the supervisor), so the old advice sentence must
// not exist anywhere in code or docs.
func TestNoFalseSpawnAutoStartAdvice(t *testing.T) {
	t.Parallel()
	root := repoRootForGrep(t)
	falseAdvice := regexp.MustCompile(`(?i)(spawn'? to auto-?start|auto-?starts? the memory daemon)`)

	// Every root that carries user-facing prose or help text. docs/planning
	// is a proposals archive, excluded from docs gates by design.
	roots := []string{"README.md", "AGENTS.md", "CLAUDE.md", "CHANGELOG.md", "docs", "internal", "cmd"}

	var offenders []string
	for _, r := range roots {
		start := filepath.Join(root, r)
		if _, err := os.Stat(start); err != nil {
			continue
		}
		err := filepath.WalkDir(start, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				slashed := filepath.ToSlash(path)
				if strings.HasPrefix(name, ".") ||
					name == "node_modules" ||
					slashed == filepath.ToSlash(filepath.Join(root, "docs", "planning")) ||
					slashed == filepath.ToSlash(filepath.Join(root, "internal", "webui", "dist")) {
					return filepath.SkipDir
				}
				return nil
			}
			ext := filepath.Ext(name)
			if ext != ".go" && ext != ".md" {
				return nil
			}
			if strings.HasSuffix(name, "_test.go") {
				return nil // this file quotes the pattern
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if falseAdvice.Match(data) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", r, err)
		}
	}
	if len(offenders) > 0 {
		t.Errorf("false 'spawn auto-starts the memory daemon' advice found in: %v\n"+
			"`ntm spawn` does not start daemons; only `ntm monitor` and `ntm memory serve` run the supervisor", offenders)
	}
}

func repoRootForGrep(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test working directory")
		}
		dir = parent
	}
}

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func TestMemoryStatusAndRecoveryContextCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub cm uses POSIX sh")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"cm_context"}]}}`))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}

	projectDir := t.TempDir()
	pidDir := filepath.Join(projectDir, ".ntm", "pids")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatalf("mkdir pids: %v", err)
	}
	pidPayload, _ := json.Marshal(supervisor.PIDFileInfo{
		PID:     os.Getpid(),
		Port:    port,
		OwnerID: "test",
		Command: "cm serve",
	})
	if err := os.WriteFile(filepath.Join(pidDir, "cm-testsession.pid"), pidPayload, 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	binDir := t.TempDir()
	longSnippet := strings.Repeat("verify-recovery-context-", 12)
	cmScript := "#!/bin/sh\nset -eu\n" +
		"echo '{\"success\":true,\"data\":{\"relevantBullets\":[{\"id\":\"r-1\",\"content\":\"Always validate inputs\"}],\"antiPatterns\":[{\"id\":\"ap-1\",\"content\":\"Never ignore errors\"}],\"historySnippets\":[{\"source_path\":\"/s/1.jsonl\",\"agent\":\"claude\",\"title\":\"Auth session\",\"snippet\":\"" + longSnippet + "\"}]}}'\n"
	if err := os.WriteFile(filepath.Join(binDir, "cm"), []byte(cmScript), 0o755); err != nil {
		t.Fatalf("write stub cm: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(projectDir)

	statusOut, err := captureStdout(t, func() error {
		_, runErr := execCommand(t, "memory", "status", "--json")
		return runErr
	})
	if err != nil {
		t.Fatalf("memory status --json: %v", err)
	}
	var statusPayload struct {
		CLIAvailable  bool   `json:"cli_available"`
		DaemonRunning bool   `json:"daemon_running"`
		DaemonHealthy bool   `json:"daemon_healthy"`
		SessionID     string `json:"session_id"`
		Port          int    `json:"port"`
	}
	if err := json.Unmarshal([]byte(statusOut), &statusPayload); err != nil {
		t.Fatalf("unmarshal memory status: %v\nout=%s", err, statusOut)
	}
	if !statusPayload.CLIAvailable || !statusPayload.DaemonRunning || !statusPayload.DaemonHealthy || statusPayload.Port != port {
		t.Fatalf("unexpected memory status: %+v (want port=%d)", statusPayload, port)
	}

	recOut, err := execCommand(t, "memory", "context", "harden auth", "--recovery")
	if err != nil {
		t.Fatalf("memory context --recovery: %v", err)
	}
	if !strings.Contains(recOut, "## Procedural Memory (Key Rules)") ||
		!strings.Contains(recOut, "[r-1]") ||
		!strings.Contains(recOut, "[ap-1]") ||
		!strings.Contains(recOut, "...") {
		t.Fatalf("unexpected recovery markdown output:\n%s", recOut)
	}
}
