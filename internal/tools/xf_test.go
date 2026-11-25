package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestXFHealthWhenInstalled is an end-to-end regression for issue #202: a
// healthy xf (installed, responds to --version) must pass whether or not an
// archive is indexed. Runs only where a real xf is installed.
func TestXFHealthWhenInstalled(t *testing.T) {
	t.Parallel()
	adapter := NewXFAdapter()
	if _, installed := adapter.Detect(); !installed {
		t.Skip("xf not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health() returned error: %v", err)
	}
	if !health.Healthy {
		t.Fatalf("installed xf reported unhealthy (over-strict archive/index gating regression): %s", health.Message)
	}
}

// indexTestXFArchive indexes a two-tweet X export into an isolated
// XF_DB/XF_INDEX with the installed xf, leaving the operator's archive alone.
func indexTestXFArchive(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("xf"); err != nil {
		t.Skip("xf not installed")
	}

	root := t.TempDir()
	data := filepath.Join(root, "archive", "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	tweets := `window.YTD.tweets.part0 = [
  {"tweet": {"id_str": "1234567890123456789", "created_at": "Wed Jan 08 12:00:00 +0000 2025",
    "full_text": "Hello world! This is a test tweet about Rust programming. #rust",
    "favorite_count": "42", "retweet_count": "7", "lang": "en",
    "entities": {"hashtags": [{"text": "rust"}], "user_mentions": [], "urls": []}}},
  {"tweet": {"id_str": "1234567890123456790", "created_at": "Thu Jan 09 14:30:00 +0000 2025",
    "full_text": "Learning about Tantivy search engine. It is fast for full-text search!",
    "favorite_count": "100", "retweet_count": "25", "lang": "en",
    "entities": {"hashtags": [], "user_mentions": [], "urls": []}}}
]`
	manifest := `window.YTD.manifest.part0 = {
  "userInfo": {"accountId": "999999999", "userName": "test_user", "displayName": "Test User"},
  "archiveInfo": {"sizeBytes": "1234", "generationDate": "2025-01-01T00:00:00Z", "isPartialArchive": false}
}`
	for name, content := range map[string]string{"tweets.js": tweets, "manifest.js": manifest} {
		if err := os.WriteFile(filepath.Join(data, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("XF_DB", filepath.Join(root, "xf.db"))
	t.Setenv("XF_INDEX", filepath.Join(root, "index"))
	if out, err := exec.Command("xf", "index", filepath.Join(root, "archive")).CombinedOutput(); err != nil {
		t.Fatalf("xf index: %v\n%s", err, out)
	}
}

func TestXFSearchReadsInstalledXF(t *testing.T) {
	indexTestXFArchive(t)
	adapter := NewXFAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := adapter.Search(ctx, "rust", XFSearchParams{Limit: 2})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	var found *XFSearchResult
	for i := range results {
		if results[i].ID == "1234567890123456789" {
			found = &results[i]
		}
	}
	if found == nil {
		t.Fatalf("Search() = %+v, want the Rust tweet", results)
	}
	if !strings.Contains(found.Text, "Rust programming") || found.ResultType != "tweet" ||
		found.CreatedAt != "2025-01-08T12:00:00Z" || found.Score <= 0 {
		t.Fatalf("Search() decoded %+v, want xf's text/result_type/created_at/score", *found)
	}

	// A query starting with "-" is xf query syntax (exclusion), not a flag.
	if _, err := adapter.Search(ctx, "-rust tantivy", XFSearchParams{Limit: 2}); err != nil {
		t.Fatalf("Search() with a dash-leading query: %v", err)
	}

	// Lexical search with no match prints nothing on stdout; that is zero
	// results, not a parse failure.
	none, err := adapter.Search(ctx, "zzznomatch", XFSearchParams{Mode: "lexical"})
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("Search(lexical, no match) = %+v, %v; want empty results", none, err)
	}
}

func TestXFHealthReportsIndexState(t *testing.T) {
	indexTestXFArchive(t)
	adapter := NewXFAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if !health.Healthy || !strings.Contains(health.Message, "index_valid=true") ||
		!strings.Contains(health.Message, "tweet_count=2") {
		t.Fatalf("Health() = healthy:%t %q, want an indexed archive with 2 tweets", health.Healthy, health.Message)
	}

	empty := t.TempDir()
	t.Setenv("XF_DB", filepath.Join(empty, "xf.db"))
	t.Setenv("XF_INDEX", filepath.Join(empty, "index"))
	health, err = adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if !health.Healthy || !strings.Contains(health.Message, "index_valid=false") ||
		!strings.Contains(health.Message, "No archive indexed") {
		t.Fatalf("Health() = healthy:%t %q, want healthy xf with no index", health.Healthy, health.Message)
	}
}

func TestXFHealthMessage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		ver       Version
		versionOK bool
		stats     *xfArchiveStats
		statsErr  error
		wantParts []string // substrings that must appear
		noParts   []string // substrings that must NOT appear
	}{
		{
			name:      "Indexed",
			ver:       Version{Major: 1, Minor: 0, Patch: 0, Raw: "xf 1.0.0"},
			versionOK: true,
			stats:     &xfArchiveStats{TweetsCount: 15000, IndexBuiltAt: "2026-10-06T20:27:15Z"},
			wantParts: []string{"xf 1.0.0", "version_ok=true", "index_valid=true", "tweet_count=15000", "index_built_at=2026-10-06T20:27:15Z"},
			noParts:   []string{"stats_err"},
		},
		{
			name:      "NotIndexed",
			ver:       Version{Raw: "xf 0.4.2\n  Built: 2026-09-25T21:01:26Z\n  Target: x86_64-unknown-linux-gnu"},
			versionOK: true,
			statsErr:  fmt.Errorf("exit status 1: Error: ✗ No archive indexed yet"),
			wantParts: []string{"xf 0.4.2 version_ok=true", "index_valid=false", `stats_err="exit status 1: Error: ✗ No archive indexed yet"`},
			noParts:   []string{"tweet_count", "index_valid=true", "Built:", "xf xf"},
		},
		{
			name:      "NoStatsNoError",
			ver:       Version{Raw: "xf 0.4.2"},
			versionOK: true,
			wantParts: []string{"index_valid=false"},
			noParts:   []string{"stats_err", "tweet_count"},
		},
		{
			name:      "VersionFallbackToString",
			ver:       Version{Major: 2, Minor: 3, Patch: 4},
			versionOK: true,
			stats:     &xfArchiveStats{},
			wantParts: []string{"xf 2.3.4", "tweet_count=0"},
			noParts:   []string{"index_built_at"},
		},
		{
			name:      "VersionNotOK",
			ver:       Version{Raw: "xf 0.0.1"},
			versionOK: false,
			wantParts: []string{"version_ok=false"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := xfHealthMessage(tc.ver, tc.versionOK, tc.stats, tc.statsErr)
			for _, want := range tc.wantParts {
				if !strings.Contains(got, want) {
					t.Errorf("xfHealthMessage() = %q, missing %q", got, want)
				}
			}
			for _, no := range tc.noParts {
				if strings.Contains(got, no) {
					t.Errorf("xfHealthMessage() = %q, should not contain %q", got, no)
				}
			}
		})
	}
}
