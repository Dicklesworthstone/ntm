# Reality Audit — v1.39.0

generated: 2026-11-15T05:54:04Z by scripts/reality_audit.sh
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
| 1 | README.md:368 2. Dispatch, Monitoring, and Recovery | WORKS | Executed `./ntm rotate context pending --json` (`{"pending":[],"count":0}`) and `./ntm analytics --days 7 --json` (`"period":"Last 7 days","total_sessions":30,"total_agents":81,"total_prompts":448`) against freshly built `./ntm`. |
| 2 | README.md:1991 FAQ | WORKS | Executed `./ntm --robot-status` (`"schema_id":"ntm:robot:status:v1","success":true`) and `./ntm timeline list --json` (`{"timelines":[],"total_count":0,"showing":0}`), plus verified live `./ntm serve` OpenAPI/REST in Probe 9. |
| 3 | command_palette.md:164 `reread_agents_md \| Reread AGENTS.md` | WORKS | Executed `./ntm --robot-palette` (`47` commands loaded from `command_palette.md`), returning `{"key":"reread_agents_md","label":"Reread AGENTS.md","category":"Investigation","prompt":"Reread AGENTS.md so it's still fresh in your mind."}`. |
| 4 | README.md:1941 `claude`, `codex`, `agy`, `grok`, or `gemini` not detected over SSH / tmux / non-login shells | WORKS | Executed `./ntm deps -v --json`, verifying runtime `PATH` discovery across `21` dependency checks including `tmux`, `Claude Code`, `OpenAI Codex`, `Antigravity CLI (agy)`, `Grok Build (xAI)`, `Gemini CLI (legacy)`, `OpenCode`, and `Oh My Pi (omp)`. |
| 5 | docs/ORCHESTRATION_FEATURES.md:1266 Historical Proposal: Soft-Claim Files | WORKS | Executed `./ntm claim list --json` (`{"success":true,"claims":[]}`) and `./ntm assign --help`, matching the documented distinction between authoritative Beads/ledger claims in `ntm assign` and optional advisory `ntm claim` helpers. |
| 6 | README.md:1918 From Source | WORKS | Built `./ntm` via `go build -o ntm ./cmd/ntm` and executed `./ntm version --json` (`"version":"dev","go_version":"go1.26.11","platform":"linux/amd64"`). |
| 7 | docs/ORCHESTRATION_FEATURES.md:86 Feature 1: Agent Activity Detection | WORKS | Spawned live tmux session `ntm_probe_2220566` (`ntm_probe_2220566__cc_1` emitting `claude> `) and executed `./ntm --robot-activity=ntm_probe_2220566`, returning `"agent_type":"claude","state":"WAITING","confidence":0.9,"detected_patterns":["claude_prompt"]`. |
| 8 | command_palette.md:29 `fresh_review \| Fresh Review` | WORKS | Executed `./ntm --robot-palette`, returning `{"key":"fresh_review","label":"Fresh Review","category":"Analysis & Review"}` with full prompt body. |
| 9 | README.md:1503 REST, SSE, WebSocket, and OpenAPI | WORKS | Launched `./ntm serve --port 17395 --project-dir $PROJ_A`, verified `GET /api/v1/config` (`project_dir` + `startup_project_dir`), `PATCH /api/v1/config` switching `project_dir` to `$PROJ_B` (`"updated":true,"success":true`), and `GET /api/v1/openapi.json` (`OpenAPI 3.1.0`, `180` paths, `593` component schemas). |
| 10 | docs/ORCHESTRATION_FEATURES.md:505 Solution Overview | WORKS | Executed `./ntm --robot-route=ntm_probe_2220566 --strategy=least-loaded` against live tmux session `ntm_probe_2220566`, returning `"recommendation":{"pane_id":"%198","pane_index":1,"agent_type":"claude","score":58.32,"state":"WAITING","reason":"primary strategy succeeded"}`. |

## Ledger audit (10 seeded closed beads; Proof test must exist in go test -list)

scope: closed since previous audit (2026-08-20T01:17:47Z) — post-discipline era only

| bead | proof status |
|------|--------------|
| bd-39eyk | OK (TestContextInjectCobraRejectsFullOrChangedQualifiedNativeSession) |
| bd-9ny67 | OK (TestBeadsEndpointsWithoutWorkspace) |
| bd-6n16a | OK (TestAccountRotatorListAccountsContext_ParsesRateLimitedPerAccount) |
| bd-yhc8r | OK (TestNewScenarioHarnessCreatesExpectedLayout) |
| bd-rc-track-e-uplift-xkt68.4 | OK (TestGetRoute_RelaxPersistsEvidenceLedger) |
| bd-hqblz | OK (TestAsyncJobsEnsembleSpawnAndSynthesizeValidationAndLifecycle) |
| bd-event-bus-reentrant-semaphore-deadlock-n8isd | OK (TestEventBus_ReentrantPublishAtCapacityDoesNotDeadlock) |
| bd-wvadh | OK (TestWorkflowResumeRetainsTimeoutDeadlineAndAcknowledgesOnce) |
| bd-g789p | OK (TestWorkflowResumeObservesFilesChangedWhileStopped) |
| bd-rc-track-b-core-bugs-y2bre.5 | OK (TestApprovalApproveV1_SLBSelfApproval) |

## Completeness-language check (draft notes: not provided)
```
<none>
```

## Findings
- none (`0` scripted findings; `10/10` seeded spot-probes verified `WORKS` against built `./ntm`; `10/10` seeded post-`v1.29.0` closed beads out of `103` candidates verified `OK` in `go test -list`; `allowlist-lines = 86 <= 216`).

## Verdict
- New `NO_BEAD` gaps found: `0`.
- Remaining open release-track items (`bd-rc-track-d-release-docs-3ho85.2` Rule-1 tracked-binary hygiene for `GH #346`, `bd-rc-track-d-release-docs-3ho85.1` DSR `v1.39.0` publication for `GH #347`, and epic `bd-rc-track-d-release-docs-3ho85`) are tracked and gated on operator Rule-1 / DSR authorization.
