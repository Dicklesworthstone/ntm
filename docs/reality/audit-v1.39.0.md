# Reality Audit — v1.39.0

generated: 2026-11-18T19:41:11Z by scripts/reality_audit.sh
seed: 2553148806
allowlist-lines: 86
gates: yes
total-minutes: 4 (budget 90)

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
  167 total
```

## Spot-probes (seeded from tag; exercise against the BUILT artifact, not the tree)
| # | claim row | verdict (WORKS / PARTIAL / NO_SURFACE / NO_BEAD) | evidence |
|---|-----------|--------------------------------------------------|----------|
| 1 | README.md:368 2. Dispatch, Monitoring, and Recovery | WORKS | Executed built `/tmp/ntm_wave14_probe --robot-webhook-outbox --outbox-action=list --robot-format=json` (`"schema_id":"ntm:robot:webhook-outbox:v1","success":true`), `--robot-redact` (`"schema_id":"ntm:robot:redact:v1","finding_count":1,"output":"Authorization: [REDACTED:BEARER_TOKEN:2713416b]"`), and `--robot-hooks --hooks-action=status` (`"schema_id":"ntm:robot:hooks:v1","installed_count":0`). |
| 2 | README.md:1991 FAQ | WORKS | Executed built `/tmp/ntm_wave14_probe --robot-status --robot-format=json` (`"schema_id":"ntm:robot:status:v1","success":true`) and `--robot-personas --robot-format=json` (`"schema_id":"ntm:robot:personas:v1","count":5,"builtin_count":5`). |
| 3 | command_palette.md:164 reread_agents_md | Reread AGENTS.md | WORKS | Executed `/tmp/ntm_wave14_probe --robot-palette --robot-format=json`, returning `"reread_agents_md"`. |
| 4 | README.md:1941 `claude`, `codex`, `agy`, `grok`, or `gemini` not detected over SSH / tmux / non-login shells | WORKS | Executed `/tmp/ntm_wave14_probe deps -v --json` (`{"all_installed":true}`). |
| 5 | docs/ORCHESTRATION_FEATURES.md:1266 Historical Proposal: Soft-Claim Files | WORKS | Executed `/tmp/ntm_wave14_probe claim list --json` (`{"success":true,"count":0}`). |
| 6 | README.md:1918 From Source | WORKS | Built `/tmp/ntm_wave14_probe` via `go build -o /tmp/ntm_wave14_probe ./cmd/ntm` and executed `version --json` (`{"version":"dev","go_version":"go1.26.11","platform":"linux/amd64"}`). |
| 7 | docs/ORCHESTRATION_FEATURES.md:86 Feature 1: Agent Activity Detection | WORKS | Executed `/tmp/ntm_wave14_probe --robot-adopt=wave14_spotprobe_1460428 --adopt-by-window=agy --robot-format=json` (`"schema_id":"ntm:robot:adopt:v1","adopted":1,"agents":{"agy":1}`) and `--robot-activity=wave14_spotprobe_1460428 --robot-format=json` (`"success":true`) against a live tmux session. |
| 8 | command_palette.md:29 fresh_review | Fresh Review | WORKS | Executed `/tmp/ntm_wave14_probe --robot-palette --robot-format=json`, returning `"fresh_review"`. |
| 9 | README.md:1503 REST, SSE, WebSocket, and OpenAPI | WORKS | Launched `/tmp/ntm_wave14_probe serve --port 17414`, verified `GET /api/v1/personas` (`count:5`), `GET /api/v1/recipes` (`count:6`), `GET /api/v1/hooks` (`hooks_len:5`), `POST /api/v1/redact/preview` (`finding_count:1`), `POST /api/v1/sessions/wave14_spotprobe_1460428/adopt` (`dry_run:true,total_panes:1`), `GET /api/v1/sessions/wave14_spotprobe_1460428/{context,diff,files}` (`agents_len:1`), and `GET /api/v1/openapi.json` (`OpenAPI 3.1.0`, `225` paths, `769` component schemas). |
| 10 | docs/ORCHESTRATION_FEATURES.md:505 Solution Overview | WORKS | Executed `/tmp/ntm_wave14_probe --robot-route=wave14_spotprobe_1460428 --strategy=least-loaded --robot-format=json` (`{"success":true,"strategy":"least-loaded"}`). |

## Ledger audit (10 seeded closed beads; Proof test must exist in go test -list)

scope: closed since previous audit (2026-08-20T01:17:47Z) — post-discipline era only

| bead | proof status |
|------|--------------|
| bd-39eyk | OK (TestContextInjectCobraRejectsFullOrChangedQualifiedNativeSession) |
| bd-9ny67 | OK (TestBeadsEndpointsWithoutWorkspace) |
| bd-6n16a | OK (TestAccountRotatorListAccountsContext_ParsesRateLimitedPerAccount) |
| bd-t0flp | OK (TestEnsembleAnalysisRESTEndpoints) |
| bd-s9qzg.3 | OK (TestVSCodeExtensionManifestAndVSIXContract) |
| bd-3dyeo.2 | OK (TestAnalyticsOpenAPIReflection) |
| bd-yhc8r | OK (TestNewScenarioHarnessCreatesExpectedLayout) |
| bd-suaow | OK (TestAuditListShowSearchVerifyAndPagination) |
| bd-rc-track-e-uplift-xkt68.4 | OK (TestGetRoute_RelaxPersistsEvidenceLedger) |
| bd-hqblz | OK (TestAsyncJobsEnsembleSpawnAndSynthesizeValidationAndLifecycle) |

## Completeness-language check (draft notes: not provided)
```
<none>
```

## Findings
- none (`0` scripted findings; `10/10` seeded spot-probes verified `WORKS` against built binary `/tmp/ntm_wave14_probe`; `10/10` seeded post-`v1.29.0` closed beads out of `132` candidates verified `OK` in `go test -list`, including `bd-s9qzg.3`; `allowlist-lines = 86 <= 216`).

## Verdict
- New `NO_BEAD` gaps found: `0`.
- Remaining open release-track items (`bd-rc-track-d-release-docs-3ho85.2` Rule-1 tracked-binary hygiene for `GH #346`, `bd-rc-track-d-release-docs-3ho85.1` DSR `v1.39.0` publication for `GH #347`, and epic `bd-rc-track-d-release-docs-3ho85`) remain tracked and gated on operator Rule-1 / DSR authorization.
