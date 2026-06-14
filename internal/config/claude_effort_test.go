package config

import (
	"strings"
	"testing"
	"text/template"
)

// The default Claude command template must thread ReasoningEffort into claude's
// --effort flag (which the CLI supports), mirroring the Codex template's
// model_reasoning_effort. Regression for the silent effort-drop: spawning a
// Claude agent with an effort previously rendered no flag, forcing a fragile
// interactive /effort send that could strand the agent at the picker menu.
func TestClaudeTemplateThreadsEffort(t *testing.T) {
	fm := template.FuncMap{
		"memLimitPrefix": func() string { return "" },
		"shellQuote":     func(s string) string { return s },
	}
	tp := template.Must(template.New("claude").Funcs(fm).Parse(DefaultAgentTemplates().Claude))

	render := func(vars map[string]interface{}) string {
		var b strings.Builder
		if err := tp.Execute(&b, vars); err != nil {
			t.Fatalf("render: %v", err)
		}
		return b.String()
	}

	withEffort := render(map[string]interface{}{"Model": "claude-opus-4-8", "ReasoningEffort": "xhigh"})
	if !strings.Contains(withEffort, "--effort xhigh") {
		t.Errorf("Claude template dropped effort; got: %s", withEffort)
	}
	noEffort := render(map[string]interface{}{"Model": "claude-opus-4-8"})
	if strings.Contains(noEffort, "--effort") {
		t.Errorf("empty effort must omit --effort; got: %s", noEffort)
	}
}
