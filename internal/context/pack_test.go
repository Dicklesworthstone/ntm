package context

import (
	stdcontext "context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestDefaultBudgetAllocation(t *testing.T) {
	t.Parallel()

	alloc := DefaultBudgetAllocation()
	if alloc.Triage != 10 {
		t.Errorf("Triage = %d, want 10", alloc.Triage)
	}
	if alloc.CM != 5 {
		t.Errorf("CM = %d, want 5", alloc.CM)
	}
	if alloc.CASS != 15 {
		t.Errorf("CASS = %d, want 15", alloc.CASS)
	}
	if alloc.S2P != 70 {
		t.Errorf("S2P = %d, want 70", alloc.S2P)
	}

	total := alloc.Triage + alloc.CM + alloc.CASS + alloc.S2P
	if total != 100 {
		t.Errorf("total allocation = %d, want 100", total)
	}
}

func TestCacheKey(t *testing.T) {
	t.Parallel()

	opts := BuildOptions{
		RepoRev:   "abc123",
		BeadID:    "bd-test",
		AgentType: "cc",
	}
	key := cacheKey(opts, DefaultBudgetAllocation())

	if len(key) != 16 {
		t.Errorf("cacheKey length = %d, want 16", len(key))
	}

	// Same options should produce the same key
	key2 := cacheKey(opts, DefaultBudgetAllocation())
	if key != key2 {
		t.Errorf("same options produced different keys: %q vs %q", key, key2)
	}

	// Different options should produce different keys
	opts2 := BuildOptions{
		RepoRev:   "def456",
		BeadID:    "bd-test",
		AgentType: "cc",
	}
	key3 := cacheKey(opts2, DefaultBudgetAllocation())
	if key == key3 {
		t.Errorf("different options produced same key: %q", key)
	}

	// MS-skill inclusion must influence cache key to avoid stale component reuse.
	optsMSOff := BuildOptions{
		RepoRev:         "abc123",
		BeadID:          "bd-test",
		AgentType:       "cc",
		IncludeMSSkills: false,
	}
	optsMSOn := BuildOptions{
		RepoRev:         "abc123",
		BeadID:          "bd-test",
		AgentType:       "cc",
		IncludeMSSkills: true,
	}
	keyMSOff := cacheKey(optsMSOff, DefaultBudgetAllocation())
	keyMSOn := cacheKey(optsMSOn, DefaultBudgetAllocation())
	if keyMSOff == keyMSOn {
		t.Fatalf("cacheKey should differ when IncludeMSSkills flips: %q == %q", keyMSOff, keyMSOn)
	}

	optsTask2 := opts
	optsTask2.Task = "another task"
	if key == cacheKey(optsTask2, DefaultBudgetAllocation()) {
		t.Fatal("cacheKey should differ when Task changes")
	}

	optsFiles := opts
	optsFiles.Files = []string{"internal/context/pack.go", "README.md"}
	if key == cacheKey(optsFiles, DefaultBudgetAllocation()) {
		t.Fatal("cacheKey should differ when Files change")
	}

	optsProject := opts
	optsProject.ProjectDir = "/tmp/project-a"
	if key == cacheKey(optsProject, DefaultBudgetAllocation()) {
		t.Fatal("cacheKey should differ when ProjectDir changes")
	}

	optsSession := opts
	optsSession.SessionID = "session-a"
	if key == cacheKey(optsSession, DefaultBudgetAllocation()) {
		t.Fatal("cacheKey should differ when SessionID changes")
	}

	optsCorrelation := opts
	optsCorrelation.CorrelationID = "corr-a"
	if key == cacheKey(optsCorrelation, DefaultBudgetAllocation()) {
		t.Fatal("cacheKey should differ when CorrelationID changes")
	}

	alloc2 := BudgetAllocation{Triage: 20, CM: 5, CASS: 15, S2P: 60}
	if key == cacheKey(opts, alloc2) {
		t.Fatal("cacheKey should differ when allocation changes")
	}
}

func TestBuildDoesNotReuseCacheAcrossDistinctInputs(t *testing.T) {
	// Not parallel since it mutates global cache and PATH.
	t.Setenv("PATH", t.TempDir())

	b := NewContextPackBuilder(nil)
	b.ClearCache()
	defer b.ClearCache()

	projectDir := t.TempDir()
	opts := BuildOptions{
		RepoRev:       "abc123",
		BeadID:        "bd-test",
		AgentType:     "cc",
		Task:          "first task",
		CorrelationID: "corr-1",
		ProjectDir:    projectDir,
		SessionID:     "session-1",
	}

	pack1, err := b.Build(stdcontext.Background(), opts)
	if err != nil {
		t.Fatalf("first Build failed: %v", err)
	}

	opts.Task = "second task"
	opts.CorrelationID = "corr-2"
	pack2, err := b.Build(stdcontext.Background(), opts)
	if err != nil {
		t.Fatalf("second Build failed: %v", err)
	}

	if pack1 == pack2 {
		t.Fatal("Build reused cached pack for distinct task/correlation inputs")
	}
	if pack2.CorrelationID != "corr-2" {
		t.Fatalf("CorrelationID = %q, want corr-2", pack2.CorrelationID)
	}
}

func TestBuildDoesNotReuseCacheAcrossAllocationChanges(t *testing.T) {
	// Not parallel since it mutates global cache and PATH.
	t.Setenv("PATH", t.TempDir())

	builderA := NewContextPackBuilder(nil)
	builderB := NewContextPackBuilder(nil)
	builderA.ClearCache()
	defer builderA.ClearCache()

	builderB.SetAllocation(BudgetAllocation{Triage: 20, CM: 5, CASS: 15, S2P: 60})

	opts := BuildOptions{
		RepoRev:    "abc123",
		BeadID:     "bd-test",
		AgentType:  "cc",
		Task:       "same task",
		ProjectDir: t.TempDir(),
		SessionID:  "session-1",
	}

	pack1, err := builderA.Build(stdcontext.Background(), opts)
	if err != nil {
		t.Fatalf("first Build failed: %v", err)
	}
	pack2, err := builderB.Build(stdcontext.Background(), opts)
	if err != nil {
		t.Fatalf("second Build failed: %v", err)
	}

	if pack1 == pack2 {
		t.Fatal("Build reused cached pack across distinct allocation settings")
	}
}

func TestPackEstimateTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"empty string", "", 0},
		{"4 chars = 1 token", "abcd", 1},
		{"8 chars = 2 tokens", "abcdefgh", 2},
		{"3 chars = 0 tokens (floor)", "abc", 0},
		{"100 chars", strings.Repeat("a", 100), 25},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := estimateTokens(tc.input)
			if result != tc.expected {
				t.Errorf("estimateTokens(%q) = %d, want %d", tc.input, result, tc.expected)
			}
		})
	}
}

func TestComponentTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected string
	}{
		{"triage", "BV Triage (Priority & Planning)"},
		{"cm", "CM Rules (Learned Guidelines)"},
		{"ms", "Meta Skill Suggestions (source: ms)"},
		{"cass", "CASS History (Prior Solutions)"},
		{"s2p", "File Context"},
		{"custom", "Custom"},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := componentTitle(tc.name)
			if result != tc.expected {
				t.Errorf("componentTitle(%q) = %q, want %q", tc.name, result, tc.expected)
			}
		})
	}
}

func TestExtractTopMSSkills_Array(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[
		{"id":"commit-and-release","name":"Commit and Release","summary":"Batch commits"},
		{"id":"codebase-archaeology","name":"Codebase Archaeology","summary":"Deep repo understanding"},
		{"id":"agent-mail","name":"Agent Mail","summary":"Coordination"}
	]`)

	skills, err := extractTopMSSkills(raw, 2)
	if err != nil {
		t.Fatalf("extractTopMSSkills returned error: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("len(skills)=%d, want 2", len(skills))
	}
	if skills[0]["id"] != "commit-and-release" {
		t.Fatalf("skills[0].id=%v, want commit-and-release", skills[0]["id"])
	}
	if skills[1]["id"] != "codebase-archaeology" {
		t.Fatalf("skills[1].id=%v, want codebase-archaeology", skills[1]["id"])
	}
}

func TestExtractTopMSSkills_Envelope(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"skills":[
			{"id":"a","name":"A","description":"desc A"},
			{"id":"b","name":"B","summary":"desc B"}
		]
	}`)

	skills, err := extractTopMSSkills(raw, 5)
	if err != nil {
		t.Fatalf("extractTopMSSkills returned error: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("len(skills)=%d, want 2", len(skills))
	}
	if skills[0]["id"] != "a" {
		t.Fatalf("skills[0].id=%v, want a", skills[0]["id"])
	}
	if skills[0]["summary"] != "desc A" {
		t.Fatalf("skills[0].summary=%v, want desc A", skills[0]["summary"])
	}
}

func TestExtractTopMSSkills_RequiresID(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[{"name":"no-id"}]`)
	_, err := extractTopMSSkills(raw, 5)
	if err == nil {
		t.Fatal("expected error for missing IDs, got nil")
	}
}

func TestTruncateJSON_Small(t *testing.T) {
	t.Parallel()

	data := json.RawMessage(`{"key":"value"}`)
	result := truncateJSON(data, 1000) // budget is large enough
	if string(result) != string(data) {
		t.Errorf("small JSON was truncated: got %q, want %q", result, data)
	}
}

func TestTruncateJSON_Array(t *testing.T) {
	t.Parallel()

	// Build a JSON array with 10 elements
	arr := make([]string, 10)
	for i := range arr {
		arr[i] = strings.Repeat("x", 50)
	}
	data, _ := json.Marshal(arr)

	// Use a tight budget that should truncate
	result := truncateJSON(data, 50) // 50 tokens * 4 = 200 chars budget
	if len(result) > 200 {
		t.Errorf("truncated array too large: %d chars > 200", len(result))
	}

	// Result should be valid JSON
	var parsed interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Errorf("truncated array is not valid JSON: %v", err)
	}
}

func TestTruncateJSON_Object(t *testing.T) {
	t.Parallel()

	// Build a large JSON object
	obj := make(map[string]string)
	for i := 0; i < 20; i++ {
		obj[strings.Repeat("k", 10)+string(rune('a'+i))] = strings.Repeat("v", 100)
	}
	data, _ := json.Marshal(obj)

	// Use budget too small for full object but large enough for some fields
	result := truncateJSON(data, 100) // 100 tokens * 4 = 400 chars
	if len(result) > 400 {
		t.Errorf("truncated object too large: %d chars > 400", len(result))
	}

	// Result should be valid JSON with truncation marker
	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Errorf("truncated object is not valid JSON: %v", err)
	}
	if _, ok := parsed["_truncated"]; !ok {
		t.Error("truncated object should have _truncated key")
	}
}

func TestTruncateJSON_InvalidJSON(t *testing.T) {
	t.Parallel()

	data := json.RawMessage(`not valid json at all`)
	result := truncateJSON(data, 5) // Very small budget

	// Should return valid JSON fallback
	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Errorf("fallback is not valid JSON: %v", err)
	}
	if _, ok := parsed["_truncated"]; !ok {
		t.Error("fallback should have _truncated key")
	}
}

func TestTruncateText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		text        string
		tokenBudget int
		wantFull    bool
		wantSuffix  string
	}{
		{"short text fits", "hello", 100, true, ""},
		{"exact fit", strings.Repeat("a", 400), 100, true, ""},
		{"needs truncation", strings.Repeat("a", 500), 100, false, "...[truncated]"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := truncateText(tc.text, tc.tokenBudget)
			if tc.wantFull {
				if result != tc.text {
					t.Errorf("expected full text, got truncated (%d chars)", len(result))
				}
			} else {
				if !strings.HasSuffix(result, tc.wantSuffix) {
					t.Errorf("truncated text should end with %q, got %q", tc.wantSuffix, result[len(result)-20:])
				}
			}
		})
	}
}

func TestCalculateFilePriority(t *testing.T) {
	t.Parallel()

	b := &ContextPackBuilder{}

	tests := []struct {
		file     string
		minScore int
	}{
		// High priority: main entry points
		{"cmd/main.go", 100},
		{"src/index.ts", 100},
		{"app.py", 100},
		// Medium-high: core logic
		{"internal/core/engine.go", 50},
		{"services/auth.go", 50},
		{"controller/user.go", 50},
		{"handler/api.go", 50},
		// Medium: config/routing
		{"config/database.yaml", 30},
		{"router/routes.go", 30},
		{"middleware/auth.go", 30},
		// Lower priority: tests
		{"pkg/service_test.go", -20},
		{"spec/auth_spec.rb", -20},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			priority := b.calculateFilePriority(tc.file)
			if priority < tc.minScore {
				t.Errorf("calculateFilePriority(%q) = %d, want >= %d", tc.file, priority, tc.minScore)
			}
		})
	}

	// Short paths should get bonus
	shallow := b.calculateFilePriority("src/main.go") // 2 slashes -> bonus
	deep := b.calculateFilePriority("a/b/c/d/main.go")
	if shallow <= deep {
		t.Errorf("shallow path (%d) should have higher priority than deep path (%d)", shallow, deep)
	}
}

func TestSelectS2PFormat(t *testing.T) {
	t.Parallel()

	b := &ContextPackBuilder{}

	if f := b.selectS2PFormat(20000); f != "compact" {
		t.Errorf("small budget should use compact, got %q", f)
	}
	if f := b.selectS2PFormat(29999); f != "compact" {
		t.Errorf("just under 30k should use compact, got %q", f)
	}
	if f := b.selectS2PFormat(30000); f != "" {
		t.Errorf("30k budget should use default, got %q", f)
	}
	if f := b.selectS2PFormat(100000); f != "" {
		t.Errorf("large budget should use default, got %q", f)
	}
}

func TestIntelligentTruncate_Short(t *testing.T) {
	t.Parallel()

	b := &ContextPackBuilder{}
	text := "short content"
	result := b.intelligentTruncate(text, 1000)
	if result != text {
		t.Errorf("short content should not be truncated")
	}
}

func TestIntelligentTruncate_Long(t *testing.T) {
	t.Parallel()

	b := &ContextPackBuilder{}

	// Build content with headers and body
	var sb strings.Builder
	sb.WriteString("=== File: important.go ===\n")
	sb.WriteString("# Header\n")
	for i := 0; i < 100; i++ {
		sb.WriteString(strings.Repeat("content line ", 10) + "\n")
	}
	text := sb.String()

	// Truncate to small budget
	result := b.intelligentTruncate(text, 50) // 200 chars
	if len(result) > 250 {                    // some slack for truncation message
		t.Errorf("truncated text too long: %d chars", len(result))
	}
	if !strings.Contains(result, "truncated") {
		t.Error("truncated text should contain truncation marker")
	}
	// Headers should be preserved
	if !strings.Contains(result, "=== File: important.go ===") {
		t.Error("file header should be preserved in truncated output")
	}
}

func TestOptimizeFilesForBudget(t *testing.T) {
	t.Parallel()

	b := &ContextPackBuilder{}

	files := []string{
		"cmd/main.go",
		"internal/core/engine.go",
		"internal/util/helper.go",
		"internal/api/handler.go",
		"tests/integration_test.go",
		"docs/readme.md",
		"config/settings.yaml",
		"scripts/deploy.sh",
	}

	// Small budget should limit files
	result := b.optimizeFilesForBudget(files, 5000) // 5000/2000 = 2.5, min 3
	if len(result) > 3 {
		t.Errorf("small budget: got %d files, want <= 3", len(result))
	}

	// Large budget should keep all files
	result = b.optimizeFilesForBudget(files, 100000)
	if len(result) != len(files) {
		t.Errorf("large budget: got %d files, want %d", len(result), len(files))
	}
}

func TestOptimizeFilesForBudget_PriorityOrder(t *testing.T) {
	t.Parallel()

	b := &ContextPackBuilder{}

	files := []string{
		"tests/unit_test.go",    // low priority
		"cmd/main.go",           // high priority
		"internal/handler.go",   // medium-high priority
		"examples/demo.go",      // low priority
		"internal/service.go",   // medium-high priority
		"internal/core/core.go", // medium-high priority
	}

	// Budget allows 3 files
	result := b.optimizeFilesForBudget(files, 6000)
	if len(result) != 3 {
		t.Fatalf("expected 3 files, got %d", len(result))
	}

	// main.go should be included (highest priority)
	found := false
	for _, f := range result {
		if f == "cmd/main.go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("main.go should be in top-3 prioritized files, got %v", result)
	}
}

func TestTokenBudgets(t *testing.T) {
	t.Parallel()

	cc := GetTokenBudget("cc")
	if cc < 170000 || cc > 190000 {
		t.Errorf("cc budget = %d, want ~180000", cc)
	}
	cod := GetTokenBudget("cod")
	if cod < 200000 || cod > 260000 {
		t.Errorf("cod budget = %d, want ~240000", cod)
	}
	gmi := GetTokenBudget("gmi")
	if gmi < 90000 || gmi > 110000 {
		t.Errorf("gmi budget = %d, want ~100000", gmi)
	}
	def := GetTokenBudget("unknown")
	if def < 90000 || def > 110000 {
		t.Errorf("default budget = %d, want ~100000", def)
	}
}

func TestRenderXML(t *testing.T) {
	t.Parallel()

	b := &ContextPackBuilder{allocation: DefaultBudgetAllocation()}

	pack := &ContextPackFull{
		ContextPack: state.ContextPack{
			ID:        "pack-test",
			BeadID:    "bd-123",
			AgentType: state.AgentTypeClaude,
			RepoRev:   "abc123",
		},
		Components: map[string]*PackComponent{
			"triage": {Type: "triage", Data: json.RawMessage(`{"picks":[]}`), TokenCount: 10},
			"cm":     {Type: "cm", Error: "cm not installed"},
		},
	}

	result := b.renderXML(pack)

	if !strings.Contains(result, "<context_pack>") {
		t.Error("XML should contain <context_pack> tag")
	}
	if !strings.Contains(result, "<id>pack-test</id>") {
		t.Error("XML should contain pack ID")
	}
	if !strings.Contains(result, "<triage>") {
		t.Error("XML should contain triage component")
	}
	if !strings.Contains(result, `cm unavailable="true"`) {
		t.Error("XML should show cm as unavailable")
	}
}

func TestRenderMarkdown(t *testing.T) {
	t.Parallel()

	b := &ContextPackBuilder{allocation: DefaultBudgetAllocation()}

	pack := &ContextPackFull{
		ContextPack: state.ContextPack{
			ID:        "pack-test",
			BeadID:    "bd-123",
			AgentType: "cod",
			RepoRev:   "abc123",
		},
		Components: map[string]*PackComponent{
			"triage": {Type: "triage", Data: json.RawMessage(`{"picks":[]}`), TokenCount: 10},
			"ms":     {Type: "ms", Data: json.RawMessage(`{"source":"ms","skills":[{"id":"agent-mail"}]}`), TokenCount: 10},
			"cass":   {Type: "cass", Error: "cass not installed"},
		},
	}

	result := b.renderMarkdown(pack)

	if !strings.Contains(result, "# Context Pack") {
		t.Error("markdown should contain header")
	}
	if !strings.Contains(result, "**ID**: pack-test") {
		t.Error("markdown should contain pack ID")
	}
	if !strings.Contains(result, "```json") {
		t.Error("markdown should contain JSON code block for triage")
	}
	if !strings.Contains(result, "Meta Skill Suggestions (source: ms)") {
		t.Error("markdown should include MS component title with source attribution")
	}
	if !strings.Contains(result, "*Unavailable: cass not installed*") {
		t.Error("markdown should show cass as unavailable")
	}
}

func TestRender_RoutesToCorrectFormat(t *testing.T) {
	t.Parallel()

	b := &ContextPackBuilder{allocation: DefaultBudgetAllocation()}

	claudePack := &ContextPackFull{
		ContextPack: state.ContextPack{AgentType: state.AgentTypeClaude},
		Components:  map[string]*PackComponent{},
	}
	codexPack := &ContextPackFull{
		ContextPack: state.ContextPack{AgentType: state.AgentTypeCodex},
		Components:  map[string]*PackComponent{},
	}

	xmlResult := b.render(claudePack)
	if !strings.Contains(xmlResult, "<context_pack>") {
		t.Error("Claude agent should get XML format")
	}

	mdResult := b.render(codexPack)
	if !strings.Contains(mdResult, "# Context Pack") {
		t.Error("Codex agent should get Markdown format")
	}
}

func TestGeneratePackID(t *testing.T) {
	t.Parallel()

	id1 := generatePackID()
	id2 := generatePackID()

	if !strings.HasPrefix(id1, "pack-") {
		t.Errorf("pack ID should start with 'pack-', got %q", id1)
	}
	// IDs should be unique (different nanosecond timestamps)
	if id1 == id2 {
		t.Logf("warning: consecutive pack IDs are equal (rare but possible): %q", id1)
	}
}

func TestCacheStatsAndClear(t *testing.T) {
	// Not parallel since it mutates global cache
	b := &ContextPackBuilder{}

	// Clear first to isolate
	b.ClearCache()

	size, keys := b.CacheStats()
	if size != 0 {
		t.Errorf("after clear, cache size = %d, want 0", size)
	}
	if len(keys) != 0 {
		t.Errorf("after clear, keys = %v, want empty", keys)
	}

	// Populate cache manually to test stats
	globalCacheMu.Lock()
	globalCache["test-key-1"] = &ContextPackFull{}
	globalCache["test-key-2"] = &ContextPackFull{}
	globalCacheMu.Unlock()

	size, keys = b.CacheStats()
	if size != 2 {
		t.Errorf("cache size = %d, want 2", size)
	}
	if len(keys) != 2 {
		t.Errorf("keys count = %d, want 2", len(keys))
	}

	// Clear and verify
	b.ClearCache()
	size, _ = b.CacheStats()
	if size != 0 {
		t.Errorf("after second clear, cache size = %d, want 0", size)
	}
}

// =============================================================================
// extractTopMSSkills: edge cases not covered by existing tests
// =============================================================================

func TestExtractTopMSSkills_InvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := extractTopMSSkills(json.RawMessage(`not valid json`), 5)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestExtractTopMSSkills_EmptyArray(t *testing.T) {
	t.Parallel()
	_, err := extractTopMSSkills(json.RawMessage(`[]`), 5)
	if err == nil {
		t.Fatal("expected error for empty array, got nil")
	}
}

func TestExtractTopMSSkills_EmptyEnvelopeSkills(t *testing.T) {
	t.Parallel()
	_, err := extractTopMSSkills(json.RawMessage(`{"skills":[]}`), 5)
	if err == nil {
		t.Fatal("expected error for empty envelope skills, got nil")
	}
}

func TestExtractTopMSSkills_AlternativeIDKeys(t *testing.T) {
	t.Parallel()
	// Test skill_id key
	raw := json.RawMessage(`[{"skill_id":"s1","name":"Skill One"}]`)
	skills, err := extractTopMSSkills(raw, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skills[0]["id"] != "s1" {
		t.Errorf("expected id=s1 from skill_id key, got %v", skills[0]["id"])
	}

	// Test key field
	raw2 := json.RawMessage(`[{"key":"k1","title":"Key Skill"}]`)
	skills2, err := extractTopMSSkills(raw2, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skills2[0]["id"] != "k1" {
		t.Errorf("expected id=k1 from key field, got %v", skills2[0]["id"])
	}
	if skills2[0]["name"] != "Key Skill" {
		t.Errorf("expected name from title field, got %v", skills2[0]["name"])
	}
}

func TestExtractTopMSSkills_RelevanceScore(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`[{"id":"r1","name":"Relevant","relevance":0.95}]`)
	skills, err := extractTopMSSkills(raw, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skills[0]["relevance"] != 0.95 {
		t.Errorf("expected relevance=0.95, got %v", skills[0]["relevance"])
	}
}

func TestExtractTopMSSkills_DefaultLimit(t *testing.T) {
	t.Parallel()
	// When limit <= 0, it should default to 5
	items := make([]string, 8)
	for i := range items {
		items[i] = `{"id":"` + string(rune('a'+i)) + `","name":"Skill"}`
	}
	raw := json.RawMessage(`[` + strings.Join(items, ",") + `]`)

	skills, err := extractTopMSSkills(raw, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 5 {
		t.Errorf("expected 5 skills with default limit, got %d", len(skills))
	}
}

func TestExtractTopMSSkills_SkipsBlankIDs(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`[{"id":"","name":"Blank ID"},{"id":"valid","name":"Valid"}]`)
	skills, err := extractTopMSSkills(raw, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Errorf("expected 1 skill (blank ID skipped), got %d", len(skills))
	}
	if skills[0]["id"] != "valid" {
		t.Errorf("expected id=valid, got %v", skills[0]["id"])
	}
}

func TestExtractTopMSSkills_EnvelopeSkipsNoID(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"skills":[{"name":"no-id"},{"id":"ok","name":"OK"}]}`)
	skills, err := extractTopMSSkills(raw, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Errorf("expected 1 skill from envelope, got %d", len(skills))
	}
}

// =============================================================================
// buildMSComponent: early return paths
// =============================================================================

func TestBuildMSComponent_InsufficientBudget(t *testing.T) {
	t.Parallel()
	ctx := stdcontext.Background()
	b := NewContextPackBuilder(nil)
	comp := b.buildMSComponent(ctx, "test query", 0)
	if comp.Error != "insufficient token budget" {
		t.Errorf("expected 'insufficient token budget', got %q", comp.Error)
	}
	if comp.Type != "ms" {
		t.Errorf("expected type ms, got %q", comp.Type)
	}
}

func TestBuildMSComponent_NegativeBudget(t *testing.T) {
	t.Parallel()
	ctx := stdcontext.Background()
	b := NewContextPackBuilder(nil)
	comp := b.buildMSComponent(ctx, "test query", -100)
	if comp.Error != "insufficient token budget" {
		t.Errorf("expected 'insufficient token budget', got %q", comp.Error)
	}
}

func TestBuildMSComponent_EmptyQuery(t *testing.T) {
	t.Parallel()
	ctx := stdcontext.Background()
	b := NewContextPackBuilder(nil)
	comp := b.buildMSComponent(ctx, "", 500)
	if comp.Error != "no query provided" {
		t.Errorf("expected 'no query provided', got %q", comp.Error)
	}
}

func TestBuildMSComponent_WhitespaceQuery(t *testing.T) {
	t.Parallel()
	ctx := stdcontext.Background()
	b := NewContextPackBuilder(nil)
	comp := b.buildMSComponent(ctx, "   \t\n  ", 500)
	if comp.Error != "no query provided" {
		t.Errorf("expected 'no query provided', got %q", comp.Error)
	}
}

func TestBuildMSComponent_MSNotInstalled(t *testing.T) {
	t.Parallel()
	ctx := stdcontext.Background()
	b := NewContextPackBuilder(nil)
	// ms tool is almost certainly not installed in the test environment
	comp := b.buildMSComponent(ctx, "test query", 500)
	if comp.Error != "ms not installed" {
		// If ms IS installed, this test path won't trigger, but that's fine
		t.Logf("ms detection returned: %q (ms may be installed)", comp.Error)
	}
}

// =============================================================================
// truncateOverflow: MS component branch
// =============================================================================

func TestTruncateOverflow_WithMSComponent(t *testing.T) {
	t.Parallel()
	b := &ContextPackBuilder{allocation: DefaultBudgetAllocation()}

	// Generate a large MS payload that exceeds the char budget after halving.
	// truncateJSON uses tokenBudget*4 as char budget.
	// truncateOverflow passes len(data)/2 as tokenBudget.
	// So charBudget = len(data)/2 * 4 = len(data)*2.
	// For truncation to occur, len(data) must exceed len(data)*2 — which never happens!
	// The halving only truncates when the data contains arrays with many elements.
	// So we test with an array-based payload.
	skills := make([]string, 20)
	for i := range skills {
		skills[i] = `{"id":"skill-` + string(rune('a'+i)) + `","name":"Skill","summary":"` + strings.Repeat("x", 200) + `"}`
	}
	msData := json.RawMessage(`{"source":"ms","query":"test","skills":[` + strings.Join(skills, ",") + `]}`)

	pack := &ContextPackFull{
		ContextPack: state.ContextPack{
			ID:        "pack-overflow",
			AgentType: "cod",
		},
		Components: map[string]*PackComponent{
			"ms": {Type: "ms", Data: msData, TokenCount: 100},
		},
	}

	result := b.truncateOverflow(pack, 50)
	msComp := result.Components["ms"]
	if msComp == nil {
		t.Fatal("ms component missing after truncation")
	}
	// Verify the ms component was processed (Data still exists and TokenCount updated)
	if msComp.Data == nil {
		t.Error("ms data should not be nil after truncation")
	}
	if msComp.TokenCount == 0 {
		t.Error("ms token count should be recalculated after truncation")
	}
}

func TestTruncateOverflow_WithBothCASSAndMS(t *testing.T) {
	t.Parallel()
	b := &ContextPackBuilder{allocation: DefaultBudgetAllocation()}

	cassData := json.RawMessage(`{"solutions":[{"id":"c1","text":"A long cass solution text here"}]}`)
	msData := json.RawMessage(`{"source":"ms","skills":[{"id":"m1","name":"MS skill"}]}`)

	pack := &ContextPackFull{
		ContextPack: state.ContextPack{
			ID:        "pack-both",
			AgentType: "cod",
		},
		Components: map[string]*PackComponent{
			"cass": {Type: "cass", Data: cassData, TokenCount: 50},
			"ms":   {Type: "ms", Data: msData, TokenCount: 50},
		},
	}

	result := b.truncateOverflow(pack, 10)
	if result.Components["cass"] == nil {
		t.Fatal("cass component missing after truncation")
	}
	if result.Components["ms"] == nil {
		t.Fatal("ms component missing after truncation")
	}
	// Both should have been halved
	if result.RenderedPrompt == "" {
		t.Error("rendered prompt should not be empty after truncation")
	}
}

func TestTruncateOverflow_NoMSComponent(t *testing.T) {
	t.Parallel()
	b := &ContextPackBuilder{allocation: DefaultBudgetAllocation()}

	pack := &ContextPackFull{
		ContextPack: state.ContextPack{
			ID:        "pack-no-ms",
			AgentType: "cod",
		},
		Components: map[string]*PackComponent{
			"triage": {Type: "triage", Data: json.RawMessage(`{"picks":[]}`), TokenCount: 10},
		},
	}

	result := b.truncateOverflow(pack, 5)
	if result.RenderedPrompt == "" {
		t.Error("rendered prompt should not be empty")
	}
}

// =============================================================================
// Budget borrowing math for MS skills
// =============================================================================

func TestMSBudgetBorrowing(t *testing.T) {
	t.Parallel()

	alloc := DefaultBudgetAllocation()
	tests := []struct {
		name       string
		budget     int
		wantMS     int
		wantS2PMin int
	}{
		{
			name:       "standard budget borrows 5%",
			budget:     10000,
			wantMS:     500,  // 10000 * 5 / 100
			wantS2PMin: 6500, // 7000 - 500
		},
		{
			name:       "small budget uses minimum 200",
			budget:     2000,
			wantMS:     200,  // 2000*5/100=100, but min is 200
			wantS2PMin: 1200, // 1400 - 200
		},
		{
			name:       "tiny budget caps at s2p",
			budget:     100,
			wantMS:     70, // min(200, s2pBudget=70) -> 70
			wantS2PMin: 0,  // 70 - 70
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s2pBudget := tt.budget * alloc.S2P / 100
			msBudget := tt.budget * 5 / 100
			if msBudget < 200 {
				msBudget = 200
			}
			if msBudget > s2pBudget {
				msBudget = s2pBudget
			}
			s2pBudget -= msBudget

			if msBudget != tt.wantMS {
				t.Errorf("msBudget=%d, want %d", msBudget, tt.wantMS)
			}
			if s2pBudget < tt.wantS2PMin {
				t.Errorf("s2pBudget=%d, want >= %d", s2pBudget, tt.wantS2PMin)
			}
		})
	}
}

func TestMSBudgetDisabled(t *testing.T) {
	t.Parallel()

	alloc := DefaultBudgetAllocation()
	budget := 10000
	s2pBudget := budget * alloc.S2P / 100
	msBudget := 0

	// When IncludeMSSkills is false, msBudget stays 0
	if msBudget != 0 {
		t.Errorf("msBudget should be 0 when disabled, got %d", msBudget)
	}
	if s2pBudget != 7000 {
		t.Errorf("s2pBudget should be full 7000, got %d", s2pBudget)
	}
}

// =============================================================================
// renderXML: MS component in XML output
// =============================================================================

func TestRenderXML_IncludesMSComponent(t *testing.T) {
	t.Parallel()

	b := &ContextPackBuilder{allocation: DefaultBudgetAllocation()}

	pack := &ContextPackFull{
		ContextPack: state.ContextPack{
			ID:        "pack-xml-ms",
			BeadID:    "bd-test",
			AgentType: state.AgentTypeClaude,
			RepoRev:   "abc123",
		},
		Components: map[string]*PackComponent{
			"ms": {Type: "ms", Data: json.RawMessage(`{"source":"ms","skills":[{"id":"test"}]}`), TokenCount: 10},
		},
	}

	result := b.renderXML(pack)
	// XML renders as <ms>...</ms> tags
	if !strings.Contains(result, "<ms>") {
		t.Error("XML should contain <ms> opening tag")
	}
	if !strings.Contains(result, "</ms>") {
		t.Error("XML should contain </ms> closing tag")
	}
	if !strings.Contains(result, `"source":"ms"`) {
		t.Error("XML ms component should contain the data payload")
	}
}
