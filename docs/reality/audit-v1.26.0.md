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
| 1 | docs/ORCHESTRATION_FEATURES.md:1084 Problem Statement | WORKS | Feature 7 "Partial" status is honest against the binary: `spawn --help` documents `--stagger duration[=1m30s]`/`--stagger-mode` (smart/fixed/none)/`--stagger-delay` + `--assign` exactly as claimed; `--robot-help` carries `--spawn-assign-work` but NO stagger flags — matching the section's explicit "NOT shipped" list |
| 2 | command_palette.md:46 apply_ubs \| Apply UBS | WORKS | `ntm palette --json` from repo root parses 54 commands incl. label "Apply UBS" in category "Analysis & Review" with the documented prompt text loaded verbatim from ./command_palette.md |
| 3 | docs/ORCHESTRATION_FEATURES.md:39 Smart Routing | WORKS | all three v2.0 bullets live: `[routing]` config exposes context_weight/state_weight/recency_weight (defaults 0.4/0.4/0.2 = the documented 40/40/20, internal/config/config.go:346 + config_test.go); sticky routing shipped as `ntm send --smart --route=sticky` (send.go:733) with persisted state (internal/state/routing_state.go); Agent Mail reservation scoring integrated in internal/coordinator/assign.go + internal/robot/route.go |
| 4 | command_palette.md:122 Git & Operations | WORKS | palette --json loads both section entries — "Git Commit" and "Do GH Flow" — with category "Git & Operations" matching the doc's `## Git & Operations` header |
| 5 | README.md:622 Troubleshooting | WORKS | the section's canonical remedy `ntm deps -v` exits 0 on the built binary rendering Required (tmux 3.6a ✓)/AI Agents/Recommended/Services/Flywheel sections; empty-pane guidance ("only launches tools discoverable in PATH") matches observed detection behavior |
| 6 | README.md:637 `claude`, `codex`, `agy`, `grok`, or `gemini` not detected over SSH / tmux / non-login shells | WORKS | claim probed directly: `env PATH=/usr/bin:/bin ntm deps -v` reports all five agent CLIs ✗ missing while the same binary with the login-shell PATH detects all five ✓ — PATH-of-runtime-environment behavior exactly as documented, and the wrapper fix (prepend PATH, exec ntm) is therefore sound |
| 7 | README.md:740 About Contributions | WORKS | prose-only policy statement (no outside contributions); contains no executable claims to break; the nearest command claim above it (`ntm openapi generate`, README.md:737) exists on the binary and its --help describes the same hermetic-router generation the doc implies |
| 8 | README.md:682 Pipeline resume or cleanup does not see the state you expect | WORKS | project-scoped-state claim verified: `ntm pipeline list --json` in a fresh temp project dir returns `{"success":true,"pipelines":[]}` (state resolved per project dir, `.ntm/pipelines/<run-id>.json` per internal/pipeline/state_persist_test.go); `pipeline resume`/`cleanup` subcommands exist as documented, cleanup validates flags loudly (`--older is required`) |
| 9 | docs/ORCHESTRATION_FEATURES.md:29 Activity Detection | WORKS | 2s hysteresis is real (`DefaultHysteresisDuration = 2 * time.Second`, internal/robot/activity.go:781, with PendingState/PendingSince flap tracking); `ntm wait --help` documents `--exit-on-error` (exit code 3) + composed conditions + `--any --count` partial waits; `ntm activity <session>` ran live against a throwaway tmux session (cleaned up) |
| 10 | docs/ORCHESTRATION_FEATURES.md:34 Health & Resilience | WORKS | `ntm health <session> --json` against a real throwaway tmux session returned the full envelope (process_status/activity/issues/rate_limited/progress) exit 0; soft-vs-hard restart distinction live in internal/robot/restart_pane.go (soft-restart PID detection + hard-kill escalation); context-loss notification wired (`notify_on_context_loss` toml key, ContextLoss alert via alerter, internal/robot/health.go:1642,1835-1842) |

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
(disposition added by the auditing agent; scripted findings preserved verbatim below)

- Guard suite failure — ROOT-CAUSED AND FIXED IN-TREE: only allowlist_selftest.sh failed ("well-formed allowlist rejected"); its fixture pinned bead bd-ws0-guards-klz98 as "valid open bead", but that epic closed at W0 completion, so check_allowlists.sh's closed-bead check (correctly) rejected the fixture — the guard was right, the fixture was stale. Fix: the selftest now discovers a live OPEN bead via `br list --status=open` (static shape-valid fallback for CI without br, matching check_allowlists.sh's documented CI degradation). `scripts/guards/run_all.sh` re-run: 5/5 guards OK, all canaries fire. Not a product finding — no bead needed, fixed on the spot per audit convention.
- Ledger NO_PROOF_NAMED x3 — VERIFIED HONESTLY, none is a fake close: bd-ws4-openapi-parity-wpwck.2 is a roll-up close whose proof lives in its children and is live (TestParityMatrixGeneratedFromRouter + TestParity* endpoint suite in `go test -list`; CI job `openapi-drift` in .github/workflows/ci.yml:180 regenerates and `git diff --exit-code`s; scripts/parity_harness.sh green this audit) — fails the name-grep because the close reason describes the gate rather than naming a Test verbatim. bd-igq0w (closed 2026-03-31) and bd-1d8qk (closed 2026-02-01) predate the proof-naming discipline entirely; bd-igq0w's deliverable (short test suite green in agent/audit/swarm) is re-verified by this release's gate runs, bd-1d8qk's (secret-like fixtures sanitized, push protection unblocked) is proven operationally by every push since. Hygiene, not fabrication — same disposition class as the v1.25.0 audit; the sampler refinement that scopes step 4 to closes since the previous audit is already filed as bd-yz3nb.
- --notes finding — SCRIPT USAGE ARTIFACT, resolved inline: reality_audit.sh expects `--notes <file path>` but was handed the draft-notes text itself; the completeness-language grep therefore ran against nothing. Re-run manually against the notes text below and the [Unreleased] CHANGELOG section: the only completeness-shaped phrases are "allowlists reduced to backlog+permanent composition only" (machine-checked by check_allowlists.sh + the W4 gate counts, not aspiration) and the deletion totals (measured `wc -l` facts). No completeness claim over a surface with an open gap.

Draft release notes (inlined verbatim, was the --notes argument):
> R3 deletion & config-truth release: ~13,700 lines of dead sophistication removed under operator sign-off; 37 reader-less config knobs enter staged deprecation warnings; allowlists reduced to backlog+permanent composition only

Scripted findings (verbatim):
- guard suite (scripts/guards/run_all.sh) failed
- ledger: closed bead bd-ws4-openapi-parity-wpwck.2 names no Proof test
- ledger: closed bead bd-igq0w names no Proof test
- ledger: closed bead bd-1d8qk names no Proof test
- --notes file not found: R3 deletion & config-truth release: ~13,700 lines of dead sophistication removed under operator sign-off; 37 reader-less config knobs enter staged deprecation warnings; allowlists reduced to backlog+permanent composition only

## Verdict
PASS with findings, all dispositioned.
- Spot-probes: 10/10 sampled claim rows verdict WORKS against the built release-style artifact (go build -tags ensemble_experimental, stamped 1.26.0); probes ran hermetically (temp NTM_CONFIG, throwaway tmux session created and cleaned up); no stale-doc gap found this round.
- New NO_BEAD gaps: none. No beads filed this run — the single actionable finding (stale selftest fixture) was infra drift fixed on the spot; the ledger findings are pre-discipline hygiene already covered by bd-yz3nb.
- Guard suite: FAILED at script time, root-caused to a stale selftest fixture (not a gate regression), fixed, re-run 5/5 OK.
- Ledger: 7/10 sampled beads name a live Proof test verbatim; the other 3 verified by hand (see Findings) — hygiene, not fabrication.
- Completeness language: draft notes (inlined above after the --notes usage artifact) contain no completeness claim over a surface with an open gap; the "backlog+permanent composition only" claim is enforced by check_allowlists.sh, and allowlist composition was independently verified by the W4 gate.
- Time-box: 2 scripted minutes + ~40 operator-minutes of probing/verification — well inside the 90-minute budget.
