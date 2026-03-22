# Changelog

All notable changes to **NTM** (Named Tmux Manager) are documented here.

NTM is a tmux session management tool for orchestrating multiple AI coding agents (Claude Code, OpenAI Codex, Google Gemini CLI, Cursor, Windsurf, Aider, Ollama) in parallel.

- Repository: <https://github.com/Dicklesworthstone/ntm>
- Releases marked **[GitHub Release]** have published binaries and container images on GitHub.
- Releases marked **[Tag Only]** are git tags without a published GitHub Release.
- Links point to commits on the `main` branch.

---

## [Unreleased] (v1.8.0..HEAD)

**156 commits since v1.8.0** | Development ongoing as of 2026-03-21

### TUI Overhaul ("Glamour Upgrade")

The post-v1.8.0 cycle has been dominated by a comprehensive TUI rewrite:

- **Vendored Bubbletea fork** with theme system overhaul and adaptive model ([ea7d978c](https://github.com/Dicklesworthstone/ntm/commit/ea7d978c))
- **Spring animations** engine (`SpringManager`) for progress bars, focus transitions, and dimension changes ([1b1b4ff0](https://github.com/Dicklesworthstone/ntm/commit/1b1b4ff0), [5377f3d9](https://github.com/Dicklesworthstone/ntm/commit/5377f3d9), [165fabd2](https://github.com/Dicklesworthstone/ntm/commit/165fabd2))
- **Spawn wizard** with gradient tab bar, panel gap spacing, statusbar, and icon alignment ([142b50b0](https://github.com/Dicklesworthstone/ntm/commit/142b50b0))
- **Scrollable panels** with toast system and sparkline improvements ([af1538d9](https://github.com/Dicklesworthstone/ntm/commit/af1538d9))
- **Charmbracelet/huh forms** integration for interactive TUI dialogs ([a1d5e74c](https://github.com/Dicklesworthstone/ntm/commit/a1d5e74c))
- **bubbles/list** integration for pane list rendering ([8335febc](https://github.com/Dicklesworthstone/ntm/commit/8335febc))
- Dashboard decomposed into logical sub-files ([82cf982e](https://github.com/Dicklesworthstone/ntm/commit/82cf982e))
- Toast notifications enhanced with progress bars and history ([0fb8d230](https://github.com/Dicklesworthstone/ntm/commit/0fb8d230))

### Attention Feed System

A new real-time event feed system for operator awareness:

- **`--robot-attention`** CLI command with profile-aware event filtering ([136e9ceb](https://github.com/Dicklesworthstone/ntm/commit/136e9ceb))
- Attention profile resolution engine with discoverable presets ([26735d62](https://github.com/Dicklesworthstone/ntm/commit/26735d62))
- **`--robot-events`** command with attention feed API expansion ([99cae5c9](https://github.com/Dicklesworthstone/ntm/commit/99cae5c9))
- **`--robot-digest`** flag and attention stream preparation ([05db2385](https://github.com/Dicklesworthstone/ntm/commit/05db2385))
- Attention-aware wait conditions and snapshot attention summary ([3cd98fa8](https://github.com/Dicklesworthstone/ntm/commit/3cd98fa8))
- SSE event stream endpoint and attention webhook integration ([c1a446c7](https://github.com/Dicklesworthstone/ntm/commit/c1a446c7))
- 6-panel mega layout with dedicated attention column ([5bcb734b](https://github.com/Dicklesworthstone/ntm/commit/5bcb734b))

### Agent Type Expansion

- **Cursor, Windsurf, Aider, and Ollama** recognized across all subsystems ([f8eb1c86](https://github.com/Dicklesworthstone/ntm/commit/f8eb1c86), [b16ee210](https://github.com/Dicklesworthstone/ntm/commit/b16ee210))
- `--robot-overlay` command for agent-initiated human handoff ([202d4427](https://github.com/Dicklesworthstone/ntm/commit/202d4427))

### Server & API

- Full WebSocket hub and REST API handlers replacing dummy stubs ([71e4d382](https://github.com/Dicklesworthstone/ntm/commit/71e4d382))
- Operator loop guardrails and REST/CLI parity for capabilities ([576fd45f](https://github.com/Dicklesworthstone/ntm/commit/576fd45f))

### Stability

- Panic recovery added to goroutines; closed-channel drains prevented ([1448e87e](https://github.com/Dicklesworthstone/ntm/commit/1448e87e))
- EventBus buffered delivery, health score cleanup, OOM protection ([f0aaad54](https://github.com/Dicklesworthstone/ntm/commit/f0aaad54))
- Pane ID format corrected from `session:N` to `session:.N` ([8dea427a](https://github.com/Dicklesworthstone/ntm/commit/8dea427a))
- ~1k lines of unused API handlers removed from serve package ([2a8a2d10](https://github.com/Dicklesworthstone/ntm/commit/2a8a2d10))

---

## [v1.8.0] -- 2026-03-07 **[GitHub Release]**

[Release page](https://github.com/Dicklesworthstone/ntm/releases/tag/v1.8.0) |
[Full diff](https://github.com/Dicklesworthstone/ntm/compare/v1.7.0...v1.8.0) |
**482 commits**

### Label-Based Multi-Session Support

The largest feature in v1.8.0 is `--label`, enabling multiple concurrent sessions per project:

- `ntm spawn myproject --label frontend` / `--label backend` for parallel work streams ([53e2110](https://github.com/Dicklesworthstone/ntm/commit/53e2110254986c4ccc81d798dadbf878d34e45e5), [a007a07](https://github.com/Dicklesworthstone/ntm/commit/a007a07b662407b15a3ca764641f17053f7484db))
- Label extended to `create`, `quick`, `list`, and `send` commands ([12fa1ff](https://github.com/Dicklesworthstone/ntm/commit/12fa1ff7b905e7d78989b88d715b996d240840d7))
- `--assign-cc-only` / `--cod-only` / `--gmi-only` agent type filter aliases ([487568c](https://github.com/Dicklesworthstone/ntm/commit/487568c2b9dc44677a41961b121e9da7f7cd2115))
- `--project` flag for `send` and `kill` commands ([1ca6a8a](https://github.com/Dicklesworthstone/ntm/commit/1ca6a8a8a3687d280e91c41509111438efbab49b))

### Encryption at Rest

- AES-GCM encryption for prompt history and event logs ([ff0b96b](https://github.com/Dicklesworthstone/ntm/commit/ff0b96b7aaf479b3f0a5605420a816ee1d93e5f2))

### Ensemble Execution

- Portable `export`/`import` commands with checksum-verified remote imports ([b49d2f7](https://github.com/Dicklesworthstone/ntm/commit/b49d2f7b7a82aa4288254382c7fe683a51db4838))
- Ensemble modes overlay in TUI ([b501f51](https://github.com/Dicklesworthstone/ntm/commit/b501f51146f51906c90233ee1800bbbec2176ad6))

### Robot API Expansion

- `--robot-output-format` alias ([cd542f2](https://github.com/Dicklesworthstone/ntm/commit/cd542f256c5bcc1f7431518d9c203a64ed4cb23b))
- Auto-restart-stuck agent detection ([8980064](https://github.com/Dicklesworthstone/ntm/commit/8980064a7ccc1f3c99a082bbeddcbc1cbae9ed64))
- SLB robot bridge ([1fe4ce3](https://github.com/Dicklesworthstone/ntm/commit/1fe4ce3a822e74bad89fd1abbb3a40ccce4e45d4))
- Pagination hints for ensemble modes agent output ([0a6dfa7](https://github.com/Dicklesworthstone/ntm/commit/0a6dfa772ccef036c8e183dce421e2a27a482608))

### Observability & Operations

- **Prometheus exposition format** export for metrics ([6b0d914](https://github.com/Dicklesworthstone/ntm/commit/6b0d914ac8c42e09bef7540c8cc3d07283a1ea3f))
- **Audit logging** for config commands with CSV export ([1d78217](https://github.com/Dicklesworthstone/ntm/commit/1d78217748da2dbc3d71e5abad7b806b43db431c), [5f068f0](https://github.com/Dicklesworthstone/ntm/commit/5f068f011fa210171d6c0227f2f3934f5437cdb7))
- **Ollama model management** CLI with pull progress streaming ([722e6df](https://github.com/Dicklesworthstone/ntm/commit/722e6df81375b4a6ee02d7eec023fef71742f268))
- `ntm scale` command for manual agent fleet scaling ([2382fab](https://github.com/Dicklesworthstone/ntm/commit/2382fab77e7bf0b91233c242ce809860b67cd017))
- `ntm rebalance` for workload analysis ([948d0ca](https://github.com/Dicklesworthstone/ntm/commit/948d0ca32b76bd8d910ace1ed0592030485f18d9))
- `ntm context inject` for project context injection ([c353604](https://github.com/Dicklesworthstone/ntm/commit/c353604bad8d4e1592f7396ed8c76a8073afccb1))
- Review queue command for session triage ([b02f035](https://github.com/Dicklesworthstone/ntm/commit/b02f035f19cad646dbd153b201ba5c4424553830))

### Rate Limiting & Resilience

- Codex rate-limit detection with AIMD adaptive throttling ([af51043](https://github.com/Dicklesworthstone/ntm/commit/af510435811e4f627df9b4c07667b1a7a60887df))
- PID-based liveness checks replacing text-based detection ([bbac298](https://github.com/Dicklesworthstone/ntm/commit/bbac2984c3d5038d8a1b3e32571c4fe6ce0c30d1))
- Idle agent detection at Claude Code welcome screen ([c7026a7](https://github.com/Dicklesworthstone/ntm/commit/c7026a7051efd92ef277dac572b885f172548c7c))

### Prompt Delivery

- `--base-prompt` and `--base-prompt-file` flags for send ([0d2c450](https://github.com/Dicklesworthstone/ntm/commit/0d2c45054991a890d8254b111bfc43883c4596e7))
- `--priority-order` for batch prompts ([0f9351b](https://github.com/Dicklesworthstone/ntm/commit/0f9351b28153b8a6727c669f2c3d7d505982b9a1))
- `--randomize` and `--seed` for send ([2c6aac9](https://github.com/Dicklesworthstone/ntm/commit/2c6aac9c6d287e853d2e339783ece6d7c5f08a73))
- `--marching-orders` flag ([d2402bc](https://github.com/Dicklesworthstone/ntm/commit/d2402bc40070af01c9b1c9154c988ddb30af6cc9))

### Bug Fixes

- Buffer-based delivery for Claude Code multi-line prompts ([384f91b](https://github.com/Dicklesworthstone/ntm/commit/384f91b06b5f7c5be27b8f63289f9432372b26c7))
- Default Claude model updated to Opus 4.6 ([ee50ed3](https://github.com/Dicklesworthstone/ntm/commit/ee50ed3c10fd55611c10c0eabc6ae202e550a8a7))
- Gemini: drop `--system-instruction-file` from default template ([57658b3](https://github.com/Dicklesworthstone/ntm/commit/57658b3adc5b3a3feff979083553ab834b907ac4))
- Codex: replace obsolete `--enable web_search=live` with `--search` ([9def2d8](https://github.com/Dicklesworthstone/ntm/commit/9def2d89c3d192007cefa2419aa457072731f8ae))
- DefaultProjectsBase changed to `~/ntm_Dev` instead of `/tmp` on Linux ([290155a](https://github.com/Dicklesworthstone/ntm/commit/290155af932667785ea1f1538b341e9f84825784))

### Testing

- **Massive unit test coverage sprint**: 38 batches of serve handler tests pushing coverage from ~58% to ~82%, plus comprehensive branch coverage across robot, CLI, ensemble, coordinator, and state packages.

---

## [v1.7.0] -- 2026-02-02 **[GitHub Release]**

[Release page](https://github.com/Dicklesworthstone/ntm/releases/tag/v1.7.0) |
[Full diff](https://github.com/Dicklesworthstone/ntm/compare/v1.6.0...v1.7.0) |
**257 commits**

### Privacy & Redaction System

The defining feature of v1.7.0 is a comprehensive privacy/redaction engine:

- **Redaction engine** (`internal/redaction`) with configurable modes: off, warn, block ([937bfed](https://github.com/Dicklesworthstone/ntm/commit/937bfed39b55b27d8b18598f5e1a27e186331ab8))
- Redaction middleware for REST/WS and persistence layers ([b5d45e2](https://github.com/Dicklesworthstone/ntm/commit/b5d45e209faaa87ec6175863c79ca4b87d7268c5))
- PII detection via prompt linting with `ntm preflight` command ([34476836](https://github.com/Dicklesworthstone/ntm/commit/34476836ae8268d5f4d81710e36d9a57285cf8de), [d4de897](https://github.com/Dicklesworthstone/ntm/commit/d4de89798ddf4523bffb5c947ad80b41baccf0b9))
- `ntm scrub` for outbound notification/webhook redaction ([35e29651](https://github.com/Dicklesworthstone/ntm/commit/35e29651bd576bc85bcd2a034d9e9b45419f404c))
- Privacy mode gating for persistence ([d270fb7](https://github.com/Dicklesworthstone/ntm/commit/d270fb720678916abdc21cc1eebb4825441fc730))
- Privacy mode enforcement in support bundles ([33bb619](https://github.com/Dicklesworthstone/ntm/commit/33bb61965335ab14ad8627d33a595878fe7deae5))

### Support Bundles & Diagnostics

- `ntm support-bundle` command with zip-based archive and structured manifest ([9fdffd5](https://github.com/Dicklesworthstone/ntm/commit/9fdffd563f489995beab9fc302e6089caf8532a4))
- Manifest schema for support bundle indexing ([07f72754](https://github.com/Dicklesworthstone/ntm/commit/07f72754b08b72ac2608a5cfabf68240a49ccd67))

### Swarm Orchestration

- **Swarm orchestrator** with health monitoring ([d0338ba](https://github.com/Dicklesworthstone/ntm/commit/d0338bab5bfd9f5bd19f0a0b1fac0346bfa46724))
- `--auto-rotate-accounts` for API key rotation during swarm execution ([842ab81](https://github.com/Dicklesworthstone/ntm/commit/842ab817313d3ccb58b321318c5a67db7095fe6b))
- Graceful shutdown support ([54b3153](https://github.com/Dicklesworthstone/ntm/commit/54b3153ee700b2297afe8a62c2aed5cb608faf4a))
- Account rotation history persistence ([f8a866a](https://github.com/Dicklesworthstone/ntm/commit/f8a866a117adf63d045de440b8fa209939a8a367))
- Ollama agent type recognized ([d878a1b](https://github.com/Dicklesworthstone/ntm/commit/d878a1bb337e5eb923488d76b01bb1dd04bf10d3))
- `spawn --local` / `--ollama` for local Ollama agents ([24d0640](https://github.com/Dicklesworthstone/ntm/commit/24d06408c6bc0b4526c4e8e1c1eb543f6b4a4d5a))

### Webhook & Event System

- Built-in formatters for Slack, Discord, and Teams ([6b1b107](https://github.com/Dicklesworthstone/ntm/commit/6b1b1070ae0121e84373c4bb23120334a6993a75))
- Coordinator state transition events published ([5145d94](https://github.com/Dicklesworthstone/ntm/commit/5145d947bc8d6cadf08a1062d70daf3ebb3c0e66))
- WebSocket events for mail/reservations/pipeline/approvals ([c795e20](https://github.com/Dicklesworthstone/ntm/commit/c795e2053f6d9de01562a843e42d0ebf5c8a3ea3))

### TUI Improvements

- Smart animation with adaptive tick rate ([8ff4a21](https://github.com/Dicklesworthstone/ntm/commit/8ff4a2146701b1aba667e9e85bb0d39990a72561))
- Cost panel and scrub tests ([dc9d90c](https://github.com/Dicklesworthstone/ntm/commit/dc9d90c1b9191fe7b9b1bd8880acc7f554cf16ce))
- History panel with filtering, copy, and replay ([fc44ca7](https://github.com/Dicklesworthstone/ntm/commit/fc44ca70bf4f7517b5b978d3b975b926692d801e))

### Prompt Handling

- `--dry-run` preview for send command ([da03a18](https://github.com/Dicklesworthstone/ntm/commit/da03a18ad98f28649bf9adee4db131e91ec22ce7))
- History regex search and replay ([26ca5d3](https://github.com/Dicklesworthstone/ntm/commit/26ca5d3f13f46b5a247c4f2dfaf579927afe34d4))
- Context usage and token count in AgentStatus ([39c07c4](https://github.com/Dicklesworthstone/ntm/commit/39c07c4ff571638cca643a75b3f8bf6c3cf5634a))

### Bug Fixes

- Buffer-based paste for Gemini multi-line prompts ([459176b](https://github.com/Dicklesworthstone/ntm/commit/459176b43d478679ed244884d8c8e27d1f4f7de1))
- Unique buffer names to prevent tmux race conditions ([44f4a12](https://github.com/Dicklesworthstone/ntm/commit/44f4a124c8468803b47eeba1b99eb30c88370c1e))
- Windows stub for `createFIFO` ([d728f66](https://github.com/Dicklesworthstone/ntm/commit/d728f66b3b28c65710853f1adc077c8706b9576d))
- `--allow-secret` properly downgrades block to warn ([1d0398e](https://github.com/Dicklesworthstone/ntm/commit/1d0398e7a2ad24c7c9da796cad3aaf86002fee8e))

### Logging Migration

- Systematic migration from `fmt.Fprintf(os.Stderr, ...)` to structured `slog` logging across archive, checkpoint, scanner, events, persona, and utility modules.

---

## [v1.6.0] -- 2026-01-29 **[Tag Only]**

[Tag](https://github.com/Dicklesworthstone/ntm/releases/tag/v1.6.0) |
[Full diff](https://github.com/Dicklesworthstone/ntm/compare/v1.5.0...v1.6.0) |
**638 commits** (no published GitHub Release)

This was the largest development cycle in NTM's history. No GitHub Release was published, but the tag is real. Key capabilities landed:

### Ensemble Execution Engine

- **Ensemble modes and presets** robot API ([bf296da0](https://github.com/Dicklesworthstone/ntm/commit/bf296da0))
- Ensemble synthesis robot command ([e55ee239](https://github.com/Dicklesworthstone/ntm/commit/e55ee239))
- Findings deduplication engine with clustering ([18b5ecf6](https://github.com/Dicklesworthstone/ntm/commit/18b5ecf6))
- Velocity tracking for mode output analysis ([9da90d2a](https://github.com/Dicklesworthstone/ntm/commit/9da90d2a))
- Deterministic run comparison and JFP E2E tests ([90a76af6](https://github.com/Dicklesworthstone/ntm/commit/90a76af6))

### Robot API: Pagination, BV Integration, RCH

- Pagination infrastructure for robot module ([83b7db51](https://github.com/Dicklesworthstone/ntm/commit/83b7db51))
- Pagination support for status, snapshot, and history commands ([15333a5c](https://github.com/Dicklesworthstone/ntm/commit/15333a5c))
- BV (beads_viewer) integration commands ([bc5ca8df](https://github.com/Dicklesworthstone/ntm/commit/bc5ca8df))
- RCH status and workers integration ([41f8bd57](https://github.com/Dicklesworthstone/ntm/commit/41f8bd57))
- `ru sync` integration with robot mode ([5cd30843](https://github.com/Dicklesworthstone/ntm/commit/5cd30843))
- Meta Skill wrapper and TUI compare panel ([fe616a5a](https://github.com/Dicklesworthstone/ntm/commit/fe616a5a))

### Handoff & Assignment

- Handoff ledger command and ensemble conflict/coverage analysis ([fa857398](https://github.com/Dicklesworthstone/ntm/commit/fa857398))
- AI handoff writer utilities ([5609d766](https://github.com/Dicklesworthstone/ntm/commit/5609d766))
- Coordinator assign command flags for strategy, filtering, and templates ([9a16766d](https://github.com/Dicklesworthstone/ntm/commit/9a16766d))

### CLI Additions

- `ntm search` wired to CASS queries ([4ea9bf13](https://github.com/Dicklesworthstone/ntm/commit/4ea9bf13))
- `--robot-env` flag and improved env command ([888fa859](https://github.com/Dicklesworthstone/ntm/commit/888fa859))
- Timeline markers for prompt sending and session kill ([cb9dbdf3](https://github.com/Dicklesworthstone/ntm/commit/cb9dbdf3))
- Context usage included in agent routing decisions ([11b4ee56](https://github.com/Dicklesworthstone/ntm/commit/11b4ee56))

### Other

- `FlexTime` type for flexible timestamp parsing from Agent Mail ([b472c02a](https://github.com/Dicklesworthstone/ntm/commit/b472c02a))
- Session profiles for reusable spawn configs ([5c92c1ad](https://github.com/Dicklesworthstone/ntm/commit/5c92c1ad))
- Per-agent-type default prompts in config ([208df08a](https://github.com/Dicklesworthstone/ntm/commit/208df08a))
- Network activity panel (rano) in dashboard ([92dac596](https://github.com/Dicklesworthstone/ntm/commit/92dac596))

---

## [v1.5.0] -- 2026-01-06 **[GitHub Release]**

[Release page](https://github.com/Dicklesworthstone/ntm/releases/tag/v1.5.0) |
[Full diff](https://github.com/Dicklesworthstone/ntm/compare/v1.4.1...v1.5.0) |
**116 commits**

### Session Checkpointing & Time Travel

- Complete checkpoint save/list/show/delete/restore functionality ([1cef4e1](https://github.com/Dicklesworthstone/ntm/commit/1cef4e1afe09b5a262a7632162e060f7d4afdb1f), [15a238f](https://github.com/Dicklesworthstone/ntm/commit/15a238f009e315d9260f9265e5874e8b83fb1d5d))
- Session state capture with pane layout, scrollback, and git state
- Support for "latest" restore to quickly recover sessions

### Pipeline Loop Constructs

- For-each, while, and times loop execution ([edbd42e](https://github.com/Dicklesworthstone/ntm/commit/edbd42e8980886f7b00a1b3401302eb96bb9c545))
- Multi-channel notification system for pipeline events ([dcdfb91](https://github.com/Dicklesworthstone/ntm/commit/dcdfb91d78d73b633b8a90a9e219565c1dd3241b))
- Resume and cleanup commands for state persistence ([79adeb6](https://github.com/Dicklesworthstone/ntm/commit/79adeb69ecb71142353e35bf102b52569b42be0f))

### Multi-Agent Coordination

- Session coordinator package for multi-agent workflows ([73b7359](https://github.com/Dicklesworthstone/ntm/commit/73b7359e9216841f753d62211a80fe8b71e4ba56))
- CASS (Context-Aware Session Summary) injection into robot API ([30b9f1a](https://github.com/Dicklesworthstone/ntm/commit/30b9f1a2f4ac84a404d6e7c2584644f8b427de71))
- File conflict detection for multi-agent coordination ([3f2643d](https://github.com/Dicklesworthstone/ntm/commit/3f2643dd70b89dcca0ae4c74e5fe8a9c850ee87c))
- Agent capability profiles for intelligent task assignment ([fb80111](https://github.com/Dicklesworthstone/ntm/commit/fb80111fe6b9bbdadff4c59eaca34b5d291a5569))
- File reservation integration with Agent Mail ([f942bb2](https://github.com/Dicklesworthstone/ntm/commit/f942bb25eafa2676e56f27eb6e8a5235286098f2))

### Performance

- Parallelized agent setup, limited tmux queries ([eaeb05b](https://github.com/Dicklesworthstone/ntm/commit/eaeb05b6bc36a2308a47ea20bf17969792e367bb))
- Reduced lock contention in file watcher ([5bd241f](https://github.com/Dicklesworthstone/ntm/commit/5bd241fad3d69d054fccfc669259badd643f3c74))
- Optimized file I/O for large files ([127515d](https://github.com/Dicklesworthstone/ntm/commit/127515d7eade1d95358740e62220ad4eff9b39ed))
- Consolidated atomic writes to prevent corruption ([140ea27](https://github.com/Dicklesworthstone/ntm/commit/140ea2714134f39f89b2ce4ca4b75a7089324b51))

### Robot API Enhancements

- TUI parity commands (`--robot-alerts`, `--robot-beads-list`) ([ac7afae](https://github.com/Dicklesworthstone/ntm/commit/ac7afaeacf16870a4e67168bc0e1a5f0208de64d))
- Bead management robot commands ([004e66a](https://github.com/Dicklesworthstone/ntm/commit/004e66abc6af94164feb5bfd7231dee165c9e26a))
- Spawn progress panel with real-time countdown ([e483998](https://github.com/Dicklesworthstone/ntm/commit/e483998a486ce853a8b222e868c68574d30198f0))

### Upgrade Protection

- SHA256 checksum verification for downloads ([fff4377](https://github.com/Dicklesworthstone/ntm/commit/fff4377f526f570607b8f8945fe52cb6160f588f))
- Download progress indicator ([0c123ed](https://github.com/Dicklesworthstone/ntm/commit/0c123ed295c95289917f3bb60bebaebc11d8bc72))
- Post-upgrade binary verification ([e56a21c](https://github.com/Dicklesworthstone/ntm/commit/e56a21c4e6c3c5c88353194039867c8470cbbcf2))

---

## [v1.4.1] -- 2026-01-04 **[GitHub Release]**

[Release page](https://github.com/Dicklesworthstone/ntm/releases/tag/v1.4.1) |
[Full diff](https://github.com/Dicklesworthstone/ntm/compare/v1.4.0...v1.4.1) |
**10 commits** (patch release)

### Changes

- `--safety` flag for spawn to prevent accidental session reuse ([8692043](https://github.com/Dicklesworthstone/ntm/commit/8692043b3742a719d2885e6526b6fa736cfaba8c))
- Rune-aware truncation in health command ([6baa3c0](https://github.com/Dicklesworthstone/ntm/commit/6baa3c0539b357eac74f3ab9e223068e1f7c2157))
- Periodic health data refresh in dashboard ([3071680](https://github.com/Dicklesworthstone/ntm/commit/3071680be674cd2d59950d91ea6f49ac238e11af))
- Helpful hint added to `--spawn-safety` error message ([25dcc81](https://github.com/Dicklesworthstone/ntm/commit/25dcc816e3f9209b2b245d55c1f03883732d8341))

---

## [v1.4.0] -- 2026-01-04 **[GitHub Release]**

[Release page](https://github.com/Dicklesworthstone/ntm/releases/tag/v1.4.0) |
[Full diff](https://github.com/Dicklesworthstone/ntm/compare/v1.3.0...v1.4.0) |
**113 commits**

### HTTP Server & REST API

- Full HTTP server with REST API and SSE streaming (`ntm serve`) ([e72e654](https://github.com/Dicklesworthstone/ntm/commit/e72e654929bd44dee1c4d6b2d603ce52ab38b871))

### Safety & Policy System

- Policy package for destructive command protection ([3f9715e](https://github.com/Dicklesworthstone/ntm/commit/3f9715ee16c4ea4f3979ec90f6d9a951b86f37ef))
- Approval workflow engine ([1915a12](https://github.com/Dicklesworthstone/ntm/commit/1915a120832e621f5aa34357426d53422072539f))
- `ntm approve` command ([1690f29](https://github.com/Dicklesworthstone/ntm/commit/1690f2995e79f9a0b1964b3b920a624398454cfc))
- Design invariants enforcement ([51020a7](https://github.com/Dicklesworthstone/ntm/commit/51020a72f3e4c27299a2c42ae7927a02c7705ce5))

### Agent Health Monitoring

- `--robot-health=SESSION` per-agent health API ([596a059](https://github.com/Dicklesworthstone/ntm/commit/596a05968a4e02a88d30527267bfe6cf37d9cf10))
- Health state tracking for agents ([30867cf](https://github.com/Dicklesworthstone/ntm/commit/30867cfd43b6455868a9cc6f2d7687f8374d1b61))
- Automatic restart with alerting on agent crash ([1c0db5c](https://github.com/Dicklesworthstone/ntm/commit/1c0db5ce4081d69ff7e266915a82e4500de5d58a))
- Rate limit backoff with exponential growth ([bb4917c](https://github.com/Dicklesworthstone/ntm/commit/bb4917cd5d8cc390186665247a614e0bccb5fec7))
- Alerting system for health events ([99a99a8](https://github.com/Dicklesworthstone/ntm/commit/99a99a8548235191b0baebfb429cb292bd072672))
- Health indicators in dashboard pane cards ([53a4dd9](https://github.com/Dicklesworthstone/ntm/commit/53a4dd97e1506bea59559629a58953fce262b3c3))

### Ecosystem Integration

- `ntm doctor` command for ecosystem health validation ([aa57934](https://github.com/Dicklesworthstone/ntm/commit/aa579341951e62f5b305dd14ab650926ed9d6fed))
- `ntm setup` for project initialization ([189d804](https://github.com/Dicklesworthstone/ntm/commit/189d8044c40f8d4cf18f6721a272645c5fe28bcc))
- `ntm work` for intelligent work distribution ([3f26d19](https://github.com/Dicklesworthstone/ntm/commit/3f26d19900a20effe7e2250e19bf0aeeb050a5ae))
- `ntm guards` for Agent Mail pre-commit guards ([2f712c4](https://github.com/Dicklesworthstone/ntm/commit/2f712c448f43e769d86b56719ca541ee3ded7d42))
- File reservation lifecycle commands ([75480bb](https://github.com/Dicklesworthstone/ntm/commit/75480bb4b28fda65fcf601d485491e5da76273e0))
- Tool Adapter Framework for ecosystem integration ([cc816fa](https://github.com/Dicklesworthstone/ntm/commit/cc816fad24ddcea6de91331697c94f179e158d5d))

### State Management

- State Store and Daemon Supervisor foundation ([0ff3b3f](https://github.com/Dicklesworthstone/ntm/commit/0ff3b3fe059e9d3f3603f3d7d20fa33a0c81e948))
- Event log replay and recovery ([541cd74](https://github.com/Dicklesworthstone/ntm/commit/541cd74d9da99e35a022f67319abed58802609c8))
- Config diff and validation functions ([2846ff2](https://github.com/Dicklesworthstone/ntm/commit/2846ff29abbd5bda1a020ef2521a61ba5549c1fb))

### Bug Fixes

- Critical bugs fixed in safety wrapper scripts and policy YAML ([3c46d64](https://github.com/Dicklesworthstone/ntm/commit/3c46d64a9f1903b5a0b8124bf46a1884dd5c23d2))
- Data race prevented in recovery goroutine ([56ab502](https://github.com/Dicklesworthstone/ntm/commit/56ab5027027a3efc5d4ecf98e58b53eea39567ec))
- Restart race and data races fixed in supervisor ([429a674](https://github.com/Dicklesworthstone/ntm/commit/429a674b122e8040a187f488790c229104ae5d03))

---

## [v1.3.0] -- 2026-01-03 **[GitHub Release]**

[Release page](https://github.com/Dicklesworthstone/ntm/releases/tag/v1.3.0) |
[Full diff](https://github.com/Dicklesworthstone/ntm/compare/v1.2.0...v1.3.0) |
**176 commits**

### Pipeline Execution Engine

- Workflow schema, parser, and dependency resolution ([6b57807](https://github.com/Dicklesworthstone/ntm/commit/6b57807392b7811c7b23f1bdea6ace6866eb476e))
- Execution engine core with parallel step coordination ([c5c0308](https://github.com/Dicklesworthstone/ntm/commit/c5c030818faaabe7a3715f130f33afab340b9d27), [caa85f8](https://github.com/Dicklesworthstone/ntm/commit/caa85f8ff305a18aa7abfcc2de6c113f6f771bf4))
- Variable substitution and condition evaluation ([60cc758](https://github.com/Dicklesworthstone/ntm/commit/60cc758fc21728f792a7b7c490ea09f43688db96))
- `ntm pipeline run/status/list/cancel/exec` CLI subcommands ([0f8a3d3](https://github.com/Dicklesworthstone/ntm/commit/0f8a3d3a310521501ab4f91bea98799c9656daa7))
- Robot-pipeline APIs for workflow execution ([f13b761](https://github.com/Dicklesworthstone/ntm/commit/f13b761bba88cbd8fa95cae9aa207021894959e5))

### Context Window Rotation

- Token usage monitoring for context window rotation ([950b4c7](https://github.com/Dicklesworthstone/ntm/commit/950b4c7fe0091c8d3afd31f786907db47e265789))
- Graceful degradation before rotation ([ff68ec5](https://github.com/Dicklesworthstone/ntm/commit/ff68ec587b35c0e9a209e4c5353fd7e75f62d441))
- Handoff summary generation ([d989063](https://github.com/Dicklesworthstone/ntm/commit/d9890632cc3b712cc31bfe079adee4240a4ae323))
- Seamless agent rotation ([cf942c5](https://github.com/Dicklesworthstone/ntm/commit/cf942c567df905d242cebc79f54e7e0d422b717b))
- Configurable rotation thresholds ([d6fb654](https://github.com/Dicklesworthstone/ntm/commit/d6fb6540d974c3cdf4ced666d000ec507766f8a7))
- Rotation history and audit log ([c341a12](https://github.com/Dicklesworthstone/ntm/commit/c341a12ead6acad7547de71fcc4f0a2c0a0dab2d))

### Robot Mode Hardening (R1-R11)

- Duration string support for timeout flags (R1) ([1f93142](https://github.com/Dicklesworthstone/ntm/commit/1f93142da4e54e96f07019cc59dfce824f9f72f0))
- Standardized JSON error responses (R2) ([99609b9](https://github.com/Dicklesworthstone/ntm/commit/99609b9116226cdf2c1c9e8194a420fda7771350))
- Unified agent type aliasing (R3) ([e2dd3ed](https://github.com/Dicklesworthstone/ntm/commit/e2dd3ed0ab7b8ede48991b56eeb884edb0b66442))
- `--robot-schema` for JSON Schema generation (R8) ([cf9d26b](https://github.com/Dicklesworthstone/ntm/commit/cf9d26ba9be12c6a6753369e237f27ecc6ed5a5a))
- `--robot-route` with 7 routing strategies and fallback chain ([ee96b4b](https://github.com/Dicklesworthstone/ntm/commit/ee96b4b84a9be51101862e2a6823c80489d32bd9), [7e2d325](https://github.com/Dicklesworthstone/ntm/commit/7e2d3252235a4071140f97119180e5d2b13de058))
- `--robot-assign` for work distribution ([d82c278](https://github.com/Dicklesworthstone/ntm/commit/d82c27875506e7458611140637cea0b7f65f1201))
- `--robot-context`, `--robot-tokens`, `--robot-history` flags ([7e1e5cd](https://github.com/Dicklesworthstone/ntm/commit/7e1e5cdba51836e4702ba5055a5cb4a433cd24e9), [1deb8bd](https://github.com/Dicklesworthstone/ntm/commit/1deb8bdccb461267d1f0cf968131edeb63181a26))
- Agent activity detection system ([d9ab5b8](https://github.com/Dicklesworthstone/ntm/commit/d9ab5b826cf10cbd5c2e4cbc113bf4e45920ce68))
- Wait command and agent scoring system ([2ca746c](https://github.com/Dicklesworthstone/ntm/commit/2ca746c2bb0cc44b5f430c5fd17788fc0b601fb6))
- Smart routing with `--smart` and `--route` flags on send ([610316a](https://github.com/Dicklesworthstone/ntm/commit/610316af2820ccbcfd815b4601fc9c32a7073b4f))

### TUI Improvements (T1-T11)

- Responsive help bar (T1), context-sensitive keybinding hints (T2), scroll position indicators (T3), data freshness indicators (T4), enhanced empty states (T6), error recovery feedback (T7), enhanced focus visibility (T8), standardized truncation (T9), column visibility indicators (T10), fixed-width badge rendering (T11) ([37f451c](https://github.com/Dicklesworthstone/ntm/commit/37f451cbe98e15f10ad3ad681559755b3706cdda) through [9dcf4f6](https://github.com/Dicklesworthstone/ntm/commit/9dcf4f63b8cb9806e5044853710bfb84aec9f4fc))

### Spawning

- `--stagger` flag for thundering herd prevention ([4b2d14c](https://github.com/Dicklesworthstone/ntm/commit/4b2d14c2ed8f20e18bbfebea6bd1a824ae6cdbc9))
- PasteKeys with bracketed paste mode for reliable multiline input ([9212e2d](https://github.com/Dicklesworthstone/ntm/commit/9212e2d53d61e21e662f446710f17f1bcca0f576))

### Unified Event Bus

- Pub/sub communication for all subsystems ([8800f3d](https://github.com/Dicklesworthstone/ntm/commit/8800f3df7d8022e560d472e2678d70f77f98d0b4))

### Persona System Enhancements

- Profile inheritance, sets, focus patterns, and template vars ([2f51a77](https://github.com/Dicklesworthstone/ntm/commit/2f51a77c5ce04f8c4bb2ebb9f6db4448cf6136d9))

### VS Code Extension Scaffold

- Session tree view, file decoration for reservations, openTerminal command ([324ace6](https://github.com/Dicklesworthstone/ntm/commit/324ace6802ba2502559534b67785bc8857c1666a), [206edd1](https://github.com/Dicklesworthstone/ntm/commit/206edd161216ef93be0ed3cdee8c86f9dbe3e551), [9e2ecb8](https://github.com/Dicklesworthstone/ntm/commit/9e2ecb86aad027f36d043fc27d33f4c0f133973e))

### Performance

- Output render caching in dashboard ([b84b444](https://github.com/Dicklesworthstone/ntm/commit/b84b444513566e608349ff2911eca60a33390808))
- Semantic capture helpers with line budgets ([5c357c1](https://github.com/Dicklesworthstone/ntm/commit/5c357c133f8c097e327543005e7ae10d2fdd7109))
- Stream git diff directly to file in checkpoints ([41a0d24](https://github.com/Dicklesworthstone/ntm/commit/41a0d24a68fcac8add2fb44a33c98f90e9b6c1c9))

---

## [v1.2.0] -- 2025-12-14 **[GitHub Release]**

[Release page](https://github.com/Dicklesworthstone/ntm/releases/tag/v1.2.0) |
[Full diff](https://github.com/Dicklesworthstone/ntm/compare/v1.1.0...v1.2.0) |
**283 commits**

### CASS Integration

- Full CASS (Context-Aware Session Summary) Go client with robot mode flags ([5a86d62](https://github.com/Dicklesworthstone/ntm/commit/5a86d625fa66a3e26c59f9364ddf449d4a2feaec), [8eb688f](https://github.com/Dicklesworthstone/ntm/commit/8eb688f6f592799488b3cac50dbe63f15d999984))
- `ntm cass` CLI commands: status, search, insights, timeline ([5fb96dd](https://github.com/Dicklesworthstone/ntm/commit/5fb96dd4fe822d089c102c43cb43f1fd885022ac))
- Context injection and duplicate detection ([143a113](https://github.com/Dicklesworthstone/ntm/commit/143a113aaeee7f2966175800163c66876a4315de))
- CASS search palette in dashboard ([e24a314](https://github.com/Dicklesworthstone/ntm/commit/e24a3144324eb44939ad79c8bbcf23597bbeb32e))

### Dashboard 360-View

This release transformed the dashboard from a basic status display into a full cockpit:

- MetricsPanel and HistoryPanel components ([5c20bcd](https://github.com/Dicklesworthstone/ntm/commit/5c20bcd12e68bedc8dd56fdcfd2d77d70a038346))
- Files panel and CASS context panel in sidebar ([28fd034](https://github.com/Dicklesworthstone/ntm/commit/28fd034af907a4440c3362c38712c647b226740b))
- Panel focus system with beads and alerts components ([e1c6bd1](https://github.com/Dicklesworthstone/ntm/commit/e1c6bd1f1beea38b32f36bea75205daf7f32af9c))
- Glamour renderer for detail pane output ([5048c89](https://github.com/Dicklesworthstone/ntm/commit/5048c890b6c1a8788a1a9100e1540c82350cad89))
- Help overlay with '?' toggle ([d281f18](https://github.com/Dicklesworthstone/ntm/commit/d281f186387ebe0e00f71119aa5dba68a637484e))
- Per-agent border colors with pulse animation ([6dfb343](https://github.com/Dicklesworthstone/ntm/commit/6dfb343ec082a1d2d7c83abe5ee787ada7eb9031))
- Shimmer animation for high context warnings ([1aaf537](https://github.com/Dicklesworthstone/ntm/commit/1aaf5371f7584058256b7277232b21d0faabdc8e))
- Spinner animation for working state agents ([ac552b4](https://github.com/Dicklesworthstone/ntm/commit/ac552b4059125648907652b0a82cd17a1bbb0b2d))
- TierUltra and TierMega for ultra-wide display layouts ([1d262b9](https://github.com/Dicklesworthstone/ntm/commit/1d262b9279932e60464f8495bd1f0e6401a044a2))
- Catppuccin Latte light theme and auto-detection ([f42736a](https://github.com/Dicklesworthstone/ntm/commit/f42736a5f508a8c20deaba6427a465a5a1692f88))
- NO_COLOR standard support for accessibility ([ab90a66](https://github.com/Dicklesworthstone/ntm/commit/ab90a66d2e28be2ad72dc6e3fe1ecc400d56a843))

### CLI Expansion

- `ntm diff` for comparing agent output ([304b819](https://github.com/Dicklesworthstone/ntm/commit/304b8195041093cb43690277ac6257675bed7693))
- `ntm pipeline` for multi-stage execution ([402617f](https://github.com/Dicklesworthstone/ntm/commit/402617fbde485a9242322eede35a35197c8cf87b))
- `ntm quota` for provider usage fetching ([0f60f21](https://github.com/Dicklesworthstone/ntm/commit/0f60f21ee9dddb24eb55f4cd14c62d3aeebb1ecd))
- `ntm rotate` for account rotation ([836b222](https://github.com/Dicklesworthstone/ntm/commit/836b2229ca8320f2b3e039d195c3d27b573a8f83))
- `ntm copy` with pane selector, output file, and filtering ([a41b24a](https://github.com/Dicklesworthstone/ntm/commit/a41b24a51ab5021953944472c8222d50e6803865))
- `ntm changes` and `ntm conflicts` for file modification tracking ([249ad5c](https://github.com/Dicklesworthstone/ntm/commit/249ad5cddab70c71f81171dba36ef87f7c946556))
- `--ssh` flag on all commands for remote execution ([ddcfff6](https://github.com/Dicklesworthstone/ntm/commit/ddcfff648abeb5e3bff184c19bce172341fff7ed))
- `--tag` filtering for send, list, status, interrupt, and kill ([f8a9dad](https://github.com/Dicklesworthstone/ntm/commit/f8a9dadf244e3062f2f310ab301ce04fcf0fc267))
- Clipboard integration ([6d6fb72](https://github.com/Dicklesworthstone/ntm/commit/6d6fb720211116eefa4ad937f577472574a83c97))

### Robot Mode

- `--robot-dashboard` for AI orchestrator consumption ([f13c127](https://github.com/Dicklesworthstone/ntm/commit/f13c127c5bb89267bde0580e44b38dca1cb350e1))
- `--robot-markdown` for token-efficient LLM output ([eb13a91](https://github.com/Dicklesworthstone/ntm/commit/eb13a912e3704fb5268849ef6bbb63b66db26c79))
- `--robot-save` / `--robot-restore` for session persistence ([bfbe639](https://github.com/Dicklesworthstone/ntm/commit/bfbe6399941a0e15cb7232792e9b33b9bdcae09a))
- Agent Mail integration with pane mapping and conflict detection ([bfa030a](https://github.com/Dicklesworthstone/ntm/commit/bfa030ac7f8f090d4ea7eb95aa3df36fc7d649f5))

### Persona System

- Role-based agent spawning with persona definitions ([1ea7fcf](https://github.com/Dicklesworthstone/ntm/commit/1ea7fcf3bd3e4ddeb4b296db47575acdfa4a543d))

### Plugin System

- Command plugin support and custom agent definitions via TOML ([f28750](https://github.com/Dicklesworthstone/ntm/commit/f28750565d1f41cad22e3fc284055781fedc606d), [cf2fb96](https://github.com/Dicklesworthstone/ntm/commit/cf2fb9666821d87885ab16195e381cf941eace9f))

### Configuration

- Per-project configuration via `.ntm/config.toml` ([e9222b6](https://github.com/Dicklesworthstone/ntm/commit/e9222b691eeee7e448f3eed92db76f85bd12c34a))
- Config file watcher and theme configuration ([8bcc773](https://github.com/Dicklesworthstone/ntm/commit/8bcc773779e0a2033910771512dae724ab19bfe5))
- Account rotation configuration schema ([3fa8af3](https://github.com/Dicklesworthstone/ntm/commit/3fa8af335182141a7f5261873663de8242c0c21d))

### VS Code Extension

- Initial extension scaffold with webview dashboard ([324ace6](https://github.com/Dicklesworthstone/ntm/commit/324ace6802ba2502559534b67785bc8857c1666a))
- CLI wrapper and status bar integration ([e77f069](https://github.com/Dicklesworthstone/ntm/commit/e77f0695c9fcb0715a1bed7d7988f6aef14c7307))

### Security

- Command sanitization across all launch paths ([074005c](https://github.com/Dicklesworthstone/ntm/commit/074005cae122c5eaf16a97e569d2387cd4d3349b), [a001336](https://github.com/Dicklesworthstone/ntm/commit/a0013369f49030538b42a0c95a78a4025b71b7d2))
- Model name validation to prevent shell injection ([7b94278](https://github.com/Dicklesworthstone/ntm/commit/7b94278b0a9b5e3ccbf4f94899a35085f56dc745))

---

## [v1.1.0] -- 2025-12-10 **[GitHub Release]**

[Release page](https://github.com/Dicklesworthstone/ntm/releases/tag/v1.1.0) |
[Full diff](https://github.com/Dicklesworthstone/ntm/compare/v1.0.0...v1.1.0) |
**12 commits**

A quick follow-up release on the same day as v1.0.0, focusing on stability:

- **File change tracking** with agent attribution ([8e98d07](https://github.com/Dicklesworthstone/ntm/commit/8e98d074668b35cf5cc7939b1da2217ceb10d282))
- **Polling-based fallback** for file watching with automatic fsnotify fallback ([b27d530](https://github.com/Dicklesworthstone/ntm/commit/b27d530ad1f598003e8300355ca58204ddb6bff0), [bb2ac55](https://github.com/Dicklesworthstone/ntm/commit/bb2ac557191584dac12fe7e7a8d2286e2a2d61bf))
- **Pre-commit guard** install/uninstall for Agent Mail ([f4b980c](https://github.com/Dicklesworthstone/ntm/commit/f4b980c5d0baac2a0dff0c73073853836a5c3fc9))
- Pane row rendering and context bar bounds fixes ([f543594](https://github.com/Dicklesworthstone/ntm/commit/f5435946a9458127f68c7eb365e608004e41ae1b))
- Responsive breakpoints aligned with design tokens ([b0ed46e](https://github.com/Dicklesworthstone/ntm/commit/b0ed46e0df7a9fc660801d191ffffd740609c4df))

---

## [v1.0.0] -- 2025-12-10 **[GitHub Release]**

[Release page](https://github.com/Dicklesworthstone/ntm/releases/tag/v1.0.0) |
**192 commits** | Initial release

The first public release of NTM, built from scratch in Go 1.25.

### Core Session Management

- **Spawn multi-agent sessions**: `ntm spawn myproject --cc=2 --cod=1 --gmi=1`
- **Named panes**: Each agent gets a labeled tmux pane (e.g., `myproject__cc_1`)
- **Broadcast prompts**: `ntm send myproject --cc "your prompt here"`
- **Session persistence**: Detach and reattach without losing agent state
- **Quick project setup**: Create directory, init git, spawn agents in one command

### TUI Dashboard

- Animated gradient header and Catppuccin theming
- Split view rendering for wide terminals ([0bcb773](https://github.com/Dicklesworthstone/ntm/commit/0bcb77354516382529c4becbd86186a752ad521b))
- Command palette with fuzzy search, pinned commands, and shimmer effects
- Interactive tutorial with animated slides ([db11ad5](https://github.com/Dicklesworthstone/ntm/commit/db11ad5ecaff6106f7e3f08f88da8c9de6284986))

### Robot Mode for AI Orchestration

- `--robot-status`, `--robot-list`, `--robot-send`, `--robot-spawn` for structured JSON output ([cb57dcd](https://github.com/Dicklesworthstone/ntm/commit/cb57dcda987bf59eb531ed7a8e9816b138418124), [25f6431](https://github.com/Dicklesworthstone/ntm/commit/25f64314cac70ceacaf157b5bc39ba5a48a5de9b))
- `--robot-terse` for ultra-compact output ([db4ecae](https://github.com/Dicklesworthstone/ntm/commit/db4ecae9ee4fe913cf9faac44c1d5809c1678515))
- `--robot-ack` for send confirmation tracking ([71cfce7](https://github.com/Dicklesworthstone/ntm/commit/71cfce748c36244a6f12e839c6a8d784259f8ec5))
- `--robot-interrupt` for priority course correction ([41598cb](https://github.com/Dicklesworthstone/ntm/commit/41598cb43db48477002746b63036ac074a07016c))
- `--json` support across spawn, create, add, send, and status commands

### Agent Intelligence

- Compaction detection and auto-recovery for AI agents ([4be24a4](https://github.com/Dicklesworthstone/ntm/commit/4be24a47d259d1dc665bea0a6af85f86ca432e3b))
- Agent health checking system with rate limit detection ([d277455](https://github.com/Dicklesworthstone/ntm/commit/d2774556f85a16be50463ab021a36c6125e07c8a))
- Auto-restart for crashed agents ([f4f5fca](https://github.com/Dicklesworthstone/ntm/commit/f4f5fca9137d74926a5a04103c46d07d459d7a28))
- Model variant support (e.g., `ntm spawn --cc=opus4 --cc=sonnet4`)

### Ecosystem Integrations

- Agent Mail client with session auto-registration ([4e2f305](https://github.com/Dicklesworthstone/ntm/commit/4e2f305a2b6f571a590b75dc62db7daccebb4178))
- CASS client types for context-aware routing ([918c2a9](https://github.com/Dicklesworthstone/ntm/commit/918c2a983958e75226fcce61d127f9f4c87ae552))
- BV (beads_viewer) integration package ([b259085](https://github.com/Dicklesworthstone/ntm/commit/b259085a52c7a60b962245a7e788645a5a36aa63))
- UBS (Ultimate Bug Scanner) Go integration and Beads bridge ([4265824](https://github.com/Dicklesworthstone/ntm/commit/42658241cc0706e88ecb89494cddd4dd2f259b57))
- Self-update command for seamless upgrades ([7a2c1dd](https://github.com/Dicklesworthstone/ntm/commit/7a2c1ddb3b168bcddde0b3f2577a084a423ed0df))

### CLI Commands

- `ntm grep` for pane output search ([fa30ffb](https://github.com/Dicklesworthstone/ntm/commit/fa30ffb6facc7cb4ce746b937d36d02568fb89f9))
- `ntm history` for prompt management ([8af6eb1](https://github.com/Dicklesworthstone/ntm/commit/8af6eb174ac957682fbdf80eef9f7f0f0d8f71dd))
- `ntm recipes` for session presets ([ed832de](https://github.com/Dicklesworthstone/ntm/commit/ed832dee6fe081e2e9bd3bbe7f233abae1150382))
- `ntm scan` for UBS integration ([155cd7a](https://github.com/Dicklesworthstone/ntm/commit/155cd7a884914e52b7b462c96988fdaca9fa46f5))
- `ntm extract` with copy, apply, and select flags
- `ntm analytics` for session usage statistics
- Pre/post-send hooks ([9cdf6bc](https://github.com/Dicklesworthstone/ntm/commit/9cdf6bc866d59041d42564887436b5f0d2d5e00b))

### Infrastructure

- CI/CD pipeline with GoReleaser and GitHub Actions ([155b458](https://github.com/Dicklesworthstone/ntm/commit/155b4584fea5fb1745ce0fab2b85df23ec425aa1))
- Go 1.25, modernized install script ([ac58aa1](https://github.com/Dicklesworthstone/ntm/commit/ac58aa13bffac5d07130581440b052710fd5ad30))
- JSONL event logging framework ([3e0df4d](https://github.com/Dicklesworthstone/ntm/commit/3e0df4ddd36ddf9f803d9c4d1f9b2c25d75e26b5))
- Profiler package for startup timing ([7ccf8e5](https://github.com/Dicklesworthstone/ntm/commit/7ccf8e5ac745da924ca8cb6ddd95b68a1ba3245e))
- Cross-platform support with Windows/FreeBSD build stubs

---

*First commit: [5df73386](https://github.com/Dicklesworthstone/ntm/commit/5df733861a8931f2352cebf9fc120281c90da67d) on 2025-12-10.*
