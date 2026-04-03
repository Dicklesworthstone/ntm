package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func TestParsePersonaSpec(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    PersonaSpec
		wantErr bool
	}{
		{
			name:  "simple name",
			input: "architect",
			want:  PersonaSpec{Name: "architect", Count: 1},
		},
		{
			name:  "name with count",
			input: "implementer:2",
			want:  PersonaSpec{Name: "implementer", Count: 2},
		},
		{
			name:  "name with spaces",
			input: " reviewer ",
			want:  PersonaSpec{Name: "reviewer", Count: 1},
		},
		{
			name:  "count with spaces",
			input: "tester: 3 ",
			want:  PersonaSpec{Name: "tester", Count: 3},
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid count",
			input:   "architect:abc",
			wantErr: true,
		},
		{
			name:    "zero count",
			input:   "architect:0",
			wantErr: true,
		},
		{
			name:    "negative count",
			input:   "architect:-1",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePersonaSpec(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePersonaSpec(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Name != tt.want.Name {
					t.Errorf("ParsePersonaSpec(%q).Name = %v, want %v", tt.input, got.Name, tt.want.Name)
				}
				if got.Count != tt.want.Count {
					t.Errorf("ParsePersonaSpec(%q).Count = %v, want %v", tt.input, got.Count, tt.want.Count)
				}
			}
		})
	}
}

func TestPersonaSpecsString(t *testing.T) {
	tests := []struct {
		name  string
		specs PersonaSpecs
		want  string
	}{
		{
			name:  "empty",
			specs: PersonaSpecs{},
			want:  "",
		},
		{
			name: "single with count 1",
			specs: PersonaSpecs{
				{Name: "architect", Count: 1},
			},
			want: "architect",
		},
		{
			name: "single with count > 1",
			specs: PersonaSpecs{
				{Name: "implementer", Count: 2},
			},
			want: "implementer:2",
		},
		{
			name: "multiple",
			specs: PersonaSpecs{
				{Name: "architect", Count: 1},
				{Name: "implementer", Count: 2},
			},
			want: "architect,implementer:2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.specs.String(); got != tt.want {
				t.Errorf("PersonaSpecs.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPersonaSpecsSet(t *testing.T) {
	var specs PersonaSpecs

	// Add first spec
	if err := specs.Set("architect"); err != nil {
		t.Errorf("PersonaSpecs.Set(architect) unexpected error: %v", err)
	}
	if len(specs) != 1 {
		t.Errorf("after first Set, len = %d, want 1", len(specs))
	}

	// Add second spec
	if err := specs.Set("implementer:2"); err != nil {
		t.Errorf("PersonaSpecs.Set(implementer:2) unexpected error: %v", err)
	}
	if len(specs) != 2 {
		t.Errorf("after second Set, len = %d, want 2", len(specs))
	}

	// Verify specs
	if specs[0].Name != "architect" || specs[0].Count != 1 {
		t.Errorf("specs[0] = %+v, want {architect, 1}", specs[0])
	}
	if specs[1].Name != "implementer" || specs[1].Count != 2 {
		t.Errorf("specs[1] = %+v, want {implementer, 2}", specs[1])
	}
}

func TestPersonaSpecsTotalCount(t *testing.T) {
	tests := []struct {
		name  string
		specs PersonaSpecs
		want  int
	}{
		{
			name:  "empty",
			specs: PersonaSpecs{},
			want:  0,
		},
		{
			name: "single",
			specs: PersonaSpecs{
				{Name: "architect", Count: 1},
			},
			want: 1,
		},
		{
			name: "multiple",
			specs: PersonaSpecs{
				{Name: "architect", Count: 1},
				{Name: "implementer", Count: 2},
				{Name: "reviewer", Count: 3},
			},
			want: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.specs.TotalCount(); got != tt.want {
				t.Errorf("PersonaSpecs.TotalCount() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPersonaSpecsType(t *testing.T) {
	var specs PersonaSpecs
	if got := specs.Type(); got != "name[:count]" {
		t.Errorf("PersonaSpecs.Type() = %v, want name[:count]", got)
	}
}

func TestMatchesPersonaAgentFilter(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		filter    string
		want      bool
	}{
		{name: "empty filter", agentType: "claude", filter: "", want: true},
		{name: "claude alias", agentType: "claude", filter: "claude_code", want: true},
		{name: "codex alias", agentType: "codex", filter: "openai-codex", want: true},
		{name: "gemini alias", agentType: "gemini", filter: "google_gemini", want: true},
		{name: "windsurf alias", agentType: "windsurf", filter: "ws", want: true},
		{name: "mismatch", agentType: "claude", filter: "codex", want: false},
		{name: "invalid filter", agentType: "claude", filter: "not-an-agent", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesPersonaAgentFilter(tt.agentType, tt.filter); got != tt.want {
				t.Errorf("matchesPersonaAgentFilter(%q, %q) = %v, want %v", tt.agentType, tt.filter, got, tt.want)
			}
		})
	}
}

func TestFormatAgentTypeCanonicalizesAliases(t *testing.T) {
	got := formatAgentType(" google-gemini ", theme.Plain, icons.ASCII)
	if got != "G gmi" {
		t.Errorf("formatAgentType() = %q, want %q", got, "G gmi")
	}

	got = formatAgentType("ws", theme.Plain, icons.ASCII)
	if got != "W windsurf" {
		t.Errorf("formatAgentType() = %q, want %q", got, "W windsurf")
	}
}

func TestDetermineSourceUsesUserConfigFallbackPath(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	userDir := filepath.Join(tmpDir, "xdg", "ntm")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("MkdirAll(project) failed: %v", err)
	}
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(user) failed: %v", err)
	}

	t.Setenv("NTM_CONFIG", filepath.Join(tmpDir, "custom-root", "config.toml"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))

	content := `
[[personas]]
name = "user-only"
agent_type = "claude"
system_prompt = "prompt"
`
	userPath := filepath.Join(userDir, "personas.toml")
	if err := os.WriteFile(userPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(user personas) failed: %v", err)
	}

	got := determineSourceFromProjectDir("user-only", projectDir)
	if !strings.Contains(got, userPath) {
		t.Fatalf("determineSourceFromProjectDir() = %q, want path containing %q", got, userPath)
	}
}

func TestDetermineSourcePrefersProjectOverBuiltinAndUser(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	userDir := filepath.Join(tmpDir, "xdg", "ntm")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("MkdirAll(project) failed: %v", err)
	}
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(user) failed: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))

	userContent := `
[[personas]]
name = "architect"
agent_type = "claude"
system_prompt = "user override"
`
	if err := os.WriteFile(filepath.Join(userDir, "personas.toml"), []byte(userContent), 0o644); err != nil {
		t.Fatalf("WriteFile(user personas) failed: %v", err)
	}

	projectContent := `
[[personas]]
name = "architect"
agent_type = "codex"
system_prompt = "project override"
`
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "personas.toml"), []byte(projectContent), 0o644); err != nil {
		t.Fatalf("WriteFile(project personas) failed: %v", err)
	}

	if got := determineSourceFromProjectDir("architect", projectDir); got != "project (.ntm/personas.toml)" {
		t.Fatalf("determineSourceFromProjectDir() = %q, want %q", got, "project (.ntm/personas.toml)")
	}
}
