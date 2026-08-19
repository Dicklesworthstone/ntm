package ensemble

import (
	"strings"
	"testing"
)

func TestFallbackModeOutputText(t *testing.T) {
	output := &ModeOutput{
		ModeID: "deductive",
		Thesis: "thesis",
		TopFindings: []Finding{
			{Finding: "finding one", Reasoning: "because"},
		},
		Risks: []Risk{
			{Risk: "risk one", Impact: ImpactLow, Likelihood: 0.2},
		},
		Recommendations: []Recommendation{
			{Recommendation: "do thing", Priority: ImpactMedium},
		},
		QuestionsForUser: []Question{
			{Question: "clarify", Context: "need context"},
		},
	}

	text := fallbackModeOutputText(output)
	if text == "" {
		t.Fatal("expected fallback output text")
	}
	if !strings.Contains(text, "Mode: deductive") {
		t.Fatalf("expected mode id in fallback text, got %q", text)
	}
	if !strings.Contains(text, "Thesis: thesis") {
		t.Fatalf("expected thesis in fallback text, got %q", text)
	}
	if !strings.Contains(text, "finding one") {
		t.Fatalf("expected finding in fallback text, got %q", text)
	}
	if !strings.Contains(text, "risk one") {
		t.Fatalf("expected risk in fallback text, got %q", text)
	}
	if !strings.Contains(text, "do thing") {
		t.Fatalf("expected recommendation in fallback text, got %q", text)
	}
	if !strings.Contains(text, "clarify") {
		t.Fatalf("expected question in fallback text, got %q", text)
	}
}
