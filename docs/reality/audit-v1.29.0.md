# Reality Audit — v1.29.0

generated: 2026-08-20T01:17:47Z by scripts/reality_audit.sh
seed: 3508572683
allowlist-lines: 216
gates: yes
total-minutes: 3 (budget 90)

## Step timings
- 1-checklist: 0m (budget 10m)
- 2-gates: 1m (budget 15m)
- 3-probes: 1m (budget 30m)
- 4-ledger: 2m (budget 20m)

## Checklist drift (vs docs/reality/checklist-v1.28.0.md)
```diff
<no drift>
```

## Allowlist burndown
```
      37 ci/allowlists/config.txt
      16 ci/allowlists/contracts.txt
     218 ci/allowlists/deadcode.txt
       9 ci/allowlists/docs.txt
      10 ci/allowlists/placebo.txt
     290 total
```

## Spot-probes (seeded from tag; exercise against the BUILT artifact, not the tree)
| # | claim row | verdict (WORKS / PARTIAL / NO_SURFACE / NO_BEAD) | evidence |
|---|-----------|--------------------------------------------------|----------|
| 1 | SKILL.md:245 Project Resolution | WORKS | probed hermetically (temp `NTM_CONFIG`, temp `projects_base`): `config get projects_base` returns the configured path exit 0; `ntm quick a29audit --no-session` created the project under `projects_base/<name>` (git init, .claude, .vscode, .gitignore) exactly as documented; `quick --help` carries `--template` (go, python, node, rust); label naming real — `spawn --help` documents `PROJECT--label` session naming and spawn.go:1768 rejects `--` in project names as the reserved separator (bd-1933u) |
| 2 | docs/ORCHESTRATION_FEATURES.md:693 How It Works | WORKS | the injection pipeline of the diagram is live: `ntm cass preview "rate limiting"` extracted keywords (`rate, limiting`), queried CASS, and reported "6 hits found, 0 after filtering" (relevance threshold step real); `send --help` documents `--with-cass` (inject context above prompt, degrades gracefully) + `--no-cass` + the full `[cass.context]` knob list (enabled/max_sessions/lookback_days/max_tokens/min_relevance/skip_if_context_above/prefer_same_project) covering diagram steps 1–5 |
| 3 | README.md:208 3. Work Graph Triage and Assignment | WORKS | all doc commands on the binary: `work` exposes triage/alerts/search/impact/next/graph (+8 more); live `ntm work next` in this repo returned a real bv recommendation (ntm-5171); live `work triage --format=json` returned the triage envelope (needed `NTM_BV_TIMEOUT=120` — raw `bv --robot-triage` takes ~31s on this 1600+-bead graph vs the 30s default; knob documented in internal/bv/timeout.go, GH#253); `assign --help` lists `--strategy=dependency` verbatim plus balanced/speed/quality/round-robin; hermetic `[assign] operator_gated_labels=["audit-gate"]` surfaced in `config show` merged output |
| 4 | docs/ORCHESTRATION_FEATURES.md:1047 Dependency Graph | WORKS | every node in the diagram resolves to a live surface on the built binary: Feature 1 `activity`, Feature 2 `health`, Feature 3 `send --smart/--route`, Feature 4 `summary`/`diff`, Feature 5 CASS injection (row 2), Feature 6 `pipeline`; the adjacent Feature 7 "Partial" status line is honest as written — `spawn --help` has 10 stagger mentions, `--robot-spawn`'s `--spawn-*` flag set has zero stagger flags, and no `[spawn]` table appears in `config show` |
| 5 | command_palette.md:34 randomly_inspect_code | Randomly Inspect Code | WORKS | `ntm palette --json` parses 54 commands; key `randomly_inspect_code`, label "Randomly Inspect Code", category "Analysis & Review" (matching the doc's section heading), 732-char prompt whose first 120 chars match the doc's prompt verbatim |
| 6 | docs/ORCHESTRATION_FEATURES.md:601 Scope (Phase 1) | WORKS | all three Phase-1 scope items live against a real throwaway tmux session: (1) conflict detection — `conflicts a29audit` "No conflicts detected." exit 0, `--json` empty result `[]`; `--robot-diff=a29audit --since=10m` returned the documented envelope (timeframe.since/since_ts, files.modified, and a real `potential_conflicts` entry with `likely_modifiers` pane IDs + "concurrent modification" reason — the git-based multi-agent heuristic working); (2) output comparison — `diff` present; (3) activity summary — `summary --since 30m` documented, `activity --json` ran live |
| 7 | docs/ORCHESTRATION_FEATURES.md:1188 Coordinator Toggle Persistence | WORKS | probed against a comment-bearing hermetic config: `coordinator enable auto-assign`, `enable digest --interval=30m`, `disable conflict-negotiate` each printed "Persisted to <file>" and the file afterward kept the operator comments and `[assign]` section intact while gaining a `[coordinator]` table with auto_assign=true, send_digests=true, digest_interval="30m", conflict_negotiate=false; root dotted form honored (`coordinator.auto_assign = false` updated in place to `true`, no table rewrite); inline whole-section form refused exactly as documented ("cannot surgically update a whole-section or parent inline value") with the file byte-identical after |
| 8 | README.md:532 Recoverable State | WORKS | each named surface has a live recovery path on the binary: sessions `resume` + `checkpoint restore` (a real `checkpoint save` against the throwaway session created 20260819-212545.694-d741 and `checkpoint list` replayed it); pipelines `pipeline resume`/`pipeline list` ("No pipelines tracked." clean empty state); attention feed cursor replay via `--robot-events --since-cursor` and `--robot-digest` (live digest returned cursor_start/cursor_end/event_count with the session's own health event); approvals `approve list` ("No pending approvals"); history `history search` + `timeline list/show` clean empty states exit 0 |
| 9 | docs/ORCHESTRATION_FEATURES.md:471 Feature 3: Smart Work Distribution | WORKS | the "Status: Shipped" line is accurate: `send --help` documents `--smart` + `--route` with the exact strategy list claimed (least-loaded, first-available, round-robin, round-robin-available, random, sticky, explicit — all 7, matching "beyond this design"); `--robot-route` and `--robot-assign` both on the root flag set; live `--robot-route=a29audit` returned the structured envelope (strategy:"least-loaded", candidates:[], `_agent_hints` with actionable suggestions) against the agent-less throwaway session |
| 10 | SKILL.md:125 Coordination and Recovery | PARTIAL→WORKS (drift fixed) | 17 of the 18 doc-block commands real and probed: `mail inbox`/`locks list` exist and fail loud+actionable when Agent Mail is unreachable (HTTP 401 surfaced verbatim, matching the doc's own external-process caveat); `coordinator status/digest` ran live against the throwaway session; `checkpoint save/list/restore`, `timeline list/show`, `history search`, `audit show`, `resume` all live; force-release approval gate real in code (locks.go:1106 `automation.force_release` knob → approval engine, `approve list` live). The 1 miss: `ntm changes conflicts myproject` — `changes` takes only `[session]` and never had a `conflicts` subcommand (git -S over internal/cli confirms); conflict detection is top-level `ntm conflicts`. Doc drift FIXED in place this audit (SKILL.md:145, README.md:401, references/COMMANDS.md:148 → `ntm conflicts …`) |

## Ledger audit (10 seeded closed beads; Proof test must exist in go test -list)

scope: closed since previous audit (2026-08-19T00:20:08Z) — post-discipline era only

| bead | proof status |
|------|--------------|
| bd-1bdvy | NO_PROOF_NAMED |
| bd-g1-deadcode-backlog-2ijy8 | OK (TestBVClientIsAvailableContextDoesNotCacheCanceledProbe) |
| bd-gjo4k | OK (TestLoadArchiveOutputsSinceFilter) |
| bd-jio7h | NO_PROOF_NAMED |
| bd-d2uxt | OK (TestApprovalEndpointsFailClosedWithoutStateStore) |
| bd-uh7la | OK (TestClassifyRobotExecuteErrorCuratedMisGuessHints) |
| bd-lzu08 | OK (TestTokenCorpusFixturesMatchCurrentStructs) |
| bd-obkeb | OK (TestGetSkillMatchedWorkItemsNoSignalIsLoud) |
| bd-ad54k | OK (TestDeprecatedKnobMultiKeyOneError) |
| bd-pcssq | PROOF_MISSING (TestRealPrefixCollisionSessionTargets not in go test -list) |

## Completeness-language check (draft notes: not provided)
```
<none>
```

## Findings (triaged 2026-08-19, probed against release-parity binary /tmp/ntm-a29 = v1.29.0 pre-tag)
- ledger: bd-1bdvy names no live Proof test — **PROOF EXISTS, close-note wording tripped the scanner**: the close names the test file (internal/cli/kill_mail_cleanup_test.go) but no verbatim `Test…` token; the proofs are real and in `go test -list` (TestCleanupAgentMailOnKill_ReleasesSessionAgents, _MailDown, _NoRegistry). Fixed by appending the verbatim names as a bead comment on bd-1bdvy.
- ledger: bd-jio7h names no live Proof test — **same class**: close says "7 new tests in internal/pipeline/parallel_ready_steps_test.go" without a verbatim name; all 7 (TestParallelReadySteps_DiamondOverlap et al.) are in `go test -list`. Fixed by appending the verbatim names as a bead comment on bd-jio7h.
- ledger: bd-pcssq TestRealPrefixCollisionSessionTargets not found by `go test -list` — **FALSE POSITIVE**: the test lives in internal/tmux/prefix_real_test.go behind `//go:build integration` (it needs a real tmux server), so the untagged scan can't see it. Re-verified this audit: `go test -tags integration -list` finds it and `go test -tags integration -run TestRealPrefixCollisionSessionTargets ./internal/tmux/` PASSES against real tmux 3.6a. Scanner improvement candidate: also list with the `integration` tag before flagging PROOF_MISSING. Comment noting this appended to bd-pcssq.

### New probe findings
- Doc drift, fixed in place (row 10): `ntm changes conflicts <session>` documented in three places but never implemented (`changes` accepts only `[session]`; `git log -S` finds no such subcommand ever in internal/cli). Corrected to `ntm conflicts <session>` in SKILL.md:145, README.md:401, references/COMMANDS.md:148. No bead needed — pure doc fix, verified against the built binary.
- Confirmed live, not a regression: the v1.29.0 deprecated-key flip is real on this binary — a global config still carrying `accounts.auto_rotate`/`accounts.reset_buffer_minutes` now hard-errors with the promised migration pointer to the v1.28.0 CHANGELOG table. Operators upgrading must clean their config; the error text names each key and the fix.
- Minor wart, not bead-worthy: `work triage` on a very large graph (this repo, ~31s raw `bv --robot-triage`) can exceed the 30s default bv timeout and the INTERNAL_ERROR hint ("Retry the command or inspect ntm diagnostics") doesn't mention the existing escape hatches (`NTM_BV_TIMEOUT`, `[integrations.bv] timeout_seconds`). Timeout knobs themselves work (probed with NTM_BV_TIMEOUT=120 → success).

## Verdict
- 10/10 spot-probes exercised against the built artifact (`go build -tags ensemble_experimental`, Version=1.29.0) with a hermetic NTM_CONFIG and a real throwaway tmux session (killed after): **9 WORKS, 1 PARTIAL-now-WORKS** (row 10's phantom `changes conflicts` doc drift fixed in-tree this audit). No code regression found; coordinator toggle persistence (comments/dotted-form/inline-refusal), the conflicts `[]` empty-JSON envelope, and the deprecation flip all verified live on the release-parity binary.
- All 3 scripted ledger findings triaged: 2 are close-note wording gaps with real, listed, passing proofs (verbatim names now appended to bd-1bdvy and bd-jio7h); 1 is a scanner false positive (bd-pcssq's proof is integration-tagged and passes against real tmux). No new NO_BEAD gaps; no beads filed because no finding survived as a product defect.
- No completeness-language restrictions carried into the v1.29.0 release notes from this audit. Tag-flow reminder inherited from v1.28.0: bump the Homebrew tap cask to 1.29.0 per AGENTS.md:100 when this release ships.
