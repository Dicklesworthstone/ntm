package robot

// Disk usage trajectory + optional per-pane build-dir attribution (ntm-1k9g).
//
// This file gives --robot-metrics and --robot-snapshot a "disk" section that
// answers "how fast is the disk filling?" from interval samples persisted in
// the runtime store, replacing the operator skill's hand-rolled `df` sampling
// into /tmp tick files. The optional --disk-attribution flag (metrics only)
// adds "who is filling it?": bounded du of well-known build dirs under each
// agent pane's live cwd (tmux pane_current_path). No environment values are
// persisted or read for attribution — the ntm-8lvd closure deliberately
// removed per-pane env persistence for credential safety, and the pane cwd is
// the safe substitute source.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/git"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// WatermarkTypeDiskSample is the watermark type for persisted disk samples.
//
// Storage choice: disk samples reuse the output_watermarks table rather than
// adding a new table because (a) the data is exactly one small row per
// sampled scope — a cursor-like value plus a timestamp — which is the shape
// output_watermarks already stores, migrates, and GC-manages; (b) the
// cross-invocation persistence this feature needs (each --robot-metrics /
// --robot-snapshot process records one sample and reads the previous one) is
// precisely what the watermark API provides; and (c) WatermarkTypeOutputSeq
// (activity.go) already established the precedent of documenting per-type
// column reuse instead of widening the schema. For rows of this type the
// generic columns are used as follows:
//
//	Scope        — the sampled project directory (git root when resolvable,
//	               else the working directory). statfs values are mount-wide,
//	               so two projects on one mount keep independent-but-consistent
//	               rows; row count stays bounded by distinct project dirs.
//	LastCursor   — available bytes at the last accepted sample
//	LastTs       — when the last sample was accepted
//	BaselineHash — last computed delta_bytes_per_min as a decimal string
//	               (empty until two samples >= minDiskSampleInterval apart
//	               have been observed); reused so "reuse last delta" survives
//	               process boundaries without new columns
//	Consumer     — the statted path, informational only
const WatermarkTypeDiskSample = "disk_sample"

// minDiskSampleInterval is the minimum spacing between two persisted samples
// used for a fresh delta computation. Invocations closer together than this
// reuse the previously persisted delta instead of computing a noisy one.
const minDiskSampleInterval = 60 * time.Second

// maxProjectionMinutes caps how far ahead projected_full_at is computed.
// Beyond ~10 years the projection carries no operational signal and the
// float→Duration conversion would risk int64 overflow, so it is omitted.
const maxProjectionMinutes = 10 * 365 * 24 * 60

// Attribution walk bounds: each well-known build dir gets its own deadline
// and entry cap; hitting either reports the bytes accumulated so far with
// truncated=true ("size-capped reporting") rather than blocking the call.
const (
	diskBuildDirTimeout    = 2 * time.Second
	diskBuildDirMaxEntries = 100_000
	// diskAttributionBudget bounds the whole attribution pass (all panes).
	diskAttributionBudget = 15 * time.Second
)

// wellKnownBuildDirs are the depth-1 directory names probed under each agent
// pane's cwd for attribution. Only these fixed names are ever statted; no
// recursive discovery of arbitrary directories happens.
var wellKnownBuildDirs = [...]string{"target", "node_modules", ".venv", "dist"}

// AlertTypeDiskTrajectory is the alert/attention family published when the
// disk is projected to fill within the configured horizon
// ([alerts] disk_full_horizon_hours; 0 disables). It mirrors how
// disk_low_threshold_gb fires: an alert in the global tracker that snapshot /
// alert surfaces pick up via alerts.GetActiveAlerts.
const AlertTypeDiskTrajectory alerts.AlertType = "disk_trajectory"

// alertSourceDiskTrajectory is deliberately NOT one of the generator-owned
// periodic sources ("disk", "agents", "beads"), so tracker.Update cycles
// preserve the alert instead of auto-resolving it between samples.
const alertSourceDiskTrajectory = "disk_trajectory"

// DiskSection is the disk trajectory block on --robot-metrics and
// --robot-snapshot output.
type DiskSection struct {
	// MountPath is the directory whose filesystem was sampled (project root).
	MountPath string `json:"mount_path"`
	// MountUsedPct is df-style used percent: 100*used/(used+available).
	MountUsedPct float64 `json:"mount_used_pct"`
	// AvailableBytes is the space available to the calling user.
	AvailableBytes int64 `json:"available_bytes"`
	// DeltaBytesPerMin is the fill rate (positive = filling). Omitted until a
	// previous persisted sample exists to diff against.
	DeltaBytesPerMin *float64 `json:"delta_bytes_per_min,omitempty"`
	// ProjectedFullAt is when available_bytes hits zero at the current rate
	// (RFC3339). Omitted when delta is absent, zero, or negative (disk not
	// filling), or when the projection exceeds maxProjectionMinutes.
	ProjectedFullAt string `json:"projected_full_at,omitempty"`
}

// BuildDirUsage is the measured size of one well-known build directory.
type BuildDirUsage struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	// Truncated reports that the walk hit its deadline or entry cap, so Bytes
	// is a lower bound.
	Truncated bool `json:"truncated,omitempty"`
}

// DiskAttributionEntry maps one agent pane to the build dirs under its live
// cwd. Panes whose cwd contains none of the well-known dirs are omitted.
type DiskAttributionEntry struct {
	Session   string          `json:"session"`
	Pane      string          `json:"pane"` // canonical window.pane address
	Cwd       string          `json:"cwd"`
	BuildDirs []BuildDirUsage `json:"build_dirs"`
}

// diskUsage is one raw statfs observation, produced by the platform-specific
// statDiskUsage implementations (disk_stat_*.go).
type diskUsage struct {
	Path       string // path that was statted
	UsedBytes  uint64 // blocks in use on the filesystem
	AvailBytes uint64 // bytes available to the calling user
}

// statDiskUsageFn indirects the platform statfs so tests can seed usage.
var statDiskUsageFn = statDiskUsage

// collectDiskSection samples the working directory's filesystem and computes
// the trajectory against the previous persisted sample. Returns nil (and the
// callers omit the section) when the filesystem cannot be statted. A nil
// runtime store degrades to a sample-only section: usage is reported but no
// delta can be computed or persisted.
func collectDiskSection() *DiskSection {
	cwd, err := os.Getwd()
	if err != nil {
		slog.Debug("disk sample skipped: cannot resolve working directory", "error", err)
		return nil
	}
	scope := cwd
	if root, rootErr := git.FindProjectRoot(cwd); rootErr == nil && root != "" {
		scope = root
	}
	usage, err := statDiskUsageFn(scope)
	if err != nil {
		slog.Debug("disk sample skipped: statfs failed", "path", scope, "error", err)
		return nil
	}
	var store WatermarkStore
	if s := currentProjectionStore(); s != nil {
		store = s
	}
	return computeDiskSection(store, scope, usage, time.Now().UTC())
}

// computeDiskSection applies the sampling/delta rules for one observation:
//
//   - first-ever sample for the scope: persist it, omit delta
//   - previous sample >= minDiskSampleInterval old: compute a fresh
//     delta_bytes_per_min from (prevAvail - curAvail)/minutes, persist the
//     new sample and the delta
//   - previous sample newer than minDiskSampleInterval: keep the persisted
//     sample untouched and reuse its stored delta (if any) to avoid noise
//
// projected_full_at is emitted only when the resulting delta is positive.
func computeDiskSection(store WatermarkStore, scope string, usage diskUsage, now time.Time) *DiskSection {
	section := &DiskSection{
		MountPath:      scope,
		AvailableBytes: clampToInt64(usage.AvailBytes),
	}
	if denom := float64(usage.UsedBytes) + float64(usage.AvailBytes); denom > 0 {
		section.MountUsedPct = 100 * float64(usage.UsedBytes) / denom
	}

	if store == nil {
		slog.Debug("disk sample not persisted: runtime store unavailable", "scope", scope)
		return section
	}

	wm, err := store.GetWatermark(WatermarkTypeDiskSample, scope)
	if err != nil {
		slog.Debug("disk sample read failed", "scope", scope, "error", err)
		return section
	}

	avail := section.AvailableBytes
	if wm == nil || wm.LastTs == nil {
		persistDiskSample(store, scope, usage.Path, avail, "", now)
		slog.Debug("disk sample recorded (first sample; delta omitted)",
			"scope", scope, "available_bytes", avail)
		return section
	}

	elapsed := now.Sub(wm.LastTs.UTC())
	if elapsed >= minDiskSampleInterval {
		deltaPerMin := float64(wm.LastCursor-avail) / elapsed.Minutes()
		section.DeltaBytesPerMin = &deltaPerMin
		persistDiskSample(store, scope, usage.Path, avail,
			strconv.FormatFloat(deltaPerMin, 'g', -1, 64), now)
		slog.Debug("disk sample recorded",
			"scope", scope,
			"available_bytes", avail,
			"previous_bytes", wm.LastCursor,
			"elapsed", elapsed.Round(time.Second).String(),
			"delta_bytes_per_min", deltaPerMin)
	} else {
		// Too close to the previous sample for a meaningful diff: reuse the
		// last persisted delta and leave the stored sample untouched.
		if wm.BaselineHash != "" {
			if lastDelta, parseErr := strconv.ParseFloat(wm.BaselineHash, 64); parseErr == nil {
				section.DeltaBytesPerMin = &lastDelta
			}
		}
		slog.Debug("disk sample interval too short; reusing last delta",
			"scope", scope,
			"elapsed", elapsed.Round(time.Second).String(),
			"min_interval", minDiskSampleInterval.String(),
			"reused_delta", section.DeltaBytesPerMin != nil)
	}

	if section.DeltaBytesPerMin != nil && *section.DeltaBytesPerMin > 0 {
		minutesToFull := float64(avail) / *section.DeltaBytesPerMin
		if minutesToFull <= maxProjectionMinutes {
			projected := now.Add(time.Duration(minutesToFull * float64(time.Minute)))
			section.ProjectedFullAt = projected.UTC().Format(time.RFC3339)
		}
	}
	return section
}

func persistDiskSample(store WatermarkStore, scope, path string, availBytes int64, deltaStr string, now time.Time) {
	err := store.SetWatermark(&state.OutputWatermark{
		WatermarkType: WatermarkTypeDiskSample,
		Scope:         scope,
		LastCursor:    availBytes,
		LastTs:        &now,
		BaselineHash:  deltaStr,
		Consumer:      path,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		slog.Debug("disk sample write failed", "scope", scope, "error", err)
	}
}

func clampToInt64(v uint64) int64 {
	const maxInt64 = uint64(1)<<63 - 1
	if v > maxInt64 {
		return int64(maxInt64)
	}
	return int64(v)
}

// =============================================================================
// Disk trajectory alert (attention family "disk_trajectory")
// =============================================================================

// diskTrajectoryAlertID mirrors the deterministic-hash ID scheme the alerts
// generator uses (sha256 of "type:session:pane", first 8 bytes hex) so the
// single global trajectory alert dedupes across publishes.
func diskTrajectoryAlertID() string {
	sum := sha256.Sum256([]byte(string(AlertTypeDiskTrajectory) + "::"))
	return hex.EncodeToString(sum[:8])
}

// publishDiskTrajectoryAlert publishes (or resolves) the disk trajectory
// alert against the global tracker, mirroring how disk_low fires: the alert
// lands in the same tracker that alerts.GetActiveAlerts drains for snapshot,
// alert, and attention surfaces. horizonHours <= 0 disables the family
// ([alerts] disk_full_horizon_hours defaults to 0/off).
func publishDiskTrajectoryAlert(horizonHours float64, disk *DiskSection, now time.Time) {
	tracker := alerts.GetGlobalTracker()
	id := diskTrajectoryAlertID()

	resolve := func(reason string) {
		if tracker.ManualResolve(id) {
			slog.Debug("disk trajectory alert resolved", "reason", reason)
		}
	}

	if horizonHours <= 0 {
		resolve("horizon disabled")
		return
	}
	if disk == nil || disk.ProjectedFullAt == "" || disk.DeltaBytesPerMin == nil {
		resolve("no positive fill projection")
		return
	}
	projectedAt, err := time.Parse(time.RFC3339, disk.ProjectedFullAt)
	if err != nil {
		resolve("unparseable projection")
		return
	}
	untilFull := projectedAt.Sub(now)
	horizon := time.Duration(horizonHours * float64(time.Hour))
	if untilFull > horizon {
		resolve("projection beyond horizon")
		return
	}

	severity := alerts.SeverityWarning
	if untilFull <= time.Hour {
		severity = alerts.SeverityCritical
	}
	tracker.AddAlert(alerts.Alert{
		ID:       id,
		Type:     AlertTypeDiskTrajectory,
		Severity: severity,
		Source:   alertSourceDiskTrajectory,
		Message: fmt.Sprintf("Disk projected full at %s (%.1f MB/min, %.1f GB free on %s)",
			disk.ProjectedFullAt,
			*disk.DeltaBytesPerMin/(1024*1024),
			float64(disk.AvailableBytes)/(1024*1024*1024),
			disk.MountPath),
		Context: map[string]interface{}{
			"projected_full_at":   disk.ProjectedFullAt,
			"delta_bytes_per_min": *disk.DeltaBytesPerMin,
			"available_bytes":     disk.AvailableBytes,
			"mount_used_pct":      disk.MountUsedPct,
			"mount_path":          disk.MountPath,
			"horizon_hours":       horizonHours,
		},
	})
	slog.Debug("disk trajectory alert published",
		"projected_full_at", disk.ProjectedFullAt,
		"until_full", untilFull.Round(time.Minute).String(),
		"horizon_hours", horizonHours,
		"severity", string(severity))
}

// publishDiskTrajectoryFromMergedConfig fires the trajectory alert from
// surfaces that do not carry an effective config (GetMetrics). Follows the
// robot package convention of loading the merged config from disk when the
// caller did not supply one; any load failure just skips alerting.
func publishDiskTrajectoryFromMergedConfig(disk *DiskSection, now time.Time) {
	wd, err := os.Getwd()
	if err != nil {
		return
	}
	cfg, err := config.LoadMerged(wd, config.DefaultPath())
	if err != nil || cfg == nil {
		slog.Debug("disk trajectory alert skipped: config unavailable", "error", err)
		return
	}
	publishDiskTrajectoryAlert(cfg.Alerts.DiskFullHorizonHours, disk, now)
}

// attachSnapshotDisk computes the disk section for a snapshot and fires the
// trajectory alert before the snapshot drains alerts.GetActiveAlerts, so a
// within-horizon projection appears in the same snapshot's alert sections.
func attachSnapshotDisk(output *SnapshotOutput, cfg *config.Config) {
	if output == nil {
		return
	}
	output.Disk = collectDiskSection()
	horizon := 0.0
	if cfg != nil {
		horizon = cfg.Alerts.DiskFullHorizonHours
	}
	publishDiskTrajectoryAlert(horizon, output.Disk, time.Now().UTC())
}

// =============================================================================
// Per-pane build-dir attribution (--disk-attribution)
// =============================================================================

// collectPaneBuildDirs resolves each agent pane's live cwd via tmux
// pane_current_path and measures the well-known build dirs directly under it.
// session == "" scans all sessions. Sizes are memoized per build-dir path so
// several panes sharing a cwd pay for one walk.
func collectPaneBuildDirs(ctx context.Context, session string) []DiskAttributionEntry {
	var sessions []string
	if session != "" {
		sessions = []string{session}
	} else {
		list, err := tmux.ListSessions()
		if err != nil {
			slog.Debug("disk attribution skipped: list sessions failed", "error", err)
			return nil
		}
		for _, s := range list {
			sessions = append(sessions, s.Name)
		}
	}

	memo := make(map[string]BuildDirUsage)
	var entries []DiskAttributionEntry
	for _, sess := range sessions {
		panes, err := tmux.GetPanes(sess)
		if err != nil {
			slog.Debug("disk attribution: get panes failed", "session", sess, "error", err)
			continue
		}
		for _, pane := range panes {
			if ctx.Err() != nil {
				slog.Debug("disk attribution budget exhausted", "session", sess)
				return entries
			}
			agentType := paneAgentType(pane)
			if agentType == "" || agentType == "unknown" || agentType == "user" {
				continue
			}
			cwd := paneCurrentPathForTarget(ctx, pane.ID)
			if cwd == "" {
				continue
			}
			dirs := buildDirUsagesUnder(ctx, cwd, memo)
			if len(dirs) == 0 {
				continue
			}
			entries = append(entries, DiskAttributionEntry{
				Session:   sess,
				Pane:      fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index),
				Cwd:       cwd,
				BuildDirs: dirs,
			})
		}
	}
	return entries
}

// buildDirUsagesUnder probes the fixed well-known build dir names at depth 1
// under cwd (cwd/target, cwd/node_modules, ...) and returns bounded sizes for
// the ones that exist as directories.
func buildDirUsagesUnder(ctx context.Context, cwd string, memo map[string]BuildDirUsage) []BuildDirUsage {
	var dirs []BuildDirUsage
	for _, name := range wellKnownBuildDirs {
		if ctx.Err() != nil {
			break
		}
		dir := filepath.Join(cwd, name)
		if usage, ok := memo[dir]; ok {
			dirs = append(dirs, usage)
			continue
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		bytes, truncated := boundedDirSize(ctx, dir, diskBuildDirTimeout, diskBuildDirMaxEntries)
		usage := BuildDirUsage{Path: dir, Bytes: bytes, Truncated: truncated}
		memo[dir] = usage
		dirs = append(dirs, usage)
		slog.Debug("disk attribution build dir measured",
			"dir", dir, "bytes", bytes, "truncated", truncated)
	}
	return dirs
}

// boundedDirSize is the bounded du: it recursively sums regular-file sizes
// under dir, stopping at the per-dir timeout or entry cap. Symlinks are not
// followed (WalkDir does not follow them). On truncation the bytes measured
// so far are returned as a lower bound with truncated=true.
func boundedDirSize(ctx context.Context, dir string, timeout time.Duration, maxEntries int) (int64, bool) {
	deadline := time.Now().Add(timeout)
	var total int64
	var entries int
	truncated := false
	errStop := fmt.Errorf("disk attribution walk bound reached")

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries just don't count
		}
		entries++
		// Checking the clock every entry is ~50ns; negligible next to the stat.
		if entries > maxEntries || time.Now().After(deadline) || ctx.Err() != nil {
			truncated = true
			return errStop
		}
		if d.Type().IsRegular() {
			if info, infoErr := d.Info(); infoErr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if err != nil && err != errStop {
		slog.Debug("disk attribution walk error", "dir", dir, "error", err)
	}
	return total, truncated
}
