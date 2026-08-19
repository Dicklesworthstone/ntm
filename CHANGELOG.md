# Changelog

All notable changes to **NTM** (Named Tmux Manager) are documented here.

NTM is a tmux session management tool for orchestrating multiple AI coding agents (Claude Code, OpenAI Codex, Google Gemini CLI, Antigravity, Grok Build, Cursor, Windsurf, Aider, Opencode, Ollama) in parallel with a stunning TUI dashboard, robot mode APIs, and deep ecosystem integrations.

- Repository: <https://github.com/Dicklesworthstone/ntm>
- Releases marked **[GitHub Release]** have published release assets on GitHub.
- Releases marked **[Tag Only]** are git tags without a published GitHub Release.
- Links point to individual commits for traceability.

---

## [Unreleased]

### Changed — BREAKING (v1.29.0)

- **The second deprecation batch of config keys now fails the loader**
  (v1.29.0 flip promised in the v1.28.0 entry; `bd-6otuk`, flip bead
  `bd-ad54k`): the 128 reader-less config keys deprecated in v1.28.0 (second
  dead-knob batch — `[accounts]`, `[scanner.*]` except `ubs_path`, the
  `[spawn_pacing]` rate/backoff/headroom subset, the `[cass]` subset,
  integrations leaves, the `[checkpoints]` subset,
  `[tmux.activity_indicators]`, `robot.output.{pretty,timestamps,compress}`,
  `rotation.prefer_restart`/`rotation.accounts.priority`, and the seven
  singles) no longer load with a deprecation warning — each one is now a hard
  strict-loader **error** with the same key + disposition text the v1.28.0
  warning carried, exactly like the v1.26.0→v1.27.0 flip. A single failed
  load lists **every** offending key present (v1.26.0-batch removed keys,
  v1.28.0-batch deprecated keys, and genuinely unknown fields together), so
  one pass over the error tells you everything to delete. `ntm config
  set`/persistence validation rejects the same keys with the same disposition
  text. `ntm doctor` still names each deprecated key present in a config file
  (now as an error check) because it scans the file leniently — useful
  precisely when the strict loader refuses to start. The project-level
  `.ntm.yaml` scanner schema is unchanged, and the `ensemble.*` keys remain
  valid (they were claimed as live in v1.28.0, not deprecated). The
  deprecation list is frozen; this release adds no new deprecations.
  **Migration:** delete the listed keys from your config file — see the
  deprecated-key migration table in the v1.28.0 entry below for the full list
  and per-key dispositions. Nothing changes behaviorally — these values were
  already ignored.

### Notes

- **Corrected commit provenance.** Commit `d08893d9`'s message incorrectly
  attributed the fail-closed serve safety-policy change to its completion and
  tmux diff. The actual safety-policy implementation is `dda4aae8`; history
  is preserved and this note is the forward correction.

---

## [v1.28.0] -- 2026-08-18 [GitHub Release]

**The issue-triage release: Grok Build phase 2, the Agent Mail delivery loop closed, and every open GitHub issue resolved** (#231, #251, #252, #253), plus the post-v1.27.0 review fixes and the second staged config-deprecation batch.

### Features

- **Grok Build phase 2 (#251).** Grok panes graduate from spawn-only to full swarm citizenship: readiness/working/composer patterns observed from the real authenticated TUI (permanent `│ ❯ │` composer chrome, braille spinner phase verbs, `Worked for Ns` turn ends), composer-gated `send`/`--robot-send` with per-submission verification and one-Enter rescue, plain-Ctrl+C interrupt, restart/relaunch honoring `--model`/`--effort` overrides, assignment, wait-until-ready, and health grading — every surface live-verified through ntm against the real TUI (~45 refusal tests across 16 packages became acceptance tests). Persona injection alone stays refused: the grok CLI has no system-prompt mechanism.
- **Coordinator mail nudge (#231).** A default-off coordinator checker (`[coordinator] mail_nudge`, `nudge_cooldown_seconds`, `nudge_message`) closes the Agent Mail delivery loop: idle panes with unread mail get a composer-verified "check your inbox" nudge through the gated dispatch path — never into a working pane, fail-closed for undetectable agent types, per-pane cooldown watermarks, every decision on the attention feed, proven by a fakeagent E2E.
- **Configurable bv timeout (#253).** `[integrations.bv] timeout_seconds` (default 30) and `NTM_BV_TIMEOUT` govern every bv subprocess; caller-set `BV_*` env vars provably reach the child, so `BV_NO_CACHE=1 BV_ROBOT_HISTORY_TIMEOUT_MS=2000` wrappers are no longer needed.

### Reliability

- **Linked worktrees are one project (#252).** Session project identity resolves through `git rev-parse --git-common-dir`, so a base checkout plus its `--worktrees` panes group as one repository (the base checkout wins as canonical root) while genuinely distinct repositories are still refused.
- **Post-v1.27.0 review fixes now released:** routing filter keys canonicalize agent-type aliases; the JSON/robot session-kill path clears routing state; the workflow review gate counts only the gating role's approvals; kqueue stat-fallback baselines survive snapshot races; `ntm conflicts --json`/`changes --json` emit `[]` instead of `null`; `FindGitRoot` carries a bounded TTL cache; `swarm.force_global_auth_clobber` config wiring actually reads the config; nine dangling references from the dead-code sweep corrected (including a secret-scanning ignore that had silently unshielded live redaction fixtures).

### Deprecated — config keys (second dead-knob batch, bd-6otuk)

- **128 reader-less config keys deprecated, WS6-style staged removal** (G2
  config-key liveness audit, `bd-6otuk`): each key below is now parsed by
  nothing — setting it still loads, but emits one loud startup **warning**
  per key naming the key and its disposition, and the value is **ignored**.
  In v1.29.0 these keys become hard strict-loader **errors** with the same
  disposition text, exactly like the v1.26.0→v1.27.0 flip. `ntm doctor`
  lists every deprecated key present in the config file (warning check);
  `ntm config set` persistence tolerates them for this release.

  Deprecated-key migration table (v1.28.0 → error in v1.29.0):

  | Key family | Keys | Disposition |
  |---|---|---|
  | `[accounts]` (whole section) | `state_file`, `auto_rotate`, `reset_buffer_minutes`, `[[accounts.<provider>]]` `email`/`alias`/`priority` for claude, codex, gemini, antigravity, cursor, windsurf, aider, ollama (27) | never had an effect — account rotation reads `[rotation]` and caam, not `[accounts]` |
  | `[scanner.*]` except `ubs_path` | all of `[scanner.defaults]`, `[scanner.thresholds.{pre_commit,ci,dashboard,interactive}]`, `[scanner.tools]`, `[scanner.beads]`, `[scanner.notifications]` (37) | never had an effect — only `scanner.ubs_path` is read; the auto-scan chain never shipped. The project-level `.ntm.yaml` scanner schema is unchanged |
  | `[spawn_pacing]` rate/backoff/headroom | `max_spawns_per_sec`, `burst_size`, `default_retries`, `retry_delay_ms`, `backpressure_threshold`, `agent_caps.*_rate_per_sec`, `agent_caps.*_ramp_up_delay_ms`, `agent_caps.cooldown_on_failure_ms`, `agent_caps.recovery_successes`, all `[spawn_pacing.backoff]`, all `[spawn_pacing.headroom]` (24) | never had an effect — live pacing knobs: `enabled`, `max_concurrent_spawns`, `agent_caps.{claude,codex,gemini}_max_concurrent` |
  | `[cass]` subset | `show_install_hints`, all `[cass.duplicates]`, `[cass.search]`, `[cass.tui]` (10) | never had an effect — duplicate checking is driven by CLI flags; `[cass]` core + `[cass.context]` stay live |
  | integrations leaves | `caam.{enabled,auto_rotate,providers}`, `rano.{binary_path,providers}`, `process_triage.binary_path`, `rch.{min_build_time,show_location,preferred_worker,dcg_whitelist,fallback_local}` (11) | never had an effect — adapters resolve binaries from PATH; `rch.dcg_whitelist` was a documented legacy no-op |
  | `[checkpoints]` subset | `auto_checkpoint_on_spawn`, `interval_minutes`, `on_rotation`, `on_error` (4) | never had an effect — the background checkpoint worker never shipped; the other `[checkpoints]` knobs and manual `ntm checkpoint` are unaffected |
  | `[tmux.activity_indicators]` | `enabled`, `active_seconds`, `stalled_seconds` (3) | never had an effect |
  | `[robot.output]` subset | `pretty`, `timestamps`, `compress` (3) | never had an effect — `robot.output.format` remains live |
  | rotation subset | `rotation.prefer_restart`, `rotation.accounts.priority` (2) | never had an effect — rotation accounts are used in the order written |
  | singles | `agent_mail.program_name`, `agents.default_count`, `recovery.stale_threshold_hours`, `resilience.rate_limit.patterns`, `suggestions_enabled`, `preflight.enabled`, `command_hooks.description` (7) | never had an effect — the agent-mail program name is fixed to `ntm`; rate-limit patterns are built into `internal/agent`; `preflight.strict` and `command_hooks.name` remain live |

  **Migration:** delete the listed keys from `~/.config/ntm/config.toml`
  (the startup warning names each one). Nothing changes behaviorally — these
  values were already ignored.

- **Excluded from deprecation — `ensemble.*` (24 keys stay valid)**: the
  audit's premise ("reader compiled out of shipped binaries") was only half
  true. Eight ensemble defaults (`default_ensemble`, `agent_mix`,
  `assignment`, `allow_advanced`, `mode_tier_default`, `budget.total`,
  `budget.per_agent`, `cache.enabled`) are read in EVERY build by the
  `--robot-ensemble-spawn` config-default path and are now claimed as live;
  the remaining 16 (`synthesis.*`, `cache.*`, `budget.{synthesis,context_pack}`,
  `early_stop.*`) drive real spawns under `-tags ensemble_experimental` and
  are claimed under that tag, with permanent build-tag-blind-spot allowlist
  entries on the untagged side.

### Fixed

- **`swarm.force_global_auth_clobber` now works** (`bd-6otuk`): the config
  value was overwritten by the `--force-global-auth-clobber` flag's
  hard-coded `false` default before any read, making the knob a silent
  no-op. The config value now seeds the flag default (mirroring
  `--auto-rotate-accounts`); an explicit flag still overrides.

- **Corrected commit provenance.** Commit `d08893d9`'s message incorrectly
  attributed the fail-closed serve safety-policy change to its completion and
  tmux diff. The actual safety-policy implementation is `dda4aae8`; history
  is preserved and this note is the forward correction.

### Reliability

- **Post-v1.27.0 follow-up fixes.** Routing state for killed sessions is
  dropped and filter keys are canonicalized before lookup
  ([c28737ee](https://github.com/Dicklesworthstone/ntm/commit/c28737ee));
  workflow review gates count approvals per role and keep fresh kqueue
  baselines after missed-write stat fallbacks
  ([49ff8087](https://github.com/Dicklesworthstone/ntm/commit/49ff8087));
  the guard scripts take the first bead id from `br list` output instead of
  the last token
  ([1b1259f4](https://github.com/Dicklesworthstone/ntm/commit/1b1259f4));
  and the REST beads isolation test proves isolation with a marker title
  rather than a row count
  ([2979458b](https://github.com/Dicklesworthstone/ntm/commit/2979458b)).

---

## [v1.27.0] -- 2026-08-18 [GitHub Release]

**The bug-fix and backlog-burndown release.** Every open defect bead from the
reality-bridge review gates is fixed, the pre-existing E2E debt is retired, and
the staged config removal completes its second phase.

### Changed — BREAKING

- **Previously-deprecated config keys now fail the loader** (WS6-remove-finalize,
  `bd-ws6-config-truth-ienmd.3`): the config keys removed in v1.26.0
  (WS6-remove, `bd-ws6-config-truth-ienmd.2`) no longer load with a
  deprecation warning — each one is now a hard strict-loader **error** with
  the same key + disposition text the v1.26.0 warning carried. A single
  failed load lists **every** removed key present (plus any genuinely unknown
  fields), so one pass over the error tells you everything to delete.
  `ntm config set`/persistence validation rejects the same keys with the same
  disposition text. `ntm doctor` still names each removed key present in a
  config file (now as an error check) because it scans the file leniently —
  useful precisely when the strict loader refuses to start. The removal list
  is frozen; this release adds no new removals. **Migration:** delete the
  listed keys from your config file — see the removed-key migration table in
  the v1.26.0 entry below for the full list and per-key dispositions.

### Reliability

- **Review-gate follow-ups, all closed.** cm refuses port-fallback duplicate daemons; the guard degraded-ledger splits unreachable-vs-error reasons, self-prunes, and doctor checks that `ntm` is on the hook's PATH; the MCP health probe rejects port squatters; conflict negotiation publishes honest `conflict_release_requested` outcomes with cooldown-after-success and deep-copied holder state; deadlock wording is honestly advisory; workflow runner state is race-clean with serialized dispatch, review gates cover every role/target shape, kqueue-missed file writes fire via a stat fallback, and tilde paths resolve.
- **Routing hardened end-to-end.** Rotation state is keyed by `(session, filter)` with pane-anchored cursors that skip nothing when panes vanish; advisory routes take no write locks; stale rows purge; affinity resolves session-first project keys through a process-shared TTL cache; the coordinator's auto-assign selects by multi-factor score instead of first-fit (the redundant strategy layer is deleted).
- **Truth in envelopes and jobs.** Job stores are bounded with deterministic listings; nested list-shaped schema types are flagged (28 caught); arrays-never-null covers by-value payloads and compact capabilities; assignment skip reasons are honest; mixed-target CASS injection uses neutral formatting and records config-disabled skips.
- **Platform and test integrity.** Darwin liveness answers from one sysctl call (eliminating 42 subprocess spawns per status invocation); the integration test package fails loudly in CI instead of silently no-opping, and REST beads tests are provably isolated from the repository's own beads DB; `@vscode/vsce` is vendored so vsix builds fetch nothing at release time; the canonical-pane/replay/spawn E2E clusters pass against the hardened delivery gates via a new fakeshell fixture (14 tests un-stuck, all siblings green).

### Verification And Release

- The G1 dead-code backlog burned 607 allowlist lines (six orphan packages, eight dead files, sixteen partial strips — ~24k LOC); all 408 live config keys carry liveness claims with 153 reader-less knobs re-tagged for their own staged removal; all 12 waived designer docs are rewritten to shipped shapes and every legacy stub-marker comment is resolved; the audit ledger sampler is era-scoped.

---

## [v1.26.0] -- 2026-08-18 [GitHub Release]

**R3 of the reality-bridge program: the deletion & config-truth release.** Wave
W4 removed ~13,700 lines of dead sophistication under the operator's recorded
sign-off ([77744bfe](https://github.com/Dicklesworthstone/ntm/commit/77744bfe)),
moved every reader-less config knob into staged removal with a one-release
migration runway, and closed the program's allowlist register down to
backlog + permanent composition only (zero registered-item lines) — verified by
an independent W4 review gate with spot-revert proof-of-proof and the scripted
v1.26.0 reality re-audit (10/10 spot-probes WORKS against a release-style
binary; `docs/reality/audit-v1.26.0.md`).

### Removed (W4 deletion batch, operator-approved 2026-08-17)

- **Dead auto-respawner deleted** (C5, `bd-ws2-wire-or-delete-ykmcz.5`): the
  never-wired 1,227-line `internal/swarm/auto_respawner.go` and its dead-only
  tests are gone; the shipped resilience/respawn path is unaffected. The live
  graceful-exit key-sequence helpers it hosted moved to
  `internal/swarm/graceful_exit.go`. Git history is the attic.
- **Four redundant dashboard panels deleted** (C6-delete,
  `bd-ws2-wire-or-delete-ykmcz.7`): the never-instantiated compare, context,
  effectiveness, and ensemble panels duplicated `ntm compare`, the context
  CLI, and the ensemble subcommands. The wired quota, ratelimit, and accounts
  panels (and the history panel) stay.
- **Pressure governor `Gate()` and the bottleneck profiler deleted** (C8,
  `bd-ws2-wire-or-delete-ykmcz.9`): zero callers, and the vnext verification
  matrix's cited `BenchmarkGate` evidence never existed. The live pressure
  snapshot/admission path (`New`/`Refresh`/`Latest`/`RobotSnapshot`,
  `EvaluateSpawnAdmission`) and the live profiler spans/recommendations
  surface (`ntm --profile`) are untouched;
  `internal/profiler/{bottleneck,backpressure}.go` and the unreachable
  profiler report/timing helpers are gone.
- **Nine orphaned packages, the caut coordinator, and the rch build-storm
  advisory deleted** (C9, `bd-ws2-wire-or-delete-ykmcz.10`):
  `internal/{contentionforecast,dispatchplan,doctor,driftaudit,evidencebudget,fairness,faultharness,identityhygiene,parity}`
  had no importers outside their own tests; the caut→CAAM coordinator and the
  rch build-storm advisory cascaded with them. Repo-wide reference sweep at
  the W4 gate found zero live references to any deleted surface; the
  survivors it verified by test (resilience monitor, quota/ratelimit/accounts
  panels, `pressure` governor snapshot path, caut `CachedClient`,
  `kernel.Run`) all stay.
- **The kernel-registry OpenAPI generator and its TypeScript predecessor
  deleted** (WS4-resolution, `bd-ws4-openapi-parity-wpwck`):
  `docs/openapi-kernel.json` (a 13-path hand-mirror of a 263-handler router)
  and `scripts/gen_openapi.ts` are gone. The served chi router is the single
  source of truth — `ntm openapi generate` walks it hermetically, and the
  `openapi-drift` CI job regenerates and diff-gates `docs/openapi.json` +
  `docs/parity_matrix.json` on every build.
- **Reader-less config knobs removed — staged, with a one-release migration
  runway** (WS6-remove, `bd-ws6-config-truth-ienmd.2`): every config key that
  was parsed, validated, and printed but read by nothing is removed. In
  v1.26.0 a config that still sets one of these keys **loads normally** and
  the loader emits a loud per-key deprecation warning naming the key and its
  disposition; `ntm doctor` lists the same warnings. In v1.27.0 the warnings
  become strict-loader errors with identical text
  (`bd-ws6-config-truth-ienmd.3`). One knob escalated to KEEP instead:
  `integrations.xf.enabled` has a live reader (it gates the built-in
  `xf-search` palette entry) and now carries a G2 liveness claim plus a
  behavior test.

  Migration table (all removed keys; disposition: removed, no replacement —
  the key never had an effect):

  | Removed key(s) | Notes |
  |---|---|
  | `tmux.palette_key` | dead knob (H3 decision) |
  | `integrations.caam.rate_limit_patterns`, `.account_cooldown`, `.alert_threshold` | built-in behavior unchanged |
  | `[integrations.caut].*` (whole section) | cascades from the C9 caut coordinator deletion |
  | `[integrations.proxy].*` (whole section) | rust_proxy integration had no reader |
  | `integrations.process_triage.on_stuck` | no stuck-action machinery ships |
  | `integrations.rano.persist_history`, `.history_days` | no history persistence ships |
  | `integrations.xf.bin_path`, `.archive_path`, `.default_mode` | xf resolves from PATH; use `--xf-mode` per invocation (`integrations.xf.enabled` KEPT) |
  | `[rotation.dashboard].*` (whole section) | dashboard panels never read it |
  | `[swarm.limit_patterns]`, `[swarm.marching_orders]` | never read by the swarm engine |
  | `[retry.scheduler]`, `[retry.completion]`, `[retry.db]`, `[retry.assign]` | no such retry loops ship; live overrides are `[retry.webhook]`, `[retry.alerts]`, `[retry.agent_mail]` |
  | `memory.include_anti_patterns`, `memory.include_history` | no substrate |

  Genuinely unknown keys remain hard load errors, exactly as before.

### Infrastructure

- **The allowlist register reached its end-state composition:** zero
  registered-item lines remain; every surviving line is backlog-tagged
  (`config` 408, `deadcode` 2058 + 49 C4 residue, `docs` 12, `placebo` 25)
  or `# permanent:` (2 deadcode test hooks), with `contracts` at 0 —
  enforced by `check_allowlists.sh`'s closed-bead and both-direction
  ratchet checks.
- **Reality re-audit v1.26.0 committed** (`docs/reality/audit-v1.26.0.md`):
  10/10 seeded spot-probes WORKS against the release-style binary; the one
  guard failure it caught (a selftest fixture pinning a bead that has since
  closed) was root-caused and fixed in
  `scripts/guards/allowlist_selftest.sh`, which now discovers a live open
  bead instead of pinning one.

---

## [v1.25.0] -- 2026-08-17 [GitHub Release]

**R2 of the reality-bridge program: the wiring release.** Waves W2+W3 wired every
"engine built, never reachable" capability to a real user surface, shipped the
never-released deliverables, and made the docs tell the truth — verified by
independent review gates with proof-of-proof spot-reverts and the first scripted
reality re-audit (10/10 probes against a release-style binary).

### Features

- **The web UI ships.** All 12 dashboard routes (including new memory and rebuilt agents pages) export statically, ride the binary via go:embed (+1.2 MB), and serve from `ntm web` / `ntm serve --web` with version lockstep and a weekly dependency-audit CI job.
- **The VSCode extension ships.** Compiles in CI, packages as a release `.vsix` (pinned toolchain), documented install-from-vsix.
- **Ensemble spawn ships.** The `ensemble_experimental` tag rides release builds after its fakeagent E2E bar went green live (mode-disjoint dispatch proven from fixture event logs).
- **`ntm workflow run`** executes the previously-unreachable RuntimeCoordinator against live panes through gated dispatch — all four builtins plus user TOMLs, live-proven (red-green handoff transcript-asserted).
- **`--with-cass` / `--no-cass`** wire send-time CASS context injection on both send surfaces, redacted and framed data-not-instructions, with a degraded skip path.
- **Coordinator conflict negotiation** runs behind the persisted flag with published outcomes; **deadlock detection** is reachable via `locks --check-deadlocks`, the robot envelope, and the digest; **reservation-affinity scoring** populates from live Agent Mail data; **`--dist-strategy`** routes through real planners with propagated confidence; the **quota, ratelimit, and accounts panels** joined the dashboard.
- **`ntm guards install`** writes a hook that actually checks staged files against reservations (visible fail-open, strict opt-in); **`ntm memory serve`** really supervises cm with an MCP-aware health probe.

### Reliability

- **Contract breadth is now enforced, not asserted:** arrays-never-null is an encoder invariant with a registry-walk test; pagination covers five formerly-unbounded surfaces with exact machine-checkable scope; Idempotency-Key covers all 73 mutating routes with single-flight and a bounded LRU; the jobs API dispatches three real operations (cancel actually cancels) and returns honest NOT_IMPLEMENTED elsewhere.
- **OpenAPI and the parity matrix are generated from the served router** (158 paths/178 ops, deterministic, drift-gated in CI), replacing a stale hand-curated spec.
- **Config knobs govern real behavior:** `[retry]` drives three retry loops, rotation thresholds bind, `[recovery]` aliases with deprecation warnings; token-efficiency claims are measured (markdown ~84%, TOON ~39%) with pinned floors.
- **UX truth:** NDJSON under `--json` for watch surfaces; `chars_sent` equals delivered bytes; per-pane best-effort interrupt with `PARTIAL_INTERRUPT` envelopes; personas wired for gmi/agy with loud grok refusal; palette help generated from the keymap (dead keys gone, q-quits-while-typing fixed); installer checks tmux and PATH loudly; approve history returns every decision state; user panes titled with visible border-status; shell completion covers all 114 commands from the cobra tree.
- **Docs truth:** every ORCHESTRATION example is real (skip-budget burned to zero), planned-but-shipped claims corrected under grep-gates, the vnext verification matrix cites only gates that exist.

- **Arrays-never-null is now an encoder invariant, not a constructor
  convention.** (bd-ws3-contract-breadth-psvyu.2) Every robot envelope —
  success and failure terminals alike — is normalized through
  `EnsureArraysNeverNull` before encoding, so slice fields marshal as `[]`
  (or are omitted via `omitempty`), never as JSON `null`. A registry-walk
  conformance test (`TestArraysNeverNullRegistryWalk`) zero-instantiates
  every registered schema type and fails on any null-where-array, with an
  empty exception list; new envelope types are covered at registration time.
- **Pagination scope is exact, exhaustive, and machine-checkable; five
  unbounded list surfaces gained real offset/limit paging.**
  (bd-ws3-contract-breadth-psvyu.1)
  - `ntm list`, `ntm history`, `ntm audit show/search`,
    `ntm checkpoint list`, and `ntm approve list` accept `--limit/--offset`
    and return `count`/`total_matches`/`has_more` plus
    `_agent_hints.next_offset` in JSON mode (`ntm history` offsets count
    back from the newest entry so the default stays "last N").
  - The WS0-G6 single-declaration flag map
    (`internal/robot/schema_pagination.go`) marks EVERY list-shaped schema
    type `paginated: true/false` with a recorded reason; an unflagged list
    type fails the registry-walk conformance test, and
    `--robot-capabilities` exposes `paginated`/`paginated_reason` per
    surface (plus a `pagination_contract_violations` self-check, empty on
    healthy builds).
  - Registry metadata that over-claimed offset pagination
    (`attention`, `events`, `beads-list` Boundedness) was corrected to
    cursor/limit truncation honesty, and
    `docs/robot-ordering-pagination.md` now scopes the pagination claim to
    the exact flagged surface table instead of implying list-wide support.
- **Assignment strategies are real now: `--dist-strategy` / `--strategy`
  route through the graph-aware planner.** (bd-ws2-wire-or-delete-ykmcz.4)
  - Previously `--dist-strategy` (send `--distribute`) and `--strategy`
    (`--robot-assign`) were cosmetic: beads were paired with idle agents
    sequentially regardless of strategy, and only the printed reasoning
    changed — the output claimed strategy behavior that never ran.
  - `balanced`, `speed`, `quality`, and `dependency` now run the real
    planner in `internal/assign`: pairings are scored against the agent
    capability matrix, and `dependency` consumes the bead graph's
    unblocks fan-out (blockers that unblock more work are assigned first).
    Each strategy produces genuinely different assignments (proof:
    `TestPlanAssignments_*` in `internal/robot`).
  - Planner confidence is propagated instead of discarded: `--robot-assign`
    recommendations carry the planner's confidence, and the distribute
    envelope's recommendation objects gain a `confidence` field.
  - **Behavior/name change for defaults:** the historical sequential
    pairing keeps running under its honest name `simple`, which is the new
    default for both flags (previously the default was *named* `balanced`
    while *behaving* sequentially). Users who never pass the flag get
    byte-identical pairing behavior, but envelopes now report
    `"strategy": "simple"`, and the simple reasoning string honestly says
    "simple sequential pairing" instead of claiming a strategy.
    Explicitly passing `balanced`/`speed`/`quality`/`dependency` now
    changes real assignment output. Defaults are pinned by
    `TestAssignStrategyDefaultPinnedToSimple` and
    `Test*StrategyFlagDefaultPinnedToSimple`.

- **Config truth (WS6-wire): `[retry]`, `rotation.thresholds`, and the
  `memory.*`/`[recovery]` shadowing are now real.**
  (bd-ws6-config-truth-ienmd.1)
  - `[retry]` gains its first real readers. `config.RetryPolicyFor` now
    governs three shipping retry loops: webhook event delivery
    (`[retry.webhook]` + the global backoff shape knobs `max_delay_ms`,
    `backoff_factor`, `jitter`), robot alert webhook delivery
    (`[retry.alerts]`, inheriting globals), and Agent Mail MCP busy retries
    (new `[retry.agent_mail]` override; defaults 3 retries / 500ms preserve
    the historical hardcoded loop). Defaults are behavior-identical to the
    previous hardcoded values; `retry.jitter` now defaults to `false`
    because no shipped loop ever jittered while the section was dead —
    jitter is strictly opt-in.
  - `rotation.thresholds.*` now governs the rotation engine.
    `warning_percent`/`critical_percent` classify quota readings in
    `ntm rotate all-limited` (a pane at or above `critical_percent` is now
    selected for rotation even before the provider hard-limits it;
    `warning_percent` readings are surfaced without rotating).
    `restart_if_tokens_above`/`restart_if_session_hours` add restart
    triggers to the coordinator's context-rotation check (active only when
    `rotation.usage_percent_threshold` > 0 enables that checker).
  - `[recovery]` is now the single session-recovery section. The
    overlapping deprecated `memory.*` keys (`include_in_recovery`,
    `max_rules`, `query_timeout_seconds`, and the recovery side of
    `enabled`) are aliased into their `[recovery]` counterparts for this
    release with a loud per-key deprecation warning; the aliases are
    removed in v1.27.0. Explicit `[recovery]` keys always win. The
    send-scoped `memory.send_*` keys are unaffected and stay in `[memory]`.

- **`ntm metrics snapshot list` now really lists saved snapshots.** The
  command previously returned an empty result unconditionally while
  `snapshot save` and `compare` genuinely persisted to the
  `metric_snapshots` table, telling users their saved snapshots did not
  exist. The missing List query is now implemented
  (`Collector.ListSnapshots`): snapshots are returned oldest-first, scoped
  to the session, and the `--json` envelope's `snapshots` array is always
  present (empty array, never null). Proof tests:
  `TestMetricsSnapshotList_SaveTwoThenListJSON`,
  `TestMetricsSnapshotList_EmptyDBJSON`,
  `TestCollectorWithStore_ListSnapshots` (bd-ws1-truth-safety-l5ddi.3).

- **Corrected commit provenance.** Commit `d08893d9`'s message incorrectly
  attributed the fail-closed serve safety-policy change to its completion and
  tmux diff. The actual safety-policy implementation is `dda4aae8`; history
  is preserved and this note is the forward correction.

---

## [v1.24.4] -- 2026-08-16 [GitHub Release]

**R1 of the reality-bridge program** -- a project-wide reality check (code vs documented promises) produced systemic CI guards and this first truth-and-bugs wave.

### Reliability

- **Placebo surfaces made real.** `ntm metrics snapshot list` runs an actual session-scoped query instead of unconditionally returning an empty list; `work queue-dry --create-beads` resolves the roadmap parent from the *target* project (flag, then a single open epic, never a hardcoded foreign epic) and reports `dry_run` truthfully from what actually happened.
- **Live bugs fixed.** Worktree merge/sync detect the repository's default branch instead of assuming `main`; a failed quota fetch reads UNKNOWN instead of healthy; `--robot-dcg-status` reports the real `[integrations.dcg]` config and counts actual check cycles; session labels split on the canonical `--` helper (three copy-paste extractors unified); robot-mode spawns now start the same resilience monitor CLI spawns get, with envelope-visible best-effort semantics; `random` routing is genuinely random and sticky/round-robin persist per-session state (migration 019).
- **Review-gate catches.** Monitor manifests record live `%N` pane IDs (the physical-address form would have read every robot-spawned agent as crashed); dry-run routing no longer advances persisted cursors.

### Verification And Release

- **Seven systemic CI guards** now make the audited gap classes unrepresentable: a dead-code gate (deadcode over `cmd/ntm`, multi-GOOS), a config-key liveness test, a docs-example conformance test (which found 80 broken examples on its first run), a release-artifact lockstep preflight, a placebo lint, single-definition contract lints, and both-direction allowlist ratchets with mandatory firing canaries. The hermetic serve parity harness runs in CI with an executed-count>0 assertion -- which immediately exposed that the integration test package had silently never run in CI (bd-zaarm).
- The One Reachable Implementation rule is now an ADR in AGENTS.md; a scripted 90-minute re-audit ritual (`scripts/reality_audit.sh`) becomes the release gate.

---

## [v1.24.3] -- 2026-08-16 [GitHub Release]

**6 commits since v1.24.2** -- a fourth fresh-eyes pass: the released contract smoke-verified live, the E2E backlog triaged to root cause, and two small shipped defects corrected.

### Reliability

- **Solo operators are no longer stranded by the force-release gate.** The approval-required refusal now explains that self-approval is rejected for SLB requests and names the escape hatch (`ntm policy automation --force-release auto`); the approval workflow is documented where force-release appears in the README and operator references ([5a8ddc6b](https://github.com/Dicklesworthstone/ntm/commit/5a8ddc6b)).
- **CAAM operand validation scans the raw name.** The control-byte check ran over the trimmed string while the raw string reached the caam argv, so whitespace-wrapped control bytes passed; both call sites now agree and scan the raw operand ([3dfcfcda](https://github.com/Dicklesworthstone/ntm/commit/3dfcfcda)).

### Verification And Release

- Smoke-verified the released v1.24.2 binary live against its own source contract -- config surfaces, refusal envelopes, approval flow, capabilities/schema, idempotent preflight replay, disk metrics -- with zero deviations found ([d5e8a524](https://github.com/Dicklesworthstone/ntm/commit/d5e8a524)).
- Triaged the full E2E backlog to root cause: the atomic-assignment cluster was the composer-visibility gate's intentional fail-closed hardening outpacing the test harness (panes now primed with real composer glyphs), and the grep/doctor/dedupe/ensemble clusters were pre-session test drift -- 62/66 of the affected tests now pass, with the remaining pre-session fixture work tracked in bd-h4t0j ([73e30fe6](https://github.com/Dicklesworthstone/ntm/commit/73e30fe6), [173ece8f](https://github.com/Dicklesworthstone/ntm/commit/173ece8f), [d5e8a524](https://github.com/Dicklesworthstone/ntm/commit/d5e8a524)).

---

## [v1.24.2] -- 2026-08-16 [GitHub Release]

**8 commits since v1.24.1** -- a third fresh-eyes pass: race-detector-clean, security-hardened, Windows-correct.

### Reliability

- **Windows: process liveness detection actually works.** The POSIX signal-0 probe silently reports every process dead on Windows (Go's `os.Process` implements only `Kill` there), which made v1.24's CM daemon discovery skip live daemons and serve's memory-daemon health check judge running daemons stopped; liveness now uses a native check on Windows ([b45681bc](https://github.com/Dicklesworthstone/ntm/commit/b45681bc)).
- **Security hardening across the new surfaces.** Scanner-derived finding text is sanitized (control bytes stripped, whitespace collapsed, per-field length caps) and explicitly attributed as data-not-instructions before any pane nudge; the bugs digest and attention-item paths now pass through the same redaction as dispatch; CM rule content is scrubbed of non-printable runes before send; caam account operands are validated against flag-shaped names; the serve force-release 403 no longer leaks policy file paths or content into HTTP bodies; and `ntm approve --help` now states plainly that approver identity is asserted, not authenticated ([bc8dceae](https://github.com/Dicklesworthstone/ntm/commit/bc8dceae), [2ce89523](https://github.com/Dicklesworthstone/ntm/commit/2ce89523), [8b4ccc79](https://github.com/Dicklesworthstone/ntm/commit/8b4ccc79), [eb05b264](https://github.com/Dicklesworthstone/ntm/commit/eb05b264)).
- **Portable fakeagent fixture.** The E2E fixture's SIGWINCH handling moved behind build tags so `GOOS=windows go build ./...` is clean ([3e914cd3](https://github.com/Dicklesworthstone/ntm/commit/3e914cd3)).

### Verification And Release

- Ran the race detector across the approval, state, robot, coordinator, cli, and serve packages -- zero data races; a cold-read sweep of the full v1.23.0..HEAD production diff confirmed the remaining suspicious patterns benign, and stale doc comments orphaned by the wave's refactors were removed ([54fc1575](https://github.com/Dicklesworthstone/ntm/commit/54fc1575)).

---

## [v1.24.1] -- 2026-08-15 [GitHub Release]

**3 commits since v1.24.0** -- a same-day second fresh-eyes pass over the v1.24.0 wave.

### Reliability

- **Security: approval decisions can no longer race each other.** Pending approvals now transition to approved, denied, or expired through a status-guarded SQL update, so a concurrent `ntm approve` in another process can never overwrite a landed denial (or a lazy expiry clobber a landed decision) -- closing the last blind write in the machinery hardened for v1.24.0's force-release gate ([6db712e9](https://github.com/Dicklesworthstone/ntm/commit/6db712e9)).
- **Cross-version idempotent replay restored.** The send-operation binding hash writes the new with-memory toggle only when set, so byte-identical retries of operations recorded by v1.23.0 replay their recorded outcome instead of failing with a spurious `IDEMPOTENCY_CONFLICT`; the pre-1.24 hash formula is pinned field-for-field by a regression test ([6db712e9](https://github.com/Dicklesworthstone/ntm/commit/6db712e9)).
- **Refusals are visible.** The dashboard conflict force action's policy refusal raises an on-screen toast instead of vanishing into a log file; `ntm bugs watch --json` emits a `PERMISSION_DENIED` envelope with a remediation hint; and `ntm config show --json` gains the `memory`, `bugs`, and `rotation` sections plus the CAAM failover and disk-horizon keys ([6db712e9](https://github.com/Dicklesworthstone/ntm/commit/6db712e9)).

### Verification And Release

- Reverified all eight fakeagent E2E suites green at HEAD after the hardening wave (48 pre-existing failures elsewhere in the tagged suite were triaged as predating v1.24.0 and tracked separately); corrected three stale commit counts and one release label in the changelog backfill, and documented `--with-memory` in the AGENTS.md robot-send modifier table ([6db712e9](https://github.com/Dicklesworthstone/ntm/commit/6db712e9)).

---

## [v1.24.0] -- 2026-08-15 [GitHub Release]

**22 commits since v1.23.0** -- a controllable fakeagent E2E persona with six live real-tmux verification suites, coordinator-driven context rotation and CAAM auto-failover (both default-off), durable WebSocket replay, disk-usage trajectory metrics, CM memory injection, reservation-routed bug nudges, and a policy-gated force-release approval fix.

### Features

- **Fakeagent E2E fixture.** A controllable Go agent-TUI persona binary runs as a real non-shell pane process and satisfies NTM's detector contracts -- bottom-pinned composer markers, bracketed-paste semantics, working chrome, interactive gate screens, rate-limit banners, strand-N submit swallowing, and a JSONL ground-truth event log -- with live acceptance proving a robot-send delivers, verifies, and rescues a stranded composer against the actual binary in real tmux ([01402394](https://github.com/Dicklesworthstone/ntm/commit/01402394)).
- **Six new real-tmux E2E suites.** Live fakeagent proofs for idempotent send (claim, replay, conflict, takeover, receipt; #245), composer delivery (readiness refusal sends zero keystrokes; stranded-composer rescue), gates/restart (gated panes left untouched; `SHELL_NOT_RETURNED`), transcript context (hermetic-HOME Claude/Codex transcripts through `--robot-context`), send tracking/ack (echo-detected vs timeout), and live context rotation (threshold trigger with evidence, all four safety gates, a real pane replacement with handoff submit proof) ([6612c54a](https://github.com/Dicklesworthstone/ntm/commit/6612c54a), [de68c6b1](https://github.com/Dicklesworthstone/ntm/commit/de68c6b1), [73a91f6b](https://github.com/Dicklesworthstone/ntm/commit/73a91f6b)).
- **Coordinator context rotation on transcript usage.** `[rotation] usage_percent_threshold` (default 0 = off) enqueues a rotation when a pane's transcript-sourced usage -- never scrollback estimates -- crosses the threshold, through the same store `ntm rotate context pending/confirm` reads; safety gates (not working, not rate-limited, no interactive gate, no unsubmitted composer input) re-check a fresh pane capture at fire time, every skip is logged and published to the attention feed, and read-only coordinator surfaces force the threshold to 0 ([0827a84f](https://github.com/Dicklesworthstone/ntm/commit/0827a84f)).
- **Durable WebSocket replay.** WSEventStore persists sequence cursors on every event frame: cursor-based reconnect replays the gap exactly once then goes live, expired cursors get `stream.reset`, slow-client drops are recorded, retention pruning actually runs, and an O(n^2) ring-buffer walk is fixed -- proven by a full-server integration test ([de68c6b1](https://github.com/Dicklesworthstone/ntm/commit/de68c6b1)).
- **Disk-usage trajectory metrics.** `--robot-metrics`/`--robot-snapshot` carry a disk section (mount_used_pct, available_bytes, delta_bytes_per_min, projected_full_at) from cross-invocation watermark samples; optional `--disk-attribution` walks well-known build dirs under each pane's live cwd (bounded, no env persistence), and a disk_trajectory alert fires on `[alerts] disk_full_horizon_hours` ([de68c6b1](https://github.com/Dicklesworthstone/ntm/commit/de68c6b1)).
- **Build-slot orphan diagnosis.** Build-slot leases are reconciled against Agent Mail's archive; diagnose surfaces orphaned leases and `--fix` releases them using the registered holder tokens, worktree-mode transitions release orphans best-effort, and everything degrades gracefully when Agent Mail is away ([de68c6b1](https://github.com/Dicklesworthstone/ntm/commit/de68c6b1)).
- **CAAM auto-failover (default-off).** `[integrations.caam] auto_failover` switches accounts on banner-verified rate limits whose reset is past the horizon, only after a verified alternate CAAM account with headroom is found -- allow-listed providers, a one-hour per-pane cooldown, never interrupting a working pane -- and hardened to pass the same guardAutoRotation gates as every other automatic rotation (account pins honored, Codex global-auth clobber refused without safe-restore) ([3cfefd93](https://github.com/Dicklesworthstone/ntm/commit/3cfefd93), [177f33fc](https://github.com/Dicklesworthstone/ntm/commit/177f33fc)).
- **CM memory injection on robot sends.** `--robot-send --with-memory` (or `[memory] send_injection=true`) queries cm (MCP, then CLI fallback) and prepends the top-N rules inside `send_budget_tokens` as a compact project-rules block; memory is enrichment, never a gate -- a missing, wedged, or empty cm records a skip on the envelope and still sends -- with injected-rule outcome feedback reported automatically and a real-tmux test proving the block reaches the delivered pane payload ([d893cf91](https://github.com/Dicklesworthstone/ntm/commit/d893cf91), [177f33fc](https://github.com/Dicklesworthstone/ntm/commit/177f33fc)).
- **`ntm bugs watch`.** Periodically reruns the same UBS invocation as `ntm bugs list`, fingerprints findings, and routes each new one to the agent holding the affected file's Agent Mail reservation through the gated dispatch service (composer-ready, submission-verified, rate-limited per pane, never interrupting a working pane); unreserved paths go out as a single digest-style broadcast, and `[bugs] push_routing` is opt-in ([243330b3](https://github.com/Dicklesworthstone/ntm/commit/243330b3)).

### Reliability

- **Security: force-release approval machinery hardened (P1).** The approved -> consumed transition of a durable one-shot SLB approval is now atomic in SQL, so two racing processes can never both spend one approval; approved records expire with their validity window; the serve HTTP force-release endpoint and the dashboard conflict "force" action -- both of which previously bypassed the policy gate entirely -- are policy-gated fail-closed with blocked attempts written to the audit log; and a live two-person SLB approval E2E pins the workflow against the real binary with a hermetic HOME and state DB ([177f33fc](https://github.com/Dicklesworthstone/ntm/commit/177f33fc), [38fb5e1f](https://github.com/Dicklesworthstone/ntm/commit/38fb5e1f)).
- Auto-restart-stuck declines blocked and rate-limited panes with typed skip reasons: a restart cannot answer an interactive gate and does not lift a rate limit, so both become typed skips carried on every exit path, and SessionHealthSummary gains a blocked counter ([5c4a1451](https://github.com/Dicklesworthstone/ntm/commit/5c4a1451), [fa2f3b18](https://github.com/Dicklesworthstone/ntm/commit/fa2f3b18)).
- Fixed the bugs-watch pane-staleness clock that deferred every nudge forever under a real wall clock, registered the scanner digest identity before sending, wired `[bugs]` into config show/get/diff, and stopped ID-less CM rules from leaking empty strings into injection metadata ([177f33fc](https://github.com/Dicklesworthstone/ntm/commit/177f33fc)).

### Verification And Release

- Covered the GetSend durable idempotency claim branches with a real store and real tmux ([722f8c36](https://github.com/Dicklesworthstone/ntm/commit/722f8c36), [7490f1a7](https://github.com/Dicklesworthstone/ntm/commit/7490f1a7)), pinned CAAM auto-failover TOML, GetValue, Diff, and Validate behavior ([98592275](https://github.com/Dicklesworthstone/ntm/commit/98592275)), and covered bugs-watch routing, cooldown, and `[bugs]` config ([b4ae3e71](https://github.com/Dicklesworthstone/ntm/commit/b4ae3e71)).
- Hardened fakeagent start-waits in the gates-restart scenarios and closed the full E2E verification epic -- fixture plus scenario suites, all green against real tmux with fixture ground-truth assertions and JSONL step logging ([cf9ed007](https://github.com/Dicklesworthstone/ntm/commit/cf9ed007), [73a91f6b](https://github.com/Dicklesworthstone/ntm/commit/73a91f6b)).

---

## [v1.23.0] -- 2026-08-14 [GitHub Release]

**25 commits since v1.22.1** -- the GitHub issue-triage wave (#244-#249): durable robot-send idempotency and output sequencing, a CM MCP client rewrite, width-adaptive detection, interactive-gate health, and transcript-sourced context usage.

### Features

- **Durable send idempotency (#245).** A send_operations table claims each caller-supplied `--op-id` atomically before any keystroke is injected, bound to session, canonical targets, and a SHA-256 digest of the exact payload (payload bytes never stored); identical retries replay the recorded outcome without sending again, a reused ID with a different binding errors `IDEMPOTENCY_CONFLICT`, a race or mid-send crash reports `OPERATION_IN_PROGRESS`, and per-target admission receipts are queryable via `--robot-send-receipt` (REST `Idempotency-Key` is the same operation ID) ([ef727a7f](https://github.com/Dicklesworthstone/ntm/commit/ef727a7f), [d233570f](https://github.com/Dicklesworthstone/ntm/commit/d233570f), [b3dc8346](https://github.com/Dicklesworthstone/ntm/commit/b3dc8346)).
- **Durable output sequencing (#246).** A privacy-preserving output-change signal -- an opaque epoch plus a monotonic cursor that advances only when NTM detects a real output change for a pane (content hashed, raw bytes never stored) -- attached to activity and status reads ([ef727a7f](https://github.com/Dicklesworthstone/ntm/commit/ef727a7f)).
- **Transcript-sourced context usage.** Context reporting reads the agent's own session transcripts (Claude usage records; Codex token_count events with model_context_window) via bounded 256KB tail reads and pane-cwd correlation, reports models verbatim, walks Codex sessions newest-first skipping subagents, attributes transcripts only when a cwd is unambiguous, and keeps scrollback estimation as a clearly marked fallback ([edcc7d43](https://github.com/Dicklesworthstone/ntm/commit/edcc7d43), [2a8488fe](https://github.com/Dicklesworthstone/ntm/commit/2a8488fe), [88bc623d](https://github.com/Dicklesworthstone/ntm/commit/88bc623d)).
- **Width-adaptive detection.** Live tail windows scale with the real tmux pane width (capped 4x), so a narrow pane that wraps a spinner frame across many physical rows no longer classifies a working agent as idle; the detectors are folded into the primary agent APIs ([f0388b69](https://github.com/Dicklesworthstone/ntm/commit/f0388b69), [e6ef5764](https://github.com/Dicklesworthstone/ntm/commit/e6ef5764)).
- **Composer visibility and delivery readiness.** InspectComposer surfaces unsubmitted_input and queued_messages on `--robot-is-working` panes and `--robot-status` agents, the dispatch deliverer refuses typed prompts to panes whose screen positively shows neither composer marker nor working chrome (a typed, retryable failure instead of a silent swallow), and the queued-message footer is detected only on the live tail ([f0388b69](https://github.com/Dicklesworthstone/ntm/commit/f0388b69), [fb6c8657](https://github.com/Dicklesworthstone/ntm/commit/fb6c8657)).
- **Rate limits, spawn grammar, and Agent Mail parity.** Codex/Gemini usage-limit banners are detected on every surface with best-effort reset hints, robot spawn flags accept the CLI's `count:model@effort` grammar with `INVALID_FLAG` before any tmux mutation, and Agent Mail identity is registered on add/adopt and re-registered by the whole relaunch family ([7982a5e6](https://github.com/Dicklesworthstone/ntm/commit/7982a5e6)).
- **Ergonomics and models.** `--version`, a `--message` alias, and did-you-mean flag hints ([98a954f8](https://github.com/Dicklesworthstone/ntm/commit/98a954f8)); handoff `--auto` infers goal/now from captured recent pane output ([c9d62f35](https://github.com/Dicklesworthstone/ntm/commit/c9d62f35)); and the models registry gains Claude 5 family aliases at 200k context ([5f070f1f](https://github.com/Dicklesworthstone/ntm/commit/5f070f1f)).

### Reliability

- **CM MCP client rewrite (#249).** NTM now speaks MCP JSON-RPC at the cm daemon root instead of invented REST routes, surfaces HTTP-200-with-JSON-RPC-error bodies as real failures instead of dropping context silently, matches CM's history-snippet wire schema, and health-checks through the MCP client -- with a fake MCP daemon standing in for tests ([6935c2d8](https://github.com/Dicklesworthstone/ntm/commit/6935c2d8), [b3dc8346](https://github.com/Dicklesworthstone/ntm/commit/b3dc8346), [398531ec](https://github.com/Dicklesworthstone/ntm/commit/398531ec)).
- **Interactive gates never restart.** Shared gate detection (trust dialogs, auth/login gates, onboarding screens) drives health, is-working, session-health, and the fleet overview out of the false-healthy bucket with `HealthBlocked` and the gate text as reason; health prescribes MANUAL_INTERVENTION instead of restart; and restart prompt delivery gates on composer readiness with typed `RESTART_PROMPT_NOT_DELIVERED` / `SHELL_NOT_RETURNED` failures instead of typing into a bare shell ([edcc7d43](https://github.com/Dicklesworthstone/ntm/commit/edcc7d43), [32cc2ade](https://github.com/Dicklesworthstone/ntm/commit/32cc2ade), [a7b07644](https://github.com/Dicklesworthstone/ntm/commit/a7b07644)).
- The installer probes SHA256SUMS before checksums.txt without printing expected 404s (#247) ([d9d114e1](https://github.com/Dicklesworthstone/ntm/commit/d9d114e1), [3f69f016](https://github.com/Dicklesworthstone/ntm/commit/3f69f016)); project overlays may only disable integrations, never enable them ([57a18c39](https://github.com/Dicklesworthstone/ntm/commit/57a18c39)); spawn re-renders launch commands after assignment policy ([7e875979](https://github.com/Dicklesworthstone/ntm/commit/7e875979)); usage-limit banner vocabulary is deduplicated ([debbf943](https://github.com/Dicklesworthstone/ntm/commit/debbf943)); and send idempotency is bound to the input command with output-seq row GC ([d233570f](https://github.com/Dicklesworthstone/ntm/commit/d233570f)).

---

## [v1.22.1] -- 2026-08-06 [GitHub Release]

**1 commit since v1.22.0** -- default both Claude and Codex reasoning effort to xhigh ([1110bce2](https://github.com/Dicklesworthstone/ntm/commit/1110bce2)).

---

## [v1.22.0] -- 2026-08-06 [GitHub Release]

**386 commits since v1.21.0** -- a fleet-scale hardening release: repeated adversarial fresh-eyes audit waves over serve, robot, swarm, and tmux; real Claude credential isolation; fail-closed contracts everywhere; durable workflow state; and a test suite made trustworthy under live-fleet load.

### Features

- **Durable workflow and swarm state.** Per-pane prompt sequences persisted under `.ntm` with documented review rotations, workflow pause/stage state and runtime transition triggers that survive restarts, a dashboard panel that polls and toggles durable workflow state, and evidence-backed swarm productivity convergence reporting through robot output ([903d459a](https://github.com/Dicklesworthstone/ntm/commit/903d459a), [48b2d3c6](https://github.com/Dicklesworthstone/ntm/commit/48b2d3c6), [b08eb8bd](https://github.com/Dicklesworthstone/ntm/commit/b08eb8bd), [cf9406f5](https://github.com/Dicklesworthstone/ntm/commit/cf9406f5)).
- **Verified delivery evidence.** Optional `--verify-render` captures rendered proof that a robot-send actually reached the pane, pinned by a real-tmux rendered-delivery test ([f83b951a](https://github.com/Dicklesworthstone/ntm/commit/f83b951a), [499be1f5](https://github.com/Dicklesworthstone/ntm/commit/499be1f5)).
- **Canonical pane selectors everywhere.** `N` / `W.P` / `%ID` selectors accepted across `health --pane`, rotate, activity, and watch, with correct pane IDs in multi-window sessions ([0b41028d](https://github.com/Dicklesworthstone/ntm/commit/0b41028d), [d8c0ed03](https://github.com/Dicklesworthstone/ntm/commit/d8c0ed03)).
- **Live capture backpressure.** A backpressure tracker for overload snapshots plus single-owner pipe-pane claims so concurrent streamers cannot fight over one pane ([361d406f](https://github.com/Dicklesworthstone/ntm/commit/361d406f), [a6ac0188](https://github.com/Dicklesworthstone/ntm/commit/a6ac0188)).
- Cursor dependency detection now checks for the Cursor Agent CLI rather than the IDE binary (#233), and per-pane Claude credential isolation lands as a first-class spawn feature (#237) ([ce4c337f](https://github.com/Dicklesworthstone/ntm/commit/ce4c337f), [8b0be5a7](https://github.com/Dicklesworthstone/ntm/commit/8b0be5a7)).

### Reliability

- **Security: audit, redaction, and approvals.** Closed three mail read paths that bypassed secret redaction, gave each audit writer its own hash chain, rotated and pruned audit JSONL by retention with write failures counted, and fixed three concurrency defects in approvals, the scanner, and the audit store ([b5296509](https://github.com/Dicklesworthstone/ntm/commit/b5296509), [a0b14f4b](https://github.com/Dicklesworthstone/ntm/commit/a0b14f4b), [ff685137](https://github.com/Dicklesworthstone/ntm/commit/ff685137), [98449f40](https://github.com/Dicklesworthstone/ntm/commit/98449f40)).
- **Claude credential isolation made real.** The token no longer leaks through the launch environment, macOS no longer claims an isolation success it did not achieve, and isolation survives controller relaunch and session restore ([e27382ba](https://github.com/Dicklesworthstone/ntm/commit/e27382ba)).
- **Fail closed, everywhere.** Serve safety-policy status, unsafe batch rotation, inaccessible Keychain credentials, assignment lock-cancellation races, and live CAAM/RCH failure contracts all fail closed instead of open, and tmux errors are classified on stderr rather than on the argv that carries agent prompts ([dda4aae8](https://github.com/Dicklesworthstone/ntm/commit/dda4aae8), [d9a37979](https://github.com/Dicklesworthstone/ntm/commit/d9a37979), [851959c9](https://github.com/Dicklesworthstone/ntm/commit/851959c9), [ac3a4847](https://github.com/Dicklesworthstone/ntm/commit/ac3a4847)).
- **Fresh-eyes audit waves.** Repeated adversarial review passes over recent agent work produced and fixed dozens of findings across serve, robot, swarm, checkpoint, and session restore -- including a restore that would silently drop agents, a circuit breaker that never half-opened, and CAAM account rotation broken against the current CLI ([1e8d4770](https://github.com/Dicklesworthstone/ntm/commit/1e8d4770), [4125df5f](https://github.com/Dicklesworthstone/ntm/commit/4125df5f), [d9e12d20](https://github.com/Dicklesworthstone/ntm/commit/d9e12d20), [59414dcc](https://github.com/Dicklesworthstone/ntm/commit/59414dcc)).
- Recursive directory watches with `**` path patterns in workflows, and the Windows build restored after a Unix-only process-group kill ([3a14b4f6](https://github.com/Dicklesworthstone/ntm/commit/3a14b4f6), [85851434](https://github.com/Dicklesworthstone/ntm/commit/85851434)).

### Verification And Release

- Made the suite trustworthy on a loaded machine: wall-clock budgets scale under live agent-fleet load, completion tests wait on pane PIDs rather than session removal, and load-sensitive fixtures are isolated ([11e74e88](https://github.com/Dicklesworthstone/ntm/commit/11e74e88)).
- Expanded real-tmux send E2E coverage and proved rendered send delivery in live panes ([3929cac0](https://github.com/Dicklesworthstone/ntm/commit/3929cac0), [499be1f5](https://github.com/Dicklesworthstone/ntm/commit/499be1f5)).

---

## [v1.21.0] -- 2026-08-04 [GitHub Release]

**116 commits since v1.20.0** -- agent lifecycle verbs, verified prompt delivery with stranded-composer rescue, liveness-gated sends, durable attention correctness, enforced encryption-at-rest, and a GitHub issue triage sweep (#230-#242).

### Features

- **Agent lifecycle verbs.** Graceful `exit-cli` and `kill-agent` without destroying the pane, per-pane removal via `ntm kill --pane` / `--robot-kill-pane`, bounded `rotate --force` for wedged CLIs, an incident-resolve verb with loud empty-flag errors, and per-pane addressing cards with stable `%pane_id` ([50618431](https://github.com/Dicklesworthstone/ntm/commit/50618431), [340eb348](https://github.com/Dicklesworthstone/ntm/commit/340eb348), [e00f81df](https://github.com/Dicklesworthstone/ntm/commit/e00f81df), [8a2676ae](https://github.com/Dicklesworthstone/ntm/commit/8a2676ae), [2b1c46f6](https://github.com/Dicklesworthstone/ntm/commit/2b1c46f6)).
- **Verified prompt delivery.** Claude and Codex submissions are verified after delivery with picker-stranded prompts rescued (#241), `--clear-input` gives verified pre-send composer hygiene, `--msg-file -` reads payloads from stdin to shield them from command-safety scanners with oversize detection instead of silent truncation, and `--all` excludes the user pane unless `--include-user` ([049b58dc](https://github.com/Dicklesworthstone/ntm/commit/049b58dc), [9d4e426b](https://github.com/Dicklesworthstone/ntm/commit/9d4e426b), [423b43c1](https://github.com/Dicklesworthstone/ntm/commit/423b43c1), [d53550ea](https://github.com/Dicklesworthstone/ntm/commit/d53550ea), [059eb691](https://github.com/Dicklesworthstone/ntm/commit/059eb691)).
- **Liveness and readiness gates.** Agent-process liveness refuses sends to panes whose CLI is a bare shell, a structured `wake_ping` probe checks rate-limit liveness, spawn `--verify-boot` fails loudly when agents never reach a prompt, and robot-wait gains `agent_ready` and `rate_limit_lifted` conditions to retire post-relaunch sleep folklore ([7bebf88f](https://github.com/Dicklesworthstone/ntm/commit/7bebf88f), [51f96e2c](https://github.com/Dicklesworthstone/ntm/commit/51f96e2c), [617fb71b](https://github.com/Dicklesworthstone/ntm/commit/617fb71b), [d3c72d57](https://github.com/Dicklesworthstone/ntm/commit/d3c72d57), [b5eed774](https://github.com/Dicklesworthstone/ntm/commit/b5eed774)).
- **Spawn and restart controls.** `model@effort` variant suffixes for per-spawn reasoning effort, `--restart-model` / `--restart-agent-args` relaunch overrides, `pane_started_at` / agent-uptime evidence in pane records, dialog classification with policy-gated answering, and `robot-tail --fresh` forcing direct live capture ([ffc172a8](https://github.com/Dicklesworthstone/ntm/commit/ffc172a8), [19338fec](https://github.com/Dicklesworthstone/ntm/commit/19338fec), [32c31cce](https://github.com/Dicklesworthstone/ntm/commit/32c31cce), [202a81c9](https://github.com/Dicklesworthstone/ntm/commit/202a81c9), [be69d537](https://github.com/Dicklesworthstone/ntm/commit/be69d537)).

### Reliability

- **Durable attention correctness.** `--robot-snapshot --since` reads the durable attention feed, ephemeral heartbeats no longer consume durable cursors, and routing never recommends excluded or bare-shell panes and actually rotates stateless round-robin by anchor ([d20b9a35](https://github.com/Dicklesworthstone/ntm/commit/d20b9a35), [fb51b398](https://github.com/Dicklesworthstone/ntm/commit/fb51b398), [e8a30bca](https://github.com/Dicklesworthstone/ntm/commit/e8a30bca)).
- **GitHub issue triage sweep (#230-#242).** Workspace-trust dialogs classify as ERROR rather than OK (#230), added agents register with Agent Mail (#240), epic container types are excluded from auto-dispatch (#242), spawn recovery-source timeouts degrade to partial recovery (#232), and the stale parser works with canonical idle (#234) ([b845f3f6](https://github.com/Dicklesworthstone/ntm/commit/b845f3f6), [5a7d1fbe](https://github.com/Dicklesworthstone/ntm/commit/5a7d1fbe), [e5983245](https://github.com/Dicklesworthstone/ntm/commit/e5983245), [1eb864d0](https://github.com/Dicklesworthstone/ntm/commit/1eb864d0), [9f8424fd](https://github.com/Dicklesworthstone/ntm/commit/9f8424fd)).
- **Serve and audit integrity.** The audit middleware is actually installed so mutating actions get recorded, projectDir reads stop racing, workflow paths are confined, authentication is usable with the WebSocket CSRF hole closed, and encryption-at-rest is enforced rather than best-effort with analytics reading event logs through the honoring path ([fecaa0cc](https://github.com/Dicklesworthstone/ntm/commit/fecaa0cc), [487ac3a7](https://github.com/Dicklesworthstone/ntm/commit/487ac3a7), [a115fe49](https://github.com/Dicklesworthstone/ntm/commit/a115fe49), [810bee1b](https://github.com/Dicklesworthstone/ntm/commit/810bee1b)).
- Closed the 70-finding self-audit backlog, all six P0s first: prompts delivered intact, idle shells no longer reported as crashed, cursor and numeric defects that hid live data repaired, readiness and acknowledgments never fabricated, and auto-restart/timeline checkpointing no longer destroy state ([054edd0e](https://github.com/Dicklesworthstone/ntm/commit/054edd0e), [78341980](https://github.com/Dicklesworthstone/ntm/commit/78341980), [bd4fc018](https://github.com/Dicklesworthstone/ntm/commit/bd4fc018), [8a99a5b3](https://github.com/Dicklesworthstone/ntm/commit/8a99a5b3)).

### Verification And Release

- README flag-drift gate: every `ntm` example flag must exist in the CLI registry ([cb4141b6](https://github.com/Dicklesworthstone/ntm/commit/cb4141b6)); the Go toolchain moves to 1.26.5 with all direct dependencies at latest ([e0881df8](https://github.com/Dicklesworthstone/ntm/commit/e0881df8)); the double-Ctrl+C exit timing contract is pinned ([1b279c80](https://github.com/Dicklesworthstone/ntm/commit/1b279c80)); and encryption-at-rest is driven through the real binary end-to-end ([aa5b908b](https://github.com/Dicklesworthstone/ntm/commit/aa5b908b)).

---

## [v1.20.0] -- 2026-07-21 [GitHub Release]

**82 commits since v1.19.1** -- atomic agent assignment, canonical pane identity, unified robot contracts, first-class Grok Build launch support, and substantially broader real-tmux verification.

### Features

- **Atomic assignment and reassignment.** Every assignment surface now uses one claim -> reserve -> dispatch coordinator with cross-process locking, generation-scoped delivery identities, durable ledger replicas, fail-closed rollback, and tracker-proven reopen behavior ([85bda87b](https://github.com/Dicklesworthstone/ntm/commit/85bda87b), [2a5bbf90](https://github.com/Dicklesworthstone/ntm/commit/2a5bbf90), [fb16d569](https://github.com/Dicklesworthstone/ntm/commit/fb16d569)).
- **Canonical pane targeting and unified dispatch.** Stable pane IDs, strict `N` / `window.pane` / `%id` selectors, topology-aware send behavior, and one shared dispatch application service now cover CLI, robot, scheduler, coordinator, and TUI paths ([0ea6c5c5](https://github.com/Dicklesworthstone/ntm/commit/0ea6c5c5), [3344430d](https://github.com/Dicklesworthstone/ntm/commit/3344430d), [6087eca3](https://github.com/Dicklesworthstone/ntm/commit/6087eca3)).
- **Fresh, confidence-scored observation.** Session state estimates carry capture time, confidence, and evidence; actuation revalidates freshness before dispatch, and coordinator auto-assignment only targets freshly observed idle agents ([65a20b55](https://github.com/Dicklesworthstone/ntm/commit/65a20b55), [1ce5e680](https://github.com/Dicklesworthstone/ntm/commit/1ce5e680), [ec20c417](https://github.com/Dicklesworthstone/ntm/commit/ec20c417)).
- **Robot-mode contract hardening.** Commands emit one stable JSON envelope, typed failures determine process exit status, selectors are canonical, sensitive interrupt follow-ups are redacted, and discovery/capability output is more concise and truthful ([222b57da](https://github.com/Dicklesworthstone/ntm/commit/222b57da), [0bee4911](https://github.com/Dicklesworthstone/ntm/commit/0bee4911), [1a39a4da](https://github.com/Dicklesworthstone/ntm/commit/1a39a4da)).
- **Grok Build phase one.** Added first-class `grok` configuration, CLI add plus CLI/robot/REST spawn surfaces, configured default-model and exact `--effort` forwarding, stable-process and idle-pane launch admission, process/model discovery, counts, schemas, plain Ctrl+C interrupt support, optional doctor/dependency reporting, and topology-only saved-session restore. Authenticated-TUI prompt delivery, assignment, interrupt retasking, restart, and restore-time relaunch fail closed before pane mutation ([4bcb6fef](https://github.com/Dicklesworthstone/ntm/commit/4bcb6fef)).
- **Configurable operator approval gates.** Global and project `[assign] operator_gated_labels` extend the built-in, case-insensitive gate vocabulary without allowing a repository to remove operator protections (#223) ([6761798a](https://github.com/Dicklesworthstone/ntm/commit/6761798a)).
- **Persistent coordinator toggles.** `coordinator enable|disable` now updates the selected config atomically, preserves unrelated content and symlink targets, serializes concurrent writers, validates digest intervals and the strict NTM schema, and reports the exact persisted values in JSON (#223) ([6761798a](https://github.com/Dicklesworthstone/ntm/commit/6761798a)).

### Reliability

- Reconcile assignment ledgers and reservations without stealing live work; propagate the resolved project directory to every `br` subprocess; bind saved sessions to authoritative agent sessions ([fc6cba92](https://github.com/Dicklesworthstone/ntm/commit/fc6cba92), [6dc42ece](https://github.com/Dicklesworthstone/ntm/commit/6dc42ece), [268489d7](https://github.com/Dicklesworthstone/ntm/commit/268489d7)).
- Bound assignment observation by the dispatch freshness window, preserve explicit shorter deadlines, and guarantee timeout failures leave Beads, ledgers, Agent Mail, and panes unchanged ([045c4a37](https://github.com/Dicklesworthstone/ntm/commit/045c4a37), [f05c0ad8](https://github.com/Dicklesworthstone/ntm/commit/f05c0ad8)).
- Honor disabled scheduler configuration, tighten pipeline cancellation, isolate serve-side host integrations, and make support-bundle probes hermetic ([79017882](https://github.com/Dicklesworthstone/ntm/commit/79017882), [09c618f0](https://github.com/Dicklesworthstone/ntm/commit/09c618f0), [5840f61a](https://github.com/Dicklesworthstone/ntm/commit/5840f61a)).
- Make the dependency-aware `bv --robot-plan` a mandatory automated-assignment boundary; reject command, parse, structure, blank-ID, and live-label coverage failures, replace stale triage labels with authoritative `br` state, and retain plan-only epic operator gates (#224).
- Strictly load assignment policy from the project that owns the Beads workspace before retry, reassign, rebalance, coordinator, robot assign, robot bulk, robot spawn, watch, or distribute work. Cross-project robot and distribute commands now honor custom gates, and live distribute dispatch revalidates every eligibility field immediately before delivery.
- Removed the unused `[health]` TOML section and made `[resilience]` the single restart/monitoring configuration. Existing `[health]` files now fail strict unknown-field validation and must be migrated; the `ntm health` command is unchanged.
- Refuse repo-scoped hook writes through redirected `core.hooksPath`, and reject unusable worktree repositories before any pane or session mutation ([26cf34a4](https://github.com/Dicklesworthstone/ntm/commit/26cf34a4), [7d7f70d8](https://github.com/Dicklesworthstone/ntm/commit/7d7f70d8)).
- Preserve DCG pre-send verdicts for leading-dash and bulleted command candidates (#228), rebuild and retry recognized Beads database corruption once under the guarded mutation boundary (#227), and classify pane activity from each pane's own content changes so a busy neighbor cannot pin `wait --completion` (#213) ([060e6cb3](https://github.com/Dicklesworthstone/ntm/commit/060e6cb3), [34e41a11](https://github.com/Dicklesworthstone/ntm/commit/34e41a11), [b8787274](https://github.com/Dicklesworthstone/ntm/commit/b8787274)).
- Bound Agent Mail availability discovery to one sub-two-second deadline across lock contention, retries, and backoff; retry only transient transport, timeout, busy, 408, 425, 429, and 5xx failures; fail permanent errors after one probe; preserve cancellation state; and surface the terminal cause to CLI callers.
- Run assignment-mutation policy preflight without unrelated projection refreshes, preserving zero-side-effect failures when selected or target-project configuration is invalid ([de487f7f](https://github.com/Dicklesworthstone/ntm/commit/de487f7f)).
- Preserve missing `bv` and `br` identities through assignment planning so robot recommendation and bulk surfaces return `DEPENDENCY_MISSING`; validate robot assignment policy before optional projection refresh; and classify unauthorized rebalance or reassign generations as `BEAD_INELIGIBLE` without mutation.

### Verification And Release

- Added real-tmux dashboard, assignment, reassignment, timeout, recovery, coordinator, robot bulk/spawn, watch, and distribute coverage, including process-boundary assertions for durable side effects, authoritative cross-project policy, malformed planning data, and zero-side-effect failures ([71f66f64](https://github.com/Dicklesworthstone/ntm/commit/71f66f64), [43048fa3](https://github.com/Dicklesworthstone/ntm/commit/43048fa3), [90010945](https://github.com/Dicklesworthstone/ntm/commit/90010945)).
- Hardened DSR quality gates across macOS paths, race scheduling, terminal capability detection, private tmux-server isolation, primary E2E, and legacy bulk E2E ([bc4f7a68](https://github.com/Dicklesworthstone/ntm/commit/bc4f7a68), [08acf37b](https://github.com/Dicklesworthstone/ntm/commit/08acf37b), [b43dff17](https://github.com/Dicklesworthstone/ntm/commit/b43dff17), [5a5bdc85](https://github.com/Dicklesworthstone/ntm/commit/5a5bdc85)).
- Kept real-tmux verification portable under long or capacity-constrained temp roots, and replaced scheduler-sensitive cancellation and worktree checks with bounded, deterministic synchronization.
- Strict upgrade verification now accepts DSR's native macOS architecture artifacts while retaining exact legacy `darwin_all` support.
- Added built-binary Agent Mail E2E coverage for permanent one-shot failure, bounded transient exhaustion, and successful transient recovery.
- Expanded built-binary assignment E2E coverage for terminal Beads, fail-closed label enrichment, missing `bv` and `br`, completion lease takeover, generation conflicts, reservation refusal, spawn cancellation, and zero-side-effect policy rejection; completion watchers now use immutable per-process fixtures, and direct replay assertions derive pane identity from the isolated tmux topology.
- Added the canonical `VERSION` marker used by DSR version detection ([c727c38d](https://github.com/Dicklesworthstone/ntm/commit/c727c38d)).

---

## [v1.19.1] -- 2026-07-06 [GitHub Release]

**1 commit since v1.19.0** -- make orphan-process reaping portable to Windows ([04877bc6](https://github.com/Dicklesworthstone/ntm/commit/04877bc6)).

---

## [v1.19.0] -- 2026-07-06 [Tag Only]

**35 commits since v1.18.3** -- first-class Antigravity agents, safer autonomous swarms, and dashboard/robot improvements.

### Features

- Added Antigravity (`agy`) as a first-class provider across spawning, session discovery, resume, models, docs, and aliases ([abbd51a9](https://github.com/Dicklesworthstone/ntm/commit/abbd51a9), [ea023fd7](https://github.com/Dicklesworthstone/ntm/commit/ea023fd7), [e488e980](https://github.com/Dicklesworthstone/ntm/commit/e488e980)).
- Added per-pane `CODEX_HOME` isolation and safe account rotation, semantic progress tokens, plugin model resolution, and faithful window/session restoration ([12fd6c41](https://github.com/Dicklesworthstone/ntm/commit/12fd6c41), [ecdcfcd5](https://github.com/Dicklesworthstone/ntm/commit/ecdcfcd5), [4f75a99b](https://github.com/Dicklesworthstone/ntm/commit/4f75a99b), [a3b9d840](https://github.com/Dicklesworthstone/ntm/commit/a3b9d840)).

### Bug Fixes

- Hardened autonomous dispatch, agent idle detection, robot restart/send behavior, dashboard overlays, palette messaging, health probes, and typed robot failures ([bef7ffcb](https://github.com/Dicklesworthstone/ntm/commit/bef7ffcb), [e250fd5a](https://github.com/Dicklesworthstone/ntm/commit/e250fd5a), [d291ffe3](https://github.com/Dicklesworthstone/ntm/commit/d291ffe3), [d84ee534](https://github.com/Dicklesworthstone/ntm/commit/d84ee534)).

---

## [v1.18.3] -- 2026-06-11 [GitHub Release]

**38 commits since v1.18.2** -- saved-session lifecycle, Codex goal controls, topology-aware robot operations, and macOS/test hardening.

### Features

- Added save/list/resume/archive with agent-session state, Codex goal lifecycle and palette-state commands, and structured Agent Mail lock failure codes ([507e6138](https://github.com/Dicklesworthstone/ntm/commit/507e6138), [f8259311](https://github.com/Dicklesworthstone/ntm/commit/f8259311), [c8a5018e](https://github.com/Dicklesworthstone/ntm/commit/c8a5018e), [069fe4be](https://github.com/Dicklesworthstone/ntm/commit/069fe4be)).

### Bug Fixes

- Made pane addressing window-aware, failed loudly on untargetable robot actions, captured only visible Codex preflight state, and resumed the correct Claude session ([0e5fbcd5](https://github.com/Dicklesworthstone/ntm/commit/0e5fbcd5), [d5ca3b95](https://github.com/Dicklesworthstone/ntm/commit/d5ca3b95), [4ec77ee4](https://github.com/Dicklesworthstone/ntm/commit/4ec77ee4), [e68d9d72](https://github.com/Dicklesworthstone/ntm/commit/e68d9d72)).
- Corrected invalid project overlays, actionable assignment filtering, terminal resize repainting, symlink checks, and macOS path/test behavior ([122d517d](https://github.com/Dicklesworthstone/ntm/commit/122d517d), [74f9b601](https://github.com/Dicklesworthstone/ntm/commit/74f9b601), [6615dd7d](https://github.com/Dicklesworthstone/ntm/commit/6615dd7d), [89811b9a](https://github.com/Dicklesworthstone/ntm/commit/89811b9a)).

---

## [v1.18.2] -- 2026-05-21 [GitHub Release]

**2 commits since v1.18.1** -- widen the idle-prompt scan window while preserving spinner ordering, and refresh release history ([e28763ea](https://github.com/Dicklesworthstone/ntm/commit/e28763ea), [1635316b](https://github.com/Dicklesworthstone/ntm/commit/1635316b)).

---

## [v1.18.1] -- 2026-05-20 [GitHub Release]

**2 commits since v1.18.0** — Codex preflight no longer over-rejects ChatGPT/OAuth users.

### Bug Fixes

- **Stop hard-rejecting `gpt-*-codex` on ChatGPT/OAuth logins.** The preflight assumed every `gpt-*-codex` id fails with HTTP 400 on ChatGPT-billed accounts, but that is not universal — recent Codex CLI + ChatGPT plans run `gpt-5.3-codex` and answer prompts fine. The local Codex CLI is now the source of truth: an explicit `gpt-*-codex` model on a ChatGPT login emits a non-blocking advisory and proceeds (capability preserved), with `NTM_CODEX_PREFLIGHT_STRICT=1` to opt back into a hard block (#155) ([0fe72100](https://github.com/Dicklesworthstone/ntm/commit/0fe72100))
- Reattach an orphaned `waitForAgentsReady` doc comment ([76306f90](https://github.com/Dicklesworthstone/ntm/commit/76306f90))

---

## [v1.18.0] -- 2026-05-20 [GitHub Release]

**10 commits since v1.17.0** — `--profile-set` becomes a first-class persona spawn contract, plus cross-surface fixes.

### Features

- **`--profile-set` / `--profiles` now drive the spawn.** A persona set expands into concrete, ordered agents *before* pane creation — each agent takes its persona's own `agent_type`, model, and prompt, so a persona can never silently run the wrong agent CLI. Adds fail-closed validation on count/type conflicts, deterministic pane assignment, persona-named pane titles, and a machine-readable persona→pane mapping in the spawn JSON (#149) ([779c3a47](https://github.com/Dicklesworthstone/ntm/commit/779c3a47), [dcddd71b](https://github.com/Dicklesworthstone/ntm/commit/dcddd71b), [990e08e4](https://github.com/Dicklesworthstone/ntm/commit/990e08e4))
- **Alias-aware pane filtering** for robot history ([978f614a](https://github.com/Dicklesworthstone/ntm/commit/978f614a))

### Bug Fixes

- Surface `assign.prompt_template` / `prompt_template_file` in `config diff` for parity with `config show`/`get` (#153) ([358ceb5d](https://github.com/Dicklesworthstone/ntm/commit/358ceb5d))
- Clamp exported timeline events to the requested time range with carry-in state ([82d270b6](https://github.com/Dicklesworthstone/ntm/commit/82d270b6))
- Propagate IO errors and fix a temp-file leak in the context-pending rotation store ([f50e8145](https://github.com/Dicklesworthstone/ntm/commit/f50e8145))
- Tighten the Codex/Gemini "limited" quota regex against neutral quota prose ([17eb10a2](https://github.com/Dicklesworthstone/ntm/commit/17eb10a2))
- Regression test locking in that expanded personas survive `normalizeSpawnOptions` ([fb02449d](https://github.com/Dicklesworthstone/ntm/commit/fb02449d))

---

## [v1.17.0] -- 2026-05-20 [GitHub Release]

**1 commit since v1.16.3** — project-level default for the assign dispatch template.

### Features

- **Config-driven default bulk-assign dispatch template.** New `[assign] prompt_template` / `prompt_template_file` keys let a project pin its dispatch contract (e.g. "Read SKILL.md", "Set gc.outcome when done") instead of wrapping every `--robot-bulk-assign` call. Resolution precedence: per-invocation `--bulk-assign-template` > configured file > configured inline > built-in const (#153) ([1db27e35](https://github.com/Dicklesworthstone/ntm/commit/1db27e35))

---

## [v1.16.3] -- 2026-05-20 [GitHub Release]

**1 commit since v1.16.2** — worktree CLI correctness.

### Bug Fixes

- **Worktree fixes:** manually-provisioned worktrees are now cleanable by `clean-session` (consistent branch naming), `clean-session` reports the actual count removed instead of false success, and `worktree list --json` emits JSON (#150, #151, #152) ([87a948b7](https://github.com/Dicklesworthstone/ntm/commit/87a948b7))

---

## [v1.16.2] -- 2026-05-20 [GitHub Release]

**1 commit since v1.16.1** — Codex model default.

### Bug Fixes

- Default Codex agents to `gpt-5.5` instead of the obsolete `gpt-5.3-codex` (#148) ([00e789ea](https://github.com/Dicklesworthstone/ntm/commit/00e789ea))

---

## [v1.16.1] -- 2026-05-20 [GitHub Release]

**4 commits since v1.16.0** — Agent Mail and Codex spawn fixes.

### Bug Fixes

- Agent Mail `registration_token` plumbing + overseer via `send_message` (#146) ([7779cbb3](https://github.com/Dicklesworthstone/ntm/commit/7779cbb3))
- Respect the resolved Codex model in the ChatGPT-account preflight (#147) ([b61f6584](https://github.com/Dicklesworthstone/ntm/commit/b61f6584))
- Tighten worktree-root + pane-lookup resolution, parse the rch daemon status envelope, allow "." in the lock-path matcher ([d71736b7](https://github.com/Dicklesworthstone/ntm/commit/d71736b7))
- Fresh-eyes release follow-ups ([308b895a](https://github.com/Dicklesworthstone/ntm/commit/308b895a))

---

## [v1.16.0] -- 2026-05-16 [GitHub Release]

**11 commits since v1.15.1** — wrapper-parity fixes and context-cancellation hardening.

### Features

- **Wrapper-parity bundles:** assign/unlock/redact/state fixes ([9a46a801](https://github.com/Dicklesworthstone/ntm/commit/9a46a801)) and spawn/worktrees/switch fixes ([00e5e1a8](https://github.com/Dicklesworthstone/ntm/commit/00e5e1a8))
- Modernize the Claude Code hooks integration and tighten handoff validation ([06090fd0](https://github.com/Dicklesworthstone/ntm/commit/06090fd0))

### Bug Fixes

- Make `cmd.Context()` actually cancellable and plumb `context.Context` through the diagnose/fix paths ([880d5799](https://github.com/Dicklesworthstone/ntm/commit/880d5799), [c9aa587c](https://github.com/Dicklesworthstone/ntm/commit/c9aa587c), [a3f79894](https://github.com/Dicklesworthstone/ntm/commit/a3f79894))
- Resolve tmux panes by ID rather than `session:idx` so pane targeting is `pane-base-index` safe ([b540a212](https://github.com/Dicklesworthstone/ntm/commit/b540a212))
- DCG integration: always advertise robot mode, use dcg's actual subcommand names ([44cae8f7](https://github.com/Dicklesworthstone/ntm/commit/44cae8f7), [489d235b](https://github.com/Dicklesworthstone/ntm/commit/489d235b))

---

## [v1.15.1] -- 2026-05-14 [GitHub Release]

**1 commit since v1.15.0** — docs.

- Correct source-install guidance in the release docs ([401df929](https://github.com/Dicklesworthstone/ntm/commit/401df929))

---

## [v1.15.0] -- 2026-05-14 [GitHub Release]

**951 commits since v1.14.0** — Go 1.26 toolchain, cross-surface state/audit/tool contracts, and symlink-safety hardening.

### Toolchain & Dependencies

- Bump the Go toolchain to 1.26 and refresh the charmbracelet/chromedp vendored stack ([5b4e99f3](https://github.com/Dicklesworthstone/ntm/commit/5b4e99f3), [f635b12a](https://github.com/Dicklesworthstone/ntm/commit/f635b12a), [7d21473a](https://github.com/Dicklesworthstone/ntm/commit/7d21473a))

### Concurrency & Reliability

- Cross-surface cleanups landing the new state/audit/tool contracts across cli/robot/tui/swarm/ollama/bv/cass/archive/util/webhook/serve ([00457000](https://github.com/Dicklesworthstone/ntm/commit/00457000))
- Avoid deadlocks under load and surface rate-limit "cleared" transitions across resilience/status/summary/metrics/events ([36213942](https://github.com/Dicklesworthstone/ntm/commit/36213942))
- Tighten migration TX rollback, expand SQLite pragmas, and stabilise timeline persistence ([8f6693f4](https://github.com/Dicklesworthstone/ntm/commit/8f6693f4))
- Retry SQLite-locked errors per call and double-check writers under contention ([281bbab0](https://github.com/Dicklesworthstone/ntm/commit/281bbab0))
- Harden resilience PID/stream cancellation; respect scheduler `GlobalMax` in waiter wake-ups ([1565c8f7](https://github.com/Dicklesworthstone/ntm/commit/1565c8f7), [0aa56e4e](https://github.com/Dicklesworthstone/ntm/commit/0aa56e4e))
- Recursively register newly-created watcher subdirectories and drop descendant entries on removal/rename ([bfa272c1](https://github.com/Dicklesworthstone/ntm/commit/bfa272c1))

### Security Hardening

- Reject symlinked saved profiles, persona prompt files outside the project, and incremental-diff symlink writes ([9364afc1](https://github.com/Dicklesworthstone/ntm/commit/9364afc1), [bdb0966f](https://github.com/Dicklesworthstone/ntm/commit/bdb0966f), [46be3f95](https://github.com/Dicklesworthstone/ntm/commit/46be3f95))
- Skip symlink files in directory bundles; resolve git hook paths safely ([b424537d](https://github.com/Dicklesworthstone/ntm/commit/b424537d), [346204e8](https://github.com/Dicklesworthstone/ntm/commit/346204e8))

### Locks & Coordination

- Add the `ntm locks check` API for wrapper-contract callers; own-holder priority + empty-pattern guard ([98dff276](https://github.com/Dicklesworthstone/ntm/commit/98dff276), [f21b54e0](https://github.com/Dicklesworthstone/ntm/commit/f21b54e0))
- Ignore malformed reservation expiries and shared reservations in `check` ([0da61910](https://github.com/Dicklesworthstone/ntm/commit/0da61910), [475ed187](https://github.com/Dicklesworthstone/ntm/commit/475ed187))
- Harden Agent Mail pane-identity files ([d031eb0e](https://github.com/Dicklesworthstone/ntm/commit/d031eb0e))

### Pipeline & Assignment

- Split pipeline substitution into seal-retaining vs seal-restoring variants so foreach materialisation never double-substitutes; preserve persisted StepResults for resume-suppressed iterations ([804268f3](https://github.com/Dicklesworthstone/ntm/commit/804268f3), [6a607dfe](https://github.com/Dicklesworthstone/ntm/commit/6a607dfe))
- Compare agent types canonically so allowed-type checks survive provider aliases; deterministic agent-to-bead selection across strategies ([66ab6887](https://github.com/Dicklesworthstone/ntm/commit/66ab6887), [c9aaba3e](https://github.com/Dicklesworthstone/ntm/commit/c9aaba3e))

---

## [v1.14.0] -- 2026-04-24 [GitHub Release]

**26 commits since v1.13.1** — Claude Code model snapshot/restore and operator-prompt fixes.

### Features

- **Snapshot/restore the Claude Code model setting** across the swarm lifecycle, so per-swarm model overrides don't leak into the user's global Claude Code config (#110) ([29f6efbe](https://github.com/Dicklesworthstone/ntm/commit/29f6efbe), [d27cf24f](https://github.com/Dicklesworthstone/ntm/commit/d27cf24f))

### Bug Fixes

- Teach the controller prompt to use `--robot-*` state commands and `ntm mail inbox SESSION` instead of the broken `ntm view` / `--mail-project=SESSION` forms (#109) ([c6e15d97](https://github.com/Dicklesworthstone/ntm/commit/c6e15d97), [bc734496](https://github.com/Dicklesworthstone/ntm/commit/bc734496))
- Suppress the fresh-project recovery-inbox warning and tighten the agent-not-found heuristic to avoid APIError false-positives (#108) ([278aa1a1](https://github.com/Dicklesworthstone/ntm/commit/278aa1a1), [ab36867d](https://github.com/Dicklesworthstone/ntm/commit/ab36867d), [04062366](https://github.com/Dicklesworthstone/ntm/commit/04062366))
- Don't mis-delete a `settings.json` containing JSON null; prevent a `WriteModel` panic on null settings ([7ba6c11a](https://github.com/Dicklesworthstone/ntm/commit/7ba6c11a), [6256bfcb](https://github.com/Dicklesworthstone/ntm/commit/6256bfcb))
- Only honor the active-spinner override when the spinner appears after the most recent idle prompt ([0a916561](https://github.com/Dicklesworthstone/ntm/commit/0a916561))
- Register `newTimelineCmd()` so `ntm timeline` is available ([a8649f87](https://github.com/Dicklesworthstone/ntm/commit/a8649f87))
- Repair CI failures across config merge, alerts, pane-identity, bead handlers, and the perf bench ([eb99f75f](https://github.com/Dicklesworthstone/ntm/commit/eb99f75f))

---

## [v1.13.1] -- 2026-04-16 [GitHub Release]

**1 commit since v1.13.0** — Agent Mail pane-identity contract.

- Converge on the canonical Agent Mail pane-identity contract, fixing drift (#107) ([5ca8a452](https://github.com/Dicklesworthstone/ntm/commit/5ca8a452))

---

## [v1.13.0] -- 2026-04-12 [GitHub Release]

**58 commits since v1.12.1** — Lifecycle hardening, concurrency safety, and security improvements.

### Lifecycle & Concurrency

- **Graceful goroutine shutdown** across all subsystems with checkpoint restore via respawn-pane ([111f10e6](https://github.com/Dicklesworthstone/ntm/commit/111f10e6))
- **Lifecycle mutex** to serialize Start/Stop across all background subsystems: coordinator, resilience monitor, autoscanner, timeline, persister, supervisor ([86a9f4b0](https://github.com/Dicklesworthstone/ntm/commit/86a9f4b0), [ff259a9a](https://github.com/Dicklesworthstone/ntm/commit/ff259a9a), [46e42a4e](https://github.com/Dicklesworthstone/ntm/commit/46e42a4e))
- **ScannerStore deep-cloning**, executor snapshot race fix, and `t.Parallel()` removal across 90+ test files ([dc891e6d](https://github.com/Dicklesworthstone/ntm/commit/dc891e6d), [556c0ae5](https://github.com/Dicklesworthstone/ntm/commit/556c0ae5))
- Release read lock before invoking replay handler to prevent callback deadlocks ([bd0f59cb](https://github.com/Dicklesworthstone/ntm/commit/bd0f59cb))
- Event log rotation without blocking concurrent writers or losing events ([89f7fe53](https://github.com/Dicklesworthstone/ntm/commit/89f7fe53))
- RWMutex and double-checked locking for triage cache ([f57638fd](https://github.com/Dicklesworthstone/ntm/commit/f57638fd))
- Clear handler entry on unsubscribe to prevent closure memory leak ([15379029](https://github.com/Dicklesworthstone/ntm/commit/15379029))

### Features

- **Tmux circuit breaker** with session reconciliation and server startup hardening ([b3854367](https://github.com/Dicklesworthstone/ntm/commit/b3854367))
- **Dashboard alerts section** with CLI/serve test coverage expansion ([9199bbd0](https://github.com/Dicklesworthstone/ntm/commit/9199bbd0))
- **Poll-based Codex throttle gate** for Acquire() ([96df8386](https://github.com/Dicklesworthstone/ntm/commit/96df8386))
- **Typed cap waiters** with reset abort, audit DB connection hardening ([83ba973f](https://github.com/Dicklesworthstone/ntm/commit/83ba973f))
- Agent effectiveness ranking against all supported agent types ([b62cfbc1](https://github.com/Dicklesworthstone/ntm/commit/b62cfbc1))
- Alert Source field exposed in all output paths, redaction config upgraded to reader locks ([fbcd2e7e](https://github.com/Dicklesworthstone/ntm/commit/fbcd2e7e))
- Remove stubs and unimplemented features, add truthfulness tests and rate-limit detection ([420584cc](https://github.com/Dicklesworthstone/ntm/commit/420584cc))

### Security Hardening

- Bound HTTP JSON response bodies at 10 MiB with `io.LimitReader` ([1192ccce](https://github.com/Dicklesworthstone/ntm/commit/1192ccce), [4e8f9350](https://github.com/Dicklesworthstone/ntm/commit/4e8f9350))
- Cap Agent Mail overseer response body at 10 MB to prevent DoS/OOM ([5ba4dd1d](https://github.com/Dicklesworthstone/ntm/commit/5ba4dd1d))
- Stream checkpoint export bodies instead of buffering in memory ([e04472ff](https://github.com/Dicklesworthstone/ntm/commit/e04472ff))
- Propagate auth claims through middleware and pick highest realm_access role ([6090f111](https://github.com/Dicklesworthstone/ntm/commit/6090f111))
- Enforce directory boundary in wildcard glob matching ([0c09978f](https://github.com/Dicklesworthstone/ntm/commit/0c09978f))
- Session name validation, race-safe config endpoints, policy check error logging ([b5e7315a](https://github.com/Dicklesworthstone/ntm/commit/b5e7315a))

### Bug Fixes

- Thinking signals now outrank co-present idle-prompt patterns, preventing false WAITING classification ([408fa8e4](https://github.com/Dicklesworthstone/ntm/commit/408fa8e4))
- Distinguish infrastructure errors from application errors in circuit breaker ([d7700b8f](https://github.com/Dicklesworthstone/ntm/commit/d7700b8f))
- Use `key.Matches` for quit binding in edit phase instead of raw KeyCtrlC ([f3ac4384](https://github.com/Dicklesworthstone/ntm/commit/f3ac4384))
- Surface failed-iteration errors from loops; persist parallel state outside global mutex ([20cb58af](https://github.com/Dicklesworthstone/ntm/commit/20cb58af))
- Deterministic ExportCSV rows in metrics collector ([e00f4ee5](https://github.com/Dicklesworthstone/ntm/commit/e00f4ee5))
- Skip redundant status fetch when commands overlap, throttle score pruning to once per 24h ([28042ae5](https://github.com/Dicklesworthstone/ntm/commit/28042ae5))
- Single ticker for worker periodic polling in scheduler ([17a87fb0](https://github.com/Dicklesworthstone/ntm/commit/17a87fb0))

### Performance

- Hoist runtime regex compilation to package-level vars ([cdebcc27](https://github.com/Dicklesworthstone/ntm/commit/cdebcc27))
- Simplify CancelSession/CancelBatch with single-pass retain pattern ([ad7b1809](https://github.com/Dicklesworthstone/ntm/commit/ad7b1809))

---

## [v1.12.1] -- 2026-04-04 [GitHub Release]

- fix(docker): restore release container builds

## [v1.12.0] -- 2026-04-04 [GitHub Release]

- Stabilize release gates

## [v1.11.0] -- 2026-04-01 [GitHub Release]

- Unblock release checks

## [v1.10.0] -- 2026-03-25 [GitHub Release]

- Incremental stability and CI improvements

## [v1.9.0] -- 2026-03-24 [GitHub Release]

- Major stability, TUI, and infrastructure release (see GitHub release notes for full details)

## [Unreleased before v1.9.0] (after v1.8.0)

**Development cycle between v1.8.0 and v1.9.0**

### TUI Overhaul ("Glamour Upgrade")

The post-v1.8.0 cycle is dominated by a comprehensive TUI rewrite:

- **Vendored Bubbletea fork** with theme system overhaul, reservation watcher refactor, and adaptive model ([ea7d978c](https://github.com/Dicklesworthstone/ntm/commit/ea7d978c))
- **Spring animations engine** (`SpringManager`) for progress bars, focus transitions, and dimension changes ([1b1b4ff0](https://github.com/Dicklesworthstone/ntm/commit/1b1b4ff0), [5377f3d9](https://github.com/Dicklesworthstone/ntm/commit/5377f3d9), [165fabd2](https://github.com/Dicklesworthstone/ntm/commit/165fabd2), [2ba0b7b0](https://github.com/Dicklesworthstone/ntm/commit/2ba0b7b0))
- **Spawn wizard** with gradient tab bar, panel gap spacing, statusbar, overlay, and icon alignment ([142b50b0](https://github.com/Dicklesworthstone/ntm/commit/142b50b0))
- **Scrollable panels** with toast system and sparkline improvements ([af1538d9](https://github.com/Dicklesworthstone/ntm/commit/af1538d9))
- **Charmbracelet/huh forms** integration for interactive TUI dialogs ([a1d5e74c](https://github.com/Dicklesworthstone/ntm/commit/a1d5e74c))
- **bubbles/list** integration for pane list rendering ([8335febc](https://github.com/Dicklesworthstone/ntm/commit/8335febc))
- Toast notifications enhanced with progress bars and history ([0fb8d230](https://github.com/Dicklesworthstone/ntm/commit/0fb8d230))
- Dashboard decomposed into logical sub-files ([82cf982e](https://github.com/Dicklesworthstone/ntm/commit/82cf982e))
- Help overlay rewritten with bubbles/help FullHelp and Catppuccin theming ([57240f6d](https://github.com/Dicklesworthstone/ntm/commit/57240f6d))

### Attention Feed System

A new real-time event monitoring subsystem for agent orchestrators:

- **Attention feed runtime** with cursor-based event replay ([93f01ec9](https://github.com/Dicklesworthstone/ntm/commit/93f01ec9))
- `--robot-attention` CLI command with profile-aware event filtering ([136e9ceb](https://github.com/Dicklesworthstone/ntm/commit/136e9ceb))
- `--robot-events` command with attention feed API expansion ([99cae5c9](https://github.com/Dicklesworthstone/ntm/commit/99cae5c9))
- Attention profile resolution engine with discoverable presets ([26735d62](https://github.com/Dicklesworthstone/ntm/commit/26735d62))
- Attention-aware wait conditions and snapshot attention summary ([3cd98fa8](https://github.com/Dicklesworthstone/ntm/commit/3cd98fa8))
- Digest engine with conflict event types ([87f5baff](https://github.com/Dicklesworthstone/ntm/commit/87f5baff))
- `--robot-digest` flag and attention stream preparation ([05db2385](https://github.com/Dicklesworthstone/ntm/commit/05db2385))
- SSE event stream endpoint and attention webhook integration ([c1a446c7](https://github.com/Dicklesworthstone/ntm/commit/c1a446c7))
- 6-panel mega layout with dedicated attention column ([5bcb734b](https://github.com/Dicklesworthstone/ntm/commit/5bcb734b))
- Dashboard attention panel with cursor retention, mail signals, stats badges, zoom propagation ([d8a0f106](https://github.com/Dicklesworthstone/ntm/commit/d8a0f106))

### Expanded Agent Support

- **Cursor, Windsurf, Aider, and Ollama** agent types recognized across all subsystems (CLI, robot, TUI, E2E) ([b16ee210](https://github.com/Dicklesworthstone/ntm/commit/b16ee210), [f8eb1c86](https://github.com/Dicklesworthstone/ntm/commit/f8eb1c86), [92f9563a](https://github.com/Dicklesworthstone/ntm/commit/92f9563a), [ca456659](https://github.com/Dicklesworthstone/ntm/commit/ca456659))
- `--robot-overlay` command for agent-initiated human handoff ([202d4427](https://github.com/Dicklesworthstone/ntm/commit/202d4427))
- `--attention-cursor` flag for dashboard and overlay commands ([48f9894e](https://github.com/Dicklesworthstone/ntm/commit/48f9894e))

### Server and API

- Full WebSocket hub with REST API handlers, replacing dummy stubs ([71e4d382](https://github.com/Dicklesworthstone/ntm/commit/71e4d382))
- Operator loop guardrails and REST/CLI parity for capabilities ([576fd45f](https://github.com/Dicklesworthstone/ntm/commit/576fd45f))

### Stability and Fixes

- Panic recovery in goroutines, closed-channel drain prevention ([1448e87e](https://github.com/Dicklesworthstone/ntm/commit/1448e87e))
- EventBus buffered delivery, health score cleanup ([f0aaad54](https://github.com/Dicklesworthstone/ntm/commit/f0aaad54))
- UTF-8 truncation, escape parsing, OOM protection hardening ([fc33335e](https://github.com/Dicklesworthstone/ntm/commit/fc33335e))
- Tmux pane ID format corrected from `session:N` to `session:.N` ([8dea427a](https://github.com/Dicklesworthstone/ntm/commit/8dea427a))
- Goroutine leak fixes in swarm and webhook subsystems ([97728349](https://github.com/Dicklesworthstone/ntm/commit/97728349), [78a68229](https://github.com/Dicklesworthstone/ntm/commit/78a68229))
- Route printable keystrokes to filter input when palette is focused ([5ff9e918](https://github.com/Dicklesworthstone/ntm/commit/5ff9e918))
- ~1k lines of dead code removed from serve package ([2a8a2d10](https://github.com/Dicklesworthstone/ntm/commit/2a8a2d10))
- Dashboard prefetch panes before TUI init, seed initial state ([12aaea2b](https://github.com/Dicklesworthstone/ntm/commit/12aaea2b))

---

## [v1.8.0] -- 2026-03-07 [GitHub Release]

### Session Labeling and Multi-Session Workflows

- **`--label` flag** for spawn, create, and quick commands enables goal-labeled multi-session support per project ([53e2110](https://github.com/Dicklesworthstone/ntm/commit/53e2110254986c4ccc81d798dadbf878d34e45e5), [a007a07](https://github.com/Dicklesworthstone/ntm/commit/a007a07b662407b15a3ca764641f17053f7484db), [893ad4e](https://github.com/Dicklesworthstone/ntm/commit/893ad4e496e36dc3ae8a470d1cbe917b3c77c5d5), [12fa1ff](https://github.com/Dicklesworthstone/ntm/commit/12fa1ff7b905e7d78989b88d715b996d240840d7))
- `--project` flag added to send, kill, and list commands for project-based filtering ([1ca6a8a](https://github.com/Dicklesworthstone/ntm/commit/1ca6a8a8a3687d280e91c41509111438efbab49b), [103d635](https://github.com/Dicklesworthstone/ntm/commit/103d6350227e534ad82ad7d852199d62362be64f))
- `ntm scale` command for manual agent fleet scaling ([2382fab](https://github.com/Dicklesworthstone/ntm/commit/2382fab77e7bf0b91233c242ce809860b67cd017))

### Encryption and Audit

- **Encryption at rest** for prompt history and event logs ([ff0b96b](https://github.com/Dicklesworthstone/ntm/commit/ff0b96b7aaf479b3f0a5605420a816ee1d93e5f2), [86fd745](https://github.com/Dicklesworthstone/ntm/commit/86fd745a147b1335562ccfe0202c57630542455b))
- `ntm audit` subcommands for log query and verification ([5f068f0](https://github.com/Dicklesworthstone/ntm/commit/5f068f011fa210171d6c0227f2f3934f5437cdb7))
- Comprehensive audit logging for config commands ([1d78217](https://github.com/Dicklesworthstone/ntm/commit/1d78217748da2dbc3d71e5abad7b806b43db431c))

### Ollama Local Model Management

- Ollama model management CLI with pull progress streaming and model deletion ([722e6df](https://github.com/Dicklesworthstone/ntm/commit/722e6df81375b4a6ee02d7eec023fef71742f268))
- Ollama local fallback with provider selection in spawn ([0ef6be6](https://github.com/Dicklesworthstone/ntm/commit/0ef6be6da5df2e2d6c75069b8e64f7b45386ca94))
- `--assign-cc-only`/`--cod-only`/`--gmi-only` agent type filter aliases ([487568c](https://github.com/Dicklesworthstone/ntm/commit/487568c2b9dc44677a41961b121e9da7f7cd2115))

### Ensemble and Analysis

- Ensemble export/import commands with checksum-verified remote imports ([b49d2f7](https://github.com/Dicklesworthstone/ntm/commit/b49d2f7b7a82aa4288254382c7fe683a51db4838), [bc020e3](https://github.com/Dicklesworthstone/ntm/commit/bc020e34057c7ad54ff242525fdfd867edb716e0))
- `ntm rebalance` command for workload analysis ([948d0ca](https://github.com/Dicklesworthstone/ntm/commit/948d0ca32b76bd8d910ace1ed0592030485f18d9))
- Review queue command for session triage ([b02f035](https://github.com/Dicklesworthstone/ntm/commit/b02f035f19cad646dbd153b201ba5c4424553830))
- Redact preview and tests ([ac949cf](https://github.com/Dicklesworthstone/ntm/commit/ac949cf733c4bbd552b11f9c385477cadfae403c))

### Monitoring and Observability

- Prometheus exposition format export ([6b0d914](https://github.com/Dicklesworthstone/ntm/commit/6b0d914ac8c42e09bef7540c8cc3d07283a1ea3f))
- Expanded webhook payload templates and event types ([1685f47](https://github.com/Dicklesworthstone/ntm/commit/1685f478250448f6210c44600761dc2e19ae9d02))
- Effectiveness tracking module for assignments ([3f367fe](https://github.com/Dicklesworthstone/ntm/commit/3f367fe420917ec882924437ae78caca67156359), [d618407](https://github.com/Dicklesworthstone/ntm/commit/d618407d44a1718b51b602c68ed30e5974730cac))
- Effectiveness dashboard panel in TUI ([d06428d](https://github.com/Dicklesworthstone/ntm/commit/d06428d0f7b433fd681f889192995ac168403505))
- Scoring tracker with comprehensive metrics ([7ba67bd](https://github.com/Dicklesworthstone/ntm/commit/7ba67bd6a1059c04ec75636226c344e1b840c775))
- `ntm context inject` command for project context injection ([c353604](https://github.com/Dicklesworthstone/ntm/commit/c353604bad8d4e1592f7396ed8c76a8073afccb1), [1f2ee43](https://github.com/Dicklesworthstone/ntm/commit/1f2ee43fc2afa3ab4305f7d9a71c263432267b7c))

### Rate Limiting and Resilience

- Codex rate-limit detection with AIMD adaptive throttling ([af51043](https://github.com/Dicklesworthstone/ntm/commit/af510435811e4f627df9b4c07667b1a7a60887df))
- PID-based liveness checks replacing text-based detection, false-positive reduction ([bbac298](https://github.com/Dicklesworthstone/ntm/commit/bbac2984c3d5038d8a1b3e32571c4fe6ce0c30d1), [b91dd7f](https://github.com/Dicklesworthstone/ntm/commit/b91dd7f185791d97063863de945b3f0504712290))
- Auto-restart-stuck agent detection ([8980064](https://github.com/Dicklesworthstone/ntm/commit/8980064a7ccc1f3c99a082bbeddcbc1cbae9ed64))

### Robot API Expansion

- Major expansion of robot API infrastructure ([b2e2cec](https://github.com/Dicklesworthstone/ntm/commit/b2e2cec106492e8b95918e8eca11bfe2fc40e260), [856bdc1](https://github.com/Dicklesworthstone/ntm/commit/856bdc15f7ebe5e2aa621f53ccab840eeac9887a))
- SLB robot bridge ([1fe4ce3](https://github.com/Dicklesworthstone/ntm/commit/1fe4ce3a822e74bad89fd1abbb3a40ccce4e45d4))
- GIIL fetch wrapper ([94f9e2d](https://github.com/Dicklesworthstone/ntm/commit/94f9e2d77150b4bb31af9e610244b01961d30074))
- `--robot-output-format` alias ([cd542f2](https://github.com/Dicklesworthstone/ntm/commit/cd542f256c5bcc1f7431518d9c203a64ed4cb23b))
- Pagination hints in ensemble modes agent output ([0a6dfa7](https://github.com/Dicklesworthstone/ntm/commit/0a6dfa772ccef036c8e183dce421e2a27a482608))

### Bug Fixes and Hardening

- Claude Code idle detection overhauled to prevent false positives ([781a117](https://github.com/Dicklesworthstone/ntm/commit/781a117dfe515887b80a89b3d3b62defed56e3ca), [c7026a7](https://github.com/Dicklesworthstone/ntm/commit/c7026a7051efd92ef277dac572b885f172548c7c))
- Palette viewport scrolling corrected -- position reset, exact line calculation, bypass clamping ([9a00a9b](https://github.com/Dicklesworthstone/ntm/commit/9a00a9b4b2178ebf1c7622b3eb45509555cbee40), [b97a17a](https://github.com/Dicklesworthstone/ntm/commit/b97a17a1866e17ea160a48cba4ea5290668fe245), [ca40eaa](https://github.com/Dicklesworthstone/ntm/commit/ca40eaaa6c8ebfa150d70e50addd7aa9fea1997c))
- Tmux buffer-based delivery for Claude Code multi-line prompts ([384f91b](https://github.com/Dicklesworthstone/ntm/commit/384f91b06b5f7c5be27b8f63289f9432372b26c7))
- `history-limit 50000` on new sessions to preserve scrollback ([7c559da](https://github.com/Dicklesworthstone/ntm/commit/7c559da21348370a32256524ec0e15af61d5d3de))
- Default Claude model updated to Opus 4.6 ([ee50ed3](https://github.com/Dicklesworthstone/ntm/commit/ee50ed3c10fd55611c10c0eabc6ae202e550a8a7))
- Replaced obsolete `--enable web_search=live` with `--search` in Codex templates ([9def2d8](https://github.com/Dicklesworthstone/ntm/commit/9def2d89c3d192007cefa2419aa457072731f8ae))
- Default memory config uses cgroup limits instead of NODE_OPTIONS ([9b0b338](https://github.com/Dicklesworthstone/ntm/commit/9b0b3389783f5fda8548c1eba67d1a97c6468a70))
- Security: palette uses command description instead of CLI example as prompt ([adc7ce2](https://github.com/Dicklesworthstone/ntm/commit/adc7ce2a59b0b8003f1457d245a6f2f25b674614))
- Pipeline data race on `state.UpdatedAt` and audit log permissions ([fe1dfdb](https://github.com/Dicklesworthstone/ntm/commit/fe1dfdb02140a5d515aabc9daa13c651d0d9a181))
- Integer overflow guard in backoff delay calculation ([4c9890d](https://github.com/Dicklesworthstone/ntm/commit/4c9890ddc710950924d9650f19a4165774f9dea5))

### Testing

- Massive test coverage push across dozens of packages: branch tests for agents, alerts, assignment, bundle, bv, cass, checkpoint, cli, config, context, coordinator, dashboard, ensemble, events, handoff, hooks, lint, output, palette, pipeline, policy, profiler, ratelimit, recovery, redaction, resilience, robot, scanner, scheduler, serve, state, summary, swarm, templates, tools, tui, util, watcher, webhook, and workflow.

---

## [v1.7.0] -- 2026-02-02 [GitHub Release]

### Privacy and Redaction System

A comprehensive privacy and safety layer added to NTM:

- **Redaction engine** (`internal/redaction`) with PII detection, priority-sorted overlap deduplication ([937bfed](https://github.com/Dicklesworthstone/ntm/commit/937bfed39b55b27d8b18598f5e1a27e186331ab8), [4b08734](https://github.com/Dicklesworthstone/ntm/commit/4b08734f7eae43db8f2c02cb8f4f8de1c7ef264f))
- **Privacy mode** spec with config and CLI flags ([577f9bb](https://github.com/Dicklesworthstone/ntm/commit/577f9bbda061d2f7845e926b8654a2a9515c6e22), [d270fb7](https://github.com/Dicklesworthstone/ntm/commit/d270fb720678916abdc21cc1eebb4825441fc730))
- Redaction config and flags plumbing ([64892e0](https://github.com/Dicklesworthstone/ntm/commit/64892e0bd7df3a2727a96df4a95d670548363c5a))
- Redaction middleware for REST/WS and persistence ([b5d45e2](https://github.com/Dicklesworthstone/ntm/commit/b5d45e209faaa87ec6175863c79ca4b87d7268c5))
- Integrated with send command, agent mail, copy/save outputs ([5e78569](https://github.com/Dicklesworthstone/ntm/commit/5e785692f13b2b3779c24310efc6a9a67b60b22d), [e28ff33](https://github.com/Dicklesworthstone/ntm/commit/e28ff3361a25e0911f3268c22d443310c50b05bd), [c30f971](https://github.com/Dicklesworthstone/ntm/commit/c30f97169bf4c63c67ea33eff8577a14bb9a83f5))
- `ntm scrub` command for outbound notification/webhook redaction ([35e2965](https://github.com/Dicklesworthstone/ntm/commit/35e29651bd576bc85bcd2a034d9e9b45419f404c))
- Safety profiles and robot support bundle flag ([f626ef6](https://github.com/Dicklesworthstone/ntm/commit/f626ef6880b965eae1f1cdc1e4ec16b6b53859c3))

### Prompt Preflight and Linting

- `ntm preflight` command for prompt validation ([d4de897](https://github.com/Dicklesworthstone/ntm/commit/d4de89798ddf4523bffb5c947ad80b41baccf0b9))
- Core lint rules and PII detection checkers ([61df055](https://github.com/Dicklesworthstone/ntm/commit/61df05f533feb7e4f8884d49d96f0cfbe19e640a), [34476836](https://github.com/Dicklesworthstone/ntm/commit/34476836ae8268d5f4d81710e36d9a57285cf8de), [8c8427f](https://github.com/Dicklesworthstone/ntm/commit/8c8427fe55c57fe9698f7d8a8e68a23ecdaa0b76))
- DCG check integration in preflight ([8ca6ed7](https://github.com/Dicklesworthstone/ntm/commit/8ca6ed70ce5719be892975d7110a50e3519e546c))

### Support Bundle

- `ntm support-bundle` command for diagnostic data collection ([9fdffd5](https://github.com/Dicklesworthstone/ntm/commit/9fdffd563f489995beab9fc302e6089caf8532a4))
- Manifest schema and verification for bundle indexing ([07f7275](https://github.com/Dicklesworthstone/ntm/commit/07f72754b08b72ac2608a5cfabf68240a49ccd67), [d0d6655](https://github.com/Dicklesworthstone/ntm/commit/d0d66550c0cba6514787591a555e9828f2cdc64b))
- Privacy mode enforcement in support bundles ([33bb619](https://github.com/Dicklesworthstone/ntm/commit/33bb61965335ab14ad8627d33a595878fe7deae5))

### Agent Ecosystem

- **Ollama** recognized as a new agent type ([d878a1b](https://github.com/Dicklesworthstone/ntm/commit/d878a1bb337e5eb923488d76b01bb1dd04bf10d3))
- `spawn --local`/`--ollama` for local Ollama agents ([24d0640](https://github.com/Dicklesworthstone/ntm/commit/24d06408c6bc0b4526c4e8e1c1eb543f6b4a4d5a))
- Swarm orchestration snapshot in system state output ([31077eb](https://github.com/Dicklesworthstone/ntm/commit/31077ebe9b7ac99077318ecc1e4705cd366f1e58))
- Webhook built-in formatters for Slack, Discord, and Teams ([6b1b107](https://github.com/Dicklesworthstone/ntm/commit/6b1b1070ae0121e84373c4bb23120334a6993a75))

### TUI and Dashboard

- Smart animation with adaptive tick rate (fixes #32) ([8ff4a21](https://github.com/Dicklesworthstone/ntm/commit/8ff4a2146701b1aba667e9e85bb0d39990a72561))
- Cost panel and scrub tests ([dc9d90c](https://github.com/Dicklesworthstone/ntm/commit/dc9d90c1b9191fe7b9b1bd8880acc7f554cf16ce))
- History panel enhanced with filtering, copy, and replay ([fc44ca7](https://github.com/Dicklesworthstone/ntm/commit/fc44ca70bf4f7517b5b978d3b975b926692d801e))
- Context usage and token counts added to AgentStatus ([39c07c4](https://github.com/Dicklesworthstone/ntm/commit/39c07c4ff571638cca643a75b3f8bf6c3cf5634a), [5f80f0a](https://github.com/Dicklesworthstone/ntm/commit/5f80f0aadad93fe5a7a94f8a55ed3e921a8c728c))
- Configurable help verbosity for CLI and TUI ([64de8d9](https://github.com/Dicklesworthstone/ntm/commit/64de8d9e9756e08e8019c2b63ed9795a3b06b496))

### History and Events

- Regex search and history replay ([26ca5d3](https://github.com/Dicklesworthstone/ntm/commit/26ca5d3f13f46b5a247c4f2dfaf579927afe34d4))
- Coordinator state transitions published to event bus ([5145d94](https://github.com/Dicklesworthstone/ntm/commit/5145d947bc8d6cadf08a1062d70daf3ebb3c0e66))
- `--dry-run` preview for send command ([da03a18](https://github.com/Dicklesworthstone/ntm/commit/da03a18ad98f28649bf9adee4db131e91ec22ce7))

### Bug Fixes

- Buffer-based paste for Gemini multi-line prompts with unique buffer names to prevent races ([459176b](https://github.com/Dicklesworthstone/ntm/commit/459176b43d478679ed244884d8c8e27d1f4f7de1), [44f4a12](https://github.com/Dicklesworthstone/ntm/commit/44f4a124c8468803b47eeba1b99eb30c88370c1e))
- Idle prompts now take priority over historical errors in status detection ([8b8af4a](https://github.com/Dicklesworthstone/ntm/commit/8b8af4aadaa22a7e75c771c68657335cedd8b82e))
- Windows stub for `createFIFO` ([d728f66](https://github.com/Dicklesworthstone/ntm/commit/d728f66b3b28c65710853f1adc077c8706b9576d))
- `--allow-secret` downgrades block to warn instead of disabling scanning ([1d0398e](https://github.com/Dicklesworthstone/ntm/commit/1d0398e7a2ad24c7c9da796cad3aaf86002fee8e))
- NTM_REDUCE_MOTION parsing aligned with styles subsystem ([e9142bf](https://github.com/Dicklesworthstone/ntm/commit/e9142bf9013bf7470ba1f2d34f75b7e5eb1362d8))
- Multiple deep-review bug fixes across swarm, serve, and coordinator ([a76634b](https://github.com/Dicklesworthstone/ntm/commit/a76634b17961d8957232b13230fda0a2a332d769), [541d8a4](https://github.com/Dicklesworthstone/ntm/commit/541d8a4918cb3ede63d65c6afd1f1432a9105648))

### Logging Migration

- Stderr-based warnings migrated to structured `slog` logging across archive, checkpoint, events, persona, scanner, and supervisor modules ([a679909](https://github.com/Dicklesworthstone/ntm/commit/a679909813b012f849ce4525d511d8da0b8b5946), [bc699e0](https://github.com/Dicklesworthstone/ntm/commit/bc699e0b1ae6ccd882058ad5ca4ead2669ae7561), [374b576](https://github.com/Dicklesworthstone/ntm/commit/374b57658c7d9abfc773bf390f27d3896645fe44))

### E2E Test Coverage Expansion

- Comprehensive E2E tests added for: activity, metrics, checkpoint, rollback, config validate, doctor, init workflow, logs, history, summary, profiles, watch, robot-monitor, git operations, guards, hooks, copy, lock/unlock ([2c0e53c](https://github.com/Dicklesworthstone/ntm/commit/2c0e53c8b122fe119e8b19e65654704fde51bbe7)...[3ac08e0](https://github.com/Dicklesworthstone/ntm/commit/3ac08e00b3330db17aab3efbf82f4fb4cdf3486b))

---

## [v1.6.0] -- 2026-01-29 [Tag Only]

This is a tag without a published GitHub Release. It represents a large development cycle (~638 commits from v1.5.0) focused on the REST API, ensemble synthesis, and coordinator maturity.

### REST API and Server

- **OpenAPI specification generation** ([cd797dd](https://github.com/Dicklesworthstone/ntm/commit/cd797dde))
- Comprehensive session, pane, and agent management endpoints ([fb8e973](https://github.com/Dicklesworthstone/ntm/commit/fb8e973b))
- Safety, policy, and approvals management endpoints ([1b15c20](https://github.com/Dicklesworthstone/ntm/commit/1b15c209))
- Beads REST API, WebSocket events, web dashboard ([bb3aa41](https://github.com/Dicklesworthstone/ntm/commit/bb3aa41a))
- WebSocket event persistence and backpressure ([dfc3c9f](https://github.com/Dicklesworthstone/ntm/commit/dfc3c9f4))
- CASS, checkpoints, accounts APIs and pane streaming ([15eceb7](https://github.com/Dicklesworthstone/ntm/commit/15eceb78))
- Accounts RBAC permissions and pane output streaming ([8007a52](https://github.com/Dicklesworthstone/ntm/commit/8007a52e))

### Ensemble Synthesis

- **Ensemble compare** subcommand for run diffing ([256cee9](https://github.com/Dicklesworthstone/ntm/commit/256cee9b))
- Deterministic run comparison and JFP E2E tests ([90a76af](https://github.com/Dicklesworthstone/ntm/commit/90a76af6))
- Ensemble synthesis robot command ([e55ee23](https://github.com/Dicklesworthstone/ntm/commit/e55ee239))
- Ensemble modes and presets robot API ([bf296da](https://github.com/Dicklesworthstone/ntm/commit/bf296da0))
- Findings deduplication engine with clustering ([18b5ecf](https://github.com/Dicklesworthstone/ntm/commit/18b5ecf6))
- Velocity tracking for mode output analysis ([9da90d2](https://github.com/Dicklesworthstone/ntm/commit/9da90d2a))
- Presets command to list ensemble configurations ([7f783d5](https://github.com/Dicklesworthstone/ntm/commit/7f783d53))

### Robot Mode Enhancements

- **Pagination infrastructure** for robot module with status, snapshot, and history ([83b7db5](https://github.com/Dicklesworthstone/ntm/commit/83b7db51), [15333a5](https://github.com/Dicklesworthstone/ntm/commit/15333a5c))
- `--robot-health-oauth` flag for OAuth/rate-limit status ([bc978d7](https://github.com/Dicklesworthstone/ntm/commit/bc978d72))
- `--robot-docs` command for programmatic documentation access ([2ab2a93](https://github.com/Dicklesworthstone/ntm/commit/2ab2a93f))
- `--robot-env` flag and env command improvements ([888fa85](https://github.com/Dicklesworthstone/ntm/commit/888fa859))
- `--bead` and `--prompt` flags for restart-pane ([1046b88](https://github.com/Dicklesworthstone/ntm/commit/1046b88e))
- RCH status and workers integration ([41f8bd5](https://github.com/Dicklesworthstone/ntm/commit/41f8bd57))
- JeffreysPrompts and MetaSkill command registrations ([a46dbb5](https://github.com/Dicklesworthstone/ntm/commit/a46dbb57))
- ru sync integration with robot mode support ([5cd30843](https://github.com/Dicklesworthstone/ntm/commit/5cd30843))
- Context usage included in agent routing decisions ([11b4ee5](https://github.com/Dicklesworthstone/ntm/commit/11b4ee56))
- Ensemble commands added to capabilities schema ([e443ef1](https://github.com/Dicklesworthstone/ntm/commit/e443ef11))

### CLI Additions

- `ntm search` command wired for CASS queries ([4ea9bf1](https://github.com/Dicklesworthstone/ntm/commit/4ea9bf13))
- `ntm logs` command for aggregated agent log viewing ([97315c6](https://github.com/Dicklesworthstone/ntm/commit/97315c61))
- Handoff ledger command; ensemble conflict/coverage analysis ([fa857398](https://github.com/Dicklesworthstone/ntm/commit/fa857398))
- `--summarize` flag on `ntm kill` ([7eed0ce](https://github.com/Dicklesworthstone/ntm/commit/7eed0ce5))
- Timeline markers for prompt sending and session kill ([cb9dbdf](https://github.com/Dicklesworthstone/ntm/commit/cb9dbdf3))
- Fuzzy session name resolution with prefix matching ([b17ab04](https://github.com/Dicklesworthstone/ntm/commit/b17ab04e))
- Controller agent command and spawn pacing configuration ([95a3a7b](https://github.com/Dicklesworthstone/ntm/commit/95a3a7b3))
- Coordinator assign command flags for strategy, filtering, and templates ([9a16766](https://github.com/Dicklesworthstone/ntm/commit/9a16766d))

### Agent Detection

- Cursor and Windsurf patterns added to agent parser ([3e1a335](https://github.com/Dicklesworthstone/ntm/commit/3e1a335f))
- FlexTime type for flexible timestamp parsing from Agent Mail ([b472c02](https://github.com/Dicklesworthstone/ntm/commit/b472c02a))

### Spawn Improvements

- Codex cooldown gating for rate limit awareness ([28d4e26](https://github.com/Dicklesworthstone/ntm/commit/28d4e264))
- BV adapter for structured bv command execution ([c3b501a](https://github.com/Dicklesworthstone/ntm/commit/c3b501ad))

### Context and Rotation

- Context rotation triggering and compaction improvements ([bdf6de6](https://github.com/Dicklesworthstone/ntm/commit/bdf6de6e))
- CASS integration and template defaults ([b445fab](https://github.com/Dicklesworthstone/ntm/commit/b445fab8))

---

## [v1.5.0] -- 2026-01-06 [GitHub Release]

### Agent Capability and Coordination

- **Agent capability profiles** for intelligent task assignment ([fb80111](https://github.com/Dicklesworthstone/ntm/commit/fb80111fe6b9bbdadff4c59eaca34b5d291a5569))
- **Session coordinator package** for multi-agent workflows ([73b7359](https://github.com/Dicklesworthstone/ntm/commit/73b7359e9216841f753d62211a80fe8b71e4ba56))
- **ContextPackBuilder** for agent task assignment ([f75fe82](https://github.com/Dicklesworthstone/ntm/commit/f75fe821dfe11f865bbc672a1a46c495d85f537e))
- File conflict detection for multi-agent coordination ([3f2643d](https://github.com/Dicklesworthstone/ntm/commit/3f2643dd70b89dcca0ae4c74e5fe8a9c850ee87c))
- Spawn order context for agent coordination ([8a3038f](https://github.com/Dicklesworthstone/ntm/commit/8a3038f05bd9a48aa621ffdb55fe190cf5db4a06))
- Agent Mail file reservations integrated with routing ([f942bb2](https://github.com/Dicklesworthstone/ntm/commit/f942bb25eafa2676e56f27eb6e8a5235286098f2))

### Pipeline Workflows

- Pipeline resume and cleanup commands with state persistence ([79adeb6](https://github.com/Dicklesworthstone/ntm/commit/79adeb69ecb71142353e35bf102b52569b42be0f))
- Loop constructs for workflows ([edbd42e](https://github.com/Dicklesworthstone/ntm/commit/edbd42e8980886f7b00a1b3401302eb96bb9c545))
- Enhanced error handling with dependency tracking ([0fe14ce](https://github.com/Dicklesworthstone/ntm/commit/0fe14ce2ca426da6aa2069a680cd7dc19b1f7be4))
- Multi-channel notification system for pipeline events ([dcdfb91](https://github.com/Dicklesworthstone/ntm/commit/dcdfb91d78d73b633b8a90a9e219565c1dd3241b), [ce9e289](https://github.com/Dicklesworthstone/ntm/commit/ce9e289f9b297a40f434edb32b2c8632e5e83933))

### Robot Mode Expansion

- `--robot-alerts` and `--robot-beads-list` for TUI parity ([ac7afae](https://github.com/Dicklesworthstone/ntm/commit/ac7afaeacf16870a4e67168bc0e1a5f0208de64d))
- Bead management robot commands ([004e66a](https://github.com/Dicklesworthstone/ntm/commit/004e66abc6af94164feb5bfd7231dee165c9e26a))
- CASS injection to robot API with relevance filtering and topic filtering ([30b9f1a](https://github.com/Dicklesworthstone/ntm/commit/30b9f1a2f4ac84a404d6e7c2584644f8b427de71), [00a98c3](https://github.com/Dicklesworthstone/ntm/commit/00a98c358235eaab07b331abd9d1c9ba49cde181), [30cd6e3](https://github.com/Dicklesworthstone/ntm/commit/30cd6e3af3e77af651b1083a284a13de8ca261fa))
- Pre-send CASS query for automatic context injection ([00c6608](https://github.com/Dicklesworthstone/ntm/commit/00c660807b5dbb4d5973777644ac15453f2a24fe), [68a4603](https://github.com/Dicklesworthstone/ntm/commit/68a46037ffc01ae1bfa6b65843298987588ef450))

### CLI Additions

- Checkpoint restore command with `--latest` support ([15a238f](https://github.com/Dicklesworthstone/ntm/commit/15a238f009e315d9260f9265e5874e8b83fb1d5d), [1cef4e1](https://github.com/Dicklesworthstone/ntm/commit/1cef4e1afe09b5a262a7632162e060f7d4afdb1f))
- Memory privacy commands for cross-agent settings ([f58ceef](https://github.com/Dicklesworthstone/ntm/commit/f58ceefcccad954cd4997ec8f39e9deb131712c7))
- `ntm cass preview` command ([42250ba](https://github.com/Dicklesworthstone/ntm/commit/42250ba2ee51f10d7c0d50c189f7dc0b4243b1e1))
- Dynamic profile switching command ([acc3c14](https://github.com/Dicklesworthstone/ntm/commit/acc3c145e65a9efb46848114b2225d54396a4b2d))
- Unified messaging via `ntm message` ([1e66d55](https://github.com/Dicklesworthstone/ntm/commit/1e66d552f73f9e213aaa9cfb54c77abe3bc8bd9c), [61b391d](https://github.com/Dicklesworthstone/ntm/commit/61b391d8ce5e086be146cec2e945aea46b186f66))

### CASS Memory Integration

- CASS Memory server integration and CLI commands ([11ba6b7](https://github.com/Dicklesworthstone/ntm/commit/11ba6b7348fb6b3a8aed4c967e792e04abec6b16))
- CASS injection configuration in config schema ([d22708f](https://github.com/Dicklesworthstone/ntm/commit/d22708f9052677d40251ddeac6c6e466993d9de6))

### Upgrade Protection

- SHA256 checksum verification for downloads ([fff4377](https://github.com/Dicklesworthstone/ntm/commit/fff4377f526f570607b8f8945fe52cb6160f588f))
- Download progress indicator ([0c123ed](https://github.com/Dicklesworthstone/ntm/commit/0c123ed295c95289917f3bb60bebaebc11d8bc72))
- Post-upgrade binary verification ([e56a21c](https://github.com/Dicklesworthstone/ntm/commit/e56a21c4e6c3c5c88353194039867c8470cbbcf2))
- Structured diagnostic error messages ([0d2564f](https://github.com/Dicklesworthstone/ntm/commit/0d2564f22a290b611faba97d0d05d01435955aaa))

### Dashboard

- Routing info in metrics panel ([eb7e0d9](https://github.com/Dicklesworthstone/ntm/commit/eb7e0d954140d4e89b284d23c1f98311d53c4f1a))
- Profile names displayed prominently in pane cards ([bfc7ab6](https://github.com/Dicklesworthstone/ntm/commit/bfc7ab6c491675d53f1a1479520cef821db7a3a9))
- Spawn progress panel with real-time countdown ([e483998](https://github.com/Dicklesworthstone/ntm/commit/e483998a486ce853a8b222e868c68574d30198f0))

### Metrics and Monitoring

- Success Metrics Tracking system ([91d28ca](https://github.com/Dicklesworthstone/ntm/commit/91d28ca5b8621c896572d44e13cc62e598a3a1f1))
- Health monitoring configuration schema ([44983bd](https://github.com/Dicklesworthstone/ntm/commit/44983bd849ba4e2e4286ff9e3a48ffa7f8574994))
- bd daemon integration and health command support ([745cd92](https://github.com/Dicklesworthstone/ntm/commit/745cd926b4537065a4d903a1efc561b23b25e9dc))
- Server config (host binding, log directory, poll interval) ([3e5b93d](https://github.com/Dicklesworthstone/ntm/commit/3e5b93da8ec83921d2c82e9cf1af9219731102a8))

### Performance

- Parallelized agent setup and limited tmux queries ([eaeb05b](https://github.com/Dicklesworthstone/ntm/commit/eaeb05b6bc36a2308a47ea20bf17969792e367bb))
- Optimized file I/O for large files and command output ([127515d](https://github.com/Dicklesworthstone/ntm/commit/127515d7eade1d95358740e62220ad4eff9b39ed))
- Reduced lock contention in watcher Add/addRecursive ([5bd241f](https://github.com/Dicklesworthstone/ntm/commit/5bd241fad3d69d054fccfc669259badd643f3c74))

---

## [v1.4.1] -- 2026-01-04 [GitHub Release]

Patch release addressing robot mode and dashboard issues.

### Features

- `--safety` flag for spawn to prevent accidental session reuse ([8692043](https://github.com/Dicklesworthstone/ntm/commit/8692043b3742a719d2885e6526b6fa736cfaba8c))

### Bug Fixes

- Dashboard health data now refreshes periodically with status updates ([3071680](https://github.com/Dicklesworthstone/ntm/commit/3071680be674cd2d59950d91ea6f49ac238e11af))
- Redundant condition removed in health `detectActivity` ([138144b](https://github.com/Dicklesworthstone/ntm/commit/138144b2c3ae9019165d12106aff761ff035dd6c))
- Text truncation removed from robot markdown output ([092ab95](https://github.com/Dicklesworthstone/ntm/commit/092ab956e82826e13326bbb6411ec48ce711d43e))
- Helpful hint added to `--spawn-safety` error message ([25dcc81](https://github.com/Dicklesworthstone/ntm/commit/25dcc816e3f9209b2b245d55c1f03883732d8341))
- `json.Encoder` error handling in `outputError` ([4aaf3ec](https://github.com/Dicklesworthstone/ntm/commit/4aaf3ec2d37385d40b6496db163bd0aae78b1921))
- Rune-aware truncation in health command ([6baa3c0](https://github.com/Dicklesworthstone/ntm/commit/6baa3c0539b357eac74f3ab9e223068e1f7c2157))

---

## [v1.4.0] -- 2026-01-04 [GitHub Release]

### HTTP Server and REST API

- **HTTP server** with REST API and SSE streaming (`ntm serve`) ([e72e654](https://github.com/Dicklesworthstone/ntm/commit/e72e654929bd44dee1c4d6b2d603ce52ab38b871))

### Safety and Policy

- **Policy package** for destructive command protection with automation support ([3f9715e](https://github.com/Dicklesworthstone/ntm/commit/3f9715ee16c4ea4f3979ec90f6d9a951b86f37ef))
- **Approval workflow engine** for supervised operations ([1915a12](https://github.com/Dicklesworthstone/ntm/commit/1915a120832e621f5aa34357426d53422072539f), [1690f29](https://github.com/Dicklesworthstone/ntm/commit/1690f2995e79f9a0b1964b3b920a624398454cfc))
- **Design invariants enforcement** ([51020a7](https://github.com/Dicklesworthstone/ntm/commit/51020a72f3e4c27299a2c42ae7927a02c7705ce5))

### Health Monitoring Infrastructure

- Per-agent health API via `--robot-health=SESSION` ([596a059](https://github.com/Dicklesworthstone/ntm/commit/596a05968a4e02a88d30527267bfe6cf37d9cf10))
- Health state tracking for agents ([30867cf](https://github.com/Dicklesworthstone/ntm/commit/30867cfd43b6455868a9cc6f2d7687f8374d1b61))
- Alerting system for health events ([99a99a8](https://github.com/Dicklesworthstone/ntm/commit/99a99a8548235191b0baebfb429cb292bd072672))
- Automatic restart with alerting integration ([1c0db5c](https://github.com/Dicklesworthstone/ntm/commit/1c0db5ce4081d69ff7e266915a82e4500de5d58a))
- Rate limit backoff with exponential growth ([bb4917c](https://github.com/Dicklesworthstone/ntm/commit/bb4917cd5d8cc390186665247a614e0bccb5fec7))
- Health indicators added to dashboard pane cards ([53a4dd9](https://github.com/Dicklesworthstone/ntm/commit/53a4dd97e1506bea59559629a58953fce262b3c3))

### State and Event Infrastructure

- **State Store and Daemon Supervisor** foundations ([0ff3b3f](https://github.com/Dicklesworthstone/ntm/commit/0ff3b3fe059e9d3f3603f3d7d20fa33a0c81e948))
- Event log replay and `Since` functions for recovery ([541cd74](https://github.com/Dicklesworthstone/ntm/commit/541cd74d9da99e35a022f67319abed58802609c8))
- **Tool Adapter Framework** for ecosystem integration ([cc816fa](https://github.com/Dicklesworthstone/ntm/commit/cc816fad24ddcea6de91331697c94f179e158d5d))

### CLI Additions

- `ntm doctor` command for ecosystem health validation ([aa57934](https://github.com/Dicklesworthstone/ntm/commit/aa579341951e62f5b305dd14ab650926ed9d6fed))
- `ntm setup` command for project initialization ([189d804](https://github.com/Dicklesworthstone/ntm/commit/189d8044c40f8d4cf18f6721a272645c5fe28bcc))
- `ntm work` commands for intelligent work distribution ([3f26d19](https://github.com/Dicklesworthstone/ntm/commit/3f26d19900a20effe7e2250e19bf0aeeb050a5ae))
- `ntm guards` for Agent Mail pre-commit guards ([2f712c4](https://github.com/Dicklesworthstone/ntm/commit/2f712c448f43e769d86b56719ca541ee3ded7d42))
- `ntm health` with watch mode and filters ([26d4e9e](https://github.com/Dicklesworthstone/ntm/commit/26d4e9efc98d5b8b1ba6ed49be3843d6c7f3e767))
- File reservation lifecycle commands ([75480bb](https://github.com/Dicklesworthstone/ntm/commit/75480bb4b28fda65fcf601d485491e5da76273e0))
- Config diff, validation, and test strategy foundation ([2846ff2](https://github.com/Dicklesworthstone/ntm/commit/2846ff29abbd5bda1a020ef2521a61ba5549c1fb), [bc7efc5](https://github.com/Dicklesworthstone/ntm/commit/bc7efc5ebf4932608afcb96cbe50ef1d94cdea09))

### Beads Viewer Integration

- Markdown triage output for token savings ([31ddcf1](https://github.com/Dicklesworthstone/ntm/commit/31ddcf1d7ecaf9c718a2c99a7db2de0c719d17a0))
- `-robot-triage` mega-command integration with caching ([1e8e6cb](https://github.com/Dicklesworthstone/ntm/commit/1e8e6cb57ec8807eed14e864f71f435a71ae7e21))

### Notification System

- File inbox and advanced routing ([647b6b8](https://github.com/Dicklesworthstone/ntm/commit/647b6b871a5097bac5f42e2a5ba6be18f699c66d))
- Styled UX components and error helpers ([64cc085](https://github.com/Dicklesworthstone/ntm/commit/64cc0850330cd7dba80832aa8c0f5f4c4004b834))

### Bug Fixes

- Critical safety bugs in wrapper scripts and policy YAML ([3c46d64](https://github.com/Dicklesworthstone/ntm/commit/3c46d64a9f1903b5a0b8124bf46a1884dd5c23d2), [c7288664](https://github.com/Dicklesworthstone/ntm/commit/c7288664feb76fd4311d43a92a0b28fb5c6fb133))
- Data race prevention in recovery goroutine and supervisor restart ([56ab502](https://github.com/Dicklesworthstone/ntm/commit/56ab5027027a3efc5d4ecf98e58b53eea39567ec), [429a674](https://github.com/Dicklesworthstone/ntm/commit/429a674b122e8040a187f488790c229104ae5d03))
- UTF-8 boundary handling in multiple truncate functions ([a38bf19](https://github.com/Dicklesworthstone/ntm/commit/a38bf19451da1bef90f1d555c26d73f2080a072f), [a1f2b0b](https://github.com/Dicklesworthstone/ntm/commit/a1f2b0b685e9577a9ef3bcb5c54954c484814b9c))
- NULL column handling in SQL queries with COALESCE ([5fa701f](https://github.com/Dicklesworthstone/ntm/commit/5fa701fa7edaf64b0cc1556f4c1e2f51814b5689))

---

## [v1.3.0] -- 2026-01-03 [GitHub Release]

### Pipeline Execution Engine

- **Workflow schema, parser, and dependency resolution** ([6b57807](https://github.com/Dicklesworthstone/ntm/commit/6b57807392b7811c7b23f1bdea6ace6866eb476e))
- **Execution engine core** with executor ([c5c0308](https://github.com/Dicklesworthstone/ntm/commit/c5c030818faaabe7a3715f130f33afab340b9d27))
- Parallel step execution with coordination ([caa85f8](https://github.com/Dicklesworthstone/ntm/commit/caa85f8ff305a18aa7abfcc2de6c113f6f771bf4))
- Variable substitution and condition evaluation ([60cc758](https://github.com/Dicklesworthstone/ntm/commit/60cc758fc21728f792a7b7c490ea09f43688db96))
- Context isolation between stages ([2234e14](https://github.com/Dicklesworthstone/ntm/commit/2234e14c816573ba418b9e0c692286144828df0f))
- Robot-pipeline APIs for workflow execution ([f13b761](https://github.com/Dicklesworthstone/ntm/commit/f13b761bba88cbd8fa95cae9aa207021894959e5))
- Pipeline CLI subcommands: run, status, list, cancel, exec ([0f8a3d3](https://github.com/Dicklesworthstone/ntm/commit/0f8a3d3a310521501ab4f91bea98799c9656daa7))

### Context Window Rotation

- **Seamless agent rotation** when context windows fill ([cf942c5](https://github.com/Dicklesworthstone/ntm/commit/cf942c567df905d242cebc79f54e7e0d422b717b))
- Token usage monitoring for rotation triggers ([950b4c7](https://github.com/Dicklesworthstone/ntm/commit/950b4c7fe0091c8d3afd31f786907db47e265789))
- Graceful degradation before rotation ([ff68ec5](https://github.com/Dicklesworthstone/ntm/commit/ff68ec587b35c0e9a209e4c5353fd7e75f62d441))
- Handoff summary generation ([d9890632](https://github.com/Dicklesworthstone/ntm/commit/d9890632cc3b712cc31bfe079adee4240a4ae323))
- Rotation history and audit log ([c341a12](https://github.com/Dicklesworthstone/ntm/commit/c341a12ead6acad7547de71fcc4f0a2c0a0dab2d))
- Configurable rotation thresholds ([d6fb654](https://github.com/Dicklesworthstone/ntm/commit/d6fb6540d974c3cdf4ced666d000ec507766f8a7))

### Robot Mode Hardening

- `--robot-schema` for JSON Schema generation ([cf9d26b](https://github.com/Dicklesworthstone/ntm/commit/cf9d26ba9be12c6a6753369e237f27ecc6ed5a5a))
- `--robot-route` API with 7 routing strategies and fallback chain ([ee96b4b](https://github.com/Dicklesworthstone/ntm/commit/ee96b4b84a9be51101862e2a6823c80489d32bd9), [7e2d325](https://github.com/Dicklesworthstone/ntm/commit/7e2d3252235a4071140f97119180e5d2b13de058))
- `--robot-assign` for work distribution ([d82c278](https://github.com/Dicklesworthstone/ntm/commit/d82c27875506e7458611140637cea0b7f65f1201))
- `--robot-context` for context window usage ([7e1e5cd](https://github.com/Dicklesworthstone/ntm/commit/7e1e5cdba51836e4702ba5055a5cb4a433cd24e9))
- `--robot-tokens` and `--robot-history` flags ([1deb8bd](https://github.com/Dicklesworthstone/ntm/commit/1deb8bdccb461267d1f0cf968131edeb63181a26))
- Agent wait command and scoring system ([2ca746c](https://github.com/Dicklesworthstone/ntm/commit/2ca746c2bb0cc44b5f430c5fd17788fc0b601fb6))
- Standardized JSON error responses (R2), unified agent type aliasing (R3), duration string support (R1), dry-run (R5/R6) ([99609b9](https://github.com/Dicklesworthstone/ntm/commit/99609b9116226cdf2c1c9e8194a420fda7771350), [e2dd3ed](https://github.com/Dicklesworthstone/ntm/commit/e2dd3ed0ab7b8ede48991b56eeb884edb0b66442), [1f93142](https://github.com/Dicklesworthstone/ntm/commit/1f93142da4e54e96f07019cc59dfce824f9f72f0), [2ffb102](https://github.com/Dicklesworthstone/ntm/commit/2ffb1028dcbb1f2123148944756fa85ae57b4f18))
- Agent activity detection system ([d9ab5b8](https://github.com/Dicklesworthstone/ntm/commit/d9ab5b826cf10cbd5c2e4cbc113bf4e45920ce68))

### TUI Polish

- Responsive help bar (T1), context-sensitive panel hints (T2), scroll indicators (T3), data freshness indicators (T4) ([37f451c](https://github.com/Dicklesworthstone/ntm/commit/37f451cbe98e15f10ad3ad681559755b3706cdda), [376f0ff](https://github.com/Dicklesworthstone/ntm/commit/376f0ffb57768fd7cb22af64c9b8168341da4c53), [30a78b9](https://github.com/Dicklesworthstone/ntm/commit/30a78b9761cd679d471e5b3ac84ff6acf63e6ff8), [02f382a](https://github.com/Dicklesworthstone/ntm/commit/02f382a7cef3cd067931d61f1c1b4e07fc41e07d))
- Enhanced empty states (T6), error recovery feedback (T7), focus visibility (T8), standardized truncation (T9), column visibility (T10), fixed-width badges (T11) ([f530861](https://github.com/Dicklesworthstone/ntm/commit/f530861df0c7f20d475fb40b3dc104bc44eb3289)...[9dcf4f6](https://github.com/Dicklesworthstone/ntm/commit/9dcf4f63b8cb9806e5044853710bfb84aec9f4fc))
- UBS scanning toggle ('u' key) and configurable refresh ([bb7847a](https://github.com/Dicklesworthstone/ntm/commit/bb7847ae65a83fc66b1d172630fb58eaa33083e3), [6de0d6b](https://github.com/Dicklesworthstone/ntm/commit/6de0d6b6e68242387f24598976b32cc270bc5c19), [3e24e5d](https://github.com/Dicklesworthstone/ntm/commit/3e24e5d357f9f68e6298b4f7a4255dada9e4f653))

### Persona System

- Profile inheritance, sets, focus patterns, and template variables ([2f51a77](https://github.com/Dicklesworthstone/ntm/commit/2f51a77c5ce04f8c4bb2ebb9f6db4448cf6136d9))
- `ntm profiles list/show` commands with profile sets display ([09303c4](https://github.com/Dicklesworthstone/ntm/commit/09303c4976f3442cfa1194adfb76a6ac24e5c9b1))
- Polished personas/profiles output with box-drawing tables ([0a46769](https://github.com/Dicklesworthstone/ntm/commit/0a46769585ca14ab70c130aa804d1e601b3963aa))

### Event Bus

- Unified event bus for pub/sub communication ([8800f3d](https://github.com/Dicklesworthstone/ntm/commit/8800f3df7d8022e560d472e2678d70f77f98d0b4))

### Tmux Improvements

- `PasteKeys` with bracketed paste mode for reliable multiline input ([9212e2d](https://github.com/Dicklesworthstone/ntm/commit/9212e2d53d61e21e662f446710f17f1bcca0f576))
- Smart routing integration with `--smart` and `--route` flags ([610316a](https://github.com/Dicklesworthstone/ntm/commit/610316af2820ccbcfd815b4601fc9c32a7073b4f))
- `--stagger` flag for thundering herd prevention ([4b2d14c](https://github.com/Dicklesworthstone/ntm/commit/4b2d14c2ed8f20e18bbfebea6bd1a824ae6cdbc9))

### VSCode Extension

- File decoration provider for reservation status ([9e2ecb8](https://github.com/Dicklesworthstone/ntm/commit/9e2ecb86aad027f36d043fc27d33f4c0f133973e))
- Session tree view in activity bar ([206edd1](https://github.com/Dicklesworthstone/ntm/commit/206edd161216ef93be0ed3cdee8c86f9dbe3e551))
- Terminal command and mail interface ([4d74c78](https://github.com/Dicklesworthstone/ntm/commit/4d74c785a15b91be998b2603703ef1f1338a1570))

### Bug Fixes

- UTF-8 corruption prevention in SendKeys chunking ([38f7c64](https://github.com/Dicklesworthstone/ntm/commit/38f7c64a5082cb1088e7e8e8e2c5ef1d959e869f), [135e0c5](https://github.com/Dicklesworthstone/ntm/commit/135e0c5e5664869ca343a87f1310a3c1c026df1e))
- Pipeline cycle detection state corruption fixed ([62d2a11](https://github.com/Dicklesworthstone/ntm/commit/62d2a115833133f33d091ce87f81f76c0329759e))
- Resilience monitor rewritten as persistent auto-restart ([7f3d066](https://github.com/Dicklesworthstone/ntm/commit/7f3d066f39183ede1049d6329ff1a2c9cf156731))
- macOS universal binary support in installer ([8b0a0d3](https://github.com/Dicklesworthstone/ntm/commit/8b0a0d3077fcbe876c0d5284b7048e1a6cb11230))
- Tmux field separator changed to avoid parsing conflicts ([fdd873a](https://github.com/Dicklesworthstone/ntm/commit/fdd873ab4e51070c3bed8fd530e8629969ffc6b9))
- Context rotation multiple bug fixes: nil monitor panics, compaction logic, freed percentage calculation ([23de2cd](https://github.com/Dicklesworthstone/ntm/commit/23de2cdc414abcc47fd42e9e9b517ab2dc7b5c2a), [bcedd1e](https://github.com/Dicklesworthstone/ntm/commit/bcedd1ed0f495bef6a4a981b956ee20d34fb803d), [3338afa](https://github.com/Dicklesworthstone/ntm/commit/3338afa1c3384eb197fde31d91b088e3bfb48c92))
- ANSI escape sequence stripping improvements ([7da5a83](https://github.com/Dicklesworthstone/ntm/commit/7da5a8396f0e8e9ae47e3f6d00aadfd52692f587))
- GitHub issues #5 and #6 resolved ([e77cb05](https://github.com/Dicklesworthstone/ntm/commit/e77cb05dfdeaf480869f73907fa6643befff8a47))

### Performance

- Checkpoint git diff streamed directly to file ([41a0d24](https://github.com/Dicklesworthstone/ntm/commit/41a0d24a68fcac8add2fb44a33c98f90e9b6c1c9))
- Dashboard output render caching ([b84b444](https://github.com/Dicklesworthstone/ntm/commit/b84b444513566e608349ff2911eca60a33390808))
- Robot status command optimized, broken file tracking removed ([02871a9](https://github.com/Dicklesworthstone/ntm/commit/02871a903b14a1a209afbbd28b7ef62d72fe4360))
- Semantic capture helpers with line budgets ([5c357c1](https://github.com/Dicklesworthstone/ntm/commit/5c357c133f8c097e327543005e7ae10d2fdd7109))

### Security

- Input validation and output sanitization hardened ([7adc929](https://github.com/Dicklesworthstone/ntm/commit/7adc929b46cf1642bd3b224b4d8150aad75815c7))

---

## [v1.2.0] -- 2025-12-14 [GitHub Release]

### CASS Integration

- **CASS (Context-Aware Semantic Search) Go client** for robot mode ([5a86d62](https://github.com/Dicklesworthstone/ntm/commit/5a86d625fa66a3e26c59f9364ddf449d4a2feaec))
- `ntm cass` CLI commands: status, search, insights, timeline ([5fb96dd](https://github.com/Dicklesworthstone/ntm/commit/5fb96dd4fe822d089c102c43cb43f1fd885022ac))
- Robot mode flags for CASS integration ([8eb688f](https://github.com/Dicklesworthstone/ntm/commit/8eb688f6f592799488b3cac50dbe63f15d999984))
- Context injection and duplicate detection ([143a113](https://github.com/Dicklesworthstone/ntm/commit/143a113aaeee7f2966175800163c66876a4315de))
- Full health check and capabilities discovery ([330dec7](https://github.com/Dicklesworthstone/ntm/commit/330dec72803927a50bf3ea554d6a7f67c01bc4a6))
- Graceful degradation when CASS is unavailable ([07b8d35](https://github.com/Dicklesworthstone/ntm/commit/07b8d359d442b8562f9efee37eac4b9fad09e99d))
- CASS search palette in dashboard ([e24a314](https://github.com/Dicklesworthstone/ntm/commit/e24a3144324eb44939ad79c8bbcf23597bbeb32e))

### Account Rotation

- **Account rotation system** with `ntm rotate` command ([836b222](https://github.com/Dicklesworthstone/ntm/commit/836b2229ca8320f2b3e039d195c3d27b573a8f83), [da53e6f](https://github.com/Dicklesworthstone/ntm/commit/da53e6f457561b55bdc1d88b5e739f4cafbe8c2b))
- Multi-provider support (Codex, Gemini) via Provider interface ([496bb3c](https://github.com/Dicklesworthstone/ntm/commit/496bb3c4f7b2a7d151c3c9459778170d89071a40))
- Auto-trigger rotation on rate limit detection ([1d98c45](https://github.com/Dicklesworthstone/ntm/commit/1d98c451f18376645a7f4118c7c607b32136cb7f))
- `--all-limited` flag for batch rotation ([5163bed](https://github.com/Dicklesworthstone/ntm/commit/5163bed69a6917eda51d528463eaf3631f7ac516))
- Full restart and re-auth strategies ([c463c17](https://github.com/Dicklesworthstone/ntm/commit/c463c1770629195933c564261131a46d3cac5568))

### Authentication

- Claude Code authentication flow handler ([d0c0308](https://github.com/Dicklesworthstone/ntm/commit/d0c03084424b468e67dc84345a3141785bee6d1d))
- Auth flow detection patterns and restart strategy with shell detection ([b2beca0](https://github.com/Dicklesworthstone/ntm/commit/b2beca09a4d9c3f457e29f7f3591485ad9508893), [79b7c05](https://github.com/Dicklesworthstone/ntm/commit/79b7c0547af8d094ecf0f84b258d6e5ba052ca78))

### Dashboard Enhancements

- **Shimmer animation** for high context warnings ([1aaf537](https://github.com/Dicklesworthstone/ntm/commit/1aaf5371f7584058256b7277232b21d0faabdc8e))
- **Spinner animation** for working state agents ([ac552b4](https://github.com/Dicklesworthstone/ntm/commit/ac552b4059125648907652b0a82cd17a1bbb0b2d))
- Per-agent border colors with pulse animation ([6dfb343](https://github.com/Dicklesworthstone/ntm/commit/6dfb343ec082a1d2d7c83abe5ee787ada7eb9031))
- MetricsPanel and HistoryPanel components ([5c20bcd](https://github.com/Dicklesworthstone/ntm/commit/5c20bcd12e68bedc8dd56fdcfd2d77d70a038346))
- Panel system with beads, alerts, and ticker panels ([e1c6bd1](https://github.com/Dicklesworthstone/ntm/commit/e1c6bd1f1beea38b32f36bea75205daf7f32af9c), [15d20b7](https://github.com/Dicklesworthstone/ntm/commit/15d20b75d4d892a9f8a8d7c4439f94f7b42b3b24))
- Glamour renderer for detail pane output ([5048c89](https://github.com/Dicklesworthstone/ntm/commit/5048c890b6c1a8788a1a9100e1540c82350cad89))
- Shimmer progress bar and enhanced badges ([f40cf61](https://github.com/Dicklesworthstone/ntm/commit/f40cf61682a7cb6a36828937f548e6a2d19f51da), [1e86dd5](https://github.com/Dicklesworthstone/ntm/commit/1e86dd56bcbb87c141aa6e8a0e009cf5319cfa15))
- Help overlay with '?' toggle ([d281f18](https://github.com/Dicklesworthstone/ntm/commit/d281f186387ebe0e00f71119aa5dba68a637484e))
- Agent Mail integration fields in model ([e4f28b8](https://github.com/Dicklesworthstone/ntm/commit/e4f28b846eb43ccb34753e9e65f66b2d3a23a140))
- UBS scan status badge and layout fixes ([9d54b97](https://github.com/Dicklesworthstone/ntm/commit/9d54b97aa13deaa92e737c7bad226f39fb1c84c7))

### Command Palette

- Live reload, history tracking, and responsive layout ([72c89cf](https://github.com/Dicklesworthstone/ntm/commit/72c89cfc7c858e7ed5d440ced40074a59e8831c2))
- Pinned commands, favorites, recents, and help overlay ([474938f](https://github.com/Dicklesworthstone/ntm/commit/474938fb806ff81095f783e0f86a43e5e73e37d9))
- Improved navigation, filtering, and visual feedback ([45f3109](https://github.com/Dicklesworthstone/ntm/commit/45f310963390cbc4a760ae1eaeb9217d1ffe85f7))

### CLI Additions

- `ntm diff` command for comparing pane output ([304b819](https://github.com/Dicklesworthstone/ntm/commit/304b8195041093cb43690277ac6257675bed7693))
- `ntm copy` with pane selector, output file, and enhanced filtering ([a41b24a](https://github.com/Dicklesworthstone/ntm/commit/a41b24a51ab5021953944472c8222d50e6803865))
- `ntm pipeline` for multi-stage execution ([402617f](https://github.com/Dicklesworthstone/ntm/commit/402617fbde485a9242322eede35a35197c8cf87b))
- `ntm quota` command and provider usage fetching ([0f60f21](https://github.com/Dicklesworthstone/ntm/commit/0f60f21ee9dddb24eb55f4cd14c62d3aeebb1ecd), [d9ab127](https://github.com/Dicklesworthstone/ntm/commit/d9ab1273e51760aba4417196413cbdd0bdd5baae))
- `ntm changes` and `ntm conflicts` for file modification tracking ([249ad5c](https://github.com/Dicklesworthstone/ntm/commit/249ad5cddab70c71f81171dba36ef87f7c946556))
- `ntm mail` read/ack, inbox, and Human Overseer messaging ([ef67236](https://github.com/Dicklesworthstone/ntm/commit/ef672366a0c0b0f17c230d4d9b691704338536c0), [4b4d3cf](https://github.com/Dicklesworthstone/ntm/commit/4b4d3cf0239add06432f46785ef5cae97affaf14), [7a27dcb](https://github.com/Dicklesworthstone/ntm/commit/7a27dcbaf8625e82c7d65c21e7b6e428d9bab11a))
- `--ssh` flag on all commands for remote execution ([ddcfff6](https://github.com/Dicklesworthstone/ntm/commit/ddcfff648abeb5e3bff184c19bce172341fff7ed))
- `--tag` filtering for send, list, status, interrupt, kill ([f8a9dad](https://github.com/Dicklesworthstone/ntm/commit/f8a9dadf244e3062f2f310ab301ce04fcf0fc267))
- Clipboard integration ([6d6fb72](https://github.com/Dicklesworthstone/ntm/commit/6d6fb720211116eefa4ad937f577472574a83c97), [6efe375](https://github.com/Dicklesworthstone/ntm/commit/6efe37538eebf04e95752085bb7fe88aefda24d0))
- File watching mode, plugins command, and hooks directory support ([0cc100e](https://github.com/Dicklesworthstone/ntm/commit/0cc100eca6a4eae5fb501d4cee0f3f5456ad56b2))
- `--force` flag for project init ([0c87071](https://github.com/Dicklesworthstone/ntm/commit/0c87071b7bc8657b760d6333a0553ac8c5384e18))

### Robot Mode

- `--robot-dashboard` for AI orchestrator consumption ([f13c127](https://github.com/Dicklesworthstone/ntm/commit/f13c127c5bb89267bde0580e44b38dca1cb350e1))
- `--robot-markdown` for token-efficient LLM output ([eb13a91](https://github.com/Dicklesworthstone/ntm/commit/eb13a912e3704fb5268849ef6bbb63b66db26c79))
- `--robot-save` and `--robot-restore` for session persistence ([bfbe639](https://github.com/Dicklesworthstone/ntm/commit/bfbe6399941a0e15cb7232792e9b33b9bdcae09a))
- Agent mail integration with pane mapping and conflict detection ([bfa030a](https://github.com/Dicklesworthstone/ntm/commit/bfa030ac7f8f090d4ea7eb95aa3df36fc7d649f5), [31bea69](https://github.com/Dicklesworthstone/ntm/commit/31bea69fee4cf42a0797b036ded0469ecb9168fe))
- Rollback interrupts agents before rolling back git state ([6d0a8f9](https://github.com/Dicklesworthstone/ntm/commit/6d0a8f9bec1e0dbf2733ceb5c49354e2a1d8bcb6))

### Theme System

- Catppuccin Latte light theme and auto-detection ([f42736a](https://github.com/Dicklesworthstone/ntm/commit/f42736a5f508a8c20deaba6427a465a5a1692f88))
- NO_COLOR standard support for accessibility ([ab90a66](https://github.com/Dicklesworthstone/ntm/commit/ab90a66d2e28be2ad72dc6e3fe1ecc400d56a843))
- Style builder functions using design tokens ([ab98b4e](https://github.com/Dicklesworthstone/ntm/commit/ab98b4e82d27f5e0fdc8ad3f2dc0db8cd5cb27ea))

### Tracker and File Watching

- Git-ignore aware file snapshots ([113c506](https://github.com/Dicklesworthstone/ntm/commit/113c5069a4936129b2afe7c05b7ce4749961c53b))
- Severity classification and conflict filtering ([d0cf74a](https://github.com/Dicklesworthstone/ntm/commit/d0cf74a361e7718604d6ac1496d51c6c0a1f165f), [61abde2](https://github.com/Dicklesworthstone/ntm/commit/61abde28842b994d30ad2a6481df9e10e21ddc42))
- Config file watcher and theme configuration ([8bcc773](https://github.com/Dicklesworthstone/ntm/commit/8bcc773779e0a2033910771512dae724ab19bfe5))

### Two-Phase Startup

- Implemented two-phase startup architecture for faster initialization ([7e94bad](https://github.com/Dicklesworthstone/ntm/commit/7e94badb912feabfd14d8d0ff6e02d826b39dca0))

### Profiler

- Recommendations integrated into profile output ([a875729](https://github.com/Dicklesworthstone/ntm/commit/a8757296fa1768397d6b2e74b703f771c6310d89))

### Plugins

- Command plugins support ([f287505](https://github.com/Dicklesworthstone/ntm/commit/f28750565d1f41cad22e3fc284055781fedc606d))
- Custom agent definitions via TOML files ([cf2fb96](https://github.com/Dicklesworthstone/ntm/commit/cf2fb9666821d87885ab16195e381cf941eace9f))

### VSCode Extension

- Initial extension scaffold with webview dashboard, status bar, CLI wrapper ([324ace6](https://github.com/Dicklesworthstone/ntm/commit/324ace6802ba2502559534b67785bc8857c1666a), [a3f2450](https://github.com/Dicklesworthstone/ntm/commit/a3f2450d5119b0615037db0cf6adac40a41a460b), [e77f069](https://github.com/Dicklesworthstone/ntm/commit/e77f0695c9fcb0715a1bed7d7988f6aef14c7307))
- Context commands and improved send targets ([1571c25](https://github.com/Dicklesworthstone/ntm/commit/1571c2566c2e5d04825f121af78c95491425699f))

### History

- Duration tracking for send operations with display column ([91dc833](https://github.com/Dicklesworthstone/ntm/commit/91dc8338fe375e058fc94f892b7e1e7f620ddec3), [836c98f](https://github.com/Dicklesworthstone/ntm/commit/836c98f767c5878801f54deb38fb69dba0a898f4))

### Gemini

- Auto-select Pro model feature for Gemini agents ([8687242](https://github.com/Dicklesworthstone/ntm/commit/8687242275be245e3afabeca77e56ba4a01ae2cc))

### Bug Fixes

- ASCII-safe glyphs replace emoji to prevent terminal width drift ([724db10](https://github.com/Dicklesworthstone/ntm/commit/724db10c4028d54fcba51bd1dab13b552fdc78e4), [74f3a48](https://github.com/Dicklesworthstone/ntm/commit/74f3a489a016093b2b0335a2a347124ee144d004))
- Tmux command injection prevented via safe directory quoting ([55fc4f3](https://github.com/Dicklesworthstone/ntm/commit/55fc4f3b2415fcdf0f93cd7fa310dd475b87b4a3))
- ANSI escape code corruption fixed in scrolling text ([bab88ba](https://github.com/Dicklesworthstone/ntm/commit/bab88ba728b20433c2861d66fca6d333521b292b))
- Agent Mail thundering herd prevention, Unicode handling, session targeting ([e465e3c](https://github.com/Dicklesworthstone/ntm/commit/e465e3c9c644446f2af56c5816c1979bebbfa522), [066c2d0](https://github.com/Dicklesworthstone/ntm/commit/066c2d03e4e55c0b92f43c408d14deb3c2368ae1))
- Non-zero exit codes on error in JSON output mode ([a4fa233](https://github.com/Dicklesworthstone/ntm/commit/a4fa233edd50d173bcf30373f490b32404d6f76d))
- False idle detection from `$` in command history prevented ([77abd5e](https://github.com/Dicklesworthstone/ntm/commit/77abd5ede1e005d7969357dad9eb5be92fba1292), [c19d8ef](https://github.com/Dicklesworthstone/ntm/commit/c19d8ef32ffeb2a5f6bfbaa11cb41bb66600608b))
- Pipeline stage timeout increased from 5 to 30 minutes ([9b39f8f](https://github.com/Dicklesworthstone/ntm/commit/9b39f8fe1a8c0d47f2f08cdf6c1765a767538f43))
- Pane name regex corrected for bracket tags ([332bad0](https://github.com/Dicklesworthstone/ntm/commit/332bad0407143e68074e1a4c656d09062b605226))

### Storage

- Atomic writes, file locking, and efficient scanning ([49bd65a](https://github.com/Dicklesworthstone/ntm/commit/49bd65a6de7121914d0389ba1563ffbd69ad1d8b))

### Per-Project Configuration

- `.ntm/config.toml` for per-project settings ([e9222b6](https://github.com/Dicklesworthstone/ntm/commit/e9222b691eeee7e448f3eed92db76f85bd12c34a))
- `NTM_CONFIG` env var and JSON output for quick command ([bd24bca](https://github.com/Dicklesworthstone/ntm/commit/bd24bcaaa8a3ecb3e4a7b5fc7e2b24a18282527c))

### Layout

- TierUltra and TierMega for ultra-wide display layouts ([1d262b9](https://github.com/Dicklesworthstone/ntm/commit/1d262b9279932e60464f8495bd1f0e6401a044a2))

### CLIError System

- Structured errors with remediation hints ([859c914](https://github.com/Dicklesworthstone/ntm/commit/859c9142b40c482e67dc2a7980f4bb9df6d93193))
- "What next?" success footers for spawn and quick commands ([19967d8](https://github.com/Dicklesworthstone/ntm/commit/19967d86af3ad77933e8841019630d6d439fda62))
- Standard progress patterns for long operations ([bb9e2fe](https://github.com/Dicklesworthstone/ntm/commit/bb9e2fe736f2903b905281e38549afd9d83f2fee))

### Hooks

- Pre/post hooks for add and create commands ([8c30c14](https://github.com/Dicklesworthstone/ntm/commit/8c30c1450f688b977590135451511e67a6046480))

### Tmux Client Refactor

- Tmux operations refactored to use Client struct for remote execution support ([1de71bb](https://github.com/Dicklesworthstone/ntm/commit/1de71bbabb83c0c034cca0d8a608abee38b9944d))
- Context.Context support for cancellable operations ([a3a732f](https://github.com/Dicklesworthstone/ntm/commit/a3a732fc08ff82845414ad70d5aae1b4ae58a71b))
- Capture caching infrastructure ([3bddb39](https://github.com/Dicklesworthstone/ntm/commit/3bddb390baa50313145fae5190a1bee2724baff8))

### Persona System

- Persona system for role-based agent spawning ([1ea7fcf](https://github.com/Dicklesworthstone/ntm/commit/1ea7fcf3bd3e4ddeb4b296db47575acdfa4a543d))

### Scanner

- Continuous scanning mode with `--watch` ([61a07a9](https://github.com/Dicklesworthstone/ntm/commit/61a07a9bf3c2ee057816e611fcbdb3f07d8d9e78))
- Agent mail notifications for scan results ([08b85cc](https://github.com/Dicklesworthstone/ntm/commit/08b85cc2ba9ec9bec0f1000748f7ae132917a269))
- BV graph analysis integration ([ba331df](https://github.com/Dicklesworthstone/ntm/commit/ba331df0c04fb2445cd928494c63a99040a8cb3a))

---

## [v1.1.0] -- 2025-12-10 [GitHub Release]

### File Change Tracking

- **File change tracking** with agent attribution ([8e98d07](https://github.com/Dicklesworthstone/ntm/commit/8e98d074668b35cf5cc7939b1da2217ceb10d282))

### Watcher Improvements

- Polling-based fallback for file watching with automatic fsnotify fallback ([b27d530](https://github.com/Dicklesworthstone/ntm/commit/b27d530ad1f598003e8300355ca58204ddb6bff0), [bb2ac55](https://github.com/Dicklesworthstone/ntm/commit/bb2ac557191584dac12fe7e7a8d2286e2a2d61bf))

### Agent Mail

- Pre-commit guard install/uninstall commands ([f4b980c](https://github.com/Dicklesworthstone/ntm/commit/f4b980c5d0baac2a0dff0c73073853836a5c3fc9))

### Bug Fixes

- Dashboard pane row rendering and context bar bounds ([f543594](https://github.com/Dicklesworthstone/ntm/commit/f5435946a9458127f68c7eb365e608004e41ae1b))
- Responsive layout breakpoints aligned with design tokens ([b0ed46e](https://github.com/Dicklesworthstone/ntm/commit/b0ed46e0df7a9fc660801d191ffffd740609c4df))
- Tutorial arrow animation and ASCII AnimatedBorder ([41998ae](https://github.com/Dicklesworthstone/ntm/commit/41998aeb3d70452c7eca8fa6273c69acb2519038))
- Watch command uses correct pane.ID for output capture ([34d9c01](https://github.com/Dicklesworthstone/ntm/commit/34d9c01299403c6b0c34eca7d7e5b18bdc840d58))
- Idle grace period removed, replaced with user pane heuristic for cleaner status detection ([e73713c](https://github.com/Dicklesworthstone/ntm/commit/e73713c50b812e44e4226be2eca4287f5f8275b6))

---

## [v1.0.0] -- 2025-12-10 [GitHub Release]

The inaugural release of NTM, establishing the core multi-agent tmux orchestration platform.

### Core Session Management

- **Spawn, manage, and coordinate** Claude Code, OpenAI Codex, and Google Gemini CLI agents across tiled tmux panes
- Named panes (e.g., `myproject__cc_1`, `myproject__cod_2`) for agent identification
- Broadcast prompts to all agents of a specific type with `ntm send`
- Session persistence across SSH disconnections
- Quick project setup with `ntm quick` (directory, git, VSCode settings, Claude config, agents) ([5df7338](https://github.com/Dicklesworthstone/ntm/commit/5df733861a8931f2352cebf9fc120281c90da67d))

### Robot Mode

- **Machine-readable JSON output** for all commands via `--robot-*` flags ([cb57dcd](https://github.com/Dicklesworthstone/ntm/commit/cb57dcda987bf59eb531ed7a8e9816b138418124))
- `--robot-status`, `--robot-list`, `--robot-send`, `--robot-spawn`, `--robot-graph` ([4304a06](https://github.com/Dicklesworthstone/ntm/commit/4304a06bb35a3785df56bfd09682ac0515909e63), [25f6431](https://github.com/Dicklesworthstone/ntm/commit/25f64314cac70ceacaf157b5bc39ba5a48a5de9b))
- `--robot-terse` for ultra-compact output ([db4ecae](https://github.com/Dicklesworthstone/ntm/commit/db4ecae9ee4fe913cf9faac44c1d5809c1678515), [b98a795](https://github.com/Dicklesworthstone/ntm/commit/b98a7956438877d9f218377d776f5386c5f0a047))
- `--robot-interrupt` for priority course correction ([41598cb](https://github.com/Dicklesworthstone/ntm/commit/41598cb43db48477002746b63036ac074a07016c))
- `--robot-ack` for send confirmation tracking ([71cfce7](https://github.com/Dicklesworthstone/ntm/commit/71cfce748c36244a6f12e839c6a8d784259f8ec5))
- `--json` support across status, spawn, create, add, send commands ([c762bff](https://github.com/Dicklesworthstone/ntm/commit/c762bfff50a1985d3ca03c532619d9409a8a22a6), [c232384](https://github.com/Dicklesworthstone/ntm/commit/c232384d49785786c0496e5901567bc99bd1ea51))

### TUI Dashboard

- Split view rendering for wide terminals ([0bcb773](https://github.com/Dicklesworthstone/ntm/commit/0bcb77354516382529c4becbd86186a752ad521b))
- Responsive layout system for ultra-wide displays ([460d623](https://github.com/Dicklesworthstone/ntm/commit/460d6235a7a2480d20b9cc68aa3b3e11b4d7b0cf))
- Context usage status in pane cards ([6c00a0c](https://github.com/Dicklesworthstone/ntm/commit/6c00a0c872862ba42478b63313c6b6085e68c7ec))
- Theme-aware badge rendering ([fa207e8](https://github.com/Dicklesworthstone/ntm/commit/fa207e858d8117a918d34e82fa9ad85e425ebafe))
- Semantic color palette for consistent UI ([d48af24](https://github.com/Dicklesworthstone/ntm/commit/d48af2422956b43d59d4e502426670a9b243cec5))

### Health Monitoring

- Comprehensive agent health checking system ([d277455](https://github.com/Dicklesworthstone/ntm/commit/d2774556f85a16be50463ab021a36c6125e07c8a))
- Agent progress detection and rate limit enhancements ([9ef0bbe](https://github.com/Dicklesworthstone/ntm/commit/9ef0bbe8769062bef519cb6aa23846b7b983a8ec))
- Rate limit detection with wait time parsing ([9dd88ba](https://github.com/Dicklesworthstone/ntm/commit/9dd88ba15d0f961aa5f42ee3763d88905698e6a6))
- Compaction detection and auto-recovery ([55712084](https://github.com/Dicklesworthstone/ntm/commit/55712084a0f10c5f50b7821e87bb39e88e25bcfa), [4be24a4](https://github.com/Dicklesworthstone/ntm/commit/4be24a47d259d1dc665bea0a6af85f86ca432e3b))
- Auto-restart for crashed agents ([f4f5fca](https://github.com/Dicklesworthstone/ntm/commit/f4f5fca9137d74926a5a04103c46d07d459d7a28))

### Agent Mail Integration

- Auto-register sessions as Agent Mail agents ([4e2f305](https://github.com/Dicklesworthstone/ntm/commit/4e2f305a2b6f571a590b75dc62db7daccebb4178))
- Human Overseer messaging ([db8c059](https://github.com/Dicklesworthstone/ntm/commit/db8c0597a08ca2026507c64437a7e6cb3613f41b))
- ListReservations API for file locks ([1572f55](https://github.com/Dicklesworthstone/ntm/commit/1572f553ce40a7a8dd24518e26348ce80f991cc0))
- Delta snapshot tracking and mail integration ([453b6c3](https://github.com/Dicklesworthstone/ntm/commit/453b6c30794ac3fdcbbdac97287d5aadda7b7278))

### CLI Commands

- `ntm extract` with `--copy`, `--apply`, `--select` flags ([306b0e2](https://github.com/Dicklesworthstone/ntm/commit/306b0e2672f5d90e1f9c92dde81d044c1b207a6a), [c5a5ed5](https://github.com/Dicklesworthstone/ntm/commit/c5a5ed5a33907ae49ecbf6c45003a194e5b7961e))
- `ntm grep` for pane output search ([fa30ffb](https://github.com/Dicklesworthstone/ntm/commit/fa30ffb6facc7cb4ce746b937d36d02568fb89f9))
- `ntm history` for prompt management ([8af6eb1](https://github.com/Dicklesworthstone/ntm/commit/8af6eb174ac957682fbdf80eef9f7f0f0d8f71dd))
- `ntm personas list/show` ([e9d55d1](https://github.com/Dicklesworthstone/ntm/commit/e9d55d1a7f503780b98580cf58d058fd78fb1317))
- `ntm recipes` for managing session presets ([ed832de](https://github.com/Dicklesworthstone/ntm/commit/ed832dee6fe081e2e9bd3bbe7f233abae1150382))
- `ntm scan` for UBS integration ([155cd7a](https://github.com/Dicklesworthstone/ntm/commit/155cd7a884914e52b7b462c96988fdaca9fa46f5))
- `ntm analytics` for session usage statistics ([522dff9](https://github.com/Dicklesworthstone/ntm/commit/522dff9618766d585dd82c716c16c0111e2b663a))
- `ntm git status` and `ntm git sync` ([d67df46](https://github.com/Dicklesworthstone/ntm/commit/d67df46bcec5bc8837723f798986714054139109), [21dafee](https://github.com/Dicklesworthstone/ntm/commit/21dafeed6ca7934b14f4d7de189932778338cce2))
- `ntm checkpoint` with auto-checkpoint on risky operations ([3fb3328](https://github.com/Dicklesworthstone/ntm/commit/3fb33287b32a514afdc59432709a1cbca9ce4f17))
- `ntm self-update` for seamless upgrades ([7a2c1dd](https://github.com/Dicklesworthstone/ntm/commit/7a2c1ddb3b168bcddde0b3f2577a084a423ed0df))
- `--context` flag for file content injection ([6e67640](https://github.com/Dicklesworthstone/ntm/commit/6e67640a0674c450307dc285219da5dab09694ab))
- Interactive tutorial with animated slides ([db11ad5](https://github.com/Dicklesworthstone/ntm/commit/db11ad5ecaff6106f7e3f08f88da8c9de6284986))

### Model and Variant Support

- Model specifier parsing for spawn command ([103d7f9](https://github.com/Dicklesworthstone/ntm/commit/103d7f9e617ffab73a3c5805cc5e2a7ad8ce9923))
- Variant tracking, variant-aware targeting for send ([a16cb9f](https://github.com/Dicklesworthstone/ntm/commit/a16cb9f77644ac345c6b9ec61b3bd94ae309b308))
- Model-specific agent spawning with variant support ([95e5345](https://github.com/Dicklesworthstone/ntm/commit/95e53450ba48aaf1543e7e4352999a49c7b5802f))

### Notification System

- Multi-channel notifications (desktop, webhook, shell, log) ([9a3bc45](https://github.com/Dicklesworthstone/ntm/commit/9a3bc459ad60d8a9a1163b47b9fe73852f9adfc9))

### Event Logging

- JSONL event logging framework ([3e0df4d](https://github.com/Dicklesworthstone/ntm/commit/3e0df4ddd36ddf9f803d9c4d1f9b2c25d75e26b5))
- Token estimation for analytics and events ([040f196](https://github.com/Dicklesworthstone/ntm/commit/040f196874dcf1d3fa4b2ce618808d17394f3583))

### Prompt Templates

- Variable substitution in prompt templates ([79901ad](https://github.com/Dicklesworthstone/ntm/commit/79901adf3434cbea7905217ae7751d6c595f0593))

### Shell Integration

- Bash, Zsh, and Fish shell integration via `ntm init`

### Build and Distribution

- CI/CD pipeline and release infrastructure ([155b458](https://github.com/Dicklesworthstone/ntm/commit/155b4584fea5fb1745ce0fab2b85df23ec425aa1))
- Go 1.25 support with modernized install script ([ac58aa1](https://github.com/Dicklesworthstone/ntm/commit/ac58aa13bffac5d07130581440b052710fd5ad30))
- Homebrew formula, goreleaser, and container images
- Cross-platform support (Linux, macOS, Windows stubs)

### Configuration

- Config show with models section ([eac9904](https://github.com/Dicklesworthstone/ntm/commit/eac990412d61cfec0b25e1e9681a9ccd9d8e20fc))
- Agent command templates with comprehensive tests ([7a82a2b](https://github.com/Dicklesworthstone/ntm/commit/7a82a2b36c1f73d5e64cccdc74867bb6e0cb68dd))
- Pre/post-send hooks ([9cdf6bc](https://github.com/Dicklesworthstone/ntm/commit/9cdf6bc866d59041d42564887436b5f0d2d5e00b))
- Startup profiling with `--profile-startup` flag ([baf63e5](https://github.com/Dicklesworthstone/ntm/commit/baf63e53b36c5d58842513ba1213bb0253804c1d))

### Security

- Restrictive file permissions for tmux.conf ([bb1a401](https://github.com/Dicklesworthstone/ntm/commit/bb1a401e454b57a239d557c8fe97fbc84f2c003a))

---

*Full commit history: <https://github.com/Dicklesworthstone/ntm/commits/main>*
