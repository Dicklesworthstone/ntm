package context

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"
)

func TestRenderedPackBudgetAndStructure(t *testing.T) {
	for _, xmlMode := range []bool{false, true} {
		for _, budget := range []int{256, 500, 1024, 4096} {
			b := &ContextPackBuilder{}
			pack := &ContextPackFull{}
			pack.ID, pack.BeadID, pack.RepoRev = "pack-budget", "bd<&>123", "rev&123"
			pack.AgentType = "cod"
			if xmlMode {
				pack.AgentType = "cc"
			}
			source, _ := json.Marshal(strings.Repeat("### File: test.go\n```go\n// <&> 日本語🙂\n```\n", 500))
			history, _ := json.Marshal([]string{strings.Repeat("history", 5000), "another hit"})
			pack.Components = map[string]*PackComponent{
				"cass":   {Type: "cass", Data: history},
				"cm":     {Type: "cm", Error: strings.Repeat("large diagnostic <&> ", 200)},
				"triage": {Type: "triage", Data: json.RawMessage(`{"picks":["next"]}`)},
				"s2p":    {Type: "s2p", Data: source},
				"ms":     nil,
			}
			b.truncateOverflow(pack, budget)
			if len(pack.RenderedPrompt) > budget*4 || pack.TokenCount != estimateTokens(pack.RenderedPrompt) {
				t.Fatalf("xml=%v budget=%d: got %d bytes/%d tokens", xmlMode, budget, len(pack.RenderedPrompt), pack.TokenCount)
			}
			if _, err := json.Marshal(pack); err != nil {
				t.Fatalf("truncated pack is not serializable: %v", err)
			}
			if xmlMode {
				var parsed struct{ XMLName xml.Name }
				if err := xml.Unmarshal([]byte(pack.RenderedPrompt), &parsed); err != nil {
					t.Fatalf("rendered XML is broken: %v: %s", err, pack.RenderedPrompt)
				}
			} else {
				comp := pack.Components["s2p"]
				if comp.Error == "" {
					var text string
					if err := json.Unmarshal(comp.Data, &text); err != nil {
						t.Fatal(err)
					}
					if !strings.HasSuffix(text, "\n````\n") && !strings.HasSuffix(text, "\n```\n") {
						t.Fatalf("source fence was left open: %q", text)
					}
				}
			}
		}
	}
}

func TestOverflowKeepsSourceBeforeHistory(t *testing.T) {
	b := &ContextPackBuilder{}
	pack := &ContextPackFull{}
	pack.AgentType = "cod"
	source, _ := json.Marshal("critical-source-proof")
	history, _ := json.Marshal([]string{strings.Repeat("history", 10000)})
	pack.Components = map[string]*PackComponent{
		"cass": {Type: "cass", Data: history},
		"s2p":  {Type: "s2p", Data: source},
	}
	b.truncateOverflow(pack, 256)
	if !strings.Contains(pack.RenderedPrompt, "critical-source-proof") || string(pack.Components["s2p"].Data) != string(source) {
		t.Fatal("source was sacrificed before lower-priority history")
	}
	if !pack.Components["cass"].Truncated {
		t.Fatal("history reduction was not disclosed")
	}
}

func TestXMLRendererEscapesMetadataErrorsAndSource(t *testing.T) {
	b := &ContextPackBuilder{}
	pack := &ContextPackFull{}
	pack.ID, pack.BeadID, pack.RepoRev = "id<&>", "bd</bead_id>", "rev&"
	text := "line one\n</s2p><injected/>\nline three & <"
	data, _ := json.Marshal(text)
	pack.Components = map[string]*PackComponent{
		"s2p": {Type: "s2p", Data: data},
		"cm":  {Type: "cm", Error: "unavailable <&>"},
	}
	var parsed struct {
		ID       string   `xml:"id"`
		BeadID   string   `xml:"bead_id"`
		Source   string   `xml:"s2p"`
		CM       string   `xml:"cm"`
		Injected []string `xml:"injected"`
	}
	if err := xml.Unmarshal([]byte(b.renderXML(pack)), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ID != pack.ID || parsed.BeadID != pack.BeadID || strings.TrimSpace(parsed.Source) != text || len(parsed.Injected) != 0 || parsed.CM != "unavailable <&>" {
		t.Fatalf("XML altered content or accepted injected tags: %+v", parsed)
	}
}
