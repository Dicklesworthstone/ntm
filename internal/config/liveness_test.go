package config

// WS0-G2 config-key liveness test (bd-ws0-guards-klz98.3).
//
// Walks every leaf key of the parsed Config struct; each must be claimed via
// config.RegisterReader (see liveness.go) or carry a line in
// ci/allowlists/config.txt tied to an OPEN bead. The ratchet runs in BOTH
// directions with the same semantics as scripts/guards/lib/ratchet.sh
// (implemented here in Go because the walk requires reflection over the
// struct): an unclaimed key with no allowlist line is NEW DEBT; an allowlist
// line whose key is now claimed (or gone) is STALE and must be deleted in the
// same PR. CI coverage: the ordinary `test` job (go test ./...) and the
// `reality-guards-go` job both run this; the bare reality-guards job runs
// only the format side via scripts/guards/config_liveness.sh.

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// leafKeys returns the dotted TOML paths of every leaf key reachable from t.
// Rules: `toml:"-"` fields are skipped; nested structs (and pointers/slices/
// arrays of structs) recurse under their tag; time.Time, time.Duration, and
// every non-struct kind are leaves; maps are leaves (arbitrary user keys).
func leafKeys(t reflect.Type, prefix string, out map[string]bool) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		if prefix != "" {
			out[prefix] = true
		}
		return
	}
	if t.PkgPath() == "time" { // time.Time etc: opaque leaf
		out[prefix] = true
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("toml")
		if idx := strings.Index(tag, ","); idx >= 0 {
			tag = tag[:idx]
		}
		if tag == "-" {
			continue
		}
		name := tag
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		ft := f.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.Struct:
			if ft == reflect.TypeOf(time.Time{}) || ft.PkgPath() == "time" {
				out[key] = true
			} else {
				leafKeys(ft, key, out)
			}
		case reflect.Slice, reflect.Array:
			et := ft.Elem()
			for et.Kind() == reflect.Ptr {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct && et.PkgPath() != "time" {
				leafKeys(et, key, out)
			} else {
				out[key] = true
			}
		default:
			out[key] = true
		}
	}
}

// unclaimedKeys computes leaves(root) minus claimed, and fails t for any
// claimed key that does not correspond to a walked leaf (a claim for a
// removed/renamed knob is itself drift).
func unclaimedKeys(t *testing.T, root reflect.Type, claimed map[string]any) []string {
	t.Helper()
	leaves := map[string]bool{}
	leafKeys(root, "", leaves)
	for key, fn := range claimed {
		if !leaves[key] {
			t.Errorf("config liveness: RegisterReader(%q) claims a key that is not a leaf of the config struct — fix or remove the claim", key)
		}
		v := reflect.ValueOf(fn)
		if !v.IsValid() || v.Kind() != reflect.Func || v.IsNil() {
			t.Errorf("config liveness: claim for %q is not a non-nil function reference", key)
		}
	}
	var unclaimed []string
	for key := range leaves {
		if _, ok := claimed[key]; !ok {
			unclaimed = append(unclaimed, key)
		}
	}
	sort.Strings(unclaimed)
	return unclaimed
}

// allowlistEntries parses ci/allowlists/config.txt into entry->bead
// (non-permanent) and the set of permanent entries.
func allowlistEntries(t *testing.T, path string) (map[string]string, map[string]bool) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("config liveness: cannot open allowlist %s: %v", path, err)
	}
	defer f.Close()
	entries := map[string]string{}
	permanent := map[string]bool{}
	inPermanent := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, "permanent:") {
				inPermanent = true
			}
			continue
		}
		if trimmed == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			t.Errorf("config liveness: malformed allowlist line (want entry\\tbead\\treason): %q", line)
			continue
		}
		if inPermanent && fields[1] == "permanent" {
			permanent[fields[0]] = true
			continue
		}
		entries[fields[0]] = fields[1]
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("config liveness: reading %s: %v", path, err)
	}
	return entries, permanent
}

// TestConfigKeyLivenessCanary is the mandatory canary: a fixture knob with no
// reader MUST be caught by the walk, and a claimed knob must pass. If either
// assertion fails the gate is a placebo and the whole test file is untrusted.
func TestConfigKeyLivenessCanary(t *testing.T) {
	type fixtureSub struct {
		Orphan string `toml:"orphan_knob"` // deliberately reader-less
	}
	type fixture struct {
		Wired string     `toml:"wired_knob"`
		Sub   fixtureSub `toml:"sub"`
		Skip  string     `toml:"-"`
	}
	claimed := map[string]any{"wired_knob": func() {}}
	unclaimed := unclaimedKeys(t, reflect.TypeOf(fixture{}), claimed)
	if len(unclaimed) != 1 || unclaimed[0] != "sub.orphan_knob" {
		t.Fatalf("CANARY FAILURE — walk did not isolate the reader-less fixture knob; got unclaimed=%v (gate would be a placebo)", unclaimed)
	}
}

// TestConfigKeyLiveness is the G2 gate proper.
func TestConfigKeyLiveness(t *testing.T) {
	readerMu.Lock()
	claimed := make(map[string]any, len(readerRegistry))
	for k, v := range readerRegistry {
		claimed[k] = v
	}
	readerMu.Unlock()

	unclaimed := unclaimedKeys(t, reflect.TypeOf(Config{}), claimed)

	allowPath := filepath.Join("..", "..", "ci", "allowlists", "config.txt")
	entries, permanent := allowlistEntries(t, allowPath)

	if dump := os.Getenv("NTM_CONFIG_LIVENESS_DUMP"); dump != "" {
		var b strings.Builder
		for _, key := range unclaimed {
			b.WriteString(key + "\n")
		}
		if err := os.WriteFile(dump, []byte(b.String()), 0o644); err != nil {
			t.Fatalf("dump: %v", err)
		}
	}

	unclaimedSet := map[string]bool{}
	for _, key := range unclaimed {
		unclaimedSet[key] = true
		if permanent[key] {
			continue
		}
		if _, ok := entries[key]; !ok {
			t.Errorf("UNCLAIMED\t%s", key)
		}
	}
	var stale []string
	for entry := range entries {
		if !unclaimedSet[entry] {
			stale = append(stale, entry)
		}
	}
	sort.Strings(stale)
	for _, entry := range stale {
		t.Errorf("STALE\t%s\t%s", entry, entries[entry])
	}
	if t.Failed() {
		t.Log("config-key liveness ratchet (bd-ws0-guards-klz98.3):")
		t.Log("  UNCLAIMED = parsed key with no config.RegisterReader claim and no ci/allowlists/config.txt line.")
		t.Log("    Waiver protocol is bead-first: open a bead, then add '<key>\\t<bead-id>\\t<reason>'.")
		t.Log("    The real fix is a claim: config.RegisterReader(key, realReaderFunc) from the consuming package.")
		t.Log("  STALE = allowlist line whose key is now claimed or no longer parsed — delete it in this PR.")
	}
}
