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
```

## Reasoning Ensembles

```bash
ntm --robot-ensemble=myensemble
ntm --robot-ensemble-modes --tier=core
ntm --robot-ensemble-presets
ntm --robot-ensemble-suggest="What security issues exist?"
ntm --robot-ensemble-spawn=myensemble --preset=project-diagnosis --question="What are the main risks?"
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
