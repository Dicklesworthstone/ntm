package robot

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
)

// BackpressureOutput is the robot-readable envelope for overload snapshots.
type BackpressureOutput struct {
	RobotResponse
	Snapshot backpressure.BackpressureSnapshot `json:"backpressure"`
}

// CommandBackpressureStats describes one robot command execution pressure row.
type CommandBackpressureStats struct {
	Command       string
	Session       string
	Pane          string
	QueueDepth    int
	QueueCapacity int
	Latency       time.Duration
	SourceLoaded  bool
}
