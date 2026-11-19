# NTM Robot Mode

Use this file when you need the deeper, structured automation side of NTM.

## Output Formats

| Flag | Meaning |
| --- | --- |
| `--robot-format=json` | Full JSON output |
| `--robot-format=toon` | More token-efficient structured output |
| `--robot-format=auto` | Auto-select current default |

Verbosity: `--robot-verbosity=terse|default|debug`

## Start Here

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

`--robot-capabilities` is the canonical schema/discovery surface. Prefer it over
parsing human help text.

## Canonical Operator Loop

```text
1. Bootstrap with ntm --robot-snapshot
2. Read latest cursor / attention summary
3. Tend with ntm --robot-attention or ntm --robot-wait
4. Act with ntm --robot-send, ntm send, ntm assign, ntm mail, or ntm locks
5. Repeat

If the cursor expires, re-run --robot-snapshot.
```

## Attention Feed

| Command | Purpose |
| --- | --- |
| `--robot-snapshot` | Bootstrap unified state plus attention summary and cursor handoff |
| `--robot-events` | Raw replay since a cursor |
| `--robot-digest` | Aggregated summary since a cursor |
| `--robot-attention` | Wait-then-digest tending command |
| `--robot-overlay` | Human handoff / overlay actuator |
| `--robot-wait` | Wait for pane or attention conditions |

Example flow:

```bash
ntm --robot-snapshot
ntm --robot-events --since-cursor=42 --events-limit=50 --events-category=agent
ntm --robot-digest --since-cursor=42
ntm --robot-attention --since-cursor=42
ntm --robot-overlay --overlay-session=myproject --overlay-no-wait
```

### Operator Attention Actions

Durable operator state transitions for surfaced attention items (reflected in subsequent `--robot-snapshot` and `--robot-attention` digests):

```bash
ntm attention list --json
ntm attention ack 42 --reason "investigating pane_2"
ntm attention snooze 42 --until 30m --reason "waiting on build"
ntm attention pin 42 --reason "release blocker"
ntm attention dismiss 42 --reason "false alarm"
```

### Attention Profiles

| Profile | Flag | Behavior |
| --- | --- | --- |
| `operator` | `--profile=operator` | Default operator-focused blend |
| `debug` | `--profile=debug` | Full verbosity |
| `minimal` | `--profile=minimal` | Only the most urgent items |
| `alerts` | `--profile=alerts` | Alert-centric view |

Explicit filters override profile defaults.

### Wait Conditions

Pane-oriented conditions:

- `idle`
- `complete`
- `generating`
- `healthy`

Attention-oriented conditions:

- `attention`
- `action_required`
- `mail_pending`
- `mail_ack_required`
- `context_hot`
- `reservation_conflict`
- `file_conflict`
- `session_changed`
- `pane_changed`

Deliberately unsupported:

- `bead_orphaned`

Example:

```bash
ntm --robot-wait=myproject --condition=idle --timeout=5m
ntm --robot-wait=myproject --condition=action_required --since-cursor=42
ntm --robot-wait=myproject --condition=mail_pending,reservation_conflict
```

## Core Robot Actions

```bash
ntm --robot-send=myproject --msg="Fix auth" --type=claude
ntm --robot-ack=myproject --timeout=30s
ntm --robot-tail=myproject --lines=50
ntm --robot-interrupt=myproject
ntm --robot-inspect-pane=myproject --index=2

# Idempotent sends and durable request tracking
ntm --robot-send=myproject --msg="Apply schema migration" --op-id=deploy-42
ntm --robot-send-receipt=deploy-42
ntm --robot-action-status=deploy-42
ntm --robot-wait-request=deploy-42 --timeout=30s
ntm --robot-cancel-request=deploy-42

# Session Goal/Now continuity handoffs and quality scores
ntm --robot-handoff=myproject
ntm --robot-handoff=all --handoff-limit=5
ntm --robot-handoff=myproject --handoff-create --handoff-goal="Implemented auth" --handoff-now="Run E2E suite"
ntm --robot-handoff=myproject --handoff-create --handoff-auto

# Multi-harness session, prompt, token, crash/restart, and error analytics
ntm --robot-analytics
ntm --robot-analytics --analytics-days=7 --analytics-sessions
ntm --robot-analytics --analytics-since=2026-01-01

# Tamper-evident audit logs, hash-chain verification, and HTTP daemon audit trail
ntm --robot-audit
ntm --robot-audit=myproject --audit-limit=25
ntm --robot-audit --audit-search=spawn --audit-days=7
ntm --robot-audit=myproject --audit-verify
ntm --robot-audit --audit-sources
ntm audit http --action=create --limit=20 --json

# Persisted session timelines and aggregate storage statistics
ntm --robot-timeline
ntm --robot-timeline=myproject --timeline-events
ntm --robot-timeline --timeline-stats

# Ecosystem health doctor and prompt preflight validation
ntm --robot-doctor
ntm --robot-doctor --doctor-contention --doctor-verbose
ntm --robot-preflight="fix the bug in auth.go"
ntm --robot-preflight="rm -rf /tmp/cache" --preflight-strict
ntm --robot-preflight --preflight-file=prompt.txt

# 11-harness scaling, workload rebalancing, review queue, coordinator, and webhook outbox
ntm --robot-scale=myproject --scale-cc=4 --scale-cod=2 --dry-run
ntm --robot-rebalance=myproject --rebalance-apply --rebalance-threshold=0.5
ntm --robot-review-queue=myproject --review-dispatch --review-commits=5
ntm --robot-coordinator=myproject --coordinator-action=status
ntm --robot-coordinator=myproject --coordinator-action=digest
ntm --robot-coordinator=myproject --coordinator-action=conflicts
ntm --robot-webhook-outbox --outbox-action=list

# External session adoption, 11-harness personas, git hooks, and secret redaction preview
ntm --robot-adopt=external_proj --adopt-cc=0,1 --adopt-cod=2 --dry-run
ntm --robot-adopt=external_proj --adopt-by-window=cc
ntm --robot-personas --personas-agent=claude
ntm --robot-hooks --hooks-action=status
ntm --robot-hooks --hooks-action=run --hooks-type=pre-commit
ntm --robot-redact="Authorization: Bearer sk-live-1234567890abcdef1234567890"

# Saved sessions, casr resume readiness, Agent Mail <-> pane mapping, workflows, and session templates
ntm --robot-saved-sessions
ntm --robot-saved-sessions --saved-action=show --saved-name=myproject
ntm --robot-saved-sessions --saved-action=save --saved-session=myproject --saved-overwrite
ntm --robot-mapping=myproject
ntm --robot-workflows --workflows-name=red-green
ntm --robot-session-templates --template-name=refactor

# Pane grep, code-block extract, config/policy validation, plugins, and local models
ntm --robot-grep='panic:|FAIL' --grep-session=myproject --type=cc,cod
ntm --robot-extract=myproject --extract-lang=go --lines=1000
ntm --robot-validate --validate-all
ntm --robot-plugins
ntm --robot-models --models-action=recommend
```

## Reasoning Ensembles

```bash
ntm --robot-ensemble=myensemble
ntm --robot-ensemble-modes --tier=core
ntm --robot-ensemble-presets
ntm --robot-ensemble-suggest="What security issues exist?"
ntm --robot-ensemble-spawn=myensemble --preset=project-diagnosis --question="What are the main risks?"
ntm --robot-ensemble-synthesize=myensemble --synth-strategy=consensus --synth-format=markdown
ntm --robot-ensemble-stop=myensemble
```

## Backpressure, Swarm Lab, Scanner, and Support Bundles

```bash
ntm --robot-backpressure
ntm --robot-backpressure --session=myproject
ntm --robot-swarm-lab --lab-action=list
ntm --robot-swarm-lab --lab-action=run --lab-scenario=short_smoke --lab-gate=short
ntm --robot-swarm-lab --lab-action=corpus --lab-gate=short
ntm --robot-swarm-lab --lab-action=calibrate
ntm --robot-ubs-scan=.
ntm --robot-ubs-scan --scan-staged
ntm --robot-ubs-scan --scan-diff
ntm --robot-support-bundle=myproject --bundle-output=/tmp/ntm-debug.zip
```

## Beads, Mail, and CASS

```bash
ntm --robot-beads-list --status=open
ntm --robot-bead-claim=br-123 --bead-assignee=agent1
ntm --robot-bead-close=br-123 --bead-close-reason="Completed"

ntm --robot-mail-check --mail-project=myproject --urgent-only
ntm --robot-cass-search="authentication error"
```

These are useful when a script or agent needs structured access to work state,
coordination state, or past-session search.

## Human-Friendly Robot Views

When JSON is too heavy but you still need automation-friendly output:

```bash
ntm --robot-markdown
ntm --robot-markdown --compact
ntm --robot-terse
```

Use `--robot-terse` for operator summaries. Use `--robot-markdown` when a human
or another model benefits from lower-token tables instead of raw JSON.
