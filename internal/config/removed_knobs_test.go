package config

// WS6-remove-finalize proof (bd-ws6-config-truth-ienmd.3): a config
// containing any removed knob FAILS the strict loader with a hard error
// naming the key + disposition (the exact v1.26.0 warning text, severity
// flipped), every removed key present is listed in the one load error, the
// keys stay visible to `ntm doctor` via ScanRemovedKnobs (which reads the
// file leniently), and a clean config loads silently. Same fixtures as the
// v1.26.0 warn-mode proof (bd-ws6-config-truth-ienmd.2) — only the
// assertions changed severity.

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// removedKnobFixtures covers every removed knob family: TOML that sets the
// key, the dotted key the load error must name, and its full disposition
// text.
var removedKnobFixtures = []struct {
	name        string
	toml        string
	key         string
	disposition string
}{
	{"tmux.palette_key", "[tmux]\npalette_key = \"F5\"\n", "tmux.palette_key", noEffect},

	{"caam.rate_limit_patterns", "[integrations.caam]\nrate_limit_patterns = [\"x\"]\n", "integrations.caam.rate_limit_patterns", noEffect},
	{"caam.account_cooldown", "[integrations.caam]\naccount_cooldown = 600\n", "integrations.caam.account_cooldown", noEffect},
	{"caam.alert_threshold", "[integrations.caam]\nalert_threshold = 90\n", "integrations.caam.alert_threshold", noEffect},

	{"caut.section", "[integrations.caut]\nenabled = true\npoll_interval = 30\n", "integrations.caut.enabled", noEffect + " (the orphaned caut integration was deleted)"},
	{"proxy.section", "[integrations.proxy]\nenabled = true\nbin_path = \"rust_proxy\"\n", "integrations.proxy.enabled", noEffect},

	{"process_triage.on_stuck", "[integrations.process_triage]\non_stuck = \"kill\"\n", "integrations.process_triage.on_stuck", noEffect},

	{"rano.persist_history", "[integrations.rano]\npersist_history = true\n", "integrations.rano.persist_history", noEffect},
	{"rano.history_days", "[integrations.rano]\nhistory_days = 14\n", "integrations.rano.history_days", noEffect},

	{"xf.bin_path", "[integrations.xf]\nbin_path = \"/opt/xf\"\n", "integrations.xf.bin_path", noEffect + " (the shipped xf surfaces resolve the binary from PATH)"},
	{"xf.archive_path", "[integrations.xf]\narchive_path = \"~/.xf/a\"\n", "integrations.xf.archive_path", noEffect + " (the shipped xf surfaces resolve the binary from PATH)"},
	{"xf.default_mode", "[integrations.xf]\ndefault_mode = \"semantic\"\n", "integrations.xf.default_mode", noEffect + " (use --xf-mode per invocation)"},

	{"rotation.dashboard", "[rotation.dashboard]\nshow_quota_bars = false\n", "rotation.dashboard.show_quota_bars", noEffect},

	{"memory.include_anti_patterns", "[memory]\ninclude_anti_patterns = false\n", "memory.include_anti_patterns", noEffect},
	{"memory.include_history", "[memory]\ninclude_history = false\n", "memory.include_history", noEffect},

	{"retry.scheduler", "[retry.scheduler]\nmax_attempts = 9\n", "retry.scheduler.max_attempts", noEffect + " (no scheduler retry loop ships; live overrides: [retry.webhook], [retry.alerts], [retry.agent_mail])"},
	{"retry.completion", "[retry.completion]\ninitial_delay_ms = 9\n", "retry.completion.initial_delay_ms", noEffect + " (no completion retry loop ships; live overrides: [retry.webhook], [retry.alerts], [retry.agent_mail])"},
	{"retry.db", "[retry.db]\nmax_attempts = 9\n", "retry.db.max_attempts", noEffect + " (no db retry loop ships; live overrides: [retry.webhook], [retry.alerts], [retry.agent_mail])"},
	{"retry.assign", "[retry.assign]\nmax_attempts = 9\n", "retry.assign.max_attempts", noEffect + " (no assign retry loop ships; live overrides: [retry.webhook], [retry.alerts], [retry.agent_mail])"},

	{"swarm.limit_patterns", "[swarm.limit_patterns]\ncc = [\"limit\"]\n", "swarm.limit_patterns.cc", noEffect},
	{"swarm.marching_orders", "[swarm.marching_orders]\ndefault = \"go\"\n", "swarm.marching_orders.default", noEffect},
}

// loadCapturingStderr runs Load while capturing everything it writes to
// os.Stderr (the startup warning stream).
func loadCapturingStderr(t *testing.T, path string) (*Config, string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	cfg, loadErr := Load(path)
	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("reading captured stderr: %v", err)
	}
	return cfg, buf.String(), loadErr
}

// TestRemovedKnobsErrorAtLoad is the per-key proof (v1.27.0 flip): a config
// containing a removed key FAILS to load, the error names the key +
// disposition with the exact contract text, and the key stays visible to the
// doctor surface via ScanRemovedKnobs.
func TestRemovedKnobsErrorAtLoad(t *testing.T) {
	base := "projects_base = \"/tmp/removed-knob-proof\"\n"

	for _, tt := range removedKnobFixtures {
		t.Run(tt.name, func(t *testing.T) {
			path := createTempConfig(t, base+tt.toml)
			cfg, stderr, err := loadCapturingStderr(t, path)
			if err == nil {
				t.Fatalf("Load must FAIL since v1.27.0 with removed key %s, but it succeeded", tt.key)
			}
			if cfg != nil {
				t.Fatalf("Load returned a non-nil config alongside the removed-key error for %s", tt.key)
			}

			want := removedKnobErrorLine(RemovedKnob{Key: tt.key, Disposition: tt.disposition})
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error text mismatch for %s.\nwant line: %q\ngot error: %q", tt.key, want, err.Error())
			}
			// No leftover warning path: severity flipped, not duplicated.
			if strings.Contains(stderr, "ntm: warning: config key") {
				t.Errorf("removed key %s must not also emit the old deprecation warning; stderr: %q", tt.key, stderr)
			}

			// Doctor surface: same key + disposition via ScanRemovedKnobs,
			// which must keep working on configs the strict loader refuses.
			knobs, scanErr := ScanRemovedKnobs(path)
			if scanErr != nil {
				t.Fatalf("ScanRemovedKnobs: %v", scanErr)
			}
			found := false
			for _, k := range knobs {
				if k.Key == tt.key {
					found = true
					if k.Disposition != tt.disposition {
						t.Errorf("ScanRemovedKnobs disposition = %q, want %q", k.Disposition, tt.disposition)
					}
				}
			}
			if !found {
				t.Errorf("ScanRemovedKnobs did not surface %s (got %v)", tt.key, knobs)
			}
		})
	}
}

// TestCleanConfigLoadsSilently: a config with no removed keys loads without
// error and without any removed-key (or other) warning on stderr.
func TestCleanConfigLoadsSilently(t *testing.T) {
	path := createTempConfig(t, "projects_base = \"/tmp/removed-knob-proof\"\n")
	cfg, stderr, err := loadCapturingStderr(t, path)
	if err != nil {
		t.Fatalf("clean config must load, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("clean config load returned nil config")
	}
	if strings.Contains(stderr, "ntm: warning") || strings.Contains(stderr, "config key") {
		t.Fatalf("clean config must load silently, got stderr: %q", stderr)
	}
}

// TestRemovedKnobsAllAtOnce loads a config setting every removed knob family
// simultaneously: the load fails with ONE error listing every removed key
// (not first-only).
func TestRemovedKnobsAllAtOnce(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("projects_base = \"/tmp/removed-knob-proof\"\n")
	// Merge fixtures that share a section header to keep the TOML valid.
	sections := map[string][]string{}
	var order []string
	for _, tt := range removedKnobFixtures {
		lines := strings.SplitN(tt.toml, "\n", 2)
		header := lines[0]
		if _, seen := sections[header]; !seen {
			order = append(order, header)
		}
		sections[header] = append(sections[header], strings.TrimRight(lines[1], "\n"))
	}
	for _, header := range order {
		sb.WriteString(header + "\n")
		for _, body := range sections[header] {
			sb.WriteString(body + "\n")
		}
	}

	path := createTempConfig(t, sb.String())
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load must fail with all removed keys present")
	}
	for _, tt := range removedKnobFixtures {
		if !strings.Contains(err.Error(), "config key "+tt.key+" was ") {
			t.Errorf("combined load error must list %s; got: %v", tt.key, err)
		}
	}
}

// TestUnknownFieldStillErrors: the removed-knob tolerance must not weaken the
// strict loader for genuinely unknown keys.
func TestUnknownFieldStillErrors(t *testing.T) {
	path := createTempConfig(t, "definitely_not_a_key = true\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}

	// A removed knob alongside an unknown key: one hard error naming BOTH —
	// the unknown field as unknown, the removed knob with its disposition —
	// so a single failed load lists everything to fix.
	path = createTempConfig(t, "[tmux]\npalette_key = \"F5\"\n\n[bogus_section]\nx = 1\n")
	_, err = Load(path)
	if err == nil || !strings.Contains(err.Error(), "bogus_section") {
		t.Fatalf("expected unknown-field error naming bogus_section, got %v", err)
	}
	wantRemoved := removedKnobErrorLine(RemovedKnob{Key: "tmux.palette_key", Disposition: noEffect})
	if !strings.Contains(err.Error(), wantRemoved) {
		t.Fatalf("error must also list the removed knob with its disposition.\nwant line: %q\ngot: %v", wantRemoved, err)
	}
}

// TestClassifyUndecodedKeys_TableHeaderDedup: when a removed table has
// concrete child keys, only the children are reported.
func TestClassifyUndecodedKeys_TableHeaderDedup(t *testing.T) {
	removed, deprecated, unknown := classifyUndecodedKeys([]string{
		"integrations.caut",
		"integrations.caut.enabled",
		"integrations.caut.currency",
	})
	if len(unknown) != 0 {
		t.Fatalf("unexpected unknown keys: %v", unknown)
	}
	if len(deprecated) != 0 {
		t.Fatalf("unexpected deprecated keys: %v", deprecated)
	}
	got := make([]string, 0, len(removed))
	for _, k := range removed {
		got = append(got, k.Key)
	}
	want := []string{"integrations.caut.currency", "integrations.caut.enabled"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("classifyUndecodedKeys = %v, want %v", got, want)
	}
}

// TestScanRemovedKnobs_MissingFile: a missing config is not an error and has
// no removed knobs.
func TestScanRemovedKnobs_MissingFile(t *testing.T) {
	knobs, err := ScanRemovedKnobs("/nonexistent/ntm-config.toml")
	if err != nil {
		t.Fatalf("ScanRemovedKnobs(missing) error: %v", err)
	}
	if len(knobs) != 0 {
		t.Fatalf("expected no knobs for missing file, got %v", knobs)
	}
}
