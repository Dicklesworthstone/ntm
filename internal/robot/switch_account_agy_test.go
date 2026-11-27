package robot

// Regression tests for GitHub issue #388: caam keeps Antigravity (agy) logins
// under its own tool, "agy", with credential files separate from gemini's.
// The account surfaces folded agy into "gemini", so
// --robot-switch-account=agy:<profile> ran `caam activate gemini <profile>`
// and agy's own profiles were listed as Gemini accounts.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// caamGeminiAndAgyStatus is caam's `robot status --compact` shape with one
// profile pool per tool.
const caamGeminiAndAgyStatus = `{
  "success": true,
  "data": {
    "providers": [
      {"id": "gemini", "logged_in": true, "profiles": [
        {"name": "g-main", "active": true, "system": false, "health": {"status": "ok"}}
      ]},
      {"id": "agy", "logged_in": true, "profiles": [
        {"name": "agy-work", "active": true,  "system": false, "health": {"status": "ok"}},
        {"name": "agy-home", "active": false, "system": false, "health": {"status": "ok"}}
      ]}
    ]
  }
}`

// installRecordingCAAM puts a caam on PATH that answers status, records every
// activate argv to a log file, and reports success. It returns the log path.
func installRecordingCAAM(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	payload := filepath.Join(dir, "status.json")
	if err := os.WriteFile(payload, []byte(caamGeminiAndAgyStatus), 0o644); err != nil {
		t.Fatalf("write fake status payload: %v", err)
	}
	logPath := filepath.Join(dir, "activate.log")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"robot\" ] && [ \"$2\" = \"status\" ]; then cat " + payload + "; exit 0; fi\n" +
		"if [ \"$1\" = \"cost\" ]; then echo '{\"sessions\":[]}'; exit 0; fi\n" +
		"if [ \"$1\" = \"activate\" ]; then echo \"$*\" >> " + logPath + "; " +
		"echo '{\"success\":true,\"profile\":\"agy-home\",\"previous_profile\":\"agy-work\"}'; exit 0; fi\n" +
		"echo 'unsupported' 1>&2\n" +
		"exit 1\n"
	fake := filepath.Join(dir, "caam")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake caam: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tools.NewCAAMAdapter().InvalidateCache()
	t.Cleanup(func() { tools.NewCAAMAdapter().InvalidateCache() })
	return logPath
}

func readActivateLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read activate log: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestParseSwitchAccountArgKeepsAntigravityOutOfGemini(t *testing.T) {
	for _, arg := range []string{"agy", "antigravity", "AGY:agy-home"} {
		if got := ParseSwitchAccountArg(arg).Provider; got != "agy" {
			t.Errorf("ParseSwitchAccountArg(%q).Provider = %q, want agy", arg, got)
		}
	}
	for _, arg := range []string{"gemini", "gmi", "google"} {
		if got := ParseSwitchAccountArg(arg).Provider; got != "gemini" {
			t.Errorf("ParseSwitchAccountArg(%q).Provider = %q, want gemini", arg, got)
		}
	}
	// Quota stays pooled: agy draws on the Gemini quota bucket.
	if got := canonicalRobotProvider("agy"); got != "gemini" {
		t.Errorf("canonicalRobotProvider(agy) = %q, want the gemini quota bucket", got)
	}
}

func TestGetSwitchAccountActivatesCAAMAgyTool(t *testing.T) {
	logPath := installRecordingCAAM(t)

	named, err := GetSwitchAccount(ParseSwitchAccountArg("agy:agy-home"))
	if err != nil {
		t.Fatalf("GetSwitchAccount(agy:agy-home) error = %v", err)
	}
	if !named.Success || !named.Switch.Success || named.Switch.Provider != "agy" || named.Switch.NewAccount != "agy-home" {
		t.Fatalf("named agy switch = %+v", named)
	}

	next, err := GetSwitchAccount(ParseSwitchAccountArg("antigravity"))
	if err != nil {
		t.Fatalf("GetSwitchAccount(antigravity) error = %v", err)
	}
	if !next.Success || next.Switch.Provider != "agy" {
		t.Fatalf("next agy switch = %+v", next)
	}

	got := readActivateLog(t, logPath)
	want := []string{"activate agy agy-home --json", "activate agy --auto --json"}
	if len(got) != len(want) {
		t.Fatalf("caam activate calls = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("caam activate call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAccountSurfacesKeepAgyProfilesSeparateFromGemini(t *testing.T) {
	installRecordingCAAM(t)

	all, err := GetAccountsList(AccountsListOptions{})
	if err != nil {
		t.Fatalf("GetAccountsList error = %v", err)
	}
	byProvider := map[string][]string{}
	for _, acc := range all.Accounts {
		byProvider[acc.Provider] = append(byProvider[acc.Provider], acc.ID)
	}
	if len(byProvider["gemini"]) != 1 || byProvider["gemini"][0] != "g-main" {
		t.Errorf("gemini accounts = %v, want only g-main", byProvider["gemini"])
	}
	if len(byProvider["agy"]) != 2 {
		t.Errorf("agy accounts = %v, want agy-work and agy-home", byProvider["agy"])
	}

	filtered, err := GetAccountsList(AccountsListOptions{Provider: "antigravity"})
	if err != nil {
		t.Fatalf("GetAccountsList(antigravity) error = %v", err)
	}
	if !filtered.Success || len(filtered.Accounts) != 2 {
		t.Fatalf("antigravity-filtered accounts = %+v, want the two agy profiles", filtered)
	}
	for _, acc := range filtered.Accounts {
		if acc.Provider != "agy" {
			t.Errorf("filtered account %+v is not an agy profile", acc)
		}
	}

	status, err := GetAccountStatus(AccountStatusOptions{})
	if err != nil {
		t.Fatalf("GetAccountStatus error = %v", err)
	}
	if got := status.Accounts["gemini"]; got.Current != "g-main" || got.AvailableAccounts != 1 {
		t.Errorf("gemini status = %+v, want current g-main with 1 account", got)
	}
	if got := status.Accounts["agy"]; got.Current != "agy-work" || got.AvailableAccounts != 2 {
		t.Errorf("agy status = %+v, want current agy-work with 2 accounts", got)
	}
}

func TestDiagnoseRateLimitFixCommandSwitchesAgyTool(t *testing.T) {
	rec := buildRateLimitRecommendation(1, "0.1", "demo", &HealthCheck{AgentType: "antigravity"})
	if !strings.Contains(rec.FixCommand, "--robot-switch-account=agy") {
		t.Fatalf("agy fix command = %q, want --robot-switch-account=agy", rec.FixCommand)
	}
}
