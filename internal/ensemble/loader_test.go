package ensemble

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModeLoader_EmbeddedOnly(t *testing.T) {
	loader := &ModeLoader{
		UserConfigDir: "/nonexistent/path",
		ProjectDir:    "/nonexistent/path",
	}

	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() with missing user/project files should succeed: %v", err)
	}
	if catalog.Count() != 80 {
		t.Errorf("catalog count = %d, want 80", catalog.Count())
	}
}

func TestModeLoader_UserOverride(t *testing.T) {
	dir := t.TempDir()

	// Write a user modes file that overrides the "deductive" mode
	modesContent := `
[[modes]]
id = "deductive"
name = "Custom Deductive"
category = "Formal"
short_desc = "Overridden deductive mode"
code = "A1"
tier = "core"
`
	if err := os.WriteFile(filepath.Join(dir, "modes.toml"), []byte(modesContent), 0644); err != nil {
		t.Fatal(err)
	}

	loader := &ModeLoader{
		UserConfigDir: dir,
		ProjectDir:    "/nonexistent",
	}

	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	m := catalog.GetMode("deductive")
	if m == nil {
		t.Fatal("deductive mode not found")
	}
	if m.Name != "Custom Deductive" {
		t.Errorf("deductive.Name = %q, want %q", m.Name, "Custom Deductive")
	}
	if m.Source != "user" {
		t.Errorf("deductive.Source = %q, want %q", m.Source, "user")
	}
}

func TestModeLoader_ProjectOverridesUser(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	// User override
	userContent := `
[[modes]]
id = "deductive"
name = "User Deductive"
category = "Formal"
short_desc = "User version"
code = "A1"
tier = "core"
`
	if err := os.WriteFile(filepath.Join(userDir, "modes.toml"), []byte(userContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Project override (higher precedence)
	ntmDir := filepath.Join(projectDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatal(err)
	}
	projectContent := `
[[modes]]
id = "deductive"
name = "Project Deductive"
category = "Formal"
short_desc = "Project version"
code = "A1"
tier = "core"
`
	if err := os.WriteFile(filepath.Join(ntmDir, "modes.toml"), []byte(projectContent), 0644); err != nil {
		t.Fatal(err)
	}

	loader := &ModeLoader{
		UserConfigDir: userDir,
		ProjectDir:    projectDir,
	}

	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	m := catalog.GetMode("deductive")
	if m == nil {
		t.Fatal("deductive mode not found")
	}
	if m.Name != "Project Deductive" {
		t.Errorf("deductive.Name = %q, want %q (project should override user)", m.Name, "Project Deductive")
	}
	if m.Source != "project" {
		t.Errorf("deductive.Source = %q, want %q", m.Source, "project")
	}
}

func TestModeLoader_NewCustomMode(t *testing.T) {
	dir := t.TempDir()

	modesContent := `
[[modes]]
id = "my-custom-mode"
name = "My Custom Mode"
category = "Domain"
short_desc = "A custom reasoning mode"
code = "K8"
`
	if err := os.WriteFile(filepath.Join(dir, "modes.toml"), []byte(modesContent), 0644); err != nil {
		t.Fatal(err)
	}

	loader := &ModeLoader{
		UserConfigDir: dir,
		ProjectDir:    "/nonexistent",
	}

	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Should have 81 modes (80 embedded + 1 custom)
	if catalog.Count() != 81 {
		t.Errorf("catalog count = %d, want 81", catalog.Count())
	}

	m := catalog.GetMode("my-custom-mode")
	if m == nil {
		t.Fatal("custom mode not found")
	}
	if m.Tier != TierAdvanced {
		t.Errorf("custom mode tier = %q, want %q (default)", m.Tier, TierAdvanced)
	}
	if m.Source != "user" {
		t.Errorf("custom mode source = %q, want %q", m.Source, "user")
	}
}

func TestModeLoader_DefaultTierAdvanced(t *testing.T) {
	dir := t.TempDir()

	// Mode without explicit tier
	modesContent := `
[[modes]]
id = "no-tier-mode"
name = "No Tier Mode"
category = "Meta"
short_desc = "Mode without tier specified"
code = "L8"
`
	if err := os.WriteFile(filepath.Join(dir, "modes.toml"), []byte(modesContent), 0644); err != nil {
		t.Fatal(err)
	}

	loader := &ModeLoader{
		UserConfigDir: dir,
		ProjectDir:    "/nonexistent",
	}

	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	m := catalog.GetMode("no-tier-mode")
	if m == nil {
		t.Fatal("mode not found")
	}
	if m.Tier != TierAdvanced {
		t.Errorf("tier = %q, want %q", m.Tier, TierAdvanced)
	}
}

func TestModeLoader_InvalidTOML(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "modes.toml"), []byte("not valid [toml [["), 0644); err != nil {
		t.Fatal(err)
	}

	loader := &ModeLoader{
		UserConfigDir: dir,
		ProjectDir:    "/nonexistent",
	}

	_, err := loader.Load()
	if err == nil {
		t.Fatal("Load() should fail on invalid TOML")
	}
}

func TestModeLoader_MissingID(t *testing.T) {
	dir := t.TempDir()

	modesContent := `
[[modes]]
name = "No ID Mode"
category = "Meta"
short_desc = "Missing ID"
`
	if err := os.WriteFile(filepath.Join(dir, "modes.toml"), []byte(modesContent), 0644); err != nil {
		t.Fatal(err)
	}

	loader := &ModeLoader{
		UserConfigDir: dir,
		ProjectDir:    "/nonexistent",
	}

	_, err := loader.Load()
	if err == nil {
		t.Fatal("Load() should fail on mode without ID")
	}
}

func TestModeLoader_EmptyProjectDir(t *testing.T) {
	loader := &ModeLoader{
		UserConfigDir: "/nonexistent",
		ProjectDir:    "",
	}

	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() with empty ProjectDir should succeed: %v", err)
	}
	if catalog.Count() != 80 {
		t.Errorf("catalog count = %d, want 80", catalog.Count())
	}
}

func TestLoadModeCatalog(t *testing.T) {
	// Should succeed with just embedded modes when no config files exist
	catalog, err := LoadModeCatalog()
	if err != nil {
		t.Fatalf("LoadModeCatalog() error: %v", err)
	}
	if catalog.Count() < 80 {
		t.Errorf("catalog count = %d, want >= 80", catalog.Count())
	}
}

func TestGlobalCatalog(t *testing.T) {
	ResetGlobalCatalog()
	defer ResetGlobalCatalog()

	catalog1, err := GlobalCatalog()
	if err != nil {
		t.Fatalf("GlobalCatalog() error: %v", err)
	}

	catalog2, err := GlobalCatalog()
	if err != nil {
		t.Fatalf("second GlobalCatalog() error: %v", err)
	}

	if catalog1 != catalog2 {
		t.Error("GlobalCatalog() should return the same instance")
	}
}

func TestNewModeLoader(t *testing.T) {
	loader := NewModeLoader()
	if loader.UserConfigDir == "" {
		t.Error("UserConfigDir should not be empty")
	}
	// ProjectDir might be empty in some test environments, but check it's set
	if loader.ProjectDir == "" {
		t.Log("ProjectDir is empty (expected in some environments)")
	}
}
