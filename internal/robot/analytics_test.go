package robot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
)

func TestAnalyticsMultiHarnessAggregationAndSessions(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	now := time.Now().UTC()
	fixtureEvents := []events.Event{
		{
			Timestamp: now.Add(-48 * time.Hour),
			Type:      events.EventSessionCreate,
			Session:   "alpha",
			Data: events.ToMap(events.SessionCreateData{
				ClaudeCount:      2,
				CodexCount:       1,
				AntigravityCount: 2,
				OpencodeCount:    1,
				GrokCount:        1,
				OmpCount:         1,
				OllamaCount:      1,
			}),
		},
		{
			Timestamp: now.Add(-24 * time.Hour),
			Type:      events.EventSessionCreate,
			Session:   "beta",
			Data: map[string]interface{}{
				"agy_count":      float64(1),
				"oc_count":       float64(2),
				"gemini_count":   float64(1),
				"cursor_count":   float64(1),
				"windsurf_count": float64(1),
				"aider_count":    float64(1),
			},
		},
		{
			Timestamp: now.Add(-23 * time.Hour),
			Type:      events.EventAgentSpawn,
			Session:   "beta",
			Data:      map[string]interface{}{"agent_type": "antigravity"},
		},
		{
			Timestamp: now.Add(-12 * time.Hour),
			Type:      events.EventAgentAdd,
			Session:   "alpha",
			Data:      map[string]interface{}{"agent_type": "agy"},
		},
		{
			Timestamp: now.Add(-11 * time.Hour),
			Type:      events.EventAgentAdd,
			Session:   "beta",
			Data:      map[string]interface{}{"agent_type": "opencode"},
		},
		{
			Timestamp: now.Add(-6 * time.Hour),
			Type:      events.EventPromptSend,
			Session:   "alpha",
			Data: map[string]interface{}{
				"prompt_length":    float64(700),
				"estimated_tokens": float64(200),
				"target_types":     "agy,oc,cc,cod",
			},
		},
		{
			Timestamp: now.Add(-5 * time.Hour),
			Type:      events.EventPromptSend,
			Session:   "beta",
			Data: map[string]interface{}{
				"prompt_length": float64(350),
				"target_types":  "grok,omp,ollama",
			},
		},
		{
			Timestamp: now.Add(-3 * time.Hour),
			Type:      events.EventError,
			Session:   "alpha",
			Data:      map[string]interface{}{"error_type": "rate_limit"},
		},
		{
			Timestamp: now.Add(-2 * time.Hour),
			Type:      events.EventError,
			Session:   "beta",
			Data:      map[string]interface{}{"error_type": "spawn_failed"},
		},
		{
			Timestamp: now.Add(-90 * time.Minute),
			Type:      events.EventError,
			Session:   "alpha",
			Data:      map[string]interface{}{"error_type": "rate_limit"},
		},
		{
			Timestamp: now.Add(-60 * time.Minute),
			Type:      events.EventAgentCrash,
			Session:   "alpha",
			Data:      events.ToMap(events.AgentLifecycleData{PaneID: "%1", AgentType: "antigravity", Reason: "process_exited"}),
		},
		{
			Timestamp: now.Add(-59 * time.Minute),
			Type:      events.EventAgentRestart,
			Session:   "alpha",
			Data:      events.ToMap(events.AgentLifecycleData{PaneID: "%1", AgentType: "antigravity", RestartCount: 1}),
		},
		{
			Timestamp: now.Add(-30 * time.Minute),
			Type:      events.EventAgentCrash,
			Session:   "beta",
			Data:      events.ToMap(events.AgentLifecycleData{PaneID: "%2", AgentType: "oc", Reason: "hung"}),
		},
	}

	var buf []byte
	for _, ev := range fixtureEvents {
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(logPath, buf, 0o644); err != nil {
		t.Fatalf("write events log: %v", err)
	}

	// Verify in-memory int counts via ToMap also aggregate identically before JSON round-trip.
	inMem := AggregateAnalyticsEvents(fixtureEvents[:1], 7, "", now.AddDate(0, 0, -7))
	if inMem.TotalAgents != 9 || inMem.AgentBreakdown["antigravity"].Count != 2 || inMem.AgentBreakdown["oc"].Count != 1 {
		t.Fatalf("in-memory ToMap int counts failed: total=%d breakdown=%+v", inMem.TotalAgents, inMem.AgentBreakdown)
	}

	out, err := GetAnalytics(context.Background(), AnalyticsOptions{
		Days:     7,
		Sessions: true,
		LogPath:  logPath,
	})
	if err != nil {
		t.Fatalf("GetAnalytics returned error: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected Success=true, got error=%q code=%q", out.Error, out.ErrorCode)
	}
	if out.SchemaID != "ntm:robot:analytics:v1" || out.SchemaVersion != "1.0.0" {
		t.Fatalf("unexpected schema identity: id=%q version=%q", out.SchemaID, out.SchemaVersion)
	}
	if out.Days != 7 || out.Period != "Last 7 days" || out.Since == "" {
		t.Fatalf("unexpected window metadata: days=%d period=%q since=%q", out.Days, out.Period, out.Since)
	}
	if out.TotalSessions != 2 {
		t.Fatalf("TotalSessions = %d, want 2", out.TotalSessions)
	}
	// alpha: 2+1+2+1+1+1+1 = 9 spawned + 1 agy added = 10
	// beta: 1+2+1+1+1+1 = 7 spawned + 1 opencode added = 8 -> total 18
	if out.TotalAgents != 18 {
		t.Fatalf("TotalAgents = %d, want 18", out.TotalAgents)
	}
	if out.TotalPrompts != 2 || out.TotalCharsSent != 1050 || out.TotalTokensEst != 300 {
		t.Fatalf("prompt totals = prompts:%d chars:%d tokens:%d, want 2/1050/300",
			out.TotalPrompts, out.TotalCharsSent, out.TotalTokensEst)
	}
	if out.ErrorCount != 3 || out.ErrorTypes["rate_limit"] != 2 || out.ErrorTypes["spawn_failed"] != 1 {
		t.Fatalf("error totals = count:%d types:%+v", out.ErrorCount, out.ErrorTypes)
	}
	if out.AgentCrashes != 2 || out.AgentRestarts != 1 {
		t.Fatalf("lifecycle totals = crashes:%d restarts:%d, want 2/1", out.AgentCrashes, out.AgentRestarts)
	}

	wantBreakdown := map[string]AnalyticsAgentStats{
		"antigravity": {Count: 4, Prompts: 1, CharsSent: 175, TokensEst: 50},
		"oc":          {Count: 4, Prompts: 1, CharsSent: 175, TokensEst: 50},
		"claude":      {Count: 2, Prompts: 1, CharsSent: 175, TokensEst: 50},
		"codex":       {Count: 1, Prompts: 1, CharsSent: 175, TokensEst: 50},
		"grok":        {Count: 1, Prompts: 1, CharsSent: 118, TokensEst: 33},
		"omp":         {Count: 1, Prompts: 1, CharsSent: 116, TokensEst: 33},
		"ollama":      {Count: 1, Prompts: 1, CharsSent: 116, TokensEst: 33},
		"gemini":      {Count: 1},
		"cursor":      {Count: 1},
		"windsurf":    {Count: 1},
		"aider":       {Count: 1},
	}
	for harness, want := range wantBreakdown {
		got, ok := out.AgentBreakdown[harness]
		if !ok {
			t.Errorf("missing harness %q in AgentBreakdown", harness)
			continue
		}
		if got != want {
			t.Errorf("AgentBreakdown[%q] = %+v, want %+v", harness, got, want)
		}
	}

	if len(out.Sessions) != 2 {
		t.Fatalf("len(Sessions) = %d, want 2", len(out.Sessions))
	}
	if out.Sessions[0].Name != "beta" || out.Sessions[0].AgentCount != 8 || out.Sessions[0].PromptCount != 1 {
		t.Fatalf("Sessions[0] = %+v, want beta/8/1", out.Sessions[0])
	}
	if out.Sessions[1].Name != "alpha" || out.Sessions[1].AgentCount != 10 || out.Sessions[1].PromptCount != 1 {
		t.Fatalf("Sessions[1] = %+v, want alpha/10/1", out.Sessions[1])
	}

	promBaseline := FormatAnalyticsPrometheus(out)
	for _, metric := range []string{
		`ntm_sessions_total{period="Last 7 days"} 2`,
		`ntm_agents_total{period="Last 7 days"} 18`,
		`ntm_prompts_total{period="Last 7 days"} 2`,
		`ntm_chars_sent_total{period="Last 7 days"} 1050`,
		`ntm_tokens_estimated_total{period="Last 7 days"} 300`,
		`ntm_errors_total{period="Last 7 days"} 3`,
		`ntm_agent_crashes_total{period="Last 7 days"} 2`,
		`ntm_agent_restarts_total{period="Last 7 days"} 1`,
		`ntm_agent_spawns{period="Last 7 days",agent_type="antigravity"} 4`,
		`ntm_agent_spawns{period="Last 7 days",agent_type="oc"} 4`,
		`ntm_agent_prompts{period="Last 7 days",agent_type="antigravity"} 1`,
		`ntm_agent_chars_sent{period="Last 7 days",agent_type="oc"} 175`,
		`ntm_agent_tokens_estimated{period="Last 7 days",agent_type="antigravity"} 50`,
		`ntm_errors_by_type{period="Last 7 days",error_type="rate_limit"} 2`,
		`ntm_errors_by_type{period="Last 7 days",error_type="spawn_failed"} 1`,
	} {
		if !strings.Contains(promBaseline, metric) {
			t.Errorf("Prometheus output missing %q:\n%s", metric, promBaseline)
		}
	}
	for i := 0; i < 20; i++ {
		if got := FormatAnalyticsPrometheus(out); got != promBaseline {
			t.Fatalf("FormatAnalyticsPrometheus is non-deterministic on iteration %d", i)
		}
	}
}

func TestAnalyticsValidationAndMissingLog(t *testing.T) {
	// 1. Canceled context -> ErrCodeTimeout
	canceledCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	timeoutOut, err := GetAnalytics(canceledCtx, AnalyticsOptions{})
	if err != nil {
		t.Fatalf("unexpected Go error on canceled ctx: %v", err)
	}
	if timeoutOut.Success || timeoutOut.ErrorCode != ErrCodeTimeout {
		t.Fatalf("expected ErrCodeTimeout, got %+v", timeoutOut)
	}

	// 2. Negative Days -> ErrCodeInvalidFlag
	negOut, err := GetAnalytics(context.Background(), AnalyticsOptions{Days: -3, Sessions: true})
	if err != nil {
		t.Fatalf("unexpected Go error on negative Days: %v", err)
	}
	if negOut.Success || negOut.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected ErrCodeInvalidFlag for negative Days, got %+v", negOut)
	}
	if negOut.AgentBreakdown == nil || negOut.ErrorTypes == nil || negOut.Sessions == nil {
		t.Fatalf("expected non-nil empty collections on error, got %+v", negOut)
	}

	// 3. Invalid --analytics-since -> ErrCodeInvalidFlag
	badSince, err := GetAnalytics(context.Background(), AnalyticsOptions{Since: "not-a-timestamp"})
	if err != nil {
		t.Fatalf("unexpected Go error on invalid Since: %v", err)
	}
	if badSince.Success || badSince.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected ErrCodeInvalidFlag for invalid Since, got %+v", badSince)
	}

	// 4. Missing log file with default Days -> success: true, empty non-nil maps
	missingPath := filepath.Join(t.TempDir(), "missing", "events.jsonl")
	missingDays, err := GetAnalytics(context.Background(), AnalyticsOptions{
		LogPath:  missingPath,
		Sessions: true,
	})
	if err != nil {
		t.Fatalf("unexpected Go error on missing log: %v", err)
	}
	if !missingDays.Success {
		t.Fatalf("expected Success=true for missing log, got %+v", missingDays)
	}
	if missingDays.Period != "Last 30 days (no events)" || missingDays.Days != 30 {
		t.Fatalf("unexpected Period/Days for missing log: period=%q days=%d", missingDays.Period, missingDays.Days)
	}
	if missingDays.AgentBreakdown == nil || len(missingDays.AgentBreakdown) != 0 {
		t.Fatalf("expected non-nil empty AgentBreakdown, got %#v", missingDays.AgentBreakdown)
	}
	if missingDays.ErrorTypes == nil || len(missingDays.ErrorTypes) != 0 {
		t.Fatalf("expected non-nil empty ErrorTypes, got %#v", missingDays.ErrorTypes)
	}
	if missingDays.Sessions == nil || len(missingDays.Sessions) != 0 {
		t.Fatalf("expected non-nil empty Sessions when Sessions=true, got %#v", missingDays.Sessions)
	}
	if missingDays.AgentHints == nil || len(missingDays.AgentHints.SuggestedActions) == 0 {
		t.Fatalf("expected helpful AgentHints for missing log, got %+v", missingDays.AgentHints)
	}

	// 5. Missing log file with RFC3339 Since -> "Since ... (no events)"
	missingSince, err := GetAnalytics(context.Background(), AnalyticsOptions{
		Since:   "2026-01-15T00:00:00Z",
		LogPath: missingPath,
	})
	if err != nil {
		t.Fatalf("unexpected Go error on missing log with Since: %v", err)
	}
	if !missingSince.Success || missingSince.Period != "Since 2026-01-15T00:00:00Z (no events)" || missingSince.Days != 0 {
		t.Fatalf("unexpected RFC3339 Since missing-log output: %+v", missingSince)
	}
	if missingSince.Since != "2026-01-15T00:00:00Z" {
		t.Fatalf("Since = %q, want 2026-01-15T00:00:00Z", missingSince.Since)
	}
}

func TestAnalyticsRegistryAndSchemaContract(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "events.jsonl")

	prevFormat := GetOutputFormat()
	SetOutputFormat(FormatJSON)
	t.Cleanup(func() { SetOutputFormat(prevFormat) })

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printErr := PrintAnalytics(context.Background(), AnalyticsOptions{
		Days:    14,
		LogPath: missingPath,
	})
	_ = w.Close()
	os.Stdout = origStdout
	if printErr != nil {
		t.Fatalf("PrintAnalytics returned error: %v", printErr)
	}

	var rawMap map[string]interface{}
	if err := json.NewDecoder(r).Decode(&rawMap); err != nil {
		t.Fatalf("decode PrintAnalytics stdout: %v", err)
	}
	_ = r.Close()

	if rawMap["success"] != true || rawMap["schema_id"] != "ntm:robot:analytics:v1" {
		t.Fatalf("unexpected PrintAnalytics JSON envelope: %+v", rawMap)
	}
	if _, ok := rawMap["agent_breakdown"].(map[string]interface{}); !ok {
		t.Fatalf("expected JSON object for agent_breakdown (not null), got %T (%v)", rawMap["agent_breakdown"], rawMap["agent_breakdown"])
	}
	if _, ok := rawMap["error_types"].(map[string]interface{}); !ok {
		t.Fatalf("expected JSON object for error_types (not null), got %T (%v)", rawMap["error_types"], rawMap["error_types"])
	}

	surface, ok := GetRobotRegistry().Surface("analytics")
	if !ok {
		t.Fatal("expected 'analytics' surface in GetRobotRegistry()")
	}
	if surface.SchemaID != "ntm:robot:analytics:v1" || surface.SchemaType != "analytics" || surface.Flag != "--robot-analytics" {
		t.Fatalf("unexpected analytics surface descriptor: %+v", surface)
	}

	schemaOut, err := GetSchema("analytics")
	if err != nil {
		t.Fatalf("GetSchema(analytics) failed: %v", err)
	}
	if !schemaOut.Success || schemaOut.Schema == nil {
		t.Fatalf("expected valid JSON schema for analytics, got %+v", schemaOut)
	}
	for _, prop := range []string{
		"schema_id",
		"schema_version",
		"period",
		"since",
		"total_sessions",
		"total_agents",
		"total_prompts",
		"total_chars_sent",
		"total_tokens_estimated",
		"agent_breakdown",
		"error_count",
		"error_types",
		"agent_crashes",
		"agent_restarts",
	} {
		if _, exists := schemaOut.Schema.Properties[prop]; !exists {
			t.Fatalf("expected property %q in analytics JSON schema, got %v", prop, schemaOut.Schema.Properties)
		}
	}

	if violations := SchemaPaginationViolations(); len(violations) != 0 {
		t.Fatalf("SchemaPaginationViolations: %v", violations)
	}
}
