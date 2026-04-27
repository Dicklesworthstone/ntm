# NTM Improvement Plan

> **Document Purpose**: This is a comprehensive, self-contained strategic plan for improving NTM (Neural Terminal Manager). It is designed to be read by any LLM or human without requiring additional context—everything needed to understand and evaluate the plan is included here.

---

## About This Document

This plan outlines strategic improvements to elevate **NTM** from a capable power-user tool to the definitive command center for AI-assisted development. The document covers:

1. **What NTM is** and its role in the broader ecosystem
2. **The complete tool ecosystem** (the "Dicklesworthstone Stack")
3. **CRITICAL: Tier 0 integrations** - Completely unused features with massive impact
4. **Underexplored integrations** (bv robot modes, CASS search, s2p, UBS)
5. **Existing planned integrations** (CAAM, CM, SLB, Agent Mail)
6. **Concrete implementation patterns** with Go code examples
7. **Priority matrix** for implementation sequencing

**Key Insight**: NTM is the **cockpit** of an Agentic Coding Flywheel—an orchestration layer that coordinates multiple AI coding agents working in parallel. Deep research has revealed that most ecosystem tools have capabilities that remain **completely untapped** by NTM's current implementation.

**Critical Discovery**: The latest research identified **9 Tier 0 integrations**—features that are designed specifically for agent coordination but have ZERO usage in NTM. These represent the highest-impact, lowest-effort improvements available.

---

## Design Invariants, Non-Goals, and Risks

### Design Invariants (must always hold)

1. **No silent data loss**: NTM must never cause untracked destructive actions without explicit, recorded approval.
2. **Graceful degradation**: If any external tool is missing/unhealthy, NTM continues with reduced capability and clear warnings.
3. **Idempotent orchestration**: Spawning, reserving, assigning, and messaging should be safe to retry without duplicating work.
4. **Recoverable state**: NTM must be able to re-attach to an existing session after crash/restart.
5. **Auditable actions**: Critical actions (reservations, releases, force-releases, blocked commands, approvals) are logged with correlation IDs.
6. **Safe-by-default**: Risky automation (auto-push, force-release, destructive commands) is opt-in and policy-gated.

### Non-Goals (explicitly out of scope for v1.x)

- Replacing IDEs/editors or becoming a full CI/CD system
- Multi-user remote orchestration over the internet (local-first only)
- A custom agent runtime (NTM orchestrates existing agent CLIs)

### Risk Register (what can break the system)

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| External tool version drift (macros change, JSON schema shifts) | High | High | Tool Adapter layer with capability detection + schema guards |
| Daemon lifecycle flakiness (ports, orphaned processes) | Medium | High | Supervisor with PID files, health checks, restart policies |
| Partial failures in multi-step workflows | Medium | High | Transaction-like patterns with rollback, correlation IDs |
| Over-aggressive automation (auto-push, force-release) | Medium | Critical | Policy-gated, disabled by default, requires approval |
| Policy bypass (agents ignoring hooks, PATH issues) | Low | Critical | Provider-agnostic PATH wrappers, not just Claude hooks |
| State loss on crash | Medium | Medium | Durable state store (SQLite) + event log |
| Race conditions in file reservations | Low | Medium | Database-backed reservations with proper locking |

---

## Table of Contents

### Part I: Foundation
1. [Design Invariants, Non-Goals, and Risks](#design-invariants-non-goals-and-risks)
2. [What is NTM?](#what-is-ntm)
3. [The Dicklesworthstone Stack (Complete Ecosystem)](#the-dicklesworthstone-stack-complete-ecosystem)
4. [The Agentic Coding Flywheel](#the-agentic-coding-flywheel)
5. [Current Integration Status](#current-integration-status)

### Part II: CRITICAL - Tier 0 Integrations (Zero Usage, Maximum Impact)
6. [CRITICAL: Agent Mail Macros](#critical-agent-mail-macros)
7. [CRITICAL: File Reservation Lifecycle](#critical-file-reservation-lifecycle)
8. [CRITICAL: BV Mega-Commands](#critical-bv-mega-commands)
9. [CRITICAL: CM Server Mode](#critical-cm-server-mode)
10. [CRITICAL: Destructive Command Protection](#critical-destructive-command-protection)
11. [CRITICAL: Session Coordinator Intelligence](#critical-session-coordinator-intelligence)
12. [CRITICAL: BD Message Integration](#critical-bd-message-integration)
13. [CRITICAL: BD Daemon Mode](#critical-bd-daemon-mode)

### Part III: Underexplored Integrations (Tier 1)
14. [UNDEREXPLORED: bv Robot Modes (Detailed)](#underexplored-bv-beads-viewer-robot-modes)
15. [UNDEREXPLORED: CASS Historical Context Injection](#underexplored-cass-historical-context-injection)
16. [HIGH-LEVERAGE: Workspace Isolation via Git Worktrees](#high-leverage-workspace-isolation-via-git-worktrees)
17. [UNDEREXPLORED: s2p Context Preparation](#underexplored-s2p-source-to-prompt-context-preparation)
18. [UNDEREXPLORED: UBS Dashboard & Notifications](#underexplored-ubs-dashboard--agent-notifications)

### Part IV: Existing Planned Integrations (Tier 2-3)
19. [Existing: CAAM (Account Manager)](#existing-integration-caam-coding-agent-account-manager)
20. [Existing: CM (Memory System)](#existing-integration-cass-memory-system-cm)
21. [Existing: SLB (Safety Guardrails)](#existing-integration-slb-safety-guardrails)
22. [Existing: MCP Agent Mail](#existing-integration-mcp-agent-mail)

### Part V: Planning & Implementation
23. [Ecosystem Discovery](#ecosystem-discovery-additional-tools)
24. [Foundations: Durable State + Event Log](#foundations-durable-state--event-log)
25. [Priority Matrix](#priority-matrix)
26. [Implementation Roadmap](#implementation-roadmap)
27. [Success Metrics](#success-metrics)
28. [Test Strategy](#test-strategy-required-for-reliability)

---

## What is NTM?

### Overview

**NTM (Neural Terminal Manager)** is a Go-based command-line tool for orchestrating multiple AI coding agents in parallel within tmux sessions. It allows developers to:

- **Spawn** multiple AI agents (Claude, Codex, Gemini) in parallel tmux panes
- **Monitor** agent status (idle, working, error, waiting for input)
- **Coordinate** work distribution across agents
- **Track** context window usage and trigger rotations
- **Provide** robot-mode JSON output for programmatic consumption

### Core Capabilities

| Capability | Command | Description |
|-----------|---------|-------------|
| **Spawn sessions** | `ntm spawn myproject --cc=3 --cod=2` | Create tmux session with 3 Claude + 2 Codex agents |
| **List sessions** | `ntm list` | Show all active NTM sessions with agent counts |
| **Monitor status** | `ntm status myproject` | Real-time TUI showing all agent states |
| **Robot output** | `ntm --robot-status` | JSON output for programmatic integration |
| **Kill sessions** | `ntm kill myproject` | Terminate session and all agents |
| **Dashboard** | `ntm dashboard` | Web-based monitoring (planned) |

### Agent Types Supported

| Type | CLI | Provider | Strengths |
|------|-----|----------|-----------|
| `cc` | Claude Code | Anthropic | Analysis, architecture, complex refactoring |
| `cod` | Codex CLI | OpenAI | Fast implementations, bug fixes |
| `gmi` | Gemini CLI | Google | Documentation, research, multi-modal |

### Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                          NTM Control Plane                            │
├──────────────────────────────────────────────────────────────────────┤
│  State Store (SQLite)    Scheduler/Coordinator   Policy/Safety Gates  │
│  Event Log + Event Bus   Task/Reservation FSM    Audits + Approvals   │
├──────────────────────────────────────────────────────────────────────┤
│                            NTM API Surface                            │
│                CLI  |  TUI  |  Robot JSON  |  Web UI (ntm serve)      │
└──────────────────────────────────────────────────────────────────────┘
                  ▲                          ▲
                  │ (events)                 │ (commands)
                  │                          │
┌─────────────────┴──────────────────────────┴──────────────────────────┐
│                              Data Plane                                │
├──────────────────────────────────────────────────────────────────────┤
│ tmux adapter   Agent adapters    Tool adapters     Daemon supervisor   │
│ (panes)        (cc/cod/gmi)      (am/bv/bd/cm/...) (cm serve/bd daemon)│
└──────────────────────────────────────────────────────────────────────┘
```

**Key Architecture Principle**: Everything important is an **event** (spawned, assigned, reserved, blocked, completed…). UIs and automations consume the same event stream; no duplicated logic.

**Why Control Plane / Data Plane separation**:
- **Better testability**: Mock events + mock adapters
- **Better resilience**: Restart + replay events to rebuild state
- **Cleaner integration surfaces**: Each tool gets an adapter
- **Easier dashboard**: Subscribe to events, don't poll repeatedly

### Key Source Files

| File | Purpose |
|------|---------|
| `cmd/ntm/main.go` | CLI entry point, flag parsing |
| `internal/cli/` | Command implementations (spawn, list, kill, status) |
| `internal/robot/` | Robot mode JSON output generators |
| `internal/tmux/` | tmux session/pane management |
| `internal/status/` | Agent state detection (idle, working, error) |
| `internal/monitor/` | Real-time agent monitoring |
| `internal/context/` | Context window tracking |
| `internal/pipeline/` | Multi-stage pipeline execution |
| `internal/agentmail/` | Agent Mail client integration |
| `internal/tools/` | **NEW**: Tool adapter interfaces, capability detection, schema/version guards |
| `internal/supervisor/` | **NEW**: Daemon lifecycle manager (start/stop/health/restart/log capture) |
| `internal/events/` | **NEW**: Event bus + event log + subscriptions for CLI/TUI/web |
| `internal/state/` | **NEW**: Durable state store (sessions, agents, tasks, reservations, messages) |
| `internal/policy/` | **NEW**: Safety policy enforcement, approval workflows |

---

## The Dicklesworthstone Stack (Complete Ecosystem)

NTM is part of a larger ecosystem of coordinated tools designed for AI-assisted software development. Understanding this ecosystem is crucial for understanding the integration opportunities.

### Tool Overview

| Tool | Command | Language | LOC | Purpose | Integration Status |
|------|---------|----------|-----|---------|-------------------|
| **NTM** | `ntm` | Go | ~15K | Agent orchestration (this project) | N/A |
| **MCP Agent Mail** | `am` | Python | ~8K | Inter-agent messaging, file reservations | ⚠️ Basic (macros unused) |
| **UBS** | `ubs` | Python | ~12K | Static bug scanning (8 languages) | ✅ Via `internal/scanner/` |
| **Beads/bv** | `bd`, `bv` | Go | ~10K | Issue tracking with dependency graphs | ⚠️ Minimal (37/41 modes unused) |
| **CASS** | `cass` | Rust | ~50K | Session indexing across 11 agent types | ❌ None |
| **CASS Memory (CM)** | `cm` | Python | ~5K | Three-layer cognitive memory | ❌ None (server mode unused) |
| **CAAM** | `caam` | Python | ~3K | Account switching, rate limit failover | ❌ Planned |
| **SLB** | `slb` | Go | ~4K | Two-person rule for dangerous commands | ❌ Planned |
| **s2p** | `s2p` | TypeScript | ~3.5K | Source-to-prompt conversion | ❌ None |

### Integration Status Legend

| Symbol | Meaning | Action Required |
|--------|---------|-----------------|
| ✅ | Production integration | Maintain/enhance |
| ⚠️ | Partial/minimal usage | **Expand usage** |
| ❌ | No integration | **Implement** |

### Ecosystem Relationships

```
                    ┌─────────────────────────────────────┐
                    │           Human Developer           │
                    └────────────────┬────────────────────┘
                                     │
                    ┌────────────────▼────────────────────┐
                    │              NTM                     │
                    │   (Central Orchestration Layer)     │
                    │                                     │
                    │  MISSING: Macro usage, file locks,  │
                    │  daemon modes, mega-commands        │
                    └────────────────┬────────────────────┘
                                     │
       ┌─────────────┬───────────────┼───────────────┬─────────────┐
       │             │               │               │             │
       ▼             ▼               ▼               ▼             ▼
┌────────────┐ ┌──────────┐ ┌───────────────┐ ┌──────────┐ ┌────────────┐
│    CAAM    │ │   SLB    │ │  Agent Mail   │ │  bd/bv   │ │    CASS    │
│ (Accounts) │ │ (Safety) │ │ (Messaging)   │ │ (Tasks)  │ │ (History)  │
│            │ │          │ │               │ │          │ │            │
│ ❌ Unused  │ │ ❌ Unused│ │ ⚠️ Macros    │ │ ⚠️ 37    │ │ ❌ Unused  │
│            │ │          │ │    unused     │ │  modes   │ │            │
│            │ │          │ │               │ │  unused  │ │            │
└─────┬──────┘ └────┬─────┘ └───────┬───────┘ └────┬─────┘ └─────┬──────┘
      │             │               │              │             │
      └─────────────┴───────────────┼──────────────┴─────────────┘
                                    │
                    ┌───────────────▼───────────────┐
                    │         AI Agents             │
                    │  Claude | Codex | Gemini      │
                    └───────────────┬───────────────┘
                                    │
       ┌────────────────────────────┼────────────────────────────┐
       │                            │                            │
       ▼                            ▼                            ▼
┌────────────────┐         ┌───────────────┐          ┌─────────────────┐
│      UBS       │         │      CM       │          │       s2p       │
│ (Bug Scanning) │         │   (Memory)    │          │ (Context Prep)  │
│                │         │               │          │                 │
│ ✅ Integrated  │         │ ❌ Server     │          │ ❌ Unused       │
│                │         │    mode unused│          │                 │
└────────────────┘         └───────────────┘          └─────────────────┘
```

---

## The Agentic Coding Flywheel

The tools form a closed-loop learning system where each cycle compounds:

```
                    ┌────────────────────────────────────────┐
                    │                                        │
    ┌───────────────▼───────────────┐                        │
    │        PLAN (Beads/bv)        │                        │
    │   - Ready work queue          │                        │
    │   - Dependency graph          │                        │
    │   - Priority scoring          │                        │
    │   - Execution track planning  │ ◀── CRITICAL: Use      │
    │   - Graph-based prioritization│     -robot-triage      │
    └───────────────┬───────────────┘                        │
                    │                                        │
    ┌───────────────▼───────────────┐                        │
    │    COORDINATE (Agent Mail)    │                        │
    │   - File reservations         │ ◀── CRITICAL: Use      │
    │   - Message routing           │     macros + lifecycle │
    │   - Thread tracking           │                        │
    └───────────────┬───────────────┘                        │
                    │                                        │
    ┌───────────────▼───────────────┐                        │
    │      EXECUTE (NTM + Agents)   │ ◀── SAFETY (SLB)       │
    │   - Multi-agent sessions      │     Two-person rule    │
    │   - Account rotation (CAAM)   │     for dangerous ops  │
    │   - Parallel task dispatch    │                        │
    │   - Context preparation (s2p) │ ◀── CRITICAL: Use      │
    │   - Historical context (CASS) │     cm serve daemon    │
    │   - Destructive cmd protection│ ◀── CRITICAL: Auto-    │
    └───────────────┬───────────────┘     install hooks      │
                    │                                        │
    ┌───────────────▼───────────────┐                        │
    │         SCAN (UBS)            │                        │
    │   - Static analysis           │                        │
    │   - Bug detection             │                        │
    │   - Pre-commit checks         │                        │
    │   - Agent notifications       │                        │
    └───────────────┬───────────────┘                        │
                    │                                        │
    ┌───────────────▼───────────────┐                        │
    │    REMEMBER (CASS + CM)       │                        │
    │   - Session indexing          │                        │
    │   - Rule extraction           │                        │
    │   - Confidence scoring        │ ◀── CRITICAL: Use      │
    │   - Feedback loop (cm outcome)│     cm outcome         │
    └───────────────┴────────────────────────────────────────┘
```

---

## Current Integration Status

### Integration Maturity Levels (Updated)

| Integration | Status | Maturity | Gap Analysis |
|-------------|--------|----------|--------------|
| **Agent Mail Macros** | ❌ **CRITICAL** | Zero | 4 macros completely unused |
| **File Reservation Lifecycle** | ❌ **CRITICAL** | Zero | No reserve/release/force-release |
| **BV Mega-Commands** | ❌ **CRITICAL** | Zero | 37/41 robot modes unused |
| **CM Server Mode** | ❌ **CRITICAL** | Zero | HTTP daemon not used |
| **Destructive Cmd Protection** | ❌ **CRITICAL** | Zero | No auto-install of hooks |
| **Session Coordinator** | ❌ **CRITICAL** | Zero | Intelligence layer missing |
| **BD Message Integration** | ❌ **CRITICAL** | Zero | bd message commands unused |
| **BD Daemon Mode** | ❌ **CRITICAL** | Zero | Background sync not used |
| **BV -robot-markdown** | ❌ **CRITICAL** | Zero | Large token waste for smaller-context agents |
| **UBS** | ✅ Implemented | Production | Dashboard/notifications missing |
| **bv (basic)** | ⚠️ Minimal | PoC | Only 4 of 41 robot modes used |
| **Agent Mail (basic)** | ⚠️ Minimal | PoC | Macros, reservations unused |
| **CAAM** | ❌ Planned | Design | Rate limit failover missing |
| **CM (basic)** | ❌ Planned | Design | Memory injection missing |
| **SLB** | ❌ Planned | Design | Safety gates missing |
| **CASS** | ❌ None | Gap | Historical context missing |
| **s2p** | ❌ None | Gap | Context preparation missing |

### The Gap: Current State vs Target State

```
┌─────────────────────────────────────────────────────────────────┐
│                   CURRENT STATE                                   │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  NTM spawns agents → Agents work → NTM monitors status          │
│                                                                 │
│  CRITICAL Problems (Tier 0):                                    │
│  ❌ Agent Mail macros unused (4-5 calls instead of 1)           │
│  ❌ No file reservations (agents can edit same file)            │
│  ❌ Only 4/41 bv modes used (missing -robot-triage mega-cmd)    │
│  ❌ CM subprocess calls (no HTTP daemon)                        │
│  ❌ No destructive command protection (git checkout -- risk)    │
│  ❌ Session coordinator is passive (no intelligence)            │
│  ❌ BD messaging unused (coordination gap)                      │
│  ❌ Manual bd sync (no background daemon)                       │
│                                                                 │
│  Additional Problems (Tier 1-2):                                │
│  ❌ No smart task distribution                                  │
│  ❌ No historical context from CASS                             │
│  ❌ No token budget management via s2p                          │
│  ❌ No rate limit failover via CAAM                             │
│  ❌ No safety gates via SLB                                     │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│                   TARGET STATE                                   │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  NTM spawns agents with:                                         │
│  ✅ One-call bootstrap (macro_start_session)                    │
│  ✅ File reservations before work assignment                    │
│  ✅ Single -robot-triage call for complete work analysis        │
│  ✅ CM HTTP daemon for fast memory queries                      │
│  ✅ Auto-installed destructive command hooks                    │
│  ✅ Intelligent session coordinator                             │
│  ✅ BD messaging for agent-to-agent coordination                │
│  ✅ Background BD daemon for continuous sync                    │
│  ✅ Smart task assignment (bv graph analysis)                   │
│  ✅ Historical context (CASS search)                            │
│  ✅ Token budgets (s2p)                                         │
│  ✅ Automatic failover (CAAM)                                   │
│  ✅ Safety gates (SLB)                                          │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

---

# Part II: CRITICAL - Tier 0 Integrations

These integrations have **zero current usage** despite being designed specifically for agent coordination. They represent the highest-impact improvements available.

---

## CRITICAL: Agent Mail Macros

### The Problem

NTM currently makes **4-5 separate API calls** to set up each agent:

```go
// CURRENT: Multiple error-prone calls
err := ensureProject(ctx, projectKey)
if err != nil { return err }

agent, err := registerAgent(ctx, projectKey, program, model)
if err != nil { return err }

reservations, err := reservePaths(ctx, projectKey, agent.Name, paths)
if err != nil { return err }

inbox, err := fetchInbox(ctx, projectKey, agent.Name)
if err != nil { return err }
```

### The Solution: macro_start_session (with capability-gated fallback)

Agent Mail provides a **one-call macro** that does everything:

```go
// NEW: Single call does everything
result, err := macroStartSession(ctx, MacroStartSessionOptions{
    HumanKey:              projectKey,  // Absolute path to project
    Program:               "claude-code",
    Model:                 "opus-4.5",
    TaskDescription:       "Implementing auth module",
    FileReservationPaths:  []string{"internal/auth/**/*.go"},
    FileReservationTTL:    3600,  // 1 hour
    InboxLimit:            10,
})
// Returns: project + agent + reservations + inbox in one response
```

**Fallback Policy**:
- If Agent Mail does not support the macro (older version), NTM automatically falls back to the 4–5 legacy calls.
- Tool version + capability detection is performed once per session and cached.
- This ensures graceful degradation (Design Invariant #2).

### All Four Macros

| Macro | Purpose | Current Usage |
|-------|---------|---------------|
| `macro_start_session` | Bootstrap: register + reserve + inbox | ❌ None |
| `macro_prepare_thread` | Align agent with existing thread + LLM summary | ❌ None |
| `macro_file_reservation_cycle` | Reserve → work → auto-release | ❌ None |
| `macro_contact_handshake` | Establish inter-agent messaging permission | ❌ None |

### Integration 1: One-Call Agent Bootstrap

```go
// internal/agentmail/macros.go - NEW FILE

type MacroStartSessionOptions struct {
    HumanKey              string   `json:"human_key"`
    Program               string   `json:"program"`
    Model                 string   `json:"model"`
    AgentName             string   `json:"agent_name,omitempty"` // Auto-generated if empty
    TaskDescription       string   `json:"task_description"`
    FileReservationPaths  []string `json:"file_reservation_paths,omitempty"`
    FileReservationTTL    int      `json:"file_reservation_ttl_seconds"`
    FileReservationReason string   `json:"file_reservation_reason"`
    InboxLimit            int      `json:"inbox_limit"`
}

type MacroStartSessionResult struct {
    Project      ProjectInfo      `json:"project"`
    Agent        AgentInfo        `json:"agent"`
    Reservations ReservationInfo  `json:"file_reservations"`
    Inbox        []InboxMessage   `json:"inbox"`
}

// StartSession uses the macro_start_session MCP tool
func (c *Client) StartSession(ctx context.Context, opts MacroStartSessionOptions) (*MacroStartSessionResult, error) {
    args := map[string]interface{}{
        "human_key":                  opts.HumanKey,
        "program":                    opts.Program,
        "model":                      opts.Model,
        "task_description":           opts.TaskDescription,
        "inbox_limit":                opts.InboxLimit,
    }

    if opts.AgentName != "" {
        args["agent_name"] = opts.AgentName
    }

    if len(opts.FileReservationPaths) > 0 {
        args["file_reservation_paths"] = opts.FileReservationPaths
        args["file_reservation_ttl_seconds"] = opts.FileReservationTTL
        args["file_reservation_reason"] = opts.FileReservationReason
    }

    result, err := c.callToolWithTimeout(ctx, "macro_start_session", args, LongTimeout)
    if err != nil {
        return nil, fmt.Errorf("macro_start_session failed: %w", err)
    }

    var startResult MacroStartSessionResult
    if err := json.Unmarshal(result, &startResult); err != nil {
        return nil, err
    }
    return &startResult, nil
}
```

### Integration 2: Thread Continuation

When spawning a new agent to continue existing work:

```go
// internal/agentmail/macros.go

type MacroPrepareThreadOptions struct {
    ProjectKey      string `json:"project_key"`
    ThreadID        string `json:"thread_id"`       // e.g., "FEAT-123"
    Program         string `json:"program"`
    Model           string `json:"model"`
    AgentName       string `json:"agent_name,omitempty"`
    TaskDescription string `json:"task_description"`
    IncludeExamples bool   `json:"include_examples"` // Include sample messages
    LLMMode         bool   `json:"llm_mode"`         // Use LLM to refine summary
    InboxLimit      int    `json:"inbox_limit"`
}

type MacroPrepareThreadResult struct {
    Agent         AgentInfo     `json:"agent"`
    ThreadSummary ThreadSummary `json:"thread_summary"`
    Inbox         []InboxMessage `json:"inbox"`
}

// PrepareThread aligns an agent with an existing conversation thread
func (c *Client) PrepareThread(ctx context.Context, opts MacroPrepareThreadOptions) (*MacroPrepareThreadResult, error) {
    args := map[string]interface{}{
        "project_key":       opts.ProjectKey,
        "thread_id":         opts.ThreadID,
        "program":           opts.Program,
        "model":             opts.Model,
        "task_description":  opts.TaskDescription,
        "include_examples":  opts.IncludeExamples,
        "llm_mode":          opts.LLMMode,
        "inbox_limit":       opts.InboxLimit,
    }

    if opts.AgentName != "" {
        args["agent_name"] = opts.AgentName
    }

    result, err := c.callToolWithTimeout(ctx, "macro_prepare_thread", args, LongTimeout)
    if err != nil {
        return nil, fmt.Errorf("macro_prepare_thread failed: %w", err)
    }

    var prepareResult MacroPrepareThreadResult
    if err := json.Unmarshal(result, &prepareResult); err != nil {
        return nil, err
    }
    return &prepareResult, nil
}
```

### Integration 3: Contact Handshake for Cross-Project Coordination

```go
// internal/agentmail/macros.go

type MacroContactHandshakeOptions struct {
    ProjectKey     string `json:"project_key"`
    AgentName      string `json:"agent_name,omitempty"`
    Target         string `json:"target"`          // Target agent name
    ToProject      string `json:"to_project,omitempty"` // For cross-project
    Reason         string `json:"reason"`
    AutoAccept     bool   `json:"auto_accept"`
    WelcomeSubject string `json:"welcome_subject,omitempty"`
    WelcomeBody    string `json:"welcome_body,omitempty"`
    TTLSeconds     int    `json:"ttl_seconds"`
}

// ContactHandshake establishes inter-agent messaging permission
func (c *Client) ContactHandshake(ctx context.Context, opts MacroContactHandshakeOptions) error {
    args := map[string]interface{}{
        "project_key":     opts.ProjectKey,
        "target":          opts.Target,
        "reason":          opts.Reason,
        "auto_accept":     opts.AutoAccept,
        "ttl_seconds":     opts.TTLSeconds,
    }

    if opts.AgentName != "" {
        args["agent_name"] = opts.AgentName
    }
    if opts.ToProject != "" {
        args["to_project"] = opts.ToProject
    }
    if opts.WelcomeSubject != "" {
        args["welcome_subject"] = opts.WelcomeSubject
        args["welcome_body"] = opts.WelcomeBody
    }

    _, err := c.callToolWithTimeout(ctx, "macro_contact_handshake", args, DefaultTimeout)
    return err
}
```

### Updated Spawn Workflow

```go
// internal/cli/spawn.go - UPDATED

func spawnAgentWithMacro(ctx context.Context, session string, agentType, model string, files []string) (*AgentInfo, error) {
    projectPath, _ := os.Getwd()

    // ONE CALL does everything
    result, err := agentmail.StartSession(ctx, agentmail.MacroStartSessionOptions{
        HumanKey:              projectPath,
        Program:               agentTypeToProgram(agentType), // "claude-code", "codex-cli", etc.
        Model:                 model,
        TaskDescription:       fmt.Sprintf("Agent in session %s", session),
        FileReservationPaths:  files,
        FileReservationTTL:    3600,
        FileReservationReason: fmt.Sprintf("Working in NTM session %s", session),
        InboxLimit:            5,
    })
    if err != nil {
        return nil, err
    }

    // Check for reservation conflicts
    if len(result.Reservations.Conflicts) > 0 {
        log.Printf("Warning: File conflicts detected: %v", result.Reservations.Conflicts)
        // Could route to different files or wait
    }

    // Check inbox for pending messages
    if len(result.Inbox) > 0 {
        log.Printf("Agent %s has %d pending messages", result.Agent.Name, len(result.Inbox))
    }

    return &result.Agent, nil
}
```

### New NTM Commands

```bash
# Spawn with macro (default)
ntm spawn myproject --cc=2 --reserve="internal/**/*.go"

# Spawn to continue existing thread
ntm spawn myproject --cc=1 --thread=FEAT-123

# Cross-project agent coordination
ntm contact myproject/GreenLake other-project/BlueDog --reason="Need review help"

# Project onboarding & diagnostics
ntm init                 # Sets up .ntm/, policy defaults, wrappers, optional hooks
ntm doctor               # Validates tools, versions, daemons, capabilities, tmux health

# Local API server for dashboard + robot endpoints
ntm serve --port 7337    # Starts HTTP server with WebSocket event streaming

# Config profiles
ntm config show
ntm config set scheduler.preferCriticalPath=true
ntm config set safety.autoInstallWrappers=true
```

---

## CRITICAL: File Reservation Lifecycle

### The Problem

NTM spawns multiple agents on the same codebase with **no file coordination**:

```
Agent 1: Editing internal/auth/login.go
Agent 2: Also editing internal/auth/login.go  ← CONFLICT!
Result: Merge conflicts, lost work, frustrated developers
```

### The Solution: Reserve → Work → Release Pattern

Agent Mail provides advisory file locks that NTM completely ignores:

```
┌─────────────────────────────────────────────────────────────────┐
│                 File Reservation Lifecycle                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  1. RESERVE (before assigning work)                              │
│     ┌─────────────────────────────────────────────────────────┐ │
│     │ reservePaths(project, agent, ["auth/**/*.go"], 3600)    │ │
│     │                                                          │ │
│     │ Returns: { granted: [...], conflicts: [...] }            │ │
│     └─────────────────────────────────────────────────────────┘ │
│                           │                                      │
│              ┌────────────┴────────────┐                         │
│              │                         │                         │
│              ▼                         ▼                         │
│        No Conflicts              Has Conflicts                   │
│              │                         │                         │
│              ▼                         ▼                         │
│     Assign work to agent    Route to different files OR wait     │
│              │                                                   │
│              ▼                                                   │
│  2. WORK (agent edits files)                                     │
│     ┌─────────────────────────────────────────────────────────┐ │
│     │ Agent makes changes with confidence that no other        │ │
│     │ agent will interfere with the same files                 │ │
│     └─────────────────────────────────────────────────────────┘ │
│              │                                                   │
│              ▼                                                   │
│  3. RENEW (heartbeat while work is active)                       │
│     ┌─────────────────────────────────────────────────────────┐ │
│     │ renewReservations(project, agent, reservationIds, ttl)   │ │
│     │ - Called periodically while agent is working             │ │
│     │ - Extends TTL without re-acquiring                       │ │
│     │ - Correlation ID links to task                           │ │
│     └─────────────────────────────────────────────────────────┘ │
│              │                                                   │
│              ▼                                                   │
│  4. RELEASE (after work complete)                                │
│     ┌─────────────────────────────────────────────────────────┐ │
│     │ releaseReservations(project, agent)                      │ │
│     │ - Task-scoped: release only what this task acquired      │ │
│     └─────────────────────────────────────────────────────────┘ │
│              │                                                   │
│              ▼                                                   │
│  5. FORCE-RELEASE (if agent crashes; policy-gated)               │
│     ┌─────────────────────────────────────────────────────────┐ │
│     │ forceReleaseReservation(project, admin, reservationId)  │ │
│     │ - Validates agent is inactive                            │ │
│     │ - Notifies previous holder                               │ │
│     └─────────────────────────────────────────────────────────┘ │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Integration 1: Reserve Before Assignment

```go
// internal/robot/assign.go - UPDATED

func assignWorkWithReservations(ctx context.Context, session string, agent AgentInfo, bead BeadPreview) (*AssignResult, error) {
    projectPath, _ := os.Getwd()

    // 1. Determine files that will be affected
    filesToReserve := predictAffectedFiles(bead)

    // 2. Attempt to reserve files
    reservations, err := agentmail.ReservePaths(ctx, agentmail.FileReservationOptions{
        ProjectKey: projectPath,
        AgentName:  agent.Name,
        Paths:      filesToReserve,
        TTLSeconds: 3600,  // 1 hour
        Exclusive:  true,
        Reason:     fmt.Sprintf("Working on %s: %s", bead.ID, bead.Title),
    })
    if err != nil {
        return nil, fmt.Errorf("failed to reserve files: %w", err)
    }

    // 3. Handle conflicts
    if len(reservations.Conflicts) > 0 {
        // Option A: Find alternative work
        alternativeWork := findNonConflictingWork(bead, reservations.Conflicts)
        if alternativeWork != nil {
            return assignWorkWithReservations(ctx, session, agent, *alternativeWork)
        }

        // Option B: Wait for release
        return &AssignResult{
            Status:    "blocked",
            Conflicts: reservations.Conflicts,
            Message:   fmt.Sprintf("Files held by: %v", getHolders(reservations.Conflicts)),
        }, nil
    }

    // 4. Assign work
    return &AssignResult{
        Status:       "assigned",
        Agent:        agent,
        Bead:         bead,
        Reservations: reservations.Granted,
    }, nil
}

// predictAffectedFiles uses bead metadata and bv analysis to predict which files will be touched
func predictAffectedFiles(bead BeadPreview) []string {
    // Use bv --robot-impact if available
    out, err := exec.Command("bv", "-robot-impact", bead.ID, "--json").Output()
    if err == nil {
        var impact struct {
            Files []string `json:"affected_files"`
        }
        json.Unmarshal(out, &impact)
        if len(impact.Files) > 0 {
            return impact.Files
        }
    }

    // Fallback: use glob patterns from bead labels
    patterns := []string{}
    for _, label := range bead.Labels {
        if pattern, ok := labelToFilePattern[label]; ok {
            patterns = append(patterns, pattern)
        }
    }

    if len(patterns) == 0 {
        // Default: reserve nothing (no conflicts, but no protection)
        return nil
    }

    return patterns
}

var labelToFilePattern = map[string]string{
    "auth":       "internal/auth/**/*.go",
    "api":        "internal/api/**/*.go",
    "frontend":   "web/**/*.tsx",
    "database":   "internal/db/**/*.go",
    "tests":      "**/*_test.go",
}
```

### Integration 2: Release After Completion

```go
// internal/monitor/completion.go - NEW FILE

// OnTaskComplete is called when an agent completes a task
func OnTaskComplete(ctx context.Context, session, agentName string) error {
    projectPath, _ := os.Getwd()

    // Release all reservations held by this agent
    result, err := agentmail.ReleaseReservations(ctx, projectPath, agentName, nil, nil)
    if err != nil {
        log.Printf("Warning: Failed to release reservations for %s: %v", agentName, err)
        return err
    }

    log.Printf("Released %d reservations for agent %s", result.Released, agentName)
    return nil
}

// OnSessionEnd releases all reservations for all agents in session
func OnSessionEnd(ctx context.Context, session string) error {
    projectPath, _ := os.Getwd()

    // Get all agents in session
    agents := getSessionAgents(session)

    for _, agent := range agents {
        if err := OnTaskComplete(ctx, session, agent.Name); err != nil {
            log.Printf("Warning: Failed to release for %s: %v", agent.Name, err)
        }
    }

    return nil
}
```

### Integration 3: Force-Release Stale Reservations (with approvals)

**Revision**:
- Force-release is **approval_required** by default (policy-gated).
- The coordinator must record:
  - Why it believes the agent is inactive
  - Which task/reservation it belongs to (correlation_id)
  - Who approved the force-release (human or SLB)

```go
// internal/monitor/stale.go - NEW FILE

type StaleReservationMonitor struct {
    session       string
    checkInterval time.Duration
    staleTimeout  time.Duration
    policyChecker *policy.Checker
    eventLog      *events.Log
}

func NewStaleReservationMonitor(session string, policyChecker *policy.Checker, eventLog *events.Log) *StaleReservationMonitor {
    return &StaleReservationMonitor{
        session:       session,
        checkInterval: 5 * time.Minute,
        staleTimeout:  30 * time.Minute,
        policyChecker: policyChecker,
        eventLog:      eventLog,
    }
}

func (m *StaleReservationMonitor) Start(ctx context.Context) {
    ticker := time.NewTicker(m.checkInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            m.checkForStaleReservations(ctx)
        }
    }
}

func (m *StaleReservationMonitor) checkForStaleReservations(ctx context.Context) {
    projectPath, _ := os.Getwd()

    // Get all reservations in project
    reservations, err := agentmail.ListReservations(ctx, projectPath, "", true)
    if err != nil {
        log.Printf("Failed to list reservations: %v", err)
        return
    }

    for _, res := range reservations {
        // Check if agent is still active
        agent, err := agentmail.Whois(ctx, projectPath, res.AgentName, true)
        if err != nil {
            continue
        }

        inactiveFor := time.Since(agent.LastActiveTS)

        if inactiveFor > m.staleTimeout {
            // Check policy before force-releasing (approval_required by default)
            approval, err := m.policyChecker.CheckApproval(ctx, policy.ApprovalRequest{
                Action:        "force_release",
                Resource:      fmt.Sprintf("reservation:%d", res.ID),
                Reason:        fmt.Sprintf("Agent %s inactive for %v", res.AgentName, inactiveFor),
                CorrelationID: res.CorrelationID,
            })
            if err != nil {
                log.Printf("Policy check failed for reservation %d: %v", res.ID, err)
                continue
            }

            if !approval.Granted {
                // Log pending approval request for human review
                m.eventLog.Append(events.Event{
                    Type: "approval.pending",
                    Data: map[string]interface{}{
                        "action":         "force_release",
                        "reservation_id": res.ID,
                        "agent":          res.AgentName,
                        "inactive_for":   inactiveFor.String(),
                        "reason":         approval.DenialReason,
                    },
                })
                log.Printf("Force-release of reservation %d requires approval (run: ntm approve %s)",
                    res.ID, approval.ApprovalToken)
                continue
            }

            log.Printf("Agent %s inactive for %v, force-releasing reservation %d (approved by: %s)",
                res.AgentName, inactiveFor, res.ID, approval.ApprovedBy)

            // Force release with audit trail
            err = agentmail.ForceReleaseReservation(ctx, agentmail.ForceReleaseOptions{
                ProjectKey:     projectPath,
                AgentName:      "NTM-Coordinator",
                ReservationID:  res.ID,
                NotifyPrevious: true,
                Note:           fmt.Sprintf("Auto-released: agent inactive for %v (approved by: %s)", inactiveFor, approval.ApprovedBy),
            })
            if err != nil {
                log.Printf("Failed to force-release: %v", err)
                continue
            }

            // Log successful force-release
            m.eventLog.Append(events.Event{
                Type: "reservation.force_released",
                Data: map[string]interface{}{
                    "reservation_id": res.ID,
                    "agent":          res.AgentName,
                    "approved_by":    approval.ApprovedBy,
                    "correlation_id": res.CorrelationID,
                },
            })
        }
    }
}
```

### Integration 4: Pre-Commit Guards

```go
// internal/hooks/precommit.go - NEW FILE

// InstallPrecommitGuard installs the Agent Mail pre-commit hook in a repository
func InstallPrecommitGuard(ctx context.Context, projectPath, repoPath string) error {
    return agentmail.InstallPrecommitGuard(ctx, projectPath, repoPath)
}

// UninstallPrecommitGuard removes the pre-commit hook
func UninstallPrecommitGuard(ctx context.Context, repoPath string) error {
    return agentmail.UninstallPrecommitGuard(ctx, repoPath)
}

// AutoInstallGuards installs guards during session spawn
func AutoInstallGuards(ctx context.Context, session string) error {
    projectPath, _ := os.Getwd()

    // Find all git repos in project
    repos := findGitRepos(projectPath)

    for _, repo := range repos {
        if err := InstallPrecommitGuard(ctx, projectPath, repo); err != nil {
            log.Printf("Warning: Failed to install guard in %s: %v", repo, err)
        } else {
            log.Printf("Installed pre-commit guard in %s", repo)
        }
    }

    return nil
}
```

### New NTM Commands

```bash
# Reserve files manually
ntm reserve "internal/auth/**/*.go" --agent=GreenLake --ttl=1h

# Release files
ntm release --agent=GreenLake
ntm release --all  # Release all in session

# List reservations
ntm reservations list
ntm reservations list --all-projects

# Force release stale
ntm reservations force-release <id> --reason="Agent crashed"

# Install pre-commit guards
ntm guards install
ntm guards uninstall
ntm guards status
```

---

## CRITICAL: BV Mega-Commands

### The Problem

NTM currently calls **4 separate bv commands** to get work information:

```go
// CURRENT: 4 separate calls
insights := exec.Command("bv", "-robot-insights", "--json")
priority := exec.Command("bv", "-robot-priority", "--json")
plan := exec.Command("bv", "-robot-plan", "--json")
recipes := exec.Command("bv", "-robot-recipes", "--json")
```

### The Solution: -robot-triage

BV provides a **single mega-command** that returns everything:

```go
// NEW: 1 call returns everything
triage := exec.Command("bv", "-robot-triage", "--json")
// Returns: insights + priority + plan + recipes + alerts + more
```

### All BV Robot Modes (41 Total)

| Category | Mode | Purpose | Usage |
|----------|------|---------|-------|
| **Mega-Commands** | `-robot-triage` | **All-in-one** (replaces 4 calls) | ❌ Unused |
| | `-robot-triage-by-label` | Grouped by label | ❌ Unused |
| | `-robot-triage-by-track` | Grouped by execution track | ❌ Unused |
| **Currently Used** | `-robot-insights` | Graph metrics | ✅ Used |
| | `-robot-priority` | Priority ranking | ✅ Used |
| | `-robot-plan` | Execution plan | ✅ Used |
| | `-robot-recipes` | Workflow recipes | ✅ Used |
| **Analysis** | `-robot-alerts` | Proactive issue detection | ❌ Unused |
| | `-robot-graph` | Dependency graph (JSON/DOT/Mermaid) | ❌ Unused |
| | `-robot-forecast` | ETA predictions | ❌ Unused |
| | `-robot-causality` | Causal chain analysis | ❌ Unused |
| | `-robot-impact` | File impact analysis | ❌ Unused |
| | `-robot-suggest` | Smart suggestions | ❌ Unused |
| | `-robot-search` | Semantic vector search | ❌ Unused |
| | `-robot-capacity` | Team capacity simulation | ❌ Unused |
| **Efficiency** | `-robot-markdown` | **50% token savings** | ❌ Unused |
| | `-robot-next` | Single top recommendation | ❌ Unused |
| **Correlation** | `-robot-history` | Commit correlations | ❌ Unused |
| | `-robot-orphans` | Orphan commits | ❌ Unused |
| | `-robot-correlation-stats` | Correlation feedback | ❌ Unused |
| **Labels** | `-robot-label-attention` | Label priority | ❌ Unused |
| | `-robot-label-flow` | Cross-label dependencies | ❌ Unused |
| | `-robot-label-health` | Label health metrics | ❌ Unused |
| **Files** | `-robot-file-beads` | File-to-bead mapping | ❌ Unused |
| | `-robot-file-hotspots` | Frequently changed files | ❌ Unused |
| | `-robot-file-relations` | Files that change together | ❌ Unused |
| **Network** | `-robot-related` | Related issues | ❌ Unused |
| | `-robot-blocker-chain` | Transitive blockers | ❌ Unused |
| **Baseline** | `-robot-drift` | Baseline drift detection | ❌ Unused |
| | `-check-drift` | Drift check with exit codes | ❌ Unused |
| **Sprints** | `-robot-sprint-list` | Available sprints | ❌ Unused |
| | `-robot-sprint-show` | Sprint details | ❌ Unused |

### Integration 1: Replace 4 Calls with 1

```go
// internal/bv/triage.go - NEW FILE

type TriageResult struct {
    // From -robot-insights
    Insights struct {
        PageRank    map[string]float64 `json:"pagerank"`
        Betweenness map[string]float64 `json:"betweenness"`
        InDegree    map[string]int     `json:"in_degree"`
        KCore       map[string]int     `json:"k_core"`
    } `json:"insights"`

    // From -robot-priority
    Priority []struct {
        ID       string  `json:"id"`
        Title    string  `json:"title"`
        Score    float64 `json:"score"`
        Reason   string  `json:"reason"`
    } `json:"priority"`

    // From -robot-plan
    Plan struct {
        Tracks      []ExecutionTrack `json:"tracks"`
        CritPath    []string         `json:"critical_path"`
        Parallelism int              `json:"max_parallelism"`
    } `json:"plan"`

    // From -robot-alerts
    Alerts []struct {
        Type     string `json:"type"`
        Severity string `json:"severity"`
        Message  string `json:"message"`
        BeadID   string `json:"bead_id,omitempty"`
    } `json:"alerts"`

    // From -robot-suggest
    Suggestions []struct {
        Type       string  `json:"type"`
        FromID     string  `json:"from_id"`
        ToID       string  `json:"to_id"`
        Confidence float64 `json:"confidence"`
        Reason     string  `json:"reason"`
    } `json:"suggestions"`
}

// Caching layer for bv triage results
var (
    triageCache     *TriageResult
    triageCacheTime time.Time
    triageCacheTTL  = 30 * time.Second  // Don't call bv every tick
    triageCacheMu   sync.Mutex
)

// GetTriage fetches complete triage data in one call (with caching)
func GetTriage(ctx context.Context) (*TriageResult, error) {
    triageCacheMu.Lock()
    defer triageCacheMu.Unlock()

    // Return cached result if fresh
    if triageCache != nil && time.Since(triageCacheTime) < triageCacheTTL {
        return triageCache, nil
    }

    cmd := exec.CommandContext(ctx, "bv", "-robot-triage", "--json")
    out, err := cmd.Output()
    if err != nil {
        return nil, fmt.Errorf("bv -robot-triage failed: %w", err)
    }

    var result TriageResult
    if err := json.Unmarshal(out, &result); err != nil {
        return nil, err
    }

    // Cache the result
    triageCache = &result
    triageCacheTime = time.Now()

    return &result, nil
}

// InvalidateTriageCache should be called on bd sync events
func InvalidateTriageCache() {
    triageCacheMu.Lock()
    triageCache = nil
    triageCacheMu.Unlock()
}

// GetTriageByLabel groups work by label for specialized assignment
func GetTriageByLabel(ctx context.Context) (map[string][]BeadPreview, error) {
    cmd := exec.CommandContext(ctx, "bv", "-robot-triage-by-label", "--json")
    out, err := cmd.Output()
    if err != nil {
        return nil, err
    }

    var result map[string][]BeadPreview
    if err := json.Unmarshal(out, &result); err != nil {
        return nil, err
    }
    return result, nil
}

// GetTriageByTrack groups work by execution track
func GetTriageByTrack(ctx context.Context) ([]ExecutionTrack, error) {
    cmd := exec.CommandContext(ctx, "bv", "-robot-triage-by-track", "--json")
    out, err := cmd.Output()
    if err != nil {
        return nil, err
    }

    var result []ExecutionTrack
    if err := json.Unmarshal(out, &result); err != nil {
        return nil, err
    }
    return result, nil
}
```

### Integration 2: Proactive Alert Monitoring

```go
// internal/monitor/alerts.go - NEW FILE

type AlertMonitor struct {
    session       string
    checkInterval time.Duration
}

func (m *AlertMonitor) Start(ctx context.Context) {
    ticker := time.NewTicker(m.checkInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            m.checkAlerts(ctx)
        }
    }
}

func (m *AlertMonitor) checkAlerts(ctx context.Context) {
    cmd := exec.CommandContext(ctx, "bv", "-robot-alerts", "--severity", "critical", "--json")
    out, err := cmd.Output()
    if err != nil {
        return
    }

    var alerts []struct {
        Type     string `json:"type"`
        Severity string `json:"severity"`
        Message  string `json:"message"`
        BeadID   string `json:"bead_id,omitempty"`
    }

    if err := json.Unmarshal(out, &alerts); err != nil {
        return
    }

    for _, alert := range alerts {
        switch alert.Type {
        case "cycle":
            // Dependency cycle detected - urgent!
            log.Printf("CRITICAL: Dependency cycle detected: %s", alert.Message)
            notifyAllAgents(m.session, fmt.Sprintf("⚠️ CYCLE DETECTED: %s", alert.Message))

        case "stale":
            // Stale issues
            log.Printf("Warning: Stale issues detected: %s", alert.Message)

        case "orphan":
            // Orphan commits
            log.Printf("Info: Orphan commits detected: %s", alert.Message)
        }
    }
}
```

### Integration 3: Token-Efficient Markdown Output

```go
// internal/bv/markdown.go - NEW FILE

// GetTriageMarkdown returns triage data in markdown format (50% smaller than JSON)
func GetTriageMarkdown(ctx context.Context, compact bool) (string, error) {
    args := []string{"-robot-markdown"}
    if compact {
        args = append(args, "--md-compact")
    }

    cmd := exec.CommandContext(ctx, "bv", args...)
    out, err := cmd.Output()
    if err != nil {
        return "", err
    }

    return string(out), nil
}

// Use markdown for context-limited scenarios
func getAgentContext(agentType string) (string, error) {
    // Claude has large context - use JSON
    if agentType == "claude" {
        triage, _ := GetTriage(context.Background())
        data, _ := json.Marshal(triage)
        return string(data), nil
    }

    // Codex/Gemini - use markdown to save tokens
    return GetTriageMarkdown(context.Background(), true)
}
```

### Integration 4: Semantic Search

```go
// internal/bv/search.go - NEW FILE

type SearchResult struct {
    ID        string  `json:"id"`
    Title     string  `json:"title"`
    Score     float64 `json:"score"`
    Snippet   string  `json:"snippet"`
}

// SemanticSearch finds issues by natural language query
func SemanticSearch(ctx context.Context, query string, limit int) ([]SearchResult, error) {
    cmd := exec.CommandContext(ctx, "bv",
        "-robot-search", query,
        "--search-limit", fmt.Sprintf("%d", limit),
        "--search-mode", "hybrid",
        "--json",
    )
    out, err := cmd.Output()
    if err != nil {
        return nil, err
    }

    var results []SearchResult
    if err := json.Unmarshal(out, &results); err != nil {
        return nil, err
    }
    return results, nil
}

// FindRelatedWork finds work related to agent's current task
func FindRelatedWork(ctx context.Context, taskDescription string) ([]SearchResult, error) {
    return SemanticSearch(ctx, taskDescription, 5)
}
```

### New NTM Commands

```bash
# Get complete triage (replaces 4 calls)
ntm work triage
ntm work triage --by-label
ntm work triage --by-track

# Alerts
ntm work alerts
ntm work alerts --critical-only

# Search
ntm work search "implement JWT authentication"

# Impact analysis
ntm work impact internal/auth/*.go

# Use markdown output
ntm --robot-triage --format=markdown
```

---

## CRITICAL: CM Server Mode

### The Problem

NTM makes **subprocess calls** for every CM query:

```go
// CURRENT: Slow subprocess for each query
cmd := exec.Command("cm", "context", task, "--json")
out, err := cmd.Output()  // ~500ms per call
```

### The Solution: HTTP Daemon

CM provides an **HTTP MCP server** that NTM ignores:

```bash
# Start once, query infinitely
cm serve --port 8765 --host 127.0.0.1
```

### CM Hidden Features

| Feature | Command | Purpose | Usage |
|---------|---------|---------|-------|
| **HTTP Server** | `cm serve` | Single daemon for all queries | ❌ Unused |
| **Outcome Feedback** | `cm outcome` | Record task success/failure | ❌ Unused |
| **Session Audit** | `cm audit` | Audit sessions against rules | ❌ Unused |
| **Privacy Controls** | `cm privacy` | Cross-agent knowledge sharing | ❌ Unused |
| **Agent Onboarding** | `cm onboard` | Self-training on playbook | ❌ Unused |
| **Similar Rules** | `cm similar` | Semantic rule matching | ❌ Unused |
| **Top Rules** | `cm top` | Most effective rules | ❌ Unused |
| **Stale Rules** | `cm stale` | Rules without recent feedback | ❌ Unused |
| **Rule Provenance** | `cm why` | Rule origin tracing | ❌ Unused |

### Integration 1: Launch CM Daemon (under NTM Supervisor)

**Revision**: CM should be started/stopped by a shared `internal/supervisor/` component that:
- Chooses an available port (or reuses an existing healthy daemon)
- Writes a PID file under `.ntm/pids/cm-<session>.pid`
- Streams stdout/stderr to `.ntm/logs/cm-<session>.log`
- Restarts with exponential backoff if health checks fail
- Records daemon health in the state store

```go
// internal/supervisor/supervisor.go - NEW FILE

type DaemonSpec struct {
    Name        string   // "cm", "bd"
    Command     string   // "cm", "bd"
    Args        []string // ["serve", "--port", "8765"]
    HealthURL   string   // "http://127.0.0.1:8765/health"
    PortFlag    string   // "--port"
    DefaultPort int
}

type Supervisor struct {
    session    string
    projectDir string
    daemons    map[string]*ManagedDaemon
    mu         sync.Mutex
}

type ManagedDaemon struct {
    spec       DaemonSpec
    cmd        *exec.Cmd
    port       int
    pid        int
    logFile    *os.File
    restarts   int
    lastStart  time.Time
    healthy    bool
}

func (s *Supervisor) Start(ctx context.Context, spec DaemonSpec) (*ManagedDaemon, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    // Check if already running
    if existing, ok := s.daemons[spec.Name]; ok && existing.healthy {
        return existing, nil
    }

    // Find available port
    port := findAvailablePort(spec.DefaultPort)

    // Prepare log file (absolute path)
    logDir := filepath.Join(s.projectDir, ".ntm", "logs")
    os.MkdirAll(logDir, 0755)
    logPath := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", spec.Name, s.session))
    logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)

    // Build args with port
    args := append(spec.Args, spec.PortFlag, strconv.Itoa(port))

    cmd := exec.CommandContext(ctx, spec.Command, args...)
    cmd.Stdout = logFile
    cmd.Stderr = logFile

    if err := cmd.Start(); err != nil {
        return nil, err
    }

    // Write PID file
    pidDir := filepath.Join(s.projectDir, ".ntm", "pids")
    os.MkdirAll(pidDir, 0755)
    pidPath := filepath.Join(pidDir, fmt.Sprintf("%s-%s.pid", spec.Name, s.session))
    os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0644)

    daemon := &ManagedDaemon{
        spec:      spec,
        cmd:       cmd,
        port:      port,
        pid:       cmd.Process.Pid,
        logFile:   logFile,
        lastStart: time.Now(),
    }

    // Wait for health
    healthURL := strings.Replace(spec.HealthURL, strconv.Itoa(spec.DefaultPort), strconv.Itoa(port), 1)
    daemon.healthy = waitForHealth(ctx, healthURL, 5*time.Second)

    s.daemons[spec.Name] = daemon
    return daemon, nil
}

func (s *Supervisor) Stop(name string) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    if daemon, ok := s.daemons[name]; ok {
        daemon.logFile.Close()
        if daemon.cmd.Process != nil {
            daemon.cmd.Process.Kill()
        }
        delete(s.daemons, name)
    }
    return nil
}
```

**Shutdown policy**:
- If daemon was started by NTM for this session, stop it on session end.
- If daemon existed before (different PID owner), do not kill it; only disconnect.

Now the CM-specific client:

```go
// internal/cm/daemon.go - NEW FILE

type CMDaemon struct {
    port    int
    host    string
    cmd     *exec.Cmd
    client  *http.Client
    baseURL string
}

func NewCMDaemon(port int) *CMDaemon {
    return &CMDaemon{
        port:    port,
        host:    "127.0.0.1",
        client:  &http.Client{Timeout: 10 * time.Second},
        baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
    }
}

func (d *CMDaemon) Start(ctx context.Context) error {
    // Check if already running
    if d.isRunning() {
        log.Printf("CM daemon already running on port %d", d.port)
        return nil
    }

    // Start the daemon
    d.cmd = exec.CommandContext(ctx, "cm", "serve",
        "--port", fmt.Sprintf("%d", d.port),
        "--host", d.host,
    )

    if err := d.cmd.Start(); err != nil {
        return fmt.Errorf("failed to start cm serve: %w", err)
    }

    // Wait for it to be ready
    for i := 0; i < 30; i++ {
        if d.isRunning() {
            log.Printf("CM daemon started on port %d", d.port)
            return nil
        }
        time.Sleep(100 * time.Millisecond)
    }

    return fmt.Errorf("cm serve did not start within 3 seconds")
}

func (d *CMDaemon) isRunning() bool {
    resp, err := d.client.Get(d.baseURL + "/health")
    if err != nil {
        return false
    }
    defer resp.Body.Close()
    return resp.StatusCode == 200
}

func (d *CMDaemon) Stop() {
    if d.cmd != nil && d.cmd.Process != nil {
        d.cmd.Process.Kill()
    }
}
```

### Integration 2: Query Context via HTTP

```go
// internal/cm/client.go - NEW FILE

type CMClient struct {
    daemon *CMDaemon
}

func NewCMClient(daemon *CMDaemon) *CMClient {
    return &CMClient{daemon: daemon}
}

type ContextResult struct {
    RelevantBullets []Rule    `json:"relevantBullets"`
    AntiPatterns    []Rule    `json:"antiPatterns"`
    HistorySnippets []Snippet `json:"historySnippets"`
    SuggestedQueries []string `json:"suggestedCassQueries"`
}

// GetContext queries CM for task-relevant rules via HTTP (fast!)
func (c *CMClient) GetContext(ctx context.Context, task string) (*ContextResult, error) {
    req, _ := http.NewRequestWithContext(ctx, "POST",
        c.daemon.baseURL+"/context",
        strings.NewReader(fmt.Sprintf(`{"task": %q}`, task)),
    )
    req.Header.Set("Content-Type", "application/json")

    resp, err := c.daemon.client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var result ContextResult
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }
    return &result, nil
}
```

### Integration 3: Outcome Feedback Loop

```go
// internal/cm/feedback.go - NEW FILE

type OutcomeStatus string

const (
    OutcomeSuccess OutcomeStatus = "success"
    OutcomeFailure OutcomeStatus = "failure"
    OutcomePartial OutcomeStatus = "partial"
)

type OutcomeReport struct {
    Status    OutcomeStatus `json:"status"`
    RuleIDs   []string      `json:"rule_ids"`   // Rules that were applied
    Sentiment string        `json:"sentiment"`  // positive, negative, neutral
    Notes     string        `json:"notes,omitempty"`
}

// RecordOutcome sends feedback about rule effectiveness
func (c *CMClient) RecordOutcome(ctx context.Context, report OutcomeReport) error {
    data, _ := json.Marshal(report)
    req, _ := http.NewRequestWithContext(ctx, "POST",
        c.daemon.baseURL+"/outcome",
        bytes.NewReader(data),
    )
    req.Header.Set("Content-Type", "application/json")

    resp, err := c.daemon.client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != 200 {
        return fmt.Errorf("cm outcome failed: %s", resp.Status)
    }
    return nil
}

// OnTaskComplete records outcome when agent finishes work
func OnTaskComplete(ctx context.Context, cmClient *CMClient, agent AgentInfo, success bool, appliedRules []string) {
    status := OutcomeSuccess
    sentiment := "positive"
    if !success {
        status = OutcomeFailure
        sentiment = "negative"
    }

    cmClient.RecordOutcome(ctx, OutcomeReport{
        Status:    status,
        RuleIDs:   appliedRules,
        Sentiment: sentiment,
        Notes:     fmt.Sprintf("Agent %s completed task", agent.Name),
    })
}
```

### Integration 4: Cross-Agent Knowledge Sharing

```go
// internal/cm/privacy.go - NEW FILE

type PrivacyPolicy struct {
    AgentName     string   `json:"agent_name"`
    AllowedAgents []string `json:"allowed_agents"`
    DeniedAgents  []string `json:"denied_agents"`
    Enabled       bool     `json:"enabled"`
}

// ConfigurePrivacy sets up cross-agent knowledge sharing rules
func (c *CMClient) ConfigurePrivacy(ctx context.Context, policy PrivacyPolicy) error {
    data, _ := json.Marshal(policy)
    req, _ := http.NewRequestWithContext(ctx, "POST",
        c.daemon.baseURL+"/privacy",
        bytes.NewReader(data),
    )
    req.Header.Set("Content-Type", "application/json")

    resp, err := c.daemon.client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    return nil
}
```

### New NTM Commands

```bash
# Start CM daemon
ntm memory serve
ntm memory serve --port 8765

# Query context
ntm memory context "implement JWT auth"

# Record outcome
ntm memory outcome success --rules b-123,b-456
ntm memory outcome failure --rules b-789

# Privacy controls
ntm memory privacy status
ntm memory privacy allow GreenLake
ntm memory privacy deny MaliciousBot
```

---

## CRITICAL: Destructive Command Protection

### The Problem

A real incident: An agent ran `git checkout --` and **erased hours of another agent's work**.

Instructions in AGENTS.md say "don't run destructive commands" but agents can violate instructions.

### The Solution: Provider-Agnostic Enforcement + Policy Gates

**Revision**: Keep Claude Code hooks, but also add a provider-agnostic layer so Codex/Gemini panes are protected too.

#### Safety Policy File (repo-local)

NTM manages a policy file at `.ntm/policy.yaml`:

```yaml
# .ntm/policy.yaml - Safety policy for NTM sessions
version: 1

# Commands that are always blocked (no override possible)
blocked:
  - pattern: "git reset --hard"
    reason: "Hard reset loses commits"
  - pattern: "git checkout --"
    reason: "Discards uncommitted changes"
  - pattern: "git clean -f"
    reason: "Force clean deletes files"
  - pattern: "rm -rf /"
    reason: "System destruction"

# Commands that require human or SLB approval
approval_required:
  - pattern: "git push --force"
    reason: "Force push rewrites history"
  - pattern: "git push -f"
    reason: "Force push rewrites history"
  - pattern: "rm -rf"
    reason: "Recursive delete requires approval"
    exceptions:
      - "/tmp/*"
      - ".ntm/cache/*"

# Commands that are explicitly allowed (bypass checks)
allowed:
  - pattern: "git checkout -b"
    reason: "Create branch is safe"
  - pattern: "git restore --staged"
    reason: "Unstage is safe"
  - pattern: "rm -rf /tmp/"
    reason: "Temp cleanup is safe"

# Automation settings
automation:
  auto_push: false          # --auto-push disabled by default (CRITICAL)
  force_release: approval   # Force-release requires approval
  auto_commit: true         # Auto-commit is safe
```

#### PATH-based Wrappers (covers all agent tools)

During spawn, NTM prepends `.ntm/bin` to PATH inside each tmux pane and installs lightweight wrappers:

```bash
# .ntm/bin/git - Wrapper that checks policy before executing real git
#!/bin/bash
POLICY_FILE="${POLICY_FILE:-$PWD/.ntm/policy.yaml}"
NTM_LOG="${NTM_LOG:-$PWD/.ntm/logs/blocked.jsonl}"

check_blocked() {
    local cmd="$*"
    # Check against policy (simplified - real impl uses yq or Go binary)
    if echo "$cmd" | grep -qE "(reset --hard|checkout --|clean -f)"; then
        echo "🛑 BLOCKED by NTM policy" >&2
        echo "{\"ts\":\"$(date -Iseconds)\",\"cmd\":\"$cmd\",\"blocked\":true}" >> "$NTM_LOG"
        exit 1
    fi
    if echo "$cmd" | grep -qE "(push --force|push -f)" && [ "$NTM_APPROVED" != "1" ]; then
        echo "⚠️  APPROVAL REQUIRED: Run 'ntm approve' or set NTM_APPROVED=1" >&2
        exit 1
    fi
}

check_blocked "$@"
exec /usr/bin/git "$@"
```

This ensures safety even if a provider ignores hook systems.

#### Claude Code Hooks (Enhanced)

Claude Code's `PreToolUse` hook system can **mechanically block** commands before execution:

```python
# .claude/hooks/git_safety_guard.py
import re
import sys
import json

BLOCKED_PATTERNS = [
    (r'git\s+checkout\s+--', "Discards uncommitted changes"),
    (r'git\s+reset\s+--hard', "Hard reset loses commits"),
    (r'git\s+clean\s+-f', "Force clean deletes files"),
    (r'git\s+push\s+--force', "Force push rewrites history"),
    (r'git\s+stash\s+drop', "Drops stashed changes"),
    (r'git\s+stash\s+clear', "Clears all stashes"),
    (r'rm\s+-rf\s+(?!/tmp)', "Recursive delete (except /tmp)"),
]

# Safe variants that look similar but are allowed
ALLOWED_PATTERNS = [
    r'git\s+checkout\s+-b',      # Create branch (safe)
    r'git\s+restore\s+--staged', # Unstage (safe)
    r'rm\s+-rf\s+/tmp/',         # Clean temp (safe)
]

def check_command(cmd):
    # Allow safe variants first
    for pattern in ALLOWED_PATTERNS:
        if re.search(pattern, cmd, re.IGNORECASE):
            return True, None

    # Block dangerous patterns
    for pattern, reason in BLOCKED_PATTERNS:
        if re.search(pattern, cmd, re.IGNORECASE):
            return False, reason

    return True, None

def main():
    # Read hook input from stdin
    hook_input = json.load(sys.stdin)

    if hook_input.get("tool_name") != "Bash":
        # Only check Bash commands
        print(json.dumps({"decision": "approve"}))
        return

    command = hook_input.get("tool_input", {}).get("command", "")
    allowed, reason = check_command(command)

    if not allowed:
        print(json.dumps({
            "decision": "block",
            "message": f"🛑 BLOCKED: {reason}\nCommand: {command}\n\nUse a safer alternative or ask for human approval."
        }))
    else:
        print(json.dumps({"decision": "approve"}))

if __name__ == "__main__":
    main()
```

### Integration 1: Auto-Install During Spawn

```go
// internal/hooks/safety.go - NEW FILE

const safetyHookScript = `#!/usr/bin/env python3
# Auto-generated by NTM - Destructive Command Protection
import re
import sys
import json

BLOCKED_PATTERNS = [
    (r'git\s+checkout\s+--', "Discards uncommitted changes"),
    (r'git\s+reset\s+--hard', "Hard reset loses commits"),
    (r'git\s+clean\s+-f', "Force clean deletes files"),
    (r'git\s+push\s+--force', "Force push rewrites history"),
    (r'git\s+stash\s+drop', "Drops stashed changes"),
    (r'git\s+stash\s+clear', "Clears all stashes"),
    (r'rm\s+-rf\s+(?!/tmp)', "Recursive delete (except /tmp)"),
]

ALLOWED_PATTERNS = [
    r'git\s+checkout\s+-b',
    r'git\s+restore\s+--staged',
    r'rm\s+-rf\s+/tmp/',
]

def check_command(cmd):
    for pattern in ALLOWED_PATTERNS:
        if re.search(pattern, cmd, re.IGNORECASE):
            return True, None
    for pattern, reason in BLOCKED_PATTERNS:
        if re.search(pattern, cmd, re.IGNORECASE):
            return False, reason
    return True, None

def main():
    hook_input = json.load(sys.stdin)
    if hook_input.get("tool_name") != "Bash":
        print(json.dumps({"decision": "approve"}))
        return
    command = hook_input.get("tool_input", {}).get("command", "")
    allowed, reason = check_command(command)
    if not allowed:
        print(json.dumps({
            "decision": "block",
            "message": f"🛑 BLOCKED: {reason}\\nCommand: {command}"
        }))
    else:
        print(json.dumps({"decision": "approve"}))

if __name__ == "__main__":
    main()
`

const safetyHookSettings = `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [".claude/hooks/git_safety_guard.py"]
      }
    ]
  }
}
`

// InstallSafetyHooks installs destructive command protection
func InstallSafetyHooks(projectPath string) error {
    hookDir := filepath.Join(projectPath, ".claude", "hooks")
    if err := os.MkdirAll(hookDir, 0755); err != nil {
        return err
    }

    // Write hook script
    hookPath := filepath.Join(hookDir, "git_safety_guard.py")
    if err := os.WriteFile(hookPath, []byte(safetyHookScript), 0755); err != nil {
        return err
    }

    // Write/update settings
    settingsPath := filepath.Join(projectPath, ".claude", "settings.json")

    // Merge with existing settings if present
    existingSettings := make(map[string]interface{})
    if data, err := os.ReadFile(settingsPath); err == nil {
        json.Unmarshal(data, &existingSettings)
    }

    var newSettings map[string]interface{}
    json.Unmarshal([]byte(safetyHookSettings), &newSettings)

    // Merge hooks
    if existingHooks, ok := existingSettings["hooks"].(map[string]interface{}); ok {
        if newHooks, ok := newSettings["hooks"].(map[string]interface{}); ok {
            for k, v := range newHooks {
                existingHooks[k] = v
            }
        }
        existingSettings["hooks"] = existingHooks
    } else {
        existingSettings["hooks"] = newSettings["hooks"]
    }

    data, _ := json.MarshalIndent(existingSettings, "", "  ")
    return os.WriteFile(settingsPath, data, 0644)
}

// UninstallSafetyHooks removes the protection
func UninstallSafetyHooks(projectPath string) error {
    hookPath := filepath.Join(projectPath, ".claude", "hooks", "git_safety_guard.py")
    return os.Remove(hookPath)
}
```

### Integration 2: Auto-Install on Spawn

```go
// internal/cli/spawn.go - UPDATED

func spawnSession(ctx context.Context, opts SpawnOptions) (*Session, error) {
    projectPath, _ := os.Getwd()

    // 1. Install safety hooks BEFORE spawning agents
    if opts.SafetyHooks {
        if err := hooks.InstallSafetyHooks(projectPath); err != nil {
            log.Printf("Warning: Failed to install safety hooks: %v", err)
        } else {
            log.Printf("Installed destructive command protection")
        }
    }

    // 2. Continue with normal spawn...
    // ...
}
```

### Integration 3: Blocked Command Logging

```go
// internal/monitor/blocked.go - NEW FILE

type BlockedCommand struct {
    Timestamp time.Time `json:"timestamp"`
    Session   string    `json:"session"`
    Agent     string    `json:"agent"`
    Command   string    `json:"command"`
    Reason    string    `json:"reason"`
}

var blockedCommands []BlockedCommand
var blockedMu sync.Mutex

// LogBlockedCommand records a blocked destructive command
func LogBlockedCommand(session, agent, command, reason string) {
    blockedMu.Lock()
    defer blockedMu.Unlock()

    blockedCommands = append(blockedCommands, BlockedCommand{
        Timestamp: time.Now(),
        Session:   session,
        Agent:     agent,
        Command:   command,
        Reason:    reason,
    })

    // Also log to file for audit
    logPath := filepath.Join(".ntm", "blocked_commands.jsonl")
    f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    defer f.Close()

    data, _ := json.Marshal(blockedCommands[len(blockedCommands)-1])
    f.Write(data)
    f.WriteString("\n")
}

// GetBlockedCommands returns recent blocked commands
func GetBlockedCommands(limit int) []BlockedCommand {
    blockedMu.Lock()
    defer blockedMu.Unlock()

    if len(blockedCommands) <= limit {
        return blockedCommands
    }
    return blockedCommands[len(blockedCommands)-limit:]
}
```

### New NTM Commands

```bash
# Safety hooks
ntm safety install           # Install destructive command protection
ntm safety uninstall         # Remove protection
ntm safety status            # Show hook status

# Blocked commands
ntm safety blocked           # List recently blocked commands
ntm safety blocked --all     # List all blocked commands

# Spawn with safety (default: enabled)
ntm spawn myproject --cc=2 --safety=true
ntm spawn myproject --cc=2 --no-safety  # Disable (not recommended)
```

---

## CRITICAL: Session Coordinator Intelligence

### The Problem

NTM **already registers itself as an Agent Mail agent** (the "session coordinator") but does nothing with it:

```go
// This already happens in session.go:
RegisterSessionAgent(ctx, "myproject", projectPath)
// Creates agent like "OrangeFox" (session coordinator)
// Then... nothing. It's just a passive identity holder.
```

### The Solution: Intelligent Coordinator

The session coordinator should **actively manage** agents:

```
┌─────────────────────────────────────────────────────────────────┐
│              Session Coordinator Intelligence                     │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  CURRENT (Passive):                                              │
│  - Registers identity                                            │
│  - Stores locally                                                │
│  - That's it                                                     │
│                                                                 │
│  TARGET (Active):                                                │
│  1. Monitor all agents in session                                │
│  2. Send periodic digest summaries to human                      │
│  3. Detect file conflicts and negotiate resolutions              │
│  4. Assign work based on Agent Mail scoring                      │
│  5. Scale agents up/down based on queue depth                    │
│  6. Coordinate cross-agent communication                         │
│  7. Handle crashed agent recovery                                │
│  8. Manage file reservation lifecycle                            │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Integration 1: Active Monitoring

```go
// internal/coordinator/coordinator.go - NEW FILE

type SessionCoordinator struct {
    session     string
    agentName   string  // e.g., "OrangeFox"
    projectPath string

    // Subsystems
    mailClient     *agentmail.Client
    reservationMon *StaleReservationMonitor
    alertMon       *AlertMonitor
    qualityMon     *QualityMonitor

    // State
    agents      map[string]*AgentState
    agentsMu    sync.RWMutex

    // Channels
    events chan CoordinatorEvent
    done   chan struct{}
}

type CoordinatorEvent struct {
    Type    string      `json:"type"`
    Payload interface{} `json:"payload"`
}

func NewSessionCoordinator(session, projectPath string) (*SessionCoordinator, error) {
    // Register as coordinator agent
    result, err := agentmail.StartSession(context.Background(), agentmail.MacroStartSessionOptions{
        HumanKey:        projectPath,
        Program:         "ntm-coordinator",
        Model:           "internal",
        TaskDescription: fmt.Sprintf("Coordinating session %s", session),
    })
    if err != nil {
        return nil, err
    }

    return &SessionCoordinator{
        session:     session,
        agentName:   result.Agent.Name,
        projectPath: projectPath,
        mailClient:  agentmail.NewClient(),
        agents:      make(map[string]*AgentState),
        events:      make(chan CoordinatorEvent, 100),
        done:        make(chan struct{}),
    }, nil
}

func (c *SessionCoordinator) Start(ctx context.Context) {
    // Start subsystems
    go c.reservationMon.Start(ctx)
    go c.alertMon.Start(ctx)
    go c.qualityMon.Start(ctx)

    // Main coordination loop
    go c.coordinationLoop(ctx)

    // Inbox polling
    go c.inboxPoller(ctx)
}
```

### Integration 2: Digest Summaries

```go
// internal/coordinator/digest.go - NEW FILE

type DigestSummary struct {
    Session       string            `json:"session"`
    GeneratedAt   time.Time         `json:"generated_at"`
    AgentStatus   map[string]string `json:"agent_status"`
    WorkCompleted int               `json:"work_completed"`
    WorkPending   int               `json:"work_pending"`
    Conflicts     []string          `json:"conflicts,omitempty"`
    Alerts        []string          `json:"alerts,omitempty"`
    Quality       QualityMetrics    `json:"quality"`
}

// GenerateDigest creates a summary of session state
func (c *SessionCoordinator) GenerateDigest() *DigestSummary {
    c.agentsMu.RLock()
    defer c.agentsMu.RUnlock()

    status := make(map[string]string)
    for name, agent := range c.agents {
        status[name] = string(agent.State)
    }

    triage, _ := bv.GetTriage(context.Background())

    return &DigestSummary{
        Session:       c.session,
        GeneratedAt:   time.Now(),
        AgentStatus:   status,
        WorkCompleted: countCompletedToday(),
        WorkPending:   len(triage.Priority),
        Alerts:        extractAlertMessages(triage.Alerts),
    }
}

// SendDigestToHuman sends periodic digest via Agent Mail
func (c *SessionCoordinator) SendDigestToHuman(ctx context.Context) error {
    digest := c.GenerateDigest()

    body := formatDigestMarkdown(digest)

    // "Human" is a reserved Agent Mail identity that routes to configured notification channels
    // (Slack, email, desktop notification) based on .ntm/config.yaml settings.
    // If no Human identity is configured, messages are logged to .ntm/human_inbox/
    return c.mailClient.SendMessage(ctx, agentmail.MessageOptions{
        ProjectKey: c.projectPath,
        SenderName: c.agentName,
        To:         []string{"Human"},  // Reserved: routes to human notification channels
        Subject:    fmt.Sprintf("Session %s Digest - %s", c.session, time.Now().Format("15:04")),
        BodyMD:     body,
        Importance: "normal",
    })
}
```

### Integration 3: Conflict Resolution

```go
// internal/coordinator/conflicts.go - NEW FILE

// DetectConflicts checks for file reservation conflicts
func (c *SessionCoordinator) DetectConflicts(ctx context.Context) []Conflict {
    reservations, _ := c.mailClient.ListReservations(ctx, c.projectPath, "", true)

    // Group by file pattern
    byPattern := make(map[string][]FileReservation)
    for _, r := range reservations {
        byPattern[r.PathPattern] = append(byPattern[r.PathPattern], r)
    }

    var conflicts []Conflict
    for pattern, holders := range byPattern {
        if len(holders) > 1 {
            conflicts = append(conflicts, Conflict{
                Pattern: pattern,
                Holders: holders,
            })
        }
    }

    return conflicts
}

// NegotiateConflict attempts to resolve a file conflict
func (c *SessionCoordinator) NegotiateConflict(ctx context.Context, conflict Conflict) error {
    // Strategy: Ask the agent with lower priority to release
    // Priority = (time held) / (work remaining)

    var lowestPriority *FileReservation
    lowestScore := math.MaxFloat64

    for _, holder := range conflict.Holders {
        score := calculatePriority(holder)
        if score < lowestScore {
            lowestScore = score
            lowestPriority = &holder
        }
    }

    // Send message requesting release
    return c.mailClient.SendMessage(ctx, agentmail.MessageOptions{
        ProjectKey: c.projectPath,
        SenderName: c.agentName,
        To:         []string{lowestPriority.AgentName},
        Subject:    "Request: Release file reservation",
        BodyMD: fmt.Sprintf(`
Hi %s,

There's a conflict for files matching **%s**.

Another agent needs these files. Could you:
1. Complete your current edit quickly, OR
2. Release the reservation with: "Release reservation for %s"

Thanks!
- Session Coordinator
`, lowestPriority.AgentName, conflict.Pattern, conflict.Pattern),
        Importance: "high",
    })
}
```

### Integration 4: Work Assignment

```go
// internal/coordinator/assign.go - NEW FILE

// AssignWork distributes work to idle agents
func (c *SessionCoordinator) AssignWork(ctx context.Context) error {
    // Get idle agents
    idleAgents := c.getIdleAgents()
    if len(idleAgents) == 0 {
        return nil // No idle agents
    }

    // Get prioritized work
    triage, err := bv.GetTriage(ctx)
    if err != nil {
        return err
    }

    // Revised: Score-based assignment (capability + file overlap + critical path)
    assignments := ScoreAndSelectAssignments(idleAgents, triage, ScoreConfig{
        PreferCriticalPath:  true,
        PenalizeFileOverlap: true,
        UseAgentProfiles:    true,
        BudgetAware:         true,
    })

    for _, a := range assignments {
        agent := a.Agent
        work := a.Work

        // Predict files + penalize overlap with other active tasks
        files := predictAffectedFiles(work)
        reservations, _ := c.mailClient.ReservePaths(ctx, agentmail.FileReservationOptions{
            ProjectKey: c.projectPath,
            AgentName:  agent.Name,
            Paths:      files,
            TTLSeconds: 3600,
            Exclusive:  true,
            Reason:     fmt.Sprintf("Working on %s", work.ID),
        })

        if len(reservations.Conflicts) > 0 {
            continue // Skip, find different work
        }

        // Send assignment message
        c.mailClient.SendMessage(ctx, agentmail.MessageOptions{
            ProjectKey: c.projectPath,
            SenderName: c.agentName,
            To:         []string{agent.Name},
            Subject:    fmt.Sprintf("Assignment: %s", work.Title),
            BodyMD: fmt.Sprintf(`
## New Assignment

**Bead:** %s
**Title:** %s
**Priority:** %s

### Reason
%s

### Reserved Files
%s

Please start work on this item.
`, work.ID, work.Title, work.Priority, work.Reason, strings.Join(files, "\n- ")),
            Importance: "high",
        })
    }

    return nil
}
```

### New NTM Commands

```bash
# Coordinator control
ntm coordinator status        # Show coordinator status
ntm coordinator digest        # Generate and display digest
ntm coordinator conflicts     # List current conflicts
ntm coordinator assign        # Trigger work assignment

# Enable/disable features
ntm coordinator enable auto-assign
ntm coordinator enable digest --interval=30m
ntm coordinator disable conflict-resolution
```

---

## CRITICAL: BD Message Integration

### The Problem

The beads CLI (`bd`) has a **complete messaging system** that NTM ignores:

```bash
# These commands exist but NTM never uses them
bd message send <agent> <message>
bd message inbox [--unread-only] [--urgent-only]
bd message read <msg-id>
bd message ack <msg-id>
```

### The Solution: Unified Messaging via BD

```go
// internal/bd/message.go - NEW FILE

// BDMessageClient wraps bd message commands
type BDMessageClient struct {
    projectPath string
    agentName   string
}

func NewBDMessageClient(projectPath, agentName string) *BDMessageClient {
    return &BDMessageClient{
        projectPath: projectPath,
        agentName:   agentName,
    }
}

// Send sends a message to another agent
func (c *BDMessageClient) Send(ctx context.Context, to, message string) error {
    cmd := exec.CommandContext(ctx, "bd", "message", "send", to, message)
    cmd.Env = append(os.Environ(),
        fmt.Sprintf("BEADS_AGENT_NAME=%s", c.agentName),
        fmt.Sprintf("BEADS_PROJECT_ID=%s", c.projectPath),
    )
    return cmd.Run()
}

// Inbox retrieves messages for the agent
func (c *BDMessageClient) Inbox(ctx context.Context, unreadOnly, urgentOnly bool) ([]Message, error) {
    args := []string{"message", "inbox", "--json"}
    if unreadOnly {
        args = append(args, "--unread-only")
    }
    if urgentOnly {
        args = append(args, "--urgent-only")
    }

    cmd := exec.CommandContext(ctx, "bd", args...)
    cmd.Env = append(os.Environ(),
        fmt.Sprintf("BEADS_AGENT_NAME=%s", c.agentName),
        fmt.Sprintf("BEADS_PROJECT_ID=%s", c.projectPath),
    )

    out, err := cmd.Output()
    if err != nil {
        return nil, err
    }

    var messages []Message
    json.Unmarshal(out, &messages)
    return messages, nil
}

// Read marks a message as read and returns its content
func (c *BDMessageClient) Read(ctx context.Context, msgID string) (*Message, error) {
    cmd := exec.CommandContext(ctx, "bd", "message", "read", msgID, "--json")
    cmd.Env = append(os.Environ(),
        fmt.Sprintf("BEADS_AGENT_NAME=%s", c.agentName),
        fmt.Sprintf("BEADS_PROJECT_ID=%s", c.projectPath),
    )

    out, err := cmd.Output()
    if err != nil {
        return nil, err
    }

    var msg Message
    json.Unmarshal(out, &msg)
    return &msg, nil
}

// Ack acknowledges receipt of a message
func (c *BDMessageClient) Ack(ctx context.Context, msgID string) error {
    cmd := exec.CommandContext(ctx, "bd", "message", "ack", msgID)
    cmd.Env = append(os.Environ(),
        fmt.Sprintf("BEADS_AGENT_NAME=%s", c.agentName),
        fmt.Sprintf("BEADS_PROJECT_ID=%s", c.projectPath),
    )
    return cmd.Run()
}
```

### Integration: Unified Messaging

```go
// internal/messaging/unified.go - NEW FILE

// UnifiedMessenger combines Agent Mail and BD messaging into a canonical event stream
type UnifiedMessenger struct {
    agentMail     *agentmail.Client
    bdMessage     *bd.BDMessageClient
    preferred     string // "agentmail" or "bd"
    stateStore    *state.Store
    seenMessages  map[string]bool // Dedupe key: channel/message_id
}

func NewUnifiedMessenger(projectPath, agentName string, preferred string) *UnifiedMessenger {
    return &UnifiedMessenger{
        agentMail: agentmail.NewClient(),
        bdMessage: bd.NewBDMessageClient(projectPath, agentName),
        preferred: preferred,
    }
}

// Send sends a message using the preferred channel
func (m *UnifiedMessenger) Send(ctx context.Context, to, subject, body string) error {
    switch m.preferred {
    case "bd":
        return m.bdMessage.Send(ctx, to, fmt.Sprintf("%s: %s", subject, body))
    default:
        return m.agentMail.SendMessage(ctx, agentmail.MessageOptions{
            To:      []string{to},
            Subject: subject,
            BodyMD:  body,
        })
    }
}

// InboxAll retrieves messages from all channels, normalizes, dedupes, and stores them
func (m *UnifiedMessenger) InboxAll(ctx context.Context) ([]Message, error) {
    var all []Message

    // Agent Mail
    amMsgs, _ := m.agentMail.FetchInbox(ctx, agentmail.InboxOptions{Limit: 50})
    for _, msg := range convertAMMessages(amMsgs) {
        dedupeKey := fmt.Sprintf("agentmail/%d", msg.ID)
        if !m.seenMessages[dedupeKey] {
            m.seenMessages[dedupeKey] = true
            msg.Channel = "agentmail"
            msg.CorrelationID = msg.ThreadID // Link to task if known
            all = append(all, msg)
        }
    }

    // BD Messages
    bdMsgs, _ := m.bdMessage.Inbox(ctx, false, false)
    for _, msg := range bdMsgs {
        dedupeKey := fmt.Sprintf("bd/%s", msg.ID)
        if !m.seenMessages[dedupeKey] {
            m.seenMessages[dedupeKey] = true
            msg.Channel = "bd"
            all = append(all, msg)
        }
    }

    // Store in state store so UIs can be event-driven instead of polling
    for _, msg := range all {
        m.stateStore.UpsertMessage(msg)
    }

    // Sort by timestamp (stable)
    sort.Slice(all, func(i, j int) bool {
        return all[i].Timestamp.After(all[j].Timestamp)
    })

    return all, nil
}

// Canonical Message Schema
type Message struct {
    ID            string    `json:"id"`
    Channel       string    `json:"channel"`       // "agentmail" or "bd"
    From          string    `json:"from"`
    To            []string  `json:"to"`
    Subject       string    `json:"subject"`
    Body          string    `json:"body"`
    Timestamp     time.Time `json:"timestamp"`
    ThreadID      string    `json:"thread_id,omitempty"`
    CorrelationID string    `json:"correlation_id,omitempty"` // Links to task/reservation
    Importance    string    `json:"importance"`
    Read          bool      `json:"read"`
    Acknowledged  bool      `json:"acknowledged"`
}
```

### New NTM Commands

```bash
# Messaging
ntm message send GreenLake "Please review auth changes"
ntm message inbox
ntm message inbox --unread
ntm message inbox --urgent
ntm message read <msg-id>
ntm message ack <msg-id>

# Channel selection
ntm message send GreenLake "Hello" --via=agentmail
ntm message send GreenLake "Hello" --via=bd
```

---

## CRITICAL: BD Daemon Mode

### The Problem

NTM requires manual `bd sync` calls to keep beads in sync:

```bash
# Currently: Manual sync required
bd sync  # Developer must remember to run this
```

### The Solution: Background Daemon

```bash
# BD has a daemon mode that NTM ignores
bd daemon --start --auto-commit --interval 5s --health --metrics --json
```

**Policy change**: `--auto-push` is **disabled by default** and requires:
- Explicit CLI flag (`--allow-auto-push`)
- Policy permission in `.ntm/policy.yaml` (see Destructive Command Protection)
- Recorded approval (human or SLB gate)

### Integration: Auto-Start Daemon

```go
// internal/bd/daemon.go - NEW FILE

type BDDaemon struct {
    cmd       *exec.Cmd
    port      int
    isRunning bool
    autoPush  bool  // Disabled by default; requires policy approval
}

type BDDaemonOptions struct {
    AutoCommit bool
    AutoPush   bool  // DANGEROUS: requires policy.yaml permission + approval
    Interval   time.Duration
}

func NewBDDaemon(opts BDDaemonOptions) *BDDaemon {
    return &BDDaemon{
        port:     8766,
        autoPush: opts.AutoPush,
    }
}

func (d *BDDaemon) Start(ctx context.Context) error {
    if d.isRunning {
        return nil
    }

    args := []string{
        "daemon",
        "--start",
        "--auto-commit",
        "--interval", "5s",
        "--health",
        "--metrics",
        "--json",
    }

    // --auto-push is policy-gated and disabled by default (see .ntm/policy.yaml)
    if d.autoPush {
        args = append(args, "--auto-push")
    }

    d.cmd = exec.CommandContext(ctx, "bd", args...)

    if err := d.cmd.Start(); err != nil {
        return err
    }

    d.isRunning = true
    log.Printf("BD daemon started")
    return nil
}

func (d *BDDaemon) Stop() error {
    if d.cmd != nil && d.cmd.Process != nil {
        d.isRunning = false
        return d.cmd.Process.Kill()
    }
    return nil
}

func (d *BDDaemon) Health() (*DaemonHealth, error) {
    cmd := exec.Command("bd", "daemon", "--health", "--json")
    out, err := cmd.Output()
    if err != nil {
        return nil, err
    }

    var health DaemonHealth
    json.Unmarshal(out, &health)
    return &health, nil
}

func (d *BDDaemon) Metrics() (*DaemonMetrics, error) {
    cmd := exec.Command("bd", "daemon", "--metrics", "--json")
    out, err := cmd.Output()
    if err != nil {
        return nil, err
    }

    var metrics DaemonMetrics
    json.Unmarshal(out, &metrics)
    return &metrics, nil
}
```

### Integration: Auto-Start on Spawn

```go
// internal/cli/spawn.go - UPDATED

func spawnSession(ctx context.Context, opts SpawnOptions) (*Session, error) {
    // 1. Start BD daemon if not running
    if opts.BDDaemon {
        // AutoPush is disabled by default; only enable if explicitly approved
        bdDaemon := bd.NewBDDaemon(bd.BDDaemonOptions{
            AutoCommit: true,
            AutoPush:   opts.AllowAutoPush && policyAllows("auto_push"),
            Interval:   5 * time.Second,
        })
        if err := bdDaemon.Start(ctx); err != nil {
            log.Printf("Warning: Failed to start BD daemon: %v", err)
        }
    }

    // 2. Continue with spawn...
}
```

### New NTM Commands

```bash
# BD daemon control
ntm beads daemon start
ntm beads daemon stop
ntm beads daemon status
ntm beads daemon health
ntm beads daemon metrics

# Spawn with daemon (default: enabled)
ntm spawn myproject --cc=2 --bd-daemon=true
```

---

# Part III-V: [Existing Sections]

*[The following sections remain from the previous version of this document and provide additional context. They are included for completeness but represent Tier 1-3 integrations rather than the Tier 0 critical items above.]*

---

## UNDEREXPLORED: bv (Beads Viewer) Robot Modes

*[Previous detailed section on bv robot modes - see Part II: CRITICAL: BV Mega-Commands for the updated, more complete treatment]*

The key insight from further research is that **-robot-triage replaces 4 separate calls** and should be the primary interface.

---

## UNDEREXPLORED: CASS Historical Context Injection

### The Opportunity

CASS indexes **50K+ sessions** across **11 different agent types** with sub-60ms search. NTM could inject relevant historical context before spawning agents, so they don't reinvent solutions.

### CASS Capabilities

| Feature | Description |
|---------|-------------|
| **Multi-agent indexing** | Claude, Codex, Cursor, Aider, Roo, Cline, Windsurf, etc. |
| **Full-text search** | Search across all session content |
| **Semantic search** | Embedding-based similarity search |
| **Hybrid search** | Combined full-text + semantic |
| **Multi-machine** | Unified index across multiple development machines |

### Integration: Pre-Task Context Enrichment

```go
// internal/context/historical.go

// CASSMatch represents a single search result from CASS
type CASSMatch struct {
    SessionID   string    `json:"session_id"`
    AgentType   string    `json:"agent_type"`
    Timestamp   time.Time `json:"timestamp"`
    Score       float64   `json:"score"`
    Snippet     string    `json:"snippet"`
    FilesEdited []string  `json:"files_edited,omitempty"`
}

// HistoricalContext contains CASS search results
type HistoricalContext struct {
    Query   string      `json:"query"`
    Matches []CASSMatch `json:"matches"`
}

// searchHistoricalContext searches CASS for relevant past sessions
func searchHistoricalContext(task string, limit int) ([]CASSMatch, error) {
    cmd := exec.Command("cass", "search",
        "--query", task,
        "--limit", fmt.Sprintf("%d", limit),
        "--mode", "hybrid",
        "--json",
    )

    out, err := cmd.Output()
    if err != nil {
        log.Printf("CASS search failed (continuing without history): %v", err)
        return nil, nil  // Graceful degradation
    }

    var ctx HistoricalContext
    if err := json.Unmarshal(out, &ctx); err != nil {
        return nil, err
    }
    return ctx.Matches, nil
}

// Revision: Context Pack builder composes CASS + CM + bv + s2p
// and enforces token budgets per agent type.
//
// Output is a single artifact stored in state store and referenced by correlation_id.

type ContextPack struct {
    ID           string            `json:"id"`
    BeadID       string            `json:"bead_id"`
    AgentType    string            `json:"agent_type"`
    CreatedAt    time.Time         `json:"created_at"`
    TokenCount   int               `json:"token_count"`

    // Components
    Triage       *bv.TriageResult  `json:"triage,omitempty"`
    CMRules      []cm.Rule         `json:"cm_rules,omitempty"`
    CASSHistory  []CASSMatch       `json:"cass_history,omitempty"`
    S2PContext   string            `json:"s2p_context,omitempty"`

    // Rendered
    RenderedPrompt string          `json:"rendered_prompt"`
}

// truncateToTokenBudget truncates data to fit within a token budget
func truncateToTokenBudget(data interface{}, maxTokens int) interface{} {
    jsonBytes, _ := json.Marshal(data)
    currentTokens := len(jsonBytes) / 4  // ~4 chars per token

    if currentTokens <= maxTokens {
        return data
    }

    // For slices, progressively remove elements from the end
    switch v := data.(type) {
    case []CASSMatch:
        for len(v) > 0 && estimateTokens(v) > maxTokens {
            v = v[:len(v)-1]
        }
        return v
    case []cm.Rule:
        for len(v) > 0 && estimateTokens(v) > maxTokens {
            v = v[:len(v)-1]
        }
        return v
    }
    return data
}

// BuildContextPack composes all context sources into a single artifact
func BuildContextPack(ctx context.Context, task string, beadID string, agentType string) (*ContextPack, error) {
    pack := &ContextPack{
        ID:        fmt.Sprintf("ctx-%s-%d", beadID, time.Now().UnixNano()),
        BeadID:    beadID,
        AgentType: agentType,
        CreatedAt: time.Now(),
    }

    // Token budgets by agent type
    budgets := map[string]int{
        "claude": 180000,
        "codex":  120000,
        "gemini": 100000,
    }
    budget := budgets[agentType]

    // Budget allocation per component
    triageBudget := budget * 10 / 100   // 10%
    cmBudget := budget * 5 / 100        // 5%
    cassBudget := budget * 15 / 100     // 15%
    s2pBudget := budget * 70 / 100      // 70%

    // 1) BV triage/impact (10% of budget, truncated if needed)
    triage, _ := bv.GetTriage(ctx)
    if triage != nil {
        pack.Triage = truncateToTokenBudget(triage, triageBudget).(*bv.TriageResult)
    }

    // 2) CM rules via daemon (5% of budget, truncated if needed)
    if cmClient != nil {
        result, _ := cmClient.GetContext(ctx, task)
        if result != nil {
            pack.CMRules = truncateToTokenBudget(result.RelevantBullets, cmBudget).([]cm.Rule)
        }
    }

    // 3) CASS history - hybrid search (15% of budget, limited results)
    maxCASSResults := cassBudget / 500  // ~500 tokens per match estimate
    if maxCASSResults < 1 {
        maxCASSResults = 1
    }
    pack.CASSHistory, _ = searchHistoricalContext(task, maxCASSResults)

    // 4) S2P file context (remaining 70% budget, enforced by s2p)
    files := predictAffectedFiles(bv.BeadPreview{ID: beadID, Title: task})
    pack.S2PContext, _ = prepareContext(files, s2pBudget)

    // 5) Render via per-agent template
    pack.RenderedPrompt = renderContextTemplate(pack, agentType)
    pack.TokenCount = estimateTokens(pack.RenderedPrompt)

    // 6) Validate total doesn't exceed budget (final safety check)
    if pack.TokenCount > budget {
        log.Printf("Warning: ContextPack exceeds budget (%d > %d), truncating S2P", pack.TokenCount, budget)
        excess := pack.TokenCount - budget
        pack.S2PContext = truncateString(pack.S2PContext, len(pack.S2PContext) - excess*4)
        pack.RenderedPrompt = renderContextTemplate(pack, agentType)
        pack.TokenCount = estimateTokens(pack.RenderedPrompt)
    }

    // 7) Cache by (repo_rev, beadID, agentType)
    cacheContextPack(pack)

    return pack, nil
}
```

---

## HIGH-LEVERAGE: Workspace Isolation via Git Worktrees

### Motivation

File reservations prevent many conflicts, but Git worktrees **dramatically reduce blast radius**:
- Agents can safely operate in parallel even on overlapping files
- Destructive commands are isolated to one worktree
- Coordinator can merge work on clean boundaries
- Live conflicts become merge-time conflicts (much safer)

### Integration: Per-Agent Worktrees

On spawn with `--worktrees`, create:
`.ntm/worktrees/<agentName>/` on branch `ntm/<session>/<agentName>`

```go
// internal/worktrees/worktrees.go - NEW FILE

type WorktreeManager struct {
    projectPath string
    session     string
}

func (m *WorktreeManager) CreateForAgent(agentName string) (string, error) {
    worktreePath := filepath.Join(m.projectPath, ".ntm", "worktrees", agentName)
    branchName := fmt.Sprintf("ntm/%s/%s", m.session, agentName)

    // Create branch from current HEAD
    cmd := exec.Command("git", "worktree", "add", "-b", branchName, worktreePath)
    cmd.Dir = m.projectPath
    if err := cmd.Run(); err != nil {
        return "", err
    }

    return worktreePath, nil
}

func (m *WorktreeManager) Cleanup() error {
    worktreesPath := filepath.Join(m.projectPath, ".ntm", "worktrees")
    entries, _ := os.ReadDir(worktreesPath)
    for _, e := range entries {
        wtPath := filepath.Join(worktreesPath, e.Name())
        exec.Command("git", "worktree", "remove", wtPath).Run()
    }
    return nil
}
```

### New Commands

```bash
ntm spawn myproject --cc=3 --worktrees   # Each agent gets isolated worktree
ntm worktrees list                        # List active worktrees
ntm worktrees merge GreenLake             # Merge agent's work back
ntm worktrees clean --session myproject   # Cleanup all worktrees
```

### Interaction with Reservations

With worktrees enabled:
- Reservations become "soft coordination" rather than "hard safety"
- Coordinator uses reservations to reduce merge conflicts, not to prevent live overwrites
- Agents can work on same files; merge happens at commit boundaries

---

## UNDEREXPLORED: s2p (Source-to-Prompt) Context Preparation

### The Opportunity

s2p converts source code to LLM-ready prompts with **real-time token counting**. This prevents context overflow.

### Integration: Token-Budgeted Context

```go
// internal/context/s2p.go

// prepareAgentContext prepares context for an agent with budget enforcement
func prepareAgentContext(files []string, agentType string) (*S2POutput, error) {
    budgets := map[string]int{
        "claude": 180000,
        "codex":  120000,
        "gemini": 100000,
    }

    return prepareContext(S2PConfig{
        Files:       files,
        TokenBudget: budgets[agentType],
        IncludeTree: true,
        Format:      "xml",
    })
}
```

---

## UNDEREXPLORED: UBS Dashboard & Agent Notifications

### The Opportunity

UBS is already integrated but **dashboard integration** and **agent notifications** are minimal.

### Integration: Agent Bug Notifications

```go
// internal/monitor/ubs_notify.go

// notifyAgents sends bug findings to relevant agents
func (n *BugNotifier) notifyAgents(findings []UBSFinding) {
    byFile := make(map[string][]UBSFinding)
    for _, f := range findings {
        byFile[f.File] = append(byFile[f.File], f)
    }

    panes, _ := tmux.GetPanes(n.session)
    for _, pane := range panes {
        agentFiles := detectAgentWorkingFiles(pane.ID)
        for file, fileFindings := range byFile {
            if contains(agentFiles, file) {
                sendBugNotification(pane, file, fileFindings)
            }
        }
    }
}
```

---

## Ecosystem Discovery: Additional Tools

Research identified **21 total projects** in the ecosystem:

### Tier 1: Core Tools (8)
NTM, Agent Mail, UBS, bv/bd, CASS, CM, CAAM, SLB

### Tier 2: Valuable (3)
| Tool | Purpose | Integration Value |
|------|---------|------------------|
| **misc_coding_agent_tips_and_scripts** | Battle-tested patterns | **Destructive cmd protection** |
| **s2p** | Context preparation | Token budgeting |
| **chat_shared_conversation_to_file** | Conversation export | Post-mortem analysis |

### Tier 3: Supporting (10+)
llm_price_arena, project_to_jsonl, repo_to_llm_prompt, etc.

---

## Foundations: Durable State + Event Log

### Why This Is Required for Reliability

NTM currently "knows" everything by actively polling tmux and tools. That fails hard if:
- NTM crashes
- The terminal closes
- A daemon is restarted
- Tool output changes or is temporarily unavailable

A durable store enables:
- **Resume / re-attach** to sessions after crash
- **Dashboard** built on stored state (not constant polling)
- **Auditability** (who force-released what, with correlation IDs)
- **Performance** (cache tool responses; store last-known states)

### State Store Schema (SQLite)

NTM should store all orchestration-critical data in a local durable store:

```sql
-- Sessions
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    project_path TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    status TEXT NOT NULL  -- active, paused, terminated
);

-- Agents
CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    session_id TEXT REFERENCES sessions(id),
    name TEXT NOT NULL,  -- e.g., "GreenLake"
    type TEXT NOT NULL,  -- cc, cod, gmi
    model TEXT,
    tmux_pane_id TEXT,
    last_seen TIMESTAMP,
    status TEXT NOT NULL  -- idle, working, error, crashed
);

-- Tasks/Assignments
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    session_id TEXT REFERENCES sessions(id),
    agent_id TEXT REFERENCES agents(id),
    bead_id TEXT,
    correlation_id TEXT,  -- Links to reservations, messages
    status TEXT NOT NULL,
    created_at TIMESTAMP,
    completed_at TIMESTAMP
);

-- Reservations
CREATE TABLE reservations (
    id INTEGER PRIMARY KEY,
    session_id TEXT REFERENCES sessions(id),
    agent_id TEXT REFERENCES agents(id),
    path_pattern TEXT NOT NULL,
    exclusive BOOLEAN NOT NULL,
    correlation_id TEXT,
    expires_at TIMESTAMP,
    released_at TIMESTAMP,
    force_released_by TEXT
);

-- Tool Health Snapshots
CREATE TABLE tool_health (
    tool TEXT PRIMARY KEY,
    version TEXT,
    capabilities TEXT,  -- JSON array
    last_ok TIMESTAMP,
    last_error TEXT
);
```

### Event Log (JSONL)

NTM should also append a lightweight event log for replay/debugging:

```jsonl
{"ts":"2025-01-03T10:00:00Z","event":"session.spawned","session":"myproject","agents":["GreenLake","BlueDog"]}
{"ts":"2025-01-03T10:00:01Z","event":"agent.spawned","agent":"GreenLake","type":"cc","model":"opus"}
{"ts":"2025-01-03T10:00:05Z","event":"reservation.granted","agent":"GreenLake","pattern":"internal/auth/**","correlation":"task-123"}
{"ts":"2025-01-03T10:00:10Z","event":"task.assigned","agent":"GreenLake","bead":"ntm-abc","correlation":"task-123"}
{"ts":"2025-01-03T10:30:00Z","event":"command.blocked","agent":"GreenLake","command":"git checkout --","reason":"destructive"}
{"ts":"2025-01-03T11:00:00Z","event":"reservation.released","agent":"GreenLake","pattern":"internal/auth/**"}
```

Event types:
- `session.spawned`, `session.terminated`
- `agent.spawned`, `agent.crashed`, `agent.rotated`
- `task.assigned`, `task.completed`, `task.failed`
- `reservation.granted`, `reservation.conflicted`, `reservation.released`, `reservation.force_released`
- `command.blocked`, `approval.requested`, `approval.granted`

This enables: 1) Crash-safe recovery, 2) Faster UI, 3) Audits, 4) Deterministic testing.

---

## Priority Matrix

### Updated Priority Matrix with Tier 0

```
                              CRITICAL IMPACT
                                    │
        ┌───────────────────────────┼───────────────────────────┐
        │                           │                           │
        │  Agent Mail Macros ●      │      ● File Reservation   │
        │  (1 call vs 4-5)          │        Lifecycle          │
        │                           │                           │
        │  BV -robot-triage ●       │      ● CM Server Mode     │
        │  (1 call vs 4)            │        (HTTP daemon)      │
        │                           │                           │
        │  Destructive Cmd ●        │      ● Session Coord      │
        │  Protection               │        Intelligence       │
        │                           │                           │
   LOW ─┼───────────────────────────┼───────────────────────────┼─ HIGH
 EFFORT │                           │                           │ EFFORT
        │                           │                           │
        │  BD Message ●             │      ● CASS Historical    │
        │  Integration              │        Context            │
        │                           │                           │
        │  BD Daemon Mode ●         │      ● s2p Context        │
        │                           │        Preparation        │
        │                           │                           │
        │  BV -robot-markdown ●     │      ● CAAM Integration   │
        │  (50% token savings)      │                           │
        │                           │                           │
        └───────────────────────────┼───────────────────────────┘
                                    │
                              MEDIUM IMPACT
```

### Implementation Tiers (Updated)

#### Tier 0: CRITICAL - Zero Usage, Maximum Impact (Do FIRST)

| Integration | Effort | Impact | Why |
|-------------|--------|--------|-----|
| **Agent Mail Macros** | Very Low | Critical | One call replaces 4-5 |
| **BV -robot-triage** | Very Low | Critical | One call replaces 4 |
| **Destructive Cmd Protection** | Low | Critical | Prevents data loss |
| **File Reservation Lifecycle** | Low | Critical | Prevents conflicts |
| **CM Server Mode** | Low | High | Eliminates subprocess overhead |
| **Session Coordinator Intelligence** | Medium | High | Active vs passive coordination |
| **BD Message Integration** | Low | Medium | Unified messaging |
| **BD Daemon Mode** | Very Low | Medium | Background sync |
| **BV -robot-markdown** | Very Low | Medium | 50% token savings |

#### Tier 1: Underexplored - High Value (Do Next)

| Integration | Effort | Impact | Why |
|-------------|--------|--------|-----|
| **CASS Historical Context** | Medium | High | Agents learn from history |
| **s2p Context Preparation** | Medium | Medium | Prevents context overflow |
| **UBS Notifications** | Low | Medium | Bug awareness |
| **BV Remaining Modes** | Low | Medium | 33 more modes available |

#### Tier 2-3: Planned (Do Later)

| Integration | Effort | Impact |
|-------------|--------|--------|
| **CAAM** | Medium | Medium |
| **CM Memory Rules** | High | Medium |
| **SLB Safety** | Medium | Medium |

---

## Implementation Roadmap (Updated)

### Phase -1: Foundations (Do FIRST; enables everything else)

These foundational components make all Tier 0 integrations faster and safer to implement:

- [ ] **Tool Adapter Framework** (`internal/tools/`)
  - Detect(), Version(), Capabilities(), Health() for each tool
  - Schema guards for JSON responses
  - Automatic fallback when capabilities missing
  - Cache results per session

- [ ] **Daemon Supervisor** (`internal/supervisor/`)
  - Port allocation + PID files
  - Log capture to `.ntm/logs/`
  - Health checks + exponential backoff restart
  - Clean shutdown on session end

- [ ] **Durable State Store** (`internal/state/`)
  - SQLite schema for sessions, agents, tasks, reservations, messages
  - Correlation IDs for traceability
  - Event log (JSONL) for replay/debugging

- [ ] **Event Bus** (`internal/events/`)
  - Pub/sub for session lifecycle events
  - Subscriptions for TUI, robot API, web dashboard
  - Replay from event log for crash recovery

- [ ] **`ntm doctor` Baseline Checks**
  - Tool detection (bv, bd, am, cm, cass, s2p)
  - Version compatibility
  - Daemon health
  - tmux version and configuration
  - PATH wrapper status

### Phase 0: Critical Tier 0 (Highest Priority)

**Agent Mail Macros**
- [ ] Implement `macro_start_session` wrapper
- [ ] Implement `macro_prepare_thread` wrapper
- [ ] Update spawn workflow to use macros
- [ ] Test one-call vs multi-call performance

**BV Mega-Commands**
- [ ] Implement `-robot-triage` integration
- [ ] Replace 4-call pattern with 1-call
- [ ] Add `-robot-markdown` for token savings
- [ ] Update assign workflow

**Destructive Command Protection**
- [ ] Create safety hook script
- [ ] Implement auto-install during spawn
- [ ] Add blocked command logging
- [ ] Test with common destructive patterns

**File Reservation Lifecycle**
- [ ] Implement reserve-before-assign
- [ ] Implement release-after-complete
- [ ] Implement force-release for stale (with approval workflow)
- [ ] Add pre-commit guard installation

### Phase 1: Remaining Tier 0 (High Priority)

**CM Server Mode**
- [ ] Implement daemon launcher (via Supervisor)
- [ ] Create HTTP client
- [ ] Add outcome feedback
- [ ] Test performance improvement

**Session Coordinator Intelligence**
- [ ] Implement active monitoring
- [ ] Add digest generation
- [ ] Implement conflict resolution
- [ ] Add work assignment with score-based scheduling

**BD Integration**
- [ ] Implement BD message client
- [ ] Implement BD daemon control (via Supervisor)
- [ ] Add unified messaging with deduplication
- [ ] Auto-start daemon on spawn

### Phase 2: Tier 1 Integrations

- [ ] CASS historical context injection
- [ ] s2p context preparation with token budgets
- [ ] UBS agent notifications
- [ ] Remaining BV robot modes
- [ ] Git worktree isolation (optional, high-leverage)

### Phase 3: Tier 2-3 Integrations

- [ ] CAAM account management
- [ ] CM memory rule injection
- [ ] SLB safety gates

---

## Success Metrics (Updated)

### Tier 0 Metrics

| Metric | Baseline | Target | Measurement |
|--------|----------|--------|-------------|
| Agent bootstrap calls | 4-5 per agent | 1 per agent | API call count |
| BV triage calls | 4 per analysis | 1 per analysis | Command count |
| Destructive cmd incidents | Unknown | 0 | Blocked log |
| File conflicts | Unknown | 0 | Conflict log |
| CM query latency | ~500ms (subprocess) | <50ms (HTTP) | Timing |
| Coordinator active features | 0 | 8 | Feature count |
| Token usage (markdown) | 100% | 50% | Token count |

### Overall Metrics

| Metric | Target | Measurement |
|--------|--------|-------------|
| Time to first working session | <1 minute | User testing |
| Agent coordination failures | <1% | Error logs |
| Work assignment efficiency | >90% match | Completion rates |
| Cross-agent conflicts | 0 | Conflict count |

---

## Conclusion

This comprehensive plan identifies **9 Tier 0 critical integrations** that have **zero current usage** (or effectively zero, in the case of token-efficiency) despite being designed specifically for agent coordination:

1. **Agent Mail Macros** - One call replaces 4-5 separate calls
2. **File Reservation Lifecycle** - Prevents multi-agent conflicts
3. **BV Mega-Commands** - `-robot-triage` replaces 4 calls
4. **CM Server Mode** - HTTP daemon eliminates subprocess overhead
5. **Destructive Command Protection** - Mechanical enforcement of safety
6. **Session Coordinator Intelligence** - Active vs passive coordination
7. **BD Message Integration** - Unified messaging through beads
8. **BD Daemon Mode** - Background sync for all agents
9. **BV -robot-markdown** - Token-efficient triage/context for smaller-context agents

These Tier 0 integrations, combined with the Tier 1 underexplored features (CASS, s2p, UBS notifications, remaining bv modes) and planned Tier 2-3 integrations (CAAM, CM, SLB), will transform NTM from a session manager into an **intelligent orchestrator** that:

- **Bootstraps agents efficiently** (macros)
- **Prevents file conflicts** (reservations)
- **Analyzes work optimally** (bv mega-commands)
- **Queries memory fast** (CM daemon)
- **Protects against accidents** (destructive cmd hooks)
- **Coordinates actively** (intelligent coordinator)
- **Messages seamlessly** (unified messaging)
- **Syncs continuously** (bd daemon)

The result is a closed-loop system where each cycle compounds, making the entire development flywheel spin faster and more reliably.

---

## Test Strategy (Required for Reliability)

Orchestrators fail at the seams. The test strategy must specifically cover integration boundaries and failure modes.

### 1) Tool Contract Tests (when tools are installed)

These run in CI when the full ecosystem is available:

```go
// internal/tools/contract_test.go

func TestBVVersionParsing(t *testing.T) {
    adapter := NewBVAdapter()
    version, err := adapter.Version()
    require.NoError(t, err)
    require.True(t, version.GreaterOrEqual("0.30.0"))
}

func TestBVCapabilityDetection(t *testing.T) {
    adapter := NewBVAdapter()
    caps, err := adapter.Capabilities()
    require.NoError(t, err)
    require.Contains(t, caps, "robot-triage")
}

func TestBVTriageSchema(t *testing.T) {
    adapter := NewBVAdapter()
    result, err := adapter.GetTriage(context.Background())
    require.NoError(t, err)

    // Golden test - validate schema hasn't drifted
    golden := loadGolden(t, "testdata/bv_triage_schema.json")
    validateSchema(t, result, golden)
}
```

### 2) Deterministic Fake Tools (always in CI)

Fake binaries in `testdata/faketools/` that simulate tool behavior:

```bash
# testdata/faketools/bv - Fake bv for testing
#!/bin/bash

# Handle combined arguments properly
ALL_ARGS="$*"

case "$ALL_ARGS" in
    *"-robot-triage"*)
        cat testdata/fixtures/bv_triage_response.json
        ;;
    *"-robot-markdown"*)
        cat testdata/fixtures/bv_markdown_response.md
        ;;
    *"-robot-alerts"*)
        cat testdata/fixtures/bv_alerts_response.json
        ;;
    "--version"|"-v")
        echo "bv version 0.31.0 (fake)"
        ;;
    *)
        echo "Unknown command: $ALL_ARGS" >&2
        exit 1
        ;;
esac
```

Test scenarios:
- Normal operation
- Timeout (sleep forever)
- Partial output (truncated JSON)
- Schema change (different field names)
- Non-zero exit codes
- Missing binary

### 3) Daemon Chaos Tests

Test supervisor resilience:

```go
func TestDaemonRestartOnCrash(t *testing.T) {
    sup := NewSupervisor("test-session", t.TempDir())

    // Start daemon
    daemon, err := sup.Start(ctx, cmSpec)
    require.NoError(t, err)

    // Kill it unexpectedly
    daemon.cmd.Process.Kill()

    // Wait for supervisor to detect and restart
    time.Sleep(5 * time.Second)

    // Verify restarted
    require.True(t, daemon.healthy)
    require.Equal(t, 1, daemon.restarts)
}

func TestPortCollision(t *testing.T) {
    // Start something on the default port
    listener, _ := net.Listen("tcp", ":8765")
    defer listener.Close()

    sup := NewSupervisor("test-session", t.TempDir())
    daemon, err := sup.Start(ctx, cmSpec)
    require.NoError(t, err)

    // Should have chosen a different port
    require.NotEqual(t, 8765, daemon.port)
}

func TestHealthCheckFlapping(t *testing.T) {
    // Daemon that intermittently fails health checks
    // Verify supervisor doesn't restart too aggressively
}
```

### 4) End-to-End Session Tests

Full integration tests with real tmux:

```go
func TestFullSessionLifecycle(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping E2E test")
    }

    // Spawn session
    session, err := SpawnSession(ctx, SpawnOptions{
        Name:   "e2e-test",
        CC:     1,
        Safety: true,
    })
    require.NoError(t, err)
    defer session.Kill()

    // Verify agent registered in state store
    agents, _ := stateStore.GetAgents(session.ID)
    require.Len(t, agents, 1)

    // Verify events emitted
    events := eventLog.Since(session.CreatedAt)
    require.Contains(t, eventTypes(events), "session.spawned")
    require.Contains(t, eventTypes(events), "agent.spawned")

    // Verify recovery after simulated crash
    stateStore.Close()
    stateStore = state.Open(dbPath)
    recovered, _ := stateStore.GetSession(session.ID)
    require.Equal(t, "active", recovered.Status)
}
```

### 5) Policy Enforcement Tests

```go
func TestBlockedCommandLogged(t *testing.T) {
    // Execute blocked command through wrapper
    // Verify logged to .ntm/logs/blocked.jsonl
    // Verify event emitted
}

func TestApprovalWorkflow(t *testing.T) {
    // Execute approval_required command
    // Verify blocked without approval
    // Set NTM_APPROVED=1
    // Verify command proceeds
}
```

---

*Document generated: 2026-01-03*
*NTM Version: v1.3.0*
*Ecosystem: Dicklesworthstone Stack v1.0*
*Research depth: Tier 0 Critical Discovery + Architectural Review*
