package serve

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// jobSpawnDeliveryParams supplies work to the shared spawn engine, not a
// second send after spawn. GetSpawn owns readiness, policy, durable assignment
// and prompt delivery; the job keeps its cancellation budget through all of it.
type jobSpawnDeliveryParams struct {
	// Prompt is initial input for every agent. With assign_work it prefixes
	// each assignment in the same delivery rather than starting a second turn.
	Prompt string `json:"prompt,omitempty"`
	// StaggerMode paces prompt delivery, unlike launch_interval, which paces
	// process creation. Smart mode uses the engine's learned provider delays.
	StaggerMode string `json:"stagger_mode,omitempty"`
	// A nil delay selects the fixed-mode default; an explicit "0s" disables
	// spacing. Use a string so the API accepts Go durations, not nanoseconds.
	StaggerDelay *string `json:"stagger_delay,omitempty"`
	// Context enrichment applies to fresh bead assignments, before their
	// durable intent is recorded. Plain initial prompts are not enriched.
	WithCASS   bool `json:"with_cass,omitempty"`
	WithMemory bool `json:"with_memory,omitempty"`
}

func (req jobSpawnDeliveryParams) applyDeliveryOptions(opts *robot.SpawnOptions) error {
	if err := config.ValidateSpawnStaggerMode(req.StaggerMode); err != nil {
		return fmt.Errorf("stagger_mode %w", err)
	}
	var delay time.Duration
	if req.StaggerMode == config.SpawnStaggerFixed {
		delay = config.DefaultSpawnStaggerDelay
	}
	if req.StaggerDelay != nil {
		if req.StaggerMode != config.SpawnStaggerFixed {
			return fmt.Errorf("stagger_delay requires stagger_mode=fixed; smart mode learns its own delay")
		}
		var err error
		delay, err = time.ParseDuration(*req.StaggerDelay)
		if err != nil {
			return fmt.Errorf("stagger_delay must be a duration such as 20s: %w", err)
		}
	}
	if err := config.ValidateSpawnStaggerDelay(delay); err != nil {
		return fmt.Errorf("stagger_delay %w", err)
	}
	if req.StaggerMode != "" && req.StaggerMode != config.SpawnStaggerNone && strings.TrimSpace(req.Prompt) == "" && !opts.AssignWork {
		return fmt.Errorf("stagger_mode requires a non-blank prompt or assign_work=true; use launch_interval to pace process creation")
	}

	if (req.WithCASS || req.WithMemory) && !opts.AssignWork {
		return fmt.Errorf("with_cass and with_memory require assign_work=true")
	}

	// Do not trim, split or deliver the prompt here. The shared engine must
	// see the original text for policy checks and atomic assignment prefixing.
	opts.Prompt = req.Prompt
	opts.StaggerMode = req.StaggerMode
	opts.StaggerDelay = delay
	opts.PromptContext = robot.PromptContextOptions{WithCASS: req.WithCASS, WithMemory: req.WithMemory}
	return nil
}
