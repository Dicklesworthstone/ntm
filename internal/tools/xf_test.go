package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestXFHealthWhenInstalled is an end-to-end regression for issue #202: a
// healthy xf (installed, responds to --version) must pass even when the default
// ~/.xf/archive is absent or the archive lives at a custom path (XF_DB/XF_INDEX)
// and when the removed `xf stats --output json` probe would fail. Runs only
// where a real xf is installed.
func TestXFHealthWhenInstalled(t *testing.T) {
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

func TestXFHealthMessage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		ver         Version
		versionOK   bool
		archivePath string
		archiveOK   bool
		archiveErr  error
		indexValid  bool
		indexStatus string
		tweetCount  int
		statsErr    error
		wantParts   []string // substrings that must appear
		noParts     []string // substrings that must NOT appear
	}{
		{
			name:        "HealthyFull",
			ver:         Version{Major: 1, Minor: 0, Patch: 0, Raw: "xf 1.0.0"},
			versionOK:   true,
			archivePath: "/home/user/.xf/archive",
			archiveOK:   true,
			indexValid:  true,
			indexStatus: "ok",
			tweetCount:  15000,
			wantParts:   []string{"xf 1.0.0", "version_ok=true", "archive=/home/user/.xf/archive", "archive_ok=true", "index_valid=true", `index_status="ok"`, "tweet_count=15000"},
			noParts:     []string{"stats_err"},
		},
		{
			name:        "ArchiveMissingWithError",
			ver:         Version{Major: 0, Minor: 2, Patch: 1, Raw: "xf 0.2.1"},
			versionOK:   true,
			archivePath: "/tmp/missing",
			archiveOK:   false,
			archiveErr:  fmt.Errorf("no such file or directory"),
			indexValid:  false,
			wantParts:   []string{"archive_ok=false(no such file or directory)", "index_valid=false"},
			noParts:     []string{"tweet_count", "index_status"},
		},
		{
			name:        "ArchiveNotOKNoError",
			ver:         Version{Raw: "xf 0.1.0"},
			versionOK:   true,
			archivePath: "/tmp/not-a-dir",
			archiveOK:   false,
			indexValid:  false,
			wantParts:   []string{"archive_ok=false"},
			noParts:     []string{"archive_ok=false("},
		},
		{
			name:        "VersionFallbackToString",
			ver:         Version{Major: 2, Minor: 3, Patch: 4},
			versionOK:   true,
			archivePath: "/a",
			archiveOK:   true,
			indexValid:  true,
			wantParts:   []string{"xf 2.3.4"},
		},
		{
			name:        "VersionNotOK",
			ver:         Version{Raw: "xf 0.0.1"},
			versionOK:   false,
			archivePath: "/a",
			archiveOK:   true,
			indexValid:  true,
			wantParts:   []string{"version_ok=false"},
		},
		{
			name:        "StatsError",
			ver:         Version{Raw: "xf 1.0.0"},
			versionOK:   true,
			archivePath: "/a",
			archiveOK:   true,
			indexValid:  false,
			statsErr:    fmt.Errorf("connection refused"),
			wantParts:   []string{`stats_err="connection refused"`},
		},
		{
			name:        "ZeroTweetCountOmitted",
			ver:         Version{Raw: "xf 1.0.0"},
			versionOK:   true,
			archivePath: "/a",
			archiveOK:   true,
			indexValid:  true,
			tweetCount:  0,
			noParts:     []string{"tweet_count"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := xfHealthMessage(tc.ver, tc.versionOK, tc.archivePath, tc.archiveOK, tc.archiveErr, tc.indexValid, tc.indexStatus, tc.tweetCount, tc.statsErr)
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
