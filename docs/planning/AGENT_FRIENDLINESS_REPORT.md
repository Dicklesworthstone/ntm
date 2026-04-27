# Agent-Friendliness Report: Named Tmux Manager (ntm)

**Bead ID**: bd-3en
**Date**: 2026-01-25
**Agent**: Claude Opus 4.5

## Executive Summary

**Status: EXCELLENT AGENT-FRIENDLINESS MATURITY**

NTM is exceptionally well-optimized for AI coding agent usage:
- TOON output fully integrated via `--robot-format=toon`
- 100+ `--robot-*` flags for machine-readable output
- Comprehensive AGENTS.md documentation (30KB)
- Unified renderer architecture with pluggable formats

## 1. Current State Assessment

### 1.1 Robot Mode Support

| Feature | Status | Details |
|---------|--------|---------|
| `--robot-*` flags | YES | 100+ robot flags for different operations |
| `--robot-format` flag | YES | json, toon, auto |
| `--robot-markdown` flag | YES | Markdown table output |
| `--robot-terse` flag | YES | Minimal single-line output |
| `--robot-verbosity` flag | YES | terse, default, debug profiles |
| `NTM_ROBOT_FORMAT` env | YES | Default robot format |
| `NTM_OUTPUT_FORMAT` env | YES | Output format control |
| `TOON_DEFAULT_FORMAT` env | YES | TOON fallback default |

### 1.2 Output Formats

| Format | Description |
|--------|-------------|
| `json` | Pretty-printed JSON (default) |
| `toon` | Token-efficient via tru binary |
| `auto` | Auto-detect based on context |
| `markdown` | Markdown tables (~50% token savings) |
| `terse` | Single-line minimal output |

### 1.3 Robot Command Categories

| Category | Example Flags |
|----------|---------------|
| Session Management | `--robot-status`, `--robot-spawn`, `--robot-health` |
| Agent Operations | `--robot-send`, `--robot-tail`, `--robot-errors`, `--robot-is-working` |
| Beads Integration | `--robot-bead-create`, `--robot-bead-claim`, `--robot-triage` |
| CASS Integration | `--robot-cass-search`, `--robot-cass-status`, `--robot-cass-context` |
| Monitoring | `--robot-monitor`, `--robot-diagnose`, `--robot-agent-health` |

### 1.4 Unified Renderer Architecture

```go
// internal/robot/renderer.go
type RobotFormat string

const (
    FormatJSON RobotFormat = "json"   // Default
    FormatTOON RobotFormat = "toon"   // Token-efficient
    FormatAuto RobotFormat = "auto"   // Auto-detect
)

// Single entry point
func Render(payload any, format RobotFormat) (string, error)
func Output(payload any, format RobotFormat) error
```

### 1.5 TOON Encoder

- Delegates to **toon_rust's `tru` binary**
- Binary discovery: `tru` in PATH, or `TOON_BIN`/`TOON_TRU_BIN` env
- Falls back to JSON for unsupported nested structures
- Content-type: `text/x-toon` vs `application/json`

## 2. Documentation Assessment

### 2.1 AGENTS.md

**Status**: EXISTS and comprehensive (30KB)

Contains:
- Rule 0: Fundamental override prerogative
- Rule 1: Absolute file deletion protection
- Go toolchain guidelines
- Code editing discipline
- Backwards compatibility rules
- Logging standards

### 2.2 Additional Documentation

- README.md: 132KB comprehensive guide
- RESEARCH_FINDINGS.md: 5.1KB TOON integration research
- TOON_INTEGRATION_BRIEF.md: 4.4KB integration plan
- SKILL.md: 15KB skill specification
- command_palette.md: 13KB palette documentation

## 3. Scorecard

| Dimension | Score (1-5) | Notes |
|-----------|-------------|-------|
| Documentation | 5 | Comprehensive AGENTS.md + detailed docs |
| CLI Ergonomics | 5 | Excellent 60+ subcommand structure |
| Robot Mode | 5 | 100+ robot flags, format control |
| Error Handling | 5 | Structured JSON errors |
| Consistency | 5 | Unified renderer, consistent patterns |
| Zero-shot Usability | 5 | Excellent --help, examples |
| **Overall** | **5.0** | Exceptional maturity |

## 4. TOON Integration Status

**Status: FULLY INTEGRATED**

From RESEARCH_FINDINGS.md:
- `FormatTOON` constant defined
- `TOONRenderer` struct implementing `Renderer` interface
- `--robot-format=toon` CLI flag working
- Environment variable support present
- Content-type hints available
- Fallback to JSON for complex structures

Test verification:
```bash
# Test TOON output
ntm --robot-status --robot-format=toon

# Test JSON output
ntm --robot-status --robot-format=json
```

## 5. Recommendations

### 5.1 High Priority (P1)

None - ntm is already exceptionally agent-friendly

### 5.2 Medium Priority (P2)

None - comprehensive coverage

### 5.3 Low Priority (P3)

1. Add `--robot-schema` flag for JSON Schema emission
2. Document token savings metrics

## 6. JSON Output Structure

The `--robot-status` output shows excellent structure:
```json
{
  "success": true,
  "timestamp": "2026-01-25T...",
  "system": {
    "version": "...",
    "tmux_available": true
  },
  "sessions": [...]
}
```

## 7. Conclusion

NTM is the most agent-friendly tool in the suite with:
- Full TOON integration via unified renderer
- 100+ robot flags for comprehensive automation
- Excellent documentation (30KB AGENTS.md)
- Multiple output formats (json, toon, markdown, terse)

Score: **5.0/5** - Exceptional maturity, gold standard for agent-friendliness.

---
*Generated by Claude Opus 4.5 during bd-3en execution*
