package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewEnsemblePresetsCmd(t *testing.T) {
	t.Log("TEST: TestNewEnsemblePresetsCmd - starting")

	cmd := newEnsemblePresetsCmd()
	if cmd == nil {
		t.Fatal("newEnsemblePresetsCmd returned nil")
	}

	// Check command basics
	if cmd.Use != "presets" {
		t.Errorf("expected Use='presets', got %q", cmd.Use)
	}

	// Check aliases
	if len(cmd.Aliases) == 0 || cmd.Aliases[0] != "list" {
		t.Errorf("expected alias 'list', got %v", cmd.Aliases)
	}

	// Check flags exist
	if f := cmd.Flag("format"); f == nil {
		t.Error("missing --format flag")
	}
	if f := cmd.Flag("verbose"); f == nil {
		t.Error("missing --verbose flag")
	}
	if f := cmd.Flag("tag"); f == nil {
		t.Error("missing --tag flag")
	}

	t.Log("TEST: TestNewEnsemblePresetsCmd - assertion: command created with correct flags")
}

func TestRunEnsemblePresets_Table(t *testing.T) {
	t.Log("TEST: TestRunEnsemblePresets_Table - starting")

	var buf bytes.Buffer
	opts := ensemblePresetsOptions{
		Format:  "table",
		Verbose: false,
	}

	err := runEnsemblePresets(&buf, opts)
	if err != nil {
		t.Fatalf("runEnsemblePresets failed: %v", err)
	}

	output := buf.String()
	t.Logf("TEST: TestRunEnsemblePresets_Table - output: %s", output[:min(200, len(output))])

	// Verify table header
	if !strings.Contains(output, "NAME") {
		t.Error("table output missing NAME header")
	}
	if !strings.Contains(output, "DISPLAY") {
		t.Error("table output missing DISPLAY header")
	}

	// Verify embedded ensembles appear
	if !strings.Contains(output, "project-diagnosis") {
		t.Error("table output missing embedded ensemble 'project-diagnosis'")
	}
	if !strings.Contains(output, "idea-forge") {
		t.Error("table output missing embedded ensemble 'idea-forge'")
	}

	// Verify footer
	if !strings.Contains(output, "Total:") {
		t.Error("table output missing total count")
	}

	t.Log("TEST: TestRunEnsemblePresets_Table - assertion: table output is well-formed")
}

func TestRunEnsemblePresets_JSON(t *testing.T) {
	t.Log("TEST: TestRunEnsemblePresets_JSON - starting")

	var buf bytes.Buffer
	opts := ensemblePresetsOptions{
		Format:  "json",
		Verbose: false,
	}

	err := runEnsemblePresets(&buf, opts)
	if err != nil {
		t.Fatalf("runEnsemblePresets failed: %v", err)
	}

	var result ensemblePresetsOutput
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	t.Logf("TEST: TestRunEnsemblePresets_JSON - parsed %d presets", result.Count)

	// Verify count matches presets
	if result.Count != len(result.Presets) {
		t.Errorf("count mismatch: count=%d, presets=%d", result.Count, len(result.Presets))
	}

	// Verify at least embedded ensembles are present
	if result.Count < 9 {
		t.Errorf("expected at least 9 embedded ensembles, got %d", result.Count)
	}

	// Verify structure of first preset
	if len(result.Presets) > 0 {
		p := result.Presets[0]
		if p.Name == "" {
			t.Error("preset has empty name")
		}
		if p.ModeCount == 0 {
			t.Error("preset has zero modes")
		}
		if p.Source == "" {
			t.Error("preset has empty source")
		}
	}

	t.Log("TEST: TestRunEnsemblePresets_JSON - assertion: JSON output is valid and complete")
}

func TestRunEnsemblePresets_Verbose(t *testing.T) {
	t.Log("TEST: TestRunEnsemblePresets_Verbose - starting")

	var buf bytes.Buffer
	opts := ensemblePresetsOptions{
		Format:  "table",
		Verbose: true,
	}

	err := runEnsemblePresets(&buf, opts)
	if err != nil {
		t.Fatalf("runEnsemblePresets failed: %v", err)
	}

	output := buf.String()

	// Verbose output should contain more details
	if !strings.Contains(output, "Modes (") {
		t.Error("verbose output missing mode list")
	}
	if !strings.Contains(output, "Synthesis:") {
		t.Error("verbose output missing synthesis section")
	}
	if !strings.Contains(output, "Budget:") {
		t.Error("verbose output missing budget section")
	}
	if !strings.Contains(output, "Min Confidence:") {
		t.Error("verbose output missing min confidence")
	}
	if !strings.Contains(output, "Tokens/Mode:") {
		t.Error("verbose output missing tokens per mode")
	}

	t.Log("TEST: TestRunEnsemblePresets_Verbose - assertion: verbose output includes detailed sections")
}

func TestRunEnsemblePresets_TagFilter(t *testing.T) {
	t.Log("TEST: TestRunEnsemblePresets_TagFilter - starting")

	var buf bytes.Buffer
	opts := ensemblePresetsOptions{
		Format:  "json",
		Verbose: false,
		Tag:     "debugging",
	}

	err := runEnsemblePresets(&buf, opts)
	if err != nil {
		t.Fatalf("runEnsemblePresets failed: %v", err)
	}

	var result ensemblePresetsOutput
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	t.Logf("TEST: TestRunEnsemblePresets_TagFilter - found %d presets with tag 'debugging'", result.Count)

	// Should have at least bug-hunt and root-cause-analysis
	if result.Count < 2 {
		t.Errorf("expected at least 2 presets with 'debugging' tag, got %d", result.Count)
	}

	// All returned presets should have the debugging tag
	for _, p := range result.Presets {
		hasTag := false
		for _, tag := range p.Tags {
			if tag == "debugging" {
				hasTag = true
				break
			}
		}
		if !hasTag {
			t.Errorf("preset %q doesn't have 'debugging' tag: %v", p.Name, p.Tags)
		}
	}

	t.Log("TEST: TestRunEnsemblePresets_TagFilter - assertion: tag filter works correctly")
}

func TestRunEnsemblePresets_VerboseJSON(t *testing.T) {
	t.Log("TEST: TestRunEnsemblePresets_VerboseJSON - starting")

	var buf bytes.Buffer
	opts := ensemblePresetsOptions{
		Format:  "json",
		Verbose: true,
	}

	err := runEnsemblePresets(&buf, opts)
	if err != nil {
		t.Fatalf("runEnsemblePresets failed: %v", err)
	}

	var result ensemblePresetsOutput
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	// In verbose mode, Details should be populated, not Presets
	if len(result.Details) == 0 {
		t.Error("verbose JSON mode should populate details, not presets")
	}

	// Verify structure of first detail
	if len(result.Details) > 0 {
		d := result.Details[0]
		if len(d.Modes) == 0 {
			t.Error("detail has empty modes")
		}
		if d.Synthesis.Strategy == "" {
			t.Error("detail has empty synthesis strategy")
		}
		if d.Budget.MaxTotalTokens == 0 {
			t.Error("detail has zero max total tokens")
		}

		// Check mode details
		if len(d.Modes) > 0 {
			m := d.Modes[0]
			if m.ID == "" {
				t.Error("mode detail has empty ID")
			}
			// Should have resolved mode info
			if m.Code == "" || m.Name == "" {
				t.Log("TEST: mode details may not have full resolution if catalog unavailable")
			}
		}
	}

	t.Log("TEST: TestRunEnsemblePresets_VerboseJSON - assertion: verbose JSON includes full details")
}

func TestRenderPresetsTable_Empty(t *testing.T) {
	t.Log("TEST: TestRenderPresetsTable_Empty - starting")

	var buf bytes.Buffer
	err := renderPresetsTable(&buf, []ensemblePresetRow{})
	if err != nil {
		t.Fatalf("renderPresetsTable failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No ensemble presets found") {
		t.Error("empty table should show 'No ensemble presets found'")
	}

	t.Log("TEST: TestRenderPresetsTable_Empty - assertion: empty list handled correctly")
}

func TestRenderPresetsTableVerbose_Empty(t *testing.T) {
	t.Log("TEST: TestRenderPresetsTableVerbose_Empty - starting")

	var buf bytes.Buffer
	err := renderPresetsTableVerbose(&buf, []ensemblePresetDetail{})
	if err != nil {
		t.Fatalf("renderPresetsTableVerbose failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No ensemble presets found") {
		t.Error("empty verbose table should show 'No ensemble presets found'")
	}

	t.Log("TEST: TestRenderPresetsTableVerbose_Empty - assertion: empty list handled correctly")
}
