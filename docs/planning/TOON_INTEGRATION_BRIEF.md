# TOON Integration Brief: ntm (Named Tmux Manager)

Date: 2026-01-24
Bead: bd-1p4

## 1) Output Surfaces (robot)
Robot mode is exposed via `ntm --robot-*` flags (stdout data, stderr diagnostics). Key surfaces:
- State/inspection: `--robot-status`, `--robot-context=SESSION`, `--robot-snapshot`, `--robot-tail=SESSION`, `--robot-inspect-pane=SESSION`, `--robot-files=SESSION`, `--robot-metrics=SESSION`, `--robot-history=SESSION`, `--robot-tokens`, `--robot-health`, `--robot-version`, `--robot-help`, `--robot-capabilities`
- Control: `--robot-send=SESSION`, `--robot-ack=SESSION`, `--robot-spawn=SESSION`, `--robot-interrupt=SESSION`, `--robot-restart-pane=SESSION`
- Planner/graph/palette: `--robot-plan`, `--robot-graph`, `--robot-palette`, `--robot-dashboard`, `--robot-markdown`
- Beads/alerts/mail/CASS: `--robot-bead-*`, `--robot-alerts`, `--robot-dismiss-alert*`, `--robot-mail`, `--robot-cass-*`
- Terse special case: `--robot-terse` (single-line compact summary; not JSON/TOON)

## 2) Serialization Entry Points (files + functions)
Core renderer + encoder:
- `internal/robot/renderer.go`: `Render(payload, format)`, `Output(payload, format)`
  - Formats: `FormatJSON`, `FormatTOON`, `FormatAuto` (auto currently defaults to JSON)
- `internal/robot/robot.go`: `encodeJSON(v)` delegates to `Output(applyVerbosity(v), OutputFormat)`
- `internal/robot/types.go`: `outputJSON(v)` just calls `encodeJSON`

TOON encoder backend:
- `internal/robot/toon.go`: `toonEncode(payload, delimiter)` shells out to `tru --encode`
  - Accepts `TOON_TRU_BIN` or `TOON_BIN` env overrides
  - Refuses Node.js `toon` CLI; verifies `tru` via `--help/--version`

Most robot commands call `encodeJSON(...)` directly in `internal/robot/*.go`.

## 3) Format Flags & Env Precedence (current behavior)
Output format is already implemented and wired:
1. CLI flag: `--robot-format=json|toon|auto`
2. Env var: `NTM_ROBOT_FORMAT`
3. Default: `auto` (currently resolves to JSON in `GetRenderer`)

Verbosity is separate:
1. CLI flag: `--robot-verbosity=terse|default|debug`
2. Env var: `NTM_ROBOT_VERBOSITY`
3. Config: `~/.config/ntm/config.toml` → `[robot] verbosity = "default"`
4. Default: `default`

Special case:
- `--robot-terse` emits a **single-line** compact format and ignores `--robot-format` / `--robot-verbosity`.

Note: `config.toml` has `[robot.output] format = "json|toon"`, but this is **not wired** into `resolveRobotFormat()` today; only flag/env are honored.

## 4) TOON Strategy (already implemented)
- Use `--robot-format=toon` to switch to TOON output.
- TOON encoding uses `tru` (toon_rust) for canonical output.
- `FormatAuto` currently returns JSON; future auto-detection can be layered without changing call sites.
- On unsupported shapes or missing `tru`, TOON returns an error; callers typically propagate and exit non-zero.

## 4.1) Go Integration Approach (subprocess, not CGO/WASM)
Decision: call toon_rust via subprocess (`tru --encode`) from Go (see `internal/robot/toon.go`).

Rationale:
- Robot mode output is typically low frequency and human-paced (or low Hz automation).
- Subprocess keeps the Go build pure (no CGO), avoids cross-platform Rust toolchain coupling, and makes upgrades easier.

Quick local benchmark (this machine):
- `tru --version`: `tru 0.1.1`
- `tru --encode` (50 iterations, small JSON payload, includes process startup): avg ~3.45ms/call

If we ever need higher throughput, prefer a long-lived worker process (stdin/stdout) before considering CGO.

## 5) Protocol Constraints
- JSON remains the default and must stay backward compatible.
- TOON is opt-in (`--robot-format=toon`).
- `--robot-terse` remains its own compact line protocol (not TOON).
- Stdout stays data-only; stderr for warnings/diagnostics.

## 6) Docs to Update / Verify
- `README.md` → Robot Mode section (already documents `--robot-format=json|toon|auto`, verbosity, and TOON caveats).
- `docs/robot-api-design.md` → Mentions TOON opt-in and flag table (already present).
- Consider adding a short note about `TOON_TRU_BIN` / `TOON_BIN` env overrides and failure mode.

## 7) Fixtures to Capture (future)
Suggested capture commands (safe):
- `ntm --robot-status --robot-format=json`
- `ntm --robot-status --robot-format=toon`
- `ntm --robot-context=SESSION --robot-format=toon`
- `ntm --robot-files=SESSION --robot-format=toon`
- `ntm --robot-capabilities --robot-format=toon`

Store in `testdata/fixtures/robot/` (or agreed fixture location).

## 8) Test Plan
Unit tests:
- Flag precedence: `--robot-format` > `NTM_ROBOT_FORMAT` > default auto.
- TOON render: `Render(payload, FormatTOON)` succeeds for uniform arrays + simple objects.
- Failure path: missing `tru` returns error; ensure error code and hint propagate.

E2E (optional):
- Run `ntm --robot-status --robot-format=toon`, decode TOON and compare JSON equivalence.
- Include `--robot-verbosity=debug` to verify debug block in TOON/JSON.

## 9) Risks & Edge Cases
- Any robot command producing nested or irregular shapes may fail in TOON (by design).
- Missing `tru` or bad `TOON_TRU_BIN` path causes TOON failures; JSON default remains safe.
- `config.toml [robot.output.format]` not yet respected by `resolveRobotFormat()`.
