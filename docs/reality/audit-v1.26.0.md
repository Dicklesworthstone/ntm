# Reality Audit — v1.26.0

generated: 2026-08-17T21:52:51Z by scripts/reality_audit.sh
seed: 877196394
allowlist-lines: 2554
gates: no
total-minutes: 2 (budget 90)

## Step timings
- 1-checklist: 0m (budget 10m)
- 2-gates: 1m (budget 15m)
- 3-probes: 1m (budget 30m)
- 4-ledger: 1m (budget 20m)

## Checklist drift (vs docs/reality/checklist-v1.25.0.md)
```diff
<no drift>
```

## Allowlist burndown
```
     415 ci/allowlists/config.txt
      16 ci/allowlists/contracts.txt
    2120 ci/allowlists/deadcode.txt
      21 ci/allowlists/docs.txt
      35 ci/allowlists/placebo.txt
    2607 total
```

## Spot-probes (seeded from tag; exercise against the BUILT artifact, not the tree)
| # | claim row | verdict (WORKS / PARTIAL / NO_SURFACE / NO_BEAD) | evidence |
|---|-----------|--------------------------------------------------|----------|
| 1 | docs/ORCHESTRATION_FEATURES.md:1084 Problem Statement | UNPROBED | |
| 2 | command_palette.md:46 apply_ubs | Apply UBS | UNPROBED | |
| 3 | docs/ORCHESTRATION_FEATURES.md:39 Smart Routing | UNPROBED | |
| 4 | command_palette.md:122 Git & Operations | UNPROBED | |
| 5 | README.md:622 Troubleshooting | UNPROBED | |
| 6 | README.md:637 `claude`, `codex`, `agy`, `grok`, or `gemini` not detected over SSH / tmux / non-login shells | UNPROBED | |
| 7 | README.md:740 About Contributions | UNPROBED | |
| 8 | README.md:682 Pipeline resume or cleanup does not see the state you expect | UNPROBED | |
| 9 | docs/ORCHESTRATION_FEATURES.md:29 Activity Detection | UNPROBED | |
| 10 | docs/ORCHESTRATION_FEATURES.md:34 Health & Resilience | UNPROBED | |

## Ledger audit (10 seeded closed beads; Proof test must exist in go test -list)
| bead | proof status |
|------|--------------|
| bd-fresh-eyes-audit-2026-07-yll4m.2 | OK (TestPersistNormalizedProjection_ReplacesRows) |
| bd-ws4-openapi-parity-wpwck.2 | NO_PROOF_NAMED |
| bd-68vr1 | OK (TestSortConflictsByLastAtThenPath_DeterministicWithTies) |
| bd-igq0w | NO_PROOF_NAMED |
| bd-y9ndb | OK (TestSessionMatchesWorktree) |
| bd-1d8qk | NO_PROOF_NAMED |
| bd-yd10o | OK (TestModelRefreshOllamaPSIfNeeded_Success) |
| bd-fresh-eyes-audit-2026-07-yll4m.5 | OK (TestAuthenticateRequestGrantsRoleForSharedCredentials) |
| bd-y9z33 | OK (TestOrchestrator_StartNewAgentSession) |
| bd-ws1-truth-safety-l5ddi.3 | OK (TestMetricsSnapshotList_SaveTwoThenListJSON) |

## Completeness-language check (draft notes: R3 deletion & config-truth release: ~13,700 lines of dead sophistication removed under operator sign-off; 37 reader-less config knobs enter staged deprecation warnings; allowlists reduced to backlog+permanent composition only)
```
<none>
```

## Findings
- guard suite (scripts/guards/run_all.sh) failed
- ledger: closed bead bd-ws4-openapi-parity-wpwck.2 names no Proof test
- ledger: closed bead bd-igq0w names no Proof test
- ledger: closed bead bd-1d8qk names no Proof test
- --notes file not found: R3 deletion & config-truth release: ~13,700 lines of dead sophistication removed under operator sign-off; 37 reader-less config knobs enter staged deprecation warnings; allowlists reduced to backlog+permanent composition only

## Verdict
- New NO_BEAD gaps found: file beads ON THE SPOT (label reality-bridge) and list IDs here.
- Release notes may not use completeness language for any surface with an open gap.
