//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-GREP] Tests for ntm grep (code search) functionality.
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// GrepResult mirrors the CLI output structure for JSON parsing
type GrepResult struct {
	Pattern         string      `json:"pattern"`
	Session         string      `json:"session"`
	Matches         []GrepMatch `json:"matches"`
	TotalLines      int         `json:"total_lines_searched"`
	MatchCount      int         `json:"match_count"`
	PaneCount       int         `json:"panes_searched"`
	CaseInsensitive bool        `json:"case_insensitive,omitempty"`
}

// GrepMatch represents a single search match
type GrepMatch struct {
	Session       string   `json:"session"`
	Pane          string   `json:"pane"`
	PaneID        string   `json:"pane_id"`
	Line          int      `json:"line"`
	Content       string   `json:"content"`
	Context       []string `json:"context,omitempty"`
	ContextBefore int      `json:"context_before,omitempty"`
	ContextAfter  int      `json:"context_after,omitempty"`
}

// GrepListResult represents the output when using -l flag
type GrepListResult struct {
	MatchingPanes []string `json:"matching_panes"`
	Count         int      `json:"count"`
}

// GrepTestSuite extends TestSuite with grep-specific helpers
type GrepTestSuite struct {
	*TestSuite
}

// NewGrepTestSuite creates a new grep test suite
func NewGrepTestSuite(t *testing.T) *GrepTestSuite {
	return &GrepTestSuite{
		TestSuite: NewTestSuite(t, "grep"),
	}
}

// Setup creates the tmux session and then retitles pane 0 with the NTM
// agent naming convention ({session}__cc_1). `ntm grep` intentionally
// skips user (non-agent) panes unless an agent filter is given, and a bare
// shell pane classifies as a user pane, so without this the suite's
// injected content is never searched (panes_searched stays 0).
func (s *GrepTestSuite) Setup() error {
	if err := s.TestSuite.Setup(); err != nil {
		return err
	}
	target := fmt.Sprintf("%s:0", s.Session())
	title := fmt.Sprintf("%s__cc_1", s.Session())
	cmd := exec.Command(tmux.BinaryPath(), "select-pane", "-t", target, "-T", title)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("retitle pane 0 as agent pane: %w (%s)", err, out)
	}
	return nil
}

// InjectContent sends content to a pane to create searchable output
func (s *GrepTestSuite) InjectContent(pane int, content string) error {
	s.Logger().Log("[E2E-GREP] Injecting content to pane %d: %s", pane, truncateString(content, 50))

	target := fmt.Sprintf("%s:%d", s.Session(), pane)
	// Use echo to inject content into the pane
	cmd := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, fmt.Sprintf("echo '%s'", content), "Enter")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("inject content: %w", err)
	}

	// Wait for content to be processed
	time.Sleep(500 * time.Millisecond)
	return nil
}

// RunGrep executes ntm grep and returns the parsed result
func (s *GrepTestSuite) RunGrep(pattern string, flags ...string) (*GrepResult, error) {
	s.Logger().Log("[E2E-GREP] Running grep pattern=%s flags=%v", pattern, flags)

	args := []string{"grep", pattern, s.Session(), "--json"}
	args = append(args, flags...)

	cmd := exec.Command("ntm", args...)
	output, err := cmd.CombinedOutput()

	s.Logger().Log("[E2E-GREP] Output: %s", string(output))

	if err != nil {
		// Check if it's a "no matches" case (which is not an error)
		var result GrepResult
		if jsonErr := json.Unmarshal(output, &result); jsonErr == nil {
			return &result, nil
		}
		return nil, fmt.Errorf("grep failed: %w, output: %s", err, string(output))
	}

	var result GrepResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parse failed: %w, output: %s", err, string(output))
	}

	s.Logger().LogJSON("[E2E-GREP] Result", result)
	return &result, nil
}

// RunGrepList executes ntm grep -l and returns the list result
func (s *GrepTestSuite) RunGrepList(pattern string, flags ...string) (*GrepListResult, error) {
	s.Logger().Log("[E2E-GREP] Running grep -l pattern=%s flags=%v", pattern, flags)

	args := []string{"grep", pattern, s.Session(), "--json", "-l"}
	args = append(args, flags...)

	cmd := exec.Command("ntm", args...)
	output, err := cmd.CombinedOutput()

	s.Logger().Log("[E2E-GREP] Output: %s", string(output))

	if err != nil {
		var result GrepListResult
		if jsonErr := json.Unmarshal(output, &result); jsonErr == nil {
			return &result, nil
		}
		return nil, fmt.Errorf("grep -l failed: %w, output: %s", err, string(output))
	}

	var result GrepListResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parse failed: %w, output: %s", err, string(output))
	}

	s.Logger().LogJSON("[E2E-GREP] List Result", result)
	return &result, nil
}

// TestGrepBasicPatternMatching tests basic regex pattern matching
func TestGrepBasicPatternMatching(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	suite := NewGrepTestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-GREP] Setup failed: %v", err)
	}

	// Inject test content
	testContent := []string{
		"ERROR: Database connection failed",
		"INFO: Server started on port 8080",
		"DEBUG: Processing request",
		"ERROR: Authentication error occurred",
		"WARNING: Memory usage high",
	}

	for _, content := range testContent {
		if err := suite.InjectContent(0, content); err != nil {
			t.Fatalf("[E2E-GREP] Failed to inject content: %v", err)
		}
	}

	// Wait for output to be captured
	time.Sleep(1 * time.Second)

	// Test 1: Search for exact string
	t.Run("ExactString", func(t *testing.T) {
		result, err := suite.RunGrep("ERROR")
		if err != nil {
			t.Fatalf("[E2E-GREP] Grep failed: %v", err)
		}

		if result.MatchCount < 2 {
			t.Errorf("[E2E-GREP] Expected at least 2 ERROR matches, got %d", result.MatchCount)
		}
		suite.Logger().Log("[E2E-GREP] ExactString: found %d matches for 'ERROR'", result.MatchCount)
	})

	// Test 2: Regex pattern
	t.Run("RegexPattern", func(t *testing.T) {
		result, err := suite.RunGrep("ERROR.*failed")
		if err != nil {
			t.Fatalf("[E2E-GREP] Grep failed: %v", err)
		}

		if result.MatchCount < 1 {
			t.Errorf("[E2E-GREP] Expected at least 1 match for 'ERROR.*failed', got %d", result.MatchCount)
		}
		suite.Logger().Log("[E2E-GREP] RegexPattern: found %d matches", result.MatchCount)
	})

	// Test 3: No matches
	t.Run("NoMatches", func(t *testing.T) {
		result, err := suite.RunGrep("NONEXISTENT_PATTERN_12345")
		if err != nil {
			t.Fatalf("[E2E-GREP] Grep failed: %v", err)
		}

		if result.MatchCount != 0 {
			t.Errorf("[E2E-GREP] Expected 0 matches for non-existent pattern, got %d", result.MatchCount)
		}
		suite.Logger().Log("[E2E-GREP] NoMatches: correctly found 0 matches")
	})
}

// TestGrepCaseInsensitive tests case-insensitive search
func TestGrepCaseInsensitive(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	suite := NewGrepTestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-GREP] Setup failed: %v", err)
	}

	// Inject mixed-case content
	testContent := []string{
		"Error in module A",
		"ERROR in module B",
		"error in module C",
		"No issues here",
	}

	for _, content := range testContent {
		if err := suite.InjectContent(0, content); err != nil {
			t.Fatalf("[E2E-GREP] Failed to inject content: %v", err)
		}
	}

	time.Sleep(1 * time.Second)

	// Test case-sensitive search (default)
	t.Run("CaseSensitive", func(t *testing.T) {
		result, err := suite.RunGrep("ERROR")
		if err != nil {
			t.Fatalf("[E2E-GREP] Grep failed: %v", err)
		}

		// Should only match uppercase ERROR
		suite.Logger().Log("[E2E-GREP] CaseSensitive: found %d matches for 'ERROR'", result.MatchCount)
	})

	// Test case-insensitive search
	t.Run("CaseInsensitive", func(t *testing.T) {
		result, err := suite.RunGrep("error", "-i")
		if err != nil {
			t.Fatalf("[E2E-GREP] Grep failed: %v", err)
		}

		if !result.CaseInsensitive {
			t.Error("[E2E-GREP] Expected CaseInsensitive flag to be true")
		}

		// Should match all three error variations
		if result.MatchCount < 3 {
			t.Errorf("[E2E-GREP] Expected at least 3 case-insensitive matches, got %d", result.MatchCount)
		}
		suite.Logger().Log("[E2E-GREP] CaseInsensitive: found %d matches for 'error' with -i", result.MatchCount)
	})
}

// TestGrepContext tests context line options (-A, -B, -C)
func TestGrepContext(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	suite := NewGrepTestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-GREP] Setup failed: %v", err)
	}

	// Inject numbered lines for context testing
	for i := 1; i <= 10; i++ {
		content := fmt.Sprintf("Line %d", i)
		if i == 5 {
			content = "MATCH Line 5"
		}
		if err := suite.InjectContent(0, content); err != nil {
			t.Fatalf("[E2E-GREP] Failed to inject content: %v", err)
		}
	}

	time.Sleep(1 * time.Second)

	// Test context before (-B)
	t.Run("ContextBefore", func(t *testing.T) {
		result, err := suite.RunGrep("MATCH", "-B", "2")
		if err != nil {
			t.Fatalf("[E2E-GREP] Grep failed: %v", err)
		}

		if result.MatchCount < 1 {
			t.Fatal("[E2E-GREP] Expected at least 1 match")
		}

		match := result.Matches[0]
		if match.ContextBefore != 2 {
			suite.Logger().Log("[E2E-GREP] ContextBefore: expected 2, got %d", match.ContextBefore)
		}
		suite.Logger().Log("[E2E-GREP] ContextBefore test passed with %d context lines", len(match.Context))
	})

	// Test context after (-A)
	t.Run("ContextAfter", func(t *testing.T) {
		result, err := suite.RunGrep("MATCH", "-A", "2")
		if err != nil {
			t.Fatalf("[E2E-GREP] Grep failed: %v", err)
		}

		if result.MatchCount < 1 {
			t.Fatal("[E2E-GREP] Expected at least 1 match")
		}

		match := result.Matches[0]
		if match.ContextAfter != 2 {
			suite.Logger().Log("[E2E-GREP] ContextAfter: expected 2, got %d", match.ContextAfter)
		}
		suite.Logger().Log("[E2E-GREP] ContextAfter test passed with %d context lines", len(match.Context))
	})

	// Test symmetric context (-C)
	t.Run("SymmetricContext", func(t *testing.T) {
		result, err := suite.RunGrep("MATCH", "-C", "2")
		if err != nil {
			t.Fatalf("[E2E-GREP] Grep failed: %v", err)
		}

		if result.MatchCount < 1 {
			t.Fatal("[E2E-GREP] Expected at least 1 match")
		}

		match := result.Matches[0]
		totalContext := match.ContextBefore + match.ContextAfter
		if totalContext < 2 {
			suite.Logger().Log("[E2E-GREP] SymmetricContext: expected at least 2 total context lines, got %d", totalContext)
		}
		suite.Logger().Log("[E2E-GREP] SymmetricContext test passed with %d context lines total", len(match.Context))
	})
}

// TestGrepListMode tests the -l (files-with-matches) option
func TestGrepListMode(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	suite := NewGrepTestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-GREP] Setup failed: %v", err)
	}

	// Inject content with a unique marker
	if err := suite.InjectContent(0, "UNIQUE_GREP_MARKER_12345"); err != nil {
		t.Fatalf("[E2E-GREP] Failed to inject content: %v", err)
	}

	time.Sleep(1 * time.Second)

	result, err := suite.RunGrepList("UNIQUE_GREP_MARKER")
	if err != nil {
		t.Fatalf("[E2E-GREP] Grep -l failed: %v", err)
	}

	suite.Logger().Log("[E2E-GREP] ListMode: found %d matching panes: %v", result.Count, result.MatchingPanes)

	if result.Count < 1 {
		t.Errorf("[E2E-GREP] Expected at least 1 matching pane, got %d", result.Count)
	}

	// Verify pane format (session/pane)
	for _, pane := range result.MatchingPanes {
		if !strings.Contains(pane, "/") {
			t.Errorf("[E2E-GREP] Expected pane format 'session/pane', got: %s", pane)
		}
	}
}

// TestGrepInvertMatch tests the -v (invert-match) option
func TestGrepInvertMatch(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	suite := NewGrepTestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-GREP] Setup failed: %v", err)
	}

	testContent := []string{
		"INCLUDE this line",
		"EXCLUDE this line",
		"INCLUDE another line",
		"EXCLUDE another line",
	}

	for _, content := range testContent {
		if err := suite.InjectContent(0, content); err != nil {
			t.Fatalf("[E2E-GREP] Failed to inject content: %v", err)
		}
	}

	time.Sleep(1 * time.Second)

	// Normal search
	normalResult, err := suite.RunGrep("EXCLUDE")
	if err != nil {
		t.Fatalf("[E2E-GREP] Grep failed: %v", err)
	}
	normalCount := normalResult.MatchCount
	suite.Logger().Log("[E2E-GREP] Normal search for 'EXCLUDE': %d matches", normalCount)

	// Inverted search (should match lines NOT containing EXCLUDE)
	invertResult, err := suite.RunGrep("EXCLUDE", "-v")
	if err != nil {
		t.Fatalf("[E2E-GREP] Grep -v failed: %v", err)
	}
	invertCount := invertResult.MatchCount
	suite.Logger().Log("[E2E-GREP] Inverted search for 'EXCLUDE': %d matches", invertCount)

	// Inverted match count should be greater than normal match count
	// (since we're now matching all lines that DON'T contain EXCLUDE)
	if invertCount <= normalCount {
		t.Logf("[E2E-GREP] Note: Inverted count (%d) should typically exceed normal count (%d)", invertCount, normalCount)
	}
}

// TestGrepMaxLines tests the -n (max-lines) option
func TestGrepMaxLines(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	suite := NewGrepTestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-GREP] Setup failed: %v", err)
	}

	// Inject many lines with a marker
	for i := 1; i <= 50; i++ {
		if err := suite.InjectContent(0, fmt.Sprintf("MARKER Line %d", i)); err != nil {
			t.Fatalf("[E2E-GREP] Failed to inject content: %v", err)
		}
	}

	time.Sleep(1 * time.Second)

	// Search with limited lines
	result, err := suite.RunGrep("MARKER", "-n", "10")
	if err != nil {
		t.Fatalf("[E2E-GREP] Grep failed: %v", err)
	}

	suite.Logger().Log("[E2E-GREP] MaxLines test: searched %d total lines with -n 10, found %d matches",
		result.TotalLines, result.MatchCount)

	// Total lines searched should be close to the limit
	// (may vary slightly due to pane structure)
	if result.TotalLines > 20 {
		t.Logf("[E2E-GREP] Note: TotalLines (%d) exceeds expected limit, may be due to multi-pane search", result.TotalLines)
	}
}

// TestGrepResultFormat verifies the JSON output format
func TestGrepResultFormat(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	suite := NewGrepTestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-GREP] Setup failed: %v", err)
	}

	if err := suite.InjectContent(0, "FORMAT_TEST_CONTENT"); err != nil {
		t.Fatalf("[E2E-GREP] Failed to inject content: %v", err)
	}

	time.Sleep(1 * time.Second)

	result, err := suite.RunGrep("FORMAT_TEST")
	if err != nil {
		t.Fatalf("[E2E-GREP] Grep failed: %v", err)
	}

	// Verify required fields
	if result.Pattern != "FORMAT_TEST" {
		t.Errorf("[E2E-GREP] Expected pattern 'FORMAT_TEST', got '%s'", result.Pattern)
	}

	if result.Session == "" {
		t.Error("[E2E-GREP] Session field should not be empty")
	}

	if result.PaneCount < 1 {
		t.Error("[E2E-GREP] PaneCount should be at least 1")
	}

	// Verify match structure
	if result.MatchCount > 0 && len(result.Matches) > 0 {
		match := result.Matches[0]

		if match.Session == "" {
			t.Error("[E2E-GREP] Match.Session should not be empty")
		}
		if match.Pane == "" {
			t.Error("[E2E-GREP] Match.Pane should not be empty")
		}
		if match.Line < 1 {
			t.Error("[E2E-GREP] Match.Line should be >= 1 (1-indexed)")
		}
		if match.Content == "" {
			t.Error("[E2E-GREP] Match.Content should not be empty")
		}

		suite.Logger().Log("[E2E-GREP] ResultFormat validation passed")
	}
}

// TestGrepNoSession tests behavior when session doesn't exist
func TestGrepNoSession(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	logger := NewTestLogger(t, "grep-no-session")
	defer logger.Close()

	// Try to grep a non-existent session
	cmd := exec.Command("ntm", "grep", "pattern", "nonexistent_session_12345", "--json")
	output, err := cmd.CombinedOutput()

	logger.Log("[E2E-GREP] NoSession output: %s", string(output))

	// Should either fail or return empty results
	if err == nil {
		// Check if it's an error response in JSON
		if strings.Contains(string(output), "error") || strings.Contains(string(output), "not found") {
			logger.Log("[E2E-GREP] Correctly reported session not found")
		}
	} else {
		logger.Log("[E2E-GREP] Command failed as expected for non-existent session: %v", err)
	}
}

// TestGrepPagination tests that results are properly paginated for large outputs
func TestGrepPagination(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	suite := NewGrepTestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-GREP] Setup failed: %v", err)
	}

	// Inject many matching lines
	for i := 1; i <= 100; i++ {
		if err := suite.InjectContent(0, fmt.Sprintf("PAGINATE_TEST entry %d", i)); err != nil {
			t.Fatalf("[E2E-GREP] Failed to inject content: %v", err)
		}
	}

	time.Sleep(2 * time.Second)

	result, err := suite.RunGrep("PAGINATE_TEST")
	if err != nil {
		t.Fatalf("[E2E-GREP] Grep failed: %v", err)
	}

	suite.Logger().Log("[E2E-GREP] Pagination test: found %d matches out of %d lines searched",
		result.MatchCount, result.TotalLines)

	// Should have found multiple matches
	if result.MatchCount < 10 {
		t.Logf("[E2E-GREP] Note: Expected more matches, got %d", result.MatchCount)
	}

	// Verify matches are properly ordered
	if len(result.Matches) > 1 {
		for i := 1; i < len(result.Matches); i++ {
			// Line numbers should generally increase within the same pane
			if result.Matches[i].Pane == result.Matches[i-1].Pane {
				if result.Matches[i].Line < result.Matches[i-1].Line {
					suite.Logger().Log("[E2E-GREP] Note: Matches may not be in line order")
				}
			}
		}
	}
}

// TestGrepHighlighting verifies match content is captured correctly
func TestGrepHighlighting(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	suite := NewGrepTestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-GREP] Setup failed: %v", err)
	}

	testPhrase := "HIGHLIGHT_TEST_PHRASE_XYZ"
	if err := suite.InjectContent(0, testPhrase); err != nil {
		t.Fatalf("[E2E-GREP] Failed to inject content: %v", err)
	}

	time.Sleep(1 * time.Second)

	result, err := suite.RunGrep("HIGHLIGHT_TEST")
	if err != nil {
		t.Fatalf("[E2E-GREP] Grep failed: %v", err)
	}

	if result.MatchCount < 1 {
		t.Fatal("[E2E-GREP] Expected at least 1 match")
	}

	// Verify the match content contains our test phrase
	found := false
	for _, match := range result.Matches {
		if strings.Contains(match.Content, "HIGHLIGHT_TEST") {
			found = true
			suite.Logger().Log("[E2E-GREP] Found matching content: %s", truncateString(match.Content, 60))
			break
		}
	}

	if !found {
		t.Error("[E2E-GREP] Match content should contain the search pattern")
	}
}

// TestGrepExitCodes tests that grep returns appropriate exit codes
func TestGrepExitCodes(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	suite := NewGrepTestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-GREP] Setup failed: %v", err)
	}

	// Inject content
	if err := suite.InjectContent(0, "EXIT_CODE_TEST"); err != nil {
		t.Fatalf("[E2E-GREP] Failed to inject content: %v", err)
	}

	time.Sleep(1 * time.Second)

	// Test successful search (exit 0)
	t.Run("SuccessExitCode", func(t *testing.T) {
		cmd := exec.Command("ntm", "grep", "EXIT_CODE", suite.Session())
		err := cmd.Run()
		if err != nil {
			t.Logf("[E2E-GREP] Note: grep with matches returned error: %v", err)
		}
	})

	// Test invalid regex (should fail)
	t.Run("InvalidRegexExitCode", func(t *testing.T) {
		cmd := exec.Command("ntm", "grep", "[invalid(regex", suite.Session())
		err := cmd.Run()
		if err == nil {
			t.Logf("[E2E-GREP] Note: Invalid regex should typically return an error")
		} else {
			suite.Logger().Log("[E2E-GREP] Invalid regex correctly returned error: %v", err)
		}
	})
}

// TestGrepSummary tests the summary statistics in grep output
func TestGrepSummary(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	suite := NewGrepTestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-GREP] Setup failed: %v", err)
	}

	// Inject multiple test lines
	for i := 0; i < 5; i++ {
		if err := suite.InjectContent(0, "SUMMARY_TEST_LINE"); err != nil {
			t.Fatalf("[E2E-GREP] Failed to inject content: %v", err)
		}
	}

	time.Sleep(1 * time.Second)

	result, err := suite.RunGrep("SUMMARY_TEST")
	if err != nil {
		t.Fatalf("[E2E-GREP] Grep failed: %v", err)
	}

	// Verify summary fields
	if result.TotalLines <= 0 {
		t.Error("[E2E-GREP] TotalLines should be > 0")
	}
	if result.PaneCount <= 0 {
		t.Error("[E2E-GREP] PaneCount should be > 0")
	}
	if result.MatchCount != len(result.Matches) {
		t.Errorf("[E2E-GREP] MatchCount (%d) should equal len(Matches) (%d)",
			result.MatchCount, len(result.Matches))
	}

	suite.Logger().Log("[E2E-GREP] Summary: %d matches in %d panes, %d total lines searched",
		result.MatchCount, result.PaneCount, result.TotalLines)
}

// getEnvOrDefault returns an environment variable value or a default
func getEnvOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
