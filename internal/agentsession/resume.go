package agentsession

import (
	"os/exec"
	"strings"
)

// antigravityModel is the model the Antigravity CLI (agy) must be pinned to on
// every (re)launch. The agy resume path is invalid without an explicit --model,
// and the migration mandate fixes it to this exact human-readable name.
const antigravityModel = "Gemini 3.8 Flash (High)"

// ResumeCommand builds the shell command that resumes a captured agent session
// inside its pane. Per the ntm design principle, ntm does NOT reimplement
// provider-specific resume: it delegates to casr (Cross Agent Session Resumer)
// when available, and falls back to the agent's native `--resume <id>` flag.
//
//	provider   casr/native provider name ("claude", "codex", "gemini",
//	           "antigravity")
//	sessionID  the captured provider session id
//	preferCASR when true (and casr is on PATH), use casr; otherwise native.
//
// "gemini" (the retired Gemini CLI) and "antigravity" (its successor, agy) are
// distinct providers with distinct resume commands and must not be conflated.
//
// Returns "" if no resume command can be constructed (unknown provider or empty
// id). The returned string is a single command line suitable for sending to a
// tmux pane via SendKeysForAgent.
func ResumeCommand(provider, sessionID string, preferCASR bool) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	sessionID = strings.TrimSpace(sessionID)
	if provider == "" || sessionID == "" {
		return ""
	}

	if preferCASR && casrAvailable() {
		// casr auto-detects the source provider from the id and writes a
		// native session for the target, then prints/launches the resume.
		// The short flag form is the documented ergonomic path.
		switch provider {
		case "claude":
			return "casr -cc " + shellQuote(sessionID)
		case "codex":
			return "casr -cod " + shellQuote(sessionID)
		case "gemini":
			return "casr -gmi " + shellQuote(sessionID)
		}
		// Antigravity has no casr short-flag; fall through to its native
		// resume command below.
	}

	// Native fallback: each agent CLI accepts a resume-by-id flag.
	switch provider {
	case "claude":
		return "claude --resume " + shellQuote(sessionID)
	case "codex":
		return "codex resume " + shellQuote(sessionID)
	case "gemini":
		return "gemini --resume " + shellQuote(sessionID)
	case "antigravity":
		// agy resumes a conversation by id and REQUIRES the model pinned.
		return "agy --conversation " + shellQuote(sessionID) +
			" --model " + shellQuote(antigravityModel)
	}
	return ""
}

// casrAvailable reports whether the casr binary is on PATH. Overridable in
// tests via the lookPath indirection.
func casrAvailable() bool {
	_, err := lookPath("casr")
	return err == nil
}

var lookPath = exec.LookPath

// shellQuote single-quotes a value for safe inclusion in a shell command,
// escaping embedded single quotes. Session ids are normally UUID-like, but we
// quote defensively.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
