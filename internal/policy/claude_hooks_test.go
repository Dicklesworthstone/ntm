package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %s: %v\n%s", path, err, data)
	}
	return out
}

func TestRegisterClaudeHookCreatesSettingsAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	settings := ClaudeUserSettingsPath(home)
	script := ClaudeHookScriptPath(home)

	if ok, err := ClaudeHookRegistered(settings, script); err != nil || ok {
		t.Fatalf("registered before install = %v, %v; want false, nil", ok, err)
	}
	changed, err := RegisterClaudeHook(settings, script)
	if err != nil || !changed {
		t.Fatalf("RegisterClaudeHook = %v, %v; want true, nil", changed, err)
	}
	if ok, err := ClaudeHookRegistered(settings, script); err != nil || !ok {
		t.Fatalf("registered after install = %v, %v; want true, nil", ok, err)
	}
	got := readJSONFile(t, settings)
	groups := got["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(groups) != 1 {
		t.Fatalf("PreToolUse groups = %d, want 1: %v", len(groups), groups)
	}
	group := groups[0].(map[string]any)
	handler := group["hooks"].([]any)[0].(map[string]any)
	if group["matcher"] != "Bash" || handler["type"] != "command" || handler["command"] != script {
		t.Fatalf("registered group = %v, want a Bash command hook running %s", group, script)
	}

	changed, err = RegisterClaudeHook(settings, script)
	if err != nil || changed {
		t.Fatalf("second RegisterClaudeHook = %v, %v; want false, nil", changed, err)
	}
	if n := len(readJSONFile(t, settings)["hooks"].(map[string]any)["PreToolUse"].([]any)); n != 1 {
		t.Fatalf("re-registration duplicated the hook: %d groups", n)
	}
}

// The user's own keys, hooks and key order survive register + unregister.
func TestClaudeHookRegistrationPreservesUserSettings(t *testing.T) {
	home := t.TempDir()
	settings := ClaudeUserSettingsPath(home)
	script := ClaudeHookScriptPath(home)
	original := `{
  "model": "opus",
  "hooks": {
    "PostToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "fmt.sh"}]}],
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "mine.sh"}]}]
  },
  "permissions": {"allow": ["Bash(ls)"]}
}
`
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RegisterClaudeHook(settings, script); err != nil {
		t.Fatalf("RegisterClaudeHook: %v", err)
	}
	data, _ := os.ReadFile(settings)
	text := string(data)
	if strings.Index(text, `"model"`) > strings.Index(text, `"hooks"`) || strings.Index(text, `"hooks"`) > strings.Index(text, `"permissions"`) {
		t.Fatalf("top-level key order changed:\n%s", text)
	}
	if strings.Index(text, `"PostToolUse"`) > strings.Index(text, `"PreToolUse"`) {
		t.Fatalf("hooks key order changed:\n%s", text)
	}
	if !strings.Contains(text, "mine.sh") || !strings.Contains(text, "fmt.sh") || !strings.Contains(text, script) {
		t.Fatalf("settings lost a hook:\n%s", text)
	}
	if info, err := os.Stat(settings); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %v (err %v), want 0600 preserved", info.Mode().Perm(), err)
	}

	changed, err := UnregisterClaudeHook(settings, script)
	if err != nil || !changed {
		t.Fatalf("UnregisterClaudeHook = %v, %v; want true, nil", changed, err)
	}
	got := readJSONFile(t, settings)
	var want map[string]any
	if err := json.Unmarshal([]byte(original), &want); err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("after unregister settings = %s, want the original %s", gotJSON, wantJSON)
	}
	if changed, err := UnregisterClaudeHook(settings, script); err != nil || changed {
		t.Fatalf("second UnregisterClaudeHook = %v, %v; want false, nil", changed, err)
	}
}

// Unregistering removes only ntm's handler from a shared group, and drops the
// containers ntm created when they end up empty.
func TestUnregisterClaudeHookKeepsSiblingHandlersAndDropsEmptyContainers(t *testing.T) {
	home := t.TempDir()
	settings := ClaudeUserSettingsPath(home)
	script := ClaudeHookScriptPath(home)
	shared := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"other.sh"},{"type":"command","command":"` + script + `"}]}]}}`
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(shared), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _ := ClaudeHookRegistered(settings, script); !ok {
		t.Fatal("hook in a shared group not recognized as registered")
	}
	if _, err := UnregisterClaudeHook(settings, script); err != nil {
		t.Fatal(err)
	}
	handlers := readJSONFile(t, settings)["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)
	if len(handlers) != 1 || handlers[0].(map[string]any)["command"] != "other.sh" {
		t.Fatalf("handlers after unregister = %v, want only other.sh", handlers)
	}

	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterClaudeHook(settings, script); err != nil {
		t.Fatal(err)
	}
	if _, err := UnregisterClaudeHook(settings, script); err != nil {
		t.Fatal(err)
	}
	if got := readJSONFile(t, settings); len(got) != 0 {
		t.Fatalf("settings after register+unregister on a fresh file = %v, want {}", got)
	}
}

func TestClaudeHookRegisteredRequiresBashMatcher(t *testing.T) {
	home := t.TempDir()
	settings := ClaudeUserSettingsPath(home)
	script := ClaudeHookScriptPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	for matcher, want := range map[string]bool{"Edit": false, "Edit|Bash": true, "": true, "*": true} {
		body := `{"hooks":{"PreToolUse":[{"matcher":"` + matcher + `","hooks":[{"type":"command","command":"` + script + `"}]}]}}`
		if err := os.WriteFile(settings, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := ClaudeHookRegistered(settings, script); err != nil || got != want {
			t.Errorf("matcher %q: registered = %v, %v; want %v", matcher, got, err, want)
		}
	}
}

// A settings file ntm cannot represent is an error, never overwritten.
func TestRegisterClaudeHookRefusesInvalidSettings(t *testing.T) {
	home := t.TempDir()
	settings := ClaudeUserSettingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"model": "opus",`, `["not", "an", "object"]`, `{"hooks": []}`} {
		if err := os.WriteFile(settings, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := RegisterClaudeHook(settings, ClaudeHookScriptPath(home)); err == nil {
			t.Errorf("RegisterClaudeHook accepted %q", body)
		}
		if data, _ := os.ReadFile(settings); string(data) != body {
			t.Errorf("settings %q was rewritten to %q", body, data)
		}
	}
}

// A symlinked settings file (dotfile managers) is updated at its target and
// stays a symlink.
func TestRegisterClaudeHookWritesThroughSymlink(t *testing.T) {
	home := t.TempDir()
	settings := ClaudeUserSettingsPath(home)
	target := filepath.Join(home, "dotfiles", "claude-settings.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"model":"opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterClaudeHook(settings, ClaudeHookScriptPath(home)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(settings); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("settings symlink replaced by a regular file (err %v)", err)
	}
	if got := readJSONFile(t, target); got["model"] != "opus" || got["hooks"] == nil {
		t.Fatalf("symlink target = %v, want model kept and hooks added", got)
	}
}

func TestParseClaudeHookInput(t *testing.T) {
	in, err := ParseClaudeHookInput(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status","timeout":5},"cwd":"/repo","session_id":"s1"}`))
	if err != nil || in.ToolName != "Bash" || in.ToolInput.Command != "git status" || in.Cwd != "/repo" {
		t.Fatalf("ParseClaudeHookInput = %+v, %v", in, err)
	}
	for _, bad := range []string{"", "   ", "{not json", strings.Repeat("x", maxClaudeHookInput+1)} {
		if _, err := ParseClaudeHookInput(strings.NewReader(bad)); err == nil {
			t.Errorf("ParseClaudeHookInput accepted %.20q", bad)
		}
	}
}

func decodeLaunchSettings(t *testing.T, settings string) []map[string]any {
	t.Helper()
	var payload struct {
		Hooks struct {
			PreToolUse []map[string]any `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(settings), &payload); err != nil {
		t.Fatalf("launch settings %q: %v", settings, err)
	}
	return payload.Hooks.PreToolUse
}

func TestClaudeAgentLaunchSettingsCarriesPolicyHookByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := claudeNTMBinary
	claudeNTMBinary = func() string { return "/opt/ntm bin/ntm" }
	t.Cleanup(func() { claudeNTMBinary = prev })

	cfg := config.Default()
	cfg.Integrations.DCG.Enabled = false
	cfg.Integrations.RCH.Enabled = false
	launch := ClaudeAgentLaunchSettings(cfg)
	if len(launch.Sources) != 1 || launch.Sources[0] != "ntm-policy" {
		t.Fatalf("sources = %v, want [ntm-policy]", launch.Sources)
	}
	groups := decodeLaunchSettings(t, launch.Settings)
	handler := groups[0]["hooks"].([]any)[0].(map[string]any)
	if groups[0]["matcher"] != "Bash" || handler["command"] != `'/opt/ntm bin/ntm' safety claude-hook` {
		t.Fatalf("policy hook = %v", groups[0])
	}

	cfg.Safety.ClaudePolicyHook = false
	if got := ClaudeAgentLaunchSettings(cfg); got.Settings != "" || len(got.Sources) != 0 {
		t.Fatalf("claude_policy_hook=false still produced %+v", got)
	}
}

// When `ntm safety install` registered the same check in the user settings,
// Claude Code would run it twice, so launches skip their copy.
func TestClaudeAgentLaunchSettingsSkipsPolicyHookAlreadyRegistered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := config.Default()
	cfg.Integrations.DCG.Enabled = false
	cfg.Integrations.RCH.Enabled = false

	script := ClaudeHookScriptPath(home)
	if _, err := RegisterClaudeHook(ClaudeUserSettingsPath(home), script); err != nil {
		t.Fatal(err)
	}
	// Registered but the script is missing: Claude Code would fail to run
	// it, so the launch keeps its own policy hook.
	if got := ClaudeAgentLaunchSettings(cfg); len(got.Sources) != 1 {
		t.Fatalf("registered-but-missing script: sources = %v, want the launch policy hook", got.Sources)
	}
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(ClaudeHookScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ClaudeAgentLaunchSettings(cfg); got.Settings != "" {
		t.Fatalf("globally registered hook duplicated in launch settings: %+v", got)
	}
}

func TestClaudeAgentLaunchSettingsRCHHook(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.Default()
	cfg.Safety.ClaudePolicyHook = false
	cfg.Integrations.DCG.Enabled = false
	cfg.Integrations.RCH.Enabled = true
	cfg.Integrations.RCH.BinaryPath = "/usr/local/bin/rch"
	cfg.Integrations.RCH.InterceptPatterns = []string{"^go build"}

	// rch is enabled by default "when available": a missing binary gets no
	// hook, since Claude Code would report a hook error on every Bash call.
	prev := claudeHookLookPath
	t.Cleanup(func() { claudeHookLookPath = prev })
	claudeHookLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if got := ClaudeAgentLaunchSettings(cfg); got.Settings != "" {
		t.Fatalf("missing rch still configured: %+v", got)
	}

	claudeHookLookPath = func(name string) (string, error) { return name, nil }
	launch := ClaudeAgentLaunchSettings(cfg)
	if len(launch.Sources) != 1 || launch.Sources[0] != "rch" {
		t.Fatalf("sources = %v, want [rch]", launch.Sources)
	}
	groups := decodeLaunchSettings(t, launch.Settings)
	if cmd := groups[0]["hooks"].([]any)[0].(map[string]any)["command"]; cmd != "'/usr/local/bin/rch'" {
		t.Fatalf("rch hook command = %v", cmd)
	}
}

func TestClaudeAgentLaunchSettingsKeepsDCGHookWithAuditLogAndCustomLists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Create a real executable stub so dcg.ShouldConfigureHooks finds it.
	fakeDir := t.TempDir()
	fakeDCG := filepath.Join(fakeDir, "dcg")
	if err := os.WriteFile(fakeDCG, []byte("#!/bin/sh\necho 'dcg 1.0.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Safety.ClaudePolicyHook = false
	cfg.Integrations.RCH.Enabled = false
	cfg.Integrations.DCG.Enabled = true
	cfg.Integrations.DCG.BinaryPath = fakeDCG
	cfg.Integrations.DCG.AuditLog = filepath.Join(fakeDir, "dcg-audit.jsonl")
	cfg.Integrations.DCG.CustomBlocklist = []string{"DROP DATABASE"}
	cfg.Integrations.DCG.CustomWhitelist = []string{"git status"}

	launch := ClaudeAgentLaunchSettings(cfg)
	if len(launch.Sources) != 1 || launch.Sources[0] != "dcg" {
		t.Fatalf("sources = %v (warnings=%v), want [dcg] — configuring audit_log/custom lists must NOT drop Claude DCG hook", launch.Sources, launch.Warnings)
	}
	groups := decodeLaunchSettings(t, launch.Settings)
	if len(groups) != 1 || groups[0]["matcher"] != "Bash" {
		t.Fatalf("unexpected DCG launch groups: %+v", groups)
	}
	cmd := groups[0]["hooks"].([]any)[0].(map[string]any)["command"].(string)
	if !strings.Contains(cmd, fakeDCG) {
		t.Fatalf("DCG hook command = %q, want to reference %q", cmd, fakeDCG)
	}
	if len(launch.Warnings) == 0 {
		t.Fatalf("expected non-dropping advisory warning about custom_blocklist/custom_whitelist scoping")
	}
}
