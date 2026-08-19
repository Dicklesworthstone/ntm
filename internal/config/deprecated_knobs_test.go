package config

// bd-6otuk proof, v1.29.0 flip (bd-ad54k): a config containing any key from
// the second dead-knob batch now FAILS the strict loader with a hard error
// naming the key + disposition (the exact v1.28.0 warning key+disposition
// text, severity flipped — same runway as the v1.26.0→v1.27.0 flip in
// removed_knobs_test.go), every deprecated key present is listed in the one
// load error alongside removed and unknown keys, the keys stay visible to
// `ntm doctor` via ScanDeprecatedKnobs (which reads the file leniently), and
// persistence validation rejects them too. Same fixtures as the v1.28.0
// warn-mode proof — only the assertions changed severity.

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

// TestDeprecatedKnobsErrorAtLoad is the per-key proof (v1.29.0 flip): a
// config containing a deprecated key FAILS to load, the error names the key +
// disposition with the exact contract text, and the key stays visible to the
// doctor surface via ScanDeprecatedKnobs.
func TestDeprecatedKnobsErrorAtLoad(t *testing.T) {
	base := "projects_base = \"/tmp/deprecated-knob-proof\"\n"

	for _, tt := range deprecatedKnobFixtures {
		t.Run(tt.name, func(t *testing.T) {
			path := createTempConfig(t, base+tt.toml)
			cfg, stderr, err := loadCapturingStderr(t, path)
			if err == nil {
				t.Fatalf("Load must FAIL since v1.29.0 with deprecated key %s, but it succeeded", tt.key)
			}
			if cfg != nil {
				t.Fatalf("Load returned a non-nil config alongside the deprecated-key error for %s", tt.key)
			}

			want := deprecatedKnobErrorLine(RemovedKnob{Key: tt.key, Disposition: tt.disposition})
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error text mismatch for %s.\nwant line: %q\ngot error: %q", tt.key, want, err.Error())
			}
			// No leftover warning path: severity flipped, not duplicated.
			if strings.Contains(stderr, "ntm: warning: config key") {
				t.Errorf("deprecated key %s must not also emit the old v1.28.0 warning; stderr: %q", tt.key, stderr)
			}

			// Doctor surface parity: ScanDeprecatedKnobs must keep working on
			// configs the strict loader refuses.
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

// TestDeprecatedKnobMultiKeyOneError: several deprecated keys in one config
// yield ONE load failure whose error carries the exact contract line for
// every key (not first-only).
func TestDeprecatedKnobMultiKeyOneError(t *testing.T) {
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
	if err == nil {
		t.Fatal("Load must fail with deprecated keys present")
	}
	if cfg != nil {
		t.Fatal("Load returned a non-nil config alongside the deprecated-key error")
	}
	for _, want := range []RemovedKnob{
		{Key: "spawn_pacing.burst_size", Disposition: deprecatedSpawnPacingDisp},
		{Key: "spawn_pacing.max_spawns_per_sec", Disposition: deprecatedSpawnPacingDisp},
		{Key: "spawn_pacing.backoff.initial_delay_ms", Disposition: deprecatedSpawnPacingDisp},
		{Key: "preflight.enabled", Disposition: noEffect + " (preflight.strict remains live)"},
	} {
		if !strings.Contains(err.Error(), deprecatedKnobErrorLine(want)) {
			t.Errorf("error must list %s with its disposition; got: %v", want.Key, err)
		}
	}
	if strings.Contains(stderr, "ntm: warning: config key") {
		t.Errorf("no leftover warning path expected; stderr: %q", stderr)
	}
}

// TestDeprecatedPlusRemovedKeys: a removed (v1.26.0-era) key alongside a
// deprecated one fails the load with BOTH keys named in the one error, each
// with its own release-pair text.
func TestDeprecatedPlusRemovedKeys(t *testing.T) {
	path := createTempConfig(t, `projects_base = "/tmp/deprecated-knob-proof"
[tmux]
palette_key = "F5"

[preflight]
enabled = false
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load must fail when removed and deprecated keys are present")
	}
	wantRemoved := removedKnobErrorLine(RemovedKnob{Key: "tmux.palette_key", Disposition: noEffect})
	if !strings.Contains(err.Error(), wantRemoved) {
		t.Fatalf("error must carry the removed-key line; got: %v", err)
	}
	wantDeprecated := deprecatedKnobErrorLine(RemovedKnob{Key: "preflight.enabled", Disposition: noEffect + " (preflight.strict remains live)"})
	if !strings.Contains(err.Error(), wantDeprecated) {
		t.Fatalf("error must also carry the deprecated-key line; got: %v", err)
	}
}

// TestDeprecatedPlusUnknownKeys: an unknown key and a deprecated key are BOTH
// named in the one load error, so a single failed load lists everything to
// fix.
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
	if !strings.Contains(err.Error(), "definitely_not_a_key") {
		t.Fatalf("error must name the unknown field, got %v", err)
	}
	wantDeprecated := deprecatedKnobErrorLine(RemovedKnob{Key: "preflight.enabled", Disposition: noEffect + " (preflight.strict remains live)"})
	if !strings.Contains(err.Error(), wantDeprecated) {
		t.Fatalf("error must also carry the deprecated-key line; got: %v", err)
	}
}

// TestDeprecatedKnobsAllAtOnce: one config carrying a representative key from
// every family fails with ONE error listing every key (not first-only).
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
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load must fail with deprecated keys present")
	}
	for _, tt := range deprecatedKnobFixtures {
		if !strings.HasPrefix(tt.toml, "[") || strings.HasPrefix(tt.toml, "[[") {
			continue
		}
		if !strings.Contains(err.Error(), "config key "+tt.key+" was ") {
			t.Errorf("combined load error must list %s; got: %v", tt.key, err)
		}
	}
}

// TestValidateNTMConfigTOMLRejectsDeprecated: persistence validation (config
// set path) rejects deprecated keys since the v1.29.0 flip — the same
// partition as the strict loader, so `ntm config set` cannot persist a config
// the loader would refuse — and keeps rejecting removed and unknown ones.
func TestValidateNTMConfigTOMLRejectsDeprecated(t *testing.T) {
	err := validateNTMConfigTOML("[preflight]\nenabled = false\n")
	if err == nil {
		t.Fatal("deprecated key must fail persistence validation since v1.29.0")
	}
	want := deprecatedKnobErrorLine(RemovedKnob{Key: "preflight.enabled", Disposition: noEffect + " (preflight.strict remains live)"})
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("persistence rejection must carry the contract line; got: %v", err)
	}
	if err := validateNTMConfigTOML("[tmux]\npalette_key = \"F5\"\n"); err == nil {
		t.Fatal("removed key must still fail persistence validation")
	}
	if err := validateNTMConfigTOML("bogus_key = 1\n"); err == nil {
		t.Fatal("unknown key must still fail persistence validation")
	}
}

// TestScannerTOMLErrorsYAMLSurfaceIntact: the scanner.* keys (all but
// ubs_path) are detached from TOML via toml:"-" and hard-error at load since
// v1.29.0, while the SAME ScannerConfig type keeps serving as the
// project-level .ntm.yaml schema — that YAML surface must stay fully live.
func TestScannerTOMLErrorsYAMLSurfaceIntact(t *testing.T) {
	// TOML: hard error with the contract line.
	path := createTempConfig(t, "projects_base = \"/tmp/deprecated-knob-proof\"\n[scanner.defaults]\ntimeout = \"30s\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("scanner.defaults TOML key must fail the strict loader since v1.29.0")
	}
	want := deprecatedKnobErrorLine(RemovedKnob{Key: "scanner.defaults.timeout", Disposition: deprecatedScannerDisp})
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("scanner TOML error must carry the contract line; got: %v", err)
	}

	// scanner.ubs_path stays live in TOML.
	path = createTempConfig(t, "projects_base = \"/tmp/deprecated-knob-proof\"\n[scanner]\nubs_path = \"/opt/ubs\"\n")
	cfg, stderr, err := loadCapturingStderr(t, path)
	if err != nil {
		t.Fatalf("scanner.ubs_path must stay loadable: %v", err)
	}
	if cfg.Scanner.UBSPath != "/opt/ubs" {
		t.Fatalf("scanner.ubs_path must be parsed, got %q", cfg.Scanner.UBSPath)
	}
	if strings.Contains(stderr, "scanner") {
		t.Fatalf("scanner.ubs_path must not warn; stderr: %q", stderr)
	}

	// YAML: the toml:"-" detach must not touch the YAML surface — the same
	// fields keep decoding through ScannerConfig's yaml tags (the project
	// .ntm.yaml scanner schema).
	var scfg ScannerConfig
	if err := yaml.Unmarshal([]byte("defaults:\n  timeout: 30s\n"), &scfg); err != nil {
		t.Fatalf("yaml scanner schema must stay untouched by the TOML flip: %v", err)
	}
	if scfg.Defaults.Timeout != "30s" {
		t.Fatalf("yaml scanner defaults.timeout = %q, want 30s", scfg.Defaults.Timeout)
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
