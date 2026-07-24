# Encryption At Rest - Key Management + Config Spec

## Overview
This document defines **how encryption at rest is enabled**, **where keys come from**, the
**rotation story**, and **failure modes** for NTM artifacts. It is intentionally explicit so
implementation can proceed without ambiguity.

Scope includes key management and configuration. Encryption primitives and file formats are
covered in the encryption library task (`bd-z7ses`).

## Goals
- Enable opt-in encryption for NTM artifacts (history, events, checkpoints, etc.).
- Provide a single, explicit configuration surface.
- Support key rotation without data loss.
- Fail loudly on missing/invalid keys (no silent ephemeral keys).

## Non-Goals
- Key escrow or cloud KMS integration.
- Automatic key generation for persistent data.

## Supported Artifacts (Initial)
- Prompt history
- Event logs
- Checkpoint exports
- Support bundles (if encrypted output requested)

## Configuration
Add an `[encryption]` section to `config.toml` and `.ntm/config.toml`.

```toml
[encryption]
# Top-level toggle for encryption-at-rest (default false).
enabled = false

# Key source: env | file | command
key_source = "env"

# For key_source = "env"
key_env = "NTM_ENCRYPTION_KEY"

# For key_source = "file"
key_file = "~/.config/ntm/keys/ntm.key"

# For key_source = "command"
# The command must print the raw key bytes encoded as hex or base64.
key_command = "security find-generic-password -a $USER -s ntm-key -w"

# Key encoding for env/file/command output: hex | base64
key_format = "hex"

# Active key id for new writes (required when multiple keys are present).
active_key_id = "k1"

# Optional keyring for rotation (id -> key material).
# Each entry is encoded using key_format.
# If provided, decryption tries all keys in the keyring.
[encryption.keyring]
# k1 = "hex:..."
# k2 = "hex:..."
```

### Key Requirements
- AES-256-GCM requires **32 bytes** (256-bit key).
- Encodings are interpreted as:
  - `hex`: 64 hex chars -> 32 bytes
  - `base64`: decodes to 32 bytes
- For `key_source=command`, stdout is trimmed; stderr is ignored.

### Keyring Semantics
- If `keyring` is provided, `active_key_id` is **required** and **must** name an
  entry in it. Config validation rejects anything else: without an active entry,
  writes would use the `key_source` key while reads only tried keyring entries,
  leaving every new artifact undecryptable.
- New encryptions use `active_key_id` only.
- Decryption attempts the active key first (it wrote the newest artifacts), then
  the remaining entries by ascending key ID. TOML decodes the keyring into a map,
  so declaration order is not recoverable; sorting keeps the order deterministic
  across runs.
- If no keyring is provided, a single key from `key_source` is used for both
  encryption and decryption.

## Rotation Story
1. Add a new key (`k2`) to the keyring.
2. Set `active_key_id = "k2"`.
3. Existing data remains decryptable with older keys in the keyring.
4. Optionally re-encrypt older artifacts during maintenance.

## Failure Modes (Explicit)
- **Missing key**: return an error with a clear remediation hint
  (e.g. set `NTM_ENCRYPTION_KEY` or `key_file`).
- **Invalid format/length**: error describing expected length and encoding.
- **Decryption failure**: error indicating key mismatch or corrupted data.
- **Keyring missing active key**: error, refuse to start encryption.

Every one of these is fatal at startup. When `[encryption] enabled = true` and the
key cannot be resolved, NTM exits non-zero rather than continuing with encryption
disabled — falling back to plaintext would silently defeat the setting the
operator turned on. Machine invocations receive a single `INTERNAL_ERROR` JSON
document; humans receive the error plus a remediation hint on stderr. To persist
artifacts unencrypted, set `enabled = false` explicitly.

No automatic or ephemeral keys are generated for persistent data.

## Integration Points
- Config validation should fail fast on invalid encryption config.
- Logging must never print raw key material.
- A new `ntm doctor` check can surface encryption enabled/disabled status.

## CLI Flags (Optional Future)
- `--encrypt` or `--encryption` to override config for a single command.
- `--allow-plaintext` to explicitly skip encryption when config is enabled.

## Security Notes
- Key files should have `0600` permissions.
- Commands used for `key_source=command` should avoid echoing secrets in logs.
- Do not persist encryption keys in NTM artifacts.
