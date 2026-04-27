# NTM Web Platform: REST API, WebSocket, and World-Class UI

> **A Comprehensive Plan for Transforming NTM into a Full-Stack Multi-Agent Orchestration Platform**
>
> *Integrating the Complete Agent Flywheel Ecosystem: Agent Mail, BV, UBS, CASS, CM, CAAM, SLB*

---

## North-Star Vision

NTM becomes a **multi-agent command center that lives everywhere**:

- **Terminal-first** (CLI + TUI) for power users and SSH
- **Web-first** (desktop + mobile) for visibility, orchestration, and "at-a-glance" control
- **API-first** (REST + WebSocket) so humans *and agents* can automate anything

### Non-Negotiable Requirement

> **Anything you can do today in NTM (CLI + TUI) must be possible via REST.**
> No "hidden features" in the terminal. No drift. No "you can only do that in the TUI."

---

## Executive Summary

This document outlines a comprehensive plan to extend NTM (Named Tmux Manager) from a terminal/TUI application into a full-featured web platform. NTM is the **orchestration backbone** of the Agent Flywheel—a self-improving development cycle where AI coding agents work in parallel, coordinate via messaging, and compound their learnings over time.

The architecture introduces three new layers:

1. **REST API Layer** — A performant, well-documented HTTP API replicating 100% of CLI/TUI functionality across **all 8 flywheel tools**
2. **WebSocket Layer** — Real-time bidirectional streaming for logs, events, agent interactions, file reservations, scanner results, and memory updates
3. **Web UI Layer** — A world-class Next.js 16 / React 19 interface with Stripe-level polish, providing unified access to the entire flywheel ecosystem

The design prioritizes:
- **Mechanical parity** — Command Kernel ensures API = CLI capability surface
- **Flywheel acceleration** — Every feature designed to make the virtuous cycle spin faster
- **Full ecosystem integration** — Agent Mail, BV, UBS, CASS, CM, CAAM, SLB unified under one UI
- **Real-time capability** — Sub-100ms event propagation across all tools
- **Developer experience** — OpenAPI 3.1 spec with rich examples for AI agent consumption
- **Visual excellence** — Desktop and mobile-optimized UX with separate interaction paradigms

---

## Table of Contents

1. [Product Outcomes](#1-product-outcomes)
2. [Ground Truth: How NTM Actually Works Today](#2-ground-truth-how-ntm-actually-works-today)
3. [The Agent Flywheel Philosophy](#3-the-agent-flywheel-philosophy)
4. [Research Findings](#4-research-findings)
5. [Architecture Overview](#5-architecture-overview)
6. [Command Kernel: The Parity Guarantee](#6-command-kernel-the-parity-guarantee)
7. [REST API Layer](#7-rest-api-layer)
8. [WebSocket Layer](#8-websocket-layer)
9. [Agent Mail Deep Integration](#9-agent-mail-deep-integration)
10. [Beads & BV Integration](#10-beads--bv-integration)
11. [CASS & Memory System Integration](#11-cass--memory-system-integration)
12. [UBS Scanner Integration](#12-ubs-scanner-integration)
13. [CAAM Account Management](#13-caam-account-management)
14. [SLB Safety Guardrails](#14-slb-safety-guardrails)
15. [Pipeline & Workflow Engine](#15-pipeline--workflow-engine)
16. [Web UI Layer](#16-web-ui-layer)
17. [Desktop vs Mobile UX Strategy](#17-desktop-vs-mobile-ux-strategy)
18. [Agent SDK Integration Strategy](#18-agent-sdk-integration-strategy)
19. [Security Model](#19-security-model)
20. [Testing Strategy](#20-testing-strategy)
21. [Risk Register & Mitigations](#21-risk-register--mitigations)
22. [Implementation Phases](#22-implementation-phases)
23. [File Structure](#23-file-structure)
24. [Technical Specifications](#24-technical-specifications)
25. [Appendix A: Complete CLI/REST Parity Matrix](#appendix-a-complete-clirest-parity-matrix)
26. [Appendix B: Robot Mode Parity Matrix](#appendix-b-robot-mode-parity-matrix)

---

## 1. Product Outcomes

### 1.1 Outcomes for Humans

1. **One-page clarity:**
   In < 10 seconds, you can answer:
   - Which sessions exist and where?
   - Which agents are active / stalled / erroring?
   - Which panes are producing output now?
   - Where are conflicts forming?
   - Which commands were recently run?

2. **"Stripe-level" UI polish and confidence:**
   The UI should feel inevitable, crisp, and *calm*—even while coordinating chaos.

3. **Mobile becomes genuinely useful (not "just a viewer"):**
   - Triage alerts, restart agents, broadcast prompts, view recent output, resolve conflicts
   - Do all that safely (RBAC + approvals)

### 1.2 Outcomes for Agents / Automation

1. **OpenAPI that teaches itself**
   - Every endpoint has: clear description, realistic examples, error cases, when/why to use it
   - Agents should be able to "just read the spec" and act correctly

2. **WebSocket stream as a universal feed**
   - Pane output, agent activity states, tool calls/results
   - Notifications, file changes + conflicts, checkpoints + history
   - All in a consistent event envelope with replay/resume

---

## 2. Ground Truth: How NTM Actually Works Today

This section maps actual code paths. These are the foundations we must reuse, not replace.

### 2.1 CLI + Robot Entry Points
- CLI root + command tree: `internal/cli/root.go`
- Robot JSON mode: `internal/robot/*` (invoked from root run path)
- Key detail: the root command already short-circuits to robot mode for non-interactive usage. This is the **closest existing API surface** and should be mapped 1:1 into REST.

### 2.2 tmux Core Layer
- tmux client abstraction: `internal/tmux/client.go`
- Session/pane parsing & operations: `internal/tmux/session.go`
- Pane title parsing encodes agent type/variant/tags. This is the **canonical identity model**.

### 2.3 State Store (SQLite)
- Store API: `internal/state/store.go`
- Schema types: `internal/state/schema.go`
- Key entities already exist and should be exposed via REST:
  - Sessions, Agents, Tasks, Reservations, Approvals, ContextPacks, ToolHealth

### 2.4 Event Bus
- `internal/events/bus.go`
- Provides in-memory pub/sub + ring buffer history
- This is the natural source for WS streaming
- Must be extended for event persistence and replay

### 2.5 Tool Adapter Framework (Flywheel Integration)
- `internal/tools/*` includes adapters for bv, bd, am, cm, cass, s2p
- `internal/cli/doctor.go` already checks tool health
- This adapter registry is an excellent foundation for API + UI tool health panels

### 2.6 Context Pack Builder (Flywheel Brain)
- `internal/context/pack.go` builds context packs from BV/CM/CASS/S2P
- Token budgets per agent type; component allocation (triage/cm/cass/s2p)
- Persists packs into state store and renders prompt format per agent type

### 2.7 Scanner + UBS → Beads Bridge
- `internal/cli/scan.go` + `internal/scanner/*`
- UBS results can auto-create beads (issue tracking), dedupe, update/close
- This is a **hard flywheel loop**: Scan → Beads → BV → Context → Send

### 2.8 Supervisor + Daemons
- `internal/supervisor/supervisor.go` manages long-running daemons (cm server, bd daemon)
- Tracks PID, ports, health checks, restarts, logs
- This must be surfaced in API + UI for operational visibility

### 2.9 Approvals + SLB (Two-Person Rule)
- `internal/approval/engine.go` enforces approvals and SLB
- `internal/policy/policy.go` defines blocking/approval rules
- Approval queue + SLB decisioning should be first-class API & UI features

### 2.10 Existing Serve Mode
- `internal/serve/server.go` - Basic HTTP server with SSE
- Endpoints: sessions list/details, robot stubs, SSE `/events`
- This is **not** feature-complete. We will expand it to be the full API server.

---

## 3. The Agent Flywheel Philosophy

### 3.1 What Is The Agent Flywheel?

The Agent Flywheel is a **self-improving development cycle** where:

```
┌─────────────────────────────────────────────────────────────────┐
│                    THE AGENT FLYWHEEL                           │
│                                                                 │
│         ┌─────────┐                                            │
│         │  PLAN   │◄────────────────────────────────┐          │
│         │  (BV)   │                                 │          │
│         └────┬────┘                                 │          │
│              │                                      │          │
│              ▼                                      │          │
│         ┌─────────┐                                 │          │
│         │COORDINATE                                 │          │
│         │(Agent   │                                 │          │
│         │ Mail)   │                                 │          │
│         └────┬────┘                                 │          │
│              │                                      │          │
│              ▼                                      │          │
│         ┌─────────┐         ┌─────────┐            │          │
│         │ EXECUTE │────────▶│  SCAN   │            │          │
│         │ (NTM +  │         │  (UBS)  │            │          │
│         │ Agents) │         └────┬────┘            │          │
│         └─────────┘              │                 │          │
│                                  ▼                 │          │
│                             ┌─────────┐            │          │
│                             │REMEMBER │────────────┘          │
│                             │(CASS+CM)│                       │
│                             └─────────┘                       │
│                                                                 │
│  Each cycle is better than the last because:                   │
│  • Memory improves (CM gets smarter)                           │
│  • Sessions are searchable (find past solutions)               │
│  • Agents coordinate (no duplicated work)                      │
│  • Quality gates enforce standards (UBS)                       │
│  • Context is preserved (Agent Mail + CM)                      │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### 3.2 The Eight Tools of the Flywheel

| # | Tool | Purpose | Integration Priority |
|---|------|---------|---------------------|
| 1 | **NTM** | Session orchestration & agent spawning | Core (existing) |
| 2 | **Agent Mail** | Agent messaging & file coordination | Critical |
| 3 | **BV** | Task management & graph analysis | Critical |
| 4 | **UBS** | Code quality scanning | High |
| 5 | **CASS** | Session history search & indexing | High |
| 6 | **CM** | Procedural memory for agents | High |
| 7 | **CAAM** | Authentication credential rotation | Medium |
| 8 | **SLB** | Safety guardrails (two-person rule) | Medium |

### 3.3 How The Web UI Accelerates The Flywheel

The web UI transforms each phase:

| Phase | CLI Experience | Web UI Experience |
|-------|----------------|-------------------|
| **PLAN** | `bv` TUI, `bd ready` | Visual Kanban, dependency graph, drag-drop prioritization |
| **COORDINATE** | `am` commands, inbox polling | Real-time chat, file reservation map, @mentions |
| **EXECUTE** | `ntm spawn`, tmux attach | Visual agent grid, live terminals, one-click spawn |
| **SCAN** | `ubs .` output | Dashboard with severity charts, inline annotations |
| **REMEMBER** | `cm context`, `cass search` | Semantic search UI, memory timeline, rule browser |

### 3.4 Design Principle: Flywheel-First

Every feature should answer: **"Does this make the flywheel spin faster?"**

- ✅ Real-time file reservation map → Prevents conflicts, faster coordination
- ✅ Visual dependency graph → Better prioritization, faster planning
- ✅ Inline UBS annotations → Faster bug fixing, better quality
- ✅ Memory search UI → Faster context retrieval, better first attempts
- ❌ Pretty animations with no function → Slower page loads, distraction

---

## 4. Research Findings

### 4.1 Agent Client Protocol (ACP)

The [Agent Client Protocol](https://agentclientprotocol.com/) is an emerging open standard (Apache 2.0) created by Zed for connecting AI coding agents to editors/IDEs. Key findings:

- **JSON-RPC 2.0 based** — Bidirectional communication over stdio
- **Industry adoption** — JetBrains, Neovim, and Google (Gemini CLI reference implementation)
- **Complements MCP** — MCP handles data/tools; ACP handles agent-editor integration
- **Adapters available** — Open-source adapters for Claude Code, Codex, Gemini CLI
- **Remote support** — Work in progress; streamable HTTP transport is draft stage
- **MCP-friendly** — Re-uses MCP types and includes UX-oriented types like diff rendering

**Implication for NTM:** ACP provides a standardized way to communicate with agents that could eventually supplement or replace tmux-based text streaming. We should design the API to support both paradigms via an Agent Driver abstraction.

### 4.2 Official Agent SDKs

| SDK | Package | Version | Key Features |
|-----|---------|---------|--------------|
| **Claude Agent SDK** | `@anthropic-ai/claude-agent-sdk` | 0.1.76 | Async streaming, tool use, file ops |
| **OpenAI Codex SDK** | `@openai/codex-sdk` | Latest | JSONL events over stdin/stdout, thread persistence |
| **Google GenAI SDK** | `@google/genai` | 1.34.0 (GA) | 1M token context, MCP support |

**Implication:** We can offer a "direct SDK mode" as an alternative to tmux spawning, giving users choice between:
- **Tmux mode** — Current approach, battle-tested, visual terminal access
- **SDK mode** — Lower overhead, programmatic control, no tmux dependency

### 4.3 Next.js 16 / React 19.2

Released October 2025, Next.js 16 brings:

- **Turbopack stable** — 10× faster Fast Refresh (default in dev and build)
- **React Compiler 1.0** — Automatic memoization, zero manual optimization
- **React 19.2 features**:
  - `View Transitions` — Native animation between route changes
  - `Activity` — Background rendering with state preservation
  - `useEffectEvent` — Non-reactive Effect logic extraction
- **Cache Components** — Explicit `"use cache"` directive (opt-in caching)
- **Enhanced routing** — Layout deduplication, incremental prefetching

### 4.4 MCP Agent Mail Protocol

NTM's existing Agent Mail integration uses HTTP JSON-RPC to `localhost:8765`. Key capabilities:

- **Project & Agent Management**: `EnsureProject`, `RegisterAgent`, `CreateAgentIdentity`
- **Messaging**: `SendMessage`, `ReplyMessage`, `FetchInbox`, `SearchMessages`, `SummarizeThread`
- **File Reservations**: `ReservePaths`, `ReleaseReservations`, `RenewReservations`, `ForceReleaseReservation`
- **Contact Management**: `RequestContact`, `RespondContact`, `ListContacts`
- **Macros**: `StartSession`, `PrepareThread`, `ContactHandshake`
- **Overseer Mode**: `SendOverseerMessage` (bypass contact policies)
- **Pre-commit Guards**: `InstallPrecommitGuard`, `UninstallPrecommitGuard`

### 4.5 BV Robot Mode Commands

NTM integrates with BV for task management:

- `GetTriage()` — Comprehensive triage with scoring, recommendations, quick wins (30s cache)
- `GetInsights()` — Graph analysis: bottlenecks, keystones, hubs, authorities, cycles
- `GetPriority()` — Priority recommendations with impact scoring
- `GetPlan()` — Parallel execution plan for work distribution

### 4.6 Deployment Reality Check

**Important constraint:** Vercel does not support native WebSockets on serverless functions (as of November 2025).

**Implication:**
- Deploy the **web UI** on Vercel (great for static/SSR)
- Deploy the **NTM API/WebSocket daemon** on a long-lived server platform (Fly.io, Render, bare metal)
- Or run locally with SSH-forward, Tailscale, or Cloudflare Tunnel

### 4.7 Real-Time Streaming Best Practices

From 2025 WebSocket research:

- **Bidirectional necessity** — Terminal interaction requires full-duplex
- **Reconnection handling** — Must be application-specific with state recovery
- **Horizontal scaling** — Redis adapter pattern for multi-server broadcast
- **Message ordering** — Critical for terminal output coherence
- **Edge deployment** — Reduce latency via geo-distributed WebSocket servers

### 4.8 TanStack Query + WebSocket Pattern

TanStack Query v5 doesn't have first-class WebSocket support, but the recommended pattern:

```typescript
// Initial fetch with useQuery
const { data } = useQuery({ queryKey: ['session', id], queryFn: fetchSession });

// WebSocket updates via queryClient.setQueryData
ws.onmessage = (event) => {
  queryClient.setQueryData(['session', id], (old) => merge(old, event.data));
};
```

The new `streamedQuery` API in v5 provides 3× faster perceived performance for streaming data.

---

## 5. Architecture Overview

### 5.1 High-Level Architecture

```
┌──────────────────────────────────────────────────────────────────────────────────┐
│                           NTM WEB PLATFORM                                        │
│                    (Agent Flywheel Command Center)                                │
├──────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  ┌────────────────────────────────────────────────────────────────────────────┐ │
│  │                         WEB UI (Next.js 16)                                 │ │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐         │ │
│  │  │Dashboard │ │ Sessions │ │  Beads   │ │  Memory  │ │ Scanner  │         │ │
│  │  │  Deck    │ │   Deck   │ │   Deck   │ │   Deck   │ │   Deck   │         │ │
│  │  └──────────┘ └──────────┘ └──────────┘ └──────────┘ └──────────┘         │ │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐         │ │
│  │  │ Comms    │ │ Safety   │ │ Accounts │ │ Pipeline │ │  Mobile  │         │ │
│  │  │   Deck   │ │   Deck   │ │   Deck   │ │   Deck   │ │   Deck   │         │ │
│  │  └──────────┘ └──────────┘ └──────────┘ └──────────┘ └──────────┘         │ │
│  │                                                                            │ │
│  │  ┌──────────────────────────────────────────────────────────────────────┐ │ │
│  │  │           TanStack Query + WebSocket Provider + Zustand              │ │ │
│  │  └──────────────────────────────────────────────────────────────────────┘ │ │
│  └────────────────────────────────────────────────────────────────────────────┘ │
│                              │                    │                              │
│                         HTTP/REST            WebSocket                           │
│                              │                    │                              │
│  ┌────────────────────────────────────────────────────────────────────────────┐ │
│  │                      GO HTTP SERVER (net/http + chi)                        │ │
│  │  ┌────────────────────────────┐  ┌────────────────────────────────────┐    │ │
│  │  │       REST ROUTER          │  │        WEBSOCKET HUB               │    │ │
│  │  │                            │  │                                    │    │ │
│  │  │  /api/v1/sessions          │  │  Topics:                           │    │ │
│  │  │  /api/v1/agents            │  │  • sessions:{name}                 │    │ │
│  │  │  /api/v1/beads             │  │  • panes:{session}:{index}         │    │ │
│  │  │  /api/v1/mail              │  │  • alerts                          │    │ │
│  │  │  /api/v1/reservations      │  │  • notifications                   │    │ │
│  │  │  /api/v1/cass              │  │  • scanner                         │    │ │
│  │  │  /api/v1/memory            │  │  • beads                           │    │ │
│  │  │  /api/v1/scanner           │  │  • mail                            │    │ │
│  │  │  /api/v1/accounts          │  │  • conflicts                       │    │ │
│  │  │  /api/v1/pipelines         │  │  • pipeline                        │    │ │
│  │  │  /api/v1/safety            │  │  • metrics                         │    │ │
│  │  └────────────────────────────┘  └────────────────────────────────────┘    │ │
│  └────────────────────────────────────────────────────────────────────────────┘ │
│                                       │                                          │
│  ┌────────────────────────────────────┴───────────────────────────────────────┐ │
│  │                           COMMAND KERNEL                                    │ │
│  │  ┌─────────────────────────────────────────────────────────────────────┐   │ │
│  │  │  • Command Registry (schemas + metadata)                             │   │ │
│  │  │  • Input Validation + Safety Checks                                  │   │ │
│  │  │  • Hooks + Events Emission                                           │   │ │
│  │  │  • Audit Trail + Idempotency                                         │   │ │
│  │  └─────────────────────────────────────────────────────────────────────┘   │ │
│  └────────────────────────────────────────────────────────────────────────────┘ │
│                                       │                                          │
│  ┌────────────────────────────────────┴───────────────────────────────────────┐ │
│  │                        NTM CORE (Existing Go Packages)                      │ │
│  │                                                                             │ │
│  │  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐  │ │
│  │  │  tmux/  │ │ robot/  │ │ config/ │ │ agents/ │ │   bv/   │ │  cass/  │  │ │
│  │  └─────────┘ └─────────┘ └─────────┘ └─────────┘ └─────────┘ └─────────┘  │ │
│  │  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐  │ │
│  │  │agentmail│ │ scanner │ │checkpoint│ │palette/ │ │pipeline │ │resilience│  │ │
│  │  └─────────┘ └─────────┘ └─────────┘ └─────────┘ └─────────┘ └─────────┘  │ │
│  └─────────────────────────────────────────────────────────────────────────────┘ │
│                                       │                                          │
│                    ┌──────────────────┼──────────────────┐                       │
│                    │                  │                  │                       │
│  ┌─────────────────▼───┐  ┌──────────▼──────────┐  ┌────▼────────────────────┐  │
│  │    TMUX SERVER      │  │   AGENT MAIL MCP    │  │   EXTERNAL TOOLS        │  │
│  │  (Sessions/Panes)   │  │   (localhost:8765)  │  │  • UBS (scanner)        │  │
│  └─────────────────────┘  └─────────────────────┘  │  • CASS (search)        │  │
│                                                     │  • CM (memory)          │  │
│  ┌─────────────────────────────────────────────┐   │  • CAAM (accounts)      │  │
│  │           AI CODING AGENTS                   │   │  • SLB (safety)         │  │
│  │  Claude Code │ Codex CLI │ Gemini CLI        │   └─────────────────────────┘  │
│  └─────────────────────────────────────────────┘                                 │
│                                                                                  │
└──────────────────────────────────────────────────────────────────────────────────┘
```

### 5.2 Design Principles

1. **Zero functionality loss** — Every CLI command has an API equivalent
2. **Robot mode as foundation** — REST responses mirror existing `--robot-*` JSON structures
3. **Flywheel-first** — Every feature accelerates the virtuous cycle
4. **Streaming-first** — WebSocket for all real-time data; REST for commands/queries
5. **Unified ecosystem** — All 8 tools accessible from single UI
6. **Layered abstraction** — API layer is thin; business logic stays in existing packages
7. **Backward compatible** — CLI continues to work unchanged
8. **Progressive enhancement** — Web UI enhances but doesn't replace terminal workflow

### 5.3 Key Architectural Invariants

- **No silent data loss** stays true (API must enforce same safety rules)
- **Idempotency** for automation: repeated calls shouldn't create duplicate sessions or spam agents
- **All operations are auditable**: every API mutation creates a history entry and emits an event
- **Everything is streamable**: if it matters, it emits events and/or is queryable

### 5.4 Technology Decisions

| Layer | Technology | Rationale |
|-------|------------|-----------|
| **REST Server** | Go `net/http` + `chi` router | Native Go, performant, middleware ecosystem |
| **WebSocket** | `gorilla/websocket` | Battle-tested, concurrent-safe, ping/pong |
| **Event Bus** | Internal Go pub/sub | Already exists in NTM, 100-event ring buffer |
| **Event Persistence** | SQLite | Already used; enables replay/resume |
| **API Docs** | OpenAPI 3.1 + Swagger UI | Industry standard, code generation |
| **Frontend** | Next.js 16 + React 19 | Latest features, Turbopack, React Compiler |
| **State** | TanStack Query v5 + Zustand | Server state + client state separation |
| **Styling** | Tailwind CSS 4 + Framer Motion | Utility-first, animation primitives |
| **Terminal** | xterm.js | Full terminal emulation in browser |
| **Icons** | Lucide React | Consistent, tree-shakeable |
| **Charts** | Recharts + Tremor | Dashboard visualizations |
| **Graphs** | React Flow | Dependency graph visualization |

### 5.5 Supervisor Strategy

The existing `internal/supervisor` package is configured with `DefaultSpecs` to manage flywheel daemons. `ntm serve` will initialize a `Supervisor` to ensure the following are running:

- **`cm` (CASS Memory):** Started via `cm serve`
- **`am` (Agent Mail):** Started via `mcp-agent-mail serve` (if local)
- **`bd` (Beads):** Started via `bd daemon`

The API Gateway will communicate with these daemons via their respective internal clients (`agentmail.Client`, `cm.Client`), acting as a unified proxy.

---

## 6. Command Kernel: The Parity Guarantee

### 6.1 The Problem: API Drift

Without a forcing function, APIs drift from CLIs:
- CLI gets a new flag, API doesn't
- API returns different error format
- Robot mode and REST diverge

### 6.2 The Solution: Command Kernel

Create a **Command Kernel** (a registry of commands and schemas) that drives:

- CLI (Cobra commands become thin wrappers)
- TUI actions (palette/dashboard call kernel)
- REST endpoints (generated from same registry)
- OpenAPI docs (generated from same registry)
- Web UI "command palette" (pull metadata from API)

```go
// internal/kernel/registry.go
type Command struct {
    Name        string
    Description string
    Category    string

    // Input/Output schemas
    InputSchema  reflect.Type
    OutputSchema reflect.Type

    // Bindings
    CLIFlags     []FlagDef
    RESTBinding  RESTBinding

    // Behavior
    Handler      func(ctx context.Context, input any) (any, error)

    // Metadata for OpenAPI
    Examples     []Example
    SafetyLevel  SafetyLevel // safe, requires_approval, dangerous
    EmitsEvents  []string
    Idempotent   bool
}

type RESTBinding struct {
    Method string // GET, POST, PUT, DELETE
    Path   string // /api/v1/sessions/{session}/spawn
}
```

### 6.3 Refactoring `internal/robot`

Currently, `internal/robot` functions mix data gathering with JSON encoding to `os.Stdout`.

**Action:** Split into **Data Getters** and **Printers**.

```go
// Before
func PrintStatus() {
    data := gatherStatus()
    json.NewEncoder(os.Stdout).Encode(data)
}

// After
func GetStatus() (*StatusOutput, error) {
    return gatherStatus()
}

func PrintStatus() {
    data, _ := GetStatus()
    PrintJSON(data)
}
```

The API will call `GetStatus()` directly, reusing the exact same `StatusOutput` struct.

### 6.4 Service Layer Extraction

Refactor CLI commands into reusable services that both CLI and API call:

| Service | Responsibilities |
|---------|-----------------|
| `SessionService` | create, spawn, kill, view, zoom, attach |
| `AgentService` | send, interrupt, wait, route, add |
| `OutputService` | copy, save, grep, extract, diff |
| `ContextService` | build, show, stats, clear |
| `ToolingService` | doctor, adapters, health |
| `ApprovalService` | approvals, SLB |
| `BeadsService` | bd daemon, triage, insights |
| `ScannerService` | UBS, bridge to beads |

### 6.5 Parity Gate CI Tests

Add CI tests that assert:
- Every command in the registry has CLI binding metadata
- Every command has REST binding metadata (method/path)
- Every command has OpenAPI examples
- Every CLI command is registered in the kernel (no "ad hoc cobra")
- The OpenAPI spec is generated in CI and compared to checked-in `openapi.json`

This makes parity a *mechanical property*.

---

## 7. REST API Layer

### 7.1 API Design Philosophy

The REST API follows these principles:

1. **Resource-oriented** — Sessions, agents, panes, beads, reservations as resources
2. **Consistent responses** — All responses follow the robot mode structure
3. **Idempotent where possible** — PUT/DELETE operations are idempotent
4. **Rich error responses** — Error codes, messages, and actionable hints
5. **AI-agent friendly** — Comprehensive examples for LLM consumption

### 7.2 Base URL Structure

```
Production:  https://api.ntm.local/v1
Development: http://localhost:8080/api/v1
```

### 7.3 API Conventions

**Content types:**
- Requests: `application/json`
- Responses: `application/json`

**Pagination:**
```
?limit=50&cursor=cursor_01H...
```

**Filtering:**
```
?session=myproject&agent_type=claude
```

**Sorting:**
```
?sort=-updated_at
```

**Idempotency:**
For any POST that mutates state, accept:
- `Idempotency-Key: <uuid>` header
- Server stores key + result for a TTL window

### 7.4 Error Model

```json
{
  "error": {
    "code": "SESSION_NOT_FOUND",
    "message": "Session 'myproject' not found",
    "details": {"session": "myproject"},
    "request_id": "req_01H..."
  }
}
```

### 7.5 Success Response Envelope

```typescript
interface ApiResponse<T> {
  data: T;
  request_id: string;
  timestamp: string;        // RFC3339 UTC
  _agent_hints?: {          // For AI agent consumers
    summary: string;
    suggested_actions: Action[];
    warnings: string[];
  };
}
```

### 7.6 Capabilities Endpoint

```
GET /api/v1/capabilities
```

Returns what's available:
- Which optional tools are installed (tmux/bv/cass/agentmail/ubs)
- Which stream topics exist
- What auth methods are enabled
- Feature flags

### 7.7 Jobs for Long-Running Operations

Operations that take time return a job:

```json
// POST /api/v1/sessions/myproject/spawn
// Response: 202 Accepted
{
  "operation_id": "op_01H...",
  "status": "running",
  "session": "myproject",
  "started_at": "2026-01-07T00:00:00Z"
}
```

Query job status:
```
GET /api/v1/jobs/{operation_id}
DELETE /api/v1/jobs/{operation_id}
```

### 7.8 Core Endpoint Categories

#### 7.8.1 System (`/api/v1/...`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health` | Health check |
| `GET` | `/version` | Version info |
| `GET` | `/deps` | Dependencies status |
| `GET` | `/config` | Effective config |
| `PATCH` | `/config` | Update config entries |
| `GET` | `/capabilities` | Available features |
| `GET` | `/doctor` | Tool health check |

#### 7.8.2 Sessions (`/api/v1/sessions`)

| Method | Endpoint | Robot Equivalent | Description |
|--------|----------|------------------|-------------|
| `GET` | `/sessions` | `--robot-status` | List all sessions |
| `POST` | `/sessions` | `ntm create` | Create empty session |
| `GET` | `/sessions/{name}` | `--robot-status` | Get session details |
| `DELETE` | `/sessions/{name}` | `ntm kill` | Kill session |
| `POST` | `/sessions/{name}/spawn` | `--robot-spawn` | Create with agents |
| `POST` | `/sessions/{name}/quick` | `ntm quick` | Project scaffolding |
| `POST` | `/sessions/{name}/attach` | `ntm attach` | Mark attached |
| `POST` | `/sessions/{name}/view` | `ntm view` | Tile and attach |
| `POST` | `/sessions/{name}/zoom` | `ntm zoom` | Zoom to pane |
| `GET` | `/sessions/{name}/snapshot` | `--robot-snapshot` | Full state capture |
| `GET` | `/sessions/{name}/watch` | `ntm watch` | Watch mode (use WS) |

#### 7.8.3 Panes (`/api/v1/sessions/{name}/panes`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/sessions/{name}/panes` | List panes |
| `GET` | `/sessions/{name}/panes/{idx}` | Pane details |
| `POST` | `/sessions/{name}/panes/{idx}/input` | Send keys/text |
| `POST` | `/sessions/{name}/panes/{idx}/interrupt` | Ctrl-C |
| `GET` | `/sessions/{name}/panes/{idx}/output` | Tail output |
| `GET` | `/sessions/{name}/panes/{idx}/capture` | Capture pane |
| `POST` | `/sessions/{name}/panes/{idx}/title` | Rename title |

#### 7.8.4 Agents (`/api/v1/sessions/{name}/agents`)

| Method | Endpoint | Robot Equivalent | Description |
|--------|----------|------------------|-------------|
| `GET` | `/sessions/{name}/agents` | — | List agents |
| `POST` | `/sessions/{name}/agents/add` | `ntm add` | Add agents |
| `POST` | `/sessions/{name}/agents/send` | `--robot-send` | Send prompt |
| `POST` | `/sessions/{name}/agents/interrupt` | `--robot-interrupt` | Interrupt |
| `POST` | `/sessions/{name}/agents/wait` | `--robot-wait` | Wait for condition |
| `GET` | `/sessions/{name}/agents/route` | `--robot-route` | Routing recommendation |
| `GET` | `/sessions/{name}/agents/activity` | `--robot-activity` | Activity states |
| `GET` | `/sessions/{name}/agents/health` | `--robot-health` | Health status |
| `GET` | `/sessions/{name}/agents/context` | `--robot-context` | Context usage |
| `POST` | `/sessions/{name}/agents/rotate` | `ntm rotate` | Rotation/compaction |

#### 7.8.5 Output Tooling (`/api/v1/sessions/{name}/output`)

| Method | Endpoint | CLI Equivalent | Description |
|--------|----------|----------------|-------------|
| `POST` | `/sessions/{name}/output/copy` | `ntm copy` | Copy to clipboard |
| `POST` | `/sessions/{name}/output/save` | `ntm save` | Save to file |
| `POST` | `/sessions/{name}/output/grep` | `ntm grep` | Search output |
| `POST` | `/sessions/{name}/output/extract` | `ntm extract` | Extract content |
| `GET` | `/sessions/{name}/output/diff` | `ntm diff` | Compare panes |
| `GET` | `/sessions/{name}/output/changes` | `ntm changes` | File changes |
| `GET` | `/sessions/{name}/output/conflicts` | `ntm conflicts` | Detect conflicts |
| `GET` | `/sessions/{name}/output/summary` | `ntm summary` | Summarize |
| `POST` | `/sessions/{name}/output/watch` | — | Create watch job |

#### 7.8.6 Palette & History

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/sessions/{name}/palette` | Palette commands |
| `POST` | `/sessions/{name}/palette/run` | Run command |
| `POST` | `/sessions/{name}/palette/pin` | Pin command |
| `POST` | `/sessions/{name}/palette/favorite` | Favorite command |
| `GET` | `/history` | Prompt history |
| `GET` | `/history/{id}` | Show history entry |
| `POST` | `/history/clear` | Clear history |
| `GET` | `/history/stats` | History statistics |
| `POST` | `/history/export` | Export history |
| `POST` | `/history/prune` | Prune old entries |
| `POST` | `/history/replay` | Replay command |

#### 7.8.7 Checkpoints (`/api/v1/sessions/{name}/checkpoints`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/sessions/{name}/checkpoints` | List checkpoints |
| `POST` | `/sessions/{name}/checkpoints` | Create checkpoint |
| `GET` | `/sessions/{name}/checkpoints/{id}` | Get checkpoint |
| `DELETE` | `/sessions/{name}/checkpoints/{id}` | Delete checkpoint |
| `POST` | `/sessions/{name}/checkpoints/{id}/restore` | Restore |
| `POST` | `/sessions/{name}/checkpoints/{id}/verify` | Verify integrity |
| `POST` | `/sessions/{name}/checkpoints/{id}/export` | Export archive |
| `POST` | `/sessions/{name}/checkpoints/import` | Import archive |
| `POST` | `/sessions/{name}/rollback` | Rollback to checkpoint |

#### 7.8.8 Metrics & Analytics

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/metrics` | Session metrics |
| `GET` | `/metrics/compare` | Compare snapshots |
| `GET` | `/metrics/export` | Export metrics |
| `POST` | `/metrics/snapshot` | Create snapshot |
| `POST` | `/metrics/save` | Save named snapshot |
| `GET` | `/metrics/list` | List saved snapshots |
| `GET` | `/analytics` | Analytics dashboard |

#### 7.8.9 Context Packs (`/api/v1/context`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/context/build` | Build context pack |
| `GET` | `/context/{id}` | Get pack |
| `GET` | `/context/stats` | Context statistics |
| `DELETE` | `/context/cache` | Clear cache |

#### 7.8.10 Git Coordination (`/api/v1/git`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/git/sync` | Sync with remote |
| `GET` | `/git/status` | Git status |

---

## 8. WebSocket Layer

### 8.1 Connection Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                    WebSocket Connection Manager                      │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │                    TOPIC ROUTER                              │   │
│  │                                                              │   │
│  │  Global Topics:              Session Topics:                 │   │
│  │  • events                    • sessions:{name}               │   │
│  │  • alerts                    • panes:{session}:{index}       │   │
│  │  • notifications             • panes:{session}:cc            │   │
│  │  • scanner                   • panes:{session}:cod           │   │
│  │  • beads                     • panes:{session}:gmi           │   │
│  │  • mail                                                      │   │
│  │  • conflicts                                                 │   │
│  │  • metrics                                                   │   │
│  │  • pipeline                                                  │   │
│  │  • memory                                                    │   │
│  │  • history                                                   │   │
│  │                                                              │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                              │                                      │
│                              ▼                                      │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │                    EVENT SOURCES                             │   │
│  │                                                              │   │
│  │  • Tmux pipe-pane streaming (real-time)                     │   │
│  │  • Agent Mail inbox polling                                  │   │
│  │  • BV triage cache invalidation                             │   │
│  │  • UBS auto-scanner results                                  │   │
│  │  • CASS index updates                                        │   │
│  │  • CM memory changes                                         │   │
│  │  • File system watchers                                      │   │
│  │  • Health check results                                      │   │
│  │  • Pipeline state changes                                    │   │
│  │  • Account rotation events                                   │   │
│  │                                                              │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### 8.2 Connection Endpoint

```
WebSocket URL: wss://api.ntm.local/v1/ws
              ws://localhost:8080/api/v1/ws (development)

Query Parameters:
  - api_key: Authentication token
```

### 8.3 Subscription Protocol

Client sends:
```json
{
  "op": "subscribe",
  "topics": [
    "events",
    "sessions:myproject",
    "panes:myproject:1",
    "panes:myproject:cc",
    "notifications"
  ],
  "since": "cursor_01H..."
}
```

Server responds:
```json
{
  "op": "subscribed",
  "topics": ["events", "sessions:myproject", "panes:myproject:1", "notifications"],
  "server_time": "2026-01-07T00:00:00Z"
}
```

### 8.4 Event Envelope

```json
{
  "type": "pane.output.append",
  "ts": "2026-01-07T00:00:00.123Z",
  "seq": 184224,
  "topic": "panes:myproject:1",
  "data": {
    "session": "myproject",
    "pane": 1,
    "agent_type": "claude",
    "agent_mail_name": "GreenCastle",
    "chunk": "Analyzing the authentication module...\n",
    "encoding": "utf-8",
    "truncated": false,
    "detected_state": "working"
  }
}
```

**Design notes:**
- `seq` is monotonically increasing (enables resume from last seen)
- `topic` is explicit (simplifies client routing)
- `data` is typed by `type`

### 8.5 Event Types

#### Session Events
- `session.created`
- `session.deleted`
- `session.status_changed`

#### Pane Events
- `pane.output.append` — New output
- `pane.output.dropped` — Client can't keep up
- `pane.created`
- `pane.closed`
- `pane.title_changed`

#### Agent Events
- `agent.spawned`
- `agent.state_changed` — Active ↔ Idle
- `agent.error`
- `agent.compaction`

#### Beads Events
- `bead.created`
- `bead.updated`
- `bead.closed`
- `bead.claimed`

#### Mail Events
- `mail.received`
- `mail.read`
- `mail.acknowledged`

#### Reservation Events
- `reservation.granted`
- `reservation.released`
- `reservation.conflict`
- `reservation.expired`

#### Scanner Events
- `scanner.started`
- `scanner.finding`
- `scanner.complete`

#### Pipeline Events
- `pipeline.started`
- `pipeline.step_completed`
- `pipeline.step_failed`
- `pipeline.complete`

#### System Events
- `alert.created`
- `alert.dismissed`
- `approval.requested`
- `approval.resolved`
- `health.changed`
- `account.rotated`

### 8.6 Backpressure & Performance

For many panes, output can exceed what a browser can render.

**Server-side:**
- Per-pane ring buffer (configurable, default 10000 lines)
- Per-client subscription limits

**Client options:**
```json
{
  "op": "subscribe",
  "topics": ["panes:myproject:1"],
  "options": {
    "mode": "lines",       // "lines" (safe) or "raw" (fast)
    "throttle_ms": 100,    // Batch updates
    "max_lines_per_msg": 50
  }
}
```

**Dropped output notification:**
```json
{
  "type": "pane.output.dropped",
  "topic": "panes:myproject:1",
  "data": {
    "dropped_lines": 150,
    "reason": "client_slow"
  }
}
```

### 8.7 Replay / Resume Model

Each event stream stores a short retention ring (e.g., last 60 seconds or last N events).

Client sends `since`:
- A cursor string: `"since": "cursor_01H..."`
- Or a sequence number: `"since": {"seq": 123}`

If the cursor is too old:
```json
{
  "type": "stream.reset",
  "topic": "panes:myproject:1",
  "data": {
    "reason": "cursor_expired",
    "snapshot": {
      "lines": ["...last 200 lines..."],
      "current_seq": 185000
    }
  }
}
```

### 8.8 Snapshot-on-Connect Pattern

When a web client opens a pane view:
1. `GET /api/v1/sessions/{name}/panes/{idx}/output?tail=200` returns snapshot
2. WebSocket subscription starts immediately after
3. No blank pane, seamless continuation

### 8.9 tmux Output Capture Architecture

**Problem:** `tmux capture-pane` is too expensive for live streaming.

**Solution: `tmux pipe-pane` capture**

When a pane is created/spawned:
```bash
tmux pipe-pane -o -t <pane> "<ntm_internal_streamer --pane-id ...>"
```

The streamer writes:
- To the event bus (WebSocket)
- To disk log (for tail/replay)
- Optionally to analytics counters

**Properties:**
- Near real-time
- Minimal polling
- Stable even with many panes

**Fallback:** If pipe-pane fails, poll `capture-pane` at conservative rate (1s) only for panes with active subscribers.

---

## 9. Agent Mail Deep Integration

### 9.1 REST API Endpoints (`/api/v1/mail`)

| Method | Endpoint | MCP Method | Description |
|--------|----------|------------|-------------|
| `GET` | `/mail/health` | `health_check` | MCP server health |
| `POST` | `/mail/projects` | `ensure_project` | Ensure project exists |
| `POST` | `/mail/agents` | `register_agent` | Register agent identity |
| `POST` | `/mail/agents/create` | `create_agent_identity` | Create new identity |
| `GET` | `/mail/agents/{name}` | `whois` | Agent profile lookup |
| `GET` | `/mail/inbox` | `fetch_inbox` | Fetch agent inbox |
| `POST` | `/mail/messages` | `send_message` | Send message |
| `POST` | `/mail/messages/{id}/reply` | `reply_message` | Reply to message |
| `POST` | `/mail/messages/{id}/read` | `mark_message_read` | Mark as read |
| `POST` | `/mail/messages/{id}/ack` | `acknowledge_message` | Acknowledge message |
| `GET` | `/mail/search` | `search_messages` | Full-text search |
| `GET` | `/mail/threads/{id}/summary` | `summarize_thread` | Thread summary |
| `POST` | `/mail/contacts/request` | `request_contact` | Request contact |
| `POST` | `/mail/contacts/respond` | `respond_contact` | Accept/deny contact |
| `GET` | `/mail/contacts` | `list_contacts` | List contacts |
| `PUT` | `/mail/contacts/policy` | `set_contact_policy` | Set contact policy |

### 9.2 File Reservations API (`/api/v1/reservations`)

| Method | Endpoint | MCP Method | Description |
|--------|----------|------------|-------------|
| `POST` | `/reservations` | `file_reservation_paths` | Reserve paths |
| `DELETE` | `/reservations` | `release_file_reservations` | Release reservations |
| `POST` | `/reservations/{id}/release` | — | Release specific |
| `POST` | `/reservations/{id}/renew` | `renew_file_reservations` | Extend TTL |
| `POST` | `/reservations/{id}/force-release` | `force_release_file_reservation` | Force release stale |
| `GET` | `/reservations` | — | List all reservations |
| `GET` | `/reservations/conflicts` | — | Current conflicts |

### 9.3 File Reservation Map Component

```tsx
// components/reservations/FileReservationMap.tsx
'use client';

import { useQuery } from '@tanstack/react-query';
import { motion, AnimatePresence } from 'framer-motion';
import { FileIcon, LockIcon, AlertTriangleIcon, UserIcon } from 'lucide-react';

interface FileReservation {
  id: number;
  path_pattern: string;
  agent_name: string;
  exclusive: boolean;
  reason: string;
  created_at: string;
  expires_at: string;
  has_conflict: boolean;
}

export function FileReservationMap({ sessionName }: { sessionName: string }) {
  const { data: reservations } = useQuery({
    queryKey: ['reservations', sessionName],
    queryFn: () => api.getReservations(sessionName),
    refetchInterval: 5000,
  });

  // Group by file path
  const fileGroups = groupByPath(reservations ?? []);

  return (
    <div className="bg-slate-900 rounded-xl p-6 border border-slate-800">
      <div className="flex items-center gap-2 mb-6">
        <LockIcon className="w-5 h-5 text-amber-400" />
        <h3 className="text-lg font-semibold text-white">File Reservations</h3>
      </div>

      <div className="space-y-2">
        <AnimatePresence mode="popLayout">
          {Object.entries(fileGroups).map(([path, holders]) => (
            <motion.div
              key={path}
              initial={{ opacity: 0, y: -10 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, x: -20 }}
              className={`p-3 rounded-lg border ${
                holders.some(h => h.has_conflict)
                  ? 'bg-red-900/20 border-red-500/50'
                  : holders.some(h => h.exclusive)
                  ? 'bg-amber-900/20 border-amber-500/50'
                  : 'bg-slate-800/50 border-slate-700'
              }`}
            >
              <div className="flex items-center gap-3">
                <FileIcon className="w-4 h-4 text-slate-400" />
                <code className="text-sm text-slate-300 flex-1 font-mono">
                  {path}
                </code>
                {holders.some(h => h.has_conflict) && (
                  <AlertTriangleIcon className="w-4 h-4 text-red-400" />
                )}
              </div>

              <div className="mt-2 flex flex-wrap gap-2">
                {holders.map(holder => (
                  <AgentBadge
                    key={holder.id}
                    name={holder.agent_name}
                    exclusive={holder.exclusive}
                    expiresAt={holder.expires_at}
                  />
                ))}
              </div>
            </motion.div>
          ))}
        </AnimatePresence>
      </div>
    </div>
  );
}
```

### 9.4 WebSocket Events for Mail

```typescript
// Mail-related WebSocket events
interface MailEvent {
  type: 'mail.received' | 'mail.read' | 'mail.acknowledged';
  data: {
    message_id: number;
    thread_id?: string;
    from_agent: string;
    to_agents: string[];
    subject: string;
    importance: 'low' | 'normal' | 'high' | 'urgent';
    ack_required: boolean;
  };
}

interface ReservationEvent {
  type: 'reservation.granted' | 'reservation.released' |
        'reservation.conflict' | 'reservation.expired';
  data: {
    reservation_id: number;
    path_pattern: string;
    agent_name: string;
    exclusive: boolean;
    conflicting_agents?: string[];
  };
}
```

---

## 10. Beads & BV Integration

### 10.1 REST API Endpoints (`/api/v1/beads`)

| Method | Endpoint | Source | Description |
|--------|----------|--------|-------------|
| `GET` | `/beads` | `bd list` | List all beads |
| `POST` | `/beads` | `bd create` | Create bead |
| `GET` | `/beads/{id}` | `bd show` | Get bead details |
| `PATCH` | `/beads/{id}` | `bd update` | Update bead |
| `POST` | `/beads/{id}/close` | `bd close` | Close bead |
| `POST` | `/beads/{id}/claim` | `bd update --status=in_progress` | Claim bead |
| `GET` | `/beads/ready` | `bd ready` | Get ready work |
| `GET` | `/beads/blocked` | `bd blocked` | Get blocked beads |
| `POST` | `/beads/{id}/deps` | `bd dep add` | Add dependency |
| `DELETE` | `/beads/{id}/deps/{dep_id}` | `bd dep remove` | Remove dependency |
| `GET` | `/beads/triage` | `bv --robot-triage` | Full triage analysis |
| `GET` | `/beads/insights` | `bv --robot-insights` | Graph insights |
| `GET` | `/beads/plan` | `bv --robot-plan` | Parallel execution plan |
| `GET` | `/beads/stats` | `bd stats` | Project statistics |
| `GET` | `/beads/daemon/status` | — | Daemon status |
| `POST` | `/beads/daemon/start` | `bd daemon` | Start daemon |
| `POST` | `/beads/daemon/stop` | — | Stop daemon |
| `POST` | `/beads/sync` | `bd sync` | Sync with git |

### 10.2 Kanban Board Component

```tsx
// components/beads/KanbanBoard.tsx
'use client';

import { DragDropContext, Droppable, Draggable } from '@hello-pangea/dnd';
import { motion } from 'framer-motion';

const COLUMNS = [
  { id: 'open', title: 'Open', color: 'slate' },
  { id: 'in_progress', title: 'In Progress', color: 'blue' },
  { id: 'blocked', title: 'Blocked', color: 'red' },
  { id: 'review', title: 'Review', color: 'amber' },
  { id: 'closed', title: 'Done', color: 'green' },
];

export function KanbanBoard() {
  const { data: beads } = useBeads();
  const updateBead = useUpdateBead();

  const handleDragEnd = (result: DropResult) => {
    if (!result.destination) return;

    const beadId = result.draggableId;
    const newStatus = result.destination.droppableId;

    updateBead.mutate({ id: beadId, status: newStatus });
  };

  const groupedBeads = groupByStatus(beads ?? []);

  return (
    <DragDropContext onDragEnd={handleDragEnd}>
      <div className="flex gap-4 overflow-x-auto pb-4">
        {COLUMNS.map(column => (
          <Droppable key={column.id} droppableId={column.id}>
            {(provided, snapshot) => (
              <div
                ref={provided.innerRef}
                {...provided.droppableProps}
                className={`flex-shrink-0 w-80 bg-slate-900/50 rounded-xl p-4 border ${
                  snapshot.isDraggingOver ? 'border-blue-500' : 'border-slate-800'
                }`}
              >
                <ColumnHeader column={column} count={groupedBeads[column.id]?.length ?? 0} />

                <div className="space-y-3 mt-4 min-h-[200px]">
                  {groupedBeads[column.id]?.map((bead, index) => (
                    <Draggable key={bead.id} draggableId={bead.id} index={index}>
                      {(provided, snapshot) => (
                        <BeadCard
                          ref={provided.innerRef}
                          {...provided.draggableProps}
                          {...provided.dragHandleProps}
                          bead={bead}
                          isDragging={snapshot.isDragging}
                        />
                      )}
                    </Draggable>
                  ))}
                  {provided.placeholder}
                </div>
              </div>
            )}
          </Droppable>
        ))}
      </div>
    </DragDropContext>
  );
}
```

### 10.3 Dependency Graph (Galaxy View)

```tsx
// components/beads/DependencyGraph.tsx
'use client';

import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  MarkerType,
} from 'reactflow';

const nodeTypes = {
  bead: BeadNode,
  bottleneck: BottleneckNode,
  keystone: KeystoneNode,
};

export function DependencyGraph() {
  const { data: insights } = useInsights();
  const { data: beads } = useBeads();

  const [nodes, setNodes, onNodesChange] = useNodesState([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState([]);

  useEffect(() => {
    if (!beads || !insights) return;

    // Build node positions using force-directed layout
    const { nodes: layoutNodes, edges: layoutEdges } = buildGalaxyLayout(beads, insights);

    // Color-code by role
    const coloredNodes = layoutNodes.map(node => ({
      ...node,
      type: getNodeType(node.id, insights),
      style: getNodeStyle(node.id, insights),
    }));

    setNodes(coloredNodes);
    setEdges(layoutEdges);
  }, [beads, insights]);

  return (
    <div className="h-[600px] bg-slate-950 rounded-xl border border-slate-800">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        nodeTypes={nodeTypes}
        fitView
      >
        <Background color="#334155" gap={16} />
        <Controls className="bg-slate-800 border-slate-700" />
        <MiniMap
          nodeColor={node => {
            if (node.type === 'bottleneck') return '#ef4444';
            if (node.type === 'keystone') return '#f59e0b';
            return '#64748b';
          }}
        />
      </ReactFlow>

      {/* Legend */}
      <div className="absolute bottom-4 left-4 bg-slate-900/90 rounded-lg p-3">
        <div className="flex items-center gap-4 text-xs">
          <span className="flex items-center gap-1">
            <span className="w-3 h-3 rounded-full bg-red-500" /> Bottleneck
          </span>
          <span className="flex items-center gap-1">
            <span className="w-3 h-3 rounded-full bg-amber-500" /> Keystone
          </span>
          <span className="flex items-center gap-1">
            <span className="w-3 h-3 rounded-full bg-blue-500" /> Hub
          </span>
        </div>
      </div>
    </div>
  );
}

function getNodeType(id: string, insights: Insights): string {
  if (insights.bottlenecks.some(b => b.id === id)) return 'bottleneck';
  if (insights.keystones.some(k => k.id === id)) return 'keystone';
  return 'bead';
}
```

### 10.4 Triage Panel Component

```tsx
// components/beads/TriagePanel.tsx
'use client';

export function TriagePanel() {
  const { data: triage } = useTriage();

  if (!triage) return <TriageSkeleton />;

  return (
    <div className="space-y-6">
      {/* Health Score */}
      <div className="bg-slate-900 rounded-xl p-6 border border-slate-800">
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-sm font-medium text-slate-400">Project Health</h3>
            <p className="text-3xl font-bold text-white mt-1">
              {triage.health_score}%
            </p>
          </div>
          <HealthRing score={triage.health_score} />
        </div>
      </div>

      {/* Quick Reference */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <StatCard label="Ready" value={triage.quick_ref.ready} color="green" />
        <StatCard label="In Progress" value={triage.quick_ref.in_progress} color="blue" />
        <StatCard label="Blocked" value={triage.quick_ref.blocked} color="red" />
        <StatCard label="Total Open" value={triage.quick_ref.open} color="slate" />
      </div>

      {/* Recommendations */}
      <div className="bg-slate-900 rounded-xl p-6 border border-slate-800">
        <h3 className="text-lg font-semibold text-white mb-4">Recommendations</h3>
        <div className="space-y-3">
          {triage.recommendations.map((rec, i) => (
            <motion.div
              key={rec.bead_id}
              initial={{ opacity: 0, x: -20 }}
              animate={{ opacity: 1, x: 0 }}
              transition={{ delay: i * 0.1 }}
              className="flex items-start gap-3 p-3 bg-slate-800/50 rounded-lg"
            >
              <PriorityBadge priority={rec.priority} />
              <div className="flex-1">
                <p className="text-sm font-medium text-white">{rec.title}</p>
                <p className="text-xs text-slate-400 mt-1">{rec.reason}</p>
              </div>
              <Button size="sm" onClick={() => claimBead(rec.bead_id)}>
                Claim
              </Button>
            </motion.div>
          ))}
        </div>
      </div>

      {/* Quick Wins */}
      {triage.quick_wins.length > 0 && (
        <div className="bg-green-900/20 rounded-xl p-6 border border-green-500/30">
          <h3 className="text-lg font-semibold text-green-400 mb-4">
            ⚡ Quick Wins
          </h3>
          <div className="space-y-2">
            {triage.quick_wins.map(win => (
              <QuickWinCard key={win.bead_id} win={win} />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
```

---

## 11. CASS & Memory System Integration

### 11.1 REST API Endpoints (`/api/v1/cass` and `/api/v1/memory`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/cass/status` | CASS service status |
| `GET` | `/cass/search` | Semantic search |
| `GET` | `/cass/insights` | Search insights |
| `GET` | `/cass/timeline` | Session timeline |
| `POST` | `/cass/preview` | Preview context injection |
| `POST` | `/memory/daemon/start` | Start CM server |
| `POST` | `/memory/daemon/stop` | Stop CM server |
| `GET` | `/memory/daemon/status` | Daemon status |
| `POST` | `/memory/context` | Get context for task |
| `POST` | `/memory/outcome` | Record outcome |
| `GET` | `/memory/privacy` | Privacy settings |
| `PUT` | `/memory/privacy` | Update privacy |
| `GET` | `/memory/rules` | List memory rules |

### 11.2 Semantic Search UI Component

```tsx
// components/memory/SemanticSearch.tsx
'use client';

import { useState } from 'react';
import { useDebounce } from '@/hooks/useDebounce';
import { SearchIcon, ClockIcon, FileTextIcon } from 'lucide-react';

export function SemanticSearch() {
  const [query, setQuery] = useState('');
  const debouncedQuery = useDebounce(query, 300);

  const { data: results, isLoading } = useQuery({
    queryKey: ['cass-search', debouncedQuery],
    queryFn: () => api.cassSearch(debouncedQuery),
    enabled: debouncedQuery.length > 2,
  });

  return (
    <div className="space-y-4">
      {/* Search Input */}
      <div className="relative">
        <SearchIcon className="absolute left-4 top-1/2 -translate-y-1/2 w-5 h-5 text-slate-400" />
        <input
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search past sessions, code, conversations..."
          className="w-full pl-12 pr-4 py-3 bg-slate-900 border border-slate-700 rounded-xl
                     text-white placeholder-slate-500 focus:border-blue-500 focus:ring-1
                     focus:ring-blue-500 transition-all"
        />
      </div>

      {/* Results */}
      {isLoading ? (
        <SearchSkeleton />
      ) : results?.length > 0 ? (
        <div className="space-y-3">
          {results.map((result, i) => (
            <motion.div
              key={result.id}
              initial={{ opacity: 0, y: 10 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ delay: i * 0.05 }}
              className="bg-slate-900 rounded-lg p-4 border border-slate-800 hover:border-slate-700"
            >
              <div className="flex items-start gap-3">
                <FileTextIcon className="w-5 h-5 text-slate-400 mt-0.5" />
                <div className="flex-1 min-w-0">
                  <h4 className="text-sm font-medium text-white truncate">
                    {result.session_name}
                  </h4>
                  <p className="text-xs text-slate-400 mt-1 line-clamp-2">
                    {result.snippet}
                  </p>
                  <div className="flex items-center gap-2 mt-2 text-xs text-slate-500">
                    <ClockIcon className="w-3 h-3" />
                    {formatRelativeTime(result.timestamp)}
                    <span className="px-1.5 py-0.5 bg-slate-800 rounded text-slate-400">
                      Score: {(result.score * 100).toFixed(0)}%
                    </span>
                  </div>
                </div>
              </div>
            </motion.div>
          ))}
        </div>
      ) : query.length > 2 ? (
        <EmptyState message="No matching sessions found" />
      ) : null}
    </div>
  );
}
```

### 11.3 Context Pack Studio

```tsx
// components/memory/ContextPackStudio.tsx
'use client';

export function ContextPackStudio() {
  const [config, setConfig] = useState({
    triage_budget: 2000,
    cm_budget: 3000,
    cass_budget: 2000,
    s2p_budget: 1000,
  });

  const totalBudget = Object.values(config).reduce((a, b) => a + b, 0);

  const { data: preview } = useQuery({
    queryKey: ['context-preview', config],
    queryFn: () => api.previewContextPack(config),
  });

  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
      {/* Budget Sliders */}
      <div className="bg-slate-900 rounded-xl p-6 border border-slate-800">
        <h3 className="text-lg font-semibold text-white mb-6">Token Budget</h3>

        <div className="space-y-6">
          <BudgetSlider
            label="Triage (BV)"
            value={config.triage_budget}
            onChange={(v) => setConfig({ ...config, triage_budget: v })}
            max={5000}
            color="amber"
          />
          <BudgetSlider
            label="Memory (CM)"
            value={config.cm_budget}
            onChange={(v) => setConfig({ ...config, cm_budget: v })}
            max={5000}
            color="purple"
          />
          <BudgetSlider
            label="Search (CASS)"
            value={config.cass_budget}
            onChange={(v) => setConfig({ ...config, cass_budget: v })}
            max={5000}
            color="blue"
          />
          <BudgetSlider
            label="Sessions (S2P)"
            value={config.s2p_budget}
            onChange={(v) => setConfig({ ...config, s2p_budget: v })}
            max={3000}
            color="green"
          />
        </div>

        <div className="mt-6 pt-4 border-t border-slate-700">
          <div className="flex justify-between text-sm">
            <span className="text-slate-400">Total Budget</span>
            <span className="text-white font-medium">{totalBudget.toLocaleString()} tokens</span>
          </div>
        </div>
      </div>

      {/* Preview */}
      <div className="bg-slate-900 rounded-xl p-6 border border-slate-800">
        <h3 className="text-lg font-semibold text-white mb-4">Preview</h3>
        {preview && (
          <div className="space-y-4">
            <PreviewSection title="Triage" content={preview.triage} tokens={preview.triage_tokens} />
            <PreviewSection title="Memory" content={preview.memory} tokens={preview.cm_tokens} />
            <PreviewSection title="Search" content={preview.cass} tokens={preview.cass_tokens} />
          </div>
        )}
      </div>
    </div>
  );
}
```

---

## 12. UBS Scanner Integration

### 12.1 REST API Endpoints (`/api/v1/scanner`)

| Method | Endpoint | CLI Equivalent | Description |
|--------|----------|----------------|-------------|
| `POST` | `/scanner/run` | `ntm scan` | Run scan |
| `GET` | `/scanner/status` | — | Scanner status |
| `GET` | `/scanner/findings` | — | List findings |
| `GET` | `/scanner/findings/{id}` | — | Finding details |
| `POST` | `/scanner/findings/{id}/dismiss` | — | Dismiss finding |
| `POST` | `/scanner/findings/{id}/create-bead` | — | Create bead from finding |
| `GET` | `/scanner/history` | — | Scan history |
| `GET` | `/bugs` | `ntm bugs list` | Bug list |
| `GET` | `/bugs/summary` | `ntm bugs summary` | Bug summary |
| `POST` | `/bugs/notify` | `ntm bugs notify` | Send notifications |

### 12.2 Scanner Dashboard Component

```tsx
// components/scanner/ScannerDashboard.tsx
'use client';

import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer } from 'recharts';

export function ScannerDashboard() {
  const { data: status } = useScannerStatus();
  const { data: findings } = useScannerFindings();

  const severityCounts = {
    critical: findings?.filter(f => f.severity === 'critical').length ?? 0,
    high: findings?.filter(f => f.severity === 'high').length ?? 0,
    medium: findings?.filter(f => f.severity === 'medium').length ?? 0,
    low: findings?.filter(f => f.severity === 'low').length ?? 0,
  };

  return (
    <div className="space-y-6">
      {/* Status Header */}
      <div className="bg-slate-900 rounded-xl p-6 border border-slate-800">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-xl font-semibold text-white">Code Health</h2>
            <p className="text-sm text-slate-400 mt-1">
              Last scan: {status?.last_scan ? formatRelativeTime(status.last_scan) : 'Never'}
            </p>
          </div>
          <Button onClick={() => runScan.mutate()}>
            <RefreshCwIcon className="w-4 h-4 mr-2" />
            Scan Now
          </Button>
        </div>
      </div>

      {/* Severity Cards */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <SeverityCard severity="critical" count={severityCounts.critical} />
        <SeverityCard severity="high" count={severityCounts.high} />
        <SeverityCard severity="medium" count={severityCounts.medium} />
        <SeverityCard severity="low" count={severityCounts.low} />
      </div>

      {/* Severity Chart */}
      <div className="bg-slate-900 rounded-xl p-6 border border-slate-800">
        <h3 className="text-lg font-semibold text-white mb-4">Findings by Severity</h3>
        <div className="h-64">
          <ResponsiveContainer width="100%" height="100%">
            <BarChart data={chartData}>
              <XAxis dataKey="name" stroke="#64748b" />
              <YAxis stroke="#64748b" />
              <Tooltip
                contentStyle={{ backgroundColor: '#1e293b', border: '1px solid #334155' }}
              />
              <Bar dataKey="count" fill="#3b82f6" radius={[4, 4, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      {/* Findings List */}
      <div className="bg-slate-900 rounded-xl p-6 border border-slate-800">
        <h3 className="text-lg font-semibold text-white mb-4">Recent Findings</h3>
        <div className="space-y-3">
          {findings?.slice(0, 10).map(finding => (
            <FindingCard key={finding.id} finding={finding} />
          ))}
        </div>
      </div>
    </div>
  );
}
```

---

## 13. CAAM Account Management

### 13.1 REST API Endpoints (`/api/v1/accounts`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/accounts` | List accounts |
| `GET` | `/accounts/{id}` | Account details |
| `POST` | `/accounts/{id}/rotate` | Rotate to account |
| `GET` | `/accounts/active` | Active account |
| `GET` | `/accounts/quota` | Quota status |
| `POST` | `/accounts/auto-rotate` | Enable auto-rotation |
| `GET` | `/accounts/history` | Rotation history |

### 13.2 Account Manager Component

```tsx
// components/accounts/AccountManager.tsx
'use client';

export function AccountManager() {
  const { data: accounts } = useAccounts();
  const { data: activeAccount } = useActiveAccount();
  const rotate = useAccountRotate();

  return (
    <div className="space-y-6">
      {/* Active Account */}
      <div className="bg-gradient-to-r from-blue-900/50 to-purple-900/50 rounded-xl p-6 border border-blue-500/30">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm text-blue-300">Active Account</p>
            <h3 className="text-xl font-semibold text-white mt-1">
              {activeAccount?.name}
            </h3>
            <p className="text-sm text-slate-400 mt-2">
              {activeAccount?.provider} • {activeAccount?.tier}
            </p>
          </div>
          <QuotaRing quota={activeAccount?.quota} />
        </div>
      </div>

      {/* Account List */}
      <div className="bg-slate-900 rounded-xl p-6 border border-slate-800">
        <h3 className="text-lg font-semibold text-white mb-4">All Accounts</h3>
        <div className="space-y-3">
          {accounts?.map(account => (
            <AccountCard
              key={account.id}
              account={account}
              isActive={account.id === activeAccount?.id}
              onRotate={() => rotate.mutate(account.id)}
            />
          ))}
        </div>
      </div>
    </div>
  );
}
```

---

## 14. SLB Safety Guardrails

### 14.1 REST API Endpoints (`/api/v1/safety`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/safety/status` | Safety system status |
| `GET` | `/safety/blocked` | Blocked commands |
| `POST` | `/safety/check` | Check command safety |
| `POST` | `/safety/install` | Install safety hooks |
| `POST` | `/safety/uninstall` | Uninstall hooks |
| `GET` | `/policy` | Current policy |
| `PUT` | `/policy` | Update policy |
| `POST` | `/policy/validate` | Validate policy |
| `POST` | `/policy/reset` | Reset to defaults |
| `GET` | `/approvals` | List pending approvals |
| `GET` | `/approvals/{id}` | Approval details |
| `POST` | `/approvals/{id}/approve` | Approve action |
| `POST` | `/approvals/{id}/deny` | Deny action |
| `GET` | `/approvals/history` | Approval history |
| `GET` | `/hooks/status` | Hook status |
| `POST` | `/hooks/install` | Install hooks |
| `POST` | `/hooks/uninstall` | Uninstall hooks |
| `GET` | `/guards/status` | Guards status |
| `POST` | `/guards/install` | Install guards |
| `POST` | `/guards/uninstall` | Uninstall guards |

### 14.2 Approval Workflow Component

```tsx
// components/safety/ApprovalWorkflow.tsx
'use client';

export function ApprovalWorkflow() {
  const { data: pendingApprovals } = usePendingApprovals();
  const approve = useApprove();
  const deny = useDeny();

  return (
    <div className="space-y-6">
      {/* Pending Count */}
      <div className={`rounded-xl p-6 border ${
        pendingApprovals?.length > 0
          ? 'bg-amber-900/20 border-amber-500/30'
          : 'bg-slate-900 border-slate-800'
      }`}>
        <div className="flex items-center gap-4">
          <ShieldAlertIcon className={`w-8 h-8 ${
            pendingApprovals?.length > 0 ? 'text-amber-400' : 'text-slate-500'
          }`} />
          <div>
            <h3 className="text-lg font-semibold text-white">
              {pendingApprovals?.length ?? 0} Pending Approvals
            </h3>
            <p className="text-sm text-slate-400">
              Actions requiring human review
            </p>
          </div>
        </div>
      </div>

      {/* Approval List */}
      <div className="space-y-4">
        {pendingApprovals?.map(approval => (
          <motion.div
            key={approval.id}
            initial={{ opacity: 0, y: 20 }}
            animate={{ opacity: 1, y: 0 }}
            className="bg-slate-900 rounded-xl p-6 border border-slate-800"
          >
            <div className="flex items-start justify-between">
              <div>
                <div className="flex items-center gap-2">
                  <RiskBadge level={approval.risk_level} />
                  <h4 className="text-white font-medium">{approval.action}</h4>
                </div>
                <p className="text-sm text-slate-400 mt-2">{approval.description}</p>
                <div className="flex items-center gap-4 mt-3 text-xs text-slate-500">
                  <span>Requested by: {approval.requestor}</span>
                  <span>Session: {approval.session}</span>
                  <span>{formatRelativeTime(approval.requested_at)}</span>
                </div>
              </div>

              <div className="flex gap-2">
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => deny.mutate(approval.id)}
                >
                  Deny
                </Button>
                <Button
                  variant="default"
                  size="sm"
                  onClick={() => approve.mutate(approval.id)}
                >
                  Approve
                </Button>
              </div>
            </div>

            {/* Details Expandable */}
            {approval.details && (
              <details className="mt-4">
                <summary className="text-sm text-slate-400 cursor-pointer">
                  View Details
                </summary>
                <pre className="mt-2 p-3 bg-slate-950 rounded text-xs text-slate-300 overflow-x-auto">
                  {JSON.stringify(approval.details, null, 2)}
                </pre>
              </details>
            )}
          </motion.div>
        ))}
      </div>
    </div>
  );
}
```

---

## 15. Pipeline & Workflow Engine

### 15.1 REST API Endpoints (`/api/v1/pipelines`)

| Method | Endpoint | CLI Equivalent | Description |
|--------|----------|----------------|-------------|
| `GET` | `/pipelines` | `ntm pipeline list` | List pipelines |
| `POST` | `/pipelines/run` | `ntm pipeline run` | Run pipeline |
| `GET` | `/pipelines/{id}` | `ntm pipeline status` | Pipeline status |
| `POST` | `/pipelines/{id}/cancel` | `ntm pipeline cancel` | Cancel pipeline |
| `POST` | `/pipelines/{id}/resume` | `ntm pipeline resume` | Resume pipeline |
| `POST` | `/pipelines/cleanup` | `ntm pipeline cleanup` | Cleanup |
| `POST` | `/pipelines/exec` | `ntm pipeline exec` | Execute step |
| `GET` | `/pipelines/templates` | — | Pipeline templates |
| `POST` | `/pipelines/validate` | — | Validate pipeline |

### 15.2 Visual Pipeline Builder (Conceptual)

```tsx
// components/pipeline/PipelineBuilder.tsx
'use client';

import ReactFlow, { addEdge, useNodesState, useEdgesState } from 'reactflow';

const stepTypes = {
  spawn: SpawnStepNode,
  send: SendStepNode,
  wait: WaitStepNode,
  scan: ScanStepNode,
  checkpoint: CheckpointStepNode,
  condition: ConditionNode,
};

export function PipelineBuilder() {
  const [nodes, setNodes, onNodesChange] = useNodesState([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState([]);

  const onConnect = useCallback((params) => {
    setEdges((eds) => addEdge(params, eds));
  }, []);

  const exportPipeline = () => {
    const workflow = {
      name: 'Custom Pipeline',
      steps: nodes.map(node => ({
        id: node.id,
        type: node.type,
        config: node.data,
        depends_on: edges
          .filter(e => e.target === node.id)
          .map(e => e.source),
      })),
    };
    return workflow;
  };

  return (
    <div className="h-[600px] bg-slate-950 rounded-xl border border-slate-800">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onConnect={onConnect}
        nodeTypes={stepTypes}
        fitView
      >
        <Background />
        <Controls />

        {/* Step Palette */}
        <Panel position="top-left">
          <div className="bg-slate-900 rounded-lg p-3 space-y-2">
            <h4 className="text-xs font-medium text-slate-400 mb-2">Steps</h4>
            <DraggableStep type="spawn" label="Spawn" icon={PlayIcon} />
            <DraggableStep type="send" label="Send" icon={SendIcon} />
            <DraggableStep type="wait" label="Wait" icon={ClockIcon} />
            <DraggableStep type="scan" label="Scan" icon={SearchIcon} />
            <DraggableStep type="checkpoint" label="Checkpoint" icon={SaveIcon} />
            <DraggableStep type="condition" label="Condition" icon={GitBranchIcon} />
          </div>
        </Panel>
      </ReactFlow>
    </div>
  );
}
```

---

## 16. Web UI Layer

### 16.1 UX Thesis

NTM's web UI should not be "tmux in a browser".

It should be:
- **A cockpit** (overview → drilldown)
- **A lens** (see what matters now)
- **A coordinator** (send actions safely and confidently)
- **A recorder** (history, analytics, replay, audit)

### 16.2 Design Principles

1. **Clarity over cleverness** — Gradients and motion create hierarchy, not noise
2. **Focus with progressive disclosure** — Show what matters at a glance; details one click away
3. **Latency is a design feature** — 0-jank streaming, optimistic UI, intentional skeletons
4. **Keyboard-first on desktop** — Global palette, pane switching, search everywhere
5. **Thumb-first on mobile** — Bottom nav, large targets, swipe navigation

### 16.3 Routes (Information Architecture)

```
/connect          — Connect to NTM server
/sessions         — Sessions overview (cards + filters)
/sessions/[name]  — Session dashboard
  /panes/[idx]    — Pane detail (live output + prompt)
/beads            — Kanban + Galaxy view
/beads/[id]       — Bead detail
/mail             — Agent Mail inbox
/mail/threads/[id] — Thread view
/memory           — CASS search + CM rules
/scanner          — UBS dashboard
/accounts         — CAAM manager
/safety           — Approvals + policy
/pipelines        — Pipeline builder + runs
/analytics        — Usage metrics
/settings         — Server config
```

### 16.4 "Decks" Organization

The UI is organized into **Decks** for each flywheel component:

| Deck | Primary Data Source | Key Components |
|------|---------------------|----------------|
| **Dashboard** | `robot.SnapshotOutput` | Overview cards, alerts, quick actions |
| **Sessions** | `robot.StatusOutput` | Session grid, pane viewers, xterm.js |
| **Beads** | `bv.TriageResponse` | Kanban, Galaxy view, triage panel |
| **Comms** | `agentmail.InboxMessage` | Three-pane email, reservations map |
| **Memory** | CASS + CM APIs | Semantic search, context studio |
| **Scanner** | `scanner.ScanResult` | Severity charts, findings list, treemap |
| **Accounts** | CAAM API | Account cards, quota rings |
| **Safety** | Approval API | Approval inbox, policy editor |
| **Pipeline** | Pipeline API | Visual builder, execution monitor |

### 16.5 Signature UI Components

#### Session Card

```tsx
// components/session/SessionCard.tsx
export function SessionCard({ session }: { session: Session }) {
  return (
    <motion.div
      whileHover={{ scale: 1.02 }}
      className="bg-gradient-to-br from-slate-900 to-slate-800 rounded-xl p-6 border border-slate-700"
    >
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-lg font-semibold text-white">{session.name}</h3>
        <HealthIndicator health={session.health} />
      </div>

      {/* Agent counts */}
      <div className="flex gap-2 mb-4">
        <AgentBadge type="claude" count={session.agents.claude} />
        <AgentBadge type="codex" count={session.agents.codex} />
        <AgentBadge type="gemini" count={session.agents.gemini} />
      </div>

      {/* Activity sparkline */}
      <ActivitySparkline data={session.activity_history} />

      <div className="flex items-center justify-between mt-4 pt-4 border-t border-slate-700">
        <span className="text-xs text-slate-400">
          {formatRelativeTime(session.last_activity)}
        </span>
        <Button size="sm">Resume</Button>
      </div>
    </motion.div>
  );
}
```

#### Pane Stream Viewer (Virtualized)

```tsx
// components/terminal/PaneStreamViewer.tsx
import { useVirtualizer } from '@tanstack/react-virtual';

export function PaneStreamViewer({ sessionName, paneIndex }: Props) {
  const parentRef = useRef<HTMLDivElement>(null);
  const [lines, setLines] = useState<OutputLine[]>([]);

  // WebSocket subscription
  useWebSocket(`panes:${sessionName}:${paneIndex}`, {
    onMessage: (event) => {
      if (event.type === 'pane.output.append') {
        setLines(prev => [...prev, ...event.data.lines].slice(-10000));
      }
    },
  });

  const virtualizer = useVirtualizer({
    count: lines.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 20,
    overscan: 50,
  });

  // Auto-scroll to bottom
  useEffect(() => {
    if (autoScroll) {
      virtualizer.scrollToIndex(lines.length - 1);
    }
  }, [lines.length]);

  return (
    <div
      ref={parentRef}
      className="h-full overflow-auto bg-slate-950 font-mono text-sm"
    >
      <div
        style={{ height: `${virtualizer.getTotalSize()}px`, position: 'relative' }}
      >
        {virtualizer.getVirtualItems().map(virtualItem => (
          <div
            key={virtualItem.key}
            style={{
              position: 'absolute',
              top: 0,
              left: 0,
              width: '100%',
              transform: `translateY(${virtualItem.start}px)`,
            }}
          >
            <OutputLine line={lines[virtualItem.index]} />
          </div>
        ))}
      </div>
    </div>
  );
}
```

#### Command Palette

```tsx
// components/palette/CommandPalette.tsx
import { Command } from 'cmdk';

export function CommandPalette() {
  const [open, setOpen] = useState(false);
  const { data: commands } = usePaletteCommands();

  useHotkeys('mod+k', () => setOpen(true));

  return (
    <Command.Dialog
      open={open}
      onOpenChange={setOpen}
      className="fixed inset-0 z-50 flex items-start justify-center pt-[20vh]"
    >
      <Command.Input
        placeholder="Type a command or search..."
        className="w-full px-4 py-3 bg-slate-900 border-b border-slate-700 text-white"
      />

      <Command.List className="max-h-96 overflow-auto p-2">
        <Command.Group heading="Sessions">
          {commands?.sessions.map(cmd => (
            <Command.Item key={cmd.id} onSelect={() => execute(cmd)}>
              {cmd.icon}
              <span>{cmd.label}</span>
              <kbd>{cmd.shortcut}</kbd>
            </Command.Item>
          ))}
        </Command.Group>

        <Command.Group heading="Agents">
          {commands?.agents.map(cmd => (
            <Command.Item key={cmd.id} onSelect={() => execute(cmd)}>
              {cmd.icon}
              <span>{cmd.label}</span>
            </Command.Item>
          ))}
        </Command.Group>
      </Command.List>
    </Command.Dialog>
  );
}
```

#### Conflict Heatmap

```tsx
// components/conflicts/ConflictHeatmap.tsx
export function ConflictHeatmap() {
  const { data: conflicts } = useConflicts();

  // Build matrix: files (y) × agents (x)
  const { files, agents, matrix } = buildConflictMatrix(conflicts);

  return (
    <div className="bg-slate-900 rounded-xl p-6 border border-slate-800">
      <h3 className="text-lg font-semibold text-white mb-4">Conflict Map</h3>

      <div className="overflow-x-auto">
        <table className="w-full">
          <thead>
            <tr>
              <th className="text-left text-xs text-slate-500 pb-2">File</th>
              {agents.map(agent => (
                <th key={agent} className="text-center text-xs text-slate-500 pb-2 px-2">
                  {agent}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {files.map(file => (
              <tr key={file}>
                <td className="text-xs text-slate-300 font-mono py-1 pr-4 truncate max-w-48">
                  {file}
                </td>
                {agents.map(agent => {
                  const cell = matrix[file]?.[agent];
                  return (
                    <td key={agent} className="text-center px-2 py-1">
                      {cell && (
                        <div
                          className={`w-6 h-6 rounded ${
                            cell.severity === 'high'
                              ? 'bg-red-500'
                              : cell.severity === 'medium'
                              ? 'bg-amber-500'
                              : 'bg-blue-500'
                          }`}
                          title={`${agent}: ${cell.reason}`}
                        />
                      )}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
```

---

## 17. Desktop vs Mobile UX Strategy

### 17.1 Desktop Layout

```
┌─────────────────────────────────────────────────────────────────────┐
│                          TOP NAV BAR                                 │
│  Logo │ Session Selector │ Search │ Alerts │ Settings │ User        │
├──────────┬─────────────────────────────────────┬───────────────────┤
│          │                                     │                   │
│  LEFT    │           MAIN CONTENT              │      RIGHT        │
│  RAIL    │                                     │    INSPECTOR      │
│          │   (Session Grid / Beads Kanban /    │                   │
│  Sessions│     Terminal / Mail / etc.)         │   Selected Item   │
│  Decks   │                                     │   Details         │
│  Search  │                                     │   Actions         │
│          │                                     │   History         │
│          │                                     │                   │
├──────────┴─────────────────────────────────────┴───────────────────┤
│                        STATUS BAR                                    │
│  Connected │ 3 Sessions │ 12 Agents │ 2 Alerts │ Last sync: 2s ago  │
└─────────────────────────────────────────────────────────────────────┘
```

**Desktop-specific features:**
- Command palette: `⌘K` / `Ctrl+K`
- Split views: View multiple panes side-by-side
- Keyboard navigation: Number keys to switch panes
- Hover previews: Quick look without clicking
- Right-click context menus

### 17.2 Mobile Layout

```
┌─────────────────────┐
│     TOP HEADER      │
│  ≡  Session Name  ⋮ │
├─────────────────────┤
│                     │
│                     │
│    MAIN CONTENT     │
│                     │
│  (Stacked Cards /   │
│   Swipeable Views)  │
│                     │
│                     │
│                     │
├─────────────────────┤
│    BOTTOM NAV       │
│ 🏠  📋  💬  🔔  ⚙️  │
│Home Beads Mail Alert Set│
└─────────────────────┘
```

**Mobile-specific features:**
- Bottom navigation: 5 primary sections
- Swipe gestures: Switch between agents/panes
- Pull-to-refresh: Update data
- Floating action button: Quick send/interrupt
- Haptic feedback: Confirm actions
- Voice input: Send prompts (future)

### 17.3 Responsive Breakpoints

| Breakpoint | Width | Layout |
|------------|-------|--------|
| `sm` | < 640px | Mobile (stacked) |
| `md` | 640-1024px | Tablet (collapsed rail) |
| `lg` | 1024-1280px | Desktop (full) |
| `xl` | > 1280px | Desktop (expanded inspector) |

### 17.4 UI Performance Budgets

| Metric | Target | Method |
|--------|--------|--------|
| Session list interaction | < 100ms | Optimistic UI |
| Pane stream scroll | 60fps | Virtualization |
| WebSocket processing | Non-blocking | Web Worker |
| Initial load (desktop) | < 2s | Code splitting |
| Initial load (mobile) | < 3s | Progressive loading |

---

## 18. Agent SDK Integration Strategy

### 18.1 Agent Driver Abstraction

To support both tmux-based and SDK-based agent execution:

```go
// internal/agents/driver.go
type AgentDriver interface {
    Spawn(ctx context.Context, config AgentConfig) (*Agent, error)
    Send(ctx context.Context, agentID string, message string) error
    Interrupt(ctx context.Context, agentID string) error
    GetOutput(ctx context.Context, agentID string, since time.Time) ([]OutputLine, error)
    Subscribe(ctx context.Context, agentID string) (<-chan Event, error)
    Close() error
}

// internal/agents/tmux_driver.go
type TmuxDriver struct {
    client *tmux.Client
}

// internal/agents/acp_driver.go (future)
type ACPDriver struct {
    // Agent Client Protocol implementation
}

// internal/agents/sdk_driver.go (future)
type SDKDriver struct {
    claudeSDK *claude.Client
    codexSDK  *codex.Client
    geminiSDK *genai.Client
}
```

### 18.2 Execution Modes

| Mode | Driver | Features | Use Case |
|------|--------|----------|----------|
| **Tmux** | `TmuxDriver` | Visual terminal, battle-tested | Default, power users |
| **ACP** | `ACPDriver` | Structured events, diff rendering | IDE integration |
| **SDK** | `SDKDriver` | Programmatic, no tmux dependency | Automation, CI/CD |

### 18.3 Phase-In Strategy

1. **Phase 1 (Now):** Tmux-only, current behavior
2. **Phase 2 (v2.0):** Add Agent Driver interface, refactor tmux code
3. **Phase 3 (v2.5):** Add ACP driver for structured events
4. **Phase 4 (v3.0):** Add SDK driver for direct API mode

---

## 19. Security Model

### 19.1 Default Safety Posture

- `ntm serve` binds to **127.0.0.1 only** by default
- To bind externally, require explicit flags:
  - `--listen 0.0.0.0`
  - `--auth required`
  - `--tls` (recommended)

### 19.2 Authentication Options

| Mode | Description | Use Case |
|------|-------------|----------|
| **Local** | No auth required | localhost development |
| **API Key** | Bearer token | Single-user remote |
| **OIDC** | OAuth/SSO integration | Multi-user org |
| **mTLS** | Client certificates | High-security |

### 19.3 RBAC Roles

| Role | Permissions |
|------|-------------|
| `viewer` | Read sessions, output, status |
| `operator` | Send prompts, interrupt, checkpoint |
| `admin` | Kill sessions, config, safety, accounts |

### 19.4 Approval Flow for Dangerous Actions

Actions marked `SafetyLevel: dangerous` require explicit approval:

```
1. Client calls POST /api/v1/sessions/prod/kill
2. Server returns 409 APPROVAL_REQUIRED with approval_token
3. Client shows confirmation dialog
4. User confirms, client calls POST /api/v1/approvals/{token}/approve
5. Server executes action
```

### 19.5 Audit Trail

Every mutating API call records:
- **Who**: Token/user identity
- **What**: Command + parameters
- **When**: Timestamp
- **Result**: Success/failure + output
- **Correlation ID**: For tracing

---

## 20. Testing Strategy

### 20.1 Test Categories

| Category | Description | Tools |
|----------|-------------|-------|
| **Unit** | Service layer, business logic | Go `testing` |
| **Contract** | OpenAPI spec vs implementation | `oasdiff`, custom |
| **Parity** | CLI output = API output | Custom harness |
| **Integration** | Full API + tmux | `testcontainers` |
| **E2E** | Web UI flows | Playwright |
| **Load** | WebSocket concurrency | k6 |

### 20.2 Parity Tests

For each kernel command:
1. Execute via CLI wrapper
2. Execute via REST API
3. Compare normalized output (where deterministic)

```go
func TestSessionListParity(t *testing.T) {
    // CLI execution
    cliOutput := runCLI("ntm", "--robot-status")

    // API execution
    resp := httpClient.Get("/api/v1/sessions")
    apiOutput := parseJSON(resp.Body)

    // Compare
    assert.JSONEqual(t, cliOutput, apiOutput)
}
```

### 20.3 WebSocket Tests

- Reconnection / resume from cursor
- Backpressure behavior
- Dropped output correctness
- Multi-client fan-out

### 20.4 UI E2E Tests

```typescript
// tests/e2e/session-spawn.spec.ts
test('spawn session and send prompt', async ({ page }) => {
  await page.goto('/sessions');
  await page.click('button:has-text("New Session")');
  await page.fill('input[name="sessionName"]', 'test-project');
  await page.click('button:has-text("Create")');

  await expect(page.locator('.session-card')).toContainText('test-project');

  await page.click('.session-card:has-text("test-project")');
  await page.fill('textarea[name="prompt"]', 'Hello, agent!');
  await page.click('button:has-text("Send")');

  await expect(page.locator('.pane-output')).toContainText('received', { timeout: 10000 });
});
```

---

## 21. Risk Register & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|------|--------|------------|------------|
| **API drift / feature mismatch** | High | Medium | Command Kernel + Parity Gate CI |
| **WebSocket scaling (many panes)** | Medium | Medium | pipe-pane, ring buffers, virtualization |
| **Security exposure (remote control)** | High | Low | localhost default, mandatory auth, RBAC |
| **"tmux in browser" complexity** | Medium | Medium | Start with streaming, add terminal later |
| **Vercel WebSocket limitations** | Low | Certain | UI on Vercel, WS daemon elsewhere |
| **Performance with large outputs** | Medium | Medium | Virtualization, throttling, Web Worker |
| **Mobile UX compromises** | Low | Medium | Separate mobile layouts, not responsive-only |

---

## 22. Implementation Phases

### Phase 0: Kernel Refactor (Weeks 1-3)

**Goal:** Create Command Kernel for mechanical parity

- [ ] Create `internal/kernel` package with command registry
- [ ] Refactor `internal/robot` into GetX() + PrintX() pattern
- [ ] Wire CLI commands through kernel
- [ ] Add Parity Gate CI tests

### Phase 1: REST API Foundation (Weeks 4-6)

**Goal:** Core sessions/agents API

- [ ] Expand `internal/serve` with chi router
- [ ] Implement session endpoints (CRUD, spawn, kill)
- [ ] Implement agent endpoints (send, interrupt, wait)
- [ ] Add OpenAPI spec generation
- [ ] Add Swagger UI at `/docs`

### Phase 2: Full REST Parity (Weeks 7-10)

**Goal:** Every CLI command via REST

- [ ] Output tooling endpoints
- [ ] Checkpoint endpoints
- [ ] Beads/BV endpoints
- [ ] Agent Mail endpoints
- [ ] Scanner endpoints
- [ ] Safety/approval endpoints
- [ ] Idempotency key support
- [ ] Jobs for long-running operations

### Phase 3: WebSocket Streaming (Weeks 11-13)

**Goal:** Real-time event layer

- [ ] WebSocket hub implementation
- [ ] Topic-based subscriptions
- [ ] pipe-pane capture integration
- [ ] Event persistence in SQLite
- [ ] Replay/resume with cursors
- [ ] Backpressure handling

### Phase 4: Web UI MVP (Weeks 14-17)

**Goal:** Functional web interface

- [ ] Next.js 16 setup with TanStack Query
- [ ] Session list and detail views
- [ ] Pane viewer with virtualization
- [ ] Command palette
- [ ] Beads Kanban view
- [ ] Mail inbox
- [ ] Mobile responsive layout

### Phase 5: Full Flywheel Integration (Weeks 18-22)

**Goal:** All 8 tools integrated

- [ ] Scanner dashboard
- [ ] Memory search + context studio
- [ ] Account manager
- [ ] Safety/approval workflow
- [ ] Pipeline builder
- [ ] Galaxy view (dependency graph)
- [ ] Conflict heatmap

### Phase 6: Polish & Mobile (Weeks 23-26)

**Goal:** Production-ready quality

- [ ] Mobile-specific layouts
- [ ] Touch interactions
- [ ] Performance optimization
- [ ] Accessibility audit
- [ ] Documentation
- [ ] Design system finalization

---

## 23. File Structure

### Go Backend Additions

```
internal/
├── kernel/                    # Command Kernel
│   ├── registry.go
│   ├── command.go
│   └── parity_test.go
├── api/                       # REST API layer
│   ├── api.go
│   ├── handlers/
│   │   ├── sessions.go
│   │   ├── agents.go
│   │   ├── panes.go
│   │   ├── beads.go
│   │   ├── mail.go
│   │   ├── reservations.go
│   │   ├── cass.go
│   │   ├── memory.go
│   │   ├── scanner.go
│   │   ├── accounts.go
│   │   ├── pipelines.go
│   │   ├── safety.go
│   │   └── system.go
│   ├── middleware/
│   │   ├── auth.go
│   │   ├── cors.go
│   │   ├── logging.go
│   │   └── ratelimit.go
│   └── openapi/
│       └── spec.go
├── ws/                        # WebSocket layer
│   ├── hub.go
│   ├── client.go
│   ├── topics.go
│   ├── backpressure.go
│   └── channels/
│       ├── output.go
│       ├── beads.go
│       ├── mail.go
│       └── ...
└── serve/
    └── server.go              # Enhanced server
```

### Frontend Structure

```
web/
├── app/
│   ├── (auth)/
│   │   ├── dashboard/
│   │   ├── sessions/
│   │   │   └── [name]/
│   │   │       └── panes/
│   │   │           └── [idx]/
│   │   ├── beads/
│   │   ├── mail/
│   │   ├── memory/
│   │   ├── scanner/
│   │   ├── accounts/
│   │   ├── safety/
│   │   ├── pipelines/
│   │   └── settings/
│   └── layout.tsx
├── components/
│   ├── ui/                    # Shadcn/UI base
│   ├── dashboard/
│   ├── session/
│   ├── terminal/
│   ├── beads/
│   ├── mail/
│   ├── memory/
│   ├── scanner/
│   ├── safety/
│   ├── pipeline/
│   └── mobile/
├── hooks/
│   ├── useWebSocket.ts
│   ├── useSession.ts
│   └── ...
├── lib/
│   ├── api.ts
│   └── ws.ts
├── stores/
│   └── ui.ts
└── types/
    └── api.ts                 # Generated from OpenAPI
```

---

## 24. Technical Specifications

### API Performance Targets

| Metric | Target |
|--------|--------|
| REST response time (p50) | < 50ms |
| REST response time (p99) | < 200ms |
| WebSocket latency | < 100ms |
| Concurrent WS connections | 1000+ |
| Event throughput | 10k events/sec |

### Frontend Performance Targets

| Metric | Target |
|--------|--------|
| First Contentful Paint | < 1.2s |
| Largest Contentful Paint | < 2.5s |
| Time to Interactive | < 3.5s |
| Cumulative Layout Shift | < 0.1 |
| Pane scroll FPS | 60fps |

### Browser Support

| Browser | Minimum Version |
|---------|-----------------|
| Chrome | 111+ |
| Firefox | 111+ |
| Safari | 16.4+ |
| Edge | 111+ |

---

## Appendix A: Complete CLI/REST Parity Matrix

See the GPT plan's Appendix A for the exhaustive mapping of every CLI command to REST endpoint. Key categories:

- **Session lifecycle:** create, spawn, quick, add, attach, list, status, view, zoom, kill
- **Agent actions:** send, interrupt, wait, replay, activity, summary, health, quota, rotate
- **Output tooling:** copy, save, grep, extract, diff, changes, conflicts
- **History & metrics:** history show/clear/stats/export/prune, metrics show/compare/export/snapshot
- **Checkpoints:** save, list, show, delete, verify, export, import, rollback
- **Flywheel tools:** beads/daemon, work/triage, cass/search, context/build, memory/serve
- **Agent Mail:** mail/send, message/inbox, lock/unlock, locks/list
- **Safety:** approve, safety/status, policy/show, hooks/install, guards/status
- **Config:** config/init/show/set/validate, setup, deps, doctor

---

## Appendix B: Robot Mode Parity Matrix

Every `--robot-*` flag maps to a REST endpoint:

| Robot Flag | Handler | REST Endpoint |
|------------|---------|---------------|
| `--robot-status` | `robot.PrintStatus` | `GET /api/v1/robot/status` |
| `--robot-snapshot` | `robot.PrintSnapshot` | `GET /api/v1/robot/snapshot` |
| `--robot-tail` | `robot.PrintTail` | `GET /api/v1/robot/tail` |
| `--robot-send` | `robot.PrintSend` | `POST /api/v1/robot/send` |
| `--robot-spawn` | `robot.PrintSpawn` | `POST /api/v1/robot/spawn` |
| `--robot-interrupt` | `robot.PrintInterrupt` | `POST /api/v1/robot/interrupt` |
| `--robot-health` | `robot.PrintHealth` | `GET /api/v1/robot/health` |
| `--robot-activity` | `robot.PrintActivity` | `GET /api/v1/robot/activity` |
| `--robot-context` | `robot.PrintContext` | `GET /api/v1/robot/context` |
| `--robot-graph` | `robot.PrintGraph` | `GET /api/v1/robot/graph` |
| `--robot-plan` | `robot.PrintPlan` | `GET /api/v1/robot/plan` |
| `--robot-terse` | `robot.PrintTerse` | `GET /api/v1/robot/terse` |
| `--robot-dashboard` | `robot.PrintDashboard` | `GET /api/v1/robot/dashboard` |
| `--robot-cass-*` | `robot.PrintCASS*` | `GET /api/v1/robot/cass/*` |
| `--robot-beads-*` | `robot.PrintBeads*` | `GET /api/v1/robot/beads/*` |
| `--robot-pipeline-*` | `pipeline.Print*` | `GET/POST /api/v1/robot/pipeline/*` |

---

## Appendix C: References

- [Agent Client Protocol](https://agentclientprotocol.com/)
- [Claude Code in Zed via ACP](https://zed.dev/blog/claude-code-via-acp)
- [@anthropic-ai/claude-agent-sdk](https://www.npmjs.com/package/@anthropic-ai/claude-agent-sdk)
- [@openai/codex-sdk](https://developers.openai.com/codex/sdk/)
- [@google/genai](https://www.npmjs.com/package/@google/genai)
- [Next.js 16](https://nextjs.org/blog/next-16)
- [TanStack Query + WebSockets](https://tkdodo.eu/blog/using-web-sockets-with-react-query)
- [WebSocket Architecture Best Practices](https://ably.com/topic/websocket-architecture-best-practices)
- [Stripe Apps UI Toolkit](https://docs.stripe.com/stripe-apps/components)

---

## What "Done" Looks Like

When this plan is executed, NTM becomes:

- A **first-class platform**, not just a CLI
- A **web cockpit** that makes multi-agent orchestration feel easy and beautiful
- An **automation substrate** where agents can self-serve via OpenAPI and stream events via WebSocket
- A system that stays **safe-by-default**, even as it gets more powerful
- The **unified gateway** to the entire Agent Flywheel ecosystem

**Single mantra for implementation:**

> **Make the API the truth, and make the UI a gorgeous lens over it.**

---

*Document Version: 3.0.0 (Ultimate Hybrid)*
*Last Updated: January 7, 2026*
*Author: Claude Opus 4.5*
*Incorporates insights from: Gemini, GPT, GPT Pro plans*

