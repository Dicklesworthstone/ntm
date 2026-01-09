# PLAN_TO_ADD_WEB_UI_AND_REST_AND_WEBSOCKET_API_LAYERS_TO_NTM__GPT.md

## 0) Executive Summary (Rewritten, Deep‑Dive Edition)

We will evolve NTM into a **full Flywheel Control Plane** with a complete REST API and high‑performance WebSocket streaming layer that preserves the existing Go/tmux core. The web app will be the flagship UI for the Agentic Coding Flywheel (ACFS) stack, not a superficial dashboard. Every CLI/TUI feature will be reachable via API, and every flywheel tool (Agent Mail, UBS, BV/BD, CASS, CM, S2P, SLB) will be modeled and visible in realtime.

The design centers on:
- **Parity**: API = CLI/TUI capability surface.
- **Streaming**: outputs, events, approvals, conflicts, health, and tool activity flow live.
- **Flywheel workflow**: the UI maps directly to Plan → Coordinate → Execute → Scan → Remember → Safety.
- **Go core preserved**: existing packages become services; no logic duplication.

### 0.1 Non‑Negotiables (Blended Best‑Ideas)
- **Flywheel‑first UX**: every feature accelerates Plan → Coordinate → Execute → Scan → Remember → Safety.
- **One core, many interfaces**: CLI/TUI/REST/Web share a single Command Kernel + service layer.
- **Idempotent + auditable**: every mutation is logged, replayable, and safe to re‑call.
- **Local‑first security**: bind localhost by default, token auth + optional RBAC, SLB enforced everywhere.
- **Performance by design**: tmux `pipe-pane` streaming, event bus fan‑out, sub‑100ms propagation.
- **Accessibility and polish**: WCAG 2.1 AA baseline, Stripe‑grade visual system, mobile‑first.

---

## 1) Ground‑Truth: How NTM Actually Works Today

This section is based on real code paths in this repo. These are the foundations we must reuse, not replace.

### 1.1 CLI + Robot Entry Points
- CLI root + command tree: `internal/cli/root.go`
- Robot JSON mode: `internal/robot/*` (invoked from root run path)
- Key detail: the root command already short‑circuits to robot mode for non‑interactive usage. This is the **closest existing API surface** and should be mapped 1:1 into REST.

### 1.2 tmux Core Layer
- tmux client abstraction: `internal/tmux/client.go`
- Session/pane parsing & operations: `internal/tmux/session.go`
- Pane title parsing encodes agent type/variant/tags. This is the **canonical identity model**.

### 1.3 State Store (SQLite)
- Store API: `internal/state/store.go`
- Schema types: `internal/state/schema.go`
- Key entities already exist and should be exposed via REST:
  - Sessions, Agents, Tasks, Reservations, Approvals, ContextPacks, ToolHealth

### 1.4 Event Bus
- `internal/events/bus.go`
- Provides in‑memory pub/sub + ring buffer history. This is the natural source for WS streaming. Must be extended for event persistence and replay.

### 1.5 Tool Adapter Framework (Flywheel Integration)
- `internal/tools/*` includes adapters for bv, bd, am, cm, cass, s2p
- `internal/cli/doctor.go` already checks tool health.
- This adapter registry is an excellent foundation for API + UI tool health panels.

### 1.6 Context Pack Builder (Flywheel Brain)
- `internal/context/pack.go` builds context packs from BV/CM/CASS/S2P
- Token budgets per agent type; component allocation (triage/cm/cass/s2p)
- Persists packs into state store and renders prompt format per agent type

### 1.7 Scanner + UBS → Beads Bridge
- `internal/cli/scan.go` + `internal/scanner/*`
- UBS results can auto‑create beads (issue tracking), dedupe, update/close.
- This is a **hard flywheel loop**: Scan → Beads → BV → Context → Send.

### 1.8 Supervisor + Daemons
- `internal/supervisor/supervisor.go` manages long‑running daemons (cm server, bd daemon)
- Tracks PID, ports, health checks, restarts, logs
- This must be surfaced in API + UI for operational visibility.

### 1.9 Approvals + SLB (Two‑Person Rule)
- `internal/approval/engine.go` enforces approvals and SLB
- `internal/policy/policy.go` defines blocking/approval rules
- Approval queue + SLB decisioning should be first‑class API & UI features.

---

## 2) Flywheel Ecosystem (ACFS Stack Integration)

ACFS installs and wires the “Dicklesworthstone Stack” including NTM, Agent Mail, UBS, BV/BD, CASS, CM, SLB, and (future) CAAM. This is explicitly documented in ACFS manifest and onboarding.

### 2.1 ACFS Tool Stack (installed on VPS)
From `acfs.manifest.yaml` in the ACFS repo:
- `ntm` — cockpit
- `mcp_agent_mail` — coordination server (HTTP at 127.0.0.1:8765)
- `ubs` — bug scanner
- `bv`/`bd` — issue tracking + planning
- `cass` — semantic search
- `cm` — memory system
- `slb` — two‑person safety guard
- `caam` — account switching (planned; not currently in NTM core)

### 2.2 ACFS Flywheel Model
From ACFS onboarding lesson:
```
Plan (Beads/BV) → Coordinate (Agent Mail) → Execute (NTM/Agents)
     ↑                                                  ↓
Remember (CASS/CM) ← Scan (UBS) ← Safety (SLB)
```
The web UI should be explicitly structured around this loop.

### 2.3 Integration Implications
- REST API must expose **tool health** and **daemon status** for ACFS readiness checks.
- UI must surface whether these tools are installed/running.
- WS streaming should include tool events (UBS scan findings, BD daemon sync, CM context retrieval).

### 2.4 Agent Protocol & SDK Future‑Proofing (ACP + SDK Mode)
We should design the API/WS layer so tmux is **not the only future transport**:
- **ACP (Agent Client Protocol)** support keeps us aligned with industry tooling (Claude/Codex/Gemini adapters).
- **SDK mode** becomes a second agent backend (no tmux dependency, richer structured events).
- REST + WS should accept **either** tmux‑backed agents or ACP‑backed agents through the same envelopes.
- The UI should not assume panes are “tmux panes”; they are **agent channels** with a transport type.

---

## 3) What Exists Today in “Serve” Mode (Gap Analysis)

NTM has a small HTTP server:
- `internal/serve/server.go`
- Endpoints: sessions list/details, robot stubs, SSE `/events`

This is **not** feature‑complete. It lacks nearly all CLI/TUI features and does not use the robot output or service layer. We will treat `serve` as a skeleton to be replaced by a full API server.

### 3.1 New Runtime Modes
- `ntm serve` → starts REST + WS + (optional) static UI
- `ntm web` → convenience launcher for UI + API (dev server in dev, embedded assets in prod)

---

## 4) Target Architecture (Preserve Core, Add API/WS Layers)

### 4.0 Command Kernel (Single Source of Truth)
Create a **Command Kernel registry** that defines every command once (name, schema, side‑effects, safety).
This registry will drive:
- CLI commands (thin wrappers)
- TUI actions (palette + dashboard)
- REST endpoints (auto‑generated from registry)
- OpenAPI spec (auto‑generated with examples)
- Web UI command palette metadata

This makes **parity drift impossible** by design.

### 4.1 New Packages (Concrete)
```
internal/api/           # REST handlers (HTTP)
internal/api/openapi/   # OpenAPI spec and generator
internal/api/services/  # Extracted business logic (CLI + API reuse)
internal/ws/            # WebSocket hub + subscriptions
internal/stream/        # Stream message schema + envelope
```

### 4.2 Service Layer Extraction (No Duplication)
We will refactor CLI commands into reusable services:
- `SessionService` → create/spawn/kill/view/zoom
- `AgentService` → send/interrupt/wait/route
- `OutputService` → copy/save/grep/extract/diff
- `ContextService` → context packs
- `ToolingService` → doctor + adapters
- `ApprovalService` → approvals/SLB
- `BeadsService` → bd daemon
- `ScannerService` → UBS + bridge

CLI will call these services. REST will call these services. This is the parity guarantee.

### 4.3 Event & Stream Layer
- Use existing event bus (`internal/events`) as primary in‑memory bus.
- Add optional event persistence in SQLite for replay.
- Unified event envelope for WS streaming.
- **Use tmux `pipe-pane`** for output capture where possible (reduces polling CPU).

### 4.4 API Server & Router
- Use a lightweight router (Chi) with middleware for auth, CORS, rate limits.
- Use OpenAPI‑first codegen (`ogen`) for strongly‑typed handlers and client SDKs.
- JSON encoding: stdlib for correctness, optional `sonic` for hot paths.

### 4.5 Supervisor‑Managed Daemons
- Extend `internal/supervisor` to optionally start/monitor **cm**, **bd**, and **agent‑mail** servers.
- Surface health + logs in API and UI.

---

## 5) REST API Plan (CLI/TUI Parity Map)

Below is a high‑level parity map grouped by CLI command families.

### 5.0 API Conventions (Stability + Agent Friendliness)
- Base path: `/api/v1`
- Standard success envelope `{success, data?, warnings?, error?}`
- Standard error envelope `{error: {code, message, details}}`
- Cursor pagination: `?cursor=&limit=`
- Idempotency key header for all mutating requests
- Correlation ID header for tracing across REST + WS
- Explicit **when/why** guidance in OpenAPI (human + agent‑readable)
- Authentication: Bearer token + optional RBAC, local‑only default binding
- CORS allowlist + rate limits (defense‑in‑depth)

### 5.0.1 Error Taxonomy (Shared Across CLI/TUI/REST)
Error responses use the same envelope everywhere:
```
{ "error": { "code": "APPROVAL_REQUIRED", "message": "...", "details": { ... } } }
```
Core error codes + HTTP mapping:
- `INVALID_ARGUMENT` → 400
- `VALIDATION_FAILED` → 400 (schema errors with field details)
- `UNAUTHENTICATED` → 401
- `PERMISSION_DENIED` → 403
- `NOT_FOUND` → 404
- `CONFLICT` → 409 (name collisions, state conflict)
- `SESSION_BUSY` → 409 (active tmux pane busy)
- `AGENT_BUSY` → 409 (agent already executing)
- `PRECONDITION_FAILED` → 412 (idempotency key mismatch)
- `APPROVAL_REQUIRED` → 428 (SLB gate)
- `RATE_LIMITED` → 429
- `TOOL_UNAVAILABLE` → 503 (bv/cass/cm/am down)
- `TEMPORARY_UNAVAILABLE` → 503 (restart/backoff recommended)
- `NOT_IMPLEMENTED` → 501
- `INTERNAL` → 500 (always includes correlation_id)

### 5.1 Sessions & Agents
- `POST /api/v1/sessions` — create (from `newCreateCmd`)
- `POST /api/v1/sessions/{id}/spawn` — spawn agents (`newSpawnCmd`)
- `POST /api/v1/sessions/{id}/add` — add agents (`newAddCmd`)
- `GET /api/v1/sessions` — list (`newListCmd`)
- `GET /api/v1/sessions/{id}` — session details
- `POST /api/v1/sessions/{id}/attach` — attach/switch (`newAttachCmd`)
- `POST /api/v1/sessions/{id}/view` — tiled view (`newViewCmd`)
- `POST /api/v1/sessions/{id}/zoom` — zoom pane (`newZoomCmd`)
- `DELETE /api/v1/sessions/{id}` — kill (`newKillCmd`)

### 5.2 Agent Actions
- `POST /api/v1/sessions/{id}/send` — broadcast / targeting (`newSendCmd`)
- `POST /api/v1/sessions/{id}/interrupt` — stop agents (`newInterruptCmd`)
- `POST /api/v1/sessions/{id}/wait` — wait state (`newWaitCmd`)
- `POST /api/v1/sessions/{id}/replay` — replay history (`newReplayCmd`)
- `POST /api/v1/sessions/{id}/route` — smart routing (robot/route)

### 5.3 Output & Analysis
- `GET /api/v1/sessions/{id}/copy` (`newCopyCmd`)
- `GET /api/v1/sessions/{id}/save` (`newSaveCmd`)
- `POST /api/v1/sessions/{id}/grep` (`newGrepCmd`)
- `POST /api/v1/sessions/{id}/extract` (`newExtractCmd`)
- `GET /api/v1/sessions/{id}/diff` (`newDiffCmd`)
- `GET /api/v1/sessions/{id}/changes` (`newChangesCmd`)
- `GET /api/v1/sessions/{id}/conflicts` (`newConflictsCmd`)
- `GET /api/v1/sessions/{id}/summary` (`newSummaryCmd`)

### 5.4 TUI & Dashboard Parity
- `GET /api/v1/palette` + `POST /api/v1/palette/run`
- `GET /api/v1/dashboard/{id}` (data aggregation)
- `GET /api/v1/tutorial` (meta, not necessarily interactive)

### 5.5 Context Packs (Flywheel‑native)
- `POST /api/v1/context/build` (`internal/context/pack.go`)
- `GET /api/v1/context/{id}`
- `GET /api/v1/context/cache`

### 5.6 Tools + Doctor
- `GET /api/v1/tools`
- `GET /api/v1/tools/{name}`
- `GET /api/v1/doctor`

### 5.7 UBS + Beads Bridge
- `POST /api/v1/scan` (UBS)
- `POST /api/v1/scan/bridge` (auto‑beads)
- `POST /api/v1/scan/watch` (stream)

### 5.8 Beads / BV / BD Daemon
- `GET /api/v1/beads` (list)
- `POST /api/v1/beads/create|close|claim`
- `POST /api/v1/beads/daemon/start|stop|status`
- `POST /api/v1/bv/triage|plan|insights`

### 5.9 Agent Mail
- `GET /api/v1/agentmail/health`
- `POST /api/v1/agentmail/ensure_project`
- `POST /api/v1/agentmail/register_agent`
- `POST /api/v1/agentmail/send_message`
- `GET /api/v1/agentmail/inbox`
- `POST /api/v1/agentmail/file_reservations`

### 5.10 Approvals + SLB
- `POST /api/v1/approvals/request`
- `POST /api/v1/approvals/{id}/approve`
- `POST /api/v1/approvals/{id}/deny`
- `GET /api/v1/approvals`

### 5.11 Auth + Identity
- `POST /api/v1/auth/login`
- `POST /api/v1/auth/refresh`
- `POST /api/v1/auth/logout`
- `GET /api/v1/auth/whoami`

### 5.12 Robot Mode Parity
Every `--robot-*` flag becomes a REST endpoint for automation:
- `/api/v1/robot/status`, `/snapshot`, `/graph`, `/tail`, `/send`, `/ack`, `/spawn`, `/interrupt`, `/health`, `/recipes`, `/schema`, `/files`, `/inspect`, `/metrics`, `/alerts`, `/beads`, `/summary`, etc.

### 5.13 OpenAPI “Golden Path” (First‑class Example Sequence)
This is the minimal “happy path” a client or agent should follow:
1. `POST /api/v1/auth/login` → get token
2. `GET /api/v1/health` → sanity check
3. `POST /api/v1/sessions` → create session
4. `POST /api/v1/sessions/{id}/spawn` → spawn agents
5. `POST /api/v1/sessions/{id}/send` → send task prompt
6. `GET /api/v1/robot/status` → verify state
7. `GET /api/v1/robot/tail` → initial output buffer
8. `GET /api/v1/robot/snapshot` → full system view
9. `GET /api/v1/work/triage` → prioritize next tasks
10. `POST /api/v1/scan` → UBS scan
11. `GET /api/v1/mail/inbox` → check coordination
12. `GET /api/v1/approvals` → SLB queue
13. `GET /api/v1/context/build` → build context pack


---

## 6) WebSocket Streaming Layer (Flywheel‑grade)

### 6.1 WS Endpoint
- `wss://host/api/v1/stream`
- Client subscribes to channels by session/tool/type

### 6.2 Unified Envelope
```
{
  "type": "scanner.finding",
  "stream": "global|session|agent|tool",
  "session": "myproject",
  "seq": 48291,
  "timestamp": "2026-01-07T00:00:00Z",
  "correlation_id": "evt_...",
  "data": { ... }
}
```

### 6.3 Stream Categories (Expanded)
- session.output
- session.status
- agent.state
- tool.call / tool.result (best‑effort now, structured later)
- approval.requested / approval.resolved
- scanner.finding
- beads.update
- tool.health
- agentmail.message
- cass.index / cm.memory
- context.pack

### 6.4 Replay + Backpressure
- Cursor + event persistence in SQLite
- Per‑client throttling

### 6.5 Reliability Features
- Resume from `last_seq` to avoid losing output on reconnect
- Optional ACKs for critical events (approvals, mail)
- Event ordering per stream (session/agent/tool)

### 6.6 Scale‑Out Option
- Optional Redis/pubsub adapter to fan‑out events across multiple API nodes.

---

## 7) UI Plan (Flywheel‑Native + Stripe‑Grade)

### 7.1 Core Screens
- **Flywheel Control Center** (Plan/Coordinate/Execute/Scan/Remember/Safety)
- **Session Grid** (live pane cards, output streams)
- **Omnibar** (global Cmd+K palette for spawn/send/kill)
- **Context Pack Studio** (token budget sliders + preview)
- **UBS → Beads Hub** (scan findings → bead creation)
- **Approvals Inbox** (SLB workflow)
- **Agent Mail Hub** (threads + reservations)
- **Tool Health Dashboard** (doctor + daemon status)
- **Mission Control Decks**:
  - Orchestrator Deck (live terminals + agent grid)
  - Beads Deck (kanban + dependency graph “galaxy view”)
  - Comms Deck (mail threads + file reservation heatmap)
  - Health Deck (UBS findings + hotspots)
  - Safety Deck (SLB approvals + audit log)
  - Memory Deck (CASS/CM context search + rule browser)

### 7.1.1 Terminal Rendering
- Use **xterm.js** (WebGL renderer) for live panes.
- Fall back to “Read Mode” (Markdown) on mobile for readability.

### 7.2 Desktop Layout
- Left rail: Sessions + Flywheel steps
- Center: Live grid + detailed pane viewer
- Right: Alerts + Approvals + Tool Health

### 7.3 Mobile Layout
- Bottom nav: Sessions / Stream / Tools / Alerts / Settings
- Stacked cards for session panes
 - Swipe gestures to switch agents, pull‑to‑refresh

### 7.4 Design System Principles
- Tokenized color + spacing + typography scale
- Motion system (page‑load + staggered reveals, no gratuitous motion)
- Accessibility first: WCAG 2.1 AA, focus states, contrast, keyboard navigation

### 7.5 End‑to‑End Journeys (Real‑World Use)
**Journey A: New Project Kickoff (Desktop)**
1. Create session → spawn agents (Orchestrator Deck).
2. Send initial brief → watch live output (Terminal grid).
3. Run BV triage → pick “top 3” recommendations (Beads Deck).
4. Create context pack → broadcast to agents (Context Studio).

**Journey B: Incident Response (Desktop + Mobile)**
1. Receive UBS alert + approval request (Safety Deck).
2. Approve remediation (mobile‑safe view).
3. Jump to affected pane output + diff (Session Grid).
4. Create bead and assign agent (Beads Deck).

**Journey C: Coordination & Conflict Avoidance**
1. See live file reservations map (Comms Deck).
2. Request contact or reserve file (Agent Mail UI).
3. Send task update and link to run output.

**Journey D: Memory & Learning Loop**
1. Run CASS search for prior fix.
2. Pull CM context pack into active session.
3. Save result as rule to memory store.

---

## 8) ACFS‑Specific Integration

### 8.1 Stack Readiness Endpoint
- `GET /api/v1/flywheel/stack` returns installed tools, versions, daemon health

### 8.2 Wizard Integration
- ACFS wizard can probe `/health` and `/flywheel/stack` to verify NTM web server is live

### 8.3 CAAM Future Hook
- Placeholder endpoints for account switching (future‑proofing)

### 8.4 Deployment Strategy (Real‑World Constraints)
- **Web UI**: Vercel (fast static + SSR)
- **API + WS**: long‑lived server (Fly.io/Render/bare metal)
- **Remote access**: Tailscale / Cloudflare Tunnel / SSH port‑forward
- Split deployment is required because most serverless hosts don’t support durable WebSockets.

---

## 9) Implementation Phases (Strict, Measurable)

Phase 0 — Build parity matrix + OpenAPI outline
Phase 0.5 — Implement Command Kernel registry + codegen hooks
Phase 1 — Extract service layer + implement core sessions/agents
Phase 2 — Full REST parity + OpenAPI examples + CI drift checks
Phase 3 — WS streaming layer + event persistence
Phase 4 — Flywheel web UI
Phase 5 — ACFS integration + CAAM hooks

---

## 10) Validation & Testing

- Service layer unit tests
- API contract tests vs OpenAPI
- WS load tests (burst + sustained)
- End‑to‑end flow tests for flywheel loop
- CI guard: regenerate OpenAPI + diff check + schema validation

---

## 11) Open Questions

- Should REST allow remote execution by default or be localhost‑only?
- Should we expose Agent Mail raw MCP JSON‑RPC, or keep a simplified REST wrapper?
- Should WS stream include raw pane output or structured deltas?
- Do we ship ACP/SDK mode in v1 or keep it behind a feature flag?
- Which hosting path do we standardize for WS (Fly.io vs local + tunnel)?

---

## 12) Immediate Next Work

1) Generate the **full CLI/TUI parity matrix** (command‑by‑command, robot flags included).
2) Produce a **detailed OpenAPI draft** for top 30 endpoints with real examples.
3) Extract the first service (`SessionService`) and wire CLI + API to it.
4) Add OpenAPI regeneration + validation to CI to prevent drift.


---

## Appendix A — CLI/TUI/Robot → REST Parity Matrix (Exact Commands + Files)

This matrix is derived directly from the CLI command definitions in `internal/cli/*.go` and robot flags in `internal/cli/root.go` (with handlers in `internal/robot/*` and `internal/pipeline/robot.go`).

### A.1 CLI & TUI Commands (Exact Use strings + files)

#### Session lifecycle & layout
- `ntm create <session-name>` — `internal/cli/create.go` — `POST /api/v1/sessions`
- `ntm spawn <session-name>` — `internal/cli/spawn.go` — `POST /api/v1/sessions/{id}/spawn`
- `ntm quick <project-name>` — `internal/cli/quick.go` — `POST /api/v1/projects/quick`
- `ntm add <session-name>` — `internal/cli/add.go` — `POST /api/v1/sessions/{id}/add`
- `ntm attach <session-name>` — `internal/cli/session.go` — `POST /api/v1/sessions/{id}/attach`
- `ntm list` — `internal/cli/session.go` — `GET /api/v1/sessions`
- `ntm status <session-name>` — `internal/cli/session.go` — `GET /api/v1/sessions/{id}/status`
- `ntm view [session-name]` — `internal/cli/view.go` — `POST /api/v1/sessions/{id}/view`
- `ntm zoom [session-name] [pane-index]` — `internal/cli/zoom.go` — `POST /api/v1/sessions/{id}/zoom`
- `ntm dashboard [session-name]` — `internal/cli/dashboard.go` — `GET /api/v1/dashboard/{id}`
- `ntm watch [session-name]` — `internal/cli/watch.go` — `GET /api/v1/sessions/{id}/watch` (WS preferred)
- `ntm kill <session>` — `internal/cli/send.go` — `DELETE /api/v1/sessions/{id}`
- `ntm internal-monitor <session>` — `internal/cli/monitor.go` — `GET /api/v1/internal/monitor/{id}` (admin/debug)

#### Agent actions & orchestration
- `ntm send <session> [prompt]` — `internal/cli/send.go` — `POST /api/v1/sessions/{id}/send`
- `ntm interrupt <session>` — `internal/cli/send.go` — `POST /api/v1/sessions/{id}/interrupt`
- `ntm wait [session]` — `internal/cli/wait.go` — `POST /api/v1/sessions/{id}/wait`
- `ntm replay [index|id]` — `internal/cli/replay.go` — `POST /api/v1/sessions/{id}/replay`
- `ntm activity [session]` — `internal/cli/activity.go` — `GET /api/v1/sessions/{id}/activity`
- `ntm summary [session]` — `internal/cli/summary.go` — `GET /api/v1/sessions/{id}/summary`
- `ntm health [session]` — `internal/cli/health.go` — `GET /api/v1/sessions/{id}/health`
- `ntm quota [session]` — `internal/cli/quota.go` — `GET /api/v1/sessions/{id}/quota`
- `ntm rotate [session]` — `internal/cli/rotate.go` — `POST /api/v1/sessions/{id}/rotate`
- `ntm rotate context history [session]` — `internal/cli/rotate_context.go` — `GET /api/v1/rotate/context/history`
- `ntm rotate context stats` — `internal/cli/rotate_context.go` — `GET /api/v1/rotate/context/stats`
- `ntm rotate context clear` — `internal/cli/rotate_context.go` — `DELETE /api/v1/rotate/context/history`

#### Output, capture, and inspection
- `ntm copy [session[:pane]]` — `internal/cli/copy.go` — `GET /api/v1/sessions/{id}/copy`
- `ntm save [session-name]` — `internal/cli/save.go` — `GET /api/v1/sessions/{id}/save`
- `ntm grep <pattern> [session-name]` — `internal/cli/grep.go` — `POST /api/v1/sessions/{id}/grep`
- `ntm extract <session> [pane]` — `internal/cli/extract.go` — `POST /api/v1/sessions/{id}/extract`
- `ntm diff <session> <pane1> <pane2>` — `internal/cli/diff.go` — `GET /api/v1/sessions/{id}/diff`
- `ntm changes [session]` — `internal/cli/changes.go` — `GET /api/v1/sessions/{id}/changes`
- `ntm conflicts [session]` — `internal/cli/changes.go` — `GET /api/v1/sessions/{id}/conflicts`

#### History, analytics, metrics, persistence
- `ntm history show <id-or-index>` — `internal/cli/history.go` — `GET /api/v1/history/{id}`
- `ntm history clear` — `internal/cli/history.go` — `POST /api/v1/history/clear`
- `ntm history stats` — `internal/cli/history.go` — `GET /api/v1/history/stats`
- `ntm history export <file>` — `internal/cli/history.go` — `POST /api/v1/history/export`
- `ntm history prune` — `internal/cli/history.go` — `POST /api/v1/history/prune`
- `ntm analytics` — `internal/cli/analytics.go` — `GET /api/v1/analytics`
- `ntm metrics show` — `internal/cli/metrics_cmd.go` — `GET /api/v1/metrics`
- `ntm metrics compare [snapshot-name]` — `internal/cli/metrics_cmd.go` — `GET /api/v1/metrics/compare`
- `ntm metrics export` — `internal/cli/metrics_cmd.go` — `GET /api/v1/metrics/export`
- `ntm metrics snapshot` — `internal/cli/metrics_cmd.go` — `POST /api/v1/metrics/snapshot`
- `ntm metrics save <name>` — `internal/cli/metrics_cmd.go` — `POST /api/v1/metrics/save`
- `ntm metrics list` — `internal/cli/metrics_cmd.go` — `GET /api/v1/metrics/list`
- `ntm checkpoint save <session>` — `internal/cli/checkpoint.go` — `POST /api/v1/checkpoints`
- `ntm checkpoint list [session]` — `internal/cli/checkpoint.go` — `GET /api/v1/checkpoints`
- `ntm checkpoint show <session> <id>` — `internal/cli/checkpoint.go` — `GET /api/v1/checkpoints/{id}`
- `ntm checkpoint delete <session> <id>` — `internal/cli/checkpoint.go` — `DELETE /api/v1/checkpoints/{id}`
- `ntm checkpoint verify <session> [id]` — `internal/cli/checkpoint.go` — `POST /api/v1/checkpoints/{id}/verify`
- `ntm checkpoint export <session> <id>` — `internal/cli/checkpoint.go` — `POST /api/v1/checkpoints/{id}/export`
- `ntm checkpoint import <archive>` — `internal/cli/checkpoint.go` — `POST /api/v1/checkpoints/import`
- `ntm rollback <session> [checkpoint-id]` — `internal/cli/rollback.go` — `POST /api/v1/sessions/{id}/rollback`
- `ntm sessions save [session-name]` — `internal/cli/session_persist.go` — `POST /api/v1/sessions/saved`
- `ntm sessions list` — `internal/cli/session_persist.go` — `GET /api/v1/sessions/saved`
- `ntm sessions show <name>` — `internal/cli/session_persist.go` — `GET /api/v1/sessions/saved/{name}`
- `ntm sessions delete <name>` — `internal/cli/session_persist.go` — `DELETE /api/v1/sessions/saved/{name}`
- `ntm sessions restore <saved-name>` — `internal/cli/session_persist.go` — `POST /api/v1/sessions/saved/{name}/restore`

#### Flywheel tooling (Beads/BV/CASS/CM/S2P/UBS)
- `ntm beads daemon start` — `internal/cli/beads.go` — `POST /api/v1/beads/daemon/start`
- `ntm beads daemon stop` — `internal/cli/beads.go` — `POST /api/v1/beads/daemon/stop`
- `ntm beads daemon status` — `internal/cli/beads.go` — `GET /api/v1/beads/daemon/status`
- `ntm beads daemon health` — `internal/cli/beads.go` — `GET /api/v1/beads/daemon/health`
- `ntm beads daemon metrics` — `internal/cli/beads.go` — `GET /api/v1/beads/daemon/metrics`
- `ntm work triage` — `internal/cli/work.go` — `GET /api/v1/work/triage`
- `ntm work alerts` — `internal/cli/work.go` — `GET /api/v1/work/alerts`
- `ntm work search <query>` — `internal/cli/work.go` — `GET /api/v1/work/search?q=...`
- `ntm work impact <paths...>` — `internal/cli/work.go` — `POST /api/v1/work/impact`
- `ntm work next` — `internal/cli/work.go` — `GET /api/v1/work/next`
- `ntm cass status` — `internal/cli/cass.go` — `GET /api/v1/cass/status`
- `ntm cass search <query>` — `internal/cli/cass.go` — `GET /api/v1/cass/search?q=...`
- `ntm cass insights` — `internal/cli/cass.go` — `GET /api/v1/cass/insights`
- `ntm cass timeline` — `internal/cli/cass.go` — `GET /api/v1/cass/timeline`
- `ntm cass preview <prompt>` — `internal/cli/cass.go` — `POST /api/v1/cass/preview`
- `ntm context build` — `internal/cli/context.go` — `POST /api/v1/context/build`
- `ntm context show <pack-id>` — `internal/cli/context.go` — `GET /api/v1/context/{id}`
- `ntm context stats` — `internal/cli/context.go` — `GET /api/v1/context/stats`
- `ntm context clear` — `internal/cli/context.go` — `DELETE /api/v1/context/cache`
- `ntm memory serve` — `internal/cli/memory.go` — `POST /api/v1/memory/daemon/start`
- `ntm memory context <task>` — `internal/cli/memory.go` — `POST /api/v1/memory/context`
- `ntm memory outcome <success|failure|partial>` — `internal/cli/memory.go` — `POST /api/v1/memory/outcome`
- `ntm memory privacy status|enable|disable|allow|deny` — `internal/cli/memory.go` — `GET/POST /api/v1/memory/privacy`
- `ntm scan [path]` — `internal/cli/scan.go` — `POST /api/v1/scan`
- `ntm bugs list [path]` — `internal/cli/bugs.go` — `GET /api/v1/bugs`
- `ntm bugs notify [path]` — `internal/cli/bugs.go` — `POST /api/v1/bugs/notify`
- `ntm bugs summary [path]` — `internal/cli/bugs.go` — `GET /api/v1/bugs/summary`

#### Agent Mail, reservations, approvals
- `ntm mail send <session> [message]` — `internal/cli/mail.go` — `POST /api/v1/mail/send`
- `ntm mail inbox [session]` — `internal/cli/mail.go` — `GET /api/v1/mail/inbox`
- `ntm message inbox` — `internal/cli/message.go` — `GET /api/v1/message/inbox`
- `ntm message send <to> <body>` — `internal/cli/message.go` — `POST /api/v1/message/send`
- `ntm message read <msg-id>` — `internal/cli/message.go` — `GET /api/v1/message/{id}`
- `ntm message ack <msg-id>` — `internal/cli/message.go` — `POST /api/v1/message/{id}/ack`
- `ntm lock <session> <patterns...>` — `internal/cli/lock.go` — `POST /api/v1/locks`
- `ntm unlock <session> [patterns...]` — `internal/cli/unlock.go` — `POST /api/v1/locks/release`
- `ntm locks list <session>` — `internal/cli/locks.go` — `GET /api/v1/locks`
- `ntm locks force-release <session> <reservation-id>` — `internal/cli/locks.go` — `POST /api/v1/locks/{id}/force-release`
- `ntm locks renew <session>` — `internal/cli/locks.go` — `POST /api/v1/locks/renew`
- `ntm approve list` — `internal/cli/approve.go` — `GET /api/v1/approvals`
- `ntm approve deny <token>` — `internal/cli/approve.go` — `POST /api/v1/approvals/{id}/deny`
- `ntm approve show <token>` — `internal/cli/approve.go` — `GET /api/v1/approvals/{id}`
- `ntm approve history` — `internal/cli/approve.go` — `GET /api/v1/approvals/history`

#### Profiles, personas, templates, recipes, plugins
- `ntm personas` — `internal/cli/personas.go` — `GET /api/v1/personas`
- `ntm personas list` — `internal/cli/personas.go` — `GET /api/v1/personas`
- `ntm personas show <name>` — `internal/cli/personas.go` — `GET /api/v1/personas/{name}`
- `ntm profiles` — `internal/cli/personas.go` — `GET /api/v1/profiles`
- `ntm profiles list` — `internal/cli/personas.go` — `GET /api/v1/profiles`
- `ntm profiles show <name>` — `internal/cli/personas.go` — `GET /api/v1/profiles/{name}`
- `ntm profiles switch <agent-id> <new-profile>` — `internal/cli/profile_switch.go` — `POST /api/v1/profiles/switch`
- `ntm recipes` — `internal/cli/recipes.go` — `GET /api/v1/recipes`
- `ntm recipes list` — `internal/cli/recipes.go` — `GET /api/v1/recipes`
- `ntm recipes show <recipe-name>` — `internal/cli/recipes.go` — `GET /api/v1/recipes/{name}`
- `ntm template` — `internal/cli/template.go` — `GET /api/v1/templates`
- `ntm template list` — `internal/cli/template.go` — `GET /api/v1/templates`
- `ntm template show <name>` — `internal/cli/template.go` — `GET /api/v1/templates/{name}`
- `ntm plugins` — `internal/cli/plugins.go` — `GET /api/v1/plugins`
- `ntm plugins list` — `internal/cli/plugins.go` — `GET /api/v1/plugins`

#### Pipelines
- `ntm pipeline run <workflow-file>` — `internal/cli/pipeline.go` — `POST /api/v1/pipelines/run`
- `ntm pipeline status [run-id]` — `internal/cli/pipeline.go` — `GET /api/v1/pipelines/{id}`
- `ntm pipeline list` — `internal/cli/pipeline.go` — `GET /api/v1/pipelines`
- `ntm pipeline cancel <run-id>` — `internal/cli/pipeline.go` — `POST /api/v1/pipelines/{id}/cancel`
- `ntm pipeline resume <run-id>` — `internal/cli/pipeline.go` — `POST /api/v1/pipelines/{id}/resume`
- `ntm pipeline cleanup` — `internal/cli/pipeline.go` — `POST /api/v1/pipelines/cleanup`
- `ntm pipeline exec <session>` — `internal/cli/pipeline.go` — `POST /api/v1/pipelines/exec`

#### Git coordination
- `ntm git sync [session]` — `internal/cli/git.go` — `POST /api/v1/git/sync`
- `ntm git status [session]` — `internal/cli/git.go` — `GET /api/v1/git/status`

#### Safety, policy, hooks, guards
- `ntm safety status` — `internal/cli/safety.go` — `GET /api/v1/safety/status`
- `ntm safety blocked` — `internal/cli/safety.go` — `GET /api/v1/safety/blocked`
- `ntm safety check <command>` — `internal/cli/safety.go` — `POST /api/v1/safety/check`
- `ntm safety install` — `internal/cli/safety.go` — `POST /api/v1/safety/install`
- `ntm safety uninstall` — `internal/cli/safety.go` — `POST /api/v1/safety/uninstall`
- `ntm policy show` — `internal/cli/policy_cmd.go` — `GET /api/v1/policy`
- `ntm policy validate [file]` — `internal/cli/policy_cmd.go` — `POST /api/v1/policy/validate`
- `ntm policy reset` — `internal/cli/policy_cmd.go` — `POST /api/v1/policy/reset`
- `ntm policy edit` — `internal/cli/policy_cmd.go` — `POST /api/v1/policy/edit`
- `ntm policy automation` — `internal/cli/policy_cmd.go` — `POST /api/v1/policy/automation`
- `ntm hooks install [hook-type]` — `internal/cli/hooks.go` — `POST /api/v1/hooks/install`
- `ntm hooks uninstall [hook-type]` — `internal/cli/hooks.go` — `POST /api/v1/hooks/uninstall`
- `ntm hooks status` — `internal/cli/hooks.go` — `GET /api/v1/hooks/status`
- `ntm hooks run <hook-type>` — `internal/cli/hooks.go` — `POST /api/v1/hooks/run`
- `ntm hooks guard install` — `internal/cli/hooks.go` — `POST /api/v1/hooks/guard/install`
- `ntm hooks guard uninstall` — `internal/cli/hooks.go` — `POST /api/v1/hooks/guard/uninstall`
- `ntm guards install` — `internal/cli/guards.go` — `POST /api/v1/guards/install`
- `ntm guards uninstall` — `internal/cli/guards.go` — `POST /api/v1/guards/uninstall`
- `ntm guards status` — `internal/cli/guards.go` — `GET /api/v1/guards/status`

#### Config & shell integration
- `ntm config init` — `internal/cli/root.go` — `POST /api/v1/config/init`
- `ntm config path` — `internal/cli/root.go` — `GET /api/v1/config/path`
- `ntm config set projects-base <path>` — `internal/cli/root.go` — `PUT /api/v1/config/projects-base`
- `ntm config show` — `internal/cli/root.go` — `GET /api/v1/config`
- `ntm config diff` — `internal/cli/root.go` — `GET /api/v1/config/diff`
- `ntm config validate` — `internal/cli/validate.go` — `POST /api/v1/config/validate`
- `ntm config get <key>` — `internal/cli/root.go` — `GET /api/v1/config/{key}`
- `ntm config edit` — `internal/cli/root.go` — `POST /api/v1/config/edit`
- `ntm config reset` — `internal/cli/root.go` — `POST /api/v1/config/reset`
- `ntm config project init` — `internal/cli/root.go` — `POST /api/v1/config/project/init`
- `ntm validate` — `internal/cli/validate.go` — `POST /api/v1/validate`
- `ntm setup` — `internal/cli/setup.go` — `POST /api/v1/setup`
- `ntm init <shell>` — `internal/cli/init.go` — `POST /api/v1/shell/init`
- `ntm completion <shell>` — `internal/cli/init.go` — `GET /api/v1/shell/completion`
- `ntm bind` — `internal/cli/bind.go` — `POST /api/v1/tmux/bind`
- `ntm deps` — `internal/cli/deps.go` — `GET /api/v1/deps`
- `ntm doctor` — `internal/cli/doctor.go` — `GET /api/v1/doctor`
- `ntm serve` — `internal/cli/serve.go` — server bootstrap (not proxied)
- `ntm upgrade` — `internal/cli/upgrade.go` — `POST /api/v1/upgrade`
- `ntm version` — `internal/cli/root.go` — `GET /api/v1/version`
- `ntm tutorial` — `internal/cli/tutorial.go` — `GET /api/v1/tutorial`

### A.2 Robot Flags (Exact flag names + files + REST)

These are the machine‑readable surfaces that must be mirrored exactly in REST. All flags are defined in `internal/cli/root.go` with handlers in `internal/robot/*` or `internal/pipeline/robot.go`.

#### Core state & snapshot
- `--robot-help` → `internal/robot/robot.go:PrintHelp` → `GET /api/v1/robot/help`
- `--robot-status` → `internal/robot/robot.go:PrintStatus` → `GET /api/v1/robot/status`
- `--robot-version` → `internal/robot/robot.go:PrintVersion` → `GET /api/v1/robot/version`
- `--robot-plan` → `internal/robot/robot.go:PrintPlan` → `GET /api/v1/robot/plan`
- `--robot-snapshot` → `internal/robot/robot.go:PrintSnapshot/PrintSnapshotDelta` → `GET /api/v1/robot/snapshot`
- `--robot-graph` → `internal/robot/robot.go:PrintGraph` → `GET /api/v1/robot/graph`
- `--robot-dashboard` → `internal/robot/robot_dashboard.go:PrintDashboard` → `GET /api/v1/robot/dashboard`
- `--robot-context` → `internal/robot/robot.go:PrintContext` → `GET /api/v1/robot/context`
- `--robot-terse` → `internal/robot/robot.go:PrintTerse` → `GET /api/v1/robot/terse`
- `--robot-markdown` → `internal/robot/markdown.go:PrintMarkdown` → `GET /api/v1/robot/markdown`

#### Output & messaging
- `--robot-tail` → `internal/robot/robot.go:PrintTail` → `GET /api/v1/robot/tail`
- `--robot-send` → `internal/robot/robot.go:PrintSend` → `POST /api/v1/robot/send`
- `--robot-ack` → `internal/robot/ack.go:PrintAck` → `POST /api/v1/robot/ack`
- `--robot-send --track` → `internal/robot/ack.go:PrintSendAndAck` → `POST /api/v1/robot/send-and-ack`

#### Session control
- `--robot-spawn` → `internal/robot/spawn.go:PrintSpawn` → `POST /api/v1/robot/spawn`
- `--robot-interrupt` → `internal/robot/interrupt.go:PrintInterrupt` → `POST /api/v1/robot/interrupt`
- `--robot-save` → `internal/robot/session.go:PrintSave` → `POST /api/v1/robot/save`
- `--robot-restore` → `internal/robot/session.go:PrintRestore` → `POST /api/v1/robot/restore`
- `--robot-wait` → `internal/robot/wait.go:PrintWait` → `POST /api/v1/robot/wait`
- `--robot-route` → `internal/robot/route.go:PrintRoute` → `GET /api/v1/robot/route`

#### Health, activity, history
- `--robot-health` → `internal/robot/health.go:PrintSessionHealth` → `GET /api/v1/robot/health`
- `--robot-activity` → `internal/robot/robot.go:PrintActivity` → `GET /api/v1/robot/activity`
- `--robot-history` → `internal/robot/history.go:PrintHistory` → `GET /api/v1/robot/history`
- `--robot-summary` → `internal/robot/synthesis.go:PrintSummary` → `GET /api/v1/robot/summary`
- `--robot-diff` → `internal/robot/robot.go:PrintDiff` → `GET /api/v1/robot/diff`

#### Recipes & schema
- `--robot-recipes` → `internal/robot/robot.go:PrintRecipes` → `GET /api/v1/robot/recipes`
- `--robot-schema` → `internal/robot/schema.go:PrintSchema` → `GET /api/v1/robot/schema/{type}`

#### CASS
- `--robot-cass-status` → `internal/robot/robot.go:PrintCASSStatus` → `GET /api/v1/robot/cass/status`
- `--robot-cass-search` → `internal/robot/robot.go:PrintCASSSearch` → `GET /api/v1/robot/cass/search`
- `--robot-cass-insights` → `internal/robot/robot.go:PrintCASSInsights` → `GET /api/v1/robot/cass/insights`
- `--robot-cass-context` → `internal/robot/robot.go:PrintCASSContext` → `GET /api/v1/robot/cass/context`

#### Tokens
- `--robot-tokens` → `internal/robot/tokens.go:PrintTokens` → `GET /api/v1/robot/tokens`

#### Assignments
- `--robot-assign` → `internal/robot/assign.go:PrintAssign` → `GET /api/v1/robot/assign`

#### Pipeline (robot mode)
- `--robot-pipeline-run` → `internal/pipeline/robot.go:PrintPipelineRun` → `POST /api/v1/robot/pipeline/run`
- `--robot-pipeline` → `internal/pipeline/robot.go:PrintPipelineStatus` → `GET /api/v1/robot/pipeline/{id}`
- `--robot-pipeline-list` → `internal/pipeline/robot.go:PrintPipelineList` → `GET /api/v1/robot/pipeline`
- `--robot-pipeline-cancel` → `internal/pipeline/robot.go:PrintPipelineCancel` → `POST /api/v1/robot/pipeline/{id}/cancel`

#### TUI parity robot flags (dashboard components)
- `--robot-files` → `internal/robot/tui_parity.go:PrintFiles` → `GET /api/v1/robot/files`
- `--robot-inspect-pane` → `internal/robot/tui_parity.go:PrintInspectPane` → `GET /api/v1/robot/inspect-pane`
- `--robot-metrics` → `internal/robot/tui_parity.go:PrintMetrics` → `GET /api/v1/robot/metrics`
- `--robot-replay` → `internal/robot/tui_parity.go:PrintReplay` → `POST /api/v1/robot/replay`
- `--robot-palette` → `internal/robot/tui_parity.go:PrintPalette` → `GET /api/v1/robot/palette`
- `--robot-dismiss-alert` → `internal/robot/tui_parity.go:PrintDismissAlert` → `POST /api/v1/robot/alerts/dismiss`
- `--robot-alerts` → `internal/robot/tui_parity.go:PrintAlertsTUI` → `GET /api/v1/robot/alerts`
- `--robot-beads-list` → `internal/robot/tui_parity.go:PrintBeadsList` → `GET /api/v1/robot/beads`
- `--robot-bead-claim` → `internal/robot/tui_parity.go:PrintBeadClaim` → `POST /api/v1/robot/beads/{id}/claim`
- `--robot-bead-create` → `internal/robot/tui_parity.go:PrintBeadCreate` → `POST /api/v1/robot/beads`
- `--robot-bead-show` → `internal/robot/tui_parity.go:PrintBeadShow` → `GET /api/v1/robot/beads/{id}`
- `--robot-bead-close` → `internal/robot/tui_parity.go:PrintBeadClose` → `POST /api/v1/robot/beads/{id}/close`


### A.1a Base Commands (Top‑Level command nodes)

These are the top‑level command nodes that primarily provide help/entrypoints but still need REST parity for discoverability:
- `ntm work` — `internal/cli/work.go` — `GET /api/v1/work` (summary/help)
- `ntm beads` — `internal/cli/beads.go` — `GET /api/v1/beads` (summary/help)
- `ntm cass` — `internal/cli/cass.go` — `GET /api/v1/cass` (summary/help)
- `ntm context` — `internal/cli/context.go` — `GET /api/v1/context` (summary/help)
- `ntm memory` — `internal/cli/memory.go` — `GET /api/v1/memory` (summary/help)
- `ntm mail` — `internal/cli/mail.go` — `GET /api/v1/mail` (summary/help)
- `ntm message` — `internal/cli/message.go` — `GET /api/v1/message` (summary/help)
- `ntm locks` — `internal/cli/locks.go` — `GET /api/v1/locks` (summary/help)
- `ntm pipeline` — `internal/cli/pipeline.go` — `GET /api/v1/pipelines` (summary/help)
- `ntm git` — `internal/cli/git.go` — `GET /api/v1/git` (summary/help)
- `ntm safety` — `internal/cli/safety.go` — `GET /api/v1/safety` (summary/help)
- `ntm policy` — `internal/cli/policy_cmd.go` — `GET /api/v1/policy` (summary/help)
- `ntm hooks` — `internal/cli/hooks.go` — `GET /api/v1/hooks` (summary/help)
- `ntm guards` — `internal/cli/guards.go` — `GET /api/v1/guards` (summary/help)
- `ntm config` — `internal/cli/root.go` — `GET /api/v1/config` (summary/help)
- `ntm personas` — `internal/cli/personas.go` — `GET /api/v1/personas` (summary/help)
- `ntm profiles` — `internal/cli/personas.go` — `GET /api/v1/profiles` (summary/help)
- `ntm recipes` — `internal/cli/recipes.go` — `GET /api/v1/recipes` (summary/help)
- `ntm template` — `internal/cli/template.go` — `GET /api/v1/templates` (summary/help)
- `ntm plugins` — `internal/cli/plugins.go` — `GET /api/v1/plugins` (summary/help)
- `ntm metrics` — `internal/cli/metrics_cmd.go` — `GET /api/v1/metrics` (summary/help)
- `ntm history` — `internal/cli/history.go` — `GET /api/v1/history` (summary/help)
- `ntm checkpoint` — `internal/cli/checkpoint.go` — `GET /api/v1/checkpoints` (summary/help)
- `ntm approve` — `internal/cli/approve.go` — `GET /api/v1/approvals` (summary/help)

### A.2a Robot Flag Coverage Addendum
- `--robot-mail` → `internal/robot/robot.go:PrintMail` → `GET /api/v1/robot/mail`
