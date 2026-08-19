package ensemble

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDryRunMode_Fields(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())
	mode := DryRunMode{
		ID:        "deductive",
		Code:      "A1",
		Name:      "Deductive Inference",
		Category:  "Formal",
		Tier:      "core",
		ShortDesc: "Classical logical inference",
	}

	t.Logf("TEST: %s - mode: %+v", t.Name(), mode)

	if mode.ID != "deductive" {
		t.Errorf("expected ID=deductive, got %s", mode.ID)
	}
	if mode.Code != "A1" {
		t.Errorf("expected Code=A1, got %s", mode.Code)
	}
	if mode.Name != "Deductive Inference" {
		t.Errorf("expected Name=Deductive Inference, got %s", mode.Name)
	}
	if mode.Category != "Formal" {
		t.Errorf("expected Category=Formal, got %s", mode.Category)
	}
	if mode.Tier != "core" {
		t.Errorf("expected Tier=core, got %s", mode.Tier)
	}
}

func TestDryRunAssign_Fields(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())
	assign := DryRunAssign{
		ModeID:      "deductive",
		ModeCode:    "A1",
		AgentType:   "cc",
		PaneIndex:   1,
		TokenBudget: 4000,
	}

	t.Logf("TEST: %s - assign: %+v", t.Name(), assign)

	if assign.ModeID != "deductive" {
		t.Errorf("expected ModeID=deductive, got %s", assign.ModeID)
	}
	if assign.ModeCode != "A1" {
		t.Errorf("expected ModeCode=A1, got %s", assign.ModeCode)
	}
	if assign.AgentType != "cc" {
		t.Errorf("expected AgentType=cc, got %s", assign.AgentType)
	}
	if assign.PaneIndex != 1 {
		t.Errorf("expected PaneIndex=1, got %d", assign.PaneIndex)
	}
	if assign.TokenBudget != 4000 {
		t.Errorf("expected TokenBudget=4000, got %d", assign.TokenBudget)
	}
}

func TestDryRunSynthesis_Fields(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())
	synthesis := DryRunSynthesis{
		Strategy:           "consensus",
		SynthesizerModeID:  "meta-cognitive",
		MinConfidence:      0.7,
		MaxFindings:        20,
		ConflictResolution: "voting",
	}

	t.Logf("TEST: %s - synthesis: %+v", t.Name(), synthesis)

	if synthesis.Strategy != "consensus" {
		t.Errorf("expected Strategy=consensus, got %s", synthesis.Strategy)
	}
	if synthesis.SynthesizerModeID != "meta-cognitive" {
		t.Errorf("expected SynthesizerModeID=meta-cognitive, got %s", synthesis.SynthesizerModeID)
	}
	if synthesis.MinConfidence != 0.7 {
		t.Errorf("expected MinConfidence=0.7, got %f", synthesis.MinConfidence)
	}
	if synthesis.MaxFindings != 20 {
		t.Errorf("expected MaxFindings=20, got %d", synthesis.MaxFindings)
	}
	if synthesis.ConflictResolution != "voting" {
		t.Errorf("expected ConflictResolution=voting, got %s", synthesis.ConflictResolution)
	}
}

func TestDryRun_BasicPreset(t *testing.T) {
	input := map[string]string{
		"session":  "dryrun-basic",
		"question": "Assess project health",
		"preset":   "project-diagnosis",
	}
	logTestStartDryRun(t, input)

	catalog, err := LoadModeCatalog()
	logTestResultDryRun(t, err)
	assertNoErrorDryRun(t, "load mode catalog", err)

	registry := NewEnsembleRegistry(EmbeddedEnsembles, catalog)
	preset := registry.Get(input["preset"])
	logTestResultDryRun(t, map[string]any{"preset_found": preset != nil})
	assertTrueDryRun(t, "preset found", preset != nil)

	modeIDs, err := preset.ResolveIDs(catalog)
	logTestResultDryRun(t, map[string]any{"preset": preset.Name, "modes": len(modeIDs), "err": err})
	assertNoErrorDryRun(t, "resolve preset modes", err)
	assertEqualDryRun(t, "preset name resolved", preset.Name, input["preset"])
	assertTrueDryRun(t, "modes resolved", len(modeIDs) > 0)
}

func TestDryRun_CustomModes(t *testing.T) {
	input := map[string]any{
		"session":  "dryrun-custom",
		"question": "Review architecture",
		"modes":    []string{"deductive", "mathematical-proof"},
	}
	logTestStartDryRun(t, input)

	catalog, err := LoadModeCatalog()
	assertNoErrorDryRun(t, "load mode catalog", err)
	refs := []ModeRef{
		{ID: input["modes"].([]string)[0]},
		{ID: input["modes"].([]string)[1]},
	}
	modeIDs, err := ResolveModeRefs(refs, catalog)
	logTestResultDryRun(t, modeIDs)
	assertNoErrorDryRun(t, "resolve custom mode refs", err)
	assertTrueDryRun(t, "includes deductive", containsStringDryRun(modeIDs, "deductive"))
	assertTrueDryRun(t, "includes mathematical-proof", containsStringDryRun(modeIDs, "mathematical-proof"))
}

func TestDryRun_OutputFormatting(t *testing.T) {
	plan := &DryRunPlan{
		GeneratedAt: time.Now().UTC(),
		SessionName: "dryrun-format",
		Question:    "Explain output formatting",
		PresetUsed:  "project-diagnosis",
		Modes: []DryRunMode{
			{ID: "deductive", Code: "A1", Name: "Deductive"},
		},
		Budget: DryRunBudget{EstimatedTotalTokens: 4000, ModeCount: 1},
		Validation: DryRunValidation{
			Valid: true,
		},
	}
	logTestStartDryRun(t, plan.SessionName)

	data, err := json.MarshalIndent(plan, "", "  ")
	logTestResultDryRun(t, string(data))
	assertNoErrorDryRun(t, "marshal dryrun plan", err)
	assertTrueDryRun(t, "includes session_name", strings.Contains(string(data), "\"session_name\""))
	assertTrueDryRun(t, "includes preset_used", strings.Contains(string(data), "\"preset_used\""))
}

func TestDryRun_RobotJSON(t *testing.T) {
	plan := &DryRunPlan{
		GeneratedAt: time.Now().UTC(),
		SessionName: "dryrun-robot",
		Question:    "Robot JSON dryrun",
		Validation:  DryRunValidation{Valid: true},
	}
	logTestStartDryRun(t, plan.SessionName)

	data, err := json.Marshal(plan)
	assertNoErrorDryRun(t, "marshal plan", err)

	var decoded map[string]any
	err = json.Unmarshal(data, &decoded)
	logTestResultDryRun(t, decoded)
	assertNoErrorDryRun(t, "unmarshal plan json", err)
	assertEqualDryRun(t, "session_name field", decoded["session_name"], plan.SessionName)
	assertEqualDryRun(t, "question field", decoded["question"], plan.Question)
}

func logTestStartDryRun(t *testing.T, input any) {
	t.Helper()
	t.Logf("TEST: %s - starting with input: %v", t.Name(), input)
}

func logTestResultDryRun(t *testing.T, result any) {
	t.Helper()
	t.Logf("TEST: %s - got result: %v", t.Name(), result)
}

func assertNoErrorDryRun(t *testing.T, desc string, err error) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if err != nil {
		t.Fatalf("%s: %v", desc, err)
	}
}

func assertEqualDryRun(t *testing.T, desc string, got, want any) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if got != want {
		t.Fatalf("%s: got %v want %v", desc, got, want)
	}
}

func assertTrueDryRun(t *testing.T, desc string, ok bool) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if !ok {
		t.Fatalf("assertion failed: %s", desc)
	}
}

func containsStringDryRun(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func TestDryRunValidation_Fields(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	// Valid case
	valid := DryRunValidation{
		Valid:    true,
		Warnings: []string{"large ensemble"},
	}
	t.Logf("TEST: %s - valid: %+v", t.Name(), valid)
	if !valid.Valid {
		t.Error("expected Valid=true")
	}
	if len(valid.Warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(valid.Warnings))
	}

	// Invalid case
	invalid := DryRunValidation{
		Valid:    false,
		Warnings: []string{"budget exceeded"},
		Errors:   []string{"mode not found"},
	}
	t.Logf("TEST: %s - invalid: %+v", t.Name(), invalid)
	if invalid.Valid {
		t.Error("expected Valid=false")
	}
	if len(invalid.Errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(invalid.Errors))
	}
}

func TestDryRunPreamble_Fields(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())
	preamble := DryRunPreamble{
		ModeID:   "deductive",
		ModeCode: "A1",
		Preview:  "You are an expert in deductive reasoning...",
		Length:   2500,
	}

	t.Logf("TEST: %s - preamble: %+v", t.Name(), preamble)

	if preamble.ModeID != "deductive" {
		t.Errorf("expected ModeID=deductive, got %s", preamble.ModeID)
	}
	if preamble.ModeCode != "A1" {
		t.Errorf("expected ModeCode=A1, got %s", preamble.ModeCode)
	}
	if preamble.Length != 2500 {
		t.Errorf("expected Length=2500, got %d", preamble.Length)
	}
}

func TestDryRunOptions_Fields(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())
	opts := DryRunOptions{
		IncludePreambles:      true,
		PreamblePreviewLength: 500,
	}

	t.Logf("TEST: %s - opts: %+v", t.Name(), opts)

	if !opts.IncludePreambles {
		t.Error("expected IncludePreambles=true")
	}
	if opts.PreamblePreviewLength != 500 {
		t.Errorf("expected PreamblePreviewLength=500, got %d", opts.PreamblePreviewLength)
	}
}
