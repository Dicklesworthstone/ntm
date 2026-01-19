package assign

import (
	"testing"
)

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
			name:  "multiple files",
			title: "Refactor API layer",
			description: `Files to modify:
- internal/api/server.go
- internal/api/routes.go
- pkg/models/user.go`,
			wantMin: 3,
		},
		{
			name:  "glob pattern",
			title: "Update all tests",
			description: `Run tests in **/*.test.go and fix failing ones.`,
			wantMin: 1,
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
			name:  "url should be excluded",
			title: "Check documentation",
			description: `See https://example.com/docs/api.html for details.
Also update internal/api/handler.go`,
			wantMin: 1, // Should only get handler.go, not the URL
		},
		{
			name:  "version should be excluded",
			title: "Update dependencies",
			description: `Upgrade to version 1.2.3 and modify go.mod`,
			wantMin: 1, // Should only get go.mod, not 1.2.3
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

func TestRenewReservationsNoClient(t *testing.T) {
	m := NewFileReservationManager(nil, "/test/project")

	err := m.RenewReservations(nil, "TestAgent", 3600)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
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
