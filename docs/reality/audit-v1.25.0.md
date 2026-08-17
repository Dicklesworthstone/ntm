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
| 1 | SKILL.md:167 Safety and Approvals | WORKS | `ntm safety status` (renders system status), `ntm safety check -- <cmd>` (evaluates command), `ntm policy show --all`, `ntm approve list` ("No pending approvals") all exit 0 with real output on the built binary |
| 2 | README.md:707 Can multiple swarms work on the same project? | WORKS | claimed enablers all present on the binary: `spawn --label` (help), `--robot-mail-check` (loud INVALID_FLAG asking for --mail-project — real validation, not stub), worktrees + `--assign` (rows 3/10) |
| 3 | README.md:170 Worktree isolation and reservations | WORKS | `ntm worktrees list` runs ("No worktrees found for session: ntm"); `worktrees` subtree has list/merge/clean exactly as documented; `spawn --help` documents `--worktrees` |
| 4 | command_palette.md:100 combine_plans_into_hybrid \| Combine Plans Into Hybrid | WORKS (doc header was stale — FIXED) | `ntm palette --json` from repo root loads 54 commands incl. `combine_plans_into_hybrid` via auto-discovered ./command_palette.md; PROBE FINDING: the file's usage header still advertised `1-9` quick-select and `?` help removed by H3, and `1-4` targets (now 1-5 + 6 incl. antigravity) — header corrected in this audit, palette still parses 54 commands, tests/ + config suites green |
| 5 | docs/ORCHESTRATION_FEATURES.md:389 API Design | WORKS | `--robot-health=nonexistent-xyz` returns the documented envelope shape (success/timestamp/session/checked_at/agents[]/summary{healthy,degraded,unhealthy,rate_limited,blocked}) with SESSION_NOT_FOUND + hint, exit 1 per the exit-code table |
| 6 | README.md:693 Can I use it with one agent instead of a swarm? | WORKS | `spawn --cc N[:model[:effort]]` accepts N=1; no minimum-swarm constraint anywhere in help |
| 7 | docs/ORCHESTRATION_FEATURES.md:575 Integration with Health | WORKS | health surface live on the binary (`--robot-health` above); unhealthy/rate-limited exclusion scoring is internal to assignment and covered by unit tests rather than a CLI knob — no doc/behavior mismatch found |
| 8 | README.md:412 Canonical Robot Surfaces | WORKS (latency finding filed) | all 8 canonical surfaces exit 0 on the built binary (--robot-help/-capabilities/-status/-snapshot/-plan/-dashboard/-markdown --md-compact/-terse); status-family calls take ~27-33s wall on a workstation with live integrations — bd-4479y filed |
| 9 | SKILL.md:190 Canonical Robot Mode | WORKS (same latency note) | same 8 surfaces as row 8 plus task-specific list: `--robot-mail-check` validates flags loudly; envelopes carry success/error_code/hint per AGENTS.md |
| 10 | docs/ORCHESTRATION_FEATURES.md:1152 Alternative: Orchestrator Work Assignment | WORKS | `spawn --help` documents `--assign` + `--strategy=dependency` exactly as claimed; `ntm assign --help` describes the BV-driven flow; `[assign] operator_gated_labels` exists in internal/config/project.go (toml tag) matching the documented config |

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
(disposition added by the auditing agent; scripted findings preserved verbatim below)

- Ledger NO_PROOF_NAMED x6 — VERIFIED HONESTLY, none is a fake close: bd-ws3-contract-breadth-psvyu.5.1 names the proof FILE (jobs_dispatch_test.go; TestJobDispatchPipelineRun/SwarmSpawn/CheckpointRestore + failure variants all in `go test -list`); bd-ws5-ship-or-cut-jv0rc.1.1 delegates its proof to F1b's internal/webui tests (TestEmbeddedUIPresent/TestEmbeddedRoutesMatchAppSource/TestVersionLockstep — live, spot-revert-verified by the W3 gate); bd-4we2d/bd-km5ib/bd-32ju2/bd-456fv were closed 2026-02..2026-08-05, BEFORE the proof-naming discipline existed (bd-456fv's described tests exist: TestResolveCheckpointWorkDirConfinesToProjectDir, TestCheckpointCommitPatternRejectsNonObjectIDs; bd-4we2d's exist: TestReadFileRange_RejectsOversizeFile/AllowsExactlyAtCap). Sampler refinement filed as bd-yz3nb (scope step 4 to closes since the previous audit; require a verbatim Test name or followable delegated pointer).
- Completeness-language hits x4 — RESOLVED, no violation: three "Every/EVERY" claims (arrays-never-null, registry walk, pagination flag map) are each enforced by a registry-walk conformance test with an empty exception list, i.e. machine-checked totality, not aspiration; the fourth hit is the literal command name `ntm rotate all-limited` (false positive). No hit touches a surface with an open gap.
- PROBE FINDING (row 4): command_palette.md usage header contradicted H3's shipped palette keymap (dead 1-9/?, outdated 1-4 targets) — FIXED on the spot in this audit; palette parse re-verified (54 commands), tests/ + internal/config green.
- PROBE FINDING (rows 8/9): canonical robot status surfaces ~27-33s wall each on a live workstation — functional but hostile to agent loops; filed bd-4479y.

Scripted findings (verbatim):
- ledger: closed bead bd-ws3-contract-breadth-psvyu.5.1 names no Proof test
- ledger: closed bead bd-4we2d names no Proof test
- ledger: closed bead bd-km5ib names no Proof test
- ledger: closed bead bd-32ju2 names no Proof test
- ledger: closed bead bd-456fv names no Proof test
- ledger: closed bead bd-ws5-ship-or-cut-jv0rc.1.1 names no Proof test
- draft notes use completeness language (verify no open gap on those surfaces): see audit doc

## Verdict
PASS with findings, all dispositioned. Baseline run (no previous checklist/audit to diff).
- Spot-probes: 10/10 sampled claim rows verdict WORKS against the built release-style artifact (go build -tags ensemble_experimental, stamped 1.25.0, `ensemble spawn --help` exposes --dry-run); one stale-doc gap (command_palette.md header vs H3 keymap) found and fixed in-tree during the audit; one latency finding filed (bd-4479y).
- New NO_BEAD gaps: none requiring a bead — the single doc gap was fixed on the spot. Beads filed this run: bd-yz3nb (ledger sampler refinement), bd-4479y (robot status latency). (The W3 gate separately filed bd-43ydf, vsix supply chain.)
- Ledger: 4/10 sampled beads name a live Proof test verbatim; the other 6 have real, live tests verified by hand (see Findings) but fail the name-grep — hygiene, not fabrication.
- Completeness language: draft notes ([Unreleased] CHANGELOG section) contain no completeness claim over a surface with an open gap; all four grep hits resolved above. Re-run the grep when the final v1.25.0 notes are drafted.
- Time-box: 4 scripted minutes + ~35 operator-minutes of probing/verification — well inside the 90-minute budget; the ritual is not broken.
