// Package robot provides machine-readable output for AI agents.
// rch_status.go implements the --robot-rch-status command.
package robot

import (
	"context"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// RCHStatusOutput represents the response from --robot-rch-status.
type RCHStatusOutput struct {
	RobotResponse
	RCH RCHStatusInfo `json:"rch"`
}

// RCHStatusInfo contains RCH status information.
type RCHStatusInfo struct {
	Enabled      bool              `json:"enabled"`
	Available    bool              `json:"available"`
	Version      string            `json:"version,omitempty"`
	Workers      RCHWorkersSummary `json:"workers"`
	SessionStats *RCHSessionStats  `json:"session_stats,omitempty"`
}

// RCHWorkersSummary contains worker counts.
type RCHWorkersSummary struct {
	Total   int `json:"total"`
	Healthy int `json:"healthy"`
	Busy    int `json:"busy"`
}

// RCHSessionStats represents per-session stats (if available).
type RCHSessionStats struct {
	BuildsTotal      int `json:"builds_total"`
	BuildsRemote     int `json:"builds_remote"`
	BuildsLocal      int `json:"builds_local"`
	TimeSavedSeconds int `json:"time_saved_seconds"`
}

// GetRCHStatus returns RCH status information.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetRCHStatus() (*RCHStatusOutput, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adapter := tools.NewRCHAdapter()
	availability, err := adapter.GetAvailability(ctx)
	if err != nil {
		return &RCHStatusOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to check RCH availability"),
			RCH: RCHStatusInfo{
				Enabled:   false,
				Available: false,
				Workers:   RCHWorkersSummary{},
			},
		}, nil
	}

	if availability == nil || !availability.Available {
		return &RCHStatusOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("rch not installed"),
				ErrCodeDependencyMissing,
				"Install rch and ensure it is on PATH",
			),
			RCH: RCHStatusInfo{
				Enabled:   false,
				Available: false,
				Workers:   RCHWorkersSummary{},
			},
		}, nil
	}
	if !availability.Compatible {
		return &RCHStatusOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("rch version incompatible"),
				ErrCodeDependencyMissing,
				"Update rch to a compatible version",
			),
			RCH: RCHStatusInfo{
				Enabled:   false,
				Available: true,
				Workers:   RCHWorkersSummary{},
			},
		}, nil
	}

	status := RCHStatusInfo{
		Enabled:   false,
		Available: true,
		Workers: RCHWorkersSummary{
			Total:   availability.WorkerCount,
			Healthy: availability.HealthyCount,
		},
	}
	if availability.Version.Raw != "" {
		status.Version = availability.Version.String()
	}

	rchStatus, err := adapter.GetStatus(ctx)
	if err != nil {
		return &RCHStatusOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Check the RCH daemon and run rch status --json"),
			RCH:           status,
		}, nil
	}
	if rchStatus != nil {
		status.Enabled = rchStatus.Enabled
		workers := rchStatus.Workers
		status.Workers.Total = len(workers)
		status.Workers.Healthy = countRCHHealthyWorkers(workers)
		status.Workers.Busy = countRCHBusyWorkers(workers)
		if rchStatus.SessionStats != nil {
			status.SessionStats = &RCHSessionStats{
				BuildsTotal:      rchStatus.SessionStats.BuildsTotal,
				BuildsRemote:     rchStatus.SessionStats.BuildsRemote,
				BuildsLocal:      rchStatus.SessionStats.BuildsLocal,
				TimeSavedSeconds: rchStatus.SessionStats.TimeSavedSeconds,
			}
		}
	}

	return &RCHStatusOutput{
		RobotResponse: NewRobotResponse(true),
		RCH:           status,
	}, nil
}

// PrintRCHStatus handles the --robot-rch-status command.
// This is a thin wrapper around GetRCHStatus() for CLI output.
func PrintRCHStatus() error {
	output, err := GetRCHStatus()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot rch status failed")
}

func countRCHHealthyWorkers(workers []tools.RCHWorker) int {
	count := 0
	for _, w := range workers {
		if w.Available && w.Healthy {
			count++
		}
	}
	return count
}

func countRCHBusyWorkers(workers []tools.RCHWorker) int {
	count := 0
	for _, w := range workers {
		if rchWorkerStatus(w) == "busy" {
			count++
		}
	}
	return count
}
