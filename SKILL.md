---
name: ntm
description: >-
  Run NTM for multi-agent tmux orchestration, work triage, robot mode, safety,
  coordination, and local APIs. Use when spawning swarms, dispatching work, or
  operating `ntm` as an agent or human operator.
---

<!-- TOC: Quick Start | Session Orchestration | Dispatch & Reusable Assets | Work Intelligence | Coordination | Safety | Robot Mode | Serve API | Project Resolution | References | Related Skills -->

# NTM — Named Tmux Manager

> **Core capability:** Turn `tmux` into a structured, recoverable multi-agent workspace.

> **Read the repo first.** If the target repository has `AGENTS.md` or `README.md`, read those before applying this skill. Repo-local instructions override generic NTM advice.

> **Interactive vs automation:**
> - `ntm dashboard`, `ntm palette`, and other TUI surfaces are for humans.
> - For machine-readable automation, prefer `--robot-*`.
> - Non-interactive CLI commands such as `ntm send`, `ntm work triage`, `ntm locks list`, `ntm pipeline status`, and `ntm serve` are fine when they are the clearest tool.

> **Coordination and isolation:**
> - Agent Mail reservations are the default coordination primitive.
> - `--worktrees` and `ntm worktrees ...` are supported isolation tools when the repo policy allows them.
> - If a repo `AGENTS.md` prefers reservations-only or has worktree-specific rules, follow that repo.

## Quick Start

```bash
# Install / sanity check
curl -fsSL "https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh?$(date +%s)" | bash -s -- --easy-mode
ntm deps -v

# Create or resolve a project
ntm quick myproject --template=go

# Launch a mixed swarm
ntm spawn myproject --cc=2 --cod=1 --gmi=1

# Dispatch work
ntm send myproject --cc "Map the auth layer and propose a refactor plan."

# Inspect the current work graph and system state
ntm work triage --format=markdown
ntm --robot-snapshot
```

## Session Orchestration

Use these for day-to-day session lifecycle management:

```bash
ntm spawn myproject --cc=3 --cod=2 --gmi=1
ntm spawn myproject --label frontend --cc=2
ntm spawn myproject --label backend --cc=2 --worktrees
ntm add myproject --cc=1
ntm add myproject --label frontend --cod=1
ntm list
ntm status myproject
ntm view myproject
ntm zoom myproject 3
ntm attach myproject
ntm dashboard myproject
ntm palette myproject
```

Useful spawn patterns:

```bash
ntm spawn myproject --prompt "Read AGENTS.md and start on ready work"
ntm spawn myproject -r full-stack
ntm spawn myproject -t red-green
ntm spawn myproject --persona=architect --persona=implementer:2
ntm spawn myproject --stagger-mode=smart --cc=6 --cod=4
```

## Dispatch and Reusable Assets

High-leverage NTM usage is not just `spawn` plus `send`. The real power shows up when
you combine richer dispatch patterns with reusable session and prompt assets.

```bash
ntm send myproject --all "Checkpoint and summarize blockers."
ntm send myproject --pane=2 "Own the auth migration."
ntm send --project myproject "Sync to main and report conflicts."
ntm send myproject -c internal/auth/service.go "Review this subsystem"
ntm send myproject -t fix --var issue="nil pointer" --file internal/auth/service.go
ntm send myproject --smart --route=affinity "Take the auth follow-up"
ntm send myproject --distribute --dist-strategy=dependency

ntm recipes list
ntm recipes show full-stack
ntm workflows list
ntm workflows show red-green
ntm template list
ntm template show refactor
ntm session-templates list
ntm session-templates show refactor
```

User-level and project-level assets both matter. NTM can resolve configuration from
`~/.config/ntm/...` and project-local `.ntm/...` trees, so check the repo before
assuming defaults.

## Work Intelligence

NTM is no longer just a pane launcher. It has first-class work selection and assignment:

```bash
ntm work triage
ntm work triage --by-track
ntm work alerts
ntm work search "JWT auth"
ntm work impact internal/api/auth.go
ntm work next
ntm work graph

ntm assign myproject --auto --strategy=dependency
ntm assign myproject --beads=br-123,br-124 --agent=codex
```

Use `ntm work ...` when you want NTM to wrap `bv` and present work in operator-friendly form.
Use raw `bv --robot-*` when you specifically want the graph engine's native robot output.

## Coordination and Recovery

NTM now exposes the surrounding coordination stack directly:

```bash
ntm mail send myproject --all "Report blockers and current file focus."
ntm mail inbox myproject
ntm locks list myproject --all-agents
ntm locks renew myproject
ntm locks force-release myproject 42 --note "agent inactive"
ntm coordinator status myproject
ntm coordinator digest myproject
ntm coordinator conflicts myproject
ntm checkpoint save myproject -m "before risky refactor"
ntm checkpoint list myproject
ntm checkpoint restore myproject
ntm timeline list
ntm timeline show <session-id>
ntm history search "authentication error"
ntm audit show myproject
ntm changes conflicts myproject
ntm resume myproject
```

Isolation options:

```bash
# Coordination-first
ntm locks list myproject

# Isolation-first when policy allows it
ntm spawn myproject --cc=3 --worktrees
ntm worktrees list
ntm worktrees merge claude_1
```

## Safety and Approvals

NTM has built-in safety, policy, and approval surfaces. Use them instead of ad hoc shell habits:

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

If the repo instructions require offloading builds or tests through another tool such as `rch`, obey the repo instructions.

## Canonical Robot Mode

Start with these:

```bash
ntm --robot-help
ntm --robot-capabilities
ntm --robot-status
ntm --robot-snapshot
ntm --robot-plan
ntm --robot-dashboard
ntm --robot-markdown --md-compact
ntm --robot-terse
```

Common task-specific robot surfaces:

```bash
ntm --robot-send=myproject --msg="Summarize blockers." --type=claude
ntm --robot-ack=myproject --ack-timeout=30s
ntm --robot-tail=myproject --lines=50
ntm --robot-mail-check --mail-project=myproject --urgent-only
ntm --robot-cass-search="authentication error"
ntm --robot-beads-list --beads-status=open
ntm --robot-bead-claim=br-123 --bead-assignee=agent1
ntm --robot-bead-close=br-123 --bead-close-reason="Completed"
```

Operator loop:

```text
1. Bootstrap with --robot-snapshot
2. Tend with --robot-attention or --robot-wait
3. Act with --robot-send, ntm send, ntm assign, ntm locks, or ntm mail
4. Re-bootstrap with --robot-snapshot if the cursor expires
```

Prefer `--robot-*` when another agent or script needs structured output.

## Serve API and Pipeline Surfaces

NTM also exposes local API and durable workflow surfaces:

```bash
ntm serve --port 7337
ntm openapi generate
ntm pipeline run .ntm/pipelines/review.yaml --session myproject
ntm pipeline status run-20241230-123456-abcd
ntm pipeline list
ntm pipeline resume run-20241230-123456-abcd
ntm pipeline cleanup --older=7d
```

Use `ntm serve` for long-lived local integrations. Use `--robot-*` for single-shot agent control.

## Project Resolution

`ntm spawn` needs a project directory that NTM can resolve.

```bash
ntm config get projects_base
ntm quick myproject --template=go

# Or point projects_base at an existing repo layout / create a symlink when needed
```

The session name usually matches the project directory name. Labels extend the session name as `project--label`.

## Reference Index

Read these when you need deeper detail without bloating the main skill body:

| Topic | Reference |
| --- | --- |
| High-leverage command patterns, output capture, monitoring, reusable assets | [COMMANDS.md](references/COMMANDS.md) |
| Attention feed, robot output formats, wait conditions, mail/cass/bead robot flows | [ROBOT-MODE.md](references/ROBOT-MODE.md) |
| Human dashboard, palette, keybindings, and TUI implementation notes | [DASHBOARD.md](references/DASHBOARD.md) |
| Project resolution, `projects_base`, config paths, and project-local assets | [CONFIG.md](references/CONFIG.md) |

## Related Skills

- `agent-mail` for inboxes, contact handshakes, and file reservations
- `br` for bead state changes and syncing
- `bv` for graph-aware task prioritization
- `cass` for prior-session retrieval
