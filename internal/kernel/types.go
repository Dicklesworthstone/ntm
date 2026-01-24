package kernel

// SafetyLevel indicates the operational risk of a command.
type SafetyLevel string

const (
	SafetySafe    SafetyLevel = "safe"
	SafetyCaution SafetyLevel = "caution"
	SafetyDanger  SafetyLevel = "danger"
)

// SchemaRef points to an input or output schema used by a command.
// Ref should be a stable identifier (e.g., a Go type name or JSON Schema ref).
type SchemaRef struct {
	Name        string `json:"name,omitempty"`
	Ref         string `json:"ref,omitempty"`
	Description string `json:"description,omitempty"`
}

// RESTBinding describes the REST endpoint mapping for a command.
type RESTBinding struct {
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
}

// Example provides a usage example for OpenAPI and documentation.
type Example struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command,omitempty"`
	Input       string `json:"input,omitempty"`
	Output      string `json:"output,omitempty"`
}

// Command describes a kernel command with metadata for CLI/TUI/REST.
type Command struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Category    string       `json:"category,omitempty"`
	Input       *SchemaRef   `json:"input,omitempty"`
	Output      *SchemaRef   `json:"output,omitempty"`
	REST        *RESTBinding `json:"rest,omitempty"`
	Examples    []Example    `json:"examples,omitempty"`
	SafetyLevel SafetyLevel  `json:"safety_level,omitempty"`
	EmitsEvents []string     `json:"emits_events,omitempty"`
	Idempotent  bool         `json:"idempotent,omitempty"`
}

// ListResponse is the output schema for listing registered commands.
type ListResponse struct {
	Commands []Command `json:"commands"`
	Count    int       `json:"count"`
}
