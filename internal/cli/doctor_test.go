package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

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
