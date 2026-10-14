package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareSystemPromptContentAddressedSnapshot(t *testing.T) {
	project := t.TempDir()
	p := &Persona{Name: "reviewer", AgentType: "claude", SystemPrompt: "Review {{project_name}}"}

	first, err := PrepareSystemPromptWithContext(p, project, &TemplateContext{ProjectName: "alpha", CustomVars: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareSystemPromptWithContext(p, project, &TemplateContext{ProjectName: "beta", CustomVars: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("different rendered prompts reused path %q", first)
	}
	firstData, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondData, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstData) != "Review alpha" || string(secondData) != "Review beta" {
		t.Fatalf("prepared snapshots changed: first=%q second=%q", firstData, secondData)
	}
	again, err := PrepareSystemPromptWithContext(p, project, &TemplateContext{ProjectName: "alpha", CustomVars: map[string]string{}})
	if err != nil || again != first {
		t.Fatalf("identical rendering = %q, %v; want stable %q", again, err, first)
	}
}

func TestPrepareSystemPromptContextOnly(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "CONTEXT.md"), []byte("only context"), 0644); err != nil {
		t.Fatal(err)
	}
	p := &Persona{Name: "contextual", AgentType: "claude", ContextFiles: []string{"CONTEXT.md"}}
	path, err := PrepareSystemPrompt(p, project)
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("context-only persona produced no prompt file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "only context") {
		t.Fatalf("context-only prompt = %q", data)
	}
}

func TestPrepareSystemPromptFailsClosedOnInvalidContext(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	p := &Persona{
		Name: "strict", AgentType: "claude", SystemPrompt: "do not launch without context",
		ContextFiles: []string{"docs"},
	}
	if path, err := PrepareSystemPrompt(p, project); err == nil || path != "" {
		t.Fatalf("invalid declared context was silently dropped: path=%q err=%v", path, err)
	}
}

func TestPrepareContextFilesDeduplicatesOverlappingSources(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "one.md"), []byte("unique payload"), 0644); err != nil {
		t.Fatal(err)
	}
	p := &Persona{Name: "dedupe", AgentType: "claude", ContextFiles: []string{"*.md", "one.md"}}
	got, err := PrepareContextFiles(p, project)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "unique payload") != 1 || strings.Count(got, "## one.md") != 1 {
		t.Fatalf("overlapping context sources were duplicated: %q", got)
	}
}

func TestPrepareContextFilesRejectsOversizedFile(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, "huge.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxPersonaContextFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	p := &Persona{Name: "bounded", AgentType: "claude", ContextFiles: []string{"huge.txt"}}
	if _, err := PrepareContextFiles(p, project); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized context accepted: %v", err)
	}
}
