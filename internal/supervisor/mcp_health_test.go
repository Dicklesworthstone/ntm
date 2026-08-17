package supervisor

// Tests for the MCP-aware cm health probe (bd-ws1-truth-safety-l5ddi.2).
//
// cm 0.2.x speaks MCP JSON-RPC at its root and has NO REST /health endpoint.
// The old REST probe (GET :8200/health) could never pass, so a supervised cm
// pinned in StateStarting forever. These tests prove:
//   - an MCP-speaking stub daemon transitions StateStarting -> StateRunning;
//   - a REST-only stub transitions to StateUnhealthy within the startup
//     health timeout — never StateStarting-forever;
//   - a JSON-RPC error object inside an HTTP 200 body (the 200-with-error
//     trap) is UNHEALTHY;
//   - a missing daemon binary fails fast and loud at Start().

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCheckHealthMCP(t *testing.T) {
	s := &Supervisor{}

	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    bool
	}{
		{
			name: "mcp initialize round-trip is healthy",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "method", http.StatusMethodNotAllowed)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"stub-cm"}}}`)
			},
			want: true,
		},
		{
			name: "REST-only 200 body is unhealthy",
			handler: func(w http.ResponseWriter, r *http.Request) {
				// A REST health server answers 200 with a non-JSON-RPC body.
				fmt.Fprint(w, "OK")
			},
			want: false,
		},
		{
			name: "json-rpc error in 200 body is unhealthy (200-with-error trap)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Unsupported method"}}`)
			},
			want: false,
		},
		{
			name: "200 with unrelated JSON is unhealthy",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"status":"ok"}`)
			},
			want: false,
		},
		{
			name: "non-200 is unhealthy",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "not found", http.StatusNotFound)
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(tt.handler)
			defer ts.Close()
			if got := s.checkHealthMCP(ts.URL); got != tt.want {
				t.Errorf("checkHealthMCP() = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("connection refused is unhealthy", func(t *testing.T) {
		port, err := findAvailablePort()
		if err != nil {
			t.Fatalf("findAvailablePort: %v", err)
		}
		if s.checkHealthMCP(fmt.Sprintf("http://127.0.0.1:%d/", port)) {
			t.Error("checkHealthMCP() = true for a closed port")
		}
	})
}

// stubCMSource is a minimal cm daemon stand-in. In its default (mcp) mode it
// speaks MCP JSON-RPC at its root: initialize and tools/list return results,
// anything else returns a JSON-RPC error in an HTTP 200 body — exactly like
// cm 0.2.x. In "rest" mode (STUBCM_MODE=rest) it serves only plain-text REST
// responses, modeling a daemon with no MCP endpoint.
const stubCMSource = `package main

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

	mux := http.NewServeMux()
	if os.Getenv("STUBCM_MODE") == "rest" {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "OK")
		})
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     any    ` + "`json:\"id\"`" + `
				Method string ` + "`json:\"method\"`" + `
			}
			json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			switch req.Method {
			case "initialize":
				json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"result": map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]string{"name": "stub-cm"}},
				})
			case "tools/list":
				json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"result": map[string]any{"tools": []any{}},
				})
			default:
				json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]any{"code": -32601, "message": "Unsupported method"},
				})
			}
		})
	}
	if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`

// buildStubCM compiles the stub cm daemon into dir with the given basename
// and returns the binary path.
func buildStubCM(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub daemon behavioral test requires unix process semantics")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}

	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(src, []byte(stubCMSource), 0644); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module stubcm\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write stub go.mod: %v", err)
	}

	bin := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stub cm: %v\n%s", err, out)
	}
	return bin
}

// waitForDaemonState polls until the daemon reaches want (returning the final
// snapshot) or the deadline expires.
func waitForDaemonState(t *testing.T, s *Supervisor, name string, want DaemonState, deadline time.Duration) *ManagedDaemon {
	t.Helper()
	end := time.Now().Add(deadline)
	var last *ManagedDaemon
	for time.Now().Before(end) {
		if d, ok := s.GetDaemon(name); ok {
			last = d
			if d.State == want {
				return d
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last != nil {
		t.Fatalf("daemon %s never reached state %q within %v (last state %q, restarts %d)", name, want, deadline, last.State, last.Restarts)
	}
	t.Fatalf("daemon %s never tracked within %v", name, deadline)
	return nil
}

func TestSupervisedMCPDaemonTurnsHealthy(t *testing.T) {
	tmpDir := t.TempDir()
	stub := buildStubCM(t, t.TempDir(), "stubcm")

	s, err := New(Config{
		SessionID:      "mcp-healthy",
		ProjectDir:     tmpDir,
		HealthInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	port, err := findAvailablePort()
	if err != nil {
		t.Fatalf("findAvailablePort: %v", err)
	}
	spec := DaemonSpec{
		Name:        "cm",
		Command:     stub,
		Args:        []string{"serve"},
		HealthMCP:   true,
		PortFlag:    "--port",
		DefaultPort: port,
	}
	if err := s.Start(spec); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// The monitor sleeps 2s before its first probe; allow generous slack.
	waitForDaemonState(t, s, "cm", StateRunning, 10*time.Second)

	logData, err := os.ReadFile(s.logPath("cm"))
	if err != nil {
		t.Fatalf("read supervisor log: %v", err)
	}
	log := string(logData)
	if !strings.Contains(log, "launched daemon cm") {
		t.Errorf("log missing launch line:\n%s", log)
	}
	if !strings.Contains(log, "state starting -> running") {
		t.Errorf("log missing starting -> running transition:\n%s", log)
	}
}

func TestSupervisedRESTOnlyDaemonTurnsUnhealthyNotStartingForever(t *testing.T) {
	tmpDir := t.TempDir()
	stub := buildStubCM(t, t.TempDir(), "stubcm")

	const startupTimeout = 1500 * time.Millisecond
	s, err := New(Config{
		SessionID:            "mcp-rest-only",
		ProjectDir:           tmpDir,
		HealthInterval:       100 * time.Millisecond,
		StartupHealthTimeout: startupTimeout,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	port, err := findAvailablePort()
	if err != nil {
		t.Fatalf("findAvailablePort: %v", err)
	}
	spec := DaemonSpec{
		Name:        "cm",
		Command:     stub,
		Args:        []string{"serve"},
		Env:         []string{"STUBCM_MODE=rest"},
		HealthMCP:   true,
		PortFlag:    "--port",
		DefaultPort: port,
	}
	if err := s.Start(spec); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// A REST-only daemon must transition out of StateStarting within the
	// probe timeout (2s monitor warm-up + startup timeout + probe slack) —
	// StateStarting-forever is the audited bug.
	d := waitForDaemonState(t, s, "cm", StateUnhealthy, 10*time.Second)
	if d.State == StateRunning {
		t.Fatal("REST-only daemon must never be blessed as running")
	}

	logData, err := os.ReadFile(s.logPath("cm"))
	if err != nil {
		t.Fatalf("read supervisor log: %v", err)
	}
	log := string(logData)
	if !strings.Contains(log, "state starting -> unhealthy") {
		t.Errorf("log missing starting -> unhealthy transition:\n%s", log)
	}
	if strings.Contains(log, "-> running") {
		t.Errorf("REST-only daemon was wrongly marked running:\n%s", log)
	}
}

func TestStartMissingBinaryFailsFastAndLoud(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "missing-binary",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	start := time.Now()
	err = s.Start(DaemonSpec{
		Name:    "cm",
		Command: "definitely-not-a-real-cm-binary-l5ddi2",
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Start() with missing binary must fail")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Start() took %v; missing binary must fail fast", elapsed)
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-cm-binary-l5ddi2") {
		t.Errorf("error must name the missing binary; got: %v", err)
	}
	if !strings.Contains(err.Error(), "not found in PATH") {
		t.Errorf("error must say the binary is not in PATH; got: %v", err)
	}
	if _, exists := s.GetDaemon("cm"); exists {
		t.Error("no daemon should be tracked after a failed start")
	}
}

func TestDefaultSpecsCMUsesMCPProbe(t *testing.T) {
	for _, spec := range DefaultSpecs() {
		if spec.Name != "cm" {
			continue
		}
		if !spec.HealthMCP {
			t.Error("cm spec must use the MCP health probe (HealthMCP)")
		}
		if spec.HealthURL != "" {
			t.Errorf("cm spec must not carry a REST HealthURL (cm has no REST /health); got %q", spec.HealthURL)
		}
		return
	}
	t.Fatal("DefaultSpecs() missing 'cm' daemon")
}
