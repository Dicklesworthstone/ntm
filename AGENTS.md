# AGENTS.md — ntm

> Guidelines for AI coding agents working in this Go codebase.

---

## RULE 0 - THE FUNDAMENTAL OVERRIDE PREROGATIVE

If I tell you to do something, even if it goes against what follows below, YOU MUST LISTEN TO ME. I AM IN CHARGE, NOT YOU.

---

## RULE NUMBER 1: NO FILE DELETION

**YOU ARE NEVER ALLOWED TO DELETE A FILE WITHOUT EXPRESS PERMISSION.** Even a new file that you yourself created, such as a test code file. You have a horrible track record of deleting critically important files or otherwise throwing away tons of expensive work. As a result, you have permanently lost any and all rights to determine that a file or folder should be deleted.

**YOU MUST ALWAYS ASK AND RECEIVE CLEAR, WRITTEN PERMISSION BEFORE EVER DELETING A FILE OR FOLDER OF ANY KIND.**

---

## Irreversible Git & Filesystem Actions — DO NOT EVER BREAK GLASS

1. **Absolutely forbidden commands:** `git reset --hard`, `git clean -fd`, `rm -rf`, or any command that can delete or overwrite code/data must never be run unless the user explicitly provides the exact command and states, in the same message, that they understand and want the irreversible consequences.
2. **No guessing:** If there is any uncertainty about what a command might delete or overwrite, stop immediately and ask the user for specific approval. "I think it's safe" is never acceptable.
3. **Safer alternatives first:** When cleanup or rollbacks are needed, request permission to use non-destructive options (`git status`, `git diff`, `git stash`, copying to backups) before ever considering a destructive command.
4. **Mandatory explicit plan:** Even after explicit user authorization, restate the command verbatim, list exactly what will be affected, and wait for a confirmation that your understanding is correct. Only then may you execute it—if anything remains ambiguous, refuse and escalate.
5. **Document the confirmation:** When running any approved destructive command, record (in the session notes / final response) the exact user text that authorized it, the command actually run, and the execution time. If that record is absent, the operation did not happen.

---

## Git Branch: `main` Only

**This repository uses exactly one branch: `main`.**

- **All work happens on `main`** — commits, PRs, and automation all target `main`
- **Do not create, use, or sync `master`** — if you see branch logic that references `master`, remove it
- **Do not create or keep side branches like `beads-sync`** unless I explicitly direct it for a temporary workflow

**If you see non-`main` branch handling in code or docs:**
1. Update it to `main`
2. Remove any implication that `master` or another long-lived branch should exist

---

## Toolchain: Go

We only use **Go** in this project. This is a pure Go project — never introduce non-Go tooling for building or testing.

- **Version:** Go 1.26.3+ (as specified in `go.mod`)
- **Lockfiles:** `go.mod` and `go.sum` only
- **Build:** `go build ./cmd/ntm`
- **Format:** `gofmt` or `goimports`
- **Unsafe code:** Not applicable (Go is memory-safe by design)

### Key Dependencies

| Package | Purpose |
|---------|---------|
| `spf13/cobra` | CLI command framework |
| `charmbracelet/bubbletea` | Terminal UI framework |
| `charmbracelet/bubbles` | TUI component library |
| `charmbracelet/lipgloss` | Terminal styling |
| `charmbracelet/glamour` | Markdown rendering |
| `mattn/go-sqlite3` | SQLite database driver |
| `go-chi/chi/v5` | HTTP router |
| `gorilla/websocket` | WebSocket support |
| `fsnotify/fsnotify` | File system event watching |
| `chromedp/chromedp` | Chrome DevTools Protocol |
| `shirou/gopsutil/v4` | System process utilities |
| `sergi/go-diff` | Text diffing |
| `BurntSushi/toml` | TOML configuration parsing |
| `muesli/termenv` | Terminal environment detection |

### Build & Release

```makefile
# Build for current platform
go build -trimpath -ldflags "-s -w" -o ntm ./cmd/ntm

# Cross-compile (darwin/linux, amd64/arm64)
make build-all
```

### Release Checklist

After creating a new release (via `dsr fallback` or manual cross-compile + `gh release create`):

1. **Verify install script works**: `curl -fsSL ".../install.sh" | bash -s -- --version=vX.Y.Z --dir=/tmp/test --no-shell`
2. **Check flywheel setup checksums**: If `install.sh` content changed, update the SHA256 in `/dp/agentic_coding_flywheel_setup/checksums.yaml` under the `ntm:` entry. If `install.sh` was not modified, no update is needed (the checksum pins the installer script, not the release binaries).

### Logging & Console Output

- Use the standard `log` package or `log/slog` for structured logging.
- No random `fmt.Println` in library code; if needed, make them debug-only and clean them up.
- Log structured context: IDs, session names, pane indices, agent types, etc.
- If a logging pattern exists in the codebase, follow it; do not invent a different pattern.

---

## Code Editing Discipline

### No Script-Based Changes

**NEVER** run a script that processes/changes code files in this repo. Brittle regex-based transformations create far more problems than they solve.

- **Always make code changes manually**, even when there are many instances
- For many simple changes: use parallel subagents
- For subtle/complex changes: do them methodically yourself

### No File Proliferation

If you want to change something or add a feature, **revise existing code files in place**.

**NEVER** create variations like:
- `manager_v2.go`
- `session_improved.go`
- `robot_enhanced.go`

New files are reserved for **genuinely new functionality** that makes zero sense to include in any existing file. The bar for creating new files is **incredibly high**.

---

## Backwards Compatibility

We do not care about backwards compatibility—we're in early development with no users. We want to do things the **RIGHT** way with **NO TECH DEBT**.

- Never create "compatibility shims"
- Never create wrapper functions for deprecated APIs
- Just fix the code directly

---

## Compiler Checks (CRITICAL)

**After any substantive code changes, you MUST verify no errors were introduced:**

```bash
# Build the binary
go build ./cmd/ntm

# Run tests (fast, skips E2E)
go test -short ./...

# Run linter (if available)
golangci-lint run

# Verify formatting
gofmt -l .
goimports -l .
```

If you see errors, **carefully understand and resolve each issue**. Read sufficient context to fix them the RIGHT way.

---

## Testing

### Testing Policy

Tests live alongside the implementation in `_test.go` files within each package. Tests must cover:
- Happy path
- Edge cases (empty input, max values, boundary conditions)
- Error conditions

E2E tests live in the `e2e/` directory and require running agents.

### Running Tests

```bash
# Run all tests (fast, skips E2E)
go test -short ./...

# Run all tests including E2E (requires agents)
go test -v ./...

# Run E2E tests only
go test -v ./e2e/...

# Run tests with coverage
go test -v -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html

# Run specific package tests
go test -v ./internal/robot/...
go test -v ./internal/session/...
go test -v ./internal/tmux/...

# Validate upgrade asset naming contract
go test -v -run TestUpgradeAsset ./internal/cli/
```

---

## Third-Party Library Usage

If you aren't 100% sure how to use a third-party library, **SEARCH ONLINE** to find the latest documentation and current best practices.

---

## ntm — This Project

**This is the project you're working on.** ntm (Named Tmux Manager) is a tmux session manager with built-in multi-agent orchestration, robot-mode JSON API, and a TUI dashboard.

### What It Does

Manages named tmux sessions with agent-aware panes, providing a JSON API (`--robot-*` flags) for AI agents to spawn, inspect, coordinate, and control sessions. Includes a TUI dashboard, CASS integration, and multi-agent ensemble orchestration.

### Project Structure

```
ntm/
├── cmd/ntm/                # Binary entry point
├── internal/
│   ├── cli/                # Cobra command definitions, version, upgrade
│   ├── robot/              # Robot-mode JSON API (--robot-* flags)
│   ├── session/            # Named session lifecycle management
│   ├── tmux/               # Tmux process interaction layer
│   ├── tui/                # Bubbletea TUI dashboard
│   ├── agent/              # Agent type detection and metadata
│   ├── agentmail/          # MCP Agent Mail integration
│   ├── ensemble/           # Multi-agent ensemble orchestration
│   ├── swarm/              # Swarm coordination
│   ├── coordinator/        # Agent coordination logic
│   ├── assign/             # Task assignment engine
│   ├── scheduler/          # Task scheduling
│   ├── pipeline/           # Execution pipeline
│   ├── context/            # Context window tracking
│   ├── checkpoint/         # Session checkpoint/restore
│   ├── state/              # Persistent state management
│   ├── config/             # Configuration loading (TOML/YAML)
│   ├── scanner/            # Agent session scanning
│   ├── events/             # Event system
│   ├── alerts/             # Alert generation
│   ├── metrics/            # Metrics collection
│   ├── health/             # Health checks
│   ├── hooks/              # Lifecycle hooks
│   ├── handoff/            # Agent handoff protocol
│   ├── output/             # Output formatting
│   ├── palette/            # Color palette management
│   ├── serve/              # HTTP/WebSocket server
│   ├── bv/                 # bv (graph triage) integration
│   ├── cass/               # CASS (cross-agent search) integration
│   ├── cm/                 # cass-memory integration
│   ├── git/                # Git operations
│   ├── audit/              # Audit logging
│   ├── auth/               # Authentication
│   ├── scoring/            # Agent scoring
│   ├── watcher/            # File/process watching
│   ├── webhook/            # Webhook dispatch
│   ├── workflow/           # Workflow definitions
│   ├── worktrees/          # Git worktree management
│   ├── tools/              # Tool registry
│   ├── tracker/            # Progress tracking
│   ├── util/               # Shared utilities
│   └── ...                 # Additional internal packages
├── e2e/                    # End-to-end tests
├── docs/                   # Documentation
└── Makefile                # Build targets
```

### Robot Mode API Design

NTM provides a JSON API for AI agents via `--robot-*` flags. When working on or using this API:

- **Key patterns**:
  - Global commands: bool flags (`--robot-status`)
  - Session-scoped: `=SESSION` syntax (`--robot-send=myproject`)
  - Modifiers: unprefixed global flags (`--limit`, `--since`, `--type`)
- **Deprecation**: Old prefixed flags (e.g., `--cass-limit`) remain for backward compatibility but canonical unprefixed forms are preferred
- **Quick reference**: `ntm --robot-help`
- **Machine-readable schema**: `ntm --robot-capabilities`

### Robot Command Exit Codes

All `--robot-*` commands follow a consistent exit code convention:

| Exit Code | Meaning | JSON Response | Agent Action |
|-----------|---------|---------------|--------------|
| 0 | Success | `{"success": true, ...}` | Proceed with response data |
| 1 | Error | `{"success": false, "error_code": "...", ...}` | Handle error, maybe retry |
| 2 | Unavailable | `{"success": false, "error_code": "NOT_IMPLEMENTED", ...}` | Skip gracefully, log for awareness |

Common error codes: `SESSION_NOT_FOUND`, `PANE_NOT_FOUND`, `INVALID_FLAG`, `TIMEOUT`, `INTERNAL_ERROR`, `NOT_IMPLEMENTED`.

### JSON Field Semantics

Robot command outputs follow consistent semantics for absent, null, and empty fields:

**Always Present (Required Fields)**
- `success`: boolean - Whether the operation succeeded
- `timestamp`: RFC3339 string - When the response was generated
- Critical arrays like `sessions`, `panes`, `targets`, `agents` - Always present, empty array `[]` if none found

**Absent Fields** — Fields may be absent when they don't apply to this response type (e.g., `_agent_hints` absent when no hints are relevant, `dry_run` absent when not in preview mode).

**Empty Arrays vs Absent** — Empty arrays indicate "checked, found nothing" (distinct from "didn't check"). Critical arrays are never absent.

**Optional Fields (omitempty)** — Only present when they have meaningful values: `error`, `error_code`, `hint`, `variant`, `preset_used`, `_agent_hints`, `warnings`, `notes`.

**Null Fields** — Go doesn't typically emit `null` for missing values. Fields are either present with a value or absent entirely.

### Robot Flag Quick Reference

**State Inspection Commands:**

| Flag | Description | Example |
|------|-------------|---------|
| `--robot-status` | Get sessions, panes, agent states | `ntm --robot-status` |
| `--robot-context` | Context window usage estimates per agent | `ntm --robot-context=proj` |
| `--robot-snapshot` | Unified state: sessions + beads + alerts + mail | `ntm --robot-snapshot --since=2025-01-01T00:00:00Z` |
| `--robot-tail=SESSION` | Capture recent pane output | `ntm --robot-tail=proj --lines=50 --panes=1,2` |
| `--robot-plan` | Get bv execution plan with parallelizable tracks | `ntm --robot-plan` |
| `--robot-graph` | Get dependency graph insights | `ntm --robot-graph` |
| `--robot-dashboard` | Dashboard summary as markdown | `ntm --robot-dashboard` |
| `--robot-terse` | Single-line state (minimal tokens) | `ntm --robot-terse` |
| `--robot-markdown` | System state as markdown tables | `ntm --robot-markdown --md-sections=sessions,beads` |

**Agent Control Commands:**

| Flag | Description | Example |
|------|-------------|---------|
| `--robot-send=SESSION` | Send message to panes | `ntm --robot-send=proj --msg='Fix auth' --type=claude` |
| `--robot-ack=SESSION` | Watch for agent responses | `ntm --robot-ack=proj --ack-timeout=30s` |
| `--robot-spawn=SESSION` | Create session with agents | `ntm --robot-spawn=proj --spawn-cc=2 --spawn-wait` |
| `--robot-interrupt=SESSION` | Send Ctrl+C, optionally new task | `ntm --robot-interrupt=proj --interrupt-msg='Stop'` |

**Supporting Flags:**

| Flag | Required With | Optional With | Description |
|------|---------------|---------------|-------------|
| `--msg` | `--robot-send` | `--robot-ack` | Message content |
| `--panes` | - | `--robot-tail`, `--robot-send`, `--robot-ack`, `--robot-interrupt` | Filter to pane indices |
| `--type` | - | `--robot-send`, `--robot-ack`, `--robot-interrupt` | Agent type: claude\|cc, codex\|cod, gemini\|gmi |
| `--all` | - | `--robot-send`, `--robot-interrupt` | Include user pane |
| `--track` | - | `--robot-send` | Combined send+ack mode |
| `--lines` | - | `--robot-tail` | Lines per pane (default 20) |
| `--since` | - | `--robot-snapshot` | RFC3339 timestamp for delta |

**CASS Integration:**

| Flag | Description | Example |
|------|-------------|---------|
| `--robot-cass-search=QUERY` | Search past conversations | `ntm --robot-cass-search='auth error' --cass-since=7d` |
| `--robot-cass-status` | Get CASS health/stats | `ntm --robot-cass-status` |
| `--robot-cass-context=QUERY` | Get relevant past context | `ntm --robot-cass-context='how to implement auth'` |
| `--cass-agent` | Filter by agent type | `--cass-agent=claude` |
| `--cass-since` | Filter by recency | `--cass-since=7d` |

---

## MCP Agent Mail — Multi-Agent Coordination

A mail-like layer that lets coding agents coordinate asynchronously via MCP tools and resources. Provides identities, inbox/outbox, searchable threads, and advisory file reservations with human-auditable artifacts in Git.

Agent Mail is already available as an MCP server; do not treat it as a CLI you must shell out to. MCP Agent Mail *should* be available to you as an MCP server; if it's not, then flag to the user. They might need to start Agent Mail using the `am` alias or by running `cd "<directory_where_they_installed_agent_mail>/mcp_agent_mail" && bash scripts/run_server_with_token.sh` if the alias isn't available or isn't working.

### Why It's Useful

- **Prevents conflicts:** Explicit file reservations (leases) for files/globs
- **Token-efficient:** Messages stored in per-project archive, not in context
- **Quick reads:** `resource://inbox/...`, `resource://thread/...`

### Same Repository Workflow

1. **Register identity:**
   ```
   ensure_project(project_key=<abs-path>)
   register_agent(project_key, program, model)
   ```

2. **Reserve files before editing:**
   ```
   file_reservation_paths(project_key, agent_name, ["internal/**"], ttl_seconds=3600, exclusive=true)
   ```

3. **Communicate with threads:**
   ```
   send_message(..., thread_id="FEAT-123")
   fetch_inbox(project_key, agent_name)
   acknowledge_message(project_key, agent_name, message_id)
   ```

4. **Quick reads:**
   ```
   resource://inbox/{Agent}?project=<abs-path>&limit=20
   resource://thread/{id}?project=<abs-path>&include_bodies=true
   ```

5. **Optional guard configuration:**
   - Set `AGENT_NAME` so the pre-commit guard can block conflicting commits.
   - `WORKTREES_ENABLED=1` and `AGENT_MAIL_GUARD_MODE=warn` during trials.
   - Check hooks with `mcp-agent-mail guard status .` and identity with `mcp-agent-mail mail status .`.

### Multiple Repos in One Product

- Option A: Same `project_key` for all; use specific reservations (`frontend/**`, `backend/**`).
- Option B: Different projects linked via `macro_contact_handshake` or `request_contact` / `respond_contact`. Use a shared `thread_id` (e.g., ticket key) for cross-repo threads.

### Product Bus

```bash
# Create/ensure product
mcp-agent-mail products ensure MyProduct --name "My Product"
# Link repo
mcp-agent-mail products link MyProduct .
# Inspect
mcp-agent-mail products status MyProduct
# Search
mcp-agent-mail products search MyProduct "br-123 OR \"release plan\"" --limit 50
# Product inbox
mcp-agent-mail products inbox MyProduct YourAgent --limit 50 --urgent-only --include-bodies
# Summaries
mcp-agent-mail products summarize-thread MyProduct "br-123" --per-thread-limit 100 --no-llm
```

Server-side tools (for orchestrators) include: `ensure_product(product_key|name)`, `products_link(product_key, project_key)`, `resource://product/{key}`, `search_messages_product(product_key, query, limit=20)`.

### Macros vs Granular Tools

- **Prefer macros for speed:** `macro_start_session`, `macro_prepare_thread`, `macro_file_reservation_cycle`, `macro_contact_handshake`
- **Use granular tools for control:** `register_agent`, `file_reservation_paths`, `send_message`, `fetch_inbox`, `acknowledge_message`

### Common Pitfalls

- `"from_agent not registered"`: Always `register_agent` in the correct `project_key` first
- `"FILE_RESERVATION_CONFLICT"`: Adjust patterns, wait for expiry, or use non-exclusive reservation
- **Auth errors:** If JWT+JWKS enabled, include bearer token with matching `kid`; static bearer only when JWT disabled

---

## Beads (br) — Dependency-Aware Issue Tracking

Beads provides a lightweight, dependency-aware issue database and CLI (`br` - beads_rust) for selecting "ready work," setting priorities, and tracking status. It complements MCP Agent Mail's messaging and file reservations.

**Important:** `br` is non-invasive—it NEVER runs git commands automatically. You must manually commit changes after `br sync --flush-only`.

### Conventions

- **Single source of truth:** Beads for task status/priority/dependencies; Agent Mail for conversation and audit
- **Shared identifiers:** Use Beads issue ID (e.g., `br-123`) as Mail `thread_id` and prefix subjects with `[br-123]`
- **Reservations:** When starting a task, call `file_reservation_paths()` with the issue ID in `reason`
- `.beads/` is authoritative state and **must always be committed** with code changes
- Do not edit `.beads/*.jsonl` directly; only via `br`

### Typical Agent Flow

1. **Pick ready work (Beads):**
   ```bash
   br ready --json  # Choose highest priority, no blockers
   ```

2. **Reserve edit surface (Mail):**
   ```
   file_reservation_paths(project_key, agent_name, ["internal/**"], ttl_seconds=3600, exclusive=true, reason="br-123")
   ```

3. **Announce start (Mail):**
   ```
   send_message(..., thread_id="br-123", subject="[br-123] Start: <title>", ack_required=true)
   ```

4. **Work and update:** Reply in-thread with progress

5. **Complete and release:**
   ```bash
   br close br-42 --reason "Completed"
   br sync --flush-only  # Export to JSONL (no git operations)
   ```
   ```
   release_file_reservations(project_key, agent_name, paths=["internal/**"])
   ```
   Final Mail reply: `[br-123] Completed` with summary

### Issue Types & Priorities

| Priority | Meaning |
|----------|---------|
| `0` | Critical (security, data loss, broken builds) |
| `1` | High |
| `2` | Medium (default) |
| `3` | Low |
| `4` | Backlog |

Types: `bug`, `feature`, `task`, `epic`, `chore`

### Mapping Cheat Sheet

| Concept | Value |
|---------|-------|
| Mail `thread_id` | `br-###` |
| Mail subject | `[br-###] ...` |
| File reservation `reason` | `br-###` |
| Commit messages | Include `br-###` for traceability |

---

## bv — Graph-Aware Triage Engine

bv is a graph-aware triage engine for Beads projects (`.beads/beads.jsonl`). It computes PageRank, betweenness, critical path, cycles, HITS, eigenvector, and k-core metrics deterministically.

**Scope boundary:** bv handles *what to work on* (triage, priority, planning). For agent-to-agent coordination (messaging, work claiming, file reservations), use MCP Agent Mail.

**CRITICAL: Use ONLY `--robot-*` flags. Bare `bv` launches an interactive TUI that blocks your session.**

### The Workflow: Start With Triage

**`bv --robot-triage` is your single entry point.** It returns:
- `quick_ref`: at-a-glance counts + top 3 picks
- `recommendations`: ranked actionable items with scores, reasons, unblock info
- `quick_wins`: low-effort high-impact items
- `blockers_to_clear`: items that unblock the most downstream work
- `project_health`: status/type/priority distributions, graph metrics
- `commands`: copy-paste shell commands for next steps

```bash
bv --robot-triage        # THE MEGA-COMMAND: start here
bv --robot-next          # Minimal: just the single top pick + claim command
```

### Command Reference

**Planning:**
| Command | Returns |
|---------|---------|
| `--robot-plan` | Parallel execution tracks with `unblocks` lists |
| `--robot-priority` | Priority misalignment detection with confidence |

**Graph Analysis:**
| Command | Returns |
|---------|---------|
| `--robot-insights` | Full metrics: PageRank, betweenness, HITS, eigenvector, critical path, cycles, k-core, articulation points, slack |
| `--robot-label-health` | Per-label health: `health_level`, `velocity_score`, `staleness`, `blocked_count` |
| `--robot-label-flow` | Cross-label dependency: `flow_matrix`, `dependencies`, `bottleneck_labels` |
| `--robot-label-attention [--attention-limit=N]` | Attention-ranked labels |

**History & Change Tracking:**
| Command | Returns |
|---------|---------|
| `--robot-history` | Bead-to-commit correlations |
| `--robot-diff --diff-since <ref>` | Changes since ref: new/closed/modified issues, cycles |

**Other:**
| Command | Returns |
|---------|---------|
| `--robot-burndown <sprint>` | Sprint burndown, scope changes, at-risk items |
| `--robot-forecast <id\|all>` | ETA predictions with dependency-aware scheduling |
| `--robot-alerts` | Stale issues, blocking cascades, priority mismatches |
| `--robot-suggest` | Hygiene: duplicates, missing deps, label suggestions |
| `--robot-graph [--graph-format=json\|dot\|mermaid]` | Dependency graph export |
| `--export-graph <file.html>` | Interactive HTML visualization |

### Scoping & Filtering

```bash
bv --robot-plan --label backend              # Scope to label's subgraph
bv --robot-insights --as-of HEAD~30          # Historical point-in-time
bv --recipe actionable --robot-plan          # Pre-filter: ready to work
bv --recipe high-impact --robot-triage       # Pre-filter: top PageRank
bv --robot-triage --robot-triage-by-track    # Group by parallel work streams
bv --robot-triage --robot-triage-by-label    # Group by domain
```

### Understanding Robot Output

**All robot JSON includes:**
- `data_hash` — Fingerprint of source beads.jsonl
- `status` — Per-metric state: `computed|approx|timeout|skipped` + elapsed ms
- `as_of` / `as_of_commit` — Present when using `--as-of`

**Two-phase analysis:**
- **Phase 1 (instant):** degree, topo sort, density
- **Phase 2 (async, 500ms timeout):** PageRank, betweenness, HITS, eigenvector, cycles

### jq Quick Reference

```bash
bv --robot-triage | jq '.quick_ref'                        # At-a-glance summary
bv --robot-triage | jq '.recommendations[0]'               # Top recommendation
bv --robot-plan | jq '.plan.summary.highest_impact'        # Best unblock target
bv --robot-insights | jq '.status'                         # Check metric readiness
bv --robot-insights | jq '.Cycles'                         # Circular deps (must fix!)
bv --robot-label-health | jq '.results.labels[] | select(.health_level == "critical")'
```

---

## UBS — Ultimate Bug Scanner

**Golden Rule:** `ubs <changed-files>` before every commit. Exit 0 = safe. Exit >0 = fix & re-run.

### Commands

```bash
ubs file.go file2.go                        # Specific files (< 1s) — USE THIS
ubs $(git diff --name-only --cached)        # Staged files — before commit
ubs --only=go internal/                     # Language filter (3-5x faster)
ubs --ci --fail-on-warning .                # CI mode — before PR
ubs .                                       # Whole project (ignores things like .venv and node_modules automatically)
```

### Output Format

```
Warning  Category (N errors)
    file.go:42:5 - Issue description
    Suggested fix
Exit code: 1
```

Parse: `file:line:col` -> location | Suggested fix -> how to fix | Exit 0/1 -> pass/fail

### Fix Workflow

1. Read finding -> category + fix suggestion
2. Navigate `file:line:col` -> view context
3. Verify real issue (not false positive)
4. Fix root cause (not symptom)
5. Re-run `ubs <file>` -> exit 0
6. Commit

### Bug Severity

- **Critical (always fix):** Nil pointer dereference, race conditions, goroutine leaks, unchecked errors
- **Important (production):** Type narrowing, division-by-zero, resource leaks, unbounded allocations
- **Contextual (judgment):** TODO/FIXME, fmt.Println debug statements

---

## RCH — Remote Compilation Helper

RCH offloads `go build`, `go test`, `golangci-lint`, and other compilation commands to a fleet of 8 remote Contabo VPS workers instead of building locally. This prevents compilation storms from overwhelming csd when many agents run simultaneously.

**RCH is installed at `~/.local/bin/rch` and is hooked into Claude Code's PreToolUse automatically.** Most of the time you don't need to do anything if you are Claude Code — builds are intercepted and offloaded transparently.

To manually offload a build:
```bash
rch exec -- go build ./cmd/ntm
rch exec -- go test ./...
rch exec -- golangci-lint run
```

Quick commands:
```bash
rch doctor                    # Health check
rch workers probe --all       # Test connectivity to all 8 workers
rch status                    # Overview of current state
rch queue                     # See active/waiting builds
```

If rch or its workers are unavailable, it fails open — builds run locally as normal.

**Note for Codex/GPT-5.2:** Codex does not have the automatic PreToolUse hook, but you can (and should) still manually offload compute-intensive compilation commands using `rch exec -- <command>`. This avoids local resource contention when multiple agents are building simultaneously.

---

## ast-grep vs ripgrep

**Use `ast-grep` when structure matters.** It parses code and matches AST nodes, ignoring comments/strings, and can **safely rewrite** code.

- Refactors/codemods: rename APIs, change import forms
- Policy checks: enforce patterns across a repo
- Editor/automation: LSP mode, `--json` output

**Use `ripgrep` when text is enough.** Fastest way to grep literals/regex.

- Recon: find strings, TODOs, log lines, config values
- Pre-filter: narrow candidate files before ast-grep

### Rule of Thumb

- Need correctness or **applying changes** -> `ast-grep`
- Need raw speed or **hunting text** -> `rg`
- Often combine: `rg` to shortlist files, then `ast-grep` to match/modify

### Go Examples

```bash
# Find all fmt.Println statements
ast-grep run -l Go -p 'fmt.Println($$$)'

# Find all error returns without wrapping
ast-grep run -l Go -p 'return err'

# Quick textual hunt
rg -n 'func.*LoadConfig' -t go

# Combine speed + precision
rg -l -t go 'sync.Mutex' | xargs ast-grep run -l Go -p 'mu.Lock()'
```

---

## Morph Warp Grep — AI-Powered Code Search

**Use `mcp__morph-mcp__warp_grep` for exploratory "how does X work?" questions.** An AI agent expands your query, greps the codebase, reads relevant files, and returns precise line ranges with full context.

**Use `ripgrep` for targeted searches.** When you know exactly what you're looking for.

**Use `ast-grep` for structural patterns.** When you need AST precision for matching/rewriting.

### When to Use What

| Scenario | Tool | Why |
|----------|------|-----|
| "How does robot mode spawn sessions?" | `warp_grep` | Exploratory; don't know where to start |
| "Where is the tmux integration?" | `warp_grep` | Need to understand architecture |
| "Find all uses of `SendKeys`" | `ripgrep` | Targeted literal search |
| "Find files with `fmt.Println`" | `ripgrep` | Simple pattern |
| "Replace all `var` with `const`" | `ast-grep` | Structural refactor |

### warp_grep Usage

```
mcp__morph-mcp__warp_grep(
  repoPath: "/dp/ntm",
  query: "How does robot mode spawn sessions?"
)
```

Returns structured results with file paths, line ranges, and extracted code snippets.

### Anti-Patterns

- **Don't** use `warp_grep` to find a specific function name -> use `ripgrep`
- **Don't** use `ripgrep` to understand "how does X work" -> wastes time with manual reads
- **Don't** use `ripgrep` for codemods -> risks collateral edits

---

## cass — Cross-Agent Search

`cass` indexes prior agent conversations (Claude Code, Codex, Cursor, Gemini, ChatGPT, etc.) so we can reuse solved problems.

Rules:
- Never run bare `cass` (TUI). Always use `--robot` or `--json`.

```bash
cass health
cass search "authentication error" --robot --limit 5
cass view /path/to/session.jsonl -n 42 --json
cass expand /path/to/session.jsonl -n 42 -C 3 --json
cass capabilities --json
cass robot-docs guide
```

Tips:
- Use `--fields minimal` for lean output.
- Filter by agent with `--agent`.
- Use `--days N` to limit to recent history.

stdout is data-only, stderr is diagnostics; exit code 0 means success.

Treat cass as a way to avoid re-solving problems other agents already handled.

---

## Memory System: cass-memory

The Cass Memory System (cm) is a tool for giving agents an effective memory based on the ability to quickly search across previous coding agent sessions across an array of different coding agent tools (e.g., Claude Code, Codex, Gemini-CLI, Cursor, etc.) and projects (and even across multiple machines, optionally) and then reflect on what they find and learn in new sessions to draw out useful lessons and takeaways; these lessons are then stored and can be queried and retrieved later, much like how human memory works.

The `cm onboard` command guides you through analyzing historical sessions and extracting valuable rules.

### Quick Start

```bash
# 1. Check status and see recommendations
cm onboard status

# 2. Get sessions to analyze (filtered by gaps in your playbook)
cm onboard sample --fill-gaps

# 3. Read a session with rich context
cm onboard read /path/to/session.jsonl --template

# 4. Add extracted rules (one at a time or batch)
cm playbook add "Your rule content" --category "debugging"
# Or batch add:
cm playbook add --file rules.json

# 5. Mark session as processed
cm onboard mark-done /path/to/session.jsonl
```

Before starting complex tasks, retrieve relevant context:

```bash
cm context "<task description>" --json
```

This returns:
- **relevantBullets**: Rules that may help with your task
- **antiPatterns**: Pitfalls to avoid
- **historySnippets**: Past sessions that solved similar problems
- **suggestedCassQueries**: Searches for deeper investigation

### Protocol

1. **START**: Run `cm context "<task>" --json` before non-trivial work
2. **WORK**: Reference rule IDs when following them (e.g., "Following b-8f3a2c...")
3. **FEEDBACK**: Leave inline comments when rules help/hurt:
   - `// [cass: helpful b-xyz] - reason`
   - `// [cass: harmful b-xyz] - reason`
4. **END**: Just finish your work. Learning happens automatically.

### Key Flags

| Flag | Purpose |
|------|---------|
| `--json` | Machine-readable JSON output (required!) |
| `--limit N` | Cap number of rules returned |
| `--no-history` | Skip historical snippets for faster response |

stdout = data only, stderr = diagnostics. Exit 0 = success.

<!-- bv-agent-instructions-v1 -->

---

## Beads Workflow Integration

This project uses [beads_rust](https://github.com/Dicklesworthstone/beads_rust) (`br`) for issue tracking. Issues are stored in `.beads/` and tracked in git.

**Important:** `br` is non-invasive—it NEVER executes git commands. After `br sync --flush-only`, you must manually run `git add .beads/ && git commit`.

### Essential Commands

```bash
# View issues (launches TUI - avoid in automated sessions)
bv

# CLI commands for agents (use these instead)
br ready              # Show issues ready to work (no blockers)
br list --status=open # All open issues
br show <id>          # Full issue details with dependencies
br create --title="..." --type=task --priority=2
br update <id> --status=in_progress
br close <id> --reason "Completed"
br close <id1> <id2>  # Close multiple issues at once
br sync --flush-only  # Export to JSONL (NO git operations)
```

### Workflow Pattern

1. **Start**: Run `br ready` to find actionable work
2. **Claim**: Use `br update <id> --status=in_progress`
3. **Work**: Implement the task
4. **Complete**: Use `br close <id>`
5. **Sync**: Run `br sync --flush-only` then manually commit

### Key Concepts

- **Dependencies**: Issues can block other issues. `br ready` shows only unblocked work.
- **Priority**: P0=critical, P1=high, P2=medium, P3=low, P4=backlog (use numbers, not words)
- **Types**: task, bug, feature, epic, question, docs

### Session Protocol

**Before ending any session, run this checklist:**

```bash
git status              # Check what changed
git add <files>         # Stage code changes
br sync --flush-only    # Export beads to JSONL
git add .beads/         # Stage beads changes
git commit -m "..."     # Commit everything together
git push                # Push to remote
```

### Best Practices

- Check `br ready` at session start to find available work
- Update status as you work (in_progress -> closed)
- Create new issues with `br create` when you discover tasks
- Use descriptive titles and set appropriate priority/type
- Always `br sync --flush-only && git add .beads/` before ending session

<!-- end-bv-agent-instructions -->

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Sync beads** - `br sync --flush-only` to export to JSONL
5. **Hand off** - Provide context for next session

---

## Note for Codex/GPT-5.2

You constantly bother me and stop working with concerned questions that look similar to this:

```
Unexpected changes (need guidance)

- Working tree still shows edits I did not make in go.mod, go.sum, internal/cli/send.go, internal/session/manager.go. Please advise whether to keep/commit/revert these before any further work. I did not touch them.

Next steps (pick one)

1. Decide how to handle the unrelated modified files above so we can resume cleanly.
```

NEVER EVER DO THAT AGAIN. The answer is literally ALWAYS the same: those are changes created by the potentially dozen of other agents working on the project at the same time. This is not only a common occurrence, it happens multiple times PER MINUTE. The way to deal with it is simple: you NEVER, under ANY CIRCUMSTANCE, stash, revert, overwrite, or otherwise disturb in ANY way the work of other agents. Just treat those changes identically to changes that you yourself made. Just fool yourself into thinking YOU made the changes and simply don't recall it for some reason.

---

## Note on Built-in TODO Functionality

Also, if I ask you to explicitly use your built-in TODO functionality, don't complain about this and say you need to use beads. You can use built-in TODOs if I tell you specifically to do so. Always comply with such orders.
