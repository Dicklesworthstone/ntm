package integrations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

type fakeRCHBuildStormReader struct {
	availability *tools.RCHAvailability
	status       *tools.RCHStatus
	availErr     error
	statusErr    error
	statusCalls  int
}

func (f *fakeRCHBuildStormReader) GetAvailability(context.Context) (*tools.RCHAvailability, error) {
	if f.availErr != nil {
		return nil, f.availErr
	}
	return f.availability, nil
}

func (f *fakeRCHBuildStormReader) GetStatus(context.Context) (*tools.RCHStatus, error) {
	f.statusCalls++
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	return f.status, nil
}

func TestBuildStormInputFromRCHStatusCountsWorkers(t *testing.T) {
	t.Parallel()

	input := BuildStormInputFromRCHStatus(&tools.RCHStatus{
		Enabled: true,
		Workers: []tools.RCHWorker{
			{Name: "w1", Available: true, Healthy: true, CurrentBuild: "go test"},
			{Name: "w2", Available: true, Healthy: true, Queue: 2},
			{Name: "w3", Available: true, Healthy: true, Load: 81},
			{Name: "w4", Available: true, Healthy: true},
			{Name: "w5", Available: false, Healthy: true, Queue: 4},
		},
	}, &tools.RCHAvailability{
		Available:  true,
		Compatible: true,
	}, RCHBuildStormOptions{
		Session:              "proj",
		RequestedBuilds:      2,
		LocalBuildCount:      1,
		SessionRunningBuilds: 1,
		MaxBuildsPerSession:  3,
	})

	if input.WorkerCount != 5 {
		t.Fatalf("WorkerCount = %d, want 5", input.WorkerCount)
	}
	if input.HealthyWorkers != 4 {
		t.Fatalf("HealthyWorkers = %d, want 4", input.HealthyWorkers)
	}
	if input.BusyWorkers != 3 {
		t.Fatalf("BusyWorkers = %d, want 3", input.BusyWorkers)
	}
	if input.QueueDepth != 2 {
		t.Fatalf("QueueDepth = %d, want 2 (available+healthy workers only)", input.QueueDepth)
	}
	if strings.Compare(input.Session, "proj") != 0 || input.SessionRunningBuilds != 1 || input.MaxBuildsPerSession != 3 {
		t.Fatalf("scheduler fields not preserved: %+v", input)
	}
}

func TestBuildStormInputFromRCHStatusDerivesHealthyFromWorkersWhenSummaryMissing(t *testing.T) {
	t.Parallel()

	input := BuildStormInputFromRCHStatus(&tools.RCHStatus{
		Enabled:      true,
		WorkerCount:  0, // missing in status payload
		HealthyCount: 0, // missing in status payload
		Workers: []tools.RCHWorker{
			{Name: "w1", Available: true, Healthy: true},
			{Name: "w2", Available: true, Healthy: true, CurrentBuild: "go build"},
			{Name: "w3", Available: false, Healthy: true},
		},
	}, &tools.RCHAvailability{
		Available:    true,
		Compatible:   true,
		WorkerCount:  8, // stale cache shape
		HealthyCount: 8, // stale cache shape
	}, RCHBuildStormOptions{})

	if input.WorkerCount != 3 {
		t.Fatalf("WorkerCount = %d, want worker-derived 3 when status summary is missing", input.WorkerCount)
	}
	if input.HealthyWorkers != 2 {
		t.Fatalf("HealthyWorkers = %d, want worker-derived 2 when status healthy_count is missing", input.HealthyWorkers)
	}
	if input.BusyWorkers != 1 {
		t.Fatalf("BusyWorkers = %d, want 1", input.BusyWorkers)
	}
}

func TestBuildStormInputFromRCHStatusDoesNotOverrideExplicitUnavailableAvailability(t *testing.T) {
	t.Parallel()

	input := BuildStormInputFromRCHStatus(&tools.RCHStatus{
		Enabled:      true,
		WorkerCount:  3,
		HealthyCount: 3,
		Workers: []tools.RCHWorker{
			{Name: "w1", Available: true, Healthy: true},
			{Name: "w2", Available: true, Healthy: true},
			{Name: "w3", Available: true, Healthy: true},
		},
	}, &tools.RCHAvailability{
		Available:  false,
		Compatible: false,
	}, RCHBuildStormOptions{})

	if input.RCHAvailable {
		t.Fatalf("RCHAvailable = true, want false when availability explicitly reports unavailable")
	}
	if input.RCHCompatible {
		t.Fatalf("RCHCompatible = true, want false when availability explicitly reports incompatible")
	}

	report := pressure.EvaluateBuildStorm(input)
	if strings.Compare(string(report.Decision), string(pressure.BuildStormAdmit)) != 0 {
		t.Fatalf("Decision = %s, want admit when rch is explicitly unavailable with local headroom", report.Decision)
	}
	if strings.Compare(report.Reason, "rch_unavailable_local_headroom") != 0 {
		t.Fatalf("Reason = %q, want rch_unavailable_local_headroom", report.Reason)
	}
}

func TestBuildStormInputFromRCHStatusPrefersWorkerRosterWhenSummariesAreInflated(t *testing.T) {
	t.Parallel()

	input := BuildStormInputFromRCHStatus(&tools.RCHStatus{
		Enabled:      true,
		WorkerCount:  8, // stale/inflated summary
		HealthyCount: 8, // stale/inflated summary
		Workers: []tools.RCHWorker{
			{Name: "w1", Available: true, Healthy: true},
			{Name: "w2", Available: true, Healthy: true},
			{Name: "w3", Available: false, Healthy: true, Queue: 50},
		},
	}, &tools.RCHAvailability{
		Available:    true,
		Compatible:   true,
		WorkerCount:  8,
		HealthyCount: 8,
	}, RCHBuildStormOptions{
		RequestedBuilds: 3,
	})

	if input.WorkerCount != 3 {
		t.Fatalf("WorkerCount = %d, want roster-derived 3", input.WorkerCount)
	}
	if input.HealthyWorkers != 2 {
		t.Fatalf("HealthyWorkers = %d, want roster-derived 2 available+healthy workers", input.HealthyWorkers)
	}
	if input.QueueDepth != 0 {
		t.Fatalf("QueueDepth = %d, want 0 (queued unavailable worker should not affect pressure)", input.QueueDepth)
	}

	report := pressure.EvaluateBuildStorm(input)
	if strings.Compare(string(report.Decision), string(pressure.BuildStormDefer)) != 0 {
		t.Fatalf("Decision = %s, want defer when requested builds exceed real remote headroom", report.Decision)
	}
	if strings.Compare(report.Reason, "rch_headroom_insufficient") != 0 {
		t.Fatalf("Reason = %q, want rch_headroom_insufficient", report.Reason)
	}
}

func TestEvaluateRCHBuildStormUsesReader(t *testing.T) {
	t.Parallel()

	reader := &fakeRCHBuildStormReader{
		availability: &tools.RCHAvailability{
			Available:    true,
			Compatible:   true,
			WorkerCount:  2,
			HealthyCount: 2,
		},
		status: &tools.RCHStatus{
			Enabled:      true,
			WorkerCount:  2,
			HealthyCount: 2,
			Workers: []tools.RCHWorker{
				{Name: "w1", Available: true, Healthy: true},
				{Name: "w2", Available: true, Healthy: true, CurrentBuild: "go build"},
			},
		},
	}

	report, err := EvaluateRCHBuildStorm(context.Background(), reader, RCHBuildStormOptions{
		Now:             time.Date(2026, 5, 9, 8, 15, 0, 0, time.UTC),
		RequestedBuilds: 1,
	})
	if err != nil {
		t.Fatalf("EvaluateRCHBuildStorm returned error: %v", err)
	}
	if reader.statusCalls != 1 {
		t.Fatalf("statusCalls = %d, want 1", reader.statusCalls)
	}
	if strings.Compare(string(report.Decision), string(pressure.BuildStormOffload)) != 0 {
		t.Fatalf("Decision = %s, want offload", report.Decision)
	}
	if report.WorkerCount != 2 || report.BusyWorkers != 1 || report.QueueDepth != 0 {
		t.Fatalf("report worker fields = %d/%d/%d, want 2/1/0", report.WorkerCount, report.BusyWorkers, report.QueueDepth)
	}
}

func TestEvaluateRCHBuildStormSkipsStatusWhenUnavailable(t *testing.T) {
	t.Parallel()

	reader := &fakeRCHBuildStormReader{
		availability: &tools.RCHAvailability{Available: false, Compatible: false},
		status:       &tools.RCHStatus{Enabled: true, WorkerCount: 3},
	}

	report, err := EvaluateRCHBuildStorm(context.Background(), reader, RCHBuildStormOptions{
		LocalBuildCount: 1,
		MaxLocalBuilds:  4,
	})
	if err != nil {
		t.Fatalf("EvaluateRCHBuildStorm returned error: %v", err)
	}
	if reader.statusCalls != 0 {
		t.Fatalf("statusCalls = %d, want 0", reader.statusCalls)
	}
	if strings.Compare(string(report.Decision), string(pressure.BuildStormAdmit)) != 0 {
		t.Fatalf("Decision = %s, want admit", report.Decision)
	}
	if strings.Compare(report.Reason, "rch_unavailable_local_headroom") != 0 {
		t.Fatalf("Reason = %q, want rch_unavailable_local_headroom", report.Reason)
	}
}

func TestEvaluateRCHBuildStormReturnsReaderErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("status failed")
	reader := &fakeRCHBuildStormReader{
		availability: &tools.RCHAvailability{Available: true, Compatible: true},
		statusErr:    wantErr,
	}

	_, err := EvaluateRCHBuildStorm(context.Background(), reader, RCHBuildStormOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestEvaluateRCHBuildStormIgnoresUnavailableWorkerQueueForPressure(t *testing.T) {
	t.Parallel()

	reader := &fakeRCHBuildStormReader{
		availability: &tools.RCHAvailability{
			Available:    true,
			Compatible:   true,
			WorkerCount:  3,
			HealthyCount: 2,
		},
		status: &tools.RCHStatus{
			Enabled:      true,
			WorkerCount:  3,
			HealthyCount: 2,
			Workers: []tools.RCHWorker{
				{Name: "w1", Available: true, Healthy: true},
				{Name: "w2", Available: true, Healthy: true},
				{Name: "w3", Available: false, Healthy: true, Queue: 50},
			},
		},
	}

	report, err := EvaluateRCHBuildStorm(context.Background(), reader, RCHBuildStormOptions{
		Now:             time.Date(2026, 5, 9, 8, 45, 0, 0, time.UTC),
		RequestedBuilds: 1,
	})
	if err != nil {
		t.Fatalf("EvaluateRCHBuildStorm returned error: %v", err)
	}
	if report.QueueDepth != 0 {
		t.Fatalf("QueueDepth = %d, want 0 when only unavailable workers are queued", report.QueueDepth)
	}
	if strings.Compare(string(report.Decision), string(pressure.BuildStormOffload)) != 0 {
		t.Fatalf("Decision = %s, want offload", report.Decision)
	}
	if strings.Compare(report.Reason, "remote_headroom_available") != 0 {
		t.Fatalf("Reason = %q, want remote_headroom_available", report.Reason)
	}
}
