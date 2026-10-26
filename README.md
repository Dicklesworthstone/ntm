# NTM - Named Tmux Manager

<div align="center">
  <img src="ntm_dashboard.webp" alt="NTM dashboard">
</div>

<div align="center">

![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS-blue.svg)
![Go Version](https://img.shields.io/badge/go-1.26.9+-00ADD8.svg)
![License](https://img.shields.io/badge/License-MIT%2BOpenAI%2FAnthropic%20Rider-blue.svg)
![Release](https://img.shields.io/github/v/release/Dicklesworthstone/ntm?include_prereleases)

</div>

NTM turns `tmux` into a local control plane for multi-agent software development.
It combines session orchestration, graph-aware work triage, safety policy and approvals,
Agent Mail coordination, durable state capture, machine-readable robot surfaces, and a
local REST/WebSocket API in one Go binary.

<div align="center">

```bash
curl -fsSL "https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh?$(date +%s)" | bash -s -- --easy-mode
```

</div>

## TL;DR

### The Problem

Running several coding agents in parallel is easy to start and annoying to sustain.
Plain `tmux` gives you panes, but it does not give you durable coordination, work
selection, safety policy, approvals, history, replayable automation surfaces, or a
shared control model that both humans and agents can use.

### The Solution

NTM gives you a single local system for:

- spawning labeled multi-agent sessions in `tmux`
- sending work, interrupts, and follow-ups across panes
- triaging what to do next with `br` and `bv`
- coordinating agents with Agent Mail, file reservations, and assignments
- protecting dangerous operations with policy, approvals, and guards
- exposing the whole system through `--robot-*`, REST, SSE, WebSocket, and OpenAPI
- capturing state with checkpoints, timelines, audit trails, and pipeline state

### Why NTM

| Area | What NTM provides | Typical commands |
| --- | --- | --- |
| Session orchestration | Spawn, label, inspect, zoom, dashboard, palette | `ntm spawn`, `ntm dashboard`, `ntm palette` |
| Work intelligence | Graph-aware triage, next-step selection, impact analysis, assignment | `ntm work triage`, `ntm work next`, `ntm assign` |
| Coordination | Human overseer mail, inbox views, file reservations, worktrees | `ntm mail`, `ntm locks`, `ntm worktrees` |
| Safety | Destructive-command protection, policy editing, approval workflows | `ntm safety`, `ntm policy`, `ntm approve`, `ntm guards` |
| Durable operations | Checkpoints, timelines, audit logs, saved sessions, pipelines | `ntm checkpoint`, `ntm timeline`, `ntm audit`, `ntm pipeline` |
| Automation surfaces | Robot JSON, REST API, SSE/WebSocket streams, OpenAPI | `ntm --robot-snapshot`, `ntm serve`, `ntm openapi generate` |

## Quick Start

### Requirements

NTM is a pure Go project, but the runtime experience is intentionally integration-heavy.

- Required: `tmux`
- Required for agent spawning: whichever CLIs you want to run, typically Claude Code, Codex, Antigravity CLI, Oh My Pi (`omp`), or Grok Build (Gemini CLI is supported as legacy)
- Optional but powerful: `br`, `bv`, Agent Mail, `cass`, `dcg`, `pt`
- Sanity check everything with `ntm deps -v`

### First Session

```bash
# Install
curl -fsSL "https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh?$(date +%s)" | bash -s -- --easy-mode

# Enable shell integration
eval "$(ntm shell zsh)"

# Verify tools and integrations
ntm deps -v

# Scaffold a project directory
ntm quick api --template=go

# Launch a mixed swarm
ntm spawn api --cc=2 --cod=1 --agy=1

# Open the live operator surfaces
ntm dashboard api
ntm palette api

# Dispatch work
ntm send api --cc "Map the auth layer and propose a refactor plan."

# If the repo uses br/bv, inspect the work graph
ntm work triage --format=markdown

# Save a recoverable checkpoint
ntm checkpoint save api -m "before auth refactor"

# Expose local APIs for dashboards, scripts, and agents
ntm serve --port 7337
ntm --robot-snapshot
```

## Core Workflows

### 1. Multi-Agent Session Orchestration

NTM is built around named `tmux` sessions with explicit agent panes and a user pane.
It handles session naming, pane layout, agent startup, labels, and inspection so you
can treat a swarm like a manageable unit instead of a pile of terminals.

```bash
ntm quick payments --template=go
ntm spawn payments --cc=3 --cod=2 --agy=1
ntm add payments --cc=1
ntm list
ntm status payments
ntm view payments
ntm zoom payments 3
ntm attach payments
```

A bare `--cc=N` passes no `--model`, so Claude Code starts with its own default
or saved model and context setting. Pin one with `--cc=N:MODEL` (the built-in
aliases `opus`, `sonnet`, and `haiku` pass Claude Code's own family aliases,
which track the newest model) or set `[models] default_claude` in
`~/.config/ntm/config.toml`; `default_claude = ""` restores the delegate
behaviour.

#### Grok Build

NTM recognizes the official xAI Grok Build CLI as the canonical `grok` agent
type. Install it using [xAI's current instructions](https://docs.x.ai/build/overview),
then authenticate without putting credentials in NTM configuration:

```bash
curl -fsSL https://x.ai/cli/install.sh | bash
grok login                   # browser authentication
grok login --device-auth     # SSH/headless alternative
ntm deps -v
```

A bare spec delegates model selection to Grok Build. Use `grok models` to inspect
the models available to the authenticated account, then pass an exact model ID
only when an override is needed:

```bash
ntm spawn research --grok=1
ntm spawn research --grok=1:MODEL_ID
ntm spawn research --grok=1:MODEL_ID:EFFORT
ntm --robot-spawn=research --spawn-grok=1
```

NTM launches Grok Build with its official `--always-approve` automation flag.
Phase 2 (GH#251) implements the interactive TUI protocol against live Grok Build
captures: readiness and working-state detection, composer-gated and
submission-verified prompt delivery (send, assign, `--robot-spawn --spawn-wait`
and `--spawn-assign-work`), interrupt, restart, and restore-time relaunch. The
one deliberate refusal is personas: the Grok Build CLI has no system-prompt
flag or environment variable, so a persona on a Grok agent fails the launch
instead of being silently dropped.

#### Oh My Pi (`omp`)

NTM operates [Oh My Pi](https://omp.sh) as the built-in `omp` agent type (panes
are named `session__omp_N`; `oh-my-pi` is accepted as an alias). Finish omp's
own setup once before the first spawn, otherwise the first prompt lands in the
setup wizard:

```bash
omp setup
ntm deps -v                               # probes the omp binary
ntm spawn swarm --omp=8                   # omp picks its default model
ntm spawn swarm --omp=2:MODEL             # pins MODEL (omp fuzzy-matches it)
ntm spawn swarm --omp=2:MODEL:high        # also sets the thinking level (or MODEL@high)
ntm add swarm --omp=1
ntm adopt swarm --omp=1,2,3               # adopt existing omp panes
ntm --robot-spawn=swarm --spawn-omp=8 --spawn-wait
ntm send swarm --omp "Run the test suite and report failures"
```

The launch command is `omp --auto-approve`, plus `--model`, `--thinking`, and
`--append-system-prompt <file>` (personas) when requested. Override it with
`[agents] omp = "..."` (a template; keep `{{.ReasoningEffort}}` if you want
effort specs honoured, otherwise NTM rejects them rather than dropping them),
set `[models] default_omp` or `[models.omp]` aliases, and `[prompts]
omp_default`. `[spawn_pacing.agent_caps] omp_max_concurrent` (default 8) is
omp's share of the host-wide agent budget robot spawn admission enforces
(running plus requested agents may not exceed the sum of all agent caps);
launches are never serialized per type, and the default admits a full
`--spawn-omp=8` swarm alongside the default Claude/Codex/Gemini budget.

Everything NTM does to an omp pane is derived from omp's live TUI (v18.2.3,
across its nerd, unicode and ascii symbol presets):

- **Ready / working / idle.** omp keeps a bordered composer at the bottom of the
  pane. A turn in flight shows a spinner and elapsed timer in the composer's top
  border (`╭── ⠧ 1s`), an Esc-hint activity line above it (`⎋ Working…` or an
  intent label), a queued `Steering · N` block, or a ` Subagents` panel listing
  running subagents. A ` TODO` tree alone is not read as working, since a plan
  can outlive its turn. A quiet composer with nothing below it is idle.
- **Delivery.** Enter submits. A message sent to a busy pane is queued by omp
  as steering; delivery is confirmed when the payload leaves the composer. A
  staged large paste (`#1` token) is reported by `--robot-dialogs` as paste
  limbo and cleared with Ctrl+C.
- **Interrupt and exit.** omp interrupts on **Escape** (Ctrl+C only clears the
  draft, and a double Ctrl+C quits), so `ntm interrupt`, `--robot-interrupt`,
  restart, and the REST interrupt send Escape to omp panes. Graceful exit is
  Escape, Ctrl+C, then Ctrl+D.
- **Context.** `--robot-context`, `--robot-snapshot`, `ntm status`, and the
  coordinator's context-rotation trigger read omp's own context gauge from the
  composer border (`7%` … `262K`), reported as `source: "status_bar"`, so the
  reading stays per-pane even when many omp panes share one directory. Compaction
  uses native `/compact` and verifies a fresh reduction in the context gauge.
- **Sessions.** `ntm sessions save` binds each pane to its omp session (omp
  holds its transcript under `~/.omp/agent/sessions/` open, so the binding is
  exact per pane; profile, XDG and `PI_CODING_AGENT_*` stores are honoured), and
  resume relaunches with `omp --auto-approve --resume <id>`.
- **Errors.** A failed turn leaves a provider-error block framed by rules
  directly above the quiet composer (`server_error: ERROR`, `401 Invalid API
  Key`, then `Dismissed when you send your next message.`). NTM reports it as
  an error, never as idle-after-completion: `--robot-is-working` returns
  `ERROR_STATE` with `indicator_basis: "provider_error"` and a retry hint, the
  status observation and coordinator report an error (`auth` for 401/403,
  otherwise generic), and assignment will not feed the pane new work. An
  `F5 to Retry` hint may sit between the block and the input box. Press `F5`
  (omp's retry key) to re-run the failed turn, or send any message, such as a
  continue prompt, which dismisses the block and retries.
  `Error: No model selected.` is an error too. No omp rate-limit screen has
  been captured yet, so omp panes are never marked rate-limited from
  transcript text; a provider 429 surfaces as a provider error.
- **Quota.** omp has no quota or usage command. `ntm quota` lists omp panes as
  "no quota API (provider-error classification)", `ntm rotate all-limited`
  skips them with that reason, and `--robot-quota-check --provider=omp` answers
  `NOT_IMPLEMENTED` (exit 2) naming the same fallback.
- **Palette, assignment, boot checks.** In `ntm palette`, key `7` targets omp
  panes (`6` still picks individual agents). `--assign` scores omp with the
  per-task midpoint of the Claude and Codex capability profiles, since omp's
  strengths follow whichever model it runs. `--verify-boot` counts a pane as
  booted when it is idle, or when it received a spawn prompt (recovery context,
  CASS context, or your prompt) and is working on it.

#### OpenCode (`oc`)

[OpenCode](https://opencode.ai) panes are named `session__oc_N`. The model is
OpenCode's `provider/model` form; with no model, OpenCode uses its own default.

```bash
ntm spawn swarm --oc=2                          # OpenCode picks its default model
ntm spawn swarm --oc=2:opencode/big-pickle      # pins the model
ntm add swarm --oc=1
ntm --robot-spawn=swarm --spawn-oc=2:opencode/big-pickle --spawn-wait
ntm send swarm --oc "Run the test suite and report failures"
```

The launch command is `opencode --model <model>`. There is no effort knob: the
TUI has no `--variant` flag (only `opencode run` does), so an effort in the
spec is ignored. Override the command with `[agents] oc = "..."` (a template;
keep `{{if .Model}} --model {{shellQuote .Model}}{{end}}` if you want model
specs honoured), and set `[models] default_opencode` or `[models.opencode]`
aliases. Prompts are delivered with bracketed paste and verified against the
composer: a prompt the TUI drops fails the send instead of reporting success.

**Unattended panes and permissions.** OpenCode asks before actions its
permission config marks `ask` (by default this includes paths outside the
project, `external_directory`). With nobody at the pane, the request is
auto-rejected and the turn stops. Two ways to pre-approve:

- Launch with `--auto` (OpenCode 1.18.0+), which approves every request that
  is not explicitly denied, for that pane only:

  ```toml
  [agents]
  oc = "opencode --auto{{if .Model}} --model {{shellQuote .Model}}{{end}}"
  ```

  `deny` rules still apply, so you can pair `--auto` with deny rules for
  anything a pane must never do.
- Set OpenCode's own `"permission"` config. It is global for every OpenCode
  session that reads it, not per pane: `~/.config/opencode/opencode.json`, a
  project `opencode.json`, or the `OPENCODE_CONFIG` /
  `OPENCODE_CONFIG_CONTENT` environment variables. Each key takes `allow`,
  `ask` or `deny`, and `"permission": "allow"` sets them all at once:

  ```json
  {
    "$schema": "https://opencode.ai/config.json",
    "permission": {
      "edit": "allow",
      "bash": "allow",
      "external_directory": { "~/shared/fixtures/**": "allow" }
    }
  }
  ```

  Path patterns support `*`, `?` and a leading `~`. Use `**` to cover a
  directory tree: OpenCode may check the parent directory's glob rather than
  the exact path it touches, so a pattern for one leaf directory can still be
  rejected.

Use labels when you want multiple coordinated swarms on the same project while
keeping a shared project directory:

```bash
ntm quick payments --template=go
ntm spawn payments --label backend --cc=2 --cod=1
ntm spawn payments --label frontend --cc=2
ntm add payments --label frontend --cc=1
```

#### Staggered prompts (thundering-herd prevention)

When several agents boot together and all pick work from `bv`/`br` at once,
they race for the same bead. Stagger their initial prompts so each agent has
claimed something before the next one starts looking:

```bash
ntm spawn payments --cc=3 --prompt="Read AGENTS.md, then pick ready work" --stagger-mode=fixed --stagger-delay=90s
ntm spawn payments --cc=5 --prompt="..." --stagger-mode=smart   # learned rate-limit spacing
ntm --robot-spawn=payments --spawn-cc=3 --spawn-prompt="Read AGENTS.md, then pick ready work" \
    --spawn-stagger-mode=fixed --spawn-stagger-delay=90s
```

The agent at position `i` receives its prompt `i × interval` after the first.
`--robot-spawn --spawn-prompt` (or `--spawn-prompt-file`, `-` for stdin)
waits for readiness, delivers through robot send's dispatch path, and reports
`stagger.schedule[]` and per-agent `prompt_deliveries[]`; the same
`--spawn-stagger-*` flags pace `--spawn-assign-work` prompts. Set defaults for
both commands in `~/.config/ntm/config.toml` (flags still win):

```toml
[spawn]
stagger_mode = "fixed"   # none (default), fixed, or smart
stagger_delay = "90s"    # fixed-mode interval, 0-5m (default 30s)
```

#### Worktree isolation and reservations

Use `--worktrees` when agents need independent Git checkouts as well as separate
tmux panes. NTM creates one `ntm/<session>/<agent>` branch and worktree per
agent, so filesystem changes and destructive Git operations stay isolated until
you deliberately merge them:

```bash
ntm spawn payments --cc=3 --worktrees
ntm worktrees list
ntm worktrees merge cc_1
ntm worktrees clean --session payments
```

Worktrees do not replace Agent Mail reservations. Continue to reserve the files
or areas an agent owns: reservations communicate intent, reduce merge conflicts,
and give operators an auditable ownership record; worktrees provide the separate
checkout boundary. Merge reviewed agent branches into `main`, then clean the
session's worktrees when that session is finished.

Reservations are enforced at commit time: the pre-commit hook `ntm init`
installs runs `ntm guards check --staged`, which refuses a commit touching files
another agent holds an exclusive reservation on and names the holder (when Agent
Mail is unreachable it warns and records a degraded run for `ntm doctor`; set
`NTM_GUARD_STRICT=1` to fail closed). `ntm guards install` adds the check to a
repository without that hook, or updates an older `ntm init` hook in place.

### 2. Dispatch, Monitoring, and Recovery

Humans can broadcast prompts, interrupt panes, stream output, inspect health, compare
responses, search pane history, and keep an eye on activity without dropping to raw
`tmux` commands.

```bash
ntm send payments --all "Checkpoint and summarize current progress."
ntm interrupt payments
ntm activity payments --watch
ntm health payments
ntm watch payments --cc
ntm extract payments --lang=go
ntm diff payments cc_1 cod_1
ntm grep "timeout" payments -C 3
ntm analytics --days 7
```

Pending context rotations can be acted on directly:

```bash
ntm rotate context pending
ntm rotate context confirm payments__cc_1 --action=rotate
ntm rotate context confirm payments__cc_1 --action=compact
```

CLI and dashboard confirmation execute the selected action and report its actual
result. Rotation verifies the original process, requests a fresh handoff, and
delivers it to a ready replacement before retiring the old pane. Confirmation
ownership and results survive process interruption: repeating a completed choice
returns its saved result, while a failed or interrupted choice remains visible in
`pending`. The pending output identifies the execution, both physical panes, the
handoff phase, and the available recovery actions. Inspect the affected panes
before using the indicated retry or recovery action.
Rotation transfers the existing task, Agent Mail identity, file reservations, and
restart configuration to the replacement. Both panes are fenced against new task
dispatch and automatic restarts while ownership is being transferred; existing
reservation leases continue to renew. If an accepted handoff is interrupted during
ownership transfer, `--retry` finishes that transfer without launching another
agent or resending the handoff. When handoff delivery is uncertain, the pending
record and pane fences remain in place for inspection, and retry or ignore cannot
silently discard that unresolved state.

Resolve an interrupted rotation with an explicit operator decision:

```bash
ntm rotate context pending --json
ntm rotate context recover payments__cc_1 --execution-id=EXECUTION_ID \
  --action=accept --reason="Replacement is working on the handed-off task"
# Alternatively, stop the replacement and keep the original agent:
ntm rotate context recover payments__cc_1 --execution-id=EXECUTION_ID \
  --action=abort --reason="Continue the task in the original pane"
```

`accept` records the operator's observation of an uncertain handoff and finishes
the existing ownership transfer without spawning or sending again. `abort`
verifies the original ownership, stops only the recorded replacement, and verifies
its absence before releasing the original task's rotation barrier. Work already
performed by the replacement is preserved. An accepted ownership transfer cannot
be aborted. Both actions require the exact execution ID and a nonempty reason;
the decision is recorded durably, conflicting decisions are rejected, and the
same command can resume recovery after another interruption. Changed process
identities, foreign ownership, and unaccounted-for panes keep the recovery fenced
for further inspection.

Native compaction preserves the conversation and requires fresh provider context
accounting to prove a reduction; it does not automatically clear history. Claude
compaction reads the transcript the pane's own Claude process names in its
session record, so it works when many Claude panes share one project directory.
Without that binding it falls back to the directory's newest transcript and
refuses ambiguous scope, including another Claude pane in a different session
using the same transcript directory.

### 3. Work Graph Triage and Assignment

NTM integrates with `br` and `bv` so the operator loop is not just "send prompts and
hope." It can surface the best next task, highlight blockers, analyze impact, forecast
work, and push assignments to specific panes or agent types.

```bash
ntm work triage
ntm work triage --by-track
ntm work alerts
ntm work search "JWT auth"
ntm work impact internal/api/auth.go
ntm work next
ntm work graph
ntm assign payments --auto --strategy=dependency
ntm assign payments --beads=br-123,br-124 --agent=codex
```

Automated assignment treats tracker labels as an authorization boundary. Add
organization-specific approval labels globally or in `.ntm/config.toml`; project
labels extend the global list and cannot remove built-in gates:

```toml
[assign]
operator_gated_labels = ["security-review", "legal-approval"]
```

Matching is case-insensitive. NTM requires a structurally valid full actionable
plan, uses scored triage only to rank IDs authorized by that plan, and restores
every candidate's labels from both `br ready` and `br list --status open` so
epics are covered. Any plan command, parse, structure, ID, label lookup, or
coverage failure stops automated assignment before dispatch.

Executing assignment paths can recover a corrupt local Beads SQLite index
before reading the evidence needed to claim work. This includes automatic and
watch assignment, coordinator assignment, and robot assignment preparation.
NTM retries the failed read once after rebuilding from authoritative JSONL. If
`br` refuses that rebuild because corruption prevents tombstone enumeration,
NTM independently checks that the tombstone indexes agree and JSONL preserves
every known tombstone before running `br doctor --repair --json`. It requires a
verified database, validates the structured preservation evidence, and checks
the known tombstones again after repair. An accepted partial repair retains
its warnings, verified backups, and recovery marker. Unreadable indexes,
missing tombstones, invalid JSONL, or failed verification stop assignment.
Preview and dry-run commands do not request this repair. Recovery never replays
a claim, reservation, or dispatch, and the normal eligibility and ownership
checks still run against freshly read evidence.

When `br` and `bv` report that no ready work exists, use the queue-dry flow to
distinguish a genuinely empty queue from stale coordination state:

```bash
# Confirm the work queue first. Do not run bare bv; use robot output.
br ready --json
bv --robot-triage | jq '.triage.quick_ref'

# Diagnose why the queue appears dry.
ntm work queue-dry --format=json | jq '{queue_dry, evidence, recommendations}'

# Render an advisory roadmap only after the dry queue is confirmed.
ntm work queue-dry --ideate --format=json | jq '{
  queue_dry,
  ideation: {
    status: .ideation.status,
    guard: .ideation.guard.recommendation,
    rendered: .ideation.roadmap.rendered_count,
    preview: .ideation.creation.remaining_commands
  },
  warnings
}'

# The same plan is available as markdown for human review.
ntm work queue-dry --ideate --format=markdown
```

Review the duplicate and novelty evidence before creating anything. If `br ready`
has work, or `bv --robot-triage` shows actionable recommendations, claim that work
instead of ideating. `--force` is only for an explicit preview when an operator wants
to inspect the plan despite ready work or degraded tracker state.

Gated creation is opt-in and still uses Beads as the source of truth:

```bash
# Re-check the preview and guard before mutating Beads.
ntm work queue-dry --ideate --format=json | jq '.ideation.creation.remaining_commands'

# Create proposed beads only after review. The plan version is an audit token.
ntm work queue-dry --ideate --create-beads --yes --plan-version="$(git rev-parse --short HEAD)"

# Validate the graph and export Beads state after any mutation.
br dep cycles --json
bv --robot-triage | jq '.triage.quick_ref'
br sync --flush-only
git add .beads/issues.jsonl
```

If Agent Mail, CASS, or CM are unavailable, `queue-dry --ideate` keeps running and
marks those sources as degraded in `warnings`. Treat degraded Agent Mail reservation
visibility as a coordination stop sign for mutating creation; fix coordination or use
the non-mutating preview. Never edit `.beads/*.jsonl` directly, and use
`ntm work queue-dry --help` for the current flag surface.

### 4. Coordination, Reservations, and Human Oversight

NTM exposes Agent Mail and reservation workflows directly from the CLI. You can act as
Human Overseer, inspect inbox state, review reservations, renew or force-release stale
locks, and coordinate work without inventing an ad hoc protocol.

```bash
ntm mail send payments --all "Sync to main and report blockers."
ntm mail inbox payments
ntm locks list payments --all-agents
ntm locks renew payments
ntm locks force-release payments 42 --note "agent inactive"
ntm coordinator status payments
ntm coordinator digest payments
ntm coordinator conflicts payments
ntm coordinator enable auto-assign
ntm coordinator enable digest --interval=30m
ntm coordinator disable conflict-negotiate
```

`ntm locks force-release` is approval-gated by default (`automation.force_release:
approval`): the first invocation files a durable approval request, a second operator
grants it with `ntm approve <id>` (self-approval is rejected), and re-running the
command consumes that approval and executes exactly once. Use
`ntm policy automation --force-release auto` to allow unattended force-release, or
`never` to disable it entirely; the serve HTTP endpoint and the dashboard conflict
action honor the same policy setting.

#### Pane identity badges

In a mixed session, pane titles name the harness (`payments__cc_1`) while
coordination happens under Agent Mail names (`BlueLake`). NTM can show each managed
pane's assigned name in its tmux pane border next to the existing title:

```toml
[agent_mail]
enabled = true
pane_badges = true                        # default false (opt-in)
# pane_badge_format = "[{name}{drift}]{lifecycle}"
```

The badge is NTM's assigned identity (the session agent registry, keyed by stable
pane id), never whatever a later process wrote to the pane identity file. At every
reconciliation NTM compares the assignment with the canonical Agent Mail identity
file (legacy plain-text name or the structured record Agent Mail >= 0.3.31 writes)
and caches the outcome in per-pane tmux user options
(`@ntm_agent_mail_name`, `@ntm_agent_mail_state`, `@ntm_agent_mail_lifecycle`,
`@ntm_agent_mail_label`); the window's `pane-border-format` gains a fragment that
renders only the cached label, so tmux never calls Agent Mail while drawing.

| Border | Meaning |
|---|---|
| `[BlueLake]` | current assignment; names agree (binding verified, or a legacy file that cannot be verified) |
| `[BlueLake!]` | current assignment; the identity file disagrees (`name-disagreement`), is `missing-file` / `unreadable-file` / `invalid-file`, or its binding is `binding-unverifiable` / `binding-stale` — the assigned name is retained |
| `[?!]` | no current assignment: `assignment-stale` (pane generation changed), `assignment-unobservable` (tmux unreadable) or `assignment-unregistered` |
| `… (starting)`, `… (exited)`, `… (unknown)` | lifecycle observed at the last reconciliation; running agents carry no marker |

Badges are written alongside identity assignment before the agent launches
(`(starting)`) and reconciled on `ntm spawn`, `ntm add`, `ntm adopt`, restart/respawn
and on explicit refresh with `ntm mapping --session <session>`, which also prints the
discrepancy report (assigned vs resolved name, source path, assignment/observation
state, `last_attempt_at` / `last_success_at`) and carries the same data under
`identity` and `discrepancies` in `--json`. They describe the last reconciliation, not
continuous liveness. Badge failures warn and never block a launch or change an
assignment.

Set `pane_badges = false` and run `ntm mapping --session <session>` to withdraw the
pane options and restore the window's previous `pane-border-format` (including
inheritance). Opt a single session out with
`tmux set-option -t =<session>: @ntm_agent_mail_badges off`. A `pane-border-format`
you wrote yourself that references `@ntm_agent_mail_*` is left untouched, and windows
linked into several sessions are skipped with a diagnostic.

Every session started by `ntm spawn` runs the coordinator inside its session
monitor; `ntm coordinator status` shows it under Runtime (`runtime` in
`--json`). The monitor always maintains assignment leases: it renews the file
reservations of in-progress assignments, including those taken by a one-shot
`ntm assign`, before their one-hour leases lapse, and releases them when the
assigned bead closes. Every other coordinator action (`auto-assign`, `digest`,
`conflict-negotiate`, `mail-nudge`) is off until enabled. `conflict-notify`
never runs in the session monitor, only under `ntm coordinator run`: older
`ntm config init` files set it to true, and it mails every holder of
overlapping reservations in the project (`ntm coordinator status` notes when it
is set). A foreground `ntm coordinator run` or a reservation-maintaining
`ntm assign --watch` pauses the monitor's coordinator while it runs, so a
session is never coordinated twice.

`coordinator enable` and `disable` persist the selected `--config` file, or the
global config by default, without replacing unrelated settings or comments.
Session monitors apply a toggle within 15 seconds; restart an already running
foreground `ntm coordinator run` to apply it there.
The selected file may use a `[coordinator]` table or root dotted assignments
such as `coordinator.auto_assign = false`. A whole-section inline assignment
such as `coordinator = { auto_assign = false }` is rejected without changing
the file; convert it to either supported form before toggling a feature.

`ntm assign <session> --watch --reserve-files` maintains the saved file
reservations for active assignments immediately on startup and every 30 seconds,
including while scans or completion handlers are busy. Each maintenance pass is
bounded to 30 seconds. The watcher renews the exact recorded leases only after
verifying the current task claim and pane owner, and releases recorded leases
when the task is confirmed terminal. It does not recreate expired or missing
leases; failures remain visible, including under `--quiet`, and are retried.

The monitor-hosted coordinator and `ntm coordinator run` use the same
maintenance path before admitting new work.
If one pane cannot be observed, independently verified healthy assignments can
still renew and confirmed terminal work can still be cleaned up. Unverified
assignments remain in the ledger, and a degraded coordinator cycle reports an
error without dispatching new work. Watch mode checks the current saved ledger
before exiting with `--stop-when-done`.

The coordinator also recovers a delivered assignment when its original physical
pane disappears. It verifies absence across the entire tmux server, rereads agent
registries to rule out a moved or rebound owner, and checks the exact current
Beads claim and recorded reservations. Only then does it persist task failure,
release that assignment's leases, and reopen its claim for ordinary reassignment.
Incomplete observations, a remaining pane, changed ownership, or failed cleanup
keep the assignment protected. Interrupted cleanup resumes from its durable
record; the previous prompt remains recorded as sent and is not replayed.

### 5. Safety Policy and Approvals

NTM includes a first-class safety system for destructive or sensitive actions. Policy
rules define what is allowed, blocked, or approval-gated. Approvals are durable, auditable,
and support SLB-style two-person workflows for high-risk operations.

```bash
ntm safety status
ntm safety check -- git reset --hard
ntm safety blocked --hours 24
ntm safety install

ntm policy show --all
ntm policy validate
ntm policy edit
ntm policy automation

ntm approve list
ntm approve show abc123
ntm approve abc123
ntm approve deny abc123 --reason "wrong target branch"
```

Every Claude agent NTM launches (spawn, add, robot spawn, restart, restore, rotation,
swarm) gets a Claude Code `PreToolUse` hook through `--settings` that checks each Bash
command against this policy and refuses blocked ones (`[safety] claude_policy_hook`,
default on). Claude Code runs `PreToolUse` hooks even under
`--dangerously-skip-permissions`, and runs only hooks a settings file or `--settings`
lists, so `ntm safety install` also registers its hook script in
`~/.claude/settings.json` for Claude sessions started outside NTM, and `ntm safety status`
reports the hook as installed only when it is registered. dcg and rch hooks ride on the
same `--settings` when those integrations are enabled and installed. A custom
`[agents] claude` command whose last shell command is not the `claude` invocation (a
pipe, a subshell, a wrapper script) must place the hooks itself with
`--settings {{shellQuote .ClaudeSettings}}`; otherwise NTM launches it without them and
warns at every launch.

With `[integrations.dcg] enabled = true`, command lines in messages bound for non-Claude
agents are checked by dcg before delivery. `ntm send`, `--robot-send` (including `--track`)
and REST sends all refuse a blocked command — robot callers get `DESTRUCTIVE_COMMAND_BLOCKED`
— and each refusal counts toward `ntm metrics` destructive-command incidents. A missing dcg
binary skips the check; a check that errors or is interrupted refuses the send instead.

### 6. Pipelines, Templates, Recipes, and Workflow Assets

NTM supports several layers of reusable automation:

- `recipes`: reusable session presets
- `workflows`: runnable orchestration patterns such as pipeline, ping-pong, and review-gate — `ntm workflow run <name>` executes a template's coordination loop against a live session (`ntm spawn -t <name>` only uses a template's agent counts to size a session)
- `template`: prompt templates and substitutions
- `pipeline`: executable multi-step agent workflows with variables, dependencies, resume, and cleanup
- `session-templates`: higher-level session layouts

```bash
ntm recipes list
ntm recipes show full-stack
ntm workflows list
ntm workflows show red-green
ntm workflow run red-green --var feature="parser rewrite"
ntm workflow run ./my-flow.toml --session payments
ntm template list
ntm template show fix-bug
ntm send payments --cc -t marching_orders --bead bd-123   # rendered per pane: own {{agent_num}}/{{agent_type}}

ntm pipeline run .ntm/pipelines/review.yaml --session payments
ntm pipeline status run-20241230-123456-abcd
ntm pipeline list
ntm pipeline resume run-20241230-123456-abcd --mode=continue
ntm pipeline cleanup --older=7d
```

Pipeline resume preserves completed step outputs. Prompt and template steps also
save their delivery and response evidence: `--mode=continue` resumes observing a
confirmed request on its original tmux host and physical pane without submitting
it again. Partial output survives interruption, and a completed response can be
restored even if the process stopped before saving the enclosing step result.
Recognized composer and footer redraws are excluded from response evidence; an
idle pane showing only the submitted prompt is not counted as completed work.

Unknown submission outcomes, replaced pane processes or hosts, and missing output
boundaries stop recovery with the receipt preserved. If you verify that the
original agent received an uncertain submission, record that observation and
continue without resending:

```bash
# Inspect saved identities and obtain the exact receipt token.
ntm pipeline recover run-20241230-123456-abcd --json

# Accept only the inspected delivery, after checking the original agent.
ntm pipeline recover run-20241230-123456-abcd --step-id review \
  --receipt <token-from-inspection> --accept-delivery \
  --reason "The original agent is working on the submitted review"

# Collect the original response and continue dependent steps.
ntm pipeline resume run-20241230-123456-abcd --mode=continue
```

Recovery runs from the project containing the saved pipeline state. Inspection
shows receipt identities without prompt or transcript contents. Acceptance
requires the original tmux endpoint, physical pane and process, an intact output
boundary, and the frozen workflow. It records the reason and time without sending
input, completing the task, or starting dependent steps. Resume keeps its existing
step-definition checks. A live run owner, stale receipt, or conflicting decision
is refused; repeating the same receipt and reason returns the saved decision,
including after the run completes.

REST clients can perform the same recovery through the server's configured
project. `GET /api/v1/pipelines/{id}/recover` requires `pipelines:read` and returns
the inspectable delivery identities and their `receipt_token` values. To record
an observation, send `POST /api/v1/pipelines/{id}/recover` with
`pipelines:write` permission, `Content-Type: application/json`, and this JSON body:

```json
{
  "step_id": "review",
  "receipt": "<receipt_token from GET>",
  "accept_delivery": true,
  "reason": "The original agent is working on the submitted review"
}
```

Acceptance records the same durable decision as the CLI. Continue with
`POST /api/v1/pipelines/{id}/resume`, or submit a `pipeline_resume` job for
pollable execution. Inspection and acceptance expose receipt metadata without
prompt or transcript contents. Recovery requests cannot select another project
or replace the saved session. Stale receipts, conflicting observations, and a live
run owner produce a conflict response without changing the checkpoint.

Each REST pipeline request retains the project selected when it was admitted.
Changing `project_dir` through the configuration API affects subsequent requests;
it cannot redirect an admitted request's workflow lookup, run ownership,
execution, or cancellation. HTTP idempotency keys use that same project scope,
so retrying a key after switching projects cannot replay the previous project's
success. Clearing the configured project also cannot replay a cached file-based
operation that required it. File-based run, validation, and resume still require
a configured project; recovery additionally requires an accessible absolute
project directory.

Foreground command steps in the main workflow have a separate CLI recovery path
for the case where the command finished but its final checkpoint was lost. `pipeline recover`
includes these command receipts in its `commands` JSON array, with eligibility
and refusal information. Before accepting one, independently verify that the
original command **and all its descendants have finished**, then supply its
observed exit code and complete captured output:

```bash
ntm pipeline recover run-20241230-123456-abcd --json

ntm pipeline recover run-20241230-123456-abcd --step-id migrate \
  --receipt <command-receipt-from-inspection> --accept-command \
  --exit-code 0 --output-file migration-output.txt \
  --reason "Verified the migration and all child processes finished successfully"

ntm pipeline resume run-20241230-123456-abcd --mode=continue
```

Both `--exit-code` and `--output-file` must be supplied explicitly; an empty
regular file records an observed empty output. Use complete UTF-8 stdout for a
step with `output_parse`, or captured combined stdout and stderr otherwise.
Recovery enforces a 16 MiB bound and the frozen workflow's stdout limit.
Inspection and acceptance responses contain identities and output fingerprints,
without command text, environment values, or captured output.

Command acceptance records an immutable observation without starting a process,
parsing output, or executing dependent work. Ordinary resume checks the exact
materialized command definition and applies the existing output parser and
failure policy, including parsed output and named variables used by dependents.
A nonzero exit code does not authorize an automatic rerun. Repeating the same
receipt, reason, exit code, and exact output returns the saved decision, including
after completion; a conflicting observation is refused. A live run owner,
background command, cleanup or post-pipeline command, or command whose enclosing
work already completed prevents acceptance. Recorded PIDs are diagnostic only: recovery neither infers completion
from PID disappearance nor signals a saved process ID.

Pipeline `mail_send` steps also checkpoint their send intent and delivery receipt.
If a response is lost or the run stops while a send is unresolved, automatic
retries and ordinary resume refuse to send the message again: the recipient may
already have received it. Inspect the recipient's inbox and original thread
before deliberately choosing `--mode=restart-failed` to resend. A saved successful
receipt restores the original output and parsed variables without another send,
even if the scheduler's final step checkpoint was lost. A local output-parsing
failure does not authorize resending delivered mail. Explicit server refusals
remain eligible for the step's normal retry policy. Interrupted sends from older
checkpoints without a receipt require the same deliberate replay decision.

Pipeline file reservations retain their resolved request, exact lease identities,
and partial grant evidence. A lost acquisition response or unverified grant never
triggers automatic reacquisition. Resume checks that retained protection still
has the original IDs, owner, paths, mode, and creation identity, with live expiry,
before dispatching further work. A same-ID renewal is accepted; a replacement
lease on the same path is not adopted. Release steps for this run's acquisitions
select only the recorded IDs and verify their disappearance, so another run's
same-path reservations remain intact. Standalone release steps without acquisition
evidence retain their explicit path behavior.

Verified partial grants remain available for exact-ID cleanup. Unverified handles
are inspection evidence, not authority to release somebody else's leases. Finish
releasing known active leases before resetting acquisition history. If an authored
cancellation or finalization hook already released protection, resuming unfinished
protected work requires a whole-workflow reset to acquire it again; iteration
replay cannot retain those released locks. Cleanup-only resume can finish when the
original IDs have already expired or disappeared.

While a pipeline is running, a heartbeat renews its verified reservation IDs
before expiry, including during long commands, agent waits, and post-pipeline
work. Independent readback must confirm the original lease identities and an
extended expiry; a lost renewal reply is accepted only when that readback proves
protection. Dispatch also checks ownership immediately before sending more work.
Lost, replaced, expired, or unverifiable protection fails the run and cancels
active waits and commands. Input already delivered to an external agent cannot
be recalled. Authored `on_cancel` cleanup still runs; reservations are never
automatically reacquired or released. Use explicit workflow release steps.

If you instead intend to repeat failed or interrupted agent work, inspect the
original pane before using `--mode=restart-failed`; that mode still preserves
completed delivery receipts. Use
`--keep-state=false` or `--mode=force-iter --step-id=<id> --iteration=<n>` for an
intentional reset or iteration replay. Commands with a normally settled outcome
retain ordinary retry behavior; operator-accepted outcomes require an explicit
reset before repeating them. An unresolved command is never automatically
repeated. Before deliberately restarting one, inspect its side effects and stop
any surviving command processes. These restart choices can repeat side effects.

### 7. Durable State, Audit, and Recovery

NTM treats recoverability as a core feature. Sessions can be checkpointed, timelines can
be replayed, audit records can be exported, and prompt/session history remains available
for analysis or resumption.

```bash
ntm checkpoint save payments -m "pre-migration"
ntm checkpoint list payments
ntm checkpoint restore payments
ntm checkpoint restore payments last --as payments-recovered --directory /path/to/recovery-worktree

ntm sessions save payments
ntm sessions restore payments --name payments-restored --launch
ntm sessions resume payments --name payments-resumed

ntm timeline list
ntm timeline show <session-id>
ntm history search "authentication error"
ntm audit show payments
ntm conflicts payments
ntm resume payments
```

Ordinary `ntm spawn payments --cc=2` also restores the latest readable local handoff for
that session from the resolved project directory. The previous goal, immediate
task, decisions, and next steps are delivered before the new user prompt through
the same readiness and submission checks as other spawn prompts. This works
without Beads, Agent Mail, or CM being installed. The handoff uses a bounded
recovery budget, and JSON output reports its source as `recovery.handoff_path`.
`--no-recovery`, `recovery.enabled = false`, or
`recovery.auto_inject_on_spawn = false` disables this injection. The shared
handoff reader can fall back to an older readable file if the newest is corrupt;
the reported source identifies the file actually used. If no handoff can be read,
or its session or project does not match, spawn reports partial recovery and can
proceed with its other available context.
Remote tmux sessions skip local handoffs and report that source as unavailable;
NTM does not treat a matching local path as proof of the remote project's identity.

Checkpoint restoration accepts `--as` to recover into a different tmux session
without replacing the source session or rewriting its checkpoint. The REST
restore endpoint accepts the same option as `target_session`. A separate session
still uses the checkpoint's working directory unless `--directory` (REST:
`custom_directory`) selects another working tree. Restores honor cancellation;
JSON responses retain the last restore stage, pane count, warnings, and
interruption status so partially restored sessions can be inspected before retrying.

Newly spawned agents also record their rendered launch command independently of
the pane title. Checkpoints preserve that command and its model, persona,
reasoning effort, and account-profile reference. Restore validates these inputs
before replacing an existing session, then recreates any configured Claude
credential isolation for the destination pane. Prepared persona prompt files
remain external references: a missing file or changed contents stops automatic
replay. Explicit pane or plugin environment values are not copied into this
record; recovery reports missing inputs instead of silently substituting the
current environment. Older panes without a launch record retain the existing
command-reconstruction path.

Saved sessions preserve each pane's working directory and the same recorded
launch settings, so panes in separate worktrees return to their own directories.
`sessions restore --launch` replays those settings; `sessions resume` also
replaces the native conversation selector when the save contains a fresh
provider binding. Recorded commands keep their model, reasoning, persona, and
account settings instead of taking current defaults. Native resume refuses
ambiguous shell commands or conflicting conversation selectors before `--force`
replaces a live session. Older saves without a recorded command retain the
existing CASR preference. Missing worktrees, changed persona files, unavailable
profiles, and omitted environment inputs prevent automatic relaunch. Agent
counts report successful command dispatch; they do not assert that an agent has
finished starting.

### 8. Multi-Project Swarm Execution

`ntm swarm --json` launches the requested swarm and returns an execution receipt.
Use `swarm plan` or `--dry-run` to preview allocations without creating sessions.

```bash
ntm swarm plan --scan-dir=/projects --json
ntm swarm --scan-dir=/projects --json --wait-ready
ntm swarm --scan-dir=/projects --json --prompt="Run the tests and report failures"
```

The receipt distinguishes planned allocations, created sessions and physical
panes, agent launches, initial prompt delivery, and optional readiness results.
Partial failures and cancellation return a nonzero exit status while retaining
the actual outcomes. Prompts go only to successfully launched agents that pass
readiness and identity checks; surviving sessions remain available for inspection.

`--auto-rotate-accounts` starts an acknowledged resident monitor for eligible
local Claude and Codex panes. It checks CAAM account availability and the actual
configured launch commands before creating sessions. JSON receipts identify
eligible panes, declined providers, and each monitor's PID and generation;
`swarm status --json` checks the resident's live process and heartbeat.
If monitor startup partially fails, the command returns a nonzero exit status
and preserves launched agents, successful monitors, and their startup receipts.

```bash
ntm swarm --scan-dir=/projects --auto-rotate-accounts --json
ntm swarm --scan-dir=/projects --auto-rotate-accounts --force-global-auth-clobber --json
```

This recovery lane requires Linux, default global credentials, and a live
provider process with a verifiable open main conversation transcript. Codex
also requires consent through the global-auth flag above or
`swarm.force_global_auth_clobber = true` in the configuration. Scoped profiles,
isolated credentials, custom credential homes or providers, and ambiguous
commands for otherwise eligible panes fail preflight before session creation.
Unsupported or excluded providers and Codex without global-auth consent can
launch unmonitored, provided at least one eligible pane remains. A temporary
lack of transcript evidence defers recovery until another observation; NTM does
not infer a conversation from the newest file in a worktree. Configured provider
restrictions and reset horizons still apply.

Recovery preserves the recorded command and native conversation, verifies the
replacement process and conversation, and reports account activation separately
from successful conversation recovery. A provider lock and durable activation
receipt coordinate multiple sessions: stalled sibling panes can resume using
the already activated account without repeatedly switching the global account.
Local session kill and forced checkpoint or saved-session replacement wait for
their resident to finish any in-flight recovery before destroying the session.
Remote tmux operations leave local monitors and processes untouched.

## Robot Mode and Local API

NTM has two automation layers:

- `--robot-*` for local, machine-readable CLI interactions
- `ntm serve` for REST, SSE, WebSocket, and OpenAPI-backed integrations

### Canonical Robot Surfaces

Start with these:

```bash
ntm --robot-help
ntm --robot-capabilities
ntm --robot-status
ntm --robot-snapshot
ntm --robot-plan
ntm --robot-dashboard
ntm --robot-markdown --compact
ntm --robot-terse
```

Common task-specific surfaces:

```bash
ntm --robot-send=payments --msg="Summarize current blockers." --type=claude
ntm --robot-ack=payments --timeout=30s
ntm --robot-tail=payments --lines=50
ntm --robot-mail-check --mail-project=payments --urgent-only
ntm --robot-cass-search="authentication error"
```

Optional CM memory injection (`--with-memory` or `memory.send_injection`) uses
the selected panes' verified project directory and the target session's daemon.
Running the command from another checkout cannot select that checkout's memory,
including when both projects have the same directory name. The JSON memory
receipt identifies the queried workspace or explains why injection was skipped.
Remote sessions and unresolved or mixed project scopes skip local CM retrieval
and continue ordinary prompt delivery. Context-pack builds use the same explicit
workspace scope.

With `--track`, successful acknowledgment reports memory feedback to the daemon
that supplied the rules. Changing daemon PID records while the send is pending
does not redirect that feedback. Canceling the command stops pending memory
retrieval and acknowledgment tracking.

### REST, SSE, WebSocket, and OpenAPI

Run the local server:

```bash
ntm serve
```

Set the default project with `ntm serve --project-dir /absolute/project/path`.
`PATCH /api/v1/config` changes it at runtime for every caller:
`{"project_dir": "/absolute/other/project"}` selects another project, and
`{"project_dir": ""}` or `{"project_dir": null}` resets it to the
`--project-dir` value (no project if the server started without one). Omitting
`project_dir` leaves it unchanged. `GET /api/v1/config` reports the current
`project_dir` and the reset target as `startup_project_dir`.

The selection scopes later requests on every project-scoped route, including
mail, beads, jobs, and pipelines. Jobs and pipeline runs already admitted, and
mail requests already in flight, keep the project they started with. The
target must be an existing directory, so a reset returns 400 and changes
nothing if the startup directory has since been removed.

Agent Mail operations under `/api/v1/mail/` and file reservation operations under
`/api/v1/reservations` also accept a `project` query parameter, so agents in
multiple projects can share one server:

```bash
curl --get http://localhost:7337/api/v1/mail/inbox \
  --data-urlencode 'project=/absolute/other/project' \
  --data-urlencode 'agent_name=BlueLake'

curl --get http://localhost:7337/api/v1/reservations \
  --data-urlencode 'project=/absolute/other/project' \
  --data-urlencode 'agent_name=BlueLake'
```

The override must name an existing absolute directory on the server host.
It applies to that request's mail agents, messages, inbox, search, threads, and
contacts, and to reservation creation, listing, lookup, conflict inspection,
release, renewal, and force-release. Omit it to use the server's configured
project. Each request keeps its chosen project throughout execution, including
retries with an `Idempotency-Key`; selecting a project does not change the server
configuration. Changing the default project separates subsequent retries from
the previous project's cached results. Force-release approvals remain bound to
the project and reservation the operator reviewed.

Mail read and acknowledgement events, reservation events, and reservation
approval-request events include `project_key` so subscribers can distinguish
identically named agents and reservation IDs in different projects. The mail
health endpoint checks the shared service and takes no project selector. Other
API groups continue to use the configured server project.

Important surfaces:

- REST API under `/api/v1`
- server-sent events at `/events`
- WebSocket subscriptions at `/api/v1/ws`
- health check at `/health`
- generated OpenAPI spec at [`docs/openapi.json`](docs/openapi.json)

Generate or refresh the OpenAPI document:

```bash
ntm openapi generate
ntm openapi generate --stdout
```

## Command Map

| Command group | What it covers |
| --- | --- |
| `quick`, `init`, `spawn`, `add`, `attach`, `view`, `zoom`, `dashboard`, `palette`, `kill` | Project bootstrap and session lifecycle |
| `send`, `interrupt`, `watch`, `activity`, `health`, `extract`, `diff`, `grep`, `analytics` | Day-to-day operator loop |
| `work`, `assign`, `coordinator` | Graph-aware prioritization, assignment, and conflict management |
| `mail`, `locks`, `worktrees` | Agent Mail coordination and file reservations |
| `safety`, `policy`, `approve`, `guards` | Safe-by-default operations and approval workflows |
| `checkpoint`, `timeline`, `history`, `audit`, `changes`, `resume` | Durable state and forensic surfaces |
| `recipes`, `workflows`, `template`, `session-templates`, `pipeline`, `ensemble` | Reusable orchestration assets |
| `serve`, `openapi`, `config`, `deps`, `upgrade`, `tutorial` | Integration, configuration, and operations |

`ntm --help` remains the canonical full command reference.

## Configuration and Project Assets

NTM supports user-level and project-level assets.

### User-Level

- main config: `~/.config/ntm/config.toml`
- recipes: `~/.config/ntm/recipes.toml`
- workflows: `~/.config/ntm/workflows/`
- personas/profiles: `~/.config/ntm/personas.toml`
- policy: `~/.ntm/policy.yaml`

### Project-Level

Project-local assets live under `.ntm/` and override built-ins and user defaults where appropriate.

- `.ntm/workflows/`
- `.ntm/pipelines/`
- `.ntm/personas.toml`
- `.ntm/recipes.toml`
- `.ntm/config.toml` for project-scoped settings such as additional assignment approval labels

Checkpoints are per user, not per project: they live under
`~/.local/share/ntm/checkpoints/<session>/`.

Useful config commands:

```bash
ntm config init
ntm config show
ntm config diff
ntm config get projects_base
ntm config edit
ntm config reset
```

Configuration loading is strict: unknown fields are errors. The unused TOML
`[health]` section has been removed; migrate restart and monitoring settings to
`[resilience]` (`auto_restart`, `max_restarts`, `restart_delay_seconds`,
`health_check_seconds`, and `crash_threshold`). The `ntm health` command remains
available and is unrelated to that removed config section.

### CAAM Seat Selection (opt-in, default off)

If you run several interchangeable subscription seats through
[CAAM](https://github.com/Dicklesworthstone/caam) — three Codex Pro logins, say
— NTM can choose which one a new pane launches on instead of leaving that to
whatever your agent command template pins:

```toml
[integrations.caam]
seat_selection = true
```

With the key on, a `spawn`/`add` pane that carries **no** persona asks
`caam limits <provider> --rank earliest-reset-headroom` and launches under the
matching `codex-<profile>` / `claude-<profile>` persona. The rank spends the
included allowance that refreshes **soonest while it still has headroom** and
preserves the later-resetting reserve seat. (It is deliberately not
`caam precheck --best`, which ranks lowest utilization and would pick the idle
reserve — the opposite of the policy.)

The semantics, exactly:

- **A pin always wins.** `--persona=codex-acme`, `--profile-set`, and recipe
  personas are never re-ranked and never overridden. Only a pane with no
  persona at all is placed.
- **One query per provider per command.** `ntm add --cod=4` shells out to caam
  once, not four times; the ranked seat is a property of the pool.
- **Later `ntm add`s re-rank.** A pane added to a running swarm is a new pane
  and gets the seat that is right now, not the one that was right at spawn.
  (Answers are memoized for 20 seconds, so two adds in quick succession share
  one ranking — provider windows do not move on that timescale.) Credentials
  inside an already-running pane are never rotated.
- **An unreadable pool skips those panes, loudly.** If caam is missing, times
  out, or ranks the pool and finds nothing with included headroom, no agent is
  launched into the affected panes and the reason is printed (and reported in
  `seat_skips` under `--json`). In `ntm add` the pane is never created at all;
  in `ntm spawn` the panes were already split before seats were resolved, so a
  refused one is left as a plain shell and named in the warning. Siblings on a
  provider that answered still come up: `ntm add --cod=2 --cc=2` with an
  exhausted Codex pool brings up the two Claude panes. A skipped pane never
  falls through to a static profile pin — that fallthrough is the failure this
  exists to prevent.
- **`ntm scale` honors it too.** Scale-up reports the number of panes that
  actually launched, not the number requested, and surfaces each refusal.
- **Only Claude and Codex are affected.** `caam limits` answers for no other
  provider, so gemini/grok/ollama/plugin panes are untouched.

`seat_selection` is unrelated to `auto_failover` in the same table, despite
sitting next to it: `auto_failover` swaps the **host** credential mid-session
for a pane that hit a rate limit; `seat_selection` only chooses which account a
**new** pane launches under. Enabling one does not enable the other, and
`seat_selection` starts no caam subprocess when it is off.

Requires caam >= v0.1.19 (the release that added the
`earliest-reset-headroom` rank mode).

### Rano Network Activity

[rano](https://github.com/Dicklesworthstone/rano)'s monitor records each agent
process's connect/close events into an SQLite database. ntm reads that history
(`rano export`) for `--robot-rano-stats`, the dashboard's Network Activity panel
and the process-triage monitor's network signal; it never starts the monitor.
Point ntm at the database the monitor writes (rano's default is
`observer.sqlite` in the monitor's working directory):

```toml
[integrations.rano]
sqlite_path = "/path/to/observer.sqlite"   # what you pass as `rano --sqlite`
```

The counts are connections, not requests or bytes. With no database at that
path, `--robot-rano-stats` fails with `DEPENDENCY_MISSING` and the panel shows
why, instead of reporting zero traffic. Details: `docs/rano-stats.md`.

### Agent Plugins

Custom agent types load from the `agents/` directory that sits next to the
selected config file: `~/.config/ntm/agents/*.toml` by default,
`$XDG_CONFIG_HOME/ntm/agents/` under an XDG override, or
`<dir-of-config>/agents/` with an explicit `--config`. Each TOML declares a
name/alias (which become `--<name>`/`--<alias>` spawn, `resume --spawn`, and `send` selectors), a
command template, and optional `[agent.readiness]` regexes that drive
idle/working/error classification for `status`, `activity`, `--robot-tail`,
`--robot-activity`, `--robot-is-working`, `--robot-health`, and `--verify-boot`
exactly like the built-in agents. NTM never modifies files in
`agents/` — an existing preset you have customised is yours.

Oh My Pi is now a built-in agent type (see [Oh My Pi](#oh-my-pi-omp)), so the
former `examples/agents/omp.toml` plugin preset is no longer needed. A leftover
`~/.config/ntm/agents/omp.toml` is harmless: built-in flags and the built-in
omp detectors take precedence over a plugin of the same name.

#### Pi coding agent (`pi`)

Install the [pi preset](examples/agents/pi.toml) as `agents/pi.toml` beside
your selected NTM configuration. Authenticate pi and configure its project
trust before automated launches. The preset preserves pi's own defaults;
model, thinking level, and persona system prompts are supplied when requested.

```bash
ntm spawn swarm --pi=2
ntm spawn swarm --pi=1:MODEL:high
ntm --robot-spawn=automated --spawn-pi=2:MODEL:high --spawn-wait
ntm send swarm --pi "Review the changes and run the tests"
ntm sessions save swarm
ntm sessions resume swarm --name recovered --native
ntm resume next-session --from path/to/handoff --spawn --pi=2 --omp=1
```

Pi has its own session provider and terminal parser. Its JSONL header supplies
the session ID and working directory, including when a transcript was renamed.
Discovery honors `--session-dir`, `PI_CODING_AGENT_SESSION_DIR`, and
`PI_CODING_AGENT_DIR`, with `~/.pi/agent/sessions/<encoded-cwd>/` as the default.
Files in a shared store are matched by their header's working directory;
malformed headers and ambiguous ID prefixes are not promoted to exact bindings.
Explicit `--session` and `--session-id` selectors are recognized during
discovery. Saved-session resume resolves the exact saved ID to an existing
transcript before launching `pi --session <path>`, retaining the original
model, thinking, prompt, and environment settings. A missing or ambiguous
transcript stops resume before an existing tmux session can be replaced.
Saved sessions retain the exact discovered pi transcript path in their private
save file, so resume still selects the original transcript when the caller's
session directory, agent directory, or home environment changes. A missing or
changed original transcript is an error even if another store contains the same
session ID. This additional stored reference is omitted from public state
fields; native resume commands still contain the selected transcript path.
Managed `spawn` and `add` launches assign each fresh pi pane a distinct
`--session-id` and record it with the launch command. Simultaneous panes in the
same working directory can therefore be saved and resumed without guessing
from transcript timestamps. Explicit pi session selectors remain authoritative.
Workflow template profiles named `pi` also retain their plugin type and count.
Robot spawning loads registered agents from the selected configuration and
accepts the same count, model, and thinking-level grammar through
`--spawn-<agent>` flags, including registered aliases. Recipes can mix pi with
built-in agents and retain each instance's model and thinking level. Plugin
readiness is shared by initial startup and pane restart, and the detached
monitor retains the selected configuration. Plugin environment values travel
through the private launch transport; saved launch metadata records their
names rather than their values.

For pipelines and workflows, pi's standard composer, unsent draft, cwd, and
usage rows are excluded from response evidence. Blank redraws retain the last
trusted boundary. A complete repaint can recover through a unique echo of the
current delivered prompt; an idle pane alone cannot establish that a
replacement screen answers the current turn. Unknown custom chrome, lost
prompt boundaries, and unrelated replacement screens remain conservative
refusals. Oh My Pi continues to use its separate `omp` session and terminal
behavior.

Response detection also accounts for pi rendering submitted Markdown. A
waiting pipeline step requires a unique echo of its current delivered prompt
and response text after that echo; workflow verdict matches must start after
the echo without changing the original regular-expression anchors. Supported
headings, emphasis, lists, and code retain their expected visible identity.
Bare URLs, angle-delimited URLs and email addresses also work, as do Markdown
links whose plain label exactly matches the destination. Named or formatted
links remain unsupported when their visible text depends on terminal hyperlink
support.
Prompts with unsupported or terminal-dependent rendering are refused before
automated response-waiting delivery; use plain text for those prompts. Explicit
pipeline `wait: none` remains fire-and-forget. Operator acceptance of an
uncertain pi delivery uses the same repaint checks, and normal resume collects
the response without sending the prompt again. Old in-flight pi receipts that
lack the rendered prompt identity require inspection and a deliberate restart.

## Design Principles

### No Silent Data Loss

Stateful operations are designed to leave artifacts behind: checkpoints, timelines, audit
records, pipeline state, and serialized robot/API responses.

### Graceful Degradation

Optional integrations such as Agent Mail, `bv`, `cass`, or worktree helpers make NTM stronger,
but the system is designed to remain locally useful without pretending missing tools are present.

### Idempotent Orchestration

Robot mode, durable stores, and resumable workflows are designed so operators and agents can
re-issue state queries and recover from interruptions without inventing undocumented side channels.

### Recoverable State

Sessions, pipelines, attention feeds, approvals, and history all have explicit recovery paths.

### Auditable Actions

NTM favors explicit logs, status surfaces, and durable state over invisible orchestration magic.

### Safe by Default

Destructive operations, guard rails, and approval workflows are treated as core product behavior,
not bolt-on scripts.

## Architecture

```text
                     +---------------------------+
                     |  Human Operator / Agent   |
                     |  CLI, TUI, Robot, REST    |
                     +-------------+-------------+
                                   |
                                   v
                     +---------------------------+
                     |            NTM            |
                     |---------------------------|
                     | session orchestration     |
                     | dashboard + palette       |
                     | work triage + assignment  |
                     | safety + policy + approve |
                     | pipelines + checkpoints   |
                     | serve + robot surfaces    |
                     +------+------+-------------+
                            |      |
                            |      +--------------------------+
                            |                                 |
                            v                                 v
              +---------------------------+      +---------------------------+
              | Durable state + event bus |      | Optional integrations     |
              | checkpoints, history,     |      | br, bv, Agent Mail, cass, |
              | timelines, audit, alerts  |      | dcg, pt, worktrees        |
              +-------------+-------------+      +---------------------------+
                            |
                            v
              +------------------------------+
              | tmux sessions and panes      |
              | Claude / Codex / AGY / Grok  |
              | labeled multi-agent work     |
              +------------------------------+
```

## Installation

### Install Script

```bash
curl -fsSL "https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh?$(date +%s)" | bash -s -- --easy-mode
```

### Homebrew

```bash
brew install dicklesworthstone/tap/ntm
```

### Docker

Multi-arch images (`linux/amd64`, `linux/arm64`) are pushed to GitHub Container
Registry by the release workflow, tagged by semver (`1`, `1.22`, `1.22.0`) and
commit SHA:

```bash
docker pull ghcr.io/dicklesworthstone/ntm:1
docker run --rm -it ghcr.io/dicklesworthstone/ntm:1
```

Or build locally:

```bash
docker build -t ntm .
docker run --rm -it ntm
```

### From Source

```bash
git clone https://github.com/Dicklesworthstone/ntm.git
cd ntm
go install -tags ensemble_experimental ./cmd/ntm   # or: make build
```

The `ensemble_experimental` tag matches the release binaries; without it
`ntm ensemble <name> "<question>"` only reports that it needs a tagged build.

## Troubleshooting

### `tmux not found`

Install `tmux` first, then re-run:

```bash
ntm deps -v
```

### Agent panes start empty or an agent CLI fails immediately

NTM can only launch tools that are installed and discoverable in `PATH`.
Use `ntm deps -v` to check what it sees.

### `claude`, `codex`, `agy`, `grok`, or `gemini` not detected over SSH / tmux / non-login shells

NTM discovers agent CLIs via the `PATH` of the **runtime environment it is launched in** —
not the `PATH` of your interactive login shell. Tools installed under npm-global or
`~/.local/bin` are often added to `PATH` by your `~/.bashrc` / `~/.zshrc` / `~/.profile`,
which a non-interactive or non-login shell (a bare SSH command, a detached tmux server, a
systemd unit, a CI runner) does not source. In that case `ntm deps -v` reports the agents as
missing even though they work fine in your normal terminal.

First, confirm what *NTM's* environment actually resolves, under the same shell/SSH/tmux
context where you run NTM:

```bash
command -v claude
command -v codex
command -v agy
command -v grok
command -v gemini
ntm deps -v
```

If those `command -v` checks come up empty here but succeed in your interactive shell, the
fix is to put the missing directories on `PATH` before launching NTM. The most robust option
is a small wrapper that exports the right `PATH` and then `exec`s NTM (paths vary by host):

```bash
#!/usr/bin/env bash
# ~/bin/ntm-wrapper — ensure agent CLIs are on PATH, then hand off to ntm.
export PATH="$HOME/.npm-global/bin:$HOME/.local/bin:$PATH"
exec ntm "$@"
```

Make it executable (`chmod +x ~/bin/ntm-wrapper`) and invoke that instead of `ntm`. Re-run
`ntm deps -v` through the wrapper to confirm all listed CLIs are now detected.

### A work command has nothing useful to say

`ntm work ...` depends on running inside a repo with Beads/BV data available.
If you are outside the project root, change directories or bootstrap the repo first.

### Mail, locks, or overseer commands say the server is unavailable

Those surfaces depend on Agent Mail being configured and reachable. NTM will still work for
session orchestration without it.

### Pipeline resume or cleanup does not see the state you expect

Make sure the relevant session/project is using the intended project directory. Project-scoped
state lives under that directory's `.ntm/` tree.

## FAQ

### Does NTM replace tmux?

No. NTM is a structured orchestration layer on top of `tmux`.

### Can I use it with one agent instead of a swarm?

Yes. It is perfectly fine to start with one Claude or Codex pane and only scale up when needed.

### Do I need every optional integration?

No. Core session management works with `tmux` and your agent CLIs. Work triage, Agent Mail,
CASS, and safety extras become available as those tools are configured.

### Is robot mode the preferred automation surface?

For local scripting and agent workflows, yes. For long-lived integrations, dashboards, and
service-style consumers, use `ntm serve` and the OpenAPI-backed REST/WebSocket surfaces.

### Can multiple swarms work on the same project?

Yes. Labels, Agent Mail, file reservations, worktrees, and assignment flows are designed for that.

### Does NTM preserve history and state?

Yes. Checkpoints, pipeline state, audit records, timelines, history, and event streams are all part
of the normal product model.

## Limitations

- NTM is intentionally `tmux`-centric.
- Linux and macOS are the primary environments.
- Some advanced workflows depend on external tools such as Agent Mail, `br`, `bv`, `cass`, or worktree helpers.
- Grok Build agents cannot take personas (the Grok Build CLI has no system-prompt mechanism); a persona on a Grok agent fails the launch.
- The system is local-first. It is not a hosted SaaS control plane.

## Development

Build and verification:

```bash
go build ./cmd/ntm
go test -short ./...
golangci-lint run
```

Regenerate the OpenAPI document:

```bash
ntm openapi generate
```

## About Contributions

*About Contributions:* Please don't take this the wrong way, but I do not accept outside contributions for any of my projects. I simply don't have the mental bandwidth to review anything, and it's my name on the thing, so I'm responsible for any problems it causes; thus, the risk-reward is highly asymmetric from my perspective. I'd also have to worry about other "stakeholders," which seems unwise for tools I mostly make for myself for free. Feel free to submit issues, and even PRs if you want to illustrate a proposed fix, but know I won't merge them directly. Instead, I'll have Claude or Codex review submissions via `gh` and independently decide whether and how to address them. Bug reports in particular are welcome. Sorry if this offends, but I want to avoid wasted time and hurt feelings. I understand this isn't in sync with the prevailing open-source ethos that seeks community contributions, but it's the only way I can move at this velocity and keep my sanity.

## License

NTM is released under the MIT license, with the additional rider described in [`LICENSE`](LICENSE).
