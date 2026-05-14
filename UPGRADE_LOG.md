# Dependency Upgrade Log

Started: 2026-05-14

Target release: v1.15.0

## Scope

- Go module dependencies in `go.mod` / `go.sum`.
- Vendored local Bubble Tea replacement in `third_party/bubbletea`.
- Web and VS Code package manifests.
- Release-preparation verification before tagging or publishing.

## Notes

- `third_party/bubbletea` intentionally preserves the NTM-local `tea_init.go` behavior that avoids Bubble Tea's eager terminal background probe. The local `/data/projects/charmed_rust/legacy_bubbletea` checkout currently includes that upstream probe again, so this pass does not blindly copy that file over the NTM patch.
- `chromedp` v0.15.1 requires Go 1.26, so the root module now targets `go 1.26`.
- The web app uses TypeScript 5.9.3 and ESLint 9.39.4 rather than the newest majors because `openapi-typescript` 7.13.0 still peers on TypeScript 5.x and the current Next ESLint plugin stack has not declared ESLint 10 support. These are the latest compatible no-peer-warning versions.
- `next` 16.2.6 bundles a vulnerable PostCSS version for audit purposes, so `web/package.json` overrides PostCSS to 8.5.14.

## Verification

- `go build ./cmd/ntm`
- `go test -short ./...`
- `go test -v ./...` with `E2E_NTM_BIN=/tmp/ntm-release-test`
- `go test ./...` in `third_party/bubbletea`
- `golangci-lint run`
- tracked Go files clean under `gofmt -l` and `goimports -l`
- `npm run lint`, `npm run test:run`, `npm run build`, and `npm audit --audit-level=moderate` in `web/`
- `npm run compile` and `npm audit --audit-level=moderate` in `vscode/`
- `ubs` over changed files rechecked after fixing the real unbounded dashboard fetch finding; remaining nonzero findings are false positives on ordinary equality checks and ignored-cache/tooling scan context.
