# Changelog

All notable changes to **NTM** (Named Tmux Manager) are documented here.

NTM is a tmux session management tool for orchestrating multiple AI coding agents (Claude Code, OpenAI Codex, Google Gemini CLI, Cursor, Windsurf, Aider, Ollama) in parallel with a stunning TUI dashboard, robot mode APIs, and deep ecosystem integrations.

- Repository: <https://github.com/Dicklesworthstone/ntm>
- Releases marked **[GitHub Release]** have published binaries and container images on GitHub.
- Releases marked **[Tag Only]** are git tags without a published GitHub Release.
- Links point to individual commits for traceability.

---

## [v1.13.0] -- 2026-04-12 [GitHub Release]

**56 commits since v1.12.1** — Lifecycle hardening, concurrency safety, and security improvements.

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
