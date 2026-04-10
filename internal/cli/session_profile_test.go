package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests that mutate the package-level sessionProfileDirFunc are grouped into
// a single top-level test to avoid parallel races on that global.
func TestSessionProfileCRUD(t *testing.T) {
	dir := t.TempDir()
	old := sessionProfileDirFunc
	sessionProfileDirFunc = func() string { return dir }
	defer func() { sessionProfileDirFunc = old }()

	t.Run("save and load round-trip", func(t *testing.T) {
		tr := true
		cfg := SessionProfile{
			CC:       2,
			Cod:      1,
			Gmi:      3,
			Ollama:   2,
			Cursor:   1,
			Windsurf: 1,
			Aider:    1,
			UserPane: &tr,
			Prompt:   "do stuff",
			InitFile: "~/init.md",
			Safety:   &tr,
		}

		if err := SaveSessionProfile("mytest", cfg); err != nil {
			t.Fatalf("save: %v", err)
		}

		loaded, err := LoadSessionProfile("mytest")
		if err != nil {
			t.Fatalf("load: %v", err)
		}

		if loaded.CC != 2 {
			t.Errorf("CC: want 2, got %d", loaded.CC)
		}
		if loaded.Cod != 1 {
			t.Errorf("Cod: want 1, got %d", loaded.Cod)
		}
		if loaded.Gmi != 3 {
			t.Errorf("Gmi: want 3, got %d", loaded.Gmi)
		}
		if loaded.Ollama != 2 {
			t.Errorf("Ollama: want 2, got %d", loaded.Ollama)
		}
		if loaded.Cursor != 1 {
			t.Errorf("Cursor: want 1, got %d", loaded.Cursor)
		}
		if loaded.Windsurf != 1 {
			t.Errorf("Windsurf: want 1, got %d", loaded.Windsurf)
		}
		if loaded.Aider != 1 {
			t.Errorf("Aider: want 1, got %d", loaded.Aider)
		}
		if loaded.UserPane == nil || !*loaded.UserPane {
			t.Error("UserPane: want true")
		}
		if loaded.Prompt != "do stuff" {
			t.Errorf("Prompt: want %q, got %q", "do stuff", loaded.Prompt)
		}
		if loaded.InitFile != "~/init.md" {
			t.Errorf("InitFile: want %q, got %q", "~/init.md", loaded.InitFile)
		}
		if loaded.Safety == nil || !*loaded.Safety {
			t.Error("Safety: want true")
		}
	})

	t.Run("valid names", func(t *testing.T) {
		for _, name := range []string{"abc", "A1", "my-profile", "test_123"} {
			if err := SaveSessionProfile(name, SessionProfile{CC: 1}); err != nil {
				t.Errorf("unexpected error for valid name %q: %v", name, err)
			}
		}
	})

	t.Run("load not found", func(t *testing.T) {
		_, err := LoadSessionProfile("nonexistent")
		if err == nil {
			t.Fatal("expected error for missing profile")
		}
	})

	t.Run("list sorted", func(t *testing.T) {
		// dir already has profiles from earlier subtests; clear and recreate
		subDir := t.TempDir()
		sessionProfileDirFunc = func() string { return subDir }

		names, err := ListSessionProfiles()
		if err != nil {
			t.Fatalf("list empty: %v", err)
		}
		if len(names) != 0 {
			t.Fatalf("expected empty list, got %v", names)
		}

		SaveSessionProfile("beta", SessionProfile{CC: 1})
		SaveSessionProfile("alpha", SessionProfile{Cod: 2})

		names, err = ListSessionProfiles()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(names) != 2 {
			t.Fatalf("expected 2, got %d", len(names))
		}
		if names[0] != "alpha" || names[1] != "beta" {
			t.Errorf("expected [alpha, beta], got %v", names)
		}

		// Restore dir for subsequent subtests
		sessionProfileDirFunc = func() string { return dir }
	})

	t.Run("list ignores non-toml", func(t *testing.T) {
		subDir := t.TempDir()
		sessionProfileDirFunc = func() string { return subDir }

		os.WriteFile(filepath.Join(subDir, "readme.txt"), []byte("hi"), 0o644)
		os.Mkdir(filepath.Join(subDir, "subdir.toml"), 0o755)
		SaveSessionProfile("real", SessionProfile{CC: 1})

		names, err := ListSessionProfiles()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(names) != 1 || names[0] != "real" {
			t.Errorf("expected [real], got %v", names)
		}

		sessionProfileDirFunc = func() string { return dir }
	})

	t.Run("list nonexistent dir", func(t *testing.T) {
		sessionProfileDirFunc = func() string { return "/tmp/ntm-test-nonexistent-dir-12345" }
		names, err := ListSessionProfiles()
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if names != nil {
			t.Fatalf("expected nil, got %v", names)
		}
		sessionProfileDirFunc = func() string { return dir }
	})

	t.Run("list invalid profile file errors", func(t *testing.T) {
		subDir := t.TempDir()
		sessionProfileDirFunc = func() string { return subDir }

		badPath := filepath.Join(subDir, "broken.toml")
		if err := os.WriteFile(badPath, []byte("cc = ["), 0o644); err != nil {
			t.Fatalf("write broken profile: %v", err)
		}

		_, err := ListSessionProfiles()
		if err == nil {
			t.Fatal("expected list error for invalid profile")
		}
		if !strings.Contains(err.Error(), `parsing profile "broken"`) {
			t.Fatalf("expected parse error, got %v", err)
		}

		sessionProfileDirFunc = func() string { return dir }
	})

	t.Run("list unknown profile field errors", func(t *testing.T) {
		subDir := t.TempDir()
		sessionProfileDirFunc = func() string { return subDir }

		badPath := filepath.Join(subDir, "unknown.toml")
		if err := os.WriteFile(badPath, []byte("cc = 1\nclaud = 2\n"), 0o644); err != nil {
			t.Fatalf("write unknown-field profile: %v", err)
		}

		_, err := ListSessionProfiles()
		if err == nil {
			t.Fatal("expected list error for unknown field")
		}
		if !strings.Contains(err.Error(), `unknown field(s): claud`) {
			t.Fatalf("expected unknown field error, got %v", err)
		}

		sessionProfileDirFunc = func() string { return dir }
	})

	t.Run("load negative count errors", func(t *testing.T) {
		subDir := t.TempDir()
		sessionProfileDirFunc = func() string { return subDir }

		badPath := filepath.Join(subDir, "negative.toml")
		if err := os.WriteFile(badPath, []byte("cc = -1\n"), 0o644); err != nil {
			t.Fatalf("write negative profile: %v", err)
		}

		_, err := LoadSessionProfile("negative")
		if err == nil {
			t.Fatal("expected invalid negative count error")
		}
		if !strings.Contains(err.Error(), `cc count cannot be negative`) {
			t.Fatalf("expected negative count error, got %v", err)
		}

		sessionProfileDirFunc = func() string { return dir }
	})

	t.Run("list invalid profile filename errors", func(t *testing.T) {
		subDir := t.TempDir()
		sessionProfileDirFunc = func() string { return subDir }

		badPath := filepath.Join(subDir, "bad name.toml")
		if err := os.WriteFile(badPath, []byte("cc = 1\n"), 0o644); err != nil {
			t.Fatalf("write invalid name profile: %v", err)
		}

		_, err := ListSessionProfiles()
		if err == nil {
			t.Fatal("expected list error for invalid profile filename")
		}
		if !strings.Contains(err.Error(), `invalid profile file "bad name.toml"`) {
			t.Fatalf("expected invalid filename error, got %v", err)
		}

		sessionProfileDirFunc = func() string { return dir }
	})

	t.Run("load invalid name rejected", func(t *testing.T) {
		_, err := LoadSessionProfile("../escape")
		if err == nil {
			t.Fatal("expected invalid name error")
		}
		if !strings.Contains(err.Error(), `invalid profile name`) {
			t.Fatalf("expected invalid name error, got %v", err)
		}
	})

	t.Run("delete invalid name rejected", func(t *testing.T) {
		err := DeleteSessionProfile("../escape")
		if err == nil {
			t.Fatal("expected invalid name error")
		}
		if !strings.Contains(err.Error(), `invalid profile name`) {
			t.Fatalf("expected invalid name error, got %v", err)
		}
	})

	t.Run("show invalid profile errors", func(t *testing.T) {
		subDir := t.TempDir()
		sessionProfileDirFunc = func() string { return subDir }

		badPath := filepath.Join(subDir, "broken.toml")
		if err := os.WriteFile(badPath, []byte("cc = ["), 0o644); err != nil {
			t.Fatalf("write broken profile: %v", err)
		}

		cmd := newSessionProfileShowCmd()
		err := cmd.RunE(cmd, []string{"broken"})
		if err == nil {
			t.Fatal("expected show error for invalid profile")
		}
		if !strings.Contains(err.Error(), `parsing profile "broken"`) {
			t.Fatalf("expected parse error, got %v", err)
		}

		sessionProfileDirFunc = func() string { return dir }
	})

	t.Run("delete", func(t *testing.T) {
		SaveSessionProfile("doomed", SessionProfile{CC: 1})
		if err := DeleteSessionProfile("doomed"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		_, err := LoadSessionProfile("doomed")
		if err == nil {
			t.Fatal("expected error loading deleted profile")
		}
	})

	t.Run("delete not found", func(t *testing.T) {
		err := DeleteSessionProfile("ghost")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("round-trip preserves values", func(t *testing.T) {
		subDir := t.TempDir()
		sessionProfileDirFunc = func() string { return subDir }

		if err := SaveSessionProfile("minimal", SessionProfile{CC: 1}); err != nil {
			t.Fatalf("save: %v", err)
		}

		loaded, err := LoadSessionProfile("minimal")
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if loaded.CC != 1 {
			t.Errorf("CC: want 1, got %d", loaded.CC)
		}
		if loaded.Cod != 0 {
			t.Errorf("Cod: want 0, got %d", loaded.Cod)
		}

		sessionProfileDirFunc = func() string { return dir }
	})
}

func TestSaveSessionProfile_UsesSelectedConfigPath(t *testing.T) {
	oldCfgFile := cfgFile
	oldDirFunc := sessionProfileDirFunc
	defer func() {
		cfgFile = oldCfgFile
		sessionProfileDirFunc = oldDirFunc
	}()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	cfgFile = filepath.Join(tmpDir, "custom-root", "config.toml")
	sessionProfileDirFunc = sessionProfileDir

	if err := SaveSessionProfile("selected", SessionProfile{CC: 1}); err != nil {
		t.Fatalf("SaveSessionProfile failed: %v", err)
	}

	wantPath := filepath.Join(tmpDir, "custom-root", "profiles", "selected.toml")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("selected-config profile missing: %v", err)
	}

	legacyPath := filepath.Join(tmpDir, "xdg", "ntm", "profiles", "selected.toml")
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy XDG profile path should remain untouched, stat err = %v", err)
	}
}

func TestSaveSessionProfile_InvalidName(t *testing.T) {
	tests := []struct {
		name string
	}{
		{""},
		{".hidden"},
		{"has spaces"},
		{"has/slash"},
		{"-starts-dash"},
	}
	for _, tc := range tests {
		if err := SaveSessionProfile(tc.name, SessionProfile{}); err == nil {
			t.Errorf("expected error for name %q", tc.name)
		}
	}
}

func TestSaveSessionProfile_InvalidConfig(t *testing.T) {
	if err := SaveSessionProfile("negative", SessionProfile{CC: -1}); err == nil {
		t.Fatal("expected error for negative agent count")
	}
}

func TestApplySessionProfileToSpawnOptions(t *testing.T) {

	t.Run("fills empty opts from profile", func(t *testing.T) {
		tr := true
		profile := &SessionProfile{
			CC:        3,
			Cod:       2,
			Gmi:       1,
			Ollama:    2,
			Cursor:    1,
			Windsurf:  1,
			Aider:     1,
			UserPane:  &tr,
			Prompt:    "hello",
			Safety:    &tr,
			Worktrees: &tr,
		}
		opts := &SpawnOptions{}
		ApplySessionProfileToSpawnOptions(opts, profile)

		if opts.CCCount != 3 {
			t.Errorf("CCCount: want 3, got %d", opts.CCCount)
		}
		if opts.CodCount != 2 {
			t.Errorf("CodCount: want 2, got %d", opts.CodCount)
		}
		if opts.GmiCount != 1 {
			t.Errorf("GmiCount: want 1, got %d", opts.GmiCount)
		}
		if opts.OllamaCount != 2 {
			t.Errorf("OllamaCount: want 2, got %d", opts.OllamaCount)
		}
		if opts.CursorCount != 1 {
			t.Errorf("CursorCount: want 1, got %d", opts.CursorCount)
		}
		if opts.WindsurfCount != 1 {
			t.Errorf("WindsurfCount: want 1, got %d", opts.WindsurfCount)
		}
		if opts.AiderCount != 1 {
			t.Errorf("AiderCount: want 1, got %d", opts.AiderCount)
		}
		if !opts.UserPane {
			t.Error("UserPane: want true")
		}
		if opts.Prompt != "hello" {
			t.Errorf("Prompt: want %q, got %q", "hello", opts.Prompt)
		}
		if !opts.Safety {
			t.Error("Safety: want true")
		}
		if !opts.UseWorktrees {
			t.Error("UseWorktrees: want true")
		}
	})

	t.Run("explicit flags override profile", func(t *testing.T) {
		profile := &SessionProfile{
			CC:     5,
			Cod:    5,
			Gmi:    5,
			Prompt: "from profile",
		}
		opts := &SpawnOptions{
			CCCount:  2,
			CodCount: 3,
			GmiCount: 4,
			Prompt:   "from flag",
		}
		ApplySessionProfileToSpawnOptions(opts, profile)

		if opts.CCCount != 2 {
			t.Errorf("CCCount: want 2, got %d", opts.CCCount)
		}
		if opts.CodCount != 3 {
			t.Errorf("CodCount: want 3, got %d", opts.CodCount)
		}
		if opts.GmiCount != 4 {
			t.Errorf("GmiCount: want 4, got %d", opts.GmiCount)
		}
		if opts.Prompt != "from flag" {
			t.Errorf("Prompt: want %q, got %q", "from flag", opts.Prompt)
		}
	})

	t.Run("nil booleans do not set opts", func(t *testing.T) {
		profile := &SessionProfile{CC: 1}
		opts := &SpawnOptions{}
		ApplySessionProfileToSpawnOptions(opts, profile)

		if opts.UserPane {
			t.Error("UserPane: should remain false")
		}
		if opts.Safety {
			t.Error("Safety: should remain false")
		}
		if opts.UseWorktrees {
			t.Error("UseWorktrees: should remain false")
		}
	})

	t.Run("init file loads content", func(t *testing.T) {
		tmpFile := filepath.Join(t.TempDir(), "init.md")
		os.WriteFile(tmpFile, []byte("  init prompt content  \n"), 0o644)

		profile := &SessionProfile{InitFile: tmpFile}
		opts := &SpawnOptions{}
		ApplySessionProfileToSpawnOptions(opts, profile)

		if opts.InitPrompt != "init prompt content" {
			t.Errorf("InitPrompt: want %q, got %q", "init prompt content", opts.InitPrompt)
		}
	})

	t.Run("init file missing is silent", func(t *testing.T) {
		profile := &SessionProfile{InitFile: "/nonexistent/init.md"}
		opts := &SpawnOptions{}
		ApplySessionProfileToSpawnOptions(opts, profile)

		if opts.InitPrompt != "" {
			t.Errorf("InitPrompt: want empty, got %q", opts.InitPrompt)
		}
	})
}
