package dashboard

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// Regression for #317: the dashboard only learned its configuration from the
// file watcher, which reports changes rather than the initial state, so
// `[integrations.rano] enabled = false` (and every other setting) was
// ignored until the user edited a config file mid-session. RunWithOptions
// now applies the merged config before the program starts; the rano fetch
// scheduled from that state must report the integration disabled without
// probing rano at all.
func TestApplyConfigHonorsRanoDisabledBeforeAnyReload(t *testing.T) {
	t.Cleanup(func() { theme.SetConfigured("") })

	m := newTestModel(140)
	if m.cfg != nil {
		t.Fatal("precondition: a fresh model carries no config")
	}

	cfg := config.Default()
	cfg.Integrations.Rano.Enabled = false
	cfg.Integrations.Rano.PollIntervalMs = 2500
	m.applyConfig(cfg)

	if m.cfg != cfg {
		t.Fatal("applyConfig must install the config as the effective one")
	}
	if m.ranoNetworkRefreshInterval != 2500*time.Millisecond {
		t.Fatalf("rano refresh interval = %v, want 2.5s from config", m.ranoNetworkRefreshInterval)
	}
	if m.renderer != nil {
		t.Fatal("applyConfig must leave renderer creation to the deferred startup init")
	}

	msg := m.fetchRanoNetworkStats()()
	update, ok := msg.(RanoNetworkUpdateMsg)
	if !ok {
		t.Fatalf("fetch returned %T, want RanoNetworkUpdateMsg", msg)
	}
	if update.Data.Enabled {
		t.Fatal("rano fetch must report the integration disabled")
	}
	if update.Data.Error != nil {
		t.Fatalf("disabled rano fetch must not probe rano, got error %v", update.Data.Error)
	}
	if update.Data.PollInterval != 2500*time.Millisecond {
		t.Fatalf("poll interval = %v, want 2.5s", update.Data.PollInterval)
	}
}

// The configured theme reaches the dashboard chrome and, through the
// process-wide fallback, every panel built afterwards - unless NTM_THEME
// is set explicitly, which keeps precedence.
func TestApplyConfigThemeRespectsExplicitEnvOverride(t *testing.T) {
	t.Setenv("NTM_NO_COLOR", "0")
	t.Setenv("NTM_THEME", "")
	t.Cleanup(func() { theme.SetConfigured("") })

	cfg := config.Default()
	cfg.Theme = "latte"

	m := newTestModel(140)
	m.applyConfig(cfg)
	if m.theme.Base != theme.CatppuccinLatte.Base {
		t.Fatalf("configured theme not applied, got base %s", m.theme.Base)
	}
	if theme.Current().Base != theme.CatppuccinLatte.Base {
		t.Fatal("configured theme must become the process-wide fallback for panels")
	}

	t.Setenv("NTM_THEME", "nord")
	m = newTestModel(140)
	m.applyConfig(cfg)
	if m.theme.Base != theme.Nord.Base {
		t.Fatalf("explicit NTM_THEME must win over the configured theme, got base %s", m.theme.Base)
	}
}

// A nil config is a no-op rather than a crash, and the watcher's reload
// message continues to route through the same application path.
func TestConfigReloadMsgRoutesThroughApplyConfig(t *testing.T) {
	t.Cleanup(func() { theme.SetConfigured("") })

	m := newTestModel(140)
	m.applyConfig(nil)
	if m.cfg != nil {
		t.Fatal("nil config must be ignored")
	}

	cfg := config.Default()
	cfg.Integrations.Rano.Enabled = false
	updated, cmd := m.Update(ConfigReloadMsg{Config: cfg})
	m = updated.(Model)
	if m.cfg != cfg {
		t.Fatal("ConfigReloadMsg must apply the delivered config")
	}
	if cmd == nil {
		t.Fatal("ConfigReloadMsg must re-arm the config subscription")
	}
}
