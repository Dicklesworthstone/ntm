package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vhsRepoRoot() string {
	return filepath.Clean(filepath.Join("..", "..", ".."))
}

func vhsTapeDir() string {
	return filepath.Join(vhsRepoRoot(), "testdata", "vhs")
}

func TestVHSTapesExist(t *testing.T) {
	t.Parallel()

	expected := []string{
		"dashboard-basic.tape",
		"dashboard-navigation.tape",
		"dashboard-refresh.tape",
		"dashboard-resize.tape",
		"dashboard-minimum.tape",
		"dashboard-toast-animation.tape",
		"dashboard-fuzzy-filter.tape",
		"dashboard-table-scroll.tape",
		"dashboard-focus-ring.tape",
		"palette-fuzzy.tape",
		"dashboard-wide-layout.tape",
	}

	for _, tape := range expected {
		path := filepath.Join(vhsTapeDir(), tape)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("VHS tape missing %s: %v", tape, err)
		}
	}
}

func TestVHSTapesSyntaxValid(t *testing.T) {
	t.Parallel()

	tapes, err := filepath.Glob(filepath.Join(vhsTapeDir(), "*.tape"))
	if err != nil {
		t.Fatalf("glob VHS tapes: %v", err)
	}
	if len(tapes) == 0 {
		t.Fatal("expected at least one VHS tape")
	}

	for _, tape := range tapes {
		data, err := os.ReadFile(tape)
		if err != nil {
			t.Errorf("cannot read %s: %v", filepath.Base(tape), err)
			continue
		}

		content := string(data)
		base := strings.TrimSuffix(filepath.Base(tape), ".tape")
		expectedOutput := "Output testdata/screenshots/" + base + ".png"

		if !strings.Contains(content, "Output ") {
			t.Errorf("%s: missing Output directive", filepath.Base(tape))
		}
		if !strings.Contains(content, expectedOutput) {
			t.Errorf("%s: expected main output %q", filepath.Base(tape), expectedOutput)
		}
		if !strings.Contains(content, "Require \"./ntm\"") {
			t.Errorf("%s: missing Require ./ntm guard", filepath.Base(tape))
		}
		if !strings.Contains(content, "Set Width") {
			t.Errorf("%s: missing Set Width directive", filepath.Base(tape))
		}
		if !strings.Contains(content, "Set Height") {
			t.Errorf("%s: missing Set Height directive", filepath.Base(tape))
		}
		if strings.Contains(content, "--demo") {
			t.Errorf("%s: stale --demo flag present; use a current dashboard entrypoint", filepath.Base(tape))
		}
	}
}

func TestTUIInspectorProfilesExist(t *testing.T) {
	t.Parallel()

	root := os.Getenv("NTM_TUI_INSPECTOR_ROOT")
	if strings.TrimSpace(root) == "" {
		root = "/dp/tui_inspector"
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("tui_inspector root unavailable at %s: %v", root, err)
	}

	expected := []string{
		"ntm-toast-animation.env",
		"ntm-fuzzy-filter.env",
		// bd-jynig: TUI Inspector capture profiles for new tui-upgrade features
		"ntm-context-progress.env",
		"ntm-table-view.env",
		"ntm-spawn-wizard.env",
		"ntm-sparkline-status.env",
		"ntm-scroll-indicators.env",
		"ntm-toast-stacking.env",
	}
	for _, profile := range expected {
		path := filepath.Join(root, "profiles", profile)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("missing TUI Inspector profile %s: %v", profile, err)
			continue
		}
		content := string(data)
		if !strings.Contains(content, "profile_description=") {
			t.Errorf("%s: missing profile_description", profile)
		}
		if !strings.Contains(content, "keys=") {
			t.Errorf("%s: missing keys", profile)
		}
	}
}
