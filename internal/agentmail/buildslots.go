package agentmail

// Build-slot leases are owned by the MCP Agent Mail server (ntm-83dz).
//
// The server (mcp_agent_mail app.py, WORKTREES_ENABLED=1) exposes exactly
// three build-slot tools: acquire_build_slot, renew_build_slot, and
// release_build_slot. There is NO list_build_slots tool and NO
// resource://build_slots/... resource — verified against the server source
// and a live resources/list. The only listing surface that exists is the
// shared on-disk archive the server writes leases into:
//
//	<archive>/projects/<slug>/build_slots/<slot>/<agent>__<branch>.json
//
// NTM therefore lists leases by reading that archive (the same archive
// HasArchiveForProject already consults for availability fallback) and
// releases them through the real release_build_slot tool, authenticating
// with the holder's persisted registration token. When the archive is
// absent, listing is reported as unavailable (degraded source) rather than
// fabricating a server API.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrBuildSlotListingUnavailable indicates no listing surface exists:
// the Agent Mail server exposes no list tool or resource for build slots,
// and the shared on-disk archive could not be found either.
var ErrBuildSlotListingUnavailable = errors.New(
	"build-slot lease listing unavailable: Agent Mail server exposes no list tool/resource and no on-disk archive was found")

// BuildSlotLease mirrors the JSON the Agent Mail server writes for a
// build-slot lease (acquire_build_slot payload in app.py).
type BuildSlotLease struct {
	Slot       string    `json:"slot"`
	Agent      string    `json:"agent"`
	Branch     string    `json:"branch"`
	Exclusive  bool      `json:"exclusive"`
	AcquiredTS FlexTime  `json:"acquired_ts"`
	ExpiresTS  FlexTime  `json:"expires_ts"`
	ReleasedTS *FlexTime `json:"released_ts,omitempty"`
}

// ActiveAt reports whether the lease is live at the given instant, using
// the same rules as the server's _is_active_build_slot_lease: released
// leases are dead, and a parseable expiry in the past kills the lease. A
// missing/zero expiry keeps the lease active (server parity).
func (l BuildSlotLease) ActiveAt(now time.Time) bool {
	if l.ReleasedTS != nil && !l.ReleasedTS.IsZero() {
		return false
	}
	if !l.ExpiresTS.IsZero() && !l.ExpiresTS.After(now) {
		return false
	}
	return true
}

// DefaultBuildSlotArchiveRoot returns the shared Agent Mail archive root
// (~/.mcp_agent_mail_git_mailbox_repo), or "" when the home directory is
// unresolvable.
func DefaultBuildSlotArchiveRoot() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, DefaultArchivePath)
}

// ListActiveBuildSlotLeases reads active build-slot leases for projectKey
// from the on-disk Agent Mail archive rooted at archiveRoot. Pass
// DefaultBuildSlotArchiveRoot() for the real archive.
//
// Returns ErrBuildSlotListingUnavailable when the archive root does not
// exist (no listing surface at all). A project directory without a
// build_slots subtree simply yields an empty slice: the project has no
// leases. Unparseable lease files are skipped, matching the server's own
// tolerant reader.
func ListActiveBuildSlotLeases(archiveRoot, projectKey string, now time.Time) ([]BuildSlotLease, error) {
	if archiveRoot == "" {
		return nil, ErrBuildSlotListingUnavailable
	}
	if info, err := os.Stat(archiveRoot); err != nil || !info.IsDir() {
		return nil, ErrBuildSlotListingUnavailable
	}

	leases := []BuildSlotLease{}
	seenDirs := map[string]bool{}
	for _, slug := range []string{ProjectSlugFromPath(projectKey), legacyProjectSlugFromPath(projectKey)} {
		if slug == "" {
			continue
		}
		slotsDir := filepath.Join(archiveRoot, "projects", slug, "build_slots")
		if seenDirs[slotsDir] {
			continue
		}
		seenDirs[slotsDir] = true
		slotEntries, err := os.ReadDir(slotsDir)
		if err != nil {
			continue // project or build_slots dir absent under this slug
		}
		for _, slotEntry := range slotEntries {
			if !slotEntry.IsDir() {
				continue
			}
			slotDir := filepath.Join(slotsDir, slotEntry.Name())
			leaseEntries, err := os.ReadDir(slotDir)
			if err != nil {
				continue
			}
			for _, leaseEntry := range leaseEntries {
				if leaseEntry.IsDir() || filepath.Ext(leaseEntry.Name()) != ".json" {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(slotDir, leaseEntry.Name()))
				if err != nil {
					continue
				}
				var lease BuildSlotLease
				if err := json.Unmarshal(raw, &lease); err != nil {
					continue
				}
				if lease.Slot == "" {
					lease.Slot = slotEntry.Name()
				}
				if lease.Agent == "" {
					continue
				}
				if lease.ActiveAt(now) {
					leases = append(leases, lease)
				}
			}
		}
	}
	return leases, nil
}

// BuildSlotReleaseResult is the release_build_slot tool response.
type BuildSlotReleaseResult struct {
	Released   bool     `json:"released"`
	ReleasedAt FlexTime `json:"released_at"`
}

// ReleaseBuildSlot calls the Agent Mail release_build_slot tool as
// agentName. The server authenticates the holder, so the holder's
// registration token must be cached on the client (SetRegistrationToken /
// HydrateClientTokens) or the call will fail with AUTHENTICATION_REQUIRED.
// branch should come from the lease being released so the server
// reconstructs the same <agent>__<branch> holder id; when empty the server
// falls back to the project's current git branch.
func (c *Client) ReleaseBuildSlot(ctx context.Context, projectKey, agentName, slot, branch string) (*BuildSlotReleaseResult, error) {
	if projectKey == "" || agentName == "" || slot == "" {
		return nil, fmt.Errorf("release_build_slot requires project key, agent name, and slot")
	}
	args := map[string]interface{}{
		"project_key": projectKey,
		"agent_name":  agentName,
		"slot":        slot,
	}
	if branch != "" {
		args["branch"] = branch
	}
	c.attachRegistrationToken(args)

	result, err := c.callTool(ctx, "release_build_slot", args)
	if err != nil {
		return nil, err
	}

	var release BuildSlotReleaseResult
	if err := json.Unmarshal(result, &release); err != nil {
		return nil, NewAPIError("release_build_slot", 0, err)
	}
	return &release, nil
}
