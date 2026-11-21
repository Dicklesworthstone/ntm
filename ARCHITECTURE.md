# NTM Architecture

NTM (Named Tmux Manager) is a single-binary Go control plane that turns `tmux` into an observable, policy-enforced, multi-agent workspace with synchronized CLI, robot JSON/TOON, REST/SSE/WebSocket, TUI, embedded Web Cockpit, and VS Code extension surfaces.

## High-Level Topology

```
                     ┌──────────────────────────────────────┐
                     │           cmd/ntm (Entrypoint)       │
                     └──────────────────┬───────────────────┘
                                        │
        ┌───────────────┬───────────────┼───────────────┬───────────────┐
        ▼               ▼               ▼               ▼               ▼
  internal/cli    internal/robot  internal/serve   internal/tui   internal/webui
  (Cobra CLI)     (--robot-* API) (REST/SSE/WS)    (Bubble Tea)   (Embedded SPA)
        │               │               │               │               │
        └───────────────┴───────┬───────┴───────────────┴───────────────┘
                                ▼
             ┌────────────────────────────────────┐
             │    Shared Domain & ORI Engines     │
             ├────────────────────────────────────┤
             │ internal/agent    (12 harnesses)   │
             │ internal/tmux     (PTY/pane layer) │
             │ internal/session  (lifecycle)      │
             │ internal/dispatch / internal/send  │
             │ internal/context  (usage/rotation) │
             │ internal/pipeline (YAML workflows) │
             │ internal/ensemble (mode synthesis) │
             │ internal/policy   (DCG/redaction)  │
             │ internal/state    (SQLite runtime) │
             │ internal/webhook  (SSRF/HMAC/outbox│
             └────────────────────────────────────┘
                                │
        ┌───────────────────────┼───────────────────────┐
        ▼                       ▼                       ▼
   tmux server          External Ecosystem        Deliverables
   (panes/windows)   (br, bv, Agent Mail, cass,  (web/ Next.js export,
                      cm, dcg, caam, pt, slb)     vscode/ VSIX ext)
```

## Core Architectural Invariants

1. **One Reachable Implementation (ORI):** Every capability has at most one implementation reachable from `cmd/ntm`. Enforcement is mechanical via `scripts/guards/deadcode_gate.sh` (WS0-G1) and `scripts/guards/run_all.sh`.
2. **Declarative 12-Harness Registry (`internal/agent/capabilities.go`):** Built-in agent harnesses (`cc`, `cod`, `gmi`, `agy`, `grok`, `oc`, `omp`, `pi`, `cursor`, `windsurf`, `aider`, `ollama`) are defined once in `canonicalHarnesses` and exposed to runtime subsystems (spawn, delivery, composer readiness, context compaction/rotation, rate-limit tracking, graceful exit, TUI icons) as well as machine discovery via `ntm --robot-capabilities`.
3. **Fail-Closed Policy & Redaction (`internal/policy`, `internal/redaction`, `internal/serve`):** Outgoing prompts, REST bodies/responses, SSE/WebSocket events, checkpoints, and webhooks enforce configured redaction/block policies and SSRF/HMAC boundaries. `ntm serve` enforces RBAC permissions across both `/api/v1/*` and legacy `/api/*`/`/events` routes, supports multi-algorithm OIDC (`RS256`, `PS256`, `ES256`, `ES384`, `EdDSA`) with strict JWK pinning, bounds per-client HTTP rate-limiter memory, and serves airgapped local OpenAPI docs with strict CSP.
4. **Durable State & Recovery (`internal/state`, `internal/checkpoint`, `internal/pipeline`, `internal/webhook`):** SQLite runtime projections (`modernc.org/sqlite`), journaled context rotations, per-iteration pipeline loop checkpoints, and crash-safe webhook outbox receipts ensure interrupted operations stop for explicit operator recovery instead of blindly re-executing side effects.
5. **Surface Parity & Mechanical Guards (`scripts/guards/`):**
   - **WS0-G1 (`deadcode_gate.sh`)**: Reachability from `cmd/ntm`.
   - **WS0-G2 (`check_allowlists.sh`)**: Bead-first waiver discipline.
   - **WS0-G3 (`tests/docs_conformance_test.go`)**: Executes documented `ntm` CLI examples against the live Cobra tree.
   - **WS0-G4 (`release_preflight.sh`)**: Verifies `release_artifacts.toml` deliverables (Go binary, `install.sh`, `web/`, `vscode/` VSIX) stay version-locked to `VERSION`.
   - **WS0-G5 (`parity_harness.sh`)**: Verifies OpenAPI/REST/CLI surface parity.
   - **WS0-G6 (`SchemaPaginationViolations`)**: Enforces single-declaration pagination metadata.
   - **WS0-G8 (`web_and_surface_gate.sh`)**: Verifies `web/` static export integrity, stale buildId ratchets, VSCode test boundedness, and DSR-only workflow triggers.

## Companion Deliverables

- **Web Cockpit (`web/` → `internal/webui/dist/`):** Next.js 16 static export embedded via `go:embed` and served by `ntm serve` / `ntm web`, backed by OpenAPI-generated TypeScript types (`docs/openapi.json` → `web/src/lib/api/schema.d.ts`) and live WebSocket topic subscriptions (`global:*`, `session:*`, `panes:*`, `agent:*`, `attention`, `beads:*`, `mail:*`, `reservations:*`, `pipelines:*`).
- **VS Code Extension (`vscode/`):** Thin TypeScript client wrapping `ntm --robot-*` JSON commands for session trees, ensemble spawning, attention triage, worktrees, and launching the Web Cockpit. Packaged via `scripts/build_vsix.sh` as a DSR release asset.

## Architecture Decision Records (ADRs)

- [ADR-0001: HyperSync Deferred Out of Core NTM Scope](docs/adr/0001-hypersync-deferred-out-of-scope.md)
- [ADR-0002: tmux PTY Control Plane vs. ACP Transport](docs/adr/0002-acp-vs-tmux-control-plane.md)
