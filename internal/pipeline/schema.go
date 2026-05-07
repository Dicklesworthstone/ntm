package pipeline

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// SchemaVersion is the current workflow schema version
const SchemaVersion = "2.0"

// Workflow represents a complete workflow definition loaded from YAML/TOML
type Workflow struct {
	// Metadata
	SchemaVersion string       `yaml:"schema_version" toml:"schema_version" json:"schema_version"`
	Name          string       `yaml:"name" toml:"name" json:"name"`
	Description   string       `yaml:"description,omitempty" toml:"description,omitempty" json:"description,omitempty"`
	Version       string       `yaml:"version,omitempty" toml:"version,omitempty" json:"version,omitempty"`
	Notes         StringOrList `yaml:"notes,omitempty" toml:"notes,omitempty" json:"notes,omitempty"` // Free-form authoring notes — accepts either a single string or a list of strings.

	// Variable definitions
	Vars map[string]VarDef `yaml:"vars,omitempty" toml:"vars,omitempty" json:"vars,omitempty"`

	// Inputs is a simpler list-of-name form for declared inputs. Each name in
	// Inputs is normalized into Vars during Workflow.Normalize() so downstream
	// passes only need to consult Vars. Useful for pipelines that just need to
	// declare "I take these inputs" without per-input metadata.
	Inputs []string `yaml:"inputs,omitempty" toml:"inputs,omitempty" json:"inputs,omitempty"`

	// Defaults is a workflow-level map of default values that downstream steps
	// can reference via ${defaults.foo}. Mostly used by orchestration-heavy
	// pipelines to factor out shared constants (model_mix, hard_caps, etc.).
	Defaults map[string]interface{} `yaml:"defaults,omitempty" toml:"defaults,omitempty" json:"defaults,omitempty"`

	// Outputs declares the names of artifacts/values this workflow produces.
	// Currently informational; the executor doesn't enforce them.
	Outputs []OutputDecl `yaml:"outputs,omitempty" toml:"outputs,omitempty" json:"outputs,omitempty"`

	// Global settings
	Settings WorkflowSettings `yaml:"settings,omitempty" toml:"settings,omitempty" json:"settings,omitempty"`

	// Step definitions
	Steps []Step `yaml:"steps" toml:"steps" json:"steps"`

	// PostPipelineSteps run after the main step graph completes successfully.
	// Failures here are reported but don't retroactively fail the pipeline.
	PostPipelineSteps []Step `yaml:"post_pipeline_steps,omitempty" toml:"post_pipeline_steps,omitempty" json:"post_pipeline_steps,omitempty"`
}

// OutputDecl is a workflow-level declaration of a produced output. The string
// form (a bare path) is also accepted for brevity.
type OutputDecl struct {
	Name        string `yaml:"name,omitempty" toml:"name,omitempty" json:"name,omitempty"`
	Description string `yaml:"description,omitempty" toml:"description,omitempty" json:"description,omitempty"`
	Path        string `yaml:"path,omitempty" toml:"path,omitempty" json:"path,omitempty"`
}

// UnmarshalYAML accepts three input forms:
//
//	outputs:
//	  - deliverables/HANDBACK.md                  # bare-string path
//	  - {name: report, path: ...}                 # full struct
//	  - workspace: ${workspace_path}              # single-key shorthand
//
// The single-key shorthand is convenient for pipelines that use the output
// list as a name-to-path table (so you can both name the output AND give it
// a path inline without repeating the name). Same callback-based unmarshal
// interface as internal/ensemble Confidence.
func (o *OutputDecl) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		o.Path = s
		return nil
	}
	// Try the structured form first (so an explicit {name, path, description}
	// mapping with no extra keys takes precedence).
	type rawDecl OutputDecl
	var raw rawDecl
	if err := unmarshal(&raw); err == nil && (raw.Name != "" || raw.Path != "" || raw.Description != "") {
		*o = OutputDecl(raw)
		return nil
	}
	// Fallback: single-key shorthand. The key becomes Name; the value (when
	// scalar) becomes Path.
	var generic map[string]interface{}
	if err := unmarshal(&generic); err != nil {
		return fmt.Errorf("output declaration must be a string, mapping, or {name: path}: %w", err)
	}
	if len(generic) != 1 {
		return fmt.Errorf("output single-key shorthand must have exactly one key, got %d", len(generic))
	}
	for k, v := range generic {
		o.Name = k
		switch tv := v.(type) {
		case string:
			o.Path = tv
		case nil:
			// Bare-name declaration with no path; preserve as-is.
		default:
			o.Path = fmt.Sprintf("%v", tv)
		}
	}
	return nil
}

// UnmarshalJSON accepts the same shorthand forms as YAML: a bare string path,
// the full structured object, or a single-key {name: path} object.
func (o *OutputDecl) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		*o = OutputDecl{}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		o.Path = s
		return nil
	}

	type rawDecl OutputDecl
	var raw rawDecl
	if err := json.Unmarshal(data, &raw); err == nil && (raw.Name != "" || raw.Path != "" || raw.Description != "") {
		*o = OutputDecl(raw)
		return nil
	}

	var generic map[string]interface{}
	if err := json.Unmarshal(data, &generic); err != nil {
		return fmt.Errorf("output declaration must be a string, mapping, or {name: path}: %w", err)
	}
	if len(generic) != 1 {
		return fmt.Errorf("output single-key shorthand must have exactly one key, got %d", len(generic))
	}
	for k, v := range generic {
		o.Name = k
		switch tv := v.(type) {
		case string:
			o.Path = tv
		case nil:
		default:
			o.Path = fmt.Sprintf("%v", tv)
		}
	}
	return nil
}

// UnmarshalTOML accepts the same shorthand forms as YAML and JSON.
func (o *OutputDecl) UnmarshalTOML(data any) error {
	if s, ok := data.(string); ok {
		o.Path = s
		return nil
	}
	m, ok := tomlMap(data)
	if !ok {
		return fmt.Errorf("output declaration must be a string, mapping, or {name: path}")
	}

	type rawDecl OutputDecl
	var raw rawDecl
	if err := decodeTOMLValue(m, &raw); err == nil && (raw.Name != "" || raw.Path != "" || raw.Description != "") {
		*o = OutputDecl(raw)
		return nil
	}

	if len(m) != 1 {
		return fmt.Errorf("output single-key shorthand must have exactly one key, got %d", len(m))
	}
	for k, v := range m {
		o.Name = k
		switch tv := v.(type) {
		case string:
			o.Path = tv
		case nil:
		default:
			o.Path = fmt.Sprintf("%v", tv)
		}
	}
	return nil
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
	Timeout  Duration    `yaml:"timeout,omitempty" toml:"timeout,omitempty" json:"timeout,omitempty"`    // Global timeout (e.g., "30m")
	OnError  ErrorAction `yaml:"on_error,omitempty" toml:"on_error,omitempty" json:"on_error,omitempty"` // fail, fail_fast, continue, retry
	OnCancel []Step      `yaml:"on_cancel,omitempty" toml:"on_cancel,omitempty" json:"on_cancel,omitempty"`
	// OnCancelTimeout caps the wall-clock budget for any single
	// on_cancel cleanup step. Defaults to 60s when zero/unset; a misbehaving
	// or hung cleanup step (slow NFS unlink, dead webhook, retry loop) cannot
	// block the executor forever (bd-new9w). Set to a longer duration for
	// genuinely heavy cleanup; never set to zero in user workflows.
	OnCancelTimeout  Duration     `yaml:"on_cancel_timeout,omitempty" toml:"on_cancel_timeout,omitempty" json:"on_cancel_timeout,omitempty"`
	Limits           LimitsConfig `yaml:"limits,omitempty" toml:"limits,omitempty" json:"limits,omitempty"`
	LogDispatch      *bool        `yaml:"log_dispatch,omitempty" toml:"log_dispatch,omitempty" json:"log_dispatch,omitempty"`
	NotifyOnComplete bool         `yaml:"notify_on_complete,omitempty" toml:"notify_on_complete,omitempty" json:"notify_on_complete,omitempty"`
	NotifyOnError    bool         `yaml:"notify_on_error,omitempty" toml:"notify_on_error,omitempty" json:"notify_on_error,omitempty"`
	NotifyChannels   []string     `yaml:"notify_channels,omitempty" toml:"notify_channels,omitempty" json:"notify_channels,omitempty"` // desktop, webhook, mail
	WebhookURL       string       `yaml:"webhook_url,omitempty" toml:"webhook_url,omitempty" json:"webhook_url,omitempty"`
	MailRecipient    string       `yaml:"mail_recipient,omitempty" toml:"mail_recipient,omitempty" json:"mail_recipient,omitempty"`
}

// DispatchLoggingEnabled reports whether template dispatch audit logs should
// be written. Nil means the operator did not set the option, so logs default on.
func (s WorkflowSettings) DispatchLoggingEnabled() bool {
	return s.LogDispatch == nil || *s.LogDispatch
}

// LimitsConfig defines resource caps to prevent runaway pipelines.
// Zero values mean "use default".
type LimitsConfig struct {
	MaxForeachIterations  int   `yaml:"max_foreach_iterations,omitempty" toml:"max_foreach_iterations,omitempty" json:"max_foreach_iterations,omitempty"`
	MaxConcurrentForeach  int   `yaml:"max_concurrent_foreach,omitempty" toml:"max_concurrent_foreach,omitempty" json:"max_concurrent_foreach,omitempty"`
	MaxCommandStdoutBytes int64 `yaml:"max_command_stdout_bytes,omitempty" toml:"max_command_stdout_bytes,omitempty" json:"max_command_stdout_bytes,omitempty"`
	MaxCommandStderrBytes int64 `yaml:"max_command_stderr_bytes,omitempty" toml:"max_command_stderr_bytes,omitempty" json:"max_command_stderr_bytes,omitempty"`
	MaxTemplateBytes      int64 `yaml:"max_template_bytes,omitempty" toml:"max_template_bytes,omitempty" json:"max_template_bytes,omitempty"`
	MaxStepCountTotal     int   `yaml:"max_step_count_total,omitempty" toml:"max_step_count_total,omitempty" json:"max_step_count_total,omitempty"`
	MaxSubstitutionDepth  int   `yaml:"max_substitution_recursion,omitempty" toml:"max_substitution_recursion,omitempty" json:"max_substitution_recursion,omitempty"`
}

const (
	DefaultMaxForeachIterations  = 10000
	DefaultMaxConcurrentForeach  = 16
	DefaultMaxCommandStdoutBytes = 16 * 1024 * 1024 // 16 MB
	DefaultMaxCommandStderrBytes = 4 * 1024 * 1024  // 4 MB
	DefaultMaxTemplateBytes      = 256 * 1024       // 256 KB
	DefaultMaxStepCountTotal     = 100000
	DefaultMaxSubstitutionDepth  = 8
)

// EffectiveLimits returns the limits config with defaults applied for zero values.
func (lc LimitsConfig) EffectiveLimits() LimitsConfig {
	if lc.MaxForeachIterations <= 0 {
		lc.MaxForeachIterations = DefaultMaxForeachIterations
	}
	if lc.MaxConcurrentForeach <= 0 {
		lc.MaxConcurrentForeach = DefaultMaxConcurrentForeach
	}
	if lc.MaxCommandStdoutBytes <= 0 {
		lc.MaxCommandStdoutBytes = DefaultMaxCommandStdoutBytes
	}
	if lc.MaxCommandStderrBytes <= 0 {
		lc.MaxCommandStderrBytes = DefaultMaxCommandStderrBytes
	}
	if lc.MaxTemplateBytes <= 0 {
		lc.MaxTemplateBytes = DefaultMaxTemplateBytes
	}
	if lc.MaxStepCountTotal <= 0 {
		lc.MaxStepCountTotal = DefaultMaxStepCountTotal
	}
	if lc.MaxSubstitutionDepth <= 0 {
		lc.MaxSubstitutionDepth = DefaultMaxSubstitutionDepth
	}
	return lc
}

// Duration is a wrapper for time.Duration that supports YAML/TOML/JSON parsing
type Duration struct {
	time.Duration
}

// UnmarshalText implements encoding.TextUnmarshaler for Duration.
// Accepts both Go's time.ParseDuration form ("5m", "30s") and bare integers
// interpreted as seconds (so "300" parses as 5 minutes). The bare-integer form
// is convenient in YAML where operators routinely write `timeout: 300` without
// thinking about units.
func (d *Duration) UnmarshalText(text []byte) error {
	s := strings.TrimSpace(string(text))
	if s == "" {
		d.Duration = 0
		return nil
	}
	// Try canonical Go duration first (preserves sub-second precision).
	if dur, err := time.ParseDuration(s); err == nil {
		d.Duration = dur
		return nil
	}
	// Fall back to bare-integer-seconds parse.
	if n, err := strconv.Atoi(s); err == nil {
		d.Duration = time.Duration(n) * time.Second
		return nil
	}
	return fmt.Errorf("invalid duration %q (want '5m'/'30s' format or bare integer seconds)", s)
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

	// Description is a free-form authoring note. The executor doesn't use it.
	// Exists for symmetry with workflow-level Description and to let pipelines
	// document each step inline.
	Description string `yaml:"description,omitempty" toml:"description,omitempty" json:"description,omitempty"`

	// Agent selection (choose one)
	Agent string          `yaml:"agent,omitempty" toml:"agent,omitempty" json:"agent,omitempty"` // Agent type: claude, codex, gemini, cursor, windsurf, aider, ollama
	Pane  PaneSpec        `yaml:"pane,omitempty" toml:"pane,omitempty" json:"pane,omitempty"`    // Specific pane index (int) OR template expression (string)
	Route RoutingStrategy `yaml:"route,omitempty" toml:"route,omitempty" json:"route,omitempty"` // Routing strategy

	// Prompt (choose one)
	Prompt     string `yaml:"prompt,omitempty" toml:"prompt,omitempty" json:"prompt,omitempty"`
	PromptFile string `yaml:"prompt_file,omitempty" toml:"prompt_file,omitempty" json:"prompt_file,omitempty"`

	// Command is a shell command to execute as the step body. Mutually
	// exclusive with Prompt/PromptFile. The command runs via /bin/sh -c, so
	// pipelines and globs work normally. Stdout is captured for OutputVar /
	// OutputParse just like agent prompts. Wait/timeout/retry/on_error
	// behavior is identical to the agent-prompt path.
	Command string `yaml:"command,omitempty" toml:"command,omitempty" json:"command,omitempty"`

	// Args is a key-value bag whose meaning depends on the step kind:
	//   - For Command steps: each entry becomes an environment variable.
	//   - For Template steps: each entry becomes a template-substitution param.
	// Values are stringified (numbers and bools become their string form).
	Args map[string]interface{} `yaml:"args,omitempty" toml:"args,omitempty" json:"args,omitempty"`

	// Template names a marching-order or prompt template to render. The
	// executor reads the file, substitutes Params, and dispatches the rendered
	// text as the step's prompt. Mutually exclusive with Prompt/PromptFile.
	Template string `yaml:"template,omitempty" toml:"template,omitempty" json:"template,omitempty"`

	// Params are template-substitution parameters. Equivalent to Args for
	// template steps; offered as a separate field for readability.
	Params map[string]interface{} `yaml:"params,omitempty" toml:"params,omitempty" json:"params,omitempty"`

	// TemplateParams is a third spelling some pipelines use. Normalized into
	// Params during Workflow.Normalize().
	TemplateParams map[string]interface{} `yaml:"template_params,omitempty" toml:"template_params,omitempty" json:"template_params,omitempty"`

	// Wait configuration
	Wait    WaitCondition `yaml:"wait,omitempty" toml:"wait,omitempty" json:"wait,omitempty"` // completion, idle, time, none
	Timeout Duration      `yaml:"timeout,omitempty" toml:"timeout,omitempty" json:"timeout,omitempty"`

	// Dependencies
	DependsOn []string `yaml:"depends_on,omitempty" toml:"depends_on,omitempty" json:"depends_on,omitempty"`

	// After is an alias for DependsOn that accepts either a single step ID
	// (string) or a list of step IDs. Normalized into DependsOn during
	// Workflow.Normalize(). Many orchestration-style pipelines find `after:`
	// reads more naturally than `depends_on:`.
	After AfterRef `yaml:"after,omitempty" toml:"after,omitempty" json:"after,omitempty"`

	// Error handling
	OnError      ErrorAction   `yaml:"on_error,omitempty" toml:"on_error,omitempty" json:"on_error,omitempty"`
	OnFailure    OnFailureSpec `yaml:"on_failure,omitempty" toml:"on_failure,omitempty" json:"on_failure,omitempty"` // Alias/extension for OnError; supports values like "fallback_to_ntm_inbox" or `retry:N` or `{pane, template}` recovery dispatches.
	OnSuccess    []Step        `yaml:"on_success,omitempty" toml:"on_success,omitempty" json:"on_success,omitempty"`
	RetryCount   int           `yaml:"retry_count,omitempty" toml:"retry_count,omitempty" json:"retry_count,omitempty"`
	RetryDelay   Duration      `yaml:"retry_delay,omitempty" toml:"retry_delay,omitempty" json:"retry_delay,omitempty"`
	RetryBackoff string        `yaml:"retry_backoff,omitempty" toml:"retry_backoff,omitempty" json:"retry_backoff,omitempty"` // linear, exponential, none

	// Conditionals
	When string `yaml:"when,omitempty" toml:"when,omitempty" json:"when,omitempty"` // Skip if evaluates to false

	// Branch is a shell command (or template expression) whose stdout selects
	// which entry of Branches to execute. Equivalent to a switch statement
	// over the command's output. When Branch is set, Branches must be a
	// map keyed by the possible output values. Currently parsed but not yet
	// executed by the canonical executor — orchestration-style pipelines
	// declare this for structural hint and the operator dispatches manually.
	Branch   string                 `yaml:"branch,omitempty" toml:"branch,omitempty" json:"branch,omitempty"`
	Branches map[string]interface{} `yaml:"branches,omitempty" toml:"branches,omitempty" json:"branches,omitempty"`

	// BeadQuery runs a structured br query and captures typed bead records as
	// JSON output, avoiding shell-piped br|jq steps in pipeline definitions.
	BeadQuery *BeadQueryStep `yaml:"bead_query,omitempty" toml:"bead_query,omitempty" json:"bead_query,omitempty"`

	// Output handling
	OutputVar     string        `yaml:"output_var,omitempty" toml:"output_var,omitempty" json:"output_var,omitempty"`                // Store output in variable
	OutputVarMode OutputVarMode `yaml:"output_var_mode,omitempty" toml:"output_var_mode,omitempty" json:"output_var_mode,omitempty"` // aggregate, last, collect
	OutputParse   OutputParse   `yaml:"output_parse,omitempty" toml:"output_parse,omitempty" json:"output_parse,omitempty"`          // none, json, yaml, lines, first_line, regex

	// Parallel execution. Two forms accepted:
	//   - parallel: [<step>, <step>, ...]  — explicit inline sub-steps
	//   - parallel: true                   — flag indicating "this step's
	//     foreach/loop body should fan out across panes concurrently"
	// Both are parsed via the ParallelSpec wrapper.
	Parallel ParallelSpec `yaml:"parallel,omitempty" toml:"parallel,omitempty" json:"parallel,omitempty"`

	// Loop execution
	Loop *LoopConfig `yaml:"loop,omitempty" toml:"loop,omitempty" json:"loop,omitempty"`

	// Loop control: break or continue (only valid inside loops)
	LoopControl LoopControl `yaml:"loop_control,omitempty" toml:"loop_control,omitempty" json:"loop_control,omitempty"`

	// Foreach is a step-level fan-out: iterate `Items` (typically pane
	// indices, bead IDs, or model families) and run `Steps` (or `Template`)
	// per iteration. Distinct from Loop because Foreach has explicit
	// per-iteration pane assignment (PaneStrategy). For per-pane fan-out
	// where each iteration goes to a different pane index, use ForeachPane.
	Foreach     *ForeachConfig `yaml:"foreach,omitempty" toml:"foreach,omitempty" json:"foreach,omitempty"`
	ForeachPane *ForeachConfig `yaml:"foreach_pane,omitempty" toml:"foreach_pane,omitempty" json:"foreach_pane,omitempty"`

	// Agent Mail step kinds. These execute through MCP Agent Mail rather than
	// tmux pane dispatch and are mutually exclusive with prompt/command/etc.
	MailSend               *MailSendStep               `yaml:"mail_send,omitempty" toml:"mail_send,omitempty" json:"mail_send,omitempty"`
	FileReservationPaths   *FileReservationPathsStep   `yaml:"file_reservation_paths,omitempty" toml:"file_reservation_paths,omitempty" json:"file_reservation_paths,omitempty"`
	MailInboxCheck         *MailInboxCheckStep         `yaml:"mail_inbox_check,omitempty" toml:"mail_inbox_check,omitempty" json:"mail_inbox_check,omitempty"`
	FileReservationRelease *FileReservationReleaseStep `yaml:"file_reservation_release,omitempty" toml:"file_reservation_release,omitempty" json:"file_reservation_release,omitempty"`
}

// StringOrList accepts either a single string or a list of strings. Used for
// fields like `notes:` where authors sometimes write a single line and
// sometimes a bulleted list.
type StringOrList []string

// UnmarshalYAML accepts both forms.
func (s *StringOrList) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var single string
	if err := unmarshal(&single); err == nil {
		if single != "" {
			*s = []string{single}
		}
		return nil
	}
	var list []string
	if err := unmarshal(&list); err != nil {
		return fmt.Errorf("expected string or list of strings: %w", err)
	}
	*s = list
	return nil
}

// UnmarshalJSON accepts either a single string or a list of strings.
func (s *StringOrList) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		*s = nil
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		if single != "" {
			*s = []string{single}
		} else {
			*s = nil
		}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("expected string or list of strings: %w", err)
	}
	*s = list
	return nil
}

// UnmarshalTOML accepts either a single string or a list of strings.
func (s *StringOrList) UnmarshalTOML(data any) error {
	if single, ok := data.(string); ok {
		if single != "" {
			*s = []string{single}
		} else {
			*s = nil
		}
		return nil
	}
	list, ok := tomlStringSlice(data)
	if !ok {
		return fmt.Errorf("expected string or list of strings")
	}
	*s = list
	return nil
}

// PaneSpec is a step's `pane:` selector. The canonical form is an integer
// pane index; brennerbot-style pipelines pass `${defaults.triage_pane}` style
// template references, which arrive as strings during YAML parse and are
// resolved by the executor's variable substitution before pane selection.
// PaneSpec preserves both: Index is set when the input is a literal integer,
// Expr is set when the input is a string.
type PaneSpec struct {
	Index int    // 0 means "unset" (matches the prior int-only behavior)
	Expr  string // template expression resolved at execution time, e.g. "${defaults.triage_pane}"
}

// UnmarshalYAML accepts int or string.
func (p *PaneSpec) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var n int
	if err := unmarshal(&n); err == nil {
		p.Index = n
		return nil
	}
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("pane: must be int or template string: %w", err)
	}
	p.Expr = s
	return nil
}

// UnmarshalJSON accepts int, string, or the canonical struct object emitted by
// encoding/json.
func (p *PaneSpec) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		*p = PaneSpec{}
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		p.Index = n
		p.Expr = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		p.Index = 0
		p.Expr = s
		return nil
	}
	type raw PaneSpec
	var obj raw
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("pane: must be int or template string: %w", err)
	}
	*p = PaneSpec(obj)
	return nil
}

// UnmarshalTOML accepts int, string, or the canonical struct table emitted by
// the TOML encoder.
func (p *PaneSpec) UnmarshalTOML(data any) error {
	if n, ok := tomlInt(data); ok {
		p.Index = n
		p.Expr = ""
		return nil
	}
	if s, ok := data.(string); ok {
		p.Index = 0
		p.Expr = s
		return nil
	}
	type raw PaneSpec
	var obj raw
	if err := decodeTOMLValue(data, &obj); err != nil {
		return fmt.Errorf("pane: must be int or template string: %w", err)
	}
	*p = PaneSpec(obj)
	return nil
}

// MarshalYAML emits the literal int when set, otherwise the expression.
func (p PaneSpec) MarshalYAML() (interface{}, error) {
	if p.Expr != "" {
		return p.Expr, nil
	}
	return p.Index, nil
}

// IsZero lets `omitempty` strip empty PaneSpec values.
func (p PaneSpec) IsZero() bool { return p.Index == 0 && p.Expr == "" }

// OnFailureSpec accepts the canonical ErrorAction string ("retry", "continue",
// etc.), retry-with-count shorthand ("retry:1"), or a structured fallback
// like `{pane: 3, template: MO-recovery.md}`. The Action / RetryCount fields
// are populated for known string forms; Fallback holds the structured form
// for the executor to interpret.
type OnFailureSpec struct {
	Action     string                 // raw value as written, for downstream branching
	RetryCount int                    // populated when value is "retry:N"
	Fallback   map[string]interface{} // populated for structured form
}

// UnmarshalYAML accepts string or map.
func (o *OnFailureSpec) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		o.setActionString(s)
		return nil
	}
	var m map[string]interface{}
	if err := unmarshal(&m); err != nil {
		return fmt.Errorf("on_failure: must be string or mapping: %w", err)
	}
	o.Fallback = m
	return nil
}

// UnmarshalJSON accepts the same forms as YAML: a string action or retry:N
// shorthand, a structured fallback object, or the canonical struct object
// emitted by encoding/json.
func (o *OnFailureSpec) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		*o = OnFailureSpec{}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		o.setActionString(s)
		return nil
	}

	var rawFields struct {
		Action     string                 `json:"Action"`
		RetryCount int                    `json:"RetryCount"`
		Fallback   map[string]interface{} `json:"Fallback"`
	}
	if err := json.Unmarshal(data, &rawFields); err == nil &&
		(rawFields.Action != "" || rawFields.RetryCount != 0 || rawFields.Fallback != nil || jsonObjectHasAnyKey(data, "Action", "RetryCount", "Fallback")) {
		o.Action = rawFields.Action
		o.RetryCount = rawFields.RetryCount
		o.Fallback = rawFields.Fallback
		return nil
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("on_failure: must be string or mapping: %w", err)
	}
	o.Action = ""
	o.RetryCount = 0
	o.Fallback = m
	return nil
}

// UnmarshalTOML accepts the same forms as YAML and JSON.
func (o *OnFailureSpec) UnmarshalTOML(data any) error {
	if s, ok := data.(string); ok {
		o.setActionString(s)
		return nil
	}
	m, ok := tomlMap(data)
	if !ok {
		return fmt.Errorf("on_failure: must be string or mapping")
	}

	var rawFields struct {
		Action     string                 `json:"Action"`
		RetryCount int                    `json:"RetryCount"`
		Fallback   map[string]interface{} `json:"Fallback"`
	}
	if err := decodeTOMLValue(m, &rawFields); err == nil &&
		(rawFields.Action != "" || rawFields.RetryCount != 0 || rawFields.Fallback != nil || mapHasAnyKey(m, "Action", "RetryCount", "Fallback")) {
		o.Action = rawFields.Action
		o.RetryCount = rawFields.RetryCount
		o.Fallback = rawFields.Fallback
		return nil
	}

	o.Action = ""
	o.RetryCount = 0
	o.Fallback = m
	return nil
}

func (o *OnFailureSpec) setActionString(s string) {
	o.Action = s
	o.RetryCount = 0
	o.Fallback = nil
	if strings.HasPrefix(s, "retry:") {
		rest := strings.TrimPrefix(s, "retry:")
		if n, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil {
			o.RetryCount = n
			o.Action = "retry"
		}
	}
}

// MarshalYAML round-trips the form we read.
func (o OnFailureSpec) MarshalYAML() (interface{}, error) {
	if len(o.Fallback) > 0 {
		return o.Fallback, nil
	}
	if o.Action == "retry" && o.RetryCount > 0 {
		return fmt.Sprintf("retry:%d", o.RetryCount), nil
	}
	if o.Action != "" {
		return o.Action, nil
	}
	return nil, nil
}

// IsZero lets `omitempty` strip empty OnFailureSpec values.
func (o OnFailureSpec) IsZero() bool {
	return o.Action == "" && o.RetryCount == 0 && len(o.Fallback) == 0
}

// IntOrExpr is an integer that may also be supplied as a template
// expression resolved at execution time. Used for fields like
// `max_iterations:` where pipelines pass `${defaults.hard_caps.foo}` so the
// limit is configurable per-run.
type IntOrExpr struct {
	Value int
	Expr  string
}

// UnmarshalYAML accepts int or string.
func (i *IntOrExpr) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var n int
	if err := unmarshal(&n); err == nil {
		i.Value = n
		return nil
	}
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("expected integer or template expression: %w", err)
	}
	i.Expr = s
	return nil
}

// UnmarshalJSON accepts int, string, or the canonical struct object emitted by
// encoding/json.
func (i *IntOrExpr) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		*i = IntOrExpr{}
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		i.Value = n
		i.Expr = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		i.Value = 0
		i.Expr = s
		return nil
	}
	type raw IntOrExpr
	var obj raw
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("expected integer or template expression: %w", err)
	}
	*i = IntOrExpr(obj)
	return nil
}

// UnmarshalTOML accepts int, string, or the canonical struct table emitted by
// the TOML encoder.
func (i *IntOrExpr) UnmarshalTOML(data any) error {
	if n, ok := tomlInt(data); ok {
		i.Value = n
		i.Expr = ""
		return nil
	}
	if s, ok := data.(string); ok {
		i.Value = 0
		i.Expr = s
		return nil
	}
	type raw IntOrExpr
	var obj raw
	if err := decodeTOMLValue(data, &obj); err != nil {
		return fmt.Errorf("expected integer or template expression: %w", err)
	}
	*i = IntOrExpr(obj)
	return nil
}

// MarshalYAML emits literal int when set, otherwise the expression.
func (i IntOrExpr) MarshalYAML() (interface{}, error) {
	if i.Expr != "" {
		return i.Expr, nil
	}
	return i.Value, nil
}

// IsZero lets `omitempty` strip empty IntOrExpr values.
func (i IntOrExpr) IsZero() bool { return i.Value == 0 && i.Expr == "" }

// AfterRef accepts either a string or a list of strings, normalizing both
// into a slice. Lets pipelines write `after: spawn` or `after: [spawn, audit]`.
type AfterRef []string

// UnmarshalYAML implements yaml.Unmarshaler so AfterRef accepts both the
// string and list-of-strings forms.
func (a *AfterRef) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		if s != "" {
			*a = []string{s}
		}
		return nil
	}
	var list []string
	if err := unmarshal(&list); err != nil {
		return fmt.Errorf("after: must be a string or list of strings: %w", err)
	}
	*a = list
	return nil
}

// UnmarshalJSON accepts either a single string or a list of strings.
func (a *AfterRef) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		*a = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s != "" {
			*a = []string{s}
		} else {
			*a = nil
		}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("after: must be a string or list of strings: %w", err)
	}
	*a = list
	return nil
}

// UnmarshalTOML accepts either a single string or a list of strings.
func (a *AfterRef) UnmarshalTOML(data any) error {
	if s, ok := data.(string); ok {
		if s != "" {
			*a = []string{s}
		} else {
			*a = nil
		}
		return nil
	}
	list, ok := tomlStringSlice(data)
	if !ok {
		return fmt.Errorf("after: must be a string or list of strings")
	}
	*a = list
	return nil
}

// (Branch/Branches are shell-driven switch statements; see Step.Branch +
// Step.Branches above. Each map value is either a single step or a list of
// steps; structurally `interface{}` so we accept both shapes without forcing
// a custom unmarshaller. The executor treats this as a no-op for now and
// orchestration-style pipelines use it as structural documentation.)

// ParallelSpec is parallel: in either of two forms — a list of sub-steps to
// run concurrently, or a boolean `true` meaning "this step (foreach/loop)
// should fan out concurrently rather than serially." Bool form is preserved
// in Flag; list form populates Steps. The two are mutually exclusive in
// practice.
type ParallelSpec struct {
	Flag  bool
	Steps []Step
}

// UnmarshalYAML lets ParallelSpec decode from either bool or []Step.
func (p *ParallelSpec) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var b bool
	if err := unmarshal(&b); err == nil {
		p.Flag = b
		return nil
	}
	var steps []Step
	if err := unmarshal(&steps); err != nil {
		return fmt.Errorf("parallel: must be bool or list of steps: %w", err)
	}
	p.Steps = steps
	return nil
}

// UnmarshalJSON accepts bool, []Step, or the canonical struct object emitted
// by encoding/json.
func (p *ParallelSpec) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		*p = ParallelSpec{}
		return nil
	}
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		p.Flag = b
		p.Steps = nil
		return nil
	}
	var steps []Step
	if err := json.Unmarshal(data, &steps); err == nil {
		p.Flag = false
		p.Steps = steps
		return nil
	}
	type raw ParallelSpec
	var obj raw
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("parallel: must be bool or list of steps: %w", err)
	}
	*p = ParallelSpec(obj)
	return nil
}

// UnmarshalTOML accepts bool and the canonical struct table emitted by the
// TOML encoder. Inline table arrays of steps are intentionally rejected because
// BurntSushi/toml cannot mark their nested keys as consumed for strict unknown
// field checks.
func (p *ParallelSpec) UnmarshalTOML(data any) error {
	if b, ok := data.(bool); ok {
		p.Flag = b
		p.Steps = nil
		return nil
	}
	if arr, ok := data.([]map[string]interface{}); ok {
		var steps []Step
		if err := decodeTOMLValue(arr, &steps); err != nil {
			return fmt.Errorf("parallel: must be bool or list of steps: %w", err)
		}
		p.Flag = false
		p.Steps = steps
		return nil
	}
	if arr, ok := data.([]interface{}); ok {
		if len(arr) > 0 {
			return fmt.Errorf("parallel: TOML inline step arrays are not supported; use [[steps.parallel.steps]] tables")
		}
		p.Flag = false
		p.Steps = nil
		return nil
	}
	if m, ok := tomlMap(data); ok {
		if arr, ok := m["steps"].([]interface{}); ok && len(arr) > 0 {
			return fmt.Errorf("parallel: TOML inline step arrays are not supported; use [[steps.parallel.steps]] tables")
		}
		// bd-k44ib: detect the malformed shape `[steps.parallel]` populated with
		// step-shaped fields like id/prompt/command. The user almost certainly
		// meant `[[steps.parallel.steps]]` (an array of tables). Without this
		// check, decodeTOMLValue silently produces an empty ParallelSpec because
		// the JSON-roundtrip used by decodeTOMLValue ignores keys that aren't on
		// raw ParallelSpec, and validation later complains generically that the
		// step has no kind. Surface a precise error pointing at the bad table.
		if badKey := firstUnknownParallelTableKey(m); badKey != "" {
			return fmt.Errorf("parallel: unexpected key %q under [steps.parallel] table — to declare inline parallel sub-steps use [[steps.parallel.steps]] (an array of tables)", badKey)
		}
	}
	type raw ParallelSpec
	var obj raw
	if err := decodeTOMLValue(data, &obj); err != nil {
		return fmt.Errorf("parallel: must be bool or list of steps: %w", err)
	}
	*p = ParallelSpec(obj)
	return nil
}

// firstUnknownParallelTableKey reports the first key in a `[steps.parallel]`
// table that is not part of the canonical ParallelSpec schema. The TOML
// table form is allowed to carry "steps" (the array of Step tables) plus the
// ParallelSpec struct fields the encoder round-trips ("Flag"/"flag"). Any
// other key — particularly step-shaped keys like id, prompt, command — means
// the operator wrote a single table where they meant `[[steps.parallel.steps]]`
// (an array of tables). Returns "" when every key is recognized.
func firstUnknownParallelTableKey(m map[string]interface{}) string {
	for k := range m {
		switch k {
		case "steps", "Steps", "flag", "Flag":
			continue
		}
		return k
	}
	return ""
}

// MarshalYAML mirrors the input form — emit a bool when only Flag is set,
// emit the list otherwise. Keeps round-tripping clean for tooling.
func (p ParallelSpec) MarshalYAML() (interface{}, error) {
	if len(p.Steps) > 0 {
		return p.Steps, nil
	}
	if p.Flag {
		return true, nil
	}
	return nil, nil
}

// IsZero lets `omitempty` strip empty ParallelSpec values from output.
func (p ParallelSpec) IsZero() bool { return !p.Flag && len(p.Steps) == 0 }

// Len returns the number of inline parallel sub-steps. The Flag form has Len 0.
func (p ParallelSpec) Len() int { return len(p.Steps) }

func isJSONNull(data []byte) bool {
	return strings.TrimSpace(string(data)) == "null"
}

func jsonObjectHasAnyKey(data []byte, keys ...string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return false
	}
	for _, key := range keys {
		if _, ok := obj[key]; ok {
			return true
		}
	}
	return false
}

func decodeTOMLValue(data any, dst any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func tomlMap(data any) (map[string]interface{}, bool) {
	switch m := data.(type) {
	case map[string]interface{}:
		return m, true
	default:
		return nil, false
	}
}

func tomlStringSlice(data any) ([]string, bool) {
	switch list := data.(type) {
	case []string:
		return list, true
	case []interface{}:
		out := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

func tomlInt(data any) (int, bool) {
	switch n := data.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

func mapHasAnyKey(m map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

// ForeachConfig drives per-iteration fan-out. Items is the iterable
// (`${vars.hypothesis_active}`, `${ntm.panes}`, etc.); As is the loop var;
// Template / Steps describe what to dispatch per iteration. PaneStrategy
// optionally selects a target pane per iteration.
type ForeachConfig struct {
	Items          string                 `yaml:"items,omitempty" toml:"items,omitempty" json:"items,omitempty"`
	As             string                 `yaml:"as,omitempty" toml:"as,omitempty" json:"as,omitempty"`
	Beads          string                 `yaml:"beads,omitempty" toml:"beads,omitempty" json:"beads,omitempty"`                // Convenience: iterate beads matching a label query (e.g. `hypothesis,state:active`).
	Pairs          string                 `yaml:"pairs,omitempty" toml:"pairs,omitempty" json:"pairs,omitempty"`                // Convenience: iterate paired items produced by a generator command (e.g., debate-pair generation). Mutually exclusive with Items/Beads.
	Debates        string                 `yaml:"debates,omitempty" toml:"debates,omitempty" json:"debates,omitempty"`          // Convenience: iterate DEBATE-* beads (typically scoped by state).
	Models         StringOrList           `yaml:"models,omitempty" toml:"models,omitempty" json:"models,omitempty"`             // Convenience: iterate distinct model families in the roster. Accepts either an inline list (`["cc", "cod"]`) or a single shell command that emits one family per line.
	MaxRounds      IntOrExpr              `yaml:"max_rounds,omitempty" toml:"max_rounds,omitempty" json:"max_rounds,omitempty"` // Per-iteration round cap for orchestration patterns that internally iterate (e.g., debate rounds).
	Filter         string                 `yaml:"filter,omitempty" toml:"filter,omitempty" json:"filter,omitempty"`             // Convenience predicate over the resolved iteration set, e.g. `role==proposer` or `state==active`. Applied after Items/Beads expansion.
	PaneStrategy   string                 `yaml:"pane_assignment_strategy,omitempty" toml:"pane_assignment_strategy,omitempty" json:"pane_assignment_strategy,omitempty"`
	Template       string                 `yaml:"template,omitempty" toml:"template,omitempty" json:"template,omitempty"`
	TemplateParams map[string]interface{} `yaml:"template_params,omitempty" toml:"template_params,omitempty" json:"template_params,omitempty"`
	Params         map[string]interface{} `yaml:"params,omitempty" toml:"params,omitempty" json:"params,omitempty"`
	Steps          []Step                 `yaml:"steps,omitempty" toml:"steps,omitempty" json:"steps,omitempty"`
	Body           []Step                 `yaml:"body,omitempty" toml:"body,omitempty" json:"body,omitempty"` // Alias for Steps.
	Parallel       bool                   `yaml:"parallel,omitempty" toml:"parallel,omitempty" json:"parallel,omitempty"`
	MaxConcurrent  int                    `yaml:"max_concurrent,omitempty" toml:"max_concurrent,omitempty" json:"max_concurrent,omitempty"`
	OutputVarMode  OutputVarMode          `yaml:"output_var_mode,omitempty" toml:"output_var_mode,omitempty" json:"output_var_mode,omitempty"`
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

// UnmarshalJSON accepts the string shorthand used by text unmarshalling and
// the canonical object emitted by encoding/json.
func (o *OutputParse) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		*o = OutputParse{}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		o.Type = s
		o.Pattern = ""
		return nil
	}
	type raw OutputParse
	var obj raw
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("output_parse: must be a string or object: %w", err)
	}
	*o = OutputParse(obj)
	return nil
}

// UnmarshalTOML accepts the string shorthand and the canonical table form.
func (o *OutputParse) UnmarshalTOML(data any) error {
	if s, ok := data.(string); ok {
		o.Type = s
		o.Pattern = ""
		return nil
	}
	type raw OutputParse
	var obj raw
	if err := decodeTOMLValue(data, &obj); err != nil {
		return fmt.Errorf("output_parse: must be a string or object: %w", err)
	}
	*o = OutputParse(obj)
	return nil
}

// LoopConfig defines loop iteration settings for for-each, while, and times loops
type LoopConfig struct {
	// For-each loop: iterate over array
	Items string `yaml:"items,omitempty" toml:"items,omitempty" json:"items,omitempty"` // Expression for array (e.g., ${vars.files})
	As    string `yaml:"as,omitempty" toml:"as,omitempty" json:"as,omitempty"`          // Loop variable name (default: "item")

	// While loop: repeat until condition is false
	While string `yaml:"while,omitempty" toml:"while,omitempty" json:"while,omitempty"` // Condition expression

	// Until is a predicate-based exit: a shell command that runs after each
	// iteration; when it exits 0 the loop stops. Mutually exclusive with While
	// and Times. Supports the common orchestration pattern "loop until this
	// convergence script reports CONVERGED" without forcing the author to
	// invert the condition. Example: `until: ./scripts/convergence-check.sh`.
	Until string `yaml:"until,omitempty" toml:"until,omitempty" json:"until,omitempty"`

	// Times loop: repeat N times
	Times int `yaml:"times,omitempty" toml:"times,omitempty" json:"times,omitempty"` // Number of iterations

	// Safety and timing
	MaxIterations IntOrExpr `yaml:"max_iterations,omitempty" toml:"max_iterations,omitempty" json:"max_iterations,omitempty"` // Safety limit (default: 100, required for while loops). May be a template expression like `${defaults.hard_caps.foo}` to defer the value until run time.
	Delay         Duration  `yaml:"delay,omitempty" toml:"delay,omitempty" json:"delay,omitempty"`                            // Delay between iterations

	// Result collection
	Collect string `yaml:"collect,omitempty" toml:"collect,omitempty" json:"collect,omitempty"` // Variable name to store array of results

	// Steps to execute per iteration
	Steps []Step `yaml:"steps,omitempty" toml:"steps,omitempty" json:"steps,omitempty"`

	// Body is an alias for Steps (some pipelines find `body:` reads more
	// naturally inside a `loop:` block). Workflow.Normalize() copies Body into
	// Steps after parse, so the executor only consults Steps.
	Body []Step `yaml:"body,omitempty" toml:"body,omitempty" json:"body,omitempty"`
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

// SkipKind classifies why a step was skipped or cancelled.
type SkipKind string

const (
	SkipKindNone             SkipKind = ""
	SkipKindWhenCondition    SkipKind = "when_false"
	SkipKindNotImplemented   SkipKind = "phase_b_not_implemented"
	SkipKindFailedDependency SkipKind = "failed_dependency"
	SkipKindStartFrom        SkipKind = "start_from_excluded"
	SkipKindForeachFilter    SkipKind = "foreach_filter_excluded"
	SkipKindForeachContinue  SkipKind = "foreach_loop_control_continue"
	SkipKindForeachBreak     SkipKind = "foreach_loop_control_break_after"
	SkipKindCancelled        SkipKind = "cancelled"
	SkipKindLimit            SkipKind = "resource_limit_exceeded"
	// SkipKindOnFailureAction tags a step that failed but was recovered into
	// StatusSkipped by on_failure setting a runtime variable (bd-2ytru).
	// Distinguishes user-driven recovery from unclassified skips.
	SkipKindOnFailureAction SkipKind = "on_failure_action"
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
	SkipKind   SkipKind        `json:"skip_kind,omitempty"`   // Structured classifier for SkipReason
	Attempts   int             `json:"attempts,omitempty"`    // Number of retry attempts
}

// StepError contains detailed error information for a failed step
type StepError struct {
	Type       string      `json:"type"` // timeout, agent_error, crash, validation, routing, send, capture
	Message    string      `json:"message"`
	Details    string      `json:"details,omitempty"`     // Full error output
	PaneOutput string      `json:"pane_output,omitempty"` // Last N lines from pane for debugging
	AgentState string      `json:"agent_state,omitempty"` // Agent state at time of error
	Attempt    int         `json:"attempt,omitempty"`     // Which retry attempt
	Timestamp  time.Time   `json:"timestamp"`
	Aggregated []StepError `json:"aggregated,omitempty"` // Nested foreach/parallel errors
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
	CancelledAt  *time.Time             `json:"cancelled_at,omitempty"`
	CurrentStep  string                 `json:"current_step,omitempty"`
	Steps        map[string]StepResult  `json:"steps"`
	Variables    map[string]interface{} `json:"variables"` // Runtime variables including step outputs
	Errors       []ExecutionError       `json:"errors,omitempty"`

	// Phase-B resume metadata. This is ntm's internal persisted execution
	// format and may change between pipeline schema revisions.
	LastCheckpointAt time.Time                        `json:"last_checkpoint_at,omitempty"`
	ForeachState     map[string]ForeachIterationState `json:"foreach_state,omitempty"`
	ParallelState    map[string]ParallelGroupState    `json:"parallel_state,omitempty"`
	ScopeStack       []ScopeFrame                     `json:"scope_stack,omitempty"`
	InFlightSteps    map[string]InFlightStepState     `json:"in_flight_steps,omitempty"`
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
	logDispatch := true
	return WorkflowSettings{
		Timeout:          Duration{Duration: 30 * time.Minute},
		OnError:          ErrorActionFail,
		LogDispatch:      &logDispatch,
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

// Normalize folds the alias / convenience fields into their canonical
// counterparts so the rest of the executor only needs to consult one form.
// Idempotent — calling Normalize() twice is the same as calling it once.
//
// Specifically:
//   - Step.After is appended into Step.DependsOn (deduplicated).
//   - Step.OnFailure is mapped to Step.OnError (canonical) when OnError is
//     unset; brennerbot-specific values that don't match an ErrorAction enum
//     value are preserved verbatim and surfaced via the OnFailure field for
//     downstream branching.
//   - Step.TemplateParams is merged into Step.Params (Params wins on conflict).
//   - LoopConfig.Body is appended into LoopConfig.Steps when Steps is empty.
//   - ForeachConfig.Body is appended into ForeachConfig.Steps when Steps is
//     empty.
//   - Workflow.Inputs entries are added to Workflow.Vars as bare-required
//     declarations when not already present.
func (w *Workflow) Normalize() {
	for _, name := range w.Inputs {
		if w.Vars == nil {
			w.Vars = make(map[string]VarDef)
		}
		if _, exists := w.Vars[name]; !exists {
			w.Vars[name] = VarDef{Required: true}
		}
	}
	normalizeSteps(w.Steps)
	normalizeSteps(w.PostPipelineSteps)
}

// normalizeSteps applies Step-level Normalize transformations recursively
// (parallel / loop / foreach sub-steps).
func normalizeSteps(steps []Step) {
	for i := range steps {
		s := &steps[i]
		// After → DependsOn (preserve any pre-existing DependsOn entries; dedupe).
		if len(s.After) > 0 {
			seen := make(map[string]bool, len(s.DependsOn)+len(s.After))
			for _, d := range s.DependsOn {
				seen[d] = true
			}
			for _, a := range s.After {
				if !seen[a] {
					s.DependsOn = append(s.DependsOn, a)
					seen[a] = true
				}
			}
			s.After = nil
		}
		// OnFailure → OnError when OnError is unset and the value is a known enum.
		if s.OnFailure.Action != "" && s.OnError == "" {
			switch ErrorAction(s.OnFailure.Action) {
			case ErrorActionFail, ErrorActionFailFast, ErrorActionContinue, ErrorActionRetry:
				s.OnError = ErrorAction(s.OnFailure.Action)
				if s.OnFailure.RetryCount > 0 && s.RetryCount == 0 {
					s.RetryCount = s.OnFailure.RetryCount
				}
				s.OnFailure = OnFailureSpec{}
			}
			// Non-enum values (e.g. "fallback_to_ntm_inbox") and structured
			// fallbacks stay in OnFailure for the executor to interpret.
		}
		// TemplateParams → Params merge (Params wins on conflict).
		if len(s.TemplateParams) > 0 {
			if s.Params == nil {
				s.Params = make(map[string]interface{}, len(s.TemplateParams))
			}
			for k, v := range s.TemplateParams {
				if _, exists := s.Params[k]; !exists {
					s.Params[k] = v
				}
			}
			s.TemplateParams = nil
		}
		// Loop.Body → Loop.Steps (alias).
		if s.Loop != nil {
			if len(s.Loop.Steps) == 0 && len(s.Loop.Body) > 0 {
				s.Loop.Steps = s.Loop.Body
				s.Loop.Body = nil
			}
			normalizeSteps(s.Loop.Steps)
		}
		// Foreach.Body → Foreach.Steps (alias). Same for ForeachPane.
		for _, fc := range []*ForeachConfig{s.Foreach, s.ForeachPane} {
			if fc == nil {
				continue
			}
			if len(fc.Steps) == 0 && len(fc.Body) > 0 {
				fc.Steps = fc.Body
				fc.Body = nil
			}
			if len(fc.Params) == 0 && len(fc.TemplateParams) > 0 {
				fc.Params = fc.TemplateParams
				fc.TemplateParams = nil
			}
			normalizeSteps(fc.Steps)
		}
		// Recurse into parallel sub-steps and nested branches.
		if len(s.Parallel.Steps) > 0 {
			normalizeSteps(s.Parallel.Steps)
		}
		if len(s.OnSuccess) > 0 {
			normalizeSteps(s.OnSuccess)
		}
		// Branches now hold heterogeneous values (single step or list of
		// steps as `interface{}`); execution-time interpretation is deferred,
		// so no normalize-time recursion is required for them today.
	}
}
