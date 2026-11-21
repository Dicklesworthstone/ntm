package assign

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
)

// PromptTemplateSource resolves the raw prompt template body for an assignment
// template name ("impl", "review", "custom") and optional custom template file.
func PromptTemplateSource(templateName, templateFile string) string {
	switch strings.ToLower(templateName) {
	case "impl":
		return "Work on bead {BEAD_ID}: {TITLE}. Check dependencies first with `br dep tree {BEAD_ID}`."
	case "review":
		return "Review and verify bead {BEAD_ID}: {TITLE}. Run tests if applicable."
	case "custom":
		if templateFile != "" {
			data, err := os.ReadFile(templateFile)
			if err == nil {
				return string(data)
			}
			return "Work on bead {BEAD_ID}: {TITLE}. Check dependencies first."
		}
		return "Work on bead {BEAD_ID}: {TITLE}."
	default:
		return "Work on bead {BEAD_ID}: {TITLE}. Check dependencies first with `br dep tree {BEAD_ID}`."
	}
}

// RenderPromptTemplate substitutes {BEAD_ID} and {TITLE} placeholders into a
// prompt template string.
func RenderPromptTemplate(beadID, title, template string) string {
	result := strings.ReplaceAll(template, "{BEAD_ID}", beadID)
	result = strings.ReplaceAll(result, "{TITLE}", title)
	return result
}

// ExpandPromptTemplate resolves and renders an assignment prompt template.
func ExpandPromptTemplate(beadID, title, templateName, templateFile string) string {
	return RenderPromptTemplate(beadID, title, PromptTemplateSource(templateName, templateFile))
}

// PromptHashSHA256 returns the hex-encoded SHA-256 digest of prompt so
// dispatch receipts can verify wire fidelity without logging prompt contents.
func PromptHashSHA256(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}
