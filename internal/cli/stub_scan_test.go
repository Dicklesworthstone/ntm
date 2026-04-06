package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// =============================================================================
// TestNoNewPlaceholders — production-code placeholder/stub scan
//
// Walks all .go files under internal/ (excluding _test.go and third_party/)
// and scans for patterns that indicate placeholder code. Maintains a documented
// allowlist for known acceptable cases such as build-tagged stub files that
// return proper errors.
//
// Bead: bd-1aae9.8.1
// =============================================================================

// placeholderPattern describes a regex pattern and its human-readable label.
type placeholderPattern struct {
	label   string
	pattern *regexp.Regexp
}

// placeholderPatterns are the patterns we scan for in production code.
var placeholderPatterns = []placeholderPattern{
	{"not yet implemented (case-insensitive)", regexp.MustCompile(`(?i)not\s+yet\s+implemented`)},
	{"not implemented yet (case-insensitive)", regexp.MustCompile(`(?i)not\s+implemented\s+yet`)},
	{"placeholder (case-insensitive, standalone word)", regexp.MustCompile(`(?i)\bplaceholder\b`)},
	{"TODO: Update patterns", regexp.MustCompile(`TODO:\s*Update\s+patterns`)},
}

// placeholderAllowlist maps relative file paths (from repo root) to a set of
// allowed pattern labels. These are known acceptable occurrences.
//
// Each entry MUST include a brief justification.
var placeholderAllowlist = map[string]map[string]string{
	// hooks.go returns a typed error for unknown hook types — this is a proper
	// error path, not placeholder code.
	"internal/hooks/hooks.go": {
		"not yet implemented (case-insensitive)": "error return for unrecognized hook type in switch default",
	},

	// robot_dashboard.go emits a markdown note listing unsupported attention
	// signals. This is informational output, not placeholder code.
	"internal/robot/robot_dashboard.go": {
		"not yet implemented (case-insensitive)": "informational markdown about unsupported signals",
	},

	// The redaction package uses "placeholder" in its domain — generating
	// redaction placeholders is core functionality, not stub code.
	"internal/redaction/redaction.go": {
		"placeholder (case-insensitive, standalone word)": "redaction placeholder is domain terminology",
	},

	// Redaction types uses "placeholder" for the Redacted field doc comment.
	"internal/redaction/types.go": {
		"placeholder (case-insensitive, standalone word)": "redaction placeholder field documentation",
	},

	// Config uses "placeholder" in comments describing redaction modes and
	// known-safe patterns.
	"internal/config/config.go": {
		"placeholder (case-insensitive, standalone word)": "config documentation for redaction mode",
	},

	// Checkpoint export expands ${WORKING_DIR} placeholder — domain term.
	"internal/checkpoint/export.go": {
		"placeholder (case-insensitive, standalone word)": "working dir placeholder expansion is domain terminology",
	},

	// Pipeline variables uses "placeholder" for escaped sequence substitution —
	// domain term for variable expansion.
	"internal/pipeline/variables.go": {
		"placeholder (case-insensitive, standalone word)": "variable substitution placeholder is domain terminology",
	},

	// Windows process attribute stub — documents that Setpgid is a no-op on Windows.
	"internal/supervisor/procattr_windows.go": {
		"placeholder (case-insensitive, standalone word)": "documents Windows no-op for process group attribute",
	},

	// TUI components use "placeholder" as the standard Bubble Tea/Lip Gloss term
	// for input field hint text — this is core UI terminology.
	"internal/palette/model.go": {
		"placeholder (case-insensitive, standalone word)": "Bubble Tea input field Placeholder style",
	},
	"internal/palette/xf_search.go": {
		"placeholder (case-insensitive, standalone word)": "Bubble Tea text input Placeholder value",
	},
	"internal/tui/components/cass_search.go": {
		"placeholder (case-insensitive, standalone word)": "Bubble Tea text input Placeholder value",
	},
	"internal/tui/dashboard/dashboard.go": {
		"placeholder (case-insensitive, standalone word)": "comment describing temporary display value before data loads",
	},
	"internal/tui/dashboard/layout.go": {
		"placeholder (case-insensitive, standalone word)": "layout spacing placeholder for border indicator alignment",
	},
	"internal/tui/dashboard/panels/spawn_wizard.go": {
		"placeholder (case-insensitive, standalone word)": "huh form field Placeholder values",
	},
	"internal/tui/theme/huh.go": {
		"placeholder (case-insensitive, standalone word)": "huh theme Placeholder style",
	},
	"internal/tui/theme/semantic.go": {
		"placeholder (case-insensitive, standalone word)": "semantic color doc comment for hint/placeholder text",
	},

	// CLI redact.go documents the redaction output format which includes placeholders.
	"internal/cli/redact.go": {
		"placeholder (case-insensitive, standalone word)": "documentation of redaction output format including placeholders",
	},

	// CLI spawn_wizard.go uses huh form field Placeholder() for agent count inputs.
	"internal/cli/spawn_wizard.go": {
		"placeholder (case-insensitive, standalone word)": "huh form field Placeholder values for agent count inputs",
	},

	// config/templates.go uses "placeholder" in an error message about template
	// syntax ({{.Model}} placeholder).
	"internal/config/templates.go": {
		"placeholder (case-insensitive, standalone word)": "error message about missing template syntax placeholder",
	},

	// integrations/dcg/hooks.go documents a command placeholder for Claude Code tool input.
	"internal/integrations/dcg/hooks.go": {
		"placeholder (case-insensitive, standalone word)": "comment documenting Claude Code tool input placeholder",
	},

	// CLI init_wizard.go uses Bubble Tea text input Placeholder for default agent count.
	"internal/cli/init_wizard.go": {
		"placeholder (case-insensitive, standalone word)": "Bubble Tea text input Placeholder for default value",
	},

	// CLI mail.go uses "placeholder" in a comment about subject field being unchanged
	// from the default — this is a conditional check, not stub code.
	"internal/cli/mail.go": {
		"placeholder (case-insensitive, standalone word)": "comment about default subject field being unchanged",
	},
}

// placeholderHit records a single match found during scanning.
type placeholderHit struct {
	file    string // relative path from repo root
	line    int
	content string
	label   string
}

func TestNoNewPlaceholders(t *testing.T) {
	t.Helper()

	repoRoot := findRepoRoot(t)
	internalDir := filepath.Join(repoRoot, "internal")

	var hits []placeholderHit

	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			// Skip third_party if it exists
			if info.Name() == "third_party" {
				return filepath.SkipDir
			}
			return nil
		}

		// Only scan .go files, skip test files
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		relPath, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}

		fileHits := scanFileForPlaceholders(t, path, relPath)
		hits = append(hits, fileHits...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}

	// Filter out allowlisted hits
	var violations []placeholderHit
	for _, hit := range hits {
		allowed := placeholderAllowlist[hit.file]
		if _, ok := allowed[hit.label]; ok {
			continue
		}
		violations = append(violations, hit)
	}

	if len(violations) > 0 {
		t.Run("violations", func(t *testing.T) {
			for _, v := range violations {
				t.Errorf("placeholder found — %s:%d [%s]\n  %s\n  "+
					"If this is intentional, add it to placeholderAllowlist in stub_scan_test.go",
					v.file, v.line, v.label, strings.TrimSpace(v.content))
			}
		})
	}

	// Report allowlist entries that no longer match (stale allowlist)
	t.Run("stale_allowlist", func(t *testing.T) {
		hitSet := make(map[string]map[string]bool)
		for _, h := range hits {
			if hitSet[h.file] == nil {
				hitSet[h.file] = make(map[string]bool)
			}
			hitSet[h.file][h.label] = true
		}
		for file, labels := range placeholderAllowlist {
			for label := range labels {
				if hitSet[file] == nil || !hitSet[file][label] {
					// File or pattern no longer matches — allowlist is stale.
					// This is a warning, not a failure, to avoid breaking on refactors.
					t.Logf("STALE allowlist entry: %s [%s] — pattern no longer found", file, label)
				}
			}
		}
	})
}

// scanFileForPlaceholders scans a single file for placeholder patterns.
func scanFileForPlaceholders(t *testing.T, absPath, relPath string) []placeholderHit {
	t.Helper()

	f, err := os.Open(absPath)
	if err != nil {
		t.Fatalf("open %s: %v", relPath, err)
	}
	defer f.Close()

	var hits []placeholderHit
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip comments that are documenting the pattern itself (e.g., allowlist docs)
		// We intentionally still scan — the allowlist handles false positives.

		for _, pp := range placeholderPatterns {
			if pp.pattern.MatchString(line) {
				hits = append(hits, placeholderHit{
					file:    relPath,
					line:    lineNum,
					content: line,
					label:   pp.label,
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning %s: %v", relPath, err)
	}
	return hits
}

// findRepoRoot walks up from the working directory to find the repo root
// (directory containing go.mod).
func findRepoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}

// TestNoNewPlaceholders_AllowlistDocumented ensures every allowlist entry has a justification.
func TestNoNewPlaceholders_AllowlistDocumented(t *testing.T) {
	t.Parallel()

	for file, labels := range placeholderAllowlist {
		for label, justification := range labels {
			if strings.TrimSpace(justification) == "" {
				t.Errorf("allowlist entry %s [%s] has no justification — "+
					"add a brief reason why this is acceptable", file, label)
			}
		}
		_ = fmt.Sprintf("checked %s", file) // prevent lint unused
	}
}
