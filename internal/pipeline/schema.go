package pipeline

import (
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// SchemaVersion is the current workflow schema version
const SchemaVersion = "2.0"

// Workflow represents a complete workflow definition loaded from YAML/TOML
type Workflow struct {
	// Metadata
	SchemaVersion string `yaml:"schema_version" toml:"schema_version" json:"schema_version"`
	Name          string `yaml:"name" toml:"name" json:"name"`
	Description   string `yaml:"description,omitempty" toml:"description,omitempty" json:"description,omitempty"`
	Version       string `yaml:"version,omitempty" toml:"version,omitempty" json:"version,omitempty"`

	// Variable definitions
	Vars map[string]VarDef `yaml:"vars,omitempty" toml:"vars,omitempty" json:"vars,omitempty"`

	// Global settings
	Settings WorkflowSettings `yaml:"settings,omitempty" toml:"settings,omitempty" json:"settings,omitempty"`

	// Step definitions
	Steps []Step `yaml:"steps" toml:"steps" json:"steps"`
}

// VarDef defines a workflow variable with optional default and type info
type VarDef struct {
	Description string      `yaml:"description,omitempty" toml:"description,omitempty" json:"description,omitempty"`
	Required    bool        `yaml:"required,omitempty" toml:"required,omitempty" json:"required,omitempty"`
	Default     interface{} `yaml:"default,omitempty" toml:"default,omitempty" json:"default,omitempty"`
	Type        VarType     `yaml:"type,omitempty" toml:"type,omitempty" json:"type,omitempty"` // string, number, boolean, array
}

// VarType represents the type of a workflow variable
type VarType string

const (
	VarTypeString  VarType = "string"
	VarTypeNumber  VarType = "number"
	VarTypeBoolean VarType = "boolean"
	VarTypeArray   VarType = "array"
)

// WorkflowSettings contains global workflow configuration
type WorkflowSettings struct {
	Timeout          Duration    `yaml:"timeout,omitempty" toml:"timeout,omitempty" json:"timeout,omitempty"`    // Global timeout (e.g., "30m")
	OnError          ErrorAction `yaml:"on_error,omitempty" toml:"on_error,omitempty" json:"on_error,omitempty"` // fail, fail_fast, continue, retry
	NotifyOnComplete bool        `yaml:"notify_on_complete,omitempty" toml:"notify_on_complete,omitempty" json:"notify_on_complete,omitempty"`
	NotifyOnError    bool        `yaml:"notify_on_error,omitempty" toml:"notify_on_error,omitempty" json:"notify_on_error,omitempty"`
	NotifyChannels   []string    `yaml:"notify_channels,omitempty" toml:"notify_channels,omitempty" json:"notify_channels,omitempty"` // desktop, webhook, mail
	WebhookURL       string      `yaml:"webhook_url,omitempty" toml:"webhook_url,omitempty" json:"webhook_url,omitempty"`
	MailRecipient    string      `yaml:"mail_recipient,omitempty" toml:"mail_recipient,omitempty" json:"mail_recipient,omitempty"`
}

// Duration is a wrapper for time.Duration that supports YAML/TOML/JSON parsing
type Duration struct {
	time.Duration
}

// UnmarshalText implements encoding.TextUnmarshaler for Duration
func (d *Duration) UnmarshalText(text []byte) error {
	var err error
	d.Duration, err = time.ParseDuration(string(text))
	return err
}

// MarshalText implements encoding.TextMarshaler for Duration
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

// ErrorAction defines how to handle step errors
type ErrorAction string

const (
	ErrorActionFail     ErrorAction = "fail"      // Wait for all, report all errors
	ErrorActionFailFast ErrorAction = "fail_fast" // Cancel remaining on first error
	ErrorActionContinue ErrorAction = "continue"  // Ignore errors, continue workflow
	ErrorActionRetry    ErrorAction = "retry"     // Retry failed steps
)

// Step represents a single step in the workflow
type Step struct {
	// Identity
	ID   string `yaml:"id" toml:"id" json:"id"`                                     // Required, unique identifier
	Name string `yaml:"name,omitempty" toml:"name,omitempty" json:"name,omitempty"` // Human-readable name

	// Agent selection (choose one)
	Agent string          `yaml:"agent,omitempty" toml:"agent,omitempty" json:"agent,omitempty"` // Agent type: claude, codex, gemini, cursor, windsurf, aider, ollama
	Pane  int             `yaml:"pane,omitempty" toml:"pane,omitempty" json:"pane,omitempty"`    // Specific pane index
	Route RoutingStrategy `yaml:"route,omitempty" toml:"route,omitempty" json:"route,omitempty"` // Routing strategy

	// Prompt (choose one)
	Prompt     string `yaml:"prompt,omitempty" toml:"prompt,omitempty" json:"prompt,omitempty"`
	PromptFile string `yaml:"prompt_file,omitempty" toml:"prompt_file,omitempty" json:"prompt_file,omitempty"`

	// Wait configuration
	Wait    WaitCondition `yaml:"wait,omitempty" toml:"wait,omitempty" json:"wait,omitempty"` // completion, idle, time, none
	Timeout Duration      `yaml:"timeout,omitempty" toml:"timeout,omitempty" json:"timeout,omitempty"`

	// Dependencies
	DependsOn []string `yaml:"depends_on,omitempty" toml:"depends_on,omitempty" json:"depends_on,omitempty"`

	// Error handling
	OnError      ErrorAction `yaml:"on_error,omitempty" toml:"on_error,omitempty" json:"on_error,omitempty"`
	RetryCount   int         `yaml:"retry_count,omitempty" toml:"retry_count,omitempty" json:"retry_count,omitempty"`
	RetryDelay   Duration    `yaml:"retry_delay,omitempty" toml:"retry_delay,omitempty" json:"retry_delay,omitempty"`
	RetryBackoff string      `yaml:"retry_backoff,omitempty" toml:"retry_backoff,omitempty" json:"retry_backoff,omitempty"` // linear, exponential, none

	// Conditionals
	When string `yaml:"when,omitempty" toml:"when,omitempty" json:"when,omitempty"` // Skip if evaluates to false

	// Output handling
	OutputVar   string      `yaml:"output_var,omitempty" toml:"output_var,omitempty" json:"output_var,omitempty"`       // Store output in variable
	OutputParse OutputParse `yaml:"output_parse,omitempty" toml:"output_parse,omitempty" json:"output_parse,omitempty"` // none, json, yaml, lines, first_line, regex

	// Parallel execution (mutually exclusive with Prompt)
	Parallel []Step `yaml:"parallel,omitempty" toml:"parallel,omitempty" json:"parallel,omitempty"`

	// Loop execution
	Loop *LoopConfig `yaml:"loop,omitempty" toml:"loop,omitempty" json:"loop,omitempty"`

	// Loop control: break or continue (only valid inside loops)
	LoopControl LoopControl `yaml:"loop_control,omitempty" toml:"loop_control,omitempty" json:"loop_control,omitempty"`
}

// RoutingStrategy defines how to select an agent for a step
type RoutingStrategy string

const (
	RouteLeastLoaded    RoutingStrategy = "least-loaded"
	RouteFirstAvailable RoutingStrategy = "first-available"
	RouteRoundRobin     RoutingStrategy = "round-robin"
)

// WaitCondition defines when a step is considered complete
type WaitCondition string

const (
	WaitCompletion WaitCondition = "completion" // Wait for agent to return to idle
	WaitIdle       WaitCondition = "idle"       // Same as completion
	WaitTime       WaitCondition = "time"       // Wait for specified timeout only
	WaitNone       WaitCondition = "none"       // Fire and forget
)

// OutputParse defines how to parse step output
type OutputParse struct {
	Type    string `yaml:"type,omitempty" toml:"type,omitempty" json:"type,omitempty"`          // none, json, yaml, lines, first_line, regex
	Pattern string `yaml:"pattern,omitempty" toml:"pattern,omitempty" json:"pattern,omitempty"` // For regex type
}

// UnmarshalText allows OutputParse to be specified as a simple string
func (o *OutputParse) UnmarshalText(text []byte) error {
	o.Type = string(text)
	return nil
}

// LoopConfig defines loop iteration settings for for-each, while, and times loops
type LoopConfig struct {
	// For-each loop: iterate over array
	Items string `yaml:"items,omitempty" toml:"items,omitempty" json:"items,omitempty"` // Expression for array (e.g., ${vars.files})
	As    string `yaml:"as,omitempty" toml:"as,omitempty" json:"as,omitempty"`          // Loop variable name (default: "item")

	// While loop: repeat until condition is false
	While string `yaml:"while,omitempty" toml:"while,omitempty" json:"while,omitempty"` // Condition expression

	// Times loop: repeat N times
	Times int `yaml:"times,omitempty" toml:"times,omitempty" json:"times,omitempty"` // Number of iterations

	// Safety and timing
	MaxIterations int      `yaml:"max_iterations,omitempty" toml:"max_iterations,omitempty" json:"max_iterations,omitempty"` // Safety limit (default: 100, required for while loops)
	Delay         Duration `yaml:"delay,omitempty" toml:"delay,omitempty" json:"delay,omitempty"`                            // Delay between iterations

	// Result collection
	Collect string `yaml:"collect,omitempty" toml:"collect,omitempty" json:"collect,omitempty"` // Variable name to store array of results

	// Steps to execute per iteration
	Steps []Step `yaml:"steps,omitempty" toml:"steps,omitempty" json:"steps,omitempty"`
}

// LoopControl defines special control flow within loops
type LoopControl string

const (
	LoopControlNone     LoopControl = ""         // Normal execution
	LoopControlBreak    LoopControl = "break"    // Exit loop early
	LoopControlContinue LoopControl = "continue" // Skip to next iteration
)

// LoopContext holds the current state of loop iteration
type LoopContext struct {
	VarName string      // The "as" variable name
	Item    interface{} // Current item value
	Index   int         // 0-based iteration index
	Count   int         // Total number of items
	First   bool        // True if first iteration
	Last    bool        // True if last iteration
}

// DefaultMaxIterations is the default safety limit for loops
const DefaultMaxIterations = 100

// ExecutionStatus represents the current state of workflow execution
type ExecutionStatus string

const (
	StatusPending   ExecutionStatus = "pending"
	StatusRunning   ExecutionStatus = "running"
	StatusPaused    ExecutionStatus = "paused"
	StatusCompleted ExecutionStatus = "completed"
	StatusFailed    ExecutionStatus = "failed"
	StatusCancelled ExecutionStatus = "cancelled"
	StatusSkipped   ExecutionStatus = "skipped"
)

// StepResult contains the result of executing a step
type StepResult struct {
	StepID     string          `json:"step_id"`
	Status     ExecutionStatus `json:"status"`
	StartedAt  time.Time       `json:"started_at,omitempty"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`
	PaneUsed   string          `json:"pane_used,omitempty"`
	AgentType  string          `json:"agent_type,omitempty"`
	Output     string          `json:"output,omitempty"`
	ParsedData interface{}     `json:"parsed_data,omitempty"` // Result of output_parse
	Error      *StepError      `json:"error,omitempty"`
	SkipReason string          `json:"skip_reason,omitempty"` // If skipped due to 'when' condition
	Attempts   int             `json:"attempts,omitempty"`    // Number of retry attempts
}

// StepError contains detailed error information for a failed step
type StepError struct {
	Type       string    `json:"type"` // timeout, agent_error, crash, validation, routing, send, capture
	Message    string    `json:"message"`
	Details    string    `json:"details,omitempty"`     // Full error output
	PaneOutput string    `json:"pane_output,omitempty"` // Last N lines from pane for debugging
	AgentState string    `json:"agent_state,omitempty"` // Agent state at time of error
	Attempt    int       `json:"attempt,omitempty"`     // Which retry attempt
	Timestamp  time.Time `json:"timestamp"`
}

// ExecutionState contains the complete state of a workflow execution
type ExecutionState struct {
	RunID        string                 `json:"run_id"`
	WorkflowID   string                 `json:"workflow_id"`
	WorkflowFile string                 `json:"workflow_file,omitempty"`
	Session      string                 `json:"session,omitempty"`
	Status       ExecutionStatus        `json:"status"`
	StartedAt    time.Time              `json:"started_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
	FinishedAt   time.Time              `json:"finished_at,omitempty"`
	CurrentStep  string                 `json:"current_step,omitempty"`
	Steps        map[string]StepResult  `json:"steps"`
	Variables    map[string]interface{} `json:"variables"` // Runtime variables including step outputs
	Errors       []ExecutionError       `json:"errors,omitempty"`
}

// ExecutionError represents an error that occurred during execution
type ExecutionError struct {
	StepID    string    `json:"step_id,omitempty"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	Fatal     bool      `json:"fatal"`
}

// ProgressEvent is emitted during workflow execution for monitoring
type ProgressEvent struct {
	Type      string    `json:"type"` // step_start, step_complete, step_error, parallel_start, workflow_complete
	StepID    string    `json:"step_id,omitempty"`
	Message   string    `json:"message"`
	Progress  float64   `json:"progress"` // 0.0 - 1.0
	Timestamp time.Time `json:"timestamp"`
}

// ParallelGroupResult contains results from a parallel execution group
type ParallelGroupResult struct {
	Completed []StepResult `json:"completed"`
	Failed    []StepResult `json:"failed,omitempty"`
	Partial   bool         `json:"partial"` // Some succeeded, some failed
}

// DefaultWorkflowSettings returns sensible defaults for workflow settings
func DefaultWorkflowSettings() WorkflowSettings {
	return WorkflowSettings{
		Timeout:          Duration{Duration: 30 * time.Minute},
		OnError:          ErrorActionFail,
		NotifyOnComplete: false,
		NotifyOnError:    true,
	}
}

// DefaultStepTimeout returns the default timeout for a step
func DefaultStepTimeout() Duration {
	return Duration{Duration: 5 * time.Minute}
}

// NormalizeAgentType converts agent type aliases to canonical form.
// Case-insensitive: "Claude", "CLAUDE", "claude" all normalize to "claude".
func NormalizeAgentType(t string) string {
	trimmed := strings.TrimSpace(t)
	switch agent.AgentType(trimmed).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "codex"
	case agent.AgentTypeGemini:
		return "gemini"
	case agent.AgentTypeCursor:
		return "cursor"
	case agent.AgentTypeWindsurf:
		return "windsurf"
	case agent.AgentTypeAider:
		return "aider"
	case agent.AgentTypeOllama:
		return "ollama"
	default:
		return strings.ToLower(trimmed)
	}
}

// IsValidAgentType checks if the given agent type is recognized.
// Case-insensitive: "Claude", "CLAUDE", "claude" are all valid.
func IsValidAgentType(t string) bool {
	switch NormalizeAgentType(t) {
	case "claude", "codex", "gemini", "cursor", "windsurf", "aider", "oc", "ollama":
		return true
	default:
		return false
	}
}
