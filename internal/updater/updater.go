// Package updater provides update checking functionality for ntm.
package updater

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cli"
)

const (
	// GitHubAPIURL is the endpoint for checking releases
	GitHubAPIURL = "https://api.github.com/repos/Dicklesworthstone/ntm/releases/latest"
	// CheckTimeout is the maximum time to wait for update check
	CheckTimeout = 2 * time.Second
)

// Release represents a GitHub release
type Release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Name    string `json:"name"`
}

// UpdateInfo contains information about an available update
type UpdateInfo struct {
	Available   bool
	NewVersion  string
	CurrentVer  string
	ReleaseURL  string
	ReleaseName string
}

// CheckForUpdates queries GitHub for the latest release.
// Returns update info if a newer version is available.
// This function is designed to be fast and non-blocking.
func CheckForUpdates() (*UpdateInfo, error) {
	client := &http.Client{
		Timeout: CheckTimeout,
	}
	return checkForUpdates(client, GitHubAPIURL, cli.Version)
}

func checkForUpdates(client *http.Client, url, currentVersion string) (*UpdateInfo, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	// GitHub recommends sending a User-Agent
	req.Header.Set("User-Agent", "ntm-update-check")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// For rate limits, just return no update available
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return &UpdateInfo{Available: false, CurrentVer: currentVersion}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}

	info := &UpdateInfo{
		CurrentVer:  currentVersion,
		NewVersion:  rel.TagName,
		ReleaseURL:  rel.HTMLURL,
		ReleaseName: rel.Name,
	}

	// Compare versions
	if compareVersions(rel.TagName, currentVersion) > 0 {
		info.Available = true
	}

	return info, nil
}

// compareVersions compares semver-ish strings with optional leading 'v' and optional pre-release
// suffix (e.g., v1.2.3-alpha). Pre-release versions are considered LOWER than their corresponding
// release version per SemVer spec.
// Returns 1 if v1>v2, -1 if v1<v2, 0 if equal.
func compareVersions(v1, v2 string) int {
	type parsed struct {
		parts      []int
		prerelease bool
		preLabel   string
	}

	parse := func(v string) *parsed {
		v = strings.TrimPrefix(v, "v")
		prerelease := false
		preLabel := ""
		if idx := strings.Index(v, "-"); idx != -1 {
			prerelease = true
			preLabel = v[idx+1:]
			v = v[:idx]
		}
		parts := strings.Split(v, ".")
		res := make([]int, 3)
		for i := 0; i < len(res) && i < len(parts); i++ {
			if n, err := strconv.Atoi(parts[i]); err == nil {
				res[i] = n
			} else {
				return nil
			}
		}
		return &parsed{parts: res, prerelease: prerelease, preLabel: preLabel}
	}

	p1 := parse(v1)
	p2 := parse(v2)

	if p1 != nil && p2 != nil {
		for i := 0; i < 3; i++ {
			if p1.parts[i] > p2.parts[i] {
				return 1
			}
			if p1.parts[i] < p2.parts[i] {
				return -1
			}
		}
		// Main versions equal: compare prerelease labels
		if p1.prerelease || p2.prerelease {
			if p1.prerelease && !p2.prerelease {
				return -1 // prerelease is lower than release
			}
			if !p1.prerelease && p2.prerelease {
				return 1
			}
			// Both prerelease: lexicographic compare
			if p1.preLabel > p2.preLabel {
				return 1
			}
			if p1.preLabel < p2.preLabel {
				return -1
			}
		}
		return 0
	}

	// Fallback: lexicographic
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")
	if v1 > v2 {
		return 1
	} else if v1 < v2 {
		return -1
	}
	return 0
}

// CheckAsync runs the update check in a goroutine and returns immediately.
// The result can be retrieved from the returned channel.
// This allows the main program to continue while the check runs in background.
func CheckAsync() <-chan *UpdateInfo {
	ch := make(chan *UpdateInfo, 1)
	go func() {
		info, err := CheckForUpdates()
		if err != nil {
			ch <- &UpdateInfo{Available: false, CurrentVer: cli.Version}
		} else {
			ch <- info
		}
		close(ch)
	}()
	return ch
}
