# Contributing to NTM

Thank you for your interest in contributing to NTM! This document provides guidelines and information for contributors.

## Development Setup

### Prerequisites

- Go 1.25+
- tmux (for testing)
- golangci-lint (for linting)

### Building

```bash
go build ./cmd/ntm
```

### Testing

```bash
go test ./...
```

### Linting

```bash
golangci-lint run
```

---

## Release Infrastructure

### Upgrade Command & Asset Naming

The `ntm upgrade` command downloads release assets from GitHub Releases. For this to work, `internal/cli/upgrade.go` must know exactly what asset names GoReleaser produces.

This creates a **naming contract** between two files:
- **`.goreleaser.yaml`**: Defines asset names via `archives.name_template`
- **`internal/cli/upgrade.go`**: Contains logic to find and match assets

If these drift apart, users get "no suitable release asset found" errors. The contract is enforced by `TestUpgradeAssetNamingContract` in `internal/cli/cli_test.go`.

### Current Naming Convention

**Archives (tar.gz/zip)**:
```
ntm_{version}_{os}_{arch}.{ext}
```

**Raw Binaries**:
```
ntm_{os}_{arch}
```

**Special Cases**:

| Case | Convention | Reason |
|------|-----------|--------|
| macOS | Uses `all` instead of arch | Universal binary (arm64+amd64) |
| Windows | Uses `.zip` instead of `.tar.gz` | Native Windows archive format |
| ARM Linux | Uses `armv7` suffix | Distinguish from arm64 |

**Platform Examples**:

| Platform | Archive Name | Binary Pattern |
|----------|-------------|----------------|
| macOS ARM | `ntm_1.4.1_darwin_all.tar.gz` | `ntm_darwin_all` |
| macOS Intel | `ntm_1.4.1_darwin_all.tar.gz` | `ntm_darwin_all` |
| Linux x64 | `ntm_1.4.1_linux_amd64.tar.gz` | `ntm_linux_amd64` |
| Linux ARM64 | `ntm_1.4.1_linux_arm64.tar.gz` | `ntm_linux_arm64` |
| Linux ARM (32-bit) | `ntm_1.4.1_linux_armv7.tar.gz` | `ntm_linux_armv7` |
| Windows | `ntm_1.4.1_windows_amd64.zip` | `ntm_windows_amd64` |

**Note**: The "Binary Pattern" column shows the asset name prefix used by `upgrade.go` to find assets. The actual binary inside archives is always named `ntm` (or `ntm.exe` on Windows).

### Making Changes Safely

Before making **ANY** changes to asset naming:

1. **Understand the contract**:
   - Read this document fully
   - Review `TestUpgradeAssetNamingContract` in `internal/cli/cli_test.go`

2. **Update both files together**:
   - [ ] `.goreleaser.yaml`: Update `archives.name_template`
   - [ ] `internal/cli/upgrade.go`: Update `getAssetName()` and `getArchiveAssetName()`
   - [ ] `internal/cli/cli_test.go`: Update `TestUpgradeAssetNamingContract` expected values

3. **Verify locally**:
   ```bash
   go test -v -run TestUpgradeAsset ./internal/cli/
   ```

   Optional helpers:
   ```bash
   make upgrade-contract
   make pre-commit
   ```
   `make pre-commit` only runs the contract tests when relevant files are staged.

4. **CI will validate**:
   - `upgrade-check` job tests against latest release
   - If naming changed, expect CI to fail until new release with new naming

5. **After release**:
   - `upgrade-verify` job confirms all platforms can find assets

### Troubleshooting Upgrade Failures

**Error: "no suitable release asset found for X/Y"**

This means `upgrade.go` couldn't find a matching asset. The error now shows:
- A diagnostic box with platform and tried names
- Available assets with platform annotations
- Troubleshooting hints and links

Common causes:

1. **Naming convention mismatch**:
   - Check actual names at https://github.com/Dicklesworthstone/ntm/releases/latest
   - Compare against `TestUpgradeAssetNamingContract` expectations

2. **GoReleaser config changed**:
   - Check recent changes to `.goreleaser.yaml`
   - Verify `archives.name_template` matches `upgrade.go` logic

3. **New platform not supported**:
   - Add platform to `getAssetName()` / `getArchiveAssetName()`
   - Add test case to `TestUpgradeAssetNamingContract`

**Error: CI `upgrade-check` failing**

The current code can't find assets from the latest release. Either:
- Roll back the code change, or
- Cut a new release with compatible naming

### Protection Layers

The upgrade system has multiple protection layers:

1. **Contract Tests** (`TestUpgradeAssetNamingContract`): Catch naming drift at development time
2. **CI Upgrade Check**: Test against real releases before merge
3. **Post-Release Verification**: Verify all platforms after release
4. **Enhanced Error Messages**: Guide users to diagnose issues themselves

---

## Code Style

- Follow standard Go conventions
- Run `gofmt` before committing
- Write tests for new functionality
- Keep functions focused and small

## Pull Request Process

1. Fork the repository
2. Create a feature branch
3. Make your changes with tests
4. Ensure CI passes
5. Submit a pull request with a clear description

## Questions?

Open an issue on GitHub for questions or discussion.
