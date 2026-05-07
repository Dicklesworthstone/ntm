package pipeline

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// foreachAuthorModelFamily resolves the source model family for
// by_model_family_difference routing. Prefer explicit family fields when
// present, then fall back to author/model aliases.
func foreachAuthorModelFamily(item interface{}) string {
	return foreachAuthorModelFamilyForPanes(item, nil)
}

func foreachAuthorModelFamilyForPanes(item interface{}, panes []paneStrategyPane) string {
	raw := foreachItemStringNonEmpty(item, "model_family", "family", "type")
	if raw == "" {
		raw = foreachItemStringNonEmpty(item, "author_model", "model")
	}
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return ""
	}
	if mapped := mapModelFamilyToPaneVocabulary(normalized, panes); mapped != "" {
		return mapped
	}
	return normalized
}

func mapModelFamilyToPaneVocabulary(raw string, panes []paneStrategyPane) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return ""
	}

	for _, pane := range panes {
		paneFamily := strings.TrimSpace(pane.ModelFamily)
		if paneFamily == "" {
			continue
		}
		if strings.EqualFold(paneFamily, normalized) {
			return paneFamily
		}
	}

	rawGroup := modelFamilyGroup(normalized)
	if rawGroup == "" {
		return normalized
	}
	for _, pane := range panes {
		paneFamily := strings.TrimSpace(pane.ModelFamily)
		if paneFamily == "" {
			continue
		}
		if modelFamilyGroup(paneFamily) == rawGroup {
			return paneFamily
		}
	}
	return canonicalFamilyToken(rawGroup)
}

func modelFamilyGroup(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return ""
	}

	switch agent.AgentType(normalized).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "codex"
	case agent.AgentTypeGemini:
		return "gemini"
	}

	// Bare model variants used as pane.ModelFamily / item.author_model
	// (e.g. "opus", "sonnet", "haiku" for Claude; "pro", "flash", "ultra"
	// for Gemini) must group under their canonical family so the
	// cross-family adversarial contract treats them as same-family.
	// paneMetadataFromTmuxPane sets pane.ModelFamily from tag → variant →
	// type, so panes spawned without explicit model tags surface here as
	// the bare variant name.
	switch normalized {
	case "opus", "sonnet", "haiku":
		return "claude"
	case "pro", "flash", "ultra":
		return "gemini"
	}

	switch {
	case strings.HasPrefix(normalized, "claude"), strings.HasPrefix(normalized, "anthropic"),
		strings.HasPrefix(normalized, "opus"), strings.HasPrefix(normalized, "sonnet"),
		strings.HasPrefix(normalized, "haiku"):
		return "claude"
	case strings.HasPrefix(normalized, "codex"), strings.HasPrefix(normalized, "openai"), strings.HasPrefix(normalized, "gpt"):
		return "codex"
	case strings.HasPrefix(normalized, "gemini"), strings.HasPrefix(normalized, "google"),
		strings.HasPrefix(normalized, "flash"), strings.HasPrefix(normalized, "ultra"):
		return "gemini"
	default:
		return ""
	}
}

func canonicalFamilyToken(group string) string {
	switch group {
	case "claude":
		return "cc"
	case "codex":
		return "cod"
	case "gemini":
		return "gmi"
	default:
		return ""
	}
}

func foreachItemStringNonEmpty(item interface{}, keys ...string) string {
	switch v := item.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case map[string]interface{}:
		for _, key := range keys {
			value, ok := v[key]
			if !ok {
				continue
			}
			if s := strings.TrimSpace(fmt.Sprint(value)); s != "" {
				return s
			}
		}
	case map[string]string:
		for _, key := range keys {
			value, ok := v[key]
			if !ok {
				continue
			}
			if s := strings.TrimSpace(value); s != "" {
				return s
			}
		}
	}
	return ""
}
