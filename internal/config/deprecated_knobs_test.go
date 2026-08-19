package config

// bd-6otuk proof (v1.28.0 warn tier): a config containing any key from the
// second dead-knob batch still LOADS, emits exactly one loud per-key startup
// warning naming the key + disposition + the v1.29.0 error deadline, and the
// value is ignored (the struct fields are gone, so nothing can read it). The
// v1.26.0-era removed keys keep failing the loader (removed_knobs_test.go);
// genuinely unknown keys keep failing too.

import (
	"reflect"
	"strings"
	"testing"
)

// deprecatedKnobFixtures covers every deprecated knob family: TOML that sets
// the key, the dotted key the warning must name, and its disposition text.
var deprecatedKnobFixtures = []struct {
	name        string
	toml        string
	key         string
	disposition string
}{
	// accounts.* — whole section deprecated (prefix).
	{"accounts.auto_rotate", "[accounts]\nauto_rotate = true\n", "accounts.auto_rotate", noEffect + " (account rotation reads [rotation] and caam, not [accounts])"},
	{"accounts.state_file", "[accounts]\nstate_file = \"/tmp/s.json\"\n", "accounts.state_file", noEffect + " (account rotation reads [rotation] and caam, not [accounts])"},
	{"accounts.claude.email", "[[accounts.claude]]\nemail = \"a@b.c\"\nalias = \"main\"\npriority = 1\n", "accounts.claude.email", noEffect + " (account rotation reads [rotation] and caam, not [accounts])"},

	// scanner.* — everything but ubs_path (prefixes).
	{"scanner.defaults.timeout", "[scanner.defaults]\ntimeout = \"5s\"\n", "scanner.defaults.timeout", deprecatedScannerDisp},
	{"scanner.thresholds.ci", "[scanner.thresholds.ci]\nfail_critical = true\n", "scanner.thresholds.ci.fail_critical", deprecatedScannerDisp},
	{"scanner.tools.enabled", "[scanner.tools]\nenabled = [\"gosec\"]\n", "scanner.tools.enabled", deprecatedScannerDisp},
	{"scanner.beads.auto_create", "[scanner.beads]\nauto_create = true\n", "scanner.beads.auto_create", deprecatedScannerDisp},
	{"scanner.notifications.enabled", "[scanner.notifications]\nenabled = true\n", "scanner.notifications.enabled", deprecatedScannerDisp},

	// spawn_pacing dead leaves (exact) + backoff/headroom (prefixes).
	{"spawn_pacing.burst_size", "[spawn_pacing]\nburst_size = 99\n", "spawn_pacing.burst_size", deprecatedSpawnPacingDisp},
	{"spawn_pacing.max_spawns_per_sec", "[spawn_pacing]\nmax_spawns_per_sec = 9.5\n", "spawn_pacing.max_spawns_per_sec", deprecatedSpawnPacingDisp},
	{"spawn_pacing.agent_caps.claude_rate_per_sec", "[spawn_pacing.agent_caps]\nclaude_rate_per_sec = 2.5\n", "spawn_pacing.agent_caps.claude_rate_per_sec", deprecatedSpawnPacingDisp},
	{"spawn_pacing.backoff", "[spawn_pacing.backoff]\ninitial_delay_ms = 10\n", "spawn_pacing.backoff.initial_delay_ms", deprecatedSpawnPacingDisp},
	{"spawn_pacing.headroom", "[spawn_pacing.headroom]\nmin_free_mb = 128\n", "spawn_pacing.headroom.min_free_mb", deprecatedSpawnPacingDisp},

	// cass subset.
	{"cass.show_install_hints", "[cass]\nshow_install_hints = false\n", "cass.show_install_hints", noEffect},
	{"cass.duplicates", "[cass.duplicates]\nenabled = true\n", "cass.duplicates.enabled", noEffect + " (duplicate checking is driven by CLI flags, not config)"},
	{"cass.search", "[cass.search]\ndefault_limit = 5\n", "cass.search.default_limit", noEffect},
	{"cass.tui", "[cass.tui]\nshow_status_indicator = false\n", "cass.tui.show_status_indicator", noEffect},

	// integrations leaves.
	{"integrations.caam.enabled", "[integrations.caam]\nenabled = false\n", "integrations.caam.enabled", noEffect + " (caam availability is probed, not configured)"},
	{"integrations.caam.auto_rotate", "[integrations.caam]\nauto_rotate = false\n", "integrations.caam.auto_rotate", noEffect + " (rotation is controlled by [rotation] and the --auto-rotate-accounts flag)"},
	{"integrations.caam.providers", "[integrations.caam]\nproviders = [\"claude\"]\n", "integrations.caam.providers", noEffect},
	{"integrations.rano.binary_path", "[integrations.rano]\nbinary_path = \"/opt/rano\"\n", "integrations.rano.binary_path", noEffect + " (the rano adapter resolves the binary from PATH)"},
	{"integrations.rano.providers", "[integrations.rano]\nproviders = [\"anthropic\"]\n", "integrations.rano.providers", noEffect},
	{"integrations.process_triage.binary_path", "[integrations.process_triage]\nbinary_path = \"/opt/pt\"\n", "integrations.process_triage.binary_path", noEffect + " (the process_triage adapter resolves the binary from PATH)"},
	{"integrations.rch.min_build_time", "[integrations.rch]\nmin_build_time = 5\n", "integrations.rch.min_build_time", noEffect},
	{"integrations.rch.dcg_whitelist", "[integrations.rch]\ndcg_whitelist = true\n", "integrations.rch.dcg_whitelist", noEffect + " (documented legacy no-op)"},
	{"integrations.rch.fallback_local", "[integrations.rch]\nfallback_local = false\n", "integrations.rch.fallback_local", noEffect},
	{"integrations.rch.show_location", "[integrations.rch]\nshow_location = false\n", "integrations.rch.show_location", noEffect},
	{"integrations.rch.preferred_worker", "[integrations.rch]\npreferred_worker = \"w1\"\n", "integrations.rch.preferred_worker", noEffect},

	// checkpoints subset.
	{"checkpoints.auto_checkpoint_on_spawn", "[checkpoints]\nauto_checkpoint_on_spawn = true\n", "checkpoints.auto_checkpoint_on_spawn", deprecatedCheckpointDisp},
	{"checkpoints.interval_minutes", "[checkpoints]\ninterval_minutes = 5\n", "checkpoints.interval_minutes", deprecatedCheckpointDisp},
	{"checkpoints.on_rotation", "[checkpoints]\non_rotation = true\n", "checkpoints.on_rotation", deprecatedCheckpointDisp},
	{"checkpoints.on_error", "[checkpoints]\non_error = true\n", "checkpoints.on_error", deprecatedCheckpointDisp},

	// tmux.activity_indicators (prefix).
	{"tmux.activity_indicators", "[tmux.activity_indicators]\nenabled = true\nactive_seconds = 10\n", "tmux.activity_indicators.enabled", noEffect},

	// robot.output subset.
	{"robot.output.pretty", "[robot.output]\npretty = true\n", "robot.output.pretty", noEffect + " (robot.output.format remains live)"},
	{"robot.output.timestamps", "[robot.output]\ntimestamps = false\n", "robot.output.timestamps", noEffect + " (robot.output.format remains live)"},
	{"robot.output.compress", "[robot.output]\ncompress = true\n", "robot.output.compress", noEffect + " (robot.output.format remains live)"},

	// rotation subset.
	{"rotation.prefer_restart", "[rotation]\nprefer_restart = false\n", "rotation.prefer_restart", noEffect},
	{"rotation.accounts.priority", "[[rotation.accounts]]\nprovider = \"claude\"\nemail = \"a@b.c\"\npriority = 2\n", "rotation.accounts.priority", noEffect + " (rotation accounts are ordered as written in the config file)"},

	// singles.
	{"agent_mail.program_name", "[agent_mail]\nprogram_name = \"custom\"\n", "agent_mail.program_name", noEffect + " (the agent-mail program name is fixed to \"ntm\")"},
	{"agents.default_count", "[agents]\ndefault_count = 7\n", "agents.default_count", noEffect},
	{"recovery.stale_threshold_hours", "[recovery]\nstale_threshold_hours = 48\n", "recovery.stale_threshold_hours", noEffect},
	{"resilience.rate_limit.patterns", "[resilience.rate_limit]\npatterns = [\"x\"]\n", "resilience.rate_limit.patterns", noEffect + " (rate-limit patterns are built into internal/agent)"},
	{"suggestions_enabled", "suggestions_enabled = false\n", "suggestions_enabled", noEffect},
	{"preflight.enabled", "[preflight]\nenabled = false\n", "preflight.enabled", noEffect + " (preflight.strict remains live)"},
	{"command_hooks.description", "[[command_hooks]]\nevent = \"pre_spawn\"\ncommand = \"true\"\ndescription = \"legacy\"\n", "command_hooks.description", noEffect + " (use command_hooks.name to label hooks)"},
}

// TestDeprecatedKnobsWarnAtLoad is the per-key warn-tier proof: the config
// LOADS, stderr carries the exact contract warning line, and the doctor
// surface (ScanDeprecatedKnobs) reports the same key + disposition.
func TestDeprecatedKnobsWarnAtLoad(t *testing.T) {
	base := "projects_base = \"/tmp/deprecated-knob-proof\"\n"

	for _, tt := range deprecatedKnobFixtures {
		t.Run(tt.name, func(t *testing.T) {
			path := createTempConfig(t, base+tt.toml)
			cfg, stderr, err := loadCapturingStderr(t, path)
			if err != nil {
				t.Fatalf("Load must SUCCEED with deprecated key %s (warn tier until v1.29.0), got: %v", tt.key, err)
			}
			if cfg == nil {
				t.Fatalf("Load returned nil config for deprecated key %s", tt.key)
			}

			want := deprecatedKnobWarnLine(RemovedKnob{Key: tt.key, Disposition: tt.disposition})
			if !strings.Contains(stderr, want) {
				t.Fatalf("warning text mismatch for %s.\nwant line: %q\ngot stderr: %q", tt.key, want, stderr)
			}

			// Doctor surface parity.
			knobs, scanErr := ScanDeprecatedKnobs(path)
			if scanErr != nil {
				t.Fatalf("ScanDeprecatedKnobs: %v", scanErr)
			}
			found := false
			for _, k := range knobs {
				if k.Key == tt.key {
					found = true
					if k.Disposition != tt.disposition {
						t.Errorf("ScanDeprecatedKnobs disposition = %q, want %q", k.Disposition, tt.disposition)
					}
				}
			}
			if !found {
				t.Errorf("ScanDeprecatedKnobs did not surface %s (got %v)", tt.key, knobs)
			}

			// A deprecated key must NOT surface on the removed (error) scan.
			removed, scanErr := ScanRemovedKnobs(path)
			if scanErr != nil {
				t.Fatalf("ScanRemovedKnobs: %v", scanErr)
			}
			for _, k := range removed {
				if k.Key == tt.key {
					t.Errorf("deprecated key %s must not appear in ScanRemovedKnobs", tt.key)
				}
			}
		})
	}
}

// TestDeprecatedKnobValueIgnored proves the warn tier ignores the value: a
// config setting only deprecated spawn_pacing knobs yields exactly the
// default SpawnPacingConfig (the fields no longer exist to be set).
func TestDeprecatedKnobValueIgnored(t *testing.T) {
	path := createTempConfig(t, `projects_base = "/tmp/deprecated-knob-proof"
[spawn_pacing]
burst_size = 99
max_spawns_per_sec = 42.0

[spawn_pacing.backoff]
initial_delay_ms = 1

[preflight]
enabled = false
`)
	cfg, stderr, err := loadCapturingStderr(t, path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.SpawnPacing, DefaultSpawnPacingConfig()) {
		t.Errorf("SpawnPacing = %#v, want pristine defaults (deprecated values ignored)", cfg.SpawnPacing)
	}
	if cfg.Preflight.Strict != Default().Preflight.Strict {
		t.Errorf("Preflight.Strict changed by a deprecated sibling key")
	}
	for _, key := range []string{"spawn_pacing.burst_size", "spawn_pacing.max_spawns_per_sec", "spawn_pacing.backoff.initial_delay_ms", "preflight.enabled"} {
		if !strings.Contains(stderr, "config key "+key+" is deprecated") {
			t.Errorf("missing warning for %s; stderr: %q", key, stderr)
		}
	}
}

// TestDeprecatedPlusRemovedKeys: a removed (v1.26.0-era) key alongside a
// deprecated one still fails the load with the removed key's error; the
// deprecated key must not rescue it.
func TestDeprecatedPlusRemovedKeys(t *testing.T) {
	path := createTempConfig(t, `projects_base = "/tmp/deprecated-knob-proof"
[tmux]
palette_key = "F5"

[preflight]
enabled = false
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load must fail when a removed key is present, even alongside deprecated keys")
	}
	wantRemoved := removedKnobErrorLine(RemovedKnob{Key: "tmux.palette_key", Disposition: noEffect})
	if !strings.Contains(err.Error(), wantRemoved) {
		t.Fatalf("error must carry the removed-key line; got: %v", err)
	}
}

// TestDeprecatedPlusUnknownKeys: an unknown key still fails the load even
// when deprecated keys are present.
func TestDeprecatedPlusUnknownKeys(t *testing.T) {
	path := createTempConfig(t, `projects_base = "/tmp/deprecated-knob-proof"
definitely_not_a_key = true

[preflight]
enabled = false
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

// TestDeprecatedKnobsAllAtOnce: one config carrying a representative key from
// every family loads with one warning line per key.
func TestDeprecatedKnobsAllAtOnce(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("projects_base = \"/tmp/deprecated-knob-proof\"\n")
	sb.WriteString("suggestions_enabled = false\n")
	sections := map[string][]string{}
	var order []string
	for _, tt := range deprecatedKnobFixtures {
		if !strings.HasPrefix(tt.toml, "[") {
			continue // bare top-level keys handled above
		}
		lines := strings.SplitN(tt.toml, "\n", 2)
		header := lines[0]
		if strings.HasPrefix(header, "[[") {
			continue // array-of-tables fixtures collide when merged; covered per-fixture
		}
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
	cfg, stderr, err := loadCapturingStderr(t, path)
	if err != nil {
		t.Fatalf("Load must succeed with only deprecated keys present, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("nil config")
	}
	for _, tt := range deprecatedKnobFixtures {
		if !strings.HasPrefix(tt.toml, "[") || strings.HasPrefix(tt.toml, "[[") {
			continue
		}
		if !strings.Contains(stderr, "config key "+tt.key+" is deprecated") {
			t.Errorf("combined load must warn for %s; stderr missing it", tt.key)
		}
	}
}

// TestValidateNTMConfigTOMLToleratesDeprecated: persistence validation
// (config set path) tolerates deprecated keys but still rejects removed and
// unknown ones.
func TestValidateNTMConfigTOMLToleratesDeprecated(t *testing.T) {
	if err := validateNTMConfigTOML("[preflight]\nenabled = false\n"); err != nil {
		t.Fatalf("deprecated key must not fail persistence validation, got: %v", err)
	}
	if err := validateNTMConfigTOML("[tmux]\npalette_key = \"F5\"\n"); err == nil {
		t.Fatal("removed key must still fail persistence validation")
	}
	if err := validateNTMConfigTOML("bogus_key = 1\n"); err == nil {
		t.Fatal("unknown key must still fail persistence validation")
	}
}

// TestEnsembleKeysStayValid: the ensemble.* keys are EXCLUDED from the
// deprecation batch (bd-6otuk) — they are read in every build by the
// --robot-ensemble-spawn config-default path and drive real spawns under
// -tags ensemble_experimental. A config setting them must load silently and
// the values must be parsed, in both build variants.
func TestEnsembleKeysStayValid(t *testing.T) {
	path := createTempConfig(t, `projects_base = "/tmp/deprecated-knob-proof"
[ensemble]
default_ensemble = "diagnosis"
agent_mix = "cc"

[ensemble.synthesis]
strategy = "consensus"

[ensemble.cache]
enabled = true
ttl_minutes = 7

[ensemble.early_stop]
enabled = true
window_size = 3
`)
	cfg, stderr, err := loadCapturingStderr(t, path)
	if err != nil {
		t.Fatalf("ensemble keys must stay loadable: %v", err)
	}
	if strings.Contains(stderr, "ensemble") {
		t.Fatalf("ensemble keys must not warn; stderr: %q", stderr)
	}
	if cfg.Ensemble.DefaultEnsemble != "diagnosis" || cfg.Ensemble.Synthesis.Strategy != "consensus" ||
		cfg.Ensemble.Cache.TTLMinutes != 7 || cfg.Ensemble.EarlyStop.WindowSize != 3 {
		t.Fatalf("ensemble values must be parsed, got %#v", cfg.Ensemble)
	}
}

// TestDeprecatedAndRemovedSetsDisjoint: no key may be in both tiers.
func TestDeprecatedAndRemovedSetsDisjoint(t *testing.T) {
	for key := range deprecatedKnobExact {
		if _, ok := removedKnobExact[key]; ok {
			t.Errorf("key %s is in both removed and deprecated exact sets", key)
		}
		if _, ok := matchPrefix(key, removedKnobPrefixes); ok {
			t.Errorf("deprecated key %s is shadowed by a removed prefix", key)
		}
	}
	for prefix := range deprecatedKnobPrefixes {
		if _, ok := matchPrefix(prefix, removedKnobPrefixes); ok {
			t.Errorf("deprecated prefix %s overlaps a removed prefix", prefix)
		}
	}
}
