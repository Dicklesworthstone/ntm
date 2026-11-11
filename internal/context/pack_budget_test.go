package context

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/models"
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

func TestOverflowRetainsSmallMSPayload(t *testing.T) {
	b := &ContextPackBuilder{}
	pack := &ContextPackFull{}
	pack.ID, pack.AgentType = "pack-overflow", "cod"
	skills := make([]string, 20)
	for i := range skills {
		skills[i] = `{"id":"skill-` + string(rune('a'+i)) + `","name":"Skill","summary":"` + strings.Repeat("x", 200) + `"}`
	}
	pack.Components = map[string]*PackComponent{
		"ms": {Type: "ms", Data: json.RawMessage(`{"source":"ms","query":"test","skills":[` + strings.Join(skills, ",") + `]}`)},
	}
	b.truncateOverflow(pack, 50)
	if pack.Components["ms"].Data == nil || pack.Components["ms"].TokenCount == 0 || len(pack.RenderedPrompt) > 200 {
		t.Fatalf("small MS payload was lost or exceeded budget: %s", pack.RenderedPrompt)
	}
}

func TestBuildHonorsTargetModelWindowAndTokenCap(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(strings.Repeat("// capacity source <&>\n", 4000)), 0600); err != nil {
		t.Fatal(err)
	}
	builder := NewContextPackBuilder(nil)
	builder.ClearCache()
	t.Cleanup(builder.ClearCache)
	tests := []struct {
		name, agentType, model string
		window, cap, want      int
	}{
		{"small registered model", "gmi", "gemini-pro", 0, 0, 3200},
		{"large model explicit cap", "cc", "claude-opus-5", 0, 512, 512},
		{"provider reported window", "cod", "custom-model", 4096, 0, 3850},
		{"cap cannot enlarge window", "cc", "claude-opus-5", 1000, 5000, 900},
		{"standalone cap", "cc", "", 0, 1024, 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pack, err := builder.Build(context.Background(), BuildOptions{
				AgentType: tt.agentType, Model: tt.model, ContextWindow: tt.window,
				MaxTokens: tt.cap, ProjectDir: dir, Files: []string{"main.go"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if pack.TokenBudget != tt.want || len(pack.RenderedPrompt) > tt.want*4 || pack.TokenCount > tt.want {
				t.Fatalf("pack exceeds target budget: budget=%d tokens=%d bytes=%d want=%d", pack.TokenBudget, pack.TokenCount, len(pack.RenderedPrompt), tt.want)
			}
			if pack.Model != tt.model || !strings.Contains(pack.RenderedPrompt, "capacity source") {
				t.Fatalf("real source/model lost: model=%q prompt=%s", pack.Model, pack.RenderedPrompt)
			}
			window := tt.window
			if window == 0 && tt.model != "" {
				window = models.GetContextLimit(tt.model)
			}
			if pack.ContextWindow != window {
				t.Fatalf("window metadata = %d, want %d", pack.ContextWindow, window)
			}
			if tt.agentType == "cc" {
				var parsed struct{ XMLName xml.Name }
				if err := xml.Unmarshal([]byte(pack.RenderedPrompt), &parsed); err != nil {
					t.Fatalf("capped pack broke XML: %v", err)
				}
			}
		})
	}
}

func TestBuildRejectsInvalidBudgetsBeforeProviderIO(t *testing.T) {
	// Nil adapters make reaching provider work a failure: invalid budgets
	// must be rejected before adapters, source access, or cache lookup.
	builder := &ContextPackBuilder{allocation: DefaultBudgetAllocation()}
	builder.ClearCache()
	t.Cleanup(builder.ClearCache)
	for _, opts := range []BuildOptions{
		{AgentType: "cc", MaxTokens: -1},
		{AgentType: "cc", ContextWindow: -1},
		{AgentType: "cc", Model: "unknown-model-no-reported-window", MaxTokens: 500},
		{AgentType: "gmi", ContextWindow: 1},
	} {
		if pack, err := builder.Build(context.Background(), opts); err == nil || pack != nil {
			t.Fatalf("invalid budget returned a pack: opts=%+v pack=%v err=%v", opts, pack != nil, err)
		}
	}
	if size, _ := builder.CacheStats(); size != 0 {
		t.Fatal("invalid budget populated the cache")
	}
}

func TestBuildCacheSeparatesModelWindowAndCap(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	builder := NewContextPackBuilder(nil)
	builder.ClearCache()
	t.Cleanup(builder.ClearCache)
	base := BuildOptions{AgentType: "cc", Model: "claude-opus-4", ProjectDir: t.TempDir(), MaxTokens: 500}
	first, err := builder.Build(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"model", "window", "cap"} {
		t.Run(field, func(t *testing.T) {
			opts := base
			switch field {
			case "model":
				opts.Model = "claude-sonnet-4" // same window and cap, different target
			case "window":
				opts.ContextWindow = 100000 // cap remains 500
			case "cap":
				opts.MaxTokens = 501
			}
			pack, err := builder.Build(context.Background(), opts)
			if err != nil || pack == first {
				t.Fatalf("%s reused incompatible cached pack: %v", field, err)
			}
			if repeated, err := builder.Build(context.Background(), opts); err != nil || repeated != pack {
				t.Fatalf("identical target failed to reuse cache: %v", err)
			}
		})
	}
	if repeated, err := builder.Build(context.Background(), base); err != nil || repeated != first {
		t.Fatalf("original target lost its cache entry: %v", err)
	}
}

func TestBuildCacheReevaluatesConfiguredDefaultBudget(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	original := models.GetContextLimit("claude-opus-4")
	t.Cleanup(func() { models.ApplyOverrides(map[string]int{"claude-opus-4": original}) })
	builder := NewContextPackBuilder(nil)
	builder.ClearCache()
	t.Cleanup(builder.ClearCache)
	opts := BuildOptions{AgentType: "cc", ProjectDir: t.TempDir()}
	first, err := builder.Build(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	models.ApplyOverrides(map[string]int{"claude-opus-4": 2000})
	second, err := builder.Build(context.Background(), opts)
	if err != nil || second == first {
		t.Fatalf("changed registry reused default-model cache: %v", err)
	}
	if second.TokenBudget != 1800 || len(second.RenderedPrompt) > 7200 {
		t.Fatalf("new default budget not applied: %+v", second)
	}
}

func TestBuildTinyCapCannotPublishOversizedMetadata(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	builder := NewContextPackBuilder(nil)
	builder.ClearCache()
	t.Cleanup(builder.ClearCache)
	pack, err := builder.Build(context.Background(), BuildOptions{AgentType: "cc", ProjectDir: t.TempDir(), MaxTokens: 1})
	if err == nil || pack != nil || !strings.Contains(err.Error(), "metadata exceeds") {
		t.Fatalf("unrepresentable cap returned a pack: pack=%v err=%v", pack != nil, err)
	}
	if size, _ := builder.CacheStats(); size != 0 {
		t.Fatal("oversized metadata entered cache")
	}
}
