# Reality Audit — v1.39.0

generated: 2026-11-19T00:59:21Z by scripts/reality_audit.sh
seed: 2553148806
allowlist-lines: 86
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
  167 total
```

## Spot-probes (seeded from tag; exercise against the BUILT artifact, not the tree)
| # | claim row | verdict (WORKS / PARTIAL / NO_SURFACE / NO_BEAD) | evidence |
|---|-----------|--------------------------------------------------|----------|
| 1 | README.md:368 2. Dispatch, Monitoring, and Recovery | WORKS | Executed built `/tmp/ntm_wave15_probe --robot-saved-sessions --saved-action=save --saved-session=wave15_spotprobe_3504865 --saved-name=spot15 --saved-overwrite --robot-format=json` (`"schema_id":"ntm:robot:saved-sessions:v1","success":true,"saved_as":"spot15"`), `--saved-action=show --saved-name=spot15` (`"agents":{"agy":1,...}`), `--robot-workflows --workflows-name=red-green` (`"schema_id":"ntm:robot:workflows:v1","triggers":2`), and `--robot-session-templates --template-name=refactor` (`"schema_id":"ntm:robot:session-templates:v1","source":"builtin"`). |
| 2 | README.md:1991 FAQ | WORKS | Executed built `/tmp/ntm_wave15_probe --robot-status --robot-format=json` (`"schema_id":"ntm:robot:status:v1","success":true`) and `--robot-mapping=wave15_spotprobe_3504865 --robot-format=json` (`"schema_id":"ntm:robot:mapping:v1","count":1,"discrepancies":0`). |
| 3 | command_palette.md:164 reread_agents_md | Reread AGENTS.md | WORKS | Executed `/tmp/ntm_wave15_probe --robot-palette --robot-format=json`, returning `"reread_agents_md"`. |
| 4 | README.md:1941 `claude`, `codex`, `agy`, `grok`, or `gemini` not detected over SSH / tmux / non-login shells | WORKS | Executed `/tmp/ntm_wave15_probe deps -v --json` (`{"all_installed":true}`). |
| 5 | docs/ORCHESTRATION_FEATURES.md:1266 Historical Proposal: Soft-Claim Files | WORKS | Executed `/tmp/ntm_wave15_probe claim list --json` (`{"success":true,"count":null}`). |
| 6 | README.md:1918 From Source | WORKS | Built `/tmp/ntm_wave15_probe` via `go build -o /tmp/ntm_wave15_probe ./cmd/ntm` and executed `version --json` (`{"version":"dev","go_version":"go1.26.11","platform":"linux/amd64"}`). |
| 7 | docs/ORCHESTRATION_FEATURES.md:86 Feature 1: Agent Activity Detection | WORKS | Executed `/tmp/ntm_wave15_probe --robot-adopt=wave15_spotprobe_3504865 --adopt-by-window=agy --robot-format=json` (`"schema_id":"ntm:robot:adopt:v1","total_panes":1,"agents":{"agy":1}`) and `--robot-activity=wave15_spotprobe_3504865 --robot-format=json` (`"success":true`) against a live tmux session. |
| 8 | command_palette.md:29 fresh_review | Fresh Review | WORKS | Executed `/tmp/ntm_wave15_probe --robot-palette --robot-format=json`, returning `"fresh_review"`. |
| 9 | README.md:1503 REST, SSE, WebSocket, and OpenAPI | WORKS | Launched `/tmp/ntm_wave15_probe serve --port 17515`, verified `GET /api/v1/saved-sessions` (`count:1,first:"spot15"`), `GET /api/v1/sessions/wave15_spotprobe_3504865/mapping` (`count:1`), `GET /api/v1/workflows/red-green` (`workflow:"red-green"`), `POST /api/v1/sequences` (`action:"create",steps:2`), `GET /api/v1/session-templates/refactor` (`template:"refactor"`), and `GET /api/v1/openapi.json` (`OpenAPI 3.1.0`, `237` paths, `809` component schemas). |
| 10 | docs/ORCHESTRATION_FEATURES.md:505 Solution Overview | WORKS | Executed `/tmp/ntm_wave15_probe --robot-route=wave15_spotprobe_3504865 --strategy=least-loaded --robot-format=json` (`{"success":true,"strategy":"least-loaded"}`). |

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
- none (probe verdicts above may still add findings)

## Verdict
- New NO_BEAD gaps found: file beads ON THE SPOT (label reality-bridge) and list IDs here.
- Release notes may not use completeness language for any surface with an open gap.
