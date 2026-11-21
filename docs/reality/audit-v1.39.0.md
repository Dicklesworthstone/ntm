# Reality Audit — v1.39.0

generated: 2026-11-21T01:54:13Z by scripts/reality_audit.sh
seed: 2553148806
allowlist-lines: 106
gates: yes
total-minutes: 3 (budget 90)

## Step timings
- 1-checklist: 0m (budget 10m)
- 2-gates: 3m (budget 15m)
- 3-probes: 1m (budget 30m)
- 4-ledger: 1m (budget 20m)

## Checklist drift (vs docs/reality/checklist-v1.29.0.md)
```diff
9a10
> 8. Multi-Project Swarm Execution
14a16
> Agent Plugins
21a24,25
> Assignment-time injection
> attention_triage | Attention Triage
24a29,30
> backpressure_heatmap | Swarm Backpressure & Heatmap
> Bounded Background Archives
25a32
> CAAM Seat Selection (opt-in, default off)
57c64
< do_gh_flow | Do GH Flow
---
> do_gh_flow | Commit, Preflight & DSR Release
58a66
> Embedded Web Dashboard (`ntm web`)
64a73
> ensemble_suggest | Ensemble Suggest Preset
85c94
< Grok Build (phase one)
---
> Grok Build
111a121,122
> Oh My Pi (`omp`)
> OpenCode (`oc`)
113a125
> Pane identity badges
114a127
> Pi coding agent (`pi`)
122a136
> Rano Network Activity
123a138
> Reasoning Ensembles
139a155
> Session Coordinator Runtime
144a161
> Staggered prompts (thundering-herd prevention)
147a165
> Swarm Triage & Diagnostics
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
   27 ci/allowlists/webui_dist.txt
  194 total
```

## Spot-probes (seeded from tag; exercise against the BUILT artifact, not the tree)
| # | claim row | verdict (WORKS / PARTIAL / NO_SURFACE / NO_BEAD) | evidence |
|---|-----------|--------------------------------------------------|----------|
| 1 | README.md:368 2. Dispatch, Monitoring, and Recovery | WORKS | Executed built `/tmp/ntm_xbuild_e5cxbx/ntm_linux_amd64 --robot-saved-sessions --saved-action=save --saved-session=rc2_probe --saved-name=rc2spot --saved-overwrite --robot-format=json` (`"schema_id":"ntm:robot:saved-sessions:v1","success":true,"saved_as":"rc2spot"`), `--robot-workflows --workflows-name=red-green` (`"schema_id":"ntm:robot:workflows:v1","success":true,"workflow":"red-green"`), and `--robot-session-templates --template-name=refactor` (`"schema_id":"ntm:robot:session-templates:v1","success":true,"name":"refactor","source":"builtin"`). |
| 2 | command_palette.md:164 reread_agents_md \| Reread AGENTS.md | WORKS | Executed `/tmp/ntm_xbuild_e5cxbx/ntm_linux_amd64 --robot-palette --robot-format=json` (`"success":true,"has_reread":true,"total":53`). |
| 3 | README.md:1928 Install Script | WORKS | Executed `bash -n install.sh && bash install.sh --help` (usage & options printed cleanly), `go test -v ./tests -run TestInstaller` (21/21 subtests PASS), and `/tmp/ntm_xbuild_e5cxbx/ntm_linux_amd64 version --json` (`{"version":"dev","go_version":"go1.26.11","platform":"linux/amd64"}`). |
| 4 | docs/ORCHESTRATION_FEATURES.md:1266 Historical Proposal: Soft-Claim Files | WORKS | Executed `/tmp/ntm_xbuild_e5cxbx/ntm_linux_amd64 claim list --json` (`{"success":true,"count":null}`). |
| 5 | README.md:1966 Troubleshooting | WORKS | Executed `/tmp/ntm_xbuild_e5cxbx/ntm_linux_amd64 deps -v --json` (`{"all_installed":true,"deps_count":21}`) and `doctor --json` (`{"overall":"warning","checks_count":0}`). |
| 6 | docs/ORCHESTRATION_FEATURES.md:86 Feature 1: Agent Activity Detection | WORKS | Executed `/tmp/ntm_xbuild_e5cxbx/ntm_linux_amd64 --robot-adopt=rc2_probe_2466451 --adopt-by-window=agy --robot-format=json` (`"schema_id":"ntm:robot:adopt:v1","success":true,"total_panes":1,"agents":{"agy":1,...}`) and `--robot-activity=rc2_probe_2466451 --robot-format=json` (`{"success":true,"session":"rc2_probe_2466451"}`) against a live tmux session. |
| 7 | command_palette.md:29 fresh_review \| Fresh Review | WORKS | Executed `/tmp/ntm_xbuild_e5cxbx/ntm_linux_amd64 --robot-palette --robot-format=json` (`"success":true,"has_fresh":true,"total":53`). |
| 8 | README.md:1503 REST, SSE, WebSocket, and OpenAPI | WORKS | Launched `/tmp/ntm_xbuild_e5cxbx/ntm_linux_amd64 serve --port 17539/17540`, verified `GET /api/v1/openapi.json` (`OpenAPI 3.1.0`, `248` paths, `850` schemas, `has_restore:true`, `has_wf_run:true`), `GET /api/v1/saved-sessions` (`success:true`), `GET /api/v1/workflows/red-green` (`success:true,name:"red-green"`), `GET /api/v1/session-templates/refactor` (`success:true,name:"refactor"`), and `GET /docs` (`HTTP/1.1 200 OK`, strict nonce CSP, `unpkg_hits=0`). |
| 9 | docs/ORCHESTRATION_FEATURES.md:505 Solution Overview | WORKS | Executed `/tmp/ntm_xbuild_e5cxbx/ntm_linux_amd64 --robot-route=rc2_probe2 --strategy=least-loaded --robot-format=json` (`{"success":true,"strategy":"least-loaded","error_code":null}`). |
| 10 | README.md:2084 About Contributions | WORKS | Verified `README.md:2084-2092` policy statement matches `CONTRIBUTING.md` (no-external-PRs / issue-driven review policy) and `LICENSE` (MIT + rider). |

## Ledger audit (10 seeded closed beads; Proof test must exist in go test -list)

scope: closed since previous audit (2026-08-20T01:17:47Z) — post-discipline era only

| bead | proof status |
|------|--------------|
| bd-39eyk | OK (TestContextInjectCobraRejectsFullOrChangedQualifiedNativeSession) |
| bd-9ny67 | OK (TestBeadsEndpointsWithoutWorkspace) |
| bd-6n16a | OK (TestAccountRotatorListAccountsContext_ParsesRateLimitedPerAccount) |
| bd-t0flp | OK (TestEnsembleAnalysisRESTEndpoints) |
| bd-rc2-track-1-core-bugs-hwah4.6 | OK (FuzzCompletedRotationSummary_BulletAndANSIInvariance) |
| bd-s9qzg.3 | OK (TestVSCodeExtensionManifestAndVSIXContract) |
| bd-3dyeo.2 | OK (TestAnalyticsOpenAPIReflection) |
| bd-yhc8r | OK (TestNewScenarioHarnessCreatesExpectedLayout) |
| bd-suaow | OK (TestAuditListShowSearchVerifyAndPagination) |
| bd-rc-track-e-uplift-xkt68.4 | OK (TestGetRoute_RelaxPersistsEvidenceLedger) |

## Completeness-language check (draft notes: not provided)
```
<none>
```

## Findings
- none (probe verdicts above may still add findings)

## Verdict
- **Status:** `PASS-WITH-TRACKED-DEBT` — 0 scripted findings, 9/9 WS0 guards green (`allowlist-lines`: 216 -> 106, including 20 Rule-1-gated historical `stale_build_id` entries newly ratcheted under WS0-G8 `ci/allowlists/webui_dist.txt` -> `bd-rc-track-d-release-docs-3ho85.2`), 10/10 seeded spot-probes `WORKS` against built `linux/amd64` release binary, 10/10 seeded ledger proofs `OK`, `govulncheck ./...` 0 vulnerabilities, 4/4 release targets cross-compiled (`linux/{amd64,arm64}`, `darwin/{amd64,arm64}`), `release_preflight.sh --self-test` + `--dry-run` green.
- **New NO_BEAD gaps found:** none (all remaining gaps are tracked under open `bd-rc2-*` beads).
- **Open Rule-1 & Track 2/6 scope note:** Do not use completeness language in release notes for surfaces with open tracked beads: `bd-rc2-track-5-release-dsr-00tdd.3` (Rule-1 deletion of stale `internal/webui/dist/` buildIds, `cmd/ntm/ntm`, `fakeshell`, 30 tracked logs, `master`, and 35 `subagent-*` worktrees), `bd-rc2-track-2-harness-ori-3ydfr.2` (shared CLI/Robot/REST spawn engine), `3ydfr.3` (`SetProjectionStore` globals + CLI `init()`), `3ydfr.6` (`cli/assign.go` split), `3ydfr.7` (unified rotation FSM + `kernel`), `3ydfr.8` (TUI operational panels), `3ydfr.5` (Track 2 proof suite), and `wo9iw.2` (`-short` suite Pareto optimization).
- **DSR Release & GitHub Issue Checklist (ready once user approves Rule-1 cleanup & final release tag):**
  - Publish via DSR (`dsr release`), verify `install.sh` + `/dp/agentic_coding_flywheel_setup/checksums.yaml` + `Dicklesworthstone/homebrew-tap` `Casks/ntm.rb`, attach `vscode/dist/ntm-vscode-<version>.vsix`.
  - Close verified GitHub issues upon tagged release: `#342` (omp band composer), `#380`/`#381` (Codex 0.170.0 rotation/pane-identity readiness), plus verified Wave 11-16 fixes documented in `CHANGELOG.md`.
