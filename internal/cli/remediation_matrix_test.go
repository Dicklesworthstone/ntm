package cli

// remediation_matrix_test.go provides a regression matrix that catches
// if any placeholder-remediation fixes get reverted.
//
// Beads: bd-1aae9.9.1, bd-1aae9.9.3, bd-1aae9.9.5

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

// --------------------------------------------------------------------------
// Task 1 (bd-1aae9.9.1): Verification matrix
// --------------------------------------------------------------------------

func TestRemediationMatrix(t *testing.T) {

	// repoRoot resolves the project root from this file's location.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to resolve caller")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	type matrixRow struct {
		beadID      string
		description string
		check       func(t *testing.T)
	}

	rows := []matrixRow{
		{
			beadID:      "bd-1aae9.1.*",
			description: "dummy_handlers.go should only contain package serve",
			check: func(t *testing.T) {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(repoRoot, "internal", "serve", "dummy_handlers.go"))
				if err != nil {
					t.Fatalf("read dummy_handlers.go: %v", err)
				}
				trimmed := strings.TrimSpace(string(data))
				if trimmed != "package serve" {
					t.Errorf("dummy_handlers.go should only contain 'package serve', got %d bytes", len(data))
				}
			},
		},
		{
			beadID:      "bd-1aae9.2.2",
			description: "diff command should NOT have --side-by-side flag",
			check: func(t *testing.T) {
				t.Helper()
				cmd := newDiffCmd()
				if f := cmd.Flags().Lookup("side-by-side"); f != nil {
					t.Error("diff command still has --side-by-side flag (should be removed)")
				}
			},
		},
		{
			beadID:      "bd-1aae9.2.3",
			description: "init command should NOT have --template flag",
			check: func(t *testing.T) {
				t.Helper()
				cmd := newInitCmd()
				if f := cmd.Flags().Lookup("template"); f != nil {
					t.Error("init command still has --template flag (should be removed)")
				}
			},
		},
		{
			beadID:      "bd-1aae9.3.2",
			description: "beads command should NOT have daemon subcommand",
			check: func(t *testing.T) {
				t.Helper()
				cmd := newBeadsCmd()
				for _, sub := range cmd.Commands() {
					if sub.Name() == "daemon" {
						t.Error("beads command still has 'daemon' subcommand (should be removed)")
					}
				}
			},
		},
		{
			beadID:      "bd-1aae9.7.5",
			description: "scanner should use LegacyRuleID constant (no literal :ubs)",
			check: func(t *testing.T) {
				t.Helper()
				// Verify the constant exists and has the expected value.
				if scanner.LegacyRuleID != "ubs-legacy" {
					t.Errorf("LegacyRuleID = %q, want %q", scanner.LegacyRuleID, "ubs-legacy")
				}
				// Grep scanner source files for raw ":ubs" literal that
				// should have been replaced by the constant reference.
				scannerDir := filepath.Join(repoRoot, "internal", "scanner")
				entries, err := os.ReadDir(scannerDir)
				if err != nil {
					t.Fatalf("read scanner dir: %v", err)
				}
				for _, e := range entries {
					if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
						continue
					}
					// Skip test files — they legitimately contain the string.
					if strings.HasSuffix(e.Name(), "_test.go") {
						continue
					}
					data, err := os.ReadFile(filepath.Join(scannerDir, e.Name()))
					if err != nil {
						t.Fatalf("read %s: %v", e.Name(), err)
					}
					if strings.Contains(string(data), `":ubs"`) {
						t.Errorf("%s contains literal \":ubs\" — should use scanner.LegacyRuleID constant", e.Name())
					}
				}
			},
		},
		{
			beadID:      "bd-1kcdi",
			description: "robot shortAgentType(windsurf) should return ws not win",
			check: func(t *testing.T) {
				t.Helper()
				// shortAgentType is unexported in internal/robot, but we
				// can verify via the local copy in this package.
				got := shortAgentTypeLocal("windsurf")
				if got == "win" {
					t.Error("shortAgentTypeLocal(\"windsurf\") = \"win\", should be \"ws\"")
				}
				if got != "ws" {
					t.Errorf("shortAgentTypeLocal(\"windsurf\") = %q, want \"ws\"", got)
				}
			},
		},
	}

	for _, row := range rows {
		t.Run(row.beadID+"_"+row.description, func(t *testing.T) {
			row.check(t)
		})
	}
}

// --------------------------------------------------------------------------
// Task 3 (bd-1aae9.9.3): Structured logging verification
//
// Verifies that key remediated packages use slog, not fmt.Println.
// --------------------------------------------------------------------------

func TestRemediatedFilesUseStructuredLogging(t *testing.T) {

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to resolve caller")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	// Specific files that were remediated and should use structured
	// logging (slog/output) rather than fmt.Println.
	// NOTE: diff.go and similar CLI commands use fmt.Printf/Println
	// for user-facing output, which is fine and expected.
	remediatedFiles := []string{
		filepath.Join("internal", "serve", "dummy_handlers.go"),
		filepath.Join("internal", "serve", "server.go"),
		filepath.Join("internal", "scanner", "bridge.go"),
		filepath.Join("internal", "scanner", "dedup.go"),
		filepath.Join("internal", "cli", "beads.go"),
	}

	for _, relPath := range remediatedFiles {
		relPath := relPath
		t.Run(relPath, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(repoRoot, relPath))
			if err != nil {
				t.Fatalf("read %s: %v", relPath, err)
			}
			if strings.Contains(string(data), "fmt.Println") {
				t.Errorf("%s contains fmt.Println — remediated files should use slog or output package", relPath)
			}
		})
	}
}

// --------------------------------------------------------------------------
// Task 5 (bd-1aae9.9.5): Help/docs audit
//
// Verify that the root command help output does not contain telltale
// placeholder strings.
// --------------------------------------------------------------------------

func TestHelpOutputNoPlaceholders(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"--help"})

	var buf strings.Builder
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	// Execute just parses and prints help; we ignore errors from
	// missing runtime dependencies (tmux, etc.) because we only
	// care about the static help text.
	_ = rootCmd.Execute()

	helpText := strings.ToLower(buf.String())

	forbidden := []string{
		"not implemented",
		"placeholder",
		"todo",
	}
	for _, phrase := range forbidden {
		if strings.Contains(helpText, phrase) {
			t.Errorf("root --help output contains %q", phrase)
		}
	}
}
