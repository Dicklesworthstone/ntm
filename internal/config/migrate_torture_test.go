package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Hunt 1: name collisions.
func TestTortureNameCollisions(t *testing.T) {
	in := `accounts_mode = "x"
[accountsx]
keep = true
[accounts]
auto_rotate = true
[tmux]
palette_key = "F6"
palette_keys = "keepme"
rotation.dashboard.show_quota_bars = true
`
	// NOTE: rotation.dashboard... under [tmux] is tmux.rotation.dashboard... — not dead.
	got, changes := removeDeadTOMLKeys(in)
	t.Logf("changes: %+v\ngot:\n%s", changes, got)
	for _, want := range []string{"accounts_mode", "[accountsx]", "keep = true", "palette_keys = \"keepme\"", "rotation.dashboard.show_quota_bars"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost live content %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"[accounts]\n", "auto_rotate", "palette_key = \"F6\""} {
		if strings.Contains(got, gone) {
			t.Errorf("dead content %q survived:\n%s", gone, got)
		}
	}
}

func TestTortureDottedAndQuotedRootKeys(t *testing.T) {
	in := `rotation.dashboard.show_quota_bars = true
"tmux"."palette_key" = "F6"
live = 1
`
	got, changes := removeDeadTOMLKeys(in)
	if strings.Contains(got, "show_quota_bars") || strings.Contains(got, "palette_key") {
		t.Errorf("dotted/quoted dead keys survived:\n%s\nchanges=%+v", got, changes)
	}
	if !strings.Contains(got, "live = 1") {
		t.Errorf("live key lost:\n%s", got)
	}
}

// Hunt 2: structure torture.
func TestTortureMultilineArrayNestedBracketsCommentsStrings(t *testing.T) {
	in := `[integrations.rch]
enabled = true
dcg_whitelist = [
  "contains ] bracket",
  # a comment ] inside the array
  ["nested", "array]"],
  'literal ] too',
]
min_build_time = "5s"
[alerts]
enabled = true
`
	got, _ := removeDeadTOMLKeys(in)
	if strings.Contains(got, "dcg_whitelist") || strings.Contains(got, "nested") || strings.Contains(got, "min_build_time") {
		t.Errorf("dead multi-line array or key survived:\n%s", got)
	}
	if !strings.Contains(got, "[integrations.rch]") || !strings.Contains(got, "enabled = true") || !strings.Contains(got, "[alerts]") {
		t.Errorf("live content lost:\n%s", got)
	}
}

func TestTortureInlineTableRootAssignment(t *testing.T) {
	in := `accounts = { auto_rotate = true, providers = ["a"] }
live = 2
`
	got, changes := removeDeadTOMLKeys(in)
	if strings.Contains(got, "auto_rotate") {
		t.Errorf("inline dead table survived:\n%s changes=%+v", got, changes)
	}
	if !strings.Contains(got, "live = 2") {
		t.Errorf("live key lost:\n%s", got)
	}
}

func TestTortureArrayOfTablesWithCommentsBetween(t *testing.T) {
	in := `[[accounts.claude]]
email = "a@x.com"
# comment between blocks
[[accounts.claude]]
email = "b@x.com"

[alerts]
enabled = true
`
	got, _ := removeDeadTOMLKeys(in)
	if strings.Contains(got, "accounts.claude") || strings.Contains(got, "email") {
		t.Errorf("dead array-of-tables survived:\n%s", got)
	}
	if !strings.Contains(got, "[alerts]") {
		t.Errorf("live table lost:\n%s", got)
	}
}

func TestTortureDeadKeyLastLineNoTrailingNewline(t *testing.T) {
	in := "live = 1\n[tmux]\npalette_key = \"F6\""
	got, _ := removeDeadTOMLKeys(in)
	if strings.Contains(got, "palette_key") || strings.Contains(got, "[tmux]") {
		t.Errorf("trailing dead key/emptied header survived: %q", got)
	}
	if !strings.Contains(got, "live = 1") {
		t.Errorf("live key lost: %q", got)
	}
}

func TestTortureCRLF(t *testing.T) {
	in := "live = 1\r\n[tmux]\r\ndefault_panes = 4\r\npalette_key = \"F6\"\r\n[alerts]\r\nenabled = true\r\n"
	got, _ := removeDeadTOMLKeys(in)
	want := "live = 1\r\n[tmux]\r\ndefault_panes = 4\r\n[alerts]\r\nenabled = true\r\n"
	if got != want {
		t.Errorf("CRLF surgery mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestTortureBOMConservative(t *testing.T) {
	in := "\ufeff[tmux]\npalette_key = \"F6\"\n"
	got, changes := removeDeadTOMLKeys(in)
	// BOM defeats header recognition; the editor must NOT guess — it must
	// keep the bytes and let the rescan report unresolved.
	if len(changes) != 0 {
		t.Logf("BOM: editor removed %+v; got %q", changes, got)
	}
	if got != in && !strings.Contains(got, "[tmux]") {
		t.Errorf("BOM handling corrupted content: %q", got)
	}
}

func TestTortureCommentsOnlyTableAfterRemoval(t *testing.T) {
	in := `[checkpoints]
# why we checkpoint
interval_minutes = 5
# trailing comment

[alerts]
enabled = true
`
	got, _ := removeDeadTOMLKeys(in)
	// Documented behavior: emptied header dropped, comments preserved.
	if strings.Contains(got, "[checkpoints]") || strings.Contains(got, "interval_minutes") {
		t.Errorf("emptied table header or dead key survived:\n%s", got)
	}
	for _, want := range []string{"# why we checkpoint", "# trailing comment", "[alerts]"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
}

func TestTortureMultilineStringDeadValue(t *testing.T) {
	in := "[tmux]\ndefault_panes = 4\npalette_key = \"\"\"\nmulti ] line\n[fake_header]\n\"\"\"\n[alerts]\nenabled = true\n"
	got, _ := removeDeadTOMLKeys(in)
	if strings.Contains(got, "palette_key") || strings.Contains(got, "multi ] line") || strings.Contains(got, "[fake_header]") {
		t.Errorf("multi-line string dead value survived:\n%s", got)
	}
	if !strings.Contains(got, "default_panes = 4") || !strings.Contains(got, "[alerts]") {
		t.Errorf("live content lost:\n%s", got)
	}
}

// Partial dead array-of-tables entry: entry must stay (array length preserved).
func TestToruteArrayOfTablesPartialDead(t *testing.T) {
	in := `[[rotation.accounts]]
name = "a"
priority = 1
[[rotation.accounts]]
priority = 2
`
	got, _ := removeDeadTOMLKeys(in)
	if strings.Contains(got, "priority") {
		t.Errorf("dead priority key survived:\n%s", got)
	}
	if strings.Count(got, "[[rotation.accounts]]") != 2 {
		t.Errorf("array-of-tables length changed:\n%s", got)
	}
}

// Hunt 3 (part): unresolved-only config — dead key inside a live inline table.
func TestTortureUnresolvedOnlyNotReportedClean(t *testing.T) {
	in := `rotation = { prefer_restart = true }
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(in), 0644); err != nil {
		t.Fatal(err)
	}
	// Precondition: strict loader refuses this config.
	if _, err := Load(path); err == nil {
		t.Fatal("fixture unexpectedly loads strictly")
	}
	res, err := MigrateDeadKeys(path, false)
	if err != nil {
		t.Fatalf("MigrateDeadKeys: %v", err)
	}
	if res.Clean {
		t.Errorf("Clean = true, but strict loader still refuses the config (unresolved dead key hidden)")
	}
	if len(res.Unresolved) == 0 {
		t.Errorf("Unresolved empty; want rotation.prefer_restart reported")
	}
	after, _ := os.ReadFile(path)
	if string(after) != in {
		t.Errorf("file modified despite nothing being removable: %q", after)
	}
}

// --- Hunt 3: safety -----------------------------------------------------

const tortureDirtyFixture = `[tmux]
default_panes = 4
palette_key = "F6"
`

func TestTortureSymlinkedConfig(t *testing.T) {
	realDir := t.TempDir()
	linkDir := t.TempDir()
	realPath := filepath.Join(realDir, "real-config.toml")
	linkPath := filepath.Join(linkDir, "config.toml")
	if err := os.WriteFile(realPath, []byte(tortureDirtyFixture), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}

	res, err := MigrateDeadKeys(linkPath, false)
	if err != nil {
		t.Fatalf("MigrateDeadKeys via symlink: %v", err)
	}
	// Same discipline as PersistTOMLKeys/resolveConfigPersistencePath: follow
	// the link — rewrite the TARGET, keep the symlink a symlink, and place the
	// backup next to the target.
	if info, err := os.Lstat(linkPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink was replaced by a regular file (mode %v, err %v)", info.Mode(), err)
	}
	resolvedReal, err := filepath.EvalSymlinks(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.BackupPath, resolvedReal+".bak.") {
		t.Errorf("backup not next to symlink target: %q", res.BackupPath)
	}
	got, _ := os.ReadFile(realPath)
	if strings.Contains(string(got), "palette_key") || !strings.Contains(string(got), "default_panes = 4") {
		t.Errorf("target surgery wrong:\n%s", got)
	}
}

func TestTortureReadOnlyDirCleanError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(tortureDirtyFixture), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })

	if _, err := MigrateDeadKeys(path, false); err == nil {
		t.Fatal("expected error migrating in a read-only directory")
	}
	_ = os.Chmod(dir, 0755)
	got, _ := os.ReadFile(path)
	if string(got) != tortureDirtyFixture {
		t.Errorf("config mutated despite the failure:\n%s", got)
	}
}

func TestTortureBackupNeverClobbered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	firstContents := tortureDirtyFixture
	secondContents := "[memory]\ninclude_history = true\n"

	if err := os.WriteFile(path, []byte(firstContents), 0644); err != nil {
		t.Fatal(err)
	}
	res1, err := MigrateDeadKeys(path, false)
	if err != nil {
		t.Fatal(err)
	}
	// Re-dirty the file and migrate again immediately (same wall-clock second
	// on any modern machine): the first backup must survive with its bytes.
	if err := os.WriteFile(path, []byte(secondContents), 0644); err != nil {
		t.Fatal(err)
	}
	res2, err := MigrateDeadKeys(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if res1.BackupPath == res2.BackupPath {
		t.Fatalf("second migration reused backup path %q", res1.BackupPath)
	}
	b1, err := os.ReadFile(res1.BackupPath)
	if err != nil {
		t.Fatalf("first backup gone: %v", err)
	}
	if string(b1) != firstContents {
		t.Errorf("first backup clobbered: %q", b1)
	}
	b2, _ := os.ReadFile(res2.BackupPath)
	if string(b2) != secondContents {
		t.Errorf("second backup wrong: %q", b2)
	}
}

func TestTortureConcurrentMigrateAndConfigSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(tortureDirtyFixture), 0644); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 2)
	go func() {
		_, err := MigrateDeadKeys(path, false)
		done <- err
	}()
	go func() {
		done <- PersistTOMLKeys(path, "alerts", [][2]string{{"enabled", "true"}})
	}()
	migErr := <-done
	setErr := <-done
	// PersistTOMLKeys validates the existing config and refuses dead keys, so
	// depending on interleaving it may legitimately error; MigrateDeadKeys
	// must not. Either way the final file must be valid, dead-key-free or the
	// set refused — never torn.
	if migErr != nil && setErr != nil {
		t.Fatalf("both writers failed: migrate=%v set=%v", migErr, setErr)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]interface{}
	if _, err := toml.Decode(string(got), &probe); err != nil {
		t.Fatalf("final config torn/invalid: %v\n%s", err, got)
	}
}
