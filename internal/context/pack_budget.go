package context

import "encoding/json"

// truncateOverflow measures the complete rendered prompt, not just component
// payloads. Lower-priority history and hints are reduced before source context.
// Never cut the final string: that could leave broken XML or open code fences.
func (b *ContextPackBuilder) truncateOverflow(pack *ContextPackFull, budget int) *ContextPackFull {
	limit := tokenByteBudget(budget)
	refresh := func() bool {
		pack.RenderedPrompt = b.render(pack)
		pack.TokenCount = estimateTokens(pack.RenderedPrompt)
		return len(pack.RenderedPrompt) <= limit
	}
	if refresh() {
		return pack
	}
	for _, name := range []string{"cass", "ms", "cm", "triage", "s2p"} {
		component := pack.Components[name]
		if component == nil {
			continue
		}
		original := *component
		*component = omittedBudgetComponent(original.Type)
		if !refresh() {
			continue
		}
		// Omission fits. Recover as much of this component as can fit, always
		// retaining a known-fitting candidate even when JSON shapes change.
		best := *component
		lo, hi := 1, (len(original.Data)+len(original.Error)+3)/4
		for lo <= hi {
			mid := lo + (hi-lo)/2
			*component = reducedBudgetComponent(original, name, mid)
			if refresh() {
				best = *component
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		*component = best
		refresh()
		return pack
	}
	// Metadata alone may exceed the limit. Build must reject that result
	// rather than returning a successful oversized or malformed prompt.
	return pack
}

func omittedBudgetComponent(kind string) PackComponent {
	return PackComponent{Type: kind, Truncated: true, Error: "omitted: context pack budget"}
}

func reducedBudgetComponent(original PackComponent, name string, budget int) PackComponent {
	candidate := original
	candidate.Truncated = true
	if original.Error != "" {
		candidate.Error = truncateTextBytes(original.Error, tokenByteBudget(budget))
		candidate.Data = nil
		candidate.TokenCount = 0
		return candidate
	}
	if name == "s2p" {
		var text string
		if err := json.Unmarshal(original.Data, &text); err != nil {
			return omittedBudgetComponent(original.Type)
		}
		limit := tokenByteBudget(budget)
		if len(text) > limit {
			// An outer fence preserves structure when shortening a prepared
			// multi-file pack that itself contains Markdown code fences.
			text = renderSourceFile("source context", text, limit, true, false)
			if text == "" {
				return omittedBudgetComponent(original.Type)
			}
		}
		candidate.Data, _ = json.Marshal(text)
		candidate.TokenCount = estimateTokens(text)
	} else {
		candidate.Data = truncateJSON(original.Data, budget)
		candidate.TokenCount = estimateTokens(string(candidate.Data))
	}
	return candidate
}
