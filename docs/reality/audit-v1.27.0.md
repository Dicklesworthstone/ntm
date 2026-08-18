# Reality Audit — v1.27.0

generated: 2026-08-18T21:44:22Z by scripts/reality_audit.sh
seed: 3894767325
allowlist-lines: 1744
gates: yes
total-minutes: 2 (budget 90)

## Step timings
- 1-checklist: 0m (budget 10m)
- 2-gates: 1m (budget 15m)
- 3-probes: 1m (budget 30m)
- 4-ledger: 1m (budget 20m)

## Checklist drift (vs docs/reality/checklist-v1.26.0.md)
```diff
<no drift>
```

## Allowlist burndown
```
     162 ci/allowlists/config.txt
      16 ci/allowlists/contracts.txt
    1596 ci/allowlists/deadcode.txt
       9 ci/allowlists/docs.txt
      16 ci/allowlists/placebo.txt
    1799 total
```

## Spot-probes (seeded from tag; exercise against the BUILT artifact, not the tree)
| # | claim row | verdict (WORKS / PARTIAL / NO_SURFACE / NO_BEAD) | evidence |
|---|-----------|--------------------------------------------------|----------|
| 1 | docs/ORCHESTRATION_FEATURES.md:287 Health States | UNPROBED | |
| 2 | docs/ORCHESTRATION_FEATURES.md:598 Scope (Phase 1) | UNPROBED | |
| 3 | command_palette.md:26 Analysis & Review | UNPROBED | |
| 4 | README.md:582 Installation | UNPROBED | |
| 5 | docs/ORCHESTRATION_FEATURES.md:575 Integration with Health | UNPROBED | |
| 6 | docs/ORCHESTRATION_FEATURES.md:169 API Design | UNPROBED | |
| 7 | docs/ORCHESTRATION_FEATURES.md:272 Problem Statement | UNPROBED | |
| 8 | SKILL.md:27 Quick Start | UNPROBED | |
| 9 | docs/ORCHESTRATION_FEATURES.md:981 API Design | UNPROBED | |
| 10 | command_palette.md:122 Git & Operations | UNPROBED | |

## Ledger audit (10 seeded closed beads; Proof test must exist in go test -list)

scope: closed since previous audit (2026-08-17T21:52:51Z) — post-discipline era only

| bead | proof status |
|------|--------------|
| bd-43ydf | NO_PROOF_NAMED |
| bd-v394r | OK (TestAssignWorkSelectsByScoreNotFirstFit) |
| bd-w4fbk | PROOF_MISSING (TestRESTBeadsCreateAndUpdate not in go test -list) |
| bd-ws6-config-truth-ienmd | NO_PROOF_NAMED |
| bd-2c0yh | OK (TestDefaultSpecsCMUsesMCPProbe) |
| bd-88um4 | OK (TestRoundRobin_CursorRobustnessTopologies) |
| bd-izuqq | OK (TestDetectConflicts_StoredConflictsDoNotShareHolderArrays) |
| bd-2rtl8 | OK (TestAffinityProjectKeyPreference) |
| bd-ws6-config-truth-ienmd.3 | OK (TestCleanConfigLoadsSilently) |
| bd-qlfb2 | OK (TestDocsExamplesCanary) |

## Completeness-language check (draft notes: not provided)
```
<none>
```

## Findings
- ledger: closed bead bd-43ydf names no live Proof test (post-discipline close must name a Test function verbatim or a followable delegated-proof bead)
- ledger: closed bead bd-w4fbk Proof test TestRESTBeadsCreateAndUpdate not found by 'go test -list'
- ledger: closed bead bd-ws6-config-truth-ienmd names no live Proof test (post-discipline close must name a Test function verbatim or a followable delegated-proof bead)

## Verdict
- New NO_BEAD gaps found: file beads ON THE SPOT (label reality-bridge) and list IDs here.
- Release notes may not use completeness language for any surface with an open gap.
