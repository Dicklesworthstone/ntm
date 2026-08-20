package config

// migrate_test.go — `ntm config migrate` engine tests
// (bd-config-migrate-warning-wall-151x2): surgical dead-key removal must
// preserve every live key, comment, and blank line byte-for-byte, drop dead
// tables and array-of-tables blocks whole, drop emptied table headers, back
// up first, and no-op cleanly on clean configs.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// migrateFixtureMixed mixes live keys with comments, removed-tier keys
// (v1.26.0 batch), deprecated-tier keys (v1.28.0 batch, bd-6otuk), dead
// tables, a dead array-of-tables block, a multi-line dead array value, and a
// table that becomes empty after removal.
const migrateFixtureMixed = `# NTM global config
projects_base = "~/projects" # keep this comment

[tmux]
default_panes = 6 # live key comment
palette_key = "F6"

[memory]
enabled = true
include_anti_patterns = true

[rotation]
enabled = true

[rotation.dashboard]
port = 8080

[integrations.caut]
enabled = true

[integrations.rch]
enabled = true
dcg_whitelist = [
  "one",
  "two",
]

[checkpoints]
interval_minutes = 5
[[accounts.claude]]
email = "a@example.com"

[alerts]
enabled = true
`

// migrateFixtureMixedCleaned is the byte-exact expected output: live keys and
// their comments untouched, dead assignments (including the multi-line array)
// gone, dead table blocks gone, and the emptied [checkpoints] header gone.
const migrateFixtureMixedCleaned = `# NTM global config
projects_base = "~/projects" # keep this comment

[tmux]
default_panes = 6 # live key comment

[memory]
enabled = true

[rotation]
enabled = true

[integrations.rch]
enabled = true

[alerts]
enabled = true
`

func writeMigrateFixture(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func backupFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var backups []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".bak.") {
			backups = append(backups, filepath.Join(dir, entry.Name()))
		}
	}
	return backups
}

func TestMigrateDeadKeysSurgical(t *testing.T) {
	path := writeMigrateFixture(t, migrateFixtureMixed)

	// Precondition: the strict loader refuses this config with a typed
	// dead-key error (the incident's failure mode).
	if _, err := Load(path); err == nil {
		t.Fatal("fixture unexpectedly loads strictly; dead keys missing from fixture")
	}

	result, err := MigrateDeadKeys(path, false)
	if err != nil {
		t.Fatalf("MigrateDeadKeys: %v", err)
	}
	if result.Clean {
		t.Fatal("result.Clean = true for a dirty config")
	}
	if len(result.Unresolved) != 0 {
		t.Fatalf("Unresolved = %+v, want none", result.Unresolved)
	}

	// Backup: written next to the file, named <path>.bak.<unix>, containing
	// the original bytes.
	if result.BackupPath == "" || !strings.HasPrefix(result.BackupPath, path+".bak.") {
		t.Fatalf("BackupPath = %q, want prefix %q", result.BackupPath, path+".bak.")
	}
	backupData, err := os.ReadFile(result.BackupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backupData) != migrateFixtureMixed {
		t.Fatal("backup does not contain the original config bytes")
	}

	// Surgery: byte-exact — live keys, comments, ordering, and blank-line
	// structure preserved; every dead key line, dead table block, and the
	// emptied [checkpoints] header removed.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	if string(got) != migrateFixtureMixedCleaned {
		t.Fatalf("migrated config mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, migrateFixtureMixedCleaned)
	}

	// Every dead key reported once, with tier + disposition, in file order.
	wantKeys := map[string]string{
		"tmux.palette_key":               DeadKeyTierRemoved,
		"memory.include_anti_patterns":   DeadKeyTierRemoved,
		"rotation.dashboard":             DeadKeyTierRemoved,
		"integrations.caut":              DeadKeyTierRemoved,
		"integrations.rch.dcg_whitelist": DeadKeyTierDeprecated,
		"checkpoints.interval_minutes":   DeadKeyTierDeprecated,
		"accounts.claude":                DeadKeyTierDeprecated,
	}
	if len(result.Changes) != len(wantKeys) {
		t.Fatalf("Changes = %d entries (%+v), want %d", len(result.Changes), result.Changes, len(wantKeys))
	}
	for _, change := range result.Changes {
		tier, ok := wantKeys[change.Key]
		if !ok {
			t.Errorf("unexpected change key %q", change.Key)
			continue
		}
		if change.Tier != tier {
			t.Errorf("change %q tier = %q, want %q", change.Key, change.Tier, tier)
		}
		if strings.TrimSpace(change.Disposition) == "" {
			t.Errorf("change %q has empty disposition", change.Key)
		}
	}

	// Postcondition: the strict loader accepts the migrated config.
	if _, err := Load(path); err != nil {
		t.Fatalf("strict Load after migrate: %v", err)
	}
}

func TestMigrateDeadKeysDryRunTouchesNothing(t *testing.T) {
	path := writeMigrateFixture(t, migrateFixtureMixed)

	result, err := MigrateDeadKeys(path, true)
	if err != nil {
		t.Fatalf("MigrateDeadKeys dry-run: %v", err)
	}
	if result.Clean {
		t.Fatal("dry-run Clean = true for a dirty config")
	}
	if len(result.Changes) == 0 {
		t.Fatal("dry-run reported no changes for a dirty config")
	}
	if result.BackupPath != "" {
		t.Fatalf("dry-run BackupPath = %q, want empty", result.BackupPath)
	}
	if result.Updated != migrateFixtureMixedCleaned {
		t.Fatalf("dry-run Updated mismatch:\n--- got ---\n%s\n--- want ---\n%s", result.Updated, migrateFixtureMixedCleaned)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after dry-run: %v", err)
	}
	if string(got) != migrateFixtureMixed {
		t.Fatal("dry-run modified the config file")
	}
	if backups := backupFilesIn(t, filepath.Dir(path)); len(backups) != 0 {
		t.Fatalf("dry-run created backup file(s): %v", backups)
	}
}

func TestMigrateDeadKeysCleanConfigNoop(t *testing.T) {
	clean := `# clean config
projects_base = "~/projects"

[tmux]
default_panes = 4
`
	path := writeMigrateFixture(t, clean)

	result, err := MigrateDeadKeys(path, false)
	if err != nil {
		t.Fatalf("MigrateDeadKeys: %v", err)
	}
	if !result.Clean {
		t.Fatalf("Clean = false for a clean config; changes = %+v", result.Changes)
	}
	if len(result.Changes) != 0 {
		t.Fatalf("Changes = %+v, want none", result.Changes)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != clean {
		t.Fatal("clean-config migrate modified the file")
	}
	if backups := backupFilesIn(t, filepath.Dir(path)); len(backups) != 0 {
		t.Fatalf("clean-config migrate created backup file(s): %v", backups)
	}
}

func TestMigrateDeadKeysMissingFileIsClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	result, err := MigrateDeadKeys(path, false)
	if err != nil {
		t.Fatalf("MigrateDeadKeys on missing file: %v", err)
	}
	if !result.Clean {
		t.Fatal("missing file should be a clean no-op")
	}
}

func TestMigrateDeadKeysInvalidTOMLRefused(t *testing.T) {
	path := writeMigrateFixture(t, "not = [valid\n")
	if _, err := MigrateDeadKeys(path, false); err == nil {
		t.Fatal("expected error for syntactically invalid TOML")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "not = [valid\n" {
		t.Fatal("invalid-TOML migrate modified the file")
	}
}

// TestDeadKeyLoadErrorTextUnchanged pins the strict-loader error text: the
// typed DeadKeyLoadError must render byte-identically to the historical
// fmt.Errorf message so robot JSON envelopes and doctor output are unchanged.
func TestDeadKeyLoadErrorTextUnchanged(t *testing.T) {
	err := &DeadKeyLoadError{
		Removed:    []RemovedKnob{{Key: "tmux.palette_key", Disposition: noEffect}},
		Deprecated: []RemovedKnob{{Key: "cass.show_install_hints", Disposition: noEffect}},
		Unknown:    []string{"totally.unknown"},
	}
	want := "parsing config: " + strings.Join([]string{
		"unknown field(s): totally.unknown",
		removedKnobErrorLine(RemovedKnob{Key: "tmux.palette_key", Disposition: noEffect}),
		deprecatedKnobErrorLine(RemovedKnob{Key: "cass.show_install_hints", Disposition: noEffect}),
	}, "\n")
	if err.Error() != want {
		t.Fatalf("DeadKeyLoadError text drifted:\n got: %q\nwant: %q", err.Error(), want)
	}
	if err.DeadKeyCount() != 2 {
		t.Fatalf("DeadKeyCount = %d, want 2", err.DeadKeyCount())
	}
}
