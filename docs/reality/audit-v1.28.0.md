# Reality Audit — v1.28.0

generated: 2026-08-19T00:20:08Z by scripts/reality_audit.sh
seed: 223152316
allowlist-lines: 1606
gates: yes
total-minutes: 3 (budget 90)

## Step timings
- 1-checklist: 0m (budget 10m)
- 2-gates: 1m (budget 15m)
- 3-probes: 1m (budget 30m)
- 4-ledger: 2m (budget 20m)

## Checklist drift (vs docs/reality/checklist-v1.27.0.md)
```diff
<no drift>
```

## Allowlist burndown
```
      37 ci/allowlists/config.txt
      16 ci/allowlists/contracts.txt
    1595 ci/allowlists/deadcode.txt
       9 ci/allowlists/docs.txt
      16 ci/allowlists/placebo.txt
    1673 total
```

## Spot-probes (seeded from tag; exercise against the BUILT artifact, not the tree)
| # | claim row | verdict (WORKS / PARTIAL / NO_SURFACE / NO_BEAD) | evidence |
|---|-----------|--------------------------------------------------|----------|
| 1 | command_palette.md:124 git_commit | Git Commit | WORKS | `ntm palette --json` from repo root parses 54 commands; key `git_commit`, label "Git Commit", category "Git & Operations" present with a 293-char non-empty prompt matching the doc's `### git_commit | Git Commit` entry |
| 2 | docs/ORCHESTRATION_FEATURES.md:689 Solution Overview | WORKS | CASS auto-injection is live end-to-end: `send --help` documents `--with-cass`/`--no-cass` + `[cass.context]` config keys; a real `ntm send <session> --all --json` executed the injection pipeline (`cass_injection:{enabled:true,query,items_found,skipped_reason:"no relevant context found"}`); `ntm cass preview "rate limiting"` live-extracted keywords and reported 6 hits/0 after filtering; `--robot-send` accepts `--with-cass` (flag parsed, failed only on nonexistent session) |
| 3 | command_palette.md:99 Planning & Workflow | WORKS | palette --json carries all 7 documented section entries (combine_plans_into_hybrid, improve_beads, turn_plan_into_beads, use_bv, next_bead, work_on_your_beads, do_all_of_it) under category "Planning & Workflow", every prompt non-empty (209–1107 chars) |
| 4 | SKILL.md:105 Work Intelligence | WORKS | every doc command exists on the binary: `work triage` (`--by-track`, `--format=json|markdown|auto`, `--compact`), `work alerts`, `work search`, `work impact`, `work next` (+8 more subcommands beyond the doc); `worktrees merge`/`worktrees clean` both present with `--session` scoping |
| 5 | README.md:487 Project-Level | WORKS | overlay probed live in a hermetic temp git project: `.ntm/config.toml` with `[assign] operator_gated_labels=["audit-gate"]` surfaced in `config show` (merged over global); an overlay with unknown fields warned and fell back to global (internal/config/merge.go behavior, exit 0); all listed asset paths honored in code (`.ntm/workflows` workflow/loader.go:142, `.ntm/pipelines` config/project.go:334, `.ntm/recipes.toml` recipe/recipe.go:187, `.ntm/personas.toml` config/project.go:343) |
| 6 | README.md:190 2. Dispatch, Monitoring, and Recovery | WORKS | against a real throwaway tmux session: `health --json` full per-agent envelope; `activity --json` classified the claude-titled pane; `send --all` correctly fail-closed on the bare-shell agent pane with actionable `PANE_AGENT_DEAD` error naming `--robot-restart-pane`; `interrupt` sent Ctrl+C to 1 agent pane; all nine doc-block commands (`send/interrupt/activity/health/watch/extract/diff/grep/analytics`) exist and `grep`/`extract`/`analytics --days 7 --json` ran live |
| 7 | docs/ORCHESTRATION_FEATURES.md:970 Error Handling | WORKS | `pipeline lint` on a 3-step workflow accepts all three documented per-step values (`on_error: fail/continue/retry` — plus `fail_fast`) with "Validation: ok" exit 0; `on_error: bogus` rejected exit 1 with hint "Valid values: fail, fail_fast, continue, retry"; retry-N-times-with-delay real via `retry_count`/`retry_delay` (internal/pipeline/parser.go:590-603) |
| 8 | SKILL.md:48 Session Orchestration | WORKS | `list` and `status <session>` ran live against a real tmux session (pane roster with per-pane agent classification, "ntm send --pane=<N>" hint); `spawn --help` documents `--cc/--cod/--agy N[:model]`, `--label`, `--worktrees`; `add --help` documents `--cc=1:opus` and `--label` targeting; `view` exists |
| 9 | docs/ORCHESTRATION_FEATURES.md:609 File Conflict Detection | WORKS | `conflicts` live on the binary with the doc's git-based multi-agent heuristic (`--since`/`--limit`, help text matches "modified by multiple agents" approach); clean repo and dirty-repo-without-agent-activity both correctly report "No conflicts detected." exit 0; `conflicts --json` empty result is now `[]` — the bd-e1v97 null-token fix verifiably shipped in this release binary |
| 10 | command_palette.md:73 ensemble_status | Ensemble Status | WORKS | palette entry present (key `ensemble_status`, category "Ensemble", non-empty prompt) and the command it wraps is real on the ensemble_experimental build: `ensemble status --help` documents `--format=table|json|yaml` + `--show-contributions`; missing session returns a structured JSON error envelope (`success:false`, `error`, `hint`) exit 1 |

## Ledger audit (10 seeded closed beads; Proof test must exist in go test -list)

scope: closed since previous audit (2026-08-18T21:44:22Z) — post-discipline era only

| bead | proof status |
|------|--------------|
| bd-e1v97 | OK (TestChangesJSONEmptyIsArray) |
| bd-o82m7 | NO_PROOF_NAMED |
| bd-6otuk | OK (TestEnsembleKeysStayValid) |
| bd-6afgy | OK (TestFindGitRootCachesPerDirectory) |
| bd-m9zpa | OK (TestRESTBeadsCreateAndUpdate) |

## Completeness-language check (draft notes: not provided)
```
<none>
```

## Findings (triaged 2026-08-18, probed against release-parity binary /tmp/ntm-a28 = v1.28.0 pre-tag)
- ledger: bd-o82m7 names no live Proof test — **FALSE POSITIVE**: the surface is Homebrew distribution (Casks/ntm.rb in Dicklesworthstone/homebrew-tap), which has no Go test surface; the close names a concrete executed proof ("brew info dicklesworthstone/tap/ntm reports 1.27.0"), re-verified this audit against the upstream tap (raw Casks/ntm.rb `version "1.27.0"`; a stale local `brew info` shows 1.22.1 until `brew update`). Same scanner class as bd-43ydf last audit: the grep only follows `Test…` tokens. The promised process fix is real too — AGENTS.md:100 now carries the mandatory tap-update release step.

### New probe findings
- None. No beads needed: all 10 probes returned WORKS with zero doc drift to fix. Two prior-audit fixes were independently confirmed shipped in this binary: bd-e1v97 (`conflicts --json`/`changes --json` empty results now `[]`, not the bare `null` token) and the AGENTS.md tap-update step from bd-o82m7.
- Minor wart noted, not bead-worthy this cycle: `ensemble status <missing>` reports `error_code:"INTERNAL_ERROR"` where `--robot-send` reports the more precise `SESSION_NOT_FOUND` for the same condition.

## Verdict
- 10/10 spot-probes exercised against the built artifact (`go build -tags ensemble_experimental`, Version=1.28.0) with a hermetic NTM_CONFIG and a real throwaway tmux session (killed after): **10 WORKS, 0 PARTIAL, 0 BROKEN**. No regression found; the release binary additionally proves last audit's bd-e1v97 fix (empty-JSON `[]` envelope) live.
- The 1 scripted ledger finding (bd-o82m7 NO_PROOF_NAMED) triaged as a false positive with reasoning above; its distribution proof re-verified upstream. Reminder for the tag flow: the tap cask currently (correctly) sits at 1.27.0 and MUST be bumped to 1.28.0 per AGENTS.md:100 when this release ships.
- No new NO_BEAD gaps; no completeness-language restrictions carried into the v1.28.0 release notes from this audit.
