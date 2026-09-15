package config

import (
	"reflect"
	"strings"
	"testing"
)

// collectTOMLLeafPaths walks a config struct type and returns every dotted
// `toml` path an operator could reasonably type at `ntm config get`.
//
// Maps are reported as their own leaf (the container is addressable; arbitrary
// user-chosen keys inside it obviously are not enumerable). Slices are leaves
// for the same reason. Recursion is depth-bounded so a self-referential type
// can never hang the suite.
func collectTOMLLeafPaths(t reflect.Type, prefix string, depth int, out *[]string) {
	if depth > 8 {
		return
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue // unexported
		}
		tag := field.Tag.Get("toml")
		if tag == "-" {
			continue
		}
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			tag = tag[:comma]
		}
		if tag == "" {
			tag = strings.ToLower(field.Name)
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}

		ft := field.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() == t.PkgPath() {
			// Nested config struct: recurse, and also record the container
			// itself, which GetValue is expected to return whole.
			*out = append(*out, path)
			collectTOMLLeafPaths(ft, path, depth+1, out)
			continue
		}
		*out = append(*out, path)
	}
}

// TestGetValueReachesEveryTOMLLeaf pins the property that made the hand-written
// switch in GetValue a liability: reachability must follow from the struct, not
// from someone remembering to add a case.
//
// Before the reflection fallback, `agents.claude_isolate_credentials` and
// `agents.claude_token_file` — both live, both read at spawn time by
// internal/swarm/claude_config_home.go — answered "unknown config path", as did
// every key under [command_hooks], [retry] and [routing]. An operator checking a
// documented key with `ntm config get` was told it did not exist.
func TestGetValueReachesEveryTOMLLeaf(t *testing.T) {
	cfg := Default()
	if cfg == nil {
		t.Fatal("Default() returned nil")
	}

	var paths []string
	collectTOMLLeafPaths(reflect.TypeOf(*cfg), "", 0, &paths)
	if len(paths) < 100 {
		t.Fatalf("expected the config struct to expose many paths, collected only %d", len(paths))
	}

	var unreachable []string
	for _, path := range paths {
		// A key that was deliberately removed is reported with its disposition
		// rather than returned; that is the documented behaviour, not a gap.
		if _, _, dead := classifyDeadKey(path); dead {
			continue
		}
		if _, err := GetValue(cfg, path); err != nil {
			unreachable = append(unreachable, path+": "+err.Error())
		}
	}

	if len(unreachable) > 0 {
		t.Errorf("%d config path(s) exist in the struct but are not readable via GetValue:", len(unreachable))
		for _, u := range unreachable {
			t.Errorf("  %s", u)
		}
	}
}

// TestGetValueRegressionPaths pins the specific keys that regressed, so the
// fix cannot be undone by a future refactor of the resolver.
func TestGetValueRegressionPaths(t *testing.T) {
	cfg := Default()

	for _, path := range []string{
		"agents.claude_isolate_credentials",
		"agents.claude_token_file",
		"agent_mail.pane_badges", // *bool: unset is an answer, not "unknown path"
		"context.ms_skills",
		"command_hooks",          // whole sections the switch never indexed
		"retry.max_attempts",     //
		"routing.context_weight", //
	} {
		if _, err := GetValue(cfg, path); err != nil {
			t.Errorf("GetValue(%q) = error %v; want it readable", path, err)
		}
	}
}

// TestGetValueOverridesStillApply guards the two paths that must not return the
// raw struct field.
func TestGetValueOverridesStillApply(t *testing.T) {
	cfg := Default()
	cfg.AgentMail.Token = "super-secret-token"

	got, err := GetValue(cfg, "agent_mail.token")
	if err != nil {
		t.Fatalf("GetValue(agent_mail.token): %v", err)
	}
	if got != "[redacted]" {
		t.Errorf("agent_mail.token = %v; want [redacted] — the token must never be printed", got)
	}

	got, err = GetValue(cfg, "agent_mail.supervisor_enabled")
	if err != nil {
		t.Fatalf("GetValue(agent_mail.supervisor_enabled): %v", err)
	}
	if want := cfg.AgentMail.SupervisorEnabledOrDefault(); got != want {
		t.Errorf("agent_mail.supervisor_enabled = %v; want the effective value %v", got, want)
	}
}

// TestGetValueRejectsUnknownAndDeadPaths keeps the two distinct failure
// messages distinct: an unknown path is a typo, a dead key names its
// replacement.
func TestGetValueRejectsUnknownAndDeadPaths(t *testing.T) {
	cfg := Default()

	if _, err := GetValue(cfg, "definitely.not.a.key"); err == nil {
		t.Error("GetValue on an unknown path returned no error")
	} else if !strings.Contains(err.Error(), "unknown config path") {
		t.Errorf("unknown path error = %q; want it to say 'unknown config path'", err)
	}

	if _, err := GetValue(cfg, "memory.include_in_recovery"); err == nil {
		t.Error("GetValue on a removed key returned no error")
	} else if !strings.Contains(err.Error(), "config migrate") {
		t.Errorf("dead key error = %q; want it to point at 'ntm config migrate'", err)
	}

	if _, err := GetValue(cfg, ""); err == nil {
		t.Error("GetValue on an empty path returned no error")
	}
	if _, err := GetValue(nil, "theme"); err == nil {
		t.Error("GetValue on a nil config returned no error")
	}
}

// TestGetValueTraversesPluginMap covers the map-traversal branch: a custom
// agent command is addressable by name rather than only as a whole map.
func TestGetValueTraversesPluginMap(t *testing.T) {
	cfg := Default()
	if cfg.Agents.Plugins == nil {
		cfg.Agents.Plugins = map[string]string{}
	}
	cfg.Agents.Plugins["mytool"] = "mytool --run"

	got, err := GetValue(cfg, "agents.plugins.mytool")
	if err != nil {
		t.Fatalf("GetValue(agents.plugins.mytool): %v", err)
	}
	if got != "mytool --run" {
		t.Errorf("agents.plugins.mytool = %v; want the registered command", got)
	}

	if _, err := GetValue(cfg, "agents.plugins.absent"); err == nil {
		t.Error("a missing plugin key should not resolve")
	}
}
