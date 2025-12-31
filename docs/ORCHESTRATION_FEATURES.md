# NTM Orchestration Features - Comprehensive Design Document

> This document captures the complete design, rationale, and implementation plan for
> NTM's next-generation orchestration capabilities. It serves as the authoritative
> reference for the feature set and should be updated as implementation proceeds.

**Document Version**: 2.0 (2025-12-30)

## Revision History

| Version | Date | Changes |
|---------|------|---------|
| 2.0 | 2025-12-30 | Major revision: Added state transition hysteresis, enhanced wait command with error handling, Agent Mail integration for alerts, configurable scoring weights, sticky routing, parallel step execution, conditional logic, loop constructs, output parsing, pipeline notifications |
| 1.0 | 2025-12-30 | Initial design document |

---

## Key Improvements in v2.0

### Activity Detection
- **State Transition Hysteresis**: Prevents rapid state flapping by requiring 2s stability before transition (except ERROR which transitions immediately)
- **Unicode & ANSI Handling**: Proper character counting with escape sequence stripping
- **Enhanced Wait Command**: Error handling with `--exit-on-error`, composed conditions, partial wait support

### Health & Resilience
- **Soft vs Hard Restart**: Tries soft restart (Ctrl+C) before hard restart (kill + relaunch)
- **Agent Mail Integration**: Health alerts sent via Agent Mail for multi-agent awareness
- **Context Loss Notification**: Notifies when hard restart causes context loss

### Smart Routing
- **Configurable Scoring Weights**: Users can tune the 40%/40%/20% balance via config
- **Sticky Routing Strategy**: Prefers same agent for related tasks (context affinity)
- **Agent Mail Reservation Integration**: File reservations boost agent scores

### Output Synthesis
- **Structured Extraction**: Parse code blocks, JSON, file paths from agent output
- **Activity Summary Generation**: Per-agent reports of what was accomplished

### CASS Injection
- **Topic Filtering**: Exclude irrelevant historical context by category
- **Smart Placement**: Context injection format varies by agent type

### Workflow Pipelines (Major Enhancements)
- **Parallel Step Execution**: Run multiple steps concurrently on different agents
- **Conditional Logic**: `when:` clauses with full expression evaluation
- **Loop Constructs**: for-each, while, and times loops with break/continue
- **Output Parsing**: Extract JSON fields, regex captures from step outputs
- **Pipeline Notifications**: Desktop, webhook, and Agent Mail on completion/failure

## Executive Summary

NTM currently excels at **infrastructure** (spawning agents, sending prompts, capturing output)
but lacks **intelligence** (understanding what agents are doing, routing work smartly,
synthesizing results). This document describes six foundational features that transform
NTM from a session manager into an intelligent multi-agent orchestration platform.

---

## The Vision

```
Current NTM                          Future NTM
┌─────────────────┐                  ┌─────────────────────────────────────┐
│ Spawn agents    │                  │ Spawn agents                        │
│ Send prompts    │      ────►       │ Understand agent states             │
│ Capture output  │                  │ Route work intelligently            │
│ (blind)         │                  │ Detect/recover from failures        │
└─────────────────┘                  │ Synthesize parallel outputs         │
                                     │ Learn from history (CASS)           │
                                     │ Execute complex workflows           │
                                     └─────────────────────────────────────┘
```

---

## Feature 1: Agent Activity Detection

### Problem Statement

NTM is currently "blind" after sending a prompt. It cannot answer:
- Is the agent actively generating output?
- Is the agent waiting for input?
- Has the agent encountered an error?
- Is the agent stalled or crashed?

This blindness prevents intelligent orchestration, automated workflows, and reliable scripting.

### Solution Overview

Implement real-time activity state detection by analyzing pane output characteristics.

### Core Concepts

**1. Output Velocity**
Track characters per second being written to each pane. This is the primary activity signal.

```
High velocity (>10 c/s)  → Agent is generating
Zero velocity            → Agent may be idle OR stalled (need other signals)
Burst patterns           → Typical of AI token streaming
```

**2. Pattern Library**
Agent-specific regex patterns that identify states:

| Pattern Type | Claude Examples | Codex Examples | Gemini Examples |
|--------------|-----------------|----------------|-----------------|
| Idle Prompt | `claude>`, `Claude Code>` | `$`, `codex>` | `gemini>`, `>>>` |
| Error | `rate limit`, `error:` | `Error:`, `429` | `Error`, `quota` |
| Thinking | `...`, `Thinking` | `...` | `Thinking...` |
| Completion | `Done`, `✓` | `Complete` | `Finished` |

**3. State Classification**
Combine velocity + patterns into confidence-weighted states:

```go
type AgentState string

const (
    StateGenerating AgentState = "GENERATING"  // High velocity, active output
    StateWaiting    AgentState = "WAITING"     // Idle prompt, ready for input
    StateThinking   AgentState = "THINKING"    // Low velocity, processing
    StateError      AgentState = "ERROR"       // Error pattern detected
    StateStalled    AgentState = "STALLED"     // No activity when expected
    StateUnknown    AgentState = "UNKNOWN"     // Insufficient signals
)
```

**4. Classification Algorithm**

```
INPUT: velocity (chars/sec), last_output_age, patterns_detected, expected_state

IF error_pattern IN patterns_detected:
    RETURN ERROR, confidence=0.95

IF idle_prompt IN patterns_detected:
    IF velocity < 1:
        RETURN WAITING, confidence=0.90
    ELSE:
        RETURN GENERATING, confidence=0.70  # Outputting but has prompt visible

IF thinking_pattern IN patterns_detected:
    RETURN THINKING, confidence=0.80

IF velocity > 10:
    RETURN GENERATING, confidence=0.85

IF velocity == 0 AND last_output_age > stall_threshold:
    IF expected_state == GENERATING:
        RETURN STALLED, confidence=0.75
    ELSE:
        RETURN WAITING, confidence=0.60  # Might just be idle

RETURN UNKNOWN, confidence=0.50
```

### API Design

**Robot Mode:**
```bash
ntm --robot-activity=SESSION [--activity-panes=1,2] [--activity-type=claude]
```

```json
{
  "success": true,
  "timestamp": "2025-01-15T10:30:00Z",
  "session": "myproject",
  "agents": [
    {
      "pane": "myproject__cc_1",
      "pane_idx": 1,
      "agent_type": "claude",
      "state": "GENERATING",
      "confidence": 0.92,
      "velocity_cps": 45.2,
      "last_output_at": "2025-01-15T10:30:00Z",
      "state_since": "2025-01-15T10:29:30Z",
      "state_duration_seconds": 30,
      "detected_patterns": ["thinking_indicator"]
    }
  ],
  "summary": {
    "generating": 1,
    "waiting": 2,
    "thinking": 0,
    "error": 0,
    "stalled": 0,
    "unknown": 0
  },
  "_agent_hints": {
    "available_agents": ["myproject__cc_2", "myproject__cod_1"],
    "busy_agents": ["myproject__cc_1"],
    "suggestions": ["2 agents ready for new work"]
  }
}
```

**Human CLI:**
```
$ ntm activity myproject

Session: myproject                                    10:30:00
┌───────────────────┬──────────────┬───────────┬────────────┐
│ Pane              │ State        │ Velocity  │ Duration   │
├───────────────────┼──────────────┼───────────┼────────────┤
│ myproject__cc_1   │ ● GENERATING │ 45 c/s    │ 30s        │
│ myproject__cc_2   │ ○ WAITING    │ 0 c/s     │ 2m 15s     │
│ myproject__cod_1  │ ○ WAITING    │ 0 c/s     │ 5m 30s     │
│ myproject__gmi_1  │ ✗ ERROR      │ 0 c/s     │ 12m        │
└───────────────────┴──────────────┴───────────┴────────────┘
Summary: 1 generating, 2 waiting, 1 error
```

**Wait Command:**
```bash
# Wait for all agents to be idle
ntm wait myproject --until=idle --timeout=5m

# Wait for any agent to become available
ntm wait myproject --until=any-idle --timeout=2m

# Wait for specific pane
ntm wait myproject --pane=1 --until=idle --timeout=3m

# Robot mode
ntm --robot-wait=SESSION --wait-until=idle --wait-timeout=5m
```

### Implementation Considerations

1. **Efficiency**: Don't capture full pane output too frequently. Sample at 500ms-1s intervals.

2. **Accuracy vs Speed**: More frequent sampling = more accurate velocity, but more overhead.

3. **Agent-Specific Patterns**: Pattern library should be easily extensible for new agents.

4. **Tmux Integration**: Use `tmux capture-pane` for output, `tmux list-panes` for metadata.

5. **State Persistence**: Track state history for debugging and analytics.

### Why This is Foundational

Activity detection enables:
- **Smart Routing** (Feature 3): Route to agents that are WAITING
- **Health Monitoring** (Feature 2): Detect STALLED and ERROR states
- **Workflow Pipelines** (Feature 6): Wait for step completion
- **Dashboard Enhancement**: Show real-time agent status

---

## Feature 2: Agent Health & Resilience

### Problem Statement

Agents can fail in many ways:
- Process crashes (agent CLI exits)
- Rate limiting (API throttles requests)
- Network errors (connectivity issues)
- Context overflow (agent refuses input)
- Silent hangs (no output, no error)

Currently, users don't know about failures until they manually check. No automatic recovery exists.

### Solution Overview

Continuous health monitoring with automatic recovery and alerting.

### Health States

```
┌──────────────┬────────────────────────────────────────────────────────┐
│ State        │ Meaning                                                │
├──────────────┼────────────────────────────────────────────────────────┤
│ healthy      │ All checks pass, functioning normally                  │
│ degraded     │ Recent issues (errors, restarts) but currently working │
│ unhealthy    │ Critical failure, needs intervention or restart        │
│ rate_limited │ API rate limit detected, in backoff period             │
└──────────────┴────────────────────────────────────────────────────────┘
```

### Health Check Components

**1. Process Check**
Is the agent process still running?
- Check for shell prompt in pane (agent crashed back to shell)
- Look for exit codes or "exited" messages

**2. Stall Detection**
Is the agent hung?
- Uses Activity Detection (Feature 1)
- No output when activity expected
- Not in WAITING state but no progress

**3. Error Detection**
Has the agent hit an error?
- Rate limit patterns: `rate limit`, `429`, `quota exceeded`
- Error patterns: `error:`, `exception`, `failed`
- Crash patterns: `panic`, `segfault`, `killed`

**4. Rate Limit Tracking**
Track rate limit events for backoff:
- Count rate limits in sliding window
- Calculate appropriate backoff delay
- Prevent sending during backoff

### Automatic Recovery

**Restart Logic:**
```
ON agent_health == unhealthy:
    IF restarts_this_hour < max_restarts:
        backoff_delay = min(base * 2^restarts, max_backoff)
        WAIT backoff_delay

        SEND Ctrl+C to pane
        WAIT for shell prompt
        SEND agent launch command (cc, cod, gmi)

        restarts_this_hour++
        UPDATE health state
    ELSE:
        ALERT "Max restarts exceeded, manual intervention needed"
```

**Rate Limit Backoff:**
```
ON rate_limit_detected:
    SET agent_state = rate_limited
    backoff = 30s * 2^(consecutive_rate_limits - 1)
    backoff = min(backoff, 5m)  # Cap at 5 minutes

    BLOCK sends to this agent for backoff duration
    AFTER backoff: CLEAR rate_limited state
```

### Configuration

```toml
[health]
enabled = true
check_interval = "30s"      # How often to check health (watch mode)
stall_threshold = "60s"     # No output for this long = stalled

# Auto-restart settings
auto_restart = true
max_restarts = 3            # Per hour
restart_backoff_base = "30s"
restart_backoff_max = "5m"

# Rate limit handling
rate_limit_backoff_base = "30s"
rate_limit_backoff_max = "5m"

[alerts]
enabled = true
desktop_notify = true       # notify-send / osascript
webhook_url = ""            # POST to this URL on state change
alert_on = ["unhealthy", "rate_limited", "restart"]
```

### API Design

**Robot Mode:**
```bash
ntm --robot-health=SESSION
```

```json
{
  "success": true,
  "session": "myproject",
  "checked_at": "2025-01-15T10:30:00Z",
  "agents": [
    {
      "pane": "myproject__cc_1",
      "health": "healthy",
      "uptime_seconds": 8100,
      "restarts_total": 0,
      "restarts_this_hour": 0,
      "last_error": null,
      "rate_limit_count": 0,
      "backoff_remaining_seconds": 0
    },
    {
      "pane": "myproject__cod_1",
      "health": "degraded",
      "uptime_seconds": 2700,
      "restarts_total": 2,
      "restarts_this_hour": 2,
      "last_error": {
        "type": "rate_limit",
        "at": "2025-01-15T10:25:00Z",
        "message": "Rate limit exceeded, retrying in 60s"
      },
      "rate_limit_count": 3,
      "backoff_remaining_seconds": 45
    }
  ],
  "summary": {
    "healthy": 1,
    "degraded": 1,
    "unhealthy": 0,
    "rate_limited": 0,
    "total_restarts_24h": 2,
    "agents_in_backoff": 1
  }
}
```

**Human CLI:**
```
$ ntm health myproject

Session: myproject                Health Report
┌───────────────────┬──────────┬─────────┬──────────┬─────────────┐
│ Pane              │ Health   │ Uptime  │ Restarts │ Last Error  │
├───────────────────┼──────────┼─────────┼──────────┼─────────────┤
│ myproject__cc_1   │ ● healthy│ 2h 15m  │ 0        │ -           │
│ myproject__cod_1  │ ◐ degraded│ 45m    │ 2        │ rate_limit  │
└───────────────────┴──────────┴─────────┴──────────┴─────────────┘

$ ntm health myproject --verbose
# Shows full error details, backoff timers, etc.
```

### Implementation Notes

1. **On-Demand vs Continuous**: Start with on-demand health checks. Background monitoring can be added later.

2. **Restart Command Detection**: Need to detect the correct agent launch command per pane type.

3. **Context Loss**: Restarting an agent loses its context. This is unavoidable but should be logged.

4. **Alert Fatigue**: Debounce alerts to avoid spamming on flapping agents.

---

## Feature 3: Smart Work Distribution

### Problem Statement

With multiple agents, users manually decide which one to send work to. This leads to:
- Sending to already-busy agents (delays)
- Sending to nearly-full context agents (quality issues)
- Uneven load distribution

### Solution Overview

Intelligent routing based on agent state and context usage.

### Scoring System

Each agent gets a score (0-100) based on multiple factors:

```
Score = (context_score × 0.4) + (state_score × 0.4) + (recency_score × 0.2)

where:
  context_score = 100 - context_usage_percent
  state_score = {WAITING: 100, THINKING: 50, GENERATING: 0, ERROR: -100}
  recency_score = based on time since last activity
```

**Example Scoring:**

| Agent | Context | State | Recency | Score | Recommendation |
|-------|---------|-------|---------|-------|----------------|
| cc_1 | 72% | GENERATING | 5s | 11.2 + 0 + 15 = 26 | Skip (busy) |
| cc_2 | 28% | WAITING | 2m | 28.8 + 40 + 8 = 77 | Good choice |
| cc_3 | 45% | WAITING | 5m | 22 + 40 + 5 = 67 | Acceptable |

### Routing Strategies

**1. least-loaded (default)**
Pick agent with highest score. Best for load balancing.

**2. first-available**
Pick first agent in WAITING state. Fastest, no scoring overhead.

**3. round-robin**
Rotate through agents regardless of state. Predictable distribution.

**4. random**
Random selection among available agents. Simple load distribution.

### API Design

**Get Routing Recommendation:**
```bash
ntm --robot-route=SESSION --type=claude --strategy=least-loaded
```

```json
{
  "success": true,
  "recommendation": {
    "pane": "myproject__cc_2",
    "pane_idx": 2,
    "agent_type": "claude",
    "score": 77,
    "reason": "highest_score_available"
  },
  "strategy": "least-loaded",
  "candidates": [
    {"pane": "myproject__cc_1", "score": 26, "context_pct": 72, "state": "GENERATING"},
    {"pane": "myproject__cc_2", "score": 77, "context_pct": 28, "state": "WAITING"},
    {"pane": "myproject__cc_3", "score": 67, "context_pct": 45, "state": "WAITING"}
  ]
}
```

**Smart Send:**
```bash
# Old way: broadcast to all Claude agents
ntm send myproject --cc "Fix the bug"

# New way: send to best available Claude agent
ntm send myproject --cc --smart "Fix the bug"

# Or with explicit strategy
ntm send myproject --cc --route=least-loaded "Fix the bug"
```

**Robot Mode Send with Routing:**
```bash
ntm --robot-send=SESSION --msg="Fix bug" --type=claude --route=least-loaded
```

Response includes routing info:
```json
{
  "success": true,
  "routed_to": "myproject__cc_2",
  "routing_score": 77,
  "routing_reason": "highest_score_available",
  ...
}
```

### Integration with Health

Skip unhealthy and rate-limited agents:
- `health == unhealthy` → score = -100 (excluded)
- `health == rate_limited` → score = -50 (excluded unless only option)

---

## Feature 4: Output Synthesis

### Problem Statement

When multiple agents work in parallel:
- They may modify the same files (conflict)
- Their approaches may differ (need comparison)
- Summarizing activity is manual

### Solution Overview

Tools for detecting conflicts, comparing outputs, and summarizing activity.

### Scope (Phase 1)

1. **File Conflict Detection**: Use git to detect overlapping modifications
2. **Output Comparison**: Side-by-side or unified diff of agent outputs
3. **Activity Summary**: What did each agent do recently?

Note: AI-powered synthesis is out of scope for Phase 1.

### File Conflict Detection

Approach using git:
```bash
# 1. Get current git status
git status --porcelain

# 2. Track which files were modified during session
# 3. If same file modified by multiple agents → potential conflict
```

Challenges:
- Git doesn't know which agent modified which file
- Heuristic: Track file modification times vs. agent activity times
- Best effort: Flag files modified during multi-agent activity

### API Design

**Robot Mode:**
```bash
ntm --robot-diff=SESSION --since=10m
```

```json
{
  "success": true,
  "timeframe": {
    "since": "2025-01-15T10:20:00Z",
    "until": "2025-01-15T10:30:00Z"
  },
  "files": {
    "modified": ["src/auth.go", "src/user.go", "tests/auth_test.go"],
    "potential_conflicts": [
      {
        "file": "src/auth.go",
        "likely_modifiers": ["myproject__cc_1", "myproject__cc_2"],
        "git_status": "modified",
        "reason": "Both agents active during file modification window"
      }
    ],
    "clean": ["src/user.go", "tests/auth_test.go"]
  },
  "agent_activity": [
    {"pane": "myproject__cc_1", "output_lines": 245, "active_time_s": 180},
    {"pane": "myproject__cc_2", "output_lines": 189, "active_time_s": 150}
  ]
}
```

**Human CLI:**
```bash
$ ntm conflicts myproject

Potential Conflicts:
  src/auth.go
    ├── myproject__cc_1 (active during modification)
    └── myproject__cc_2 (active during modification)

Clean Modifications:
  src/user.go
  tests/auth_test.go

$ ntm diff myproject --panes=1,2 --last=50
# Shows side-by-side output comparison
```

---

## Feature 5: CASS Auto-Injection

### Problem Statement

CASS contains valuable historical context from past sessions. Currently:
1. Users must remember to query CASS
2. Users must manually copy/paste relevant findings
3. Context is often forgotten or overlooked

### Solution Overview

Automatically inject relevant CASS findings before sending prompts.

### How It Works

```
User: ntm send myproject --cc "Implement rate limiting"
         │
         ▼
    ┌─────────────────────────────────────┐
    │ 1. Extract keywords from prompt     │
    │ 2. Query CASS for relevant history  │
    │ 3. Filter by relevance threshold    │
    │ 4. Check agent's context budget     │
    │ 5. Format and prepend to prompt     │
    └─────────────────────────────────────┘
         │
         ▼
Injected prompt:

    [Context from past sessions]
    - Session abc (0.89 relevance, 2 days ago):
      Implemented token bucket rate limiting using Redis...
    - Session def (0.82 relevance, 5 days ago):
      Per-IP rate limits with sliding window algorithm...

    [Your task]
    Implement rate limiting
```

### Configuration

```toml
[cass]
auto_inject = true          # Enable by default
inject_limit = 3            # Max items to inject
min_relevance = 0.7         # Minimum similarity score
max_inject_tokens = 500     # Token budget for injection
skip_if_context_above = 60  # Skip if agent > 60% context usage
prefer_same_project = true  # Prefer history from same project
max_age_days = 30           # Only consider recent history
```

### API Integration

```bash
# Enable for this send
ntm send myproject --cc --with-cass "Implement rate limiting"

# Disable for this send (overrides config)
ntm send myproject --cc --no-cass "Simple fix"

# Preview what would be injected
ntm cass preview "Implement rate limiting"
```

**Robot Mode:**
```json
{
  "success": true,
  "cass_injection": {
    "enabled": true,
    "query": "Implement rate limiting",
    "items_found": 5,
    "items_injected": 2,
    "tokens_added": 380,
    "sources": [
      {"session": "abc", "relevance": 0.89, "age_days": 2},
      {"session": "def", "relevance": 0.82, "age_days": 5}
    ],
    "skipped_reason": null
  },
  ...
}
```

---

## Feature 6: Workflow Pipelines

### Problem Statement

Complex tasks require multiple steps, different agents, waiting for completion, and error handling. Currently this requires manual orchestration.

### Solution Overview

Define workflows in YAML/TOML and execute them automatically.

### Workflow Schema (v2.0)

```yaml
schema_version: "2.0"
name: feature-implementation
description: Design → Implement → Test → Review
version: "1.0"

vars:
  feature_name:
    description: "Name of the feature to implement"
    required: true
    type: string
  run_tests:
    default: true
    type: boolean

settings:
  timeout: 30m
  notify_on_complete: true
  notify_on_error: true

steps:
  - id: design
    name: "Design Phase"
    agent: claude
    route: least-loaded
    prompt: |
      Design the architecture for: ${vars.feature_name}
    wait: completion
    timeout: 5m
    output_var: design_doc      # Store output in variable
    output_parse: none          # or json, yaml, lines

  # PARALLEL STEPS - run concurrently
  - id: parallel_impl
    parallel:
      - id: backend
        agent: codex
        prompt: Implement backend for: ${vars.design_doc}
      - id: frontend
        agent: claude
        prompt: Implement frontend for: ${vars.design_doc}

  # CONDITIONAL STEP - skip if condition false
  - id: test
    name: "Testing"
    when: ${vars.run_tests}    # Only run if run_tests is true
    agent: gemini
    depends_on: [parallel_impl]
    prompt: |
      Test these implementations:
      Backend: ${steps.backend.output}
      Frontend: ${steps.frontend.output}
    on_error: retry
    retry_count: 2

  - id: review
    depends_on: [parallel_impl, test]
    prompt: Review all changes
```

### Parallel Steps

Run multiple steps concurrently on different agents:

```yaml
- id: research_phase
  parallel:
    - id: market_research
      agent: claude
      prompt: Research market trends
    - id: tech_research
      agent: codex
      prompt: Research technical options
    - id: competitor_analysis
      agent: gemini
      prompt: Analyze competitors
  # All three run simultaneously, next step waits for all

- id: synthesis
  depends_on: [research_phase]
  prompt: |
    Synthesize findings:
    ${steps.market_research.output}
    ${steps.tech_research.output}
    ${steps.competitor_analysis.output}
```

### Conditional Execution

Skip steps based on conditions:

```yaml
- id: check_env
  prompt: Return the environment name
  output_var: env
  output_parse: first_line

- id: prod_deploy
  when: ${vars.env} == "production"
  prompt: Deploy to production with full validation

- id: staging_deploy
  when: ${vars.env} != "production"
  prompt: Quick deploy to staging
```

Supported operators: `==`, `!=`, `>`, `<`, `>=`, `<=`, `AND`, `OR`, `NOT`, `contains`

### Loop Constructs

Iterate over collections:

```yaml
# For-each loop
- id: process_files
  loop:
    items: ${vars.files}
    as: file
  steps:
    - id: process
      prompt: Process ${loop.file}

# Access loop variables: ${loop.file}, ${loop.index}, ${loop.count}

# While loop
- id: poll
  loop:
    while: ${vars.status} != "ready"
    max_iterations: 10
    delay: 30s
  steps:
    - id: check
      prompt: Check status
      output_var: status
```

### Output Parsing

Extract structured data from step outputs:

```yaml
- id: get_config
  prompt: Return config as JSON
  output_var: config
  output_parse: json           # Parse as JSON

- id: use_config
  prompt: Use port ${vars.config.port}  # Access JSON fields!

# Parsing modes: none, json, yaml, lines, first_line, regex
```

### Execution Model

```
1. Load and validate workflow
2. Resolve dependencies (topological sort)
3. For each step in order:
   a. Wait for dependencies to complete
   b. Resolve variables (${vars.X}, ${steps.Y.output})
   c. Select agent (routing strategy)
   d. Send prompt
   e. Wait for completion (using Feature 1)
   f. Capture output
   g. Update state
   h. Handle errors per step config
4. Complete workflow
```

### Variable Substitution

| Variable | Example | Description |
|----------|---------|-------------|
| `${vars.X}` | `${vars.feature_name}` | Runtime variable |
| `${steps.X.output}` | `${steps.design.output}` | Previous step output |
| `${steps.X.pane}` | `${steps.design.pane}` | Pane used for step |
| `${env.X}` | `${env.HOME}` | Environment variable |
| `${session}` | `myproject` | Session name |

### Error Handling

Per-step configuration:
- `on_error: fail` - Stop pipeline immediately (default)
- `on_error: continue` - Log error, continue to next step
- `on_error: retry` - Retry step N times with delay

### State Persistence

Pipeline state saved to `.ntm/pipelines/<run-id>.json`:
- Survives NTM restarts
- Enables resume after failure
- Provides audit trail

### API Design

```bash
# Run workflow
ntm pipeline run workflow.yaml --var feature_name="user auth"

# Check status
ntm pipeline status run-abc123

# List pipelines
ntm pipeline list

# Cancel
ntm pipeline cancel run-abc123

# Resume failed pipeline
ntm pipeline resume run-abc123
```

**Robot Mode:**
```bash
ntm --robot-pipeline-run=workflow.yaml --pipeline-vars='{"feature_name":"auth"}'
ntm --robot-pipeline=run-abc123
ntm --robot-pipeline-list
ntm --robot-pipeline-cancel=run-abc123
```

```json
{
  "id": "run-abc123",
  "workflow": "feature-implementation",
  "status": "running",
  "started_at": "2025-01-15T10:00:00Z",
  "current_step": "implement",
  "progress": {
    "completed": 1,
    "running": 1,
    "pending": 2,
    "failed": 0,
    "total": 4
  },
  "steps": [
    {
      "id": "design",
      "status": "completed",
      "agent": "myproject__cc_1",
      "started_at": "2025-01-15T10:00:00Z",
      "completed_at": "2025-01-15T10:02:30Z",
      "duration_seconds": 150,
      "output_lines": 89
    },
    {
      "id": "implement",
      "status": "running",
      "agent": "myproject__cod_2",
      "started_at": "2025-01-15T10:02:35Z"
    }
  ]
}
```

---

## Dependency Graph

```
                    ┌─────────────────────────────────────┐
                    │                                     │
                    │    Feature 1: Activity Detection    │
                    │         (FOUNDATION)                │
                    │                                     │
                    └───────────────┬─────────────────────┘
                                    │
                    ┌───────────────┼───────────────┐
                    │               │               │
                    ▼               ▼               ▼
        ┌───────────────┐  ┌───────────────┐  ┌───────────────┐
        │   Feature 2:  │  │   Feature 3:  │  │   Feature 4:  │
        │    Health &   │  │     Smart     │  │    Output     │
        │   Resilience  │  │    Routing    │  │   Synthesis   │
        └───────────────┘  └───────────────┘  └───────────────┘
                │                   │
                │                   │
                └─────────┬─────────┘
                          │
                          ▼
                ┌─────────────────────┐
                │    Feature 6:       │
                │ Workflow Pipelines  │
                └─────────────────────┘

        ┌─────────────────────┐
        │    Feature 5:       │   (Independent - uses existing CASS)
        │   CASS Injection    │
        └─────────────────────┘
```

---

## Implementation Order

**Phase 1: Foundation**
1. Activity Detection (enables everything else)
2. Health & Resilience (can start after Activity basics)
3. Smart Routing (can start after Activity basics)

**Phase 2: Power Features**
4. Output Synthesis (mostly independent)
5. CASS Injection (mostly independent)
6. Workflow Pipelines (needs Activity + Routing)

---

## Success Criteria

Each feature should meet these criteria before considered complete:

1. **Robot API**: Full JSON API with consistent structure
2. **Human CLI**: User-friendly commands with good UX
3. **Tests**: Unit tests + integration tests
4. **Documentation**: Updated README, inline help
5. **Dashboard Integration**: Shows relevant info in dashboard (where applicable)
6. **Configuration**: Sensible defaults, configurable behavior

---

*This document will be updated as implementation proceeds.*
