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
