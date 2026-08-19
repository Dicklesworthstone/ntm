package ensemble

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeDispatcher records the dispatched prompt and optionally fails.
type fakeDispatcher struct {
	pane      string
	agentType string
	prompt    string
	err       error
	calls     int
}

func (d *fakeDispatcher) DispatchSynthesisPrompt(paneTarget, agentType, prompt string) error {
	d.calls++
	d.pane = paneTarget
	d.agentType = agentType
	d.prompt = prompt
	return d.err
}

// fakeCollector returns a canned response or fails.
type fakeCollector struct {
	response string
	err      error
	calls    int
	pane     string
}

func (c *fakeCollector) CollectSynthesisResponse(_ context.Context, paneTarget string) (string, error) {
	c.calls++
	c.pane = paneTarget
	if c.err != nil {
		return "", c.err
	}
	return c.response, nil
}

func leadTestOutputs() []ModeOutput {
	return []ModeOutput{
		{
			ModeID:     "deductive",
			Thesis:     "The system is sound.",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "Invariant holds", Impact: ImpactHigh, Confidence: 0.9},
			},
		},
		{
			ModeID:     "systems-thinking",
			Thesis:     "Feedback loops dominate.",
			Confidence: 0.6,
			TopFindings: []Finding{
				{Finding: "Coupling risk in stage 2", Impact: ImpactMedium, Confidence: 0.7},
			},
		},
	}
}

func leadTestInput() *SynthesisInput {
	return &SynthesisInput{
		Outputs:          leadTestOutputs(),
		OriginalQuestion: "Is the pipeline resilient?",
	}
}

const leadAgentResponse = "Working...\n" +
	"```json\n" +
	`{
  "summary": "Both modes agree the pipeline is broadly sound but stage 2 coupling needs attention.",
  "findings": [
    {"finding": "Stage 2 coupling is the dominant risk", "impact": "high", "confidence": 0.85}
  ],
  "confidence": 0.8
}` + "\n```\n"

func newAgentSynthesizer(t *testing.T, strategy SynthesisStrategy, lead *LeadAgentConfig) *Synthesizer {
	t.Helper()
	synth, err := NewSynthesizer(SynthesisConfig{Strategy: strategy, MaxFindings: 10, MinConfidence: 0.1})
	if err != nil {
		t.Fatalf("NewSynthesizer: %v", err)
	}
	synth.Lead = lead
	return synth
}

func TestAgentSynthesisComposesPromptAndParsesResponse(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	collector := &fakeCollector{response: leadAgentResponse}
	lead := &LeadAgentConfig{
		PaneTarget: "%42",
		AgentType:  "cc",
		Timeout:    time.Second,
		Dispatcher: dispatcher,
		Collector:  collector,
	}
	synth := newAgentSynthesizer(t, StrategyConsensus, lead)

	result, err := synth.SynthesizeContext(context.Background(), leadTestInput())
	if err != nil {
		t.Fatalf("SynthesizeContext: %v", err)
	}

	if dispatcher.calls != 1 {
		t.Fatalf("expected 1 dispatch, got %d", dispatcher.calls)
	}
	if dispatcher.pane != "%42" || dispatcher.agentType != "cc" {
		t.Errorf("dispatched to %q/%q, want %%42/cc", dispatcher.pane, dispatcher.agentType)
	}
	if collector.calls != 1 || collector.pane != "%42" {
		t.Errorf("collector called %d times on pane %q", collector.calls, collector.pane)
	}

	// Prompt composition: question, strategy, member contributions, schema.
	prompt := dispatcher.prompt
	for _, want := range []string{
		"You are the SYNTHESIZER",
		"Is the pipeline resilient?",
		"consensus",
		`"mode_id": "deductive"`,
		`"mode_id": "systems-thinking"`,
		"Invariant holds",
		"Coupling risk in stage 2",
		"## Output Format",
		"Response Envelope",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}

	// Response parsing.
	if result.SynthesizedBy != SynthesisPathAgent {
		t.Errorf("SynthesizedBy = %q, want %q", result.SynthesizedBy, SynthesisPathAgent)
	}
	if result.FallbackReason != "" {
		t.Errorf("unexpected fallback reason: %q", result.FallbackReason)
	}
	if !strings.Contains(result.Summary, "stage 2 coupling") {
		t.Errorf("summary not parsed from lead response: %q", result.Summary)
	}
	if len(result.Findings) != 1 || result.Findings[0].Finding != "Stage 2 coupling is the dominant risk" {
		t.Errorf("findings not parsed: %+v", result.Findings)
	}
}

func TestAgentStrategyWithoutLeadRecordsExplicitFallback(t *testing.T) {
	synth, err := NewSynthesizer(SynthesisConfig{Strategy: StrategyConsensus})
	if err != nil {
		t.Fatalf("NewSynthesizer: %v", err)
	}

	result, err := synth.Synthesize(leadTestInput())
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if result.SynthesizedBy != SynthesisPathMechanical {
		t.Errorf("SynthesizedBy = %q, want mechanical", result.SynthesizedBy)
	}
	if !strings.HasPrefix(result.FallbackReason, "fell back to mechanical: ") {
		t.Fatalf("fallback reason missing prefix: %q", result.FallbackReason)
	}
	if !strings.Contains(result.FallbackReason, "no lead agent configured") {
		t.Errorf("fallback reason should say no lead agent configured: %q", result.FallbackReason)
	}
	if !strings.Contains(result.FallbackReason, "consensus") {
		t.Errorf("fallback reason should name the strategy: %q", result.FallbackReason)
	}
}

func TestAgentDispatchFailureFallsBackWithReason(t *testing.T) {
	dispatcher := &fakeDispatcher{err: errors.New("pane vanished")}
	lead := &LeadAgentConfig{
		PaneTarget: "%1",
		AgentType:  "cc",
		Dispatcher: dispatcher,
		Collector:  &fakeCollector{response: leadAgentResponse},
	}
	synth := newAgentSynthesizer(t, StrategyAdversarial, lead)

	result, err := synth.SynthesizeContext(context.Background(), leadTestInput())
	if err != nil {
		t.Fatalf("SynthesizeContext: %v", err)
	}
	if result.SynthesizedBy != SynthesisPathMechanical {
		t.Errorf("SynthesizedBy = %q, want mechanical", result.SynthesizedBy)
	}
	if !strings.Contains(result.FallbackReason, "fell back to mechanical:") ||
		!strings.Contains(result.FallbackReason, "pane vanished") {
		t.Errorf("fallback reason should carry dispatch error: %q", result.FallbackReason)
	}
	// Mechanical result must still be usable.
	if result.Summary == "" || len(result.Findings) == 0 {
		t.Errorf("mechanical fallback produced empty result: %+v", result)
	}
}

func TestAgentCollectFailureFallsBackWithReason(t *testing.T) {
	lead := &LeadAgentConfig{
		PaneTarget: "%1",
		AgentType:  "cod",
		Dispatcher: &fakeDispatcher{},
		Collector:  &fakeCollector{err: errors.New("timed out waiting for lead")},
	}
	synth := newAgentSynthesizer(t, StrategyConsensus, lead)

	result, err := synth.SynthesizeContext(context.Background(), leadTestInput())
	if err != nil {
		t.Fatalf("SynthesizeContext: %v", err)
	}
	if result.SynthesizedBy != SynthesisPathMechanical {
		t.Errorf("SynthesizedBy = %q, want mechanical", result.SynthesizedBy)
	}
	if !strings.Contains(result.FallbackReason, "timed out waiting for lead") {
		t.Errorf("fallback reason should carry collect error: %q", result.FallbackReason)
	}
}

func TestAgentUnparseableResponseFallsBackWithReason(t *testing.T) {
	lead := &LeadAgentConfig{
		PaneTarget: "%1",
		AgentType:  "cc",
		Dispatcher: &fakeDispatcher{},
		Collector:  &fakeCollector{response: "I could not complete the synthesis, sorry."},
	}
	synth := newAgentSynthesizer(t, StrategyConsensus, lead)

	result, err := synth.SynthesizeContext(context.Background(), leadTestInput())
	if err != nil {
		t.Fatalf("SynthesizeContext: %v", err)
	}
	if result.SynthesizedBy != SynthesisPathMechanical {
		t.Errorf("SynthesizedBy = %q, want mechanical", result.SynthesizedBy)
	}
	if !strings.Contains(result.FallbackReason, "parse lead synthesis response") {
		t.Errorf("fallback reason should mention parse failure: %q", result.FallbackReason)
	}
}

func TestManualStrategyMechanicalPathUnchanged(t *testing.T) {
	synth, err := NewSynthesizer(SynthesisConfig{Strategy: StrategyManual})
	if err != nil {
		t.Fatalf("NewSynthesizer: %v", err)
	}
	result, err := synth.Synthesize(leadTestInput())
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if result.SynthesizedBy != SynthesisPathMechanical {
		t.Errorf("SynthesizedBy = %q, want mechanical", result.SynthesizedBy)
	}
	if result.FallbackReason != "" {
		t.Errorf("manual strategy must not record a fallback reason: %q", result.FallbackReason)
	}
	if result.Summary == "" || len(result.Findings) == 0 {
		t.Errorf("mechanical synthesis result incomplete: %+v", result)
	}
}

func TestParseLeadSynthesisResponseAcceptsYAML(t *testing.T) {
	raw := "noise before\n```yaml\nsummary: A real synthesis of the ensemble.\nconfidence: 0.7\n```\nnoise after"
	result, err := ParseLeadSynthesisResponse(raw)
	if err != nil {
		t.Fatalf("ParseLeadSynthesisResponse: %v", err)
	}
	if result.Summary != "A real synthesis of the ensemble." {
		t.Errorf("summary = %q", result.Summary)
	}
	if float64(result.Confidence) != 0.7 {
		t.Errorf("confidence = %v", result.Confidence)
	}
}

func TestParseLeadSynthesisResponseRejectsPromptEcho(t *testing.T) {
	// A pane echoing the prompt contains the unfenced schema example with the
	// sentinel summary; it must not be parsed as a real synthesis.
	synth, err := NewSynthesizer(SynthesisConfig{Strategy: StrategyConsensus})
	if err != nil {
		t.Fatalf("NewSynthesizer: %v", err)
	}
	echo := synth.GeneratePrompt(leadTestInput())
	if _, err := ParseLeadSynthesisResponse(echo); err == nil {
		t.Fatal("prompt echo must not parse as a synthesis response")
	}

	// Even fenced, the sentinel example must be rejected.
	fencedSentinel := "```json\n" + fmt.Sprintf(`{"summary": %q}`, sampleSynthesisSummary) + "\n```"
	if _, err := ParseLeadSynthesisResponse(fencedSentinel); err == nil {
		t.Fatal("fenced sentinel example must be rejected")
	}
}
