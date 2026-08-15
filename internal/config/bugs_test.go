package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultBugsConfig(t *testing.T) {
	b := DefaultBugsConfig()
	if b.PushRouting {
		t.Fatal("push_routing must default to false (opt-in)")
	}
	if b.Interval != 5*time.Minute {
		t.Fatalf("interval default = %s, want 5m", b.Interval)
	}
	if b.CooldownMinutes != 10 {
		t.Fatalf("cooldown_minutes default = %d, want 10", b.CooldownMinutes)
	}
	t.Logf("defaults: %+v", b)
}

func TestBugsConfigEffectiveFallbacks(t *testing.T) {
	var zero BugsConfig
	if got := zero.EffectiveInterval(); got != 5*time.Minute {
		t.Fatalf("zero interval fallback = %s, want 5m", got)
	}
	if got := zero.EffectiveCooldown(); got != 10*time.Minute {
		t.Fatalf("zero cooldown fallback = %s, want 10m", got)
	}
	set := BugsConfig{Interval: time.Minute, CooldownMinutes: 3}
	if got := set.EffectiveInterval(); got != time.Minute {
		t.Fatalf("explicit interval = %s, want 1m", got)
	}
	if got := set.EffectiveCooldown(); got != 3*time.Minute {
		t.Fatalf("explicit cooldown = %s, want 3m", got)
	}
}

func TestBugsConfigTOMLDecode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := "[bugs]\npush_routing = true\ninterval = \"90s\"\ncooldown_minutes = 4\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.Bugs.PushRouting {
		t.Fatal("push_routing = true not decoded")
	}
	if cfg.Bugs.Interval != 90*time.Second {
		t.Fatalf("interval = %s, want 90s", cfg.Bugs.Interval)
	}
	if cfg.Bugs.CooldownMinutes != 4 {
		t.Fatalf("cooldown_minutes = %d, want 4", cfg.Bugs.CooldownMinutes)
	}
	t.Logf("decoded [bugs]: %+v", cfg.Bugs)
}

// TestBugsConfigSurfaceWiring pins the [bugs] section into the config
// surface commands: `ntm config show` (Print), `ntm config get` (GetValue),
// and `ntm config diff` (Diff). The watch command's gate error tells users
// to set [bugs] push_routing, so the section must be discoverable there.
func TestBugsConfigSurfaceWiring(t *testing.T) {
	cfg := Default()
	cfg.Bugs.PushRouting = true
	cfg.Bugs.Interval = 90 * time.Second
	cfg.Bugs.CooldownMinutes = 4

	for path, want := range map[string]interface{}{
		"bugs.push_routing":     true,
		"bugs.interval":         90 * time.Second,
		"bugs.cooldown_minutes": 4,
	} {
		got, err := GetValue(cfg, path)
		if err != nil {
			t.Fatalf("GetValue(%q): %v", path, err)
		}
		if got != want {
			t.Fatalf("GetValue(%q) = %v, want %v", path, got, want)
		}
	}

	var sb strings.Builder
	if err := Print(cfg, &sb); err != nil {
		t.Fatalf("Print: %v", err)
	}
	out := sb.String()
	for _, want := range []string{"[bugs]", "push_routing = true", "cooldown_minutes = 4"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Print output missing %q", want)
		}
	}

	found := map[string]bool{}
	for _, d := range Diff(cfg) {
		found[d.Path] = true
	}
	for _, path := range []string{"bugs.push_routing", "bugs.interval", "bugs.cooldown_minutes"} {
		if !found[path] {
			t.Fatalf("Diff missing changed path %q", path)
		}
	}
}

func TestBugsConfigAbsentSectionKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("theme = \"mocha\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Bugs.PushRouting || cfg.Bugs.Interval != 5*time.Minute || cfg.Bugs.CooldownMinutes != 10 {
		t.Fatalf("absent [bugs] section changed defaults: %+v", cfg.Bugs)
	}
}
