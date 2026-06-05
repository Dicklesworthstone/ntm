# Dependency Upgrade Log

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
