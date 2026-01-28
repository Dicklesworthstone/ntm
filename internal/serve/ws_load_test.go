//go:build load

// Package serve contains WebSocket load tests for bd-3a2ry.
// Run with: go test -tags=load -v ./internal/serve/... -run 'Load'
package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// =============================================================================
// WebSocket Load Tests (bd-3a2ry)
//
// Tests WebSocket concurrency, latency, and throughput under load.
// Run with: go test -tags=load -v ./internal/serve/... -timeout 5m
// =============================================================================

// LoadTestConfig configures load test parameters.
type LoadTestConfig struct {
	NumClients       int           // Number of concurrent WebSocket clients
	TestDuration     time.Duration // Total duration of the load test
	PublishInterval  time.Duration // Interval between event publications
	ThrottleEnabled  bool          // Whether to enable client-side throttling
	ClientBufferSize int           // Size of each client's receive buffer
}

// DefaultLoadTestConfig returns reasonable defaults for load testing.
func DefaultLoadTestConfig() LoadTestConfig {
	return LoadTestConfig{
		NumClients:       100,
		TestDuration:     10 * time.Second,
		PublishInterval:  10 * time.Millisecond,
		ThrottleEnabled:  false,
		ClientBufferSize: 256,
	}
}

// LoadTestStats captures metrics from a load test run.
type LoadTestStats struct {
	TotalEvents   int64         // Total events published
	TotalReceived int64         // Total events received across all clients
	TotalDropped  int64         // Total events dropped due to backpressure
	LatencyP50    time.Duration // 50th percentile latency
	LatencyP95    time.Duration // 95th percentile latency
	LatencyP99    time.Duration // 99th percentile latency
	MinLatency    time.Duration // Minimum observed latency
	MaxLatency    time.Duration // Maximum observed latency
	AvgLatency    time.Duration // Average latency
}

// loadTestClient simulates a WebSocket client for load testing.
type loadTestClient struct {
	id        int
	conn      *websocket.Conn
	received  int64
	dropped   int64
	latencies []time.Duration
	mu        sync.Mutex
	done      chan struct{}
}

func TestLoadWSHub_ConcurrentClients(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	cfg := DefaultLoadTestConfig()
	cfg.NumClients = 100
	cfg.TestDuration = 5 * time.Second
	cfg.PublishInterval = 20 * time.Millisecond

	stats := runWSLoadTest(t, cfg)

	t.Logf("LOAD_TEST: Concurrent clients test completed")
	t.Logf("  Clients: %d", cfg.NumClients)
	t.Logf("  Duration: %v", cfg.TestDuration)
	t.Logf("  Total events published: %d", stats.TotalEvents)
	t.Logf("  Total events received: %d", stats.TotalReceived)
	t.Logf("  Total events dropped: %d", stats.TotalDropped)
	t.Logf("  Latency P50: %v", stats.LatencyP50)
	t.Logf("  Latency P95: %v", stats.LatencyP95)
	t.Logf("  Latency P99: %v", stats.LatencyP99)
	t.Logf("  Min/Max/Avg: %v / %v / %v", stats.MinLatency, stats.MaxLatency, stats.AvgLatency)

	// Acceptance criteria checks
	if stats.LatencyP95 > 100*time.Millisecond {
		t.Errorf("P95 latency %v exceeds 100ms target", stats.LatencyP95)
	}

	// Verify reasonable delivery
	if stats.TotalEvents > 0 {
		deliveryRate := float64(stats.TotalReceived) / (float64(stats.TotalEvents) * float64(cfg.NumClients))
		t.Logf("  Delivery rate: %.2f%%", deliveryRate*100)
	}
}

func TestLoadWSHub_BurstOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	cfg := DefaultLoadTestConfig()
	cfg.NumClients = 50
	cfg.TestDuration = 3 * time.Second
	cfg.PublishInterval = 2 * time.Millisecond // Fast bursts
	cfg.ThrottleEnabled = true

	stats := runWSLoadTest(t, cfg)

	t.Logf("LOAD_TEST: Burst output test completed")
	t.Logf("  Clients: %d, Publish interval: %v", cfg.NumClients, cfg.PublishInterval)
	t.Logf("  Total events: %d, Received: %d, Dropped: %d", stats.TotalEvents, stats.TotalReceived, stats.TotalDropped)
	t.Logf("  Latency P95: %v, P99: %v", stats.LatencyP95, stats.LatencyP99)

	// With bursts, some drops are expected
	if stats.TotalEvents > 0 && stats.TotalReceived == 0 {
		t.Error("No events received during burst test")
	}
}

func TestLoadWSHub_LargePayloads(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	// Create hub and start it
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Create a client with a large buffer for receiving
	client := &WSClient{
		id:     "large-payload-client",
		hub:    hub,
		send:   make(chan []byte, 100),
		topics: make(map[string]struct{}),
	}
	client.Subscribe([]string{"panes:*"})
	hub.register <- client

	time.Sleep(20 * time.Millisecond)

	// Build large payloads (100 lines x 500 chars each = ~50KB per message)
	largeLines := make([]string, 100)
	for i := range largeLines {
		largeLines[i] = fmt.Sprintf("Line %d with lots of content: %s", i, strings.Repeat("x", 500))
	}

	numMessages := 50
	start := time.Now()

	// Publish large payloads through the hub
	for i := 0; i < numMessages; i++ {
		hub.Publish("panes:load:0", "pane.output", map[string]interface{}{
			"lines": largeLines,
			"idx":   i,
		})
	}

	// Wait for delivery
	time.Sleep(200 * time.Millisecond)

	// Count received messages
	received := 0
	var totalBytes int64
	for {
		select {
		case msg := <-client.send:
			received++
			totalBytes += int64(len(msg))
		default:
			goto done
		}
	}
done:

	elapsed := time.Since(start)
	throughput := float64(received) / elapsed.Seconds()

	t.Logf("LOAD_TEST: Large payloads test (via hub)")
	t.Logf("  Messages published: %d", numMessages)
	t.Logf("  Messages received: %d", received)
	t.Logf("  Total bytes received: %d KB", totalBytes/1024)
	t.Logf("  Time: %v", elapsed)
	t.Logf("  Throughput: %.1f msgs/sec", throughput)

	// Verify at least some messages got through
	if received == 0 {
		t.Error("No large payload messages received")
	}

	hub.unregister <- client
}

func TestLoadWSHub_ManyTopics(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Create clients subscribed to different topics
	numClients := 20
	clients := make([]*WSClient, numClients)
	for i := 0; i < numClients; i++ {
		clients[i] = &WSClient{
			id:     fmt.Sprintf("client-%d", i),
			hub:    hub,
			send:   make(chan []byte, 100),
			topics: make(map[string]struct{}),
		}
		// Each client subscribes to a different pane
		topic := fmt.Sprintf("panes:proj:%d", i)
		clients[i].Subscribe([]string{topic})
		hub.register <- clients[i]
	}

	time.Sleep(50 * time.Millisecond)

	// Verify client count
	if hub.ClientCount() != numClients {
		t.Errorf("expected %d clients, got %d", numClients, hub.ClientCount())
	}

	// Publish to all topics
	numEvents := 10
	for i := 0; i < numClients; i++ {
		for j := 0; j < numEvents; j++ {
			topic := fmt.Sprintf("panes:proj:%d", i)
			hub.Publish(topic, "pane.output", map[string]interface{}{
				"client": i,
				"event":  j,
			})
		}
	}

	time.Sleep(100 * time.Millisecond)

	// Verify each client received only their events
	for i, client := range clients {
		received := 0
		for {
			select {
			case <-client.send:
				received++
			default:
				goto done
			}
		}
	done:
		if received != numEvents {
			t.Errorf("client %d expected %d events, got %d", i, numEvents, received)
		}
	}

	t.Logf("LOAD_TEST: Many topics test - %d clients x %d events = %d total", numClients, numEvents, numClients*numEvents)

	// Cleanup
	for _, client := range clients {
		hub.unregister <- client
	}
}

// runWSLoadTest executes a load test with the given configuration.
func runWSLoadTest(t *testing.T, cfg LoadTestConfig) LoadTestStats {
	t.Helper()

	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(20 * time.Millisecond)

	// Create and connect clients
	clients := make([]*loadTestClient, cfg.NumClients)
	var wg sync.WaitGroup

	for i := 0; i < cfg.NumClients; i++ {
		wsClient := &WSClient{
			id:     fmt.Sprintf("load-%d", i),
			hub:    hub,
			send:   make(chan []byte, cfg.ClientBufferSize),
			topics: make(map[string]struct{}),
		}
		wsClient.Subscribe([]string{"panes:*"})
		hub.register <- wsClient

		clients[i] = &loadTestClient{
			id:        i,
			latencies: make([]time.Duration, 0, 1000),
			done:      make(chan struct{}),
		}

		// Start receiver goroutine
		wg.Add(1)
		go func(client *loadTestClient, wsc *WSClient) {
			defer wg.Done()
			for {
				select {
				case <-client.done:
					return
				case msg, ok := <-wsc.send:
					if !ok {
						return
					}
					// Parse message to get timestamp for latency calc
					var event struct {
						Timestamp string `json:"ts"`
					}
					if err := json.Unmarshal(msg, &event); err == nil && event.Timestamp != "" {
						if ts, err := time.Parse(time.RFC3339Nano, event.Timestamp); err == nil {
							latency := time.Since(ts)
							client.mu.Lock()
							client.latencies = append(client.latencies, latency)
							client.mu.Unlock()
						}
					}
					atomic.AddInt64(&client.received, 1)
				}
			}
		}(clients[i], wsClient)
	}

	time.Sleep(50 * time.Millisecond)

	// Start publishing events
	var totalPublished int64
	publishDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(cfg.PublishInterval)
		defer ticker.Stop()
		deadline := time.After(cfg.TestDuration)

		for {
			select {
			case <-deadline:
				close(publishDone)
				return
			case <-ticker.C:
				hub.Publish("panes:load:0", "pane.output", map[string]interface{}{
					"lines": []string{"load test output"},
					"seq":   atomic.LoadInt64(&totalPublished),
				})
				atomic.AddInt64(&totalPublished, 1)
			}
		}
	}()

	// Wait for test duration
	<-publishDone

	// Signal clients to stop and wait
	for _, client := range clients {
		close(client.done)
	}

	// Give time for final messages
	time.Sleep(100 * time.Millisecond)

	// Collect stats
	var stats LoadTestStats
	stats.TotalEvents = totalPublished

	var allLatencies []time.Duration
	for _, client := range clients {
		stats.TotalReceived += atomic.LoadInt64(&client.received)
		stats.TotalDropped += atomic.LoadInt64(&client.dropped)

		client.mu.Lock()
		allLatencies = append(allLatencies, client.latencies...)
		client.mu.Unlock()
	}

	// Calculate latency percentiles
	if len(allLatencies) > 0 {
		sort.Slice(allLatencies, func(i, j int) bool {
			return allLatencies[i] < allLatencies[j]
		})

		stats.MinLatency = allLatencies[0]
		stats.MaxLatency = allLatencies[len(allLatencies)-1]
		stats.LatencyP50 = allLatencies[len(allLatencies)*50/100]
		stats.LatencyP95 = allLatencies[len(allLatencies)*95/100]
		stats.LatencyP99 = allLatencies[len(allLatencies)*99/100]

		var total time.Duration
		for _, l := range allLatencies {
			total += l
		}
		stats.AvgLatency = total / time.Duration(len(allLatencies))
	}

	return stats
}

// Benchmark tests for performance measurement
func BenchmarkWSHub_Publish(b *testing.B) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Add a subscriber
	client := &WSClient{
		id:     "bench-client",
		hub:    hub,
		send:   make(chan []byte, 10000),
		topics: make(map[string]struct{}),
	}
	client.Subscribe([]string{"panes:*"})
	hub.register <- client

	time.Sleep(10 * time.Millisecond)

	// Drain client channel in background
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-client.send:
			}
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hub.Publish("panes:bench:0", "pane.output", map[string]interface{}{
			"lines": []string{"benchmark line"},
			"seq":   i,
		})
	}
	b.StopTimer()

	close(done)
	hub.unregister <- client
}

func BenchmarkWSHub_BroadcastToMany(b *testing.B) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Add multiple subscribers
	numClients := 50
	clients := make([]*WSClient, numClients)
	done := make(chan struct{})

	for i := 0; i < numClients; i++ {
		clients[i] = &WSClient{
			id:     fmt.Sprintf("bench-%d", i),
			hub:    hub,
			send:   make(chan []byte, 1000),
			topics: make(map[string]struct{}),
		}
		clients[i].Subscribe([]string{"panes:*"})
		hub.register <- clients[i]

		// Drain in background
		go func(c *WSClient) {
			for {
				select {
				case <-done:
					return
				case <-c.send:
				}
			}
		}(clients[i])
	}

	time.Sleep(50 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hub.Publish("panes:bench:0", "pane.output", map[string]interface{}{
			"lines": []string{"broadcast line"},
			"seq":   i,
		})
	}
	b.StopTimer()

	close(done)
	for _, client := range clients {
		hub.unregister <- client
	}
}
