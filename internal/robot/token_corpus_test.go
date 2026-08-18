package robot

// token_corpus_test.go — bd-ws3-contract-breadth-psvyu.3 (D3).
//
// This file owns the committed real-envelope corpus under
// testdata/token_corpus/ and the token-efficiency regression floors that the
// user-facing docs cite. The corpus is >=50 envelopes across >=8 robot
// surfaces, generated hermetically from the same output structs and Get*
// builders the production surfaces use, then serialized with the production
// JSON renderer (Render(v, FormatJSON)).
//
// Two floors are pinned (see TestTokenCorpus_MarkdownFloor and
// TestTokenCorpus_TOONFloor). Doc claims referencing measured savings:
//   - internal/cli/root.go   --robot-markdown flag help
//   - internal/robot/robot.go robot help text
//   - docs/planning/AGENT_FRIENDLINESS_REPORT.md
//
// To regenerate the corpus (only when envelope shapes change intentionally):
//
//	NTM_UPDATE_TOKEN_CORPUS=1 go test -run TestGenerateTokenCorpus ./internal/robot/

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/tokens"
)

const (
	tokenCorpusDir        = "testdata/token_corpus"
	fixedCorpusTimestamp  = "2026-08-16T12:00:00Z"
	tokenCorpusMinFiles   = 50
	tokenCorpusMinSurface = 8
)

// corpusEnvelope pairs a real robot output payload with its surface name.
type corpusEnvelope struct {
	// Surface is the robot surface family (filename prefix).
	Surface string
	// Name distinguishes variants within a surface.
	Name string
	// Payload is the real output struct for the surface.
	Payload any
}

func fixedCorpusResponse(success bool) RobotResponse {
	return RobotResponse{
		Success:      success,
		Timestamp:    fixedCorpusTimestamp,
		Version:      EnvelopeVersion,
		OutputFormat: "json",
	}
}

func normalizeCorpusResponse(r *RobotResponse) {
	r.Timestamp = fixedCorpusTimestamp
	r.OutputFormat = "json"
	// The envelope version is a process global other tests may set; pin it so
	// corpus generation and the staleness ratchet are order-independent.
	r.Version = "dev"
}

// buildCorpusSnapshot builds a deterministic SnapshotOutput at a given scale.
// Variants vary session/agent/alert/work counts to cover the range from a
// single quiet session to a large busy swarm.
func buildCorpusSnapshot(variant int) *SnapshotOutput {
	sessionCount := 1 + variant           // 1..10
	agentsPerSession := 2 + (variant % 4) // 2..5
	alertCount := variant                 // 0..9
	workReady := 2 + variant              // 2..11
	states := []string{"active", "idle", "waiting"}
	agentTypes := []string{"claude", "codex", "gemini", "cursor"}

	sessions := make([]SnapshotSession, 0, sessionCount)
	totalAgents := 0
	for s := 0; s < sessionCount; s++ {
		agents := make([]SnapshotAgent, 0, agentsPerSession)
		for a := 0; a < agentsPerSession; a++ {
			bead := fmt.Sprintf("bd-corpus-%d%d", s, a)
			agent := SnapshotAgent{
				Pane:             fmt.Sprintf("%d", a+1),
				Type:             agentTypes[(s+a)%len(agentTypes)],
				Variant:          []string{"", "opus", "gpt-5-codex", ""}[(s+a)%4],
				TypeConfidence:   0.95,
				TypeMethod:       "process",
				State:            states[(s+a)%len(states)],
				LastOutputAgeSec: 5 + 13*a,
				OutputTailLines:  50,
				ContextPercent:   float64(20 + 7*((s+a)%10)),
				PendingMail:      (s + a) % 3,
			}
			if (s+a)%2 == 0 {
				agent.CurrentBead = &bead
			}
			agents = append(agents, agent)
			totalAgents++
		}
		sessions = append(sessions, SnapshotSession{
			Name:     fmt.Sprintf("corpus-session-%d", s),
			Attached: s%2 == 0,
			Agents:   agents,
		})
	}

	alertsDetailed := make([]AlertInfo, 0, alertCount)
	severities := []string{"critical", "warning", "info"}
	for i := 0; i < alertCount; i++ {
		alertsDetailed = append(alertsDetailed, AlertInfo{
			ID:         fmt.Sprintf("alert-%d", i),
			Type:       []string{"agent_stuck", "context_high", "rate_limited"}[i%3],
			Severity:   severities[i%3],
			Message:    fmt.Sprintf("Agent in pane %d of corpus-session-%d needs attention", i%4+1, i%sessionCount),
			Session:    fmt.Sprintf("corpus-session-%d", i%sessionCount),
			Pane:       fmt.Sprintf("%d", i%4+1),
			CreatedAt:  fixedCorpusTimestamp,
			DurationMs: int64(1000 * (i + 1)),
			Count:      1 + i%3,
		})
	}

	ready := make([]adapters.WorkItem, 0, workReady)
	for i := 0; i < workReady; i++ {
		ready = append(ready, adapters.WorkItem{
			ID:       fmt.Sprintf("bd-ready-%d", i),
			Title:    fmt.Sprintf("Implement corpus work item %d with realistic title text", i),
			Priority: i%4 + 1,
			Type:     []string{"task", "bug", "feature"}[i%3],
			Labels:   []string{"corpus", fmt.Sprintf("wave-%d", i%3)},
		})
	}

	snapshot := &SnapshotOutput{
		RobotResponse:            fixedCorpusResponse(true),
		SchemaID:                 defaultRobotSchemaID("snapshot"),
		SchemaVersion:            "1.0.0",
		Timestamp:                fixedCorpusTimestamp,
		AttentionContractVersion: "1.0",
		LatestCursor:             int64(100 + variant*17),
		ReplayWindow: SnapshotReplayWindowInfo{
			Supported:       true,
			OldestCursor:    1,
			LatestCursor:    int64(100 + variant*17),
			EventCount:      100 + variant*17,
			OldestTimestamp: fixedCorpusTimestamp,
			LatestTimestamp: fixedCorpusTimestamp,
			RetentionPeriod: "1h",
			ResyncCommand:   "ntm --robot-snapshot",
		},
		Sessions:        sessions,
		ActiveIncidents: []SnapshotIncident{},
		Summary: StatusSummary{
			TotalSessions: sessionCount,
			TotalAgents:   totalAgents,
			AttachedCount: (sessionCount + 1) / 2,
			ClaudeCount:   totalAgents / 2,
			CodexCount:    totalAgents / 4,
			GeminiCount:   totalAgents / 4,
			AgentsByState: map[string]int{
				"active":  totalAgents / 2,
				"idle":    totalAgents / 4,
				"waiting": totalAgents - totalAgents/2 - totalAgents/4,
			},
			AgentsByType: map[string]int{
				"claude": totalAgents / 2,
				"codex":  totalAgents / 4,
				"gemini": totalAgents / 4,
			},
			ReadyWork:    workReady,
			InProgress:   variant % 5,
			HealthScore:  0.9 - float64(alertCount)*0.03,
			HealthStatus: []string{"healthy", "degraded"}[min(alertCount/5, 1)],
			AlertsActive: alertCount,
			MailUnread:   variant % 4,
		},
		Work: &adapters.WorkSection{
			Available:  true,
			Ready:      ready,
			Blocked:    []adapters.WorkItem{},
			InProgress: ready[:min(len(ready), variant%3+1)],
		},
		BeadsSummary: &bv.BeadsSummary{
			Available:  true,
			Project:    "ntm",
			Total:      40 + variant*7,
			Open:       20 + variant*3,
			InProgress: variant % 5,
			Blocked:    variant % 4,
			Ready:      workReady,
			Closed:     15 + variant*4,
		},
		Alerts:         []string{},
		AlertsDetailed: alertsDetailed,
		AlertSummary: &AlertSummaryInfo{
			TotalActive: alertCount,
			BySeverity:  map[string]int{"critical": alertCount / 3, "warning": alertCount / 3, "info": alertCount - 2*(alertCount/3)},
			ByType:      map[string]int{"agent_stuck": alertCount / 2, "context_high": alertCount - alertCount/2},
		},
		AttentionSummary: &SnapshotAttentionSummary{
			TotalEvents:         30 + variant*11,
			ActionRequiredCount: variant % 4,
			InterestingCount:    variant % 7,
			TopItems: []SnapshotAttentionItem{
				{Cursor: int64(90 + variant), Category: "agent", Actionability: "action_required", Severity: "warning", Summary: "Agent idle with unread mail"},
			},
			ByCategoryCount: map[string]int{"agent": 10 + variant, "alert": variant, "system": 3},
		},
	}
	return snapshot
}

func buildCorpusStatus(variant int) *StatusOutput {
	sessionCount := 1 + variant*2
	headers := make([]StatusSessionHeader, 0, sessionCount)
	for i := 0; i < sessionCount; i++ {
		health := StatusSessionHealth{Status: []string{"healthy", "warning", "critical"}[i%3]}
		if i%3 != 0 {
			health.Reason = "agent idle >10m with pending work"
		}
		headers = append(headers, StatusSessionHeader{
			Name:       fmt.Sprintf("proj-%c", 'a'+i%26),
			Attached:   i%2 == 0,
			AgentCount: 2 + i%6,
			Health:     health,
		})
	}
	return &StatusOutput{
		RobotResponse: fixedCorpusResponse(true),
		SchemaID:      defaultRobotSchemaID("status"),
		SchemaVersion: "1.0.0",
		OverallStatus: []string{"healthy", "degraded"}[variant%2],
		System: SystemInfo{
			Version:   "1.24.0",
			Commit:    "abcdef1234567890",
			BuildDate: "2026-08-01T00:00:00Z",
			GoVersion: "go1.24",
			OS:        "darwin",
			Arch:      "arm64",
			TmuxOK:    true,
		},
		Sessions: headers,
		Summary: StatusSummary{
			TotalSessions: sessionCount,
			TotalAgents:   sessionCount * 3,
			AttachedCount: (sessionCount + 1) / 2,
			ClaudeCount:   sessionCount * 2,
			CodexCount:    sessionCount,
			AgentsByState: map[string]int{"active": sessionCount * 2, "idle": sessionCount},
			AgentsByType:  map[string]int{"claude": sessionCount * 2, "codex": sessionCount},
			ReadyWork:     5 + variant,
			InProgress:    variant,
			HealthScore:   0.88,
			HealthStatus:  "healthy",
		},
		Beads: &bv.BeadsSummary{
			Available: true,
			Project:   "ntm",
			Total:     100 + variant*10,
			Open:      40,
			Ready:     5 + variant,
			Closed:    55 + variant*10,
		},
	}
}

func buildCorpusAlerts(variant int) *AlertsOutput {
	active := make([]AlertInfo, 0, variant+1)
	for i := 0; i <= variant; i++ {
		active = append(active, AlertInfo{
			ID:         fmt.Sprintf("alert-%d-%d", variant, i),
			Source:     "monitor",
			Type:       []string{"agent_stuck", "context_high", "rate_limited", "disk_pressure"}[i%4],
			Severity:   []string{"critical", "warning", "info"}[i%3],
			Message:    fmt.Sprintf("Pane %d in session swarm-%d has produced no output for %d minutes", i%4+1, variant, 5+i),
			Session:    fmt.Sprintf("swarm-%d", variant),
			Pane:       fmt.Sprintf("%d", i%4+1),
			CreatedAt:  fixedCorpusTimestamp,
			DurationMs: int64(60000 * (i + 1)),
			Count:      1 + i,
		})
	}
	out := &AlertsOutput{
		RobotResponse: fixedCorpusResponse(true),
		Enabled:       true,
		Active:        active,
		Summary: AlertSummaryInfo{
			TotalActive: len(active),
			BySeverity:  map[string]int{"critical": len(active) / 3, "warning": len(active) / 3, "info": len(active) - 2*(len(active)/3)},
			ByType:      map[string]int{"agent_stuck": len(active)},
		},
	}
	return out
}

func buildCorpusAgentNames(variant int) *AgentNamesOutput {
	count := 2 + variant*2
	entries := make([]AgentNameEntry, 0, count)
	nato := []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot", "Golf", "Hotel", "India", "Juliett"}
	for i := 0; i < count; i++ {
		entries = append(entries, AgentNameEntry{
			Name:      nato[i%len(nato)],
			Pane:      fmt.Sprintf("%d", i+1),
			AgentType: []string{"claude", "codex", "gemini"}[i%3],
		})
	}
	return &AgentNamesOutput{
		RobotResponse: fixedCorpusResponse(true),
		Session:       fmt.Sprintf("corpus-session-%d", variant),
		Agents:        entries,
		Count:         count,
	}
}

func buildCorpusTail(variant int) *TailOutput {
	paneCount := 2 + variant
	panes := make(map[string]PaneOutput, paneCount)
	for i := 0; i < paneCount; i++ {
		lines := make([]string, 0, 8)
		for j := 0; j < 8; j++ {
			lines = append(lines, fmt.Sprintf("[%02d:%02d] agent output line %d: running go test ./internal/robot/ (pass %d)", 12, j, j, i))
		}
		panes[fmt.Sprintf("%d", i+1)] = PaneOutput{
			Type:                  []string{"claude", "codex", "user"}[i%3],
			State:                 []string{"active", "idle"}[i%2],
			Lines:                 lines,
			ObservationState:      "observed",
			ObservationFreshness:  "fresh",
			ObservationConfidence: 0.9,
			SafeToDispatch:        i%2 == 1,
			CaptureCollectedAt:    fixedCorpusTimestamp,
			CaptureProvenance:     "live",
		}
	}
	out := &TailOutput{
		RobotResponse: fixedCorpusResponse(true),
		Session:       fmt.Sprintf("corpus-session-%d", variant),
		Panes:         panes,
	}
	return out
}

func buildCorpusErrors() []corpusEnvelope {
	cases := []struct {
		name string
		code string
		msg  string
		hint string
	}{
		{"session_not_found", ErrCodeSessionNotFound, "session 'myproject' not found", "Use 'ntm list' to see available sessions"},
		{"invalid_flag", ErrCodeInvalidFlag, "unknown robot command \"statsu\"", "Use --robot-capabilities to inspect valid commands"},
		{"dependency_missing", ErrCodeDependencyMissing, "tmux is not installed", "Install tmux to use ntm"},
		{"internal_error", ErrCodeInternalError, "unexpected state while collecting panes", "Retry; if persistent file an issue"},
		{"pane_not_found", "PANE_NOT_FOUND", "pane 7 not found in session 'proj'", "Use --robot-pane-address=proj to list panes"},
		{"timeout", "TIMEOUT", "probe timed out after 10s", "Increase --probe-timeout or check agent responsiveness"},
	}
	envelopes := make([]corpusEnvelope, 0, len(cases))
	for _, c := range cases {
		resp := NewErrorResponse(fmt.Errorf("%s", c.msg), c.code, c.hint)
		normalizeCorpusResponse(&resp)
		envelopes = append(envelopes, corpusEnvelope{Surface: "error", Name: c.name, Payload: resp})
	}
	return envelopes
}

// buildTokenCorpus assembles the full deterministic corpus:
// >=50 envelopes across >=8 surfaces.
func buildTokenCorpus(t *testing.T) []corpusEnvelope {
	t.Helper()
	var corpus []corpusEnvelope

	// 1. snapshot — 10 scale variants (also the markdown-vs-JSON measurement set).
	for v := 0; v < 10; v++ {
		corpus = append(corpus, corpusEnvelope{Surface: "snapshot", Name: fmt.Sprintf("scale%d", v), Payload: buildCorpusSnapshot(v)})
	}

	// 2. capabilities — 4 variants through the real (hermetic) builder.
	capVariants := []struct {
		name string
		opts CapabilitiesOptions
	}{
		{"full", CapabilitiesOptions{}},
		{"compact", CapabilitiesOptions{Compact: true}},
		{"category_attention", CapabilitiesOptions{Category: "attention"}},
		{"query_status", CapabilitiesOptions{Query: "status"}},
	}
	for _, cv := range capVariants {
		out, err := GetCapabilitiesWithOptions(cv.opts)
		if err != nil {
			t.Fatalf("GetCapabilitiesWithOptions(%s): %v", cv.name, err)
		}
		normalizeCorpusResponse(&out.RobotResponse)
		// The catalog's own Version field mirrors the build-info global; pin
		// it so corpus generation and the ratchet are order-independent.
		out.Version = "dev"
		corpus = append(corpus, corpusEnvelope{Surface: "capabilities", Name: cv.name, Payload: out})
	}

	// 3. terse projection — real GetTerseProjection over snapshot fixtures.
	for _, v := range []int{0, 3, 6, 9} {
		proj := GetTerseProjection(buildCorpusSnapshot(v))
		proj.Timestamp = fixedCorpusTimestamp
		corpus = append(corpus, corpusEnvelope{Surface: "terse_projection", Name: fmt.Sprintf("scale%d", v), Payload: proj})
	}

	// 4. dashboard section projection — shared benchmark fixture builder.
	dashScales := []struct {
		name                     string
		sessions, agents, events int
	}{
		{"small", 1, 4, 10},
		{"medium1", 2, 8, 30},
		{"medium2", 3, 12, 50},
		{"large1", 5, 20, 100},
		{"large2", 8, 32, 160},
		{"xlarge", 10, 40, 200},
	}
	for _, ds := range dashScales {
		proj := buildTestProjection(ds.sessions, ds.agents, ds.events)
		proj.Timestamp = fixedCorpusTimestamp
		corpus = append(corpus, corpusEnvelope{Surface: "dashboard_projection", Name: ds.name, Payload: proj})
	}

	// 5. status — 6 variants.
	for v := 0; v < 6; v++ {
		corpus = append(corpus, corpusEnvelope{Surface: "status", Name: fmt.Sprintf("scale%d", v), Payload: buildCorpusStatus(v)})
	}

	// 6. alerts — 5 variants.
	for v := 0; v < 5; v++ {
		corpus = append(corpus, corpusEnvelope{Surface: "alerts", Name: fmt.Sprintf("scale%d", v), Payload: buildCorpusAlerts(v)})
	}

	// 7. agent_names — 5 variants.
	for v := 0; v < 5; v++ {
		corpus = append(corpus, corpusEnvelope{Surface: "agent_names", Name: fmt.Sprintf("scale%d", v), Payload: buildCorpusAgentNames(v)})
	}

	// 8. error envelopes — 6 codes.
	corpus = append(corpus, buildCorpusErrors()...)

	// 9. tail — 4 variants (nested map payloads stress TOON's fallback path).
	for v := 0; v < 4; v++ {
		corpus = append(corpus, corpusEnvelope{Surface: "tail", Name: fmt.Sprintf("scale%d", v), Payload: buildCorpusTail(v)})
	}

	return corpus
}

func corpusFileName(idx int, e corpusEnvelope) string {
	return fmt.Sprintf("%s__%02d_%s.json", e.Surface, idx, e.Name)
}

// TestGenerateTokenCorpus writes the corpus fixtures. Guarded by env var so
// normal test runs never rewrite committed fixtures.
func TestGenerateTokenCorpus(t *testing.T) {
	if os.Getenv("NTM_UPDATE_TOKEN_CORPUS") != "1" {
		t.Skip("set NTM_UPDATE_TOKEN_CORPUS=1 to regenerate the token corpus")
	}
	corpus := buildTokenCorpus(t)
	if err := os.MkdirAll(tokenCorpusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Remove stale fixtures so renames don't leave orphans.
	existing, _ := filepath.Glob(filepath.Join(tokenCorpusDir, "*.json"))
	for _, f := range existing {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	for i, e := range corpus {
		rendered, err := Render(e.Payload, FormatJSON)
		if err != nil {
			t.Fatalf("render %s/%s: %v", e.Surface, e.Name, err)
		}
		path := filepath.Join(tokenCorpusDir, corpusFileName(i, e))
		if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote %d corpus envelopes to %s", len(corpus), tokenCorpusDir)
}

// TestTokenCorpusFixturesMatchCurrentStructs is the staleness ratchet for the
// committed corpus (D3, bd-ws3-contract-breadth-psvyu.3): it regenerates
// every envelope in memory from the CURRENT output structs and production
// renderer and asserts byte equality with the committed fixture. Without
// this, an envelope shape change would silently leave the fixtures — and the
// token floors measured on them — describing structs that no longer exist.
func TestTokenCorpusFixturesMatchCurrentStructs(t *testing.T) {
	const regenHint = "regenerate with NTM_UPDATE_TOKEN_CORPUS=1 go test -run TestGenerateTokenCorpus ./internal/robot/"
	corpus := buildTokenCorpus(t)
	for i, e := range corpus {
		rendered, err := Render(e.Payload, FormatJSON)
		if err != nil {
			t.Fatalf("render %s/%s: %v", e.Surface, e.Name, err)
		}
		path := filepath.Join(tokenCorpusDir, corpusFileName(i, e))
		committed, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("corpus fixture %s missing (%v); %s", path, err, regenHint)
		}
		if got := string(committed); got != rendered {
			// Show the first divergence to make the drift diagnosable.
			i := 0
			for i < len(got) && i < len(rendered) && got[i] == rendered[i] {
				i++
			}
			lo := i - 60
			if lo < 0 {
				lo = 0
			}
			snip := func(s string) string {
				hi := i + 60
				if hi > len(s) {
					hi = len(s)
				}
				if lo >= len(s) {
					return ""
				}
				return s[lo:hi]
			}
			t.Errorf("corpus fixture %s is stale relative to the current structs/renderer; %s\n  fixture: …%s…\n  current: …%s…",
				path, regenHint, snip(got), snip(rendered))
		}
	}
	// Orphaned fixtures (renamed/removed envelopes) also mean staleness.
	paths, err := filepath.Glob(filepath.Join(tokenCorpusDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != len(corpus) {
		t.Errorf("corpus dir has %d fixtures but the generator builds %d; %s", len(paths), len(corpus), regenHint)
	}
}

type corpusFile struct {
	Surface string
	Name    string
	JSON    string
}

func loadTokenCorpus(t *testing.T) []corpusFile {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(tokenCorpusDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("no corpus files in %s; run NTM_UPDATE_TOKEN_CORPUS=1 go test -run TestGenerateTokenCorpus", tokenCorpusDir)
	}
	sort.Strings(paths)
	files := make([]corpusFile, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		base := filepath.Base(p)
		surface, rest, ok := strings.Cut(base, "__")
		if !ok {
			t.Fatalf("corpus file %s does not follow surface__NN_name.json naming", base)
		}
		files = append(files, corpusFile{
			Surface: surface,
			Name:    strings.TrimSuffix(rest, ".json"),
			JSON:    string(data),
		})
	}
	return files
}

// TestTokenCorpusIntegrity keeps the corpus honest: enough envelopes, enough
// surfaces, every file a parseable envelope carrying the standard fields.
func TestTokenCorpusIntegrity(t *testing.T) {
	files := loadTokenCorpus(t)
	if len(files) < tokenCorpusMinFiles {
		t.Fatalf("corpus has %d envelopes; need >= %d", len(files), tokenCorpusMinFiles)
	}
	surfaces := map[string]int{}
	for _, f := range files {
		surfaces[f.Surface]++
		var m map[string]any
		if err := json.Unmarshal([]byte(f.JSON), &m); err != nil {
			t.Fatalf("corpus file %s__%s: invalid JSON: %v", f.Surface, f.Name, err)
		}
		// Section projections (terse/dashboard) are projection payloads, not
		// RobotResponse envelopes; they carry a timestamp but no success flag.
		isProjection := strings.HasSuffix(f.Surface, "_projection")
		if _, ok := m["success"]; !ok && !isProjection {
			t.Errorf("corpus file %s__%s: missing envelope field 'success'", f.Surface, f.Name)
		}
		if _, ok := m["timestamp"]; !ok {
			if _, ok := m["ts"]; !ok {
				t.Errorf("corpus file %s__%s: missing envelope field 'timestamp'/'ts'", f.Surface, f.Name)
			}
		}
	}
	if len(surfaces) < tokenCorpusMinSurface {
		t.Fatalf("corpus covers %d surfaces; need >= %d (%v)", len(surfaces), tokenCorpusMinSurface, surfaces)
	}
	t.Logf("corpus: %d envelopes across %d surfaces: %v", len(files), len(surfaces), surfaces)
}

// markdownFloorPercent is the pinned regression floor for the
// "--robot-markdown saves tokens vs JSON" claim, measured on the committed
// snapshot corpus with the repo's chars/token heuristic
// (tokens.EstimateTokens) and set 10 points below the measured value.
// Docs cite this test by name; keep in sync with:
//   - internal/cli/root.go (--robot-markdown help)
//   - internal/robot/robot.go (robot help text)
//   - docs/planning/AGENT_FRIENDLINESS_REPORT.md
//
// Measured 2026-08-16 on the committed 50-envelope corpus: 83.8% (default),
// 93.7% (compact).
const markdownFloorPercent = 73.8

// toonFloorPercent is the pinned regression floor for TOON-vs-JSON savings
// over the TOON-encodable subset of the corpus, set 10 points below the
// measured value. Measured 2026-08-16: 39.4% (50/50 envelopes encodable).
const toonFloorPercent = 29.4

// TestTokenCorpus_MarkdownFloor measures --robot-markdown output vs the JSON
// snapshot envelope over the committed snapshot corpus and pins the floor.
func TestTokenCorpus_MarkdownFloor(t *testing.T) {
	files := loadTokenCorpus(t)
	var jsonTokens, mdTokens, mdCompactTokens int
	measured := 0
	for _, f := range files {
		if f.Surface != "snapshot" {
			continue
		}
		var snapshot SnapshotOutput
		if err := json.Unmarshal([]byte(f.JSON), &snapshot); err != nil {
			t.Fatalf("unmarshal snapshot %s: %v", f.Name, err)
		}
		md, err := renderMarkdownFromSnapshot(&snapshot, DefaultMarkdownOptions())
		if err != nil {
			t.Fatalf("render markdown %s: %v", f.Name, err)
		}
		compactOpts := DefaultMarkdownOptions()
		compactOpts.Compact = true
		mdc, err := renderMarkdownFromSnapshot(&snapshot, compactOpts)
		if err != nil {
			t.Fatalf("render compact markdown %s: %v", f.Name, err)
		}
		jsonTokens += tokens.EstimateTokens(f.JSON)
		mdTokens += tokens.EstimateTokens(md)
		mdCompactTokens += tokens.EstimateTokens(mdc)
		measured++
	}
	if measured == 0 {
		t.Fatal("no snapshot envelopes in corpus")
	}
	savings := 100 * float64(jsonTokens-mdTokens) / float64(jsonTokens)
	compactSavings := 100 * float64(jsonTokens-mdCompactTokens) / float64(jsonTokens)
	t.Logf("markdown vs JSON over %d snapshot envelopes: json=%d md=%d md_compact=%d tokens", measured, jsonTokens, mdTokens, mdCompactTokens)
	t.Logf("measured savings: default=%.1f%% compact=%.1f%% (floor=%.0f%%)", savings, compactSavings, markdownFloorPercent)
	if savings < markdownFloorPercent {
		t.Errorf("markdown token savings %.1f%% fell below pinned floor %.0f%%; either fix the regression or re-measure and update the floor AND the doc claims (root.go, robot.go, AGENT_FRIENDLINESS_REPORT.md)", savings, markdownFloorPercent)
	}
}

// TestTokenCorpus_TOONFloor measures TOON (pure-Go encoder, production
// delimiter) vs the production JSON rendering across the whole corpus and
// pins the floor over the TOON-encodable subset.
func TestTokenCorpus_TOONFloor(t *testing.T) {
	files := loadTokenCorpus(t)
	var jsonTokens, toonTokens int
	encodable, total := 0, 0
	for _, f := range files {
		total++
		var payload any
		if err := json.Unmarshal([]byte(f.JSON), &payload); err != nil {
			t.Fatalf("unmarshal %s__%s: %v", f.Surface, f.Name, err)
		}
		toonOut, err := toonEncodePureGo(payload, "\t")
		if err != nil {
			// Production FormatAuto falls back to JSON for these shapes;
			// they contribute zero savings and are excluded from the ratio.
			continue
		}
		encodable++
		jsonTokens += tokens.EstimateTokens(f.JSON)
		toonTokens += tokens.EstimateTokens(toonOut)
	}
	if encodable == 0 {
		t.Fatal("no TOON-encodable envelopes in corpus")
	}
	savings := 100 * float64(jsonTokens-toonTokens) / float64(jsonTokens)
	t.Logf("TOON vs JSON: %d/%d envelopes TOON-encodable; json=%d toon=%d tokens", encodable, total, jsonTokens, toonTokens)
	t.Logf("measured savings on encodable subset: %.1f%% (floor=%.0f%%)", savings, toonFloorPercent)
	if savings < toonFloorPercent {
		t.Errorf("TOON token savings %.1f%% fell below pinned floor %.0f%%; either fix the regression or re-measure and update the floor and docs", savings, toonFloorPercent)
	}
}
