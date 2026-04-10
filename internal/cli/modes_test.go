package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/output"
)

// =============================================================================
// renderModesList — 0% → 100%
// =============================================================================

func TestRenderModesList_JSON(t *testing.T) {

	payload := modesListOutput{
		GeneratedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Modes: []modesListRow{
			{ID: "deductive", Code: "A1", Name: "Deductive", Category: "Formal", Tier: "core", ShortDesc: "Formal logic"},
			{ID: "inductive", Code: "A2", Name: "Inductive", Category: "Causal", Tier: "advanced", ShortDesc: "Pattern-based"},
		},
		Count:  2,
		Filter: "tier=core",
	}

	var buf bytes.Buffer
	err := renderModesList(&buf, payload, "json")
	if err != nil {
		t.Fatalf("renderModesList(json): %v", err)
	}

	// Should be valid JSON
	var decoded modesListOutput
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	if decoded.Count != 2 {
		t.Errorf("Count = %d, want 2", decoded.Count)
	}
	if len(decoded.Modes) != 2 {
		t.Errorf("Modes len = %d, want 2", len(decoded.Modes))
	}
}

func TestRenderModesList_YAML(t *testing.T) {

	payload := modesListOutput{
		GeneratedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Modes: []modesListRow{
			{ID: "abductive", Code: "B1", Name: "Abductive", Category: "Causal", Tier: "core"},
		},
		Count: 1,
	}

	var buf bytes.Buffer
	err := renderModesList(&buf, payload, "yaml")
	if err != nil {
		t.Fatalf("renderModesList(yaml): %v", err)
	}

	// Should be valid YAML
	var decoded modesListOutput
	if err := yaml.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("YAML decode: %v", err)
	}
	if decoded.Count != 1 {
		t.Errorf("Count = %d, want 1", decoded.Count)
	}
}

func TestRenderModesList_Text(t *testing.T) {

	payload := modesListOutput{
		Modes: []modesListRow{
			{ID: "deductive", Code: "A1", Name: "Deductive", Category: "Formal", Tier: "core"},
		},
		Count: 1,
	}

	var buf bytes.Buffer
	err := renderModesList(&buf, payload, "text")
	if err != nil {
		t.Fatalf("renderModesList(text): %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "1 modes total") {
		t.Errorf("expected '1 modes total' in output, got: %s", out)
	}
}

func TestRenderModesList_TextEmpty(t *testing.T) {

	payload := modesListOutput{
		Modes: []modesListRow{},
		Count: 0,
	}

	var buf bytes.Buffer
	err := renderModesList(&buf, payload, "text")
	if err != nil {
		t.Fatalf("renderModesList(text empty): %v", err)
	}

	if !strings.Contains(buf.String(), "No modes found") {
		t.Errorf("expected 'No modes found' message, got: %s", buf.String())
	}
}

// =============================================================================
// renderModesExplain — 0% → 100%
// =============================================================================

func TestRenderModesExplain_JSON(t *testing.T) {

	payload := modesExplainOutput{
		GeneratedAt: output.Timestamp(),
		Card: &ensemble.ModeCard{
			ModeID:   "deductive",
			Code:     "A1",
			Name:     "Deductive",
			Category: "Formal",
			Tier:     "core",
		},
	}

	var buf bytes.Buffer
	err := renderModesExplain(&buf, payload, "json")
	if err != nil {
		t.Fatalf("renderModesExplain(json): %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	card := decoded["card"].(map[string]interface{})
	if card["mode_id"] != "deductive" {
		t.Errorf("card.mode_id = %v, want deductive", card["mode_id"])
	}
}

func TestRenderModesExplain_YAML(t *testing.T) {

	payload := modesExplainOutput{
		GeneratedAt: output.Timestamp(),
		Card: &ensemble.ModeCard{
			ModeID: "abductive",
			Code:   "B1",
			Name:   "Abductive",
		},
	}

	var buf bytes.Buffer
	err := renderModesExplain(&buf, payload, "yaml")
	if err != nil {
		t.Fatalf("renderModesExplain(yaml): %v", err)
	}

	if !strings.Contains(buf.String(), "abductive") {
		t.Errorf("expected 'abductive' in YAML output")
	}
}

func TestRenderModesExplain_Text(t *testing.T) {

	payload := modesExplainOutput{
		Card: &ensemble.ModeCard{
			ModeID:    "deductive",
			Code:      "A1",
			Name:      "Deductive",
			Category:  "Formal",
			Tier:      "core",
			ShortDesc: "Apply formal deductive logic",
		},
	}

	var buf bytes.Buffer
	err := renderModesExplain(&buf, payload, "text")
	if err != nil {
		t.Fatalf("renderModesExplain(text): %v", err)
	}

	if buf.Len() == 0 {
		t.Error("expected non-empty text output")
	}
}

// =============================================================================
// newModesCmd — command structure
// =============================================================================

func TestNewModesCmd(t *testing.T) {

	cmd := newModesCmd()
	if cmd.Use != "modes" {
		t.Errorf("Use = %q, want modes", cmd.Use)
	}

	// Should have list and explain subcommands
	subs := cmd.Commands()
	names := make(map[string]bool)
	for _, sub := range subs {
		names[sub.Name()] = true
	}
	if !names["list"] {
		t.Error("missing 'list' subcommand")
	}
	if !names["explain"] {
		t.Error("missing 'explain' subcommand")
	}
}

func TestNewModesListCmd_Flags(t *testing.T) {

	cmd := newModesListCmd()
	for _, flag := range []string{"format", "category", "tier", "all"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("missing flag: %s", flag)
		}
	}
}
