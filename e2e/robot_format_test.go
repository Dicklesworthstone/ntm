//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// robot_format_test.go validates --robot-format selection for JSON/TOON/auto.
//
// Bead: bd-1a6c4 - Task: E2E robot-format selection (json/toon/auto)
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

type robotBoundaryProcessResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

type robotProcessFixture struct {
	ntmPath    string
	tmuxPath   string
	session    string
	root       string
	projectDir string
	configDir  string
	dataDir    string
	env        []string
	paneID     string
}

func runBuiltRobotProcess(t *testing.T, ntmPath, dir string, env []string, args ...string) robotBoundaryProcessResult {
	t.Helper()
	return runBuiltRobotProcessWithin(t, 30*time.Second, ntmPath, dir, env, args...)
}

func runBuiltRobotProcessWithin(t *testing.T, timeout time.Duration, ntmPath, dir string, env []string, args ...string) robotBoundaryProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ntmPath, args...)
	group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
	if groupErr != nil {
		t.Fatalf("create owned process group for ntm %q: %v", args, groupErr)
	}
	cmd.Cancel = func() error {
		return group.Signal(os.Kill)
	}
	// A restarted pane may leave a descendant holding inherited output
	// descriptors after CommandContext kills ntm. Bound the post-cancel pipe
	// drain so a failed process assertion cannot hang the entire E2E package.
	cmd.WaitDelay = 2 * time.Second
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = append([]string(nil), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	commandErr := cmd.Run()
	if closeErr := group.Close(); closeErr != nil {
		t.Fatalf("close owned process group for ntm %q: %v", args, closeErr)
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("ntm %q timed out after %v; stdout=%s stderr=%s", args, timeout, stdout.Bytes(), stderr.Bytes())
	}

	exitCode := 0
	if commandErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(commandErr, &exitErr) {
			t.Fatalf("run ntm %q: %v", args, commandErr)
		}
		exitCode = exitErr.ExitCode()
	}
	t.Logf("[E2E-ROBOT-PROCESS] exit=%d args=%q stdout=%s stderr=%s", exitCode, args, truncateString(stdout.String(), 500), truncateString(stderr.String(), 500))
	return robotBoundaryProcessResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
}

func mergeRobotProcessEnv(base []string, overrides map[string]string) []string {
	result := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func decodeSingleRobotJSON(t *testing.T, payload []byte, destination any) {
	t.Helper()
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		t.Fatalf("expected exactly one valid JSON document, got %q", payload)
	}
	if err := json.Unmarshal(trimmed, destination); err != nil {
		t.Fatalf("decode robot JSON: %v\npayload=%s", err, payload)
	}
}

func newRobotProcessFixture(t *testing.T, scenario string) *robotProcessFixture {
	t.Helper()
	testutil.RequireTmuxThrottled(t)

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}

	root := t.TempDir()
	fixture := &robotProcessFixture{
		ntmPath:    ntmPath,
		tmuxPath:   tmuxPath,
		session:    fmt.Sprintf("ntm-e2e-%s-%d-%d", scenario, os.Getpid(), time.Now().UnixNano()),
		root:       root,
		projectDir: filepath.Join(root, "project"),
		configDir:  filepath.Join(root, "config"),
		dataDir:    filepath.Join(root, "data"),
	}
	homeDir := filepath.Join(root, "home")
	tmuxDir := testutil.ShortTmuxTempDir(t)
	for _, dir := range []string{fixture.projectDir, fixture.configDir, fixture.dataDir, homeDir, tmuxDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create process fixture directory %s: %v", dir, err)
		}
	}
	fixture.env = atomicAssignmentIsolatedEnv(map[string]string{
		"HOME":                homeDir,
		"XDG_CONFIG_HOME":     fixture.configDir,
		"XDG_DATA_HOME":       fixture.dataDir,
		"TMUX_TMPDIR":         tmuxDir,
		"NO_COLOR":            "1",
		"TERM":                "xterm-256color",
		"NTM_OUTPUT_FORMAT":   "",
		"NTM_ROBOT_FORMAT":    "",
		"TOON_DEFAULT_FORMAT": "",
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, fixture.tmuxPath, "kill-server")
		cmd.Env = append([]string(nil), fixture.env...)
		output, err := cmd.CombinedOutput()
		if ctx.Err() == context.DeadlineExceeded {
			t.Errorf("robot-format private tmux cleanup timed out after %s: output=%s", defaultTmuxSetupTimeout, output)
			return
		}
		if err != nil && !isBenignTmuxCleanupError(output) {
			t.Errorf("robot-format private tmux cleanup failed: %v output=%s", err, output)
		}
	})

	tmuxConfig := filepath.Join(root, "tmux.conf")
	config := strings.Join([]string{
		"set -g base-index 0",
		"setw -g pane-base-index 0",
		"set -g status off",
		"setw -g allow-rename off",
		"setw -g automatic-rename off",
		"",
	}, "\n")
	if err := os.WriteFile(tmuxConfig, []byte(config), 0o600); err != nil {
		t.Fatalf("write tmux config: %v", err)
	}
	fixture.paneID = strings.TrimSpace(fixture.mustTMUXOutput(t,
		"-f", tmuxConfig,
		"new-session", "-d", "-s", fixture.session,
		"-x", "160", "-y", "48", "-c", fixture.projectDir,
		"-P", "-F", "#{pane_id}",
		"/bin/bash --noprofile --norc -i",
	))
	if fixture.paneID == "" {
		t.Fatal("private tmux server returned an empty pane ID")
	}
	fixture.mustTMUXOutput(t, "select-pane", "-t", fixture.paneID, "-T", fixture.session+"__cod_1")
	return fixture
}

func (f *robotProcessFixture) mustTMUXOutput(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.tmuxPath, args...)
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("tmux %q timed out", args)
	}
	if err != nil {
		t.Fatalf("tmux %q: %v output=%s", args, err, output)
	}
	return string(output)
}

func (f *robotProcessFixture) waitForFileContents(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), want) {
			return
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", path, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in %s", want, path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// runRobotFormatCmd executes an ntm robot command with a specific format.
func runRobotFormatCmd(t *testing.T, suite *TestSuite, format string, cmd string, args ...string) []byte {
	t.Helper()

	fullArgs := append([]string{fmt.Sprintf("--robot-format=%s", format)}, args...)
	command := exec.Command("ntm", fullArgs...)
	output, err := command.CombinedOutput()

	suite.Logger().Log("[E2E-ROBOT-FORMAT] format=%s cmd=%s bytes=%d", format, cmd, len(output))

	if err != nil {
		suite.Logger().Log("[E2E-ROBOT-FORMAT] error cmd=%s err=%v output=%s", cmd, err, string(output))
		t.Fatalf("[E2E-ROBOT-FORMAT] Command failed: %v", err)
	}

	return output
}

func parseJSONOrFail(t *testing.T, output []byte) map[string]interface{} {
	t.Helper()
	var parsed map[string]interface{}
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("[E2E-ROBOT-FORMAT] JSON parse failed: %v output=%s", err, string(output))
	}
	return parsed
}

func TestE2E_RobotFormatSelection(t *testing.T) {
	CommonE2EPrerequisites(t)
	if !supportsRobotFormat(t) {
		t.Skip("ntm --robot-format not supported by current binary")
	}

	suite := NewTestSuite(t, "robot_format")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-ROBOT-FORMAT] Setup failed: %v", err)
	}

	session := suite.Session()

	t.Run("status_json", func(t *testing.T) {
		output := runRobotFormatCmd(t, suite, "json", "robot-status", "--robot-status")
		parsed := parseJSONOrFail(t, output)

		if _, ok := parsed["sessions"]; !ok {
			t.Fatalf("[E2E-ROBOT-FORMAT] status JSON missing sessions: %v", parsed)
		}
	})

	t.Run("status_toon", func(t *testing.T) {
		output := runRobotFormatCmd(t, suite, "toon", "robot-status", "--robot-status")

		trimmed := strings.TrimSpace(string(output))
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			if json.Valid([]byte(trimmed)) {
				t.Fatalf("[E2E-ROBOT-FORMAT] status TOON should not be valid JSON, got: %s", string(output))
			}
		}
		if !strings.Contains(string(output), "sessions") {
			t.Fatalf("[E2E-ROBOT-FORMAT] status TOON missing sessions field: %s", string(output))
		}
	})

	t.Run("status_auto_defaults_json", func(t *testing.T) {
		output := runRobotFormatCmd(t, suite, "auto", "robot-status", "--robot-status")
		parseJSONOrFail(t, output)
		suite.Logger().Log("[E2E-ROBOT-FORMAT] fallback=%t reason=%s", true, "auto defaults to JSON")
	})

	t.Run("assign_json", func(t *testing.T) {
		output := runRobotFormatCmd(t, suite, "json", "robot-assign", fmt.Sprintf("--robot-assign=%s", session))
		parsed := parseJSONOrFail(t, output)

		if parsed["session"] != session {
			t.Fatalf("[E2E-ROBOT-FORMAT] assign JSON session mismatch: got=%v want=%s", parsed["session"], session)
		}
		if _, ok := parsed["strategy"]; !ok {
			t.Fatalf("[E2E-ROBOT-FORMAT] assign JSON missing strategy: %v", parsed)
		}
	})

	t.Run("assign_toon", func(t *testing.T) {
		output := runRobotFormatCmd(t, suite, "toon", "robot-assign", fmt.Sprintf("--robot-assign=%s", session))
		if !strings.Contains(string(output), "session:") {
			t.Fatalf("[E2E-ROBOT-FORMAT] assign TOON missing session: %s", string(output))
		}
		if !strings.Contains(string(output), "strategy:") {
			t.Fatalf("[E2E-ROBOT-FORMAT] assign TOON missing strategy: %s", string(output))
		}
	})

	t.Run("assign_auto_defaults_json", func(t *testing.T) {
		output := runRobotFormatCmd(t, suite, "auto", "robot-assign", fmt.Sprintf("--robot-assign=%s", session))
		parseJSONOrFail(t, output)
	})
}

func TestE2ERobotCapabilitiesBuiltBinaryBudgets(t *testing.T) {
	CommonE2EPrerequisites(t)
	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}

	type capabilityCommand struct {
		Name     string `json:"name"`
		Flag     string `json:"flag"`
		Category string `json:"category"`
	}
	type capabilityEnvelope struct {
		Success      bool                `json:"success"`
		OutputFormat string              `json:"output_format"`
		Commands     []capabilityCommand `json:"commands"`
		Categories   []string            `json:"categories"`
		Filter       *struct {
			Command  string `json:"command,omitempty"`
			Category string `json:"category,omitempty"`
			Query    string `json:"query,omitempty"`
			Compact  bool   `json:"compact,omitempty"`
		} `json:"filter,omitempty"`
	}
	run := func(t *testing.T, maxBytes int, args ...string) capabilityEnvelope {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, ntmPath, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("ntm %q: %v stdout=%s stderr=%s", args, err, stdout.Bytes(), stderr.Bytes())
		}
		if ctx.Err() != nil {
			t.Fatalf("ntm %q timed out: %v", args, ctx.Err())
		}
		if stderr.Len() != 0 {
			t.Fatalf("ntm %q wrote stderr: %s", args, stderr.Bytes())
		}
		payload := bytes.TrimSpace(stdout.Bytes())
		if len(payload) == 0 || len(payload) >= maxBytes {
			t.Fatalf("ntm %q payload size=%d, require 0 < size < %d", args, len(payload), maxBytes)
		}
		if !json.Valid(payload) {
			t.Fatalf("ntm %q did not emit one valid JSON document: %s", args, payload)
		}
		var envelope capabilityEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("decode ntm %q capabilities: %v", args, err)
		}
		if !envelope.Success || envelope.Commands == nil {
			t.Fatalf("ntm %q capabilities envelope = %+v", args, envelope)
		}
		t.Logf("[E2E-CAPABILITIES] args=%q bytes=%d commands=%d", args, len(payload), len(envelope.Commands))
		return envelope
	}
	assertInvalidFilter := func(t *testing.T, args ...string) {
		t.Helper()
		process := runBuiltRobotProcess(t, ntmPath, "", nil, args...)
		if process.exitCode != 1 {
			t.Fatalf("ntm %q exit=%d, want 1; stdout=%s stderr=%s", args, process.exitCode, process.stdout, process.stderr)
		}
		if len(process.stderr) != 0 {
			t.Fatalf("ntm %q wrote stderr: %s", args, process.stderr)
		}
		var envelope struct {
			Success      *bool  `json:"success"`
			Timestamp    string `json:"timestamp"`
			OutputFormat string `json:"output_format"`
			Error        string `json:"error"`
			ErrorCode    string `json:"error_code"`
			Hint         string `json:"hint"`
		}
		decodeSingleRobotJSON(t, process.stdout, &envelope)
		if envelope.Success == nil || *envelope.Success || envelope.ErrorCode != "INVALID_FLAG" ||
			envelope.Error == "" || envelope.Hint == "" || envelope.OutputFormat != "json" {
			t.Fatalf("ntm %q invalid capability filter envelope = %+v", args, envelope)
		}
		if _, err := time.Parse(time.RFC3339, envelope.Timestamp); err != nil {
			t.Fatalf("ntm %q timestamp %q is not RFC3339: %v", args, envelope.Timestamp, err)
		}
	}

	full := run(t, 400_000, "--robot-capabilities", "--robot-format=json")
	compact := run(t, 50_000, "--robot-capabilities", "--capability-compact")
	exact := run(t, 4_000, "--robot-capabilities", "--capability-command=send", "--capability-compact")
	search := run(t, 50_000, "--robot-capabilities", "--capability-search=interrupt", "--capability-compact")
	category := run(t, 50_000, "--robot-capabilities", "--capability-category=control", "--capability-compact")

	if len(full.Commands) == 0 || len(compact.Commands) != len(full.Commands) {
		t.Fatalf("capability command counts full=%d compact=%d", len(full.Commands), len(compact.Commands))
	}
	if len(exact.Commands) != 1 || exact.Commands[0].Name != "send" || exact.Commands[0].Flag != "--robot-send" ||
		exact.Filter == nil || exact.Filter.Command != "send" || !exact.Filter.Compact {
		t.Fatalf("exact compact capability projection = %+v", exact)
	}
	if compact.OutputFormat != "json" || exact.OutputFormat != "json" {
		t.Fatalf("auto compact capability formats compact=%q exact=%q, want json", compact.OutputFormat, exact.OutputFormat)
	}
	interruptFound := false
	for _, command := range search.Commands {
		if command.Name == "interrupt" && command.Flag == "--robot-interrupt" && command.Category == "control" {
			interruptFound = true
			break
		}
	}
	if search.Filter == nil || search.Filter.Query != "interrupt" || !search.Filter.Compact ||
		len(search.Commands) == 0 || len(search.Commands) >= len(compact.Commands) || !interruptFound {
		t.Fatalf("compact capability search projection = %+v", search)
	}
	if category.Filter == nil || category.Filter.Category != "control" || !category.Filter.Compact ||
		len(category.Commands) == 0 || len(category.Commands) >= len(compact.Commands) ||
		len(category.Categories) != 1 || category.Categories[0] != "control" {
		t.Fatalf("compact capability category projection = %+v", category)
	}
	for _, command := range category.Commands {
		if command.Category != "control" {
			t.Fatalf("compact control projection included command %+v", command)
		}
	}
	assertInvalidFilter(t, "--robot-capabilities", "--capability-command=not-a-command", "--capability-compact")
	assertInvalidFilter(t, "--robot-capabilities", "--capability-category=not-a-category", "--capability-compact")
}

func validateAgentMailHealthCheckRequest(r *http.Request) error {
	var request agentmail.JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return fmt.Errorf("decode JSON-RPC request: %w", err)
	}
	if request.JSONRPC != "2.0" || request.Method != "tools/call" {
		return fmt.Errorf("unexpected JSON-RPC health request: version=%q method=%q", request.JSONRPC, request.Method)
	}
	params, ok := request.Params.(map[string]any)
	if !ok || params["name"] != "health_check" {
		return fmt.Errorf("unexpected Agent Mail tool params: %v", request.Params)
	}
	return nil
}

func TestE2EMailInboxPermanentUnauthorizedFailsFast(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "mail-inbox-unauthorized")

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateAgentMailHealthCheckRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"unauthorized"}`))
	}))
	t.Cleanup(server.Close)

	env := mergeRobotProcessEnv(fixture.env, map[string]string{
		"AGENT_MAIL_URL":   server.URL + "/mcp/",
		"AGENT_MAIL_TOKEN": "invalid-e2e-token",
	})
	const deadline = 3 * time.Second
	started := time.Now()
	process := runBuiltRobotProcessWithin(t, deadline, fixture.ntmPath, fixture.projectDir, env,
		"mail", "inbox",
	)
	elapsed := time.Since(started)

	if process.exitCode == 0 {
		t.Fatalf("mail inbox exited zero for permanent HTTP 401: stdout=%s stderr=%s", process.stdout, process.stderr)
	}
	if elapsed >= deadline {
		t.Fatalf("mail inbox permanent HTTP 401 took %s, require less than %s", elapsed, deadline)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("mail inbox permanent HTTP 401 made %d requests, want exactly 1", got)
	}
	output := strings.ToLower(string(process.stdout) + "\n" + string(process.stderr))
	if !strings.Contains(output, "http 401") || !strings.Contains(output, "unauthorized") || !strings.Contains(output, "agent mail server not available") {
		t.Fatalf("mail inbox output lost permanent failure diagnostic: stdout=%s stderr=%s", process.stdout, process.stderr)
	}
}

func TestE2EMailReadPermanentUnauthorizedSurfacesDiagnostic(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "mail-read-unauthorized")

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateAgentMailHealthCheckRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requestCount.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	env := mergeRobotProcessEnv(fixture.env, map[string]string{
		"AGENT_MAIL_URL":   server.URL + "/mcp/",
		"AGENT_MAIL_TOKEN": "invalid-e2e-token",
	})
	process := runBuiltRobotProcessWithin(t, 3*time.Second, fixture.ntmPath, fixture.projectDir, env,
		"mail", "read", fixture.session, "1", "--agent", "BlueLake",
	)

	if process.exitCode == 0 {
		t.Fatalf("mail read exited zero for permanent HTTP 401: stdout=%s stderr=%s", process.stdout, process.stderr)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("mail read permanent HTTP 401 made %d requests, want exactly 1", got)
	}
	output := strings.ToLower(string(process.stdout) + "\n" + string(process.stderr))
	if !strings.Contains(output, "http 401") || !strings.Contains(output, "unauthorized") || !strings.Contains(output, "agent mail server not available") {
		t.Fatalf("mail read output lost permanent failure diagnostic: stdout=%s stderr=%s", process.stdout, process.stderr)
	}
}

func TestE2EMailSendPermanentUnauthorizedSurfacesDiagnostic(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "mail-send-unauthorized")

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateAgentMailHealthCheckRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requestCount.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	env := mergeRobotProcessEnv(fixture.env, map[string]string{
		"AGENT_MAIL_URL":   server.URL + "/mcp/",
		"AGENT_MAIL_TOKEN": "invalid-e2e-token",
	})
	process := runBuiltRobotProcessWithin(t, 3*time.Second, fixture.ntmPath, fixture.projectDir, env,
		"mail", "send", fixture.session, "release diagnostic", "--to", "BlueLake",
	)

	if process.exitCode == 0 {
		t.Fatalf("mail send exited zero for permanent HTTP 401: stdout=%s stderr=%s", process.stdout, process.stderr)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("mail send permanent HTTP 401 made %d requests, want exactly 1", got)
	}
	output := strings.ToLower(string(process.stdout) + "\n" + string(process.stderr))
	if !strings.Contains(output, "http 401") || !strings.Contains(output, "unauthorized") || !strings.Contains(output, "agent mail server not available") {
		t.Fatalf("mail send output lost permanent failure diagnostic: stdout=%s stderr=%s", process.stdout, process.stderr)
	}
}

func TestE2EMailInboxTransientExhaustionIsBounded(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "mail-inbox-transient-exhaustion")

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	env := mergeRobotProcessEnv(fixture.env, map[string]string{
		"AGENT_MAIL_URL": server.URL + "/mcp/",
	})
	const deadline = 3 * time.Second
	started := time.Now()
	process := runBuiltRobotProcessWithin(t, deadline, fixture.ntmPath, fixture.projectDir, env,
		"mail", "inbox",
	)
	elapsed := time.Since(started)

	if process.exitCode == 0 {
		t.Fatalf("mail inbox exited zero after transient retry exhaustion: stdout=%s stderr=%s", process.stdout, process.stderr)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("mail inbox transient retry exhaustion took %s, require less than 2s", elapsed)
	}
	if got := requestCount.Load(); got != int32(3) {
		t.Fatalf("mail inbox transient retry exhaustion made %d requests, want exactly 3", got)
	}
	output := strings.ToLower(string(process.stdout) + "\n" + string(process.stderr))
	if !strings.Contains(output, "http 503") || !strings.Contains(output, "agent mail server not available") {
		t.Fatalf("mail inbox output lost terminal transient diagnostic: stdout=%s stderr=%s", process.stdout, process.stderr)
	}
}

func TestE2EMailInboxRecoversFromTransientFailure(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "mail-inbox-transient-recovery")

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := requestCount.Add(1)
		if call == 1 {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}

		var request agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var result json.RawMessage
		switch request.Method {
		case "tools/call":
			params, ok := request.Params.(map[string]any)
			if !ok || params["name"] != "health_check" {
				http.Error(w, "unexpected tool call", http.StatusBadRequest)
				return
			}
			result = json.RawMessage(`{"status":"ok"}`)
		case "resources/read":
			params, ok := request.Params.(map[string]any)
			expectedURI := "resource://agents/" + url.PathEscape(fixture.projectDir)
			if !ok || params["uri"] != expectedURI {
				http.Error(w, "unexpected resource URI", http.StatusBadRequest)
				return
			}
			result = json.RawMessage(`{"contents":[]}`)
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result:  result,
		})
	}))
	t.Cleanup(server.Close)

	env := mergeRobotProcessEnv(fixture.env, map[string]string{
		"AGENT_MAIL_URL": server.URL + "/mcp/",
	})
	process := runBuiltRobotProcessWithin(t, 3*time.Second, fixture.ntmPath, fixture.projectDir, env,
		"mail", "inbox",
	)

	if process.exitCode != 0 {
		t.Fatalf("mail inbox did not recover from a transient health failure: stdout=%s stderr=%s", process.stdout, process.stderr)
	}
	if got := requestCount.Load(); got != 3 {
		t.Fatalf("mail inbox transient recovery made %d requests, want two health probes and one inbox resource request", got)
	}
	if !strings.Contains(string(process.stdout), "Inbox empty") {
		t.Fatalf("mail inbox recovery output = %q, want empty-inbox success", process.stdout)
	}
}

func TestE2ERobotTerminalErrorContractsBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "terminal-errors")

	type terminalEnvelope struct {
		Success      bool   `json:"success"`
		Timestamp    string `json:"timestamp"`
		OutputFormat string `json:"output_format"`
		ErrorCode    string `json:"error_code"`
	}
	assertFailure := func(t *testing.T, process robotBoundaryProcessResult, wantExit int, wantCode string) terminalEnvelope {
		t.Helper()
		if process.exitCode != wantExit {
			t.Fatalf("exit=%d, want %d; stdout=%s stderr=%s", process.exitCode, wantExit, process.stdout, process.stderr)
		}
		if len(process.stderr) != 0 {
			t.Fatalf("stderr=%q, want empty canonical robot boundary", process.stderr)
		}
		var envelope terminalEnvelope
		decodeSingleRobotJSON(t, process.stdout, &envelope)
		if envelope.Success || envelope.ErrorCode != wantCode || envelope.OutputFormat != "json" {
			t.Fatalf("terminal envelope = %+v, want %s canonical JSON failure", envelope, wantCode)
		}
		if _, err := time.Parse(time.RFC3339, envelope.Timestamp); err != nil {
			t.Fatalf("terminal timestamp %q is not RFC3339: %v", envelope.Timestamp, err)
		}
		return envelope
	}
	assertSuccess := func(t *testing.T, process robotBoundaryProcessResult) terminalEnvelope {
		t.Helper()
		if process.exitCode != 0 {
			t.Fatalf("exit=%d, want 0; stdout=%s stderr=%s", process.exitCode, process.stdout, process.stderr)
		}
		if len(process.stderr) != 0 {
			t.Fatalf("stderr=%q, want empty canonical robot boundary", process.stderr)
		}
		var envelope terminalEnvelope
		decodeSingleRobotJSON(t, process.stdout, &envelope)
		if !envelope.Success || envelope.ErrorCode != "" || envelope.OutputFormat != "json" {
			t.Fatalf("terminal envelope = %+v, want canonical JSON success", envelope)
		}
		if _, err := time.Parse(time.RFC3339, envelope.Timestamp); err != nil {
			t.Fatalf("terminal timestamp %q is not RFC3339: %v", envelope.Timestamp, err)
		}
		return envelope
	}
	run := func(t *testing.T, env []string, args ...string) robotBoundaryProcessResult {
		t.Helper()
		return runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, env, args...)
	}

	t.Run("markdown_validation_under_toon", func(t *testing.T) {
		process := run(t, fixture.env,
			"--robot-markdown",
			"--sections=not-a-section",
			"--robot-format=toon",
		)
		assertFailure(t, process, 1, "INTERNAL_ERROR")
	})

	t.Run("terse_pre_dispatch_format_failure", func(t *testing.T) {
		process := run(t, fixture.env, "--robot-terse", "--robot-format=invalid")
		assertFailure(t, process, 1, "INVALID_FLAG")
	})

	t.Run("terse_runtime_dependency_failure_under_toon", func(t *testing.T) {
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY": filepath.Join(fixture.root, "missing-bin", "tmux"),
		})
		process := run(t, env, "--robot-terse", "--robot-format=toon")
		assertFailure(t, process, 1, "DEPENDENCY_MISSING")
	})

	t.Run("save_write_failure_under_toon", func(t *testing.T) {
		process := run(t, fixture.env,
			"--robot-save="+fixture.session,
			"--output="+fixture.root,
			"--robot-format=toon",
		)
		assertFailure(t, process, 1, "INTERNAL_ERROR")
	})

	t.Run("deprecated_save_output_does_not_pollute_stderr", func(t *testing.T) {
		process := run(t, fixture.env,
			"--robot-save="+fixture.session,
			"--save-output="+fixture.root,
			"--robot-format=json",
		)
		assertFailure(t, process, 1, "INTERNAL_ERROR")
	})

	t.Run("restore_missing_state_under_toon", func(t *testing.T) {
		process := run(t, fixture.env,
			"--robot-restore="+filepath.Join(fixture.root, "missing-state.json"),
			"--robot-format=toon",
		)
		assertFailure(t, process, 1, "INTERNAL_ERROR")
	})

	t.Run("dcg_dependency_is_error_not_unavailable", func(t *testing.T) {
		env := mergeRobotProcessEnv(fixture.env, map[string]string{"PATH": "/usr/bin:/bin"})
		process := run(t, env,
			"--robot-dcg-check",
			"--command=echo safe",
			"--robot-format=toon",
		)
		assertFailure(t, process, 1, "DEPENDENCY_MISSING")
	})

	t.Run("bv_runtime_failures_are_one_typed_document", func(t *testing.T) {
		for _, test := range []struct {
			name string
			body string
		}{
			{name: "command failure", body: "#!/bin/sh\nprintf 'bv exploded' >&2\nexit 7\n"},
			{name: "malformed JSON", body: "#!/bin/sh\nprintf '{malformed'\n"},
		} {
			t.Run(test.name, func(t *testing.T) {
				fakeDir := t.TempDir()
				fakeBV := filepath.Join(fakeDir, "bv")
				if err := os.WriteFile(fakeBV, []byte(test.body), 0o700); err != nil {
					t.Fatalf("write fake bv: %v", err)
				}
				env := mergeRobotProcessEnv(fixture.env, map[string]string{
					"PATH": fakeDir + string(os.PathListSeparator) + os.Getenv("PATH"),
				})
				process := run(t, env, "--robot-forecast=all", "--robot-format=json")
				assertFailure(t, process, 1, "INTERNAL_ERROR")
				var detailed struct {
					Error string `json:"error"`
					Hint  string `json:"hint"`
				}
				if err := json.Unmarshal(process.stdout, &detailed); err != nil {
					t.Fatalf("decode detailed bv failure: %v", err)
				}
				if strings.TrimSpace(detailed.Error) == "" || strings.TrimSpace(detailed.Hint) == "" {
					t.Fatalf("bv failure missing remediation detail: %+v", detailed)
				}
			})
		}
	})

	t.Run("attention_timeout_under_toon", func(t *testing.T) {
		process := run(t, fixture.env,
			"--robot-attention",
			"--attention-condition=mail_pending",
			"--attention-timeout=20ms",
			"--attention-poll=5ms",
			"--robot-format=toon",
		)
		assertFailure(t, process, 1, "TIMEOUT")
	})

	t.Run("not_implemented_is_the_only_unavailable_exit", func(t *testing.T) {
		process := run(t, fixture.env,
			"--robot-ensemble-spawn=terminal-contract",
			"--robot-format=toon",
		)
		assertFailure(t, process, 2, "NOT_IMPLEMENTED")
	})

	t.Run("unresponsive_probe_under_toon", func(t *testing.T) {
		fixture.mustTMUXOutput(t, "send-keys", "-t", fixture.paneID, "-l", "stty -echo; sleep 30")
		fixture.mustTMUXOutput(t, "send-keys", "-t", fixture.paneID, "Enter")
		deadline := time.Now().Add(5 * time.Second)
		for {
			command := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", fixture.paneID, "#{pane_current_command}"))
			if command == "sleep" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("pane command=%q, want sleep", command)
			}
			time.Sleep(25 * time.Millisecond)
		}
		process := run(t, fixture.env,
			"--robot-probe="+fixture.session,
			"--panes=0",
			"--probe-timeout=100",
			"--robot-format=toon",
		)
		assertFailure(t, process, 1, "TIMEOUT")
	})

	t.Run("invalid_project_overlay_warning_is_suppressed", func(t *testing.T) {
		projectDir := filepath.Join(fixture.root, "invalid-project-overlay")
		configDir := filepath.Join(projectDir, ".ntm")
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatalf("create invalid project config directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[invalid\n"), 0o600); err != nil {
			t.Fatalf("write invalid project config: %v", err)
		}
		process := runBuiltRobotProcess(t, fixture.ntmPath, projectDir, fixture.env,
			"--robot-status",
			"--robot-format=json",
		)
		assertSuccess(t, process)
	})

	t.Run("verbose_startup_cleanup_warning_is_suppressed", func(t *testing.T) {
		configPath := filepath.Join(fixture.root, "verbose-cleanup.toml")
		configBody := "[cleanup]\nauto_clean_on_startup = true\nmax_age_hours = 0\nverbose = true\n"
		if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
			t.Fatalf("write verbose cleanup config: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"TMPDIR": filepath.Join(fixture.root, "missing-temp-root"),
		})
		process := run(t, env,
			"--config="+configPath,
			"--robot-status",
			"--robot-format=json",
		)
		assertSuccess(t, process)
	})

	t.Run("encryption_key_failure_is_one_error_envelope", func(t *testing.T) {
		configPath := filepath.Join(fixture.root, "missing-encryption-key.toml")
		configBody := "[encryption]\nenabled = true\nkey_source = \"env\"\nkey_env = \"NTM_E2E_MISSING_ENCRYPTION_KEY\"\nkey_format = \"hex\"\n"
		if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
			t.Fatalf("write encryption config: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"NTM_E2E_MISSING_ENCRYPTION_KEY": "",
		})
		process := run(t, env,
			"--config="+configPath,
			"--robot-status",
			"--robot-format=json",
		)
		assertFailure(t, process, 1, "INTERNAL_ERROR")
	})

	t.Run("encryption_keyring_failure_is_one_error_envelope", func(t *testing.T) {
		configPath := filepath.Join(fixture.root, "invalid-encryption-keyring.toml")
		configBody := "[encryption]\nenabled = true\nkey_source = \"env\"\nkey_env = \"NTM_E2E_ENCRYPTION_KEY\"\nkey_format = \"hex\"\nactive_key_id = \"current\"\n[encryption.keyring]\ncurrent = \"not-hex\"\n"
		if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
			t.Fatalf("write invalid encryption keyring config: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"NTM_E2E_ENCRYPTION_KEY": strings.Repeat("00", 32),
		})
		process := run(t, env,
			"--config="+configPath,
			"--robot-status",
			"--robot-format=json",
		)
		assertFailure(t, process, 1, "INTERNAL_ERROR")
	})

	t.Run("startup_profile_does_not_append_a_second_document", func(t *testing.T) {
		process := run(t, fixture.env,
			"--profile-startup",
			"--robot-status",
			"--robot-format=json",
		)
		assertSuccess(t, process)
	})
}

func TestE2EHealthAutoRestartUsesEffectiveAgentConfig(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "health-configured-restart")
	markerPath := filepath.Join(fixture.root, "configured-codex-launched")
	agentPath := filepath.Join(fixture.root, "configured-codex")
	agentScript := fmt.Sprintf("#!/bin/sh\nprintf 'Codex> \\n100%%%% context left\\n'\nprintf 'configured\\n' > %q\nwhile IFS= read -r line; do\n  printf 'received: %%s\\nCodex> \\n100%%%% context left\\n' \"$line\"\ndone\n", markerPath)
	if err := os.WriteFile(agentPath, []byte(agentScript), 0o700); err != nil {
		t.Fatalf("write configured Codex agent: %v", err)
	}
	configPath := filepath.Join(fixture.root, "configured-health.toml")
	configBody := fmt.Sprintf("[agents]\ncodex = %q\n\n[spawn_pacing]\nenabled = false\n", agentPath)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write configured health file: %v", err)
	}
	// The command intentionally enforces a 30-second minimum threshold. Keep
	// the pane genuinely quiet long enough to exercise production detection.
	time.Sleep(31 * time.Second)

	tmuxWrapper := filepath.Join(fixture.root, "tmux")
	tmuxWrapperScript := `#!/bin/sh
if [ "${1:-}" = "respawn-pane" ]; then
  printf 'injected respawn failure\n' >&2
  exit 72
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(tmuxWrapper, []byte(tmuxWrapperScript), 0o700); err != nil {
		t.Fatalf("write failing tmux wrapper: %v", err)
	}
	failureEnv := mergeRobotProcessEnv(fixture.env, map[string]string{
		"NTM_TMUX_BINARY":   tmuxWrapper,
		"NTM_E2E_REAL_TMUX": fixture.tmuxPath,
	})
	failedProcess := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, failureEnv,
		"--config="+configPath,
		"--robot-health-restart-stuck="+fixture.session,
		"--stuck-threshold=30s",
		"--robot-format=json",
	)
	if failedProcess.exitCode != 1 || len(bytes.TrimSpace(failedProcess.stderr)) != 0 {
		t.Fatalf("failed health restart exit=%d stdout=%s stderr=%s", failedProcess.exitCode, failedProcess.stdout, failedProcess.stderr)
	}
	var failedOutput struct {
		Success      bool   `json:"success"`
		OutputFormat string `json:"output_format"`
		Error        string `json:"error"`
		ErrorCode    string `json:"error_code"`
		StuckPanes   []int  `json:"stuck_panes"`
		Restarted    []int  `json:"restarted"`
		Failed       []int  `json:"failed"`
	}
	decodeSingleRobotJSON(t, failedProcess.stdout, &failedOutput)
	if failedOutput.Success || failedOutput.OutputFormat != "json" || failedOutput.ErrorCode != "INTERNAL_ERROR" || failedOutput.Error == "" ||
		!slices.Equal(failedOutput.StuckPanes, []int{0}) || len(failedOutput.Restarted) != 0 || !slices.Equal(failedOutput.Failed, []int{0}) {
		t.Fatalf("failed health restart output=%+v", failedOutput)
	}

	process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, fixture.env,
		"--config="+configPath,
		"--robot-health-restart-stuck="+fixture.session,
		"--stuck-threshold=30s",
		"--robot-format=json",
	)
	if process.exitCode != 0 || len(bytes.TrimSpace(process.stderr)) != 0 {
		t.Fatalf("configured health restart exit=%d stdout=%s stderr=%s", process.exitCode, process.stdout, process.stderr)
	}
	var output struct {
		Success    bool  `json:"success"`
		StuckPanes []int `json:"stuck_panes"`
		Restarted  []int `json:"restarted"`
		Failed     []int `json:"failed"`
	}
	decodeSingleRobotJSON(t, process.stdout, &output)
	if !output.Success || !slices.Equal(output.StuckPanes, []int{0}) || !slices.Equal(output.Restarted, []int{0}) || len(output.Failed) != 0 {
		t.Fatalf("configured health restart output=%+v", output)
	}
	fixture.waitForFileContents(t, markerPath, "configured")
	deadline := time.Now().Add(5 * time.Second)
	for {
		captured := fixture.mustTMUXOutput(t, "capture-pane", "-p", "-t", fixture.paneID, "-S", "-")
		if strings.Contains(captured, "Codex>") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("configured Codex agent did not reach ready state:\n%s", captured)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestE2EAtomicRestartBeadPolicyAndDurabilityBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "atomic-restart-bead")
	brPath, err := exec.LookPath("br")
	if err != nil {
		t.Skipf("br is required for restart-bead E2E: %v", err)
	}
	runBR := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, brPath, args...)
		cmd.Dir = fixture.projectDir
		cmd.Env = append([]string(nil), fixture.env...)
		output, commandErr := cmd.CombinedOutput()
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("br %q timed out", args)
		}
		if commandErr != nil {
			t.Fatalf("br %q: %v output=%s", args, commandErr, output)
		}
		return output
	}
	runBR("init", "--prefix=restart-e2e", "--json")
	createBead := func(title string) string {
		t.Helper()
		id := strings.TrimSpace(string(runBR("create", title, "--type=task", "--priority=1", "--silent")))
		if id == "" || strings.ContainsAny(id, " \t\r\n") {
			t.Fatalf("unexpected br create output %q", id)
		}
		return id
	}
	beadState := func(beadID string) (status, assignee string, raw []byte) {
		t.Helper()
		raw = runBR("show", beadID, "--json")
		var rows []atomicAssignmentBead
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatalf("decode br show %s: %v raw=%s", beadID, err, raw)
		}
		if len(rows) != 1 || rows[0].ID != beadID {
			t.Fatalf("br show %s rows=%+v", beadID, rows)
		}
		return rows[0].Status, rows[0].Assignee, raw
	}
	panePID := func(paneID string) string {
		t.Helper()
		return strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", paneID, "#{pane_pid}"))
	}

	fakeBin := filepath.Join(fixture.root, "restart-bead-bin")
	outsideDir := filepath.Join(fixture.root, "outside")
	for _, dir := range []string{fakeBin, outsideDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create restart-bead fixture directory %s: %v", dir, err)
		}
	}
	agentLog := filepath.Join(fixture.root, "restart-agent.log")
	agentPath := filepath.Join(fakeBin, "restart-codex")
	// The fake codex must be a non-shell foreground process: the respawned
	// pane's prompt dispatch crosses the PANE_AGENT_DEAD liveness gate
	// (ntm-0g0b), and a `#!/bin/sh` fake reports pane_current_command "bash"
	// on macOS. The wrapper execs the fakeshell fixture (bd-h4t0j), which
	// renders the codex composer glyph "›" for the bd-dp9oy visibility gate
	// and appends every submitted line to the agent log like the historical
	// script did.
	fakeshellBin, fakeshellErr := ensureFakeshellBin()
	if fakeshellErr != nil {
		t.Fatalf("build fakeshell fixture: %v", fakeshellErr)
	}
	agentScript := fmt.Sprintf(`#!/bin/sh
exec %s --mode=echo --tty-noecho --log=%s --prompt='Codex> › ' --banner='100%% context left'
`, tmux.ShellQuote(fakeshellBin), tmux.ShellQuote(agentLog))
	if err := os.WriteFile(agentPath, []byte(agentScript), 0o700); err != nil {
		t.Fatalf("write restart agent: %v", err)
	}
	configPath := filepath.Join(fixture.root, "restart-config.toml")
	configBody := fmt.Sprintf("[agents]\ncodex = %q\n\n[spawn_pacing]\nenabled = false\n\n[assign]\noperator_gated_labels = [\"release-approval\"]\n", agentPath)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write restart config: %v", err)
	}
	baseEnv := mergeRobotProcessEnv(fixture.env, map[string]string{
		"PATH": fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
	})
	runRestart := func(env []string, beadID, prompt string, panes ...string) robotBoundaryProcessResult {
		t.Helper()
		args := []string{
			"--config=" + configPath,
			"--robot-format=json",
			"--robot-restart-pane=" + fixture.session,
			"--restart-bead=" + beadID,
			"--restart-prompt=" + prompt,
		}
		if len(panes) > 0 {
			args = append(args, "--panes="+strings.Join(panes, ","))
		}
		return runBuiltRobotProcessWithin(t, 60*time.Second, fixture.ntmPath, outsideDir, env, args...)
	}
	type restartOutput struct {
		Success   bool     `json:"success"`
		Error     string   `json:"error"`
		ErrorCode string   `json:"error_code"`
		Restarted []string `json:"restarted"`
		Failed    []struct {
			Pane   string `json:"pane"`
			Reason string `json:"reason"`
		} `json:"failed"`
		DryRun              bool              `json:"dry_run"`
		WouldAffect         []string          `json:"would_affect"`
		BeadAssigned        string            `json:"bead_assigned"`
		PromptSent          bool              `json:"prompt_sent"`
		PromptError         string            `json:"prompt_error"`
		PromptDelivery      map[string]string `json:"prompt_delivery"`
		AgentRelaunched     map[string]bool   `json:"agent_relaunched"`
		AgentRelaunchStatus map[string]string `json:"agent_relaunch_status"`
		ProcessAlive        map[string]bool   `json:"process_alive"`
		ClaimActor          string            `json:"claim_actor"`
		IdempotencyKey      string            `json:"idempotency_key"`
		DispatchReceiptID   string            `json:"dispatch_receipt_id"`
		AssignmentReplayed  bool              `json:"assignment_replayed"`
		AssignmentRecovered bool              `json:"assignment_recovered"`
	}
	runCancelableRestartAtMarker := func(label, markerPath string, env []string, args ...string) (int, []byte, []byte) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, fixture.ntmPath, args...)
		group, err := testutil.NewProcessGroupForTest(ctx, cmd)
		if err != nil {
			t.Fatalf("create %s process group: %v", label, err)
		}
		defer func() { _ = group.Close() }()
		cmd.Cancel = func() error { return group.Signal(os.Kill) }
		cmd.WaitDelay = 2 * time.Second
		cmd.Dir = outsideDir
		cmd.Env = append([]string(nil), env...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start %s: %v", label, err)
		}
		waitCh := make(chan error, 1)
		go func() { waitCh <- cmd.Wait() }()
		markerDeadline := time.Now().Add(20 * time.Second)
		for {
			if _, err := os.Stat(markerPath); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				cancel()
				<-waitCh
				t.Fatalf("read %s marker: %v", label, err)
			}
			select {
			case earlyErr := <-waitCh:
				t.Fatalf("%s exited before cancellation point: %v stdout=%s stderr=%s", label, earlyErr, stdout.String(), stderr.String())
			default:
			}
			if time.Now().After(markerDeadline) {
				cancel()
				<-waitCh
				t.Fatalf("timed out waiting for %s marker: stdout=%s stderr=%s", label, stdout.String(), stderr.String())
			}
			time.Sleep(25 * time.Millisecond)
		}
		signalAt := time.Now()
		if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
			cancel()
			<-waitCh
			t.Fatalf("signal %s: %v", label, err)
		}
		var waitErr error
		select {
		case waitErr = <-waitCh:
		case <-ctx.Done():
			waitErr = <-waitCh
		}
		if ctx.Err() != nil {
			t.Fatalf("%s did not join after SIGINT: %v stdout=%s stderr=%s", label, ctx.Err(), stdout.String(), stderr.String())
		}
		if elapsed := time.Since(signalAt); elapsed > 5*time.Second {
			t.Fatalf("%s cancellation took %s", label, elapsed)
		}
		if err := group.Close(); err != nil {
			t.Fatalf("close %s process group: %v", label, err)
		}
		exitCode := 0
		if waitErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) {
				t.Fatalf("wait for %s: %v", label, waitErr)
			}
			exitCode = exitErr.ExitCode()
		}
		return exitCode, append([]byte(nil), stdout.Bytes()...), append([]byte(nil), stderr.Bytes()...)
	}

	beadID := createBead("Atomic restart assignment")
	marker := fmt.Sprintf("NTM_ATOMIC_RESTART_%d", time.Now().UnixNano())
	writeSpawnFakeBV(t, filepath.Join(fakeBin, "bv"), beadID, "Atomic restart assignment")
	beforePID := panePID(fixture.paneID)
	result := runRestart(baseEnv, beadID, marker, "0")
	if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("restart assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var output restartOutput
	decodeSingleRobotJSON(t, result.stdout, &output)
	if !output.Success || !output.PromptSent || output.BeadAssigned != beadID || output.ClaimActor == "" ||
		output.IdempotencyKey == "" || output.DispatchReceiptID == "" || output.AssignmentReplayed ||
		!slices.Equal(output.Restarted, []string{"0"}) || len(output.Failed) != 0 {
		t.Fatalf("restart assignment output=%+v", output)
	}
	if afterPID := panePID(fixture.paneID); afterPID == beforePID {
		t.Fatalf("restart assignment pane PID remained %s", beforePID)
	}
	fixture.waitForFileContents(t, agentLog, marker)
	logData, err := os.ReadFile(agentLog)
	if err != nil || strings.Count(string(logData), marker) != 1 {
		t.Fatalf("restart assignment log count=%d err=%v data=%s", strings.Count(string(logData), marker), err, logData)
	}
	status, assignee, _ := beadState(beadID)
	if status != "in_progress" || assignee != output.ClaimActor {
		t.Fatalf("restart bead status=%q assignee=%q, want in_progress %q", status, assignee, output.ClaimActor)
	}
	homeDir := atomicAssignmentEnvValue(fixture.env, "HOME")
	ledgerPath := filepath.Join(homeDir, ".ntm", "sessions", fixture.session, "assignments.json")
	ledger, _ := readAtomicAssignmentLedgerAt(t, ledgerPath)
	record := ledger.Assignments[beadID]
	if record == nil || record.ClaimActor != output.ClaimActor || record.IdempotencyKey != output.IdempotencyKey ||
		record.DispatchState != "sent" || record.DispatchTarget != fixture.paneID || record.OccupancyKey != fixture.paneID ||
		record.DispatchReceiptID != output.DispatchReceiptID || record.PromptSent != marker {
		t.Fatalf("durable restart assignment=%+v output=%+v", record, output)
	}

	replayPID := panePID(fixture.paneID)
	replay := runRestart(baseEnv, beadID, marker, "0")
	if replay.exitCode != 0 || len(bytes.TrimSpace(replay.stderr)) != 0 {
		t.Fatalf("restart replay exit=%d stdout=%s stderr=%s", replay.exitCode, replay.stdout, replay.stderr)
	}
	var replayOutput restartOutput
	decodeSingleRobotJSON(t, replay.stdout, &replayOutput)
	if !replayOutput.Success || !replayOutput.PromptSent || !replayOutput.AssignmentReplayed ||
		len(replayOutput.Restarted) != 0 || replayOutput.IdempotencyKey != output.IdempotencyKey ||
		replayOutput.DispatchReceiptID != output.DispatchReceiptID || panePID(fixture.paneID) != replayPID {
		t.Fatalf("restart replay output=%+v", replayOutput)
	}
	logData, err = os.ReadFile(agentLog)
	if err != nil || strings.Count(string(logData), marker) != 1 {
		t.Fatalf("restart replay duplicated marker: err=%v data=%s", err, logData)
	}

	actuationLog := filepath.Join(fixture.root, "restart-rejection-actuation.log")
	tmuxWrapper := filepath.Join(fakeBin, "tmux-guard")
	wrapper := `#!/bin/sh
case "${1:-}" in
  respawn-pane|send-keys|load-buffer|paste-buffer|set-buffer|delete-buffer)
    printf '%s\n' "$*" >> "$NTM_E2E_RESTART_ACTUATION_LOG"
    ;;
esac
if [ "${1:-}" = "capture-pane" ] &&
   [ -n "${NTM_E2E_RESTART_READINESS_CAPTURE:-}" ] &&
   [ -n "${NTM_E2E_RESTART_AGENT_STARTED:-}" ] &&
   [ -e "$NTM_E2E_RESTART_AGENT_STARTED" ]; then
  : > "$NTM_E2E_RESTART_READINESS_CAPTURE"
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(tmuxWrapper, []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write restart tmux guard: %v", err)
	}
	rejectionEnv := mergeRobotProcessEnv(baseEnv, map[string]string{
		"NTM_TMUX_BINARY":               tmuxWrapper,
		"NTM_E2E_REAL_TMUX":             fixture.tmuxPath,
		"NTM_E2E_RESTART_ACTUATION_LOG": actuationLog,
	})

	dryRunConflictID := createBead("Dry-run occupied restart target")
	writeSpawnFakeBV(t, filepath.Join(fakeBin, "bv"), dryRunConflictID, "Dry-run occupied restart target")
	dryRunPrimary, dryRunPrimaryData := readAtomicAssignmentLedgerAt(t, ledgerPath)
	var dryRunNewerBackup map[string]json.RawMessage
	if err := json.Unmarshal(dryRunPrimaryData, &dryRunNewerBackup); err != nil {
		t.Fatalf("decode primary for dry-run publication window: %v", err)
	}
	nextGeneration, err := json.Marshal(dryRunPrimary.PersistenceGeneration + 1)
	if err != nil {
		t.Fatalf("encode dry-run backup generation: %v", err)
	}
	dryRunNewerBackup["persistence_generation"] = nextGeneration
	dryRunNewerBackupData, err := json.MarshalIndent(dryRunNewerBackup, "", "  ")
	if err != nil {
		t.Fatalf("encode dry-run newer backup: %v", err)
	}
	if err := os.WriteFile(ledgerPath+".bak", dryRunNewerBackupData, 0o600); err != nil {
		t.Fatalf("write dry-run newer backup: %v", err)
	}
	_, dryRunBeforeLedger := readAtomicAssignmentLedgerAt(t, ledgerPath)
	_, dryRunBeforeBackup := readAtomicAssignmentLedgerAt(t, ledgerPath+".bak")
	dryRunBeforePID := panePID(fixture.paneID)
	dryRunBeforeStatus, dryRunBeforeAssignee, dryRunBeforeRaw := beadState(dryRunConflictID)
	dryRunBeforeAgentLog, err := os.ReadFile(agentLog)
	if err != nil {
		t.Fatalf("read agent log before dry-run conflict: %v", err)
	}
	dryRunMarker := "NTM_DRY_RUN_CONFLICT_MUST_NOT_SEND"
	dryRunConflict := runBuiltRobotProcess(t, fixture.ntmPath, outsideDir, rejectionEnv,
		"--config="+configPath,
		"--robot-format=json",
		"--robot-restart-pane="+fixture.session,
		"--restart-bead="+dryRunConflictID,
		"--restart-prompt="+dryRunMarker,
		"--panes=0",
		"--dry-run",
	)
	if dryRunConflict.exitCode != 1 || len(bytes.TrimSpace(dryRunConflict.stderr)) != 0 {
		t.Fatalf("dry-run conflict exit=%d stdout=%s stderr=%s", dryRunConflict.exitCode, dryRunConflict.stdout, dryRunConflict.stderr)
	}
	var dryRunOutput restartOutput
	decodeSingleRobotJSON(t, dryRunConflict.stdout, &dryRunOutput)
	if dryRunOutput.Success || dryRunOutput.ErrorCode != "INVALID_FLAG" ||
		!strings.Contains(strings.ToLower(dryRunOutput.Error), "already occupied") || dryRunOutput.DryRun ||
		len(dryRunOutput.WouldAffect) != 0 || len(dryRunOutput.Restarted) != 0 || len(dryRunOutput.Failed) != 0 {
		t.Fatalf("dry-run conflict output=%+v", dryRunOutput)
	}
	_, dryRunAfterLedger := readAtomicAssignmentLedgerAt(t, ledgerPath)
	_, dryRunAfterBackup := readAtomicAssignmentLedgerAt(t, ledgerPath+".bak")
	if !bytes.Equal(dryRunBeforeLedger, dryRunAfterLedger) || !bytes.Equal(dryRunBeforeBackup, dryRunAfterBackup) ||
		panePID(fixture.paneID) != dryRunBeforePID {
		t.Fatal("dry-run conflict mutated the durable ledger or pane process")
	}
	dryRunAfterStatus, dryRunAfterAssignee, dryRunAfterRaw := beadState(dryRunConflictID)
	if dryRunAfterStatus != dryRunBeforeStatus || dryRunAfterAssignee != dryRunBeforeAssignee ||
		!bytes.Equal(bytes.TrimSpace(dryRunBeforeRaw), bytes.TrimSpace(dryRunAfterRaw)) {
		t.Fatalf("dry-run conflict mutated bead: before=%s after=%s", dryRunBeforeRaw, dryRunAfterRaw)
	}
	if data, err := os.ReadFile(actuationLog); err == nil {
		t.Fatalf("dry-run conflict actuated tmux: %s", data)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read dry-run conflict actuation log: %v", err)
	}
	dryRunAfterAgentLog, err := os.ReadFile(agentLog)
	if err != nil || !bytes.Equal(dryRunBeforeAgentLog, dryRunAfterAgentLog) || strings.Contains(string(dryRunAfterAgentLog), dryRunMarker) {
		t.Fatalf("dry-run conflict reached agent: err=%v before=%s after=%s", err, dryRunBeforeAgentLog, dryRunAfterAgentLog)
	}
	// Restore equal replicas for the later non-dry-run cancellation scenario;
	// the assertions above already proved the preview itself did not promote.
	if err := os.WriteFile(ledgerPath, dryRunBeforeBackup, 0o600); err != nil {
		t.Fatalf("normalize primary after dry-run publication-window proof: %v", err)
	}

	gatedID := createBead("Operator approval required")
	runBR("update", gatedID, "--add-label=release-approval", "--json")
	writeSpawnFakeBV(t, filepath.Join(fakeBin, "bv"), gatedID, "Operator approval required")
	_, beforeLedger := readAtomicAssignmentLedgerAt(t, ledgerPath)
	_, beforeBackup := readAtomicAssignmentLedgerAt(t, ledgerPath+".bak")
	beforeGatePID := panePID(fixture.paneID)
	beforeGateStatus, beforeGateAssignee, beforeGateRaw := beadState(gatedID)
	gateMarker := "NTM_GATED_RESTART_MUST_NOT_SEND"
	gated := runRestart(rejectionEnv, gatedID, gateMarker, "0")
	if gated.exitCode != 1 || len(bytes.TrimSpace(gated.stderr)) != 0 {
		t.Fatalf("gated restart exit=%d stdout=%s stderr=%s", gated.exitCode, gated.stdout, gated.stderr)
	}
	var gatedOutput restartOutput
	decodeSingleRobotJSON(t, gated.stdout, &gatedOutput)
	if gatedOutput.Success || gatedOutput.ErrorCode != "INVALID_FLAG" || gatedOutput.Error == "" || len(gatedOutput.Restarted) != 0 || len(gatedOutput.Failed) != 0 {
		t.Fatalf("gated restart output=%+v", gatedOutput)
	}
	_, afterLedger := readAtomicAssignmentLedgerAt(t, ledgerPath)
	_, afterBackup := readAtomicAssignmentLedgerAt(t, ledgerPath+".bak")
	if !bytes.Equal(beforeLedger, afterLedger) || !bytes.Equal(beforeBackup, afterBackup) || panePID(fixture.paneID) != beforeGatePID {
		t.Fatal("gated restart mutated the durable ledger or pane process")
	}
	afterGateStatus, afterGateAssignee, afterGateRaw := beadState(gatedID)
	if beforeGateStatus != afterGateStatus || beforeGateAssignee != afterGateAssignee || !bytes.Equal(bytes.TrimSpace(beforeGateRaw), bytes.TrimSpace(afterGateRaw)) {
		t.Fatalf("gated restart mutated bead: before=%s after=%s", beforeGateRaw, afterGateRaw)
	}
	if data, err := os.ReadFile(actuationLog); err == nil {
		t.Fatalf("gated restart actuated tmux: %s", data)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read gated restart actuation log: %v", err)
	}
	logData, err = os.ReadFile(agentLog)
	if err != nil || strings.Contains(string(logData), gateMarker) {
		t.Fatalf("gated restart reached agent: err=%v data=%s", err, logData)
	}

	ambiguousID := createBead("Ambiguous restart target")
	writeSpawnFakeBV(t, filepath.Join(fakeBin, "bv"), ambiguousID, "Ambiguous restart target")
	secondPane := strings.TrimSpace(fixture.mustTMUXOutput(t,
		"split-window", "-d", "-P", "-F", "#{pane_id}", "-t", fixture.session+":0", "-c", fixture.projectDir,
		"/bin/bash --noprofile --norc -i",
	))
	fixture.mustTMUXOutput(t, "select-pane", "-t", secondPane, "-T", fixture.session+"__cc_2")
	firstPID, secondPID := panePID(fixture.paneID), panePID(secondPane)
	_, ambiguousBefore := readAtomicAssignmentLedgerAt(t, ledgerPath)
	ambiguousStatus, ambiguousAssignee, ambiguousRaw := beadState(ambiguousID)
	ambiguous := runRestart(rejectionEnv, ambiguousID, "NTM_AMBIGUOUS_RESTART_MUST_NOT_SEND")
	if ambiguous.exitCode != 1 || len(bytes.TrimSpace(ambiguous.stderr)) != 0 {
		t.Fatalf("ambiguous restart exit=%d stdout=%s stderr=%s", ambiguous.exitCode, ambiguous.stdout, ambiguous.stderr)
	}
	var ambiguousOutput restartOutput
	decodeSingleRobotJSON(t, ambiguous.stdout, &ambiguousOutput)
	if ambiguousOutput.Success || ambiguousOutput.ErrorCode != "INVALID_FLAG" || !strings.Contains(ambiguousOutput.Error, "exactly one") ||
		len(ambiguousOutput.Restarted) != 0 || panePID(fixture.paneID) != firstPID || panePID(secondPane) != secondPID {
		t.Fatalf("ambiguous restart output=%+v", ambiguousOutput)
	}
	_, ambiguousAfter := readAtomicAssignmentLedgerAt(t, ledgerPath)
	if !bytes.Equal(ambiguousBefore, ambiguousAfter) {
		t.Fatal("ambiguous restart mutated durable ledger")
	}
	finalStatus, finalAssignee, finalRaw := beadState(ambiguousID)
	if finalStatus != ambiguousStatus || finalAssignee != ambiguousAssignee || !bytes.Equal(bytes.TrimSpace(finalRaw), bytes.TrimSpace(ambiguousRaw)) {
		t.Fatalf("ambiguous restart mutated bead: before=%s after=%s", ambiguousRaw, finalRaw)
	}
	if data, err := os.ReadFile(actuationLog); err == nil {
		t.Fatalf("ambiguous restart actuated tmux: %s", data)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read ambiguous restart actuation log: %v", err)
	}

	cancelID := createBead("Cancel restart during readiness")
	writeSpawnFakeBV(t, filepath.Join(fakeBin, "bv"), cancelID, "Cancel restart during readiness")
	cancelAgentStarted := filepath.Join(fixture.root, "restart-cancel-agent-started")
	cancelReadinessCapture := filepath.Join(fixture.root, "restart-cancel-readiness-capture")
	cancelAgentPath := filepath.Join(fakeBin, "restart-blocking-claude")
	cancelAgentScript := fmt.Sprintf(`#!/bin/sh
: > %s
printf 'WAITING_FOR_RESTART_CANCELLATION\n'
while :; do sleep 1; done
`, tmux.ShellQuote(cancelAgentStarted))
	if err := os.WriteFile(cancelAgentPath, []byte(cancelAgentScript), 0o700); err != nil {
		t.Fatalf("write cancellation agent: %v", err)
	}
	cancelConfigPath := filepath.Join(fixture.root, "restart-cancel-config.toml")
	cancelConfigBody := fmt.Sprintf("[agents]\nclaude = %q\n\n[spawn_pacing]\nenabled = false\n\n[assign]\noperator_gated_labels = [\"release-approval\"]\n", cancelAgentPath)
	if err := os.WriteFile(cancelConfigPath, []byte(cancelConfigBody), 0o600); err != nil {
		t.Fatalf("write cancellation config: %v", err)
	}
	cancelActuationLog := filepath.Join(fixture.root, "restart-cancel-actuation.log")
	cancelEnv := mergeRobotProcessEnv(rejectionEnv, map[string]string{
		"NTM_E2E_RESTART_ACTUATION_LOG":     cancelActuationLog,
		"NTM_E2E_RESTART_AGENT_STARTED":     cancelAgentStarted,
		"NTM_E2E_RESTART_READINESS_CAPTURE": cancelReadinessCapture,
	})
	cancelPaneIndex := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", secondPane, "#{pane_index}"))
	if cancelPaneIndex == "" {
		t.Fatal("cancellation pane index is empty")
	}
	cancelPrompt := "NTM_CANCELED_RESTART_MUST_NOT_SEND"
	cancelBeforePID := panePID(secondPane)
	_, cancelBeforeLedger := readAtomicAssignmentLedgerAt(t, ledgerPath)
	_, cancelBeforeBackup := readAtomicAssignmentLedgerAt(t, ledgerPath+".bak")
	cancelBeforeStatus, cancelBeforeAssignee, cancelBeforeRaw := beadState(cancelID)
	cancelBeforeAgentLog, err := os.ReadFile(agentLog)
	if err != nil {
		t.Fatalf("read agent log before cancellation: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, fixture.ntmPath,
		"--config="+cancelConfigPath,
		"--robot-format=json",
		"--robot-restart-pane="+fixture.session,
		"--restart-bead="+cancelID,
		"--restart-prompt="+cancelPrompt,
		"--panes="+cancelPaneIndex,
	)
	group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
	if groupErr != nil {
		t.Fatalf("create cancellation process group: %v", groupErr)
	}
	defer func() { _ = group.Close() }()
	cmd.Cancel = func() error { return group.Signal(os.Kill) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = outsideDir
	cmd.Env = append([]string(nil), cancelEnv...)
	var cancelStdout, cancelStderr bytes.Buffer
	cmd.Stdout = &cancelStdout
	cmd.Stderr = &cancelStderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start cancelable restart process: %v", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	readinessDeadline := time.Now().Add(45 * time.Second)
	for {
		if _, err := os.Stat(cancelReadinessCapture); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			cancel()
			<-waitCh
			t.Fatalf("read restart readiness marker: %v", err)
		}
		select {
		case earlyErr := <-waitCh:
			t.Fatalf("restart exited before readiness cancellation: %v stdout=%s stderr=%s", earlyErr, cancelStdout.String(), cancelStderr.String())
		default:
		}
		if time.Now().After(readinessDeadline) {
			cancel()
			<-waitCh
			_, agentStartErr := os.Stat(cancelAgentStarted)
			actuation, _ := os.ReadFile(cancelActuationLog)
			t.Fatalf(
				"timed out waiting for restart readiness poll: agent_started=%t actuation=%s stdout=%s stderr=%s",
				agentStartErr == nil,
				actuation,
				cancelStdout.String(),
				cancelStderr.String(),
			)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		cancel()
		<-waitCh
		t.Fatalf("signal restart process: %v", err)
	}
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		waitErr = <-waitCh
	}
	if ctx.Err() != nil {
		t.Fatalf("restart process did not join after SIGINT: %v stdout=%s stderr=%s", ctx.Err(), cancelStdout.String(), cancelStderr.String())
	}
	if closeErr := group.Close(); closeErr != nil {
		t.Fatalf("close cancellation process group: %v", closeErr)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("restart process was not joined: state=%v", cmd.ProcessState)
	}
	cancelExitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			t.Fatalf("wait for canceled restart: %v", waitErr)
		}
		cancelExitCode = exitErr.ExitCode()
	}
	if cancelExitCode != 1 || len(bytes.TrimSpace(cancelStderr.Bytes())) != 0 {
		t.Fatalf("canceled restart exit=%d stdout=%s stderr=%s", cancelExitCode, cancelStdout.Bytes(), cancelStderr.Bytes())
	}
	var cancelOutput restartOutput
	decodeSingleRobotJSON(t, cancelStdout.Bytes(), &cancelOutput)
	if cancelOutput.Success || cancelOutput.ErrorCode != "TIMEOUT" || cancelOutput.PromptSent ||
		!strings.Contains(strings.ToLower(cancelOutput.Error), "canceled") || !strings.Contains(strings.ToLower(cancelOutput.PromptError), "canceled") ||
		cancelOutput.BeadAssigned != cancelID || !slices.Equal(cancelOutput.Restarted, []string{cancelPaneIndex}) || len(cancelOutput.Failed) == 0 {
		t.Fatalf("canceled restart output=%+v", cancelOutput)
	}
	if cancelOutput.Failed[0].Pane != cancelPaneIndex || !strings.Contains(strings.ToLower(cancelOutput.Failed[0].Reason), "canceled") {
		t.Fatalf("canceled restart failure details=%+v", cancelOutput.Failed)
	}
	relaunched, relaunchReported := cancelOutput.AgentRelaunched[cancelPaneIndex]
	relaunchStatus, relaunchStatusReported := cancelOutput.AgentRelaunchStatus[cancelPaneIndex]
	alive, livenessReported := cancelOutput.ProcessAlive[cancelPaneIndex]
	if !relaunchReported || relaunched || !relaunchStatusReported || relaunchStatus != "unknown" || !livenessReported || !alive {
		t.Fatalf(
			"canceled restart lifecycle details relaunched=%v status=%v alive=%v",
			cancelOutput.AgentRelaunched,
			cancelOutput.AgentRelaunchStatus,
			cancelOutput.ProcessAlive,
		)
	}
	if panePID(secondPane) == cancelBeforePID {
		t.Fatalf("canceled restart did not preserve completed respawn detail; pane PID remained %s", cancelBeforePID)
	}
	_, cancelAfterLedger := readAtomicAssignmentLedgerAt(t, ledgerPath)
	_, cancelAfterBackup := readAtomicAssignmentLedgerAt(t, ledgerPath+".bak")
	if !bytes.Equal(cancelBeforeLedger, cancelAfterLedger) || !bytes.Equal(cancelBeforeBackup, cancelAfterBackup) {
		t.Fatal("readiness cancellation mutated the durable assignment ledger")
	}
	cancelAfterStatus, cancelAfterAssignee, cancelAfterRaw := beadState(cancelID)
	if cancelAfterStatus != cancelBeforeStatus || cancelAfterAssignee != cancelBeforeAssignee ||
		!bytes.Equal(bytes.TrimSpace(cancelBeforeRaw), bytes.TrimSpace(cancelAfterRaw)) {
		t.Fatalf("readiness cancellation mutated bead: before=%s after=%s", cancelBeforeRaw, cancelAfterRaw)
	}
	cancelActuationBefore, err := os.ReadFile(cancelActuationLog)
	if err != nil || !strings.Contains(string(cancelActuationBefore), "respawn-pane") || strings.Contains(string(cancelActuationBefore), cancelPrompt) {
		t.Fatalf("canceled restart actuation err=%v data=%s", err, cancelActuationBefore)
	}
	cancelAfterAgentLog, err := os.ReadFile(agentLog)
	if err != nil || !bytes.Equal(cancelBeforeAgentLog, cancelAfterAgentLog) || strings.Contains(string(cancelAfterAgentLog), cancelPrompt) {
		t.Fatalf("canceled restart reached agent: err=%v before=%s after=%s", err, cancelBeforeAgentLog, cancelAfterAgentLog)
	}
	time.Sleep(750 * time.Millisecond)
	cancelActuationAfter, err := os.ReadFile(cancelActuationLog)
	if err != nil || !bytes.Equal(cancelActuationBefore, cancelActuationAfter) {
		t.Fatalf("late restart actuation after joined cancellation: err=%v before=%s after=%s", err, cancelActuationBefore, cancelActuationAfter)
	}

	normalRespawnDone := filepath.Join(fixture.root, "normal-respawn-completed")
	normalActuationLog := filepath.Join(fixture.root, "normal-respawn-actuation.log")
	normalTmuxWrapper := filepath.Join(fakeBin, "tmux-normal-respawn-guard")
	normalWrapperScript := `#!/bin/sh
case "${1:-}" in
  respawn-pane|send-keys|load-buffer|paste-buffer|set-buffer|delete-buffer)
    printf '%s\n' "$*" >> "$NTM_E2E_RESTART_ACTUATION_LOG"
    ;;
esac
if [ "${1:-}" = "respawn-pane" ]; then
  "$NTM_E2E_REAL_TMUX" "$@"
  status=$?
  if [ "$status" -eq 0 ]; then
    : > "$NTM_E2E_NORMAL_RESPAWN_DONE"
	  exec sleep 30
  fi
  exit "$status"
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(normalTmuxWrapper, []byte(normalWrapperScript), 0o700); err != nil {
		t.Fatalf("write normal respawn tmux guard: %v", err)
	}
	normalEnv := mergeRobotProcessEnv(baseEnv, map[string]string{
		"NTM_TMUX_BINARY":                   normalTmuxWrapper,
		"NTM_E2E_REAL_TMUX":                 fixture.tmuxPath,
		"NTM_E2E_RESTART_ACTUATION_LOG":     normalActuationLog,
		"NTM_E2E_NORMAL_RESPAWN_DONE":       normalRespawnDone,
		"NTM_E2E_RESTART_AGENT_STARTED":     "",
		"NTM_E2E_RESTART_READINESS_CAPTURE": "",
	})
	normalMutatedPane := fixture.paneID
	normalMutatedIndex := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", normalMutatedPane, "#{pane_index}"))
	if normalMutatedIndex == "" || normalMutatedIndex == cancelPaneIndex {
		t.Fatalf("normal respawn pane indexes mutated=%q remaining=%q", normalMutatedIndex, cancelPaneIndex)
	}
	normalBeforePID := panePID(normalMutatedPane)
	normalRemainingBeforePID := panePID(secondPane)
	normalCtx, normalCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer normalCancel()
	normalCmd := exec.CommandContext(normalCtx, fixture.ntmPath,
		"--config="+cancelConfigPath,
		"respawn", fixture.session,
		"--force",
		"--panes="+normalMutatedIndex+","+cancelPaneIndex,
	)
	normalGroup, groupErr := testutil.NewProcessGroupForTest(normalCtx, normalCmd)
	if groupErr != nil {
		t.Fatalf("create normal respawn process group: %v", groupErr)
	}
	defer func() { _ = normalGroup.Close() }()
	normalCmd.Cancel = func() error { return normalGroup.Signal(os.Kill) }
	normalCmd.WaitDelay = 2 * time.Second
	normalCmd.Dir = outsideDir
	normalCmd.Env = append([]string(nil), normalEnv...)
	var normalStdout, normalStderr bytes.Buffer
	normalCmd.Stdout = &normalStdout
	normalCmd.Stderr = &normalStderr
	if err := normalCmd.Start(); err != nil {
		t.Fatalf("start normal respawn process: %v", err)
	}
	normalWaitCh := make(chan error, 1)
	go func() { normalWaitCh <- normalCmd.Wait() }()
	normalRespawnDeadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(normalRespawnDone); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			normalCancel()
			<-normalWaitCh
			t.Fatalf("read normal respawn completion marker: %v", err)
		}
		select {
		case earlyErr := <-normalWaitCh:
			t.Fatalf("normal respawn exited before cancellation point: %v stdout=%s stderr=%s", earlyErr, normalStdout.String(), normalStderr.String())
		default:
		}
		if time.Now().After(normalRespawnDeadline) {
			normalCancel()
			<-normalWaitCh
			t.Fatalf("timed out waiting for successful normal respawn: stdout=%s stderr=%s", normalStdout.String(), normalStderr.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	normalSignalAt := time.Now()
	if err := normalCmd.Process.Signal(syscall.SIGINT); err != nil {
		normalCancel()
		<-normalWaitCh
		t.Fatalf("signal normal respawn process: %v", err)
	}
	var normalWaitErr error
	select {
	case normalWaitErr = <-normalWaitCh:
	case <-normalCtx.Done():
		normalWaitErr = <-normalWaitCh
	}
	if normalCtx.Err() != nil {
		t.Fatalf("normal respawn did not join after SIGINT: %v stdout=%s stderr=%s", normalCtx.Err(), normalStdout.String(), normalStderr.String())
	}
	if elapsed := time.Since(normalSignalAt); elapsed > 5*time.Second {
		t.Fatalf("normal respawn cancellation took %s", elapsed)
	}
	if closeErr := normalGroup.Close(); closeErr != nil {
		t.Fatalf("close normal respawn process group: %v", closeErr)
	}
	normalExitCode := 0
	if normalWaitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(normalWaitErr, &exitErr) {
			t.Fatalf("wait for canceled normal respawn: %v", normalWaitErr)
		}
		normalExitCode = exitErr.ExitCode()
	}
	if normalExitCode != 1 || !strings.Contains(normalStderr.String(), "TIMEOUT") ||
		!strings.Contains(strings.ToLower(normalStderr.String()), "canceled") {
		t.Fatalf("normal canceled respawn exit=%d stdout=%s stderr=%s", normalExitCode, normalStdout.String(), normalStderr.String())
	}
	for _, want := range []string{
		"Restarted panes: " + normalMutatedIndex,
		"Failed to restart:",
		"  - " + normalMutatedIndex + ": ",
		"  - " + cancelPaneIndex + ": ",
		"lifecycle is incomplete",
		"respawn skipped",
		"canceled",
	} {
		if !strings.Contains(normalStdout.String(), want) {
			t.Fatalf("normal canceled respawn stdout missing %q: %s", want, normalStdout.String())
		}
	}
	if panePID(normalMutatedPane) == normalBeforePID {
		t.Fatalf("normal canceled respawn pane PID remained %s", normalBeforePID)
	}
	if afterPID := panePID(secondPane); afterPID != normalRemainingBeforePID {
		t.Fatalf("normal canceled respawn mutated skipped pane PID: before=%s after=%s", normalRemainingBeforePID, afterPID)
	}
	normalActuationBefore, err := os.ReadFile(normalActuationLog)
	if err != nil || strings.Count(string(normalActuationBefore), "respawn-pane") != 1 {
		t.Fatalf("normal respawn actuation err=%v data=%s", err, normalActuationBefore)
	}
	for _, forbidden := range []string{"send-keys", "load-buffer", "paste-buffer"} {
		if strings.Contains(string(normalActuationBefore), forbidden) {
			t.Fatalf("normal respawn performed late relaunch actuation %q: %s", forbidden, normalActuationBefore)
		}
	}
	time.Sleep(750 * time.Millisecond)
	normalActuationAfter, err := os.ReadFile(normalActuationLog)
	if err != nil || !bytes.Equal(normalActuationBefore, normalActuationAfter) {
		t.Fatalf("late normal respawn actuation after joined cancellation: err=%v before=%s after=%s", err, normalActuationBefore, normalActuationAfter)
	}

	lateRelaunchStarted := filepath.Join(fixture.root, "late-relaunch-agent-started")
	lateRelaunchDone := filepath.Join(fixture.root, "late-relaunch-send-return")
	lateRelaunchAgentLog := filepath.Join(fixture.root, "late-relaunch-agent.log")
	lateRelaunchAgentPath := filepath.Join(fakeBin, "late-relaunch-codex")
	// Same PANE_AGENT_DEAD constraint as restart-codex above: the relaunched
	// agent must be a non-shell foreground process (fakeshell) or the
	// follow-up prompt readiness gate refuses to type into the pane.
	lateRelaunchAgentScript := fmt.Sprintf(`#!/bin/sh
: > %s
exec %s --mode=echo --tty-noecho --log=%s --prompt='Codex> › ' --banner='100%% context left'
`, tmux.ShellQuote(lateRelaunchStarted), tmux.ShellQuote(fakeshellBin), tmux.ShellQuote(lateRelaunchAgentLog))
	if err := os.WriteFile(lateRelaunchAgentPath, []byte(lateRelaunchAgentScript), 0o700); err != nil {
		t.Fatalf("write late relaunch agent: %v", err)
	}
	lateRelaunchConfigPath := filepath.Join(fixture.root, "late-relaunch-config.toml")
	lateRelaunchConfig := fmt.Sprintf("[agents]\ncodex = %q\n\n[spawn_pacing]\nenabled = false\n", lateRelaunchAgentPath)
	if err := os.WriteFile(lateRelaunchConfigPath, []byte(lateRelaunchConfig), 0o600); err != nil {
		t.Fatalf("write late relaunch config: %v", err)
	}
	lateRelaunchActuationLog := filepath.Join(fixture.root, "late-relaunch-actuation.log")
	lateRelaunchTmuxWrapper := filepath.Join(fakeBin, "tmux-late-relaunch-guard")
	lateRelaunchWrapperScript := `#!/bin/sh
case "${1:-}" in
  respawn-pane|send-keys|load-buffer|paste-buffer|set-buffer|delete-buffer)
    printf '%s\n' "$*" >> "$NTM_E2E_RESTART_ACTUATION_LOG"
    ;;
esac
last=""
for arg in "$@"; do last="$arg"; done
if [ "${1:-}" = "send-keys" ] && [ "$last" = "Enter" ] && [ ! -e "$NTM_E2E_LATE_RELAUNCH_DONE" ]; then
  "$NTM_E2E_REAL_TMUX" "$@"
  status=$?
  if [ "$status" -eq 0 ]; then
    attempts=0
    while [ ! -e "$NTM_E2E_LATE_RELAUNCH_STARTED" ] && [ "$attempts" -lt 200 ]; do
      attempts=$((attempts + 1))
      sleep 0.01
    done
    [ -e "$NTM_E2E_LATE_RELAUNCH_STARTED" ] || exit 91
    : > "$NTM_E2E_LATE_RELAUNCH_DONE"
    exec sleep 30
  fi
  exit "$status"
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(lateRelaunchTmuxWrapper, []byte(lateRelaunchWrapperScript), 0o700); err != nil {
		t.Fatalf("write late relaunch tmux guard: %v", err)
	}
	lateRelaunchEnv := mergeRobotProcessEnv(baseEnv, map[string]string{
		"NTM_TMUX_BINARY":                   lateRelaunchTmuxWrapper,
		"NTM_E2E_REAL_TMUX":                 fixture.tmuxPath,
		"NTM_E2E_RESTART_ACTUATION_LOG":     lateRelaunchActuationLog,
		"NTM_E2E_LATE_RELAUNCH_STARTED":     lateRelaunchStarted,
		"NTM_E2E_LATE_RELAUNCH_DONE":        lateRelaunchDone,
		"NTM_E2E_RESTART_AGENT_STARTED":     "",
		"NTM_E2E_RESTART_READINESS_CAPTURE": "",
	})
	lateRelaunchExit, lateRelaunchStdout, lateRelaunchStderr := runCancelableRestartAtMarker(
		"late relaunch",
		lateRelaunchDone,
		lateRelaunchEnv,
		"--config="+lateRelaunchConfigPath,
		"--robot-format=json",
		"--robot-restart-pane="+fixture.session,
		"--panes="+normalMutatedIndex,
	)
	if lateRelaunchExit != 1 || len(bytes.TrimSpace(lateRelaunchStderr)) != 0 {
		t.Fatalf("late relaunch exit=%d stdout=%s stderr=%s", lateRelaunchExit, lateRelaunchStdout, lateRelaunchStderr)
	}
	var lateRelaunchOutput restartOutput
	decodeSingleRobotJSON(t, lateRelaunchStdout, &lateRelaunchOutput)
	if lateRelaunchOutput.Success || lateRelaunchOutput.ErrorCode != "TIMEOUT" ||
		!slices.Equal(lateRelaunchOutput.Restarted, []string{normalMutatedIndex}) || len(lateRelaunchOutput.Failed) != 1 ||
		!lateRelaunchOutput.AgentRelaunched[normalMutatedIndex] || !lateRelaunchOutput.ProcessAlive[normalMutatedIndex] ||
		lateRelaunchOutput.AgentRelaunchStatus[normalMutatedIndex] != "ready" || lateRelaunchOutput.PromptSent {
		t.Fatalf("late relaunch output=%+v", lateRelaunchOutput)
	}
	if lateRelaunchOutput.Failed[0].Pane != normalMutatedIndex ||
		!strings.Contains(lateRelaunchOutput.Failed[0].Reason, "became ready") ||
		!strings.Contains(lateRelaunchOutput.Failed[0].Reason, "lifecycle is incomplete") {
		t.Fatalf("late relaunch failure=%+v", lateRelaunchOutput.Failed)
	}
	if _, err := os.Stat(lateRelaunchStarted); err != nil {
		t.Fatalf("late relaunch agent did not start: %v", err)
	}
	lateRelaunchActuationBefore, err := os.ReadFile(lateRelaunchActuationLog)
	if err != nil || strings.Count(string(lateRelaunchActuationBefore), "respawn-pane") != 1 ||
		strings.Count(string(lateRelaunchActuationBefore), " Enter\n") != 1 {
		t.Fatalf("late relaunch actuation err=%v data=%s", err, lateRelaunchActuationBefore)
	}
	time.Sleep(750 * time.Millisecond)
	lateRelaunchActuationAfter, err := os.ReadFile(lateRelaunchActuationLog)
	if err != nil || !bytes.Equal(lateRelaunchActuationBefore, lateRelaunchActuationAfter) {
		t.Fatalf("late relaunch actuation continued after cancellation: err=%v before=%s after=%s", err, lateRelaunchActuationBefore, lateRelaunchActuationAfter)
	}

	latePrompt := fmt.Sprintf("NTM_LATE_PROMPT_%d", time.Now().UnixNano())
	latePromptStaged := filepath.Join(fixture.root, "late-prompt-staged")
	latePromptDone := filepath.Join(fixture.root, "late-prompt-consumed")
	latePromptActuationLog := filepath.Join(fixture.root, "late-prompt-actuation.log")
	latePromptTmuxWrapper := filepath.Join(fakeBin, "tmux-late-prompt-guard")
	latePromptWrapperScript := `#!/bin/sh
case "${1:-}" in
  respawn-pane|send-keys|load-buffer|paste-buffer|set-buffer|delete-buffer)
    printf '%s\n' "$*" >> "$NTM_E2E_RESTART_ACTUATION_LOG"
    ;;
esac
case " $* " in
  *"$NTM_E2E_LATE_PROMPT"*)
    "$NTM_E2E_REAL_TMUX" "$@"
    status=$?
    if [ "$status" -eq 0 ]; then : > "$NTM_E2E_LATE_PROMPT_STAGED"; fi
    exit "$status"
    ;;
esac
last=""
for arg in "$@"; do last="$arg"; done
if [ "${1:-}" = "send-keys" ] && [ "$last" = "Enter" ] && [ -e "$NTM_E2E_LATE_PROMPT_STAGED" ] && [ ! -e "$NTM_E2E_LATE_PROMPT_DONE" ]; then
  "$NTM_E2E_REAL_TMUX" "$@"
  status=$?
  if [ "$status" -eq 0 ]; then
    attempts=0
    while ! grep -F -- "$NTM_E2E_LATE_PROMPT" "$NTM_E2E_LATE_PROMPT_LOG" >/dev/null 2>&1 && [ "$attempts" -lt 200 ]; do
      attempts=$((attempts + 1))
      sleep 0.01
    done
    grep -F -- "$NTM_E2E_LATE_PROMPT" "$NTM_E2E_LATE_PROMPT_LOG" >/dev/null 2>&1 || exit 92
    : > "$NTM_E2E_LATE_PROMPT_DONE"
    exec sleep 30
  fi
  exit "$status"
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(latePromptTmuxWrapper, []byte(latePromptWrapperScript), 0o700); err != nil {
		t.Fatalf("write late prompt tmux guard: %v", err)
	}
	latePromptEnv := mergeRobotProcessEnv(baseEnv, map[string]string{
		"NTM_TMUX_BINARY":               latePromptTmuxWrapper,
		"NTM_E2E_REAL_TMUX":             fixture.tmuxPath,
		"NTM_E2E_RESTART_ACTUATION_LOG": latePromptActuationLog,
		"NTM_E2E_LATE_PROMPT":           latePrompt,
		"NTM_E2E_LATE_PROMPT_STAGED":    latePromptStaged,
		"NTM_E2E_LATE_PROMPT_DONE":      latePromptDone,
		"NTM_E2E_LATE_PROMPT_LOG":       lateRelaunchAgentLog,
	})
	latePromptExit, latePromptStdout, latePromptStderr := runCancelableRestartAtMarker(
		"late prompt",
		latePromptDone,
		latePromptEnv,
		"--config="+lateRelaunchConfigPath,
		"--robot-format=json",
		"--robot-restart-pane="+fixture.session,
		"--restart-prompt="+latePrompt,
		"--panes="+normalMutatedIndex,
	)
	if latePromptExit != 1 || len(bytes.TrimSpace(latePromptStderr)) != 0 {
		t.Fatalf("late prompt exit=%d stdout=%s stderr=%s", latePromptExit, latePromptStdout, latePromptStderr)
	}
	var latePromptOutput restartOutput
	decodeSingleRobotJSON(t, latePromptStdout, &latePromptOutput)
	if latePromptOutput.Success || latePromptOutput.ErrorCode != "TIMEOUT" || latePromptOutput.PromptSent ||
		latePromptOutput.PromptDelivery[normalMutatedIndex] != "unknown" ||
		!strings.Contains(latePromptOutput.PromptError, "outcome is unknown") ||
		!latePromptOutput.AgentRelaunched[normalMutatedIndex] || latePromptOutput.AgentRelaunchStatus[normalMutatedIndex] != "ready" ||
		!slices.Equal(latePromptOutput.Restarted, []string{normalMutatedIndex}) || len(latePromptOutput.Failed) != 1 {
		t.Fatalf("late prompt output=%+v", latePromptOutput)
	}
	if latePromptOutput.Failed[0].Pane != normalMutatedIndex || !strings.Contains(latePromptOutput.Failed[0].Reason, "prompt delivery canceled") {
		t.Fatalf("late prompt failure=%+v", latePromptOutput.Failed)
	}
	latePromptLog, err := os.ReadFile(lateRelaunchAgentLog)
	if err != nil || strings.Count(string(latePromptLog), latePrompt) != 1 {
		t.Fatalf("late prompt consumption count=%d err=%v data=%s", strings.Count(string(latePromptLog), latePrompt), err, latePromptLog)
	}
	latePromptActuationBefore, err := os.ReadFile(latePromptActuationLog)
	if err != nil || strings.Count(string(latePromptActuationBefore), "respawn-pane") != 1 ||
		strings.Count(string(latePromptActuationBefore), " Enter\n") != 2 {
		t.Fatalf("late prompt actuation err=%v data=%s", err, latePromptActuationBefore)
	}
	time.Sleep(750 * time.Millisecond)
	latePromptActuationAfter, err := os.ReadFile(latePromptActuationLog)
	if err != nil || !bytes.Equal(latePromptActuationBefore, latePromptActuationAfter) {
		t.Fatalf("late prompt actuation continued after cancellation: err=%v before=%s after=%s", err, latePromptActuationBefore, latePromptActuationAfter)
	}
}

func TestE2EAtomicRobotContextInjectProjectResolutionCancellationBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)

	t.Run("positive control injects after project resolution", func(t *testing.T) {
		fixture := newRobotProcessFixture(t, "context-inject-project-positive")
		const sentinel = "NTM_E2E_PROJECT_CONTEXT_SENTINEL"
		if err := os.WriteFile(filepath.Join(fixture.projectDir, "AGENTS.md"), []byte("# "+sentinel+"\n"), 0o600); err != nil {
			t.Fatalf("write context injection sentinel: %v", err)
		}

		mutationLog := filepath.Join(fixture.root, "context-inject-positive-mutations.log")
		tmuxWrapper := filepath.Join(fixture.root, "tmux-context-inject-positive")
		wrapperScript := `#!/bin/sh
case "${1:-}" in
  send-keys|respawn-pane|kill-pane|kill-window|kill-session|load-buffer|set-buffer|paste-buffer|delete-buffer)
    printf '%s\n' "$*" >> "$NTM_E2E_CONTEXT_INJECT_MUTATION_LOG"
    ;;
esac
exec "$NTM_E2E_REAL_TMUX" "$@"
`
		if err := os.WriteFile(tmuxWrapper, []byte(wrapperScript), 0o700); err != nil {
			t.Fatalf("write positive-control tmux wrapper: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY":                     tmuxWrapper,
			"NTM_E2E_REAL_TMUX":                   fixture.tmuxPath,
			"NTM_E2E_CONTEXT_INJECT_MUTATION_LOG": mutationLog,
		})

		result := runBuiltRobotProcess(t, fixture.ntmPath, fixture.root, env,
			"--robot-format=json",
			"--robot-context-inject="+fixture.session,
			"--inject-files=AGENTS.md",
			"--inject-pane=0",
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("positive context injection exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var output struct {
			Success       bool     `json:"success"`
			Session       string   `json:"session"`
			InjectedFiles []string `json:"injected_files"`
			PanesInjected []int    `json:"panes_injected"`
		}
		decodeSingleRobotJSON(t, result.stdout, &output)
		if !output.Success || output.Session != fixture.session ||
			!slices.Equal(output.InjectedFiles, []string{"AGENTS.md"}) ||
			!slices.Equal(output.PanesInjected, []int{0}) {
			t.Fatalf("positive context injection output=%+v", output)
		}
		mutations, err := os.ReadFile(mutationLog)
		if err != nil {
			t.Fatalf("read positive context injection mutations: %v", err)
		}
		if strings.Count(string(mutations), "send-keys") != 2 || !strings.Contains(string(mutations), sentinel) {
			t.Fatalf("positive control did not reach expected pane mutations: %s", mutations)
		}
	})

	t.Run("SIGINT cancels blocked project list-panes before mutation", func(t *testing.T) {
		fixture := newRobotProcessFixture(t, "context-inject-project-cancel")
		if err := os.WriteFile(filepath.Join(fixture.projectDir, "AGENTS.md"), []byte("# MUST_NOT_BE_INJECTED\n"), 0o600); err != nil {
			t.Fatalf("write cancellation context file: %v", err)
		}
		panePIDBefore := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", fixture.paneID, "#{pane_pid}"))
		if panePIDBefore == "" {
			t.Fatal("context injection cancellation fixture returned an empty pane PID")
		}

		listPanesStarted := filepath.Join(fixture.root, "context-inject-list-panes-started")
		mutationLog := filepath.Join(fixture.root, "context-inject-canceled-mutations.log")
		tmuxWrapper := filepath.Join(fixture.root, "tmux-context-inject-cancel")
		wrapperScript := `#!/bin/sh
if [ "${1:-}" = "list-panes" ]; then
  printf 'started\n' > "$NTM_E2E_CONTEXT_INJECT_LIST_PANES_STARTED"
  exec sleep 30
fi
case "${1:-}" in
  send-keys|respawn-pane|kill-pane|kill-window|kill-session|load-buffer|set-buffer|paste-buffer|delete-buffer)
    printf '%s\n' "$*" >> "$NTM_E2E_CONTEXT_INJECT_MUTATION_LOG"
    ;;
esac
exec "$NTM_E2E_REAL_TMUX" "$@"
`
		if err := os.WriteFile(tmuxWrapper, []byte(wrapperScript), 0o700); err != nil {
			t.Fatalf("write cancellation tmux wrapper: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY":                           tmuxWrapper,
			"NTM_E2E_REAL_TMUX":                         fixture.tmuxPath,
			"NTM_E2E_CONTEXT_INJECT_LIST_PANES_STARTED": listPanesStarted,
			"NTM_E2E_CONTEXT_INJECT_MUTATION_LOG":       mutationLog,
		})

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, fixture.ntmPath,
			"--robot-format=json",
			"--robot-context-inject="+fixture.session,
			"--inject-files=AGENTS.md",
			"--inject-pane=0",
		)
		group, err := testutil.NewProcessGroupForTest(ctx, cmd)
		if err != nil {
			t.Fatalf("create context injection cancellation process group: %v", err)
		}
		defer func() { _ = group.Close() }()
		cmd.Cancel = func() error { return group.Signal(os.Kill) }
		cmd.WaitDelay = 2 * time.Second
		cmd.Dir = fixture.root
		cmd.Env = append([]string(nil), env...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start context injection cancellation process: %v", err)
		}
		waitCh := make(chan error, 1)
		go func() { waitCh <- cmd.Wait() }()

		markerDeadline := time.Now().Add(20 * time.Second)
		for {
			if data, err := os.ReadFile(listPanesStarted); err == nil && strings.Contains(string(data), "started") {
				break
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				cancel()
				<-waitCh
				t.Fatalf("read list-panes marker: %v", err)
			}
			select {
			case earlyErr := <-waitCh:
				t.Fatalf("context injection exited before SIGINT: %v stdout=%s stderr=%s", earlyErr, stdout.String(), stderr.String())
			default:
			}
			if time.Now().After(markerDeadline) {
				cancel()
				<-waitCh
				t.Fatalf("timed out waiting for blocked project list-panes: stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
			time.Sleep(25 * time.Millisecond)
		}

		signalAt := time.Now()
		if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
			cancel()
			<-waitCh
			t.Fatalf("signal context injection process: %v", err)
		}
		var waitErr error
		select {
		case waitErr = <-waitCh:
		case <-ctx.Done():
			waitErr = <-waitCh
		}
		if ctx.Err() != nil {
			t.Fatalf("context injection did not join after SIGINT: %v stdout=%s stderr=%s", ctx.Err(), stdout.String(), stderr.String())
		}
		if elapsed := time.Since(signalAt); elapsed > 5*time.Second {
			t.Fatalf("context injection cancellation took %s", elapsed)
		}
		if closeErr := group.Close(); closeErr != nil {
			t.Fatalf("close context injection cancellation process group: %v", closeErr)
		}
		if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			t.Fatalf("context injection process was not joined: state=%v", cmd.ProcessState)
		}

		exitCode := 0
		if waitErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) {
				t.Fatalf("wait for canceled context injection: %v", waitErr)
			}
			exitCode = exitErr.ExitCode()
		}
		if exitCode != 1 || len(bytes.TrimSpace(stderr.Bytes())) != 0 {
			t.Fatalf("canceled context injection exit=%d stdout=%s stderr=%s", exitCode, stdout.Bytes(), stderr.Bytes())
		}

		var output struct {
			Success       bool            `json:"success"`
			Error         string          `json:"error"`
			ErrorCode     string          `json:"error_code"`
			InjectedFiles json.RawMessage `json:"injected_files"`
			PanesInjected json.RawMessage `json:"panes_injected"`
		}
		decodeSingleRobotJSON(t, stdout.Bytes(), &output)
		if output.Success || output.ErrorCode != "TIMEOUT" || !strings.Contains(strings.ToLower(output.Error), "canceled") {
			t.Fatalf("canceled context injection envelope=%+v", output)
		}
		if len(output.InjectedFiles) != 0 || len(output.PanesInjected) != 0 {
			t.Fatalf("canceled context injection reached downstream planning: files=%s panes=%s", output.InjectedFiles, output.PanesInjected)
		}

		assertStable := func(stage string) {
			t.Helper()
			panePIDAfter := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", fixture.paneID, "#{pane_pid}"))
			if panePIDAfter != panePIDBefore {
				t.Fatalf("%s: canceled context injection mutated pane PID: before=%s after=%s", stage, panePIDBefore, panePIDAfter)
			}
			data, err := os.ReadFile(mutationLog)
			if err == nil && len(bytes.TrimSpace(data)) != 0 {
				t.Fatalf("%s: context injection mutation escaped project resolution: %s", stage, data)
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s: read context injection mutation log: %v", stage, err)
			}
		}
		assertStable("joined return")
		time.Sleep(750 * time.Millisecond)
		assertStable("post-join stability")
	})
}

func TestE2EAtomicHandoffAutoCancellationBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	beadsDir := filepath.Join(projectDir, ".beads")
	fakeBin := filepath.Join(root, "bin")
	for _, dir := range []string{
		projectDir,
		beadsDir,
		fakeBin,
		filepath.Join(root, "home"),
		filepath.Join(root, "config"),
		filepath.Join(root, "data"),
		filepath.Join(root, "state"),
		filepath.Join(root, "cache"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create handoff cancellation fixture directory %s: %v", dir, err)
		}
	}

	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	beadsBefore := []byte("{\"id\":\"ntm-handoff-cancel-sentinel\",\"status\":\"open\"}\n")
	if err := os.WriteFile(beadsPath, beadsBefore, 0o600); err != nil {
		t.Fatalf("write handoff cancellation Beads sentinel: %v", err)
	}
	gitStarted := filepath.Join(root, "git-started")
	gitPIDPath := filepath.Join(root, "git-pid")
	gitAudit := filepath.Join(root, "git-audit")
	brAudit := filepath.Join(root, "br-audit")
	gitScript := `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_E2E_HANDOFF_GIT_AUDIT"
if [ "${1:-}" = "rev-parse" ] && [ "${2:-}" = "--is-inside-work-tree" ]; then
  printf '%s\n' "$$" > "$NTM_E2E_HANDOFF_GIT_PID"
  : > "$NTM_E2E_HANDOFF_GIT_STARTED"
  trap '' INT
  exec /bin/sleep 30
fi
exit 64
`
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte(gitScript), 0o700); err != nil {
		t.Fatalf("write blocking Git fixture: %v", err)
	}
	brScript := `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_E2E_HANDOFF_BR_AUDIT"
exit 97
`
	if err := os.WriteFile(filepath.Join(fakeBin, "br"), []byte(brScript), 0o700); err != nil {
		t.Fatalf("write Beads audit fixture: %v", err)
	}

	outputPath := filepath.Join(projectDir, "must-not-exist.yaml")
	env := atomicAssignmentIsolatedEnv(map[string]string{
		"HOME":                         filepath.Join(root, "home"),
		"XDG_CONFIG_HOME":              filepath.Join(root, "config"),
		"XDG_DATA_HOME":                filepath.Join(root, "data"),
		"XDG_STATE_HOME":               filepath.Join(root, "state"),
		"XDG_CACHE_HOME":               filepath.Join(root, "cache"),
		"PATH":                         fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"AGENT_MAIL_URL":               "http://127.0.0.1:1/mcp/",
		"AGENT_MAIL_TOKEN":             "",
		"NTM_DISABLE_INTERNAL_MONITOR": "1",
		"NTM_TEST_MODE":                "1",
		"NTM_E2E_HANDOFF_GIT_STARTED":  gitStarted,
		"NTM_E2E_HANDOFF_GIT_PID":      gitPIDPath,
		"NTM_E2E_HANDOFF_GIT_AUDIT":    gitAudit,
		"NTM_E2E_HANDOFF_BR_AUDIT":     brAudit,
		"HTTP_PROXY":                   "",
		"HTTPS_PROXY":                  "",
		"ALL_PROXY":                    "",
		"NO_PROXY":                     "127.0.0.1,localhost",
	})

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ntmPath,
		"handoff", "create", "general", "--auto", "--output="+outputPath,
	)
	group, err := testutil.NewProcessGroupForTest(ctx, cmd)
	if err != nil {
		t.Fatalf("create handoff cancellation process group: %v", err)
	}
	groupClosed := false
	defer func() {
		if !groupClosed {
			_ = group.Signal(os.Kill)
			_ = group.Close()
		}
	}()
	cmd.Cancel = func() error { return group.Signal(os.Kill) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = projectDir
	cmd.Env = append([]string(nil), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start handoff cancellation process: %v", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	markerDeadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(gitStarted); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			cancel()
			<-waitCh
			t.Fatalf("read handoff Git start marker: %v", err)
		}
		select {
		case earlyErr := <-waitCh:
			t.Fatalf("handoff create exited before SIGINT: %v stdout=%s stderr=%s", earlyErr, stdout.String(), stderr.String())
		default:
		}
		if time.Now().After(markerDeadline) {
			cancel()
			<-waitCh
			t.Fatalf("timed out waiting for blocked handoff Git probe: stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
		time.Sleep(25 * time.Millisecond)
	}

	pidBytes, err := os.ReadFile(gitPIDPath)
	if err != nil {
		t.Fatalf("read blocked Git PID: %v", err)
	}
	gitPID, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse blocked Git PID %q: %v", pidBytes, err)
	}
	signalAt := time.Now()
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		cancel()
		<-waitCh
		t.Fatalf("signal handoff create process: %v", err)
	}
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-time.After(5 * time.Second):
		cancel()
		waitErr = <-waitCh
		t.Fatalf("handoff create did not join within 5s after SIGINT: wait=%v stdout=%s stderr=%s", waitErr, stdout.String(), stderr.String())
	}
	if elapsed := time.Since(signalAt); elapsed > 5*time.Second {
		t.Fatalf("handoff cancellation took %s", elapsed)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("handoff process was not joined: state=%v", cmd.ProcessState)
	}
	var exitErr *exec.ExitError
	if waitErr == nil || !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("handoff cancellation wait=%v, want exit code 1; stdout=%s stderr=%s", waitErr, stdout.String(), stderr.String())
	}
	if combined := stdout.String() + stderr.String(); !strings.Contains(combined, "context canceled") {
		t.Fatalf("handoff cancellation omitted context identity: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	childDeadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(gitPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if err != nil {
			t.Fatalf("probe blocked Git child %d: %v", gitPID, err)
		}
		if time.Now().After(childDeadline) {
			t.Fatalf("blocked Git child %d survived handoff cancellation", gitPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if closeErr := group.Close(); closeErr != nil {
		t.Fatalf("close handoff cancellation process group: %v", closeErr)
	}
	groupClosed = true
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled handoff created requested output: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(projectDir, ".ntm", "handoffs")); err == nil && len(entries) != 0 {
		t.Fatalf("canceled handoff created default artifacts: %v", entries)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect canceled handoff artifact directory: %v", err)
	}
	beadsAfter, err := os.ReadFile(beadsPath)
	if err != nil || !bytes.Equal(beadsAfter, beadsBefore) {
		t.Fatalf("canceled handoff changed Beads state: err=%v before=%q after=%q", err, beadsBefore, beadsAfter)
	}
	if _, err := os.Stat(brAudit); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled handoff invoked br after Git cancellation: %v", err)
	}
	gitCalls, err := os.ReadFile(gitAudit)
	if err != nil || strings.TrimSpace(string(gitCalls)) != "rev-parse --is-inside-work-tree" {
		t.Fatalf("handoff cancellation did not reach the intended Git phase: err=%v audit=%s", err, gitCalls)
	}
}

func TestE2EAtomicSmartRestartCancellationBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	t.Run("cancels blocked pre-resolution list-sessions", func(t *testing.T) {
		fixture := newRobotProcessFixture(t, "smart-restart-resolve-cancel")
		paneIndex := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", fixture.paneID, "#{pane_index}"))
		panePIDBefore := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", fixture.paneID, "#{pane_pid}"))
		if paneIndex == "" || panePIDBefore == "" {
			t.Fatalf("resolve cancellation fixture pane index=%q pid=%q", paneIndex, panePIDBefore)
		}

		listStarted := filepath.Join(fixture.root, "smart-restart-list-sessions-started")
		mutationLog := filepath.Join(fixture.root, "smart-restart-pre-resolution-mutations.log")
		tmuxWrapper := filepath.Join(fixture.root, "tmux-smart-restart-resolve-cancel")
		wrapperScript := `#!/bin/sh
if [ "${1:-}" = "list-sessions" ]; then
  printf 'started\n' > "$NTM_E2E_SMART_RESTART_LIST_STARTED"
  exec sleep 30
fi
case "${1:-}" in
  send-keys|respawn-pane|kill-pane|kill-window|kill-session|load-buffer|set-buffer|paste-buffer|delete-buffer)
    printf '%s\n' "$*" >> "$NTM_E2E_SMART_RESTART_MUTATION_LOG"
    ;;
esac
exec "$NTM_E2E_REAL_TMUX" "$@"
`
		if err := os.WriteFile(tmuxWrapper, []byte(wrapperScript), 0o700); err != nil {
			t.Fatalf("write pre-resolution tmux wrapper: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY":                    tmuxWrapper,
			"NTM_E2E_REAL_TMUX":                  fixture.tmuxPath,
			"NTM_E2E_SMART_RESTART_LIST_STARTED": listStarted,
			"NTM_E2E_SMART_RESTART_MUTATION_LOG": mutationLog,
		})

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, fixture.ntmPath,
			"--robot-format=json",
			"--robot-smart-restart="+fixture.session,
			"--panes="+paneIndex,
			"--force",
			"--prompt=PRE_RESOLUTION_PROMPT_MUST_NOT_BE_SENT",
		)
		group, err := testutil.NewProcessGroupForTest(ctx, cmd)
		if err != nil {
			t.Fatalf("create pre-resolution cancellation process group: %v", err)
		}
		defer func() { _ = group.Close() }()
		cmd.Cancel = func() error { return group.Signal(os.Kill) }
		cmd.WaitDelay = 2 * time.Second
		cmd.Dir = fixture.projectDir
		cmd.Env = append([]string(nil), env...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start pre-resolution smart-restart: %v", err)
		}
		waitCh := make(chan error, 1)
		go func() { waitCh <- cmd.Wait() }()

		markerDeadline := time.Now().Add(20 * time.Second)
		for {
			if data, err := os.ReadFile(listStarted); err == nil && strings.Contains(string(data), "started") {
				break
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				cancel()
				<-waitCh
				t.Fatalf("read list-sessions marker: %v", err)
			}
			select {
			case earlyErr := <-waitCh:
				t.Fatalf("pre-resolution smart-restart exited before SIGINT: %v stdout=%s stderr=%s", earlyErr, stdout.String(), stderr.String())
			default:
			}
			if time.Now().After(markerDeadline) {
				cancel()
				<-waitCh
				t.Fatalf("timed out waiting for blocked list-sessions: stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
			time.Sleep(25 * time.Millisecond)
		}

		if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
			cancel()
			<-waitCh
			t.Fatalf("signal pre-resolution smart-restart: %v", err)
		}
		var waitErr error
		select {
		case waitErr = <-waitCh:
		case <-ctx.Done():
			waitErr = <-waitCh
		}
		if ctx.Err() != nil {
			t.Fatalf("pre-resolution smart-restart did not join after SIGINT: %v stdout=%s stderr=%s", ctx.Err(), stdout.String(), stderr.String())
		}
		if closeErr := group.Close(); closeErr != nil {
			t.Fatalf("close pre-resolution cancellation process group: %v", closeErr)
		}

		exitCode := 0
		if waitErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) {
				t.Fatalf("wait for pre-resolution smart-restart: %v", waitErr)
			}
			exitCode = exitErr.ExitCode()
		}
		if exitCode != 1 || len(bytes.TrimSpace(stderr.Bytes())) != 0 {
			t.Fatalf("pre-resolution cancellation exit=%d stdout=%s stderr=%s", exitCode, stdout.Bytes(), stderr.Bytes())
		}

		var output struct {
			Success   bool            `json:"success"`
			Error     string          `json:"error"`
			ErrorCode string          `json:"error_code"`
			Actions   json.RawMessage `json:"actions"`
			Summary   json.RawMessage `json:"summary"`
		}
		decodeSingleRobotJSON(t, stdout.Bytes(), &output)
		if output.Success || output.ErrorCode != "TIMEOUT" || !strings.Contains(strings.ToLower(output.Error), "canceled") {
			t.Fatalf("pre-resolution cancellation envelope=%+v", output)
		}
		if len(output.Actions) != 0 || len(output.Summary) != 0 {
			t.Fatalf("pre-resolution cancellation constructed an action plan: actions=%s summary=%s", output.Actions, output.Summary)
		}

		panePIDAfter := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", fixture.paneID, "#{pane_pid}"))
		if panePIDAfter != panePIDBefore {
			t.Fatalf("pre-resolution cancellation mutated pane PID: before=%s after=%s", panePIDBefore, panePIDAfter)
		}
		assertNoMutations := func(stage string) {
			t.Helper()
			data, err := os.ReadFile(mutationLog)
			if err == nil && len(bytes.TrimSpace(data)) != 0 {
				t.Fatalf("%s: smart-restart mutation escaped before resolution: %s", stage, data)
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s: read smart-restart mutation log: %v", stage, err)
			}
		}
		assertNoMutations("joined return")
		time.Sleep(750 * time.Millisecond)
		assertNoMutations("post-join stability")
	})

	fixture := newRobotProcessFixture(t, "smart-restart-cancel")
	secondPane := strings.TrimSpace(fixture.mustTMUXOutput(t,
		"split-window", "-d", "-P", "-F", "#{pane_id}", "-t", fixture.session+":0", "-c", fixture.projectDir,
		"/bin/bash --noprofile --norc -i",
	))
	fixture.mustTMUXOutput(t, "select-pane", "-t", secondPane, "-T", fixture.session+"__cod_2")

	firstIndex := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", fixture.paneID, "#{pane_index}"))
	secondIndex := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", secondPane, "#{pane_index}"))
	if firstIndex == "" || secondIndex == "" || firstIndex >= secondIndex {
		t.Fatalf("unexpected pane order first=%q second=%q", firstIndex, secondIndex)
	}
	secondPIDBefore := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", secondPane, "#{pane_pid}"))

	fakeBin := filepath.Join(fixture.root, "smart-restart-bin")
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatalf("create smart-restart fake bin: %v", err)
	}
	agentStarted := filepath.Join(fixture.root, "smart-restart-agent-started")
	agentPath := filepath.Join(fakeBin, "cod")
	agentScript := fmt.Sprintf(`#!/bin/sh
printf 'started\n' > %s
printf 'Codex> \n100%%%% context left\n'
while :; do sleep 1; done
`, tmux.ShellQuote(agentStarted))
	if err := os.WriteFile(agentPath, []byte(agentScript), 0o700); err != nil {
		t.Fatalf("write smart-restart agent: %v", err)
	}
	pathSetup := "export PATH=" + tmux.ShellQuote(fakeBin) + ":$PATH"
	for _, paneID := range []string{fixture.paneID, secondPane} {
		fixture.mustTMUXOutput(t, "send-keys", "-t", paneID, "-l", pathSetup)
		fixture.mustTMUXOutput(t, "send-keys", "-t", paneID, "Enter")
	}
	time.Sleep(100 * time.Millisecond)

	launchActuated := filepath.Join(fixture.root, "smart-restart-launch-actuated")
	actuationLog := filepath.Join(fixture.root, "smart-restart-actuation.log")
	tmuxWrapper := filepath.Join(fakeBin, "tmux-smart-restart-cancel")
	wrapperScript := `#!/bin/sh
target=""
previous=""
last=""
for arg in "$@"; do
  if [ "$previous" = "-t" ]; then target="$arg"; fi
  previous="$arg"
  last="$arg"
done
if [ "${1:-}" = "send-keys" ]; then
  launch=0
  case "$last" in cod*) launch=1 ;; esac
  printf 'send-keys target=%s launch=%s\n' "$target" "$launch" >> "$NTM_E2E_SMART_RESTART_ACTUATION_LOG"
  if [ "$launch" -eq 1 ] && [ ! -e "$NTM_E2E_SMART_RESTART_LAUNCH_ACTUATED" ]; then
    "$NTM_E2E_REAL_TMUX" "$@"
    status=$?
    if [ "$status" -eq 0 ]; then
      attempts=0
      while [ ! -e "$NTM_E2E_SMART_RESTART_AGENT_STARTED" ] && [ "$attempts" -lt 200 ]; do
        attempts=$((attempts + 1))
        sleep 0.01
      done
      [ -e "$NTM_E2E_SMART_RESTART_AGENT_STARTED" ] || exit 93
      printf 'actuated\n' > "$NTM_E2E_SMART_RESTART_LAUNCH_ACTUATED"
      exec sleep 30
    fi
    exit "$status"
  fi
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(tmuxWrapper, []byte(wrapperScript), 0o700); err != nil {
		t.Fatalf("write smart-restart tmux wrapper: %v", err)
	}
	env := mergeRobotProcessEnv(fixture.env, map[string]string{
		"NTM_TMUX_BINARY":                       tmuxWrapper,
		"NTM_E2E_REAL_TMUX":                     fixture.tmuxPath,
		"NTM_E2E_SMART_RESTART_AGENT_STARTED":   agentStarted,
		"NTM_E2E_SMART_RESTART_LAUNCH_ACTUATED": launchActuated,
		"NTM_E2E_SMART_RESTART_ACTUATION_LOG":   actuationLog,
	})

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, fixture.ntmPath,
		"--robot-format=json",
		"--robot-smart-restart="+fixture.session,
		"--panes="+firstIndex+","+secondIndex,
		"--force",
	)
	group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
	if groupErr != nil {
		t.Fatalf("create smart-restart cancellation process group: %v", groupErr)
	}
	defer func() { _ = group.Close() }()
	cmd.Cancel = func() error { return group.Signal(os.Kill) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = fixture.projectDir
	cmd.Env = append([]string(nil), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start smart-restart cancellation process: %v", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	markerDeadline := time.Now().Add(45 * time.Second)
	for {
		data, err := os.ReadFile(launchActuated)
		if err == nil && strings.Contains(string(data), "actuated") {
			break
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			cancel()
			<-waitCh
			t.Fatalf("read smart-restart launch marker: %v", err)
		}
		select {
		case earlyErr := <-waitCh:
			t.Fatalf("smart-restart exited before SIGINT point: %v stdout=%s stderr=%s", earlyErr, stdout.String(), stderr.String())
		default:
		}
		if time.Now().After(markerDeadline) {
			cancel()
			waitErr := <-waitCh
			actuationData, actuationErr := os.ReadFile(actuationLog)
			agentStartedData, agentStartedErr := os.ReadFile(agentStarted)
			t.Fatalf(
				"timed out waiting for smart-restart launch actuation: wait_err=%v actuation_log=%q actuation_err=%v agent_started=%q agent_started_err=%v stdout=%s stderr=%s",
				waitErr,
				actuationData,
				actuationErr,
				agentStartedData,
				agentStartedErr,
				stdout.String(),
				stderr.String(),
			)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		cancel()
		<-waitCh
		t.Fatalf("signal smart-restart process: %v", err)
	}
	var waitErr error
	joinTimer := time.NewTimer(5 * time.Second)
	defer joinTimer.Stop()
	select {
	case waitErr = <-waitCh:
	case <-joinTimer.C:
		cancel()
		waitErr = <-waitCh
		t.Fatalf("smart-restart did not join within 5s after SIGINT: wait_err=%v stdout=%s stderr=%s", waitErr, stdout.String(), stderr.String())
	case <-ctx.Done():
		waitErr = <-waitCh
	}
	if ctx.Err() != nil {
		t.Fatalf("smart-restart did not join after SIGINT: %v stdout=%s stderr=%s", ctx.Err(), stdout.String(), stderr.String())
	}
	if closeErr := group.Close(); closeErr != nil {
		t.Fatalf("close smart-restart cancellation process group: %v", closeErr)
	}
	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			t.Fatalf("wait for canceled smart-restart: %v", waitErr)
		}
		exitCode = exitErr.ExitCode()
	}
	if exitCode != 1 || len(bytes.TrimSpace(stderr.Bytes())) != 0 {
		t.Fatalf("canceled smart-restart exit=%d stdout=%s stderr=%s", exitCode, stdout.Bytes(), stderr.Bytes())
	}

	type smartRestartCancellationOutput struct {
		Success   bool   `json:"success"`
		Error     string `json:"error"`
		ErrorCode string `json:"error_code"`
		Actions   map[string]struct {
			Action          string `json:"action"`
			Reason          string `json:"reason"`
			Error           string `json:"error"`
			RestartSequence *struct {
				AgentLaunched     bool   `json:"agent_launched"`
				AgentLaunchStatus string `json:"agent_launch_status"`
				LaunchAttempted   bool   `json:"launch_attempted"`
				ProcessAlive      *bool  `json:"process_alive"`
				ShellPID          int    `json:"shell_pid"`
			} `json:"restart_sequence"`
		} `json:"actions"`
		Summary struct {
			Failed        int                 `json:"failed"`
			Skipped       int                 `json:"skipped"`
			PanesByAction map[string][]string `json:"panes_by_action"`
		} `json:"summary"`
	}
	var output smartRestartCancellationOutput
	decodeSingleRobotJSON(t, stdout.Bytes(), &output)
	current := output.Actions[firstIndex]
	remaining := output.Actions[secondIndex]
	if output.Success || output.ErrorCode != "TIMEOUT" || !strings.Contains(strings.ToLower(output.Error), "canceled") {
		t.Fatalf("canceled smart-restart envelope=%+v", output)
	}
	if current.Action != "FAILED" || current.RestartSequence == nil || !current.RestartSequence.LaunchAttempted ||
		!current.RestartSequence.AgentLaunched || current.RestartSequence.AgentLaunchStatus != "ready" ||
		current.RestartSequence.ProcessAlive == nil || !*current.RestartSequence.ProcessAlive || current.RestartSequence.ShellPID <= 0 {
		t.Fatalf("current smart-restart action lost late-launch truth: %+v", current)
	}
	if remaining.Action != "SKIPPED" || remaining.RestartSequence != nil || !strings.Contains(remaining.Reason, "not attempted") {
		t.Fatalf("remaining smart-restart action=%+v, want explicit unchanged skip", remaining)
	}
	if output.Summary.Failed != 1 || output.Summary.Skipped != 1 ||
		!slices.Equal(output.Summary.PanesByAction["FAILED"], []string{firstIndex}) ||
		!slices.Equal(output.Summary.PanesByAction["SKIPPED"], []string{secondIndex}) {
		t.Fatalf("canceled smart-restart summary=%+v", output.Summary)
	}
	secondPIDAfter := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", secondPane, "#{pane_pid}"))
	if secondPIDAfter != secondPIDBefore {
		t.Fatalf("smart-restart cancellation mutated remaining pane PID: before=%s after=%s", secondPIDBefore, secondPIDAfter)
	}
	actuationBefore, err := os.ReadFile(actuationLog)
	if err != nil {
		t.Fatalf("read smart-restart actuation log: %v", err)
	}
	if strings.Contains(string(actuationBefore), "target="+fixture.session+":0."+secondIndex) ||
		strings.Count(string(actuationBefore), "launch=1") != 1 {
		t.Fatalf("smart-restart touched remaining pane or relaunched more than once: %s", actuationBefore)
	}
	time.Sleep(750 * time.Millisecond)
	actuationAfter, err := os.ReadFile(actuationLog)
	if err != nil || !bytes.Equal(actuationBefore, actuationAfter) {
		t.Fatalf("late smart-restart actuation after joined cancellation: err=%v before=%s after=%s", err, actuationBefore, actuationAfter)
	}
}

func TestE2EUpgradeJSONRequiresCheckBuiltBinary(t *testing.T) {
	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	process := runBuiltRobotProcess(t, ntmPath, t.TempDir(), os.Environ(), "--json", "upgrade")
	if process.exitCode != 1 || len(bytes.TrimSpace(process.stderr)) != 0 {
		t.Fatalf("upgrade JSON validation exit=%d stdout=%s stderr=%s", process.exitCode, process.stdout, process.stderr)
	}
	var output struct {
		Success      bool   `json:"success"`
		OutputFormat string `json:"output_format"`
		Error        string `json:"error"`
		ErrorCode    string `json:"error_code"`
	}
	decodeSingleRobotJSON(t, process.stdout, &output)
	if output.Success || output.OutputFormat != "json" || output.ErrorCode != "INVALID_FLAG" || output.Error == "" {
		t.Fatalf("upgrade JSON validation output=%+v", output)
	}
}

func TestE2EGlobalJSONOverridesLocalFormatsBuiltBinary(t *testing.T) {
	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}

	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	fakeBin := filepath.Join(root, "bin")
	repoDir := filepath.Join(root, "repo")
	for _, dir := range []string{homeDir, configDir, dataDir, fakeBin, repoDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create precedence fixture directory %s: %v", dir, err)
		}
	}

	scanDir := filepath.Join(root, "scan")
	if err := os.MkdirAll(scanDir, 0o700); err != nil {
		t.Fatalf("create scrub precedence directory: %v", err)
	}
	bvArgsPath := filepath.Join(root, "bv-args")
	bvScript := `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_E2E_BV_ARGS"
case "${1:-}" in
  --robot-triage)
    printf '{"generated_at":"2026-07-14T00:00:00Z","data_hash":"fixture","triage":{"meta":{"version":"fixture","generated_at":"2026-07-14T00:00:00Z"},"quick_ref":{"open_count":0,"actionable_count":0,"blocked_count":0,"in_progress_count":0,"top_picks":[]},"recommendations":[]}}\n'
    ;;
  *)
    printf '{"nodes":[],"edges":[]}\n'
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(fakeBin, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write deterministic bv wrapper: %v", err)
	}
	brScript := "#!/bin/sh\nprintf '[]\\n'\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "br"), []byte(brScript), 0o700); err != nil {
		t.Fatalf("write deterministic br wrapper: %v", err)
	}
	env := atomicAssignmentIsolatedEnv(map[string]string{
		"HOME":            homeDir,
		"XDG_CONFIG_HOME": configDir,
		"XDG_DATA_HOME":   dataDir,
		"NO_COLOR":        "1",
		"PATH":            fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"NTM_E2E_BV_ARGS": bvArgsPath,
	})

	initCmd := exec.Command("git", "init", "-b", "main")
	initCmd.Dir = repoDir
	initCmd.Env = append([]string(nil), env...)
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("initialize precedence git fixture: %v: %s", err, output)
	}
	commitCmd := exec.Command(
		"git", "-c", "user.name=NTM E2E", "-c", "user.email=ntm-e2e@example.invalid",
		"commit", "--allow-empty", "-m", "fixture",
	)
	commitCmd.Dir = repoDir
	commitCmd.Env = append([]string(nil), env...)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("commit precedence git fixture: %v: %s", err, output)
	}

	for _, test := range []struct {
		name          string
		args          []string
		emptyArrays   []string
		wantRootArray bool
		isolateTools  bool
	}{
		{name: "analytics_csv", args: []string{"--json", "analytics", "--format=csv"}},
		{name: "analytics_prometheus", args: []string{"--json", "analytics", "--format=prometheus"}},
		{name: "analytics_uppercase_json", args: []string{"analytics", "--format=JSON"}},
		{name: "analytics_precommand_format", args: []string{"--format=json", "analytics"}},
		{name: "approve_list", args: []string{"approve", "list", "--json"}, emptyArrays: []string{"pending"}},
		{name: "audit_csv", args: []string{"--json", "audit", "export", "missing-session", "--format=csv"}},
		{name: "audit_uppercase_json", args: []string{"audit", "export", "missing-session", "--format=JSON"}},
		{name: "handoff_markdown", args: []string{"--json", "handoff", "create", "--goal=completed", "--now=continue", "--output=-", "--format=markdown", "--include-git=false"}},
		{name: "handoff_uppercase_json", args: []string{"handoff", "create", "--goal=completed", "--now=continue", "--output=-", "--format=JSON", "--include-git=false"}},
		{name: "metrics_prometheus", args: []string{"--json", "metrics", "export", "--format=prometheus"}},
		{name: "metrics_uppercase_json", args: []string{"metrics", "export", "--format=JSON"}},
		{name: "numeric_global_json", args: []string{"--json=1", "analytics", "--format=csv"}},
		{name: "root_since_before_json_command", args: []string{"--since", "7d", "summary", "--all", "--format=json"}},
		{name: "rotate_status", args: []string{"rotate", "status", "--data-dir=" + repoDir, "--json"}},
		{name: "scrub_text", args: []string{"--json", "scrub", "--path=" + scanDir, "--format=text"}, emptyArrays: []string{"findings"}},
		{name: "scrub_uppercase_json", args: []string{"scrub", "--path=" + scanDir, "--format=JSON"}, emptyArrays: []string{"findings"}},
		{name: "scrub_zero_roots_local_json", args: []string{"scrub", "--format=json"}, emptyArrays: []string{"findings"}},
		{name: "summary_empty", args: []string{"--json", "summary", "--all"}},
		{name: "work_commit_ready_text", args: []string{"--json", "work", "commit-ready", "--format=text"}, emptyArrays: []string{"findings"}},
		{name: "work_commit_readiness_alias_text", args: []string{"--json", "work", "commit-readiness", "--format=text"}, emptyArrays: []string{"findings"}},
		{name: "work_commit_ready_uppercase_json", args: []string{"work", "commit-ready", "--format=JSON"}, emptyArrays: []string{"findings"}},
		{name: "work_graph_dot", args: []string{"--json", "work", "graph", "--format=dot"}},
		{name: "work_graph_mermaid", args: []string{"--json", "work", "graph", "--format=mermaid"}},
		{name: "work_graph_uppercase_json", args: []string{"work", "graph", "--format=JSON"}},
		{name: "work_queue_dry_text", args: []string{"--json", "work", "queue-dry", "--format=text"}},
		{name: "work_queue_dry_markdown", args: []string{"--json", "work", "queue-dry", "--format=markdown"}},
		{name: "work_queue_dry_uppercase_json", args: []string{"work", "queue-dry", "--format=JSON"}},
		{name: "work_triage_markdown", args: []string{"--json", "work", "triage", "--format=markdown"}},
		{name: "work_triage_uppercase_json", args: []string{"work", "triage", "--format=JSON"}},
		{name: "worktree_table", args: []string{"--json", "worktree", "list", "--output=table"}, wantRootArray: true},
		{name: "worktree_uppercase_json", args: []string{"worktree", "list", "--output=JSON"}, wantRootArray: true},
		{name: "worktree_precommand_output", args: []string{"--output=json", "worktree", "list"}, wantRootArray: true},
		{name: "support_bundle", args: []string{"support-bundle", "--output=" + filepath.Join(root, "support-bundle"), "--json"}, isolateTools: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--profile-startup"}, test.args...)
			processEnv := env
			if test.isolateTools {
				// Doctor data in a support bundle probes every installed tool. Keep
				// this format-precedence test independent of host tools and load.
				processEnv = mergeRobotProcessEnv(env, map[string]string{
					"PATH":            fakeBin,
					"NTM_TMUX_BINARY": filepath.Join(fakeBin, "missing-tmux"),
				})
			}
			process := runBuiltRobotProcess(t, ntmPath, repoDir, processEnv, args...)
			if process.exitCode != 0 || len(bytes.TrimSpace(process.stderr)) != 0 {
				t.Fatalf("global JSON precedence exit=%d stdout=%s stderr=%s", process.exitCode, process.stdout, process.stderr)
			}
			var document any
			decodeSingleRobotJSON(t, process.stdout, &document)
			if test.wantRootArray {
				if _, ok := document.([]any); !ok {
					t.Fatalf("root document = %#v, want JSON array", document)
				}
			}
			if len(test.emptyArrays) > 0 {
				object, ok := document.(map[string]any)
				if !ok {
					t.Fatalf("root document = %#v, want JSON object", document)
				}
				for _, field := range test.emptyArrays {
					value, ok := object[field].([]any)
					if !ok || len(value) != 0 {
						t.Fatalf("%s = %#v, want empty JSON array", field, object[field])
					}
				}
			}
		})
	}

	for _, test := range []struct {
		name         string
		args         []string
		errorCode    string
		emptyArrays  []string
		outputFormat string
		exitMeta     int
	}{
		{name: "ensemble_id_only", args: []string{"--json", "ensemble", "suggest", "review architecture", "--id-only"}},
		{name: "ensemble_stop_confirmation", args: []string{"--json", "ensemble", "stop"}},
		{name: "handoff_missing_machine_inputs", args: []string{"--json", "handoff", "create", "--format=markdown"}},
		{name: "invalid_json_boolean", args: []string{"--json=bogus", "version"}},
		{name: "review_queue_send", args: []string{"--json", "review-queue", "--send"}},
		{name: "root_nonpersistent_limit", args: []string{"--limit", "5", "ensemble", "presets", "--format=json"}},
		{name: "swarm_empty_scan", args: []string{"--json", "swarm", "--dry-run", "--scan-dir=" + scanDir}, errorCode: robot.ErrCodeNotFound, emptyArrays: []string{"allocations", "sessions"}, outputFormat: "json", exitMeta: 1},
		{name: "swarm_stop_confirmation", args: []string{"swarm", "stop", "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--profile-startup"}, test.args...)
			process := runBuiltRobotProcess(t, ntmPath, repoDir, env, args...)
			if process.exitCode == 0 || len(bytes.TrimSpace(process.stderr)) != 0 {
				t.Fatalf("machine incompatibility exit=%d stdout=%s stderr=%s", process.exitCode, process.stdout, process.stderr)
			}
			var document map[string]any
			decodeSingleRobotJSON(t, process.stdout, &document)
			wantCode := test.errorCode
			if wantCode == "" {
				wantCode = robot.ErrCodeInvalidFlag
			}
			if success, _ := document["success"].(bool); success || document["error"] == "" || document["error_code"] != wantCode {
				t.Fatalf("machine incompatibility document=%+v", document)
			}
			if test.outputFormat != "" && document["output_format"] != test.outputFormat {
				t.Fatalf("output_format = %#v, want %q", document["output_format"], test.outputFormat)
			}
			if test.exitMeta != 0 {
				meta, ok := document["_meta"].(map[string]any)
				if !ok || meta["exit_code"] != float64(test.exitMeta) {
					t.Fatalf("_meta = %#v, want exit_code=%d", document["_meta"], test.exitMeta)
				}
			}
			for _, field := range test.emptyArrays {
				value, ok := document[field].([]any)
				if !ok || len(value) != 0 {
					t.Fatalf("%s = %#v, want empty JSON array", field, document[field])
				}
			}
		})
	}

	t.Run("precommand_non_json_format_preserves_human_profile", func(t *testing.T) {
		process := runBuiltRobotProcess(t, ntmPath, repoDir, env,
			"--profile-startup", "--format=csv", "audit", "export", "missing-session",
		)
		if process.exitCode != 0 {
			t.Fatalf("precommand human format exit=%d stdout=%s stderr=%s", process.exitCode, process.stdout, process.stderr)
		}
		if json.Valid(bytes.TrimSpace(process.stdout)) {
			t.Fatalf("precommand CSV format was incorrectly treated as JSON: %s", process.stdout)
		}
		if len(bytes.TrimSpace(process.stderr)) == 0 {
			t.Fatalf("human startup profile was incorrectly suppressed: stdout=%s", process.stdout)
		}
	})

	bvArgs, err := os.ReadFile(bvArgsPath)
	if err != nil {
		t.Fatalf("read bv precedence arguments: %v", err)
	}
	var graphCalls int
	for _, call := range strings.Split(strings.TrimSpace(string(bvArgs)), "\n") {
		if strings.HasPrefix(call, "--robot-graph") {
			graphCalls++
			if call != "--robot-graph --graph-format json" {
				t.Fatalf("bv graph arguments=%q, want JSON override", call)
			}
		}
	}
	if graphCalls != 3 {
		t.Fatalf("bv calls=%q, want three JSON graph calls", bvArgs)
	}
}

func TestE2ELocalJSONSessionInferenceBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "local-json-inference")

	for _, test := range []struct {
		name        string
		args        []string
		emptyArrays []string
	}{
		{name: "ensemble_status", args: []string{"ensemble", "status", "--format=JSON"}},
		{name: "ensemble_stop", args: []string{"ensemble", "stop", "--format=JSON", "--yes"}},
		{name: "ensemble_synthesize", args: []string{"ensemble", "synthesize", "--format=JSON"}},
		{name: "ensemble_export_findings", args: []string{"ensemble", "export-findings", "--format=JSON", "--all"}},
		{name: "ensemble_provenance", args: []string{"ensemble", "provenance", "--format=JSON", "--all"}},
		{name: "rebalance", args: []string{"rebalance", "--format=JSON"}},
		{name: "review_queue", args: []string{"review-queue", "--format=JSON"}, emptyArrays: []string{"suggestions"}},
		{name: "summary", args: []string{"summary", "--format=JSON"}},
		{name: "dashboard", args: []string{"dashboard", fixture.session, "--json"}},
		{name: "swarm_status", args: []string{"swarm", "status", "--json"}},
		{name: "swarm_stop_empty", args: []string{"swarm", "stop", "--json", "--yes"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--profile-startup"}, test.args...)
			process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, fixture.env, args...)
			if len(bytes.TrimSpace(process.stderr)) != 0 {
				t.Fatalf("local JSON inference exit=%d stderr=%q stdout=%s", process.exitCode, process.stderr, process.stdout)
			}
			var document any
			decodeSingleRobotJSON(t, process.stdout, &document)
			if len(test.emptyArrays) > 0 {
				object, ok := document.(map[string]any)
				if !ok {
					t.Fatalf("root document = %#v, want JSON object", document)
				}
				for _, field := range test.emptyArrays {
					value, ok := object[field].([]any)
					if !ok || len(value) != 0 {
						t.Fatalf("%s = %#v, want empty JSON array", field, object[field])
					}
				}
			}
		})
	}
}

func TestE2ERobotAssignTerminalContractsBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "assign-terminal")

	type assignEnvelope struct {
		Success         bool             `json:"success"`
		Timestamp       string           `json:"timestamp"`
		OutputFormat    string           `json:"output_format"`
		Error           string           `json:"error"`
		ErrorCode       string           `json:"error_code"`
		Recommendations []map[string]any `json:"recommendations"`
		BlockedBeads    []map[string]any `json:"blocked_beads"`
		IdleAgents      []string         `json:"idle_agents"`
	}
	assertAssignFailure := func(t *testing.T, process robotBoundaryProcessResult, wantCode string) assignEnvelope {
		t.Helper()
		if process.exitCode != 1 {
			t.Fatalf("exit=%d, want 1; stdout=%s stderr=%s", process.exitCode, process.stdout, process.stderr)
		}
		if len(bytes.TrimSpace(process.stderr)) != 0 {
			t.Fatalf("stderr=%q, want empty canonical robot boundary", process.stderr)
		}
		var envelope assignEnvelope
		decodeSingleRobotJSON(t, process.stdout, &envelope)
		if envelope.Success || envelope.ErrorCode != wantCode || envelope.Error == "" || envelope.OutputFormat != "json" {
			t.Fatalf("assign envelope=%+v, want %s canonical JSON failure", envelope, wantCode)
		}
		if envelope.Recommendations == nil || envelope.BlockedBeads == nil || envelope.IdleAgents == nil {
			t.Fatalf("assign failure lost required arrays: %+v", envelope)
		}
		if _, err := time.Parse(time.RFC3339, envelope.Timestamp); err != nil {
			t.Fatalf("terminal timestamp %q is not RFC3339: %v", envelope.Timestamp, err)
		}
		return envelope
	}
	writeTmuxGuard := func(t *testing.T, name string) (string, string, string) {
		t.Helper()
		wrapperPath := filepath.Join(fixture.root, "tmux-"+name)
		actuationLog := filepath.Join(fixture.root, name+"-tmux-actuation.log")
		hasSessionLog := filepath.Join(fixture.root, name+"-tmux-has-session.log")
		wrapper := `#!/bin/sh
command=${1:-}
target=
previous=
for argument in "$@"; do
    if [ "$previous" = "-t" ]; then
        target=$argument
    fi
    previous=$argument
done

case "$command" in
    new-session|kill-session|kill-pane|split-window|join-pane|break-pane|respawn-pane|send-keys|select-layout|select-pane|move-window|link-window|unlink-window|rename-session|rename-window|set-option|set-window-option)
        printf '%s\n' "$*" >> "$NTM_E2E_TMUX_ACTUATION_LOG"
        ;;
esac

if [ "$command" = "has-session" ] && [ "$target" = "$NTM_E2E_TARGET_SESSION" ] && [ -n "${NTM_E2E_HAS_SESSION_EXIT:-}" ]; then
    printf '%s\n' "$*" >> "$NTM_E2E_HAS_SESSION_LOG"
    exit "$NTM_E2E_HAS_SESSION_EXIT"
fi

exec "$NTM_E2E_REAL_TMUX" "$@"
`
		if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
			t.Fatalf("write %s tmux guard: %v", name, err)
		}
		return wrapperPath, actuationLog, hasSessionLog
	}
	assertFileAbsent := func(t *testing.T, path, description string) {
		t.Helper()
		if data, err := os.ReadFile(path); err == nil {
			t.Fatalf("%s unexpectedly recorded work: %s", description, data)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read %s: %v", path, err)
		}
	}
	linkTool := func(t *testing.T, dir, source, name string) {
		t.Helper()
		if err := os.Symlink(source, filepath.Join(dir, name)); err != nil {
			t.Fatalf("link %s into isolated PATH: %v", name, err)
		}
	}

	t.Run("missing_bv_is_dependency_missing", func(t *testing.T) {
		pathDir := t.TempDir()
		linkTool(t, pathDir, fixture.tmuxPath, "tmux")
		env := mergeRobotProcessEnv(fixture.env, map[string]string{"PATH": pathDir})
		process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, env,
			"--robot-format=json",
			"--robot-assign="+fixture.session,
			"--strategy=balanced",
		)
		envelope := assertAssignFailure(t, process, "DEPENDENCY_MISSING")
		if !strings.Contains(envelope.Error, "bv is not installed") {
			t.Fatalf("missing bv diagnostic=%q", envelope.Error)
		}
	})

	t.Run("missing_br_is_dependency_missing", func(t *testing.T) {
		brPath, err := exec.LookPath("br")
		if err != nil {
			t.Fatalf("real br is required: %v", err)
		}
		bvPath, err := exec.LookPath("bv")
		if err != nil {
			t.Fatalf("real bv is required: %v", err)
		}
		runRobotFixtureTool(t, fixture, defaultRunTimeout, brPath, "init", "--prefix=rae2e", "--json")
		runRobotFixtureTool(t, fixture, defaultRunTimeout, brPath,
			"create", "Missing br recommendation contract", "--type=task", "--priority=1", "--silent")
		pathDir := t.TempDir()
		linkTool(t, pathDir, fixture.tmuxPath, "tmux")
		linkTool(t, pathDir, bvPath, "bv")
		env := mergeRobotProcessEnv(fixture.env, map[string]string{"PATH": pathDir})
		process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, env,
			"--robot-format=json",
			"--robot-assign="+fixture.session,
			"--strategy=balanced",
		)
		envelope := assertAssignFailure(t, process, "DEPENDENCY_MISSING")
		if !strings.Contains(envelope.Error, "br") || !strings.Contains(envelope.Error, "executable file not found") {
			t.Fatalf("missing br diagnostic=%q", envelope.Error)
		}
	})

	t.Run("SIGINT_cancels_blocked_ready_scan_once", func(t *testing.T) {
		fakeBin := filepath.Join(fixture.root, "cancel-bin")
		if err := os.MkdirAll(fakeBin, 0o700); err != nil {
			t.Fatalf("create cancellation fake bin: %v", err)
		}
		brStarted := filepath.Join(fixture.root, "cancel-br-started")
		brCalls := filepath.Join(fixture.root, "cancel-br-calls.log")
		bvPath := filepath.Join(fakeBin, "bv")
		bvScript := `#!/bin/sh
case "${1:-}" in
    --robot-triage)
        printf '%s\n' '{"triage":{"recommendations":[]}}'
        ;;
    --robot-plan)
        printf '%s\n' '{"plan":{"tracks":[{"track_id":"cancel","items":[{"id":"cancel-ready","title":"Cancelable ready scan","status":"open","priority":1}]}]}}'
        ;;
    *)
        printf 'unexpected bv args: %s\n' "$*" >&2
        exit 64
        ;;
esac
`
		if err := os.WriteFile(bvPath, []byte(bvScript), 0o700); err != nil {
			t.Fatalf("write cancellation bv: %v", err)
		}
		brPath := filepath.Join(fakeBin, "br")
		brScript := `#!/bin/sh
if [ "${1:-}" = "--lock-timeout" ]; then
    shift 2
fi
printf '%s\n' "$*" >> "$NTM_E2E_BR_CALLS"
case "${1:-}" in
    ready)
        printf 'ready-started\n' > "$NTM_E2E_BR_STARTED"
        exec sleep 30
        ;;
    *)
        printf '[]\n'
        ;;
esac
`
		if err := os.WriteFile(brPath, []byte(brScript), 0o700); err != nil {
			t.Fatalf("write blocking br: %v", err)
		}
		tmuxWrapper, actuationLog, _ := writeTmuxGuard(t, "cancel")
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"PATH":                       fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"NTM_TMUX_BINARY":            tmuxWrapper,
			"NTM_E2E_REAL_TMUX":          fixture.tmuxPath,
			"NTM_E2E_TARGET_SESSION":     fixture.session,
			"NTM_E2E_TMUX_ACTUATION_LOG": actuationLog,
			"NTM_E2E_HAS_SESSION_LOG":    filepath.Join(fixture.root, "cancel-unused-has-session.log"),
			"NTM_E2E_HAS_SESSION_EXIT":   "",
			"NTM_E2E_BR_STARTED":         brStarted,
			"NTM_E2E_BR_CALLS":           brCalls,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, fixture.ntmPath,
			"--robot-format=json",
			"--robot-assign="+fixture.session,
			"--strategy=balanced",
		)
		cmd.Dir = fixture.projectDir
		cmd.Env = append([]string(nil), env...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start cancelable robot assign: %v", err)
		}
		waitCh := make(chan error, 1)
		go func() {
			waitCh <- cmd.Wait()
		}()
		startedDeadline := time.Now().Add(20 * time.Second)
		for {
			data, readErr := os.ReadFile(brStarted)
			if readErr == nil && strings.Contains(string(data), "ready-started") {
				break
			}
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				cancel()
				<-waitCh
				t.Fatalf("read blocked-ready marker: %v", readErr)
			}
			select {
			case earlyErr := <-waitCh:
				t.Fatalf("robot assign exited before blocked ready scan: %v stdout=%s stderr=%s", earlyErr, stdout.String(), stderr.String())
			default:
			}
			if time.Now().After(startedDeadline) {
				cancel()
				<-waitCh
				t.Fatalf("timed out waiting for robot assign ready scan: stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
			time.Sleep(25 * time.Millisecond)
		}
		if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
			cancel()
			<-waitCh
			t.Fatalf("signal robot assign: %v", err)
		}
		var err error
		select {
		case err = <-waitCh:
		case <-ctx.Done():
			err = <-waitCh
		}
		if ctx.Err() != nil {
			t.Fatalf("robot assign did not join after SIGINT: %v stdout=%s stderr=%s", ctx.Err(), stdout.String(), stderr.String())
		}
		if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			t.Fatalf("robot assign process was not joined: state=%v", cmd.ProcessState)
		}
		exitCode := 0
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("wait for canceled robot assign: %v", err)
			}
			exitCode = exitErr.ExitCode()
		}
		process := robotBoundaryProcessResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
		assertAssignFailure(t, process, "TIMEOUT")

		callsBefore, err := os.ReadFile(brCalls)
		if err != nil {
			t.Fatalf("read cancellation br calls: %v", err)
		}
		readyCalls := 0
		for _, call := range strings.Split(strings.TrimSpace(string(callsBefore)), "\n") {
			if strings.HasPrefix(call, "ready ") {
				readyCalls++
			}
		}
		if readyCalls != 1 {
			t.Fatalf("br calls=%q, want exactly one blocked ready scan", callsBefore)
		}
		assertFileAbsent(t, actuationLog, "canceled robot assign tmux actuation")
		time.Sleep(750 * time.Millisecond)
		callsAfter, err := os.ReadFile(brCalls)
		if err != nil {
			t.Fatalf("read late cancellation br calls: %v", err)
		}
		if !bytes.Equal(callsAfter, callsBefore) {
			t.Fatalf("late br work appeared after joined cancellation: before=%q after=%q", callsBefore, callsAfter)
		}
		assertFileAbsent(t, actuationLog, "late canceled robot assign tmux actuation")
	})

	for _, exitCode := range []int{2, 255} {
		t.Run(fmt.Sprintf("has_session_exit_%d_is_internal_error", exitCode), func(t *testing.T) {
			name := fmt.Sprintf("has-session-%d", exitCode)
			fakeBin := filepath.Join(fixture.root, name+"-bin")
			if err := os.MkdirAll(fakeBin, 0o700); err != nil {
				t.Fatalf("create %s fake bin: %v", name, err)
			}
			brCalls := filepath.Join(fixture.root, name+"-br-calls.log")
			brScript := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NTM_E2E_BR_CALLS\"\nprintf '[]\\n'\n"
			if err := os.WriteFile(filepath.Join(fakeBin, "br"), []byte(brScript), 0o700); err != nil {
				t.Fatalf("write %s br guard: %v", name, err)
			}
			tmuxWrapper, actuationLog, hasSessionLog := writeTmuxGuard(t, name)
			env := mergeRobotProcessEnv(fixture.env, map[string]string{
				"PATH":                       fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"NTM_TMUX_BINARY":            tmuxWrapper,
				"NTM_E2E_REAL_TMUX":          fixture.tmuxPath,
				"NTM_E2E_TARGET_SESSION":     fixture.session,
				"NTM_E2E_TMUX_ACTUATION_LOG": actuationLog,
				"NTM_E2E_HAS_SESSION_LOG":    hasSessionLog,
				"NTM_E2E_HAS_SESSION_EXIT":   fmt.Sprintf("%d", exitCode),
				"NTM_E2E_BR_CALLS":           brCalls,
			})
			process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, env,
				"--robot-format=json",
				"--robot-assign="+fixture.session,
				"--strategy=balanced",
			)
			envelope := assertAssignFailure(t, process, "INTERNAL_ERROR")
			if strings.Contains(strings.ToUpper(envelope.Error), "SESSION_NOT_FOUND") {
				t.Fatalf("has-session exit %d was collapsed to session absence: %+v", exitCode, envelope)
			}
			hasSessionCalls, err := os.ReadFile(hasSessionLog)
			if err != nil {
				t.Fatalf("read injected has-session calls: %v", err)
			}
			if strings.Count(strings.TrimSpace(string(hasSessionCalls)), "\n") != 0 || !strings.Contains(string(hasSessionCalls), "has-session") {
				t.Fatalf("injected has-session calls=%q, want exactly one", hasSessionCalls)
			}
			assertFileAbsent(t, actuationLog, fmt.Sprintf("has-session exit %d tmux actuation", exitCode))
			if data, err := os.ReadFile(brCalls); err == nil {
				for _, mutation := range []string{"update ", "close ", "claim ", "create ", "dep ", "sync "} {
					if strings.Contains(string(data), mutation) {
						t.Fatalf("has-session exit %d triggered br mutation %q: %s", exitCode, mutation, data)
					}
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("read has-session br calls: %v", err)
			}
			time.Sleep(250 * time.Millisecond)
			assertFileAbsent(t, actuationLog, fmt.Sprintf("late has-session exit %d tmux actuation", exitCode))
		})
	}
}

func TestE2EJSONCommandTerminalContractsBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "json-command-terminal")

	type failureEnvelope struct {
		Success   *bool  `json:"success"`
		Error     string `json:"error"`
		ErrorCode string `json:"error_code"`
	}
	assertOneFailure := func(t *testing.T, process robotBoundaryProcessResult) failureEnvelope {
		t.Helper()
		if process.exitCode == 0 || len(bytes.TrimSpace(process.stderr)) != 0 {
			t.Fatalf("terminal failure exit=%d stdout=%s stderr=%s", process.exitCode, process.stdout, process.stderr)
		}
		var envelope failureEnvelope
		decodeSingleRobotJSON(t, process.stdout, &envelope)
		if envelope.Success == nil || *envelope.Success || strings.TrimSpace(envelope.Error) == "" {
			t.Fatalf("terminal failure envelope = %+v", envelope)
		}
		return envelope
	}
	runFailure := func(t *testing.T, dir string, env []string, args ...string) failureEnvelope {
		t.Helper()
		return assertOneFailure(t, runBuiltRobotProcess(t, fixture.ntmPath, dir, env, args...))
	}

	t.Run("legacy_json_failure_surfaces_are_one_document", func(t *testing.T) {
		invalidPipeline := filepath.Join(fixture.root, "invalid-pipeline.yaml")
		if err := os.WriteFile(invalidPipeline, []byte("name: invalid\nsteps: [\n"), 0o600); err != nil {
			t.Fatalf("write invalid pipeline: %v", err)
		}
		invalidPolicy := filepath.Join(fixture.root, "invalid-policy.yaml")
		if err := os.WriteFile(invalidPolicy, []byte("rules: [\n"), 0o600); err != nil {
			t.Fatalf("write invalid policy: %v", err)
		}

		cases := []struct {
			name          string
			args          []string
			wantErrorCode string
		}{
			{name: "pipeline_lint", args: []string{"--json", "pipeline", "lint", invalidPipeline}},
			{name: "persona_show", args: []string{"--json", "personas", "show", "missing-terminal-persona"}},
			{name: "checkpoint_restore", args: []string{"--json", "checkpoint", "restore", fixture.session, "missing-terminal-checkpoint", "--dry-run"}},
			{name: "approval", args: []string{"approve", "missing-terminal-approval", "--json"}},
			{name: "session_template_show", args: []string{"--json", "session-templates", "show", "missing-terminal-template"}},
			{name: "workflow_show", args: []string{"--json", "workflows", "show", "missing-terminal-workflow"}},
			{name: "recipe_show", args: []string{"--json", "recipes", "show", "missing-terminal-recipe"}},
			{name: "hooks_status", args: []string{"--json", "hooks", "status"}},
			{name: "policy_validate", args: []string{"--json", "policy", "validate", invalidPolicy}},
			{name: "health_missing_session", args: []string{"--json", "health", fixture.session + "-missing"}},
			{name: "codex_palette_state", args: []string{"--json", "codex", "palette-state"}, wantErrorCode: "INVALID_FLAG"},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				envelope := runFailure(t, fixture.projectDir, fixture.env, test.args...)
				if test.wantErrorCode != "" && envelope.ErrorCode != test.wantErrorCode {
					t.Fatalf("error_code=%q, want %q; envelope=%+v", envelope.ErrorCode, test.wantErrorCode, envelope)
				}
			})
		}
	})

	t.Run("strict_preflight_failure_is_one_document", func(t *testing.T) {
		runFailure(t, fixture.projectDir, fixture.env,
			"preflight", "Run rm -rf /tmp/important to clean cache", "--strict", "--json",
		)
	})

	t.Run("config_validation_failure_is_one_document", func(t *testing.T) {
		projectDir := filepath.Join(fixture.root, "invalid-config-project")
		ntmDir := filepath.Join(projectDir, ".ntm")
		if err := os.MkdirAll(ntmDir, 0o700); err != nil {
			t.Fatalf("create invalid config project: %v", err)
		}
		if err := os.WriteFile(filepath.Join(ntmDir, "config.toml"), []byte("[invalid\n"), 0o600); err != nil {
			t.Fatalf("write invalid project config: %v", err)
		}
		runFailure(t, projectDir, fixture.env, "--json", "config", "validate")
	})

	t.Run("checkpoint_verification_failures_are_one_document", func(t *testing.T) {
		checkpointID := "terminal-invalid-checkpoint"
		checkpointDir := filepath.Join(
			fixture.root, "home", ".local", "share", "ntm", "checkpoints", fixture.session, checkpointID,
		)
		if err := os.MkdirAll(checkpointDir, 0o700); err != nil {
			t.Fatalf("create invalid checkpoint: %v", err)
		}
		if err := os.WriteFile(filepath.Join(checkpointDir, "metadata.json"), []byte("{\n"), 0o600); err != nil {
			t.Fatalf("write invalid checkpoint metadata: %v", err)
		}
		runFailure(t, fixture.projectDir, fixture.env,
			"--json", "checkpoint", "verify", fixture.session, checkpointID,
		)
		runFailure(t, fixture.projectDir, fixture.env,
			"--json", "checkpoint", "verify", fixture.session, "--all",
		)
	})

	t.Run("missing_optional_tools_are_terminal_failures", func(t *testing.T) {
		emptyPath := filepath.Join(fixture.root, "empty-path")
		if err := os.MkdirAll(emptyPath, 0o700); err != nil {
			t.Fatalf("create empty PATH: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{"PATH": emptyPath})
		for _, test := range []struct {
			name          string
			args          []string
			wantErrorCode string
		}{
			{name: "deps_without_tmux", args: []string{"--json", "deps"}, wantErrorCode: "DEPENDENCY_MISSING"},
			{name: "scan_without_ubs", args: []string{"--json", "scan", fixture.projectDir}, wantErrorCode: "DEPENDENCY_MISSING"},
			{name: "bugs_without_ubs", args: []string{"--json", "bugs", "list", fixture.projectDir}, wantErrorCode: "DEPENDENCY_MISSING"},
			{name: "cass_status_without_cass", args: []string{"--json", "cass", "status"}, wantErrorCode: "NOT_IMPLEMENTED"},
		} {
			t.Run(test.name, func(t *testing.T) {
				process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, env, test.args...)
				envelope := assertOneFailure(t, process)
				if envelope.ErrorCode != test.wantErrorCode {
					t.Fatalf("error_code=%q, want %q; envelope=%+v", envelope.ErrorCode, test.wantErrorCode, envelope)
				}
				if test.name == "cass_status_without_cass" && process.exitCode != 2 {
					t.Fatalf("CASS unavailable exit=%d, want 2", process.exitCode)
				}
			})
		}
	})

	t.Run("scan_severity_matches_process_status", func(t *testing.T) {
		fakeBin := filepath.Join(fixture.root, "fake-ubs-bin")
		if err := os.MkdirAll(fakeBin, 0o700); err != nil {
			t.Fatalf("create fake UBS bin: %v", err)
		}
		ubsPath := filepath.Join(fakeBin, "ubs")
		writeUBS := func(t *testing.T, critical, warning int) {
			t.Helper()
			payload := fmt.Sprintf(`{"project":%q,"timestamp":"2026-07-13T00:00:00Z","scanners":[],"totals":{"critical":%d,"warning":%d,"info":0,"files":1},"findings":[],"exit_code":0}`+"\n", fixture.projectDir, critical, warning)
			script := "#!/bin/sh\nprintf '%s\\n' '" + strings.TrimSpace(payload) + "'\n"
			if err := os.WriteFile(ubsPath, []byte(script), 0o700); err != nil {
				t.Fatalf("write fake UBS: %v", err)
			}
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"PATH": fakeBin + string(os.PathListSeparator) + "/usr/bin:/bin",
		})

		writeUBS(t, 0, 1)
		assertOneFailure(t, runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, env,
			"--json", "scan", fixture.projectDir, "--fail-on-warning",
		))

		writeUBS(t, 1, 0)
		assertOneFailure(t, runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, env,
			"--json", "scan", fixture.projectDir,
		))

		writeUBS(t, 0, 0)
		process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, env,
			"--json", "scan", fixture.projectDir,
		)
		if process.exitCode != 0 || len(bytes.TrimSpace(process.stderr)) != 0 {
			t.Fatalf("clean scan exit=%d stdout=%s stderr=%s", process.exitCode, process.stdout, process.stderr)
		}
		var clean map[string]any
		decodeSingleRobotJSON(t, process.stdout, &clean)

		findingPayload := fmt.Sprintf(`{"project":%q,"timestamp":"2026-07-13T00:00:00Z","scanners":[],"totals":{"critical":0,"warning":1,"info":0,"files":1},"findings":[{"file":"terminal.go","line":1,"column":1,"severity":"warning","category":"test","message":"fixture finding","rule_id":"terminal-fixture"}],"exit_code":0}`+"\n", fixture.projectDir)
		findingScript := "#!/bin/sh\nprintf '%s\\n' '" + strings.TrimSpace(findingPayload) + "'\n"
		if err := os.WriteFile(ubsPath, []byte(findingScript), 0o700); err != nil {
			t.Fatalf("write integration-failure UBS: %v", err)
		}
		assertOneFailure(t, runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, env,
			"--json", "scan", fixture.projectDir, "--create-beads",
		))
	})

	t.Run("git_sync_failures_are_terminal", func(t *testing.T) {
		runFailure(t, fixture.projectDir, fixture.env, "--json", "git", "sync")

		repoDir := filepath.Join(fixture.root, "repo-without-upstream")
		if err := os.MkdirAll(repoDir, 0o700); err != nil {
			t.Fatalf("create git fixture: %v", err)
		}
		initCmd := exec.Command("git", "init", "-b", "main")
		initCmd.Dir = repoDir
		if output, err := initCmd.CombinedOutput(); err != nil {
			t.Fatalf("init git fixture: %v: %s", err, output)
		}
		commitCmd := exec.Command("git", "-c", "user.name=NTM E2E", "-c", "user.email=ntm-e2e@example.invalid", "commit", "--allow-empty", "-m", "fixture")
		commitCmd.Dir = repoDir
		if output, err := commitCmd.CombinedOutput(); err != nil {
			t.Fatalf("commit git fixture: %v: %s", err, output)
		}
		runFailure(t, repoDir, fixture.env, "--json", "git", "sync")
	})

	t.Run("audit_verify_corruption_is_one_aggregate_document", func(t *testing.T) {
		session := "terminal-audit"
		auditDir := filepath.Join(fixture.root, "home", ".local", "share", "ntm", "audit")
		if err := os.MkdirAll(auditDir, 0o700); err != nil {
			t.Fatalf("create audit directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(auditDir, session+"-2026-07-13.jsonl"), []byte("not-json\n"), 0o600); err != nil {
			t.Fatalf("write corrupt audit log: %v", err)
		}
		runFailure(t, fixture.projectDir, fixture.env, "--json", "audit", "verify", session)
	})

	t.Run("local_format_json_is_machine_terminal", func(t *testing.T) {
		for _, args := range [][]string{
			{"ensemble", "compare", "missing-run-a", "missing-run-b", "--format=json"},
			{"ensemble", "compare", "missing-run-a", "missing-run-b", "--format", "json"},
			{"ensemble", "compare", "missing-run-a", "missing-run-b", "-f=json"},
			{"ensemble", "compare", "missing-run-a", "missing-run-b", "-fjson"},
			{"ensemble", "compare", "missing-run-a", "missing-run-b", "-f", "json"},
		} {
			runFailure(t, fixture.projectDir, fixture.env, args...)
		}

		configDir := filepath.Join(fixture.root, "presets-config")
		if err := os.MkdirAll(filepath.Join(configDir, "ntm"), 0o700); err != nil {
			t.Fatalf("create presets config directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "ntm", "ensembles.toml"), []byte("presets = [\n"), 0o600); err != nil {
			t.Fatalf("write malformed ensemble registry: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{"XDG_CONFIG_HOME": configDir})
		runFailure(t, fixture.projectDir, env, "ensemble", "presets", "--format=json")
	})

	t.Run("documented_local_json_spellings_are_machine_terminal", func(t *testing.T) {
		for _, test := range []struct {
			args        []string
			wantNonzero bool
		}{
			{args: []string{"--profile-startup", "worktree", "list", "--output=json"}, wantNonzero: true},
			{args: []string{"--profile-startup", "worktree", "list", "--output", "json"}, wantNonzero: true},
			{args: []string{"--profile-startup", "worktree", "list", "-o=json"}, wantNonzero: true},
			{args: []string{"--profile-startup", "worktree", "list", "-ojson"}, wantNonzero: true},
			{args: []string{"--profile-startup", "ensemble", "compare", "missing-run-a", "missing-run-b", "-fjson"}, wantNonzero: true},
			// commit-ready is intentionally advisory: an unsafe report remains a
			// successful process result, but it must still be one clean JSON document.
			{args: []string{"--profile-startup", "work", "commit-readiness", "--format=json"}},
			{args: []string{"--profile-startup", "work", "commit-readiness", "--format", "json"}},
		} {
			process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, fixture.env, test.args...)
			if len(bytes.TrimSpace(process.stderr)) != 0 {
				t.Fatalf("machine invocation exit=%d stderr=%q, want empty stderr", process.exitCode, process.stderr)
			}
			if test.wantNonzero && process.exitCode == 0 {
				t.Fatalf("machine failure args=%q exited zero: %s", test.args, process.stdout)
			}
			var document any
			decodeSingleRobotJSON(t, process.stdout, &document)
		}
	})

	t.Run("default_json_commands_suppress_startup_profile", func(t *testing.T) {
		for _, test := range []struct {
			name string
			args []string
		}{
			{name: "audit_export", args: []string{"--profile-startup", "audit", "export", fixture.session}},
			{name: "metrics_export", args: []string{"--profile-startup", "metrics", "export"}},
			{name: "work_graph", args: []string{"--profile-startup", "work", "graph"}},
		} {
			t.Run(test.name, func(t *testing.T) {
				process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, fixture.env, test.args...)
				if len(bytes.TrimSpace(process.stderr)) != 0 {
					t.Fatalf("default JSON command exit=%d stderr=%q, want empty machine stderr", process.exitCode, process.stderr)
				}
				var document any
				decodeSingleRobotJSON(t, process.stdout, &document)
			})
		}
	})

	t.Run("profile_parse_error_is_one_document", func(t *testing.T) {
		process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, fixture.env,
			"profiles", "switch", "not-an-agent-id", "reviewer", "--json",
		)
		assertOneFailure(t, process)
	})

	failingTMUX := filepath.Join(fixture.root, "tmux-terminal-failure")
	failingScript := `#!/bin/sh
if [ "${1:-}" = "-V" ]; then
    printf 'tmux 3.4\n'
    exit 0
fi
exit 64
`
	if err := os.WriteFile(failingTMUX, []byte(failingScript), 0o700); err != nil {
		t.Fatalf("write failing tmux wrapper: %v", err)
	}
	failingEnv := mergeRobotProcessEnv(fixture.env, map[string]string{"NTM_TMUX_BINARY": failingTMUX})
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "session_list_failure_is_one_document", args: []string{"list", "--json"}},
		{name: "session_status_failure_is_one_document", args: []string{"status", fixture.session, "--json"}},
		{name: "session_create_failure_exits_nonzero", args: []string{"create", "terminal-create-failure", "--panes=1", "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, failingEnv, test.args...)
			assertOneFailure(t, process)
		})
	}

	t.Run("profile_cancellation_is_one_document", func(t *testing.T) {
		blockingTMUX := filepath.Join(fixture.root, "tmux-profile-cancel")
		startedPath := filepath.Join(fixture.root, "profile-cancel-started")
		blockingScript := `#!/bin/sh
if [ "${1:-}" = "-V" ]; then
    printf 'tmux 3.4\n'
    exit 0
fi
printf 'started\n' > "$NTM_E2E_PROFILE_CANCEL_STARTED"
exec sleep 30
`
		if err := os.WriteFile(blockingTMUX, []byte(blockingScript), 0o700); err != nil {
			t.Fatalf("write blocking profile tmux wrapper: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY":                blockingTMUX,
			"NTM_E2E_PROFILE_CANCEL_STARTED": startedPath,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, fixture.ntmPath,
			"profiles", "switch", "cod_1", "reviewer", "--session="+fixture.session, "--json",
		)
		cmd.Dir = fixture.projectDir
		cmd.Env = append([]string(nil), env...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start cancelable profile switch: %v", err)
		}
		waitCh := make(chan error, 1)
		go func() { waitCh <- cmd.Wait() }()
		deadline := time.Now().Add(20 * time.Second)
		for {
			if data, err := os.ReadFile(startedPath); err == nil && strings.Contains(string(data), "started") {
				break
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				cancel()
				<-waitCh
				t.Fatalf("read profile cancellation marker: %v", err)
			}
			select {
			case earlyErr := <-waitCh:
				t.Fatalf("profile switch exited before cancellation: %v stdout=%s stderr=%s", earlyErr, stdout.String(), stderr.String())
			default:
			}
			if time.Now().After(deadline) {
				cancel()
				<-waitCh
				t.Fatalf("timed out waiting for profile switch dependency: stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
			time.Sleep(25 * time.Millisecond)
		}
		if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
			cancel()
			<-waitCh
			t.Fatalf("signal profile switch: %v", err)
		}
		var waitErr error
		select {
		case waitErr = <-waitCh:
		case <-ctx.Done():
			waitErr = <-waitCh
		}
		if ctx.Err() != nil || cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			t.Fatalf("profile switch did not join after SIGINT: ctx=%v state=%v stdout=%s stderr=%s", ctx.Err(), cmd.ProcessState, stdout.String(), stderr.String())
		}
		exitCode := 0
		if waitErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) {
				t.Fatalf("wait for canceled profile switch: %v", waitErr)
			}
			exitCode = exitErr.ExitCode()
		}
		assertOneFailure(t, robotBoundaryProcessResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode})
	})
}

func TestE2ERobotSpawnTerminalContractsBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "spawn-terminal")

	type assignmentDiagnostic struct {
		Pane       string `json:"pane"`
		ClaimError string `json:"claim_error"`
	}
	type agentDiagnostic struct {
		Pane  string `json:"pane"`
		Type  string `json:"type"`
		Error string `json:"error"`
	}
	type spawnEnvelope struct {
		Success      bool                   `json:"success"`
		Timestamp    string                 `json:"timestamp"`
		OutputFormat string                 `json:"output_format"`
		Error        string                 `json:"error"`
		ErrorCode    string                 `json:"error_code"`
		Agents       []agentDiagnostic      `json:"agents"`
		Assignments  []assignmentDiagnostic `json:"assignments"`
	}
	assertSpawnFailure := func(t *testing.T, process robotBoundaryProcessResult, wantCode string) spawnEnvelope {
		t.Helper()
		if process.exitCode != 1 {
			t.Fatalf("exit=%d, want 1; stdout=%s stderr=%s", process.exitCode, process.stdout, process.stderr)
		}
		if len(process.stderr) != 0 {
			t.Fatalf("stderr=%q, want empty canonical robot boundary", process.stderr)
		}
		var envelope spawnEnvelope
		decodeSingleRobotJSON(t, process.stdout, &envelope)
		if envelope.Success || envelope.ErrorCode != wantCode || envelope.Error == "" || envelope.OutputFormat != "json" {
			t.Fatalf("spawn envelope=%+v, want %s canonical JSON failure", envelope, wantCode)
		}
		if _, err := time.Parse(time.RFC3339, envelope.Timestamp); err != nil {
			t.Fatalf("terminal timestamp %q is not RFC3339: %v", envelope.Timestamp, err)
		}
		return envelope
	}
	run := func(t *testing.T, env []string, args ...string) robotBoundaryProcessResult {
		t.Helper()
		return runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, env, args...)
	}
	writeConfig := func(t *testing.T, name, command string) string {
		t.Helper()
		path := filepath.Join(fixture.root, name+".toml")
		contents := fmt.Sprintf("[agents]\nclaude = %q\n\n[spawn_pacing]\nenabled = false\n", command)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write spawn config: %v", err)
		}
		return path
	}
	writeLaunchMarkerConfig := func(t *testing.T, name string) (string, string) {
		t.Helper()
		markerPath := filepath.Join(fixture.root, name+"-launched")
		agentPath := filepath.Join(fixture.root, name+"-agent")
		agentScript := fmt.Sprintf("#!/bin/sh\nprintf 'launched\\n' > %q\nsleep 30\n", markerPath)
		if err := os.WriteFile(agentPath, []byte(agentScript), 0o700); err != nil {
			t.Fatalf("write launch-marker agent: %v", err)
		}
		return writeConfig(t, name, agentPath), markerPath
	}
	assertLaunchMarkerAbsent := func(t *testing.T, markerPath string) {
		t.Helper()
		if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("agent launch marker %s exists or is unreadable: %v", markerPath, err)
		}
	}
	assertSessionAbsent := func(t *testing.T, session string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, fixture.tmuxPath, "has-session", "-t", session)
		cmd.Env = append([]string(nil), fixture.env...)
		output, err := cmd.CombinedOutput()
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("check absent session %q timed out after %s: output=%s", session, defaultTmuxSetupTimeout, output)
		}
		if err == nil {
			t.Fatalf("session %q exists after assignment preflight rejection", session)
		}
		if !isBenignTmuxCleanupError(output) {
			t.Fatalf("check absent session %q: %v output=%s", session, err, output)
		}
	}
	physicalPaneCount := func(t *testing.T, session string) int {
		t.Helper()
		output := strings.TrimSpace(fixture.mustTMUXOutput(t,
			"list-panes", "-s", "-t", session, "-F", "#{pane_id}",
		))
		if output == "" {
			return 0
		}
		return len(strings.Split(output, "\n"))
	}
	callLogCount := func(t *testing.T, path string) int {
		t.Helper()
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		if err != nil {
			t.Fatalf("read tmux call log %s: %v", path, err)
		}
		return len(bytes.Fields(data))
	}

	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "negative count",
			args: []string{
				"--robot-spawn=invalid-negative", "--spawn-cc=-1", "--spawn-cod=1", "--spawn-no-user", "--robot-format=json",
			},
		},
		{
			name: "zero agents",
			args: []string{
				"--robot-spawn=invalid-zero", "--spawn-no-user", "--robot-format=json",
			},
		},
		{
			name: "empty assignment strategy",
			args: []string{
				"--robot-spawn=invalid-strategy", "--spawn-cc=1", "--spawn-no-user", "--spawn-assign-work", "--strategy=", "--robot-format=json",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			envelope := assertSpawnFailure(t, run(t, fixture.env, test.args...), "INVALID_FLAG")
			if envelope.Agents == nil || len(envelope.Agents) != 0 {
				t.Fatalf("agents=%v, want initialized empty array", envelope.Agents)
			}
		})
	}

	t.Run("session probe infrastructure failure prevents creation", func(t *testing.T) {
		session := fixture.session + "-probe-permission"
		configPath := writeConfig(t, "probe-permission", "sleep 30")
		createMarker := filepath.Join(fixture.root, "probe-permission-created")
		wrapperPath := filepath.Join(fixture.root, "tmux-probe-permission")
		wrapper := `#!/bin/sh
if [ "${1:-}" = "has-session" ]; then
    printf '%s\n' 'error connecting to /tmp/tmux-1000/default (Permission denied)' >&2
    exit 1
fi
if [ "${1:-}" = "new-session" ]; then
    : > "$NTM_E2E_CREATE_MARKER"
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
		if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
			t.Fatalf("write session-probe tmux wrapper: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY":       wrapperPath,
			"NTM_E2E_REAL_TMUX":     fixture.tmuxPath,
			"NTM_E2E_CREATE_MARKER": createMarker,
		})
		envelope := assertSpawnFailure(t, run(t, env,
			"--config="+configPath,
			"--robot-spawn="+session,
			"--spawn-cc=1",
			"--spawn-no-user",
			"--spawn-dir="+fixture.projectDir,
			"--robot-format=json",
		), "INTERNAL_ERROR")
		if !strings.Contains(strings.ToLower(envelope.Error), "permission denied") {
			t.Fatalf("session-probe error=%q, want permission diagnostic", envelope.Error)
		}
		if _, err := os.Stat(createMarker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("session creation marker exists or is unreadable after failed probe: %v", err)
		}
	})

	t.Run("short topology fails before layout or launch", func(t *testing.T) {
		session := fixture.session + "-short-topology"
		configPath, launchMarker := writeLaunchMarkerConfig(t, "short-topology")
		listCountPath := filepath.Join(fixture.root, "short-topology-list-count")
		layoutLogPath := filepath.Join(fixture.root, "short-topology-layout-log")
		wrapperPath := filepath.Join(fixture.root, "tmux-short-topology")
		wrapper := `#!/bin/sh
target=
previous=
for argument in "$@"; do
    if [ "$previous" = "-t" ]; then
        target=$argument
    fi
    previous=$argument
done

case "$target" in
    "$NTM_E2E_TARGET_SESSION"|"$NTM_E2E_TARGET_SESSION":*)
        if [ "${1:-}" = "select-layout" ]; then
            printf 'layout\n' >> "$NTM_E2E_LAYOUT_LOG"
        fi
        ;;
esac

if [ "${1:-}" = "list-panes" ] && [ "$target" = "$NTM_E2E_TARGET_SESSION" ]; then
    count=0
    if [ -f "$NTM_E2E_LIST_COUNT" ]; then
        read -r count < "$NTM_E2E_LIST_COUNT"
    fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$NTM_E2E_LIST_COUNT"
    if [ "$count" -ge 2 ]; then
        output=$("$NTM_E2E_REAL_TMUX" "$@") || exit $?
        printf '%s\n' "$output" | sed -n '1p'
        exit 0
    fi
fi

exec "$NTM_E2E_REAL_TMUX" "$@"
`
		if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
			t.Fatalf("write short-topology tmux wrapper: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY":        wrapperPath,
			"NTM_E2E_REAL_TMUX":      fixture.tmuxPath,
			"NTM_E2E_TARGET_SESSION": session,
			"NTM_E2E_LIST_COUNT":     listCountPath,
			"NTM_E2E_LAYOUT_LOG":     layoutLogPath,
		})
		envelope := assertSpawnFailure(t, run(t, env,
			"--config="+configPath,
			"--robot-spawn="+session,
			"--spawn-cc=2",
			"--spawn-no-user",
			"--spawn-dir="+fixture.projectDir,
			"--robot-format=json",
		), "PANE_NOT_FOUND")
		if !strings.Contains(envelope.Error, "1 pane") || !strings.Contains(envelope.Error, "2 are required") {
			t.Fatalf("short-topology error=%q, want observed and required pane counts", envelope.Error)
		}
		if envelope.Agents == nil || len(envelope.Agents) != 0 {
			t.Fatalf("agents=%v, want initialized empty array before launch", envelope.Agents)
		}
		if got := physicalPaneCount(t, session); got != 2 {
			t.Fatalf("physical pane count=%d, want two panes retained after the real split", got)
		}
		if got := callLogCount(t, layoutLogPath); got != 1 {
			t.Fatalf("layout calls=%d, want only the split-window layout before topology rejection", got)
		}
		assertLaunchMarkerAbsent(t, launchMarker)
		time.Sleep(500 * time.Millisecond)
		assertLaunchMarkerAbsent(t, launchMarker)
		if got := physicalPaneCount(t, session); got != 2 {
			t.Fatalf("late physical pane count=%d, want stable partial topology of two", got)
		}
		if got := callLogCount(t, layoutLogPath); got != 1 {
			t.Fatalf("late layout calls=%d, want no final layout after topology rejection", got)
		}
	})

	t.Run("layout failure prevents all launches", func(t *testing.T) {
		session := fixture.session + "-layout-failure"
		configPath, launchMarker := writeLaunchMarkerConfig(t, "layout-failure")
		layoutLogPath := filepath.Join(fixture.root, "layout-failure-log")
		wrapperPath := filepath.Join(fixture.root, "tmux-layout-failure")
		wrapper := `#!/bin/sh
target=
previous=
for argument in "$@"; do
    if [ "$previous" = "-t" ]; then
        target=$argument
    fi
    previous=$argument
done

case "$target" in
    "$NTM_E2E_TARGET_SESSION"|"$NTM_E2E_TARGET_SESSION":*)
        if [ "${1:-}" = "select-layout" ]; then
            printf 'layout\n' >> "$NTM_E2E_LAYOUT_LOG"
            printf 'injected robot spawn layout failure\n' >&2
            exit 93
        fi
        ;;
esac

exec "$NTM_E2E_REAL_TMUX" "$@"
`
		if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
			t.Fatalf("write layout-failure tmux wrapper: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY":        wrapperPath,
			"NTM_E2E_REAL_TMUX":      fixture.tmuxPath,
			"NTM_E2E_TARGET_SESSION": session,
			"NTM_E2E_LAYOUT_LOG":     layoutLogPath,
		})
		envelope := assertSpawnFailure(t, run(t, env,
			"--config="+configPath,
			"--robot-spawn="+session,
			"--spawn-cc=1",
			"--spawn-no-user",
			"--spawn-dir="+fixture.projectDir,
			"--robot-format=json",
		), "INTERNAL_ERROR")
		if !strings.Contains(envelope.Error, "applying tiled layout") || !strings.Contains(envelope.Error, "injected robot spawn layout failure") {
			t.Fatalf("layout failure error=%q, want operation and injected cause", envelope.Error)
		}
		if envelope.Agents == nil || len(envelope.Agents) != 0 {
			t.Fatalf("agents=%v, want initialized empty array before launch", envelope.Agents)
		}
		if got := physicalPaneCount(t, session); got != 1 {
			t.Fatalf("physical pane count=%d, want created one-pane session retained", got)
		}
		if got := callLogCount(t, layoutLogPath); got != 1 {
			t.Fatalf("layout calls=%d, want one failed final layout", got)
		}
		assertLaunchMarkerAbsent(t, launchMarker)
		time.Sleep(500 * time.Millisecond)
		assertLaunchMarkerAbsent(t, launchMarker)
		if got := physicalPaneCount(t, session); got != 1 {
			t.Fatalf("late physical pane count=%d, want stable one-pane partial topology", got)
		}
		if got := callLogCount(t, layoutLogPath); got != 1 {
			t.Fatalf("late layout calls=%d, want no retry after terminal failure", got)
		}
	})

	t.Run("launch failure retains per-agent diagnostics", func(t *testing.T) {
		// A TOML multiline command reaches the production command sanitizer and
		// deterministically fails before any agent input is sent to the pane.
		configPath := filepath.Join(fixture.root, "launch-failure.toml")
		configBody := "[agents]\nclaude = \"\"\"bad\ncommand\"\"\"\n\n[spawn_pacing]\nenabled = false\n"
		if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
			t.Fatalf("write launch failure config: %v", err)
		}
		envelope := assertSpawnFailure(t, run(t, fixture.env,
			"--config="+configPath,
			"--robot-spawn="+fixture.session+"-launch",
			"--spawn-cc=1",
			"--spawn-no-user",
			"--spawn-dir="+fixture.projectDir,
			"--robot-format=json",
		), "INTERNAL_ERROR")
		if len(envelope.Agents) != 1 || envelope.Agents[0].Error == "" {
			t.Fatalf("agents=%+v, want one per-agent launch diagnostic", envelope.Agents)
		}
	})

	t.Run("subsecond readiness deadline is TIMEOUT", func(t *testing.T) {
		const processTimeout = 60 * time.Second

		agentDispatchedMarker := filepath.Join(fixture.root, "readiness-agent-dispatched")
		scriptPath := filepath.Join(fixture.root, "agent-block")
		script := "#!/bin/sh\nprintf 'booting\\n'\nsleep 30\n"
		if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
			t.Fatalf("write blocking agent: %v", err)
		}
		captureMarker := filepath.Join(fixture.root, "readiness-capture-started")
		tmuxWrapper := filepath.Join(fixture.root, "tmux-readiness")
		wrapper := `#!/bin/sh
if [ "$1" = "paste-buffer" ] && [ ! -e "$NTM_E2E_AGENT_DISPATCHED" ]; then
  : > "$NTM_E2E_AGENT_DISPATCHED"
fi
if [ "$1" = "capture-pane" ] && [ -e "$NTM_E2E_AGENT_DISPATCHED" ] && [ ! -e "$NTM_E2E_READINESS_CAPTURE" ]; then
  : > "$NTM_E2E_READINESS_CAPTURE"
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
		if err := os.WriteFile(tmuxWrapper, []byte(wrapper), 0o700); err != nil {
			t.Fatalf("write readiness tmux wrapper: %v", err)
		}
		configPath := writeConfig(t, "readiness", scriptPath)
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY":           tmuxWrapper,
			"NTM_E2E_REAL_TMUX":         fixture.tmuxPath,
			"NTM_E2E_AGENT_DISPATCHED":  agentDispatchedMarker,
			"NTM_E2E_READINESS_CAPTURE": captureMarker,
		})
		started := time.Now()
		envelope := assertSpawnFailure(t, runBuiltRobotProcessWithin(t, processTimeout, fixture.ntmPath, fixture.projectDir, env,
			"--config="+configPath,
			"--robot-spawn="+fixture.session+"-wait",
			"--spawn-cc=1",
			"--spawn-no-user",
			"--spawn-dir="+fixture.projectDir,
			"--spawn-wait",
			"--timeout=500ms",
			"--robot-format=json",
		), "TIMEOUT")
		finished := time.Now()
		wallElapsed := finished.Sub(started)
		if !strings.Contains(envelope.Error, "500ms timeout") {
			t.Fatalf("readiness error=%q, want exact subsecond timeout diagnostic", envelope.Error)
		}
		markerInfo, err := os.Stat(captureMarker)
		if err != nil {
			t.Fatalf("stat readiness capture marker: %v", err)
		}
		readinessElapsed := finished.Sub(markerInfo.ModTime())
		if readinessElapsed < 350*time.Millisecond || readinessElapsed > 12*time.Second {
			t.Fatalf("500ms readiness timeout after launch=%s (total process time=%s)", readinessElapsed, wallElapsed)
		}
		if len(envelope.Agents) != 1 {
			t.Fatalf("agents=%+v, want launched agent retained", envelope.Agents)
		}
	})

	t.Run("malformed plan fails closed before lifecycle", func(t *testing.T) {
		fakeBin := filepath.Join(fixture.root, "malformed-plan-bin")
		if err := os.MkdirAll(fakeBin, 0o700); err != nil {
			t.Fatalf("create malformed-plan bin: %v", err)
		}
		bvTrace := filepath.Join(fixture.root, "malformed-plan-bv.trace")
		bvPath := filepath.Join(fakeBin, "bv")
		bvScript := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_E2E_BV_TRACE"
case "${1:-}" in
  --robot-triage) printf '%s\n' '{"generated_at":"2026-07-19T00:00:00Z","data_hash":"spawn-terminal-e2e","triage":{"meta":{"version":"e2e","generated_at":"2026-07-19T00:00:00Z","phase2_ready":true,"issue_count":0},"quick_ref":{"open_count":0,"actionable_count":0,"blocked_count":0,"in_progress_count":0,"top_picks":[]},"recommendations":[]}}' ;;
  --robot-plan) printf '%s\n' '{}' ;;
  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;
esac
`
		if err := os.WriteFile(bvPath, []byte(bvScript), 0o700); err != nil {
			t.Fatalf("write malformed-plan bv: %v", err)
		}
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"PATH":             fakeBin + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH"),
			"NTM_E2E_BV_TRACE": bvTrace,
		})
		configPath, launchMarker := writeLaunchMarkerConfig(t, "malformed-plan")
		session := fixture.session + "-malformed-plan"
		envelope := assertSpawnFailure(t, run(t, env,
			"--config="+configPath,
			"--robot-spawn="+session,
			"--spawn-cc=1",
			"--spawn-no-user",
			"--spawn-dir="+fixture.projectDir,
			"--spawn-assign-work",
			"--strategy=top-n",
			"--robot-format=json",
		), "INTERNAL_ERROR")
		if !strings.Contains(envelope.Error, "missing plan object") {
			t.Fatalf("malformed-plan error=%q, want missing plan object diagnostic", envelope.Error)
		}
		if envelope.Agents == nil || len(envelope.Agents) != 0 {
			t.Fatalf("agents=%v, want initialized empty array before lifecycle actuation", envelope.Agents)
		}
		trace, err := os.ReadFile(bvTrace)
		if err != nil {
			t.Fatalf("read malformed-plan bv trace: %v", err)
		}
		if got := strings.Fields(string(trace)); !slices.Equal(got, []string{"--robot-triage", "--robot-plan"}) {
			t.Fatalf("malformed-plan bv calls=%q, want triage then plan", got)
		}
		assertSessionAbsent(t, session)
		assertLaunchMarkerAbsent(t, launchMarker)
		time.Sleep(500 * time.Millisecond)
		assertSessionAbsent(t, session)
		assertLaunchMarkerAbsent(t, launchMarker)
	})

	t.Run("zero work items covers every agent and fails", func(t *testing.T) {
		fakeBin := filepath.Join(fixture.root, "fake-bin")
		if err := os.MkdirAll(fakeBin, 0o700); err != nil {
			t.Fatalf("create fake bin: %v", err)
		}
		bvPath := filepath.Join(fakeBin, "bv")
		writeSpawnEmptyBV(t, bvPath)
		env := mergeRobotProcessEnv(fixture.env, map[string]string{
			"PATH": fakeBin + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH"),
		})
		configPath := writeConfig(t, "zero-work", "sleep 30")
		envelope := assertSpawnFailure(t, run(t, env,
			"--config="+configPath,
			"--robot-spawn="+fixture.session+"-assign",
			"--spawn-cc=1",
			"--spawn-no-user",
			"--spawn-dir="+fixture.projectDir,
			"--spawn-assign-work",
			"--strategy=top-n",
			"--robot-format=json",
		), "ASSIGNMENT_FAILED")
		if len(envelope.Agents) != 1 || len(envelope.Assignments) != 1 || envelope.Assignments[0].ClaimError == "" {
			t.Fatalf("agents=%+v assignments=%+v, want complete zero-work diagnostics", envelope.Agents, envelope.Assignments)
		}
	})
}

func TestE2ERobotReplayBuiltBinaryRealTmuxDeliversExactlyOnce(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "replay-once")

	readyPath := filepath.Join(fixture.root, "pane-ready")
	readyCommand := fmt.Sprintf("printf ready > %q", readyPath)
	fixture.mustTMUXOutput(t, "send-keys", "-t", fixture.paneID, "-l", readyCommand)
	fixture.mustTMUXOutput(t, "send-keys", "-t", fixture.paneID, "Enter")
	fixture.waitForFileContents(t, readyPath, "ready")

	marker := fmt.Sprintf("NTM_E2E_REPLAY_ONCE_%d", time.Now().UnixNano())
	markerPath := filepath.Join(fixture.root, "replay-markers.txt")
	prompt := fmt.Sprintf("printf '%%s\\n' %q >> %q", marker, markerPath)
	entry := history.HistoryEntry{
		ID:        fmt.Sprintf("%d-e2ereplay", time.Now().UnixMilli()),
		Timestamp: time.Now().UTC(),
		Session:   fixture.session,
		Targets:   []string{fixture.paneID},
		Prompt:    prompt,
		Source:    history.SourceCLI,
		Success:   true,
	}
	historyPayload, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal replay history entry: %v", err)
	}
	historyDir := filepath.Join(fixture.dataDir, "ntm")
	if err := os.MkdirAll(historyDir, 0o700); err != nil {
		t.Fatalf("create replay history directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(historyDir, "history.jsonl"), append(historyPayload, '\n'), 0o600); err != nil {
		t.Fatalf("write replay history: %v", err)
	}

	process := runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, fixture.env,
		"--robot-replay="+fixture.session,
		"--id="+entry.ID,
		"--robot-format=json",
	)
	if process.exitCode != 0 {
		t.Fatalf("robot replay exit=%d, want 0; stdout=%s stderr=%s", process.exitCode, process.stdout, process.stderr)
	}
	if len(bytes.TrimSpace(process.stderr)) != 0 {
		t.Fatalf("robot replay wrote stderr: %s", process.stderr)
	}

	type replaySendResult struct {
		Success    bool              `json:"success"`
		Timestamp  string            `json:"timestamp"`
		Session    string            `json:"session"`
		Targets    []string          `json:"targets"`
		Successful []string          `json:"successful"`
		Failed     []json.RawMessage `json:"failed"`
	}
	var envelope struct {
		Success         bool              `json:"success"`
		Timestamp       string            `json:"timestamp"`
		HistoryID       string            `json:"history_id"`
		OriginalCommand string            `json:"original_command"`
		Session         string            `json:"session"`
		TargetPanes     []string          `json:"target_panes"`
		Replayed        bool              `json:"replayed"`
		SendResult      *replaySendResult `json:"send_result"`
	}
	decodeSingleRobotJSON(t, process.stdout, &envelope)
	if !envelope.Success || !envelope.Replayed || envelope.HistoryID != entry.ID ||
		envelope.OriginalCommand != prompt || envelope.Session != fixture.session {
		t.Fatalf("robot replay envelope identity/state = %+v", envelope)
	}
	if _, err := time.Parse(time.RFC3339Nano, envelope.Timestamp); err != nil {
		t.Fatalf("robot replay timestamp %q is not RFC3339: %v", envelope.Timestamp, err)
	}
	if len(envelope.TargetPanes) != 1 || envelope.TargetPanes[0] != fixture.paneID {
		t.Fatalf("robot replay target_panes=%v, want [%s]", envelope.TargetPanes, fixture.paneID)
	}
	if envelope.SendResult == nil || !envelope.SendResult.Success || envelope.SendResult.Session != fixture.session {
		t.Fatalf("robot replay send_result = %+v", envelope.SendResult)
	}
	if _, err := time.Parse(time.RFC3339Nano, envelope.SendResult.Timestamp); err != nil {
		t.Fatalf("robot replay send timestamp %q is not RFC3339: %v", envelope.SendResult.Timestamp, err)
	}
	if len(envelope.SendResult.Targets) != 1 || envelope.SendResult.Targets[0] != "0" ||
		len(envelope.SendResult.Successful) != 1 || envelope.SendResult.Successful[0] != "0" ||
		envelope.SendResult.Failed == nil || len(envelope.SendResult.Failed) != 0 {
		t.Fatalf("robot replay send delivery arrays = targets:%v successful:%v failed:%v",
			envelope.SendResult.Targets, envelope.SendResult.Successful, envelope.SendResult.Failed)
	}

	fixture.waitForFileContents(t, markerPath, marker)
	// A duplicate send submitted by the replay wrapper would execute immediately.
	time.Sleep(250 * time.Millisecond)
	markers, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read replay marker file: %v", err)
	}
	if count := strings.Count(string(markers), marker); count != 1 {
		t.Fatalf("replay marker count=%d, want exactly 1; contents=%q", count, markers)
	}
}

func TestE2ERobotInterruptFollowUpRedactionBuiltBinaryRealTmux(t *testing.T) {
	CommonE2EPrerequisites(t)
	fixture := newRobotProcessFixture(t, "interrupt-redaction")

	configPath := filepath.Join(fixture.configDir, "ntm", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	writeRedactionMode := func(mode string) {
		t.Helper()
		contents := fmt.Sprintf("[redaction]\nmode = %q\n", mode)
		if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
			t.Fatalf("write %s redaction config: %v", mode, err)
		}
	}
	waitForPaneCommand := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			current := strings.TrimSpace(fixture.mustTMUXOutput(t, "display-message", "-p", "-t", fixture.paneID, "#{pane_current_command}"))
			if current == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("pane command=%q, want %q", current, want)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	waitForPaneOutput := func(want string) string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			capture := fixture.mustTMUXOutput(t, "capture-pane", "-p", "-t", fixture.paneID, "-S", "-200")
			if strings.Contains(capture, want) {
				return capture
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q in pane output: %s", want, capture)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	startInterruptibleCommand := func() {
		t.Helper()
		fixture.mustTMUXOutput(t, "send-keys", "-t", fixture.paneID, "-l", "cat")
		fixture.mustTMUXOutput(t, "send-keys", "-t", fixture.paneID, "Enter")
		waitForPaneCommand("cat")
	}

	type interruptRedaction struct {
		Mode     string `json:"mode"`
		Findings int    `json:"findings"`
		Action   string `json:"action"`
	}
	type interruptEnvelope struct {
		Success       bool                `json:"success"`
		Timestamp     string              `json:"timestamp"`
		Error         string              `json:"error"`
		ErrorCode     string              `json:"error_code"`
		Session       string              `json:"session"`
		Interrupted   []string            `json:"interrupted"`
		MessageSent   bool                `json:"message_sent"`
		Message       string              `json:"message"`
		Redaction     *interruptRedaction `json:"redaction"`
		ReadyForInput []string            `json:"ready_for_input"`
		Failed        []json.RawMessage   `json:"failed"`
	}
	secret := strings.TrimPrefix(fakePassword, "password=")
	followUp := "Continue using " + fakePassword
	runInterrupt := func(message string) robotBoundaryProcessResult {
		t.Helper()
		return runBuiltRobotProcess(t, fixture.ntmPath, fixture.projectDir, fixture.env,
			"--robot-interrupt="+fixture.session,
			"--panes="+fixture.paneID,
			"--msg="+message,
			"--force",
			"--no-wait",
			"--robot-format=json",
		)
	}

	t.Run("block_prevents_interrupt_and_message", func(t *testing.T) {
		writeRedactionMode("block")
		startInterruptibleCommand()
		process := runInterrupt(followUp)
		if process.exitCode != 1 || len(bytes.TrimSpace(process.stderr)) != 0 {
			t.Fatalf("blocked interrupt exit=%d stderr=%q stdout=%s", process.exitCode, process.stderr, process.stdout)
		}
		if bytes.Contains(process.stdout, []byte(secret)) {
			t.Fatalf("blocked interrupt leaked secret in stdout: %s", process.stdout)
		}
		var envelope interruptEnvelope
		decodeSingleRobotJSON(t, process.stdout, &envelope)
		if envelope.Success || envelope.ErrorCode != "SENSITIVE_DATA_BLOCKED" || envelope.Session != fixture.session ||
			envelope.MessageSent || envelope.Redaction == nil || envelope.Redaction.Action != "block" || envelope.Redaction.Findings == 0 {
			t.Fatalf("blocked interrupt envelope = %+v", envelope)
		}
		if envelope.Interrupted == nil || len(envelope.Interrupted) != 0 ||
			envelope.ReadyForInput == nil || len(envelope.ReadyForInput) != 0 ||
			envelope.Failed == nil || len(envelope.Failed) == 0 {
			t.Fatalf("blocked interrupt result arrays = interrupted:%v ready:%v failed:%v",
				envelope.Interrupted, envelope.ReadyForInput, envelope.Failed)
		}
		if _, err := time.Parse(time.RFC3339Nano, envelope.Timestamp); err != nil {
			t.Fatalf("blocked interrupt timestamp %q is not RFC3339: %v", envelope.Timestamp, err)
		}
		time.Sleep(250 * time.Millisecond)
		waitForPaneCommand("cat")
		capture := fixture.mustTMUXOutput(t, "capture-pane", "-p", "-t", fixture.paneID, "-S", "-200")
		if strings.Contains(capture, secret) || strings.Contains(capture, "[REDACTED:PASSWORD:") {
			t.Fatalf("blocked interrupt typed secret into pane: %s", capture)
		}

		// End the deliberately long-running setup command before the redact case.
		fixture.mustTMUXOutput(t, "send-keys", "-t", fixture.paneID, "C-c")
		resetPath := filepath.Join(fixture.root, "block-reset-ready")
		fixture.mustTMUXOutput(t, "send-keys", "-t", fixture.paneID, "-l", fmt.Sprintf("printf ready > %s", resetPath))
		fixture.mustTMUXOutput(t, "send-keys", "-t", fixture.paneID, "Enter")
		fixture.waitForFileContents(t, resetPath, "ready")
	})

	t.Run("redact_delivers_placeholder_without_secret", func(t *testing.T) {
		writeRedactionMode("redact")
		startInterruptibleCommand()
		process := runInterrupt(followUp)
		if process.exitCode != 0 || len(bytes.TrimSpace(process.stderr)) != 0 {
			t.Fatalf("redacted interrupt exit=%d stderr=%q stdout=%s", process.exitCode, process.stderr, process.stdout)
		}
		if bytes.Contains(process.stdout, []byte(secret)) {
			t.Fatalf("redacted interrupt leaked secret in stdout: %s", process.stdout)
		}
		var envelope interruptEnvelope
		decodeSingleRobotJSON(t, process.stdout, &envelope)
		if !envelope.Success || envelope.ErrorCode != "" || envelope.Session != fixture.session ||
			!envelope.MessageSent || envelope.Redaction == nil || envelope.Redaction.Action != "redact" || envelope.Redaction.Findings == 0 ||
			!strings.Contains(envelope.Message, "[REDACTED:PASSWORD:") || strings.Contains(envelope.Message, secret) {
			t.Fatalf("redacted interrupt envelope = %+v", envelope)
		}
		if len(envelope.Interrupted) != 1 || len(envelope.ReadyForInput) != 1 ||
			envelope.Failed == nil || len(envelope.Failed) != 0 {
			t.Fatalf("redacted interrupt result arrays = interrupted:%v ready:%v failed:%v",
				envelope.Interrupted, envelope.ReadyForInput, envelope.Failed)
		}
		capture := waitForPaneOutput("[REDACTED:PASSWORD:")
		if strings.Contains(capture, secret) || !strings.Contains(capture, "[REDACTED:PASSWORD:") {
			t.Fatalf("redacted interrupt pane output = %s", capture)
		}
	})
}

func supportsRobotFormat(t *testing.T) bool {
	t.Helper()
	cmd := exec.Command("ntm", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), "--robot-format")
}
