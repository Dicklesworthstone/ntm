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

// classifyUndecodedKeys partitions the strict loader's undecoded key list
// into recognized removed knobs (warn, ignore value) and genuinely unknown
// fields (still a hard load error). When a removed table prefix has concrete
// child keys present, only the children are reported (one warning per key the
// user actually wrote); the bare table header is reported only when it
// appears alone.
func classifyUndecodedKeys(fields []string) (removed []RemovedKnob, unknown []string) {
	matched := make(map[string]string) // key -> disposition
	for _, key := range fields {
		if disp, ok := removedKnobExact[key]; ok {
			matched[key] = disp
			continue
		}
		if disp, ok := matchRemovedPrefix(key); ok {
			matched[key] = disp
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
		removed = append(removed, RemovedKnob{Key: key, Disposition: matched[key]})
	}
	return removed, unknown
}

func matchRemovedPrefix(key string) (string, bool) {
	for prefix, disp := range removedKnobPrefixes {
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

// ScanRemovedKnobs reports the removed config knobs present in the config
// file at path (empty = DefaultPath). It is the `ntm doctor` surface: same
// classification and disposition text as the strict-loader error, but it
// decodes leniently, so it works on exactly the configs the strict loader
// refuses since v1.27.0. A missing config file yields no knobs; an
// unparseable file yields an error.
func ScanRemovedKnobs(path string) ([]RemovedKnob, error) {
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
	removed, _ := classifyUndecodedKeys(undecodedConfigFields(md))
	return removed, nil
}
