package templates

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Pre-compiled regex patterns for variable substitution
var (
	// simpleVarRe matches simple {{variable}} placeholders
	simpleVarRe = regexp.MustCompile(`\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}`)
	// conditionalOpenRe matches conditional opening tags {{#variable}}
	conditionalOpenRe = regexp.MustCompile(`\{\{#([a-zA-Z_][a-zA-Z0-9_]*)\}\}`)
)

// Parse parses a template from markdown content with YAML frontmatter.
// Format:
//
//	---
//	name: template_name
//	description: What this template does
//	variables:
//	  - name: file
//	    description: File path to review
//	    required: true
//	---
//	The template body with {{variable}} placeholders.
func Parse(content string) (*Template, error) {
	tmpl := &Template{}

	// Check for frontmatter
	if strings.HasPrefix(content, "---") {
		parts := strings.SplitN(content, "---", 3)
		if len(parts) >= 3 {
			// Parse YAML frontmatter strictly so typos fail loudly.
			frontmatter := []byte(parts[1])
			if len(bytes.TrimSpace(frontmatter)) > 0 {
				dec := yaml.NewDecoder(bytes.NewReader(frontmatter))
				dec.KnownFields(true)
				if err := dec.Decode(tmpl); err != nil && err != io.EOF {
					return nil, err
				}
			}
			tmpl.Body = strings.TrimSpace(parts[2])
		} else {
			// No valid frontmatter, treat entire content as body
			tmpl.Body = strings.TrimSpace(content)
		}
	} else {
		// No frontmatter, entire content is body
		tmpl.Body = strings.TrimSpace(content)
	}

	return tmpl, nil
}

// Execute substitutes variables in the template body.
func (t *Template) Execute(ctx ExecutionContext) (string, error) {
	// Validate required variables
	if err := t.Validate(ctx); err != nil {
		return "", err
	}

	// Build variable map: defaults < builtins < user vars < special vars
	vars := make(map[string]string)

	// Apply defaults from template definition
	for _, v := range t.Variables {
		if v.Default != "" {
			vars[v.Name] = v.Default
		}
	}

	// Apply builtin variables
	for k, v := range BuiltinVariables() {
		vars[k] = v
	}

	// Apply user-provided variables
	for k, v := range ctx.Variables {
		vars[k] = v
	}

	// Apply special context variables
	if ctx.FileContent != "" {
		vars["file"] = ctx.FileContent
	}
	if ctx.Session != "" {
		vars["session"] = ctx.Session
	}
	if ctx.Clipboard != "" {
		vars["clipboard"] = ctx.Clipboard
	}

	// Apply bead context variables
	if ctx.BeadID != "" {
		vars["bead_id"] = ctx.BeadID
		vars["BEAD_ID"] = ctx.BeadID // Also support uppercase for convenience
	}
	if ctx.BeadTitle != "" {
		vars["bead_title"] = ctx.BeadTitle
		vars["TITLE"] = ctx.BeadTitle // Common alias
	}
	if ctx.BeadPriority != "" {
		vars["bead_priority"] = ctx.BeadPriority
		vars["PRIORITY"] = ctx.BeadPriority
	}
	if ctx.BeadDescription != "" {
		vars["bead_description"] = ctx.BeadDescription
		vars["DESCRIPTION"] = ctx.BeadDescription
	}
	if ctx.BeadStatus != "" {
		vars["bead_status"] = ctx.BeadStatus
	}
	if ctx.BeadType != "" {
		vars["bead_type"] = ctx.BeadType
	}

	// Apply agent context variables
	if ctx.AgentNum > 0 {
		vars["agent_num"] = fmt.Sprintf("%d", ctx.AgentNum)
		vars["AGENT_NUM"] = vars["agent_num"]
	}
	if ctx.AgentType != "" {
		vars["agent_type"] = ctx.AgentType
		vars["AGENT_TYPE"] = ctx.AgentType
	}
	if ctx.AgentVariant != "" {
		vars["agent_variant"] = ctx.AgentVariant
		vars["VARIANT"] = ctx.AgentVariant
	}
	if ctx.AgentPane != "" {
		vars["agent_pane"] = ctx.AgentPane
	}

	// Apply send batch context variables
	if ctx.SendTotal > 0 {
		vars["send_index"] = fmt.Sprintf("%d", ctx.SendIndex)
		vars["send_total"] = fmt.Sprintf("%d", ctx.SendTotal)
		vars["send_num"] = fmt.Sprintf("%d", ctx.SendIndex+1) // 1-indexed for human readability
	}

	// Perform substitution
	result := t.Body

	// First, expand conditionals {{#var}}...{{/var}}
	result = expandConditionals(result, vars)

	// Then, substitute simple variables {{var}}
	result = substituteVariables(result, vars)

	return result, nil
}

// substituteVariables replaces {{variable}} placeholders with values.
// Note: The regex only matches simple variables like {{foo}}, not conditional
// markers like {{#var}} or {{/var}} (which don't start with [a-zA-Z_]).
func substituteVariables(body string, vars map[string]string) string {
	return simpleVarRe.ReplaceAllStringFunc(body, func(match string) string {
		// Extract variable name
		name := match[2 : len(match)-2]

		if val, ok := vars[name]; ok {
			return val
		}
		return match // Leave unmatched variables as-is
	})
}

// expandConditionals handles {{#variable}}...{{/variable}} blocks.
// If the variable is set and non-empty, the block content is included.
// Otherwise, the entire block is removed.
func expandConditionals(body string, vars map[string]string) string {
	// Process until no more matches (handles nested conditionals)
	for {
		matches := conditionalOpenRe.FindStringSubmatchIndex(body)
		if matches == nil {
			break // No more opening tags
		}

		// Extract variable name
		varName := body[matches[2]:matches[3]]
		openStart := matches[0]
		openEnd := matches[1]

		// Find matching closing tag
		closeTag := "{{/" + varName + "}}"
		closeStart := strings.Index(body[openEnd:], closeTag)
		if closeStart == -1 {
			// No matching close tag, leave as-is and skip
			break
		}
		closeStart += openEnd
		closeEnd := closeStart + len(closeTag)

		// Extract content between tags
		content := body[openEnd:closeStart]

		// Determine replacement
		var replacement string
		if val, ok := vars[varName]; ok && val != "" {
			replacement = content
		}
		// else: replacement is empty string, removing the block

		// Rebuild body
		body = body[:openStart] + replacement + body[closeEnd:]
	}

	return body
}

// macroRe matches @macro-name patterns for inline template expansion.
// Supports both hyphenated names (@marching-orders) and underscored names (@marching_orders).
var macroRe = regexp.MustCompile(`@([a-zA-Z][a-zA-Z0-9_-]*)`)
