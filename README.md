# NTM - Named Tmux Manager

<div align="center">
  <img src="ntm_dashboard.webp" alt="NTM dashboard">
</div>

<div align="center">

![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS-blue.svg)
![Go Version](https://img.shields.io/badge/go-1.25+-00ADD8.svg)
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
- Required for agent spawning: whichever CLIs you want to run, typically Claude Code, Codex, and Gemini CLI
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
ntm spawn api --cc=2 --cod=1 --gmi=1

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
ntm spawn payments --cc=3 --cod=2 --gmi=1
ntm add payments --cc=1
ntm list
ntm status payments
ntm view payments
ntm zoom payments 3
ntm attach payments
```

Use labels when you want multiple coordinated swarms on the same project while
keeping a shared project directory:

```bash
ntm quick payments --template=go
ntm spawn payments --label backend --cc=2 --cod=1
ntm spawn payments --label frontend --cc=2
ntm add payments --label frontend --cc=1
```

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
```

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

### 6. Pipelines, Templates, Recipes, and Workflow Assets

NTM supports several layers of reusable automation:

- `recipes`: reusable session presets
- `workflows`: orchestration patterns such as pipeline, ping-pong, and review-gate
- `template`: prompt templates and substitutions
- `pipeline`: executable multi-step agent workflows with variables, dependencies, resume, and cleanup
- `session-templates`: higher-level session layouts

```bash
ntm recipes list
ntm recipes show full-stack
ntm workflows list
ntm workflows show red-green
ntm template list
ntm template show fix-bug

ntm pipeline run .ntm/pipelines/review.yaml --session payments
ntm pipeline status run-20241230-123456-abcd
ntm pipeline list
ntm pipeline resume run-20241230-123456-abcd
ntm pipeline cleanup --older=7d
```

### 7. Durable State, Audit, and Recovery

NTM treats recoverability as a core feature. Sessions can be checkpointed, timelines can
be replayed, audit records can be exported, and prompt/session history remains available
for analysis or resumption.

```bash
ntm checkpoint save payments -m "pre-migration"
ntm checkpoint list payments
ntm checkpoint restore payments

ntm timeline list
ntm timeline show <session-id>
ntm history search "authentication error"
ntm audit show payments
ntm changes conflicts payments
ntm resume payments
```

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
ntm --robot-markdown --md-compact
ntm --robot-terse
```

Common task-specific surfaces:

```bash
ntm --robot-send=payments --msg="Summarize current blockers." --type=claude
ntm --robot-ack=payments --ack-timeout=30s
ntm --robot-tail=payments --lines=50
ntm --robot-mail-check --mail-project=payments --urgent-only
ntm --robot-cass-search="authentication error"
```

### REST, SSE, WebSocket, and OpenAPI

Run the local server:

```bash
ntm serve
```

Important surfaces:

- REST API under `/api/v1`
- server-sent events at `/events`
- WebSocket subscriptions at `/ws`
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
- `.ntm/checkpoints/`

Useful config commands:

```bash
ntm config init
ntm config show
ntm config diff
ntm config get projects_base
ntm config edit
ntm config reset
```

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
              +---------------------------+
              | tmux sessions and panes   |
              | Claude / Codex / Gemini   |
              | labeled multi-agent work  |
              +---------------------------+
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

### Go Install

```bash
go install github.com/Dicklesworthstone/ntm/cmd/ntm@latest
```

### Docker

```bash
docker build -t ntm .
docker run --rm -it ntm
```

### From Source

```bash
git clone https://github.com/Dicklesworthstone/ntm.git
cd ntm
go build ./cmd/ntm
```

## Troubleshooting

### `tmux not found`

Install `tmux` first, then re-run:

```bash
ntm deps -v
```

### Agent panes start empty or an agent CLI fails immediately

NTM can only launch tools that are installed and discoverable in `PATH`.
Use `ntm deps -v` to check what it sees.

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
