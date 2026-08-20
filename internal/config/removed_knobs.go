package config

// WS6-remove (bd-ws6-config-truth-ienmd.2) + WS6-remove-finalize
// (bd-ws6-config-truth-ienmd.3): staged removal of every reader-less config
// knob. The struct fields for these keys are GONE — the strict loader would
// normally reject them as unknown fields. v1.26.0 shipped a one-release
// migration runway: each removed key produced a loud startup WARNING naming
// the key and its disposition while the config still loaded. Since v1.27.0
// each removed key is a hard strict-loader ERROR with the same key +
// disposition text; every removed key present is listed in the single load
// error so one failed load names everything to delete.
//
// `ntm doctor` surfaces the same keys via ScanRemovedKnobs, which reads the
// config file leniently — useful precisely because the strict loader now
// refuses such configs.
//
// SECOND TIER (bd-6otuk, v1.28.0 batch): the G2 config-key liveness audit
// found a further set of keys with no runtime reader. They followed the same
// staged-removal runway one release behind: v1.28.0 shipped them as a WARN
// tier (load succeeded, value ignored, one loud per-key startup warning).
// Since v1.29.0 each key in this tier is a hard strict-loader ERROR with the
// same key + disposition text — the same warn→error flip the v1.26.0 batch
// went through in v1.27.0, one release later.
//
// The two tiers stay SEPARATE sets even though both now error: each tier's
// error line cites its own deprecation/error release pair and migration
// table. FUTURE deprecation batches should add a third tier following the
// same pattern (warn for one release, then flip): classifyUndecodedKeys
// partitions per tier, Scan*Knobs gives doctor a lenient per-tier surface,
// and the v1.28.0 warn-mode implementation is in git history at the v1.28.0
// tag (deprecatedKnobWarnLine/warnDeprecatedKnobs).

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// RemovedKnob is a config key found in a user's config file that was removed
// in v1.26.0, together with its disposition sentence.
type RemovedKnob struct {
	Key         string // dotted key as written in the config file
	Disposition string // names the replacement, or states there is none
}

// noEffect is the honest sentence: these keys were parsed, validated, and
// printed, but read by nothing.
const noEffect = "removed, no replacement — this key never had an effect"

// removedKnobExact maps exact removed leaf keys to their dispositions.
var removedKnobExact = map[string]string{
	"tmux.palette_key": noEffect,

	"integrations.caam.rate_limit_patterns": noEffect,
	"integrations.caam.account_cooldown":    noEffect,
	"integrations.caam.alert_threshold":     noEffect,

	"integrations.process_triage.on_stuck": noEffect,

	"integrations.rano.persist_history": noEffect,
	"integrations.rano.history_days":    noEffect,

	"integrations.xf.bin_path":     noEffect + " (the shipped xf surfaces resolve the binary from PATH)",
	"integrations.xf.archive_path": noEffect + " (the shipped xf surfaces resolve the binary from PATH)",
	"integrations.xf.default_mode": noEffect + " (use --xf-mode per invocation)",

	"memory.include_anti_patterns": noEffect,
	"memory.include_history":       noEffect,
}

// removedKnobPrefixes maps removed table prefixes to their dispositions; a
// prefix matches the table key itself and every key beneath it.
var removedKnobPrefixes = map[string]string{
	"integrations.caut":  noEffect + " (the orphaned caut integration was deleted)",
	"integrations.proxy": noEffect,
	"rotation.dashboard": noEffect,

	"swarm.limit_patterns":  noEffect,
	"swarm.marching_orders": noEffect,

	"retry.scheduler":  noEffect + " (no scheduler retry loop ships; live overrides: [retry.webhook], [retry.alerts], [retry.agent_mail])",
	"retry.completion": noEffect + " (no completion retry loop ships; live overrides: [retry.webhook], [retry.alerts], [retry.agent_mail])",
	"retry.db":         noEffect + " (no db retry loop ships; live overrides: [retry.webhook], [retry.alerts], [retry.agent_mail])",
	"retry.assign":     noEffect + " (no assign retry loop ships; live overrides: [retry.webhook], [retry.alerts], [retry.agent_mail])",
}

// Dead-knob (bd-6otuk) disposition sentences that point at what stays live,
// so the error tells the user both what to delete and what still works.
const (
	deprecatedScannerDisp     = noEffect + " (only scanner.ubs_path is read; the auto-scan chain never shipped)"
	deprecatedSpawnPacingDisp = noEffect + " (live pacing knobs: spawn_pacing.enabled, spawn_pacing.max_concurrent_spawns, spawn_pacing.agent_caps.{claude,codex,gemini}_max_concurrent)"
	deprecatedCheckpointDisp  = noEffect + " (the background checkpoint worker never shipped; the other [checkpoints] knobs and manual 'ntm checkpoint' are unaffected)"
)

// deprecatedKnobExact maps the v1.28.0 dead-knob batch (bd-6otuk) exact leaf
// keys to their dispositions. Warn tier in v1.28.0; a hard strict-loader
// error since v1.29.0.
var deprecatedKnobExact = map[string]string{
	// spawn_pacing: only enabled, max_concurrent_spawns, and the three
	// *_max_concurrent agent caps have runtime readers (robot spawn
	// admission); every other pacing leaf was parsed and validated only.
	"spawn_pacing.agent_caps.claude_ramp_up_delay_ms": deprecatedSpawnPacingDisp,
	"spawn_pacing.agent_caps.claude_rate_per_sec":     deprecatedSpawnPacingDisp,
	"spawn_pacing.agent_caps.codex_ramp_up_delay_ms":  deprecatedSpawnPacingDisp,
	"spawn_pacing.agent_caps.codex_rate_per_sec":      deprecatedSpawnPacingDisp,
	"spawn_pacing.agent_caps.gemini_ramp_up_delay_ms": deprecatedSpawnPacingDisp,
	"spawn_pacing.agent_caps.gemini_rate_per_sec":     deprecatedSpawnPacingDisp,
	"spawn_pacing.agent_caps.cooldown_on_failure_ms":  deprecatedSpawnPacingDisp,
	"spawn_pacing.agent_caps.recovery_successes":      deprecatedSpawnPacingDisp,
	"spawn_pacing.backpressure_threshold":             deprecatedSpawnPacingDisp,
	"spawn_pacing.burst_size":                         deprecatedSpawnPacingDisp,
	"spawn_pacing.default_retries":                    deprecatedSpawnPacingDisp,
	"spawn_pacing.max_spawns_per_sec":                 deprecatedSpawnPacingDisp,
	"spawn_pacing.retry_delay_ms":                     deprecatedSpawnPacingDisp,

	"cass.show_install_hints": noEffect,

	"integrations.caam.providers":   noEffect,
	"integrations.caam.enabled":     noEffect + " (caam availability is probed, not configured)",
	"integrations.caam.auto_rotate": noEffect + " (rotation is controlled by [rotation] and the --auto-rotate-accounts flag)",

	"integrations.rano.providers":   noEffect,
	"integrations.rano.binary_path": noEffect + " (the rano adapter resolves the binary from PATH)",

	"integrations.process_triage.binary_path": noEffect + " (the process_triage adapter resolves the binary from PATH)",

	"integrations.rch.min_build_time":   noEffect,
	"integrations.rch.show_location":    noEffect,
	"integrations.rch.preferred_worker": noEffect,
	"integrations.rch.dcg_whitelist":    noEffect + " (documented legacy no-op)",
	"integrations.rch.fallback_local":   noEffect,

	"checkpoints.auto_checkpoint_on_spawn": deprecatedCheckpointDisp,
	"checkpoints.interval_minutes":         deprecatedCheckpointDisp,
	"checkpoints.on_error":                 deprecatedCheckpointDisp,
	"checkpoints.on_rotation":              deprecatedCheckpointDisp,

	"robot.output.pretty":     noEffect + " (robot.output.format remains live)",
	"robot.output.timestamps": noEffect + " (robot.output.format remains live)",
	"robot.output.compress":   noEffect + " (robot.output.format remains live)",

	"rotation.prefer_restart":    noEffect,
	"rotation.accounts.priority": noEffect + " (rotation accounts are ordered as written in the config file)",

	"agent_mail.program_name":        noEffect + " (the agent-mail program name is fixed to \"ntm\")",
	"agents.default_count":           noEffect,
	"recovery.stale_threshold_hours": noEffect,
	"resilience.rate_limit.patterns": noEffect + " (rate-limit patterns are built into internal/agent)",
	"suggestions_enabled":            noEffect,
	"preflight.enabled":              noEffect + " (preflight.strict remains live)",
	"command_hooks.description":      noEffect + " (use command_hooks.name to label hooks)",
}

// deprecatedKnobPrefixes maps deprecated table prefixes of the v1.28.0 batch
// to their dispositions; a prefix matches the table key itself and every key
// beneath it.
var deprecatedKnobPrefixes = map[string]string{
	"accounts": noEffect + " (account rotation reads [rotation] and caam, not [accounts])",

	"scanner.beads":         deprecatedScannerDisp,
	"scanner.defaults":      deprecatedScannerDisp,
	"scanner.notifications": deprecatedScannerDisp,
	"scanner.thresholds":    deprecatedScannerDisp,
	"scanner.tools":         deprecatedScannerDisp,

	"spawn_pacing.backoff":  deprecatedSpawnPacingDisp,
	"spawn_pacing.headroom": deprecatedSpawnPacingDisp,

	"cass.duplicates": noEffect + " (duplicate checking is driven by CLI flags, not config)",
	"cass.search":     noEffect,
	"cass.tui":        noEffect,

	"tmux.activity_indicators": noEffect,
}

// classifyDeadKey reports whether a dotted config key is a known dead knob
// (either removal tier), together with its disposition and tier name
// ("removed" for the v1.26.0 batch, "deprecated" for the v1.28.0 batch).
// Shared by the strict-loader classification and `ntm config migrate`.
func classifyDeadKey(key string) (disposition, tier string, ok bool) {
	if disp, found := removedKnobExact[key]; found {
		return disp, DeadKeyTierRemoved, true
	}
	if disp, found := matchPrefix(key, removedKnobPrefixes); found {
		return disp, DeadKeyTierRemoved, true
	}
	if disp, found := deprecatedKnobExact[key]; found {
		return disp, DeadKeyTierDeprecated, true
	}
	if disp, found := matchPrefix(key, deprecatedKnobPrefixes); found {
		return disp, DeadKeyTierDeprecated, true
	}
	return "", "", false
}

// Dead-key tier names shared by the strict loader, doctor, and config migrate.
const (
	// DeadKeyTierRemoved is the v1.26.0 removal batch (error since v1.27.0).
	DeadKeyTierRemoved = "removed"
	// DeadKeyTierDeprecated is the v1.28.0 batch (bd-6otuk; error since v1.29.0).
	DeadKeyTierDeprecated = "deprecated"
)

// DeadKeyLoadError is the strict-loader failure for a config file containing
// removed (v1.26.0 batch) and/or deprecated (v1.28.0 batch) keys, plus any
// genuinely unknown fields found in the same pass. Its Error() text is
// byte-identical to the historical fmt.Errorf message, so robot JSON error
// envelopes and doctor are unchanged; the type exists so the human CLI
// fallback (root.go PersistentPreRunE) can collapse the multi-line detail
// into a single actionable line pointing at `ntm config migrate`.
type DeadKeyLoadError struct {
	Removed    []RemovedKnob
	Deprecated []RemovedKnob
	Unknown    []string
}

func (e *DeadKeyLoadError) Error() string {
	var msgs []string
	if len(e.Unknown) > 0 {
		msgs = append(msgs, "unknown field(s): "+strings.Join(e.Unknown, ", "))
	}
	msgs = append(msgs, removedKnobErrorLines(e.Removed)...)
	msgs = append(msgs, deprecatedKnobErrorLines(e.Deprecated)...)
	return "parsing config: " + strings.Join(msgs, "\n")
}

// DeadKeyCount is the number of removed + deprecated keys in the failure
// (unknown fields excluded — those are not migratable no-ops).
func (e *DeadKeyLoadError) DeadKeyCount() int {
	return len(e.Removed) + len(e.Deprecated)
}

// classifyUndecodedKeys partitions the strict loader's undecoded key list
// into recognized removed knobs (hard error since v1.27.0), recognized
// deprecated knobs (v1.28.0 batch: hard error since v1.29.0, with its own
// release-pair error text), and genuinely unknown fields (still a hard load
// error). When a removed/deprecated table prefix
// has concrete child keys present, only the children are reported (one line
// per key the user actually wrote); the bare table header is reported only
// when it appears alone.
func classifyUndecodedKeys(fields []string) (removed, deprecated []RemovedKnob, unknown []string) {
	type match struct {
		disposition string
		deprecated  bool
	}
	matched := make(map[string]match)
	for _, key := range fields {
		if disp, ok := removedKnobExact[key]; ok {
			matched[key] = match{disposition: disp}
			continue
		}
		if disp, ok := matchPrefix(key, removedKnobPrefixes); ok {
			matched[key] = match{disposition: disp}
			continue
		}
		if disp, ok := deprecatedKnobExact[key]; ok {
			matched[key] = match{disposition: disp, deprecated: true}
			continue
		}
		if disp, ok := matchPrefix(key, deprecatedKnobPrefixes); ok {
			matched[key] = match{disposition: disp, deprecated: true}
			continue
		}
		unknown = append(unknown, key)
	}

	// Drop pure table headers shadowed by concrete child keys.
	keys := make([]string, 0, len(matched))
	for key := range matched {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if hasStrictChild(key, keys) {
			continue
		}
		knob := RemovedKnob{Key: key, Disposition: matched[key].disposition}
		if matched[key].deprecated {
			deprecated = append(deprecated, knob)
		} else {
			removed = append(removed, knob)
		}
	}
	return removed, deprecated, unknown
}

func matchPrefix(key string, prefixes map[string]string) (string, bool) {
	for prefix, disp := range prefixes {
		if key == prefix || strings.HasPrefix(key, prefix+".") {
			return disp, true
		}
	}
	return "", false
}

func hasStrictChild(key string, keys []string) bool {
	for _, other := range keys {
		if other != key && strings.HasPrefix(other, key+".") {
			return true
		}
	}
	return false
}

// removedKnobErrorLine renders the strict-loader error line for one removed
// knob. The key + disposition text is IDENTICAL to the v1.26.0 deprecation
// warning it replaces (WS6-remove-finalize, bd-ws6-config-truth-ienmd.3) —
// only the severity changed. The line tells the user exactly what to delete
// and why, and points at the v1.26.0 migration table for the full list.
func removedKnobErrorLine(knob RemovedKnob) string {
	return fmt.Sprintf(
		"config key %s was %s — delete it from your config file (removed in v1.26.0, a config error since v1.27.0; see the v1.26.0 removed-key migration table in CHANGELOG.md)",
		knob.Key, knob.Disposition)
}

// removedKnobErrorLines renders one error line per removed knob, in input
// order, so a single load failure lists every key the user must delete.
func removedKnobErrorLines(removed []RemovedKnob) []string {
	lines := make([]string, 0, len(removed))
	for _, knob := range removed {
		lines = append(lines, removedKnobErrorLine(knob))
	}
	return lines
}

// deprecatedKnobErrorLine renders the strict-loader error line for one
// v1.28.0-batch deprecated knob (bd-6otuk). The key + disposition text is
// IDENTICAL to the v1.28.0 deprecation warning it replaces — only the
// severity changed, exactly like the v1.26.0→v1.27.0 flip
// (bd-ws6-config-truth-ienmd.3). The line tells the user exactly what to
// delete and why, and points at the v1.28.0 migration table for the full
// list.
func deprecatedKnobErrorLine(knob RemovedKnob) string {
	return fmt.Sprintf(
		"config key %s was %s — delete it from your config file (deprecated in v1.28.0, a config error since v1.29.0; see the v1.28.0 deprecated-key migration table in CHANGELOG.md)",
		knob.Key, knob.Disposition)
}

// deprecatedKnobErrorLines renders one error line per v1.28.0-batch knob, in
// input order, so a single load failure lists every key the user must delete.
func deprecatedKnobErrorLines(deprecated []RemovedKnob) []string {
	lines := make([]string, 0, len(deprecated))
	for _, knob := range deprecated {
		lines = append(lines, deprecatedKnobErrorLine(knob))
	}
	return lines
}

// ScanRemovedKnobs reports the removed config knobs present in the config
// file at path (empty = DefaultPath). It is the `ntm doctor` surface: same
// classification and disposition text as the strict-loader error, but it
// decodes leniently, so it works on exactly the configs the strict loader
// refuses since v1.27.0. A missing config file yields no knobs; an
// unparseable file yields an error.
func ScanRemovedKnobs(path string) ([]RemovedKnob, error) {
	removed, _, err := scanKnobs(path)
	return removed, err
}

// ScanDeprecatedKnobs reports the v1.28.0-batch deprecated (bd-6otuk) config
// knobs present in the config file at path (empty = DefaultPath). Same
// lenient decode and disposition text as ScanRemovedKnobs — the `ntm doctor`
// remediation surface, which keeps working on exactly the configs the strict
// loader refuses since these keys became hard errors in v1.29.0.
func ScanDeprecatedKnobs(path string) ([]RemovedKnob, error) {
	_, deprecated, err := scanKnobs(path)
	return deprecated, err
}

func scanKnobs(path string) (removed, deprecated []RemovedKnob, err error) {
	if path == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	cfg := &Config{}
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing config: %w", err)
	}
	removed, deprecated, _ = classifyUndecodedKeys(undecodedConfigFields(md))
	return removed, deprecated, nil
}
