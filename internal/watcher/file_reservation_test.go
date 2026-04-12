// Package watcher tests for file reservation watcher.
package watcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type watcherToolHandler func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError)

func newWatcherMCPServer(t *testing.T, handlers map[string]watcherToolHandler) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
			return
		}

		var req agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")

		if req.Method != "tools/call" {
			_ = json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &agentmail.JSONRPCError{Code: -32601, Message: "unknown method: " + req.Method},
			})
			return
		}

		params, _ := req.Params.(map[string]interface{})
		toolName, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]interface{})

		handler, ok := handlers[toolName]
		if !ok {
			_ = json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &agentmail.JSONRPCError{Code: -32601, Message: "unknown tool: " + toolName},
			})
			return
		}

		result, rpcErr := handler(args)
		if rpcErr != nil {
			_ = json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   rpcErr,
			})
			return
		}

		resultJSON, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(resultJSON),
		})
	}))
}

// TestNewFileReservationWatcher tests the constructor and options.
func TestNewFileReservationWatcher(t *testing.T) {
	t.Run("default values", func(t *testing.T) {
		w := NewFileReservationWatcher()
		t.Logf("RESERVATION_TEST: default_values | pollInterval=%v | idleTimeout=%v | reservationTTL=%v",
			w.pollInterval, w.idleTimeout, w.reservationTTL)

		if w.pollInterval != DefaultPollIntervalReservation {
			t.Errorf("expected pollInterval=%v, got %v", DefaultPollIntervalReservation, w.pollInterval)
		}
		if w.idleTimeout != DefaultIdleTimeout {
			t.Errorf("expected idleTimeout=%v, got %v", DefaultIdleTimeout, w.idleTimeout)
		}
		if w.reservationTTL != DefaultReservationTTL {
			t.Errorf("expected reservationTTL=%v, got %v", DefaultReservationTTL, w.reservationTTL)
		}
		if w.captureLines != DefaultCaptureLinesReservation {
			t.Errorf("expected captureLines=%d, got %d", DefaultCaptureLinesReservation, w.captureLines)
		}
		if w.activeReservations == nil {
			t.Error("activeReservations should be initialized")
		}
	})

	t.Run("with options", func(t *testing.T) {
		client := agentmail.NewClient()
		w := NewFileReservationWatcher(
			WithWatcherClient(client),
			WithProjectDir("/test/project"),
			WithAgentName("TestAgent"),
			WithSessionFilter("test-session"),
			WithReservationPollInterval(5*time.Second),
			WithIdleTimeout(5*time.Minute),
			WithReservationTTL(10*time.Minute),
			WithDebug(true),
		)

		t.Logf("RESERVATION_TEST: with_options | projectDir=%s | agentName=%s | debug=%v",
			w.projectDir, w.agentName, w.debug)

		if w.client != client {
			t.Error("client not set correctly")
		}
		if w.projectDir != "/test/project" {
			t.Errorf("expected projectDir=/test/project, got %s", w.projectDir)
		}
		if w.agentName != "TestAgent" {
			t.Errorf("expected agentName=TestAgent, got %s", w.agentName)
		}
		if w.sessionFilter != "test-session" {
			t.Errorf("expected sessionFilter=test-session, got %s", w.sessionFilter)
		}
		if w.pollInterval != 5*time.Second {
			t.Errorf("expected pollInterval=5s, got %v", w.pollInterval)
		}
		if w.idleTimeout != 5*time.Minute {
			t.Errorf("expected idleTimeout=5m, got %v", w.idleTimeout)
		}
		if w.reservationTTL != 10*time.Minute {
			t.Errorf("expected reservationTTL=10m, got %v", w.reservationTTL)
		}
		if !w.debug {
			t.Error("expected debug=true")
		}
	})

	t.Run("zero duration options ignored", func(t *testing.T) {
		w := NewFileReservationWatcher(
			WithReservationPollInterval(0),
			WithIdleTimeout(0),
			WithReservationTTL(0),
		)

		// Zero values should not override defaults
		if w.pollInterval != DefaultPollIntervalReservation {
			t.Errorf("zero pollInterval should use default, got %v", w.pollInterval)
		}
		if w.idleTimeout != DefaultIdleTimeout {
			t.Errorf("zero idleTimeout should use default, got %v", w.idleTimeout)
		}
		if w.reservationTTL != DefaultReservationTTL {
			t.Errorf("zero reservationTTL should use default, got %v", w.reservationTTL)
		}
	})
}

// TestMapAgentTypeToPatternAgent tests agent type mapping.
func TestMapAgentTypeToPatternAgent(t *testing.T) {
	tests := []struct {
		agentType tmux.AgentType
		expected  string
	}{
		{tmux.AgentClaude, "claude"},
		{tmux.AgentCodex, "codex"},
		{tmux.AgentGemini, "gemini"},
		{tmux.AgentUser, "*"},
		{tmux.AgentType("unknown"), "*"},
	}

	for _, tc := range tests {
		t.Run(string(tc.agentType), func(t *testing.T) {
			result := mapAgentTypeToPatternAgent(tc.agentType)
			t.Logf("RESERVATION_TEST: mapAgentType | input=%s | result=%s", tc.agentType, result)
			if result != tc.expected {
				t.Errorf("expected %s, got %s", tc.expected, result)
			}
		})
	}
}

// TestExtractEditedFiles tests file path extraction from agent output.
func TestExtractEditedFiles(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		agentType string
		expected  []string
	}{
		{
			name:      "claude JSON file_path",
			output:    `{"file_path": "/src/main.go", "old_string": "foo"}`,
			agentType: "claude",
			expected:  []string{"/src/main.go"},
		},
		{
			name:      "claude edited file",
			output:    "I edited file: /internal/watcher/watcher.go to add the new function",
			agentType: "claude",
			expected:  []string{"/internal/watcher/watcher.go"},
		},
		{
			name:      "claude modified file",
			output:    "Modified /cmd/main.go with the new imports",
			agentType: "claude",
			expected:  []string{"/cmd/main.go"},
		},
		{
			name:      "claude created file",
			output:    "Created new file: /internal/utils/helper.go",
			agentType: "claude",
			expected:  []string{"/internal/utils/helper.go"},
		},
		{
			name:      "claude writing to file",
			output:    "Writing to /pkg/config/config.go",
			agentType: "claude",
			expected:  []string{"/pkg/config/config.go"},
		},
		{
			name:      "claude wrote file",
			output:    "Wrote to file /test/test_helper.go",
			agentType: "claude",
			expected:  []string{"/test/test_helper.go"},
		},
		{
			name:      "codex editing file",
			output:    "Editing /src/components/Button.tsx",
			agentType: "codex",
			expected:  []string{"/src/components/Button.tsx"},
		},
		{
			name:      "gemini Writing prefix",
			output:    "Writing: /app/models/user.py",
			agentType: "gemini",
			expected:  []string{"/app/models/user.py"},
		},
		{
			name:      "gemini Editing prefix",
			output:    "Editing: /app/views/home.py",
			agentType: "gemini",
			expected:  []string{"/app/views/home.py"},
		},
		{
			name:      "gemini Created prefix",
			output:    "Created: /tests/test_user.py",
			agentType: "gemini",
			expected:  []string{"/tests/test_user.py"},
		},
		{
			name:      "generic checkmark edited",
			output:    "✓ edited: /src/app.rs",
			agentType: "*",
			expected:  []string{"/src/app.rs"},
		},
		{
			name:      "generic checkmark created",
			output:    "✓ created: /src/lib.rs",
			agentType: "*",
			expected:  []string{"/src/lib.rs"},
		},
		{
			name: "multiple files",
			output: `{"file_path": "/src/a.go"}
			{"file_path": "/src/b.go"}
			Modified /src/c.go`,
			agentType: "claude",
			expected:  []string{"/src/a.go", "/src/b.go", "/src/c.go"},
		},
		{
			name:      "relative path",
			output:    "Edited ./internal/config.go",
			agentType: "claude",
			expected:  []string{"./internal/config.go"},
		},
		{
			name:      "path without extension - ignored",
			output:    "Modified /src/config",
			agentType: "claude",
			expected:  []string{},
		},
		{
			name:      "no files",
			output:    "Just some text without file paths",
			agentType: "claude",
			expected:  []string{},
		},
		{
			name:      "deduplication",
			output:    `{"file_path": "/src/main.go"} and also modified /src/main.go`,
			agentType: "claude",
			expected:  []string{"/src/main.go"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractEditedFiles(tc.output, tc.agentType)
			t.Logf("RESERVATION_TEST: extractEditedFiles | name=%s | agentType=%s | files=%v",
				tc.name, tc.agentType, result)

			if len(result) != len(tc.expected) {
				t.Errorf("expected %d files, got %d: %v", len(tc.expected), len(result), result)
				return
			}

			// Check each expected file is in result
			for _, exp := range tc.expected {
				found := false
				for _, got := range result {
					if got == exp {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected file %s not found in result %v", exp, result)
				}
			}
		})
	}
}

// TestCleanFilePathForReservation tests file path cleaning.
func TestCleanFilePathForReservation(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/src/main.go", "/src/main.go"},
		{`"/src/main.go"`, "/src/main.go"},
		{`'/src/main.go'`, "/src/main.go"},
		{"  /src/main.go  ", "/src/main.go"},
		{"/src/main.go.", "/src/main.go"},
		{"/src/main.go,", "/src/main.go"},
		{"/src/main.go;", "/src/main.go"},
		{"/src/main.go:", "/src/main.go"},
		{"/src/main.go!", "/src/main.go"},
		{"/src/main.go?", "/src/main.go"},
		{`'  /src/main.go  '`, "/src/main.go"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := cleanFilePathForReservation(tc.input)
			t.Logf("RESERVATION_TEST: cleanFilePath | input=%q | result=%q", tc.input, result)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

// TestIsValidFilePathForReservation tests file path validation.
func TestIsValidFilePathForReservation(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		// Valid paths
		{"/src/main.go", true},
		{"./internal/config.go", true},
		{"internal/config.go", true},
		{"/path/to/file.tsx", true},
		{"file.py", true},
		{"/deep/nested/path/file.rs", true},
		{"a.b", true}, // minimal valid

		// Invalid paths
		{"", false},
		{"/src/config", false}, // no extension
		{"path/without/extension", false},
		{"/path/with<invalid>.go", false},
		{"/path/with>invalid.go", false},
		{"/path/with|invalid.go", false},
		{"/path/with*invalid.go", false},
		{"/path/with?invalid.go", false},
		{"/path/with\ninvalid.go", false},
		{"/path/with\tinvalid.go", false},
		{"example.com", false},                  // domain-like
		{"localhost.test", false},               // domain-like
		{"api.v1", false},                       // version-like
		{"v1.0", false},                         // version-like
		{"/path/file.verylongextension", false}, // extension too long
		{".", false},                            // just a dot
		{"file.", false},                        // extension empty
		{"file.g@", false},                      // non-alphanumeric char in extension
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			result := isValidFilePathForReservation(tc.path)
			t.Logf("RESERVATION_TEST: isValidFilePath | path=%q | result=%v", tc.path, result)
			if result != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, result)
			}
		})
	}
}

// TestGetActiveReservations tests getting a copy of active reservations.
func TestGetActiveReservations(t *testing.T) {
	w := NewFileReservationWatcher()

	// Add some test reservations
	w.mu.Lock()
	w.activeReservations["pane1"] = &PaneReservation{
		PaneID:        "pane1",
		AgentName:     "Agent1",
		Files:         []string{"/file1.go", "/file2.go"},
		ReservationID: []int{1, 2},
		LastActivity:  time.Now(),
	}
	w.activeReservations["pane2"] = &PaneReservation{
		PaneID:        "pane2",
		AgentName:     "Agent2",
		Files:         []string{"/file3.go"},
		ReservationID: []int{3},
		LastActivity:  time.Now(),
	}
	w.mu.Unlock()

	t.Run("returns copy", func(t *testing.T) {
		result := w.GetActiveReservations()
		t.Logf("RESERVATION_TEST: getActiveReservations | count=%d", len(result))

		if len(result) != 2 {
			t.Errorf("expected 2 reservations, got %d", len(result))
		}

		// Verify it's a copy by modifying the result
		result["pane1"].Files = append(result["pane1"].Files, "/modified.go")

		// Original should be unchanged
		w.mu.Lock()
		if len(w.activeReservations["pane1"].Files) != 2 {
			t.Error("original reservation should not be modified")
		}
		w.mu.Unlock()
	})

	t.Run("empty reservations", func(t *testing.T) {
		emptyW := NewFileReservationWatcher()
		result := emptyW.GetActiveReservations()
		t.Logf("RESERVATION_TEST: getActiveReservations_empty | count=%d", len(result))
		if len(result) != 0 {
			t.Errorf("expected 0 reservations, got %d", len(result))
		}
	})
}

// TestFileReservationWatcherStartStop tests the start/stop lifecycle.
func TestFileReservationWatcherStartStop(t *testing.T) {
	t.Run("start and stop", func(t *testing.T) {
		w := NewFileReservationWatcher(
			WithReservationPollInterval(50 * time.Millisecond),
		)

		w.Start(context.Background())
		t.Logf("RESERVATION_TEST: watcher started")

		// Let it run briefly
		time.Sleep(100 * time.Millisecond)

		w.Stop()
		t.Logf("RESERVATION_TEST: watcher stopped")

		// Verify it stopped and is restartable
		if w.stopCh != nil {
			t.Fatalf("expected stopCh to be cleared after Stop()")
		}
	})

	t.Run("start with nil context", func(t *testing.T) {
		w := NewFileReservationWatcher(
			WithReservationPollInterval(50 * time.Millisecond),
		)

		// Should not panic
		w.Start(context.TODO())
		time.Sleep(50 * time.Millisecond)
		w.Stop()

		if w.stopCh != nil {
			t.Fatalf("expected stopCh to be cleared after Stop()")
		}
	})

	t.Run("double start does not deadlock stop", func(t *testing.T) {
		w := NewFileReservationWatcher(
			WithReservationPollInterval(50 * time.Millisecond),
		)

		w.Start(context.Background())
		w.Start(context.Background()) // should be idempotent

		done := make(chan struct{})
		go func() {
			w.Stop()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Stop() hung after double Start()")
		}
	})

	t.Run("stop without start", func(t *testing.T) {
		w := NewFileReservationWatcher()
		// Should not panic
		w.Stop()
		t.Logf("RESERVATION_TEST: stop_without_start completed safely")
	})

	t.Run("start waits for concurrent stop", func(t *testing.T) {
		w := NewFileReservationWatcher()

		w.mu.Lock()
		w.stopCh = make(chan struct{})
		w.mu.Unlock()

		release := make(chan struct{})
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			<-release
		}()

		stopped := make(chan struct{})
		go func() {
			w.Stop()
			close(stopped)
		}()

		deadline := time.Now().Add(time.Second)
		for {
			w.mu.Lock()
			stopCh := w.stopCh
			w.mu.Unlock()
			if stopCh == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("Stop() did not begin before timeout")
			}
			time.Sleep(time.Millisecond)
		}

		started := make(chan struct{}, 1)
		go func() {
			w.Start(context.Background())
			started <- struct{}{}
		}()

		select {
		case <-started:
			t.Fatal("Start() returned before Stop() completed")
		case <-time.After(50 * time.Millisecond):
		}

		close(release)

		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("Stop() did not return after worker released")
		}

		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("Start() did not resume after Stop() completed")
		}

		w.Stop()
	})
}

func TestCheckPaneOutputsScansAgentPanes(t *testing.T) {
	t.Parallel()

	server := newWatcherMCPServer(t, map[string]watcherToolHandler{
		"file_reservation_paths": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ReservationResult{
				Granted: []agentmail.FileReservation{
					{ID: 11, PathPattern: "/watched.go", AgentName: "TestAgent", Exclusive: true},
				},
			}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	w := NewFileReservationWatcher(
		WithWatcherClient(client),
		WithProjectDir("/test/project"),
		WithAgentName("TestAgent"),
	)

	var captured []string
	w.listAllPanes = func(ctx context.Context) (map[string][]tmux.Pane, error) {
		return map[string][]tmux.Pane{
			"test-session": {
				{ID: "%1", Type: tmux.AgentClaude},
				{ID: "%2", Type: tmux.AgentUser},
			},
		}, nil
	}
	w.capturePaneOutput = func(ctx context.Context, paneID string, lines int) (string, error) {
		captured = append(captured, paneID)
		return "Modified /watched.go", nil
	}

	w.checkPaneOutputs(context.Background())

	if len(captured) != 1 || captured[0] != "%1" {
		t.Fatalf("expected only agent pane to be captured, got %v", captured)
	}

	reservations := w.GetActiveReservations()
	got := reservations["%1"]
	if got == nil {
		t.Fatal("expected reservation for scanned agent pane")
	}
	if len(got.Files) != 1 || got.Files[0] != "/watched.go" {
		t.Fatalf("expected watched file to be reserved, got %v", got.Files)
	}
	if len(got.ReservationID) != 1 || got.ReservationID[0] != 11 {
		t.Fatalf("expected granted reservation ID to be tracked, got %v", got.ReservationID)
	}
}

func TestCheckPaneOutputsRespectsSessionFilter(t *testing.T) {
	t.Parallel()

	server := newWatcherMCPServer(t, map[string]watcherToolHandler{
		"file_reservation_paths": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ReservationResult{
				Granted: []agentmail.FileReservation{
					{ID: 12, PathPattern: "/watched.go", AgentName: "TestAgent", Exclusive: true},
				},
			}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	w := NewFileReservationWatcher(
		WithWatcherClient(client),
		WithProjectDir("/test/project"),
		WithAgentName("TestAgent"),
		WithSessionFilter("target-session"),
	)

	var captured []string
	w.listAllPanes = func(ctx context.Context) (map[string][]tmux.Pane, error) {
		return map[string][]tmux.Pane{
			"other-session": {
				{ID: "%1", Type: tmux.AgentClaude},
			},
			"target-session": {
				{ID: "%2", Type: tmux.AgentClaude},
			},
		}, nil
	}
	w.capturePaneOutput = func(ctx context.Context, paneID string, lines int) (string, error) {
		captured = append(captured, paneID)
		return "Modified /watched.go", nil
	}

	w.checkPaneOutputs(context.Background())

	if len(captured) != 1 || captured[0] != "%2" {
		t.Fatalf("expected only target-session pane to be captured, got %v", captured)
	}

	reservations := w.GetActiveReservations()
	if reservations["%1"] != nil {
		t.Fatal("expected other-session pane to be ignored")
	}
	if reservations["%2"] == nil {
		t.Fatal("expected reservation for target-session pane")
	}
}

// TestOnFileEditNoClient tests OnFileEdit behavior without a client.
func TestOnFileEditNoClient(t *testing.T) {
	w := NewFileReservationWatcher(
		WithProjectDir("/test/project"),
		WithAgentName("TestAgent"),
	)

	// Should not panic when client is nil
	ctx := context.Background()
	pane := tmux.Pane{
		ID:   "%1",
		Type: tmux.AgentClaude,
	}

	// This should be a no-op since client is nil
	w.OnFileEdit(ctx, "test-session", pane, []string{"/file.go"})
	t.Logf("RESERVATION_TEST: OnFileEdit_no_client | completed without panic")

	// Verify no reservations were added
	reservations := w.GetActiveReservations()
	if len(reservations) != 0 {
		t.Errorf("expected 0 reservations with nil client, got %d", len(reservations))
	}
}

// TestOnFileEditNoProjectDir tests OnFileEdit behavior without a project directory.
func TestOnFileEditNoProjectDir(t *testing.T) {
	client := agentmail.NewClient()
	w := NewFileReservationWatcher(
		WithWatcherClient(client),
		// No project dir
	)

	ctx := context.Background()
	pane := tmux.Pane{
		ID:   "%1",
		Type: tmux.AgentClaude,
	}

	// This should be a no-op since projectDir is empty
	w.OnFileEdit(ctx, "test-session", pane, []string{"/file.go"})
	t.Logf("RESERVATION_TEST: OnFileEdit_no_projectDir | completed without panic")

	reservations := w.GetActiveReservations()
	if len(reservations) != 0 {
		t.Errorf("expected 0 reservations with empty projectDir, got %d", len(reservations))
	}
}

func TestOnFileEditConflictInvokesCallbackAndTracksGrantedReservations(t *testing.T) {
	t.Parallel()

	server := newWatcherMCPServer(t, map[string]watcherToolHandler{
		"file_reservation_paths": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ReservationResult{
				Granted: []agentmail.FileReservation{
					{ID: 7, PathPattern: "/granted.go", AgentName: "TestAgent", Exclusive: true},
				},
				Conflicts: []agentmail.ReservationConflict{
					{Path: "/blocked.go", Holders: []string{"BlueLake"}},
				},
			}, nil
		},
		"list_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return []agentmail.FileReservation{
				{ID: 99, PathPattern: "/blocked.go", AgentName: "BlueLake"},
			}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	callbackDone := make(chan struct{})
	var w *FileReservationWatcher

	w = NewFileReservationWatcher(
		WithWatcherClient(client),
		WithProjectDir("/test/project"),
		WithAgentName("TestAgent"),
		WithConflictCallback(func(conflict FileConflict) {
			if conflict.Path != "/blocked.go" {
				t.Errorf("unexpected conflict path: %s", conflict.Path)
			}
			if conflict.RequestorAgent != "TestAgent" {
				t.Errorf("unexpected requestor agent: %s", conflict.RequestorAgent)
			}
			if len(conflict.Holders) != 1 || conflict.Holders[0] != "BlueLake" {
				t.Errorf("unexpected holders: %v", conflict.Holders)
			}

			// The callback must run without the watcher mutex held.
			reservations := w.GetActiveReservations()
			got := reservations["%1"]
			if got == nil {
				t.Fatal("expected pane reservation to exist during callback")
			}
			if len(got.Files) != 1 || got.Files[0] != "/granted.go" {
				t.Fatalf("expected granted file to be tracked, got %v", got.Files)
			}
			close(callbackDone)
		}),
	)

	pane := tmux.Pane{ID: "%1", Type: tmux.AgentClaude}
	done := make(chan struct{})
	go func() {
		w.OnFileEdit(context.Background(), "test-session", pane, []string{"/granted.go", "/blocked.go"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnFileEdit hung while processing conflict callback")
	}

	select {
	case <-callbackDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected conflict callback to be invoked")
	}

	reservations := w.GetActiveReservations()
	got := reservations["%1"]
	if got == nil {
		t.Fatal("expected reservation for pane %1")
	}
	if len(got.ReservationID) != 1 || got.ReservationID[0] != 7 {
		t.Fatalf("expected granted reservation ID to be tracked, got %v", got.ReservationID)
	}
}

func TestReleaseIdleReservationsDoesNotHoldWatcherLock(t *testing.T) {
	t.Parallel()

	releaseStarted := make(chan struct{})
	releaseAllowed := make(chan struct{})

	server := newWatcherMCPServer(t, map[string]watcherToolHandler{
		"release_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			close(releaseStarted)
			<-releaseAllowed
			return map[string]interface{}{"released": 1}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	w := NewFileReservationWatcher(
		WithWatcherClient(client),
		WithProjectDir("/test/project"),
		WithIdleTimeout(time.Minute),
	)

	w.mu.Lock()
	w.activeReservations["%1"] = &PaneReservation{
		PaneID:        "%1",
		AgentName:     "TestAgent",
		Files:         []string{"/file.go"},
		ReservationID: []int{1},
		LastActivity:  time.Now().Add(-2 * time.Minute),
	}
	w.mu.Unlock()

	done := make(chan struct{})
	go func() {
		w.releaseIdleReservations(context.Background())
		close(done)
	}()

	select {
	case <-releaseStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("release_file_reservations was not called")
	}

	getDone := make(chan struct{})
	go func() {
		_ = w.GetActiveReservations()
		close(getDone)
	}()

	select {
	case <-getDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("GetActiveReservations blocked behind releaseIdleReservations")
	}

	close(releaseAllowed)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("releaseIdleReservations did not finish")
	}
}

func TestReleaseIdleReservationsKeepsStateOnZeroRelease(t *testing.T) {
	t.Parallel()

	server := newWatcherMCPServer(t, map[string]watcherToolHandler{
		"release_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return map[string]interface{}{"released": 0}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	w := NewFileReservationWatcher(
		WithWatcherClient(client),
		WithProjectDir("/test/project"),
		WithIdleTimeout(time.Minute),
	)

	w.mu.Lock()
	w.activeReservations["%1"] = &PaneReservation{
		PaneID:        "%1",
		AgentName:     "TestAgent",
		Files:         []string{"/file.go"},
		ReservationID: []int{1},
		LastActivity:  time.Now().Add(-2 * time.Minute),
	}
	w.mu.Unlock()

	w.releaseIdleReservations(context.Background())

	reservations := w.GetActiveReservations()
	if reservations["%1"] == nil {
		t.Fatal("expected reservation to remain tracked after zero-count release")
	}
}

func TestReleaseAllReservationsKeepsStateOnFailure(t *testing.T) {
	t.Parallel()

	server := newWatcherMCPServer(t, map[string]watcherToolHandler{
		"release_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, &agentmail.JSONRPCError{Code: -32000, Message: "release failed"}
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	w := NewFileReservationWatcher(
		WithWatcherClient(client),
		WithProjectDir("/test/project"),
	)

	w.mu.Lock()
	w.activeReservations["%1"] = &PaneReservation{
		PaneID:        "%1",
		AgentName:     "TestAgent",
		Files:         []string{"/file.go"},
		ReservationID: []int{1},
		LastActivity:  time.Now(),
	}
	w.mu.Unlock()

	w.releaseAllReservations()

	reservations := w.GetActiveReservations()
	if reservations["%1"] == nil {
		t.Fatal("expected reservation to remain tracked after release failure")
	}
}

func TestRenewReservationsUsesReservationIDs(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		args []map[string]interface{}
	)

	server := newWatcherMCPServer(t, map[string]watcherToolHandler{
		"renew_file_reservations": func(callArgs map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			mu.Lock()
			copied := make(map[string]interface{}, len(callArgs))
			for k, v := range callArgs {
				copied[k] = v
			}
			args = append(args, copied)
			mu.Unlock()

			renewed := 0
			if rawIDs, ok := callArgs["file_reservation_ids"].([]interface{}); ok {
				renewed = len(rawIDs)
			}
			return agentmail.RenewReservationsResult{Renewed: renewed}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	w := NewFileReservationWatcher(
		WithWatcherClient(client),
		WithProjectDir("/test/project"),
		WithReservationTTL(15*time.Minute),
	)

	w.mu.Lock()
	w.activeReservations["%1"] = &PaneReservation{
		PaneID:        "%1",
		AgentName:     "SessionAgent",
		ReservationID: []int{1, 2},
	}
	w.activeReservations["%2"] = &PaneReservation{
		PaneID:        "%2",
		AgentName:     "SessionAgent",
		ReservationID: []int{3},
	}
	w.mu.Unlock()

	if err := w.RenewReservations(context.Background()); err != nil {
		t.Fatalf("RenewReservations returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(args) != 2 {
		t.Fatalf("expected 2 renew calls, got %d", len(args))
	}
	for _, callArgs := range args {
		rawIDs, ok := callArgs["file_reservation_ids"].([]interface{})
		if !ok || len(rawIDs) == 0 {
			t.Fatalf("expected file_reservation_ids in renew call, got %v", callArgs)
		}
	}
}

func TestRenewReservationsReturnsErrorOnIncompleteRenew(t *testing.T) {
	t.Parallel()

	server := newWatcherMCPServer(t, map[string]watcherToolHandler{
		"renew_file_reservations": func(callArgs map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.RenewReservationsResult{Renewed: 0}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	w := NewFileReservationWatcher(
		WithWatcherClient(client),
		WithProjectDir("/test/project"),
		WithReservationTTL(15*time.Minute),
	)

	w.mu.Lock()
	w.activeReservations["%1"] = &PaneReservation{
		PaneID:        "%1",
		AgentName:     "SessionAgent",
		ReservationID: []int{1, 2},
	}
	w.mu.Unlock()

	if err := w.RenewReservations(context.Background()); err == nil {
		t.Fatal("expected incomplete renew to return an error")
	}
}

func TestRenewReservationsContinuesAfterPaneLevelFailure(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		seenIDs [][]int
	)

	server := newWatcherMCPServer(t, map[string]watcherToolHandler{
		"renew_file_reservations": func(callArgs map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			rawIDs, _ := callArgs["file_reservation_ids"].([]interface{})
			ids := make([]int, 0, len(rawIDs))
			for _, raw := range rawIDs {
				if id, ok := raw.(float64); ok {
					ids = append(ids, int(id))
				}
			}

			mu.Lock()
			seenIDs = append(seenIDs, ids)
			mu.Unlock()

			if len(ids) == 1 && ids[0] == 1 {
				return nil, &agentmail.JSONRPCError{Code: -32000, Message: "renew failed"}
			}
			return agentmail.RenewReservationsResult{Renewed: len(ids)}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	w := NewFileReservationWatcher(
		WithWatcherClient(client),
		WithProjectDir("/test/project"),
		WithReservationTTL(15*time.Minute),
	)

	w.mu.Lock()
	w.activeReservations["%1"] = &PaneReservation{
		PaneID:        "%1",
		AgentName:     "SessionAgent",
		ReservationID: []int{1},
	}
	w.activeReservations["%2"] = &PaneReservation{
		PaneID:        "%2",
		AgentName:     "SessionAgent",
		ReservationID: []int{2},
	}
	w.mu.Unlock()

	err := w.RenewReservations(context.Background())
	if err == nil {
		t.Fatal("expected renew error when one pane fails")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenIDs) != 2 {
		t.Fatalf("expected both panes to be renewed despite one failure, saw %d calls: %#v", len(seenIDs), seenIDs)
	}
}

func TestRecordPaneOutputUnchangedOutputDoesNotRefreshReservation(t *testing.T) {
	t.Parallel()

	w := NewFileReservationWatcher()
	lastActivity := time.Now().Add(-time.Minute)
	output := "Edited: /file.go"

	w.mu.Lock()
	w.paneOutputs["%1"] = output
	w.activeReservations["%1"] = &PaneReservation{
		PaneID:       "%1",
		AgentName:    "TestAgent",
		LastActivity: lastActivity,
		LastOutput:   output,
	}
	w.mu.Unlock()

	if processed := w.recordPaneOutput("%1", output, time.Now()); processed {
		t.Fatal("expected unchanged pane output to be ignored")
	}

	reservations := w.GetActiveReservations()
	got := reservations["%1"]
	if got == nil {
		t.Fatal("expected reservation to remain tracked")
	}
	if !got.LastActivity.Equal(lastActivity) {
		t.Fatalf("expected LastActivity to remain %v, got %v", lastActivity, got.LastActivity)
	}
}

func TestRecordPaneOutputChangedOutputRefreshesReservation(t *testing.T) {
	t.Parallel()

	w := NewFileReservationWatcher()
	lastActivity := time.Now().Add(-time.Minute)
	oldOutput := "Edited: /file.go"
	newOutput := "Thinking about next step"
	now := time.Now()

	w.mu.Lock()
	w.paneOutputs["%1"] = oldOutput
	w.activeReservations["%1"] = &PaneReservation{
		PaneID:       "%1",
		AgentName:    "TestAgent",
		LastActivity: lastActivity,
		LastOutput:   oldOutput,
	}
	w.mu.Unlock()

	if processed := w.recordPaneOutput("%1", newOutput, now); !processed {
		t.Fatal("expected changed pane output to be processed")
	}

	reservations := w.GetActiveReservations()
	got := reservations["%1"]
	if got == nil {
		t.Fatal("expected reservation to remain tracked")
	}
	if !got.LastActivity.Equal(now) {
		t.Fatalf("expected LastActivity to refresh to %v, got %v", now, got.LastActivity)
	}
	if got.LastOutput != newOutput {
		t.Fatalf("expected LastOutput %q, got %q", newOutput, got.LastOutput)
	}
}

func TestReleaseIdleReservationsKeepsPaneOutputStateToSuppressStaleOutput(t *testing.T) {
	t.Parallel()

	server := newWatcherMCPServer(t, map[string]watcherToolHandler{
		"release_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return map[string]interface{}{"released": 1}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	w := NewFileReservationWatcher(
		WithWatcherClient(client),
		WithProjectDir("/test/project"),
		WithIdleTimeout(time.Minute),
	)

	output := "Edited: /file.go"
	w.mu.Lock()
	w.paneOutputs["%1"] = output
	w.activeReservations["%1"] = &PaneReservation{
		PaneID:        "%1",
		AgentName:     "TestAgent",
		Files:         []string{"/file.go"},
		ReservationID: []int{1},
		LastActivity:  time.Now().Add(-2 * time.Minute),
		LastOutput:    output,
	}
	w.mu.Unlock()

	w.releaseIdleReservations(context.Background())

	reservations := w.GetActiveReservations()
	if reservations["%1"] != nil {
		t.Fatal("expected idle reservation to be released")
	}

	w.mu.Lock()
	trackedOutput := w.paneOutputs["%1"]
	w.mu.Unlock()
	if trackedOutput != output {
		t.Fatalf("expected pane output state to be retained after release, got %q", trackedOutput)
	}

	if processed := w.recordPaneOutput("%1", output, time.Now()); processed {
		t.Fatal("expected stale unchanged output to be ignored after release")
	}
}

func TestPrepareReservationAttemptSeedsLastOutputForNewReservation(t *testing.T) {
	t.Parallel()

	w := NewFileReservationWatcher()
	now := time.Now()
	output := "Edited: /file.go"

	agentName, newFiles := w.prepareReservationAttempt("%1", "test-session", output, []string{"/file.go"}, now)
	if agentName != "test-session_%1" {
		t.Fatalf("expected derived agent name, got %q", agentName)
	}
	if len(newFiles) != 1 || newFiles[0] != "/file.go" {
		t.Fatalf("expected new files to include /file.go, got %v", newFiles)
	}

	reservations := w.GetActiveReservations()
	got := reservations["%1"]
	if got == nil {
		t.Fatal("expected reservation to be created")
	}
	if got.LastOutput != output {
		t.Fatalf("expected LastOutput %q, got %q", output, got.LastOutput)
	}
	if !got.LastActivity.Equal(now) {
		t.Fatalf("expected LastActivity %v, got %v", now, got.LastActivity)
	}
}

// TestFilePathPatterns tests that all pattern categories are present.
func TestFilePathPatterns(t *testing.T) {
	expectedAgents := []string{"claude", "codex", "gemini", "*"}

	for _, agent := range expectedAgents {
		t.Run(agent, func(t *testing.T) {
			patterns, ok := filePathPatterns[agent]
			t.Logf("RESERVATION_TEST: filePathPatterns | agent=%s | found=%v | count=%d",
				agent, ok, len(patterns))
			if !ok {
				t.Errorf("expected patterns for agent %s", agent)
			}
			if len(patterns) == 0 {
				t.Errorf("expected non-empty patterns for agent %s", agent)
			}
		})
	}
}

// TestPaneReservationStruct tests the PaneReservation struct.
func TestPaneReservationStruct(t *testing.T) {
	now := time.Now()
	pr := &PaneReservation{
		PaneID:        "%1",
		AgentName:     "TestAgent",
		Files:         []string{"/a.go", "/b.go"},
		ReservationID: []int{1, 2},
		LastActivity:  now,
		LastOutput:    "output hash",
	}

	t.Logf("RESERVATION_TEST: PaneReservation | PaneID=%s | AgentName=%s | Files=%v | IDs=%v",
		pr.PaneID, pr.AgentName, pr.Files, pr.ReservationID)

	if pr.PaneID != "%1" {
		t.Errorf("unexpected PaneID: %s", pr.PaneID)
	}
	if pr.AgentName != "TestAgent" {
		t.Errorf("unexpected AgentName: %s", pr.AgentName)
	}
	if len(pr.Files) != 2 {
		t.Errorf("unexpected Files count: %d", len(pr.Files))
	}
	if len(pr.ReservationID) != 2 {
		t.Errorf("unexpected ReservationID count: %d", len(pr.ReservationID))
	}
	if !pr.LastActivity.Equal(now) {
		t.Error("LastActivity not set correctly")
	}
}
