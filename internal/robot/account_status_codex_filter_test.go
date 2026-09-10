package robot

// End-to-end regression for the headline symptom of GitHub issue #319 item 4:
//
//   ntm --robot-account-status --provider=codex
//   -> available_accounts=0, current=""
//
// on a host with three healthy Codex seats. This drives the real
// GetAccountStatus against a fake caam on PATH — no live binary, no
// credentials — so the filter, the provider vocabulary and the live-window
// overlay are all exercised together.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// caamRobotStatusPayload is caam's `robot status --compact` shape: profiles
// nested under providers, with caam's own provider id ("codex").
const caamRobotStatusPayload = `{
  "success": true,
  "data": {
    "providers": [
      {
        "id": "codex",
        "logged_in": true,
        "profiles": [
          {"name": "spend-me", "active": true,  "system": false, "health": {"status": "ok"}},
          {"name": "reserve",  "active": false, "system": false, "health": {"status": "ok"}},
          {"name": "third",    "active": false, "system": false, "health": {"status": "ok"}},
          {"name": "_backup_20260101", "active": false, "system": true, "health": {"status": "ok"}}
        ]
      }
    ]
  }
}`

// installFakeCAAMStatus puts a caam on PATH that answers `version` and
// `robot status --compact`, and drops every adapter cache.
func installFakeCAAMStatus(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	payload := filepath.Join(dir, "status.json")
	if err := os.WriteFile(payload, []byte(caamRobotStatusPayload), 0o644); err != nil {
		t.Fatalf("write fake status payload: %v", err)
	}

	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"version\" ]; then echo 'caam 0.1.19 (ef01b64) built on 2026-09-10T00:00:00Z with go1.26.2'; exit 0; fi\n" +
		"if [ \"$1\" = \"robot\" ] && [ \"$2\" = \"status\" ]; then cat " + payload + "; exit 0; fi\n" +
		"if [ \"$1\" = \"robot\" ] && [ \"$2\" = \"cost\" ]; then echo '{\"sessions\":[]}'; exit 0; fi\n" +
		"echo 'unsupported' 1>&2\n" +
		"exit 1\n"

	fake := filepath.Join(dir, "caam")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake caam: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tools.NewCAAMAdapter().InvalidateCache()
	tools.InvalidateCAAMLimitsCache()
	t.Cleanup(func() {
		tools.NewCAAMAdapter().InvalidateCache()
		tools.InvalidateCAAMLimitsCache()
	})
}

// TestAccountStatusResolvesCodexAndOpenAIToTheSameSet is the reported symptom.
// Before the alias fix, --provider=codex matched nothing (accounts are stored
// under "openai") and fell into the requested-but-not-found branch.
func TestAccountStatusResolvesCodexAndOpenAIToTheSameSet(t *testing.T) {
	installFakeCAAMStatus(t)
	stubLimits(t, func(context.Context, string) (*tools.CAAMLimitsResult, error) {
		return codexPool(t), nil
	})

	for _, filter := range []string{"codex", "openai", "cod"} {
		t.Run(filter, func(t *testing.T) {
			output, err := GetAccountStatus(AccountStatusOptions{Provider: filter})
			if err != nil {
				t.Fatalf("GetAccountStatus(%q) error = %v", filter, err)
			}

			status, ok := output.Accounts["openai"]
			if !ok {
				t.Fatalf("accounts = %+v, want the Codex pool under the canonical key", output.Accounts)
			}
			if status.AvailableAccounts != 3 {
				t.Errorf("available_accounts = %d, want 3 (the reported symptom was 0)", status.AvailableAccounts)
			}
			if status.Current != "spend-me" {
				t.Errorf("current = %q, want spend-me (the reported symptom was empty)", status.Current)
			}
			if len(output.Accounts) != 1 {
				t.Errorf("accounts = %+v, want only the filtered provider", output.Accounts)
			}
		})
	}
}

// TestAccountStatusIncludesLiveWindows: the same call must now carry the
// seats' live utilization and refresh times, which is what a controller needs
// in order to respect the quota law at all.
func TestAccountStatusIncludesLiveWindows(t *testing.T) {
	installFakeCAAMStatus(t)
	stubLimits(t, func(context.Context, string) (*tools.CAAMLimitsResult, error) {
		return codexPool(t), nil
	})

	output, err := GetAccountStatus(AccountStatusOptions{Provider: "codex"})
	if err != nil {
		t.Fatalf("GetAccountStatus error = %v", err)
	}
	status := output.Accounts["openai"]

	if status.UsagePercent != 34 {
		t.Errorf("usage_percent = %d, want 34", status.UsagePercent)
	}
	if status.ResetsAt == "" {
		t.Error("resets_at is empty")
	}
	if status.RecommendedPersona != "codex-spend-me" {
		t.Errorf("recommended_persona = %q, want codex-spend-me", status.RecommendedPersona)
	}
	if len(status.Seats) != 2 {
		t.Fatalf("seats = %d, want per-seat windows", len(status.Seats))
	}
}

// TestAccountStatusSurfacesUnreadableLimits: the accounts still list, but the
// missing windows are named rather than looking like a healthy pool.
func TestAccountStatusSurfacesUnreadableLimits(t *testing.T) {
	installFakeCAAMStatus(t)
	stubLimits(t, func(context.Context, string) (*tools.CAAMLimitsResult, error) {
		return nil, tools.ErrCAAMNoSeatSelectable
	})

	output, err := GetAccountStatus(AccountStatusOptions{Provider: "codex"})
	if err != nil {
		t.Fatalf("GetAccountStatus error = %v", err)
	}
	status := output.Accounts["openai"]

	if status.AvailableAccounts != 3 {
		t.Errorf("available_accounts = %d, want the inventory to survive unreadable limits", status.AvailableAccounts)
	}
	if status.LimitsError == "" {
		t.Error("limits_error is empty; an unreadable pool must not look identical to a healthy one")
	}
	if status.RecommendedPersona != "" {
		t.Errorf("recommended_persona = %q, want none when limits are unreadable", status.RecommendedPersona)
	}
}
