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
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/supervisor"
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

// stubCMCLISource mirrors the cm 0.2.x contract: MCP JSON-RPC at the daemon
// root, no REST /health.
const stubCMCLISource = `package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("stubcm", flag.ExitOnError)
	port := fs.Int("port", 8200, "")
	fs.Parse(args)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    ` + "`json:\"id\"`" + `
			Method string ` + "`json:\"method\"`" + `
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize", "tools/list":
			json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": "2024-11-05"},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "Unsupported method"},
			})
		}
	})
	if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`

func buildStubCMBinary(t *testing.T, binDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub daemon behavioral test requires unix process semantics")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(stubCMCLISource), 0644); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module stubcm\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write stub go.mod: %v", err)
	}
	cmd := exec.Command(goTool, "build", "-o", filepath.Join(binDir, "cm"), ".")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stub cm: %v\n%s", err, out)
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
	memoryServePollInterval = 50 * time.Millisecond
	defer func() { memoryServePollInterval = origPoll }()

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
		case <-time.After(50 * time.Millisecond):
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
		time.Sleep(100 * time.Millisecond)
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

// TestNoFalseSpawnAutoStartAdvice is the grep-gate for the corrected docs/help
// text: `ntm spawn` does NOT auto-start the memory daemon (only `ntm monitor`
// and `ntm memory serve` run the supervisor), so the old advice sentence must
// not exist anywhere in code or docs.
func TestNoFalseSpawnAutoStartAdvice(t *testing.T) {
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
				if name == "node_modules" || filepath.ToSlash(path) == filepath.ToSlash(filepath.Join(root, "docs", "planning")) {
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
