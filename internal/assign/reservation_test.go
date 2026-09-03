package assign

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
)

func TestRecordGrantedReservationTracksEarliestLeaseExpiry(t *testing.T) {
	t.Parallel()
	result := &FileReservationResult{}
	later := time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)
	earlier := later.Add(-time.Hour)
	recordGrantedReservation(result, agentmail.FileReservation{
		ID: 1, PathPattern: "internal/**", ExpiresTS: agentmail.FlexTime{Time: later},
	})
	recordGrantedReservation(result, agentmail.FileReservation{
		ID: 2, PathPattern: "cmd/**", ExpiresTS: agentmail.FlexTime{Time: earlier},
	})
	if result.ExpiresAt == nil || !result.ExpiresAt.Equal(earlier) {
		t.Fatalf("ExpiresAt=%v, want %v", result.ExpiresAt, earlier)
	}
	if len(result.ReservationIDs) != 2 || len(result.GrantedPaths) != 2 {
		t.Fatalf("result=%+v", result)
	}
}

type assignToolHandler func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError)

func newAssignMCPServer(t *testing.T, handlers map[string]assignToolHandler) *httptest.Server {
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
			if toolName == "ensure_project" {
				humanKey, _ := args["human_key"].(string)
				resultJSON, _ := json.Marshal(agentmail.Project{ID: 1, HumanKey: humanKey})
				_ = json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{
					JSONRPC: "2.0", ID: req.ID,
					Result: json.RawMessage(resultJSON),
				})
				return
			}
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

func TestExtractFilePaths(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		description string
		wantPaths   []string
		wantMin     int // minimum number of paths expected
	}{
		{
			name:  "explicit go file",
			title: "Fix bug in handler",
			description: `Update the handler in src/api/handler.go to fix
the authentication bug.`,
			wantPaths: []string{"src/api/handler.go"},
			wantMin:   1,
		},
		{
			name:        "file with line suffix",
			title:       "Fix scanner issues",
			description: `Review internal/scanner/scanner.go:42 and internal/cli/root.go:10:3`,
			wantPaths:   []string{"internal/scanner/scanner.go", "internal/cli/root.go"},
			wantMin:     2,
		},
		{
			name:  "multiple files",
			title: "Refactor API layer",
			description: `Files to modify:
- internal/api/server.go
- internal/api/routes.go
- pkg/models/user.go`,
			wantMin: 3,
		},
		{
			name:        "glob pattern",
			title:       "Update all tests",
			description: `Run tests in **/*.test.go and fix failing ones.`,
			wantMin:     1,
		},
		{
			name:        "directory reference",
			title:       "Refactor internal/cli package",
			description: `Move commands to internal/cli for better organization.`,
			wantMin:     1,
		},
		{
			name:        "no paths",
			title:       "Think about architecture",
			description: `Consider how the system should evolve.`,
			wantPaths:   []string{},
			wantMin:     0,
		},
		{
			name:        "typescript files",
			title:       "Update React components",
			description: `Modify src/components/Button.tsx and lib/utils.ts`,
			wantMin:     2,
		},
		{
			name:        "config files",
			title:       "Update configuration",
			description: `Edit config.json and .env.local`,
			wantMin:     2,
		},
		{
			name:        "consecutive paths overlap bug",
			title:       "Fix paths",
			description: `Modify src/main.go src/util.go`,
			wantPaths:   []string{"src/main.go", "src/util.go"},
			wantMin:     2,
		},
		{
			name:  "url should be excluded",
			title: "Check documentation",
			description: `See https://example.com/docs/api.html for details.
Also update internal/api/handler.go`,
			wantMin: 1, // Should only get handler.go, not the URL
		},
		{
			name:        "version should be excluded",
			title:       "Update dependencies",
			description: `Upgrade to version 1.2.3 and modify go.mod`,
			wantMin:     1, // Should only get go.mod, not 1.2.3
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := ExtractFilePaths(tt.title, tt.description)

			if len(paths) < tt.wantMin {
				t.Errorf("ExtractFilePaths() got %d paths, want at least %d", len(paths), tt.wantMin)
				t.Logf("Got: %v", paths)
			}

			// If specific paths are expected, check they're included
			if len(tt.wantPaths) > 0 {
				for _, want := range tt.wantPaths {
					found := false
					for _, got := range paths {
						if got == want {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("ExtractFilePaths() missing expected path %q, got %v", want, paths)
					}
				}
			}
		})
	}
}

func TestIsValidPath(t *testing.T) {
	tests := []struct {
		path  string
		valid bool
	}{
		{"src/api/handler.go", true},
		{"internal/cli/cmd.go", true},
		{"config.json", true},
		{"**/*.go", true},
		{"http://example.com/file.go", false},
		{"https://github.com/user/repo", false},
		{"1.2.3", false},
		{"e.g.", false},
		{"etc.", false},
		{"fig.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isValidPath(tt.path)
			if got != tt.valid {
				t.Errorf("isValidPath(%q) = %v, want %v", tt.path, got, tt.valid)
			}
		})
	}
}

func TestNewFileReservationManager(t *testing.T) {
	m := NewFileReservationManager(nil, "/test/project")

	if m.projectKey != "/test/project" {
		t.Errorf("projectKey = %q, want %q", m.projectKey, "/test/project")
	}
	if m.ttlSeconds != 3600 {
		t.Errorf("ttlSeconds = %d, want 3600", m.ttlSeconds)
	}
}

func TestSetTTL(t *testing.T) {
	m := NewFileReservationManager(nil, "/test/project")

	m.SetTTL(7200)
	if m.ttlSeconds != 7200 {
		t.Errorf("ttlSeconds = %d, want 7200", m.ttlSeconds)
	}

	// Invalid TTL should be ignored
	m.SetTTL(0)
	if m.ttlSeconds != 7200 {
		t.Errorf("ttlSeconds = %d after SetTTL(0), want 7200", m.ttlSeconds)
	}

	m.SetTTL(-1)
	if m.ttlSeconds != 7200 {
		t.Errorf("ttlSeconds = %d after SetTTL(-1), want 7200", m.ttlSeconds)
	}
}

func TestFileReservationResultFields(t *testing.T) {
	result := FileReservationResult{
		BeadID:         "bd-1234",
		AgentName:      "TestAgent",
		RequestedPaths: []string{"src/api/handler.go"},
		GrantedPaths:   []string{"src/api/handler.go"},
		ReservationIDs: []int{1, 2, 3},
		Success:        true,
	}

	if result.BeadID != "bd-1234" {
		t.Errorf("BeadID = %q, want %q", result.BeadID, "bd-1234")
	}
	if !result.Success {
		t.Error("Success should be true")
	}
	if len(result.ReservationIDs) != 3 {
		t.Errorf("ReservationIDs count = %d, want 3", len(result.ReservationIDs))
	}
}

func TestReserveForBeadNoPaths(t *testing.T) {
	m := NewFileReservationManager(nil, "/test/project")

	result, err := m.ReserveForBead(nil, "bd-test", "No files", "Just thinking about stuff", "TestAgent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Success {
		t.Error("Success should be true when no paths to reserve")
	}
	if len(result.RequestedPaths) != 0 {
		t.Errorf("RequestedPaths should be empty, got %v", result.RequestedPaths)
	}
}

func TestReserveForBeadNoClient(t *testing.T) {
	m := NewFileReservationManager(nil, "/test/project")

	result, err := m.ReserveForBead(nil, "bd-test", "Fix handler", "Update src/api/handler.go", "TestAgent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should extract paths but fail gracefully without client
	if len(result.RequestedPaths) == 0 {
		t.Error("Should have extracted paths")
	}
	if result.Success {
		t.Error("Success should be false without client")
	}
	if result.Error == "" {
		t.Error("Error should be set without client")
	}
}

func TestCollectGrantedReservationsValidatesDispatchAuthority(t *testing.T) {
	valid := agentmail.FileReservation{
		ID: 71, ProjectID: 9, AgentName: "TestAgent", PathPattern: "src/api/handler.go", Exclusive: true,
		ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
		Reason:    "bead assignment: bd-test",
	}
	result := &FileReservationResult{}
	now := time.Now().UTC()
	if err := collectGrantedReservations(result, []agentmail.FileReservation{valid}, []string{"src/api/handler.go"}, "TestAgent", "bead assignment: bd-test", 9, now, false); err != nil {
		t.Fatalf("valid grant: %v", err)
	}
	if !reflect.DeepEqual(result.ReservationIDs, []int{71}) || !reflect.DeepEqual(result.GrantedPaths, []string{"src/api/handler.go"}) {
		t.Fatalf("valid grant result = %+v", result)
	}

	tests := []struct {
		name      string
		grants    []agentmail.FileReservation
		requested []string
	}{
		{name: "missing id", grants: []agentmail.FileReservation{func() agentmail.FileReservation { grant := valid; grant.ID = 0; return grant }()}, requested: []string{"src/api/handler.go"}},
		{name: "missing project", grants: []agentmail.FileReservation{func() agentmail.FileReservation { grant := valid; grant.ProjectID = 0; return grant }()}, requested: []string{"src/api/handler.go"}},
		{name: "wrong project", grants: []agentmail.FileReservation{func() agentmail.FileReservation { grant := valid; grant.ProjectID = 10; return grant }()}, requested: []string{"src/api/handler.go"}},
		{name: "missing expiry", grants: []agentmail.FileReservation{func() agentmail.FileReservation { grant := valid; grant.ExpiresTS = agentmail.FlexTime{}; return grant }()}, requested: []string{"src/api/handler.go"}},
		{name: "expired", grants: []agentmail.FileReservation{func() agentmail.FileReservation {
			grant := valid
			grant.ExpiresTS = agentmail.FlexTime{Time: now.Add(-time.Second)}
			return grant
		}()}, requested: []string{"src/api/handler.go"}},
		{name: "released", grants: []agentmail.FileReservation{func() agentmail.FileReservation {
			grant := valid
			released := agentmail.FlexTime{Time: now}
			grant.ReleasedTS = &released
			return grant
		}()}, requested: []string{"src/api/handler.go"}},
		{name: "nonexclusive", grants: []agentmail.FileReservation{func() agentmail.FileReservation { grant := valid; grant.Exclusive = false; return grant }()}, requested: []string{"src/api/handler.go"}},
		{name: "wrong agent", grants: []agentmail.FileReservation{func() agentmail.FileReservation { grant := valid; grant.AgentName = "OtherAgent"; return grant }()}, requested: []string{"src/api/handler.go"}},
		{name: "wrong reason", grants: []agentmail.FileReservation{func() agentmail.FileReservation {
			grant := valid
			grant.Reason = "bead assignment: bd-other"
			return grant
		}()}, requested: []string{"src/api/handler.go"}},
		{name: "unexpected path", grants: []agentmail.FileReservation{func() agentmail.FileReservation { grant := valid; grant.PathPattern = "src/api/other.go"; return grant }()}, requested: []string{"src/api/handler.go"}},
		{name: "duplicate id", grants: []agentmail.FileReservation{valid, valid}, requested: []string{"src/api/handler.go"}},
		{name: "duplicate path", grants: []agentmail.FileReservation{valid, func() agentmail.FileReservation { grant := valid; grant.ID = 72; return grant }()}, requested: []string{"src/api/handler.go"}},
		{name: "missing requested path", grants: []agentmail.FileReservation{valid}, requested: []string{"src/api/handler.go", "src/api/other.go"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := &FileReservationResult{}
			if err := collectGrantedReservations(got, test.grants, test.requested, "TestAgent", "bead assignment: bd-test", 9, now, false); err == nil {
				t.Fatalf("collectGrantedReservations() result=%+v, want validation error", got)
			}
		})
	}
}

func TestReleaseForBeadNoClient(t *testing.T) {
	m := NewFileReservationManager(nil, "/test/project")

	// Should succeed silently with no client
	err := m.ReleaseForBead(nil, "TestAgent", []int{1, 2, 3})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReleaseForBeadNoIDs(t *testing.T) {
	m := NewFileReservationManager(nil, "/test/project")

	// Should succeed silently with no IDs
	err := m.ReleaseForBead(nil, "TestAgent", nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReleaseByPathsNoClient(t *testing.T) {
	m := NewFileReservationManager(nil, "/test/project")

	err := m.ReleaseByPaths(nil, "TestAgent", []string{"src/api/handler.go"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReleaseForBeadIncompleteRelease(t *testing.T) {
	server := newAssignMCPServer(t, map[string]assignToolHandler{
		"release_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ReleaseReservationsResult{Released: 0}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	m := NewFileReservationManager(client, "/test/project")

	err := m.ReleaseForBead(nil, "TestAgent", []int{1, 2, 3})
	if err == nil {
		t.Fatal("expected incomplete release to return an error")
	}
}

func TestReleaseByPathsZeroRelease(t *testing.T) {
	server := newAssignMCPServer(t, map[string]assignToolHandler{
		"release_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ReleaseReservationsResult{Released: 0}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	m := NewFileReservationManager(client, "/test/project")

	err := m.ReleaseByPaths(nil, "TestAgent", []string{"src/api/handler.go"})
	if err == nil {
		t.Fatal("expected zero-count path release to return an error")
	}
}

func newReservationReconcileManager(t *testing.T, reservations []agentmail.FileReservation) *FileReservationManager {
	t.Helper()
	server := newAssignMCPServer(t, map[string]assignToolHandler{
		"list_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return reservations, nil
		},
	})
	t.Cleanup(server.Close)
	return NewFileReservationManager(agentmail.NewClient(agentmail.WithBaseURL(server.URL+"/")), "/test/project")
}

func TestReservationManagerRequiresExactProjectHumanKey(t *testing.T) {
	server := newAssignMCPServer(t, map[string]assignToolHandler{
		"ensure_project": func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.Project{ID: 7, HumanKey: ""}, nil
		},
	})
	defer server.Close()
	manager := NewFileReservationManager(agentmail.NewClient(agentmail.WithBaseURL(server.URL+"/")), "/test/project")
	if _, err := manager.ReconcileForBead(t.Context(), "bd-project", "AgentOne", []string{"internal/a.go"}); err == nil || !strings.Contains(err.Error(), "binding mismatch") {
		t.Fatalf("blank project binding error=%v", err)
	}
}

func reconcileReservation(id int, agentName, path, reason string) agentmail.FileReservation {
	return agentmail.FileReservation{
		ID:          id,
		ProjectID:   1,
		AgentName:   agentName,
		PathPattern: path,
		Exclusive:   true,
		Reason:      reason,
		ExpiresTS:   agentmail.FlexTime{Time: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
}

func exactReleaseBarrier(beadID, agentName string) *assignmentstore.Assignment {
	return &assignmentstore.Assignment{
		BeadID: beadID, AgentName: agentName, ClearState: assignmentstore.ClearStateReservationReleasing,
	}
}

func TestReleaseExactForBeadPathOnlyRequiresUniqueActiveLeasePerPath(t *testing.T) {
	for _, test := range []struct {
		name         string
		reservations []agentmail.FileReservation
		wantReleased []string
		wantErr      string
		wantReleases int
	}{
		{name: "absent is idempotent", wantReleased: []string{}},
		{
			name: "one exact lease",
			reservations: []agentmail.FileReservation{
				reconcileReservation(61, "AgentOne", "internal/shared.go", "bead assignment: bd-release"),
			},
			wantReleased: []string{"internal/shared.go"}, wantReleases: 1,
		},
		{
			name: "duplicate exact leases are ambiguous",
			reservations: []agentmail.FileReservation{
				reconcileReservation(62, "AgentOne", "internal/shared.go", "bead assignment: bd-release"),
				reconcileReservation(63, "AgentOne", "internal/shared.go", "bead assignment: bd-release"),
				reconcileReservation(64, "AgentOne", "internal/unique.go", "bead assignment: bd-release"),
			},
			wantErr: "ambiguous",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			listCalls := 0
			releaseCalls := 0
			server := newAssignMCPServer(t, map[string]assignToolHandler{
				"list_file_reservations": func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
					listCalls++
					if releaseCalls > 0 {
						return []agentmail.FileReservation{}, nil
					}
					return test.reservations, nil
				},
				"release_file_reservations": func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
					releaseCalls++
					return agentmail.ReleaseReservationsResult{Released: 1}, nil
				},
			})
			defer server.Close()
			manager := NewFileReservationManager(agentmail.NewClient(agentmail.WithBaseURL(server.URL+"/")), "/test/project")
			paths := []string{"internal/shared.go"}
			if test.name == "duplicate exact leases are ambiguous" {
				paths = append(paths, "internal/unique.go")
			}
			released, err := manager.ReleaseExactForBead(t.Context(), exactReleaseBarrier("bd-release", "AgentOne"), nil, paths)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ReleaseExactForBead() released=%v error=%v, want %q", released, err, test.wantErr)
				}
			} else if err != nil || !reflect.DeepEqual(released, test.wantReleased) {
				t.Fatalf("ReleaseExactForBead() released=%v error=%v, want %v", released, err, test.wantReleased)
			}
			if releaseCalls != test.wantReleases {
				t.Fatalf("release calls=%d, want %d", releaseCalls, test.wantReleases)
			}
			if test.wantErr != "" && listCalls != 1 {
				t.Fatalf("ambiguous cleanup list calls=%d, want one pre-release read", listCalls)
			}
		})
	}
}

func TestReleaseExactForBeadRecoversUniqueIDDriftAndRejectsAmbiguity(t *testing.T) {
	for _, test := range []struct {
		name         string
		list         func(listCall, releaseCalls int) []agentmail.FileReservation
		wantCalls    int
		wantErr      string
		wantReleased []string
	}{
		{
			name: "unique drift exists before release",
			list: func(_ int, releaseCalls int) []agentmail.FileReservation {
				if releaseCalls > 0 {
					return nil
				}
				return []agentmail.FileReservation{
					reconcileReservation(72, "AgentOne", "internal/shared.go", "bead assignment: bd-release"),
				}
			},
			wantCalls: 1, wantReleased: []string{"internal/shared.go"},
		},
		{
			name: "unique drift appears during verification",
			list: func(_ int, releaseCalls int) []agentmail.FileReservation {
				switch releaseCalls {
				case 0:
					return []agentmail.FileReservation{reconcileReservation(71, "AgentOne", "internal/shared.go", "bead assignment: bd-release")}
				case 1:
					return []agentmail.FileReservation{reconcileReservation(72, "AgentOne", "internal/shared.go", "bead assignment: bd-release")}
				default:
					return nil
				}
			},
			wantCalls: 2, wantReleased: []string{"internal/shared.go"},
		},
		{
			name: "ambiguous drift before release",
			list: func(int, int) []agentmail.FileReservation {
				return []agentmail.FileReservation{
					reconcileReservation(72, "AgentOne", "internal/shared.go", "bead assignment: bd-release"),
					reconcileReservation(73, "AgentOne", "internal/shared.go", "bead assignment: bd-release"),
				}
			},
			wantErr: "ambiguous",
		},
		{
			name: "ambiguous drift during verification",
			list: func(_ int, releaseCalls int) []agentmail.FileReservation {
				if releaseCalls == 0 {
					return []agentmail.FileReservation{reconcileReservation(71, "AgentOne", "internal/shared.go", "bead assignment: bd-release")}
				}
				return []agentmail.FileReservation{
					reconcileReservation(72, "AgentOne", "internal/shared.go", "bead assignment: bd-release"),
					reconcileReservation(73, "AgentOne", "internal/shared.go", "bead assignment: bd-release"),
				}
			},
			wantCalls: 1, wantErr: "ambiguous",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			listCalls := 0
			releaseCalls := 0
			server := newAssignMCPServer(t, map[string]assignToolHandler{
				"list_file_reservations": func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
					listCalls++
					return test.list(listCalls, releaseCalls), nil
				},
				"release_file_reservations": func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
					releaseCalls++
					return agentmail.ReleaseReservationsResult{Released: 1}, nil
				},
			})
			defer server.Close()
			manager := NewFileReservationManager(agentmail.NewClient(agentmail.WithBaseURL(server.URL+"/")), "/test/project")
			released, err := manager.ReleaseExactForBead(
				t.Context(), exactReleaseBarrier("bd-release", "AgentOne"), []int{71}, []string{"internal/shared.go"},
			)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ReleaseExactForBead() released=%v error=%v, want %q", released, err, test.wantErr)
				}
			} else if err != nil || !reflect.DeepEqual(released, test.wantReleased) {
				t.Fatalf("ReleaseExactForBead() released=%v error=%v, want %v", released, err, test.wantReleased)
			}
			if releaseCalls != test.wantCalls {
				t.Fatalf("release calls=%d, want %d", releaseCalls, test.wantCalls)
			}
		})
	}
}

func TestReleaseExactForBeadRequiresDurableBarrier(t *testing.T) {
	manager := newReservationReconcileManager(t, nil)
	_, err := manager.ReleaseExactForBead(t.Context(), &assignmentstore.Assignment{
		BeadID: "bd-release", AgentName: "AgentOne",
	}, []int{71}, []string{"internal/shared.go"})
	if err == nil || !strings.Contains(err.Error(), "no durable release barrier") {
		t.Fatalf("missing barrier error=%v", err)
	}
}

func TestReleaseExactForBeadReleasesDurablyBoundProjectMismatch(t *testing.T) {
	releaseCalls := 0
	server := newAssignMCPServer(t, map[string]assignToolHandler{
		"list_file_reservations": func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			if releaseCalls > 0 {
				return []agentmail.FileReservation{}, nil
			}
			reservation := reconcileReservation(142, "AgentOne", "internal/shared.go", "bead assignment: bd-release")
			reservation.ProjectID = 0
			return []agentmail.FileReservation{reservation}, nil
		},
		"release_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			releaseCalls++
			ids, _ := args["file_reservation_ids"].([]interface{})
			if len(ids) != 1 || int(ids[0].(float64)) != 142 {
				t.Fatalf("release reservation IDs=%v, want [142]", ids)
			}
			return agentmail.ReleaseReservationsResult{Released: 1}, nil
		},
	})
	defer server.Close()
	manager := NewFileReservationManager(agentmail.NewClient(agentmail.WithBaseURL(server.URL+"/")), "/test/project")
	released, err := manager.ReleaseExactForBead(
		t.Context(), exactReleaseBarrier("bd-release", "AgentOne"), []int{142}, []string{"internal/shared.go"},
	)
	if err != nil || !reflect.DeepEqual(released, []string{"internal/shared.go"}) || releaseCalls != 1 {
		t.Fatalf("project-mismatch release paths=%v calls=%d error=%v", released, releaseCalls, err)
	}
}

func TestReconcileForBeadReturnsOnlyExactActiveLeases(t *testing.T) {
	releasedAt := agentmail.FlexTime{Time: time.Now().UTC()}
	manager := newReservationReconcileManager(t, []agentmail.FileReservation{
		reconcileReservation(11, "AgentOne", "internal/cli/a.go", "bead assignment: bd-1"),
		reconcileReservation(12, "AgentOne", "internal/cli/b.go", "bead assignment: bd-1"),
		reconcileReservation(13, "OtherAgent", "internal/cli/a.go", "bead assignment: bd-1"),
		reconcileReservation(14, "AgentOne", "internal/cli/a.go", "bead assignment: bd-other"),
		func() agentmail.FileReservation {
			reservation := reconcileReservation(15, "AgentOne", "internal/cli/a.go", "bead assignment: bd-1")
			reservation.ReleasedTS = &releasedAt
			return reservation
		}(),
	})

	result, err := manager.ReconcileForBead(t.Context(), "bd-1", "AgentOne", []string{"internal/cli/a.go", "internal/cli/b.go"})
	if err != nil {
		t.Fatalf("ReconcileForBead() error = %v", err)
	}
	if !result.Success || len(result.ReservationIDs) != 2 || result.ReservationIDs[0] != 11 || result.ReservationIDs[1] != 12 {
		t.Fatalf("ReconcileForBead() result = %+v", result)
	}
	if result.ExpiresAt == nil || result.ExpiresAt.Year() != 2030 {
		t.Fatalf("ReconcileForBead() expiry = %v", result.ExpiresAt)
	}
}

func TestReconcileForBeadProvesAbsence(t *testing.T) {
	manager := newReservationReconcileManager(t, []agentmail.FileReservation{
		reconcileReservation(21, "OtherAgent", "internal/cli/a.go", "bead assignment: bd-1"),
	})

	result, err := manager.ReconcileForBead(t.Context(), "bd-1", "AgentOne", []string{"internal/cli/a.go"})
	if err != nil {
		t.Fatalf("ReconcileForBead() error = %v", err)
	}
	if result.Success || len(result.ReservationIDs) != 0 || len(result.GrantedPaths) != 0 {
		t.Fatalf("ReconcileForBead() absence result = %+v", result)
	}
}

func TestReconcileForBeadPreservesPartialLeaseHandles(t *testing.T) {
	manager := newReservationReconcileManager(t, []agentmail.FileReservation{
		reconcileReservation(31, "AgentOne", "internal/cli/a.go", "bead assignment: bd-1"),
	})

	result, err := manager.ReconcileForBead(t.Context(), "bd-1", "AgentOne", []string{"internal/cli/a.go", "internal/cli/b.go"})
	if err != nil {
		t.Fatalf("ReconcileForBead() error = %v", err)
	}
	if result.Success || len(result.ReservationIDs) != 1 || result.ReservationIDs[0] != 31 || result.Error == "" {
		t.Fatalf("ReconcileForBead() partial result = %+v", result)
	}
}

func TestReconcileForBeadCollectsDuplicateActiveLeaseHandlesForCleanup(t *testing.T) {
	manager := newReservationReconcileManager(t, []agentmail.FileReservation{
		reconcileReservation(41, "AgentOne", "internal/cli/a.go", "bead assignment: bd-1"),
		reconcileReservation(42, "AgentOne", "internal/cli/a.go", "bead assignment: bd-1"),
	})

	result, err := manager.ReconcileForBead(t.Context(), "bd-1", "AgentOne", []string{"internal/cli/a.go"})
	if err != nil || result == nil || !result.Success || !reflect.DeepEqual(result.ReservationIDs, []int{41, 42}) || !reflect.DeepEqual(result.GrantedPaths, []string{"internal/cli/a.go"}) {
		t.Fatalf("ReconcileForBead() duplicate result=%+v error=%v", result, err)
	}
}

func TestReconcileForBeadRejectsLeaseWithoutDurableID(t *testing.T) {
	manager := newReservationReconcileManager(t, []agentmail.FileReservation{
		reconcileReservation(0, "AgentOne", "internal/cli/a.go", "bead assignment: bd-1"),
	})

	result, err := manager.ReconcileForBead(t.Context(), "bd-1", "AgentOne", []string{"internal/cli/a.go"})
	if err == nil || result == nil || result.Error == "" {
		t.Fatalf("ReconcileForBead() missing ID result=%+v error=%v", result, err)
	}
}

func TestReconcileForBeadReturnsMalformedHandlesForFailClosedCleanup(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*agentmail.FileReservation)
	}{
		{name: "missing project", mutate: func(reservation *agentmail.FileReservation) { reservation.ProjectID = 0 }},
		{name: "wrong project", mutate: func(reservation *agentmail.FileReservation) { reservation.ProjectID = 2 }},
		{name: "nonexclusive", mutate: func(reservation *agentmail.FileReservation) { reservation.Exclusive = false }},
		{name: "missing expiry", mutate: func(reservation *agentmail.FileReservation) { reservation.ExpiresTS = agentmail.FlexTime{} }},
		{name: "expired", mutate: func(reservation *agentmail.FileReservation) {
			reservation.ExpiresTS = agentmail.FlexTime{Time: time.Now().UTC().Add(-time.Second)}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reservation := reconcileReservation(51, "AgentOne", "internal/cli/a.go", "bead assignment: bd-1")
			test.mutate(&reservation)
			manager := newReservationReconcileManager(t, []agentmail.FileReservation{reservation})
			result, err := manager.ReconcileForBead(t.Context(), "bd-1", "AgentOne", []string{"internal/cli/a.go"})
			if err == nil || result == nil || !reflect.DeepEqual(result.ReservationIDs, []int{51}) || result.Success {
				t.Fatalf("ReconcileForBead() malformed result=%+v error=%v", result, err)
			}
		})
	}
}

func TestReconcileForBeadRequiresOriginalRequestedPaths(t *testing.T) {
	manager := newReservationReconcileManager(t, nil)
	result, err := manager.ReconcileForBead(t.Context(), "bd-1", "AgentOne", []string{"", "  "})
	if err == nil || result == nil || result.Error == "" {
		t.Fatalf("ReconcileForBead() empty paths result=%+v error=%v", result, err)
	}
}

func TestRenewReservationsNoClient(t *testing.T) {
	m := NewFileReservationManager(nil, "/test/project")

	err := m.RenewReservations(nil, "TestAgent", 3600)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRenewReservationsZeroRenewed(t *testing.T) {
	server := newAssignMCPServer(t, map[string]assignToolHandler{
		"renew_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.RenewReservationsResult{Renewed: 0}, nil
		},
	})
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	m := NewFileReservationManager(client, "/test/project")

	err := m.RenewReservations(nil, "TestAgent", 3600)
	if err == nil {
		t.Fatal("expected zero-count renew to return an error")
	}
}

func TestExtractFilePathsEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantNone    bool
	}{
		{
			name:        "empty input",
			description: "",
			wantNone:    true,
		},
		{
			name:        "only whitespace",
			description: "   \n\t   ",
			wantNone:    true,
		},
		{
			name:        "code block",
			description: "```go\nfunc main() {}\n```",
			wantNone:    true,
		},
		{
			name:        "markdown link",
			description: "[link](http://example.com/path.go)",
			wantNone:    true, // URLs should be excluded
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := ExtractFilePaths("", tt.description)
			if tt.wantNone && len(paths) > 0 {
				t.Errorf("Expected no paths, got %v", paths)
			}
		})
	}
}

func TestExtractFilePathsDuplicates(t *testing.T) {
	description := `Update src/api/handler.go
Then fix src/api/handler.go again
And modify src/api/handler.go`

	paths := ExtractFilePaths("", description)

	// Count occurrences
	counts := make(map[string]int)
	for _, p := range paths {
		counts[p]++
	}

	for path, count := range counts {
		if count > 1 {
			t.Errorf("Path %q appears %d times, should be deduplicated", path, count)
		}
	}
}
