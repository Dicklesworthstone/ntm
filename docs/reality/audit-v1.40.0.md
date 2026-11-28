# Reality Audit — v1.40.0

generated: 2026-11-28T00:27:45Z by scripts/reality_audit.sh
seed: 1179245615
allowlist-lines: 115
gates: yes
total-minutes: 3 (budget 90)

## Step timings
- 1-checklist: 0m (budget 10m)
- 2-gates: 3m (budget 15m)
- 3-probes: 1m (budget 30m)
- 4-ledger: 1m (budget 20m)

## Checklist drift (vs docs/reality/checklist-v1.39.0.md)
```diff
94a95
> Pipeline recovery
118a120
> Session handoff recovery
```

## Allowlist burndown
```
   14 ci/allowlists/binary_bloat.txt
    7 ci/allowlists/config.txt
   16 ci/allowlists/contracts.txt
  105 ci/allowlists/deadcode.txt
    9 ci/allowlists/docs.txt
    6 ci/allowlists/openapi_drift.txt
   10 ci/allowlists/placebo.txt
   36 ci/allowlists/webui_dist.txt
  203 total
```

## Spot-probes (seeded from tag; exercise against the BUILT artifact, not the tree)
| # | claim row | verdict (WORKS / PARTIAL / NO_SURFACE / NO_BEAD) | evidence |
|---|-----------|--------------------------------------------------|----------|
| 1 | docs/ORCHESTRATION_FEATURES.md:1266 Historical Proposal: Soft-Claim Files | WORKS | Advisory helpers respond on the built binary: `ntm claim list --json` (`"success":true,"claims":[]`) and `ntm claim check bd-probe1 --json` (`"success":true`). |
| 2 | README.md:1978 Homebrew | WORKS | `Dicklesworthstone/homebrew-tap` `Formula/ntm.rb` and `Casks/ntm.rb` (version `1.39.0` at audit time): all four `url`/`sha256` pairs (darwin and linux, amd64 and arm64) match the v1.39.0 `SHA256SUMS`, and the archives carry `ntm` at the root (`bin.install "ntm"`, cask `binary "ntm"`). The 1.40.0 bump follows publication (AGENTS.md release step 3). |
| 3 | docs/ORCHESTRATION_FEATURES.md:825 Assignment-time injection | PARTIAL | `ntm assign --help` and `ntm coordinator assign --help` on the built binary list `--with-cass`, `--no-cass` and `--with-memory` as documented. Enrichment before the durable intent is proven by `TestExecuteAssignmentsEnhancedEnrichesPromptBeforeRecordingIntent`, `TestDirectPaneAssignWithCASSRecoveryReplaysRecordedPrompt` and `TestBulkAssignWithCASSAndMemoryEnrichesPromptAndReplaysWithoutRequery` (all pass at this commit), but the probe environment had no CASS index, so injection was not exercised through the built binary. |
| 4 | README.md:2099 Does NTM preserve history and state? | WORKS | Against live session `rprobe--alpha`: `ntm checkpoint save rprobe--alpha -m reality-probe --no-git --json` wrote checkpoint `20261128-002327.408-ae21e585081c129b`, which `ntm checkpoint list --json` returns (`"success":true`). `ntm history --json` and `ntm timeline list --json` answer (`total_count` 0 in a fresh HOME); `ntm audit list --json` lists the global audit logs the probe's own commands wrote. |
| 5 | README.md:1688 Configuration and Project Assets | WORKS | In a fresh HOME: `ntm config init` created `~/.config/ntm/config.toml`, `ntm config path` prints it, `ntm config get projects_base` returns the configured base, and `ntm config show --json` emits the merged configuration. |
| 6 | README.md:1700 Project-Level | WORKS | `ntm config project init` created `.ntm/` with `config.toml`, `personas.toml`, `palette.md`, `pipelines/` and `templates/`; `ntm config diff --json` reports 7 differences from defaults. A persona appended to `.ntm/personas.toml` appears in `ntm personas list --json` next to the five built-ins. |
| 7 | docs/ORCHESTRATION_FEATURES.md:69 The Vision | WORKS | `ntm --robot-capabilities --robot-format=json` lists 178 commands with a surface for each "Future NTM" item: `activity` and `agent-health` (agent states), `route`, `restart-pane` and `diagnose` (recovery), `ensemble-synthesize` (synthesis), `cass-search` and `cass-context` (history), `pipeline-run`, `pipeline-status` and `workflows`. |
| 8 | docs/ORCHESTRATION_FEATURES.md:327 Automatic Recovery | PARTIAL | Against live session `rprobe--alpha`: `ntm health rprobe--alpha --json` (`"overall_status":"ok"`, 1 healthy) and `ntm health rprobe--alpha --auto-restart-stuck --dry-run --json` (`"stuck_panes":[]`, `"restarted":[]`, `"dry_run":true`). The built-binary restart test `TestE2EHealthAutoRestartUsesEffectiveAgentConfig` fails in the release-gate environment at both v1.39.0 and this commit: Agent Mail warnings precede the JSON document (see Verdict). |
| 9 | README.md:2095 Can multiple swarms work on the same project? | WORKS | `ntm create rprobe --label=alpha --panes=1 --json` and `--label=beta` created `rprobe--alpha` and `rprobe--beta` for one project; `ntm list --json` returns both (`"count":2`, labels `alpha` and `beta`). Agent Mail and file reservations were not exercised (the probe environment points Agent Mail at a closed port). |
| 10 | docs/ORCHESTRATION_FEATURES.md:1239 Alternative: Orchestrator Work Assignment | PARTIAL | `ntm spawn --help` on the built binary documents assignment mode (`--assign`, `--strategy`, `--limit`, `--init-prompt`). Built-binary `TestE2ESpawnAssignmentSignalCancellationMatrix` passes; `TestE2ESpawnAssignmentPartialCoverageBuiltBinary` skips in the release-gate environment, which has no `br`/`bv` on PATH, so the plan-to-claim flow was not exercised end to end. |

## Ledger audit (10 seeded closed beads; Proof test must exist in go test -list)

scope: closed since previous audit (2026-11-21T01:54:13Z) — post-discipline era only

| bead | proof status |
|------|--------------|
| bd-rc-track-d-release-docs-3ho85.1 | OK (TestServerMTLSAndHTTP2Qualification) |
| bd-eopms | OK (TestCrashOnlyReceiptReconciliationOnResume) |
| bd-cmwce | OK (TestRotationServerScopedNativeAccounting) |
| bd-mvtas | OK (TestIdempotencyRouterCoverage) |
| bd-rc2-track-2-harness-ori-3ydfr.3 | OK (TestCLIExplicitFactoryInitialization) |
| bd-ussjm | OK (TestRestoreContextReadinessRequiresPiComposer) |
| bd-vbnrd | OK (TestEventEncryptionUnavailableWriteKey) |
| bd-o3htg | PROOF_MISSING (TestCgroup not in go test -list) |
| bd-rc2-track-5-release-dsr-00tdd.5 | OK (FuzzCompletedRotationSummary_BulletAndANSIInvariance) |
| bd-vip20 | NO_PROOF_NAMED |

## Completeness-language check (draft notes: ../release-notes-v1.40.0.md)
```
<none>
```

## Findings
- allowlist growth: 106 -> 115 entries since docs/reality/audit-v1.39.0.md — debt increased
- ledger: closed bead bd-o3htg Proof test TestCgroup not found by 'go test -list'
- ledger: closed bead bd-vip20 names no live Proof test (post-discipline close must name a Test function verbatim or a followable delegated-proof bead)

## Verdict
- **Status:** `PASS-WITH-FINDINGS`: 9/9 WS0 guards green (`gates: yes`), 7/10 seeded spot-probes `WORKS` and 3 `PARTIAL` against the built `linux/amd64` binary, 8/10 seeded ledger proofs `OK` and the other 2 traced to existing tests, and the draft release notes use no completeness language.
- **Probe binary:** a release-stamped `ntm` 1.40.0 `linux/amd64` built from `b252c48b` with the DSR build flags (`-trimpath`, `-s -w`, version, commit and date stamps). It ran in a fresh HOME with a private tmux socket, Agent Mail pointed at a closed port, and no `br`, `bv`, `cass` or agent CLIs on PATH. The tagged commit adds only these two audit files to `b252c48b`.
- **Allowlist growth 106 -> 115:** all nine new entries are `stale_build_id` lines in `ci/allowlists/webui_dist.txt`. Each records a rebuilt Next.js export whose old build directory stays in `internal/webui/dist/` until its Rule-1 deletion is approved (the tracking bead is named on each line). Seven landed between the v1.39.0 audit and the v1.39.0 tag, and two since. No other allowlist grew.
- **Ledger row 8 (PROOF_MISSING):** the close reason names "all TestCgroup groups", and the script took the prefix as a test name. The proofs exist in `internal/pipeline/process_wait_containment_test.go`: `TestCgroupObservationRetriesAndPreservesFailure`, `TestCgroupObservationFailureKeepsHealthyCommandAndOutput`, `TestCgroupObservationFailurePreservesGraceAndSettlementOwnership`, `TestCgroupSignalChecksMembershipAfterOpeningHandle` and `TestCgroupSignalLeavesStaleCandidateAlive`. This is a gap in the ledger text only.
- **Ledger row 10 (NO_PROOF_NAMED):** the close reason cites commits `29fdb7e1` and `6b82ae0c` instead of tests. Their proofs exist: `TestLoopResumeConsumesFailureAtEveryBoundary` and seven sibling tests in `internal/pipeline/loop_failures_test.go`, and `TestLoopBreakCannotRestoreAfterFailedPersistence` in `internal/pipeline/loop_break_recovery_test.go`. This is also a gap in the ledger text only.
- **PARTIAL rows:**
  - Rows 3 and 10 are limits of the probe environment (no CASS index, no `br` or `bv`). Package and built-binary tests that pass at this commit cover them.
  - Row 8 is a test-environment defect, not a product regression. The e2e fixture environment (`atomicAssignmentIsolatedEnv` in `e2e/atomic_assignment_cli_e2e_test.go`) removes `AGENT_MAIL_URL` and `AGENT_MAIL_TOKEN` without a replacement. The built `ntm` therefore falls back to the default `http://127.0.0.1:8765/mcp/` and talks to whatever Agent Mail server listens on the test host. On a host that runs one, the unauthenticated registration attempts are refused with HTTP 401, and the warnings break the test's single-JSON-document check. The failure is the same at v1.39.0.
- **New gaps without a bead:** one, the e2e Agent Mail isolation defect above. No product surface gap was found.
- **Release notes:** the completeness-language check is clean. GitHub issues #379, #384, #385, #386, #387, #388, #390 and #391 ship in this release (see CHANGELOG.md).
