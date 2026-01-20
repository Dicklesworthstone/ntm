package tests

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestBVClientParseValidTriageJSON(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	purpose := "Parse valid bv --robot-triage JSON and extract key recommendation fields"
	logger.Log("Test: %s", t.Name())
	logger.Log("Purpose: %s", purpose)

	recs := []bv.TriageRecommendation{
		{
			ID:       "bd-1",
			Title:    "First task",
			Type:     "task",
			Status:   "open",
			Priority: 1,
			Labels:   []string{"backend"},
			Score:    0.91,
			Breakdown: &bv.ScoreBreakdown{
				Pagerank:    0.55,
				Betweenness: 0.15,
			},
			Action:      "start",
			Reasons:     []string{"High impact"},
			UnblocksIDs: []string{"bd-2", "bd-3"},
			BlockedBy:   nil,
		},
		{
			ID:       "bd-2",
			Title:    "Second task",
			Type:     "task",
			Status:   "blocked",
			Priority: 2,
			Labels:   []string{"frontend"},
			Score:    0.7,
			Breakdown: &bv.ScoreBreakdown{
				Pagerank:    0.11,
				Betweenness: 0.02,
			},
			Action:      "wait",
			Reasons:     []string{"Blocked"},
			UnblocksIDs: nil,
			BlockedBy:   []string{"bd-9"},
		},
	}
	triageJSON := mustMarshalJSON(t, buildTriageResponse(recs))

	setupFakeBV(t, fakeBVConfig{stdout: triageJSON})

	client := bv.NewBVClientWithOptions("", 30*time.Second, 5*time.Second)
	logger.Log("Input: WorkspacePath=%q CacheTTL=%s Timeout=%s", client.WorkspacePath, client.CacheTTL, client.Timeout)
	logger.Log("Expected: 2 recommendations with correct PageRank, UnblocksCount, BlockedBy IDs")

	got, err := client.GetRecommendations(bv.RecommendationOpts{})
	if err != nil {
		logger.Log("FAIL: unexpected error: %v", err)
		t.Fatalf("unexpected error: %v", err)
	}
	logger.Log("Actual: recommendations=%d", len(got))

	if len(got) != 2 {
		logger.Log("FAIL: expected 2 recommendations, got %d", len(got))
		t.Fatalf("expected 2 recommendations, got %d", len(got))
	}

	if got[0].PageRank != 0.55 || got[0].UnblocksCount != 2 || len(got[0].BlockedByIDs) != 0 {
		logger.Log("FAIL: rec[0] PageRank=%f UnblocksCount=%d BlockedBy=%v", got[0].PageRank, got[0].UnblocksCount, got[0].BlockedByIDs)
		t.Fatalf("rec[0] fields mismatch: PageRank=%f UnblocksCount=%d BlockedBy=%v", got[0].PageRank, got[0].UnblocksCount, got[0].BlockedByIDs)
	}
	if got[1].PageRank != 0.11 || got[1].UnblocksCount != 0 || len(got[1].BlockedByIDs) != 1 {
		logger.Log("FAIL: rec[1] PageRank=%f UnblocksCount=%d BlockedBy=%v", got[1].PageRank, got[1].UnblocksCount, got[1].BlockedByIDs)
		t.Fatalf("rec[1] fields mismatch: PageRank=%f UnblocksCount=%d BlockedBy=%v", got[1].PageRank, got[1].UnblocksCount, got[1].BlockedByIDs)
	}

	logger.Log("PASS: parsed recommendations and extracted fields correctly")
}

func TestBVClientParseEmptyRecommendations(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	purpose := "Handle empty recommendations list without errors"
	logger.Log("Test: %s", t.Name())
	logger.Log("Purpose: %s", purpose)

	triageJSON := mustMarshalJSON(t, buildTriageResponse(nil))
	setupFakeBV(t, fakeBVConfig{stdout: triageJSON})

	client := bv.NewBVClient()
	logger.Log("Input: recommendations=[]")
	logger.Log("Expected: 0 recommendations")

	got, err := client.GetRecommendations(bv.RecommendationOpts{})
	if err != nil {
		logger.Log("FAIL: unexpected error: %v", err)
		t.Fatalf("unexpected error: %v", err)
	}
	logger.Log("Actual: recommendations=%d", len(got))

	if len(got) != 0 {
		logger.Log("FAIL: expected 0 recommendations, got %d", len(got))
		t.Fatalf("expected 0 recommendations, got %d", len(got))
	}

	logger.Log("PASS: empty recommendations handled")
}

func TestBVClientMalformedJSON(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	purpose := "Return ErrInvalidJSON when bv output is not JSON"
	logger.Log("Test: %s", t.Name())
	logger.Log("Purpose: %s", purpose)

	setupFakeBV(t, fakeBVConfig{stdout: "not-json"})

	client := bv.NewBVClient()
	logger.Log("Input: malformed JSON output")
	logger.Log("Expected: error matching ErrInvalidJSON")

	_, err := client.GetRecommendations(bv.RecommendationOpts{})
	logger.Log("Actual: error=%v", err)
	if err == nil || !errors.Is(err, bv.ErrInvalidJSON) {
		logger.Log("FAIL: expected ErrInvalidJSON, got %v", err)
		t.Fatalf("expected ErrInvalidJSON, got %v", err)
	}

	logger.Log("PASS: malformed JSON detected")
}

func TestBVClientNonZeroExit(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	purpose := "Surface bv non-zero exit code errors"
	logger.Log("Test: %s", t.Name())
	logger.Log("Purpose: %s", purpose)

	setupFakeBV(t, fakeBVConfig{stderr: "boom", exitCode: 2})

	client := bv.NewBVClient()
	logger.Log("Input: exit code=2 stderr=boom")
	logger.Log("Expected: error describing bv --robot-triage failure")

	_, err := client.GetRecommendations(bv.RecommendationOpts{})
	logger.Log("Actual: error=%v", err)
	if err == nil || !strings.Contains(err.Error(), "bv --robot-triage failed") {
		logger.Log("FAIL: expected bv failure, got %v", err)
		t.Fatalf("expected bv failure, got %v", err)
	}

	logger.Log("PASS: non-zero exit handled")
}

func TestBVClientTimeout(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	purpose := "Return ErrTimeout when bv exceeds command timeout"
	logger.Log("Test: %s", t.Name())
	logger.Log("Purpose: %s", purpose)

	setupFakeBV(t, fakeBVConfig{stdout: mustMarshalJSON(t, buildTriageResponse(nil)), sleep: 200 * time.Millisecond})

	client := bv.NewBVClientWithOptions("", 30*time.Second, 50*time.Millisecond)
	logger.Log("Input: timeout=%s sleep=%s", client.Timeout, 200*time.Millisecond)
	logger.Log("Expected: error matching ErrTimeout")

	_, err := client.GetRecommendations(bv.RecommendationOpts{})
	logger.Log("Actual: error=%v", err)
	if err == nil || !errors.Is(err, bv.ErrTimeout) {
		logger.Log("FAIL: expected ErrTimeout, got %v", err)
		t.Fatalf("expected ErrTimeout, got %v", err)
	}

	logger.Log("PASS: timeout handled")
}

func TestBVClientInvalidWorkspacePath(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	purpose := "Return error when workspace path is invalid"
	logger.Log("Test: %s", t.Name())
	logger.Log("Purpose: %s", purpose)

	recordFile := setupFakeBV(t, fakeBVConfig{stdout: mustMarshalJSON(t, buildTriageResponse(nil)), recordCalls: true})

	missingDir := filepath.Join(t.TempDir(), "does-not-exist")
	client := bv.NewBVClientWithOptions(missingDir, 30*time.Second, 5*time.Second)
	logger.Log("Input: WorkspacePath=%q", missingDir)
	logger.Log("Expected: error mentioning invalid workspace (chdir/no such file)")

	_, err := client.GetRecommendations(bv.RecommendationOpts{})
	logger.Log("Actual: error=%v", err)
	if err == nil || !(strings.Contains(err.Error(), "chdir") || strings.Contains(err.Error(), "no such file")) {
		logger.Log("FAIL: expected chdir/no such file error, got %v", err)
		t.Fatalf("expected invalid workspace error, got %v", err)
	}

	calls := countLines(t, recordFile)
	logger.Log("Actual: bv calls recorded=%d", calls)
	if calls != 0 {
		logger.Log("FAIL: expected 0 bv executions due to invalid workspace, got %d", calls)
		t.Fatalf("expected 0 bv executions due to invalid workspace, got %d", calls)
	}

	logger.Log("PASS: invalid workspace handled")
}

func TestBVClientCachingUsesCache(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	purpose := "Ensure BVClient cache prevents repeated bv invocations within TTL"
	logger.Log("Test: %s", t.Name())
	logger.Log("Purpose: %s", purpose)

	triageJSON := mustMarshalJSON(t, buildTriageResponse(nil))
	recordFile := setupFakeBV(t, fakeBVConfig{stdout: triageJSON, recordCalls: true})

	client := bv.NewBVClientWithOptions("", 200*time.Millisecond, 5*time.Second)
	logger.Log("Input: CacheTTL=%s", client.CacheTTL)
	logger.Log("Expected: 1 bv call before TTL expiry, 2 after TTL expiry")

	_, err := client.GetRecommendations(bv.RecommendationOpts{})
	if err != nil {
		logger.Log("FAIL: first GetRecommendations error=%v", err)
		t.Fatalf("first GetRecommendations error: %v", err)
	}
	_, err = client.GetRecommendations(bv.RecommendationOpts{})
	if err != nil {
		logger.Log("FAIL: second GetRecommendations error=%v", err)
		t.Fatalf("second GetRecommendations error: %v", err)
	}

	calls := countLines(t, recordFile)
	logger.Log("Actual: bv calls recorded=%d", calls)
	if calls != 1 {
		logger.Log("FAIL: expected 1 call within TTL, got %d", calls)
		t.Fatalf("expected 1 call within TTL, got %d", calls)
	}

	time.Sleep(250 * time.Millisecond)
	_, err = client.GetRecommendations(bv.RecommendationOpts{})
	if err != nil {
		logger.Log("FAIL: third GetRecommendations error=%v", err)
		t.Fatalf("third GetRecommendations error: %v", err)
	}
	calls = countLines(t, recordFile)
	logger.Log("Actual after TTL: bv calls recorded=%d", calls)
	if calls != 2 {
		logger.Log("FAIL: expected 2 calls after TTL expiry, got %d", calls)
		t.Fatalf("expected 2 calls after TTL expiry, got %d", calls)
	}

	logger.Log("PASS: caching behavior verified")
}

func TestBVClientIntegrationOrderAndLimit(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	purpose := "Call real bv and verify recommendation order and limit behavior"
	logger.Log("Test: %s", t.Name())
	logger.Log("Purpose: %s", purpose)

	if !bv.IsInstalled() {
		logger.Log("SKIP: bv not installed")
		t.Skip("bv not installed")
	}

	root := findProjectRoot(t)
	if root == "" {
		logger.Log("SKIP: .beads directory not found")
		t.Skip("no .beads directory found")
	}

	logger.Log("Input: workspace=%q", root)

	triageResp, err := readRawTriage(root)
	if err != nil {
		logger.Log("FAIL: unable to read bv triage: %v", err)
		t.Fatalf("unable to read bv triage: %v", err)
	}

	client := bv.NewBVClientWithOptions(root, 30*time.Second, 30*time.Second)
	limit := 5
	logger.Log("Expected: recommendations preserve bv order, limit=%d", limit)

	recs, err := client.GetRecommendations(bv.RecommendationOpts{Limit: limit})
	if err != nil {
		logger.Log("FAIL: GetRecommendations error=%v", err)
		t.Fatalf("GetRecommendations error: %v", err)
	}

	logger.Log("Actual: recommendations=%d", len(recs))
	if len(recs) > limit {
		logger.Log("FAIL: expected at most %d recommendations, got %d", limit, len(recs))
		t.Fatalf("expected at most %d recommendations, got %d", limit, len(recs))
	}

	for i, rec := range recs {
		if i >= len(triageResp.Triage.Recommendations) {
			break
		}
		expectedID := triageResp.Triage.Recommendations[i].ID
		logger.Log("Expected ID[%d]=%s Actual=%s", i, expectedID, rec.ID)
		if rec.ID != expectedID {
			logger.Log("FAIL: order mismatch at %d expected %s got %s", i, expectedID, rec.ID)
			t.Fatalf("order mismatch at %d expected %s got %s", i, expectedID, rec.ID)
		}
	}

	logger.Log("PASS: integration order and limit verified")
}

func TestBVClientIntegrationCacheConsistency(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	purpose := "Ensure repeated GetRecommendations calls return consistent results with real bv"
	logger.Log("Test: %s", t.Name())
	logger.Log("Purpose: %s", purpose)

	if !bv.IsInstalled() {
		logger.Log("SKIP: bv not installed")
		t.Skip("bv not installed")
	}

	root := findProjectRoot(t)
	if root == "" {
		logger.Log("SKIP: .beads directory not found")
		t.Skip("no .beads directory found")
	}

	client := bv.NewBVClientWithOptions(root, time.Minute, 30*time.Second)
	logger.Log("Input: CacheTTL=%s", client.CacheTTL)
	logger.Log("Expected: identical recommendation IDs across back-to-back calls")

	recs1, err := client.GetRecommendations(bv.RecommendationOpts{Limit: 10})
	if err != nil {
		logger.Log("FAIL: first GetRecommendations error=%v", err)
		t.Fatalf("first GetRecommendations error: %v", err)
	}
	recs2, err := client.GetRecommendations(bv.RecommendationOpts{Limit: 10})
	if err != nil {
		logger.Log("FAIL: second GetRecommendations error=%v", err)
		t.Fatalf("second GetRecommendations error: %v", err)
	}

	ids1 := extractIDs(recs1)
	ids2 := extractIDs(recs2)
	logger.Log("Actual: ids1=%v", ids1)
	logger.Log("Actual: ids2=%v", ids2)

	if strings.Join(ids1, ",") != strings.Join(ids2, ",") {
		logger.Log("FAIL: cached results mismatch")
		t.Fatalf("cached results mismatch: %v vs %v", ids1, ids2)
	}

	logger.Log("PASS: cache consistency verified")
}

func buildTriageResponse(recs []bv.TriageRecommendation) bv.TriageResponse {
	generated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	quickRef := bv.TriageQuickRef{
		OpenCount:       len(recs),
		ActionableCount: countActionable(recs),
		BlockedCount:    len(recs) - countActionable(recs),
		InProgressCount: 0,
		TopPicks:        []bv.TriageTopPick{},
	}
	if len(recs) > 0 {
		quickRef.TopPicks = []bv.TriageTopPick{{
			ID:       recs[0].ID,
			Title:    recs[0].Title,
			Score:    recs[0].Score,
			Reasons:  recs[0].Reasons,
			Unblocks: len(recs[0].UnblocksIDs),
		}}
	}

	return bv.TriageResponse{
		GeneratedAt: generated,
		DataHash:    "test-hash",
		Triage: bv.TriageData{
			Meta: bv.TriageMeta{
				Version:       "test",
				GeneratedAt:   generated,
				Phase2Ready:   true,
				IssueCount:    len(recs),
				ComputeTimeMs: 7,
			},
			QuickRef:        quickRef,
			Recommendations: recs,
			QuickWins:       []bv.TriageRecommendation{},
			BlockersToClear: []bv.BlockerToClear{},
			Commands:        map[string]string{"ready": "br ready --json"},
		},
	}
}

func countActionable(recs []bv.TriageRecommendation) int {
	count := 0
	for _, rec := range recs {
		if len(rec.BlockedBy) == 0 {
			count++
		}
	}
	return count
}

func mustMarshalJSON(t *testing.T, v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}
	return string(data)
}

type fakeBVConfig struct {
	stdout      string
	stderr      string
	exitCode    int
	sleep       time.Duration
	recordCalls bool
}

func setupFakeBV(t *testing.T, cfg fakeBVConfig) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake bv script not supported on Windows")
	}

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "bv")
	script := `#!/bin/sh
if [ -n "$BV_RECORD_FILE" ]; then
  printf "call\n" >> "$BV_RECORD_FILE"
fi
if [ -n "$BV_SLEEP" ]; then
  sleep "$BV_SLEEP"
fi
if [ -n "$BV_STDERR" ]; then
  printf "%s" "$BV_STDERR" 1>&2
fi
if [ -n "$BV_STDOUT" ]; then
  printf "%s" "$BV_STDOUT"
fi
if [ -n "$BV_EXIT_CODE" ]; then
  exit "$BV_EXIT_CODE"
fi
exit 0
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake bv script: %v", err)
	}

	currentPath := os.Getenv("PATH")
	if currentPath == "" {
		t.Setenv("PATH", tmpDir)
	} else {
		t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+currentPath)
	}
	if cfg.stdout != "" {
		t.Setenv("BV_STDOUT", cfg.stdout)
	}
	if cfg.stderr != "" {
		t.Setenv("BV_STDERR", cfg.stderr)
	}
	if cfg.exitCode != 0 {
		t.Setenv("BV_EXIT_CODE", strconv.Itoa(cfg.exitCode))
	}
	if cfg.sleep > 0 {
		t.Setenv("BV_SLEEP", fmt.Sprintf("%.3f", cfg.sleep.Seconds()))
	}

	var recordFile string
	if cfg.recordCalls {
		recordFile = filepath.Join(tmpDir, "bv_calls.log")
		t.Setenv("BV_RECORD_FILE", recordFile)
	}

	return recordFile
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("failed to read record file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return 0
	}
	return len(lines)
}

func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for dir != "/" {
		if _, err := os.Stat(filepath.Join(dir, ".beads")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func readRawTriage(dir string) (*bv.TriageResponse, error) {
	cmd := exec.Command("bv", "--robot-triage")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	if !json.Valid(output) {
		return nil, errors.New("bv returned invalid JSON")
	}
	var resp bv.TriageResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func extractIDs(recs []bv.Recommendation) []string {
	ids := make([]string, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, rec.ID)
	}
	return ids
}
