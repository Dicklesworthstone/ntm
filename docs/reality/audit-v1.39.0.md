# Reality Audit — v1.39.0

generated: 2026-11-13T17:57:02Z by scripts/reality_audit.sh
seed: 2553148806
allowlist-lines: 212
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
21a24
> Assignment-time injection
24a28
> Bounded Background Archives
25a30
> CAAM Seat Selection (opt-in, default off)
58a64
> Embedded Web Dashboard (`ntm web`)
85c91
< Grok Build (phase one)
---
> Grok Build
111a118,119
> Oh My Pi (`omp`)
> OpenCode (`oc`)
113a122
> Pane identity badges
114a124
> Pi coding agent (`pi`)
122a133
> Rano Network Activity
139a151
> Session Coordinator Runtime
144a157
> Staggered prompts (thundering-herd prevention)
```

## Allowlist burndown
```
   14 ci/allowlists/binary_bloat.txt
    7 ci/allowlists/config.txt
   16 ci/allowlists/contracts.txt
  231 ci/allowlists/deadcode.txt
    9 ci/allowlists/docs.txt
    6 ci/allowlists/openapi_drift.txt
   10 ci/allowlists/placebo.txt
  293 total
```

## Spot-probes (seeded from tag; exercise against the BUILT artifact, not the tree)
| # | claim row | verdict (WORKS / PARTIAL / NO_SURFACE / NO_BEAD) | evidence |
|---|-----------|--------------------------------------------------|----------|
| 1 | README.md:368 2. Dispatch, Monitoring, and Recovery | WORKS | `./ntm {send,interrupt,activity,health,watch,extract,diff,grep} --help`, `./ntm analytics --days 7 --json` (`total_sessions`, `total_prompts`), `./ntm rotate context pending --json` (`{"count":0,"pending_count":0}`), `confirm --action`, `recover --execution-id` verified against built `./ntm`. |
| 2 | README.md:1991 FAQ | WORKS | Clean-`HOME` offline operation verified without optional daemons: `./ntm --robot-status` (`{"success":true,"session_count":0}`), `./ntm checkpoint list --flat --json`, `./ntm audit sources --json` (`["count","sources","success"]`). |
| 3 | README.md:1941 `claude`, `codex`, `agy`, `grok`, or `gemini` not detected over SSH / tmux / non-login shells | WORKS | `PATH="/usr/bin:/bin" ./ntm deps -v --json` resolved `tmux` (`installed:true, /usr/bin/tmux`) and accurately reported `Claude Code` / `OpenAI Codex` / `Antigravity CLI (agy)` (`installed:false`) under non-login minimal `PATH`. |
| 4 | docs/ORCHESTRATION_FEATURES.md:1266 Historical Proposal: Soft-Claim Files | WORKS | Automated `ntm assign` uses atomic Beads claims + durable assignment ledger (no delay-and-check loop), while `./ntm claim bd-probe4 --agent ProbeAgent --json`, `claim check`, and `claim list` provide optional advisory `.ntm/claims/` helpers; clarified `docs/ORCHESTRATION_FEATURES.md:1268-1270` in-place to state both explicitly. |
| 5 | README.md:1918 From Source | WORKS | `make build` built `./ntm` cleanly and `./ntm version` emitted version/commit/build-time stamp. |
| 6 | command_palette.md:79 ensemble_modes_core \| Ensemble Modes (Core) | WORKS | `./ntm modes list --tier core --json` returned `{"tier_filter":["core"],"count":28}` matching the graduated default-build ensemble catalog. |
| 7 | docs/ORCHESTRATION_FEATURES.md:86 Feature 1: Agent Activity Detection | WORKS | Isolated tmux session `probe9` (`probe9__cc_1` running live child emitting `claude> `): `./ntm --robot-activity=probe9` returned `{"state":"WAITING","confidence":0.9,"safe_to_dispatch":true}`. |
| 8 | README.md:1503 REST, SSE, WebSocket, and OpenAPI | WORKS | Live `./ntm serve --port 17339 --project-dir /tmp/ntmproj_a`: `GET /api/v1/config` -> `/tmp/ntmproj_a`; `PATCH /api/v1/config {"project_dir":"/tmp/ntmproj_b"}` switched `project_dir` while preserving `startup_project_dir`; `PATCH {"project_dir":""}` reset to `/tmp/ntmproj_a`; `GET /api/v1/openapi.json` served OpenAPI `3.1.0` (`167` paths, `491` schemas); `GET /api/v1/attention/stream` streamed `200 OK text/event-stream` (`5248` bytes). |
| 9 | docs/ORCHESTRATION_FEATURES.md:505 Solution Overview | WORKS | `./ntm --robot-route=probe9 --strategy=least-loaded` scored active `WAITING` pane `probe9__cc_1` at `score:90` (`reason:"primary strategy succeeded"`) and excluded exited bare-shell panes with `reason:"agent CLI exited to a bare shell"`. |
| 10 | README.md:1888 Install Script | WORKS | `bash -n install.sh`, `bash install.sh --help` (`--version`, `--dir`, `--no-shell`, `--easy-mode`), and `go test -v ./internal/cli -run TestUpgradeAsset` (`7/7` platform naming contracts) all exited `0`. |

## Ledger audit (10 seeded closed beads; Proof test must exist in go test -list)

scope: closed since previous audit (2026-08-20T01:17:47Z) — post-discipline era only

| bead | proof status |
|------|--------------|
| bd-39eyk | OK (TestContextInjectCobraRejectsFullOrChangedQualifiedNativeSession) |
| bd-9ny67 | OK (TestBeadsEndpointsWithoutWorkspace) |
| bd-6n16a | OK (TestAccountRotatorListAccountsContext_ParsesRateLimitedPerAccount) |
| bd-yhc8r | OK (TestNewScenarioHarnessCreatesExpectedLayout) |
| bd-rc-track-e-uplift-xkt68.4 | OK (TestGetRoute_RelaxPersistsEvidenceLedger) |
| bd-event-bus-reentrant-semaphore-deadlock-n8isd | OK (TestEventBus_ReentrantPublishAtCapacityDoesNotDeadlock) |
| bd-wvadh | OK (TestWorkflowResumeRetainsTimeoutDeadlineAndAcknowledgesOnce) |
| bd-g789p | OK (TestWorkflowResumeObservesFilesChangedWhileStopped) |
| bd-rc-track-b-core-bugs-y2bre.5 | OK (TestApprovalApproveV1_SLBSelfApproval) |
| bd-rc-track-e-uplift-xkt68.3 | OK (TestVTMetamorphic_M1_AgentParserGeometryInvariance) |

## Completeness-language check (draft notes: not provided)
```
<none>
```

## Findings
- Probe 4 (`docs/ORCHESTRATION_FEATURES.md:1266`) surfaced minor doc ambiguity (`NTM does not implement that protocol` vs optional advisory `ntm claim` CLI in `internal/cli/claim.go`); clarified `docs/ORCHESTRATION_FEATURES.md:1268-1270` in-place during audit (`0` new `NO_BEAD` gaps).

## Verdict
- New NO_BEAD gaps found: file beads ON THE SPOT (label reality-bridge) and list IDs here.
- Release notes may not use completeness language for any surface with an open gap.
