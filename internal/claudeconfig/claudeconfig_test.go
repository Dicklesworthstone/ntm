package claudeconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveClaudeSettingsPathHonorsEnvVar(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/claude-alt")
	got, fromEnv, err := ResolveClaudeSettingsPath()
	if err != nil {
		t.Fatalf("ResolveClaudeSettingsPath: %v", err)
	}
	if !fromEnv {
		t.Errorf("fromEnv = false; want true")
	}
	if want := "/tmp/claude-alt/settings.json"; got != want {
		t.Errorf("path = %q; want %q", got, want)
	}
}

func TestResolveClaudeSettingsPathIgnoresBlankEnvVar(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "   ")
	got, fromEnv, err := ResolveClaudeSettingsPath()
	if err != nil {
		t.Fatalf("ResolveClaudeSettingsPath: %v", err)
	}
	if fromEnv {
		t.Errorf("fromEnv = true; want false for blank env")
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".claude", "settings.json"); got != want {
		t.Errorf("path = %q; want %q", got, want)
	}
}

func TestReadModelAbsentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	model, hasModel, err := ReadModel(path)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if hasModel || model != "" {
		t.Errorf("absent file -> (%q, %t); want (\"\", false)", model, hasModel)
	}
}

func TestReadModelEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	model, hasModel, err := ReadModel(path)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if hasModel || model != "" {
		t.Errorf("empty file -> (%q, %t); want (\"\", false)", model, hasModel)
	}
}

func TestReadModelMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadModel(path)
	if err == nil {
		t.Errorf("ReadModel on malformed JSON: expected error, got nil")
	}
}

func TestReadModelMissingField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	model, hasModel, err := ReadModel(path)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if hasModel || model != "" {
		t.Errorf("missing field -> (%q, %t); want (\"\", false)", model, hasModel)
	}
}

func TestReadModelPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"opus-4.7","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	model, hasModel, err := ReadModel(path)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if !hasModel || model != "opus-4.7" {
		t.Errorf("got (%q, %t); want (\"opus-4.7\", true)", model, hasModel)
	}
}

func TestWriteModelPreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark","font_size":14}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteModel(path, "sonnet-4.6"); err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse post-write: %v", err)
	}
	if got["model"] != "sonnet-4.6" {
		t.Errorf("model = %v; want sonnet-4.6", got["model"])
	}
	if got["theme"] != "dark" {
		t.Errorf("theme not preserved: %v", got["theme"])
	}
	// json.Unmarshal parses numbers as float64 by default.
	if got["font_size"].(float64) != 14 {
		t.Errorf("font_size not preserved: %v", got["font_size"])
	}
}

func TestWriteModelEmptyStringRemovesField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"opus-4.7","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteModel(path, ""); err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["model"]; present {
		t.Errorf("model field should have been removed, got: %v", got["model"])
	}
	if got["theme"] != "dark" {
		t.Errorf("theme lost: %v", got["theme"])
	}
}

func TestWriteModelDoesNotCreateEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "settings.json")
	if err := WriteModel(path, ""); err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s not to exist, got stat err=%v", path, err)
	}
}

func TestWriteModelCreatesFileWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "settings.json")
	if err := WriteModel(path, "haiku-4.5"); err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	model, hasModel, err := ReadModel(path)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if !hasModel || model != "haiku-4.5" {
		t.Errorf("got (%q, %t); want (\"haiku-4.5\", true)", model, hasModel)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "state", "pre.json")

	// User had model=opus before the swarm.
	if err := os.WriteFile(settings, []byte(`{"model":"opus-4.7","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(settings, snap); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Swarm mutates the model mid-run.
	if err := WriteModel(settings, "sonnet-4.6"); err != nil {
		t.Fatalf("mid-run WriteModel: %v", err)
	}

	// End-of-swarm restore.
	if err := Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Verify the original value is back, and theme/other fields are intact.
	model, hasModel, err := ReadModel(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !hasModel || model != "opus-4.7" {
		t.Errorf("post-restore model = (%q, %t); want (\"opus-4.7\", true)", model, hasModel)
	}
	raw, _ := os.ReadFile(settings)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["theme"] != "dark" {
		t.Errorf("theme lost after restore: %v", got["theme"])
	}

	// Snapshot should be consumed.
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Errorf("snapshot still present after restore: %v", err)
	}

	// Restore idempotency: second call is a no-op, not an error.
	if err := Restore(snap); err != nil {
		t.Errorf("Restore second call: %v", err)
	}
}

func TestSnapshotRestoreWhenUserHadNoModelField(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "pre.json")

	// User had a settings.json but no `model` field.
	if err := os.WriteFile(settings, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(settings, snap); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Swarm sets the model.
	if err := WriteModel(settings, "sonnet-4.6"); err != nil {
		t.Fatalf("mid-run WriteModel: %v", err)
	}

	if err := Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Restore must remove the model field rather than leave the swarm's value.
	_, hasModel, err := ReadModel(settings)
	if err != nil {
		t.Fatal(err)
	}
	if hasModel {
		t.Errorf("Restore must remove model field that user did not have pre-swarm")
	}
	raw, _ := os.ReadFile(settings)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["theme"] != "dark" {
		t.Errorf("theme lost after restore: %v", got["theme"])
	}
}

func TestRestoreMissingSnapshotIsNoOp(t *testing.T) {
	dir := t.TempDir()
	snap := filepath.Join(dir, "does-not-exist.json")
	if err := Restore(snap); err != nil {
		t.Errorf("Restore on absent snapshot should be a no-op, got %v", err)
	}
}

func TestSnapshotRestoreWhenNoSettingsFileExistedPreSwarm(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "pre.json")

	// Pre-swarm: user has no settings.json at all.
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Fatalf("expected no settings.json pre-snapshot, got stat err=%v", err)
	}
	if err := Snapshot(settings, snap); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Swarm creates settings.json with model set, as Claude Code does on
	// --model invocations.
	if err := WriteModel(settings, "sonnet-4.6"); err != nil {
		t.Fatalf("swarm WriteModel: %v", err)
	}
	if _, err := os.Stat(settings); err != nil {
		t.Fatalf("swarm should have created settings.json: %v", err)
	}

	if err := Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Post-restore: settings.json should be gone entirely — matches the
	// pre-swarm state where no file existed.
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Errorf("expected settings.json removed post-restore, got stat err=%v", err)
	}

	// Snapshot should also be consumed.
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Errorf("expected snapshot removed post-restore, got stat err=%v", err)
	}
}

func TestRestoreKeepsSettingsWithOtherFieldsWhenUserHadNoFile(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "pre.json")

	// Pre-swarm: no settings.json.
	if err := Snapshot(settings, snap); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Swarm (or something else) creates settings.json with model AND some
	// other fields (Claude Code might persist theme / MCP config changes
	// during the swarm). We should not nuke those.
	if err := os.WriteFile(settings, []byte(`{"model":"sonnet-4.6","theme":"dark","mcp_servers":["a","b"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Post-restore: settings.json still exists (we don't know the provenance
	// of `theme` / `mcp_servers`), but `model` is gone.
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("settings should still exist: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, present := got["model"]; present {
		t.Errorf("model should be removed, got: %v", got["model"])
	}
	if got["theme"] != "dark" {
		t.Errorf("theme should be preserved, got: %v", got["theme"])
	}
	if servers, ok := got["mcp_servers"].([]any); !ok || len(servers) != 2 {
		t.Errorf("mcp_servers should be preserved, got: %v", got["mcp_servers"])
	}
}

func TestRemoveIfEmptyObjectNoOpsOnNonEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeIfEmptyObject(path); err != nil {
		t.Fatalf("removeIfEmptyObject: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("non-empty file should not have been removed: %v", err)
	}
}

func TestRemoveIfEmptyObjectRemovesEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeIfEmptyObject(path); err != nil {
		t.Fatalf("removeIfEmptyObject: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("empty file should have been removed, got stat err=%v", err)
	}
}

func TestRemoveIfEmptyObjectNoOpsOnMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.json")
	if err := removeIfEmptyObject(path); err != nil {
		t.Errorf("absent path should not error, got: %v", err)
	}
}

func TestRemoveIfEmptyObjectNoOpsOnNonJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeIfEmptyObject(path); err != nil {
		t.Fatalf("removeIfEmptyObject: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("non-JSON file should not have been removed: %v", err)
	}
}
