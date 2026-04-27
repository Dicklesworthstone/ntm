# PLAN_TO_ADD_WEB_UI_AND_REST_AND_WEBSOCKET_API_LAYERS_TO_NTM__GPT_PRO.md

**Document type:** Product + Architecture Proposal  
**Project:** NTM (Named Tmux Manager)  
**Author:** GPT-5.2 Pro  
**Date:** 2026-01-07  
**Status:** Proposal (implementation-ready plan; intentionally ultra-granular)

---

## 0. North-star vision

NTM becomes a **multi-agent command center that lives everywhere**:

- **Terminal-first** (CLI + TUI) for power users and SSH.
- **Web-first** (desktop + mobile) for visibility, orchestration, and “at-a-glance” control.
- **API-first** (REST + WebSocket) so humans *and agents* can automate anything.

**Non-negotiable requirement:**  
> **Anything you can do today in NTM (CLI + TUI) must be possible via REST.**  
No “hidden features” in the terminal. No drift. No “you can only do that in the TUI.”

---

## 1. Product outcomes

### 1.1 Outcomes for humans

1. **One-page clarity:**  
   In < 10 seconds, you can answer:
   - Which sessions exist and where?
   - Which agents are active / stalled / erroring?
   - Which panes are producing output now?
   - Where are conflicts forming?
   - Which commands were recently run?

2. **“Stripe-level” UI polish and confidence:**  
   The UI should feel inevitable, crisp, and *calm*—even while coordinating chaos.

3. **Mobile becomes genuinely useful (not “just a viewer”):**
   - Triage alerts, restart agents, broadcast prompts, view recent output, resolve conflicts.
   - Do all that safely (RBAC + approvals).

### 1.2 Outcomes for agents / automation

1. **OpenAPI that teaches itself**
   - Every endpoint has:
     - Clear description
     - Realistic examples
     - Error cases
     - When/why to use it
   - Agents should be able to “just read the spec” and act correctly.

2. **WebSocket stream as a universal feed**
   - Pane output
   - Agent activity states
   - Tool calls/results (best-effort now; structured later)
   - Notifications
   - File changes + conflicts
   - Checkpoints + history
   - All in a consistent event envelope with replay/resume.

---

## 2. Hard constraints & decisions

### 2.1 JS/TS toolchain constraints (as requested)

- **bun** for install/build/test/lint/dev.  
  ❌ Never use npm/yarn/pnpm.  
  ✅ Lockfiles: **only `bun.lock`**.

- Target **latest Node.js** locally + on Vercel.

- UI stack:
  - Next.js 16 (App Router)
  - React 19.x
  - TypeScript strict
  - Tailwind CSS
  - framer-motion
  - lucide-react
  - TanStack Query (+ other TanStack libs as needed)

> Note: Next.js 16 exists and is “now available” as of the Next.js team’s October 2025 post, and it explicitly calls out React 19.2 usage in the App Router. citeturn23view0

### 2.2 Deployment reality check (important)

If you want **a real WebSocket server**, you need a platform that supports **long-lived connections**.

- Vercel has historically not supported native WebSockets on serverless functions; a Vercel community thread (Nov 2025) still indicates WebSockets are not supported (even with Fluid Compute). citeturn14view0  
- Vercel’s own guidance for “Do Vercel Serverless Functions support WebSocket connections?” points users toward third-party realtime providers instead of native WS. citeturn15view0

**Implication:**  
- Deploy the **web UI** on Vercel (great).  
- Deploy the **NTM API/WebSocket daemon** on a long-lived server platform (Fly.io, Render, bare metal, etc.), or run it yourself (SSH-forward, Tailscale, Cloudflare Tunnel).

### 2.3 “Zed abstraction” / interoperability research (ACP)

The “Zed team abstraction” you referenced maps to the **Agent Client Protocol (ACP)** ecosystem.

Key facts (because it changes the long-term architecture choices):

- ACP standardizes communication between clients/editors and coding agents; it supports local agents via **JSON-RPC over stdio**, and mentions remote scenarios via **HTTP or WebSocket** (remote support is explicitly “work in progress”). citeturn22view0  
- The ACP spec defines transports (stdio + draft streamable HTTP) and allows custom transports. citeturn22view1  
- ACP is deliberately **MCP-friendly** (re-uses MCP types) and includes UX-oriented types like **diff rendering**. citeturn22view0turn22view2  
- There is an **AI SDK ACP provider** that bridges ACP agents (Claude Code, Gemini CLI, Codex CLI, etc.) into a unified “LanguageModel” interface; it shows running Gemini CLI with `--experimental-acp`. citeturn24view0

**Why this matters for NTM:**  
You can start with tmux/text capture (fast path), but a “structured events” future (tool calls/results, diffs, file ops) is best served by ACP/SDK integration as a *second* agent backend.

---

## 3. Architecture strategy: “One Core, Many Interfaces”

### 3.1 Guiding principle: single source of truth

To guarantee **CLI/TUI/API parity**, we must eliminate duplicate implementations.

**Proposal:**  
Create a **Command Kernel** (a registry of commands and schemas) that drives:

- CLI (Cobra commands become thin wrappers)
- TUI actions (palette/dashboard call kernel)
- REST endpoints (generated from same registry)
- OpenAPI docs (generated from same registry)
- Web UI “command palette” (pull metadata from API)

This is the most direct way to make it *impossible* for API drift to happen.

### 3.2 New runtime modes

Add two top-level commands:

1. `ntm serve`  
   Starts the HTTP server:
   - REST API
   - WebSocket stream
   - (Optionally) static web assets

2. `ntm web`  
   Convenience launcher:
   - Starts `ntm serve`
   - Starts Next dev server (in dev) or serves built UI
   - Opens browser
   - Prints safe tunnel hints for remote use

### 3.3 Proposed logical layers

```
┌──────────────────────────────┐
│          Web UI (Next)        │
│  (TanStack Query + WS client) │
└───────────────┬──────────────┘
                │ REST + WS
┌───────────────▼──────────────┐
│        NTM API Server         │
│  - Auth/RBAC                  │
│  - REST controllers           │
│  - WS hub + event fanout      │
│  - OpenAPI + Swagger UI       │
└───────────────┬──────────────┘
                │ calls
┌───────────────▼──────────────┐
│      Command Kernel / Core    │
│  - Command registry           │
│  - Validation + safety        │
│  - Hooks + events             │
│  - Audit + idempotency        │
└───────────────┬──────────────┘
                │ uses
┌───────────────▼──────────────┐
│     Adapters / Integrations   │
│  - tmux adapter               │
│  - filesystem/watcher         │
│  - agent drivers (tmux)       │
│  - (future) ACP/SDK drivers   │
│  - bv/cass/agentmail/etc      │
└──────────────────────────────┘
```

### 3.4 Key architectural invariants

- **No silent data loss** stays true (API must enforce same safety rules).
- **Idempotency** for automation: repeated calls shouldn’t create duplicate sessions or spam agents.
- **All operations are auditable**: every API mutation creates a history entry and emits an event.
- **Everything is streamable**: if it matters, it emits events and/or is queryable.

---

## 4. REST API: design that scales and stays understandable

### 4.1 API conventions

**Base URL:** `/api/v1`

**Content types:**
- Requests: `application/json`
- Responses: `application/json`
- WebSocket: `application/json` messages (optionally `application/msgpack` later)

**Common features:**
- Pagination for list endpoints: `?limit=50&cursor=...`
- Filtering: `?session=myproject&agent_type=claude`
- Sorting: `?sort=-updated_at`

**Idempotency:**
- For any POST that mutates state, accept:
  - `Idempotency-Key: <uuid>` header
  - Server stores key + result for a TTL window

**Error model (consistent across REST + WS):**

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

**Success model (avoid nested “success” booleans):**
- Prefer HTTP status codes + typed body.
- Include `request_id` for traceability.

**Versioning:**
- `GET /api/v1/version` returns:
  - ntm version, commit, build date
  - API version
  - feature flags/capabilities

### 4.2 Resources: the “surface area map”

To achieve full parity, think in **capabilities**, not just “commands”.

#### 4.2.1 System

- `GET /api/v1/health`
- `GET /api/v1/version`
- `GET /api/v1/deps` (equivalent to `ntm deps`)
- `GET /api/v1/config` (effective config)
- `PATCH /api/v1/config` (update config entries safely)
- `GET /api/v1/capabilities`
  - which optional tools are available (tmux/bv/cass/agentmail/ubs)
  - which stream topics exist
  - what auth methods are enabled

#### 4.2.2 Sessions

- `GET /api/v1/sessions`
- `POST /api/v1/sessions`
  - create empty session
- `POST /api/v1/sessions/{session}/spawn`
  - spawn agents, create if missing
- `POST /api/v1/sessions/{session}/quick`
  - project scaffolding
- `GET /api/v1/sessions/{session}`
  - status summary
- `DELETE /api/v1/sessions/{session}`
  - kill (supports `force=true`)
- `POST /api/v1/sessions/{session}/attach`
  - returns best attach method (CLI command, web terminal URL, etc.)
- `POST /api/v1/sessions/{session}/view`
  - retile + attach semantics (mostly a tmux operation)
- `POST /api/v1/sessions/{session}/zoom`
  - zoom to pane index or pane id

#### 4.2.3 Panes

- `GET /api/v1/sessions/{session}/panes`
- `GET /api/v1/sessions/{session}/panes/{pane}`
  - metadata (title, agent type, active state)
- `POST /api/v1/sessions/{session}/panes/{pane}/input`
  - send keys/text
- `POST /api/v1/sessions/{session}/panes/{pane}/interrupt`
  - ctrl-c
- `GET /api/v1/sessions/{session}/panes/{pane}/output?tail=200`
  - tail output
- `GET /api/v1/sessions/{session}/panes/{pane}/capture?...`
  - capture-pane with params
- `POST /api/v1/sessions/{session}/panes/{pane}/title`
  - rename pane title
- `POST /api/v1/sessions/{session}/panes/{pane}/pipe`
  - enable/disable streaming capture backend (implementation detail; exposed for debugging)

#### 4.2.4 Agents (semantic layer)

Not every pane is an agent, but every agent maps to a pane (in tmux backend).

- `GET /api/v1/sessions/{session}/agents`
- `POST /api/v1/sessions/{session}/agents/add`
- `POST /api/v1/sessions/{session}/agents/send`
  - send prompt to cc/cod/gmi/all
- `POST /api/v1/sessions/{session}/agents/interrupt`
- `GET /api/v1/sessions/{session}/agents/activity`
- `GET /api/v1/sessions/{session}/agents/health`
- `GET /api/v1/sessions/{session}/agents/context`
- `POST /api/v1/sessions/{session}/agents/rotate`
  - rotation/compaction workflows

#### 4.2.5 Output tooling (copy/save/extract/diff/grep/watch)

- `POST /api/v1/sessions/{session}/output/copy`
- `POST /api/v1/sessions/{session}/output/save`
- `POST /api/v1/sessions/{session}/output/extract`
- `POST /api/v1/sessions/{session}/output/diff`
- `POST /api/v1/sessions/{session}/output/grep`
- `POST /api/v1/sessions/{session}/output/watch` (creates a watch job)
- `GET /api/v1/jobs/{job_id}`
- `DELETE /api/v1/jobs/{job_id}`

#### 4.2.6 Palette + History

- `GET /api/v1/sessions/{session}/palette`
- `POST /api/v1/sessions/{session}/palette/run`
- `POST /api/v1/sessions/{session}/palette/pin`
- `POST /api/v1/sessions/{session}/palette/favorite`
- `GET /api/v1/history?session=...`
- `POST /api/v1/history/replay`

#### 4.2.7 Checkpoints + persistence

- `POST /api/v1/sessions/{session}/checkpoints`
- `GET /api/v1/sessions/{session}/checkpoints`
- `GET /api/v1/sessions/{session}/checkpoints/{id}`
- `DELETE /api/v1/sessions/{session}/checkpoints/{id}`

#### 4.2.8 Safety + policy

- `GET /api/v1/safety/status`
- `POST /api/v1/safety/check`
- `GET /api/v1/safety/blocked`
- `POST /api/v1/safety/install`
- `POST /api/v1/safety/uninstall`
- `GET /api/v1/policy`
- `PUT /api/v1/policy` (validate + apply)
- `POST /api/v1/policy/validate`

#### 4.2.9 Notifications, hooks, scanner, conflicts, analytics

- `GET /api/v1/notifications`
- `POST /api/v1/notifications/test`
- `GET /api/v1/hooks`
- `PUT /api/v1/hooks`
- `GET /api/v1/scanner/status`
- `POST /api/v1/scanner/run`
- `GET /api/v1/conflicts?session=...`
- `GET /api/v1/analytics?...`

#### 4.2.10 “Robot mode” compatibility

Since NTM already has robot JSON outputs, preserve them:

- `GET /api/v1/robot/status`
- `GET /api/v1/robot/context?session=...`
- etc.

But: **Internally, robot handlers call the same Command Kernel**.

### 4.3 Endpoint parity enforcement: the “Parity Gate”

Add CI tests that assert:
- Every command in the registry has:
  - CLI binding metadata
  - REST binding metadata (method/path)
  - OpenAPI examples
- Every CLI command is registered in the kernel (no “ad hoc cobra”).
- The OpenAPI spec is generated in CI and compared to checked-in `openapi.json` (or regenerated during release).

This makes parity a *mechanical property*.

---

## 5. OpenAPI: documentation that teaches agents

### 5.1 Requirements for the spec

- OpenAPI 3.1
- Every endpoint includes:
  - Summary, description, tags
  - Request schema
  - Response schema(s)
  - Error schema(s)
  - **Concrete examples** (copy/paste ready)
  - Notes on idempotency, safety, and side effects
  - “Equivalent CLI” call (as vendor extension)

Example vendor extensions:

```yaml
x-ntm:
  cli: "ntm spawn myproject --cc=2 --cod=1"
  safety:
    destructive: false
  emits_events:
    - "session.created"
    - "agent.spawned"
```

### 5.2 Swagger UI / docs routes

- `GET /openapi.json`
- `GET /docs` (Swagger UI)
- `GET /docs/redoc` (optional; ReDoc is excellent for large APIs)

### 5.3 Auto-generated typed clients

- Generate TypeScript types with `openapi-typescript`
- Generate a TanStack Query client wrapper automatically:
  - `useSessions()`
  - `useSession(session)`
  - `useSendPrompt(...)`
  - etc.

This removes human-written drift in the UI.

---

## 6. WebSocket layer: real-time, resumable, and composable

### 6.1 Why WebSocket (and what it must do)

The WebSocket layer is not “nice to have”; it’s the backbone of the web UX:
- Live pane outputs
- Agent state/health/activity
- Notifications + scanner results
- Conflict detection alerts
- Checkpoint creation progress
- Prompt send acknowledgements

### 6.2 One socket, many topics (multiplexing)

**Endpoint:** `GET /api/v1/ws`

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

Server responds with:

```json
{
  "op": "subscribed",
  "topics": ["events", "sessions:myproject", "panes:myproject:1", "notifications"],
  "server_time": "2026-01-07T00:00:00Z"
}
```

### 6.3 Event envelope (consistent across all streams)

```json
{
  "type": "pane.output.append",
  "ts": "2026-01-07T00:00:00Z",
  "seq": 184224,
  "topic": "panes:myproject:1",
  "data": {
    "session": "myproject",
    "pane": 1,
    "agent_type": "claude",
    "chunk": "…raw text…",
    "lines": ["..."], 
    "encoding": "utf-8",
    "truncated": false
  }
}
```

**Design notes:**
- `seq` is monotonically increasing per server (or per topic).  
  It enables resume from last seen `seq`.
- `topic` is explicit to simplify client routing.
- `data` is typed by `type`.

### 6.4 Backpressure & performance

For many panes, output can exceed what a browser can render.

Rules:
- The server keeps a per-pane ring buffer (configurable).
- The client can request:
  - `mode: "lines"` (line-delimited, safe)
  - `mode: "raw"` (fast, but less structured)
- The server can emit:
  - `pane.output.dropped` events when the client can’t keep up.
- Clients can throttle rendering using virtualization (see UI plan).

### 6.5 Replay / resume model

- Each event stream stores a short retention ring (e.g., last 60 seconds or last N events).
- The client sends `since`:
  - a cursor string
  - or `{seq: 123}`

If the cursor is too old:
- server emits `stream.reset` and sends a snapshot (e.g., last 200 lines per pane).

### 6.6 Topic taxonomy

Minimum viable topics:

- `events` (everything important; filtered by RBAC)
- `sessions` (session list changes)
- `sessions:{name}`
- `panes:{session}:{paneIndex}`
- `panes:{session}:cc|cod|gmi|user`
- `alerts`
- `notifications`
- `scanner`
- `conflicts`
- `history`
- `metrics`

---

## 7. Capturing tmux output without killing performance

### 7.1 The fundamental issue

`tmux capture-pane` is okay for occasional snapshots, but it’s too expensive to call frequently for “live streaming”.

### 7.2 Proposed capture architecture (high performance)

**Default approach (recommended): `tmux pipe-pane` capture**

- When a pane is created/spawned, NTM enables a pipe:
  - `tmux pipe-pane -o -t <pane> "<ntm_internal_streamer --pane-id ...>"`
- The streamer writes:
  - to the event bus (WebSocket)
  - to disk log (for tail/replay)
  - optionally to analytics counters

**Key properties:**
- Near real-time
- Minimal polling
- Stable even with many panes

**Fallback approach: polling capture (safe mode)**
- If pipe-pane fails (permissions, tmux version mismatch):
  - Poll `capture-pane` at a conservative rate (e.g., 1s)
  - Only for panes with active subscribers

### 7.3 Snapshot-on-connect

When a web client opens a pane:
- `GET /output?tail=200` returns snapshot
- WebSocket stream appends new output immediately after snapshot

This produces “no blank pane” UX.

### 7.4 Parsing for structured events (short-term vs long-term)

Short-term (fast path):
- Parse text output heuristically for:
  - known compaction phrases
  - known tool-call markers
  - error/rate-limit patterns
- This is similar to your existing pattern-based detection system.

Long-term (structured path):
- Support optional **ACP/SDK agent drivers** that emit:
  - tool call begin/end
  - diff previews
  - file ops
  - reasoning/plan tokens (if available)
- ACP is explicitly designed to stream UI-friendly notifications and reuse MCP types. citeturn22view2turn22view3

---

## 8. Agent SDK / ACP integration (forward-looking, optional, but powerful)

You asked whether to leverage libraries like “Claude Code TypeScript SDK” and similar for Codex/Gemini. Based on current ecosystem signals, there are two promising routes:

### 8.1 Route A: Direct SDK integration (per agent provider)

**Claude Code:**  
Anthropic publishes a “Claude Agent SDK” with a TypeScript API where `query()` returns an async generator of messages, and supports `interrupt()` for stopping a run. citeturn3view4

**Codex:**  
OpenAI publishes a `@openai/codex` SDK (TypeScript) with a session-based API. citeturn3view3

**Gemini:**  
Google has a Gemini CLI + SDK story; the important piece for structured integration here is the ACP ecosystem (next section).

Pros:
- Rich, structured events
- Accurate tool call/result capture
- Better metrics + token tracking

Cons:
- Each provider is different (more code)
- You may end up re-implementing “agent process management” you already get via tmux

### 8.2 Route B: ACP as the “unifying layer”

ACP is explicitly trying to become “LSP for agents”. citeturn22view0

Evidence of practicality:
- ACP’s docs describe stdio JSON-RPC transport and note structured UI notifications. citeturn22view1turn22view2  
- The AI SDK ACP provider claims it can bridge ACP agents (Claude Code, Gemini CLI, Codex CLI) and shows a concrete example running Gemini CLI with `--experimental-acp`. citeturn24view0

Pros:
- One abstraction for multiple agent CLIs
- Structured events designed for UI
- Future-proof if ACP adoption grows

Cons:
- ACP remote support is “work in progress” and streamable HTTP is still draft. citeturn22view0turn22view1
- May require running agent processes in “ACP mode” (not necessarily human-friendly in tmux panes)

### 8.3 Recommended posture for NTM

**Phase 1 (ship value fast):**  
- Keep tmux-based agents as primary backend.
- Stream pane output as text (pipe-pane).
- Provide best-effort “tool calls/results” by parsing patterns.

**Phase 2 (structured upgrade path):**  
- Introduce an **Agent Driver Interface** with two implementations:
  1. `tmuxDriver` (existing)
  2. `acpDriver` (optional, experimental)
- Let the user decide per session (or per agent type) which driver to use.
- The web UI can show a badge: **“Structured”** vs **“Text”** mode.

This keeps the core NTM identity while enabling a “modern agent UX” evolution.

---

## 9. Web UI: information architecture + “Stripe-level” UX plan

### 9.1 UX thesis

NTM’s web UI should not be “tmux in a browser”.

It should be:
- **A cockpit** (overview → drilldown)
- **A lens** (see what matters now)
- **A coordinator** (send actions safely and confidently)
- **A recorder** (history, analytics, replay, audit)

### 9.2 Design principles (visual + interaction)

1. **Clarity over cleverness**
   - Gradients and motion are used to create hierarchy, not noise.

2. **Focus with progressive disclosure**
   - Show what matters at a glance.
   - Deep details are one click away, not always visible.

3. **Latency is a design feature**
   - 0-jank streaming
   - Optimistic UI where safe
   - Skeleton states that feel intentional

4. **Keyboard-first on desktop**
   - Global command palette
   - Pane switching by number
   - Search everywhere
   - “?” help overlay everywhere

5. **Thumb-first on mobile**
   - Bottom navigation
   - Large touch targets
   - Swipe to switch panes
   - Quick action sheets

### 9.3 Web app IA (routes)

- `/connect`  
  Connect to an NTM server (local, SSH-forwarded, or remote).
- `/sessions`  
  Sessions overview (cards + filters).
- `/sessions/[name]`  
  Session dashboard:
  - agent grid
  - activity timeline
  - alerts + conflicts
- `/sessions/[name]/panes/[pane]`  
  Pane detail:
  - live output
  - prompt input
  - tools: copy/extract/diff
- `/commands`  
  Command palette “library” (browse, pin, favorites).
- `/history`  
  Prompt history + replay + audit.
- `/checkpoints`  
  Save/restore points.
- `/analytics`  
  Token velocity, prompts, errors, agent usage.
- `/settings`  
  Server config, auth, notifications, safety policy.

### 9.4 Desktop UX (hyper-optimized)

**Layout pattern:**
- Left rail: sessions + search
- Main: session content
- Right inspector: selected pane/agent details
- Global palette overlay: `⌘K`

**Signature “Stripe-level” components:**

1. **Session Cards (overview)**
   - gradient header
   - live badges (C/X/G counts)
   - health indicator
   - last activity timestamp
   - “Resume” primary CTA

2. **Agent Grid (session detail)**
   - responsive grid
   - color-coded by agent type
   - “live” pulse when output is streaming
   - small sparklines for output velocity (optional)

3. **Pane Stream Viewer**
   - virtualized log list (huge performance win)
   - sticky “current status” header
   - inline markers for:
     - prompt sent
     - tool call (if detected)
     - error / rate limit
     - compaction / rotation

4. **Command Palette (web)**
   - fuzzy search
   - pinned + recent
   - preview panel
   - target selector (All / Claude / Codex / Gemini)
   - safe confirmations for risky commands
   - full keyboard navigation

5. **Conflict Heatmap**
   - files on y-axis, agents on x-axis
   - highlights collisions by severity
   - click file to see timeline and suggested resolution workflow

### 9.5 Mobile UX (hyper-optimized)

Mobile is not “shrunken desktop.” It’s a different instrument.

**Bottom nav:**
- Sessions
- Dashboard
- Alerts
- Palette
- Settings

**Session view:**
- Stacked agent cards
- Tap agent → live output view
- Swipe left/right to jump between agents
- Floating action button: “Send / Interrupt / Save / Checkpoint”

**Alerts view:**
- designed like an incident feed:
  - context warning
  - crash/restart
  - conflicts
  - scan findings

**One-handed prompt send:**
- preset quick prompts
- voice input (optional later)
- target selector as segmented control

### 9.6 Front-end data architecture

- REST via TanStack Query (caching, retries, stale times)
- WebSocket:
  - single connection per server
  - topic subscriptions driven by route
  - store events in an in-memory event store
  - persist minimal “last seen cursor” to localStorage

### 9.7 UI performance budgets

- Session list: < 100ms interaction latency
- Pane stream: maintain 60fps scroll (virtualized)
- WebSocket processing: offload to Web Worker if needed
- Avoid re-render storms:
  - batched state updates
  - memoized row renderers
  - throttle “velocity” updates

---

## 10. Security model (must be taken extremely seriously)

NTM can execute commands that have real consequences (especially with “dangerous flags”). A web UI increases blast radius.

### 10.1 Default safety posture

- By default, `ntm serve` binds to **127.0.0.1 only**.
- To bind externally, require explicit flags:
  - `--listen 0.0.0.0`
  - `--auth required`
  - and ideally `--tls`

### 10.2 Auth options

- **Local-only mode:** no auth needed (localhost).
- **API key mode:** simple and effective.
- **OIDC mode (optional later):** for multi-user org setups.
- **mTLS (advanced):** for high-trust environments.

### 10.3 RBAC / permissions

At minimum:
- `viewer`: can read status/output
- `operator`: can send prompts/interrupt
- `admin`: can kill sessions, change config, install safety hooks, etc.

### 10.4 Safety system integration

Everything the CLI blocks must be blocked through the API too.

- Safety checks run inside the Command Kernel.
- API never bypasses the policy engine.
- For “approval-required” actions:
  - return `409 APPROVAL_REQUIRED`
  - include an approval token flow:
    - `POST /approvals` → “approve this action with token”
    - ensures explicit second step (and optionally “two-person rule” later)

### 10.5 Audit & observability

- Every mutating API call records:
  - who (token/user)
  - what (command + params)
  - when (time)
  - result
  - correlation id
- Events flow into:
  - JSONL (existing)
  - WebSocket stream (live)
  - optional OpenTelemetry spans (future)

---

## 11. Deployment plan (practical and flexible)

### 11.1 Local development

- `ntm serve --dev`
  - runs API at `http://localhost:7337`
  - enables permissive CORS for `http://localhost:3000`
  - prints docs URLs
- `bun dev` in `web/` runs Next dev server

### 11.2 “Single binary” mode (optional but delightful)

Goal: `ntm web` “just works” without requiring Node installed.

Two approaches:

1. **Serve static export**
   - Build Next.js as static (`output: "export"`)
   - Go server serves `/` assets
   - UI uses REST + WS
   - (No Next SSR features, but UI still can be incredible)

2. **Dual-process launch**
   - `ntm web` starts:
     - Go API server
     - Next server (bun)
   - More complex to ship, but supports SSR

### 11.3 Vercel deployment for UI

Vercel supports Bun runtime configuration (public beta) and documents bun usage patterns, including setting `bunVersion` and building Next apps with Bun. citeturn16view1turn16view2

**Recommendation:**
- Deploy UI on Vercel.
- Point it at your NTM server via:
  - HTTPS URL
  - Tailscale URL
  - Cloudflare Tunnel
  - SSH port forward for personal use

### 11.4 Hosting the API + WebSocket daemon

Pick a platform that supports:
- long-lived processes
- WebSockets
- persistent disk (for logs/checkpoints)

Options:
- Fly.io / Render / Railway / bare metal
- “run it where tmux runs” (often best): the same SSH box that hosts sessions

---

## 12. Implementation roadmap (phased, shippable, testable)

### Phase 0 — Kernel refactor (parity foundation)
**Goal:** Create the Command Kernel so everything can be shared.

Deliverables:
- `internal/kernel` package:
  - command registry
  - input/output schemas
  - safety hooks
  - event emitter
- CLI commands rewritten as thin wrappers
- TUI triggers routed through kernel
- Parity Gate CI tests (registry completeness)

Acceptance criteria:
- Every CLI command is represented in kernel.
- Robot outputs are generated from kernel results.

---

### Phase 1 — REST API skeleton + OpenAPI MVP
Deliverables:
- `ntm serve`
- `GET /health`, `/version`, `/openapi.json`, `/docs`
- Sessions read endpoints:
  - list sessions
  - get session status
  - list panes
- OpenAPI includes examples and error model

Acceptance criteria:
- A client can build a “session list” UI from API alone.

---

### Phase 2 — Full command parity via REST
Deliverables:
- All mutating operations:
  - spawn/create/quick/add/send/interrupt/kill
  - copy/save/extract/diff/grep
  - checkpoints
  - config/policy/safety
  - agentmail (if installed)
- Idempotency keys
- Operation jobs for long-running tasks

Acceptance criteria:
- Every CLI/TUI action has a REST equivalent.
- OpenAPI references CLI equivalents and includes copy/paste examples.

---

### Phase 3 — WebSocket streaming MVP
Deliverables:
- `/api/v1/ws`
- Topics: sessions, panes output, notifications, alerts
- Snapshot-on-connect for pane output
- Backpressure strategy (drop + notify)
- Resume cursors

Acceptance criteria:
- Web UI can show live pane output and live status without polling.

---

### Phase 4 — Web UI MVP (already “nice”, not “prototype”)
Deliverables:
- Next.js 16 app:
  - connect page
  - session list
  - session dashboard
  - pane viewer (virtualized)
  - command palette
- TanStack Query + WebSocket event store integration
- Responsive design baseline

Acceptance criteria:
- On desktop: it feels like a real product.
- On mobile: you can triage, send, interrupt, and read outputs.

---

### Phase 5 — “Stripe-level polish” pass
Deliverables:
- Full design system:
  - tokens, components, motion guidelines
- Delightful micro-interactions:
  - command palette animations
  - smooth transitions
  - great empty states
- Accessibility audit
- Performance profiling and optimization

Acceptance criteria:
- A cold user can discover core workflows without docs.
- The UI feels premium and “finished”.

---

### Phase 6 — Structured agents (ACP/SDK) experimental track
Deliverables:
- Agent Driver abstraction:
  - tmuxDriver (default)
  - acpDriver (experimental)
- Structured tool calls/diffs where available
- UI panels:
  - tool call timeline
  - diff viewer with “apply” flow
  - structured errors

Acceptance criteria:
- A “structured session” can show diffs and tool calls without regex parsing.

---

## 13. Testing strategy (don’t ship without this)

### 13.1 API contract tests
- Validate OpenAPI → generated client → live server
- Ensure response schemas match spec

### 13.2 Parity tests
- For each kernel command:
  - call via CLI wrapper
  - call via REST
  - compare normalized output (where deterministic)

### 13.3 Streaming tests
- WS reconnection / resume
- backpressure behavior
- “dropped output” correctness

### 13.4 UI e2e
- Session list load
- Spawn session
- Send prompt
- Confirm output arrives in UI

---

## 14. Risk register + mitigations

### Risk: API drift / feature mismatch
**Mitigation:** Command Kernel + Parity Gate.

### Risk: WebSocket scaling with many panes
**Mitigation:** pipe-pane, ring buffers, topic-based subscriptions, virtualization.

### Risk: Security exposure (remote control)
**Mitigation:** localhost default, mandatory auth for remote bind, RBAC, audit, approvals.

### Risk: “tmux in a browser” complexity creep
**Mitigation:** don’t start there. Start with:
- output streaming + actions
Then optionally add “web terminal” as a separate feature.

### Risk: Vercel WebSocket limitations
**Mitigation:** UI on Vercel; WS daemon elsewhere. citeturn14view0turn15view0

---

## 15. Appendix A — Example endpoint designs (concrete)

### 15.1 Spawn session (equivalent: `ntm spawn myproject --cc=2 --cod=1`)

`POST /api/v1/sessions/myproject/spawn`

Request:

```json
{
  "agents": {"claude": 2, "codex": 1, "gemini": 0},
  "options": {
    "attach": false,
    "tile": true,
    "projects_base": null
  }
}
```

Response (202 Accepted):

```json
{
  "operation_id": "op_01H...",
  "status": "running",
  "session": "myproject",
  "started_at": "2026-01-07T00:00:00Z"
}
```

The operation emits WS events:
- `session.created`
- `agent.spawned`
- `pane.created`

### 15.2 Send prompt (equivalent: `ntm send myproject --cc "Hello"`)

`POST /api/v1/sessions/myproject/agents/send`

Request:

```json
{
  "target": {"type": "claude"},
  "message": "Hello! Explore this codebase and summarize its architecture.",
  "options": {"enter": true}
}
```

Response:

```json
{
  "delivered": 2,
  "failed": 0,
  "targets": [
    {"pane": 1, "name": "myproject__cc_1"},
    {"pane": 2, "name": "myproject__cc_2"}
  ]
}
```

---

## 16. Appendix B — Recommended repo layout (monorepo)

```
ntm/
  cmd/ntm/                   # existing
  internal/
    kernel/                  # new: command registry + schemas
    api/                     # new: REST + WS server
    stream/                  # new: pipe-pane reader + ring buffers
    auth/                    # shared auth + RBAC
  web/                       # new: Next.js app (bun)
    bun.lock
    app/
    components/
    lib/
    styles/
```

---

## 17. Appendix C — References (URLs)

(Provided as plain URLs for easy copy/paste.)

```text
Next.js 16 release post:
https://nextjs.org/blog/next-16

Vercel community thread (WebSockets support):
https://github.com/vercel/community/discussions/4999

Vercel KB: “Do Vercel Serverless Functions support WebSocket connections?”:
https://vercel.com/help/do-vercel-serverless-functions-support-websocket-connections

Bun runtime on Vercel docs:
https://vercel.com/docs/runtimes/bun

Bun blog: “Vercel Adds Bun Support”:
https://bun.sh/blog/vercel-adds-bun-support

Agent Client Protocol docs:
https://agentclientprotocol.com/overview/introduction
https://agentclientprotocol.com/protocol/transports

AI SDK ACP provider:
https://ai-sdk.dev/providers/community-providers/acp

Claude Agent SDK docs:
https://docs.anthropic.com/en/docs/claude-code/sdk

OpenAI Codex SDK docs:
https://developers.openai.com/codex/sdk
```

---

## 18. Closing: what “done” looks like

When this plan is executed, NTM becomes:

- A **first-class platform**, not just a CLI.
- A **web cockpit** that makes multi-agent orchestration feel easy and beautiful.
- An **automation substrate** where agents can self-serve via OpenAPI and stream events via WS.
- A system that stays **safe-by-default**, even as it gets more powerful.

If you want a single mantra for implementation:

> **Make the API the truth, and make the UI a gorgeous lens over it.**
