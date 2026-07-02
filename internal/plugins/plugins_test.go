package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- CommandPlugin Tests ---

func TestLoadCommandPlugins_NonExistentDir(t *testing.T) {
	t.Parallel()

	plugins, err := LoadCommandPlugins("/this/path/does/not/exist")
	if err != nil {
		t.Errorf("expected nil error for non-existent dir, got: %v", err)
	}
	if plugins != nil {
		t.Errorf("expected nil plugins for non-existent dir, got: %v", plugins)
	}
}

func TestLoadCommandPlugins_EmptyDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	plugins, err := LoadCommandPlugins(dir)
	if err != nil {
		t.Fatalf("LoadCommandPlugins failed: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins in empty dir, got: %d", len(plugins))
	}
}

func TestLoadCommandPlugins_IgnoresNonExecutable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create non-executable file
	path := filepath.Join(dir, "non-exec.sh")
	if err := os.WriteFile(path, []byte("#!/bin/bash\necho hello"), 0644); err != nil {
		t.Fatal(err)
	}

	plugins, err := LoadCommandPlugins(dir)
	if err != nil {
		t.Fatalf("LoadCommandPlugins failed: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins (non-executable should be ignored), got: %d", len(plugins))
	}
}

func TestLoadCommandPlugins_IgnoresDirectories(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create subdirectory
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	plugins, err := LoadCommandPlugins(dir)
	if err != nil {
		t.Fatalf("LoadCommandPlugins failed: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins (directories should be ignored), got: %d", len(plugins))
	}
}

func TestLoadCommandPlugins_FindsExecutables(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create executable file
	path := filepath.Join(dir, "my-plugin")
	if err := os.WriteFile(path, []byte("#!/bin/bash\necho hello"), 0755); err != nil {
		t.Fatal(err)
	}

	plugins, err := LoadCommandPlugins(dir)
	if err != nil {
		t.Fatalf("LoadCommandPlugins failed: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got: %d", len(plugins))
	}
	if plugins[0].Name != "my-plugin" {
		t.Errorf("expected name 'my-plugin', got: %s", plugins[0].Name)
	}
	if plugins[0].Path != path {
		t.Errorf("expected path %s, got: %s", path, plugins[0].Path)
	}
}

func TestLoadCommandPlugins_ParsesHeaderComments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := `#!/bin/bash
# Description: My custom plugin
# Usage: my-plugin [options]
echo "hello"
`
	path := filepath.Join(dir, "my-plugin")
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}

	plugins, err := LoadCommandPlugins(dir)
	if err != nil {
		t.Fatalf("LoadCommandPlugins failed: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got: %d", len(plugins))
	}
	if plugins[0].Description != "My custom plugin" {
		t.Errorf("expected description 'My custom plugin', got: %s", plugins[0].Description)
	}
	if plugins[0].Usage != "my-plugin [options]" {
		t.Errorf("expected usage 'my-plugin [options]', got: %s", plugins[0].Usage)
	}
}

func TestLoadCommandPlugins_DefaultDescription(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// No header comments
	content := `#!/bin/bash
echo "hello"
`
	path := filepath.Join(dir, "simple-cmd")
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}

	plugins, err := LoadCommandPlugins(dir)
	if err != nil {
		t.Fatalf("LoadCommandPlugins failed: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got: %d", len(plugins))
	}
	expected := "Custom command: simple-cmd"
	if plugins[0].Description != expected {
		t.Errorf("expected description %q, got: %s", expected, plugins[0].Description)
	}
}

func TestParseScriptHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		content       string
		expectedDesc  string
		expectedUsage string
	}{
		{
			name: "full header",
			content: `#!/bin/bash
# Description: Test plugin
# Usage: test [args]
echo "code"
`,
			expectedDesc:  "Test plugin",
			expectedUsage: "test [args]",
		},
		{
			name: "description only",
			content: `#!/bin/bash
# Description: Only desc
echo "code"
`,
			expectedDesc:  "Only desc",
			expectedUsage: "",
		},
		{
			name: "usage only",
			content: `#!/bin/bash
# Usage: cmd [opts]
echo "code"
`,
			expectedDesc:  "",
			expectedUsage: "cmd [opts]",
		},
		{
			name: "no header",
			content: `#!/bin/bash
echo "code"
`,
			expectedDesc:  "",
			expectedUsage: "",
		},
		{
			name: "stops at non-comment",
			content: `#!/bin/bash
# Description: First
VAR=value
# Description: Should not be read
`,
			expectedDesc:  "First",
			expectedUsage: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, "script.sh")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}

			desc, usage, err := parseScriptHeader(path)
			if err != nil {
				t.Fatalf("parseScriptHeader failed: %v", err)
			}
			if desc != tt.expectedDesc {
				t.Errorf("expected description %q, got %q", tt.expectedDesc, desc)
			}
			if usage != tt.expectedUsage {
				t.Errorf("expected usage %q, got %q", tt.expectedUsage, usage)
			}
		})
	}
}

func TestCommandPlugin_Execute(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outFile := filepath.Join(dir, "output.txt")

	// Create a script that writes args and env to a file
	script := `#!/bin/bash
echo "args: $@" > ` + outFile + `
echo "TEST_VAR: $TEST_VAR" >> ` + outFile + `
`
	scriptPath := filepath.Join(dir, "test-script")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	plugin := CommandPlugin{
		Name: "test-script",
		Path: scriptPath,
	}

	env := map[string]string{"TEST_VAR": "hello-world"}
	err := plugin.Execute([]string{"arg1", "arg2"}, env)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Read output file
	content, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read output: %v", err)
	}

	output := string(content)
	if output == "" {
		t.Fatal("output file is empty")
	}
	// Check args were passed
	if !strings.Contains(output, "arg1 arg2") {
		t.Errorf("expected args in output, got: %s", output)
	}
	// Check env was passed
	if !strings.Contains(output, "hello-world") {
		t.Errorf("expected TEST_VAR in output, got: %s", output)
	}
}

// --- AgentPlugin Tests ---

func TestLoadAgentPlugins_NonExistentDir(t *testing.T) {
	t.Parallel()

	plugins, err := LoadAgentPlugins("/this/path/does/not/exist")
	if err != nil {
		t.Errorf("expected nil error for non-existent dir, got: %v", err)
	}
	if plugins != nil {
		t.Errorf("expected nil plugins for non-existent dir, got: %v", plugins)
	}
}

func TestLoadAgentPlugins_EmptyDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	plugins, err := LoadAgentPlugins(dir)
	if err != nil {
		t.Fatalf("LoadAgentPlugins failed: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins in empty dir, got: %d", len(plugins))
	}
}

func TestLoadAgentPlugins_LoadsValidTOML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := `[agent]
name = "my-agent"
alias = "ma"
command = "my-agent-binary"
description = "My custom agent"

[agent.env]
CUSTOM_ENV = "present"

[agent.defaults]
model = "google/gemini-2.5-flash"
tags = ["custom", "test"]
`
	path := filepath.Join(dir, "my-agent.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	plugins, err := LoadAgentPlugins(dir)
	if err != nil {
		t.Fatalf("LoadAgentPlugins failed: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got: %d", len(plugins))
	}

	p := plugins[0]
	if p.Name != "my-agent" {
		t.Errorf("expected name 'my-agent', got: %s", p.Name)
	}
	if p.Alias != "ma" {
		t.Errorf("expected alias 'ma', got: %s", p.Alias)
	}
	if p.Command != "my-agent-binary" {
		t.Errorf("expected command 'my-agent-binary', got: %s", p.Command)
	}
	if p.Description != "My custom agent" {
		t.Errorf("expected description 'My custom agent', got: %s", p.Description)
	}
	if p.Env["CUSTOM_ENV"] != "present" {
		t.Errorf("expected env CUSTOM_ENV='present', got: %v", p.Env)
	}
	if p.Defaults.Model != "google/gemini-2.5-flash" {
		t.Errorf("expected default model 'google/gemini-2.5-flash', got: %q", p.Defaults.Model)
	}
	if len(p.Defaults.Tags) != 2 || p.Defaults.Tags[0] != "custom" {
		t.Errorf("expected tags [custom, test], got: %v", p.Defaults.Tags)
	}
}

func TestLoadAgentPlugins_UsesFilenameAsName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// No name field in TOML
	content := `[agent]
command = "some-cmd"
`
	path := filepath.Join(dir, "fallback-name.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	plugins, err := LoadAgentPlugins(dir)
	if err != nil {
		t.Fatalf("LoadAgentPlugins failed: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got: %d", len(plugins))
	}
	if plugins[0].Name != "fallback-name" {
		t.Errorf("expected name 'fallback-name' from filename, got: %s", plugins[0].Name)
	}
}

func TestLoadAgentPlugins_SkipsInvalidTOML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Invalid TOML syntax
	content := `[agent]
name = "valid
command = broken
`
	path := filepath.Join(dir, "invalid.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	plugins, err := LoadAgentPlugins(dir)
	if err != nil {
		t.Fatalf("LoadAgentPlugins failed: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins (invalid TOML should be skipped), got: %d", len(plugins))
	}
}

func TestLoadAgentPlugins_SkipsMissingCommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Missing required 'command' field
	content := `[agent]
name = "no-command"
description = "missing command field"
`
	path := filepath.Join(dir, "no-command.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	plugins, err := LoadAgentPlugins(dir)
	if err != nil {
		t.Fatalf("LoadAgentPlugins failed: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins (missing command should be skipped), got: %d", len(plugins))
	}
}

func TestLoadAgentPlugins_SkipsInvalidName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tomlName string
	}{
		{"spaces", "has spaces"},
		{"special chars", "agent@123"},
		{"dots", "agent.v2"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			content := `[agent]
name = "` + tt.tomlName + `"
command = "some-cmd"
`
			path := filepath.Join(dir, "plugin.toml")
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}

			plugins, err := LoadAgentPlugins(dir)
			if err != nil {
				t.Fatalf("LoadAgentPlugins failed: %v", err)
			}
			if len(plugins) != 0 {
				t.Errorf("expected 0 plugins (invalid name %q should be skipped), got: %d", tt.tomlName, len(plugins))
			}
		})
	}
}

func TestLoadAgentPlugins_ValidNamePatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tomlName string
	}{
		{"lowercase", "myagent"},
		{"uppercase", "MyAgent"},
		{"numbers", "agent123"},
		{"underscore", "my_agent"},
		{"hyphen", "my-agent"},
		{"mixed", "My_Agent-123"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			content := `[agent]
name = "` + tt.tomlName + `"
command = "some-cmd"
`
			path := filepath.Join(dir, "plugin.toml")
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}

			plugins, err := LoadAgentPlugins(dir)
			if err != nil {
				t.Fatalf("LoadAgentPlugins failed: %v", err)
			}
			if len(plugins) != 1 {
				t.Errorf("expected 1 plugin for valid name %q, got: %d", tt.tomlName, len(plugins))
			}
		})
	}
}

func TestLoadAgentPlugins_IgnoresNonTOMLFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create non-TOML file
	path := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(path, []byte("not a plugin"), 0644); err != nil {
		t.Fatal(err)
	}

	plugins, err := LoadAgentPlugins(dir)
	if err != nil {
		t.Fatalf("LoadAgentPlugins failed: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins (non-TOML should be ignored), got: %d", len(plugins))
	}
}

func TestLoadAgentPlugins_MultiplePlugins(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create multiple valid plugins
	for _, name := range []string{"agent-a", "agent-b", "agent-c"} {
		content := `[agent]
name = "` + name + `"
command = "` + name + `-cmd"
`
		path := filepath.Join(dir, name+".toml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	plugins, err := LoadAgentPlugins(dir)
	if err != nil {
		t.Fatalf("LoadAgentPlugins failed: %v", err)
	}
	if len(plugins) != 3 {
		t.Errorf("expected 3 plugins, got: %d", len(plugins))
	}
}

// --- Name Regex Tests ---

func TestPluginNameRegex(t *testing.T) {
	t.Parallel()

	valid := []string{
		"a", "abc", "ABC", "a1", "a_b", "a-b",
		"Agent123", "my_agent", "my-agent", "MyAgent_v2-beta",
	}
	invalid := []string{
		"", " ", "a b", "a.b", "a@b", "a/b", "a\\b",
		"agent!", "agent#", "agent$",
	}

	for _, name := range valid {
		if !pluginNameRegex.MatchString(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}
	for _, name := range invalid {
		if pluginNameRegex.MatchString(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}
