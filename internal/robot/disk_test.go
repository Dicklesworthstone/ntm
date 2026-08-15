package robot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func newDiskTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestComputeDiskSectionFirstSampleOmitsDelta(t *testing.T) {
	store := newDiskTestStore(t)
	now := time.Now().UTC()
	usage := diskUsage{Path: "/proj", UsedBytes: 900 << 30, AvailBytes: 100 << 30}

	section := computeDiskSection(store, "/proj", usage, now)
	t.Logf("first sample: used_pct=%.2f avail=%d delta=%v projected=%q",
		section.MountUsedPct, section.AvailableBytes, section.DeltaBytesPerMin, section.ProjectedFullAt)

	if section.DeltaBytesPerMin != nil {
		t.Fatalf("first sample must omit delta, got %v", *section.DeltaBytesPerMin)
	}
	if section.ProjectedFullAt != "" {
		t.Fatalf("first sample must omit projected_full_at, got %q", section.ProjectedFullAt)
	}
	if section.AvailableBytes != int64(100<<30) {
		t.Fatalf("AvailableBytes = %d, want %d", section.AvailableBytes, int64(100<<30))
	}
	if section.MountUsedPct < 89.9 || section.MountUsedPct > 90.1 {
		t.Fatalf("MountUsedPct = %.2f, want ~90", section.MountUsedPct)
	}

	wm, err := store.GetWatermark(WatermarkTypeDiskSample, "/proj")
	if err != nil || wm == nil {
		t.Fatalf("expected persisted watermark, got wm=%v err=%v", wm, err)
	}
	if wm.LastCursor != section.AvailableBytes {
		t.Fatalf("persisted LastCursor = %d, want %d", wm.LastCursor, section.AvailableBytes)
	}
	if wm.BaselineHash != "" {
		t.Fatalf("first sample must not persist a delta, got %q", wm.BaselineHash)
	}
}

func TestComputeDiskSectionDeltaAndProjection(t *testing.T) {
	store := newDiskTestStore(t)
	now := time.Now().UTC()

	// Seed: 10 minutes ago the disk had 100 GiB available.
	prev := now.Add(-10 * time.Minute)
	prevAvail := int64(100 << 30)
	if err := store.SetWatermark(&state.OutputWatermark{
		WatermarkType: WatermarkTypeDiskSample,
		Scope:         "/proj",
		LastCursor:    prevAvail,
		LastTs:        &prev,
		CreatedAt:     prev,
		UpdatedAt:     prev,
	}); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	// Now: 90 GiB available -> 10 GiB consumed in 10 minutes = 1 GiB/min.
	usage := diskUsage{Path: "/proj", UsedBytes: 910 << 30, AvailBytes: 90 << 30}
	section := computeDiskSection(store, "/proj", usage, now)
	t.Logf("sample sequence: t-10m avail=%d -> now avail=%d => delta=%v bytes/min projected=%q",
		prevAvail, section.AvailableBytes, *section.DeltaBytesPerMin, section.ProjectedFullAt)

	if section.DeltaBytesPerMin == nil {
		t.Fatal("expected delta after two spaced samples")
	}
	wantDelta := float64(1 << 30) // 1 GiB per minute
	if got := *section.DeltaBytesPerMin; got < wantDelta*0.999 || got > wantDelta*1.001 {
		t.Fatalf("delta = %f, want ~%f", got, wantDelta)
	}

	// 90 GiB free at 1 GiB/min => full in 90 minutes.
	if section.ProjectedFullAt == "" {
		t.Fatal("expected projected_full_at for positive delta")
	}
	projected, err := time.Parse(time.RFC3339, section.ProjectedFullAt)
	if err != nil {
		t.Fatalf("projected_full_at not RFC3339: %v", err)
	}
	wantFull := now.Add(90 * time.Minute)
	if diff := projected.Sub(wantFull); diff < -time.Minute || diff > time.Minute {
		t.Fatalf("projected_full_at = %v, want ~%v (diff %v)", projected, wantFull, diff)
	}

	// The fresh sample and delta must be persisted for the next invocation.
	wm, err := store.GetWatermark(WatermarkTypeDiskSample, "/proj")
	if err != nil || wm == nil {
		t.Fatalf("expected persisted watermark, got wm=%v err=%v", wm, err)
	}
	if wm.LastCursor != int64(90<<30) {
		t.Fatalf("persisted LastCursor = %d, want %d", wm.LastCursor, int64(90<<30))
	}
	persistedDelta, err := strconv.ParseFloat(wm.BaselineHash, 64)
	if err != nil {
		t.Fatalf("persisted delta %q not parseable: %v", wm.BaselineHash, err)
	}
	if persistedDelta != *section.DeltaBytesPerMin {
		t.Fatalf("persisted delta %f != reported delta %f", persistedDelta, *section.DeltaBytesPerMin)
	}
}

func TestComputeDiskSectionShortIntervalReusesLastDelta(t *testing.T) {
	store := newDiskTestStore(t)
	now := time.Now().UTC()

	prev := now.Add(-10 * time.Second) // closer than minDiskSampleInterval
	prevAvail := int64(80 << 30)
	lastDelta := 12345.5
	if err := store.SetWatermark(&state.OutputWatermark{
		WatermarkType: WatermarkTypeDiskSample,
		Scope:         "/proj",
		LastCursor:    prevAvail,
		LastTs:        &prev,
		BaselineHash:  strconv.FormatFloat(lastDelta, 'g', -1, 64),
		CreatedAt:     prev,
		UpdatedAt:     prev,
	}); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	usage := diskUsage{Path: "/proj", UsedBytes: 921 << 30, AvailBytes: 79 << 30}
	section := computeDiskSection(store, "/proj", usage, now)
	t.Logf("short interval (10s < %s): reused delta=%v", minDiskSampleInterval, *section.DeltaBytesPerMin)

	if section.DeltaBytesPerMin == nil || *section.DeltaBytesPerMin != lastDelta {
		t.Fatalf("expected reused delta %f, got %v", lastDelta, section.DeltaBytesPerMin)
	}

	// The persisted sample must be untouched so the next spaced invocation
	// diffs against the older baseline, not this noisy one.
	wm, err := store.GetWatermark(WatermarkTypeDiskSample, "/proj")
	if err != nil || wm == nil {
		t.Fatalf("expected persisted watermark, got wm=%v err=%v", wm, err)
	}
	if wm.LastCursor != prevAvail {
		t.Fatalf("short-interval sample overwrote baseline: LastCursor = %d, want %d", wm.LastCursor, prevAvail)
	}
}

func TestComputeDiskSectionFreeingDiskOmitsProjection(t *testing.T) {
	store := newDiskTestStore(t)
	now := time.Now().UTC()

	prev := now.Add(-5 * time.Minute)
	if err := store.SetWatermark(&state.OutputWatermark{
		WatermarkType: WatermarkTypeDiskSample,
		Scope:         "/proj",
		LastCursor:    int64(50 << 30),
		LastTs:        &prev,
		CreatedAt:     prev,
		UpdatedAt:     prev,
	}); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	// Available space grew: delta negative, no projection.
	usage := diskUsage{Path: "/proj", UsedBytes: 940 << 30, AvailBytes: 60 << 30}
	section := computeDiskSection(store, "/proj", usage, now)
	t.Logf("freeing disk: delta=%v projected=%q", *section.DeltaBytesPerMin, section.ProjectedFullAt)

	if section.DeltaBytesPerMin == nil || *section.DeltaBytesPerMin >= 0 {
		t.Fatalf("expected negative delta, got %v", section.DeltaBytesPerMin)
	}
	if section.ProjectedFullAt != "" {
		t.Fatalf("projected_full_at must be omitted when delta <= 0, got %q", section.ProjectedFullAt)
	}
}

func TestComputeDiskSectionNilStoreDegrades(t *testing.T) {
	usage := diskUsage{Path: "/proj", UsedBytes: 500 << 30, AvailBytes: 500 << 30}
	section := computeDiskSection(nil, "/proj", usage, time.Now().UTC())
	if section == nil {
		t.Fatal("nil store must still report usage")
	}
	if section.DeltaBytesPerMin != nil || section.ProjectedFullAt != "" {
		t.Fatalf("nil store cannot produce delta/projection, got %v %q",
			section.DeltaBytesPerMin, section.ProjectedFullAt)
	}
}

func TestDiskSectionJSONShapeAndOmission(t *testing.T) {
	// Delta/projection absent: keys must be omitted entirely.
	bare, err := json.Marshal(&DiskSection{MountPath: "/p", MountUsedPct: 42.5, AvailableBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bare section: %s", bare)
	var m map[string]any
	if err := json.Unmarshal(bare, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mount_path", "mount_used_pct", "available_bytes"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("missing required key %q in %s", key, bare)
		}
	}
	for _, key := range []string{"delta_bytes_per_min", "projected_full_at"} {
		if _, ok := m[key]; ok {
			t.Fatalf("key %q must be omitted when unset: %s", key, bare)
		}
	}

	// A zero delta is a real observation and must NOT be omitted.
	zero := 0.0
	full, err := json.Marshal(&DiskSection{MountPath: "/p", DeltaBytesPerMin: &zero})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("zero-delta section: %s", full)
	m = map[string]any{}
	if err := json.Unmarshal(full, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["delta_bytes_per_min"]; !ok {
		t.Fatalf("zero delta must be serialized: %s", full)
	}
}

func TestBoundedDirSizeFixtureTree(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "target")
	if err := os.MkdirAll(filepath.Join(target, "debug", "deps"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile := func(path string, size int) {
		t.Helper()
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(filepath.Join(target, "big.bin"), 4096)
	writeFile(filepath.Join(target, "debug", "lib.rlib"), 2048)
	writeFile(filepath.Join(target, "debug", "deps", "dep.o"), 1024)

	bytes, truncated := boundedDirSize(context.Background(), target, diskBuildDirTimeout, diskBuildDirMaxEntries)
	t.Logf("real du of fixture target/: bytes=%d truncated=%v", bytes, truncated)
	if truncated {
		t.Fatal("small fixture must not truncate")
	}
	if want := int64(4096 + 2048 + 1024); bytes != want {
		t.Fatalf("bytes = %d, want %d", bytes, want)
	}
}

func TestBoundedDirSizeRespectsBounds(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "node_modules")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f"+strconv.Itoa(i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Entry cap of 5 must truncate the 21-entry walk (root + 20 files).
	bytes, truncated := boundedDirSize(context.Background(), dir, time.Minute, 5)
	t.Logf("entry-capped walk: bytes=%d truncated=%v", bytes, truncated)
	if !truncated {
		t.Fatal("expected truncation at entry cap")
	}

	// An already-expired timeout must return promptly with truncated=true.
	start := time.Now()
	bytes, truncated = boundedDirSize(context.Background(), dir, -time.Second, diskBuildDirMaxEntries)
	elapsed := time.Since(start)
	t.Logf("expired-deadline walk: bytes=%d truncated=%v elapsed=%v", bytes, truncated, elapsed)
	if !truncated {
		t.Fatal("expected truncation on expired deadline")
	}
	if elapsed > diskBuildDirTimeout {
		t.Fatalf("expired-deadline walk took %v, want well under %v", elapsed, diskBuildDirTimeout)
	}
}

func TestBuildDirUsagesUnderFixtureLayout(t *testing.T) {
	cwd := t.TempDir()
	// Two well-known dirs present, one regular file with a well-known name
	// (must be skipped), one unrelated dir (must be ignored).
	if err := os.MkdirAll(filepath.Join(cwd, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "target", "out.bin"), make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".venv", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".venv", "lib", "site.py"), make([]byte, 256), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "dist"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	memo := make(map[string]BuildDirUsage)
	dirs := buildDirUsagesUnder(context.Background(), cwd, memo)
	t.Logf("attribution for fixture cwd: %+v", dirs)

	if len(dirs) != 2 {
		t.Fatalf("expected 2 build dirs (target, .venv), got %d: %+v", len(dirs), dirs)
	}
	byPath := map[string]BuildDirUsage{}
	for _, d := range dirs {
		byPath[filepath.Base(d.Path)] = d
	}
	if got := byPath["target"].Bytes; got != 512 {
		t.Fatalf("target bytes = %d, want 512", got)
	}
	if got := byPath[".venv"].Bytes; got != 256 {
		t.Fatalf(".venv bytes = %d, want 256", got)
	}

	// Memoized second pass must not re-walk (same values back).
	again := buildDirUsagesUnder(context.Background(), cwd, memo)
	if len(again) != 2 {
		t.Fatalf("memoized pass returned %d dirs, want 2", len(again))
	}
}

func TestPublishDiskTrajectoryAlertLifecycle(t *testing.T) {
	tracker := alerts.GetGlobalTracker()
	tracker.Clear()
	t.Cleanup(tracker.Clear)

	now := time.Now().UTC()
	delta := float64(512 << 20) // 512 MiB/min
	within := &DiskSection{
		MountPath:        "/proj",
		MountUsedPct:     97,
		AvailableBytes:   10 << 30,
		DeltaBytesPerMin: &delta,
		ProjectedFullAt:  now.Add(2 * time.Hour).Format(time.RFC3339),
	}

	// Horizon disabled (default 0): never fires.
	publishDiskTrajectoryAlert(0, within, now)
	if got := len(tracker.GetActive()); got != 0 {
		t.Fatalf("horizon=0 must not publish, got %d active alerts", got)
	}

	// Within a 4h horizon: fires as warning.
	publishDiskTrajectoryAlert(4, within, now)
	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert, got %d", len(active))
	}
	t.Logf("published alert: type=%s severity=%s msg=%q", active[0].Type, active[0].Severity, active[0].Message)
	if active[0].Type != AlertTypeDiskTrajectory {
		t.Fatalf("alert type = %q, want %q", active[0].Type, AlertTypeDiskTrajectory)
	}
	if active[0].Severity != alerts.SeverityWarning {
		t.Fatalf("severity = %q, want warning for 2h-out projection", active[0].Severity)
	}

	// Projection recedes beyond the horizon: alert resolves.
	receded := *within
	receded.ProjectedFullAt = now.Add(48 * time.Hour).Format(time.RFC3339)
	publishDiskTrajectoryAlert(4, &receded, now)
	if got := len(tracker.GetActive()); got != 0 {
		t.Fatalf("receded projection must resolve the alert, got %d active", got)
	}

	// Imminent (< 1h) projection escalates to critical.
	imminent := *within
	imminent.ProjectedFullAt = now.Add(30 * time.Minute).Format(time.RFC3339)
	publishDiskTrajectoryAlert(4, &imminent, now)
	active = tracker.GetActive()
	if len(active) != 1 || active[0].Severity != alerts.SeverityCritical {
		t.Fatalf("expected 1 critical alert for 30m-out projection, got %+v", active)
	}
	t.Logf("imminent alert: severity=%s context=%v", active[0].Severity, active[0].Context)
}

func TestMetricsOutputIncludesDiskSection(t *testing.T) {
	store := newDiskTestStore(t)
	oldStore := currentProjectionStore()
	SetProjectionStore(store)
	t.Cleanup(func() { SetProjectionStore(oldStore) })

	out, err := GetMetrics(MetricsOptions{Period: "24h"})
	if err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}
	if !out.Success {
		t.Fatalf("GetMetrics not successful: %+v", out.RobotResponse)
	}
	if out.Disk == nil {
		t.Fatal("metrics output must include the disk section on supported platforms")
	}
	t.Logf("live disk section: mount=%s used_pct=%.1f avail=%d delta=%v projected=%q",
		out.Disk.MountPath, out.Disk.MountUsedPct, out.Disk.AvailableBytes,
		out.Disk.DeltaBytesPerMin, out.Disk.ProjectedFullAt)
	if out.Disk.MountUsedPct < 0 || out.Disk.MountUsedPct > 100 {
		t.Fatalf("mount_used_pct out of range: %f", out.Disk.MountUsedPct)
	}
	if out.Disk.AvailableBytes < 0 {
		t.Fatalf("available_bytes negative: %d", out.Disk.AvailableBytes)
	}
	// First-ever sample in a fresh store: delta and projection omitted.
	if out.Disk.DeltaBytesPerMin != nil || out.Disk.ProjectedFullAt != "" {
		t.Fatalf("fresh store must omit delta/projection, got %v %q",
			out.Disk.DeltaBytesPerMin, out.Disk.ProjectedFullAt)
	}
	if out.DiskAttribution != nil {
		t.Fatalf("disk attribution must be off by default, got %+v", out.DiskAttribution)
	}
}
