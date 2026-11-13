//go:build linux

package pipeline

import (
	"context"
	"os/exec"
	"testing"
)

func BenchmarkProcessWaitContainment(b *testing.B) {
	tiers := []string{
		ContainmentTierCgroupV2,
		ContainmentTierSubreaperPIDFD,
		ContainmentTierProcPGID,
	}

	for _, tier := range tiers {
		b.Run(tier, func(b *testing.B) {
			if tier == ContainmentTierCgroupV2 {
				b.Setenv(CgroupRootEnv, "")
				if _, err := resolveWritableCgroupV2Parent(); err != nil {
					b.Skipf("cgroup_v2 delegation unavailable: %v", err)
				}
			}
			b.Setenv(ContainmentTierEnv, tier)

			b.ReportAllocs()
			statBefore := commandGroupProcStatOpens.Load()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				cmd := exec.Command("/bin/sh", "-c", "(:)& exit 0")
				configureCommandProcessGroup(cmd)
				if _, err := startCommandWithContainment(context.Background(), cmd, commandStartOptions{}); err != nil {
					b.Fatalf("startCommandWithContainment(%s): %v", tier, err)
				}
				cleanup := waitCommandWithProcessGroupCleanup(context.Background(), cmd)
				if !cleanup.Settled || cleanup.Err != nil {
					b.Fatalf("waitCommandWithProcessGroupCleanup(%s): %+v", tier, cleanup)
				}
			}

			b.StopTimer()
			totalStatOpens := commandGroupProcStatOpens.Load() - statBefore
			b.ReportMetric(float64(totalStatOpens)/float64(b.N), "stat_opens/op")
			if (tier == ContainmentTierCgroupV2 || tier == ContainmentTierSubreaperPIDFD) && totalStatOpens != 0 {
				b.Fatalf("tier %s opened /proc/<pid>/stat %d times, want 0", tier, totalStatOpens)
			}
		})
	}
}
