package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMarchingOrders_Valid(t *testing.T) {
	content := `# Agent assignments
pane:0 Review the authentication module
pane:1 Fix the database connection issue
pane:2 Write unit tests for the API layer
`
	path := writeTempFile(t, content)
	orders, err := ParseMarchingOrders(path)
	if err != nil {
		t.Fatalf("ParseMarchingOrders returned error: %v", err)
	}
	if len(orders) != 3 {
		t.Fatalf("expected 3 orders, got %d", len(orders))
	}
	if orders[0] != "Review the authentication module" {
		t.Errorf("pane 0: got %q", orders[0])
	}
	if orders[1] != "Fix the database connection issue" {
		t.Errorf("pane 1: got %q", orders[1])
	}
	if orders[2] != "Write unit tests for the API layer" {
		t.Errorf("pane 2: got %q", orders[2])
	}
}

func TestParseMarchingOrders_CommentsAndBlanks(t *testing.T) {
	content := `# This is a comment

# Another comment
pane:0 Do the thing

# trailing comment
`
	path := writeTempFile(t, content)
	orders, err := ParseMarchingOrders(path)
	if err != nil {
		t.Fatalf("ParseMarchingOrders returned error: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(orders))
	}
	if orders[0] != "Do the thing" {
		t.Errorf("pane 0: got %q", orders[0])
	}
}

func TestParseMarchingOrders_MissingFile(t *testing.T) {
	_, err := ParseMarchingOrders("/nonexistent/path/marching.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseMarchingOrders_InvalidFormat(t *testing.T) {
	cases := []struct {
		name    string
		content string
		errPart string
	}{
		{
			name:    "no pane prefix",
			content: "agent:0 do something\n",
			errPart: "expected 'pane:N <prompt>'",
		},
		{
			name:    "missing prompt",
			content: "pane:0\n",
			errPart: "missing prompt text",
		},
		{
			name:    "invalid number",
			content: "pane:abc do something\n",
			errPart: "invalid pane number",
		},
		{
			name:    "negative number",
			content: "pane:-1 do something\n",
			errPart: "must be >= 0",
		},
		{
			name:    "empty prompt text",
			content: "pane:0    \n",
			errPart: "missing prompt text",
		},
		{
			name:    "duplicate pane",
			content: "pane:0 first\npane:0 second\n",
			errPart: "duplicate entry",
		},
		{
			name:    "empty file",
			content: "# only comments\n\n",
			errPart: "no valid marching orders",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempFile(t, tc.content)
			_, err := ParseMarchingOrders(path)
			if err == nil {
				t.Fatal("expected error")
			}
			if !containsStr(err.Error(), tc.errPart) {
				t.Errorf("expected error containing %q, got %q", tc.errPart, err.Error())
			}
		})
	}
}

func TestParseMarchingOrders_NonContiguousPanes(t *testing.T) {
	content := `pane:0 First task
pane:5 Fifth task
pane:10 Tenth task
`
	path := writeTempFile(t, content)
	orders, err := ParseMarchingOrders(path)
	if err != nil {
		t.Fatalf("ParseMarchingOrders returned error: %v", err)
	}
	if len(orders) != 3 {
		t.Fatalf("expected 3 orders, got %d", len(orders))
	}
	if orders[5] != "Fifth task" {
		t.Errorf("pane 5: got %q", orders[5])
	}
	if orders[10] != "Tenth task" {
		t.Errorf("pane 10: got %q", orders[10])
	}
}

func TestParseMarchingOrders_PromptWithSpecialChars(t *testing.T) {
	content := `pane:0 Fix the bug in "auth.go" and run tests with --verbose
pane:1 Check the API endpoint /api/v1/users?limit=10&offset=0
`
	path := writeTempFile(t, content)
	orders, err := ParseMarchingOrders(path)
	if err != nil {
		t.Fatalf("ParseMarchingOrders returned error: %v", err)
	}
	if orders[0] != `Fix the bug in "auth.go" and run tests with --verbose` {
		t.Errorf("pane 0: got %q", orders[0])
	}
	if orders[1] != "Check the API endpoint /api/v1/users?limit=10&offset=0" {
		t.Errorf("pane 1: got %q", orders[1])
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "marching_orders.txt")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	return path
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
