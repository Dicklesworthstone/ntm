RULE 1 – ABSOLUTE (DO NOT EVER VIOLATE THIS)

You may NOT delete any file or directory unless I explicitly give the exact command **in this session**.

- This includes files you just created (tests, tmp files, scripts, etc.).
- You do not get to decide that something is “safe” to remove.
- If you think something should be removed, stop and ask. You must receive clear written approval **before** any deletion command is even proposed.

Treat “never delete files without permission” as a hard invariant.

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

### Code Editing Discipline

- Do **not** run scripts that bulk-modify code (codemods, invented one-off scripts, giant `sed`/regex refactors).
- Large mechanical changes: break into smaller, explicit edits and review diffs.
- Subtle/complex changes: edit by hand, file-by-file, with careful reasoning.

---

### Backwards Compatibility & File Sprawl

We optimize for a clean architecture now, not backwards compatibility.

- No “compat shims” or “v2” file clones.
- When changing behavior, migrate callers and remove old code **inside the same file**.
- New files are only for genuinely new domains that don’t fit existing modules.
- The bar for adding files is very high.

---

### Logging & Console Output

- Prefer a shared logger (e.g., `lib/logger.ts` on `pino`/`consola`) over raw `console.log`.
- No random console logs in UI components; if needed, make them dev-only and clean them up.
- Log structured context: IDs, user, request, model, etc.
- If a logger helper exists, you must use it; do not invent a different pattern.

---

### Third-Party Libraries

When unsure of an API, look up current docs (late-2025) rather than guessing.

---

## MCP Agent Mail — Multi-Agent Coordination

Agent Mail is already available as an MCP server; do not treat it as a CLI you must shell out to.

What it gives:

- Identities, inbox/outbox, searchable threads.
- Advisory file reservations (leases) to avoid agents clobbering each other.
- Persistent artifacts in git (human-auditable).

Core patterns:

1. **Same repo**
   - Register identity:
     - `ensure_project` then `register_agent` with the repo’s absolute path as `project_key`.
   - Reserve files before editing:
     - `file_reservation_paths(project_key, agent_name, ["src/**"], ttl_seconds=3600, exclusive=true)`.
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
- Search: `mcp-agent-mail products search MyProduct "bd-123 OR \"release plan\"" --limit 50`.
- Product inbox: `mcp-agent-mail products inbox MyProduct YourAgent --limit 50 --urgent-only --include-bodies`.
- Summaries: `mcp-agent-mail products summarize-thread MyProduct "bd-123" --per-thread-limit 100 --no-llm`.

Server-side tools (for orchestrators) include:

- `ensure_product(product_key|name)`
- `products_link(product_key, project_key)`
- `resource://product/{key}`
- `search_messages_product(product_key, query, limit=20)`

Common pitfalls:

- “from_agent not registered” → call `register_agent` with correct `project_key`.
- `FILE_RESERVATION_CONFLICT` → adjust patterns, wait for expiry, or use non-exclusive reservation.
- Auth issues with JWT+JWKS → bearer token with `kid` matching server JWKS; static bearer only when JWT disabled.

---

## Issue Tracking with bd (beads)

All issue tracking goes through **bd**. No other TODO systems.

Key invariants:

- `.beads/` is authoritative state and **must always be committed** with code changes.
- Do not edit `.beads/*.jsonl` directly; only via `bd`.

### Basics

Check ready work:

```bash
bd ready --json
```

Create issues:

```bash
bd create "Issue title" -t bug|feature|task -p 0-4 --json
bd create "Issue title" -p 1 --deps discovered-from:bd-123 --json
```

Update:

```bash
bd update bd-42 --status in_progress --json
bd update bd-42 --priority 1 --json
```

Complete:

```bash
bd close bd-42 --reason "Completed" --json
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

1. `bd ready` to find unblocked work.
2. Claim: `bd update <id> --status in_progress`.
3. Implement + test.
4. If you discover new work, create a new bead with `discovered-from:<parent-id>`.
5. Close when done.
6. Commit `.beads/` in the same commit as code changes.

Auto-sync:

- bd exports to `.beads/issues.jsonl` after changes (debounced).
- It imports from JSONL when newer (e.g. after `git pull`).

Never:

- Use markdown TODO lists.
- Use other trackers.
- Duplicate tracking.

---

### Using bv as an AI Sidecar

`bv` is a terminal UI + analysis layer for `.beads/beads.jsonl`. It precomputes graph metrics so you don’t have to.

Useful robot commands:

- `bv --robot-help` – overview
- `bv --robot-insights` – graph metrics (PageRank, betweenness, HITS, critical path, cycles)
- `bv --robot-plan` – parallelizable execution plan with unblocks info
- `bv --robot-priority` – priority suggestions with reasoning
- `bv --robot-recipes` – list recipes; apply via `bv --recipe <name>`
- `bv --robot-diff --diff-since <commit|date>` – JSON diff of issue changes

Use `bv` instead of rolling your own dependency graph logic.

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

Use `mcp__morph-mcp__warp_grep` for “how does X work?” discovery across the codebase.

When to use:

- You don’t know where something lives.
- You want data flow across multiple files (API → service → schema → types).
- You want all touchpoints of a cross-cutting concern (e.g., moderation, billing).

Example:

```
mcp__morph-mcp__warp_grep(
  repoPath: "/data/projects/communitai",
  query: "How is the L3 Guardian appeals system implemented?"
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
| “How is auth session validated?” | warp_grep |
| “Where is `handleSubmit` defined?” | `rg` |
| “Replace `var` with `let`” | `ast-grep` |

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

## Contribution Policy

Remove any mention of contributing/contributors from README and don't reinsert it.

---

## UBS Quick Reference for AI Agents

UBS stands for "Ultimate Bug Scanner": **The AI Coding Agent's Secret Weapon: Flagging Likely Bugs for Fixing Early On**

**Install:** `curl -sSL https://raw.githubusercontent.com/Dicklesworthstone/ultimate_bug_scanner/main/install.sh | bash`

**Golden Rule:** `ubs <changed-files>` before every commit. Exit 0 = safe. Exit >0 = fix & re-run.

**Commands:**
```bash
ubs file.ts file2.py                    # Specific files (< 1s) — USE THIS
ubs $(git diff --name-only --cached)    # Staged files — before commit
ubs --only=js,python src/               # Language filter (3-5x faster)
ubs --ci --fail-on-warning .            # CI mode — before PR
ubs --help                              # Full command reference
ubs sessions --entries 1                # Tail the latest install session log
ubs .                                   # Whole project (ignores things like .venv and node_modules automatically)
```

**Output Format:**
```
⚠️  Category (N errors)
    file.ts:42:5 – Issue description
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

**Speed Critical:** Scope to changed files. `ubs src/file.ts` (< 1s) vs `ubs .` (30s). Never full scan for small edits.

**Bug Severity:**
- **Critical** (always fix): Null safety, XSS/injection, async/await, memory leaks
- **Important** (production): Type narrowing, division-by-zero, resource leaks
- **Contextual** (judgment): TODO/FIXME, console logs

**Anti-Patterns:**
- ❌ Ignore findings → ✅ Investigate each
- ❌ Full scan per edit → ✅ Scope to file
- ❌ Fix symptom (`if (x) { x.y }`) → ✅ Root cause (`x?.y`)