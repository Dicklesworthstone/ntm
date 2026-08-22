package plugins

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadAgentPlugins_ReadinessSection(t *testing.T) {
	dir := t.TempDir()
	toml := `[agent]
name = "omp"
alias = "om"
command = "omp{{if .Model}} --model {{shellQuote .Model}}{{end}}"

[agent.readiness]
idle_patterns = ['^\s*╰─.*─╯\s*$']
working_patterns = ['⟨esc⟩', '^\s*[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]\s*Working']
error_patterns = ['(?i)^error:']
`
	if err := os.WriteFile(filepath.Join(dir, "omp.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAgentPlugins(dir)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("LoadAgentPlugins = %v, %v", loaded, err)
	}
	p := loaded[0]
	if !reflect.DeepEqual(p.Readiness.IdlePatterns, []string{`^\s*╰─.*─╯\s*$`}) {
		t.Fatalf("idle_patterns = %v", p.Readiness.IdlePatterns)
	}
	if len(p.Readiness.WorkingPatterns) != 2 || len(p.Readiness.ErrorPatterns) != 1 {
		t.Fatalf("readiness = %+v", p.Readiness)
	}
	if p.ProbeCommand() != "omp" {
		t.Fatalf("ProbeCommand = %q, want omp", p.ProbeCommand())
	}
}

func TestAgentPlugin_ProbeCommand(t *testing.T) {
	cases := map[string]string{
		"omp":                             "omp",
		"omp --model x":                   "omp",
		"omp{{if .Model}} --model{{end}}": "omp",
		"  /usr/local/bin/hermes -y ":     "/usr/local/bin/hermes",
		"FOO=1 BAR=2 hermes --yolo":       "hermes",
		"{{.Binary}} --flag":              "",
		"":                                "",
		"FOO=1":                           "",
	}
	for cmd, want := range cases {
		if got := (AgentPlugin{Command: cmd}).ProbeCommand(); got != want {
			t.Errorf("ProbeCommand(%q) = %q, want %q", cmd, got, want)
		}
	}
}
