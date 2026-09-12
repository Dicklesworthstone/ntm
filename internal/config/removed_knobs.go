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

// recoveryAliasKnobExact is the memory.*-into-[recovery] alias batch
// (ntm#323). Unlike every other dead knob these keys DID have an effect: they
// were folded into their [recovery] counterparts behind a per-key warning
// that promised removal in v1.27.0 and then shipped, unremoved and still
// emitted by `ntm config init`, all the way to v1.33.1 — so a brand-new
// config warned on every invocation while `config migrate` called it clean.
//
// Their dispositions therefore name the replacement instead of claiming the
// key was inert, and `config migrate` can finally strip them.
//
// memory.query_timeout_seconds is deliberately absent: it was never purely a
// recovery alias (internal/cli reads it to bound the cm query behind
// send-time injection), so it stays live in [memory].
// Dispositions read as the predicate of "config key X was ...", so they are
// phrased to complete that sentence.
var recoveryAliasKnobExact = map[string]string{
	"memory.include_in_recovery": "superseded by recovery.include_cm_memories",
	"memory.max_rules":           "superseded by recovery.max_cm_rules",
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

// Dead-key tier names shared by the strict loader, doctor, and config migrate.
const (
	// DeadKeyTierRemoved is the v1.26.0 removal batch (error since v1.27.0).
	DeadKeyTierRemoved = "removed"
	// DeadKeyTierDeprecated is the v1.28.0 batch (bd-6otuk; error since v1.29.0).
	DeadKeyTierDeprecated = "deprecated"
	// DeadKeyTierRecoveryAlias is the memory.*-into-[recovery] alias batch,
	// removed outright rather than run through a warn release: the warning
	// had already been shipping since v1.26.0 (ntm#323).
	DeadKeyTierRecoveryAlias = "recovery-alias"
)

// deadKeyTier is one removal batch: its key sets and the release provenance
// its error line cites.
//
// Batches are a table rather than a hand-written branch per tier because the
// release text is the part that goes stale — the memory.* aliases shipped a
// warning promising removal "in v1.27.0" and were still being written by
// `ntm config init` at v1.33.1 (ntm#323). A new batch is one entry here, and
// it cannot borrow another batch's release claim.
type deadKeyTier struct {
	name     string
	exact    map[string]string
	prefixes map[string]string
	// provenance completes the error line after the disposition, in
	// parentheses, naming when the key went away and where to read about it.
	provenance string
}

// deadKeyTiers is ordered: earlier batches classify first, so a key that
// somehow appears in two batches reports under the older one.
var deadKeyTiers = []deadKeyTier{
	{
		name:       DeadKeyTierRemoved,
		exact:      removedKnobExact,
		prefixes:   removedKnobPrefixes,
		provenance: "removed in v1.26.0, a config error since v1.27.0; see the v1.26.0 removed-key migration table in CHANGELOG.md",
	},
	{
		name:       DeadKeyTierDeprecated,
		exact:      deprecatedKnobExact,
		prefixes:   deprecatedKnobPrefixes,
		provenance: "deprecated in v1.28.0, a config error since v1.29.0; see the v1.28.0 dead-knob migration table in CHANGELOG.md",
	},
	{
		name:  DeadKeyTierRecoveryAlias,
		exact: recoveryAliasKnobExact,
		// Deliberately cites the issue rather than a release number. The
		// predecessor of this batch promised removal "in v1.27.0" and then
		// shipped that promise for seven more releases (ntm#323); naming a
		// version that has not been cut yet is how that happens. The
		// CHANGELOG entry carries the release.
		provenance: "removed with the memory.*/[recovery] split (ntm#323); the [recovery] replacement carries the same value",
	},
}

// classifyDeadKey reports whether a dotted config key is a known dead knob in
// any removal batch, together with its disposition and tier name. Shared by
// the strict-loader classification and `ntm config migrate`.
func classifyDeadKey(key string) (disposition, tier string, ok bool) {
	for _, t := range deadKeyTiers {
		if disp, found := t.exact[key]; found {
			return disp, t.name, true
		}
		if disp, found := matchPrefix(key, t.prefixes); found {
			return disp, t.name, true
		}
	}
	return "", "", false
}

// deadKeyTierProvenance returns the release text for a tier name.
func deadKeyTierProvenance(tier string) string {
	for _, t := range deadKeyTiers {
		if t.name == tier {
			return t.provenance
		}
	}
	return ""
}

// DeadKeyLoadError is the strict-loader failure for a config file containing
// removed (v1.26.0 batch) and/or deprecated (v1.28.0 batch) keys, plus any
// genuinely unknown fields found in the same pass. Its Error() text is
// byte-identical to the historical fmt.Errorf message, so robot JSON error
// envelopes and doctor are unchanged; the type exists so the human CLI
// fallback (root.go PersistentPreRunE) can collapse the multi-line detail
// into a single actionable line pointing at `ntm config migrate`.
type DeadKeyLoadError struct {
	// ByTier holds the dead keys found, grouped by removal batch and keyed
	// by DeadKeyTier* name. Grouping keeps each batch's error lines citing
	// its own release provenance instead of borrowing another batch's.
	ByTier  map[string][]RemovedKnob
	Unknown []string
}

func (e *DeadKeyLoadError) Error() string {
	var msgs []string
	if len(e.Unknown) > 0 {
		msgs = append(msgs, "unknown field(s): "+strings.Join(e.Unknown, ", "))
	}
	// Tier order, not map order, so the message is deterministic.
	for _, t := range deadKeyTiers {
		for _, knob := range e.ByTier[t.name] {
			msgs = append(msgs, deadKnobErrorLine(knob, t.provenance))
		}
	}
	return "parsing config: " + strings.Join(msgs, "\n")
}

// DeadKeyCount is the number of dead keys in the failure across every batch
// (unknown fields excluded — those are not migratable no-ops).
func (e *DeadKeyLoadError) DeadKeyCount() int {
	n := 0
	for _, knobs := range e.ByTier {
		n += len(knobs)
	}
	return n
}

// classifyUndecodedKeys partitions the strict loader's undecoded key list
// into recognized removed knobs (hard error since v1.27.0), recognized
// deprecated knobs (v1.28.0 batch: hard error since v1.29.0, with its own
// release-pair error text), and genuinely unknown fields (still a hard load
// error). When a removed/deprecated table prefix
// has concrete child keys present, only the children are reported (one line
// per key the user actually wrote); the bare table header is reported only
// when it appears alone.
func classifyUndecodedKeys(fields []string) (byTier map[string][]RemovedKnob, unknown []string) {
	type match struct {
		disposition string
		tier        string
	}
	matched := make(map[string]match)
	for _, key := range fields {
		if disp, tier, ok := classifyDeadKey(key); ok {
			matched[key] = match{disposition: disp, tier: tier}
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

	byTier = make(map[string][]RemovedKnob, len(deadKeyTiers))
	for _, key := range keys {
		if hasStrictChild(key, keys) {
			continue
		}
		m := matched[key]
		byTier[m.tier] = append(byTier[m.tier], RemovedKnob{Key: key, Disposition: m.disposition})
	}
	return byTier, unknown
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

// deadKnobErrorLine renders the strict-loader error line for one dead knob.
// The key + disposition text is IDENTICAL to the deprecation warning the
// error replaces — only the severity changed — and the provenance clause is
// the knob's OWN batch, never a neighbouring batch's release numbers. A
// borrowed release claim is how the memory.* aliases ended up telling users
// for seven releases that they had been removed in v1.27.0 (ntm#323).
func deadKnobErrorLine(knob RemovedKnob, provenance string) string {
	return fmt.Sprintf(
		"config key %s was %s — delete it from your config file (%s)",
		knob.Key, knob.Disposition, provenance)
}

// ScanRemovedKnobs reports the removed config knobs present in the config
// file at path (empty = DefaultPath). It is the `ntm doctor` surface: same
// classification and disposition text as the strict-loader error, but it
// decodes leniently, so it works on exactly the configs the strict loader
// refuses since v1.27.0. A missing config file yields no knobs; an
// unparseable file yields an error.
// ScanRemovedKnobs also reports the recovery-alias batch (ntm#323), whose
// keys are removals in the same sense: doctor lists everything the user must
// delete, and the per-tier release text lives in the strict-loader error.
func ScanRemovedKnobs(path string) ([]RemovedKnob, error) {
	byTier, err := scanKnobs(path)
	if err != nil {
		return nil, err
	}
	knobs := append([]RemovedKnob(nil), byTier[DeadKeyTierRemoved]...)
	return append(knobs, byTier[DeadKeyTierRecoveryAlias]...), nil
}

// ScanDeprecatedKnobs reports the v1.28.0-batch deprecated (bd-6otuk) config
// knobs present in the config file at path (empty = DefaultPath). Same
// lenient decode and disposition text as ScanRemovedKnobs — the `ntm doctor`
// remediation surface, which keeps working on exactly the configs the strict
// loader refuses since these keys became hard errors in v1.29.0.
func ScanDeprecatedKnobs(path string) ([]RemovedKnob, error) {
	byTier, err := scanKnobs(path)
	if err != nil {
		return nil, err
	}
	return byTier[DeadKeyTierDeprecated], nil
}

func scanKnobs(path string) (map[string][]RemovedKnob, error) {
	if path == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cfg := &Config{}
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	byTier, _ := classifyUndecodedKeys(undecodedConfigFields(md))
	return byTier, nil
}
