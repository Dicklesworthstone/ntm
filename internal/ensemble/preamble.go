package ensemble

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"text/template"
)

// SchemaVersion is the current version of the output schema contract.
const SchemaVersion = "1.0.0"

// PreambleData provides the variables for rendering a preamble.
type PreambleData struct {
	// Problem is the user's question or problem statement.
	Problem string
	// ContextPack contains project context and user-provided framing.
	ContextPack *ContextPack
	// Mode is the reasoning mode to apply.
	Mode *ReasoningMode
	// TokenCap is the maximum tokens for the response.
	TokenCap int
	// OutputSchema is the YAML schema specification (optional override).
	OutputSchema string
}

// PreambleEngine renders complete preambles from templates and data.
type PreambleEngine struct {
	baseTemplate   *template.Template
	schemaContract string
}

// NewPreambleEngine creates a new engine with the default base template.
func NewPreambleEngine() *PreambleEngine {
	return &PreambleEngine{
		baseTemplate:   parseBaseTemplate(),
		schemaContract: GetSchemaContract(),
	}
}

// Render generates the complete preamble for a mode and data.
func (e *PreambleEngine) Render(data *PreambleData) (string, error) {
	if data == nil {
		return "", fmt.Errorf("preamble data is nil")
	}
	if data.Mode == nil {
		return "", fmt.Errorf("mode is required")
	}

	var buf bytes.Buffer

	// Prepare template data
	templateData := map[string]any{
		"Problem":        data.Problem,
		"Mode":           data.Mode,
		"TokenCap":       data.TokenCap,
		"SchemaVersion":  SchemaVersion,
		"SchemaContract": e.schemaContract,
	}

	// Add context pack if available
	if data.ContextPack != nil {
		templateData["ContextPack"] = formatContextPack(data.ContextPack)
	} else {
		templateData["ContextPack"] = "(No project context provided)"
	}

	// Render base instructions section
	if err := e.baseTemplate.Execute(&buf, templateData); err != nil {
		return "", fmt.Errorf("render base template: %w", err)
	}

	// Add mode section (explicit or auto-generated)
	modeSection := e.renderModeSection(data.Mode)
	buf.WriteString("\n\n")
	buf.WriteString(modeSection)

	// Add schema contract (mandatory)
	buf.WriteString("\n\n")
	buf.WriteString(e.schemaContract)

	result := buf.String()

	// Log rendering metrics
	slog.Debug("preamble rendered",
		"mode_id", data.Mode.ID,
		"mode_code", data.Mode.Code,
		"length", len(result),
		"schema_version", SchemaVersion,
	)

	return result, nil
}

// renderModeSection generates the mode-specific section.
// If a preamble key exists, it would load the explicit preamble (future: file lookup).
// Otherwise, auto-generates from mode metadata.
func (e *PreambleEngine) renderModeSection(mode *ReasoningMode) string {
	var sb strings.Builder

	sb.WriteString("## YOUR REASONING MODE\n\n")
	sb.WriteString(fmt.Sprintf("**Mode**: %s (%s)\n", mode.Name, mode.Code))
	sb.WriteString(fmt.Sprintf("**ID**: %s\n", mode.ID))
	sb.WriteString(fmt.Sprintf("**Category**: %s\n\n", mode.Category))

	// Description
	if mode.Description != "" {
		sb.WriteString("### Approach\n")
		sb.WriteString(mode.Description)
		sb.WriteString("\n\n")
	}

	// Outputs
	if mode.Outputs != "" {
		sb.WriteString("### What You Produce\n")
		sb.WriteString(mode.Outputs)
		sb.WriteString("\n\n")
	}

	// Best for
	if len(mode.BestFor) > 0 {
		sb.WriteString("### Best Applied To\n")
		for _, b := range mode.BestFor {
			sb.WriteString(fmt.Sprintf("- %s\n", b))
		}
		sb.WriteString("\n")
	}

	// Failure modes (critical for self-monitoring)
	if len(mode.FailureModes) > 0 {
		sb.WriteString("### Watch Out For (Failure Modes)\n")
		for _, f := range mode.FailureModes {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
		sb.WriteString("\n")
	}

	// Differentiator
	if mode.Differentiator != "" {
		sb.WriteString("### What Makes This Mode Unique\n")
		sb.WriteString(mode.Differentiator)
		sb.WriteString("\n\n")
	}

	// Tier warning for non-core modes
	if mode.Tier != TierCore {
		sb.WriteString(fmt.Sprintf("**Note**: This is a %s-tier mode. ", mode.Tier))
		switch mode.Tier {
		case TierAdvanced:
			sb.WriteString("It may require more expertise to apply effectively.\n")
		case TierExperimental:
			sb.WriteString("It is experimental and may produce inconsistent results.\n")
		}
	}

	return sb.String()
}

// formatContextPack renders the context pack as a structured string.
func formatContextPack(cp *ContextPack) string {
	if cp == nil {
		return "(No context available)"
	}

	var sb strings.Builder

	if cp.ProjectBrief != nil {
		pb := cp.ProjectBrief
		sb.WriteString(fmt.Sprintf("**Project**: %s\n", pb.Name))
		if pb.Description != "" {
			sb.WriteString(fmt.Sprintf("**Description**: %s\n", pb.Description))
		}
		if len(pb.Languages) > 0 {
			sb.WriteString(fmt.Sprintf("**Languages**: %s\n", strings.Join(pb.Languages, ", ")))
		}
		if len(pb.Frameworks) > 0 {
			sb.WriteString(fmt.Sprintf("**Frameworks**: %s\n", strings.Join(pb.Frameworks, ", ")))
		}
		if pb.OpenIssues > 0 {
			sb.WriteString(fmt.Sprintf("**Open Issues**: %d\n", pb.OpenIssues))
		}
		if pb.Structure != nil {
			sb.WriteString(fmt.Sprintf("**Files**: %d, **Lines**: %d\n",
				pb.Structure.TotalFiles, pb.Structure.TotalLines))
		}
	}

	if cp.UserContext != nil {
		uc := cp.UserContext
		if uc.ProblemStatement != "" {
			sb.WriteString(fmt.Sprintf("\n**Focus**: %s\n", uc.ProblemStatement))
		}
		if len(uc.FocusAreas) > 0 {
			sb.WriteString(fmt.Sprintf("**Areas**: %s\n", strings.Join(uc.FocusAreas, ", ")))
		}
		if len(uc.Constraints) > 0 {
			sb.WriteString(fmt.Sprintf("**Constraints**: %s\n", strings.Join(uc.Constraints, ", ")))
		}
		if len(uc.Stakeholders) > 0 {
			sb.WriteString(fmt.Sprintf("**Stakeholders**: %s\n", strings.Join(uc.Stakeholders, ", ")))
		}
		if len(uc.Decisions) > 0 {
			sb.WriteString(fmt.Sprintf("**Decisions**: %s\n", strings.Join(uc.Decisions, ", ")))
		}
		if len(uc.History) > 0 {
			sb.WriteString(fmt.Sprintf("**History**: %s\n", strings.Join(uc.History, ", ")))
		}
		if len(uc.SuccessCriteria) > 0 {
			sb.WriteString(fmt.Sprintf("**Success**: %s\n", strings.Join(uc.SuccessCriteria, ", ")))
		}
	}

	if len(cp.Questions) > 0 {
		sb.WriteString("\n**Questions**:\n")
		for _, q := range cp.Questions {
			label := q.Question
			if q.Required {
				label += " (required)"
			}
			if q.WhyAsked != "" {
				label = fmt.Sprintf("%s — %s", label, q.WhyAsked)
			}
			sb.WriteString(fmt.Sprintf("- %s\n", label))
			if q.DefaultHint != "" {
				sb.WriteString(fmt.Sprintf("  hint: %s\n", q.DefaultHint))
			}
		}
	}

	if sb.Len() == 0 {
		return "(Minimal context available)"
	}

	return sb.String()
}

// parseBaseTemplate creates the base instructions template.
func parseBaseTemplate() *template.Template {
	return template.Must(template.New("base").Parse(baseInstructionsTemplate))
}

// baseInstructionsTemplate is the Go text/template for base instructions.
const baseInstructionsTemplate = `# REASONING ENSEMBLE PARTICIPANT

You are participating in a multi-perspective reasoning ensemble.

## PROBLEM STATEMENT

{{.Problem}}

## PROJECT CONTEXT

{{.ContextPack}}

## YOUR ROLE

Apply your assigned reasoning mode rigorously. You are one of several agents, each applying a different reasoning lens to the same problem. A synthesizer will combine your outputs.

## CONSTRAINTS

1. **Stay in character**: Use only your assigned reasoning mode's techniques and vocabulary
2. **Be explicit about confidence**: Use numeric values (0.0-1.0) or levels (high|medium|low)
3. **Reference evidence**: Point to specific files, lines, or facts (e.g., path/to/file.go:142)
4. **Flag failure modes**: Note when your reasoning approach might be leading you astray
5. **Token budget**: DO NOT exceed {{.TokenCap}} tokens in your response`

// GetSchemaContract returns the mandatory output schema contract.
// All mode outputs MUST conform to this structure.
func GetSchemaContract() string {
	return schemaContractYAML
}

// schemaContractYAML is the YAML schema that all mode outputs must follow.
const schemaContractYAML = `## REQUIRED OUTPUT FORMAT

You MUST structure your response as valid YAML with these exact fields.
NON-COMPLIANT OUTPUTS WILL BE REJECTED.

` + "```yaml" + `
mode_id: {{mode_id}}           # Your mode identifier (e.g., "deductive")

thesis: |
  Your one-sentence main insight or conclusion

confidence: 0.0-1.0            # Overall confidence (numeric or high|medium|low)

top_findings:
  - finding: Description of what you discovered
    impact: critical|high|medium|low
    confidence: 0.0-1.0
    evidence_pointer: path/to/file.go:142
    reasoning: How you reached this conclusion

risks:
  - risk: Description of the risk
    impact: critical|high|medium|low
    likelihood: 0.0-1.0       # Likelihood (numeric or high|medium|low)
    mitigation: Suggested mitigation approach
    affected_areas:
      - area1
      - area2

recommendations:
  - recommendation: What action to take
    priority: critical|high|medium|low
    effort: low|medium|high
    rationale: Why this helps
    related_findings:
      - 0  # indices into top_findings (0-based)

questions_for_user:
  - question: What needs clarification?
    context: Why this matters for the analysis
    blocking: false
    suggested_answers:
      - possible answer 1
      - possible answer 2

failure_modes_to_watch:
  - mode: The failure mode your reasoning might exhibit
    description: What could go wrong
    indicators:
      - sign that this failure is occurring
    prevention: How to guard against it
` + "```" + `

## FIELD REQUIREMENTS

- **mode_id**: Required. Must match your assigned mode.
- **thesis**: Required. One concise sentence summarizing your main insight.
- **confidence**: Required. Numeric (0.0-1.0) or level (high|medium|low).
- **top_findings**: At least 1 finding. Each needs impact, confidence, evidence_pointer.
- **risks**: 0 or more. Include mitigation for each.
- **recommendations**: At least 1. Link to findings where applicable.
- **questions_for_user**: 0 or more. Only for genuine clarifications.
- **failure_modes_to_watch**: At least 1 from your mode's known failure patterns.

## SCHEMA VERSION

` + SchemaVersion
