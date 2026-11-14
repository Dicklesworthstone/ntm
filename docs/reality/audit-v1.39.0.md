# Reality Audit — v1.39.0

generated: 2026-11-14T05:22:24Z by scripts/reality_audit.sh
seed: 2553148806
allowlist-lines: 86
gates: yes
total-minutes: 3 (budget 90)

## Step timings
- 1-checklist: 0m (budget 10m)
- 2-gates: 2m (budget 15m)
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
| 1 | README.md:368 2. Dispatch, Monitoring, and Recovery | WORKS | `./ntm {send,interrupt,activity,health,watch,extract,diff,grep} --help`, `./ntm analytics --days 7 --json` (`total_sessions`, `total_prompts`), `./ntm rotate context pending --json` (`{"count":0,"pending_count":0}`), `confirm --action`, `recover --execution-id` verified against built `./ntm`. |
| 2 | README.md:1991 FAQ | WORKS | Clean-`HOME` offline operation verified without optional daemons: `./ntm --robot-status` (`{"success":true,"session_count":0}`), `./ntm checkpoint list --flat --json`, `./ntm audit sources --json` (`["count","sources","success"]`). |
| 3 | command_palette.md:164 reread_agents_md \| Reread AGENTS.md | WORKS | `HOME="$(mktemp -d)" ./ntm --robot-palette` loaded all `57` commands from `./command_palette.md` and emitted `{"key":"reread_agents_md","label":"Reread AGENTS.md","category":"Investigation"}` (`prompt_len=50`). |
| 4 | README.md:1941 `claude`, `codex`, `agy`, `grok`, or `gemini` not detected over SSH / tmux / non-login shells | WORKS | `PATH="/usr/bin:/bin" ./ntm deps -v --json` resolved `tmux` (`installed:true, /usr/bin/tmux`) and accurately reported `Claude Code` / `OpenAI Codex` / `Antigravity CLI (agy)` (`installed:false`) under non-login minimal `PATH`. |
| 5 | docs/ORCHESTRATION_FEATURES.md:1266 Historical Proposal: Soft-Claim Files | WORKS | Automated `ntm assign` uses atomic Beads claims + durable assignment ledger (no delay-and-check loop), while `./ntm claim bd-probe4 --agent ProbeAgent --json`, `claim check`, and `claim list` provide optional advisory `.ntm/claims/` helpers; clarified `docs/ORCHESTRATION_FEATURES.md:1268-1270` in-place to state both explicitly. |
| 6 | README.md:1918 From Source | WORKS | `make build` built `./ntm` cleanly and `./ntm version` emitted version/commit/build-time stamp. |
| 7 | docs/ORCHESTRATION_FEATURES.md:86 Feature 1: Agent Activity Detection | WORKS | Isolated tmux session `probe9` (`probe9__cc_1` running live child emitting `claude> `): `./ntm --robot-activity=probe9` returned `{"state":"WAITING","confidence":0.9,"safe_to_dispatch":true}`. |
| 8 | command_palette.md:29 fresh_review \| Fresh Review | WORKS | `HOME="$(mktemp -d)" ./ntm --robot-palette` returned `{"key":"fresh_review","label":"Fresh Review","category":"Analysis & Review"}` (`prompt_len=263`) alongside newly wired `ensemble_suggest`, `attention_triage`, and `backpressure_heatmap`. |
| 9 | README.md:1503 REST, SSE, WebSocket, and OpenAPI | WORKS | Live `./ntm serve --port 17339 --project-dir /tmp/ntmproj_a`: `GET /api/v1/config` -> `/tmp/ntmproj_a`; `PATCH /api/v1/config {"project_dir":"/tmp/ntmproj_b"}` switched `project_dir` while preserving `startup_project_dir`; `PATCH {"project_dir":""}` reset to `/tmp/ntmproj_a`; `GET /api/v1/openapi.json` served OpenAPI `3.1.0` (`189/191 = 98.95%` typed `/api/v1/*`, `593` schemas); `GET /api/v1/attention/stream` streamed `200 OK text/event-stream`. |
| 10 | docs/ORCHESTRATION_FEATURES.md:505 Solution Overview | WORKS | `./ntm --robot-route=probe9 --strategy=least-loaded` scored active `WAITING` pane `probe9__cc_1` at `score:90` (`reason:"primary strategy succeeded"`) and excluded exited bare-shell panes with `reason:"agent CLI exited to a bare shell"`. |

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
- Probe 5 (`docs/ORCHESTRATION_FEATURES.md:1266`) surfaced minor doc ambiguity (`NTM does not implement that protocol` vs optional advisory `ntm claim` CLI in `internal/cli/claim.go`); clarified `docs/ORCHESTRATION_FEATURES.md:1268-1270` in-place during audit (`0` new `NO_BEAD` gaps).

## Verdict
- New NO_BEAD gaps found: file beads ON THE SPOT (label reality-bridge) and list IDs here.
- Release notes may not use completeness language for any surface with an open gap.
