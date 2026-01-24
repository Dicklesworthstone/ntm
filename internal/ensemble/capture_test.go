package ensemble

import (
	"strings"
	"testing"
)

func TestOutputCaptureExtractYAML_LargestValid(t *testing.T) {
	capture := &OutputCapture{validator: NewSchemaValidator()}

	input := strings.Join([]string{
		"prefix",
		"```yaml",
		"thesis: short",
		"```",
		"noise",
		"```yaml",
		"mode_id: test-mode",
		"thesis: longer",
		"confidence: 0.6",
		"top_findings:",
		"  - finding: example",
		"    impact: high",
		"    confidence: 0.5",
		"    evidence_pointer: foo.go:1",
		"    reasoning: because",
		"```",
	}, "\n")

	got, ok := capture.extractYAML(input)
	if !ok {
		t.Fatalf("expected YAML block to be extracted")
	}
	if !strings.Contains(got, "mode_id: test-mode") {
		t.Fatalf("expected largest valid YAML block, got: %q", got)
	}
}

func TestOutputCaptureExtractYAML_InvalidBlocksFallbackToFirst(t *testing.T) {
	capture := &OutputCapture{validator: NewSchemaValidator()}

	input := strings.Join([]string{
		"```yaml",
		"thesis: [",
		"```",
		"```yaml",
		": invalid",
		"```",
	}, "\n")

	got, ok := capture.extractYAML(input)
	if !ok {
		t.Fatalf("expected YAML block to be extracted")
	}
	if strings.TrimSpace(got) != "thesis: [" {
		t.Fatalf("expected first YAML block, got: %q", got)
	}
}

func TestOutputCaptureExtractYAML_ThesisFallback(t *testing.T) {
	capture := &OutputCapture{validator: NewSchemaValidator()}

	input := strings.Join([]string{
		"prefix",
		"thesis: |",
		"  hello",
		"confidence: 0.4",
	}, "\n")

	got, ok := capture.extractYAML(input)
	if !ok {
		t.Fatalf("expected thesis fallback to return YAML")
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "thesis:") {
		t.Fatalf("expected thesis fallback content, got: %q", got)
	}
}

func TestOutputCaptureExtractYAML_None(t *testing.T) {
	capture := &OutputCapture{validator: NewSchemaValidator()}

	got, ok := capture.extractYAML("no yaml here")
	if ok {
		t.Fatalf("expected no YAML block, got: %q", got)
	}
	if got != "" {
		t.Fatalf("expected empty YAML result, got: %q", got)
	}
}
