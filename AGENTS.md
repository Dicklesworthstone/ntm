RULE 1 – ABSOLUTE (DO NOT EVER VIOLATE THIS)

You may NOT delete any file or directory unless I explicitly give the exact command **in this session**.

- This includes files you just created (tests, tmp files, scripts, etc.).
- You do not get to decide that something is "safe" to remove.
- If you think something should be removed, stop and ask. You must receive clear written approval **before** any deletion command is even proposed.

Treat "never delete files without permission" as a hard invariant.

---

### IRREVERSIBLE GIT & FILESYSTEM ACTIONS

Absolutely forbidden unless I give the **exact command and explicit approval** in the same message:

- `git reset --hard`
- `git clean -fd`
- `rm -rf`
- Any command that can delete or overwrite code/data

Rules:

1. If you are not 100% sure what a command will delete, do not propose or run it. Ask first.
2. Prefer safe tools: `git status`, `git diff`, `git stash`, copying to backups, etc.
3. After approval, restate the command verbatim, list what it will affect, and wait for confirmation.
4. When a destructive command is run, record in your response:
   - The exact user text authorizing it
   - The command run
   - When you ran it

If that audit trail is missing, then you must act as if the operation never happened.

---

#### Go Toolchain

- Use **go** for everything. This is a pure Go project.
- ❌ Never introduce non-Go tooling for building or testing.

- Lockfiles: `go.mod` and `go.sum` only.
- Target **Go 1.25+** (as specified in go.mod).
- Run tests with `go test ./...`
- Build with `go build ./cmd/ntm`
- Format with `gofmt` or `goimports`

---

### Code Editing Discipline

- Do **not** run scripts that bulk-modify code (codemods, invented one-off scripts, giant `sed`/regex refactors).
- Large mechanical changes: break into smaller, explicit edits and review diffs.
- Subtle/complex changes: edit by hand, file-by-file, with careful reasoning.

---

### Backwards Compatibility & File Sprawl

We optimize for a clean architecture now, not backwards compatibility.

- No "compat shims" or "v2" file clones.
- When changing behavior, migrate callers and remove old code **inside the same file**.
- New files are only for genuinely new domains that don't fit existing modules.
- The bar for adding files is very high.

---

### Logging & Console Output

- Use the standard `log` package or `log/slog` for structured logging.
- No random `fmt.Println` in library code; if needed, make them debug-only and clean them up.
- Log structured context: IDs, session names, pane indices, agent types, etc.
- If a logging pattern exists in the codebase, follow it; do not invent a different pattern.

---

### Third-Party Libraries

When unsure of an API, look up current docs (late-2025) rather than guessing.

---

## MCP Agent Mail — Multi-Agent Coordination

Agent Mail is already available as an MCP server; do not treat it as a CLI you must shell out to. MCP Agent Mail *should* be available to you as an MCP server; if it's not, then flag to the user. They might need to start Agent Mail using the `am` alias or by running `cd "<directory_where_they_installed_agent_mail>/mcp_agent_mail" && bash scripts/run_server_with_token.sh` if the alias isn't available or isn't working.

What Agent Mail gives:

- Identities, inbox/outbox, searchable threads.
- Advisory file reservations (leases) to avoid agents clobbering each other.
- Persistent artifacts in git (human-auditable).

Core patterns:

1. **Same repo**
   - Register identity:
     - `ensure_project` then `register_agent` with the repo's absolute path as `project_key`.
   - Reserve files before editing:
     - `file_reservation_paths(project_key, agent_name, ["internal/**"], ttl_seconds=3600, exclusive=true)`.
   - Communicate:
     - `send_message(..., thread_id="FEAT-123")`.
     - `fetch_inbox`, then `acknowledge_message`.
   - Fast reads:
     - `resource://inbox/{Agent}?project=<abs-path>&limit=20`.
     - `resource://thread/{id}?project=<abs-path>&include_bodies=true`.
   - Optional:
     - Set `AGENT_NAME` so the pre-commit guard can block conflicting commits.
     - `WORKTREES_ENABLED=1` and `AGENT_MAIL_GUARD_MODE=warn` during trials.
     - Check hooks with `mcp-agent-mail guard status .` and identity with `mcp-agent-mail mail status .`.

2. **Multiple repos in one product**
   - Option A: Same `project_key` for all; use specific reservations (`frontend/**`, `backend/**`).
   - Option B: Different projects linked via:
     - `macro_contact_handshake` or `request_contact` / `respond_contact`.
     - Use a shared `thread_id` (e.g., ticket key) for cross-repo threads.

Macros vs granular:

- Prefer macros when speed is more important than fine-grained control:
  - `macro_start_session`, `macro_prepare_thread`, `macro_file_reservation_cycle`, `macro_contact_handshake`.
- Use granular tools when you need explicit behavior.

Product bus:

- Create/ensure product: `mcp-agent-mail products ensure MyProduct --name "My Product"`.
- Link repo: `mcp-agent-mail products link MyProduct .`.
- Inspect: `mcp-agent-mail products status MyProduct`.
- Search: `mcp-agent-mail products search MyProduct "br-123 OR \"release plan\"" --limit 50`.
- Product inbox: `mcp-agent-mail products inbox MyProduct YourAgent --limit 50 --urgent-only --include-bodies`.
- Summaries: `mcp-agent-mail products summarize-thread MyProduct "br-123" --per-thread-limit 100 --no-llm`.

Server-side tools (for orchestrators) include:

- `ensure_product(product_key|name)`
- `products_link(product_key, project_key)`
- `resource://product/{key}`
- `search_messages_product(product_key, query, limit=20)`

Common pitfalls:

- "from_agent not registered" → call `register_agent` with correct `project_key`.
- `FILE_RESERVATION_CONFLICT` → adjust patterns, wait for expiry, or use non-exclusive reservation.
- Auth issues with JWT+JWKS → bearer token with `kid` matching server JWKS; static bearer only when JWT disabled.

---

## Issue Tracking with br (beads_rust)

All issue tracking goes through **br**. No other TODO systems.

**Note:** `br` (beads_rust) is non-invasive and never executes git commands directly. You must manually run git operations after `br sync --flush-only`.

Key invariants:

- `.beads/` is authoritative state and **must always be committed** with code changes.
- Do not edit `.beads/*.jsonl` directly; only via `br`.

### Basics

Check ready work:

```bash
br ready --json
```

Create issues:

```bash
br create "Issue title" -t bug|feature|task -p 0-4 --json
br create "Issue title" -p 1 --deps discovered-from:br-123 --json
```

Update:

```bash
br update br-42 --status in_progress --json
br update br-42 --priority 1 --json
```

Complete:

```bash
br close br-42 --reason "Completed" --json
```

Types:

- `bug`, `feature`, `task`, `epic`, `chore`

Priorities:

- `0` critical (security, data loss, broken builds)
- `1` high
- `2` medium (default)
- `3` low
- `4` backlog

Agent workflow:

1. `br ready` to find unblocked work.
2. Claim: `br update <id> --status in_progress`.
3. Implement + test.
4. If you discover new work, create a new bead with `discovered-from:<parent-id>`.
5. Close when done.
6. Commit `.beads/` in the same commit as code changes.

Sync workflow:

- Run `br sync --flush-only` to export to JSONL (does NOT run git commands).
- Then manually run: `git add .beads/ && git commit -m "Update beads" && git push`

Never:

- Use markdown TODO lists.
- Use other trackers.
- Duplicate tracking.

---

### Using bv as an AI sidecar

bv is a graph-aware triage engine for Beads projects (.beads/beads.jsonl). Instead of parsing JSONL or hallucinating graph traversal, use robot flags for deterministic, dependency-aware outputs with precomputed metrics (PageRank, betweenness, critical path, cycles, HITS, eigenvector, k-core).

**Scope boundary:** bv handles *what to work on* (triage, priority, planning). For agent-to-agent coordination (messaging, work claiming, file reservations), use MCP Agent Mail, which should be available to you as an MCP server (if it's not, then flag to the user; they might need to start Agent Mail using the `am` alias or by running `cd "<directory_where_they_installed_agent_mail>/mcp_agent_mail" && bash scripts/run_server_with_token.sh` if the alias isn't available or isn't working.

**⚠️ CRITICAL: Use ONLY `--robot-*` flags. Bare `bv` launches an interactive TUI that blocks your session.**

#### The Workflow: Start With Triage

**`bv --robot-triage` is your single entry point.** It returns everything you need in one call:
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

#### Other bv Commands

**Planning:**
| Command | Returns |
|---------|---------|
| `--robot-plan` | Parallel execution tracks with `unblocks` lists |
| `--robot-priority` | Priority misalignment detection with confidence |

**Graph Analysis:**
| Command | Returns |
|---------|---------|
| `--robot-insights` | Full metrics: PageRank, betweenness, HITS (hubs/authorities), eigenvector, critical path, cycles, k-core, articulation points, slack |
| `--robot-label-health` | Per-label health: `health_level` (healthy\|warning\|critical), `velocity_score`, `staleness`, `blocked_count` |
| `--robot-label-flow` | Cross-label dependency: `flow_matrix`, `dependencies`, `bottleneck_labels` |
| `--robot-label-attention [--attention-limit=N]` | Attention-ranked labels by: (pagerank × staleness × block_impact) / velocity |

**History & Change Tracking:**
| Command | Returns |
|---------|---------|
| `--robot-history` | Bead-to-commit correlations: `stats`, `histories` (per-bead events/commits/milestones), `commit_index` |
| `--robot-diff --diff-since <ref>` | Changes since ref: new/closed/modified issues, cycles introduced/resolved |

**Other Commands:**
| Command | Returns |
|---------|---------|
| `--robot-burndown <sprint>` | Sprint burndown, scope changes, at-risk items |
| `--robot-forecast <id\|all>` | ETA predictions with dependency-aware scheduling |
| `--robot-alerts` | Stale issues, blocking cascades, priority mismatches |
| `--robot-suggest` | Hygiene: duplicates, missing deps, label suggestions, cycle breaks |
| `--robot-graph [--graph-format=json\|dot\|mermaid]` | Dependency graph export |
| `--export-graph <file.html>` | Self-contained interactive HTML visualization |

#### Scoping & Filtering

```bash
bv --robot-plan --label backend              # Scope to label's subgraph
bv --robot-insights --as-of HEAD~30          # Historical point-in-time
bv --recipe actionable --robot-plan          # Pre-filter: ready to work (no blockers)
bv --recipe high-impact --robot-triage       # Pre-filter: top PageRank scores
bv --robot-triage --robot-triage-by-track    # Group by parallel work streams
bv --robot-triage --robot-triage-by-label    # Group by domain
```

#### Understanding Robot Output

**All robot JSON includes:**
- `data_hash` — Fingerprint of source beads.jsonl (verify consistency across calls)
- `status` — Per-metric state: `computed|approx|timeout|skipped` + elapsed ms
- `as_of` / `as_of_commit` — Present when using `--as-of`; contains ref and resolved SHA

**Two-phase analysis:**
- **Phase 1 (instant):** degree, topo sort, density — always available immediately
- **Phase 2 (async, 500ms timeout):** PageRank, betweenness, HITS, eigenvector, cycles — check `status` flags

**For large graphs (>500 nodes):** Some metrics may be approximated or skipped. Always check `status`.

#### jq Quick Reference

```bash
bv --robot-triage | jq '.quick_ref'                        # At-a-glance summary
bv --robot-triage | jq '.recommendations[0]'               # Top recommendation
bv --robot-plan | jq '.plan.summary.highest_impact'        # Best unblock target
bv --robot-insights | jq '.status'                         # Check metric readiness
bv --robot-insights | jq '.Cycles'                         # Circular deps (must fix!)
bv --robot-label-health | jq '.results.labels[] | select(.health_level == "critical")'
```

**Performance:** Phase 1 instant, Phase 2 async (500ms timeout). Prefer `--robot-plan` over `--robot-insights` when speed matters. Results cached by data hash.

Use bv instead of parsing beads.jsonl—it computes PageRank, critical paths, cycles, and parallel tracks deterministically.

---

### Robot Command Exit Codes

All `--robot-*` commands follow a consistent exit code convention:

| Exit Code | Meaning | JSON Response | Agent Action |
|-----------|---------|---------------|--------------|
| 0 | Success | `{"success": true, ...}` | Proceed with response data |
| 1 | Error | `{"success": false, "error_code": "...", ...}` | Handle error, maybe retry |
| 2 | Unavailable | `{"success": false, "error_code": "NOT_IMPLEMENTED", ...}` | Skip gracefully, log for awareness |

Example handling:

```python
result = subprocess.run(["ntm", "--robot-tail=myproj"], capture_output=True)
data = json.loads(result.stdout)

if result.returncode == 0:
    # Success - process response
    process_agents(data["panes"])
elif result.returncode == 2:
    # Unavailable - feature not implemented yet
    logging.info(f"Feature {data.get('feature')} not available")
else:  # returncode == 1
    # Error - handle or propagate
    raise RuntimeError(f"{data['error_code']}: {data['error']}")
```

Common error codes: `SESSION_NOT_FOUND`, `PANE_NOT_FOUND`, `INVALID_FLAG`, `TIMEOUT`, `INTERNAL_ERROR`, `NOT_IMPLEMENTED`.

---

### JSON Field Semantics

Robot command outputs follow consistent semantics for absent, null, and empty fields:

**Always Present (Required Fields)**

These fields are ALWAYS present in successful responses:
- `success`: boolean - Whether the operation succeeded
- `timestamp`: RFC3339 string - When the response was generated
- Critical arrays like `sessions`, `panes`, `targets`, `agents` - Always present, empty array `[]` if none found

**Absent Fields**

Fields may be absent from JSON when:
- The field doesn't apply to this response type
- Example: `_agent_hints` absent when no hints are relevant
- Example: `dry_run` absent when not in preview mode

```json
// Normal response - no dry_run field
{"success": true, "targets": ["1", "2"]}

// Dry-run response - dry_run field present
{"success": true, "dry_run": true, "would_send_to": ["1", "2"]}
```

**Empty Arrays vs Absent**

Empty arrays indicate "checked, found nothing" - distinct from "didn't check":

```json
// Checked, found no agents
{"agents": []}

// Checked, found no errors
{"failed": []}
```

Critical arrays are never absent - they're always present even if empty. This allows safe iteration without null checks.

**Optional Fields (omitempty)**

These fields are only present when they have meaningful values:
- `error`, `error_code`, `hint` - Only on error responses
- `variant` - Only if agent has a model variant
- `preset_used` - Only if a preset was used
- `_agent_hints` - Only when hints are available
- `warnings`, `notes` - Only when there are warnings/notes

**Null Fields**

Go doesn't typically emit `null` for missing values. Fields are either present with a value or absent entirely. The only exception is pointer types where the underlying value couldn't be determined.

**Parsing Guidance**

```python
# Safe array iteration (always present)
for agent in data.get("agents", []):
    process(agent)

# Check optional fields
if "_agent_hints" in data:
    hints = data["_agent_hints"]

# Check error state
if not data["success"]:
    code = data.get("error_code", "UNKNOWN")
    msg = data.get("error", "No error message")
```

---

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

### Morph Warp Grep — AI-Powered Code Search

Use `mcp__morph-mcp__warp_grep` for "how does X work?" discovery across the codebase.

When to use:

- You don't know where something lives.
- You want data flow across multiple files (API → service → schema → types).
- You want all touchpoints of a cross-cutting concern (e.g., robot mode, tmux integration).

Example:

```
mcp__morph-mcp__warp_grep(
  repoPath: "/Users/jemanuel/projects/ntm",
  query: "How does robot mode spawn sessions?"
)
```

Warp Grep:

- Expands a natural-language query to multiple search patterns.
- Runs targeted greps, reads code, follows imports, then returns concise snippets with line numbers.
- Reduces token usage by returning only relevant slices, not entire files.

When **not** to use Warp Grep:

- You already know the function/identifier name; use `rg`.
- You know the exact file; just open it.
- You only need a yes/no existence check.

Comparison:

| Scenario | Tool |
| ---------------------------------- | ---------- |
| "How is robot mode implemented?" | warp_grep |
| "Where is `SendKeys` defined?" | `rg` |
| "Replace `var` with `const`" | `ast-grep` |

---

### cass — Cross-Agent Search

`cass` indexes prior agent conversations (Claude Code, Codex, Cursor, Gemini, ChatGPT, etc.) so we can reuse solved problems.

Rules:

- Never run bare `cass` (TUI). Always use `--robot` or `--json`.

Examples:

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

The Cass Memory System (cm) is a tool for giving agents an effective memory based on the ability to quickly search across previous coding agent sessions across an array of different coding agent tools (e.g., Claude Code, Codex, Gemini-CLI, Cursor, etc) and projects (and even across multiple machines, optionally) and then reflect on what they find and learn in new sessions to draw out useful lessons and takeaways; these lessons are then stored and can be queried and retrieved later, much like how human memory works.

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

---

## UBS Quick Reference for AI Agents

UBS stands for "Ultimate Bug Scanner": **The AI Coding Agent's Secret Weapon: Flagging Likely Bugs for Fixing Early On**

**Golden Rule:** `ubs <changed-files>` before every commit. Exit 0 = safe. Exit >0 = fix & re-run.

**Commands:**
```bash
ubs file.go file2.go                        # Specific files (< 1s) — USE THIS
ubs $(git diff --name-only --cached)        # Staged files — before commit
ubs --only=go internal/                     # Language filter (3-5x faster)
ubs --ci --fail-on-warning .                # CI mode — before PR
ubs --help                                  # Full command reference
ubs sessions --entries 1                    # Tail the latest install session log
ubs .                                       # Whole project (ignores things like .venv and node_modules automatically)
```

**Output Format:**
```
⚠️  Category (N errors)
    file.go:42:5 – Issue description
    💡 Suggested fix
Exit code: 1
```
Parse: `file:line:col` → location | 💡 → how to fix | Exit 0/1 → pass/fail

**Fix Workflow:**
1. Read finding → category + fix suggestion
2. Navigate `file:line:col` → view context
3. Verify real issue (not false positive)
4. Fix root cause (not symptom)
5. Re-run `ubs <file>` → exit 0
6. Commit

**Speed Critical:** Scope to changed files. `ubs internal/cli/send.go` (< 1s) vs `ubs .` (30s). Never full scan for small edits.

**Bug Severity:**
- **Critical** (always fix): Nil pointer dereference, race conditions, goroutine leaks, unchecked errors
- **Important** (production): Type narrowing, division-by-zero, resource leaks, unbounded allocations
- **Contextual** (judgment): TODO/FIXME, fmt.Println debug statements

**Anti-Patterns:**
- Do not ignore findings - Investigate each
- Do not full scan per edit - Scope to file
- Do not fix symptom (`if x != nil { x.Y() }`) - Fix root cause (ensure x is never nil at callsite)

---

## ast-grep vs ripgrep

**Use `ast-grep` when structure matters.** It parses code and matches AST nodes, ignoring comments/strings, and can **safely rewrite** code.

- Refactors/codemods: rename APIs, change patterns
- Policy checks: enforce patterns across a repo

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

<!-- bv-agent-instructions-v1 -->

---

## Beads Workflow Integration

This project uses [beads_rust](https://github.com/Dicklesworthstone/beads_rust) for issue tracking. Issues are stored in `.beads/` and tracked in git.

**Note:** `br` (beads_rust) is non-invasive and never executes git commands directly. You must manually run git operations after `br sync --flush-only`.

### Essential Commands

```bash
# CLI commands for agents
br ready              # Show issues ready to work (no blockers)
br list --status=open # All open issues
br show <id>          # Full issue details with dependencies
br create --title="..." --type=task --priority=2
br update <id> --status=in_progress
br close <id> --reason="Completed"
br close <id1> <id2>  # Close multiple issues at once
br sync --flush-only  # Export to JSONL (does NOT run git commands)
```

### Workflow Pattern

1. **Start**: Run `br ready` to find actionable work
2. **Claim**: Use `br update <id> --status=in_progress`
3. **Work**: Implement the task
4. **Complete**: Use `br close <id>`
5. **Sync**: Always run `br sync --flush-only` then manually: `git add .beads/ && git commit -m "Update beads" && git push`

### Key Concepts

- **Dependencies**: Issues can block other issues. `br ready` shows only unblocked work.
- **Priority**: P0=critical, P1=high, P2=medium, P3=low, P4=backlog (use numbers, not words)
- **Types**: task, bug, feature, epic, question, docs

<!-- end-bv-agent-instructions -->

---

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   br sync --flush-only  # Export to JSONL (does NOT run git commands)
   git add .beads/ && git commit -m "Update beads" && git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

---

## Note for Codex/GPT-5.2

You constantly bother me and stop working with concerned questions that look similar to this:

```
Unexpected changes (need guidance)

- Working tree still shows edits I did not make in go.mod, go.sum, internal/cli/send.go, internal/session/manager.go. Please advise whether to keep/commit/revert these before any further work. I did not touch them.

Next steps (pick one)

1. Decide how to handle the unrelated modified files above so we can resume cleanly.
```

NEVER EVER DO THAT AGAIN. The answer is literally ALWAYS the same: those are changes created by the potentially dozen of other agents working on the project at the same time. This is not only a common occurence, it happens multiple times PER MINUTE. The way to deal with it is simple: you NEVER, under ANY CIRCUMSTANCE, stash, revert, overwrite, or otherwise disturb in ANY way the work of other agents. Just treat those changes identically to changes that you yourself made. Just fool yourself into thinking YOU made the changes and simply don't recall it for some reason.
