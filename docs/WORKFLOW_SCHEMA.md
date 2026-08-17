# Workflow Schema Reference

NTM workflows define multi-step automation pipelines for orchestrating AI agents. This document describes the complete workflow schema for version 2.0.

## Table of Contents

- [Quick Start](#quick-start)
- [Custom Orchestration Templates](#custom-orchestration-templates)
- [Root Structure](#root-structure)
- [Variables](#variables)
- [Settings](#settings)
- [Steps](#steps)
- [Agent Selection](#agent-selection)
- [Wait Configuration](#wait-configuration)
- [Error Handling](#error-handling)
- [Parallel Execution](#parallel-execution)
- [Conditional Steps](#conditional-steps)
- [Output Parsing](#output-parsing)
- [Variable Substitution](#variable-substitution)
- [Examples](#examples)

## Quick Start

```yaml
# minimal-workflow.yaml
schema_version: "2.0"
name: simple-review
description: Run a code review with Claude

steps:
  - id: review
    agent: claude
    prompt: Review the code in this repository for bugs and suggest improvements.
```

## Custom Orchestration Templates

NTM also loads TOML workflow templates for its multi-agent coordination
commands. These templates are distinct from the YAML pipeline files described
below: they assign agent roles, choose a coordination model, and define the
transitions between stages.

Put a template in one of these directories (one or more `.toml` files per
directory):

1. `~/.config/ntm/workflows/` for templates available to every project.
2. `.ntm/workflows/` in the repository for project-specific templates.

Project templates override user templates with the same name, and both override
built-in templates. Confirm that a template was loaded with
`ntm workflows list`, and inspect its parsed configuration with
`ntm workflows show <name>`.

### Create a Template

1. Start from a built-in template (`ntm workflows list`) and choose a lowercase
   name containing only letters, numbers, hyphens, and underscores.
2. Add one `[[workflows.agents]]` entry for each role. Every entry requires a
   `profile` and `role`; `count` defaults to one.
3. Choose `parallel`, `pipeline`, `ping-pong`, or `review-gate` coordination.
   Pipeline templates require `flow.stages`; the other non-parallel forms
   require `flow.initial`.
4. Define at least one `[[workflows.flow.transitions]]` entry whenever a flow
   is present. Each transition needs `from`, `to`, and a valid trigger.
5. Add `[[workflows.prompts]]` entries for information that must be gathered
   before coordination starts, then run `ntm workflows show <name>` to parse
   and validate the file.

### Example: Project Review Flow

Save the following as `.ntm/workflows/my-review-flow.toml`:

```toml
[[workflows]]
name = "my-review-flow"
description = "Custom implementation and security review pipeline"
coordination = "pipeline"

[[workflows.agents]]
profile = "implementer"
role = "implement"

[[workflows.agents]]
profile = "reviewer"
role = "security-review"

[[workflows.agents]]
profile = "reviewer"
role = "perf-review"

[workflows.flow]
stages = ["implement", "security-review", "perf-review", "merge"]

[[workflows.flow.transitions]]
from = "implement"
to = "security-review"
[workflows.flow.transitions.trigger]
type = "all_agents_idle"
role = "implement"
idle_minutes = 1

[[workflows.flow.transitions]]
from = "security-review"
to = "perf-review"
[workflows.flow.transitions.trigger]
type = "manual"
label = "Security review complete"

[[workflows.flow.transitions]]
from = "perf-review"
to = "merge"
[workflows.flow.transitions.trigger]
type = "manual"
label = "Performance review complete"

[[workflows.prompts]]
key = "ticket"
question = "Ticket or issue number?"
required = true
```

### Run a Template

`ntm workflow run <name-or-path>` executes a template's coordination loop
against a live session:

```bash
# Builtins by name (red-green, review-pipeline, specialist-team, parallel-explore)
ntm workflow run red-green --var feature="parser rewrite"

# User/project templates by name (same search order as `ntm workflows list`)
ntm workflow run my-review-flow --session myproj --var ticket=BUG-123

# Any workflow TOML by explicit path (a path separator or .toml suffix)
ntm workflow run ./flows/my-flow.toml --session myproj

# Drive manual transitions automatically (unattended runs)
ntm workflow run specialist-team --var project="billing" --fire-manual
```

How a run works:

1. The template resolves by explicit path when the argument contains a path
   separator or ends in `.toml`; otherwise by name with the standard
   precedence (builtin < `~/.config/ntm/workflows/` < `.ntm/workflows/`).
   An unknown name is an error that lists the builtin names — there is no
   fallback.
2. Required setup prompts without defaults must be supplied with
   `--var key=value`; defaults fill the rest.
3. Template roles map onto the session's agent panes in pane order (the
   session needs at least as many agent panes as the template declares).
4. Each stage's prompt is delivered through the same gated dispatch path as
   `ntm send` (dead-pane gate + composer-verified submission), and the
   template's triggers advance the stages. A stage engages the role with the
   same name; otherwise the role the routing table maps for the stage, the
   role named by an outgoing transition's trigger, or the first declared
   role. Parallel coordination (and `parallel_within_stage`) engages every
   pane of the acting role.
5. The run ends when the flow reaches a stage with no outgoing transitions,
   after `--max-transitions` stage changes, or at `--timeout`. `--json`
   emits a machine-readable result (stages visited, transitions, role→pane
   mapping).

Useful flags: `--fire-manual` fires `manual` triggers automatically,
`--interval` sets the trigger poll cadence, `--trigger-timeout` bounds
command triggers, `--project-root` anchors file/command triggers, and
`--resume` clears a paused checkpoint recorded by the template's
`error_handling` pause action.

Note: `ntm spawn -t <template>` uses only the template's agent COUNTS to
size a new session — it does not run the coordination. Spawn the session
first, then `ntm workflow run <template>` inside it.

### Best Practices

- Start with a built-in template and change one coordination concern at a time.
- Keep the first flow short and use explicit roles that match its stages.
- Use setup prompts for project-specific inputs instead of embedding them in
  a role or transition.
- Prefer manual transitions while establishing a new flow; add automatic
  triggers only after their pane output is reliable.
- Validate the template with `ntm workflows show <name>` before depending on
  it in a session.

### Troubleshooting

- **Workflow not found:** ensure the file ends in `.toml` and is in
  `.ntm/workflows/` or `~/.config/ntm/workflows/`; use `ntm workflows list`
  to see the effective source. `ntm workflow run` fails closed on an unknown
  name and lists the builtin names in its error.
- **Template fails to load:** use `ntm workflows show <name>` to surface TOML,
  unknown-field, or validation errors. Pipeline flows need stages and every
  flow needs at least one valid transition.
- **Unexpected template version:** a project template with the same name
  overrides a user or built-in template; `ntm workflows list` reports its
  source.
- **Flow does not advance:** check that the trigger type has all required
  fields (for example `idle_minutes` for `all_agents_idle`) and that the
  trigger's role matches the stage role.

## Root Structure

```yaml
schema_version: "2.0"          # Required: Schema version
name: workflow-name            # Required: Unique identifier
description: What this does    # Optional: Human-readable description
version: "1.0"                 # Optional: Workflow version

vars:                          # Optional: Variable definitions
  var_name:
    description: What this is
    required: true
    default: null
    type: string

settings:                      # Optional: Global settings
  timeout: 30m
  on_error: fail
  notify_on_complete: true

steps:                         # Required: Step definitions
  - id: step_id
    # ... step configuration
```

## Variables

Variables allow workflows to be parameterized at runtime.

### Variable Definition

```yaml
vars:
  project_name:
    description: Name of the project to analyze
    required: true
    type: string

  max_files:
    description: Maximum files to process
    required: false
    default: 100
    type: number

  verbose:
    description: Enable verbose output
    default: false
    type: boolean

  file_patterns:
    description: File patterns to include
    default: ["*.go", "*.py"]
    type: array
```

### Variable Types

| Type | Description | Example Values |
|------|-------------|----------------|
| `string` | Text value | `"hello"`, `"path/to/file"` |
| `number` | Numeric value | `42`, `3.14` |
| `boolean` | True/false | `true`, `false` |
| `array` | List of values | `["a", "b", "c"]` |

### Providing Variables at Runtime

```bash
ntm pipeline run workflow.yaml --var project_name=myapp --var max_files=50
ntm pipeline run workflow.yaml --var-file vars.yaml
ntm pipeline run workflow.yaml --var file_patterns=internal,cmd,docs
```

Variable precedence is deterministic. Loop-local values such as `${item.X}` and
`${pane.X}` are innermost during foreach execution. Runtime `--var` values
override `--var-file` values, and both override `vars:` defaults. Defaults are
applied only when no runtime value was provided, and defaults may reference
other defaults with `${vars.X}` as long as they do not form a cycle. Declared
types are validated before execution; array-typed `--var` values use
comma-separated input.

## Settings

Global settings apply to the entire workflow.

```yaml
settings:
  timeout: 30m                 # Global timeout for entire workflow
  on_error: fail               # fail | continue
  notify_on_complete: true     # Send notification on completion
  notify_on_error: true        # Send notification on error
  notify_channels:             # Notification channels
    - desktop
    - webhook
    - mail
  webhook_url: https://...     # Webhook endpoint
  mail_recipient: user         # Agent mail recipient
```

### Error Actions

| Action | Behavior |
|--------|----------|
| `fail` | Stop workflow immediately on error (default) |
| `continue` | Log error and continue to next step |

## Steps

Each step defines a unit of work in the workflow.

### Basic Step Structure

```yaml
steps:
  - id: step_id              # Required: Unique identifier
    name: Human Name         # Optional: Display name

    # Agent selection (choose one)
    agent: claude            # Agent type
    pane: 1                  # OR specific pane index
    route: least-loaded      # OR routing strategy

    # Prompt (choose one)
    prompt: |
      Inline prompt text
    prompt_file: prompts/step1.md

    # Wait configuration
    wait: completion
    timeout: 5m

    # Dependencies
    depends_on: [previous_step]

    # Error handling
    on_error: fail
    retry_count: 3
    retry_delay: 30s

    # Conditionals
    when: ${vars.run_tests}

    # Output handling
    output_var: result
    output_parse: json
```

## Agent Selection

Three ways to specify which agent should execute a step:

### By Agent Type

```yaml
- id: design
  agent: claude              # Use any Claude agent
  prompt: Design the API
```

Agent types:
- `claude` / `cc` / `claude-code`
- `codex` / `cod` / `openai`
- `antigravity` / `agy` / `google-antigravity`
- `gemini` / `gmi` / `google` (legacy)

### By Pane Index

```yaml
- id: implement
  pane: 1                    # Use specific pane (0=user, 1+=agents)
  prompt: Implement the API
```

### By Routing Strategy

```yaml
- id: test
  route: least-loaded        # Smart agent selection
  prompt: Write tests
```

Routing strategies:
- `least-loaded` - Choose agent with lowest context usage
- `first-available` - Choose first idle agent
- `round-robin` - Rotate through agents

## Wait Configuration

Define when a step is considered complete:

```yaml
- id: step
  wait: completion           # Wait for agent to finish
  timeout: 5m                # Maximum wait time
```

### Wait Conditions

| Condition | Behavior |
|-----------|----------|
| `completion` | Wait for agent to return to idle state (default) |
| `idle` | Same as completion |
| `time` | Wait for timeout duration only |
| `none` | Fire and forget (don't wait) |

## Error Handling

### Step-Level Error Handling

```yaml
- id: flaky_step
  prompt: Call external API
  on_error: retry
  retry_count: 3
  retry_delay: 10s
  retry_backoff: exponential  # linear | exponential | none
```

### Retry Backoff

| Type | Behavior |
|------|----------|
| `none` | Fixed delay between retries |
| `linear` | Delay increases linearly (delay * attempt) |
| `exponential` | Delay doubles each attempt |

### Error Modes

| Mode | Behavior |
|------|----------|
| `fail` | Stop workflow, mark as failed |
| `continue` | Log error, continue to next step |
| `retry` | Retry step up to retry_count times |

## Parallel Execution

Run multiple steps concurrently using the `parallel` block:

```yaml
- id: parallel_work
  parallel:
    - id: research
      agent: claude
      prompt: Research the problem

    - id: prototype
      agent: codex
      prompt: Write initial code

    - id: review
      agent: antigravity
      prompt: Review architecture

- id: combine
  depends_on: [parallel_work]
  prompt: |
    Combine results from:
    - Research: ${steps.research.output}
    - Prototype: ${steps.prototype.output}
    - Review: ${steps.review.output}
```

TOML workflows must use array-of-tables for nested parallel steps:

```toml
[[steps]]
id = "parallel_work"

[[steps.parallel.steps]]
id = "research"
agent = "claude"
prompt = "Research the problem"

[[steps.parallel.steps]]
id = "prototype"
agent = "codex"
prompt = "Write initial code"
```

Inline TOML table arrays such as `parallel = [{ id = "research", prompt = "..." }]`
are rejected so unknown-field validation can stay strict.

### Parallel Execution Rules

1. All parallel steps run concurrently on different agents
2. The parallel group completes when all sub-steps finish
3. If any sub-step fails, the group fails (unless `on_error: continue`)
4. Outputs are accessible via `${steps.<sub_id>.output}`

## Conditional Steps

Skip steps based on runtime conditions:

```yaml
- id: check_type
  prompt: Is this a bug fix? Reply YES or NO.
  output_var: is_bugfix
  output_parse: first_line

- id: run_tests
  when: ${vars.is_bugfix} == "YES"
  prompt: Run the test suite

- id: skip_tests
  when: ${vars.is_bugfix} == "NO"
  prompt: Skip tests, proceed to review
```

### Condition Syntax

- `${vars.variable}` - Check variable truthiness
- `${vars.x} == "value"` - String equality
- `${vars.count} > 10` - Numeric comparison
- Boolean operators: `&&`, `||`, `!`

## Output Parsing

Capture and parse step outputs for use in later steps:

```yaml
- id: get_data
  prompt: Return a JSON object with count and items
  output_var: data
  output_parse: json

- id: use_data
  prompt: Process ${vars.data.count} items
```

### Parse Types

| Type | Behavior |
|------|----------|
| `none` | Raw string (default) |
| `json` | Parse as JSON, access fields with dots |
| `yaml` | Parse as YAML |
| `lines` | Split into array by newlines |
| `first_line` | First line only |
| `regex` | Extract with named capture groups |

### Regex Parsing

```yaml
- id: extract
  prompt: The count is 42.
  output_var: result
  output_parse:
    type: regex
    pattern: "count is (?P<count>\\d+)"

- id: use
  prompt: Count was ${vars.result.count}
```

## Variable Substitution

Variables can be referenced throughout the workflow using `${...}` syntax.

### Variable Types

| Variable | Example | Description |
|----------|---------|-------------|
| `${vars.X}` | `${vars.name}` | User-provided variable |
| `${steps.X.output}` | `${steps.design.output}` | Raw step output |
| `${steps.X.pane}` | `${steps.design.pane}` | Pane ID used |
| `${steps.X.duration}` | `${steps.design.duration}` | Step duration |
| `${steps.X.status}` | `${steps.design.status}` | Step status |
| `${env.X}` | `${env.HOME}` | Environment variable |
| `${session}` | `myproject` | Session name |
| `${timestamp}` | `2025-01-15T10:00:00Z` | Current time |
| `${run_id}` | `abc123` | Pipeline run ID |
| `${workflow}` | `my-workflow` | Workflow name |

Environment variables expose the runner's process environment to the pipeline.
Missing environment variables are substitution errors unless a default is supplied,
for example `${env.OPTIONAL_TOKEN | ""}`.

### Default Values

Provide fallback values for undefined variables:

```yaml
prompt: Hello ${vars.name | "World"}
```

### Escaping

Use backslash to include literal `${`:

```yaml
prompt: The syntax is \${variable}
```

## Examples

### Code Review Workflow

```yaml
schema_version: "2.0"
name: code-review
description: Automated code review with multiple agents

vars:
  branch:
    description: Branch to review
    required: true
    type: string

steps:
  - id: security_review
    agent: claude
    prompt: |
      Review the changes on branch ${vars.branch} for security issues.
      Focus on: injection, authentication, data exposure.
    output_var: security_issues

  - id: code_quality
    agent: codex
    prompt: |
      Review the changes on branch ${vars.branch} for code quality.
      Check: naming, structure, complexity, test coverage.
    output_var: quality_issues

  - id: compile_report
    agent: claude
    depends_on: [security_review, code_quality]
    prompt: |
      Compile a review report from:

      Security findings:
      ${vars.security_issues}

      Quality findings:
      ${vars.quality_issues}
```

### Red-Green-Refactor Workflow

```yaml
schema_version: "2.0"
name: red-green-refactor
description: TDD workflow with parallel test writing

steps:
  - id: write_test
    agent: claude
    prompt: Write a failing test for the new feature
    wait: completion

  - id: verify_red
    agent: codex
    depends_on: [write_test]
    prompt: Run the test and verify it fails
    output_var: test_result

  - id: implement
    agent: claude
    depends_on: [verify_red]
    when: ${vars.test_result} contains "FAIL"
    prompt: Implement the minimum code to pass the test

  - id: verify_green
    agent: codex
    depends_on: [implement]
    prompt: Run the test and verify it passes
    on_error: retry
    retry_count: 3

  - id: refactor
    agent: claude
    depends_on: [verify_green]
    prompt: Refactor the implementation while keeping tests green
```

### Parallel Investigation

```yaml
schema_version: "2.0"
name: parallel-investigate
description: Investigate an issue from multiple angles

vars:
  issue:
    description: Issue to investigate
    required: true

steps:
  - id: investigate
    parallel:
      - id: code_search
        agent: claude
        prompt: Search the codebase for code related to: ${vars.issue}

      - id: git_history
        agent: codex
        prompt: Check git history for changes related to: ${vars.issue}

      - id: log_search
        agent: antigravity
        prompt: Search logs for errors related to: ${vars.issue}

  - id: synthesize
    depends_on: [investigate]
    agent: claude
    prompt: |
      Synthesize findings from parallel investigation:

      Code search: ${steps.code_search.output}
      Git history: ${steps.git_history.output}
      Log search: ${steps.log_search.output}

      Provide a root cause analysis.
```

## Schema Versioning

The `schema_version` field ensures forward compatibility. NTM will:

1. Validate workflows against the declared schema version
2. Warn if the workflow uses a newer schema than supported
3. Apply migrations for older schemas when possible

Current version: **2.0**
