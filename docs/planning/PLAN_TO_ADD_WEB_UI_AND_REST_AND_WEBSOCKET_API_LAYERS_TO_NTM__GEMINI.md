# Plan to Add Web UI, REST API, and WebSocket Layers to NTM

## Executive Summary

This document outlines the architectural roadmap to evolve **NTM (Named Tmux Manager)** from a pure CLI/TUI application into a **"Flywheel Gateway"**—a central API and Web UI server that unifies the Agentic Coding Flywheel tools (`bv`, `agent mail`, `cass`, `ubs`, `cm`, `slb`).

We will implement a high-performance **REST and WebSocket API** within the `ntm` binary (via an expanded `serve` command) that acts as the "Command Kernel," exposing all core capabilities to external clients. On top of this, we will build a **world-class Web UI** using **Next.js 16**, **React 19**, and **Bun**, featuring "Stripe-level" design polish and a mobile-first architecture.

---

## 1. System Architecture: "One Core, Many Interfaces"

We will refactor NTM to follow a **Service Layer** pattern. The CLI, TUI, and API will all become consumers of a shared `internal/core` (or `internal/services`) layer, ensuring 100% feature parity.

```mermaid
graph TD
    subgraph "Presentation Layer"
        WebUI[**Next.js Web App**<br>(Mission Control)] -->|HTTP/WS| Gateway
        CLI[**CLI / TUI**] -->|Direct Call| Services
    end

    subgraph "NTM Backend (Gateway)"
        Gateway[**API Router**<br>(Chi / Websocket)] --> Services
        
        subgraph "Service Layer (The Kernel)"
            Services[**Core Services**<br>(Session, Agent, Beads, Mail)]
        end

        subgraph "Infrastructure"
            Orch[**Tmux Orchestrator**<br>(internal/tmux)]
            Bus[**Event Bus**<br>(internal/events)]
            Sup[**Supervisor**<br>(internal/supervisor)]
        end

        subgraph "Flywheel Integrations"
            BV_Client[**Beads Client**<br>(internal/bv)]
            AM_Client[**Mail Client**<br>(internal/agentmail)]
            UBS_Scanner[**UBS Scanner**<br>(internal/scanner)]
            SLB_Engine[**Approval Engine**<br>(internal/approval)]
        end
    end

    Services --> Orch
    Services --> BV_Client
    Services --> AM_Client
    Services --> UBS_Scanner
    Services --> SLB_Engine
    
    Gateway --> Bus
    Gateway --> Sup
    
    Orch -->|Pipe-Pane| Bus
```

### 1.1 The Supervisor Strategy
The existing `internal/supervisor` package will be leveraged by `ntm serve` to manage the lifecycle of auxiliary daemons, ensuring a zero-config startup:
*   **`cm` (CASS Memory):** Started via `cm serve`.
*   **`am` (Agent Mail):** Started via `mcp-agent-mail serve` (if local).
*   **`bd` (Beads):** Started via `bd daemon`.

---

## 2. Backend Strategy (Go)

### 2.1 Service Layer Extraction
We will extract business logic from `internal/cli` and `internal/robot` into reusable services. `internal/robot` structs will be preserved as the canonical data transfer objects (DTOs).

*   **`SessionService`**: Create, Spawn, Kill, List (wraps `internal/tmux`).
*   **`AgentService`**: Send, Interrupt, Wait, Rotate.
*   **`FlywheelService`**: Unifies BV, Mail, UBS, CASS interactions.

### 2.2 API Server (`internal/serve`)
The existing server will be expanded into a robust API gateway.

*   **Router:** `chi` (Lightweight, middleware-friendly).
*   **Auth:** JWT-based `Authorization: Bearer <token>` (Token stored in `~/.config/ntm/auth.token`).
*   **Performance:** `tmux pipe-pane` will be used for output streaming instead of polling `capture-pane`, reducing CPU load significantly.

### 2.3 Endpoints & Data Mapping

#### **1. Orchestration & Session State**
| Method | Endpoint | Response Struct | Description |
| :--- | :--- | :--- | :--- |
| `GET` | `/api/v1/status` | `robot.StatusOutput` | Full session list, agent counts. |
| `GET` | `/api/v1/snapshot` | `robot.SnapshotOutput` | Deep state: sessions + panes + mail + alerts. |
| `GET` | `/api/v1/sessions/{id}/tail` | `robot.TailOutput` | Initial scrollback buffer. |
| `POST` | `/api/v1/sessions` | `robot.SpawnOutput` | Spawn new session. |
| `POST` | `/api/v1/sessions/{id}/send` | `robot.SendOutput` | Broadcast prompt to agents. |

#### **2. Beads (Task Management)**
| Method | Endpoint | Response Struct | Description |
| :--- | :--- | :--- | :--- |
| `GET` | `/api/v1/beads/triage` | `bv.TriageResponse` | Mega-struct: priorities, health, recommendations. |
| `GET` | `/api/v1/beads/graph` | `bv.InsightsResponse` | Bottlenecks, Keystones, Galaxy View data. |
| `GET` | `/api/v1/beads/plan` | `bv.PlanResponse` | Parallel execution tracks. |
| `PATCH` | `/api/v1/beads/{id}` | `bv.Bead` | Update status/priority. |

#### **3. Agent Mail (Communication)**
| Method | Endpoint | Response Struct | Description |
| :--- | :--- | :--- | :--- |
| `GET` | `/api/v1/mail/inbox` | `[]agentmail.InboxMessage` | Unified inbox. |
| `GET` | `/api/v1/mail/threads` | `[]agentmail.ThreadSummary` | Threaded conversations. |
| `POST` | `/api/v1/mail/send` | `agentmail.SendResult` | Send human-overseer message. |
| `GET` | `/api/v1/mail/locks` | `[]agentmail.FileReservation` | Active file reservations. |

#### **4. UBS (Code Health)**
| Method | Endpoint | Response Struct | Description |
| :--- | :--- | :--- | :--- |
| `GET` | `/api/v1/health/scan` | `scanner.ScanResult` | Latest findings, totals. |
| `POST` | `/api/v1/health/scan` | `scanner.ScanResult` | Trigger immediate scan. |

#### **5. SLB (Safety & Approvals)**
| Method | Endpoint | Response Struct | Description |
| :--- | :--- | :--- | :--- |
| `GET` | `/api/v1/approvals` | `[]approval.Request` | Pending actions. |
| `POST` | `/api/v1/approvals/{id}` | `approval.Result` | Approve/Deny action. |

### 2.4 High-Performance WebSocket Stream
We will implement a unified event stream at `/api/v1/stream`.

*   **Mechanism:** `nhooyr.io/websocket`.
*   **Payload:** Typed JSON envelope with `seq` numbers for replay/resume.
*   **Source:** `tmux pipe-pane` -> `internal/events` -> WebSocket.

**Event Types:**
*   `pane.output` (Real-time delta, xterm compatible)
*   `agent.state` (Active/Idle/Error)
*   `mail.received` (New message)
*   `scan.complete` (Refresh health deck)
*   `bead.update` (Refresh kanban)

---

## 3. Frontend Strategy (Next.js 16 + Bun)

The UI will be a "Mission Control" center, organized into specialized **"Decks"**.

### 3.1 Technology Stack
*   **Framework:** Next.js 16 (App Router) + React 19.
*   **Build:** Bun (exclusive).
*   **State:** TanStack Query v5 + Zustand.
*   **Design:** Tailwind CSS 4 + Framer Motion.
*   **Terminal:** xterm.js (WebGL renderer).

### 3.2 Orchestrator Deck (Home)
*   **Pane Grid:** Live `xterm.js` instances.
*   **Agent Cards:** Overlay with status pulse (Green/Blue/Yellow) and token velocity.
*   **Omnibar:** `Cmd+K` global command palette for rapid actions (Spawn, Send, Kill).

### 3.3 Beads Deck (Project Management)
*   **Kanban:** Columns based on `bv` triage (Ready, In Progress, Blocked).
*   **Galaxy View:** React Flow visualization of dependency graph.
    *   **Red Nodes:** Bottlenecks (High Betweenness).
    *   **Gold Nodes:** Keystones (High PageRank).

### 3.4 Comms Deck (Agent Mail)
*   **Unified Inbox:** Email-client style interface.
*   **Thread View:** Markdown rendering of agent conversations.
*   **Reservation Map:** Visual heatmap of locked files.

### 3.5 Health Deck (UBS)
*   **Vitals:** Big number cards for critical/warning counts.
*   **Hotspots:** Treemap of bug density by file.
*   **Scan Log:** History of UBS runs.

### 3.6 Safety Deck (SLB)
*   **Approval Inbox:** Pending dangerous commands requiring human review.
*   **Action:** "Approve" / "Deny" buttons with audit logging.

### 3.7 Mobile Experience (Mobile-First)
*   **Navigation:** Bottom tab bar (Sessions, Beads, Mail, More).
*   **Prompt Sheet:** Swipe-up sheet for sending commands on the go.
*   **Read Mode:** Agent output rendered as Markdown instead of raw terminal text for readability.
*   **Gestures:** Swipe to switch agents, pull to refresh.

---

## 4. Safety & Security

1.  **Local-First:** Defaults to `127.0.0.1`.
2.  **Token Auth:** Randomly generated API token on first run.
3.  **CORS:** Strict origins.
4.  **No Silent Failures:** Dangerous commands (e.g. `rm -rf`, `git reset`) captured by SLB and require explicit API approval.

---

## 5. Deployment Strategy

*   **API (NTM):** Runs on the development machine/VPS (`ntm serve`).
*   **UI (Web):** Can be deployed to Vercel/Netlify (talking to NTM via Tailscale/Tunnel) OR served statically by `ntm` itself (embedded `dist/`).
*   **ACP Compatibility:** Future-proof architecture to support Agent Client Protocol (ACP) adapters if `tmux` spawning is replaced.

---

## 6. Implementation Plan

### Phase 1: Core Refactoring & API Expansion
1.  **Service Layer:** Extract logic from `internal/cli` to `internal/core`.
2.  **API Server:** Expand `internal/serve` with handlers for all decks.
3.  **Supervisor:** Wire up `ntm serve` to start `cm`, `am`, `bd`.

### Phase 2: Domain Integrations
1.  **Flywheel Clients:** Implement `internal/core` wrappers for `bv`, `agentmail`, `scanner`.
2.  **Streaming:** Implement `tmux pipe-pane` log capture and WS broadcaster.

### Phase 3: Frontend Foundation
1.  **Scaffold:** Next.js 16 + Bun setup.
2.  **Components:** Build "Stripe-level" UI kit (Buttons, Badges, Terminal).
3.  **Orchestrator:** Implement the live grid view.

### Phase 4: Full Flywheel UI
1.  **Decks:** Implement Beads, Mail, Health, and Safety decks.
2.  **Mobile:** Optimize layouts for touch.
3.  **Distribution:** Embed frontend into Go binary.

---

## 7. Why This Wins
*   **Ultimate Parity:** The API uses the exact same logic as the CLI.
*   **Performance:** `pipe-pane` + WebGL terminal rendering ensures 60fps even with busy agents.
*   **Completeness:** It's the only plan that deeply integrates the *entire* flywheel (Memory, Planning, Safety) into a cohesive visual interface.