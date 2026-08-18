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
| 1 | docs/ORCHESTRATION_FEATURES.md:287 Health States | WORKS | all four documented states are the live vocabulary: `--robot-health=<session>` against a real throwaway tmux session returned `summary:{total,healthy,degraded,unhealthy,rate_limited,blocked}` and a shell pane titled `<session>__cc_1` (claude-typed, no agent process) was classified per-agent `"health":"unhealthy"` — exact strings set in internal/robot/health.go:904-937 |
| 2 | docs/ORCHESTRATION_FEATURES.md:598 Scope (Phase 1) | PARTIAL | all three Phase-1 tools live on the binary (`conflicts`/`diff`/`changes` help + live runs; `--robot-diff=X` returns the full files/agent_activity envelope; "No conflicts detected." exit 0) — but `conflicts --json` and `changes --json` print the bare token `null` for empty results (nil slice through output.PrintJSON, internal/cli/changes.go; present since ≤v1.26.0, not a regression) → bead bd-e1v97 |
| 3 | command_palette.md:26 Analysis & Review | WORKS | `ntm palette --json` from repo root parses 54 commands; category "Analysis & Review" carries all 7 documented entries (Fresh Review … Apply UBS) with zero empty prompts |
| 4 | README.md:582 Installation | PARTIAL | install-script path verified live (raw.githubusercontent.com install.sh HTTP 200; local script implements `--easy-mode`, install.sh:13,60); but the Homebrew path is 5 releases stale — `brew info dicklesworthstone/tap/ntm` resolves Casks/ntm.rb at **1.22.1**, not 1.27.0 (tap has no Formula/ntm.rb) → bead bd-o82m7 |
| 5 | docs/ORCHESTRATION_FEATURES.md:575 Integration with Health | PARTIAL | the headline behavior (skip unhealthy + rate-limited agents) is shipped as hard boolean exclusions (`exclude_if_rate_limited` default true; `AgentScorer.checkExclusion`, internal/robot/routing.go:874-891) — but the documented `-100`/`-50` score mechanics and the rate-limited "unless only option" fallback were never implemented; doc drift fixed in-place this audit |
| 6 | docs/ORCHESTRATION_FEATURES.md:169 API Design | PARTIAL | `--robot-activity=<session>` live against a real tmux session: claude-typed pane returned in `agents[]`, `--activity-type=claude` accepted, and the doc's "no pane filter" claim holds; but the documented JSON example had drifted (actual: `velocity` not `velocity_cps`, `summary:{total_agents,by_state}` not flat per-state counts, plus observation_*/output_sequence fields) — example updated in-place this audit |
| 7 | docs/ORCHESTRATION_FEATURES.md:272 Problem Statement | WORKS | Feature 2 "Shipped" status honest: `ntm health <session> --json` returned the full per-agent envelope (process_status/activity/issues/rate_limited/wait_seconds/progress) exit 0 on a real session; `--robot-health` + `--robot-health-oauth` in --robot-help; `[resilience]` is a live config section (internal/config/config.go:95) |
| 8 | SKILL.md:27 Quick Start | WORKS | quick-start commands all real on the built binary: `ntm deps -v` exit 0 rendering Required (tmux 3.6a ok)/AI Agents sections; `quick --help` documents `--template` (go, python, node, rust); `spawn --help` documents `--cc/--cod/--agy N[:model[:effort]]`; install.sh URL live (HTTP 200) |
| 9 | docs/ORCHESTRATION_FEATURES.md:981 API Design | WORKS | every documented pipeline verb exists (`run`/`status`/`list`/`cancel` + `resume`/`cleanup`); `pipeline run --help` documents `--var`/`--var-file`; hermetic `pipeline list --json` in a fresh temp project dir returned the proper envelope `{"success":true,...,"pipelines":[]}` |
| 10 | command_palette.md:122 Git & Operations | WORKS | palette --json loads both section entries — "Git Commit" and "Do GH Flow" — under category "Git & Operations", matching the doc's `## Git & Operations` header verbatim |

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

## Findings (triaged 2026-08-18, probed against release-parity binary /tmp/ntm-a = v1.27.0+audit-docs-only)
- ledger: bd-43ydf names no live Proof test — **FALSE POSITIVE**: the surface is a bash packaging script (scripts/build_vsix.sh); the close names a concrete executed proof (full build_vsix.sh run: npm ci from lockfile, tsc, vsce packaged the vsix, baked-version check passed). No Go test surface exists for it; the scanner only greps for `Test…` tokens.
- ledger: bd-w4fbk Proof test TestRESTBeadsCreateAndUpdate not in 'go test -list' — **FALSE POSITIVE, tooling gap**: the test exists (tests/integration/rest_integration_test.go:458) and lists fine under `NTM_INTEGRATION_TESTS=1`; scripts/reality_audit.sh builds TEST_LIST without that env, so the entire integration package skips ("0 tests executed") and every integration proof will false-flag. Filed bd-m9zpa to set the gate env when listing.
- ledger: bd-ws6-config-truth-ienmd names no live Proof test — **FALSE POSITIVE**: it is an epic rollup whose close delegates proof to its three children; child .3 appears in this same table with OK (TestCleanConfigLoadsSilently). The delegated-proof grep only follows "proof … bd-x" phrasing, which the rollup text doesn't use.

### New probe findings (beads filed on the spot, label reality-bridge)
- **bd-o82m7 (P1)** — Homebrew distribution stale: the tap's Casks/ntm.rb still ships **1.22.1** while README.md:582 and SKILL.md advertise `brew install dicklesworthstone/tap/ntm`; five releases (v1.23–v1.27) never reached the tap. Not a binary regression — the released v1.27.0 artifact itself probed clean — but the advertised install path silently delivers a 5-release-old ntm.
- bd-e1v97 (P2) — `ntm conflicts --json` / `ntm changes --json` emit bare `null` for empty results (nil slice, no envelope); longstanding (≤v1.26.0), contrast with `pipeline list --json` which returns the proper `{"success":true,…,"pipelines":[]}` envelope.
- bd-m9zpa (P2) — reality_audit.sh ledger TEST_LIST misses env-gated tests/integration (root cause of the bd-w4fbk false flag).
- Doc drift fixed in-place this audit (docs/ORCHESTRATION_FEATURES.md): Integration-with-Health `-100`/`-50` score fiction replaced with the real boolean exclusions; `--robot-activity` JSON example updated to the actual field names/summary shape.

## Verdict
- 10/10 spot-probes exercised against the built artifact with a hermetic NTM_CONFIG and a real throwaway tmux session (killed after): **6 WORKS, 4 PARTIAL, 0 BROKEN**. No regression found in the released v1.27.0 binary itself; every PARTIAL is either pre-existing (null-JSON wart), out-of-binary (stale brew cask), or doc drift now fixed in-place.
- All 3 scripted ledger findings triaged as false positives with reasoning above; one produced a real tooling fix bead (bd-m9zpa).
- New gaps filed on the spot (label reality-bridge): **bd-o82m7 (P1, stale Homebrew cask — most user-visible finding of this audit)**, bd-e1v97, bd-m9zpa.
- Release notes may not use completeness language for the Installation (Homebrew) surface or the conflicts/changes JSON surface until those beads close.
