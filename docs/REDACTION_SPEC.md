# Redaction Specification & Pattern Catalog

## Overview

This document defines the canonical set of detection categories, patterns, and redaction strategies for NTM's secrets/PII protection engine.

**Implementation:** `internal/redaction` (canonical). `internal/safety/redaction` is a compatibility wrapper.

## Design Goals

1. **Minimal false positives** - Patterns should be precise enough to avoid flagging legitimate content
2. **Fast detection** - No catastrophic regex backtracking; patterns must complete in O(n) time
3. **Non-reversible redaction** - Placeholders must not leak original content or its length
4. **Category-aware** - Each finding includes its category for UX and reporting
5. **Configurable** - Users can allowlist specific patterns or disable categories

---

## Prior Art: Existing Checkpoint Export Patterns (Regression Set)

NTM already ships a small set of secret redaction patterns used by checkpoint export
(`internal/checkpoint/export.go`, `--redact-secrets`). The unified engine MUST cover these
patterns (or stricter supersets) to avoid regressions when the checkpoint exporter migrates.

| Source Regex (current) | Canonical Category |
|------------------------|-------------------|
| `(?i)(api[_-]?key|apikey)\\s*[:=]\\s*['\"]?[\\w-]{20,}['\"]?` | `GENERIC_API_KEY` |
| `(?i)(secret|password|passwd|pwd)\\s*[:=]\\s*['\"]?[^\\s'\"]{8,}['\"]?` | `PASSWORD` |
| `(?i)(token|bearer)\\s*[:=]\\s*['\"]?[\\w-]{20,}['\"]?` | `BEARER_TOKEN` |
| `(?i)Authorization:\\s*Bearer\\s+[\\w-]+` | `BEARER_TOKEN` |
| `(?i)(aws_secret|aws_access)\\s*[:=]\\s*['\"]?[\\w/+=]{20,}['\"]?` | `AWS_SECRET_KEY` |
| `ghp_[a-zA-Z0-9]{36}` | `GITHUB_TOKEN` |
| `sk-[a-zA-Z0-9]{48}` | `OPENAI_KEY` (legacy) |
| `sk-ant-[a-zA-Z0-9-]{95}` | `ANTHROPIC_KEY` |
| `AKIA[A-Z0-9]{16}` | `AWS_ACCESS_KEY` |
| `-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----` | `PRIVATE_KEY` |

Notes:
- The current checkpoint exporter replaces matches with a generic `[REDACTED]` string.
- The unified engine will emit category-aware placeholders (see below) and structured findings.

---

## Detection Categories

### 1. Provider API Keys

#### OpenAI
```
Pattern: sk-[a-zA-Z0-9]{48}                     # legacy (shipped in checkpoint export)
         sk-[a-zA-Z0-9]{10,}T3BlbkFJ[a-zA-Z0-9]{10,}
         sk-proj-[a-zA-Z0-9_-]{40,}
Category: OPENAI_KEY
Examples:
  - <<OPENAI_LEGACY_KEY>>
  - <<OPENAI_TEST_KEY>>
  - <<OPENAI_PROJ_KEY>>
```

#### Anthropic
```
Pattern: sk-ant-[a-zA-Z0-9_-]{40,}              # covers legacy {95} pattern
Category: ANTHROPIC_KEY
Examples:
  - <<ANTHROPIC_KEY>>
```

#### Google/Gemini
```
Pattern: AIza[a-zA-Z0-9_-]{35}
Category: GOOGLE_API_KEY
Examples:
  - <<GOOGLE_API_KEY>>
```

#### GitHub
```
Pattern: gh[pousr]_[a-zA-Z0-9]{30,}
         github_pat_[a-zA-Z0-9]{20,}_[a-zA-Z0-9]{40,}
Category: GITHUB_TOKEN
Examples:
  - <<GITHUB_TOKEN>>
  - <<GITHUB_FINE_PAT>>
```

### 2. Cloud Provider Credentials

#### AWS Access Keys
```
Pattern: AKIA[0-9A-Z]{16}
         ASIA[0-9A-Z]{16}
Category: AWS_ACCESS_KEY
Examples:
  - <<AWS_ACCESS_KEY>>
```

#### AWS Secret Keys (heuristic)
```
Pattern: (?i)(aws_secret|secret_access_key|secret_key)\s*[=:]\s*["']?[a-zA-Z0-9/+=]{40}["']?
Category: AWS_SECRET_KEY
Examples:
  - aws_secret_access_key = "<<AWS_SECRET_KEY>>"
```

#### Azure
```
Pattern: (?i)(client_secret|azure_secret)\s*[=:]\s*["']?[a-zA-Z0-9~.+/=_-]{30,}["']?
Category: AZURE_SECRET
```

#### GCP Service Account Key
```
Pattern: "private_key":\s*"-----BEGIN (RSA )?PRIVATE KEY-----
Category: GCP_SERVICE_KEY
```

### 3. Authentication Tokens

#### JWT (JSON Web Tokens)
```
Pattern: eyJ[a-zA-Z0-9_-]*\.eyJ[a-zA-Z0-9_-]*\.[a-zA-Z0-9_-]+
Category: JWT
Examples:
  - <<JWT>>
```

#### OAuth Bearer Tokens
```
Pattern: (?i)bearer\s+[a-zA-Z0-9._-]{20,}
Category: BEARER_TOKEN
```

### 4. Generic Secrets

#### API Keys (generic pattern)
```
Pattern: (?i)([a-z_]*api[_]?key)\s*[=:]\s*["']?[a-zA-Z0-9_-]{16,}["']?
Category: GENERIC_API_KEY
Examples:
  - API_KEY=abc123xyz789
  - my_api_key: "secret_value_here"
```

#### Passwords
```
Pattern: (?i)(password|passwd|pwd)\s*[=:]\s*["']?[^\s"']{8,}["']?
Category: PASSWORD
Examples:
  - password=mySecretPass123
  - PASSWORD: "hunter2"
```

#### Generic Secrets
```
Pattern: (?i)(secret|private[_]?key|token)\s*[=:]\s*["']?[a-zA-Z0-9/+=_-]{16,}["']?
Category: GENERIC_SECRET
```

### 5. Private Keys

#### RSA/DSA/EC Private Keys
```
Pattern: -----BEGIN\s+(RSA\s+|DSA\s+|EC\s+|OPENSSH\s+)?PRIVATE KEY-----
Category: PRIVATE_KEY
```

#### SSH Private Keys
```
Pattern: -----BEGIN\\s+OPENSSH\\s+PRIVATE\\s+KEY-----
Category: SSH_PRIVATE_KEY
```

### 6. Database Credentials

#### Connection Strings
```
Pattern: (?i)(postgres|mysql|mongodb|redis)://[^:]+:[^@]+@[^\s]+
Category: DATABASE_URL
Examples:
  - postgres://user:password@localhost/db
  - mongodb://admin:secret@mongo.example.com
```

---

## Redaction Placeholder Strategy

### Format
```
[REDACTED:<CATEGORY>:<hash8>]
```

Where:
- `CATEGORY` - Detection category name (e.g., `OPENAI_KEY`, `JWT`)
- `hash8` - First 8 characters of `sha256(category + ":" + matched_content)` in hex

### Examples
```
Original: sk-abc123...T3BlbkFJ...xyz789
Redacted: [REDACTED:OPENAI_KEY:a1b2c3d4]

Original: eyJhbGciOiJIUzI1NiIs...
Redacted: [REDACTED:JWT:5e6f7a8b]
```

### Properties
- **Non-reversible**: SHA-256 hash prevents recovery of original content
- **Length-invariant**: Fixed 8-char hash doesn't leak original length
- **Deterministic**: Same input always produces same placeholder (for caching/dedup)
- **Category-aware**: Helps users understand what was redacted

---

## Modes of Operation

### `off`
- No scanning or redaction
- All content passes through unchanged

### `warn`
- Scan for sensitive content
- Log warnings but allow operation to proceed
- Output includes finding details without redacting

### `redact`
- Scan and replace sensitive content with placeholders
- Operation proceeds with redacted content
- Findings logged for audit

### `block`
- Scan for sensitive content
- If any findings, abort operation with error
- Command exits with non-zero status

---

## UX & Error Messaging

### Warning Message Format
```
WARNING: Sensitive content detected

Category: OPENAI_KEY
Location: prompt (line 3, col 12-65)
Content:  sk-abc1...xyz9 (truncated)

To proceed anyway:
  --allow-secret              Override for this command
  ntm config redaction.mode=off   Disable globally
```

### Block Error Format
```
ERROR: Operation blocked due to sensitive content

Category: OPENAI_KEY
Location: prompt
Error Code: REDACTION_BLOCKED

Resolution options:
1. Remove the sensitive content manually
2. Add to allowlist: ntm config redaction.allowlist.add "pattern"
3. Override for this command: --allow-secret
```

### Robot Mode Error Response
```json
{
  "success": false,
  "error_code": "REDACTION_BLOCKED",
  "error": "Sensitive content detected: OPENAI_KEY",
  "findings": [
    {
      "category": "OPENAI_KEY",
      "location": "prompt",
      "line": 3,
      "column": 12
    }
  ],
  "hint": "Use --allow-secret to override"
}
```

---

## Allowlist Configuration

### Per-Pattern Allowlist
```toml
[redaction]
mode = "redact"
allowlist = [
  "sk-test-.*",           # Allow test keys
  "EXAMPLE.*KEY",         # Allow example keys in docs
]
```

### Environment Variable Override
```bash
NTM_REDACTION_ALLOWLIST="sk-test-.*,EXAMPLE.*" ntm send ...
```

---

## Test Fixtures

Some fixtures use placeholders to avoid committing secret-looking strings
(provider keys, cloud creds, and private keys). Unit tests expand these placeholders into synthetic values.
See `internal/safety/redaction/redaction_test.go` for the exact expansion logic.
### True Positives (should detect)

```
# NOTE: These are synthetic fixtures, not real credentials.

# Category: OPENAI_KEY
<<OPENAI_LEGACY_KEY>>
<<OPENAI_TEST_KEY>>
<<OPENAI_PROJ_KEY>>
OPENAI_API_KEY=<<OPENAI_LEGACY_KEY>>

# Category: ANTHROPIC_KEY
<<ANTHROPIC_KEY>>

# Category: GOOGLE_API_KEY
<<GOOGLE_API_KEY>>

# Category: GITHUB_TOKEN
<<GITHUB_TOKEN>>
<<GITHUB_FINE_PAT>>

# Category: AWS_ACCESS_KEY
<<AWS_ACCESS_KEY>>

# Category: AWS_SECRET_KEY
aws_secret_access_key="<<AWS_SECRET_KEY>>"
aws_access="aaaaaaaaaaaaaaaaaaaa/+/=AAAAAAAAAAAAAAAAAAAA"

# Category: JWT
<<JWT>>

# Category: BEARER_TOKEN
Authorization: Bearer abcdefghijklmnopqrstuvwxyzABCDE
token="abcdef1234567890_abcdef1234567890"

# Category: DATABASE_URL
postgres://myuser:mypassword@localhost:5432/mydb
mongodb://admin:secretPassword123@mongo.example.com:27017/production

# Category: PRIVATE_KEY
<<RSA_PRIVATE_KEY>>

# Category: PASSWORD
password=SuperSecretP@ssw0rd!
DATABASE_PASSWORD="hunter2"

# Category: GENERIC_API_KEY
MY_API_KEY=abcdef123456789abcdef
stripe_api_key="<<STRIPE_LIVE_KEY>>"
```

### True Negatives (should NOT detect)

```
# Not an API key - too short
sk-abc

# Not a JWT - wrong format
eyJhbGciOiJIUzI1NiJ9.notvalid

# Not a password - just the word
The password field is required

# Example/documentation strings
YOUR_API_KEY_HERE
<your-openai-key>
REPLACE_WITH_YOUR_KEY

# URL without credentials
postgres://localhost:5432/mydb

# Base64 that's not a key
data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==

# Partial matches that shouldn't trigger
Asking about API_KEY best practices
The pattern sk-* is for OpenAI
```

### Edge Cases

```
# Multiline private key (should detect full block)
<<RSA_PRIVATE_KEY>>

# Key in JSON (should detect)
{"api_key":"<<OPENAI_LEGACY_KEY>>"}

# Key in environment export (should detect)
export OPENAI_API_KEY=<<OPENAI_LEGACY_KEY>>

# Multiple keys in one line (should detect all)
OPENAI_KEY=<<OPENAI_LEGACY_KEY>> ANTHROPIC_KEY=<<ANTHROPIC_KEY>>

# Key with surrounding whitespace (should detect)
   <<OPENAI_LEGACY_KEY>>

# Key in code-like text (should detect)
export API_KEY=<<OPENAI_LEGACY_KEY>>
```

---

## Implementation Notes

### Regex Performance

NTM is written in Go; the standard `regexp` engine is RE2 (no backtracking).
Even so, patterns should remain specific enough to avoid broad false positives and
should be fast on large inputs (target: scan 1MB in <100ms on a typical dev machine).

### Pattern Compilation

Patterns should be compiled once at startup and reused:
```go
var compiledPatterns = map[string]*regexp.Regexp{
    "OPENAI_KEY": regexp.MustCompile(`sk-[a-zA-Z0-9]{10,}T3BlbkFJ[a-zA-Z0-9]{10,}|sk-proj-[a-zA-Z0-9_-]{40,}|sk-[a-zA-Z0-9]{48}`),
    // ...
}
```

### Category Priority

When a string matches multiple patterns, report the most specific:
1. Provider-specific keys (OPENAI_KEY, ANTHROPIC_KEY) over GENERIC_API_KEY
2. AWS_SECRET_KEY over GENERIC_SECRET
3. SSH_PRIVATE_KEY over PRIVATE_KEY

---

## Changelog

- 2026-02-01: Initial specification created (bd-5dfye)
