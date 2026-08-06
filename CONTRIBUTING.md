# Contributing to NTM

## About Contributions

Please don't take this the wrong way, but I do not accept outside contributions for any of my projects. I simply don't have the mental bandwidth to review anything, and it's my name on the thing, so I'm responsible for any problems it causes; thus, the risk-reward is highly asymmetric from my perspective. I'd also have to worry about other "stakeholders," which seems unwise for tools I mostly make for myself for free. Feel free to submit issues, and even PRs if you want to illustrate a proposed fix, but know I won't merge them directly. Instead, I'll have Claude or Codex review submissions via `gh` and independently decide whether and how to address them. Bug reports in particular are welcome. Sorry if this offends, but I want to avoid wasted time and hurt feelings. I understand this isn't in sync with the prevailing open-source ethos that seeks community contributions, but it's the only way I can move at this velocity and keep my sanity.

---

The rest of this document is a **developer reference** for anyone building NTM locally, running tests, or filing issues with reproduction steps.

## Development Setup

### Prerequisites

- Go 1.26.3+
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

### Shared-Worktree Commit Provenance

When multiple agents share one worktree, the generated pre-commit hook runs
`br sync --flush-only` and stages every tracked `.beads` JSONL file. That can
silently add another agent's Beads state after an explicit code pathspec was
chosen. For a code-only commit, inspect the staged diff, then commit only
owned paths with a message file and `--no-verify`; an agent that owns the
Beads update must serialize its separate sync/commit. Do not rewrite history
to correct an attribution mistake: add a forward changelog correction that
names the actual implementation commit.

---

## Release Infrastructure

### Upgrade Command & Asset Naming

The `ntm upgrade` command downloads release assets from GitHub Releases. Releases
are built and published exclusively with DSR; do not dispatch GitHub Actions or
fall back to manual `gh release create`. `internal/cli/upgrade.go` must know the
asset names produced by DSR.

This creates a **naming contract** among:
- **DSR target/artifact configuration**: Defines the published native-platform assets
- **`.goreleaser.yaml`**: Retains the legacy naming reference used by compatibility tests
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
| macOS (DSR) | Uses native `arm64` or `amd64` | DSR builds each architecture directly |
| macOS (GoReleaser) | Uses `all` instead of arch | Universal binary (arm64+amd64) |
| Windows | Uses `.zip` instead of `.tar.gz` | Native Windows archive format |
| ARM Linux | Uses `armv7` suffix | Distinguish from arm64 |

**Platform Examples**:

| Platform | Archive Name | Binary Pattern |
|----------|-------------|----------------|
| macOS ARM (DSR) | `ntm_1.20.0_darwin_arm64.tar.gz` | `ntm_darwin_arm64` |
| macOS Intel (DSR) | `ntm_1.20.0_darwin_amd64.tar.gz` | `ntm_darwin_amd64` |
| macOS universal (GoReleaser) | `ntm_1.20.0_darwin_all.tar.gz` | `ntm_darwin_all` |
| Linux x64 | `ntm_1.20.0_linux_amd64.tar.gz` | `ntm_linux_amd64` |
| Linux ARM64 | `ntm_1.20.0_linux_arm64.tar.gz` | `ntm_linux_arm64` |
| Linux ARM (32-bit) | `ntm_1.20.0_linux_armv7.tar.gz` | `ntm_linux_armv7` |
| Windows | `ntm_1.20.0_windows_amd64.zip` | `ntm_windows_amd64` |
| FreeBSD x64 | `ntm_1.20.0_freebsd_amd64.tar.gz` | `ntm_freebsd_amd64` |

**Note**: The "Binary Pattern" column shows the asset name prefix used by `upgrade.go` to find assets. The upgrader and installer prefer native DSR macOS assets and retain exact `darwin_all` support for GoReleaser releases. The actual binary inside archives is always named `ntm` (or `ntm.exe` on Windows).

### Making Changes Safely

Before making **ANY** changes to asset naming:

1. **Understand the contract**:
   - Read this document fully
   - Review `TestUpgradeAssetNamingContract` in `internal/cli/cli_test.go`

2. **Update every contract surface together**:
   - [ ] DSR target/artifact configuration: Update the authoritative release names
   - [ ] `.goreleaser.yaml`: Keep the legacy compatibility names aligned where applicable
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

4. **Verify through DSR before publication**:
   - Run the repository's DSR quality matrix, including the upgrade asset contract.
   - Build every configured release target into a fresh retained artifact directory.

5. **Verify through DSR after publication**:
   - `dsr release verify ntm X.Y.Z --verify-checksums`
   - `dsr verify upgrade ntm --build-from-source`

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

2. **Release naming config changed**:
   - Check the DSR target/artifact configuration and legacy `.goreleaser.yaml` reference.
   - Verify every emitted archive name matches `upgrade.go` logic.

3. **New platform not supported**:
   - Add platform to `getAssetName()` / `getArchiveAssetName()`
   - Add test case to `TestUpgradeAssetNamingContract`

**Error: DSR upgrade verification failing**

The current code can't find assets from the latest release. Either:
- Roll back the code change, or
- Cut a new release with compatible naming

### Protection Layers

The upgrade system has multiple protection layers:

1. **Contract Tests** (`TestUpgradeAssetNamingContract`): Catch naming drift at development time
2. **DSR Quality Check**: Test the upgrade contract before publication
3. **DSR Post-Release Verification**: Verify checksums and source-build upgrade behavior
4. **Enhanced Error Messages**: Guide users to diagnose issues themselves

### Configuration Contract

Configuration loading rejects unknown fields. `[resilience]` is the canonical
restart and monitoring section; the former unused `[health]` section is rejected.
`coordinator enable|disable` persists the selected config file and preserves
unrelated content. Project `[assign] operator_gated_labels` extend global and
built-in approval gates; they cannot remove them.

---

## Code Style

- Follow standard Go conventions
- Run `gofmt` before committing
- Write tests for new functionality
- Keep functions focused and small

## Questions?

Open an issue on GitHub for questions or discussion.
