package ensemble

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsembleLoader_MergePrecedence(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	userFile := filepath.Join(userDir, "ensembles.toml")
	importedFile := filepath.Join(userDir, "ensembles.imported.toml")
	projectFile := filepath.Join(projectDir, ".ntm", "ensembles.toml")
	if err := os.MkdirAll(filepath.Dir(projectFile), 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	importedToml := `[[ensembles]]
name = "diagnosis"
description = "imported override"

  [[ensembles.modes]]
  id = "deductive"

  [[ensembles.modes]]
  id = "abductive"

[[ensembles]]
name = "imported-only"
description = "imported only preset"

  [[ensembles.modes]]
  id = "deductive"

  [[ensembles.modes]]
  id = "practical"
`

	userToml := `[[ensembles]]
name = "diagnosis"
description = "user override"

  [[ensembles.modes]]
  id = "deductive"

  [[ensembles.modes]]
  id = "abductive"
`

	projectToml := `[[ensembles]]
name = "diagnosis"
description = "project override"

  [[ensembles.modes]]
  id = "deductive"

  [[ensembles.modes]]
  id = "practical"
`

	if err := os.WriteFile(importedFile, []byte(importedToml), 0o644); err != nil {
		t.Fatalf("write imported toml: %v", err)
	}
	if err := os.WriteFile(userFile, []byte(userToml), 0o644); err != nil {
		t.Fatalf("write user toml: %v", err)
	}
	if err := os.WriteFile(projectFile, []byte(projectToml), 0o644); err != nil {
		t.Fatalf("write project toml: %v", err)
	}

	loader := &EnsembleLoader{
		UserConfigDir: userDir,
		ProjectDir:    projectDir,
		ModeCatalog:   nil,
	}

	presets, err := loader.Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	found := false
	importedFound := false
	for _, preset := range presets {
		if preset.Name == "diagnosis" {
			found = true
			if preset.Source != "project" {
				t.Fatalf("preset source = %q, want project", preset.Source)
			}
			if preset.Description != "project override" {
				t.Fatalf("preset description = %q, want project override", preset.Description)
			}
		}
		if preset.Name == "imported-only" {
			importedFound = true
			if preset.Source != "imported" {
				t.Fatalf("imported preset source = %q, want imported", preset.Source)
			}
		}
	}
	if !found {
		t.Fatal("expected diagnosis preset in merged list")
	}
	if !importedFound {
		t.Fatal("expected imported-only preset in merged list")
	}
}

func TestEnsembleLoader_MissingFilesOk(t *testing.T) {
	loader := &EnsembleLoader{
		UserConfigDir: t.TempDir(),
		ProjectDir:    t.TempDir(),
		ModeCatalog:   nil,
	}

	if _, err := loader.Load(); err != nil {
		t.Fatalf("Load error: %v", err)
	}
}

func TestEnsembleLoader_SortsNonEmbeddedPresets(t *testing.T) {
	userDir := t.TempDir()
	content := `[[ensembles]]
name = "zeta"

  [[ensembles.modes]]
  id = "deductive"

[[ensembles]]
name = "alpha"

  [[ensembles.modes]]
  id = "deductive"

[[ensembles]]
name = "middle"

  [[ensembles.modes]]
  id = "deductive"
`
	if err := os.WriteFile(filepath.Join(userDir, "ensembles.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write user ensembles: %v", err)
	}

	presets, err := (&EnsembleLoader{UserConfigDir: userDir, ModeCatalog: nil}).Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	var customNames []string
	for _, preset := range presets {
		if preset.Source == "user" {
			customNames = append(customNames, preset.Name)
		}
	}
	want := []string{"alpha", "middle", "zeta"}
	if strings.Join(customNames, ",") != strings.Join(want, ",") {
		t.Fatalf("non-embedded presets = %v, want %v", customNames, want)
	}
}

func TestNewEnsembleLoader_Defaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	loader := NewEnsembleLoader(nil)
	if loader.UserConfigDir == "" {
		t.Fatal("expected UserConfigDir to be set")
	}
	if loader.ProjectDir == "" {
		t.Fatal("expected ProjectDir to be set")
	}
	if loader.ModeCatalog != nil {
		t.Fatal("expected ModeCatalog to be nil")
	}
}

func TestLoadEnsembles_Defaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	presets, err := LoadEnsembles(nil)
	if err != nil {
		t.Fatalf("LoadEnsembles error: %v", err)
	}
	if len(presets) == 0 {
		t.Fatal("expected embedded ensembles to be loaded")
	}
}

func TestLoadEnsemblesFile_ValidTOML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ensembles.toml")

	content := `[[ensembles]]
name = "test-ensemble"
description = "A test ensemble"

  [[ensembles.modes]]
  id = "deductive"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	presets, err := LoadEnsemblesFile(path)
	if err != nil {
		t.Fatalf("LoadEnsemblesFile: %v", err)
	}
	if len(presets) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(presets))
	}
	if presets[0].Name != "test-ensemble" {
		t.Errorf("name = %q", presets[0].Name)
	}
}

func TestLoadEnsemblesFile_MissingFile(t *testing.T) {
	t.Parallel()
	presets, err := LoadEnsemblesFile("/nonexistent/path/ensembles.toml")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if len(presets) != 0 {
		t.Errorf("expected empty slice for missing file, got %d", len(presets))
	}
}

func TestLoadEnsemblesFile_InvalidTOML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")

	if err := os.WriteFile(path, []byte("not valid toml ==="), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := LoadEnsemblesFile(path)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}

func TestLoadEnsemblesFile_RejectsUnknownFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "unknown.toml")

	content := `[[ensembles]]
name = "diagnosis"
description = "unknown field test"
legacy = true

  [[ensembles.modes]]
  id = "deductive"
`

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := LoadEnsemblesFile(path)
	if err == nil {
		t.Fatal("expected error for unknown TOML field")
	}
	if !strings.Contains(err.Error(), "unknown field(s): ensembles.legacy") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestLoadEnsemblesFile_EmptyEnsembles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.toml")

	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	presets, err := LoadEnsemblesFile(path)
	if err != nil {
		t.Fatalf("LoadEnsemblesFile: %v", err)
	}
	if len(presets) != 0 {
		t.Errorf("expected empty presets for empty file, got %d", len(presets))
	}
}

func TestSaveEnsemblesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "ensembles.toml")

	presets := []EnsemblePreset{
		{
			Name:        "saved-ensemble",
			Description: "A saved ensemble",
		},
	}

	if err := SaveEnsemblesFile(path, presets); err != nil {
		t.Fatalf("SaveEnsemblesFile: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}

	// Load it back
	loaded, err := LoadEnsemblesFile(path)
	if err != nil {
		t.Fatalf("LoadEnsemblesFile after save: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 loaded preset, got %d", len(loaded))
	}
	if loaded[0].Name != "saved-ensemble" {
		t.Errorf("name = %q", loaded[0].Name)
	}
}

func TestSaveAndLoadEnsemblesFile_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "roundtrip.toml")

	presets := []EnsemblePreset{
		{
			Name:        "ensemble-a",
			Description: "First ensemble",
		},
		{
			Name:        "ensemble-b",
			Description: "Second ensemble",
		},
	}

	if err := SaveEnsemblesFile(path, presets); err != nil {
		t.Fatalf("SaveEnsemblesFile: %v", err)
	}

	loaded, err := LoadEnsemblesFile(path)
	if err != nil {
		t.Fatalf("LoadEnsemblesFile: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 presets, got %d", len(loaded))
	}
}
