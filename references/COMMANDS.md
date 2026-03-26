# NTM Command Patterns

Use this file when the main `ntm` skill body is not enough and you need the denser
operator command patterns that make NTM powerful in practice.

## Session Lifecycle

```bash
ntm quick myproject --template=go
ntm quick myproject --label frontend

ntm spawn myproject --cc=3 --cod=2 --gmi=1
ntm spawn myproject --label frontend --cc=3
ntm spawn myproject --label backend --cc=2 --worktrees
ntm spawn myproject --no-user --cc=5 --cod=5
ntm add myproject --cc=2
ntm add myproject --label frontend --cc=1

ntm list
ntm status myproject
ntm attach myproject
ntm view myproject
ntm zoom myproject 1
ntm kill myproject
ntm kill --project myproject
```

### Agent Count Heuristics

- `--cc=3 --cod=2 --gmi=1`: good default mixed swarm
- `--cc=5`: architecture-heavy, lower coordination load
- `--cc=2 --cod=3`: straightforward implementation volume
- `--cc=5 --cod=5`: larger swarm only when the operator loop is already healthy

## High-Leverage Send Patterns

```bash
# Basic targeting
ntm send myproject --cc "Review the API design"
ntm send myproject --cod --gmi "Run tests and summarize failures"
ntm send myproject --all "Checkpoint and summarize current state"
ntm send myproject --pane=2 "You own the auth migration."
ntm send myproject --panes=2,3 "Pair on the broken build."

# Broadcast across labeled sessions for one base project
ntm send --project myproject "Sync to main and report blockers."

# File-backed prompts, stdin, and reusable wrappers
ntm send myproject --file prompts/review.md
git diff | ntm send myproject --all --prefix "Review these changes:"
ntm send myproject --base-prompt-file ./common-instructions.txt --file ./task.txt

# File context and templates
ntm send myproject -c internal/auth/service.go "Refactor this safely"
ntm send myproject -c a.go -c b.go "Compare these implementations"
ntm send myproject -t fix --var issue="nil pointer" --file internal/auth/service.go

# Smart routing and automated distribution
ntm send myproject --smart "Take the next auth follow-up"
ntm send myproject --smart --route=affinity "Continue the migration work"
ntm send myproject --distribute --dist-strategy=dependency
ntm send myproject --distribute --dist-auto --dist-strategy=balanced

# Batch / randomized sends
ntm send myproject --batch prompts.txt --delay=5s
ntm send myproject --batch prompts.txt --broadcast
ntm send myproject --all --randomize
```

## Monitoring and Output

```bash
# Output capture
ntm copy myproject:1
ntm copy myproject --all
ntm copy myproject --cc
ntm copy myproject --code
ntm save myproject

# Activity and stream monitoring
ntm activity myproject --watch
ntm health myproject
ntm watch myproject --cc
ntm logs myproject --panes=1,2

# Compare / inspect
ntm extract myproject --lines=200
ntm diff myproject cc_1 cod_1
ntm grep "timeout" myproject -C 3
```

### Human-Only Surfaces

These are excellent for operators, but not for agents driving automation:

```bash
ntm dashboard myproject
ntm palette myproject
ntm bind
ntm tutorial
```

## Work Intelligence

```bash
ntm work triage
ntm work triage --by-label
ntm work triage --by-track
ntm work triage --format=markdown --compact
ntm work alerts
ntm work search "JWT authentication"
ntm work impact internal/api/auth.go
ntm work next
ntm work history
ntm work forecast br-123
ntm work graph
ntm work label-health
ntm work label-flow
```

Use `ntm assign` when you want NTM to help push work onto panes instead of just
observing the graph:

```bash
ntm assign myproject --auto --strategy=dependency
ntm assign myproject --beads=br-123,br-124 --agent=codex
```

## Coordination, Recovery, and Durable State

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

Worktree-specific commands when repo policy allows them:

```bash
ntm worktrees list
ntm worktrees merge claude_1
ntm worktrees clean --session myproject
```

## Reusable Assets

```bash
ntm recipes list
ntm recipes show full-stack
ntm workflows list
ntm workflows show red-green
ntm template list
ntm template show fix-bug
ntm session-templates list
ntm session-templates show refactor
```

Use these when you want repeatable swarm composition rather than bespoke commands every time.
