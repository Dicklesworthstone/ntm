package ensemble

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/codeblock"
	"github.com/Dicklesworthstone/ntm/internal/status"
)

// Synthesis path labels recorded on SynthesisResult.SynthesizedBy.
const (
	// SynthesisPathAgent means a lead synthesizer agent produced the result.
	SynthesisPathAgent = "agent"
	// SynthesisPathMechanical means deterministic merging produced the result.
	SynthesisPathMechanical = "mechanical"
)

const (
	// DefaultLeadSynthesisTimeout bounds how long we wait for the lead agent.
	DefaultLeadSynthesisTimeout = 5 * time.Minute
	// DefaultLeadPollInterval is how often the pane collector polls for output.
	DefaultLeadPollInterval = 5 * time.Second
	// defaultLeadCaptureLines is how many pane lines the collector captures.
	defaultLeadCaptureLines = 2000
)

// sampleSynthesisSummary is the sentinel summary embedded in the prompt's
// schema example. Lead responses that echo it back are rejected so the
// prompt echo is never mistaken for a real synthesis.
const sampleSynthesisSummary = "A unified thesis synthesizing key insights from all reasoning modes."

// leadResponseInstructions is appended to the synthesis prompt so the lead
// agent's final answer is machine-recoverable from pane scrollback.
const leadResponseInstructions = "\n## Response Envelope\n" +
	"Output your final synthesis as a single fenced code block (```json or ```yaml) " +
	"matching the schema above. Do not repeat the example values verbatim.\n"

// SynthesisDispatcher sends a synthesis prompt to the designated lead pane
// through the gated dispatch path (rate-limited, agent-aware injection).
type SynthesisDispatcher interface {
	DispatchSynthesisPrompt(paneTarget, agentType, prompt string) error
}

// SynthesisResponseCollector waits for the lead pane to produce a parseable
// synthesis response and returns the raw content containing it.
type SynthesisResponseCollector interface {
	CollectSynthesisResponse(ctx context.Context, paneTarget string) (string, error)
}

// LeadAgentConfig designates the lead pane used for agent-based synthesis
// and the ports used to reach it.
type LeadAgentConfig struct {
	// PaneTarget is the tmux pane target (ID or title) of the lead agent.
	PaneTarget string
	// AgentType is the agent kind in the lead pane (cc, cod, gmi).
	AgentType string
	// Timeout bounds the wait for the lead response (default 5m).
	Timeout time.Duration
	// Dispatcher sends the synthesis prompt to the pane.
	Dispatcher SynthesisDispatcher
	// Collector retrieves the lead agent's response.
	Collector SynthesisResponseCollector
}

func (c *LeadAgentConfig) validate() error {
	if c == nil {
		return errors.New("lead agent config is nil")
	}
	var errs []string
	if strings.TrimSpace(c.PaneTarget) == "" {
		errs = append(errs, "pane target is required")
	}
	if c.Dispatcher == nil {
		errs = append(errs, "dispatcher is required")
	}
	if c.Collector == nil {
		errs = append(errs, "collector is required")
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid lead agent config: %s", strings.Join(errs, "; "))
	}
	return nil
}

// agentSynthesize runs the real lead-agent synthesis path: it composes the
// synthesis prompt from member contributions, dispatches it to the lead pane,
// collects the response, and parses it into a SynthesisResult.
func (s *Synthesizer) agentSynthesize(ctx context.Context, input *SynthesisInput) (*SynthesisResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lead := s.Lead
	if err := lead.validate(); err != nil {
		return nil, err
	}

	prompt := s.GeneratePrompt(input)
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("generated synthesis prompt is empty")
	}
	prompt += leadResponseInstructions

	if err := lead.Dispatcher.DispatchSynthesisPrompt(lead.PaneTarget, lead.AgentType, prompt); err != nil {
		return nil, fmt.Errorf("dispatch synthesis prompt to lead pane %s: %w", lead.PaneTarget, err)
	}

	timeout := lead.Timeout
	if timeout <= 0 {
		timeout = DefaultLeadSynthesisTimeout
	}
	collectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	raw, err := lead.Collector.CollectSynthesisResponse(collectCtx, lead.PaneTarget)
	if err != nil {
		return nil, fmt.Errorf("collect lead synthesis response from pane %s: %w", lead.PaneTarget, err)
	}

	result, err := ParseLeadSynthesisResponse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse lead synthesis response: %w", err)
	}
	return result, nil
}

// ParseLeadSynthesisResponse parses a lead agent's pane content into a
// SynthesisResult. Unlike ParseSynthesisOutput, it only accepts fenced
// ```json / ```yaml blocks (so the unfenced schema example echoed back from
// the prompt is never parsed) and rejects the sample sentinel summary.
func ParseLeadSynthesisResponse(raw string) (*SynthesisResult, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("empty lead response")
	}

	clean := status.StripANSI(raw)
	blocks := codeblock.NewParser().WithLanguageFilter([]string{"json", "yaml"}).Parse(clean)
	if len(blocks) == 0 {
		return nil, errors.New("no fenced json/yaml block found in lead response")
	}

	// Prefer the latest parseable block: agents echo earlier content first.
	var lastErr error
	for i := len(blocks) - 1; i >= 0; i-- {
		content := blocks[i].Content
		result, err := unmarshalSynthesisResult(content)
		if err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(result.Summary) == "" {
			lastErr = errors.New("lead response missing summary")
			continue
		}
		if strings.TrimSpace(result.Summary) == sampleSynthesisSummary {
			lastErr = errors.New("lead response echoes the schema example; not a real synthesis")
			continue
		}
		result.RawOutput = content
		if result.GeneratedAt.IsZero() {
			result.GeneratedAt = time.Now().UTC()
		}
		return result, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no parseable synthesis block in lead response")
	}
	return nil, lastErr
}

func unmarshalSynthesisResult(content string) (*SynthesisResult, error) {
	var result SynthesisResult
	if err := json.Unmarshal([]byte(content), &result); err == nil {
		return &result, nil
	}
	result = SynthesisResult{}
	if err := yaml.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("block is neither valid JSON nor YAML: %w", err)
	}
	return &result, nil
}
