package swarm

import (
	"strings"
	"testing"
)

func TestDefaultReviewTemplates(t *testing.T) {
	// Check that default templates exist for all agent types
	agentTypes := []string{"cc", "cod", "gmi"}

	for _, agentType := range agentTypes {
		template := defaultReviewTemplates[agentType]
		if template == "" {
			t.Errorf("expected default template for agent type %q", agentType)
		}

		// Templates should mention no changes/modifications
		templateLower := strings.ToLower(template)
		hasNoChanges := strings.Contains(templateLower, "no") &&
			(strings.Contains(templateLower, "change") || strings.Contains(templateLower, "modification"))
		if !hasNoChanges {
			t.Errorf("template for %q should mention 'no changes' or 'no modifications'", agentType)
		}
	}
}

func TestReviewOptionsZeroValue(t *testing.T) {
	opts := ReviewOptions{}

	if opts.FocusArea != "" {
		t.Error("expected empty FocusArea in zero value")
	}
	if opts.FilePattern != "" {
		t.Error("expected empty FilePattern in zero value")
	}
}
