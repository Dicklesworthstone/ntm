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

## Supported Artifacts
- Prompt history (`ENC:v1:` line-level AES-256-GCM)
- Event logs (`ENC:v1:` line-level AES-256-GCM)
- Checkpoint metadata (`metadata.json`), session state (`session.json`), pane scrollback captures, git patches/status (`git.patch`, `git_status.txt`), and exported `.tar.gz` / `.zip` archives (`NTMENC1\n` binary AEAD envelope, integrated with `ArtifactIntegrity` digest verification and `key_source = "keyring"` OS keyring resolution)
- Support bundles (`NTMENC1\n` AES-256-GCM envelope around the complete ZIP or tar.gz archive; atomic `0600` output and in-memory verification; CLI writes `.zip.enc`)

## Configuration
Add an `[encryption]` section to `config.toml` and `.ntm/config.toml`.

```toml
[encryption]
# Top-level toggle for encryption-at-rest (default false).
enabled = false

# Key source: env | file | command | keyring
key_source = "env"

# For key_source = "env" (also used as headless fallback when key_source = "keyring")
key_env = "NTM_ENCRYPTION_KEY"

# For key_source = "file" (also used as headless fallback when key_source = "keyring")
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

For commands that persist operational history or events, every one of these is fatal at startup. When `[encryption] enabled = true` and the
key cannot be resolved, NTM exits non-zero rather than continuing with encryption
disabled — falling back to plaintext would silently defeat the setting the
operator turned on. Machine invocations receive a single `INTERNAL_ERROR` JSON
document; humans receive the error plus a remediation hint on stderr. To persist
artifacts unencrypted, set `enabled = false` explicitly or pass `--allow-plaintext`
for that invocation. The diagnostic
`support-bundle` command resolves its own export key before collecting data and
applies the override only to its export. It preserves the configured encryption
requirement for history, events, checkpoints, and other bundle generators; those
writes refuse to proceed while their keys are unavailable. Supplying historical
decryption keys without an active write key still allows existing history and
events to be read, but never authorizes new plaintext entries.

No automatic or ephemeral keys are generated for persistent data.

## Integration Points
- Config validation should fail fast on invalid encryption config.
- Logging must never print raw key material.
- A new `ntm doctor` check can surface encryption enabled/disabled status.

## CLI Flags
- `--encrypt`: require encryption at rest for this invocation (overrides `[encryption] enabled = false`; fails closed if no key can be resolved).
- `--allow-plaintext`: explicitly skip encryption at rest for this invocation (overrides `[encryption] enabled = true`; mutually exclusive with `--encrypt`).

## Support Bundle Commands

```bash
# Follow [encryption] enabled; encrypted outputs receive .zip.enc.
ntm support-bundle -o diagnostics.zip

# Require encryption for this export even when the global toggle is off.
ntm support-bundle --encrypt -o diagnostics.zip.enc

# Intentionally export plaintext, including when the configured key is unavailable.
ntm support-bundle --allow-plaintext -o diagnostics.zip

# Authenticate the ciphertext and verify the archive manifest/checksums in memory.
ntm support-bundle --verify diagnostics.zip.enc

# Export a standard archive only after authentication and integrity verification.
ntm support-bundle --decrypt diagnostics.zip.enc -o diagnostics.zip
```

`--encrypt` and `--allow-plaintext` are mutually exclusive. Verification and
decryption require the configured key source or rotation keyring regardless of
the global encryption toggle; `--allow-plaintext` cannot bypass authentication.

`--robot-support-bundle` and `POST /api/v1/support-bundle` also honor the
operator-selected encryption configuration. All bundle surfaces validate the
selected global configuration and any project overlay strictly. They fail closed
when that configuration or its required key cannot be loaded; HTTP
does not offer a plaintext override. Encrypted HTTP downloads use a `.enc`
filename, `application/octet-stream`, and `encrypted: true` in the JSON envelope.

Generation builds the archive in memory, applies encryption before writing any
archive bytes to disk, and atomically publishes a private (`0600`) file. A failed
key lookup, encryption, size check, or write preserves an existing destination.
The size limit includes the encryption envelope. Encrypted verification is
bounded to 256 MiB of ciphertext and does not create plaintext temporary files.
The JSON generation result includes `encrypted`; verification reports whether
AEAD authentication was performed. Missing keys and failed verification return
non-zero exits and `success: false` in JSON mode.

Effective-config diagnostics always omit encryption keyring material and key
commands, including with `--no-redact`. Encryption does not disable redaction.

## Security Notes
- Key files should have `0600` permissions.
- Commands used for `key_source=command` should avoid echoing secrets in logs.
- Do not persist encryption keys in NTM artifacts.
