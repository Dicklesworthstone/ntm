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
ntm --robot-markdown --md-compact
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
ntm --robot-events --since-cursor=42 --limit=50 --category=agent
ntm --robot-digest --since-cursor=42
ntm --robot-attention --since-cursor=42
ntm --robot-overlay=myproject --overlay-no-wait
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
ntm --robot-ack=myproject --ack-timeout=30s
ntm --robot-tail=myproject --lines=50
ntm --robot-interrupt=myproject
ntm --robot-inspect-pane=myproject --inspect-index=2
```

## Beads, Mail, and CASS

```bash
ntm --robot-beads-list --beads-status=open
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
ntm --robot-markdown --md-compact
ntm --robot-terse
```

Use `--robot-terse` for operator summaries. Use `--robot-markdown` when a human
or another model benefits from lower-token tables instead of raw JSON.
