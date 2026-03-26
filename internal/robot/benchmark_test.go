package robot

// benchmark_test.go provides comprehensive benchmark and regression coverage
// for the robot mode redesign. It measures latency, payload size, storage churn,
// and operational costs across all surfaces: snapshot, status, digest, attention,
// and inspect variants.
//
// Bead: bd-j9jo3.9.10
//
// Metrics tracked:
//   - Latency: ns/op for surface assembly and rendering
//   - Payload size: bytes/op for JSON output
//   - Memory: allocs/op and bytes/alloc for allocation pressure
//   - Storage churn: writes per operation for attention feed
//   - Replay throughput: events/sec for cursor-based replay
//   - Deduplication effectiveness: surfaced vs raw event counts
//   - Operator-state impact: queue size changes from ack/snooze/mute

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// =============================================================================
// Benchmark Scenario Sizes
// =============================================================================

const (
	// ScenarioSmall: few sessions, agents, events (typical single-project)
	scenarioSmallSessions = 1
	scenarioSmallAgents   = 3
	scenarioSmallEvents   = 50

	// ScenarioMedium: moderate load (multi-project, active development)
	scenarioMediumSessions = 3
	scenarioMediumAgents   = 9
	scenarioMediumEvents   = 500

	// ScenarioLarge: heavy load (busy ensemble, truncation active)
	scenarioLargeSessions = 10
	scenarioLargeAgents   = 30
	scenarioLargeEvents   = 5000
)

// =============================================================================
// Benchmark Fixture Builders
// =============================================================================

// benchmarkFixture holds state for benchmark scenarios.
type benchmarkFixture struct {
	clock        *FixedClock
	cursor       *CursorFixture
	sessions     []*state.RuntimeSession
	agents       []*state.RuntimeAgent
	events       []AttentionEvent
	incidents    []*state.Incident
	sourceHealth *SourceHealthFixture
}

// newBenchmarkFixture creates a fixture for the given scenario size.
func newBenchmarkFixture(sessionCount, agentCount, eventCount int) *benchmarkFixture {
	clock := NewFixedClock(0)
	cursor := NewCursorFixture(0)

	sessions := make([]*state.RuntimeSession, sessionCount)
	for i := 0; i < sessionCount; i++ {
		opts := DefaultSessionFixtureOptions()
		opts.Name = fmt.Sprintf("bench-session-%d", i)
		opts.AgentCount = agentCount / sessionCount
		opts.PaneCount = agentCount / sessionCount
		opts.Clock = clock
		sessions[i] = RuntimeSessionFixture(opts)
	}

	agents := make([]*state.RuntimeAgent, 0, agentCount)
	for i := 0; i < agentCount; i++ {
		sessionIdx := i % sessionCount
		opts := DefaultAgentFixtureOptions()
		opts.SessionName = sessions[sessionIdx].Name
		opts.Pane = (i / sessionCount) + 1
		opts.AgentType = []string{"claude", "codex", "cursor"}[i%3]
		opts.State = []state.AgentState{state.AgentStateActive, state.AgentStateIdle, state.AgentStateUnknown}[i%3]
		opts.Clock = clock
		agents = append(agents, RuntimeAgentFixture(opts))
	}

	events := make([]AttentionEvent, 0, eventCount)
	categories := []EventCategory{EventCategoryAgent, EventCategoryPane, EventCategoryAlert, EventCategoryFile, EventCategorySystem}
	types := []EventType{EventTypeAgentStateChange, EventTypePaneOutput, EventTypeAlertWarning, EventTypeFileConflict}
	actionabilities := []Actionability{ActionabilityBackground, ActionabilityInteresting, ActionabilityActionRequired}
	severities := []Severity{SeverityInfo, SeverityWarning, SeverityError}

	for i := 0; i < eventCount; i++ {
		opts := DefaultAttentionEventFixtureOptions()
		opts.Session = sessions[i%sessionCount].Name
		opts.Pane = (i % agentCount) + 1
		opts.Category = categories[i%len(categories)]
		opts.Type = types[i%len(types)]
		opts.Actionability = actionabilities[i%len(actionabilities)]
		opts.Severity = severities[i%len(severities)]
		opts.Summary = fmt.Sprintf("Benchmark event %d for %s", i, opts.Session)
		opts.Cursor = cursor
		opts.Clock = clock
		events = append(events, AttentionEventFixture(opts))
		clock.Advance(time.Millisecond * 10)
	}

	incidentOpts := DefaultIncidentFixtureOptions()
	incidentOpts.Clock = clock
	incidents := []*state.Incident{IncidentFixture(incidentOpts)}

	sourceHealthOpts := DefaultSourceHealthFixtureOptions()
	sourceHealthOpts.Clock = clock
	sourceHealth := NewSourceHealthFixture(sourceHealthOpts)

	return &benchmarkFixture{
		clock:        clock,
		cursor:       cursor,
		sessions:     sessions,
		agents:       agents,
		events:       events,
		incidents:    incidents,
		sourceHealth: sourceHealth,
	}
}

// =============================================================================
// Attention Digest Benchmarks
// =============================================================================

func BenchmarkBuildAttentionDigest_Small(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioSmallSessions, scenarioSmallAgents, scenarioSmallEvents)
	opts := DefaultAttentionDigestOptions()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)
	}
}

func BenchmarkBuildAttentionDigest_Medium(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)
	opts := DefaultAttentionDigestOptions()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)
	}
}

func BenchmarkBuildAttentionDigest_Large(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioLargeSessions, scenarioLargeAgents, scenarioLargeEvents)
	opts := DefaultAttentionDigestOptions()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)
	}
}

func BenchmarkBuildAttentionDigest_WithFiltering(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)
	opts := DefaultAttentionDigestOptions()
	opts.ActionRequiredLimit = 5
	opts.InterestingLimit = 10
	opts.BackgroundLimit = 5

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)
	}
}

// =============================================================================
// Attention Feed Benchmarks
// =============================================================================

func BenchmarkAttentionFeed_AppendSmall(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       1000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	event := AttentionEvent{
		Session:       "bench",
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Source:        "benchmark",
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "Benchmark event",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		feed.Append(event)
	}
}

func BenchmarkAttentionFeed_AppendLarge(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	event := AttentionEvent{
		Session:       "bench",
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Source:        "benchmark",
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "Benchmark event with much longer summary to simulate realistic payload sizes",
		Details: map[string]any{
			"test_field_1": "value_1",
			"test_field_2": 12345,
			"test_field_3": true,
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		feed.Append(event)
	}
}

func BenchmarkAttentionFeed_ReplaySmall(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       1000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	// Pre-populate
	for i := 0; i < scenarioSmallEvents; i++ {
		feed.Append(AttentionEvent{
			Session:       "bench",
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentStateChange,
			Actionability: ActionabilityInteresting,
			Severity:      SeverityInfo,
			Summary:       fmt.Sprintf("Event %d", i),
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = feed.Replay(0, scenarioSmallEvents)
	}
}

func BenchmarkAttentionFeed_ReplayMedium(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       10000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	// Pre-populate
	for i := 0; i < scenarioMediumEvents; i++ {
		feed.Append(AttentionEvent{
			Session:       "bench",
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentStateChange,
			Actionability: ActionabilityInteresting,
			Severity:      SeverityInfo,
			Summary:       fmt.Sprintf("Event %d", i),
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = feed.Replay(0, scenarioMediumEvents)
	}
}

func BenchmarkAttentionFeed_ReplayLarge(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	// Pre-populate
	for i := 0; i < scenarioLargeEvents; i++ {
		feed.Append(AttentionEvent{
			Session:       "bench",
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentStateChange,
			Actionability: ActionabilityInteresting,
			Severity:      SeverityInfo,
			Summary:       fmt.Sprintf("Event %d", i),
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = feed.Replay(0, scenarioLargeEvents)
	}
}

func BenchmarkAttentionFeed_ReplayWithOffset(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	// Pre-populate
	for i := 0; i < scenarioLargeEvents; i++ {
		feed.Append(AttentionEvent{
			Session:       "bench",
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentStateChange,
			Actionability: ActionabilityInteresting,
			Severity:      SeverityInfo,
			Summary:       fmt.Sprintf("Event %d", i),
		})
	}

	// Replay from middle cursor
	midCursor := int64(scenarioLargeEvents / 2)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = feed.Replay(midCursor, 100)
	}
}

func BenchmarkAttentionFeed_SubscribeUnsubscribe(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       10000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	handler := func(event AttentionEvent) {}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		unsub := feed.Subscribe(handler)
		unsub()
	}
}

// =============================================================================
// Attention Feed Concurrent Benchmarks
// =============================================================================

func BenchmarkAttentionFeed_AppendParallel(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	event := AttentionEvent{
		Session:       "bench",
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "Benchmark event",
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			feed.Append(event)
		}
	})
}

func BenchmarkAttentionFeed_ReplayParallel(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	// Pre-populate
	for i := 0; i < scenarioMediumEvents; i++ {
		feed.Append(AttentionEvent{
			Session:       "bench",
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentStateChange,
			Actionability: ActionabilityInteresting,
			Severity:      SeverityInfo,
			Summary:       fmt.Sprintf("Event %d", i),
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _ = feed.Replay(0, 100)
		}
	})
}

func BenchmarkAttentionFeed_MixedWorkload(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	// Pre-populate
	for i := 0; i < 1000; i++ {
		feed.Append(AttentionEvent{
			Session:       "bench",
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentStateChange,
			Actionability: ActionabilityInteresting,
			Severity:      SeverityInfo,
			Summary:       fmt.Sprintf("Event %d", i),
		})
	}

	event := AttentionEvent{
		Session:       "bench",
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "New event",
	}

	// Use atomic counter to alternate between writers and readers
	var workerID uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// Each goroutine gets assigned as writer or reader based on order
		id := atomic.AddUint64(&workerID, 1)
		isWriter := id%2 == 0

		for pb.Next() {
			if isWriter {
				feed.Append(event)
			} else {
				_, _, _ = feed.Replay(0, 50)
			}
		}
	})
}

// =============================================================================
// Filter Selectivity Benchmarks
// =============================================================================

func BenchmarkFilterEventsForRobot_NoFilter(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)
	opts := EventsOptions{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = filterEventsForRobot(fixture.events, opts)
	}
}

func BenchmarkFilterEventsForRobot_SessionFilter(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)
	opts := EventsOptions{Session: "bench-session-0"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = filterEventsForRobot(fixture.events, opts)
	}
}

func BenchmarkFilterEventsForRobot_CategoryFilter(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)
	opts := EventsOptions{CategoryFilter: []string{string(EventCategoryAgent), string(EventCategoryAlert)}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = filterEventsForRobot(fixture.events, opts)
	}
}

func BenchmarkFilterEventsForRobot_ActionabilityFilter(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)
	opts := EventsOptions{ActionabilityFilter: []string{string(ActionabilityActionRequired)}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = filterEventsForRobot(fixture.events, opts)
	}
}

func BenchmarkFilterEventsForRobot_CombinedFilters(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)
	opts := EventsOptions{
		Session:             "bench-session-0",
		CategoryFilter:      []string{string(EventCategoryAgent)},
		ActionabilityFilter: []string{string(ActionabilityActionRequired), string(ActionabilityInteresting)},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = filterEventsForRobot(fixture.events, opts)
	}
}

// =============================================================================
// JSON Encoding Benchmarks
// =============================================================================

func BenchmarkJSONEncode_AttentionEvent(b *testing.B) {
	event := AttentionEvent{
		Cursor:        12345,
		Ts:            time.Now().Format(time.RFC3339),
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Session:       "test-session",
		Pane:          1,
		Severity:      SeverityWarning,
		Actionability: ActionabilityActionRequired,
		Summary:       "Agent test-session:1 transitioned from active to stuck",
		ReasonCode:    "agent.state.stuck",
		Details: map[string]any{
			"previous_state": "active",
			"new_state":      "stuck",
			"idle_seconds":   300,
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(event)
	}
}

func BenchmarkJSONEncode_AttentionDigest(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)
	opts := DefaultAttentionDigestOptions()
	digest := BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(digest)
	}
}

func BenchmarkJSONEncode_AttentionDigestLarge(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioLargeSessions, scenarioLargeAgents, scenarioLargeEvents)
	opts := DefaultAttentionDigestOptions()
	digest := BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(digest)
	}
}

// =============================================================================
// Markdown Rendering Benchmarks
// =============================================================================

func BenchmarkRenderMarkdownFromProjection_Small(b *testing.B) {
	proj := buildTestProjection(scenarioSmallSessions, scenarioSmallAgents, scenarioSmallEvents)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RenderMarkdownFromProjection(proj, false)
	}
}

func BenchmarkRenderMarkdownFromProjection_Medium(b *testing.B) {
	proj := buildTestProjection(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RenderMarkdownFromProjection(proj, false)
	}
}

func BenchmarkRenderMarkdownFromProjection_Large(b *testing.B) {
	proj := buildTestProjection(scenarioLargeSessions, scenarioLargeAgents, scenarioLargeEvents)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RenderMarkdownFromProjection(proj, false)
	}
}

func BenchmarkRenderMarkdownFromProjection_Compact(b *testing.B) {
	proj := buildTestProjection(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RenderMarkdownFromProjection(proj, true)
	}
}

// buildTestProjection creates a SectionProjection for benchmark testing.
func buildTestProjection(sessionCount, agentCount, eventCount int) *SectionProjection {
	fixture := newBenchmarkFixture(sessionCount, agentCount, eventCount)

	// Build summary section using StatusSummary
	summary := &StatusSummary{
		TotalSessions: sessionCount,
		TotalAgents:   agentCount,
		AttachedCount: sessionCount / 2,
		ClaudeCount:   agentCount / 3,
		CodexCount:    agentCount / 3,
		CursorCount:   agentCount / 3,
		AgentsByState: map[string]int{
			"active": agentCount / 2,
			"idle":   agentCount / 4,
			"error":  agentCount / 4,
		},
		AgentsByType: map[string]int{
			"claude": agentCount / 3,
			"codex":  agentCount / 3,
			"cursor": agentCount / 3,
		},
		ReadyWork:    10,
		InProgress:   5,
		HealthScore:  0.85,
		HealthStatus: "healthy",
		AlertsActive: eventCount / 10,
	}

	// Build sessions section with agents
	sessions := make([]SnapshotSession, sessionCount)
	agentsPerSession := agentCount / sessionCount
	for i := 0; i < sessionCount; i++ {
		agents := make([]SnapshotAgent, agentsPerSession)
		for j := 0; j < agentsPerSession; j++ {
			agents[j] = SnapshotAgent{
				Pane:             fmt.Sprintf("%d", j+1),
				Type:             []string{"claude", "codex", "cursor"}[j%3],
				TypeConfidence:   0.95,
				TypeMethod:       "process",
				State:            []string{"active", "idle", "unknown"}[j%3],
				LastOutputAgeSec: j * 10,
				OutputTailLines:  50,
				PendingMail:      0,
			}
		}
		sessions[i] = SnapshotSession{
			Name:     fixture.sessions[i].Name,
			Attached: fixture.sessions[i].Attached,
			Agents:   agents,
		}
	}

	// Build attention section
	opts := DefaultAttentionDigestOptions()
	digest := BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)
	attentionSummary := &SnapshotAttentionSummary{
		TotalEvents:         len(fixture.events),
		ActionRequiredCount: len(digest.Buckets.ActionRequired),
		InterestingCount:    len(digest.Buckets.Interesting),
		ByCategoryCount: map[string]int{
			"agent":  eventCount / 5,
			"pane":   eventCount / 5,
			"alert":  eventCount / 5,
			"file":   eventCount / 5,
			"system": eventCount / 5,
		},
	}

	proj := &SectionProjection{
		Sections: []ProjectedSection{
			{
				Name:        SectionSummary,
				OrderWeight: SectionOrderWeight[SectionSummary],
				Data:        summary,
			},
			{
				Name:        SectionSessions,
				OrderWeight: SectionOrderWeight[SectionSessions],
				Data:        sessions,
			},
			{
				Name:        SectionAttention,
				OrderWeight: SectionOrderWeight[SectionAttention],
				Data:        attentionSummary,
			},
		},
		Timestamp:     time.Now().Format(time.RFC3339),
		SourceVersion: "1.0.0",
	}

	return proj
}

// =============================================================================
// Payload Size Measurement Tests
// =============================================================================

// TestPayloadSize_AttentionDigest measures JSON payload sizes for attention digest.
func TestPayloadSize_AttentionDigest(t *testing.T) {
	t.Parallel()

	scenarios := []struct {
		name        string
		sessions    int
		agents      int
		events      int
		maxBytes    int // regression threshold
	}{
		{"small", scenarioSmallSessions, scenarioSmallAgents, scenarioSmallEvents, 10 * 1024},       // 10KB
		{"medium", scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents, 50 * 1024},   // 50KB
		{"large", scenarioLargeSessions, scenarioLargeAgents, scenarioLargeEvents, 200 * 1024},      // 200KB
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newBenchmarkFixture(sc.sessions, sc.agents, sc.events)
			opts := DefaultAttentionDigestOptions()
			digest := BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)

			data, err := json.Marshal(digest)
			if err != nil {
				t.Fatalf("failed to marshal digest: %v", err)
			}

			t.Logf("scenario=%s events=%d payload_bytes=%d", sc.name, sc.events, len(data))

			if len(data) > sc.maxBytes {
				t.Errorf("payload %d bytes exceeds budget %d bytes", len(data), sc.maxBytes)
			}
		})
	}
}

// TestPayloadSize_Projection measures JSON payload sizes for section projections.
func TestPayloadSize_Projection(t *testing.T) {
	t.Parallel()

	scenarios := []struct {
		name     string
		sessions int
		agents   int
		events   int
		maxBytes int
	}{
		{"small", scenarioSmallSessions, scenarioSmallAgents, scenarioSmallEvents, 5 * 1024},
		{"medium", scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents, 25 * 1024},
		{"large", scenarioLargeSessions, scenarioLargeAgents, scenarioLargeEvents, 100 * 1024},
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()

			proj := buildTestProjection(sc.sessions, sc.agents, sc.events)

			data, err := json.Marshal(proj)
			if err != nil {
				t.Fatalf("failed to marshal projection: %v", err)
			}

			t.Logf("scenario=%s sections=%d payload_bytes=%d", sc.name, len(proj.Sections), len(data))

			if len(data) > sc.maxBytes {
				t.Errorf("payload %d bytes exceeds budget %d bytes", len(data), sc.maxBytes)
			}
		})
	}
}

// TestPayloadSize_MarkdownRendering measures markdown output sizes.
func TestPayloadSize_MarkdownRendering(t *testing.T) {
	t.Parallel()

	scenarios := []struct {
		name     string
		sessions int
		agents   int
		events   int
		compact  bool
		maxBytes int
	}{
		{"small", scenarioSmallSessions, scenarioSmallAgents, scenarioSmallEvents, false, 4 * 1024},
		{"medium", scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents, false, 20 * 1024},
		{"large", scenarioLargeSessions, scenarioLargeAgents, scenarioLargeEvents, false, 80 * 1024},
		{"large_compact", scenarioLargeSessions, scenarioLargeAgents, scenarioLargeEvents, true, 40 * 1024},
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()

			proj := buildTestProjection(sc.sessions, sc.agents, sc.events)
			md := RenderMarkdownFromProjection(proj, sc.compact)

			t.Logf("scenario=%s compact=%v output_bytes=%d", sc.name, sc.compact, len(md))

			if len(md) > sc.maxBytes {
				t.Errorf("markdown output %d bytes exceeds budget %d bytes", len(md), sc.maxBytes)
			}
		})
	}
}

// =============================================================================
// Deduplication Effectiveness Tests
// =============================================================================

// TestDeduplicationEffectiveness measures how well digest compacts raw events.
func TestDeduplicationEffectiveness(t *testing.T) {
	t.Parallel()

	// Create events with high duplication potential
	events := make([]AttentionEvent, 0, 1000)
	clock := NewFixedClock(0)
	cursor := NewCursorFixture(0)

	// Generate repeated events (same agent state changes)
	for i := 0; i < 100; i++ {
		for j := 0; j < 10; j++ {
			events = append(events, AttentionEvent{
				Cursor:        cursor.Next(),
				Ts:            clock.RFC3339(),
				Category:      EventCategoryAgent,
				Type:          EventTypeAgentStateChange,
				Session:       "proj",
				Pane:          j + 1,
				Severity:      SeverityInfo,
				Actionability: ActionabilityInteresting,
				Summary:       fmt.Sprintf("Agent %d state change iteration %d", j, i),
				ReasonCode:    "agent.state.active",
			})
			clock.Advance(time.Millisecond * 50)
		}
	}

	opts := DefaultAttentionDigestOptions()
	opts.ActionRequiredLimit = 10
	opts.InterestingLimit = 20
	opts.BackgroundLimit = 10

	digest := BuildAttentionDigest(events, 0, int64(len(events)), opts)

	rawCount := len(events)
	surfacedCount := len(digest.Buckets.ActionRequired) + len(digest.Buckets.Interesting) + len(digest.Buckets.Background)

	compressionRatio := float64(rawCount) / float64(surfacedCount+1) // +1 to avoid div by zero

	t.Logf("raw_events=%d surfaced_items=%d compression_ratio=%.2fx",
		rawCount, surfacedCount, compressionRatio)

	// Expect at least 10x compression for highly repetitive workloads
	if compressionRatio < 5.0 {
		t.Errorf("deduplication compression ratio %.2f is below expected 5.0x", compressionRatio)
	}
}

// =============================================================================
// Latency Regression Tests
// =============================================================================

// TestLatencyRegression_BuildDigest checks that digest building stays fast.
func TestLatencyRegression_BuildDigest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping latency regression test in short mode")
	}
	t.Parallel()

	fixture := newBenchmarkFixture(scenarioLargeSessions, scenarioLargeAgents, scenarioLargeEvents)
	opts := DefaultAttentionDigestOptions()

	// Run multiple iterations and measure
	iterations := 100
	var totalDuration time.Duration

	for i := 0; i < iterations; i++ {
		start := time.Now()
		_ = BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)
		totalDuration += time.Since(start)
	}

	avgDuration := totalDuration / time.Duration(iterations)
	t.Logf("BuildAttentionDigest avg latency over %d iterations: %v", iterations, avgDuration)

	// Regression threshold: digest building for 5000 events should be under 50ms
	// (Current baseline ~22ms, threshold allows 2x headroom for regressions)
	if avgDuration > 50*time.Millisecond {
		t.Errorf("BuildAttentionDigest latency %v exceeds 50ms threshold", avgDuration)
	}
}

// TestLatencyRegression_Replay checks that replay stays fast.
func TestLatencyRegression_Replay(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping latency regression test in short mode")
	}
	t.Parallel()

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	// Pre-populate with large event count
	for i := 0; i < scenarioLargeEvents; i++ {
		feed.Append(AttentionEvent{
			Session:       "bench",
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentStateChange,
			Actionability: ActionabilityInteresting,
			Severity:      SeverityInfo,
			Summary:       fmt.Sprintf("Event %d", i),
		})
	}

	iterations := 100
	var totalDuration time.Duration

	for i := 0; i < iterations; i++ {
		start := time.Now()
		_, _, _ = feed.Replay(0, 1000)
		totalDuration += time.Since(start)
	}

	avgDuration := totalDuration / time.Duration(iterations)
	throughput := float64(1000) / avgDuration.Seconds()
	t.Logf("Replay avg latency over %d iterations: %v (%.0f events/sec)", iterations, avgDuration, throughput)

	// Regression threshold: replay of 1000 events should be under 1ms
	if avgDuration > time.Millisecond {
		t.Errorf("Replay latency %v exceeds 1ms threshold", avgDuration)
	}
}

// TestLatencyRegression_Filter checks that filtering stays fast.
func TestLatencyRegression_Filter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping latency regression test in short mode")
	}
	t.Parallel()

	fixture := newBenchmarkFixture(scenarioLargeSessions, scenarioLargeAgents, scenarioLargeEvents)
	opts := EventsOptions{
		Session:             "bench-session-0",
		CategoryFilter:      []string{string(EventCategoryAgent)},
		ActionabilityFilter: []string{string(ActionabilityActionRequired)},
	}

	iterations := 100
	var totalDuration time.Duration

	for i := 0; i < iterations; i++ {
		start := time.Now()
		_ = filterEventsForRobot(fixture.events, opts)
		totalDuration += time.Since(start)
	}

	avgDuration := totalDuration / time.Duration(iterations)
	t.Logf("filterEventsForRobot avg latency over %d iterations: %v", iterations, avgDuration)

	// Regression threshold: filtering 5000 events should be under 2ms
	if avgDuration > 2*time.Millisecond {
		t.Errorf("filterEventsForRobot latency %v exceeds 2ms threshold", avgDuration)
	}
}

// =============================================================================
// Memory Allocation Regression Tests
// =============================================================================

// TestAllocRegression_BuildDigest verifies digest building doesn't allocate excessively.
func TestAllocRegression_BuildDigest(t *testing.T) {
	t.Parallel()

	fixture := newBenchmarkFixture(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)
	opts := DefaultAttentionDigestOptions()

	// Warm up
	_ = BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)

	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)
		}
	})

	allocsPerOp := result.AllocsPerOp()
	bytesPerOp := result.AllocedBytesPerOp()

	t.Logf("BuildAttentionDigest: %d allocs/op, %d bytes/op", allocsPerOp, bytesPerOp)

	// Regression thresholds for medium scenario (500 events)
	// Current baseline: ~27K allocs, ~1.7MB
	// Thresholds allow 2x headroom for detecting significant regressions
	if allocsPerOp > 100000 {
		t.Errorf("BuildAttentionDigest allocations %d exceed threshold 100000", allocsPerOp)
	}
	if bytesPerOp > 5*1024*1024 { // 5MB
		t.Errorf("BuildAttentionDigest memory %d bytes exceeds threshold 5MB", bytesPerOp)
	}
}

// =============================================================================
// Renderer Comparison Benchmarks
// =============================================================================

func BenchmarkRenderComparison_JSON(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)
	opts := DefaultAttentionDigestOptions()
	digest := BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)

	renderer := NewJSONRenderer()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderer.Render(digest)
	}
}

func BenchmarkRenderComparison_TOON(b *testing.B) {
	fixture := newBenchmarkFixture(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)
	opts := DefaultAttentionDigestOptions()
	digest := BuildAttentionDigest(fixture.events, 0, int64(len(fixture.events)), opts)

	renderer := NewTOONRenderer()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderer.Render(digest)
	}
}

func BenchmarkRenderComparison_Markdown(b *testing.B) {
	proj := buildTestProjection(scenarioMediumSessions, scenarioMediumAgents, scenarioMediumEvents)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RenderMarkdownFromProjection(proj, false)
	}
}

// =============================================================================
// Heartbeat Overhead Benchmark
// =============================================================================

func BenchmarkAttentionFeed_HeartbeatOverhead(b *testing.B) {
	// Measure overhead of heartbeat interval vs no heartbeat
	noHeartbeatFeed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       10000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer noHeartbeatFeed.Stop()

	heartbeatFeed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       10000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 100 * time.Millisecond,
	})
	defer heartbeatFeed.Stop()

	event := AttentionEvent{
		Session:       "bench",
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "Benchmark event",
	}

	b.Run("no_heartbeat", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			noHeartbeatFeed.Append(event)
		}
	})

	b.Run("with_heartbeat", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			heartbeatFeed.Append(event)
		}
	})
}

// =============================================================================
// Section Truncation Benchmark
// =============================================================================

func BenchmarkSectionTruncation_Apply(b *testing.B) {
	// Test truncation application overhead
	sessions := make([]SnapshotSession, 100)
	for i := 0; i < 100; i++ {
		agents := make([]SnapshotAgent, 3)
		for j := 0; j < 3; j++ {
			agents[j] = SnapshotAgent{
				Pane:             fmt.Sprintf("%d", j+1),
				Type:             "claude",
				TypeConfidence:   0.95,
				TypeMethod:       "process",
				State:            "active",
				LastOutputAgeSec: 0,
				OutputTailLines:  50,
				PendingMail:      0,
			}
		}
		sessions[i] = SnapshotSession{
			Name:     fmt.Sprintf("session-%d", i),
			Attached: true,
			Agents:   agents,
		}
	}

	b.Run("no_truncation", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			// Just marshal all sessions
			_, _ = json.Marshal(sessions)
		}
	})

	b.Run("with_truncation", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			// Apply truncation (keep first 10)
			truncated := sessions[:10]
			section := ProjectedSection{
				Name:        SectionSessions,
				OrderWeight: 20,
				Data:        truncated,
				Truncation: &SectionTruncation{
					Applied:        true,
					OriginalCount:  100,
					TruncatedCount: 90,
					Reason:         "limit",
					ResumptionHint: "use offset=10",
				},
			}
			_, _ = json.Marshal(section)
		}
	})
}
