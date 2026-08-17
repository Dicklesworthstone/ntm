# Reality Audit — v1.25.0

generated: 2026-08-17T20:14:15Z by scripts/reality_audit.sh
seed: 1413432836
allowlist-lines: 2769
gates: yes
total-minutes: 4 (budget 90)

## Step timings
- 1-checklist: 1m (budget 10m)
- 2-gates: 2m (budget 15m)
- 3-probes: 1m (budget 30m)
- 4-ledger: 2m (budget 20m)

## Checklist drift (vs none — baseline run)
```diff
<no drift>
```

## Allowlist burndown
```
     452 ci/allowlists/config.txt
      16 ci/allowlists/contracts.txt
    2298 ci/allowlists/deadcode.txt
      21 ci/allowlists/docs.txt
      35 ci/allowlists/placebo.txt
    2822 total
```

## Spot-probes (seeded from tag; exercise against the BUILT artifact, not the tree)
| # | claim row | verdict (WORKS / PARTIAL / NO_SURFACE / NO_BEAD) | evidence |
|---|-----------|--------------------------------------------------|----------|
| 1 | SKILL.md:167 Safety and Approvals | UNPROBED | |
| 2 | README.md:707 Can multiple swarms work on the same project? | UNPROBED | |
| 3 | README.md:170 Worktree isolation and reservations | UNPROBED | |
| 4 | command_palette.md:100 combine_plans_into_hybrid | Combine Plans Into Hybrid | UNPROBED | |
| 5 | docs/ORCHESTRATION_FEATURES.md:389 API Design | UNPROBED | |
| 6 | README.md:693 Can I use it with one agent instead of a swarm? | UNPROBED | |
| 7 | docs/ORCHESTRATION_FEATURES.md:575 Integration with Health | UNPROBED | |
| 8 | README.md:412 Canonical Robot Surfaces | UNPROBED | |
| 9 | SKILL.md:190 Canonical Robot Mode | UNPROBED | |
| 10 | docs/ORCHESTRATION_FEATURES.md:1152 Alternative: Orchestrator Work Assignment | UNPROBED | |

## Ledger audit (10 seeded closed beads; Proof test must exist in go test -list)
| bead | proof status |
|------|--------------|
| bd-ws3-contract-breadth-psvyu.5.1 | NO_PROOF_NAMED |
| bd-4we2d | NO_PROOF_NAMED |
| bd-yq852 | OK (TestRestartAgentHandlesSendKeysError) |
| bd-vq37v | OK (TestIdempotencyConcurrentDuplicateSingleExecution) |
| bd-ws5-ship-or-cut-jv0rc.3 | OK (TestSchemaPaginationRegistryExhaustive) |
| bd-ws1-truth-safety-l5ddi.4 | OK (TestQueueDryIdeationParentDetectedFromTargetProjectEpic) |
| bd-km5ib | NO_PROOF_NAMED |
| bd-32ju2 | NO_PROOF_NAMED |
| bd-456fv | NO_PROOF_NAMED |
| bd-ws5-ship-or-cut-jv0rc.1.1 | NO_PROOF_NAMED |

## Completeness-language check (draft notes: /private/tmp/claude-501/-Users-jemanuel-projects-ntm/bebc8724-6a41-44a4-ad9c-e5f004972cd9/scratchpad/draft-notes-v1.25.0.md)
```
3:  convention.** (bd-ws3-contract-breadth-psvyu.2) Every robot envelope —
8:  every registered schema type and fails on any null-where-array, with an
19:    (`internal/robot/schema_pagination.go`) marks EVERY list-shaped schema
72:    `ntm rotate all-limited` (a pane at or above `critical_percent` is now
```

## Findings
- ledger: closed bead bd-ws3-contract-breadth-psvyu.5.1 names no Proof test
- ledger: closed bead bd-4we2d names no Proof test
- ledger: closed bead bd-km5ib names no Proof test
- ledger: closed bead bd-32ju2 names no Proof test
- ledger: closed bead bd-456fv names no Proof test
- ledger: closed bead bd-ws5-ship-or-cut-jv0rc.1.1 names no Proof test
- draft notes use completeness language (verify no open gap on those surfaces): see audit doc

## Verdict
- New NO_BEAD gaps found: file beads ON THE SPOT (label reality-bridge) and list IDs here.
- Release notes may not use completeness language for any surface with an open gap.
