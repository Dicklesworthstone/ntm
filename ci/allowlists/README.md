# ci/allowlists — the reality-bridge burndown ledger

Shared allowlist infrastructure for the WS0 guards (epic bd-ws0-guards-klz98).

## Files

| File           | Guard | Consumer script |
|----------------|-------|-----------------|
| `deadcode.txt` | G1 dead-code gate | `scripts/guards/deadcode_gate.sh` |
| `config.txt`   | G2 config-key liveness | `scripts/guards/config_liveness.sh` + `internal/config/liveness_test.go` |
| `docs.txt`     | G3 docs-example conformance | seeded by bd-ws0-guards-klz98.4 |
| `placebo.txt`  | G5 placebo lint | `scripts/guards/placebo_lint.sh` |
| `contracts.txt`| G6 single-definition contracts | `scripts/guards/contracts_lint.sh` |

## Line format

Tab-separated, exactly three non-empty fields:

    <entry>\t<bead-id>\t<reason>

- `#`-prefixed comment lines and blank lines are allowed anywhere.
- A line reading exactly `# permanent:` starts the permanent section. Entries after
  it use the literal `permanent` in the bead-id field and are exempt from the
  open-bead check; each permanent entry's reason MUST name the tool blind spot it
  compensates (e.g. `reflection`).
- Every non-permanent entry's bead-id must reference an OPEN bead.
  `scripts/check_allowlists.sh` enforces this (via `br` locally; bead-ID shape
  validation only in CI, where `br` is not installed).

## Waiver protocol

Adding a line = adding a bead FIRST; the PR description links both. There is no
other waiver mechanism.

## Ratchet (both directions)

Every gate diffs its current violations against its allowlist with `comm`:

- a violation not in the list fails (**new debt**);
- a listed entry that no longer matches a current violation also fails
  (**stale line — delete it in this PR**).

So the lists can only shrink truthfully. Progress metric / burndown chart:
`wc -l ci/allowlists/*.txt`. R3 requires only `#` comments and `# permanent:`
entries remaining — zero bead-linked lines.
