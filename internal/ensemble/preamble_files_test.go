package ensemble

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tokens"
	"gopkg.in/yaml.v3"
)

type preambleFile struct {
	ID       string `yaml:"id"`
	Code     string `yaml:"code"`
	Name     string `yaml:"name"`
	Tier     string `yaml:"tier"`
	Preamble string `yaml:"preamble"`
}

func loadPreambleFiles(t *testing.T) map[string]preambleFile {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join("preambles", "*.yaml"))
	if err != nil {
		t.Fatalf("glob preamble files: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no preamble files found")
	}

	sort.Strings(paths)
	files := make(map[string]preambleFile, len(paths))

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		var pf preambleFile
		if err := yaml.Unmarshal(data, &pf); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if pf.ID == "" {
			t.Fatalf("%s: missing id", path)
		}
		if _, exists := files[pf.ID]; exists {
			t.Fatalf("duplicate preamble id %q from %s", pf.ID, path)
		}
		files[pf.ID] = pf
	}

	return files
}

func TestPreambleFiles_CoreModesPresentAndValid(t *testing.T) {
	files := loadPreambleFiles(t)

	coreModes := make(map[string]ReasoningMode)
	for _, mode := range EmbeddedModes {
		if mode.Tier == TierCore {
			coreModes[mode.ID] = mode
		}
	}

	for id, mode := range coreModes {
		pf, ok := files[id]
		if !ok {
			t.Errorf("missing preamble file for core mode %s (%s)", mode.ID, mode.Code)
			continue
		}

		if pf.ID != mode.ID {
			t.Errorf("preamble id mismatch for %s: got %q", mode.ID, pf.ID)
		}
		if pf.Code != mode.Code {
			t.Errorf("preamble code mismatch for %s: got %q", mode.ID, pf.Code)
		}
		if pf.Name != mode.Name {
			t.Errorf("preamble name mismatch for %s: got %q", mode.ID, pf.Name)
		}
		if pf.Tier != string(TierCore) {
			t.Errorf("preamble tier mismatch for %s: got %q", mode.ID, pf.Tier)
		}
		if pf.Preamble == "" {
			t.Errorf("preamble content empty for %s", mode.ID)
		}

		required := []string{
			"## YOUR REASONING MODE",
			"### Approach",
			"### What You Produce",
			"### Best Applied To",
			"### Watch Out For (Failure Modes)",
			"### What Makes This Mode Unique",
		}
		for _, section := range required {
			if !strings.Contains(pf.Preamble, section) {
				t.Errorf("preamble %s missing required section %q", mode.ID, section)
			}
		}

		if !strings.Contains(pf.Preamble, mode.Name) {
			t.Errorf("preamble %s missing mode name", mode.ID)
		}
		if !strings.Contains(pf.Preamble, mode.Code) {
			t.Errorf("preamble %s missing mode code", mode.ID)
		}
		if !strings.Contains(pf.Preamble, mode.Category.String()) {
			t.Errorf("preamble %s missing category %q", mode.ID, mode.Category.String())
		}
	}
}

func TestPreambleFiles_TokenBudget(t *testing.T) {
	files := loadPreambleFiles(t)

	for id, pf := range files {
		tokensCount := tokens.EstimateTokensWithLanguageHint(pf.Preamble, tokens.ContentMarkdown)
		t.Logf("preamble %s token_estimate=%d", id, tokensCount)
		if tokensCount >= 2000 {
			t.Errorf("preamble %s exceeds token budget: %d", id, tokensCount)
		}
	}
}
