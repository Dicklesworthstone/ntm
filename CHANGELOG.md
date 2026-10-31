# Changelog

All notable changes to **NTM** (Named Tmux Manager) are documented here.

NTM is a tmux session management tool for orchestrating multiple AI coding agents (Claude Code, OpenAI Codex, Google Gemini CLI, Antigravity, Grok Build, Oh My Pi, Cursor, Windsurf, Aider, Opencode, Ollama) in parallel with a stunning TUI dashboard, robot mode APIs, and deep ecosystem integrations.

- Repository: <https://github.com/Dicklesworthstone/ntm>
- Releases marked **[GitHub Release]** have published release assets on GitHub.
- Releases marked **[Tag Only]** are git tags without a published GitHub Release.
- Links point to individual commits for traceability.

---

## [Unreleased]

### Fixed

- **JSON output confidentiality in `ntm serve`**: `block` mode now protects response credentials as well as refusing sensitive requests. Response filtering preserves meaningful non-secret diagnostics and the handler's HTTP status. Unsafe custom-pattern rewrites produce a valid, explicit `RESPONSE_REDACTION_FAILED` envelope that warns the operation may already have completed, instead of leaking credentials, emitting malformed JSON, or implying the request was rolled back. JSON `Flush` calls can no longer release unredacted fragments.
- **JSON request integrity in `ntm serve`** (GH #378): enabled `redact` and `block` modes now refuse detected secrets with `422 SECRETS_DETECTED` before handlers or idempotency replay, instead of replacing credentials with malformed JSON or executable/storable placeholders. Accepted requests retain their original bytes. Errors identify categories and byte locations without echoing credential contents or field names. The policy covers `/api/v1` bodies even with a missing or mislabeled `Content-Type`; malformed and oversized bodies are explicitly rejected rather than forwarded or truncated. Response redaction and the pipeline recovery receipt exception are preserved. See [REST request integrity](docs/robot-sensitivity-redaction.md#rest-request-integrity).

## [v1.38.0] -- 2026-10-31 [GitHub Release]

**This is the first release to carry the work on `main` that v1.37.1 and v1.37.2 held back: pipeline branch checkpoints, webhook outbox persistence, checkpoint binary and untracked capture, the reservation watcher, and Gemini readiness and context metrics. It also adds reviewed `ntm worktrees` merges, operator recovery for uncertain pipeline deliveries, pipeline commands and context rotations, per-request project selection on the REST mail and reservation routes, and pi agent support. Go moves to 1.26.10 for GO-2026-6809.** Many recovery and delivery paths now stop for an operator decision where they used to repeat work or carry on, so commands that used to proceed can now refuse. Read the Upgrade notes first, and finish or inspect in-flight pipeline runs before upgrading.

### Upgrade notes

- **Reads of a tracker above your repository are now read-only** (GH #373). A read that ntm reached by walking up out of the process's own repository carries `--no-auto-import --no-auto-flush`, so it no longer imports a newer `issues.jsonl` until an ordinary `br` command runs. This applies to a clone, worktree or submodule under a parent's tracker, and to a tree with no repository at all. A repo subdirectory such as `repo/src` keeps its behaviour, including upgrading an older tracker. See Fixed for how the line is drawn.
- **`ntm worktrees merge` no longer switches branches, and `--force` is gone.** The old path checked out the default branch and ran `git merge`. Now you check out the target branch yourself, and both source and target must be clean with no pending Git operation. Conflicts, dirty checkouts and a merge that would overwrite an ignored file are refused before Git starts. Merging and previewing need Git 2.38 or later (`git merge-tree --write-tree`). `--force` is now an unknown flag. A second ntm merge in the same repository fails at once rather than waiting. An attempt whose outcome is unknown blocks further merges, worktree cleanup and provisioning, and previews report `merge_recovery_required`, until `ntm worktrees merge-recover` settles it. With `--json`, success is a structured receipt instead of prose ([6c00df53](https://github.com/Dicklesworthstone/ntm/commit/6c00df53), [9150e2a0](https://github.com/Dicklesworthstone/ntm/commit/9150e2a0), [b709d916](https://github.com/Dicklesworthstone/ntm/commit/b709d916), [90109c0d](https://github.com/Dicklesworthstone/ntm/commit/90109c0d)).
- **`ntm worktrees remove` and `clean` stop when Git refuses.** A failed `git worktree remove --force` used to fall back to recursive deletion and pruning, which destroyed locked worktrees. It now stops and the branch stays, and the command's `--force` does not override a Git lock. Detached or changed branches, foreign repositories, symlink substitutions and non-regular `.git` files are refused, and a branch whose tip moved during removal is kept. `ntm spawn --worktrees` applies the same checks in early preflight and fails on a busy repository ([829973e3](https://github.com/Dicklesworthstone/ntm/commit/829973e3), [c80f5224](https://github.com/Dicklesworthstone/ntm/commit/c80f5224), [11113dd0](https://github.com/Dicklesworthstone/ntm/commit/11113dd0), [41539077](https://github.com/Dicklesworthstone/ntm/commit/41539077)).
- **Pipeline `depends_on` is enforced inside `parallel` bodies.** Inline parallel bodies used to start every child at once. Children now start in dependency order under the existing concurrency cap, a failed prerequisite blocks its dependents transitively (`on_error: continue` does not turn it into success), and a child's dependency on a step outside the body holds the whole container. A cycle that the flat placement used to hide is now rejected ([7fa7962d](https://github.com/Dicklesworthstone/ntm/commit/7fa7962d), [d9a213fd](https://github.com/Dicklesworthstone/ntm/commit/d9a213fd)). See `docs/pipeline-parallel-dependencies.md`.
- **Pipeline runs saved by an earlier version can stop on resume.** A branch step with child work but no saved decision, or whose definition changed, refuses to guess a path. A `mail_send` step interrupted without a receipt is not sent again by ordinary resume: inspect the recipient, then choose `--mode=restart-failed` deliberately ([44091c7b](https://github.com/Dicklesworthstone/ntm/commit/44091c7b), [1d2b898b](https://github.com/Dicklesworthstone/ntm/commit/1d2b898b)).
- **Pipelines now fail on evidence they used to ignore.** A run that loses a reservation lease, or cannot verify one, fails and cancels active waits and commands; input already delivered to an agent cannot be recalled ([f8937141](https://github.com/Dicklesworthstone/ntm/commit/f8937141)). Workflow completion fails while `wait: none` cleanup is unresolved ([22de5166](https://github.com/Dicklesworthstone/ntm/commit/22de5166)). A command counts as settled only once its output pipes have closed and, on Linux, a valid census shows its process group gone; otherwise its launch stays unresolved ([29aea2d0](https://github.com/Dicklesworthstone/ntm/commit/29aea2d0)). Prompt and template steps are refused while the composer is not ready, recorded as `not_sent` so an ordinary retry or resume can recover ([793e07dd](https://github.com/Dicklesworthstone/ntm/commit/793e07dd)), and `ntm pipeline exec --stage` now goes through the same pane lock and readiness gate ([ff5e1417](https://github.com/Dicklesworthstone/ntm/commit/ff5e1417)).
- **Schema migrations `027_approval_slb_links` and `028_runtime_agent_context`** apply on first run. They add the `approval_slb_links` table and a nullable `runtime_agents.context_percent` column; existing rows stay NULL, which means no measurement ([7b76b4ad](https://github.com/Dicklesworthstone/ntm/commit/7b76b4ad), [0bbb5994](https://github.com/Dicklesworthstone/ntm/commit/0bbb5994)).
- **Rollback checks first and reports failure as failure.** REST rollback returned 200 with a warning when a patch could not be loaded or applied. It now runs a read-only preflight, also on `dry_run`, and returns 400 (`rollback preflight failed; repository unchanged`). A repository root outside the allowed project is 403. A failed stash used to warn and continue; it now stops with 500. A patch failure after checkout is 500 (`rollback incomplete`), and any stash is kept for inspection. `ntm rollback` shares the preflight and reports patch failures as errors. Stashes now include untracked files, and checkout refuses to overwrite ignored files ([61f900e6](https://github.com/Dicklesworthstone/ntm/commit/61f900e6)).
- **REST pane input and sends refuse more.** `POST /api/v1/sessions/{sessionId}/panes/{paneIdx}/input` now uses the shared dispatch engine and refuses ambiguous indices, service and dead panes, trust and startup screens, and panes quarantined by a rotation. Expect 409 for a pane that is not a live target or whose identity changed, 403 for a policy block, 503 when policy or ownership cannot be established (always for a remote pane), and 504 on timeout. REST sends and interrupts check each selected pane's project policy before admission and again at delivery, and fail closed if it cannot be loaded; control-only interrupts stay available ([da20e32e](https://github.com/Dicklesworthstone/ntm/commit/da20e32e), [f818784f](https://github.com/Dicklesworthstone/ntm/commit/f818784f)).
- **REST retries are bound to a project.** Jobs, pipelines, mail and reservations fix a request's project before reading its body or looking up an `Idempotency-Key`, so the same key in another project creates new work instead of replaying. A durable `operation_id` recorded before this release has no project binding: a project-scoped retry of it conflicts, and the receipt stays readable. `PATCH /api/v1/config` used to ignore an empty `project_dir`; `""` or `null` now resets to the startup project, while a non-string value or a removed startup directory is 400 ([bc774f3e](https://github.com/Dicklesworthstone/ntm/commit/bc774f3e), [5d0d35f0](https://github.com/Dicklesworthstone/ntm/commit/5d0d35f0), [36264bba](https://github.com/Dicklesworthstone/ntm/commit/36264bba), [8af2b832](https://github.com/Dicklesworthstone/ntm/commit/8af2b832), [18d6049f](https://github.com/Dicklesworthstone/ntm/commit/18d6049f)) (GH #360, [43410d38](https://github.com/Dicklesworthstone/ntm/commit/43410d38)).
- **WebSocket clients must handle `stream.reset` on reconnect.** A replay that would be truncated, contains malformed history or fails to complete now ends in a reset instead of a partial success. An event whose persistence failed is never published as durable, and cursors hold across retention and restart ([8e91540d](https://github.com/Dicklesworthstone/ntm/commit/8e91540d), [63ccb6fa](https://github.com/Dicklesworthstone/ntm/commit/63ccb6fa)).
- **Webhook `filter` settings now take effect.** `session`, `agent_type` and `severity` under `.ntm.yaml` `webhooks:` were dropped when an endpoint was registered, so every subscribed event went out. An endpoint with a filter now receives fewer events. Invalid values fail registration, and an event with unknown provider metadata does not satisfy an `agent_type` filter. Queued and retried work is rechecked against the current registration before each attempt ([1c5efc7e](https://github.com/Dicklesworthstone/ntm/commit/1c5efc7e), [6f8c9d52](https://github.com/Dicklesworthstone/ntm/commit/6f8c9d52)).
- **`ntm wait` runs the robot wait engine** (refs GH #350). `--exit-on-error` used to exit 3 and the help promised 2 for invalid input; every failure now exits 1, so read `error_code` from `--json` (`TIMEOUT`, `CANCELED`, `INVALID_FLAG`, `SESSION_NOT_FOUND`, `PANE_NOT_FOUND`, `AGENT_ERROR`). A pane whose state cannot be read stays pending. `--pane` takes `N`, `W.P` or `%N`, and a bare `N` means a window in a multi-window session. `--poll` must be positive. New conditions: `agent_ready`, `stalled`, `rate_limited` and `rate_limit_lifted` ([03f650a7](https://github.com/Dicklesworthstone/ntm/commit/03f650a7)).
- **`ntm context inject` picks targets differently.** By default it now targets every known agent, including agents at pane index zero, rather than every pane above index zero, and errors when there are none. `--pane` takes `N`, `W.P` or `%N`, and a window holding more than one pane is refused for a singular target. `--inject-pane` on `--robot-context-inject` is rejected when the index exists in several windows. Source files are read through the project root: a symlink leaving the project, a FIFO or other non-regular file, and a non-text file are refused before anything is sent ([b5ef639a](https://github.com/Dicklesworthstone/ntm/commit/b5ef639a), [b1e4df5e](https://github.com/Dicklesworthstone/ntm/commit/b1e4df5e)).
- **Remote (`--ssh`) requests that acted locally are refused.** `--robot-kill-agent` and every `--robot-smart-restart` mode return `INVALID_FLAG` before observing any pane, dry runs included; run them on the tmux host. `ntm context build`, `ntm context inject` and `--robot-context-inject` refuse remote source preparation (`NOT_IMPLEMENTED` in robot mode), while stored `--pack` delivery and dry runs still work ([e011adf6](https://github.com/Dicklesworthstone/ntm/commit/e011adf6), [4e5ce409](https://github.com/Dicklesworthstone/ntm/commit/4e5ce409)).
- **Restart obeys the outgoing message policy and proves readiness** (refs GH #367). `--restart-prompt` is checked before the pane is touched: `block` refuses the restart, `redact` sends the redacted text, and `--dry-run` reports the same outcome. An invalid global or project policy refuses a restart that carries a prompt. A stopped wrapper's banner, or a Codex update or trust dialog, no longer counts as ready. CAAM launches pass `--tool`, so a saved profile recreated for another provider is refused before the pane changes ([66e2473e](https://github.com/Dicklesworthstone/ntm/commit/66e2473e), [d4bbc4d3](https://github.com/Dicklesworthstone/ntm/commit/d4bbc4d3), [c20b9241](https://github.com/Dicklesworthstone/ntm/commit/c20b9241), [a9ea0759](https://github.com/Dicklesworthstone/ntm/commit/a9ea0759)).
- **`ntm mail send` uses Agent Mail's audited intervention route** (GH #355). ntm never registers the reserved `HumanOverseer` identity: against a server without the route it needs an existing `HumanOverseer` token and otherwise fails. Rejected or ambiguous sends are not retried. `--reason` (1 to 500 characters) records why, `--all` resolves to explicit recipients and cannot be combined with other recipient flags, and a send takes at most 100 recipients ([5e024bc7](https://github.com/Dicklesworthstone/ntm/commit/5e024bc7), [b0e2f1e0](https://github.com/Dicklesworthstone/ntm/commit/b0e2f1e0), [aae3e4c6](https://github.com/Dicklesworthstone/ntm/commit/aae3e4c6), [cb3ed0a4](https://github.com/Dicklesworthstone/ntm/commit/cb3ed0a4)).
- **Isolated Claude panes can refuse to launch** (GH #366). With `claude_isolate_credentials = true`, any pane-dir entry whose name contains `credential` now counts. Links are removed on re-provisioning; a regular file or directory stops spawn, add and restart with an error naming it, and ntm leaves it for you to move. A `.credentials.json` is accepted as the pane's own login only if it is a single-link regular file sharing no bytes or refresh token with a credential in `~/.claude` or another pane, and no `claude_token_file` is set. On Windows it is always refused. A pane dir that is a symlink, leads out of `claude-homes`, or is `~/.claude`, your home directory or a parent of either is refused ([a2e85938](https://github.com/Dicklesworthstone/ntm/commit/a2e85938), [5236f430](https://github.com/Dicklesworthstone/ntm/commit/5236f430), [e013f52c](https://github.com/Dicklesworthstone/ntm/commit/e013f52c), [c0960592](https://github.com/Dicklesworthstone/ntm/commit/c0960592), [a0b9802b](https://github.com/Dicklesworthstone/ntm/commit/a0b9802b), [04b321c2](https://github.com/Dicklesworthstone/ntm/commit/04b321c2), [5523c1a1](https://github.com/Dicklesworthstone/ntm/commit/5523c1a1)). See `docs/claude-credential-isolation.md`.
- **Force-release approvals need a fresh review.** Grants are bound to the project, reservation, requester, Agent Mail actor, release options and lease identity, and each is consumed once. An approval from an earlier version stays in history but cannot authorize the new operation. After a consumed, denied, expired or changed-target approval, name it with `ntm locks force-release --new-approval <id>` or REST `new_approval`. SLB decisions count only with the exact request ID and a named approve or reject review, and historical approvals with no recorded link stay native-only. Force-release policy now comes from the target project ([233d3214](https://github.com/Dicklesworthstone/ntm/commit/233d3214), [cee7b4c6](https://github.com/Dicklesworthstone/ntm/commit/cee7b4c6), [7b76b4ad](https://github.com/Dicklesworthstone/ntm/commit/7b76b4ad), [281abc3c](https://github.com/Dicklesworthstone/ntm/commit/281abc3c)).
- **Persona `context_files` fail closed.** An invalid, unreadable or oversized file now fails preparation instead of being dropped silently, and a live profile switch fails before dispatch or any change to pane metadata ([8224e811](https://github.com/Dicklesworthstone/ntm/commit/8224e811), [63a172ec](https://github.com/Dicklesworthstone/ntm/commit/63a172ec)).
- **`ntm add` and scale-up wait for the agent process on every agent type** (GH #358). A CLI that exits at once now fails with the same "did not start a stable process" error as spawn. Previously only Grok and omp were checked ([2dc71543](https://github.com/Dicklesworthstone/ntm/commit/2dc71543)).
- **Redaction finds more** (GH #372). Detection now covers quoted JSON credentials, so with `[redaction] mode = "block"`, `ntm serve` can return 422 `SECRETS_DETECTED` for a JSON body it accepted before, and `ntm scrub` reports more findings ([cfa94379](https://github.com/Dicklesworthstone/ntm/commit/cfa94379)). See Security.
- **Robot results are stricter.** `--robot-send --track` needs new response evidence, so an echo of the input no longer confirms, and tracked status can be `failed` ([23710718](https://github.com/Dicklesworthstone/ntm/commit/23710718)). `--robot-is-working` never reports `safe_to_dispatch` alongside active work, a rate limit, a dead agent or an interactive gate ([ef726568](https://github.com/Dicklesworthstone/ntm/commit/ef726568)). Automatic routing excludes a pane holding a draft or queued message even with busy, error and rate-limit exclusions disabled, so `--robot-route` and `ntm send --smart` refuse when every pane is occupied ([c619653d](https://github.com/Dicklesworthstone/ntm/commit/c619653d)). `ntm assign --dry-run` with direct, retry, reassign or clear operations is refused ([a4cbd396](https://github.com/Dicklesworthstone/ntm/commit/a4cbd396)).

### Added

- **Reviewed worktree merges** ([6c00df53](https://github.com/Dicklesworthstone/ntm/commit/6c00df53), [9150e2a0](https://github.com/Dicklesworthstone/ntm/commit/9150e2a0), [b709d916](https://github.com/Dicklesworthstone/ntm/commit/b709d916), [90109c0d](https://github.com/Dicklesworthstone/ntm/commit/90109c0d)): `ntm worktrees merge <agent> --dry-run --json` runs Git's real three-way merge without touching a checkout and reports the source and target commits, `merge_tree`, `conflicts`, `overwrite_risks`, `blockers` and `can_merge`. `--expected-source` and `--expected-target` bind a merge to the commits you reviewed. Each attempt is journaled before Git starts. `ntm worktrees merge-status` shows the last attempt, and `merge-recover <attempt-id>` acknowledges an exact completed result or, with `--abandon`, settles an unchanged clean target. Recovery never reruns a merge, and ntm never resets, aborts or stashes for you. See `docs/worktree-merge-preview.md`.
- **Pipeline branch checkpoints** ([44091c7b](https://github.com/Dicklesworthstone/ntm/commit/44091c7b), [53bb3b13](https://github.com/Dicklesworthstone/ntm/commit/53bb3b13), [4a2eea35](https://github.com/Dicklesworthstone/ntm/commit/4a2eea35)): a branch step saves its selected branch and definition hash in the same checkpoint as its children, so retry and resume keep the original choice instead of rerunning a selector that may have changed. Completed children are adopted with their outputs instead of being dispatched again, and interrupted success hooks finish through the existing hook runner.
- **Operator recovery for pipelines** ([aefee4cd](https://github.com/Dicklesworthstone/ntm/commit/aefee4cd), [0a7c7a3c](https://github.com/Dicklesworthstone/ntm/commit/0a7c7a3c), [1121e063](https://github.com/Dicklesworthstone/ntm/commit/1121e063)): `ntm pipeline recover <run-id>` lists uncertain receipts. `--accept-delivery` records that the original agent received a prompt or template, and `--accept-command --exit-code N --output-file PATH` records a foreground command's verified outcome. Neither sends anything or starts a process; ordinary `ntm pipeline resume` then continues without repeating the work. `GET` and `POST /api/v1/pipelines/{id}/recover` expose delivery recovery to REST clients, and a refusal is 409 `RECOVERY_REFUSED`.
- **Pipeline mail and reservation journals** ([1d2b898b](https://github.com/Dicklesworthstone/ntm/commit/1d2b898b), [4f5322ea](https://github.com/Dicklesworthstone/ntm/commit/4f5322ea), [f8937141](https://github.com/Dicklesworthstone/ntm/commit/f8937141)): `mail_send` steps record intent before contacting Agent Mail and restore a saved delivery without sending again. Reservation steps record their exact lease IDs, partial grants and uncertain outcomes, and release only this run's IDs, so another run's lease on the same path stays intact. A heartbeat renews leases during long commands, agent waits and post-pipeline work, and confirms each renewal by reading the lease back.
- **Durable webhook delivery** ([8a78cb7b](https://github.com/Dicklesworthstone/ntm/commit/8a78cb7b), [9593754e](https://github.com/Dicklesworthstone/ntm/commit/9593754e), [a7efe57e](https://github.com/Dicklesworthstone/ntm/commit/a7efe57e)): opt-in with `NTM_WEBHOOK_OUTBOX_DIR`. A project webhook delivery is written to disk before its first HTTP attempt and survives process exits and crashes, keeping its delivery ID, exact body and retry deadlines. Endpoint URLs, authorization headers and signing secrets are not stored in receipts, and a changed endpoint configuration holds old receipts for inspection instead of redirecting them. `ntm webhook-outbox list`, `retry RECORD --revision REVISION` and `archive RECORD --revision REVISION` inspect, reauthorize and retire failed receipts. Delivery is at-least-once, so receivers should deduplicate on `X-NTM-Delivery-ID`. Durable mode is not supported on Windows. See `docs/webhook-outbox.md`.
- **Checkpoints keep binary changes and, on request, untracked files** ([61f900e6](https://github.com/Dicklesworthstone/ntm/commit/61f900e6)): the saved patch now handles binary changes against the recorded commit. `ntm checkpoint save --include-untracked`, or REST `include_untracked`, also saves non-ignored untracked files, including empty files, executable modes and symlinks. Sensitive files that are not ignored are included, so review the selection before exporting. A requested capture that cannot complete fails visibly. See `docs/checkpoint-git-recovery.md`.
- **Reservation watcher maintains leases through quiet work** ([1d1484de](https://github.com/Dicklesworthstone/ntm/commit/1d1484de), [1c60fd71](https://github.com/Dicklesworthstone/ntm/commit/1c60fd71), [fb85e4c4](https://github.com/Dicklesworthstone/ntm/commit/fb85e4c4), [6d49d9ec](https://github.com/Dicklesworthstone/ntm/commit/6d49d9ec), [af61483d](https://github.com/Dicklesworthstone/ntm/commit/af61483d), [eb2449ca](https://github.com/Dicklesworthstone/ntm/commit/eb2449ca)): maintenance follows canonical pane activity with quiet-work heartbeats, so a long silent turn keeps its leases and a failed or stale observation does not trigger idle cleanup. Pending edits recover after late registration or a resolved conflict. The dashboard retries its watcher start with backoff if Agent Mail is down at startup, uses the target project's policy and credentials, and shows conflicts from the shared event bus in its panel and toasts.
- **Gemini readiness, context metrics and rotation** ([62806c14](https://github.com/Dicklesworthstone/ntm/commit/62806c14), [42b643cf](https://github.com/Dicklesworthstone/ntm/commit/42b643cf), [448980bb](https://github.com/Dicklesworthstone/ntm/commit/448980bb)): one classifier now serves the parser, idle detection and session observer, so a busy sibling's window activity cannot starve an idle Gemini pane. The normal quota column and bare capacity or try-again text are no longer read as rate limits, while `RESOURCE_EXHAUSTED` and other explicit exhaustion signals still are. Gemini panes report context usage from the footer and from native transcript accounting, and they join the coordinator's pending and confirm rotation path.
- **Durable context rotation and operator recovery** ([5e2324b8](https://github.com/Dicklesworthstone/ntm/commit/5e2324b8), [ef3c2efb](https://github.com/Dicklesworthstone/ntm/commit/ef3c2efb)): rotation journals each handoff boundary and carries the task, Agent Mail identity, file reservations and restart configuration to the replacement. Both panes are fenced against new dispatch and automatic restarts during the transfer. `ntm rotate context recover <agent-id> --execution-id ID --action accept|abort --reason TEXT` settles an interrupted rotation without spawning or sending again. `ntm rotate context pending --json` now reports execution and physical pane identities, the continuation phase and the allowed actions.
- **Context exhaustion forecasts** ([9e78dd26](https://github.com/Dicklesworthstone/ntm/commit/9e78dd26)): the coordinator appends advisory `alert.warning` events with reason code `context_exhaustion_forecast` to the attention feed, carrying measured growth and estimated time remaining, before the rotation threshold is reached. Forecasts never start or confirm a rotation. They run when `[rotation] usage_percent_threshold` is positive, with no new configuration. See `docs/context-forecast.md`.
- **pi agents** ([47f4fc26](https://github.com/Dicklesworthstone/ntm/commit/47f4fc26), [814f00b1](https://github.com/Dicklesworthstone/ntm/commit/814f00b1), [7890ffa7](https://github.com/Dicklesworthstone/ntm/commit/7890ffa7), [34e1cc59](https://github.com/Dicklesworthstone/ntm/commit/34e1cc59), [a8e162ad](https://github.com/Dicklesworthstone/ntm/commit/a8e162ad), [3a7d9baa](https://github.com/Dicklesworthstone/ntm/commit/3a7d9baa), [eb0697fe](https://github.com/Dicklesworthstone/ntm/commit/eb0697fe)) (GH #361): install `examples/agents/pi.toml` as `agents/pi.toml` beside your config to use `ntm spawn --pi`, `ntm send --pi` and `--robot-spawn --spawn-pi`. Each pane gets its own `--session-id`, and saved sessions resume the exact transcript. Pipelines and workflows handle pi's full-pane repaints and Markdown rendering, and prompts whose rendering cannot be matched are refused before a response-waiting delivery. Registered plugin agents gain `--spawn-<agent>` robot flags, `resume --spawn` selectors and recipe support.
- **Per-request project selection on the REST server** ([ca9942c9](https://github.com/Dicklesworthstone/ntm/commit/ca9942c9), [5d0d35f0](https://github.com/Dicklesworthstone/ntm/commit/5d0d35f0), [36264bba](https://github.com/Dicklesworthstone/ntm/commit/36264bba), [43410d38](https://github.com/Dicklesworthstone/ntm/commit/43410d38)): `ntm serve` and `ntm web` accept `--project-dir`. Mail routes and reservation routes take a `project` query parameter naming an existing absolute directory, so agents in several projects can share one server, and their events carry `project_key`. `GET /api/v1/config` reports `startup_project_dir`.
- **Swarm jobs deliver initial work** ([27b99cbd](https://github.com/Dicklesworthstone/ntm/commit/27b99cbd), [45c06ee8](https://github.com/Dicklesworthstone/ntm/commit/45c06ee8)): `swarm_spawn` jobs accept `prompt`, `stagger_mode` and `stagger_delay`, plus `with_cass` and `with_memory` for `assign_work` jobs, resolved from the launch project's policy. See `docs/jobs-swarm-delivery.md`.
- **Restart with omitted launch environment** ([9b989d87](https://github.com/Dicklesworthstone/ntm/commit/9b989d87), [ec86acbf](https://github.com/Dicklesworthstone/ntm/commit/ec86acbf), [ed3e3078](https://github.com/Dicklesworthstone/ntm/commit/ed3e3078), [3c86934b](https://github.com/Dicklesworthstone/ntm/commit/3c86934b)) (refs GH #367): `ntm respawn` and `--robot-restart-pane` accept `--restart-env-file` for exactly one selected pane. The file is a private JSON object naming the variables that pane's saved launch left out. Values reach the agent through a private one-shot FIFO, and never through pane input, arguments, diagnostics or saved metadata. `--panes` accepts `N`, `W.P` and `%N`, and `--dry-run` runs the same preflight as a real restart.
- **Reservation warnings on routing** ([efe8f3d7](https://github.com/Dicklesworthstone/ntm/commit/efe8f3d7)): `--robot-route` and `ntm send --smart` report `recommendation.reservation_warning`, naming paths and holders, when the pick would edit files another agent reserves. Selection does not change.
- **Summaries keep Git evidence** ([09064937](https://github.com/Dicklesworthstone/ntm/commit/09064937), [6417a459](https://github.com/Dicklesworthstone/ntm/commit/6417a459)): `ntm summary` JSON gains a `git` object with `clean`, `dirty`, `conflicted` or `unavailable`. An unresolved conflict blocks handoff completion, and dirty or unavailable state makes a handoff partial. A cancelled operation no longer publishes a late summary. See `docs/summary-git-evidence.md`.
- **Monitor archive rotation** ([524d9a1f](https://github.com/Dicklesworthstone/ntm/commit/524d9a1f)) (GH #369): the session monitor rotates `~/.ntm/archive/` chunks at 64 MiB or a local date boundary and logs `archive file rotated` with `closed_path`, which an external retention job can then move or compress. `ntm summary` reads every chunk, including older daily archives. ntm does not delete archives.

### Fixed

- **An ntm read of a tracker found by walking up out of the process's own repository no longer rewrites it** (GH #373). `br` 0.7.6 can rebuild an older-schema database on a plain read, which moved its events into `.br_recovery`. Such reads now carry `--no-auto-import --no-auto-flush`. The line is drawn with stat calls: the tracker counts as the project's own only if no directory from the start up to, but not including, the tracker root has a `.git` entry, and one exists at or above the root. A `.git` file counts like a directory. So a read from a repo subdirectory such as `repo/src` still imports a newer `issues.jsonl`. It also upgrades an older tracker just as a read from the tracker's directory does, with events going to `.br_recovery`; that is tracked in beads_rust #585 and stays until that fix ships. A clone, worktree or submodule under a parent's tracker, and a tree with no repository at all, are read-only. `br info` is never used on these read paths, because it rewrites even with the flags.
- **ntm's own `.ntm/` directory no longer blocks a worktree merge.** The merge checks new in this release counted its untracked files, so a project that had not ignored `.ntm/` could never merge back.
- **`ntm serve` keeps redacted JSON responses valid.** The redaction library replaced a whole `"key":"value"` pair with a bare placeholder, which broke the JSON, and it also replaced the `receipt_token` that pipeline recovery callers must echo back. JSON responses now get `"key":"[REDACTED:...]"`. `receipt_token` stays exact only on `/api/v1/pipelines/<id>/recover`, only for a 64-character lowercase hex value, on that route's responses including an `Idempotency-Key` replay; the same key on any other route is still redacted.
- **A conflict reported on an unverified reservation receipt now raises the conflict callback.** The receipt checks new in this release dropped it silently. The unverified grant still stays out of the owned set.
- **False "Agent crashed" events** ([5470e59b](https://github.com/Dicklesworthstone/ntm/commit/5470e59b)) (GH #359): a pane created with the agent as its own command was reported as exited whenever it had no child process, which is normal between tool calls. A live non-shell root process now counts as the agent, and `pane_dead` always counts as exited even if its old PID was reused. `resilience.auto_restart` only types a launch line at an idle shell, so it cannot land in a live composer. Repeated `agent.error` events share one attention item per session, pane and agent type, and operator state carries over.
- **`ntm assign --watch` exited when work changed during a scan** ([cab19ba2](https://github.com/Dicklesworthstone/ntm/commit/cab19ba2), [a0a643db](https://github.com/Dicklesworthstone/ntm/commit/a0a643db)) (GH #370): a tracker read racing agents' closes and commits ended the watch and left agents idle. Watch now dispatches nothing from that read and verifies again on the next tick. Invalid input, unverified labels and other unverified plans still end it.
- **Reservation discovery produced patterns Agent Mail refuses** ([8dd21015](https://github.com/Dicklesworthstone/ntm/commit/8dd21015), [8493f70a](https://github.com/Dicklesworthstone/ntm/commit/8493f70a)) (GH #371): one absolute path, `..`, or bare or very short wildcard in a bead description failed the whole reservation request, so the bead was never dispatched. Those candidates are dropped. A bead left with no paths is refused unclaimed.
- **Corrupt Beads database during assignment** ([5e00a94c](https://github.com/Dicklesworthstone/ntm/commit/5e00a94c), [3ab8c4cc](https://github.com/Dicklesworthstone/ntm/commit/3ab8c4cc), [a4cbd396](https://github.com/Dicklesworthstone/ntm/commit/a4cbd396)) (GH #365): assignment, claim and release paths can recover a corrupt local index once, falling back to `br doctor --repair` only after checking that tombstones are preserved, and they recheck those tombstones afterwards. Unreadable indexes, invalid JSONL and failed verification stop assignment. Previews and dry runs never request a repair.
- **Assignment ownership** ([57d8d8a2](https://github.com/Dicklesworthstone/ntm/commit/57d8d8a2), [b275291c](https://github.com/Dicklesworthstone/ntm/commit/b275291c), [173aaaf0](https://github.com/Dicklesworthstone/ntm/commit/173aaaf0), [2553a240](https://github.com/Dicklesworthstone/ntm/commit/2553a240), [ea8db883](https://github.com/Dicklesworthstone/ntm/commit/ea8db883)): delivered work is recovered from a dead pane kept by `remain-on-exit`. Stranded dispatch recovery requires fresh proof that no live owner remains. Old failure text in a pane can no longer end a live assignment. Completion and guarded claims are bound to the selected project, so a foreign bead with the same ID cannot clear an assignment. A proven no-actuation refusal keeps its claim and leases for a same-key robot retry.
- **Plugin agents were treated as unknown** ([8e8cd388](https://github.com/Dicklesworthstone/ntm/commit/8e8cd388)) (GH #362): the robot pattern library, live-busy guard, health gate check and CLI classifier now use a registered plugin's readiness patterns, so `ntm activity`, summary, errors, `--verify-boot` and init-prompt delivery include those panes.
- **Composer and readiness detection** ([0122fa41](https://github.com/Dicklesworthstone/ntm/commit/0122fa41), [4cd48969](https://github.com/Dicklesworthstone/ntm/commit/4cd48969), [3445ce31](https://github.com/Dicklesworthstone/ntm/commit/3445ce31), [d17a7330](https://github.com/Dicklesworthstone/ntm/commit/d17a7330), [2e6b3ade](https://github.com/Dicklesworthstone/ntm/commit/2e6b3ade)) (refs GH #342, #344, #345): an idle Antigravity pane stays dispatchable beside busy siblings. omp shortcut hints are no longer read as draft text. Clearing a Codex composer no longer opens transcript rewind, and prompts are refused while Codex is browsing its transcript. Codex and Antigravity live turns are protected for every consumer. A wrapped Claude or Grok draft no longer looks submitted because its first bytes do not fit on the marker row.
- **Window-local pane indices** ([cc8a3054](https://github.com/Dicklesworthstone/ntm/commit/cc8a3054), [7fc22140](https://github.com/Dicklesworthstone/ntm/commit/7fc22140), [252531c7](https://github.com/Dicklesworthstone/ntm/commit/252531c7)): context usage, routing exclusions, dispatch hints, smart-send filters and recovery hints now use the physical pane ID, so two windows sharing an index no longer borrow each other's readings or exclusions.
- **Restart in the wrong place or on the wrong process** ([0586eca9](https://github.com/Dicklesworthstone/ntm/commit/0586eca9), [92bedb5f](https://github.com/Dicklesworthstone/ntm/commit/92bedb5f)) (refs GH #367): restart carries the confirmed pane through to execution and fences respawn, launch, delivery and rescue keys against a changed process. Trimmed tmux output could collapse `project` and `project-space`, so recovery launched in the wrong directory; the exact working directory now reaches the launch.
- **Account rotation** ([94da6cba](https://github.com/Dicklesworthstone/ntm/commit/94da6cba), [367b5319](https://github.com/Dicklesworthstone/ntm/commit/367b5319), [41226a5f](https://github.com/Dicklesworthstone/ntm/commit/41226a5f), [bf1c6445](https://github.com/Dicklesworthstone/ntm/commit/bf1c6445)) (refs GH #356, #357): ntm's own generated policy, rch and dcg settings are trusted during global credential proof, while custom settings are still rejected. Swarm monitors use a pane-qualified tmux target and wait for pane startup. The original session is rechecked around credential activation. `forceLoginMethod=claudeai` is accepted, and `forceLoginGatewayUrl` is rejected.
- **Pipeline process cleanup** ([c3126b0c](https://github.com/Dicklesworthstone/ntm/commit/c3126b0c), [9c84db19](https://github.com/Dicklesworthstone/ntm/commit/9c84db19), [4ad14d0b](https://github.com/Dicklesworthstone/ntm/commit/4ad14d0b), [ae7ddf68](https://github.com/Dicklesworthstone/ntm/commit/ae7ddf68)) (refs GH #339): on macOS and other non-Linux Unix systems, a command step now observes its whole process group while the unreaped leader pins the ID, instead of following the leader alone. On Linux, the descendant kill sweep signals through pidfd, so a stale procfs entry cannot redirect SIGKILL to a recycled PID, and a command that cannot be terminated returns an unresolved outcome instead of hanging cancellation.
- **Pipeline resume** ([8049c84b](https://github.com/Dicklesworthstone/ntm/commit/8049c84b), [7a987c70](https://github.com/Dicklesworthstone/ntm/commit/7a987c70), [6069466c](https://github.com/Dicklesworthstone/ntm/commit/6069466c)) (GH #363): resumed workflows now run their post hooks and output checks. An in-flight result no longer counts as a completed dependency. A failed step keeps its captured output when the launch journal save fails.
- **REST jobs** ([06fbb6ab](https://github.com/Dicklesworthstone/ntm/commit/06fbb6ab), [2bbcd7cd](https://github.com/Dicklesworthstone/ntm/commit/2bbcd7cd), [18d6049f](https://github.com/Dicklesworthstone/ntm/commit/18d6049f)): a pipeline job saves its run identity before the engine starts and refuses to run if that fails. Large integers in workflow variables keep their exact value through dispatch. Replay now hashes the whole bounded request rather than its first 8 MiB, and a cached 202 keeps its `Location` header.
- **Context accounting** ([f0336082](https://github.com/Dicklesworthstone/ntm/commit/f0336082), [3f261bcd](https://github.com/Dicklesworthstone/ntm/commit/3f261bcd), [916894c2](https://github.com/Dicklesworthstone/ntm/commit/916894c2)): the Claude, Codex and OMP transcript reader recovers records larger than the old 256 KiB tail, reading at most 8 MiB, and dates a reading from its own record. Compaction, rollback and branch-summary markers invalidate earlier readings until a new measurement arrives. Rotation accounting checks peers across the whole tmux server instead of one session, and refuses local files for remote panes.
- **Snapshots lost attributed context** ([0bbb5994](https://github.com/Dicklesworthstone/ntm/commit/0bbb5994), [0b284fc1](https://github.com/Dicklesworthstone/ntm/commit/0b284fc1)): transcript and gauge readings now survive collection, persistence and terse output, a measured zero stays distinct from unknown, and incremental snapshots keep event details and supported follow-up actions.
- **Context injection** ([0439c259](https://github.com/Dicklesworthstone/ntm/commit/0439c259), [b1e4df5e](https://github.com/Dicklesworthstone/ntm/commit/b1e4df5e)): respawn, rotation or an exited agent stops further input, so Enter cannot reach a replacement process. Positive message budgets now count headers, separators and the truncation marker, and zero still means unlimited.
- **Events** ([3fc42ec3](https://github.com/Dicklesworthstone/ntm/commit/3fc42ec3), [a7cacf58](https://github.com/Dicklesworthstone/ntm/commit/a7cacf58), [357abec3](https://github.com/Dicklesworthstone/ntm/commit/357abec3)): shutdown drains every accepted publication, including under continuous traffic. The event log applies session and global privacy policy at the write itself, so direct callers and sessionless events cannot bypass it.
- **CASS enrichment for sends and assignments** ([1785e02d](https://github.com/Dicklesworthstone/ntm/commit/1785e02d), [08796c06](https://github.com/Dicklesworthstone/ntm/commit/08796c06), [10c9f9a6](https://github.com/Dicklesworthstone/ntm/commit/10c9f9a6), [ed5b679d](https://github.com/Dicklesworthstone/ntm/commit/ed5b679d), [debba028](https://github.com/Dicklesworthstone/ntm/commit/debba028), [708b4cb1](https://github.com/Dicklesworthstone/ntm/commit/708b4cb1), [9ba0d32c](https://github.com/Dicklesworthstone/ntm/commit/9ba0d32c)): history now searches the target project as well as globally, so a relevant session outside the global result limit can be found. Indexed workspace and timestamp data override filename guesses, and snippet-only hits contribute. A send skips retrieval when its fullest selected pane reaches the threshold. Caller cancellation stops the search, and captured output is bounded.
- **Session restore** ([7661d0f3](https://github.com/Dicklesworthstone/ntm/commit/7661d0f3)): large multi-window sessions and checkpoints restore without split failures.
- **`ntm deps`** ([3be5205a](https://github.com/Dicklesworthstone/ntm/commit/3be5205a)) (GH #352): `--json` now lists br, bv, caam, caut, dcg, ubs, cass, cm and pt as optional tools, and each row gains `category`. Text output no longer labels `br` as "bd".
- **Unknown-flag hints** ([526f243b](https://github.com/Dicklesworthstone/ntm/commit/526f243b), [4a556baf](https://github.com/Dicklesworthstone/ntm/commit/4a556baf)) (GH #351): JSON hints are scoped to the failing command and never suggest the flag just rejected.
- **Installer** ([101c8550](https://github.com/Dicklesworthstone/ntm/commit/101c8550)) (refs GH #349): `install.sh` reads the top-level `tag_name` from compact release metadata without corrupting the tag.
- `ntm coordinator enable conflict-notify` and the enable and disable help said session monitors apply it. They now say only `ntm coordinator run` does ([9d5ed977](https://github.com/Dicklesworthstone/ntm/commit/9d5ed977)).

### Changed

- **Detached sessions start at 320x100** ([41774f0b](https://github.com/Dicklesworthstone/ntm/commit/41774f0b)) (refs GH #343): CLI, robot and server callers get usable panes and startup dialogs before the first client attaches. Only the new-session size is set, so tmux's window-size policy and later client resizing are unchanged.
- **`--clear-input` uses agent-specific keys** ([3445ce31](https://github.com/Dicklesworthstone/ntm/commit/3445ce31)) (refs GH #345): Codex drafts are cleared with C-e and C-u, and no keys are sent when the composer is already empty or unavailable.

### Performance

- **Pipeline capture on repetitive output** ([78411e97](https://github.com/Dicklesworthstone/ntm/commit/78411e97)): overlap extraction was quadratic on a near-identical terminal repaint and now does linear work. In a synthetic benchmark of the helper alone, the median on 128 KiB of repetitive input went from 159 ms to 0.797 ms; this is not an end-to-end pipeline measurement.

### Security

- **Go 1.26.9 → 1.26.10** for GO-2026-6809. The builder image `golang` is pinned by digest to the 1.26.10 image (`golang:1.26.10-alpine3.24`).
- **`golang.org/x/net` v0.60.0 → v0.62.0** fixes GO-2026-6866 and GO-2026-6860. Both are linked in through the dashboard's HTML rendering, with no call path from ntm's code according to govulncheck's source scan.
- **Credential detection in JSON and logs** ([cfa94379](https://github.com/Dicklesworthstone/ntm/commit/cfa94379), [16c8520c](https://github.com/Dicklesworthstone/ntm/commit/16c8520c), [2c9a215e](https://github.com/Dicklesworthstone/ntm/commit/2c9a215e), [1d956a88](https://github.com/Dicklesworthstone/ntm/commit/1d956a88), [f5254755](https://github.com/Dicklesworthstone/ntm/commit/f5254755), [0ea3fd92](https://github.com/Dicklesworthstone/ntm/commit/0ea3fd92)) (GH #372): the shared detector now finds quoted JSON credentials, encoded property names and assignments inside JSON log messages, complete records beside malformed neighbors, and credentials in truncated JSON. It redacts whole PEM private-key blocks, and bytes a provider match would have left behind. `ntm scrub` previews are built from full-input findings, so a cropped preview cannot expose part of a neighboring secret.
- **False positives and allowlists** ([41a632ae](https://github.com/Dicklesworthstone/ntm/commit/41a632ae), [29b8f7fd](https://github.com/Dicklesworthstone/ntm/commit/29b8f7fd)) (GH #353, #368): the PASSWORD rule no longer matches `PWD=` and `OLDPWD=` paths or placeholders such as `<redacted>`. `NTM_REDACTION_ALLOWLIST` now replaces the TOML allowlist on every config-loaded surface, and an explicitly empty value clears it.
- **CASS source labels** ([fed08c91](https://github.com/Dicklesworthstone/ntm/commit/fed08c91)): transcript filenames are scanned in full before being shortened for injected headings and send receipts, so a credential in a filename is not shown.
- **Claude pane isolation** ([a2e85938](https://github.com/Dicklesworthstone/ntm/commit/a2e85938), [a0b9802b](https://github.com/Dicklesworthstone/ntm/commit/a0b9802b), [04b321c2](https://github.com/Dicklesworthstone/ntm/commit/04b321c2), [5523c1a1](https://github.com/Dicklesworthstone/ntm/commit/5523c1a1), [232f6ab8](https://github.com/Dicklesworthstone/ntm/commit/232f6ab8)) (GH #366): backups and other copies beside `.credentials.json` were linked into every pane and reported as isolated, exposing the same refresh token. They are now excluded, and the isolation check reports them. A relative `HOME` no longer leaves a pane with broken links and no settings. See Upgrade notes.
- **Remote requests controlled local processes** ([e011adf6](https://github.com/Dicklesworthstone/ntm/commit/e011adf6)): remote `--robot-kill-agent` resolved PIDs over SSH and signaled those numbers on the local host, and smart restart could act on a same-named local session. See Upgrade notes.

### Known issues

- **New in this release: in redact mode, a JSON request carrying a credential in a field is rejected as invalid JSON.** `ntm serve` replaces the whole `"key":"value"` pair with a bare placeholder, which breaks the body. On v1.37.2 the same request reached the handler unchanged, because quoted JSON credentials were not detected. The fix in this release covers responses only. Keeping a request valid would let a handler store the placeholder as if it were the real value, so the request fails instead.
- **25 tests fail in a full sandboxed run of this release.** No test was changed or skipped to hide this.
  - 16 check behaviour that changed on purpose, or in one case were never right (GH #374). In-flight merge cancellation stays untested until its fixture is repaired, and that issue lists two more points to read first.
  - 2 catch small defects in newer code (GH #375): `TestRemoteProcessOperationsCLIRefuseBeforePaneObservation` and `TestWaitCommandJSONPreservesFailureCodes`. A remote `--robot-*` process request is refused correctly, but only after observing panes over SSH, and `ntm wait --type=nonsense` reports `PANE_NOT_FOUND` instead of `INVALID_FLAG`.
  - 2 need `SHELL` set in the environment and pass with `SHELL=/bin/bash`: `TestRealGuardedSendRechecksPIDBeforeEnter` and `TestRealPaneMutationGuardProtectsSharedSendAndRescueKeys`.
  - 5 fail on v1.37.2 as well: `TestRestoreLifecycleReportsOnlyCompletedContextDeliveries` (GH #340), `TestRunGitCommandReportsTimeoutWhileReadingOutput`, `TestRequestMirrorsToInstalledSLB`, `TestRobotSLBReviewsThroughInstalledSLB` and `TestGetEnv_GlobalMode`.
- **One more test fails intermittently (GH #376).** Run alone, `TestRESTBackgroundPipelineFreshServerCancelsActualWorker` failed 4 of 200 times on this release and 3 of 200 times on v1.37.2. All 7 failures are the same `TempDir` cleanup error, raised when the detached worker is still writing as the test ends, and no assertion failed.
- **The Go module still carries two tracked executables (GH #346):** the 59.8 MB Linux binary `cmd/ntm/ntm` and the 2.8 MB `fakeshell`. They are the same files as in v1.37.2. They make the module download larger and play no part in the built binary.

## [v1.37.2] -- 2026-10-14 [GitHub Release]

**Antigravity (agy) agents now run Gemini 4 Argon (High).** Otherwise identical to v1.37.1: same source, Go 1.26.9 and `golang.org/x/net` v0.60.0.

### Changed

- **Antigravity default model is Gemini 4 Argon (High)** (`gemini-4-argon`), replacing Gemini 3.8 Flash (High). The required-model pin, the session-resume command, the model registry (1M-token context), the flag help and both spawn wizards move together, matching ACFS's `agy-locked` launcher. Argon accepts no `--effort` value, so the pin names the model only.

### Not in this release

As with v1.37.1, the work on `main` since v1.37.0 (pipeline branch checkpoints, webhook outbox persistence, checkpoint binary/untracked capture, reservation watcher, Gemini readiness and context metrics, and more) is not included. It ships in the next minor release once it is qualified.

## [v1.37.1] -- 2026-10-14 [GitHub Release]

**Security patch: Go 1.26.9 and `golang.org/x/net` v0.60.0** ([#347](https://github.com/Dicklesworthstone/ntm/issues/347)). The source is v1.37.0's, unchanged. Only the toolchain, the HTTP/2 module, the Docker builder image and the README badge move.

### Security

- **Go 1.26.8 → 1.26.9.** On 1.26.8, `govulncheck` reports that ntm's code reaches 10 Go standard-library vulnerabilities: GO-2026-6603, 6604, 6605, 6607, 6608, 6610, 6611, 6612, 6613 and 6617, several of them in `net/http` and HTTP/2, which ntm's HTTPS download and upgrade paths use. On 1.26.9 with `x/net` v0.60.0 it reports none.
- **`golang.org/x/net` v0.59.0 → v0.60.0**, the fixed version for the HTTP/2 records.
- **Docker builder image** `golang:1.26.9-alpine3.24`, pinned by digest.

### Not in this release

The work on `main` since v1.37.0 (pipeline branch checkpoints, webhook outbox persistence, checkpoint binary/untracked capture, reservation watcher, Gemini readiness and context metrics, and more) is not included. It ships in the next minor release once it is qualified.

## [v1.37.0] -- 2026-10-07 [GitHub Release]

**Spawns can enforce per-agent-type fleet limits that hold across processes, Claude agents launched by ntm now carry the safety policy hook, robot work reads are verified against the tracker, and ntm's integrations with bv, br, cass, cm, caut, caam, ubs, xf, ms, jfp, slb, ru, rano, pt and dcg now read those tools' real output.** Also fixes GitHub issues [#283](https://github.com/Dicklesworthstone/ntm/issues/283), [#335](https://github.com/Dicklesworthstone/ntm/issues/335), [#336](https://github.com/Dicklesworthstone/ntm/issues/336), [#337](https://github.com/Dicklesworthstone/ntm/issues/337) and [#338](https://github.com/Dicklesworthstone/ntm/issues/338), and wires up several features that were configured or documented but never ran, among them `ntm spawn --privacy`, session timelines, `ntm level`, context warnings, compaction recovery and the session coordinator.

### Upgrade notes

- **`--robot-jfp-install` and `--robot-jfp-installed` are retired.** jfp 1.0.x moved skill management to jsm and answers `deprecated_command`, so both flags could only return `NOT_IMPLEMENTED` while `--robot-capabilities` advertised them. They are removed together with `--jfp-project`, their schema, pagination and capability entries; passing one now fails with `INVALID_FLAG` (unknown flag). `--robot-jfp-status` and the prompt list, search, show, suggest and export surfaces are unchanged ([83c391ec](https://github.com/Dicklesworthstone/ntm/commit/83c391ec)).
- **Config keys that never had an effect now fail to load.** `[context_rotation] enabled`, `warning_threshold`, `min_session_age_sec`, `require_confirm` and `default_confirm_action`, and `[agents.plugins]`, had no reader (the live rotation trigger is `[rotation] usage_percent_threshold`). A config that still sets one fails to load with a line per key; `ntm doctor` lists them and `ntm config migrate` deletes them, keeping a backup. Pending context rotations are manual-confirm unless `[rotation] auto_confirm` is set: `[context_rotation] confirm_timeout_sec` is now only how long a request stays open, its default rises from 60 to 600 so an unattended request is not re-announced every minute, and 0 means the default rather than "no auto-rotate" ([ef529fec](https://github.com/Dicklesworthstone/ntm/commit/ef529fec), [e209108f](https://github.com/Dicklesworthstone/ntm/commit/e209108f)).
- **Schema migrations `025_send_operations_kind` and `026_send_operations_claim_fence`** apply on first run. They add columns to the durable send-operation table. Rows left `in_progress` by an earlier version are marked as started, so retrying one of those operation IDs returns `OPERATION_OUTCOME_UNKNOWN` instead of delivering again; completed receipts are unchanged ([c631a703](https://github.com/Dicklesworthstone/ntm/commit/c631a703), [209446ea](https://github.com/Dicklesworthstone/ntm/commit/209446ea)).
- **Spawn admission is serialized across local processes and now covers `ntm add`.** With the default `[spawn_pacing]` settings (enabled, positive `agent_caps`), every non-dry-run `--robot-spawn`, REST spawn, `swarm_spawn` job, `ntm add` and `ntm scale` scale-up evaluates the fleet while holding a per-user lock (`ntm/spawn-admission/fleet.lock` under the user cache directory) and waits up to 30 seconds for it; a lock still busy after that returns `RESOURCE_BUSY` with reason `spawn_admission_busy`. `ntm add`, scale-up and the dashboard's add action are now checked against the same summed `agent_caps` budget and pressure checks as robot spawn. A tmux inventory that cannot be read defers the launch (`agent_inventory_unavailable`) instead of counting as an empty fleet. Count-capped launches against a remote tmux server (`--ssh`) or on platforms other than Linux and macOS are refused (`spawn_admission_unavailable`) rather than run without the lock: run the command on the target host, or set `[spawn_pacing] enabled = false` to keep the previous unchecked behavior ([e42387c3](https://github.com/Dicklesworthstone/ntm/commit/e42387c3), [ba1d701b](https://github.com/Dicklesworthstone/ntm/commit/ba1d701b), [fa5a66b9](https://github.com/Dicklesworthstone/ntm/commit/fa5a66b9)).
- **`ntm serve`, `ntm serve --web` and `ntm web` apply the selected config to API spawns.** REST spawn and `swarm_spawn` jobs called the spawn engine with no configuration, which bypassed configured agent commands, model defaults and fleet limits. They now load the file selected by `--config` / `NTM_CONFIG`, plus the launch project's overlay, strictly at execution; a missing explicitly selected file or an invalid config fails with `INVALID_FLAG` before any pane is created. Code that embeds `serve.New` must call `ConfigureSpawnPolicy` to get this behavior ([77b4e56d](https://github.com/Dicklesworthstone/ntm/commit/77b4e56d)).
- **Claude agents launched by ntm now get a `PreToolUse` safety hook through `--settings`** (`[safety] claude_policy_hook`, default true), plus the dcg and rch hooks when those integrations are enabled and installed. A custom `[agents] claude` command whose last shell command is not the `claude` invocation launches without the hooks and logs a warning naming the two fixes: `--settings {{shellQuote .ClaudeSettings}}` in the template, or `claude_policy_hook = false`. If you ran `ntm safety install` before, run `ntm safety install --force`: the old script was never registered with Claude Code, and the new scripts delegate to `ntm safety check --hook` and are registered in `~/.claude/settings.json`. `ntm safety status` reports an old install as "Script present but not registered" ([85b8a777](https://github.com/Dicklesworthstone/ntm/commit/85b8a777), [3d64eea1](https://github.com/Dicklesworthstone/ntm/commit/3d64eea1), [6b9b6512](https://github.com/Dicklesworthstone/ntm/commit/6b9b6512)).
- **The pre-commit hook `ntm init` installs now enforces Agent Mail file reservations.** It runs `ntm guards check --staged`, which refuses a commit touching files another agent holds exclusively and names the holder; if Agent Mail is unreachable it warns and allows the commit unless `NTM_GUARD_STRICT=1`. Existing managed hooks are not changed automatically: run `ntm guards install` to regenerate one in place (it keeps the beads sync and UBS steps instead of refusing or replacing the hook). The guard also reaches a token-protected or non-default Agent Mail server configured in `[agent_mail]`, where it previously failed open on every commit ([67f13b56](https://github.com/Dicklesworthstone/ntm/commit/67f13b56), [c0a50fb8](https://github.com/Dicklesworthstone/ntm/commit/c0a50fb8)).
- **Every spawned session now runs the session coordinator inside its monitor.** Assignment maintenance (renewing the leases of delivered assignments, releasing leases and claims when the bead closes, recovering work from lost panes) runs every 30s, so a one-shot `ntm assign` no longer loses its file lock after an hour. Features enabled with `ntm coordinator enable` (digest, conflict negotiation), mail nudges, `[rotation] usage_percent_threshold` and `[integrations.caam] auto_failover` now act in every session without `ntm coordinator run`, and config is re-read every 15s. **`[coordinator] auto_assign = true` now runs in every session monitor, not only under `ntm coordinator run`**: idle agents in every spawned session get ready beads assigned automatically, subject to the assignment safety policy. Remove the key or run `ntm coordinator disable auto-assign` if you set it for one-off `coordinator run` use. `[coordinator] conflict_notify` now defaults to false, since it mails every holder of overlapping reservations project-wide at high importance, and the session monitor never runs it even when it is set: config files written by `ntm config init`, `config edit` or `config reset` up to v1.36.1 contain `conflict_notify = true`, which cannot be told apart from an opt-in, so conflict notification stays where it was, under `ntm coordinator run`, and `ntm coordinator status` says so. `ntm coordinator run` and a reservation-maintaining `ntm assign --watch` take the session over while they run; `ntm coordinator status` gains a Runtime section ([75d083b7](https://github.com/Dicklesworthstone/ntm/commit/75d083b7), [79646496](https://github.com/Dicklesworthstone/ntm/commit/79646496)).
- **`--robot-bulk-assign` reserves each bead's files by default, like `ntm assign`.** The new `--reserve-files` defaults to true: explicit `--reservation-paths` win, otherwise each bead's paths are discovered from its title and description before the claim. A bead that names no files, or an unreachable Agent Mail, refuses that assignment unclaimed with `RESERVATION_REQUIRED`. Pass `--reserve-files=false` to opt out; combining it with `--reservation-paths` or `--require-reservation` is `INVALID_FLAG` ([38f24ba2](https://github.com/Dicklesworthstone/ntm/commit/38f24ba2)).
- **Pipeline resume no longer reruns a command step interrupted by a worker crash.** Command launches are journaled before the shell starts. On resume, a command whose process group may still be running or may already have had side effects stops the resume with a message naming the step and recorded process ID; after inspecting it and stopping any surviving processes, use `--mode=restart-failed` (or `--keep-state=false` for the whole workflow) to repeat it deliberately. Runs saved by an earlier version with an unfinished command stop the same way instead of rerunning it ([0023dfd7](https://github.com/Dicklesworthstone/ntm/commit/0023dfd7)).
- **`ntm workflow run` now acts on `on_agent_crash` and `on_agent_error`.** Nothing raised those faults before and `restart_agent` did nothing. The runner now checks every workflow pane on each poll (pane gone or dead, agent CLI exited to its shell, rate limit, auth error, blocking gate) and runs the configured action; unset values default to `notify`, and the built-in templates set them (specialist-team restarts and pauses, red-green and review-pipeline pause, parallel-explore notifies). Restarts go through the `ntm respawn` engine, re-send the current stage prompt when the pane had already received it, and are capped per agent per run by `[resilience] max_restarts`, so a crash loop stops the run. `on_timeout = restart_agent` is now rejected at validation ([d18d55a2](https://github.com/Dicklesworthstone/ntm/commit/d18d55a2)).
- **Robot error codes and REST statuses.** A retryable work-source change on `--robot-assign`, `--robot-bulk-assign` and `--robot-spawn --spawn-assign-work` is `STALE_WORK_COORDINATION` (REST 409) instead of `INTERNAL_ERROR` (GH #283). A project with no `.beads` returns `DEPENDENCY_MISSING` with a `br init` hint from the bv-backed robot analyses and 404 `BEADS_NOT_INITIALIZED` from REST beads endpoints; an unindexed cass returns `DEPENDENCY_MISSING` from the robot cass surfaces and 503 `CASS_UNAVAILABLE` from REST. `--robot-mail` now fails (exit 1; `PERMISSION_DENIED` for a rejected token, otherwise `INTERNAL_ERROR`) when it cannot read the project's agents, instead of reporting success with an empty list. `IDEMPOTENCY_CONFLICT` and `OPERATION_IN_PROGRESS` map to REST 409, and `SENSITIVE_DATA_BLOCKED` to 403, instead of 500. New: `DESTRUCTIVE_COMMAND_BLOCKED` (REST 403) on robot and REST sends that dcg refuses, and `OPERATION_OUTCOME_UNKNOWN` (REST 409) for a retried send or interrupt whose earlier delivery may have started ([a4a655a9](https://github.com/Dicklesworthstone/ntm/commit/a4a655a9), [34b5dad2](https://github.com/Dicklesworthstone/ntm/commit/34b5dad2), [4f168194](https://github.com/Dicklesworthstone/ntm/commit/4f168194), [289cc7b3](https://github.com/Dicklesworthstone/ntm/commit/289cc7b3), [d394c3e2](https://github.com/Dicklesworthstone/ntm/commit/d394c3e2), [c631a703](https://github.com/Dicklesworthstone/ntm/commit/c631a703), [4c8832cf](https://github.com/Dicklesworthstone/ntm/commit/4c8832cf), [209446ea](https://github.com/Dicklesworthstone/ntm/commit/209446ea)).
- **Robot and REST output fields.** `--robot-agent-health` lists user shells and unattributed panes under a new `non_agent_panes` map instead of grading them; `fleet_health.total_panes` counts agent panes only, and `overall_grade` is `"N/A"` when no agent pane is graded (GH #335). `--robot-cass-insights` returns `agents[]` and `workspaces[]` buckets and drops `topics`; REST `/cass/insights` returns bucket lists, `/cass/status` reports `version` and `database_size` in place of the always-zero `index_size`, and `/cass/timeline` rejects `?workspace=` with 400. `--robot-rano-stats` drops the request and byte fields (rano never provides them), adds `database`, and returns `DEPENDENCY_MISSING` when the observer database is missing. `--robot-dcg-check` agent hints drop the never-populated `safer_alternative`. REST checkpoint verification drops `checksums_valid`, which was always true; verification details now carry `checksum_status` (`verified`, `failed` or `unavailable`) and `checksums_checked`. Pending context rotations no longer carry `default_action` in `--robot-context` or `ntm rotate context pending` ([97271c76](https://github.com/Dicklesworthstone/ntm/commit/97271c76), [9037eb9d](https://github.com/Dicklesworthstone/ntm/commit/9037eb9d), [4db8ad28](https://github.com/Dicklesworthstone/ntm/commit/4db8ad28), [9f2330f5](https://github.com/Dicklesworthstone/ntm/commit/9f2330f5), [c7f2e127](https://github.com/Dicklesworthstone/ntm/commit/c7f2e127), [0b589055](https://github.com/Dicklesworthstone/ntm/commit/0b589055), [4cff2514](https://github.com/Dicklesworthstone/ntm/commit/4cff2514), [70b51d1f](https://github.com/Dicklesworthstone/ntm/commit/70b51d1f), [88d6e9ff](https://github.com/Dicklesworthstone/ntm/commit/88d6e9ff), [e209108f](https://github.com/Dicklesworthstone/ntm/commit/e209108f)).
- **Robot flags that are now applied or rejected.** `--robot-interrupt` and `--robot-ack` honor `--type`; they interrupted or watched every agent in the session. `--robot-send` rejects the `--robot-route` modifiers (`--strategy`, `--route-strategy`, `--last-agent`, `--route-type`, `--route-exclude`) with `INVALID_FLAG` instead of sending to every matching pane. `--brief` with `--fix` or `--diagnose-pane` is `INVALID_FLAG`. `--robot-slb-deny` requires `--reason`, and slb approve and deny need a reviewer session in `SLB_SESSION_ID` / `SLB_SESSION_KEY`. `--xf-mode` takes xf's own modes (`lexical`, `semantic`, `hybrid`, `two-tier`) and `--xf-sort` its orders (`relevance`, `date`, `date-desc`, `engagement`); other values are `INVALID_FLAG`. `--spawn-preset` now runs the recipe and cannot be combined with count flags ([59c4a5cc](https://github.com/Dicklesworthstone/ntm/commit/59c4a5cc), [4cf7c91a](https://github.com/Dicklesworthstone/ntm/commit/4cf7c91a), [44e34a46](https://github.com/Dicklesworthstone/ntm/commit/44e34a46), [2d21c77c](https://github.com/Dicklesworthstone/ntm/commit/2d21c77c), [dfaf8eac](https://github.com/Dicklesworthstone/ntm/commit/dfaf8eac), [7b9eba41](https://github.com/Dicklesworthstone/ntm/commit/7b9eba41), [5230731a](https://github.com/Dicklesworthstone/ntm/commit/5230731a)).
- **`ntm send`, `ntm logs` and `ntm spawn` flag changes.** `ntm send --route` now enables smart routing; it was ignored unless `--smart` was also given, so the prompt went to every matching pane. `--smart` or `--route` combined with `--all`, `--skip-first`, `--project`, `--batch` or `--distribute` is an error, and `--route=explicit` requires `--pane`. `--template` refuses to send when a placeholder is left unfilled instead of sending literal `{{var}}` text, and cannot be combined with `--batch`, `--distribute`, `--project` or `--codex-goal`, which silently dropped it. `ntm logs --since` is removed; it never filtered anything. `ntm spawn` rejects an out-of-range `--stagger-delay` in every stagger mode ([44e34a46](https://github.com/Dicklesworthstone/ntm/commit/44e34a46), [67f13b56](https://github.com/Dicklesworthstone/ntm/commit/67f13b56), [2c902d45](https://github.com/Dicklesworthstone/ntm/commit/2c902d45), [c3b640f0](https://github.com/Dicklesworthstone/ntm/commit/c3b640f0), [d0b39b04](https://github.com/Dicklesworthstone/ntm/commit/d0b39b04)).

### Added

- **Per-agent-type fleet limits** ([e42387c3](https://github.com/Dicklesworthstone/ntm/commit/e42387c3), [ba1d701b](https://github.com/Dicklesworthstone/ntm/commit/ba1d701b), [fa5a66b9](https://github.com/Dicklesworthstone/ntm/commit/fa5a66b9), [77b4e56d](https://github.com/Dicklesworthstone/ntm/commit/77b4e56d)): the opt-in `[spawn_pacing.agent_type_limits]` table (keys `claude`, `codex`, `gemini`, `antigravity`, `grok`, `omp`, `opencode`) bounds each agent type across every session the tmux client can see, in addition to the summed `agent_caps` budget. A mixed request is evaluated as one batch before any pane is created; `admission` reports sorted `agent_type_limits` rows (running, requested, projected, limit, remaining headroom, `blocks_request`), and a type-cap refusal uses reason `agent_type_limit_exceeded`. Count-capped launches hold the cross-process admission lock from fleet inventory through the last launch, so two cooperating processes cannot spend the same headroom; `admission.serialized` says whether the decision was made under the lock. `docs/spawn-agent-limits.md` documents the scope and what the lock does not cover.
- **Recipe-backed robot spawns and readiness-gated jobs** ([5230731a](https://github.com/Dicklesworthstone/ntm/commit/5230731a), [35a2b200](https://github.com/Dicklesworthstone/ntm/commit/35a2b200), [355427be](https://github.com/Dicklesworthstone/ntm/commit/355427be)): `--robot-spawn --spawn-preset=NAME` and `swarm_spawn` jobs with `preset` expand the recipe (builtin < user < project, read from the launch directory's `.ntm/recipes.toml`) into per-instance models and efforts, render every instance before launching, and admit the expanded counts; `preset_used` names the recipe. `swarm_spawn` jobs accept `launch_ready_timeout` to start agents one at a time, each passing its readiness check before the next launches. A job with no `working_dir` uses the server's selected project, a relative one resolves against it, and queued jobs keep the absolute directory captured at admission.
- **`--robot-spawn` initial prompts and stagger pacing** ([d0b39b04](https://github.com/Dicklesworthstone/ntm/commit/d0b39b04), [116098c6](https://github.com/Dicklesworthstone/ntm/commit/116098c6)) (refs GH #251): `--spawn-prompt` / `--spawn-prompt-file` deliver a first prompt after the readiness wait through the same dispatch path as work assignment, prefixed to each work prompt with `--spawn-assign-work`. `--spawn-stagger-mode=none|fixed|smart` and `--spawn-stagger-delay` pace prompts and assignment dispatch; output gains a `stagger` plan (previewed with `--dry-run`), `prompt_deliveries[]` (`PROMPT_SEND_FAILED` on any failure) and `assignments[].delivered_at`. A new `[spawn]` table (`stagger_mode`, `stagger_delay`) sets defaults for both `ntm spawn` and `--robot-spawn`, which now share one stagger planner, and `ntm spawn --json` reports the resolved mode and interval. `--robot-spawn` also accepts `--spawn-wait` and `--spawn-assign-work` for Grok panes, which it had refused with `NOT_IMPLEMENTED`.
- **Retry-safe `--robot-interrupt` and fenced send operations** ([c631a703](https://github.com/Dicklesworthstone/ntm/commit/c631a703), [209446ea](https://github.com/Dicklesworthstone/ntm/commit/209446ea)) (refs GH #245): `--op-id`, and the REST `Idempotency-Key`, now claim a durable operation as `--robot-send` does. An identical retry replays the recorded outcome without a second Ctrl+C, a conflicting reuse is `IDEMPOTENCY_CONFLICT`, and a live claim is `OPERATION_IN_PROGRESS`; `--robot-send-receipt` returns interrupt receipts. Send and interrupt operations record their final payload digest, targets and a dispatch boundary under a rotating claim token before any input, so only claims that never started are recovered, and one that may have delivered returns `OPERATION_OUTCOME_UNKNOWN` with its receipt instead of typing again.
- **`[assign.work_source]` policy** ([3bb33ade](https://github.com/Dicklesworthstone/ntm/commit/3bb33ade), [a4a655a9](https://github.com/Dicklesworthstone/ntm/commit/a4a655a9), [34b5dad2](https://github.com/Dicklesworthstone/ntm/commit/34b5dad2), [edb1a14a](https://github.com/Dicklesworthstone/ntm/commit/edb1a14a)) (GH #283): `required_ref`, `require_clean` and `program_labels` restrict what `ntm assign`, `--robot-assign`, `--robot-bulk-assign`, `--robot-spawn --spawn-assign-work`, the coordinator and `ntm work-snapshot` treat as dispatchable. A project `.ntm/config.toml` can only tighten the user's policy. Stale-source refusals carry a `work_source_mismatch` receipt with the expected and observed tracker JSONL SHA-256 and HEAD.
- **Assignment prompts carry CASS history and CM rules** ([86372035](https://github.com/Dicklesworthstone/ntm/commit/86372035), [693f775c](https://github.com/Dicklesworthstone/ntm/commit/693f775c)): `ntm assign` (and `ntm coordinator assign`), `--robot-bulk-assign`, `--robot-spawn --spawn-assign-work` and the coordinator's auto-assign enrich each new assignment prompt the way `ntm send` does, with the same precedence (`--no-cass` > `--with-cass` > `[cass]` / `[cass.context]`; `--with-memory` or `[memory] send_injection`), querying by the bead's title, labels and description. Enrichment happens before the durable intent is recorded, so retries and recovery replay the recorded prompt without querying cass or cm again. Each assignment reports `cass_injection` and `memory_injection`; a missing or failing cass or cm never blocks an assignment.
- **Context warnings and context-pressure attention events** ([1207374b](https://github.com/Dicklesworthstone/ntm/commit/1207374b), [57305163](https://github.com/Dicklesworthstone/ntm/commit/57305163), [3673e13e](https://github.com/Dicklesworthstone/ntm/commit/3673e13e)): `alerts.context_warning_threshold` (default 75%) now raises a `context_warning` alert for each agent pane at or above it, read from the pane's own session transcript; the threshold was documented but nothing produced the alert. It appears in `--robot-alerts`, robot status, snapshot and markdown, and the dashboard, and resolves when usage drops. The session monitor also appends per-pane context-pressure events to the attention feed every minute, so the `context_hot` wait condition can fire, and the reservation and file conflict conditions now fire on the coordination problems the projection refresh reports. A pane that escalates from the warning threshold to action-required surfaces at once instead of after the ten-minute dedup window.
- **Compaction recovery runs in the session monitor** ([c5a76431](https://github.com/Dicklesworthstone/ntm/commit/c5a76431), [2b27203c](https://github.com/Dicklesworthstone/ntm/commit/2b27203c), [3b989f01](https://github.com/Dicklesworthstone/ntm/commit/3b989f01)): automatic recovery after an agent compacts its context ran only while a dashboard was open. The resident session monitor now detects fresh provider completion banners and sends the recovery prompt under the `[context_rotation.recovery]` policy (enabled, prompt, cooldown, attempt limit), rechecking the pane's process and identity and waiting for fresh idle evidence before delivery, and never replaying an uncertain write. The dashboard only displays compactions; its "recovering" indicators, which could never show, are removed.
- **SLB decisions flow back into ntm approvals** ([04ac1fb9](https://github.com/Dicklesworthstone/ntm/commit/04ac1fb9)): an approval mirrored into slb is approved or denied when a reviewer decides it in slb (approver `slb:<reviewer>`, the reviewer's comments as the deny reason), through the same two-person and approver-list checks as `ntm approve`. `ntm approve`, REST `/safety` approvals and the force-release gate all see the decision.
- **Checkpoint artifact integrity** ([a6d866cf](https://github.com/Dicklesworthstone/ntm/commit/a6d866cf), [88d6e9ff](https://github.com/Dicklesworthstone/ntm/commit/88d6e9ff), [10ad3518](https://github.com/Dicklesworthstone/ntm/commit/10ad3518), [70b51d1f](https://github.com/Dicklesworthstone/ntm/commit/70b51d1f)): checkpoints record a SHA-256 and byte count for scrollback, git patches and git status before their metadata is written, and a resave keeps the original fingerprints. `ntm checkpoint verify` and REST verification check them; restore and import refuse a corrupted payload before replacing a session, launching agents or injecting context, even with force or overwrite; export verifies its source bytes and writes a manifest for what it exports. Checkpoints from earlier versions still load, with an integrity-unavailable warning.
- **`ntm agents stats` and `ntm agents recommend` use recorded outcomes** ([104ba9a3](https://github.com/Dicklesworthstone/ntm/commit/104ba9a3)): completed and failed assignments are summarized per agent type across sessions, including assignments already cleared from the ledger, which are now archived to the session's `outcomes.jsonl`. Stats show the observed success rate, completion count and mean time, and `recommend` applies the observed rate instead of a seeded prior. Agent types with no outcomes print as unmeasured.
- **`affinity` routing strategy** ([44e34a46](https://github.com/Dicklesworthstone/ntm/commit/44e34a46), [07e130d5](https://github.com/Dicklesworthstone/ntm/commit/07e130d5)): `ntm send --route=affinity` and `--robot-route --strategy=affinity` pick the available agent holding live Agent Mail reservations on the largest share of files named in the prompt, and fall back to least-loaded (reported as `fallback_used`) when no agent holds any. `--robot-route` accepts `--msg` / `--msg-file`, required for affinity, and shows `affinity_match` per candidate. Pipeline `route:` steps accept every router strategy except `explicit`, not only least-loaded, first-available and round-robin.
- **`ntm doctor` reports Claude hook coverage** ([116098c6](https://github.com/Dicklesworthstone/ntm/commit/116098c6)): Safety Defaults gains "Claude agent hooks", listing the `PreToolUse` hooks ntm-launched Claude agents carry and whether `ntm safety install` registered the policy hook, and warns when Claude agents would run unprotected. JSON adds `safety_defaults.claude_agent_hooks`, `claude_policy_hook_registered` and `claude_agent_hooks_warning`.
- **`--robot-agent-health` samples process triage without a resident monitor** ([50c26991](https://github.com/Dicklesworthstone/ntm/commit/50c26991)): a standalone call takes one bounded passive pt snapshot through the monitor's own sampler, or reuses a running monitor, and reports `pt_observation_mode` (`monitor` or `snapshot`) with explicit timeout, unavailable and empty states. Shell-only, dead and disabled selections skip pt.

### Fixed

- **User shells were graded as stalled agents** (GH #335): the tmux projection ran every pane through the agent classifier, so a plain shell that went quiet past the stall threshold became an error, a shell-only session went critical in `--robot-status`, and `--robot-agent-health` graded shells 0/F. User and unknown panes now get a neutral busy/idle/active classification and still count in session totals; session health uses agent panes as the error denominator ([97271c76](https://github.com/Dicklesworthstone/ntm/commit/97271c76), [9037eb9d](https://github.com/Dicklesworthstone/ntm/commit/9037eb9d)).
- **`ntm assign --auto` claimed beads it could not reserve** (GH #336): reservation paths came from the bead title only, so a bead that listed its files in the description was claimed, failed with "file reservation is required but no reservation paths were defined", and stayed claimed with no prompt sent. `--auto` and direct `ntm assign <bead> --pane` now resolve paths from the live title and description before claiming and refuse a bead with no paths unclaimed. When a description declares owned outputs (`## Owned outputs`, `Files to modify:`, `**Owned files**` and similar), only that section is reserved, including its sub-labels and sub-headings, so files the bead only reads do not become exclusive reservations. Path extraction accepts a Markdown backtick before a path and no longer turns `docs/notes.md` into a `docs/notes/**/*` glob ([89b32d9a](https://github.com/Dicklesworthstone/ntm/commit/89b32d9a), [15e4dd56](https://github.com/Dicklesworthstone/ntm/commit/15e4dd56)).
- **`ntm status` estimated Claude context from scrollback** (GH #338): a Claude pane's scrollback is a sliver of its session, so a six-pane swarm at 28-50% of its window read 4-6%. Status now reads each pane's own session transcript. On Linux, ntm binds a Claude process to its session through Claude Code's `~/.claude/sessions/<pid>.json`, checking the process start time, which attributes transcripts correctly when several Claude panes share a directory; previously every such pane got the newest transcript, or none. `--robot-context`, the snapshot, the coordinator's rotation trigger and native compaction use the same binding (in that layout the trigger never fired and compaction always refused). `ntm status --json` adds `context_source` (`status_bar`, `transcript` or `scrollback_estimate`), and text output marks estimates. Current Claude models (Fable 5/5.1, Mythos 5/5.1, Opus 5.5/5/4.8/4.7/4.6, Sonnet 5.5/5/4.6) are listed with a 1M window; Haiku 4.5 and Opus/Sonnet 4.0/4.5 stay at 200K ([3da30a92](https://github.com/Dicklesworthstone/ntm/commit/3da30a92), [e4d911a6](https://github.com/Dicklesworthstone/ntm/commit/e4d911a6)).
- **`--robot-snapshot` and `--robot-status` advertised blocked beads as ready** (GH #283): work rows were served on age alone, unscoped by project, and a blocked bead that triage did not list was counted as ready. Both now read work only from the source-verified observation (tracker JSONL digest, checkout HEAD, peer reservations) that the projection refresh just collected; if it no longer matches, one new observation is taken, and any other failure reports work as unavailable with its reason and zero ready. A rejected observation keeps its diagnostics without restoring readiness ([67a29ac0](https://github.com/Dicklesworthstone/ntm/commit/67a29ac0), [6034ebfd](https://github.com/Dicklesworthstone/ntm/commit/6034ebfd)).
- **Robot assignment and spawn reservations** ([38f24ba2](https://github.com/Dicklesworthstone/ntm/commit/38f24ba2), [c99121e1](https://github.com/Dicklesworthstone/ntm/commit/c99121e1), [bb92f4a2](https://github.com/Dicklesworthstone/ntm/commit/bb92f4a2)): `--robot-bulk-assign` reserved files only with `--require-reservation` plus explicit paths, so two agents given overlapping beads through the robot API could edit the same files with no lock; it now shares `ntm assign`'s discovery (see Upgrade notes) and reports a `reservation` object per assignment. `--robot-spawn --spawn-assign-work --require-reservation` without `--reservation-paths` discovers the bead's paths instead of failing. Robot conflict reports and work-coordination `reservation_conflict` problems compare glob languages the way the coordinator does, so `src/*/main.go` and `src/service/*.go` are reported as overlapping and `src/*.go` no longer matches `src/deep/main.go`.
- **Claude turn detection** ([6eb7a776](https://github.com/Dicklesworthstone/ntm/commit/6eb7a776), [482e4df6](https://github.com/Dicklesworthstone/ntm/commit/482e4df6)): Claude Code now ends a turn with a suffixed line (`✻ Crunched for 10s · done 11:09 PM · 1 shell still running`) that ntm did not recognize, so idle panes read as working and a failed tool call above the line made a completed turn an error. Inline tool output (`⎿  Error: Exit code 144`) no longer raises `agent_error` alerts or `ERROR` in `--robot-activity`, and idle Claude panes with queued text, only the footer, or a stale spinner classify as `WAITING`, matching `--robot-is-working`.
- **Claude agents never got the safety hooks** ([85b8a777](https://github.com/Dicklesworthstone/ntm/commit/85b8a777), [3d64eea1](https://github.com/Dicklesworthstone/ntm/commit/3d64eea1), [6b9b6512](https://github.com/Dicklesworthstone/ntm/commit/6b9b6512), [8ba5eedb](https://github.com/Dicklesworthstone/ntm/commit/8ba5eedb), [2db8f1d4](https://github.com/Dicklesworthstone/ntm/commit/2db8f1d4)): `ntm safety install` wrote a hook script that Claude Code never ran because no settings file listed it, while `ntm safety status` called it active; spawn and add passed the dcg and rch hooks in a `CLAUDE_CODE_HOOKS` variable Claude Code does not read; and `ntm send` skips dcg for Claude panes on the assumption that the hook covered them. Every Claude launch path, including spawn, add, robot spawn, restart, restore, rotation, swarm and CAAM account recovery, now passes the hooks with `--settings`, and the installed script delegates to a fail-closed `ntm safety claude-hook` instead of parsing with jq and allowing everything when jq was missing. Hook and wrapper refusals are logged with their tmux session, pane and pattern, so `ntm safety blocked` lists them, and both refusals and dcg-blocked sends now count in `ntm metrics` `blocked_commands`, which stayed at 0. The hook's tmux session lookup is bounded to 2s so a stuck tmux server cannot hang an agent's tool call. `ntm safety install` and the REST install endpoint ship one copy of the scripts.
- **dcg did not check robot or REST sends** ([4c8832cf](https://github.com/Dicklesworthstone/ntm/commit/4c8832cf)): only `ntm send` consulted dcg, so `--robot-send`, `--robot-send --track`, `--robot-interrupt --interrupt-msg` and REST agent sends delivered destructive command lines to non-Claude agents unchecked. They now share the guard, refuse when the check fails or times out, and interrupt refuses before sending Ctrl+C. A dcg probe cut short by cancellation is no longer cached for five minutes as "unavailable", which had switched the guard off for every sender.
- **`ntm spawn --privacy` kept nothing private** ([a48a32ab](https://github.com/Dicklesworthstone/ntm/commit/a48a32ab)): the setting lived only in the spawn process, so `ntm send`, `ntm serve` and the coordinator wrote prompt history, event logs, audit logs and checkpoints for private sessions. Spawn now records the setting on the tmux session before any agent starts and every ntm process consults it; a recorded private answer is never downgraded by a later failed lookup.
- **Recorders that nothing called** ([77dffca1](https://github.com/Dicklesworthstone/ntm/commit/77dffca1), [28f35969](https://github.com/Dicklesworthstone/ntm/commit/28f35969), [078a2473](https://github.com/Dicklesworthstone/ntm/commit/078a2473), [30d309cd](https://github.com/Dicklesworthstone/ntm/commit/30d309cd), [7780e0bc](https://github.com/Dicklesworthstone/ntm/commit/7780e0bc), [aa5515a2](https://github.com/Dicklesworthstone/ntm/commit/aa5515a2), [595a6c6f](https://github.com/Dicklesworthstone/ntm/commit/595a6c6f)): `ntm level` always showed "Commands run: 0"; successful interactive commands are now recorded (robot and JSON calls, shell completion and hidden commands are not). `ntm timeline list` returned nothing; the per-session monitor now records agent state transitions and persists the timeline, merging checkpoints so a concurrent `ntm kill` cannot wipe it. The dashboard's process-health panel was always empty; the dashboard now starts the pt monitor when `[integrations.process_triage]` is enabled, and readers get the monitor that was actually started. The state database was never garbage-collected; `ntm serve` now collects at start and hourly, and robot commands at most once an hour (tracked by a `state.db.gc` stamp file). `recovery.max_cm_rules` and `recovery.max_cm_snippets` were ignored in favor of fixed 10/3 limits.
- **Events were lost before reaching webhooks or the attention feed** ([15bcade8](https://github.com/Dicklesworthstone/ntm/commit/15bcade8), [d952e3ea](https://github.com/Dicklesworthstone/ntm/commit/d952e3ea), [3673e13e](https://github.com/Dicklesworthstone/ntm/commit/3673e13e), [ef1c9329](https://github.com/Dicklesworthstone/ntm/commit/ef1c9329), [a9dbf877](https://github.com/Dicklesworthstone/ntm/commit/a9dbf877)): the session monitor, coordinator and server now persist event-bus activity (agent crashes, restarts, rate limits, session endings, coordinator actuations) to the durable attention feed read by robot attention and incremental snapshots. Short-lived commands drain the events they emitted (spawn, add, kill, assignment) before exiting, and the webhook bridge waits for them before unsubscribing; before, the webhook for those events never fired. Terminal and robot `ntm kill` now share one implementation, so both reap orphaned agent processes and emit `session_killed`, and the paired kill and session-end events are recorded as one session destruction.
- **Robot and REST spawn skipped Agent Mail identity registration** ([fa486a81](https://github.com/Dicklesworthstone/ntm/commit/fa486a81), [54dc72e6](https://github.com/Dicklesworthstone/ntm/commit/54dc72e6), [6ea1089b](https://github.com/Dicklesworthstone/ntm/commit/6ea1089b)): only `ntm spawn`, add, adopt and relaunch ran the per-pane identity coordinator, so reservation-bearing assignment through the robot API failed with "pane has no canonical Agent Mail identity", pane badges never appeared, and `ntm lock` in such a session failed with "session has no Agent Mail identity". Robot and REST spawn now register each pane before its launch and the session identity after, and report `agent_mail` (with `agent_map` and `session_agent`) on the spawn envelope; `agents[]` entries carry `pane_id` and `agent_mail_name`.
- **Agent Mail clients ignored the configured endpoint** ([c0a50fb8](https://github.com/Dicklesworthstone/ntm/commit/c0a50fb8), [1dbb0199](https://github.com/Dicklesworthstone/ntm/commit/1dbb0199), [d394c3e2](https://github.com/Dicklesworthstone/ntm/commit/d394c3e2)): about 20 call sites, including the pre-commit reservation guard, robot bulk-assign reservations, robot mail check, pipeline mail steps, handoff, build slots and scanner notifications, read only `AGENT_MAIL_URL` / `AGENT_MAIL_TOKEN`, so against a token-protected or non-default server they got 401 or the wrong endpoint. Every client now starts from `[agent_mail] url` and `token`. With no token configured, ntm uses the bearer token `am` stored in `mcp-agent-mail/config.env`, and only for a localhost or loopback endpoint. `--robot-mail` no longer reports success with an empty agent list when it cannot read the project's agents.
- **Robot flags that were accepted and ignored** ([2d21c77c](https://github.com/Dicklesworthstone/ntm/commit/2d21c77c), [59c4a5cc](https://github.com/Dicklesworthstone/ntm/commit/59c4a5cc), [4cf7c91a](https://github.com/Dicklesworthstone/ntm/commit/4cf7c91a)): `--robot-dismiss-alert` ignored `--session` and `--all`, so `--dismiss-all --session=X` dismissed every session's alerts; `--robot-palette` ignored `--category`, `--robot-activity` `--type`, `--robot-tokens` `--agent`, `--robot-cass-search` `--since`, `--robot-markdown` `--session` and `--robot-inspect-pane` `--lines`; and the shared `--limit` was ignored by `--robot-triage`, `--robot-search`, `--robot-label-attention`, `--robot-file-beads`, `--robot-file-hotspots`, `--robot-file-relations`, `--robot-beads-list` and `--robot-files`. `--robot-interrupt` and `--robot-ack` ignored `--type`; REST interrupt now accepts `agent_types`.
- **`--robot-metrics` reported every agent at zero** ([916b5d35](https://github.com/Dicklesworthstone/ntm/commit/916b5d35)): its activity fields had no writer and `--period` was echoed but not applied. Prompts now come from prompt history (robot and REST sends are recorded there too, redacted), crashes and restarts from the session monitor's analytics events, tokens and context percent from transcripts, uptime from the pane shell's start and session duration from tmux. `--period` bounds the counters and never reaches back before the session's creation, and `unmeasured` lists what could not be measured on that call. `ntm health` and the dashboard health badges read uptime and restarts from the same sources instead of an in-memory tracker nothing wrote to.
- **`ntm send --template` sent literal placeholders** ([2c902d45](https://github.com/Dicklesworthstone/ntm/commit/2c902d45)): templates rendered once before fanning out, so every pane received "You are Agent #{{agent_num}} ({{agent_type}})" and "br update {{bead_id}}". Templates now render per target pane with its agent number, type and position in the batch, and the new `--bead <id>` fills bead fields from `br show` in the session's project. Dry runs, the JSON preview, dcg checks and prompt history use the per-pane text.
- **Smart routing ignored the requested targets** ([44e34a46](https://github.com/Dicklesworthstone/ntm/commit/44e34a46), [ca2d958c](https://github.com/Dicklesworthstone/ntm/commit/ca2d958c)): `ntm send --route` was ignored without `--smart`, and `--project`, `--batch`, `--distribute` and `--all` silently dropped smart routing. Smart routing also ignored variants (`--cc=sonnet`), `--tag`, multiple types and every type other than cc, cod, gmi and agy, so a Claude-targeted prompt could go to a Grok pane; it now chooses among exactly the panes a broadcast with the same filters would reach.
- **Quota and account data** ([eba325ac](https://github.com/Dicklesworthstone/ntm/commit/eba325ac), [3b55b1bf](https://github.com/Dicklesworthstone/ntm/commit/3b55b1bf), [e3699309](https://github.com/Dicklesworthstone/ntm/commit/e3699309), [4cff2514](https://github.com/Dicklesworthstone/ntm/commit/4cff2514)): ntm's caut parser never matched caut's published v1 contract, so usage-aware health, `--robot-monitor`, `--robot-quota-status` and the dashboard Usage & Costs panel saw no provider data; it now follows the contract, queries one provider per call, and refreshes the usage cache when it is older than a minute (its poller had been removed). `--robot-quota-check` overlays CAAM's live windows the way `--robot-quota-status` does (refs GH #319) and reports a provider with no data as `NOT_FOUND` rather than `PANE_NOT_FOUND`. A caam account in cooldown (`cooldown.active`, or `health.launch_usable=false`) is treated as unusable, so coordinator failover no longer switches a pane onto it, and account status surfaces show the cooldown. A failed `caam activate` reports caam's reason, and the rate-limit fix hint names `ntm --robot-switch-account=<provider>`.
- **ubs scans that failed were read as clean** ([b1215710](https://github.com/Dicklesworthstone/ntm/commit/b1215710)): a failed or refused scan (exit 2) or one that scanned nothing (exit 3) passed the pre-commit hook, and `ntm scan --update-beads` closed every open ubs-scan bead. They now return `SCAN_FAILED` or report that nothing was scanned, and close nothing. Findings are also read from `scanners[].findings[]` (a file with two critical `eval()` hits had produced 0 findings), and language exclusion passes `--exclude-langs`. The scan bridge, the REST beads label filter and claim evidence use the `br list` flags br accepts (`--label`, one `--status` per value).
- **bv and br output** ([54a6847a](https://github.com/Dicklesworthstone/ntm/commit/54a6847a), [403ccff6](https://github.com/Dicklesworthstone/ntm/commit/403ccff6), [843b5673](https://github.com/Dicklesworthstone/ntm/commit/843b5673), [94e66f60](https://github.com/Dicklesworthstone/ntm/commit/94e66f60), [f4e78848](https://github.com/Dicklesworthstone/ntm/commit/f4e78848), [0d917335](https://github.com/Dicklesworthstone/ntm/commit/0d917335), [540ca2a9](https://github.com/Dicklesworthstone/ntm/commit/540ca2a9), [6fbb660b](https://github.com/Dicklesworthstone/ntm/commit/6fbb660b)): bv emits dependency cycles as arrays of issue IDs, and any cyclic graph made `--robot-insights` fail to decode, so `--robot-graph` returned `INTERNAL_ERROR`, the cycle alert never fired and `--robot-health` lost its bottlenecks and keystones. `--robot-suggest`, triage `project_health` and the file-beads, hotspots and relations surfaces decode bv v0.25.2's shapes. `ntm work history` requests a bounded window (`--since`, default 30d; the full history exceeded the 10MB output cap) and decodes bv's per-bead history, `ntm work burndown` renders issue and day counts instead of "0/0 points", and `ntm work graph --format=dot|mermaid` prints the diagram instead of the JSON envelope. Blocked dependencies are read from the `br blocked --json` envelope (recovery prompts and robot health had none), scanner beads are created with `br create --json` so project-prefixed IDs are linked instead of producing a duplicate bead on retry, and dependency neighbors no longer include the queried bead itself.
- **cass and cm** ([407d4479](https://github.com/Dicklesworthstone/ntm/commit/407d4479), [ffceede9](https://github.com/Dicklesworthstone/ntm/commit/ffceede9), [c7f2e127](https://github.com/Dicklesworthstone/ntm/commit/c7f2e127), [4db8ad28](https://github.com/Dicklesworthstone/ntm/commit/4db8ad28), [9f2330f5](https://github.com/Dicklesworthstone/ntm/commit/9f2330f5), [9b811053](https://github.com/Dicklesworthstone/ntm/commit/9b811053), [166ed3ef](https://github.com/Dicklesworthstone/ntm/commit/166ed3ef), [13ab0e8f](https://github.com/Dicklesworthstone/ntm/commit/13ab0e8f)): context injection (`ntm send --with-cass`, `ntm cass preview`, robot sends) treated cass scores as an absolute 0-1 scale, and default hybrid search scores are about 0.016, so no hit passed the 0.7 threshold and injection added nothing. Scores are now relative to the best hit in each result set. `ntm cass timeline` and `GET /cass/timeline` decode cass's range/groups/sessions shape (the table was always empty and REST entries had no timestamps); `ntm cass insights` and `--robot-cass-insights` request cass's aggregation buckets and honor `--since` (default 7d); `ntm cass status` reports cass's real version, database size and counts. `ntm search --session` resolves the session to its project directory. Ideation reads cm rule text from `content`. `ntm send` cass enrichment honors `[cass] timeout` (15s when unset) and bounds pipe waits, so a wedged search cannot hold dispatch.
- **xf, ms, jfp, slb and ru** ([3535d31a](https://github.com/Dicklesworthstone/ntm/commit/3535d31a), [7b9eba41](https://github.com/Dicklesworthstone/ntm/commit/7b9eba41), [bc9ae683](https://github.com/Dicklesworthstone/ntm/commit/bc9ae683), [fa78a284](https://github.com/Dicklesworthstone/ntm/commit/fa78a284), [1d54be4a](https://github.com/Dicklesworthstone/ntm/commit/1d54be4a), [dfaf8eac](https://github.com/Dicklesworthstone/ntm/commit/dfaf8eac), [c0ed58ef](https://github.com/Dicklesworthstone/ntm/commit/c0ed58ef), [36554659](https://github.com/Dicklesworthstone/ntm/commit/36554659), [8ffddc76](https://github.com/Dicklesworthstone/ntm/commit/8ffddc76)): the palette's xf search (ctrl+k) passed a removed flag and always failed; xf search and health now use `--format json` and `xf stats`, and the robot xf flags forward limit, mode and sort. The ms adapter uses ms's global `-O json` and decodes its `results` and `skill` envelopes (skill discovery had failed or returned nothing), and `--robot-ms-search` returns a typed `skills` array. jfp list, search and suggest envelopes are unwrapped and counted, and `--robot-jfp-update` runs `jfp refresh`. `--robot-slb-approve` and `--robot-slb-deny` could never succeed; requests and reviews now go through slb sessions and `reject`, with `PERMISSION_DENIED` for a missing session, self-review or bad key and `NOT_FOUND` for an unknown request, and errors never include the session key. `--robot-ru-sync` reads ru's `data.repos` envelope, adds a `failed` list, maps ru's exit codes to `RU_CONFLICTS`, `RU_PARTIAL_FAILURE` and dependency or interrupted-sync errors, and lists a repo once even when ru reports it twice; ru's capability probe reads its help from stderr.
- **dcg rule names and tool capability probes** ([4cff2514](https://github.com/Dicklesworthstone/ntm/commit/4cff2514)): `--robot-dcg-check` reported no rule because ntm read fields dcg never emits; it now reports dcg's `rule_id` as `rule_matched` and its explanation as the suggestion. Capability probes read `--help` from both output streams, so ubs, pt and rano report their robot modes.
- **rano and pt** ([6ecedcc0](https://github.com/Dicklesworthstone/ntm/commit/6ecedcc0), [c5f3c567](https://github.com/Dicklesworthstone/ntm/commit/c5f3c567), [0b589055](https://github.com/Dicklesworthstone/ntm/commit/0b589055), [4df918ca](https://github.com/Dicklesworthstone/ntm/commit/4df918ca), [7be83421](https://github.com/Dicklesworthstone/ntm/commit/7be83421)): ntm called rano and pt subcommands that do not exist. `--robot-rano-stats` and the dashboard Network panel now read `rano export --format jsonl` from the database set by the new `[integrations.rano] sqlite_path` (default `observer.sqlite`), count connect events per pane (joined by pane ID, not title), and show connection counts, last connection and provider tags instead of always-zero traffic. Process triage uses `pt agent watch --once --threshold low --format jsonl`, never applies anything, and feeds the resident monitor, the dashboard and `--robot-agent-health` again. `ntm serve` no longer logs "alert channel full, dropping alert" for every pt alert past the 100th.
- **The web Agents page showed no agents** ([b92d5fc9](https://github.com/Dicklesworthstone/ntm/commit/b92d5fc9)): `GET /api/v1/sessions/{name}/agents` and the legacy agents endpoint answered with no agents while agents ran (one read a table nothing writes; the v1 route was shadowed by another). Both now list live agent panes enriched with projected status, `state_reason`, `last_seen`, health and current task. A hung tmux falls back to projection rows with a warning or answers 504, and a stopped session the store knows returns an empty list instead of 500.
- **`ntm swarm` launched Codex into a rate-limited account** ([b73b69c8](https://github.com/Dicklesworthstone/ntm/commit/b73b69c8)): the swarm launcher never loaded the rate-limit history that `ntm spawn` and `ntm add` use; it now waits out each pane project's recorded Codex cooldown before launching.
- **Concurrent agent additions could lose session manifest records** ([2656f647](https://github.com/Dicklesworthstone/ntm/commit/2656f647)): per-agent manifest updates were guarded only within one process. Updates, saves and deletes now take a per-session lock across processes (bounded to 5s) and reject malformed manifests or a mismatched session identity instead of overwriting them; non-Unix systems fail closed.
- **Pane badges on tmux 3.4** ([9a2f48f0](https://github.com/Dicklesworthstone/ntm/commit/9a2f48f0)): tmux 3.4 escapes the `\x1f` field separator in format output, so badge reads returned the whole record and window listing failed, which disabled badges on Ubuntu 24.04's default tmux.
- **Pipeline step failures lost their evidence** ([af9dd340](https://github.com/Dicklesworthstone/ntm/commit/af9dd340)): prompt and template steps that failed to send or timed out stopped recording the pane's last 50 lines and agent state for `ntm pipeline status` and the pipelines API; they record them again.
- `ntm personas` was registered twice and appeared twice in help and shell completion ([520fa799](https://github.com/Dicklesworthstone/ntm/commit/520fa799)).
- **Docs** ([e12b2afe](https://github.com/Dicklesworthstone/ntm/commit/e12b2afe), [8065d57a](https://github.com/Dicklesworthstone/ntm/commit/8065d57a), [375641c1](https://github.com/Dicklesworthstone/ntm/commit/375641c1), [448cd9f8](https://github.com/Dicklesworthstone/ntm/commit/448cd9f8), [fa3b17db](https://github.com/Dicklesworthstone/ntm/commit/fa3b17db)): robot API docs named flags that do not exist (`--robot-bead-list`, `--robot-pipeline-status`, `--robot-incident`, a `--query` flag, `--project` for mail check, `--limit` for xf, `--spawn-dry-run`), and examples used the deprecated `--md-compact` and `--ack-timeout`; they now use the flags the binary accepts. The README names the WebSocket endpoint (`/api/v1/ws`) and the per-user checkpoint store (`~/.local/share/ntm/checkpoints/<session>/`). `docs/openapi.json` is regenerated from the served router.

### Changed

- **Assignment matching** ([f8924140](https://github.com/Dicklesworthstone/ntm/commit/f8924140), [83c19bfb](https://github.com/Dicklesworthstone/ntm/commit/83c19bfb)): candidates are filtered by confidence before workload balancing, so an idle low-score agent no longer takes work a qualified agent could accept. Batch assignment first admits the highest-priority feasible set of beads, then maximizes total score with the min-cost flow solver, moving flexible work to free specialists instead of leaving them idle.
- **Builds include ensemble** ([116098c6](https://github.com/Dicklesworthstone/ntm/commit/116098c6), [91bc4f27](https://github.com/Dicklesworthstone/ntm/commit/91bc4f27), [77aa2db2](https://github.com/Dicklesworthstone/ntm/commit/77aa2db2)): `make build`, `make build-all` and the README's from-source install now build with `-tags ensemble_experimental`, as release binaries do, so a source-built ntm runs `ntm ensemble`. The container image builds with the digest-pinned Go 1.26.8 Alpine builder and the same tag, and installs Alpine packages from the branch's current index rather than exact builds that can disappear from it.

### Performance

- **Session monitor file-change sampling** (GH #337): the monitor ran `git status --untracked-files=all` and stat'd every record every 15s, so one untracked directory of agent scratch output (84k files in the report) cost an idle session about 13% of a core. Untracked directories are now listed per file only while small (1,000 entries each and 5,000 per sample, with the size probes bounded by the same budget and pathspecs batched 500 at a time); a larger directory is tracked as one entry. On a repository with 50,000 untracked files a sample went from about 215ms to about 7ms. The sampler also runs git with `--no-optional-locks` so it never takes `index.lock` from an agent's commit, and stats paths against the working tree's top level, which fixes missed repeat edits and commits recorded as deletions when the manifest root is a subdirectory ([3ba3702e](https://github.com/Dicklesworthstone/ntm/commit/3ba3702e), [36232c17](https://github.com/Dicklesworthstone/ntm/commit/36232c17)).
- **Robot commands skip the projection refresh they do not read** ([e39494fb](https://github.com/Dicklesworthstone/ntm/commit/e39494fb), [624077a7](https://github.com/Dicklesworthstone/ntm/commit/624077a7)): every `--robot-*` call first ran the full projection refresh (tmux captures of every pane, bv triage, br and git queries). Metadata surfaces (capabilities, help, schema, palette, recipes and similar), the tool bridges (cass, ms, xf, jfp, slb, rch, quota, account, ru-sync, dcg, mail, rano and others) and send, interrupt, ack, tail and is-working now skip it. Measured on one host: `--robot-palette` 4.65-5.25s to 0.15-0.20s, `--robot-mail` 4.03-4.72s to 0.30-0.36s, `--robot-tail` 2.44-2.61s to 0.15-0.20s, `--robot-is-working` 2.31-2.68s to 0.12-0.21s.
- **The projection refresh collects work and tmux state concurrently** ([cd55bdc0](https://github.com/Dicklesworthstone/ntm/commit/cd55bdc0)): `--robot-status` went from 3.57-4.39s to 2.36-2.71s and `--robot-snapshot` from 4.60-5.37s to 3.64-3.97s on the same host.
- The session timeline recorder samples agent states every 10s instead of 5s, matching the health-check cadence ([00322379](https://github.com/Dicklesworthstone/ntm/commit/00322379)).

### Dependencies

- Go modules: `charmbracelet/x/ansi` 0.11.7 -> 0.11.8, `go-chi/chi/v5` 5.3.1 -> 5.3.2, `mattn/go-runewidth` 0.0.27 -> 0.0.30, `shirou/gopsutil/v4` 4.26.7 -> 4.26.9, `golang.org/x/sys` 0.47 -> 0.48, `golang.org/x/term` 0.45 -> 0.46, `golang.org/x/tools` 0.49 -> 0.51 and `modernc.org/sqlite` 1.56.0 -> 1.60.1 ([c8209333](https://github.com/Dicklesworthstone/ntm/commit/c8209333), [c399825b](https://github.com/Dicklesworthstone/ntm/commit/c399825b)). `chromedp` stays at 0.16.0: 0.17 through 0.20 redesign its API and the e2e web UI test has not been ported ([8de65bda](https://github.com/Dicklesworthstone/ntm/commit/8de65bda)).

---

## [v1.36.1] -- 2026-10-01 [GitHub Release]

**ntm now registers with the Python Agent Mail server, `--robot-spawn` can launch OpenCode panes, and `--robot-capabilities` is about half its former size.** Follow-up to GitHub issue [#333](https://github.com/Dicklesworthstone/ntm/issues/333).

### Upgrade notes

- **`--robot-capabilities` `surfaces[]` entries are slimmer.** Each entry now carries only the surface-level metadata that `commands[]` lacks (`paginated`, `paginated_reason`, `consumer_guidance`, `boundedness`, `follow_up`, `action_handoff`, `request_semantics`, `attention_ops`, `explainability`, `lifecycle`) and joins to `commands[]` by `name`. Flags, descriptions, parameters, examples and transports are no longer repeated there. A consumer that read those fields from `surfaces[]` should read them from the matching `commands[]` entry. The full document drops from 431 KB to 236 KB; nothing was removed from the document as a whole ([2af3ec36](https://github.com/Dicklesworthstone/ntm/commit/2af3ec36), [ee589e7e](https://github.com/Dicklesworthstone/ntm/commit/ee589e7e)).

### Added

- **`--robot-spawn --spawn-oc=N[:model]`** launches OpenCode panes through the same command template as `ntm spawn --oc` (the built-in default or `[agents] oc`); REST spawn takes `oc_count`, and jobs take `oc_model`. There is no effort setting, since the OpenCode TUI has none. `--spawn-wait` waits for OpenCode's own composer rather than a shell prompt. The capabilities document lists the flag with an example ([a6d454a8](https://github.com/Dicklesworthstone/ntm/commit/a6d454a8), [2af3ec36](https://github.com/Dicklesworthstone/ntm/commit/2af3ec36)).
- The README documents OpenCode panes and how to let them run unattended (OpenCode 1.18+ `--auto` via `[agents] oc`, or OpenCode's own `permission` config) ([a6d454a8](https://github.com/Dicklesworthstone/ntm/commit/a6d454a8)).

### Fixed

- **`ntm spawn` failed with `ensure_project failed (HTTP 400)` against the Python Agent Mail server** (GH #333). That server's `/mcp` endpoint, ntm's default, is a stateful MCP endpoint that rejects any request without a session ID, and ntm never ran the MCP `initialize` handshake. When a server rejects a request for lack of a session, ntm now runs the handshake once, keeps the session ID for later calls, and retries. Concurrent first calls share one handshake, and a server that keeps rejecting the session gets one handshake per call, not a loop. Servers that answer one-shot requests (the Rust server, `/api`) never see a handshake. Error messages now quote the server's own text instead of a bare status line. The same server also rejected agent registration over the optional `pane_id` argument; ntm retries once without the pane binding in exactly that case ([2916aa5c](https://github.com/Dicklesworthstone/ntm/commit/2916aa5c), [bb2d57f0](https://github.com/Dicklesworthstone/ntm/commit/bb2d57f0)).
- **`--robot-context` reported `model: unknown` for panes started with an explicit model** such as `--oc=1:MODEL`, because it guessed from the pane title. It now reports the model recorded in the pane's launch spec and guesses only for panes without one. Robot-spawned panes of every type now record their resolved model, alias and effort, as `ntm spawn` panes already did. The context budget uses the recorded model only when the model registry knows it; otherwise it keeps the agent family's limit, so an agy pane keeps gemini's 1M window and `opus[1m]` keeps claude's ([a6d454a8](https://github.com/Dicklesworthstone/ntm/commit/a6d454a8), [a43f4a82](https://github.com/Dicklesworthstone/ntm/commit/a43f4a82)).
- `docs/openapi.json` is regenerated for the slimmer capabilities surfaces and picks up routes and responses added since 2026-09-17 ([ee589e7e](https://github.com/Dicklesworthstone/ntm/commit/ee589e7e)).

### Changed

- The full `--robot-capabilities` document is 236 KB, down from 431 KB, and the e2e size budget is now 300 KB so the duplication cannot come back unnoticed ([2af3ec36](https://github.com/Dicklesworthstone/ntm/commit/2af3ec36)).

---

## [v1.36.0] -- 2026-09-29 [GitHub Release]

**Oh My Pi (`omp`) becomes a built-in agent type; pipelines, REST jobs and checkpoint restores become durable across process exits and crashes; work assignment verifies candidates against the tracker, Agent Mail reservations and mutex groups before dispatch. Also fixes the three issues filed against v1.35.x** (GitHub issues [#332](https://github.com/Dicklesworthstone/ntm/issues/332), [#333](https://github.com/Dicklesworthstone/ntm/issues/333), [#334](https://github.com/Dicklesworthstone/ntm/issues/334)).

### Upgrade notes

- **Schema migration `024_runtime_work_snapshots`** applies on first run. It only adds a table (the durable work snapshots behind `ntm work-snapshot`); existing rows are untouched ([a1fb5774](https://github.com/Dicklesworthstone/ntm/commit/a1fb5774)).
- **`models.default_claude` now defaults to empty** (GH #334): a bare `--cc` pane launches with no `--model` and Claude Code uses its own default or saved model. The `opus` / `sonnet` / `haiku` / `architect` / `fast` aliases now pass Claude Code's family aliases instead of dated model IDs. If you relied on ntm pinning a specific Claude model, set `[models] default_claude` explicitly ([04ba2c7a](https://github.com/Dicklesworthstone/ntm/commit/04ba2c7a)).
- **`ntm workflow run --resume` continues the saved stage instead of clearing it and starting over.** Use the new `--restart` to repeat an unfinished run from the beginning ([20dd37bf](https://github.com/Dicklesworthstone/ntm/commit/20dd37bf)).
- **`blocked-on-ivan` is no longer a built-in operator-gated label.** Projects that used it must add it to the new `[assign] operator_gated_labels` key ([29d1e7ea](https://github.com/Dicklesworthstone/ntm/commit/29d1e7ea)).
- **`ntm serve` / `ntm web` now bound async jobs** to 4 concurrent workers and 64 queued jobs by default (`--job-concurrency`, `--job-queue-capacity`). Past that, `POST /api/v1/jobs` answers 503 with `Retry-After` ([f3e3401b](https://github.com/Dicklesworthstone/ntm/commit/f3e3401b)).

### Added

- **Oh My Pi (`omp`) is a first-class agent type** ([aa0182bc](https://github.com/Dicklesworthstone/ntm/commit/aa0182bc), [610bf2c9](https://github.com/Dicklesworthstone/ntm/commit/610bf2c9), [cb4897b2](https://github.com/Dicklesworthstone/ntm/commit/cb4897b2), [e48b1bc9](https://github.com/Dicklesworthstone/ntm/commit/e48b1bc9), [fec3f003](https://github.com/Dicklesworthstone/ntm/commit/fec3f003), [327036fa](https://github.com/Dicklesworthstone/ntm/commit/327036fa)): `ntm spawn/add/create/adopt --omp=N[:model[:effort]]`, `--robot-spawn --spawn-omp`, REST `omp_count`, respawn/restart/resume, personas, recipes, session profiles, the spawn wizard, `ntm send --omp`, activity/assign/rebalance/review-queue filters, dashboards, analytics, events, the VS Code picker and the web UI all accept omp. The default launch is `omp --auto-approve` (plus `--model`, `--thinking <effort>`, `--append-system-prompt` when requested); `[agents] omp`, `[models] default_omp` / `[models.omp]`, `[prompts] omp_default`, and `[spawn_pacing.agent_caps] omp_max_concurrent` configure it. `omp_max_concurrent` defaults to 8, omp's share of the host-wide robot spawn budget, sized so a full `--spawn-omp=8` swarm is admitted ([d66ff2df](https://github.com/Dicklesworthstone/ntm/commit/d66ff2df)). `ntm palette` targets omp on key `7` ([03f61b2b](https://github.com/Dicklesworthstone/ntm/commit/03f61b2b)); `--assign` scores omp with the midpoint of the Claude and Codex capability profiles ([0b551e0b](https://github.com/Dicklesworthstone/ntm/commit/0b551e0b)); `ntm quota`, `ntm rotate all-limited` and `--robot-quota-check` report omp as having no quota API instead of skipping it ([0ca86156](https://github.com/Dicklesworthstone/ntm/commit/0ca86156)); the dashboard cost panel prices omp panes by their configured model ([6c90e45f](https://github.com/Dicklesworthstone/ntm/commit/6c90e45f)).

- **omp pane state from its live TUI** ([36000c67](https://github.com/Dicklesworthstone/ntm/commit/36000c67), [58b0a719](https://github.com/Dicklesworthstone/ntm/commit/58b0a719), [99e46df4](https://github.com/Dicklesworthstone/ntm/commit/99e46df4), [036b6bdf](https://github.com/Dicklesworthstone/ntm/commit/036b6bdf), [b65b7004](https://github.com/Dicklesworthstone/ntm/commit/b65b7004)): captured against omp v18.2.3 in the nerd, unicode and ascii presets. Working = spinner and timer in the composer's top border, the Esc-hint activity line, a pending `Steering · N` block, or a listed ` Subagents` panel; idle = a quiet composer with nothing below it. A failed turn's rule-framed provider-error block (`server_error: ERROR`, `401 Invalid API Key`, `Dismissed when you send your next message.`) is a retryable error (`ERROR_STATE`, `indicator_basis: "provider_error"`, coordinator `agent_error`), never idle-after-completion, including when omp draws `F5 to Retry` between the block and the composer; the recommendation names `F5` (omp's retry key) and a continue prompt. omp panes are identified only by the exact `omp` executable basename, so `libomp` or `rg omp` do not match. No rate-limit state is inferred from transcript text (no omp rate-limit screen has been captured yet). `--robot-dialogs` reports a staged large paste as paste limbo and a selector overlay as a dialog.

- **omp delivery and control** ([5e3dfe17](https://github.com/Dicklesworthstone/ntm/commit/5e3dfe17), [fec3f003](https://github.com/Dicklesworthstone/ntm/commit/fec3f003)): Enter submits once; a message sent to a busy omp pane is queued as steering and confirmed when the payload leaves the composer, with one rescue Enter if it is stranded. A blank pane during omp's first second, an open completion list, or a selector overlay is refused rather than typed into. Interrupt uses **Escape** for omp (Ctrl+C only clears omp's draft and a double press quits) across `ntm interrupt`, `--robot-interrupt`, diagnose, ensemble stop and the REST pane interrupt; graceful exit is Escape, Ctrl+C, Ctrl+D.

- **omp context accounting and session resume** ([fec3f003](https://github.com/Dicklesworthstone/ntm/commit/fec3f003), [aaeb9e63](https://github.com/Dicklesworthstone/ntm/commit/aaeb9e63), [92282cb9](https://github.com/Dicklesworthstone/ntm/commit/92282cb9)): `--robot-context` (`source: "status_bar"`), `--robot-snapshot`, `ntm status` and the coordinator's context-rotation trigger read omp's composer-border gauge (`7%` … `262K`, including the compressed `69%󰁨262K` form), so readings stay per pane when many omp panes share a directory; transcripts under `~/.omp/agent/sessions` provide transcript usage too, and `/compact` and `/clear` are used for compaction. `ntm sessions save` binds each omp pane to its session through the transcript omp holds open (sub-agent transcripts excluded; profile, XDG and `PI_CODING_AGENT_*` stores honoured) and resume relaunches with `omp --auto-approve --resume <id>`.

- **Background pipelines run in detached worker processes** ([fb73d986](https://github.com/Dicklesworthstone/ntm/commit/fb73d986), [98d7d635](https://github.com/Dicklesworthstone/ntm/commit/98d7d635), [d1ca923a](https://github.com/Dicklesworthstone/ntm/commit/d1ca923a), [ffff34de](https://github.com/Dicklesworthstone/ntm/commit/ffff34de)): CLI `--background` runs, REST background runs and the new `ntm pipeline resume --background` re-exec ntm as a worker that survives the launching command or server exiting, instead of a goroutine that died with it. Worker progress is journaled and relayed over the existing WebSocket events; `--config` and `--ssh` carry over to the worker; dry runs stay synchronous and write nothing.

- **Pipeline runs have cross-process ownership and cancellation** ([e6edbc2b](https://github.com/Dicklesworthstone/ntm/commit/e6edbc2b), [bd954462](https://github.com/Dicklesworthstone/ntm/commit/bd954462), [02d7356f](https://github.com/Dicklesworthstone/ntm/commit/02d7356f)): every run (foreground, background, REST, Jobs, resume) holds an OS-level per-project run lock through cleanup, so two resumes of one run cannot both execute; the loser gets `PIPELINE_RUNNING` (HTTP 409) without touching the checkpoint. A fresh process can cancel a run it does not own; it reports `cancellation_requested` only after the owner acknowledges, and requests are token-fenced so an old request cannot cancel a replacement run. REST cancel/list/inspect read the configured project's durable state instead of a process-global registry keyed only by run ID, which could have cancelled a same-named run in another project.

- **Pipeline recovery uses frozen inputs** ([2c2c52d3](https://github.com/Dicklesworthstone/ntm/commit/2c2c52d3), [2be31295](https://github.com/Dicklesworthstone/ntm/commit/2be31295), [34c43c23](https://github.com/Dicklesworthstone/ntm/commit/34c43c23), [4bc393eb](https://github.com/Dicklesworthstone/ntm/commit/4bc393eb), [f358b7d8](https://github.com/Dicklesworthstone/ntm/commit/f358b7d8)): before dispatch, a run's workflow definition, its existing template files and its `prompt_file` inputs are copied into a private, content-addressed snapshot, and the run executes that copy. Editing the workflow or its templates mid-run no longer changes what a resume executes; a missing or corrupted snapshot fails the resume instead of falling back to the mutable original. Templates generated during execution remain live inputs and are warned about.

- **Pipeline resume does not resend confirmed agent prompts** ([4ee45168](https://github.com/Dicklesworthstone/ntm/commit/4ee45168), [a7df9acb](https://github.com/Dicklesworthstone/ntm/commit/a7df9acb), [718222c0](https://github.com/Dicklesworthstone/ntm/commit/718222c0), [7e5064ee](https://github.com/Dicklesworthstone/ntm/commit/7e5064ee), [12230ccc](https://github.com/Dicklesworthstone/ntm/commit/12230ccc)): prompt and template dispatches are journaled before sending; resume keeps observing an already-delivered request instead of pasting it again, and an uncertain delivery requires an explicit restart. This covers loop, foreach, foreach_pane and nested parallel/branch bodies. A resumed foreach whose item list changed after partial dispatch is refused, and forced loop rewinds clear stale downstream outputs instead of reusing them.

- **Mixed and nested `parallel` blocks** ([2b3d8fb9](https://github.com/Dicklesworthstone/ntm/commit/2b3d8fb9), [2ac3c158](https://github.com/Dicklesworthstone/ntm/commit/2ac3c158)): parallel children can now be commands, templates and composite steps, not only prompts, and inherit retries, cancellation, checkpoint gates and hooks. Branch bodies follow the same lifecycle (conditions, retries, hooks, checkpointed outputs), and an unresolved branch selector is rejected before any shell command runs.

- **REST jobs for pipelines, swarms and restores are durable** ([e3ebd1fd](https://github.com/Dicklesworthstone/ntm/commit/e3ebd1fd), [0b4ec465](https://github.com/Dicklesworthstone/ntm/commit/0b4ec465), [0c2dfe64](https://github.com/Dicklesworthstone/ntm/commit/0c2dfe64), [095e743f](https://github.com/Dicklesworthstone/ntm/commit/095e743f), [8cb79e1b](https://github.com/Dicklesworthstone/ntm/commit/8cb79e1b), [f94c5382](https://github.com/Dicklesworthstone/ntm/commit/f94c5382)): new `pipeline_exec` and `pipeline_resume` job types. Jobs are journaled before dispatch and after cleanup, so completed, failed and cancelled results survive a server restart; a job interrupted by a crash is reported as outcome-unknown and never replayed. An optional `params.operation_id` makes a retried request replay the original result instead of running again. Swarm-spawn jobs checkpoint live startup (pane IDs, launches, readiness), which job GET/list expose while work is running.

- **Job scheduling and swarm launch controls** ([f3e3401b](https://github.com/Dicklesworthstone/ntm/commit/f3e3401b), [536d0d3a](https://github.com/Dicklesworthstone/ntm/commit/536d0d3a), [fcec2e03](https://github.com/Dicklesworthstone/ntm/commit/fcec2e03), [2e678888](https://github.com/Dicklesworthstone/ntm/commit/2e678888), [5a78887c](https://github.com/Dicklesworthstone/ntm/commit/5a78887c), [9d21fff8](https://github.com/Dicklesworthstone/ntm/commit/9d21fff8), [d387ecb0](https://github.com/Dicklesworthstone/ntm/commit/d387ecb0)): jobs are admitted through a bounded FIFO queue and serialized per tmux session (and per saved run for resumes) while independent sessions run in parallel. `swarm_spawn` jobs accept dry-run, working directory, model/effort, readiness timeout, assignment and reservation options, plus `launch_interval` and `startup_timeout`; unknown or unsafe parameters are rejected instead of silently dropped. A top-level job `session` is now honoured and a conflicting nested session is rejected.

- **Checkpoint restore into a separate session, cancellable and journaled** ([852fd63f](https://github.com/Dicklesworthstone/ntm/commit/852fd63f), [efd82a74](https://github.com/Dicklesworthstone/ntm/commit/efd82a74), [5df64389](https://github.com/Dicklesworthstone/ntm/commit/5df64389), [59a2365a](https://github.com/Dicklesworthstone/ntm/commit/59a2365a), [5a1c25a3](https://github.com/Dicklesworthstone/ntm/commit/5a1c25a3)): `ntm checkpoint restore --as <session>` and REST/job `target_session` recover alongside a live session without modifying it or the stored checkpoint. Restores can be cancelled at every phase and report the stage reached on partial failure. Saved context is sent only after the restored agent shows two fresh idle readings, and never into a user shell.

- **Exact launch settings survive recovery** ([64070ddf](https://github.com/Dicklesworthstone/ntm/commit/64070ddf), [6a9a976e](https://github.com/Dicklesworthstone/ntm/commit/6a9a976e), [4a65220e](https://github.com/Dicklesworthstone/ntm/commit/4a65220e)): spawn and add record each pane's rendered command, model, persona, reasoning effort, account profile, credential isolation and worktree. Checkpoint restore, `ntm sessions` restore, native conversation resume and every resilience restart path replay those settings instead of reconstructing them from pane titles, and preflight every saved launch before replacing a session.

- **`ntm work-snapshot`: source-verified ready work** ([a1fb5774](https://github.com/Dicklesworthstone/ntm/commit/a1fb5774), [dd0829ac](https://github.com/Dicklesworthstone/ntm/commit/dd0829ac), [c1d2375a](https://github.com/Dicklesworthstone/ntm/commit/c1d2375a), [66f598db](https://github.com/Dicklesworthstone/ntm/commit/66f598db)): a read-only query that reports ready candidates checked against the tracker JSONL, git HEAD, eligibility policy, mutex groups and live Agent Mail reservations, persisted per project. `--watch` streams observations, `--wait-ready N` waits for N verified candidates (optionally `--require-reservations`), and `--refresh` forces a new collection. Source drift or a failed read is reported as unavailable, never as an empty queue.

- **Work eligibility and mutex groups are checked before dispatch** ([3ef99a18](https://github.com/Dicklesworthstone/ntm/commit/3ef99a18), [5c3aae2e](https://github.com/Dicklesworthstone/ntm/commit/5c3aae2e), [25bb5f10](https://github.com/Dicklesworthstone/ntm/commit/25bb5f10), [7ffd8934](https://github.com/Dicklesworthstone/ntm/commit/7ffd8934), [b9a108f8](https://github.com/Dicklesworthstone/ntm/commit/b9a108f8), [7fa4a9d9](https://github.com/Dicklesworthstone/ntm/commit/7fa4a9d9), [8ae5a96d](https://github.com/Dicklesworthstone/ntm/commit/8ae5a96d), [403dbdd7](https://github.com/Dicklesworthstone/ntm/commit/403dbdd7), [575224a2](https://github.com/Dicklesworthstone/ntm/commit/575224a2), [2154af0b](https://github.com/Dicklesworthstone/ntm/commit/2154af0b), [38ea93a7](https://github.com/Dicklesworthstone/ntm/commit/38ea93a7)): the bv triage cache is bound to the tracker JSONL digest and HEAD rather than a TTL alone. Recommendations filter lifecycle, ownership, dependency, deferral and mutex exclusions before applying the caller's limit, so a blocked top-N no longer hides eligible work below it; mutually exclusive tasks are never recommended together, and the atomic tracker claim enforces mutex groups across competing processes. Live peer reservations exclude reserved candidates. A gated-only or all-ineligible queue is reported as nothing to do rather than as an error.

- **Better assignment batches** ([8224b5df](https://github.com/Dicklesworthstone/ntm/commit/8224b5df), [74a20ae9](https://github.com/Dicklesworthstone/ntm/commit/74a20ae9), [6cae70fb](https://github.com/Dicklesworthstone/ntm/commit/6cae70fb), [aeaef0cc](https://github.com/Dicklesworthstone/ntm/commit/aeaef0cc)): the balanced `ntm assign` planner uses min-cost maximum flow and the coordinator uses maximum-weight matching instead of greedy selection, so specialist-only work is no longer stranded by a flexible task taking its only worker. Session capacity is consumed across the whole batch, and same-named workers in different sessions are kept distinct. File intent extracted from task text ignores URLs and issue references and normalizes Markdown links and `file:line` suffixes before reserving.

- **Assignment reservations are maintained and recovered** ([04a173b6](https://github.com/Dicklesworthstone/ntm/commit/04a173b6), [3c2efc84](https://github.com/Dicklesworthstone/ntm/commit/3c2efc84), [eeec6259](https://github.com/Dicklesworthstone/ntm/commit/eeec6259), [4ba38e38](https://github.com/Dicklesworthstone/ntm/commit/4ba38e38), [2936dfd1](https://github.com/Dicklesworthstone/ntm/commit/2936dfd1)): the coordinator and `assign --watch` renew the exact reservation leases of delivered assignments in their final 15 minutes, even when automatic assignment is disabled, and stop admitting new work if protection cannot be confirmed. When a pane running delivered work is proven gone, its reservations are released by ID and the claim is reopened before new work is assigned.

- **Agent Mail grants are verified before they are trusted** ([7dee1382](https://github.com/Dicklesworthstone/ntm/commit/7dee1382), [09df4491](https://github.com/Dicklesworthstone/ntm/commit/09df4491), [57fd740c](https://github.com/Dicklesworthstone/ntm/commit/57fd740c), [38f4b251](https://github.com/Dicklesworthstone/ntm/commit/38f4b251), [bd2c8b71](https://github.com/Dicklesworthstone/ntm/commit/bd2c8b71), [6ffa27f2](https://github.com/Dicklesworthstone/ntm/commit/6ffa27f2)) (refs [#328](https://github.com/Dicklesworthstone/ntm/issues/328)): reservation readback follows every page instead of stopping after 20 leases, and every grant is checked against an independent, project-scoped read of active reservations (owner, path, exclusivity, expiry). Shared reservations send `exclusive=false` explicitly, and a reply that does not cover every requested path exactly once is treated as unverified. Verification reads only the reservation resource, which current Agent Mail servers provide, and is time-bounded.

- **Reservation overlap compares glob languages** ([7440dbd2](https://github.com/Dicklesworthstone/ntm/commit/7440dbd2), [ab736233](https://github.com/Dicklesworthstone/ntm/commit/ab736233), [0bd96535](https://github.com/Dicklesworthstone/ntm/commit/0bd96535)): `src/*/main.go` and `src/service/*.go` now conflict before `src/service/main.go` exists. One bounded matcher is shared by the coordinator, the Agent Mail staged-file guard and the lock-risk advisor; malformed or over-complex patterns are treated as possibly overlapping, and a lease with no expiry counts as active.

- **Handoff reservation transfer is fenced by lease ID** ([4705ace7](https://github.com/Dicklesworthstone/ntm/commit/4705ace7), [1e75701f](https://github.com/Dicklesworthstone/ntm/commit/1e75701f), [b3a8407d](https://github.com/Dicklesworthstone/ntm/commit/b3a8407d), [712c9832](https://github.com/Dicklesworthstone/ntm/commit/712c9832), [d02d0d23](https://github.com/Dicklesworthstone/ntm/commit/d02d0d23), [4082835e](https://github.com/Dicklesworthstone/ntm/commit/4082835e), [189776eb](https://github.com/Dicklesworthstone/ntm/commit/189776eb)): handoffs capture each reservation's ID and owner; release, renewal and cleanup act on those exact IDs, so a replacement lease on the same path can no longer be released through its old path. Incomplete or unverified grant replies stop cleanup and retries, renewals are read back before being reported, and rollback receipts are kept for recovery.

- **Spawn delivers the latest project handoff** ([1c37fc73](https://github.com/Dicklesworthstone/ntm/commit/1c37fc73)): an ordinary spawn loads the newest readable local handoff for the project and delivers its task, decisions and next steps before the user prompt. JSON recovery status names the source; recovery opt-outs are honoured and remote or mismatched handoffs are rejected.

- **Native context packs and `ntm context inject --build`** ([90efe6c9](https://github.com/Dicklesworthstone/ntm/commit/90efe6c9), [626e81aa](https://github.com/Dicklesworthstone/ntm/commit/626e81aa), [4c3b1886](https://github.com/Dicklesworthstone/ntm/commit/4c3b1886), [bc9b1bec](https://github.com/Dicklesworthstone/ntm/commit/bc9b1bec), [4d77b49c](https://github.com/Dicklesworthstone/ntm/commit/4d77b49c), [ab9a691b](https://github.com/Dicklesworthstone/ntm/commit/ab9a691b), [af9a8041](https://github.com/Dicklesworthstone/ntm/commit/af9a8041)): `ntm context build --files` reads project files, directories and `**` globs directly (no interactive `s2p` needed), within a measured rendered-token budget that keeps XML/Markdown structure closed and redistributes unused budget from small files. `ntm context inject --build` prepares one pack per agent type and delivers it through the agent-aware sender (`--task`, `--bead`, `--files`, `--pack <id>`, `--dry-run`), reporting per-pane delivered/uncertain/not-attempted outcomes; a failed send is no longer reported as a successful injection. Packs with source files are always rebuilt, so dirty edits are never served from cache.

- **Context rotation and compaction confirmations execute and persist** ([993cec1a](https://github.com/Dicklesworthstone/ntm/commit/993cec1a), [61b0fa61](https://github.com/Dicklesworthstone/ntm/commit/61b0fa61), [fc06fcd9](https://github.com/Dicklesworthstone/ntm/commit/fc06fcd9)): CLI and dashboard confirmations run the rotation engine and return its outcome, with receipts persisted across processes (`ntm rotate context confirm --retry` repeats an interrupted one). Rotation waits for a real summary from the original agent, launches the replacement with the original model, effort, persona, worktree and credentials, and keeps the predecessor if the handoff fails.

- **`ntm workflow status` and `ntm workflow recover`** ([8fbb3a7d](https://github.com/Dicklesworthstone/ntm/commit/8fbb3a7d), [20dd37bf](https://github.com/Dicklesworthstone/ntm/commit/20dd37bf), [04448b81](https://github.com/Dicklesworthstone/ntm/commit/04448b81)): inspect a workflow checkpoint (stage, per-pane receipts, a `checkpoint_token`) without a live tmux server, and record an independently verified `delivered` / `not-sent` outcome for one uncertain send so `workflow run --resume` can continue. Each stage's delivery plan is journaled before sending. Review approvals are bound to the stage and pane process, so stale scrollback or a prompt echo cannot advance a workflow.

- **`--robot-sequence advance --sequence-expected-position`** ([494525ac](https://github.com/Dicklesworthstone/ntm/commit/494525ac), [08067007](https://github.com/Dicklesworthstone/ntm/commit/08067007), [6987df6f](https://github.com/Dicklesworthstone/ntm/commit/6987df6f)): a retried or stale advance returns the current prompt instead of skipping one. Sequence state is guarded by per-sequence OS locks, so concurrent processes no longer lose advances.

- **`--robot-dialogs` / `--robot-answer-dialog` handle cursor and hotkey menus** ([82a13bc3](https://github.com/Dicklesworthstone/ntm/commit/82a13bc3)) (refs [#325](https://github.com/Dicklesworthstone/ntm/issues/325)): Claude and Antigravity cursor menus and Grok's immediate y/n workspace gate are inspected and answered, with the selection re-captured and verified before Enter. Reports include `input_mode` and displayed hotkeys.

- **`ntm swarm --json` launches and reports real outcomes** ([70a6222a](https://github.com/Dicklesworthstone/ntm/commit/70a6222a), [7b7abc03](https://github.com/Dicklesworthstone/ntm/commit/7b7abc03)): JSON mode executes the swarm and returns ordered creation, launch, prompt, readiness and cancellation receipts, with a failure status for incomplete runs; plan and dry-run modes stay read-only. `--auto-rotate-accounts` now starts resident account monitors for eligible local Linux Claude and Codex panes and reports their eligibility and health.

- **Operator-gated labels are configurable** ([29d1e7ea](https://github.com/Dicklesworthstone/ntm/commit/29d1e7ea)): `[assign] operator_gated_labels`, globally or in a project `.ntm/config.toml`, adds labels to the built-in gate set. Configuration can only widen the gate, so a repository config cannot un-gate work.

- **Web dashboard** ([522fccd5](https://github.com/Dicklesworthstone/ntm/commit/522fccd5)): a bead detail dialog, Open / In Progress / Blocked / Ready totals that filter the board, pane titles and a `dead` status on the Agents page, and a taller pane viewer.

### Changed

- **`--robot-status` and other robot flag help name the flags that exist** ([97dbfc2d](https://github.com/Dicklesworthstone/ntm/commit/97dbfc2d)): examples taught retired spellings (`--triage-limit`, `--spawn-timeout`, `--files-window`, `--inspect-index`, `--beads-status`); they now use `--limit`, `--timeout`, `--window`, `--index`, `--status`, and `--ready-timeout` is marked deprecated.
- **The `examples/agents/omp.toml` plugin preset is superseded by the built-in type**; built-in agent types take precedence over a plugin of the same name ([99e46df4](https://github.com/Dicklesworthstone/ntm/commit/99e46df4)).
- **`[spawn_pacing.agent_caps]` are documented as what they are** ([d66ff2df](https://github.com/Dicklesworthstone/ntm/commit/d66ff2df)): summed into one host-wide agent budget for robot spawn admission, not per-type launch concurrency.
- **Normal dispatch refuses live workspace-trust prompts** ([d6d088e9](https://github.com/Dicklesworthstone/ntm/commit/d6d088e9)) (refs [#325](https://github.com/Dicklesworthstone/ntm/issues/325)): a Claude, Antigravity or Grok trust menu now returns `PANE_INTERACTIVE_GATE` before any keys are sent, so the prompt is not typed into the menu (or into Grok's immediate quit hotkey) and the assignment can be retried after `--robot-answer-dialog`. ntm never approves trust automatically.

### Fixed

- **`--robot-send --verify-render` with `--enter=false` reported a landed paste as failed on idle Claude panes** (GH #332, [1139b7b0](https://github.com/Dicklesworthstone/ntm/commit/1139b7b0)): the check compared the baseline with one capture taken the instant the keys were written, before Claude Code redrew its composer, and with `--op-id` the false failure was recorded and replayed. A delivered pane whose screen is unchanged is now re-captured every 100ms for up to about 2s, stopping as soon as the render changes.
- **`ntm send` silently failed to deliver long prompts to OpenCode panes while reporting success** (GH #333, [8114b9b5](https://github.com/Dicklesworthstone/ntm/commit/8114b9b5)): prompts were typed with `send-keys -l`, and OpenCode drops typed bursts (multi-KB prompts entirely, sometimes a few hundred bytes), while ntm printed `Sent to 1 pane(s)` and exited 0. OpenCode prompts now always go through bracketed paste; a composer that is still empty before Enter fails the send, and a new submission check confirms the prompt left the composer, with one rescue Enter.
- **The default Claude model was a hard-coded ID that went stale with each model release** (GH #334, [04ba2c7a](https://github.com/Dicklesworthstone/ntm/commit/04ba2c7a), [e7ae5818](https://github.com/Dicklesworthstone/ntm/commit/e7ae5818)): it also overrode the model saved in Claude Code. `models.default_claude` is now empty (see Upgrade notes). Status, robot status and the dashboard still estimate context and cost for such panes against the Claude Opus family, and spawn no longer tells users to set `models.default_claude` for every bare `--cc` pane.
- **The dashboard Cost panel panicked at table widths 36-43** ([#331](https://github.com/Dicklesworthstone/ntm/issues/331), [ac7e550e](https://github.com/Dicklesworthstone/ntm/commit/ac7e550e)): rows carried five cells for four columns (`index out of range [4] with length 4`) once three or more agents were listed. Rows are now built from the column list itself. Reported with a root-cause analysis by @ericziko.
- **`--panes` selectors behave the same on every robot verb** ([#329](https://github.com/Dicklesworthstone/ntm/issues/329), [32b64590](https://github.com/Dicklesworthstone/ntm/commit/32b64590), [b470fc2f](https://github.com/Dicklesworthstone/ntm/commit/b470fc2f)): `--robot-probe`, `--robot-watch-bead`, `--robot-monitor` and `--robot-errors` rejected `W.P` and `%N` selectors or matched bare `N` in every window, and `--robot-restart-pane` / `ntm respawn --panes` silently ignored a malformed selector and restarted only the rest. All now use the shared grammar and fail with `INVALID_FLAG` or `PANE_NOT_FOUND` before touching any pane.
- **The web dashboard showed no sessions or agents while agents were running** ([bf1b5872](https://github.com/Dicklesworthstone/ntm/commit/bf1b5872), [3b731728](https://github.com/Dicklesworthstone/ntm/commit/3b731728), [84b0a4fe](https://github.com/Dicklesworthstone/ntm/commit/84b0a4fe)): `/api/v1/sessions` read only a store the spawn path never writes; it now merges live tmux sessions (bounded to 5s, answering 504/503 rather than hanging or 500 when tmux fails). The agents endpoint returns the `id`/`name`/`type` fields the Agents page reads, and bead endpoints accept child IDs such as `bd-1aae9.1`.
- **Respawn dropped a pane's model pin** ([67a33ee0](https://github.com/Dicklesworthstone/ntm/commit/67a33ee0)): `ntm respawn`, `--robot-restart-pane` and `health --auto-restart-stuck` relaunched with an empty template, so a pane pinned to a model came back on the default with no warning.
- **`ntm doctor --json` exited 0 on an unhealthy ecosystem** ([e032074c](https://github.com/Dicklesworthstone/ntm/commit/e032074c)); it now exits non-zero while still printing exactly one JSON document.
- **`ntm config get` called live keys unknown** ([6982ca94](https://github.com/Dicklesworthstone/ntm/commit/6982ca94), [7af5ea75](https://github.com/Dicklesworthstone/ntm/commit/7af5ea75)): `agents.claude_isolate_credentials`, `agents.claude_token_file`, `agent_mail.pane_badges` and every key under `[command_hooks]`, `[retry]` and `[routing]` answered "unknown config path". Every TOML key is now readable, and removed keys are named as such.
- **Pane interrupt, pane selection, `--verify-boot` and smart stagger** ([cb4897b2](https://github.com/Dicklesworthstone/ntm/commit/cb4897b2), [f6a9f9df](https://github.com/Dicklesworthstone/ntm/commit/f6a9f9df), [4b30a7ae](https://github.com/Dicklesworthstone/ntm/commit/4b30a7ae), [d66ff2df](https://github.com/Dicklesworthstone/ntm/commit/d66ff2df)): diagnose, ensemble stop and the REST pane interrupt typed the literal text `C-c` into panes instead of sending Ctrl+C; `--robot-markdown` session tables counted OpenCode panes as "other"; `ntm spawn --verify-boot` timed out on healthy panes of every agent type that started working on a delivered spawn prompt (such panes now count as booted, while unprompted panes must still be idle); `--robot-is-working` (and `--robot-agent-health`, `--robot-smart-restart`, `--robot-monitor` defaults) dropped the lowest-index pane of every multi-pane window even when ntm had recorded it as an agent; and `--stagger-mode=smart` staggered omp-only spawns on Anthropic's backoff instead of omp's own bucket.
- **Robot waits and stall diagnosis no longer certify what they did not observe** ([da56121a](https://github.com/Dicklesworthstone/ntm/commit/da56121a), [7e0132de](https://github.com/Dicklesworthstone/ntm/commit/7e0132de), [44a9e758](https://github.com/Dicklesworthstone/ntm/commit/44a9e758), [e47d9dfa](https://github.com/Dicklesworthstone/ntm/commit/e47d9dfa), [dfa214de](https://github.com/Dicklesworthstone/ntm/commit/dfa214de), [8317f7c1](https://github.com/Dicklesworthstone/ntm/commit/8317f7c1)): an ALL wait no longer completes on a pane it could not read, and attention evidence is kept across polls and replay pages. `suspected_wedge` requires both git and bead evidence; a malformed or failed `br` read is unavailable evidence rather than zero work. Productivity observations share one deadline, cap subprocess output at 8 MiB and collect evidence concurrently.
- **`--robot-probe` could report an operational failure as a stuck agent** ([ad5beca9](https://github.com/Dicklesworthstone/ntm/commit/ad5beca9)): probes are bound to the physical pane ID, recheck identity after slow captures, report capture or send errors as unknown rather than unresponsive, and never escalate on them.
- **Session and checkpoint restore could leave broken panes or loop** ([2b44cba9](https://github.com/Dicklesworthstone/ntm/commit/2b44cba9), [b8508e5f](https://github.com/Dicklesworthstone/ntm/commit/b8508e5f)): saved topology, directories and commands are validated before a session is replaced, and per-pane launch failures are reported. A pane captured as a bare `node`/`bun`/`python` relaunches the recorded agent instead of a REPL, and a slow-starting wrapper is no longer killed repeatedly with `respawn-pane -k`.
- **Checkpoint export and import were not atomic** ([6ac8a230](https://github.com/Dicklesworthstone/ntm/commit/6ac8a230), [5df672f8](https://github.com/Dicklesworthstone/ntm/commit/5df672f8), [091618ca](https://github.com/Dicklesworthstone/ntm/commit/091618ca)): exports are staged and renamed into place (a failed export no longer destroys the previous file) with scrollback kept 0600. Import verifies the gzip trailer so a truncated or corrupt archive is rejected, and on Linux and macOS publishes a whole checkpoint directory in one atomic exchange; other systems stage create-only imports and refuse overwrite.
- **A pipeline could keep dispatching after its checkpoint failed to save** ([82dda265](https://github.com/Dicklesworthstone/ntm/commit/82dda265), [276f7593](https://github.com/Dicklesworthstone/ntm/commit/276f7593), [b56c0240](https://github.com/Dicklesworthstone/ntm/commit/b56c0240)): a checkpoint write failure now stops the run, even with `on_error: continue`. Background run status keeps the fatal cause after the worker exits, and a later failure is not hidden behind an earlier warning.
- **Pipeline subprocesses and pane locks** ([6585b817](https://github.com/Dicklesworthstone/ntm/commit/6585b817), [6f769c82](https://github.com/Dicklesworthstone/ntm/commit/6f769c82), [e46330ef](https://github.com/Dicklesworthstone/ntm/commit/e46330ef), [bc2f2e5c](https://github.com/Dicklesworthstone/ntm/commit/bc2f2e5c), [35e5e379](https://github.com/Dicklesworthstone/ntm/commit/35e5e379)): on Linux, cancellation waits for and kills every member of a command's process group instead of treating shell exit as completion; branch predicates, foreach sources and `br` queries have bounded output. The per-pane dispatch lock now also holds across different project roots and when no project directory is set. An async REST pipeline job with `background=true` stays attached to its execution until it finishes.
- **Webhook delivery** ([dd9e4658](https://github.com/Dicklesworthstone/ntm/commit/dd9e4658), [3f08c83e](https://github.com/Dicklesworthstone/ntm/commit/3f08c83e)): redirects and non-2xx responses are failures, `Retry-After` is honoured and 408 is retried; retries run on the worker pool so one slow endpoint no longer blocks the rest, and the retry backlog is bounded, with dead letters kept on overflow and shutdown.
- **Event log history could be lost** ([c295f23d](https://github.com/Dicklesworthstone/ntm/commit/c295f23d), [276e2fdb](https://github.com/Dicklesworthstone/ntm/commit/276e2fdb), [4e200503](https://github.com/Dicklesworthstone/ntm/commit/4e200503)): a failed retention rotation no longer drops history; appends and rotation are coordinated across ntm processes so one process cannot write to a file another just replaced; and a partially written record no longer swallows the next event.
- **`ntm history` pruning, export and filtering** ([44263204](https://github.com/Dicklesworthstone/ntm/commit/44263204), [2a3cf187](https://github.com/Dicklesworthstone/ntm/commit/2a3cf187), [99cf6174](https://github.com/Dicklesworthstone/ntm/commit/99cf6174)): pruning stops on an incomplete read or wrong key instead of rewriting a partial file; plaintext exports are written atomically at 0600 and cannot target the history file itself; session and search filters combine correctly, and full-ID lookups no longer return null or panic.
- **`ntm rotate --preserve-context` continued before re-login finished** ([9dbcf5b4](https://github.com/Dicklesworthstone/ntm/commit/9dbcf5b4), [4b00973f](https://github.com/Dicklesworthstone/ntm/commit/4b00973f)): old scrollback from a previous login could be read as success. The wait now tracks only the current attempt and requires completion.
- **Daemon supervision** ([06783b31](https://github.com/Dicklesworthstone/ntm/commit/06783b31), [5d013e67](https://github.com/Dicklesworthstone/ntm/commit/5d013e67), [130043e8](https://github.com/Dicklesworthstone/ntm/commit/130043e8)): failed relaunches are retried within the restart budget instead of leaving the daemon down; two ntm processes can no longer both launch the same project daemon; health probes are tied to the daemon generation they check and reject truncated or mismatched responses. `ntm memory serve` reports the real failure after exhausting retries.
- **Robot send memory could come from the wrong project** ([fc17a38f](https://github.com/Dicklesworthstone/ntm/commit/fc17a38f)): memory retrieval and feedback are resolved from the target session's panes and project, not the caller's directory.

---

## [v1.35.1] -- 2026-09-14 [GitHub Release]

**Seven defects in v1.35.0's own file-change capture, found reviewing it with fresh eyes. No schema change; upgrading is a binary swap.**

v1.35.0 replaced surfaces that reported nothing with surfaces that record for real. Reviewing that work turned up seven ways the new code could report nothing, or claim to have recorded something it had not — the same class it was written to fix.

### Fixed

- **Readers resolved the project from the working directory instead of the one they were handed** ([3bf417f3](https://github.com/Dicklesworthstone/ntm/commit/3bf417f3), [c8dc6273](https://github.com/Dicklesworthstone/ntm/commit/c8dc6273)): the ledger is keyed by project, and `RecordedChanges` derives that key by running `git rev-parse` against the current directory. That is correct for a one-shot command run inside the project and wrong for everything else. The dashboard is launched from anywhere and displays a session whose project directory may come from that session's overlay, so its Files panel read a different project's ledger — usually none — and showed nothing while the monitor recorded normally; it also paid for a subprocess on every refresh tick. `--robot-metrics` takes a session explicitly and is routinely run from elsewhere, so `files_changed` reported zero for any session outside the caller's checkout. Both now pass the project they already hold, through `tracker.ChangesForProject` / `ChangesForProjectSince` / `ConflictsForProject`; `--robot-metrics` resolves it from the named session's manifest. The working-directory resolution is memoized, since it only changes if the process chdirs.

- **A failed ledger write silently dropped the changes it failed to write** ([3bf417f3](https://github.com/Dicklesworthstone/ntm/commit/3bf417f3)): `Sample` advanced its baseline before persisting, so a transient busy database lost those changes permanently — the next sample compared against a state whose changes had never been recorded anywhere. `AppendFileChanges` is a single transaction, so a failure wrote nothing; persisting first and advancing only on success costs a re-detect on the next tick instead.

- **The ledger key could differ between writer and reader** ([3bf417f3](https://github.com/Dicklesworthstone/ntm/commit/3bf417f3)): a session manifest's project directory is not guaranteed to be the repository root, while a reader keys by its git toplevel. A session rooted at a subdirectory would have been written under one key and read under another, and every surface would have reported nothing while recording worked perfectly. The recorder now resolves its key exactly as a reader resolves its own location.

- **The monitor's recorder set was fixed at startup** ([3bf417f3](https://github.com/Dicklesworthstone/ntm/commit/3bf417f3)): `ntm add` can give a running session new agents, and with worktree isolation new working trees. A set built once kept attributing everything to the session — and never looked inside the new trees at all, because worktrees live under the gitignored `.ntm`, so that work went entirely unrecorded. The sampler re-derives the set each tick and keeps the recorders it already has, since dropping one would discard its baseline and silently re-baseline past the changes since the last tick. Same build-once defect fixed for the resilience monitor's watched set in v1.34.0.

- **Recording with no project directory reported success** ([b262a179](https://github.com/Dicklesworthstone/ntm/commit/b262a179)): the ledger is keyed by project, so those rows would be written nowhere and read back by nobody while the caller advanced its baseline and reported them recorded. Unreachable today — `Sample` refuses an empty root — but guarded, because the cost of being wrong is losing changes while claiming to have stored them.

- **The dashboard cost column was too narrow for its own contents** ([3bf417f3](https://github.com/Dicklesworthstone/ntm/commit/3bf417f3)): v1.35.0 added a `~` prefix and a pricing-confidence marker, each a column wide, to a field sized at eight. `~$123.45?` rendered as `~$123.4` — a figure that reads as precise and is not even the right number.

- **The send-idempotency check matched schema text literally** ([3bf417f3](https://github.com/Dicklesworthstone/ntm/commit/3bf417f3), [242b8435](https://github.com/Dicklesworthstone/ntm/commit/242b8435)): `sqlite_master` stores `CREATE TABLE` text exactly as the migration wrote it, so reflowing migration 018 would have made `ntm doctor` report the guarantee missing when it was intact. The match is whitespace-tolerant now. The same check also read the schema through `Migrate()`, so an invariant check rewrote the operator's database as a side effect of being asked a question; it reads without migrating, and distinguishes a schema that has simply never been initialized (reported unverified) from `send_operations` absent from an initialized one (still an error), which would otherwise have failed every fresh install.

---

## [v1.35.0] -- 2026-09-13 [GitHub Release]

**Output that reported things nothing had measured — six fabricated `doctor` ticks, two commands that could only ever return empty, a success rate over zero tasks — plus a redaction setting that never reached the REST API and a `metrics snapshot save` that had never once succeeded.**

Most of this release is one defect repeated: a surface that reports a verdict, a count or a statistic it never took. A check that cannot fail and a counter that nothing increments both read as good news, which is worse than an error, because an operator scanning for problems finds none.

**Upgrading:** two schema migrations apply on first run. `022_file_changes` adds the cross-process ledger described below. `023_metrics_session_fk` rebuilds `blocked_commands` and `metric_snapshots` without their foreign keys — those referenced a `sessions` table that nothing in the shipped binary has ever written, so every insert into both was rejected; the rebuild copies existing rows and preserves them. `ntm_file_conflicts_total` is still exported under that name but is now declared a **gauge** rather than a counter; anything computing `rate()` or `increase()` over it was reading resets that never happened and should switch to the raw value.

### Added

- **File changes are recorded again, so `ntm changes` and `ntm conflicts` can answer** ([32329213](https://github.com/Dicklesworthstone/ntm/commit/32329213)): the capture pipeline behind `tracker.GlobalFileChanges` was deleted as dead code in v1.27.0 and its last writer swept up in v1.29.0, leaving six read surfaces — `ntm changes`, `ntm conflicts`, `--robot-status`, `--robot-metrics`, the dashboard Files panel and the work-coordination adapter's `FileConflicts` input — reporting a clean all-clear no matter what the agents did. Verified against the shipped v1.34.0 binary on a host running eleven live agent sessions: `ntm conflicts` printed "No conflicts detected." as a structural guarantee, not an observation. Capture is now sampling rather than the deleted design's directory walk: the session monitor, which already runs one process per session for that session's lifetime, samples every 15s with a `git status --porcelain -z --untracked-files=all` plus one stat per dirty file, so git narrows the candidate set (honouring `.gitignore`) and the stats distinguish a second edit from a file that merely stayed dirty. Attribution is read off the filesystem and never guessed: a session using worktree isolation gets one recorder per agent tree, because git can then say exactly who touched a file, while a shared checkout gets a single recorder attributed to the session, because git cannot. That distinction is load-bearing — conflict detection treats three distinct agents on one path as critical, so attributing a shared tree's edits to every agent in the session would report one agent editing one file twice as a critical three-way conflict. Recorded paths are repo-relative, which is what lets the same file compare equal across two agents' worktrees; absolute paths would never match and no conflict could ever be found. The store also had to become durable: it was a per-process in-memory ring, and the process that observes changes is the monitor while every reader is a different process, so nothing could ever reach a reader. Changes now go to a project-scoped `file_changes` table in the `state.db` every ntm process already shares, pruned on write, with the in-memory ring kept as the same-process fallback.

- **`ntm worktrees remove` confirms before destroying work, and takes `--force`** ([e6ab8e7f](https://github.com/Dicklesworthstone/ntm/commit/e6ab8e7f)): the command runs `git worktree remove --force` followed by `git branch -D`, so neither git nor ntm refused on dirty or unmerged work — while its own help text warned it "will permanently delete any uncommitted changes". Its sibling `worktrees clean` has always confirmed before removing many worktrees; this removed one just as permanently, silently. It now reports what is about to be lost — uncommitted files, and commits unreachable from the default branch — and asks. `--force` skips the prompt; JSON mode refuses rather than prompting into the caller's stream. An assessment that fails reads as "unknown", never as "nothing at risk", because treating an inspection error as zero work is how a prompt talks an operator into deleting something it never managed to look at. Counting uses `--untracked-files=all`: the default collapses an untracked directory into one entry, so fifty new files were announced as one. Note `ntm worktree remove` (singular) is a different code path and was never affected — it omits `--force`, so git itself refuses a dirty tree.

- **`--robot-metrics` declares which of its fields nothing measures** ([9b998154](https://github.com/Dicklesworthstone/ntm/commit/9b998154)): the payload now carries an `unmeasured` list naming every field with no writer, so a consumer can tell "no activity" from "never measured". Additive and `omitempty`, so no existing consumer breaks.

### Fixed

- **Redaction reaches the REST API and the WebSocket feed** ([e6ab8e7f](https://github.com/Dicklesworthstone/ntm/commit/e6ab8e7f)): `redactionMiddleware` has been mounted since v1.20.x and `WSHub.broadcastEvent` reads a redaction config too, but nothing outside tests ever wrote either field — the G1 dead-code gate noticed the setters had no callers and deleted them in v1.29.0, removing the evidence rather than the bug. An operator setting `redaction.mode = "redact"` or `"block"` got scrubbed prompt history, event logs, audit records, session prompts and checkpoints while the REST API and WebSocket feed shipped secrets in the clear. `serve.Config` now carries the redaction config and `New` deep-copies it into both consumers. Only `redact` and `block` activate it: `warn` is the shipped default and merely logs, so honouring it here would add a regex scan to every HTTP response and every pane-output frame for every user while redacting nothing. Default behaviour is unchanged. The pre-existing redaction tests all construct the wired state by hand and passed throughout; the new ones go through `New`.

- **`ntm metrics snapshot save` works, and blocked commands are counted** ([a2147382](https://github.com/Dicklesworthstone/ntm/commit/a2147382)): `blocked_commands.session_id` and `metric_snapshots.session_id` were declared `REFERENCES sessions(id)` and the store enables `PRAGMA foreign_keys`, but nothing in the shipped binary ever inserts into `sessions` — `state.Store.CreateSession` has no production caller, because the live session model is `runtime_sessions`. Every insert into both tables was therefore rejected by the foreign key. `RecordBlockedCommand` discarded the error, so the Tier-0 `destructive_cmd_incidents` metric counted zero incidents forever; `ntm metrics snapshot save` surfaced it as a raw `constraint failed: FOREIGN KEY constraint failed (787)` and had never once succeeded. Measured on a live 3.2 MB `state.db`: every table carrying a foreign key to `sessions`/`agents` held zero rows, while every table without one was populated. Migration 023 rebuilds the two tables that have writers. The read side was wrong too — `ntm metrics` builds a collector and reports immediately, so the in-memory counter it reported was whatever that one-shot process had observed, which is zero. It now prefers the persisted per-session count.

- **`ntm doctor` stops reporting six design invariants it never checked** ([4bae2022](https://github.com/Dicklesworthstone/ntm/commit/4bae2022), [595bf3cf](https://github.com/Dicklesworthstone/ntm/commit/595bf3cf)): every one of the six checks ended in an unconditional `Passed = true; Status = "ok"` — there was not one conditional verdict in the file, so the section printed six green ticks on every machine, in every repository, forever. Three of them gathered real evidence and discarded it: the No Silent Data Loss tick printed directly above its own detail line reading "no pre-commit hook (run ntm guards install)". `checkSafeByDefault` opened nothing at all and asserted "automation.auto_commit defaults to false" as fixed prose, which is also untrue — the default policy sets it true — so an operator who had enabled auto-push was still told "safe defaults enforced by policy engine". All six are now measured: the guard's actual presence, `tmux` actually on PATH, a write probe against the audit directory, the effective policy's real `auto_push`/`auto_commit`/`force_release` values, whether any tool is unavailable while nothing reported it, and whether the `send_operations` composite key that makes `ntm send` retry-safe is present in the live schema. The test suite had encoded the lie — a test named `TestInvariantViolationDetection` asserted that two checks "should always pass" — and now asserts each can fail.

- **`ntm doctor` stops ticking green for daemons that are not running** ([99486db0](https://github.com/Dicklesworthstone/ntm/commit/99486db0)): both arms of the daemon check set status "ok", so a daemon that was up reported "listening on port 8765" and a daemon that was down reported "port 8765 available" — both with a green tick, including one the operator had enabled in config. A daemon that is down is now a warning when it is expected and an unjudged "?" when it is not.

- **`ntm agents` stops reporting a 90% success rate over zero tasks** ([285bb580](https://github.com/Dicklesworthstone/ntm/commit/285bb580)): the profile view printed "Tasks Completed: 0" directly above "Success Rate: 90.0%". Nothing in the binary writes agent performance; the rate is a seeded routing prior marked "Default assumption" in the source. Both the profile view and the `agents stats` table now respect whether any outcome was actually recorded, and say "No task outcomes recorded yet" rather than inventing a percentage.

- **Workflow notifications actually fire** ([57fb5af1](https://github.com/Dicklesworthstone/ntm/commit/57fb5af1)): `notify_on_complete`, `notify_channels`, `webhook_url` and `mail_recipient` parsed, the 400-line implementation was complete and tested, and the executor had five call sites ready — but nothing outside tests ever called `SetNotifier`, so the notifier was nil on every path and each notification returned at the first line. Wiring it exposed two problems the always-nil notifier had been hiding: four of the five call sites invoked notification while holding the state lock, which would have held it across a 10s webhook POST, and the fifth read execution state after releasing it. Payloads are now snapshotted under the lock and delivered without it.

- **`--robot-metrics` no longer reports an idle session** ([9b998154](https://github.com/Dicklesworthstone/ntm/commit/9b998154)): on a session with twelve agents that had been working for hours it returned zero tokens, zero cost, zero prompts received and empty uptime for every agent, because nothing populates any of it. `session_stats.files_changed` was separately wrong — it read the per-process in-memory ring, always empty in a one-shot CLI — and now reads the durable ledger.

- **The metrics collector no longer crashes without a state store** ([a2147382](https://github.com/Dicklesworthstone/ntm/commit/a2147382)): `getDB` asserted a possibly-nil `*state.Store` to an interface and called `DB()` on it. A nil pointer inside an interface is not a nil interface, so that dereferenced nil instead of returning "no storage" — and the metrics command constructs exactly that collector when `state.db` cannot be opened, so a blocked command on such a machine took the process down.

### Changed

- **`ntm_file_conflicts_total` is a gauge** ([ef97af65](https://github.com/Dicklesworthstone/ntm/commit/ef97af65), [06dcd3e2](https://github.com/Dicklesworthstone/ntm/commit/06dcd3e2)): it holds the number of conflicts inside the tracker's rolling window, so it falls as old conflicts age out, and Prometheus reads any decline in a counter as a reset — `rate()` and `increase()` over it reported traffic that never happened. The name is deliberately unchanged so existing dashboards and alerts keep resolving, which means the `_total` suffix now contradicts Prometheus convention on purpose; the HELP text says so. Its description also claimed "reservation conflicts" — these are file *edit* conflicts, a different thing from the lease conflicts in the `file_conflicts` table.

- **The dashboard cost panel says it is an estimate** ([cc8fd73e](https://github.com/Dicklesworthstone/ntm/commit/cc8fd73e), [115c191e](https://github.com/Dicklesworthstone/ntm/commit/115c191e)): it was titled "Cost Tracking" and rendered figures like `$0.0137`. Neither claim survives how the number is made — token counts are a ~3.5-chars-per-token estimate over scraped pane scrollback, including box drawing and redraws, multiplied by a static price table, with provider billing never consulted. It is now titled "Cost (estimated)" with the basis stated under the totals, amounts render as `~$1.24` or `<$0.01` rather than to a hundredth of a cent, and the price table's vintage is shown because it predates most of the models it prices. Rows priced from a family prefix rather than the model itself are marked `~`, and rows that fell through to the default row are marked `?` — the previous release's boolean reported prefix matches as known prices, so `claude-opus-5` was presented as a verified rate while being charged at the `claude-opus` figure. Provider-reported costs in `--robot-quota-status` are real and unaffected.

- **Design invariants can report `unverified`** ([4bae2022](https://github.com/Dicklesworthstone/ntm/commit/4bae2022)): a check with no evidence available now renders as "?" rather than a tick, which `doctor` already displayed for unknown statuses.

---

## [v1.34.0] -- 2026-09-12 [GitHub Release]

**Opt-in CAAM seat selection, an opt-out for the `cc` alias, and the seven issues filed against v1.33.x — plus a review pass that closed two cross-process races, a silent checkpoint data loss, and a hole in the auto-restart guarantee** (GitHub issues [#313](https://github.com/Dicklesworthstone/ntm/issues/313), [#314](https://github.com/Dicklesworthstone/ntm/issues/314), [#315](https://github.com/Dicklesworthstone/ntm/issues/315), [#316](https://github.com/Dicklesworthstone/ntm/issues/316), [#319](https://github.com/Dicklesworthstone/ntm/issues/319), [#320](https://github.com/Dicklesworthstone/ntm/issues/320), [#321](https://github.com/Dicklesworthstone/ntm/issues/321), [#322](https://github.com/Dicklesworthstone/ntm/issues/322), [#323](https://github.com/Dicklesworthstone/ntm/issues/323), [#324](https://github.com/Dicklesworthstone/ntm/issues/324)).

**Upgrading:** if your config sets `memory.include_in_recovery` or `memory.max_rules`, the strict loader now refuses it until you run `ntm config migrate` (a backup is kept). Both keys were aliases into `[recovery]`, whose defaults are identical, so a config that never set them is unaffected — see **Changed** below.

### Added

- **CAAM seat selection for unpinned spawn/add panes, opt-in** ([#319](https://github.com/Dicklesworthstone/ntm/issues/319)): `[integrations.caam] seat_selection = true` makes a `spawn`/`add` pane that carries no persona ask `caam limits <provider> --rank earliest-reset-headroom` and launch under the matching `codex-<profile>` / `claude-<profile>` persona, so the included allowance that refreshes soonest is spent while it still has headroom and the later-resetting reserve seat is preserved. Before this, a bare `--cod=1` / `--cc=1` left `PersonaName` empty and the agent command template's else-branch pinned a static profile — on a three-seat pool, the reserve seat the quota law exists to protect. Default **off**: with the key unset nothing calls caam, no subprocess starts, and every launch path behaves exactly as before. An explicit `--persona` / `--profile-set` / recipe persona is never offered to the ranker, so a pin can never be re-ranked away. One caam invocation per provider per command, not one per pane. A pool that cannot be ranked — caam missing, a timeout, or caam answering that nothing has included headroom — **skips the affected panes with the reason reported** (`seat_skips` in `--json`, a warning otherwise) rather than aborting the batch or falling through to a static pin; siblings on a provider that answered still launch, so `ntm add --cod=2 --cc=2` against an exhausted Codex pool brings up the two Claude panes. `ntm add` resolves seats before the first pane is split, so a refused pane is never created and the reported added-count is the count that launched; `ntm spawn` resolves them after the panes exist, so a refused one is left as a plain shell and named in the warning. `ntm scale` reads the same outcome rather than crediting the requested count. Seat selection fills the model only when the operator did not request one, so `--cod=1:gpt-5.1-codex-max` keeps its model and a seat persona that declares none never demotes a pane to the config default. The key is named for the policy rather than for caam so it does not read as a sibling of `auto_failover`, which it does not interact with. Claude and Codex only — `caam limits` answers for no other provider, and panes of every other agent type are untouched. Requires caam >= v0.1.19.

- **Shell integration without the agent aliases, so `cc` stays the C compiler** ([#314](https://github.com/Dicklesworthstone/ntm/issues/314), [53855970](https://github.com/Dicklesworthstone/ntm/commit/53855970)): `ntm shell {zsh,bash,fish}` always emitted `alias cc=…`, which shadows `/usr/bin/cc` — the conventional C compiler — in every interactive shell, with no way to decline short of giving up the integration entirely. `--no-agent-aliases` omits `cc`, `cod` and `gmi` while keeping everything else: the ntm command aliases, the cobra-generated completions, the short-alias session completer and the F6 palette binding. The three generators previously carried three copies of the alias block, which is why the flag needed one seam rather than three; they now emit through a single helper parameterized by each shell's `alias k=v` / `alias k v` syntax, and the test asserts both syntaxes are absent under the flag *and* that the rest of the integration survives, per shell.

### Fixed

- **`--robot-account-status --provider=codex` sees the Codex pool, and both robot surfaces carry live CAAM windows** ([#319](https://github.com/Dicklesworthstone/ntm/issues/319), [bfcc31ce](https://github.com/Dicklesworthstone/ntm/commit/bfcc31ce)): `canonicalRobotProvider` folded the claude and gemini spellings but had no case for Codex, and ntm stores Codex accounts under the provider id `openai`, so a `codex` filter matched nothing and reported `available_accounts=0` with an empty `current` on a host with three healthy seats. `codex`, `cod`, `chatgpt`, `openai-codex` and `openai` now all fold onto `openai` (and `cc` onto `claude`), reaching accounts-list, switch-account and OAuth health, which had the same miss. `--robot-account-status` gained `recommended_seat` / `recommended_persona` / `tier` / `usage_percent` / `governing_window` / `resets_at` plus a per-profile `seats` array, and reports `limits_error` with no recommendation when the pool cannot be read, so an unreadable pool never looks like a healthy one. `--robot-quota-status` now includes CAAM's subscription windows for Claude and Codex, with caut authoritative wherever it has a figure.
- **A dispatched prompt is confirmed out of the composer before the pipeline waits on it** ([#320](https://github.com/Dicklesworthstone/ntm/issues/320), [2a0d277e](https://github.com/Dicklesworthstone/ntm/commit/2a0d277e)): pipeline dispatch pasted a prompt and went straight to waiting for the agent to finish, so a TUI that swallowed the submission (the Codex/Claude autocomplete-picker and double-Enter cases) left the text sitting in the composer while the step waited out its timeout on work that had never started. Dispatch now verifies the payload actually left the composer, sending one rescue Enter when it finds its own text still stranded there.
- **Rotated Agent Mail registration tokens survive, and a restarted pane's identity is restored before launch** ([#321](https://github.com/Dicklesworthstone/ntm/issues/321), [3df116cc](https://github.com/Dicklesworthstone/ntm/commit/3df116cc)): a token rotated by the server was used for the rest of the process and then discarded, so the next invocation authenticated with the stale one; and a restarted pane relaunched before its identity was re-registered, so the new process came up without one.
- **`--robot-snapshot` stops executing disabled integrations** ([#313](https://github.com/Dicklesworthstone/ntm/issues/313), [6fa495b6](https://github.com/Dicklesworthstone/ntm/commit/6fa495b6)): with `[cass] enabled = false`, a snapshot still shelled out to `cass --version` and `cass health --json`, spending seconds inside a tool the operator had turned off. The defect was never cass-specific — the tool registry probed every adapter and consulted no configuration — so the fix is at the registry: `cass`, `cm`, `am`, `rch`, `pt`, `rano` and `xf` are now reported as `"disabled": true` without being probed, distinct from `installed: false`, which means "we looked and it is not on PATH". Every one of those toggles defaults to on, so this changes behaviour only for an operator who explicitly turned something off.
- **The zsh integration no longer prints `command not found: compdef`** ([#315](https://github.com/Dicklesworthstone/ntm/issues/315), [53855970](https://github.com/Dicklesworthstone/ntm/commit/53855970)): the short-alias `compdef` registration sat outside the `(( $+functions[compdef] ))` guard that wraps the completion source, so a login shell that has not run `compinit` printed an error while evaluating `ntm shell zsh`. Both `compdef` calls are now inside one guard, and the regression test walks the generated script's guard nesting rather than grepping for the line, so any future unguarded `compdef` fails the build wherever it is added.
- **`ntm doctor` agrees with robot Mail about an authenticated Agent Mail server** ([#316](https://github.com/Dicklesworthstone/ntm/issues/316), [b7065382](https://github.com/Dicklesworthstone/ntm/commit/b7065382)): the doctor's Agent Mail probe kept its own hard-coded `127.0.0.1:8765` base URL and sent an unauthenticated GET, so a correctly auth-walled endpoint was reported unhealthy while robot Mail talked to it happily; its `SetServerURL` setter had never been called by any production code. The probe now goes through the same client every other Agent Mail surface uses, honouring `[agent_mail] url`/`token` and attaching the bearer. While wiring it, the config-to-client conversion turned out to be open-coded at five call sites that disagreed — `AGENT_MAIL_TOKEN` beat the config file under `ntm mail` and lost under `ntm spawn` — so all five now share one `agentmail.ConfigOptions`, which is the single place that precedence rule lives.
- **Dashboard "Recent Output" shows the transcript, not the status line** ([#322](https://github.com/Dicklesworthstone/ntm/issues/322), [377f340d](https://github.com/Dicklesworthstone/ntm/commit/377f340d)): the Detail view rendered a blind trailing 200-character truncation of the pane capture. Agent TUIs pin their chrome to the bottom of the pane, so on a Claude pane with a two-line status line — itself longer than the preview window — the section was 100% status line and none of the agent's output. Previews are now chrome-aware, anchored on the composer box: a status line is arbitrary user-configured text that cannot be recognised on its own, but it always renders *below* the input box. A boxed diff in the transcript is not mistaken for the composer (the row must carry the composer marker), a markdown blockquote is not either (`>` counts only inside a bordered row), and the upward walk stops at another box's closing rule so a transcript box glued to the composer is not absorbed. When no composer is found the raw tail is returned unchanged — trimming is a refinement, never a reason to show nothing. `--robot-interrupt`'s last-output field had grown its own partial copy of this and now shares the one implementation.
- **Pipeline dispatch is exclusive against a pane across ntm processes** ([#324](https://github.com/Dicklesworthstone/ntm/issues/324), [652d3574](https://github.com/Dicklesworthstone/ntm/commit/652d3574)): the per-pane dispatch lock was an in-memory map on the executor, and every `ntm pipeline run` builds a fresh executor, so two pipeline processes aimed at one pane had no mutual exclusion at all — after an interruption the older prompt could run in place of the assignment ntm had just reported delivered. This is a different guarantee from [#320](https://github.com/Dicklesworthstone/ntm/issues/320), which verifies *this* call's payload left the composer and says nothing about who owns the pane next. Dispatch now takes an advisory file lock per pane under the project's `.ntm/pipelines/locks/`, layered outside the existing in-process lock; the OS releases it when the holder dies, so a pipeline killed mid-dispatch cannot wedge a pane. On contention the later pipeline waits, then fails the step with a typed `pane_busy_other_process` reason naming the pane and stating that nothing was sent — the lock is taken before the first capture, so a refusal means no prompt was pasted and none was queued. The project path is canonicalized so two processes that spell it differently (a relative path, or a symlinked `/var` vs `/private/var`) still meet on the same lock file. `--dry-run` takes no file lock: it dispatches nothing, must not write under `.ntm`, and every dry-run step reports the same synthetic pane id.
- **Audit records carry the real source address** ([d7e062b1](https://github.com/Dicklesworthstone/ntm/commit/d7e062b1)): `ntm serve` used chi's `RealIP` middleware, which rewrites the remote address from `True-Client-IP` / `X-Real-IP` / the leftmost `X-Forwarded-For` of whatever client sent them, with no trusted-proxy allowlist — chi has deprecated it for exactly that reason. That value is persisted as the audit record's remote address for every mutating request, so any caller could stamp a forged origin onto the audit trail of a dangerous or approval-gated action with one header. ntm terminates connections directly, so the socket address is the truthful answer; a future reverse-proxy deployment needs a configured trusted-proxy list rather than blanket header trust.
- **Round-robin routing no longer loses a turn under concurrent sends** ([d7e062b1](https://github.com/Dicklesworthstone/ntm/commit/d7e062b1), [a1b24971](https://github.com/Dicklesworthstone/ntm/commit/a1b24971)): selecting the next agent is a read-modify-write over the persisted cursor — load it, pick from it, store it — across two separately-locked store calls, and every `ntm send` opens its own store. Two concurrent sends both read cursor N, both selected agent N+1 and both wrote N+1, so one agent took both messages while the agent whose turn it was was skipped. The write is now guarded on the row the decision was routed from, and a lost race re-selects rather than dispatching on a stale cursor; persistence happens before the send, so a retry costs nothing.
- **A checkpoint no longer reports success when the git patch could not be written** ([855889ca](https://github.com/Dicklesworthstone/ntm/commit/855889ca)): the error from saving the uncommitted diff was discarded, so a full disk, a quota or an EIO produced a checkpoint recorded as complete — dirty, with no patch file — while the work was gone, and nothing said so in the metadata, the logs, or `Verify()`. The uncommitted diff is the single artifact a checkpoint exists to preserve; a failed write is now recorded as a failed git capture, the same way a failed `git diff` already was.
- **Agents added to a running session are auto-restarted** ([855889ca](https://github.com/Dicklesworthstone/ntm/commit/855889ca), [a1b24971](https://github.com/Dicklesworthstone/ntm/commit/a1b24971)): the resilience monitor's watched set was populated exactly once, at monitor-process start, and nothing re-read it. `ntm add` appends the new agent to the spawn manifest but has no way to reach the running monitor — no signal handler, no watch, no reload — so a pane added to a live session was never health-checked and never restarted, silently and for the life of the session. The monitor now reconciles from the manifest each health tick, adopting only panes it does not already know (re-registering a known one would reset its restart bookkeeping and let a crash-looping agent never reach max-restarts) and never re-adopting one it has deliberately retired as gone from the session.
- **The automatic CAAM failover cooldown is claimed atomically** ([da37abd7](https://github.com/Dicklesworthstone/ntm/commit/da37abd7)): the per-pane one-hour cooldown gates an action but was enforced with a plain read and a plain write, and the gates in between include a real `caam list --json` subprocess. Nothing prevents two `ntm coordinator run` processes from watching one session, so both could read "not recently switched", pass every gate, and rotate the same pane seconds apart — moving quota onto an unexpected account. The slot is now claimed atomically and a lost claim declines instead of switching; the cooldown still starts at fire time, so a broken caam is still not retried every tick.
- **The Agent Mail inventory probe fails fast when the server is not running** ([63b18176](https://github.com/Dicklesworthstone/ntm/commit/63b18176)): routing the doctor/tools probe through the shared client's availability check made the common case — Agent Mail simply not running — cost about 1.25s, because that call retries three times with backoff by design. That is right for gating a message send and wrong for filling an inventory row the tools registry rebuilds on every snapshot. A single cheap liveness request now decides the usual cases, escalating to the retrying probe only when the liveness endpoint cannot answer, which is the path that makes an authenticated server report healthy. Measured at roughly two milliseconds against a closed port.

### Changed

- **`memory.include_in_recovery` and `memory.max_rules` are removed** ([#323](https://github.com/Dicklesworthstone/ntm/issues/323), [53855970](https://github.com/Dicklesworthstone/ntm/commit/53855970)): `ntm config init` wrote three `memory.*` keys that the same binary immediately warned were deprecated, the warning claimed the aliases had been "removed in v1.27.0" while they were still shipping seven releases later, and `ntm config migrate` reported such a file as clean — so a brand-new config produced three warning lines on every invocation with no built-in way to clear them. The two keys that were purely aliases into `[recovery]` are now removed: the strict loader names them and points at `recovery.include_cm_memories` / `recovery.max_cm_rules`, and `ntm config migrate` strips them. Their `[recovery]` counterparts default to exactly the values the aliases did (`true` and `10`), so a config that never set them behaves identically. `memory.query_timeout_seconds` deliberately stayed and is no longer deprecated — it was never purely an alias, since it also bounds the cm query behind send-time injection — and it no longer reaches into `[recovery]`, which has its own budget. Removal batches now carry their own release provenance rather than borrowing a neighbouring batch's, so `ntm doctor` can no longer tell you a key stopped loading in a release where it still worked.

---

## [v1.33.1] -- 2026-09-08 [GitHub Release]

**Dashboard patch: the two issues filed against v1.33.0, fixed, plus the Go 1.26.8 toolchain floor** (GitHub issues [#317](https://github.com/Dicklesworthstone/ntm/issues/317), [#318](https://github.com/Dicklesworthstone/ntm/issues/318)).

### Fixed

- **Process logs stay off the terminal while a TUI runs** ([#317](https://github.com/Dicklesworthstone/ntm/issues/317)): ntm never installed a slog handler, so `slog.Default()` and the standard `log` package wrote to stderr — and inside the full-screen dashboard stderr *is* the rendered frame. The rano PID-map refresh logged `refreshed PID map` at INFO on every one-second poll, and the lines scrolled straight over the panels; some thirty packages on the dashboard's import graph log the same way, so demoting that one line would not have fixed the class. The new `internal/tui/tuilog` package routes the default slog logger (and, through slog's bridge, the standard `log` package) to `$XDG_DATA_HOME/ntm/logs/tui.log` (default `~/.local/share/ntm/logs/tui.log`) for the lifetime of every Bubble Tea program — the dashboard including its tmux-popup relaunch, the command palette, the session selector and the tutorial — and restores both loggers afterwards. Records carry `tui=<name>`, the file is truncated when it outgrows 8 MiB, a file that cannot be opened discards records rather than printing them to the terminal, and the debug level follows the existing `NTM_TUI_DEBUG` / `NTM_DEBUG` / `--debug` switch. The per-poll PID-map success line is now Debug; the Warn for a failed pane listing is unchanged.
- **`ntm dashboard` honors its configuration from the first frame** ([#317](https://github.com/Dicklesworthstone/ntm/issues/317)): the dashboard learned its configuration only from the config watcher, which reports *changes*, so `[integrations.rano] enabled = false` — and every other "enabled unless configured" default — had no effect until a config file was edited mid-session. `ntm dashboard` now loads the merged config (project overlay from the session's project directory; `--config` / `NTM_CONFIG` honored) and applies it before the program starts, through the same path the watcher uses; a load failure warns on stderr before the TUI starts and keeps the previous default behavior.
- **The configured theme reaches every panel** ([#317](https://github.com/Dicklesworthstone/ntm/issues/317)): the config theme was applied only to lipgloss defaults and never reached `theme.Current()` callers (panels, overlays). `theme.SetConfigured` records the configured name process-wide, `Current()` falls back to it when `NTM_THEME` is unset (the explicit env override keeps precedence), and the dashboard records the merged config's theme before its panels are built, so a session whose project overlay configures a different theme than the launch directory no longer renders panels in one theme and chrome in the other.
- **The dashboard spawn wizard advances past the method step** ([#318](https://github.com/Dicklesworthstone/ntm/issues/318)): the Ctrl+W spawn wizard is built on huh forms, which complete through their own messages — Enter on the method `Select` returns a `NextField` command, the group answers with its next-group message, and only that second message completes the form. `Model.Update` forwarded key presses to the overlay and dropped everything else, so selecting Minimal and pressing Enter left the wizard on the configuration-method step indefinitely (the counts and confirm steps had the same problem). Every message the dashboard does not consume is now forwarded to the open wizard — keys are never forwarded twice, and a finished form is closed before it can be fed its completion message and spawn again — and window resizes reach the wizard, so the overlay lays out against the current terminal size instead of its creation-time bounds.

### Changed

- **Go toolchain floor is 1.26.8**: the `go.mod` directive moves from 1.26.5 to 1.26.8. The six Go standard-library advisories `govulncheck` reported as reachable from ntm at v1.33.0 (GO-2026-6218 `net/url`, GO-2026-6090 `crypto/tls`, GO-2026-6089 `net/http`, GO-2026-6088 `encoding/xml`, GO-2026-5972 `encoding/asn1`, GO-2026-5026 `net/http`; all fixed in go1.26.6+) are cleared by the toolchain that builds ntm, not by `go.mod` alone, so the directive now names that toolchain and `govulncheck ./...` reports no vulnerabilities. Building from source needs Go 1.26.8, or an older Go with `GOTOOLCHAIN=auto` that can fetch it; the release binaries are built with go1.26.8.

### Internal

- Release lockstep: `VERSION`, `web/package.json`, `vscode/package.json` (with their lockfiles) and the embedded dashboard export (`data-ntm-version`) move to 1.33.1; the README Go badge follows the `go.mod` directive (pinned by `TestReadmeGoVersionBadge`).

---

## [v1.33.0] -- 2026-09-07 [GitHub Release]

**Launch-affinity restarts, Agent Mail pane badges, per-pane placeability, and the selector/checkpoint/dead-pane fix wave** (GitHub issues [#283](https://github.com/Dicklesworthstone/ntm/issues/283), [#298](https://github.com/Dicklesworthstone/ntm/issues/298), [#304](https://github.com/Dicklesworthstone/ntm/issues/304), [#305](https://github.com/Dicklesworthstone/ntm/issues/305), [#307](https://github.com/Dicklesworthstone/ntm/issues/307), [#308](https://github.com/Dicklesworthstone/ntm/issues/308), [#310](https://github.com/Dicklesworthstone/ntm/issues/310), [#311](https://github.com/Dicklesworthstone/ntm/issues/311), [#312](https://github.com/Dicklesworthstone/ntm/issues/312); PRs [#306](https://github.com/Dicklesworthstone/ntm/pull/306), [#309](https://github.com/Dicklesworthstone/ntm/pull/309)).

### Added

- **Restart keeps the pane's CAAM profile** ([#298](https://github.com/Dicklesworthstone/ntm/issues/298)): `ntm spawn` and `ntm add` record, per agent, an opaque provider-scoped launch binding in the resilience manifest — the `SHALLOW_PROFILE` name in effect when the pane was created, launcher `caam` (`launch_binding`; never `HOME`, tokens or arbitrary pane environment, and `ntm add` now writes its agent into the manifest at all). `--robot-restart-pane` and the resilience monitor resolve that binding through the same launcher before touching the pane (`caam shallow-spawn <profile> --print-env`, a strict dry run), relaunch as `caam shallow-spawn <profile> -- sh -c '<command>'` so every part of the command — env-assignment prefixes, `systemd-run`, `a && b` — runs under the profile, and refuse before any respawn, naming the binding, when the profile cannot be resolved. The restart output reports `launch_affinity` per pane: `preserved`, or `unknown` for agents recorded before bindings existed, which relaunch exactly as before instead of implying the account was preserved. Dry-run restarts run the same preflight. The launcher binary is `[integrations.caam] binary_path` (default: `caam` on `PATH`) and must be caam >= v0.1.12, the release that added `shallow-spawn --print-env`; an older caam fails the preflight, so a bound restart refuses instead of relaunching under the controller's profile.

- **Agent Mail identity badges in tmux pane borders** ([#312](https://github.com/Dicklesworthstone/ntm/issues/312)): with `[agent_mail] pane_badges = true` (default off), every managed pane shows its NTM-assigned Agent Mail name next to the existing pane title — `[BlueLake]`, or `[BlueLake!]` when the canonical identity file disagrees, with `(starting)` / `(exited)` / `(unknown)` lifecycle markers and `[?!]` for a stale, unobservable or unregistered assignment. The badge is the session agent registry's assignment keyed by stable pane id, never whatever a later process wrote to the identity file: a disagreement produces a report, not a relabel. Badge state is cached in per-pane tmux user options (`@ntm_agent_mail_name`, `@ntm_agent_mail_state`, `@ntm_agent_mail_lifecycle`, `@ntm_agent_mail_label`) written through the tmux argv API, published as part of identity assignment before the agent command is sent, and reconciled on spawn, add, adopt, restart/respawn and `ntm mapping --session`; the window `pane-border-format` gains a fragment that renders only the cached label, so tmux never touches the registry or Agent Mail while drawing. Reconciliation follows the issue's outcome table (`matched`, `legacy-unverified`, `name-disagreement`, `missing-file`, `unreadable-file`, `invalid-file`, `binding-unverifiable`, `binding-stale`; `assignment-stale`, `assignment-unobservable`, `assignment-unregistered`), keeps every simultaneous problem, verifies structured records locally against tmux's own `pane_pid`/session/socket (no Agent Mail daemon calls), and records `last_attempt_at` per attempt and `last_success_at` per completed observation in `agent_badges.json` beside the registry. `ntm mapping` gains `badge`/`identity` columns, an "Identity discrepancies" section and, in `--json`, per-entry `identity`, top-level `discrepancies` and a `badges` publication summary. Publication failures warn and never block a launch or change an assignment; a pane whose generation changed between observation and publication has its badge withdrawn; windows linked into several sessions are skipped with a diagnostic; a session can opt out with `@ntm_agent_mail_badges off`; disabling (`pane_badges = false` + `ntm mapping`) withdraws the pane options and restores the previous `pane-border-format` including inheritance, keeping edits the user made while badges were on. `pane_badge_format` customises the label (`{name}`, `{drift}`, `{lifecycle}`), validated against tmux format syntax.
- **`ntm assign` reports per-pane placeability instead of one opaque "no idle agents"** ([#306](https://github.com/Dicklesworthstone/ntm/pull/306)): "not placeable" was a conjunction of independent gates (non-agent pane, agent-type filter, an active assignment record fencing the pane, `SafeToDispatch` refusing on stale/errored/low-confidence/non-idle evidence, persona role gating) that all collapsed into `Idle Agents: 0` and a `no_idle_agents` skip reason, so two panes sitting at an empty prompt but fenced by leaked assignment records were indistinguishable from busy ones. Every pane now gets one verdict, in gate order: `placeable`, `not_agent_pane`, `agent_type_filtered`, `active_assignment` (with the holding bead), `observation_stale`, `observation_error`, `low_confidence`, `not_idle`, or `role_ineligible`. The human zero-agent path prints `Reason: No placeable agents (1 active_assignment, 1 not_idle, …)` followed by one line per pane (`--verbose` lists panes on the other paths too); JSON gains a stable `pane_placeability` array (`pane_id`, `pane_target`, `agent_type`, `state`, `confidence`, `placeable`, `verdict`, `detail`) and `summary.placeability_counts`, with every existing field unchanged. Beads skipped for lack of agents now carry reason `no_placeable_agents` on the recommendation path (the auto-reassignment path keeps `no_idle_agents`). The `SafeToDispatch` sub-reason comes from the new `status.PaneObservation.DispatchRefusal()`, which `SafeToDispatch` is now defined in terms of so the two cannot drift, and both the recommendation and auto-reassignment idle scans share one gate evaluator.

### Changed

- **Codex launches default to `gpt-6-astra`**: `models.default_codex` (and the `gpt5`/`codex` model aliases) move from `gpt-5.6-sol` to `gpt-6-astra`, and agent-type normalisation recognises the new name as Codex, so a pane or persona described by the default model no longer normalises to the bare model string.

### Fixed

- **Stranded `sending` dispatches are resolved from pane evidence** ([#304](https://github.com/Dicklesworthstone/ntm/issues/304)): a dispatch interrupted between the send and its receipt stayed in `sending` forever — `recoverPendingAssignments` always answered `ErrDispatchOutcomeUnknown`, so the pane could not be reused even when tmux still listed it or Agent Mail already held the message. Recovery keeps the fail-closed default but now probes the live pane topology (every listed pane, including user/unknown panes) and mailbox delivery, records `stranded_dispatch_reason` / `stranded_dispatch_resolved_at` on the assignment so the observation that unstuck it is never overwritten, and invalidates its live-pane set when a whole-session observation fails so absence is never inferred from a snapshot it could not take. Adopting a recovered delivery receipt only moves `claiming`/`claimed` rows to `assigned` (a row that already reached `working` is never walked backwards) and emits the same bead-assigned event a normal dispatch does.
- **Per-identity assignment lock files no longer accumulate** ([#309](https://github.com/Dicklesworthstone/ntm/pull/309)): every bead, occupancy target and external-cleanup identity took its own `assignments.json.atomic-<namespace>-<hash>.lock`, and nothing ever unlinked it, so a long-lived session directory grew one inode per identity it had ever touched. Those locks are now transient: the releaser removes its own file, a waiter that was polling on the removed inode detects that the path no longer names the file it locked (fstat/stat identity, immune to inode-number reuse because the waiter still holds the old inode open) and retries on the current path, and a releaser only ever removes the file it locked, so a replacement created after an external deletion is left alone. Unix unlinks before dropping the flock; Windows removes after closing the handle, where the missing `FILE_SHARE_DELETE` guarantees a delete can only succeed when no contender holds the file. The whole-store `assignments.json.lock` stays persistent.
- **Work-coordination collection failures name their cause** ([#283](https://github.com/Dicklesworthstone/ntm/issues/283)): when both the work and coordination sections are unavailable, `sources.work_coordination.last_error` now reads `work/coordination sources unavailable (work: br stats failed: …; coordination: agent mail unavailable)` instead of the bare summary, so a snapshot whose work projection is empty says what the collector actually failed on.
- **Dependency advisories** ([#307](https://github.com/Dicklesworthstone/ntm/issues/307)): `golang.org/x/text` v0.38.0 → v0.41.0 clears GO-2026-5970 (the only advisory `govulncheck` found reachable, via `robot.PrintHelp`); `golang.org/x/mod` moves to v0.40.0 (GO-2026-6179/6180, not reachable from ntm). The remaining `govulncheck` findings are Go standard library issues fixed in go1.26.6+ and depend on the toolchain that builds ntm, not on `go.mod`.
- **`ntm add` no longer fails on a retained dead pane** ([#311](https://github.com/Dicklesworthstone/ntm/issues/311)): a pane whose process exited under `remain-on-exit` reports an empty `#{pane_current_path}`, and live-session project resolution treated that as `current path "" is not absolute` and refused the whole session. The pane model now carries tmux's `#{pane_dead}` (`tmux.Pane.Dead`, appended as the trailing field of every `list-panes` format ntm parses, with parser fixtures pinning that nothing before it shifts), the resolver skips dead panes before any lookup, a live pane with an empty path still fails closed, distinct repositories still fail the consistency check, and a session whose panes are all dead returns a deliberate `no live panes` error (`robot.ErrNoLivePanes`) instead of the misleading "panes span multiple project roots". `ntm add` and the other explicit-session commands fall back to the validated saved/configured session project in the all-dead case, exactly as they do when the live session cannot be listed, and otherwise report the dead-pane reason.
- **Checkpoints capture Git state again, and say so when they cannot** ([#310](https://github.com/Dicklesworthstone/ntm/issues/310)): checkpoint capture asked tmux for the session's working directory with `display-message -p -t =session`, which on tmux 3.4 and 3.6a exits 0 with empty output, so every checkpoint recorded `working_dir: ""` and skipped Git capture while its `git` block looked like a clean repository (empty branch/commit, zero dirty counts). The query now uses the pane-qualified exact target (`=session:`, the session's current window and active pane) via the new `tmux.SessionPaneTarget` helper, and an empty answer is an error rather than a path. The same target fix is applied to the checkpoint layout query, `ntm rollback`'s working-directory lookup (which would otherwise have run git in the caller's cwd), the coordinator's `session_created` read and session-capture's layout read. Checkpoints now carry `git.captured` plus `git.skip_reason`/`git.skip_detail` (`working_dir_unavailable`, `capture_disabled`, `not_git_repository`, `capture_failed`) and `working_dir_error`, `ntm checkpoint verify` reports `git_skip_reason` and warns on an unexpected skip, and `checkpoint save`/`show` print "Git: not captured (reason)" instead of nothing. Checkpoints written before these fields existed keep reading as before.
- **Unsupported pane selectors fail closed before actuation** ([#308](https://github.com/Dicklesworthstone/ntm/issues/308)): `--robot-interrupt --pane=%12` used to accept the singular flag, ignore it and interrupt every agent pane in the session. Every robot surface now rejects an explicitly supplied `--pane`/`--panes` (and the `--history-pane`/`--wait-panes` aliases) that it does not read, with `INVALID_FLAG` and a hint naming the selector it does accept (`Use --panes=%12 with --robot-interrupt`), before any session lookup or actuation. The check is driven by the robot registry's per-surface parameter declarations, so `--robot-history --panes=…`, `--robot-wait --pane=…`, `--robot-restart-pane --pane=…` and the other singular/plural mix-ups are covered by the same guard.
- **Service panes are no longer counted as agents** ([#305](https://github.com/Dicklesworthstone/ntm/issues/305)): ACFS runs long-lived service processes (`cm`, `cass`, …) in tmux panes alongside agent panes and tags each one with the `@acfs_service` pane option. NTM now reads that durable tag — plus the vendor-neutral `@ntm_service` — straight off `list-panes`, and excludes tagged panes from every agent surface: `--robot-status` counts and `agents_by_state`/`agents_by_type`, `--robot-snapshot` agents, session health rollups, `--robot-errors`, `--robot-activity`, the dashboard summary, `ntm activity` and `ntm errors`. A healthy service whose terminal has been quiet for hours can no longer turn its session `critical` with a phantom agent in `error`. Service panes stay visible instead of vanishing: `--robot-snapshot` reports them per session under `services`, and `--robot-health=<session>` lists them under `services`. An explicit `ntm adopt` (which records `@ntm_agent_type`) still outranks the tag, so a pane can be deliberately treated as an agent.

### Internal

- `tmux.SessionPaneTarget` and `SessionOptionTarget` share one `=name:` target builder (an already-qualified `=foo:` no longer becomes `=foo::`; a compound target is only pinned to exact match) and `GetRobotRegistry` reuses the cached registry (refs [#308](https://github.com/Dicklesworthstone/ntm/issues/308), [#310](https://github.com/Dicklesworthstone/ntm/issues/310)); the pane selector guard drops an unused error field.
- Release lockstep: `VERSION`, `web/package.json`, `vscode/package.json` (with their lockfiles) and the embedded dashboard export (`data-ntm-version`) move to 1.33.0.

---

## [v1.32.0] -- 2026-09-04 [GitHub Release]

**Pane liveness and launch safety, anchored failure evidence, durable activity state, and the Gemini 3.8 Antigravity pin** (GitHub issues [#283](https://github.com/Dicklesworthstone/ntm/issues/283), [#288](https://github.com/Dicklesworthstone/ntm/issues/288), [#292](https://github.com/Dicklesworthstone/ntm/issues/292), [#294](https://github.com/Dicklesworthstone/ntm/issues/294), [#295](https://github.com/Dicklesworthstone/ntm/issues/295), [#297](https://github.com/Dicklesworthstone/ntm/issues/297), [#299](https://github.com/Dicklesworthstone/ntm/issues/299), [#300](https://github.com/Dicklesworthstone/ntm/issues/300), [#301](https://github.com/Dicklesworthstone/ntm/issues/301), [#302](https://github.com/Dicklesworthstone/ntm/issues/302), [#303](https://github.com/Dicklesworthstone/ntm/issues/303); PRs [#290](https://github.com/Dicklesworthstone/ntm/pull/290), [#291](https://github.com/Dicklesworthstone/ntm/pull/291)).

### Added

- **Durable activity state** ([#301](https://github.com/Dicklesworthstone/ntm/issues/301)): `ntm activity` reconciles each pane's classified state against a watermark scoped to the pane lifetime (session, pane ID, pane PID), so `state_since` and the duration grow across one-shot invocations instead of resetting to "now". Transient TUI markers are classified from the visible viewport, not the scrollback, so a spinner that scrolled away hours ago no longer pins a pane to THINKING.
- **Agent Mail probe budget** ([#295](https://github.com/Dicklesworthstone/ntm/issues/295)): `NTM_AGENT_MAIL_PROBE_BUDGET` (a Go duration) overrides the 1250 ms availability budget, a cheap unauthenticated `/health` liveness probe decides availability before falling back to the MCP health tool, and every reservation verb (`lock`, `unlock`, `locks`, `renew`, `force-release`) reports the probe's actual error instead of a bare "Agent Mail server unavailable".
- **Ready counts bound to direct tracker evidence** ([#283](https://github.com/Dicklesworthstone/ntm/issues/283)): the work-readiness model derives the complete ready count and the capped preview from one `br ready --json` result, and blocked snapshots keep blocker IDs, blocker counts, priority and issue type instead of title-only rows.
- **Typed composer blockers** ([#300](https://github.com/Dicklesworthstone/ntm/issues/300)): `--clear-input` reports whether a composer is held by stranded text, queued messages or a modal dialog, and how many clearing rounds ran, so `COMPOSER_NOT_CLEARED` tells the operator whether to inspect, wait or answer a dialog.

### Changed

- **Antigravity default model is Gemini 3.8 Flash (High)** (`gemini-3.8-flash`): the required-model pin, the session-resume model, the registry alias, flag help and the spawn wizards all move from 3.7, matching ACFS's `agy-locked` launcher and model guard.
- **Spawn never launches into an occupied pane** ([#291](https://github.com/Dicklesworthstone/ntm/pull/291)): every agent pane of a batch is preflighted before any launch, the exact pane is re-read immediately before `send-keys`, and an occupied, missing or unreadable pane fails closed with the running command named and a pointer to `ntm add`. Previously only Grok launches checked, and the launch line (including `--dangerously-skip-permissions`) could be typed into a live agent or editor.

### Fixed

- **Pane liveness is judged by the terminal's foreground process group, not the command name**: a job other than the pane's root shell owning the tty is the "agent took over the pane" signal, so agents started as `sh -c ...` and non-exec'ing wrapper scripts count as live while only a bare interactive shell counts as dead (`PANE_AGENT_DEAD`). Fresh panes get a 3 s settle window (tmux reports itself as the foreground command for about 300 ms before the shell execs), and the post-launch stable-process wait now runs for every agent type, not only Grok, so a prompt is never dispatched into a shell the agent has not yet replaced.
- **Wrapped composer clear** ([#300](https://github.com/Dicklesworthstone/ntm/issues/300)): the whole bordered composer block (marker row plus wrapped continuation rows) is verified empty, and a pane with no composer marker at all is reported as unverifiable rather than as blocked on a modal.
- **Anchored failure evidence** ([#297](https://github.com/Dicklesworthstone/ntm/issues/297), [#299](https://github.com/Dicklesworthstone/ntm/issues/299)): prose about failure ("the planted negative failed as expected", a 20000 ms timeout mentioned in a plan, a SHA-256 that contains "429") no longer marks a pane ERROR or degrades a session. Only anchored runtime signatures, structured status codes (case-insensitive) and process outcomes count, for alerts, robot health and handoff text alike; named exception lines such as `ValueError: bad input` are recognised.
- **A fresh `--robot-status` process no longer reports every agent busy** ([#288](https://github.com/Dicklesworthstone/ntm/issues/288)): a first observation only records a baseline; without an in-process clock the durable output sequence is used, a pane resting at its prompt is idle and never "stalled", and no clock plus no decisive tail yields unknown, never busy.
- **Assignment rail** ([#294](https://github.com/Dicklesworthstone/ntm/issues/294), [#302](https://github.com/Dicklesworthstone/ntm/issues/302)): pre-actuation dispatch refusals (dead CLI, unsupported protocol, composer not ready or not clearable) are retried behind the idempotency barrier with bounded exponential backoff instead of permanently failing the assignment; reservation candidates must resolve inside the repository, so prose like `13714/2m` or `2026/09/03` no longer becomes an Agent Mail lease; occupied-target refusals name the owning bead with its status, age and assignment time.
- **`br` write-lock timeouts are retried** ([#290](https://github.com/Dicklesworthstone/ntm/pull/290)) with jittered, deadline-bounded backoff, so one contended `br ready` during `ntm assign --watch` is no longer a hard failure; other `CONFIG_ERROR`s stay terminal.
- **Audit JSONL honours `XDG_DATA_HOME`** ([#292](https://github.com/Dicklesworthstone/ntm/issues/292)) the same way resilience manifests and monitor logs already did.
- **Checkpoint pane resolution on tmux 3.6**: the session's selected pane is resolved with the `=name:` target form (tmux 3.6 answers `=name` with empty output) and an empty answer is logged instead of silently recording the last window's active pane.
- **E2E hygiene** ([#303](https://github.com/Dicklesworthstone/ntm/issues/303)): `ntm-tmux-test-*` servers leaked by a killed run are reaped at suite start, age-guarded and never the suite's own socket root.

---

## [v1.31.0] -- 2026-09-01 [GitHub Release]

**Durable agent identity, coordinator/Agent Mail integrity, and the Antigravity/Codex delivery-gate wave** (GitHub issues #262, #263, #265, #266, #268, #269, #270, #271, #273, #277, #285).

### Added

- **Oh My Pi plugin preset** ([#262](https://github.com/Dicklesworthstone/ntm/issues/262)): ships the maintained Oh My Pi preset with a setup guide.
- **Stable pane identity in activity output**: `ntm activity` now emits `window_index` and a stable `pane_id`, so robot-mode consumers can track panes across layout changes.
- **Pane-generation-bound Agent Mail identities**: spawned identities are bound to their pane generation and server receipts are preserved, so a respawned pane can never inherit a predecessor's mail identity.
- **Tool-rejection classification in atomic dispatch**: the coordinator classifies tool rejections during dispatch and streamlines spawn availability checks.

### Fixed

- **Durable agent type** ([GH#268](https://github.com/Dicklesworthstone/ntm/issues/268)): the pane's agent type is recorded on the pane (not just its title) and persists across spawn, add, adopt, controller changes, rotation, profile switch, restore, and restart; the `user` pseudo-type is never stamped as durable.
- **Antigravity support hardening** ([GH#270](https://github.com/Dicklesworthstone/ntm/issues/270), [GH#269](https://github.com/Dicklesworthstone/ntm/issues/269), [GH#271](https://github.com/Dicklesworthstone/ntm/issues/271)): idle and working TUI frames are classified correctly, pinned Antigravity personas pass model validation, `agy` model overrides are rejected with a clear explanation, and error strings are normalized.
- **Codex ultra composer delivery gate** ([#273](https://github.com/Dicklesworthstone/ntm/issues/273)): the delivery gate accepts the Codex ultra composer glyph `»` and rejects dialog frames, so sends no longer stall on Codex ultra.
- **Agent Mail read state** ([#277](https://github.com/Dicklesworthstone/ntm/issues/277)): inbox rows accept `read_ts`, so mail that has been read stops counting as unread; structured pane-binding JSON is decoded to its name and the real coordinator identity is registered.
- **Robot-mode reliability**: completed Beads work is no longer dropped when Agent Mail history is slow; stale errors are debounced only on authoritative live work; mail checks use the configured Agent Mail client; restarts are gated on live pane membership; crash handling runs even for a known-dead PID.
- **Assignment flow**: explicit persona roles are respected when matching work to panes, and stale `bv` plan rows are reconciled against live bead lifecycle instead of failing assignment.
- **Controller safety**: the controller never takes over a running agent's pane, and dashboard mail-client semantics are aligned with it; Grok Build panes are admitted through the shared mail-nudge safety gate.
- **Event bus**: reentrant publishing at capacity no longer deadlocks (non-blocking caller-runs backpressure).
- **Windows**: drive paths serialize as empty-authority SQLite URIs and embedded migration paths are slash-joined, fixing database access on Windows ([#263](https://github.com/Dicklesworthstone/ntm/issues/263) family).
- **Pipeline**: executor snapshot persistence to disk is serialized.
- **Docs/CLI polish**: shipped-plugin send flags parse as optional values; `adopt` help no longer claims 0-based pane indices (honours `pane-base-index`).

---

## [v1.30.0] -- 2026-08-22 [GitHub Release]

**Agent Mail identity integrity + first-class agent plugins** (GitHub issues #256, #257, #258, #260, #261).

### Fixed

- **Live Agent Mail identities are never re-issued** ([#256](https://github.com/Dicklesworthstone/ntm/issues/256)): `ntm add` combined title-derived indexing with a title-first registry lookup, so two live panes could be handed the same identity. The session registry now records pane ids and pids, resolves pane-id-first, and reuses a title only when its holder is provably dead (absent, or present with a different pid); unobservable liveness means "occupied" and a fresh identity is minted. `ntm add` also folds live registry titles into its index allocation.
- **`--worktrees` panes are addressable under both project keys** ([#257](https://github.com/Dicklesworthstone/ntm/issues/257)): spawn published pane identities under the session project key while agents derived theirs from the worktree cwd, so mail addressed one key and agents registered under another. Identities are now published under the session key, its resolved form, and the worktree directory, and `AGENT_MAIL_PROJECT=<session dir>` is injected into worktree panes unless plugin env or `--pane-env` overrides it.
- **The agentmail fallback test can never touch the real `~/.config`** ([#258](https://github.com/Dicklesworthstone/ntm/issues/258)): the test set only `HOME`, but `os.UserConfigDir()` honours `XDG_CONFIG_HOME` first, so `go test ./internal/...` could delete the real config directory. The test is now hermetic (both variables isolated, paths derived from the production resolver, deletion refused unless provably inside the temp sandbox) with regression tests pinning the containment.
- **Corrected commit provenance.** Commit `d08893d9`'s message incorrectly attributed the fail-closed serve safety-policy change to its completion and tmux diff. The actual safety-policy implementation is `dda4aae8`; history is preserved and this note is the forward correction.

### Added

- **OpenCode parity** ([#261](https://github.com/Dicklesworthstone/ntm/issues/261)): readiness/working detection for the OpenCode TUI, `ntm send --oc`/`--grok` targeting, a deps probe, `models.default_opencode`, and a registration fallback (`<program>/cli-default`) when no model is configured, so bare OpenCode panes still get Agent Mail identities.
- **First-class agent plugin readiness contract** ([#260](https://github.com/Dicklesworthstone/ntm/issues/260), partial): plugins can register `[agent.readiness]` idle/working/error patterns that the parser and status detector honour, plugin agent types flow through robot reports, `ntm send --<plugin>` targeting works, and deps probes call the plugin's probe command. Verified live against Oh My Pi v18; setup/preflight TOML support is tracked on the issue.

### Internal

- Removed the superseded legacy e2e scenario harness (bd-yhc8r).

---

## [v1.29.3] -- 2026-08-20 [GitHub Release]

**Migrate-hardening patch** (adversarial torture-test findings against v1.29.2's headline feature).

### Reliability

- **`ntm config migrate` verdicts are honest**: when dead keys survive the line surgery (live inline tables like `rotation = { prefer_restart = true }`, BOM-defeated headers), the command no longer reports "config is clean" — the lenient re-scan always runs, `clean` requires zero unresolved keys, the file stays untouched when nothing is removable, and each unresolved key is named for manual cleanup.
- **Backups can never clobber**: same-second migrations get `O_EXCL` collision-proof backup names. Sixteen new torture tests pin prefix-collision, dotted/quoted-key, nested-bracket-array, inline-table, CRLF, BOM, symlink, concurrent-lock, and read-only behavior, and an end-to-end test proves `ntm shell` still reflects custom `[agents]` config.

---

## [v1.29.2] -- 2026-08-20 [GitHub Release]

**Config-migration UX patch** — fixes the removed-key warning wall that greeted every new terminal pane on machines with pre-v1.26 configs (field incident, `bd-config-migrate-warning-wall-151x2`).

### Features

- **`ntm config migrate`** surgically deletes every removed/deprecated config key (both migration batches) from the selected config file — keys that by definition never had an effect, so behavior cannot change. Timestamped backup always written first; comments, ordering, and every live byte preserved; emptied tables removed; `--dry-run` and `--json` supported; clean configs no-op.

### Reliability

- The startup config warning collapses to **one line** pointing at `ntm config migrate` and `ntm doctor` (the full per-key detail remains in robot error envelopes, doctor, and `--dry-run`); `ntm shell`, `completion`, and shell-completion internals no longer load config at all and emit zero stderr — new panes stay clean.

---

## [v1.29.1] -- 2026-08-19 [GitHub Release]

**Same-day patch: the two issues filed against v1.29.0, fixed.**

### Reliability

- **Stale projections no longer hide live sessions (#254).** When every runtime-projection row passes its staleness horizon, `--robot-snapshot`/`--robot-status` now verify against live tmux and serve the live view with a staleness alert instead of returning an empty session list; an empty result is served only when live tmux confirms idle.
- **Pane identities publish before agents launch (#255).** Spawn writes each agent's Agent Mail identity (canonical + legacy files, durable registry) immediately before its launch keystrokes, so a booting agent can never race its own registration; identity failures stay fail-open (mail down never blocks a spawn).
- **Tagged test code compiles in CI again.** A new tagged-vet guard compiles the integration, e2e+ensemble, and load test variants with a firing canary and a tag census — and immediately caught three more tagged tests rotted by the dead-code sweep, now fixed.

---

## [v1.29.0] -- 2026-08-19 [GitHub Release]

**The P3-backlog and fleet-friction release**: the final open beads implemented, ~72k lines of dead code removed, the second config-deprecation batch flipped to errors as promised, and a fleet-wide cass mining pass converted three weeks of real agent friction into fixes.

### Features

- **Parallel pipeline execution.** Independent ready steps run concurrently (`settings.limits.max_parallel_steps`, default 4; `1` restores serial order) with fail_fast sibling cancellation, per-pane dispatch serialization, and race-clean state recording.
- **Real ensemble lead-agent synthesis.** Agent-based synthesis strategies dispatch the lead synthesis agent through gated delivery with fenced-response parsing; degradation to mechanical merge is recorded loudly, never silent.
- **Skill-matched spawn assignment** routes through the capability matrix with per-assignment rationale (provably different from top-n); `summary --since` genuinely filters via the archive time oracle; **durable HTTP approvals** — serve's approval endpoints share one approval space with the CLI (two-person SLB rule surfaces as 403, WS events fire after durable transitions, 503 fail-closed without a store).

### Reliability

- **tmux session targets are exact-match** (`=` sigil through one canonical helper, ~90 call sites): spawning `foo` beside `foo_bar` can no longer cross-wire panes into the sibling session — a field-mined collision class.
- **Fleet-friction batch** (from cass mining across seven machines): paste-buffer names are collision-proof across processes; `-V` joins `--version`; curated did-you-mean hints cover the fleet's most-guessed flags (`--message` aliases `--msg`); missing beads workspaces read as clean empty results instead of INTERNAL_ERROR; empty-message errors are INVALID_ARGS; invalid model IDs get advisory registry suggestions; kill-time Agent Mail cleanup releases dead pane agents' reservations.
- **Dead-code burndown complete**: the pre-existing backlog reached zero — 1,380 symbols (~72k LOC) deleted after per-symbol re-verification under both build variants, 32 justified permanent entries remain.

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
  **Migration:** delete the listed keys from your config file (or run
  `ntm config migrate`, which removes them surgically with a backup) — see
  the deprecated-key migration table in the v1.28.0 entry below for the full
  list and per-key dispositions. Nothing changes behaviorally — these values
  were already ignored.

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
  (the startup warning names each one), or run `ntm config migrate` to
  remove them surgically with a backup. Nothing changes behaviorally — these
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
  listed keys from your config file (or run `ntm config migrate`, which
  removes them surgically with a backup) — see the removed-key migration
  table in the v1.26.0 entry below for the full list and per-key
  dispositions.

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
  (Since v1.29.2: delete the keys by hand or run
  `ntm config migrate`, which removes them surgically with a backup.)

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
