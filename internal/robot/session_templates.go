package robot

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/templates"
)

const (
	sessionTemplatesSchemaID      = "ntm:robot:session-templates:v1"
	sessionTemplatesSchemaVersion = "1.0.0"

	SessionTemplatesSchemaID      = sessionTemplatesSchemaID
	SessionTemplatesSchemaVersion = sessionTemplatesSchemaVersion

	ErrCodeTemplateNotFound = "TEMPLATE_NOT_FOUND"
)

// SessionTemplatesListResult is the CLI-compatible list payload for session templates.
type SessionTemplatesListResult struct {
	Templates []SessionTemplateInfo `json:"templates"`
	Total     int                   `json:"total"`
}

// SessionTemplateInfo summarizes a session template.
type SessionTemplateInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      string   `json:"source"`
	Tags        []string `json:"tags,omitempty"`
	AgentCount  int      `json:"agent_count"`
}

// SessionTemplateShowResult contains detailed inspection for a session template.
type SessionTemplateShowResult struct {
	Name        string                        `json:"name"`
	Description string                        `json:"description"`
	Source      string                        `json:"source"`
	Tags        []string                      `json:"tags,omitempty"`
	Agents      *templates.AgentsSpec         `json:"agents"`
	Prompts     *templates.PromptsSpec        `json:"prompts,omitempty"`
	CASS        *templates.CASSSpec           `json:"cass,omitempty"`
	Beads       *templates.BeadsSpec          `json:"beads,omitempty"`
	Options     *templates.SessionOptionsSpec `json:"options,omitempty"`
}

// SessionTemplatesOptions configures GetSessionTemplates.
type SessionTemplatesOptions struct {
	ProjectDir string
	Name       string
}

// SessionTemplatesOutput is the canonical robot envelope for --robot-session-templates and /api/v1/session-templates*.
type SessionTemplatesOutput struct {
	RobotResponse
	SchemaID      string                     `json:"schema_id"`
	SchemaVersion string                     `json:"schema_version"`
	Action        string                     `json:"action"`
	ProjectDir    string                     `json:"project_dir,omitempty"`
	Templates     []SessionTemplateInfo      `json:"templates,omitempty"`
	Total         *int                       `json:"total,omitempty"`
	Template      *SessionTemplateShowResult `json:"template,omitempty"`
	Suggestions   []string                   `json:"suggestions,omitempty"`
	AgentHints    *AgentHints                `json:"_agent_hints,omitempty"`
}

// SessionTemplateSuggestions builds actionable remediation hints when loading or validating a session template fails.
func SessionTemplateSuggestions(err error, name string) []string {
	if err == nil {
		return nil
	}
	errStr := err.Error()
	var suggestions []string
	if strings.Contains(errStr, "session template not found") {
		suggestions = append(suggestions, "Run `ntm session-templates list` to see available templates")
		suggestions = append(suggestions, "Check ~/.config/ntm/templates or .ntm/templates for custom templates")
		if strings.Compare(name, "") != 0 {
			suggestions = append(suggestions, fmt.Sprintf("Check spelling for template name %q", name))
		}
	}
	if strings.Contains(errStr, "validation failed") {
		suggestions = append(suggestions, "Fix validation errors in the template YAML")
		suggestions = append(suggestions, "Ensure metadata.name uses only letters, numbers, '-' or '_'")
		if strings.Compare(name, "") != 0 {
			suggestions = append(suggestions, fmt.Sprintf("Rename metadata.name to a valid value like %q", name))
		}
	}
	if len(suggestions) == 0 {
		suggestions = append(suggestions, "Run `ntm session-templates list` to see available templates")
	}
	return suggestions
}

// BuildSessionTemplateShowResult converts a validated SessionTemplate into SessionTemplateShowResult.
func BuildSessionTemplateShowResult(tmpl *templates.SessionTemplate) SessionTemplateShowResult {
	result := SessionTemplateShowResult{
		Name:        tmpl.Metadata.Name,
		Description: tmpl.Metadata.Description,
		Source:      tmpl.Metadata.Source,
		Tags:        tmpl.Metadata.Tags,
		Agents:      &tmpl.Spec.Agents,
	}
	if strings.Compare(tmpl.Spec.Prompts.Initial, "") != 0 || len(tmpl.Spec.Prompts.PerAgent) > 0 {
		result.Prompts = &tmpl.Spec.Prompts
	}
	if tmpl.Spec.CASS.Enabled != nil && *tmpl.Spec.CASS.Enabled {
		result.CASS = &tmpl.Spec.CASS
	}
	if tmpl.Spec.Beads.AutoAssign || strings.Compare(tmpl.Spec.Beads.Filter, "") != 0 {
		result.Beads = &tmpl.Spec.Beads
	}
	if tmpl.Spec.Options.Stagger != nil || tmpl.Spec.Options.Checkpoint != nil {
		result.Options = &tmpl.Spec.Options
	}
	return result
}

// GetSessionTemplates lists built-in, user, and project session templates or inspects one by name.
func GetSessionTemplates(opts SessionTemplatesOptions) (*SessionTemplatesOutput, error) {
	projectDir := strings.TrimSpace(opts.ProjectDir)
	loader := templates.NewSessionTemplateLoaderForProject(projectDir)
	targetName := strings.TrimSpace(opts.Name)

	if strings.Compare(targetName, "") != 0 {
		tmpl, err := loader.Load(targetName)
		if err != nil {
			code := ErrCodeInternalError
			if strings.Contains(err.Error(), "session template not found") {
				code = ErrCodeTemplateNotFound
			} else if strings.Contains(err.Error(), "validation failed") {
				code = ErrCodeInvalidFlag
			}
			suggestions := SessionTemplateSuggestions(err, targetName)
			return &SessionTemplatesOutput{
				RobotResponse: NewErrorResponse(err, code, suggestions[0]),
				SchemaID:      sessionTemplatesSchemaID,
				SchemaVersion: sessionTemplatesSchemaVersion,
				Action:        "show",
				ProjectDir:    projectDir,
				Suggestions:   suggestions,
			}, nil
		}
		if err := tmpl.Validate(); err != nil {
			suggestions := SessionTemplateSuggestions(err, targetName)
			return &SessionTemplatesOutput{
				RobotResponse: NewErrorResponse(err, ErrCodeInvalidFlag, suggestions[0]),
				SchemaID:      sessionTemplatesSchemaID,
				SchemaVersion: sessionTemplatesSchemaVersion,
				Action:        "show",
				ProjectDir:    projectDir,
				Suggestions:   suggestions,
			}, nil
		}
		detail := BuildSessionTemplateShowResult(tmpl)
		return &SessionTemplatesOutput{
			RobotResponse: NewRobotResponse(true),
			SchemaID:      sessionTemplatesSchemaID,
			SchemaVersion: sessionTemplatesSchemaVersion,
			Action:        "show",
			ProjectDir:    projectDir,
			Template:      &detail,
			AgentHints: &AgentHints{
				Summary: fmt.Sprintf("Loaded session template %q (%s source, %d agent(s)).", detail.Name, detail.Source, tmpl.GetAgentCount()),
			},
		}, nil
	}

	tmpls, err := loader.List()
	if err != nil {
		suggestions := SessionTemplateSuggestions(err, "")
		return &SessionTemplatesOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, suggestions[0]),
			SchemaID:      sessionTemplatesSchemaID,
			SchemaVersion: sessionTemplatesSchemaVersion,
			Action:        "list",
			ProjectDir:    projectDir,
			Templates:     []SessionTemplateInfo{},
			Suggestions:   suggestions,
		}, nil
	}
	items := make([]SessionTemplateInfo, len(tmpls))
	for i, t := range tmpls {
		items[i] = SessionTemplateInfo{
			Name:        t.Metadata.Name,
			Description: t.Metadata.Description,
			Source:      t.Metadata.Source,
			Tags:        t.Metadata.Tags,
			AgentCount:  t.GetAgentCount(),
		}
	}
	total := len(items)
	return &SessionTemplatesOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      sessionTemplatesSchemaID,
		SchemaVersion: sessionTemplatesSchemaVersion,
		Action:        "list",
		ProjectDir:    projectDir,
		Templates:     items,
		Total:         &total,
		AgentHints: &AgentHints{
			Summary: fmt.Sprintf("Loaded %d session template(s).", total),
		},
	}, nil
}

// PrintSessionTemplates writes the terminal JSON/TOON response for --robot-session-templates.
func PrintSessionTemplates(opts SessionTemplatesOptions) error {
	out, err := GetSessionTemplates(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot session-templates failed")
}

func sessionTemplatesCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "session-templates",
		Flag:        "--robot-session-templates",
		Category:    "spawn",
		Summary:     "List or inspect session templates.",
		Description: "List built-in, user, and project multi-agent session templates or inspect a template specification by name.",
		Parameters: []RobotParameter{
			{Name: "template-name", Flag: "--template-name", Type: "string", Required: false, Description: "Session template name to inspect in detail (omit to list all)"},
			{Name: "project", Flag: "--project", Type: "string", Required: false, Description: "Project directory for .ntm/templates overrides"},
		},
		Examples: []string{
			"ntm --robot-session-templates",
			"ntm --robot-session-templates --template-name=refactor",
		},
	}
}
