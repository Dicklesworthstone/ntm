package cli

import (
	"context"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/output"
)

// GitHub #352: `ntm deps --json` must report the flywheel tools the README
// Requirements section tells users to verify with `ntm deps -v`.
func TestFlywheelDependencyChecks_ReportsReadmeTools(t *testing.T) {
	toolDir := t.TempDir()
	writeFakeVersionTool(t, toolDir, "br", "br 0.9.1")
	writeFakeVersionTool(t, toolDir, "bv", "bv 0.15.0")
	writeFakeVersionTool(t, toolDir, "dcg", "dcg 0.15.3")
	writeFakeVersionTool(t, toolDir, "pt", "pt 2.0.0")
	t.Setenv("PATH", toolDir)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows := flywheelDependencyChecks(ctx)

	byName := make(map[string]output.DependencyCheck, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
		if row.Required {
			t.Errorf("flywheel tool %q marked required", row.Name)
		}
		if row.Category != "Flywheel Tools" {
			t.Errorf("flywheel tool %q category = %q", row.Name, row.Category)
		}
	}
	for _, name := range []string{"br", "bv", "dcg", "pt"} {
		row, ok := byName[name]
		if !ok {
			t.Fatalf("missing %q in flywheel rows: %+v", name, rows)
		}
		if !row.Installed || row.Path == "" {
			t.Errorf("%q: installed=%v path=%q, want installed with a path", name, row.Installed, row.Path)
		}
	}
	for _, name := range []string{"cass", "cm", "caam", "caut", "ubs"} {
		row, ok := byName[name]
		if !ok {
			t.Fatalf("missing %q in flywheel rows: %+v", name, rows)
		}
		if row.Installed {
			t.Errorf("%q reported installed with an empty PATH", name)
		}
	}
	if _, ok := byName["bd"]; ok {
		t.Errorf("beads row labelled bd although br is the installed binary")
	}
}

func TestFlywheelToolLabel_LegacyBeadsBinary(t *testing.T) {
	if got := flywheelToolLabel("bd", "/usr/local/bin/bd"); got != "bd" {
		t.Errorf("label for legacy bd binary = %q, want bd", got)
	}
	if got := flywheelToolLabel("bd", ""); got != "br" {
		t.Errorf("label for missing beads binary = %q, want br", got)
	}
	if got := flywheelToolLabel("dcg", "/x/dcg"); got != "dcg" {
		t.Errorf("label for dcg = %q", got)
	}
}
