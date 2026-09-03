package agentmail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type deadlineObservedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

type doneObservedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *doneObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

type readErrorCloser struct {
	err error
}

func (r readErrorCloser) Read([]byte) (int, error) { return 0, r.err }
func (readErrorCloser) Close() error               { return nil }

func (c *deadlineObservedContext) Deadline() (time.Time, bool) {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Deadline()
}

func TestNewClient(t *testing.T) {
	// Test default configuration
	c := NewClient()
	if c.baseURL != DefaultBaseURL {
		t.Errorf("expected base URL %s, got %s", DefaultBaseURL, c.baseURL)
	}
	if c.httpClient == nil {
		t.Error("expected HTTP client to be initialized")
	}

	// Test with options
	customURL := "http://custom:8080/mcp/"
	c = NewClient(WithBaseURL(customURL), WithToken("test-token"))
	if c.baseURL != customURL {
		t.Errorf("expected base URL %s, got %s", customURL, c.baseURL)
	}
	if c.bearerToken != "test-token" {
		t.Errorf("expected token 'test-token', got %s", c.bearerToken)
	}
}

func TestHealthCheck(t *testing.T) {
	// Mock MCP JSON-RPC server for health_check tool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		// Verify it's a health_check tool call
		params, ok := req.Params.(map[string]interface{})
		if !ok {
			t.Fatal("expected params to be a map")
		}
		if params["name"] != "health_check" {
			t.Errorf("expected health_check tool, got %v", params["name"])
		}

		// Return health status via JSON-RPC
		healthStatus := HealthStatus{
			Status:    "ok",
			Timestamp: time.Now().Format(time.RFC3339),
		}
		statusJSON, _ := json.Marshal(healthStatus)

		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(statusJSON),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	status, err := c.HealthCheck(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "ok" {
		t.Errorf("expected status 'ok', got %s", status.Status)
	}
}

func TestIsAvailable(t *testing.T) {
	if NewClient().IsAvailableContext(nil) {
		t.Fatal("nil-context availability probe reported success")
	}

	// Mock MCP JSON-RPC server for health_check. It exposes no cheap liveness
	// endpoint, so the availability probe must fall back to the tool call.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		// Return healthy status
		healthStatus := HealthStatus{Status: "ok"}
		statusJSON, _ := json.Marshal(healthStatus)

		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(statusJSON),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	if !c.IsAvailable() {
		t.Error("expected IsAvailable to return true")
	}

	// Test unavailable server
	c = NewClient(WithBaseURL("http://localhost:1/"))
	if c.IsAvailable() {
		t.Error("expected IsAvailable to return false for unreachable server")
	}
}

// TestIsAvailableContextRetriesTransientProbeFailures guards the bounded
// availability retry: a remote Agent Mail server that hiccups for one or two
// probes must not be branded unavailable (and negatively cached) off a single
// failed health check.
func TestIsAvailableContextRetriesTransientProbeFailures(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		n := calls.Add(1)
		if n <= 2 {
			// Transient failure: refuse the first two probes.
			http.Error(w, "temporarily overloaded", http.StatusServiceUnavailable)
			return
		}
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return
		}
		statusJSON, _ := json.Marshal(HealthStatus{Status: "ok"})
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: statusJSON})
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	if !c.IsAvailableContext(context.Background()) {
		t.Fatalf("transiently failing server was branded unavailable after %d probes", calls.Load())
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 probes (2 transient failures + 1 success), got %d", got)
	}
	if err := c.LastAvailabilityError(); err != nil {
		t.Fatalf("successful availability must clear the last probe error, got %v", err)
	}
}

func TestIsAvailableContextRetriesResponseBodyTransportFailure(t *testing.T) {
	var calls atomic.Int32
	client := NewClient(WithBaseURL("http://agentmail.invalid/"))
	client.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		body := io.ReadCloser(readErrorCloser{err: io.ErrUnexpectedEOF})
		if call > 1 {
			body = io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":2,"result":{"status":"ok"}}`))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       body,
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	if !client.IsAvailableContext(context.Background()) {
		t.Fatalf("availability did not recover after a transient response-body read failure: %v", client.LastAvailabilityError())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("transient response-body read failure made %d calls, want one retry and one success", got)
	}
	if err := client.LastAvailabilityError(); err != nil {
		t.Fatalf("successful retry did not clear response-body diagnostic: %v", err)
	}
}

func TestAvailabilityProbeBudgetBoundsAllAttempts(t *testing.T) {
	budget := availabilityProbeBudget()
	if budget <= 0 || budget >= 2*time.Second {
		t.Fatalf("availability probe budget must be positive and below 2s, got %v", budget)
	}

	var calls atomic.Int32
	client := NewClient(WithBaseURL("http://agentmail.invalid/"))
	client.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}

	started := time.Now()
	if client.IsAvailableContext(context.Background()) {
		t.Fatal("deadline-bound availability probe reported success")
	}
	elapsed := time.Since(started)
	if elapsed < budget-100*time.Millisecond {
		t.Fatalf("availability probe returned before its budget: elapsed=%v budget=%v", elapsed, budget)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("availability probe exceeded the strict 2s bound: %v", elapsed)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("a hung attempt must consume the one overall budget; got %d calls", got)
	}
	if err := client.LastAvailabilityError(); !errors.Is(err, ErrTimeout) {
		t.Fatalf("budget exhaustion must retain the terminal timeout, got %v", err)
	}
}

func TestAvailabilityRetryClassification(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{name: "server unavailable", err: ErrServerUnavailable, retryable: true},
		{name: "timeout", err: ErrTimeout, retryable: true},
		{name: "transient busy", err: ErrTransientBusy, retryable: true},
		{name: "HTTP 408", err: NewAPIError("health_check", http.StatusRequestTimeout, errors.New("status")), retryable: true},
		{name: "HTTP 425", err: NewAPIError("health_check", http.StatusTooEarly, errors.New("status")), retryable: true},
		{name: "HTTP 429", err: NewAPIError("health_check", http.StatusTooManyRequests, errors.New("status")), retryable: true},
		{name: "HTTP 500", err: NewAPIError("health_check", http.StatusInternalServerError, errors.New("status")), retryable: true},
		{name: "HTTP 599", err: NewAPIError("health_check", 599, errors.New("status")), retryable: true},
		{name: "unauthorized", err: NewAPIError("health_check", http.StatusUnauthorized, ErrUnauthorized)},
		{name: "other HTTP 4xx", err: NewAPIError("health_check", http.StatusBadRequest, errors.New("status"))},
		{name: "permanent JSON-RPC", err: NewAPIError("health_check", 0, &JSONRPCError{Code: -32602, Message: "invalid params"})},
		{name: "malformed decode", err: NewAPIError("health_check", 0, errors.New("invalid character"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableAvailabilityError(tt.err); got != tt.retryable {
				t.Fatalf("isRetryableAvailabilityError() = %t, want %t for %v", got, tt.retryable, tt.err)
			}
		})
	}
}

func TestIsAvailableContextPermanentFailuresAreOneShot(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized},
		{name: "other HTTP 4xx", statusCode: http.StatusBadRequest},
		{name: "permanent JSON-RPC", statusCode: http.StatusOK, body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid params"}}`},
		{name: "malformed decode", statusCode: http.StatusOK, body: `{`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					http.NotFound(w, r)
					return
				}
				calls.Add(1)
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewClient(WithBaseURL(server.URL + "/"))
			if client.IsAvailableContext(context.Background()) {
				t.Fatal("permanent health failure reported available")
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("permanent failure must not be retried; got %d calls", got)
			}
			if err := client.LastAvailabilityError(); err == nil {
				t.Fatal("permanent health failure did not retain its diagnostic")
			}
		})
	}
}

func TestIsAvailableContextTransientExhaustionRetainsTerminalError(t *testing.T) {
	statuses := []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusTooEarly}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := int(calls.Add(1))
		http.Error(w, "transient", statuses[call-1])
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL + "/"))
	if client.IsAvailableContext(context.Background()) {
		t.Fatal("transiently failing server reported available after retry exhaustion")
	}
	if got := calls.Load(); got != int32(availabilityProbeAttempts) {
		t.Fatalf("expected %d attempts, got %d", availabilityProbeAttempts, got)
	}
	var apiErr *APIError
	if err := client.LastAvailabilityError(); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooEarly {
		t.Fatalf("retry exhaustion must retain the terminal HTTP 425 error, got %v", err)
	}
}

func TestIsAvailableContextCancellationDuringBackoffPreservesState(t *testing.T) {
	firstResponse := make(chan struct{})
	var firstResponseOnce sync.Once
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "transient", http.StatusServiceUnavailable)
		firstResponseOnce.Do(func() { close(firstResponse) })
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL + "/"))
	previousErr := NewAPIError("previous_probe", http.StatusBadGateway, ErrServerUnavailable)
	client.lastAvailabilityErr.Store(availabilityErrBox{err: previousErr})
	client.availableCache.Store(true)
	client.availableCacheTime.Store(0)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bool, 1)
	go func() { result <- client.IsAvailableContext(ctx) }()
	select {
	case <-firstResponse:
	case <-time.After(time.Second):
		t.Fatal("first availability response did not complete")
	}
	cancel()

	select {
	case available := <-result:
		if available {
			t.Fatal("canceled availability probe reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("availability probe did not return after backoff cancellation")
	}
	if got := calls.Load(); got < 1 || got > int32(availabilityProbeAttempts) {
		t.Fatalf("backoff cancellation made an invalid number of calls: %d", got)
	}
	if got := client.LastAvailabilityError(); got != previousErr {
		t.Fatalf("backoff cancellation overwrote the previous diagnostic: got %v want %v", got, previousErr)
	}
	if got := client.availableCacheTime.Load(); got != 0 {
		t.Fatalf("backoff cancellation populated the cache timestamp: %d", got)
	}
	if !client.availableCache.Load() {
		t.Fatal("backoff cancellation overwrote the previous cached value")
	}
}

func TestWaitForAvailabilityRetryHonorsCallerCancellation(t *testing.T) {
	baseCtx, cancel := context.WithCancel(context.Background())
	observed := make(chan struct{})
	ctx := &doneObservedContext{Context: baseCtx, observed: observed}
	result := make(chan bool, 1)
	go func() {
		result <- waitForAvailabilityRetry(ctx, context.Background(), time.NewTimer(time.Second))
	}()

	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("availability retry did not begin waiting on caller cancellation")
	}
	cancel()

	select {
	case retry := <-result:
		if retry {
			t.Fatal("caller cancellation allowed another availability retry")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("availability retry did not stop after caller cancellation")
	}
}

func TestIsAvailableContextInvalidatedFailureThenSuccessClearsDiagnostic(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) == 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		statusJSON, _ := json.Marshal(HealthStatus{Status: "ok"})
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: 2, Result: statusJSON})
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL + "/"))
	if client.IsAvailableContext(context.Background()) {
		t.Fatal("initial real failure reported available")
	}
	if err := client.LastAvailabilityError(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("initial failure diagnostic = %v, want unauthorized", err)
	}

	client.InvalidateCache()
	if !client.IsAvailableContext(context.Background()) {
		t.Fatal("fresh successful probe after cache invalidation reported unavailable")
	}
	if err := client.LastAvailabilityError(); err != nil {
		t.Fatalf("successful probe did not clear the previous diagnostic: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected one failed and one successful probe, got %d calls", got)
	}
}

// TestIsAvailableContextSurfacesTerminalProbeError guards the diagnostics
// side: a hard-down server yields unavailable AND a retrievable reason.
func TestIsAvailableContextSurfacesTerminalProbeError(t *testing.T) {
	c := NewClient(WithBaseURL("http://localhost:1/"))
	if c.IsAvailableContext(context.Background()) {
		t.Fatal("unreachable server reported available")
	}
	if err := c.LastAvailabilityError(); err == nil {
		t.Fatal("terminal probe failure must surface a reason via LastAvailabilityError")
	}
}

func TestIsAvailableContextCancelsWhileAnotherHealthCheckOwnsLock(t *testing.T) {
	probeStarted := make(chan struct{}, 1)
	releaseProbe := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeStarted <- struct{}{}
		<-releaseProbe
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return
		}
		statusJSON, _ := json.Marshal(HealthStatus{Status: "ok"})
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: statusJSON})
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL + "/"))
	firstDone := make(chan bool, 1)
	go func() { firstDone <- client.IsAvailableContext(context.Background()) }()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("first availability probe did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	if client.IsAvailableContext(ctx) {
		t.Fatal("canceled availability waiter reported success")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("availability waiter ignored cancellation for %v", elapsed)
	}

	close(releaseProbe)
	select {
	case available := <-firstDone:
		if !available {
			t.Fatal("owning availability probe reported unavailable")
		}
	case <-time.After(time.Second):
		t.Fatal("owning availability probe did not finish")
	}
}

func TestAvailabilityProbeBudgetIncludesLockContention(t *testing.T) {
	firstProbeStarted := make(chan struct{}, 1)
	var calls atomic.Int32
	client := NewClient(WithBaseURL("http://agentmail.invalid/"))
	client.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			firstProbeStarted <- struct{}{}
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}

	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	ownerDone := make(chan bool, 1)
	go func() { ownerDone <- client.IsAvailableContext(ownerCtx) }()
	select {
	case <-firstProbeStarted:
	case <-time.After(time.Second):
		t.Fatal("owning availability probe did not start")
	}

	deadlineObserved := make(chan struct{})
	waiterCtx := &deadlineObservedContext{Context: context.Background(), observed: deadlineObserved}
	waiterDone := make(chan bool, 1)
	go func() { waiterDone <- client.IsAvailableContext(waiterCtx) }()
	select {
	case <-deadlineObserved:
	case <-time.After(500 * time.Millisecond):
		cancelOwner()
		t.Fatal("availability waiter did not establish its overall deadline before lock acquisition")
	}
	waiterStarted := time.Now()

	// Leave the waiter less than a fresh probe budget after the owner releases
	// the lock. A per-attempt deadline would therefore exceed the total bound.
	time.Sleep(availabilityProbeBudget() - 350*time.Millisecond)
	cancelOwner()
	select {
	case available := <-ownerDone:
		if available {
			t.Fatal("canceled owning availability probe reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("owning availability probe did not return after cancellation")
	}

	select {
	case available := <-waiterDone:
		if available {
			t.Fatal("deadline-bound availability waiter reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("availability waiter exceeded the strict two-second total budget")
	}
	if elapsed := time.Since(waiterStarted); elapsed >= 2*time.Second {
		t.Fatalf("availability waiter exceeded the strict two-second total budget: %v", elapsed)
	}
	if got := calls.Load(); got < 1 || got > 2 {
		t.Fatalf("availability contention made %d probes, want the owner plus at most one bounded waiter probe", got)
	}
}

func TestIsAvailableContextDoesNotCacheCanceledProbe(t *testing.T) {
	probeStarted := make(chan struct{}, 1)
	releaseProbe := make(chan struct{})
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if probes.Add(1) == 1 {
			probeStarted <- struct{}{}
			<-releaseProbe
			return
		}
		statusJSON, _ := json.Marshal(HealthStatus{Status: "ok"})
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: 1, Result: statusJSON})
	}))
	defer server.Close()
	defer close(releaseProbe)

	client := NewClient(WithBaseURL(server.URL + "/"))
	previousErr := NewAPIError("previous_probe", http.StatusBadGateway, ErrServerUnavailable)
	client.lastAvailabilityErr.Store(availabilityErrBox{err: previousErr})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bool, 1)
	go func() { result <- client.IsAvailableContext(ctx) }()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("availability probe did not start")
	}
	cancel()
	select {
	case available := <-result:
		if available {
			t.Fatal("canceled availability probe reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("availability probe did not return after cancellation")
	}
	if cacheTime := client.availableCacheTime.Load(); cacheTime != 0 {
		t.Fatalf("canceled availability probe populated cache timestamp %d", cacheTime)
	}
	if got := client.LastAvailabilityError(); got != previousErr {
		t.Fatalf("probe cancellation overwrote the previous diagnostic: got %v want %v", got, previousErr)
	}
	if !client.IsAvailableContext(t.Context()) {
		t.Fatal("live probe after cancellation was poisoned by canceled availability result")
	}
}

func TestCallTool(t *testing.T) {
	// Mock JSON-RPC server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.JSONRPC != "2.0" {
			t.Errorf("expected jsonrpc 2.0, got %s", req.JSONRPC)
		}
		if req.Method != "tools/call" {
			t.Errorf("expected method tools/call, got %s", req.Method)
		}

		// Return success response
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"id": 1, "name": "TestAgent"}`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.callTool(context.Background(), "test_tool", map[string]interface{}{"key": "value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if data.ID != 1 || data.Name != "TestAgent" {
		t.Errorf("unexpected result: %+v", data)
	}
}

func TestCallToolError(t *testing.T) {
	// Mock server that returns JSON-RPC error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Error: &JSONRPCError{
				Code:    -32600,
				Message: "Invalid Request",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	_, err := c.callTool(context.Background(), "test_tool", nil)
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	_, err := c.callTool(context.Background(), "test_tool", nil)
	if err == nil {
		t.Error("expected error, got nil")
	}
	if !IsUnauthorized(err) {
		t.Errorf("expected unauthorized error, got: %v", err)
	}
}

func TestProjectKey(t *testing.T) {
	c := NewClient(WithProjectKey("/test/project"))
	if c.ProjectKey() != "/test/project" {
		t.Errorf("expected /test/project, got %s", c.ProjectKey())
	}

	c.SetProjectKey("/new/project")
	if c.ProjectKey() != "/new/project" {
		t.Errorf("expected /new/project, got %s", c.ProjectKey())
	}
}

func TestJSONRPCError(t *testing.T) {
	err := &JSONRPCError{
		Code:    -32600,
		Message: "Invalid Request",
	}

	expected := "JSON-RPC error -32600: Invalid Request"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}

	// With data
	err.Data = map[string]string{"field": "value"}
	if err.Error() == expected {
		t.Error("expected error message to include data")
	}
}

func TestAPIError(t *testing.T) {
	innerErr := ErrServerUnavailable
	err := NewAPIError("test_op", 503, innerErr)

	if err.Operation != "test_op" {
		t.Errorf("expected operation 'test_op', got %s", err.Operation)
	}
	if err.StatusCode != 503 {
		t.Errorf("expected status 503, got %d", err.StatusCode)
	}
	if err.Unwrap() != innerErr {
		t.Error("Unwrap should return the inner error")
	}
	if !IsServerUnavailable(err) {
		t.Error("expected IsServerUnavailable to return true")
	}
}

func TestErrorHelpers(t *testing.T) {
	tests := []struct {
		err    error
		check  func(error) bool
		expect bool
	}{
		{ErrServerUnavailable, IsServerUnavailable, true},
		{ErrUnauthorized, IsUnauthorized, true},
		{ErrNotFound, IsNotFound, true},
		{ErrInvalidRequest, IsInvalidRequest, true},
		{ErrTimeout, IsTimeout, true},
		{ErrNotImplemented, IsNotImplemented, true},
		{ErrReservationConflict, IsReservationConflict, true},
		{ErrContactBlocked, IsContactBlocked, true},
		{ErrServerUnavailable, IsUnauthorized, false},
		{NewAPIError("test", 0, ErrNotFound), IsNotFound, true},
		{NewAPIError("test", 0, ErrNotImplemented), IsNotImplemented, true},
	}

	for _, tt := range tests {
		result := tt.check(tt.err)
		if result != tt.expect {
			t.Errorf("for %v, expected %v, got %v", tt.err, tt.expect, result)
		}
	}
}

func TestExtractMCPContent(t *testing.T) {
	tests := []struct {
		name        string
		input       json.RawMessage
		wantErr     bool
		errContains string
		validate    func(t *testing.T, result json.RawMessage)
	}{
		{
			name:  "raw result (no envelope) - backward compatibility",
			input: json.RawMessage(`{"id": 1, "name": "TestAgent"}`),
			validate: func(t *testing.T, result json.RawMessage) {
				var data struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				}
				if err := json.Unmarshal(result, &data); err != nil {
					t.Fatalf("failed to unmarshal: %v", err)
				}
				if data.ID != 1 || data.Name != "TestAgent" {
					t.Errorf("unexpected data: %+v", data)
				}
			},
		},
		{
			name: "MCP envelope with structuredContent (preferred)",
			input: json.RawMessage(`{
				"content": [{"type": "text", "text": "{\"id\":99,\"name\":\"Ignored\"}"}],
				"structuredContent": {"id": 115, "name": "BrownOtter"},
				"isError": false
			}`),
			validate: func(t *testing.T, result json.RawMessage) {
				var data struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				}
				if err := json.Unmarshal(result, &data); err != nil {
					t.Fatalf("failed to unmarshal: %v", err)
				}
				if data.ID != 115 || data.Name != "BrownOtter" {
					t.Errorf("expected {115, BrownOtter}, got %+v", data)
				}
			},
		},
		{
			name: "MCP envelope with content text (fallback)",
			input: json.RawMessage(`{
				"content": [{"type": "text", "text": "{\"id\":42,\"name\":\"GreenLake\"}"}],
				"isError": false
			}`),
			validate: func(t *testing.T, result json.RawMessage) {
				var data struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				}
				if err := json.Unmarshal(result, &data); err != nil {
					t.Fatalf("failed to unmarshal: %v", err)
				}
				if data.ID != 42 || data.Name != "GreenLake" {
					t.Errorf("expected {42, GreenLake}, got %+v", data)
				}
			},
		},
		{
			name: "MCP envelope with isError=true and message",
			input: json.RawMessage(`{
				"content": [{"type": "text", "text": "Agent name already in use"}],
				"isError": true
			}`),
			wantErr:     true,
			errContains: "Agent name already in use",
		},
		{
			name: "MCP envelope with isError=true no message",
			input: json.RawMessage(`{
				"content": [],
				"isError": true
			}`),
			wantErr:     true,
			errContains: "tool returned error",
		},
		{
			name:  "empty result",
			input: json.RawMessage(``),
			validate: func(t *testing.T, result json.RawMessage) {
				if len(result) != 0 {
					t.Errorf("expected empty result, got %s", string(result))
				}
			},
		},
		{
			name:  "null result",
			input: json.RawMessage(`null`),
			validate: func(t *testing.T, result json.RawMessage) {
				// null is valid JSON, should pass through
				if string(result) != "null" {
					t.Errorf("expected null, got %s", string(result))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := extractMCPContent(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestCallToolWithMCPEnvelope(t *testing.T) {
	// Mock server that returns MCP envelope format
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			// MCP envelope with structuredContent
			Result: json.RawMessage(`{
				"content": [{"type": "text", "text": "{\"id\":115,\"name\":\"BrownOtter\"}"}],
				"structuredContent": {"id": 115, "name": "BrownOtter", "program": "ntm", "model": "coordinator"},
				"isError": false
			}`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.callTool(context.Background(), "register_agent", map[string]interface{}{
		"project_key": "/test/project",
		"program":     "ntm",
		"model":       "coordinator",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify we got the extracted structuredContent, not the envelope
	var agent struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Program string `json:"program"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal(result, &agent); err != nil {
		t.Fatalf("failed to unmarshal agent: %v", err)
	}
	if agent.ID != 115 {
		t.Errorf("expected ID 115, got %d", agent.ID)
	}
	if agent.Name != "BrownOtter" {
		t.Errorf("expected name BrownOtter, got %s", agent.Name)
	}
	if agent.Program != "ntm" {
		t.Errorf("expected program ntm, got %s", agent.Program)
	}
}

func TestFlexTime_UnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		want        time.Time
		wantErr     bool
		wantUTCOnly bool
	}{
		{
			name:        "rfc3339",
			input:       `"2026-01-31T01:24:27Z"`,
			want:        time.Date(2026, 1, 31, 1, 24, 27, 0, time.UTC),
			wantUTCOnly: true,
		},
		{
			name:        "rfc3339_nano",
			input:       `"2026-01-31T01:24:27.123456789Z"`,
			want:        time.Date(2026, 1, 31, 1, 24, 27, 123456789, time.UTC),
			wantUTCOnly: true,
		},
		{
			name:        "bare_seconds_assume_utc",
			input:       `"2026-01-31T01:24:27"`,
			want:        time.Date(2026, 1, 31, 1, 24, 27, 0, time.UTC),
			wantUTCOnly: true,
		},
		{
			name:        "bare_millis_assume_utc",
			input:       `"2026-01-31T01:24:27.123"`,
			want:        time.Date(2026, 1, 31, 1, 24, 27, 123000000, time.UTC),
			wantUTCOnly: true,
		},
		{
			name:  "empty_string_sets_zero",
			input: `""`,
			want:  time.Time{},
		},
		{
			name:    "invalid_format",
			input:   `"not-a-timestamp"`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ft FlexTime
			err := json.Unmarshal([]byte(tt.input), &ft)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !ft.Time.Equal(tt.want) {
				t.Fatalf("expected %s, got %s", tt.want.Format(time.RFC3339Nano), ft.Time.Format(time.RFC3339Nano))
			}
			if tt.wantUTCOnly && ft.Time.Location() != time.UTC {
				t.Fatalf("expected UTC, got %s", ft.Time.Location())
			}
		})
	}
}

func TestFlexTime_MarshalJSON(t *testing.T) {
	t.Parallel()

	ft := FlexTime{Time: time.Date(2026, 1, 31, 1, 24, 27, 123456789, time.UTC)}
	got, err := json.Marshal(ft)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != `"2026-01-31T01:24:27.123456789Z"` {
		t.Fatalf("unexpected JSON: %s", string(got))
	}
}

func TestReservationConflict_UnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantPath    string
		wantHolders []string
		wantErr     bool
	}{
		{
			name:        "null_holders_becomes_empty_slice",
			input:       `{"path":"internal/agentmail/*","holders":null}`,
			wantPath:    "internal/agentmail/*",
			wantHolders: []string{},
		},
		{
			name:        "legacy_list_of_names",
			input:       `{"path":"x","holders":["BlueLake","RedStone"]}`,
			wantPath:    "x",
			wantHolders: []string{"BlueLake", "RedStone"},
		},
		{
			name:        "current_list_of_objects",
			input:       `{"path":"x","holders":[{"agent":"BlueLake"},{"agent_name":"RedStone"},{"agent":"","agent_name":"GreenCastle"}]}`,
			wantPath:    "x",
			wantHolders: []string{"BlueLake", "RedStone", "GreenCastle"},
		},
		{
			name:    "unsupported_format_errors",
			input:   `{"path":"x","holders":123}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c ReservationConflict
			err := json.Unmarshal([]byte(tt.input), &c)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if c.Path != tt.wantPath {
				t.Fatalf("expected path %q, got %q", tt.wantPath, c.Path)
			}
			if !reflect.DeepEqual(c.Holders, tt.wantHolders) {
				t.Fatalf("expected holders %#v, got %#v", tt.wantHolders, c.Holders)
			}
		})
	}
}

// =============================================================================
// Client option and method tests for coverage
// =============================================================================

func TestInvalidateCache(t *testing.T) {
	t.Parallel()

	c := NewClient()
	// Manually set cache time to simulate a cached result
	c.availableCacheTime.Store(time.Now().Unix())
	c.availableCache.Store(true)

	// Verify cache was set
	if c.availableCacheTime.Load() == 0 {
		t.Fatal("expected cache time to be set")
	}

	// Invalidate
	c.InvalidateCache()

	// Verify cache was cleared
	if c.availableCacheTime.Load() != 0 {
		t.Error("expected cache time to be cleared after InvalidateCache")
	}
}

func TestBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		inputURL string
		wantURL  string
	}{
		{"default URL", "", DefaultBaseURL},
		{"custom URL with trailing slash", "http://custom:8080/mcp/", "http://custom:8080/mcp/"},
		{"custom URL without trailing slash", "http://custom:8080/mcp", "http://custom:8080/mcp/"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var c *Client
			if tc.inputURL == "" {
				c = NewClient()
			} else {
				c = NewClient(WithBaseURL(tc.inputURL))
			}
			got := c.BaseURL()
			if got != tc.wantURL {
				t.Errorf("BaseURL() = %q, want %q", got, tc.wantURL)
			}
		})
	}
}

func TestHttpBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{"MCP URL with trailing slash", "http://127.0.0.1:8765/mcp/", "http://127.0.0.1:8765"},
		{"MCP URL without trailing slash", "http://127.0.0.1:8765/mcp", "http://127.0.0.1:8765"},
		{"non-MCP URL with trailing slash", "http://example.com/api/", "http://example.com/api"},
		{"non-MCP URL without trailing slash", "http://example.com/api", "http://example.com/api"},
		{"root URL with trailing slash", "http://localhost:8000/", "http://localhost:8000"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewClient(WithBaseURL(tc.baseURL))
			got := c.httpBaseURL()
			if got != tc.want {
				t.Errorf("httpBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFetchInbox_UnmarshalFormats(t *testing.T) {
	t.Parallel()

	makeServer := func(t *testing.T, rawResult json.RawMessage) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req JSONRPCRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Method != "tools/call" {
				t.Fatalf("expected tools/call, got %s", req.Method)
			}

			params, ok := req.Params.(map[string]interface{})
			if !ok {
				t.Fatal("expected params to be a map")
			}
			if params["name"] != "fetch_inbox" {
				t.Fatalf("expected tool fetch_inbox, got %v", params["name"])
			}

			args, _ := params["arguments"].(map[string]interface{})
			if args["project_key"] != "/test/project" {
				t.Fatalf("expected project_key /test/project, got %v", args["project_key"])
			}
			if args["agent_name"] != "BlueLake" {
				t.Fatalf("expected agent_name BlueLake, got %v", args["agent_name"])
			}
			if args["include_bodies"] != true {
				t.Fatalf("expected include_bodies true, got %v", args["include_bodies"])
			}

			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  rawResult,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
	}

	t.Run("wrapper_object", func(t *testing.T) {
		server := makeServer(t, json.RawMessage(`{"result":[{"id":1,"subject":"Hello","from":"alice","created_ts":"2026-01-01T00:00:00Z","importance":"normal","ack_required":false,"kind":"to","body_md":"hi"}]}`))
		defer server.Close()

		c := NewClient(WithBaseURL(server.URL + "/"))
		msgs, err := c.FetchInbox(context.Background(), FetchInboxOptions{
			ProjectKey:    "/test/project",
			AgentName:     "BlueLake",
			IncludeBodies: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(msgs) != 1 || msgs[0].ID != 1 || msgs[0].From != "alice" {
			t.Fatalf("unexpected messages: %+v", msgs)
		}
	})

	t.Run("direct_array", func(t *testing.T) {
		server := makeServer(t, json.RawMessage(`[{"id":2,"subject":"Yo","from":"bob","created_ts":"2026-01-01T00:00:00Z","importance":"normal","ack_required":false,"kind":"to","body_md":"hey"}]`))
		defer server.Close()

		c := NewClient(WithBaseURL(server.URL + "/"))
		msgs, err := c.FetchInbox(context.Background(), FetchInboxOptions{
			ProjectKey:    "/test/project",
			AgentName:     "BlueLake",
			IncludeBodies: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(msgs) != 1 || msgs[0].ID != 2 || msgs[0].From != "bob" {
			t.Fatalf("unexpected messages: %+v", msgs)
		}
	})

	t.Run("schema_drift_is_error", func(t *testing.T) {
		server := makeServer(t, json.RawMessage(`{"messages":[]}`))
		defer server.Close()

		c := NewClient(WithBaseURL(server.URL + "/"))
		if _, err := c.FetchInbox(context.Background(), FetchInboxOptions{
			ProjectKey:    "/test/project",
			AgentName:     "BlueLake",
			IncludeBodies: true,
		}); err == nil {
			t.Fatal("expected schema drift to return an error")
		}
	})
}

func TestExtractMCPContentTypedToolRejection(t *testing.T) {
	t.Run("rejection_with_message", func(t *testing.T) {
		_, err := extractMCPContent(json.RawMessage(`{
			"content": [{"type": "text", "text": "Agent 'NTM-Coordinator' not found"}],
			"isError": true
		}`))
		if err == nil || !IsToolRejection(err) {
			t.Fatalf("extractMCPContent error=%v, want typed tool rejection", err)
		}
		if IsServerUnavailable(err) || IsTimeout(err) {
			t.Fatalf("tool rejection misclassified as ambiguous transport failure: %v", err)
		}
	})

	t.Run("busy_rejection_keeps_transient_classification", func(t *testing.T) {
		_, err := extractMCPContent(json.RawMessage(`{
			"content": [{"type": "text", "text": "database is busy"}],
			"isError": true
		}`))
		if err == nil || !IsToolRejection(err) || !IsTransientBusy(err) {
			t.Fatalf("busy rejection lost a classification: rejection=%v busy=%v err=%v",
				IsToolRejection(err), IsTransientBusy(err), err)
		}
	})

	t.Run("rejection_without_message", func(t *testing.T) {
		_, err := extractMCPContent(json.RawMessage(`{"isError": true}`))
		if err == nil || !IsToolRejection(err) {
			t.Fatalf("extractMCPContent error=%v, want typed tool rejection", err)
		}
	})
}

// TestIsAvailableSlowHealthCheckToolStillAvailable is the #295 regression: a
// server that answers the cheap liveness endpoint instantly must be reported
// available even when the MCP `health_check` tool — a full archive/SQLite
// inventory on a real mailbox — takes far longer than the probe budget. The
// old probe called only the tool, so every reservation verb refused against a
// healthy, granting server.
func TestIsAvailableSlowHealthCheckToolStillAvailable(t *testing.T) {
	var toolCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ready"}`))
			return
		}
		toolCalls.Add(1)
		// Far beyond the probe budget, like health_check on a large store.
		time.Sleep(3 * time.Second)
		statusJSON, _ := json.Marshal(HealthStatus{Status: "ok"})
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: 1, Result: statusJSON})
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL + "/mcp/"))
	started := time.Now()
	if !client.IsAvailableContext(context.Background()) {
		t.Fatalf("server with instant liveness reported unavailable: %v", client.LastAvailabilityError())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("liveness-based probe took %v; it must not wait on the health_check tool", elapsed)
	}
	if got := toolCalls.Load(); got != 0 {
		t.Fatalf("availability probe called the heavy health_check tool %d times, want 0", got)
	}
	if err := client.LastAvailabilityError(); err != nil {
		t.Fatalf("available server retained a diagnostic: %v", err)
	}
}

// TestIsAvailableFallsBackToHealthCheckToolWithoutLivenessEndpoint keeps the
// positive control: a server with no /health endpoint is still judged by the
// MCP dispatch path.
func TestIsAvailableFallsBackToHealthCheckToolWithoutLivenessEndpoint(t *testing.T) {
	var toolCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		toolCalls.Add(1)
		statusJSON, _ := json.Marshal(HealthStatus{Status: "ok"})
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: 1, Result: statusJSON})
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL + "/mcp/"))
	if !client.IsAvailableContext(context.Background()) {
		t.Fatalf("MCP-only server reported unavailable: %v", client.LastAvailabilityError())
	}
	if got := toolCalls.Load(); got != 1 {
		t.Fatalf("fallback made %d health_check calls, want 1", got)
	}
}

// TestIsAvailableDownServerStaysUnavailable is the negative control: the
// liveness-first probe must not make a dead server look alive, and must keep
// the terminal reason.
func TestIsAvailableDownServerStaysUnavailable(t *testing.T) {
	client := NewClient(WithBaseURL("http://127.0.0.1:1/mcp/"))
	if client.IsAvailableContext(context.Background()) {
		t.Fatal("unreachable server reported available")
	}
	if err := client.LastAvailabilityError(); err == nil {
		t.Fatal("unreachable server left no diagnostic")
	}
}

// TestLivenessURLDerivesOriginRoot pins that the liveness endpoint is resolved
// against the server origin, not against the MCP mount path.
func TestLivenessURLDerivesOriginRoot(t *testing.T) {
	tests := []struct {
		base string
		want string
	}{
		{base: "http://127.0.0.1:8765/mcp/", want: "http://127.0.0.1:8765/health"},
		{base: "https://mail.example.test/a/b/mcp/", want: "https://mail.example.test/health"},
	}
	for _, tt := range tests {
		c := NewClient(WithBaseURL(tt.base))
		got, err := c.livenessURL()
		if err != nil {
			t.Fatalf("livenessURL(%q) error = %v", tt.base, err)
		}
		if got != tt.want {
			t.Fatalf("livenessURL(%q) = %q, want %q", tt.base, got, tt.want)
		}
	}
}

// TestAvailabilityProbeBudgetHonorsEnvOverride covers the operator escape
// hatch requested in #295 for servers with no liveness endpoint and a mailbox
// large enough that health_check cannot land inside the default budget.
func TestAvailabilityProbeBudgetHonorsEnvOverride(t *testing.T) {
	if got := availabilityProbeBudget(); got != defaultAvailabilityProbeBudget {
		t.Fatalf("default budget = %v, want %v", got, defaultAvailabilityProbeBudget)
	}
	t.Setenv(ProbeBudgetEnv, "4s")
	if got := availabilityProbeBudget(); got != 4*time.Second {
		t.Fatalf("overridden budget = %v, want 4s", got)
	}
	for _, bad := range []string{"", "not-a-duration", "0s", "-1s"} {
		t.Setenv(ProbeBudgetEnv, bad)
		if got := availabilityProbeBudget(); got != defaultAvailabilityProbeBudget {
			t.Fatalf("budget with %q = %v, want the default %v", bad, got, defaultAvailabilityProbeBudget)
		}
	}
}
