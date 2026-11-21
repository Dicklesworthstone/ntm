package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

type contentionProcess struct {
	pid       int
	cwd       string
	command   string
	age       time.Duration
	status    string
	openFiles []string
}

func classifyContentionProcess(proc contentionProcess) []string {
	return robot.ClassifyContentionProcess(robot.DoctorContentionProcess{
		PID:       proc.pid,
		CWD:       proc.cwd,
		Command:   proc.command,
		Age:       proc.age,
		Status:    proc.status,
		OpenFiles: proc.openFiles,
	})
}

func buildSafetyDefaults(cfg *config.Config) SafetyDefaults {
	return robot.BuildSafetyDefaults(cfg)
}

func sameOrChildPath(candidate, root string) bool {
	return robot.SameOrChildPath(candidate, root)
}

func commandHasExecutable(command, executable string) bool {
	return robot.CommandHasExecutable(command, executable)
}

func agentMailDaemonAddr() (string, int) {
	return robot.AgentMailDaemonAddr()
}

func collectVerboseToolDetails(ctx context.Context, adapter tools.Adapter) []string {
	return robot.CollectVerboseToolDetails(ctx, adapter)
}

func daemonVerdict(running, expected bool, port int) (string, string) {
	return robot.DaemonVerdict(running, expected, port)
}

func checkDependencies(ctx context.Context) []DepCheck {
	return robot.CheckDoctorDependencies(ctx)
}

func deadKnobDoctorMessage(knob config.RemovedKnob) string {
	return robot.DeadKnobDoctorMessage(knob)
}

func guardDegradationCheck() ConfigCheck {
	_, check := robot.CheckDoctorGuardState()
	return check
}

func guardHookPathCheck() ConfigCheck {
	return robot.CheckDoctorGuardHookPath()
}

func TestBuildSafetyDefaults(t *testing.T) {
	cfg := config.Default()
	cfg.Redaction.Mode = "redact"
	cfg.Redaction.Allowlist = []string{"safe-token", "test-key"}
	cfg.Privacy.Enabled = true
	cfg.Preflight.Strict = true

	got := buildSafetyDefaults(cfg)

	if got.RedactionMode != "redact" {
		t.Fatalf("RedactionMode=%q, want %q", got.RedactionMode, "redact")
	}
	if !got.RedactionAllowlistEnabled {
		t.Fatal("RedactionAllowlistEnabled=false, want true")
	}
	if got.RedactionAllowlistCount != 2 {
		t.Fatalf("RedactionAllowlistCount=%d, want 2", got.RedactionAllowlistCount)
	}
	if !got.PrivacyDefaultEnabled {
		t.Fatal("PrivacyDefaultEnabled=false, want true")
	}
	if got.EncryptionAtRestEnabled {
		t.Fatal("EncryptionAtRestEnabled=true, want false")
	}
	if !got.PreflightDefaultStrict {
		t.Fatal("PreflightDefaultStrict=false, want true")
	}
}

// Doctor says whether the Claude agents ntm launches are covered by the safety
// policy hook, and why not when they are not.
func TestBuildSafetyDefaultsClaudeAgentHookCoverage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	newCfg := func() *config.Config {
		cfg := config.Default()
		cfg.Integrations.DCG.Enabled = false
		cfg.Integrations.RCH.Enabled = false
		return cfg
	}

	got := buildSafetyDefaults(newCfg())
	if len(got.ClaudeAgentHooks) != 1 || got.ClaudeAgentHooks[0] != "ntm-policy" || got.ClaudeAgentHooksWarning != "" {
		t.Fatalf("default config: hooks=%v warning=%q, want [ntm-policy] and no warning", got.ClaudeAgentHooks, got.ClaudeAgentHooksWarning)
	}

	piped := newCfg()
	piped.Agents.Claude = "claude --verbose | tee /tmp/claude.log"
	got = buildSafetyDefaults(piped)
	if len(got.ClaudeAgentHooks) != 0 || !strings.Contains(got.ClaudeAgentHooksWarning, "cannot carry --settings") {
		t.Fatalf("piped command: hooks=%v warning=%q, want none and a --settings warning", got.ClaudeAgentHooks, got.ClaudeAgentHooksWarning)
	}

	off := newCfg()
	off.Safety.ClaudePolicyHook = false
	got = buildSafetyDefaults(off)
	if !strings.Contains(got.ClaudeAgentHooksWarning, "claude_policy_hook = false") {
		t.Fatalf("policy hook off: warning=%q", got.ClaudeAgentHooksWarning)
	}

	// `ntm safety install` covers every Claude session, so the knob being off
	// (or a command that cannot carry --settings) is no longer a gap.
	script := policy.ClaudeHookScriptPath(home)
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(policy.ClaudeHookScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.RegisterClaudeHook(policy.ClaudeUserSettingsPath(home), script); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]*config.Config{"knob off": off, "piped": piped} {
		got = buildSafetyDefaults(cfg)
		if !got.ClaudePolicyHookRegistered || got.ClaudeAgentHooksWarning != "" {
			t.Fatalf("%s with registered hook: registered=%v warning=%q", name, got.ClaudePolicyHookRegistered, got.ClaudeAgentHooksWarning)
		}
	}
}

// TestBuildSafetyDefaults_NilConfig tests the nil cfg branch (line 459-461).
func TestBuildSafetyDefaults_NilConfig(t *testing.T) {

	got := buildSafetyDefaults(nil)
	// nil config falls back to config.Default(), which has a non-empty Mode
	if got.RedactionMode == "" {
		t.Error("RedactionMode should be non-empty with nil config (uses default)")
	}
}

// TestBuildSafetyDefaults_EmptyRedactionMode tests the empty mode branch (line 464-466).
func TestBuildSafetyDefaults_EmptyRedactionMode(t *testing.T) {

	cfg := config.Default()
	cfg.Redaction.Mode = "" // Empty mode should fall back to default

	got := buildSafetyDefaults(cfg)
	if got.RedactionMode == "" {
		t.Error("RedactionMode should not be empty when Mode is empty string")
	}
	// Should use the default mode
	defaultMode := config.DefaultRedactionConfig().Mode
	if got.RedactionMode != defaultMode {
		t.Errorf("RedactionMode = %q, want default %q", got.RedactionMode, defaultMode)
	}
}

func TestEncodeDoctorJSONIncludesSafetyDefaults(t *testing.T) {
	report := &DoctorReport{
		Timestamp: time.Date(2026, 2, 4, 0, 0, 0, 0, time.UTC),
		Overall:   "healthy",
		SafetyDefaults: SafetyDefaults{
			RedactionMode:             "warn",
			RedactionAllowlistEnabled: true,
			RedactionAllowlistCount:   1,
			PrivacyDefaultEnabled:     false,
			EncryptionAtRestEnabled:   false,
			PreflightDefaultStrict:    false,
		},
		Tools:         []ToolCheck{},
		Dependencies:  []DepCheck{},
		Daemons:       []DaemonCheck{},
		Configuration: []ConfigCheck{},
		Invariants:    []InvariantCheck{},
	}

	buf := &bytes.Buffer{}
	if err := encodeDoctorJSON(buf, report); err != nil {
		t.Fatalf("encodeDoctorJSON error: %v", err)
	}

	var decoded DoctorReport
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("json unmarshal error: %v", err)
	}

	if decoded.SafetyDefaults.RedactionMode != "warn" {
		t.Fatalf("decoded RedactionMode=%q, want %q", decoded.SafetyDefaults.RedactionMode, "warn")
	}
}

func TestRenderDoctorTUIIncludesSafetyDefaults(t *testing.T) {
	report := &DoctorReport{
		Timestamp: time.Now(),
		Overall:   "healthy",
		SafetyDefaults: SafetyDefaults{
			RedactionMode: "warn",
		},
	}

	buf := &bytes.Buffer{}
	if err := renderDoctorTUITo(buf, report); err != nil {
		t.Fatalf("renderDoctorTUITo error: %v", err)
	}

	out := buf.String()
	for _, needle := range []string{
		"Safety Defaults",
		"Redaction mode",
		"Privacy default",
		"Encryption at rest",
		"Prompt preflight",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("expected output to contain %q", needle)
		}
	}
}

func TestClassifyContentionProcess(t *testing.T) {
	resources := classifyContentionProcess(contentionProcess{
		command: "rsync registry",
		status:  "D",
		openFiles: []string{
			"/work/ntm/.beads/beads.db-wal",
		},
	})
	want := []string{"beads_db", "rsync", "uninterruptible_io"}
	if len(resources) != len(want) {
		t.Fatalf("resources=%v, want %v", resources, want)
	}
	for i := range want {
		if resources[i] != want[i] {
			t.Fatalf("resources=%v, want %v", resources, want)
		}
	}

	if got := classifyContentionProcess(contentionProcess{command: "go test ./..."}); len(got) != 0 {
		t.Fatalf("non-contention process classified as %v", got)
	}
	if got := classifyContentionProcess(contentionProcess{command: "/usr/bin/cargo test"}); len(got) != 1 || got[0] != "cargo_registry" {
		t.Fatalf("cargo process classified as %v", got)
	}
}

func TestSameOrChildPath(t *testing.T) {
	if !sameOrChildPath("/work/ntm/subdir", "/work/ntm") {
		t.Fatal("child path should be in swarm")
	}
	if sameOrChildPath("/work/other", "/work/ntm") {
		t.Fatal("sibling path should not be in swarm")
	}
	if sameOrChildPath("", "/work/ntm") {
		t.Fatal("empty path should not be in swarm")
	}
}

func TestCommandHasExecutable(t *testing.T) {
	if !commandHasExecutable("/usr/local/bin/br ready --json", "br") {
		t.Fatal("br executable was not recognized")
	}
	if commandHasExecutable("echo br", "br") {
		t.Fatal("argument must not be treated as the executable")
	}
}

func TestTerminateContentionHolderGuards(t *testing.T) {
	holders := []ContentionHolder{{PID: 123, InSwarm: true}}
	if err := terminateContentionHolder(t.Context(), holders, contentionCommandOptions{terminatePID: 123}); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("missing confirmation error = %v", err)
	}
	if err := terminateContentionHolder(t.Context(), holders, contentionCommandOptions{terminatePID: 123, confirm: true}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("in-swarm refusal error = %v", err)
	}
	if err := terminateContentionHolder(t.Context(), holders, contentionCommandOptions{terminatePID: 999, confirm: true}); err == nil || !strings.Contains(err.Error(), "not a detected") {
		t.Fatalf("unknown PID error = %v", err)
	}
}

func TestAgentMailDaemonAddrUsesAMAdapterServerURL(t *testing.T) {
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:9765/mcp/")
	host, port := agentMailDaemonAddr()
	if host != "127.0.0.1" || port != 9765 {
		t.Fatalf("agentMailDaemonAddr() = (%q, %d), want (\"127.0.0.1\", 9765)", host, port)
	}
}

func TestCollectVerboseToolDetailsAndRenderDoctorVerbose(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable := func(name, body string) {
		t.Helper()
		p := filepath.Join(binDir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}

	writeExecutable("acfs", `
case "$1" in
  info) printf '{"os":"linux"}' ;;
  status) printf '{"phase":"ready"}' ;;
  doctor) printf '{"healthy":true}' ;;
esac
`)
	writeExecutable("caam", `
if [ "$1" = "robot" ] && [ "$2" = "status" ]; then
  printf '{"success":true,"data":{"providers":[{"id":"claude","logged_in":true,"profiles":[{"name":"work","active":true},{"name":"backup","active":false}]}]}}'
  exit 0
fi
printf 'caam 0.2.0\n'
`)
	writeExecutable("cass", `
if [ "$1" = "capabilities" ]; then
  printf '{"lexical":true,"semantic":true}'
  exit 0
fi
printf 'cass 0.1.0\n'
`)
	writeExecutable("cm", `
if [ "$1" = "onboard" ] && [ "$2" = "status" ]; then
  printf '{"onboarded":true}'
  exit 0
fi
printf 'cm 0.1.0\n'
`)
	writeExecutable("dcg", `
if [ "$1" = "doctor" ]; then
  printf '{"enabled":true,"blocked_patterns":["git reset --hard"]}'
  exit 0
fi
printf 'dcg 0.3.0\n'
`)
	writeExecutable("rch", `
if [ "$1" = "--version" ]; then
  printf 'rch 0.2.0\n'
  exit 0
fi
if [ "$1" = "status" ]; then
  printf '{"success":true,"data":{"daemon":{"workers_total":1,"workers_healthy":1,"workers":[{"id":"w1","name":"builder-1","host":"10.0.0.2","status":"healthy","used_slots":1,"total_slots":4}]}}}'
  exit 0
fi
`)
	writeExecutable("ru", `printf 'ru doctor ok\n'`)
	writeExecutable("ubs", `printf 'ubs doctor ok\n'`)
	writeExecutable("xf", `printf 'xf doctor ok\n'`)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	amSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"status\":\"ok\"}"}]}}`))
	}))
	defer amSrv.Close()
	t.Setenv("AGENT_MAIL_URL", amSrv.URL+"/mcp/")

	ctx := context.Background()
	tools.NewCAAMAdapter().InvalidateCache()
	tools.NewRCHAdapter().InvalidateAvailabilityCache()
	tools.NewDCGAdapter().InvalidateAvailabilityCache()

	acfsDetails := collectVerboseToolDetails(ctx, tools.NewACFSAdapter())
	if len(acfsDetails) != 3 {
		t.Fatalf("ACFS verbose details = %v, want 3 entries", acfsDetails)
	}
	amDetails := collectVerboseToolDetails(ctx, tools.NewAMAdapter())
	if len(amDetails) < 2 || !strings.Contains(amDetails[0], amSrv.URL) {
		t.Fatalf("AM verbose details = %v, want endpoint + health_check", amDetails)
	}
	caamDetails := collectVerboseToolDetails(ctx, tools.NewCAAMAdapter())
	if len(caamDetails) < 2 || !strings.Contains(strings.Join(caamDetails, " "), "multiple_accounts=true") {
		t.Fatalf("CAAM verbose details = %v, want accounts_configured & active_account", caamDetails)
	}
	cassDetails := collectVerboseToolDetails(ctx, tools.NewCASSAdapter())
	if len(cassDetails) != 1 || !strings.Contains(cassDetails[0], "capabilities_json:") {
		t.Fatalf("CASS verbose details = %v", cassDetails)
	}
	cmDetails := collectVerboseToolDetails(ctx, tools.NewCMAdapter())
	if len(cmDetails) != 1 || !strings.Contains(cmDetails[0], "onboard:") {
		t.Fatalf("CM verbose details = %v", cmDetails)
	}
	dcgDetails := collectVerboseToolDetails(ctx, tools.NewDCGAdapter())
	if len(dcgDetails) != 1 || !strings.Contains(dcgDetails[0], "guard_enabled=true") {
		t.Fatalf("DCG verbose details = %v", dcgDetails)
	}
	rchDetails := collectVerboseToolDetails(ctx, tools.NewRCHAdapter())
	if len(rchDetails) < 2 || !strings.Contains(strings.Join(rchDetails, " "), "selected_worker: w1 (load=25%)") {
		t.Fatalf("RCH verbose details = %v, want worker selection", rchDetails)
	}
	for name, adapter := range map[string]tools.Adapter{
		"ru":  tools.NewRUAdapter(),
		"ubs": tools.NewUBSAdapter(),
		"xf":  tools.NewXFAdapter(),
	} {
		got := collectVerboseToolDetails(ctx, adapter)
		if len(got) != 1 || !strings.Contains(got[0], name+" doctor ok") {
			t.Fatalf("%s verbose details = %v", name, got)
		}
	}

	prevVerbose := doctorVerbose
	doctorVerbose = true
	defer func() { doctorVerbose = prevVerbose }()

	var out bytes.Buffer
	report := &DoctorReport{
		Timestamp: time.Now(),
		Overall:   "healthy",
		Tools: []ToolCheck{{
			Name:      "rch",
			Installed: true,
			Status:    "ok",
			Message:   "0.2.0",
			Details:   rchDetails,
		}},
	}
	if err := renderDoctorTUITo(&out, report); err != nil {
		t.Fatalf("renderDoctorTUITo: %v", err)
	}
	if !strings.Contains(out.String(), "selected_worker: w1 (load=25%)") {
		t.Fatalf("verbose doctor output missing detail line:\n%s", out.String())
	}
}

func TestKernelMapInputDecodingAndCanonicalRESTPaths(t *testing.T) {
	// 1. Assert all 14 CLI kernel.MustRegister commands expose canonical /api/v1/* REST paths.
	byName := make(map[string]kernel.Command)
	for _, cmd := range kernel.List() {
		byName[cmd.Name] = cmd
	}
	expectedREST := map[string][2]string{
		"core.version":        {"GET", "/api/v1/version"},
		"core.deps":           {"GET", "/api/v1/deps"},
		"kernel.list":         {"GET", "/api/v1/kernel/commands"},
		"sessions.create":     {"POST", "/api/v1/sessions"},
		"sessions.list":       {"GET", "/api/v1/sessions"},
		"sessions.status":     {"GET", "/api/v1/sessions/{id}/status"},
		"sessions.attach":     {"POST", "/api/v1/sessions/{id}/attach"},
		"sessions.view":       {"POST", "/api/v1/sessions/{id}/view"},
		"sessions.zoom":       {"POST", "/api/v1/sessions/{id}/zoom"},
		"sessions.health":     {"GET", "/api/v1/sessions/{id}/health"},
		"sessions.controller": {"POST", "/api/v1/sessions/{id}/controller"},
		"sessions.interrupt":  {"POST", "/api/v1/sessions/{id}/interrupt"},
		"sessions.kill":       {"DELETE", "/api/v1/sessions/{id}"},
		"prompt.preflight":    {"POST", "/api/v1/prompt/preflight"},
	}
	for name, want := range expectedREST {
		cmd, ok := byName[name]
		if !ok {
			t.Fatalf("kernel command %q is not registered", name)
		}
		if cmd.REST == nil || cmd.REST.Method != want[0] || cmd.REST.Path != want[1] {
			t.Fatalf("kernel command %q REST = %+v, want %s %s", name, cmd.REST, want[0], want[1])
		}
	}

	// 2. Verify decodeKernelInput decodes all JSON fields from map[string]any.
	killOpts, err := decodeKernelInput[SessionKillInput]("sessions.kill", map[string]any{
		"session":   "demo",
		"force":     true,
		"tags":      []string{"cc", "cod"},
		"no_hooks":  true,
		"summarize": true,
	})
	if err != nil || killOpts.Session != "demo" || !killOpts.Force || len(killOpts.Tags) != 2 || !killOpts.NoHooks || !killOpts.Summarize {
		t.Fatalf("decodeKernelInput[SessionKillInput] = (%+v, %v)", killOpts, err)
	}

	ctrlOpts, err := decodeKernelInput[ControllerInput]("sessions.controller", map[string]any{
		"session":     "demo",
		"agent_type":  "cod",
		"prompt_file": "ctrl.md",
		"no_prompt":   true,
	})
	if err != nil || ctrlOpts.Session != "demo" || ctrlOpts.AgentType != "cod" || ctrlOpts.PromptFile != "ctrl.md" || !ctrlOpts.NoPrompt {
		t.Fatalf("decodeKernelInput[ControllerInput] = (%+v, %v)", ctrlOpts, err)
	}

	healthOpts, err := decodeKernelInput[SessionHealthInput]("sessions.health", map[string]any{
		"session":       "demo",
		"pane":          2,
		"pane_selector": "0.2",
		"status":        "warning",
		"verbose":       true,
	})
	if err != nil || healthOpts.Session != "demo" || healthOpts.Pane == nil || *healthOpts.Pane != 2 || healthOpts.PaneSelector != "0.2" || healthOpts.Status != "warning" || !healthOpts.Verbose {
		t.Fatalf("decodeKernelInput[SessionHealthInput] = (%+v, %v)", healthOpts, err)
	}

	// 3. Execute kernel.Run with map[string]any across session/core/preflight handlers
	// and verify none fail with "session is required" when "session" is provided in the map.
	ctx := context.Background()

	if verRes, err := kernel.Run(ctx, "core.version", map[string]any{"short": true}); err != nil || verRes == nil {
		t.Fatalf("kernel.Run(core.version, map) = (%#v, %v)", verRes, err)
	}

	healthRes, err := kernel.Run(ctx, "sessions.health", map[string]any{
		"session": "nonexistent-kernel-session-xyz",
		"status":  "warning",
		"verbose": true,
	})
	if err != nil {
		if strings.Contains(err.Error(), "session is required") || !strings.Contains(err.Error(), "nonexistent-kernel-session-xyz") {
			t.Fatalf("kernel.Run(sessions.health, map) failed to decode session field: %v", err)
		}
	} else {
		healthOut, ok := healthRes.(HealthOutput)
		if !ok || healthOut.SessionHealth == nil || healthOut.SessionHealth.Session != "nonexistent-kernel-session-xyz" {
			t.Fatalf("kernel.Run(sessions.health, map) = %#v, want decoded session name", healthRes)
		}
	}

	for _, tc := range []struct {
		name  string
		input map[string]any
	}{
		{"sessions.interrupt", map[string]any{"session": "nonexistent-kernel-session-xyz", "tags": []string{"cc"}}},
		{"sessions.kill", map[string]any{"session": "nonexistent-kernel-session-xyz", "force": true, "no_hooks": true}},
		{"sessions.controller", map[string]any{"session": "nonexistent-kernel-session-xyz", "agent_type": "cod", "no_prompt": true}},
		{"sessions.status", map[string]any{"session": "nonexistent-kernel-session-xyz", "show_assignments": true}},
		{"sessions.attach", map[string]any{"session": "nonexistent-kernel-session-xyz"}},
		{"sessions.view", map[string]any{"session": "nonexistent-kernel-session-xyz"}},
		{"sessions.zoom", map[string]any{"session": "nonexistent-kernel-session-xyz", "pane": 0}},
	} {
		_, runErr := kernel.Run(ctx, tc.name, tc.input)
		if runErr != nil && (strings.Contains(runErr.Error(), "session is required") || strings.Contains(runErr.Error(), "pane is required")) {
			t.Fatalf("kernel.Run(%s, map) failed to decode map fields: %v", tc.name, runErr)
		}
	}
}

func TestDoctor_RobotSubprocessExecution(t *testing.T) {
	t.Parallel()
	homeDir := t.TempDir()
	configHome := t.TempDir()
	dataHome := t.TempDir()
	projectDir := t.TempDir()

	runDoctorSubprocess := func(pathEnv, cfgPath string, args ...string) ([]byte, int) {
		t.Helper()
		rawArgs, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
		cmd.Dir = projectDir
		cmd.Env = envWithOverrides(os.Environ(),
			"HOME="+homeDir,
			"XDG_CONFIG_HOME="+configHome,
			"XDG_DATA_HOME="+dataHome,
			"PATH="+pathEnv,
			"NTM_NO_COLOR=1",
			"NTM_CONFIG="+cfgPath,
			"NTM_ROBOT_FORMAT=json",
			"NTM_OUTPUT_FORMAT=",
			"TOON_DEFAULT_FORMAT=",
			"NTM_ROBOT_VERBOSITY=",
			"NTM_ROBOT_CONTRACT_ARGS="+string(rawArgs),
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		exitCode := 0
		if runErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(runErr, &exitErr) {
				t.Fatalf("run subprocess %v: %v (stderr=%s)", args, runErr, stderr.String())
			}
			exitCode = exitErr.ExitCode()
		}
		return stdout.Bytes(), exitCode
	}

	// 1. Empty PATH forces missing required tools/tmux -> overall="unhealthy", exitCode != 0, single JSON document.
	emptyBin := t.TempDir()
	rawUnhealthy, code := runDoctorSubprocess(emptyBin, "", "--robot-doctor")
	if code == 0 {
		t.Fatalf("--robot-doctor with empty PATH should exit non-zero, got 0: %s", string(rawUnhealthy))
	}
	var unhealthyOut robot.DoctorOutput
	if err := json.Unmarshal(rawUnhealthy, &unhealthyOut); err != nil {
		t.Fatalf("decode unhealthy --robot-doctor: %v (raw=%s)", err, string(rawUnhealthy))
	}
	if unhealthyOut.Success || unhealthyOut.Overall != "unhealthy" || unhealthyOut.SchemaID != "ntm:robot:doctor:v1" || unhealthyOut.Errors == 0 {
		t.Fatalf("unexpected unhealthy --robot-doctor output: %+v", unhealthyOut)
	}
	if unhealthyOut.Tools == nil || unhealthyOut.Dependencies == nil || unhealthyOut.Contention == nil {
		t.Fatalf("expected non-nil slices on unhealthy --robot-doctor: %+v", unhealthyOut)
	}

	// 2. Fake required binaries (tmux, bv, br) on PATH -> overall="healthy" or "warning", exitCode == 0.
	fakeBin := t.TempDir()
	for name, body := range map[string]string{
		"tmux": "printf 'tmux 3.4\\n'",
		"bv":   "printf 'bv 0.5.0\\n'",
		"br":   "printf 'br 0.4.0\\n'",
	} {
		p := filepath.Join(fakeBin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	rawHealthy, code := runDoctorSubprocess(fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), "", "--robot-doctor")
	if code != 0 {
		t.Fatalf("--robot-doctor with fake required tools exited %d: %s", code, string(rawHealthy))
	}
	var healthyOut robot.DoctorOutput
	if err := json.Unmarshal(rawHealthy, &healthyOut); err != nil {
		t.Fatalf("decode healthy --robot-doctor: %v (raw=%s)", err, string(rawHealthy))
	}
	if !healthyOut.Success || healthyOut.Overall == "unhealthy" || healthyOut.GuardHook == nil {
		t.Fatalf("unexpected healthy --robot-doctor output: %+v", healthyOut)
	}
}
