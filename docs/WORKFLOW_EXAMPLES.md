# Workflow Examples

Practical workflow examples demonstrating NTM pipeline patterns. For schema details, see [WORKFLOW_SCHEMA.md](WORKFLOW_SCHEMA.md).

## Table of Contents

- [Design-Implement-Test Workflow](#design-implement-test-workflow)
- [Implement-Review-Revise Workflow](#implement-review-revise-workflow)
- [Error Handling with Retry](#error-handling-with-retry)
- [Loop Workflows](#loop-workflows)
- [Best Practices](#best-practices)
- [Troubleshooting](#troubleshooting)

---

## Design-Implement-Test Workflow

A simple sequential workflow where one agent designs a feature, another implements it, and a third writes tests.

```yaml
schema_version: "2.0"
name: design-implement-test
description: Sequential workflow for feature development

vars:
  feature:
    description: Feature to implement
    required: true
    type: string

  target_file:
    description: Target file for implementation
    required: false
    default: ""
    type: string

settings:
  timeout: 30m
  on_error: fail
  notify_on_complete: true

steps:
  # Step 1: Design the feature
  - id: design
    agent: claude
    prompt: |
      Design a solution for the following feature:

      ${vars.feature}

      Provide:
      1. High-level approach
      2. Key functions/methods needed
      3. Data structures
      4. Error handling strategy

      Keep the design concise and implementation-ready.
    timeout: 5m
    output_var: design_doc

  # Step 2: Implement based on design
  - id: implement
    agent: codex
    depends_on: [design]
    prompt: |
      Implement the following design:

      ${vars.design_doc}

      ${vars.target_file != "" ? "Target file: " + vars.target_file : "Choose an appropriate file location."}

      Follow the existing code style. Write clean, well-documented code.
    timeout: 10m
    output_var: implementation

  # Step 3: Write tests
  - id: test
    agent: claude
    depends_on: [implement]
    prompt: |
      Write comprehensive tests for the implementation:

      ${vars.implementation}

      Include:
      - Unit tests for core functions
      - Edge case coverage
      - Error condition tests

      Follow the project's testing patterns.
    timeout: 8m
    output_var: tests

  # Step 4: Run tests and verify
  - id: verify
    agent: codex
    depends_on: [test]
    prompt: |
      Run the tests that were just written and verify they pass.
      If any tests fail, fix the implementation to make them pass.
    timeout: 5m
```

### Usage

```bash
# Run the workflow
ntm pipeline run design-implement-test.yaml \
  --var feature="Add rate limiting to the API endpoint" \
  --var target_file="internal/api/ratelimit.go"

# Run with progress tracking
ntm pipeline run design-implement-test.yaml \
  --var feature="Implement user authentication" \
  --progress
```

---

## Implement-Review-Revise Workflow

A review pipeline where code is implemented, reviewed by another agent, and revised based on feedback.

```yaml
schema_version: "2.0"
name: implement-review-revise
description: Code implementation with peer review and revision

vars:
  task:
    description: Implementation task
    required: true
    type: string

  max_revisions:
    description: Maximum revision cycles
    required: false
    default: 2
    type: number

settings:
  timeout: 45m
  on_error: continue

steps:
  # Initial implementation
  - id: implement
    agent: claude
    prompt: |
      Implement the following task:

      ${vars.task}

      Write production-quality code with:
      - Clear function signatures
      - Error handling
      - Comments for complex logic
    timeout: 10m
    output_var: initial_code

  # Code review by different agent
  - id: review
    agent: codex
    depends_on: [implement]
    prompt: |
      Review the following code implementation:

      ${vars.initial_code}

      Provide structured feedback:
      1. **Critical Issues**: Bugs, security problems, logic errors
      2. **Improvements**: Performance, readability, maintainability
      3. **Style**: Naming, formatting, documentation

      Format your response as:
      APPROVAL_STATUS: APPROVED | NEEDS_REVISION

      CRITICAL_ISSUES:
      - [list any critical issues]

      IMPROVEMENTS:
      - [list suggested improvements]

      STYLE:
      - [list style suggestions]
    timeout: 5m
    output_var: review_feedback
    output_parse:
      type: regex
      pattern: "APPROVAL_STATUS:\\s*(?P<status>APPROVED|NEEDS_REVISION)"

  # Revision based on feedback
  - id: revise
    agent: claude
    depends_on: [review]
    when: ${vars.review_feedback_parsed.status} == "NEEDS_REVISION"
    prompt: |
      Revise the code based on the following review feedback:

      Original code:
      ${vars.initial_code}

      Review feedback:
      ${vars.review_feedback}

      Address all critical issues and incorporate reasonable improvements.
    timeout: 10m
    output_var: revised_code

  # Final review
  - id: final_review
    agent: gemini
    depends_on: [revise]
    when: ${steps.revise.status} == "completed"
    prompt: |
      Final review of the revised code:

      ${vars.revised_code}

      Verify that:
      1. All critical issues have been addressed
      2. The code follows best practices
      3. No new issues were introduced

      Respond with FINAL_APPROVAL: YES or NO
    timeout: 5m
    output_var: final_approval

  # Summary step (always runs)
  - id: summary
    agent: claude
    depends_on: [implement, review, revise, final_review]
    prompt: |
      Summarize the implementation and review process:

      Initial implementation: ${vars.initial_code}
      Review feedback: ${vars.review_feedback}
      ${steps.revise.status == "completed" ? "Revised code: " + vars.revised_code : "No revision needed."}
      ${steps.final_review.status == "completed" ? "Final approval: " + vars.final_approval : ""}

      Provide a brief summary of what was built and any remaining considerations.
    timeout: 3m
```

### Usage

```bash
# Run the review workflow
ntm pipeline run implement-review-revise.yaml \
  --var task="Add pagination to the list endpoint with cursor-based navigation"

# Resume from a failed state
ntm pipeline resume implement-review-revise.yaml
```

---

## Error Handling with Retry

Demonstrates retry logic with backoff for flaky operations like external API calls.

```yaml
schema_version: "2.0"
name: error-handling-demo
description: Demonstrates retry and error recovery patterns

vars:
  api_endpoint:
    description: External API to call
    required: true
    type: string

  fallback_enabled:
    description: Enable fallback behavior on persistent failure
    required: false
    default: true
    type: boolean

settings:
  timeout: 20m
  on_error: continue
  notify_on_error: true

steps:
  # Step with retry and exponential backoff
  - id: fetch_external_data
    agent: claude
    prompt: |
      Fetch data from the external API: ${vars.api_endpoint}

      Make the HTTP request and return the response.
      If the request fails, throw an error with the status code.
    timeout: 2m
    on_error: retry
    retry_count: 3
    retry_delay: 5s
    retry_backoff: exponential
    output_var: api_response

  # Fallback step if primary fails
  - id: fallback_data
    agent: codex
    depends_on: [fetch_external_data]
    when: ${steps.fetch_external_data.status} == "failed" && ${vars.fallback_enabled}
    prompt: |
      The external API call failed. Generate mock data as a fallback.

      API endpoint: ${vars.api_endpoint}
      Error: ${steps.fetch_external_data.error | "Unknown error"}

      Create realistic mock data that matches the expected response format.
    timeout: 2m
    output_var: fallback_response

  # Process data from either source
  - id: process_data
    agent: claude
    depends_on: [fetch_external_data, fallback_data]
    prompt: |
      Process the following data:

      ${steps.fetch_external_data.status == "completed"
        ? "API Response: " + vars.api_response
        : "Fallback Data: " + vars.fallback_response}

      Transform the data into the required format for downstream processing.
    timeout: 5m
    output_var: processed_data

  # Parallel validation with fail_fast
  - id: validate
    on_error: fail_fast
    timeout: 3m
    parallel:
      - id: schema_check
        agent: codex
        prompt: |
          Validate the data schema:
          ${vars.processed_data}

          Respond with VALID or INVALID with reason.
        output_var: schema_result

      - id: business_rules_check
        agent: gemini
        prompt: |
          Check business rules against:
          ${vars.processed_data}

          Respond with VALID or INVALID with reason.
        output_var: rules_result

  # Recovery on validation failure
  - id: recovery
    agent: claude
    depends_on: [validate]
    when: ${steps.validate.status} == "failed"
    prompt: |
      Data validation failed. Analyze the issues and attempt recovery:

      Schema check: ${vars.schema_result | "Not completed"}
      Rules check: ${vars.rules_result | "Not completed"}

      Suggest corrections or escalate if unrecoverable.
    timeout: 3m
    on_error: fail
```

### Retry Timing Example

With `retry_delay: 5s` and `retry_backoff: exponential`:

| Attempt | Wait Before |
|---------|-------------|
| 1       | 0s (immediate) |
| 2       | 5s |
| 3       | 10s |
| 4       | 20s |

### Usage

```bash
# Run with retries
ntm pipeline run error-handling-demo.yaml \
  --var api_endpoint="https://api.example.com/data"

# Dry run to validate workflow
ntm pipeline run error-handling-demo.yaml \
  --var api_endpoint="https://api.example.com/data" \
  --dry-run
```

---

## Loop Workflows

Examples of iterative workflows using loops.

### For-Each Loop

Process a list of items:

```yaml
schema_version: "2.0"
name: foreach-example
description: Process multiple files

vars:
  files:
    description: List of files to process
    default: ["src/api.go", "src/handler.go", "src/model.go"]
    type: array

steps:
  - id: process_files
    loop:
      items: ${vars.files}
      as: file
      max_iterations: 10
      collect: results
      steps:
        - id: review_file
          agent: claude
          prompt: Review the file ${loop.file} for code quality issues.
          output_var: file_review
    output_var: all_reviews

  - id: summarize
    depends_on: [process_files]
    agent: claude
    prompt: |
      Summarize the reviews for all files:
      ${vars.all_reviews}
```

### While Loop

Iterate until a condition is met:

```yaml
schema_version: "2.0"
name: while-example
description: Iterative refinement until quality threshold

steps:
  - id: initial_draft
    agent: claude
    prompt: Write an initial draft of the documentation.
    output_var: draft

  - id: refine
    depends_on: [initial_draft]
    loop:
      while: ${vars.quality_score} < 8
      max_iterations: 5
      delay: 2s
      steps:
        - id: evaluate
          agent: codex
          prompt: |
            Rate this documentation 1-10:
            ${vars.draft}

            Respond with just the number.
          output_var: quality_score
          output_parse: first_line

        - id: improve
          agent: claude
          when: ${vars.quality_score} < 8
          prompt: |
            Improve this documentation (current score: ${vars.quality_score}):
            ${vars.draft}
          output_var: draft
```

### Times Loop

Run a fixed number of iterations:

```yaml
schema_version: "2.0"
name: times-example
description: Generate multiple variations

steps:
  - id: generate_variations
    loop:
      times: 3
      collect: variations
      steps:
        - id: generate
          agent: claude
          prompt: |
            Generate a unique variation of the API response handler.
            This is variation ${loop.index + 1} of 3.
          output_var: variation
    output_var: all_variations

  - id: select_best
    depends_on: [generate_variations]
    agent: codex
    prompt: |
      Select the best variation from:
      ${vars.all_variations}

      Explain your choice.
```

---

## Best Practices

### 1. Step Design

**Keep steps focused**
```yaml
# Good: Single responsibility
- id: fetch_data
  prompt: Fetch user data from the API

- id: transform_data
  prompt: Transform the user data into report format

# Avoid: Multiple concerns in one step
- id: fetch_and_transform
  prompt: Fetch user data and transform it into a report
```

**Use meaningful IDs**
```yaml
# Good: Descriptive IDs
- id: validate_schema
- id: generate_report
- id: notify_stakeholders

# Avoid: Generic IDs
- id: step1
- id: step2
```

### 2. Dependencies

**Explicit dependencies for clarity**
```yaml
# Good: Explicit chain
- id: design
  prompt: Design the solution

- id: implement
  depends_on: [design]
  prompt: Implement based on ${steps.design.output}

# Avoid: Implicit ordering assumptions
```

**Fan-in pattern for aggregation**
```yaml
- id: parallel_work
  parallel:
    - id: research
    - id: prototype
    - id: review

- id: synthesize
  depends_on: [parallel_work]  # Wait for all parallel steps
  prompt: Combine all findings...
```

### 3. Error Handling

**Use retry for transient failures**
```yaml
- id: external_call
  on_error: retry
  retry_count: 3
  retry_delay: 10s
  retry_backoff: exponential
```

**Use continue for non-critical steps**
```yaml
settings:
  on_error: continue  # Don't fail workflow on non-critical errors

steps:
  - id: optional_notification
    prompt: Send Slack notification
    on_error: continue  # OK if this fails
```

**Provide fallbacks**
```yaml
- id: primary_action
  prompt: Try the preferred approach

- id: fallback_action
  when: ${steps.primary_action.status} == "failed"
  prompt: Use alternative approach
```

### 4. Variables

**Validate required inputs**
```yaml
vars:
  api_key:
    description: API key for authentication
    required: true
    type: string

  max_retries:
    description: Maximum retry attempts
    required: false
    default: 3
    type: number
```

**Use defaults for optional parameters**
```yaml
prompt: |
  Process ${vars.items | "[]"} items
  Timeout: ${vars.timeout | "5m"}
```

### 5. Parallel Execution

**Use parallel for independent work**
```yaml
- id: multi_review
  parallel:
    - id: security_review
      agent: claude
      prompt: Security review

    - id: performance_review
      agent: codex
      prompt: Performance review
```

**Set appropriate timeouts for parallel groups**
```yaml
- id: parallel_group
  timeout: 10m  # Timeout for entire group
  parallel:
    - id: task1
      timeout: 5m  # Individual timeout
    - id: task2
      timeout: 5m
```

### 6. Output Management

**Parse structured output when needed**
```yaml
- id: get_config
  prompt: Return the configuration as JSON
  output_parse: json
  output_var: config

- id: use_config
  prompt: Use setting ${vars.config.timeout}
```

**Use descriptive variable names**
```yaml
output_var: user_validation_result  # Good
output_var: result                   # Too generic
```

---

## Troubleshooting

### Common Issues

#### 1. Step Timeout

**Symptom**: Step fails with "timeout waiting for completion"

**Causes**:
- Agent is still processing
- Agent is stuck in an error state
- Prompt is too complex

**Solutions**:
```yaml
# Increase timeout
- id: complex_task
  timeout: 15m  # Increase from default 5m

# Split into smaller steps
- id: part1
  prompt: First half of the task

- id: part2
  depends_on: [part1]
  prompt: Second half using ${steps.part1.output}
```

#### 2. Variable Not Found

**Symptom**: `${vars.x}` appears literally in output

**Causes**:
- Variable not defined
- Typo in variable name
- Variable from failed step

**Solutions**:
```yaml
# Add default value
prompt: Hello ${vars.name | "User"}

# Check step status before using output
- id: use_result
  when: ${steps.previous.status} == "completed"
  prompt: Process ${vars.previous_output}
```

#### 3. Parallel Step Conflicts

**Symptom**: Parallel steps interfere with each other

**Causes**:
- Steps modifying same files
- Resource contention

**Solutions**:
```yaml
# Assign specific panes to avoid agent contention
- id: parallel_work
  parallel:
    - id: task1
      pane: 1
      prompt: Work on module A

    - id: task2
      pane: 2
      prompt: Work on module B
```

#### 4. Dependency Cycle

**Symptom**: Workflow fails validation with "cycle detected"

**Causes**:
- Circular `depends_on` references

**Solutions**:
```yaml
# Wrong: Circular dependency
- id: a
  depends_on: [b]
- id: b
  depends_on: [a]

# Correct: Linear or fan-in/fan-out
- id: a
- id: b
  depends_on: [a]
- id: c
  depends_on: [a]
- id: d
  depends_on: [b, c]
```

#### 5. Resume Not Working

**Symptom**: Resume starts from the beginning

**Causes**:
- State file not saved
- Different workflow file

**Solutions**:
```bash
# Check state file exists
ls .ntm/pipeline-state/

# Use explicit state file
ntm pipeline resume workflow.yaml --state-file .ntm/pipeline-state/run-xxx.json
```

### Debugging Tips

#### Enable Verbose Output

```bash
ntm pipeline run workflow.yaml --verbose
```

#### Dry Run First

```bash
ntm pipeline run workflow.yaml --dry-run
```

#### Check Step Status

```bash
ntm pipeline status workflow.yaml
```

#### View Execution State

```bash
# List recent runs
ntm pipeline list

# Show specific run details
ntm pipeline show <run-id>
```

#### Monitor Progress

```bash
# Real-time progress
ntm pipeline run workflow.yaml --progress

# Tail execution logs
ntm pipeline tail <run-id>
```
