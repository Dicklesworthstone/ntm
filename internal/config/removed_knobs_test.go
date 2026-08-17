package config

// WS6-remove proof (bd-ws6-config-truth-ienmd.2): a config containing each
// removed knob still LOADS, emits a loud per-key WARNING with the exact
// disposition text, is surfaced by ScanRemovedKnobs (the `ntm doctor`
// surface), and the value is provably ignored (behavior identical to a
// config without the key). These same fixtures flip to hard-error assertions
// in v1.27.0 (bd-ws6-config-truth-ienmd.3).

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// removedKnobFixtures covers every removed knob family: TOML that sets the
// key, the dotted key the warning must name, and its full disposition text.
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

func printedConfig(t *testing.T, cfg *Config) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Print(cfg, &buf); err != nil {
		t.Fatalf("Print: %v", err)
	}
	return buf.String()
}

// TestRemovedKnobsWarnAndLoad is the per-key proof: config still loads, the
// warning names the key + disposition with the exact contract text, and the
// loaded config is byte-identical (via Print) to one loaded without the key —
// the value is ignored.
func TestRemovedKnobsWarnAndLoad(t *testing.T) {
	base := "projects_base = \"/tmp/removed-knob-proof\"\n"
	baselinePath := createTempConfig(t, base)
	baseline, err := Load(baselinePath)
	if err != nil {
		t.Fatalf("baseline load: %v", err)
	}
	baselinePrinted := printedConfig(t, baseline)

	for _, tt := range removedKnobFixtures {
		t.Run(tt.name, func(t *testing.T) {
			path := createTempConfig(t, base+tt.toml)
			cfg, stderr, err := loadCapturingStderr(t, path)
			if err != nil {
				t.Fatalf("Load must succeed in v1.26.0 with removed key %s, got error: %v", tt.key, err)
			}

			want := fmt.Sprintf(
				"ntm: warning: config key %s was %s; the key is ignored in v1.26.0 and becomes a config error in v1.27.0 — delete it from your config file\n",
				tt.key, tt.disposition)
			if !strings.Contains(stderr, want) {
				t.Fatalf("warning text mismatch for %s.\nwant line: %q\ngot stderr: %q", tt.key, want, stderr)
			}

			// Value provably ignored: identical effective config.
			if got := printedConfig(t, cfg); got != baselinePrinted {
				t.Errorf("removed key %s changed the effective config; it must be ignored", tt.key)
			}

			// Doctor surface: same key + disposition via ScanRemovedKnobs.
			knobs, err := ScanRemovedKnobs(path)
			if err != nil {
				t.Fatalf("ScanRemovedKnobs: %v", err)
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

// TestRemovedKnobsAllAtOnce loads a config setting every removed knob family
// simultaneously: one warning per key, load still succeeds.
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
	_, stderr, err := loadCapturingStderr(t, path)
	if err != nil {
		t.Fatalf("Load must succeed with all removed keys present, got: %v", err)
	}
	for _, tt := range removedKnobFixtures {
		if !strings.Contains(stderr, "config key "+tt.key+" was ") {
			t.Errorf("missing warning for %s in combined load", tt.key)
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

	// A removed knob alongside an unknown key: still a hard error naming only
	// the unknown key.
	path = createTempConfig(t, "[tmux]\npalette_key = \"F5\"\n\n[bogus_section]\nx = 1\n")
	_, err = Load(path)
	if err == nil || !strings.Contains(err.Error(), "bogus_section") {
		t.Fatalf("expected unknown-field error naming bogus_section, got %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "palette_key") {
		t.Fatalf("removed knob must not appear in the unknown-field error: %v", err)
	}
}

// TestClassifyUndecodedKeys_TableHeaderDedup: when a removed table has
// concrete child keys, only the children are reported.
func TestClassifyUndecodedKeys_TableHeaderDedup(t *testing.T) {
	removed, unknown := classifyUndecodedKeys([]string{
		"integrations.caut",
		"integrations.caut.enabled",
		"integrations.caut.currency",
	})
	if len(unknown) != 0 {
		t.Fatalf("unexpected unknown keys: %v", unknown)
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
