# Dependency Upgrade Log

**Date:** 2026-11-22  |  **Project:** ntm  |  **Language:** Go

## Summary (2026-11-22)

- **Method:** `go get -u ./...`, then holds (below), `go mod tidy`. `web/` and `vscode/` were refreshed on 2026-11-20 and were not touched.
- **Go toolchain:** `go.mod` `go 1.26.10` -> `go 1.26.11`, with the workflow `GO_VERSION` pins and the version notes in `AGENTS.md`, `README.md` and `CONTRIBUTING.md` kept in sync. `govulncheck` reported GO-2026-6859 in `net/http@go1.26.10` (reachable from `internal/context`), fixed in go1.26.11. Go 1.27 was not adopted in this pass.
- **Updated (direct):** `charmbracelet/x/ansi` v0.11.8 -> v0.11.10, `go-chi/chi/v5` v5.3.2 -> v5.5.0, `mattn/go-runewidth` v0.0.30 -> v0.0.31, `sergi/go-diff` v1.4.0 -> v1.4.5, `shirou/gopsutil/v4` v4.26.9 -> v4.26.10, `spf13/pflag` v1.0.10 -> v1.1.0, `yuin/goldmark` v1.8.2 -> v1.8.7, `golang.org/x/sys` v0.49.0 -> v0.50.0, `golang.org/x/term` v0.47.0 -> v0.48.0, `golang.org/x/tools` v0.51.0 -> v0.53.0.
- **Updated (indirect):** go-humanize v1.1.0, purego v0.11.3, go-colorful v1.4.1, go-strftime v1.1.0, sahilm/fuzzy v0.1.3, xo/terminfo v1.3.1, charmbracelet/x/exp/slice v0.1.0, x/mod v0.43.0, x/net v0.63.0, x/sync v0.25.0, x/text v0.44.0, x/telemetry, plan9stats, perfstat. `golang.org/x/exp` dropped by tidy.
- **Held:**
  - `chromedp/chromedp` v0.16.0 and `chromedp/cdproto`: v0.17+ is an API redesign (an earlier bump was reverted for the e2e and serve web UI tests).
  - `alecthomas/chroma/v2` v2.24.1: v2.27.0 (which moves to `dlclark/regexp2/v2`) made glamour rendering of a code-heavy document use about 7.5% more CPU (300 renders, 7 interleaved rounds, median CPU 3071 ms -> 3302 ms; wall 2327 -> 2368 ms). glamour renders markdown in the dashboard, so it stays.
  - `modernc.org/sqlite` v1.60.1 and `modernc.org/libc` v1.77.1: v1.63.0 (with the libc v1.80.1 it requires) is faster on an sqlite workload (median 3094 -> 2889 ms, CPU 2594 -> 2316 ms), but in two separate interleaved runs of the dashboard benchmarks the binaries linking it were slower (`BenchmarkUltraLayout_100` +9.5% and +9.7%, `BenchmarkDashboardUpdate` +9.9% and +2.7%, with identical allocations), while the same update set without it was flat. Held until that is understood.
- **Performance gate (dashboard/components benchmarks, 6 interleaved rounds with alternating order, median vs base):** the shipped set is within -3.4%..+1.7% on every benchmark (`DashboardUpdate` -3.4%, `DashboardView` -1.5%, `DashboardViewWide` -2.5%, `MegaLayout_100` -0.3%, `PaneList_Wide_1000` -2.5%, `UltraLayout_100` +1.7%, `StylesProgressBar` -0.6%, `StylesProgressBar3Color` -2.6%). An A/A control of two identical base builds moved by up to 6% on this host, so differences under that are noise.
- **Gates (macOS, `go test -short -p 4 -count=1 ./...`, final tree on go1.26.11):** build clean; 110 packages pass. The same 18 packages fail before and after the update, all failing at the base commit on this host too (91 failing tests at base, 93 with the update; the 2 extra, `TestRestoreLifecyclePreservesPaneAfterUncertainStartup` and `TestSendCASSCommandCapacityMeasurementLifecycle`, pass 3/3 when run alone). `go vet ./...` fails before and after on `internal/pipeline/process_group_unix_test.go`, which uses a Linux-only symbol under a `linux || darwin` build tag. `gofmt -l` lists `internal/cli/resume.go` and `internal/robot/markdown.go`, unchanged by this update. `go mod tidy -diff` is clean.
- **Audit:** `govulncheck ./...`: no vulnerabilities after the toolchain bump (1 standard-library finding before it).

---

**Date:** 2026-11-20  |  **Project:** ntm  |  **Languages:** Go + `web/` Next.js Cockpit + `vscode/` Extension

## Summary (2026-11-20)

- **Go toolchain:** `go.mod` pinned at `go 1.26.10` with `ignore ./web/node_modules` so `go list ./...` never traverses frontend packages; workflows (`ci.yml`, `release.yml`, `e2e-tests.yml`) and docs (`AGENTS.md`, `README.md`, `CONTRIBUTING.md`) synchronized to `1.26.10`.
- **Web Cockpit (`web/package.json`):** Upgraded `next` (`^16.4.6`), `eslint-config-next` (`^16.4.6`), `zod` (`^4.6.7`), added overrides for `sharp` (`>=0.35.5`) and `source-map-js` (`>=1.2.4`), and pinned `engines.node >=22.12.0`. `npm audit --omit=dev` reports **0 vulnerabilities**; `typecheck`, `lint` (0 errors/0 warnings), `test:run` (98/98), and `next build` (18/18 static routes) all pass.
- **VS Code Extension (`vscode/`):** Fixed async `setInterval` leak and unbounded test hang; `npm run compile` and `npm test` (42/42 in ~0.3s) pass cleanly.

---

**Date:** 2026-08-03  |  **Project:** ntm  |  **Language:** Go

## Summary

- **Toolchain:** Homebrew Go 1.25.6 → 1.26.5 (latest stable); `go.mod` directive 1.26.3 → 1.26.5
- **Updated:** 9 direct dependencies (all minor/patch; no breaking changes hit)
- **Skipped:** `charmbracelet/bubbletea` (local `third_party/` replace — never touch), all other directs already latest
- **Failed:** 0

## Updates (direct dependencies)

| Dependency | From | To | Tests |
|---|---|---|---|
| golang.org/x/sys | v0.44.0 | v0.47.0 | ✓ |
| golang.org/x/term | v0.43.0 | v0.45.0 | ✓ |
| github.com/chromedp/chromedp | v0.15.1 | v0.16.0 | ✓ |
| github.com/chromedp/cdproto | 20260427 | 20260719 | ✓ |
| github.com/go-chi/chi/v5 | v5.2.5 | v5.3.1 | ✓ |
| github.com/mattn/go-isatty | v0.0.22 | v0.0.24 | ✓ |
| github.com/mattn/go-runewidth | v0.0.23 | v0.0.27 | ✓ |
| github.com/shirou/gopsutil/v4 | v4.26.4 | v4.26.7 | ✓ |
| modernc.org/sqlite | v1.50.1 | v1.56.0 | ✓ |

Indirects (x/net, x/text, x/tools, modernc.org/libc, purego, pprof, …) rolled forward via `go get`/`go mod tidy`.

## Pre-existing failures found during validation (NOT caused by the updates — verified by re-running against baseline go.mod)

1. **`internal/bv` corruption recovery** used `br sync --rebuild`, which br 0.2.19 rejects (`--rebuild` now requires `--import-only`). Fixed in `internal/bv/bv.go`.
2. **`internal/bv` claim/release content-hash finalization**: br 0.2.19's fsqlite engine silently loses its DB-side export bookkeeping (`export_hashes` insert, `dirty_issues` clear) when another process holds the DB open, while still exiting 0 and reporting `cleared_dirty`. Added a Go-side fallback that computes br's content hash (byte-compatible with `beads_rust/src/util/hash.rs`) and finalizes locally. Upstream br bug worth filing separately.
3. **`internal/robot` `QueryCASS`** shelled out to `cass` with no timeout; a wedged cass index hung tests (and would hang dispatch) indefinitely. Added `CASSConfig.Timeout` (default 15s) with `exec.CommandContext`.
4. **`internal/swarm` `TestGetPaneTarget`** depends on live tmux state: it fails when a real session named `test` exists (one did, with base-index 0). Test-isolation issue; bead filed.
5. `internal/cli` / `internal/completion` tests flaked once under full-suite parallel load; both pass in isolation and on re-run.

---

# Previous run

**Date:** 2026-05-16  |  **Project:** ntm  |  **Languages:** Go + bundled web/vscode manifests

## Summary

- **Updated:** Go toolchain target + 4 direct dependencies
- **Skipped:** 9 module-graph-only Go entries, 2 Node latest-major candidates
- **Failed:** 0
- **Needs attention:** 0 release blockers

## Discovery

- Primary Go manifest: `go.mod`; lock file: `go.sum`.
- Additional tracked manifests checked: `web/package.json`, `web/package-lock.json`, `vscode/package.json`.
- Local `/dp` Go modules checked: no imported or replaced module from `/dp` is used by `go.mod`.
- Local Go replace preserved: `github.com/charmbracelet/bubbletea => ./third_party/bubbletea`.
- Go toolchain target was raised to `go 1.26.3` so the module builds and scans against the current patched 1.26 runtime.
- `go mod tidy -diff` is clean after the update pass.
- Node checks were run with the FNM Node binary ahead of Bun on `PATH`; the default shell currently resolves `node` to Bun's wrapper.

## Updates

### web: @types/node: ^25.7.0 -> ^25.8.0

- **Research:** `npm outdated --depth=0` reported 25.8.0 as both wanted and latest.
- **Breaking:** None in ntm web code.
- **Transitive lockfile change:** `undici-types` moved from 7.21.0 to 7.24.6.
- **Tests:** Passed `npm run test:run` after the update.

### web: @vitejs/plugin-react: ^6.0.1 -> ^6.0.2

- **Research:** `npm outdated --depth=0` reported 6.0.2 as both wanted and latest.
- **Breaking:** None in ntm web code.
- **Transitive lockfile change:** `@rolldown/pluginutils` moved from 1.0.0-rc.7 to 1.0.1.
- **Tests:** Passed `npm run test:run` after the update.

### vscode: @types/node: ^25.7.0 -> ^25.8.0

- **Research:** `npm outdated --depth=0` reported 25.8.0 as latest.
- **Breaking:** None in extension code.
- **Tests:** Passed `npm run compile` after the update.

### vscode: typescript: ^5.9.3 -> ^6.0.3

- **Research:** `npm outdated --depth=0` reported 6.0.3 as latest.
- **Migration:** Added explicit `"types": ["node", "vscode"]` to `vscode/tsconfig.json`; TypeScript 6 no longer inferred the ambient globals this extension relies on.
- **Tests:** Passed `npm run compile` after the tsconfig fix.

## Skipped

### Go module-graph-only stale entries

- `go list -m -u all` reports newer versions for these modules: `github.com/charmbracelet/x/errors`, `github.com/cpuguy83/go-md2man/v2`, `github.com/ianlancetaylor/demangle`, `github.com/kr/pty`, `github.com/pkg/diff`, `github.com/stretchr/objx`, and `modernc.org/ccgo/v4`.
- None of those modules appears in `go list -deps -test ./...`; they come from dependency module requirements or dependency test-only edges rather than ntm's compile/test package closure.
- Forcing an explicit indirect pin makes `go mod tidy -diff` dirty, and CI has a tidy check. The tidy-clean state is the correct release state.

### web: typescript 5.9.3 -> 6.0.3

- **Reason:** `openapi-typescript@7.13.0` is the latest release and still declares peer dependency `typescript: ^5.x`.
- **Action:** Kept TypeScript 5.9.3 in `web/` to avoid peer warnings.

### web: eslint 9.39.4 -> 10.4.0

- **Reason:** Installing ESLint 10 emits peer override warnings from the current `eslint-config-next` plugin stack (`eslint-plugin-import`, `eslint-plugin-jsx-a11y`, and `eslint-plugin-react` still cap their peers at ESLint 9).
- **Action:** Restored ESLint 9.39.4 so `npm install`, lint, and release checks remain warning-free.

## Failed

None.

## Prior Release Notes Preserved

- `third_party/bubbletea` intentionally preserves the NTM-local `tea_init.go` behavior that avoids Bubble Tea's eager terminal background probe. The local `/data/projects/charmed_rust/legacy_bubbletea` checkout previously included that upstream probe again, so this pass does not blindly copy that file over the NTM patch.
- `chromedp` v0.15.1 requires Go 1.26, so the root module continues to target `go 1.26`.
- NTM keeps a local patched Bubble Tea replacement, so versioned `go install github.com/Dicklesworthstone/ntm/cmd/ntm@...` is not a supported install path. Source-build instructions should clone the repo first and run `go install ./cmd/ntm` inside the checkout.
- Release-template source-build and install-script fallback snippets should pin the requested tag before running `go install ./cmd/ntm`.
- `next` 16.2.6 bundles a vulnerable PostCSS version for audit purposes, so `web/package.json` continues to override PostCSS to 8.5.14.

## Verification

- `npm run test:run` in `web/` after `@types/node` update.
- `npm run test:run` in `web/` after `@vitejs/plugin-react` update.
- `npm run lint` in `web/`.
- `npm run build` in `web/`.
- `npm audit --audit-level=moderate --json` in `web/`: 0 vulnerabilities.
- `npm run compile` in `vscode/` after `@types/node` update.
- `npm run compile` in `vscode/` after TypeScript 6 + tsconfig update.
- `npm audit --audit-level=moderate --json` in `vscode/`: 0 vulnerabilities.
- `go build ./cmd/ntm`.
- `go mod tidy -diff`.
- `go test -short ./... -count=1` with current repo binary on `PATH`.
- `go test -v ./... -count=1` with `PATH=/data/projects/ntm:$PATH` and `E2E_NTM_BIN=/data/projects/ntm/ntm`, so E2E subprocesses use the just-built binary instead of the stale installed v1.14.0 binary.
- `golangci-lint run --timeout=5m`: 0 issues.
- `govulncheck ./...`: no vulnerabilities.
- `git ls-files '*.go' | xargs gofmt -l`: clean.
- `git ls-files '*.go' | xargs goimports -l`: clean.
