package serve

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// ConfigureSpawnPolicy binds the server's spawn service to the operator's
// selected global configuration. Call it during construction, before restoring
// jobs or serving requests; it is not a runtime configuration setter. Both the
// synchronous endpoint and queued swarm jobs use this same service.
//
// Only the absolute config path is captured here. Each execution strictly loads
// that file and its own launch project's overlay, so a queued job never borrows
// an overlay from the server's later project selection. An explicitly selected
// file disappearing is a refusal, not permission to use built-in defaults.
func (s *Server) ConfigureSpawnPolicy(globalPath string, requireGlobal bool) error {
	if s == nil {
		return errors.New("spawn policy requires a server")
	}
	if strings.TrimSpace(globalPath) == "" {
		return errors.New("spawn policy requires a selected global config path")
	}
	selected, err := filepath.Abs(globalPath)
	if err != nil {
		return fmt.Errorf("resolve spawn config path: %w", err)
	}
	s.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		return s.spawnWithPolicy(ctx, opts, selected, requireGlobal,
			config.LoadAssignmentPolicyStrict, robot.GetSpawn)
	}
	return nil
}

// spawnWithPolicy supplies inputs to GetSpawn, not another admission or launch
// engine. The narrow ports let tests exercise the policy boundary without
// starting agents. A new config is loaded per call: MergeConfig mutates its
// input, so sharing a merged pointer would leak project policy across jobs.
func (s *Server) spawnWithPolicy(
	ctx context.Context, opts robot.SpawnOptions, globalPath string, requireGlobal bool,
	load func(string, string, bool) (*config.Config, error),
	spawn func(context.Context, robot.SpawnOptions, *config.Config) (*robot.SpawnOutput, error),
) (*robot.SpawnOutput, error) {
	failure := func(err error, code, hint string) (*robot.SpawnOutput, error) {
		return &robot.SpawnOutput{
			RobotResponse: robot.NewErrorResponse(err, code, hint),
			Session:       opts.Session, WorkingDir: opts.WorkingDir,
			CreatedAt:  time.Now().UTC().Format(time.RFC3339),
			PresetUsed: opts.Preset, DryRun: opts.DryRun, Layout: "tiled",
			Agents: []robot.SpawnedAgent{}, Error: err.Error(),
		}, nil
	}
	if ctx == nil {
		return failure(errors.New("spawn policy requires a context"), robot.ErrCodeInvalidFlag, "Supply a cancellable spawn context")
	}
	if err := ctx.Err(); err != nil {
		return failure(err, robot.ErrCodeTimeout, "Spawn was cancelled before policy loading")
	}

	// Queued jobs already supply an absolute admitted directory. Synchronous
	// spawns resolve relative paths once against the selected project, not CWD.
	project := opts.WorkingDir
	if !filepath.IsAbs(project) {
		project = filepath.Join(s.projectDirSnapshot(), project)
	}
	project, err := filepath.Abs(project)
	if err != nil {
		return failure(fmt.Errorf("resolve spawn project: %w", err), robot.ErrCodeInvalidFlag, "Select an accessible launch directory")
	}
	opts.WorkingDir = project
	// HTTP clients cannot substitute a different global policy. Also forward
	// this binding to GetSpawn's authoritative assignment preflight.
	opts.ConfigPath, opts.RequireConfig = globalPath, requireGlobal
	effective, err := load(project, globalPath, requireGlobal)
	if cancelErr := ctx.Err(); cancelErr != nil {
		return failure(cancelErr, robot.ErrCodeTimeout, "Spawn was cancelled during policy loading")
	}
	if err != nil {
		return failure(fmt.Errorf("load spawn policy: %w", err), robot.ErrCodeInvalidFlag,
			"Fix the server's selected global config and the launch project's .ntm/config.toml")
	}
	if effective == nil {
		return failure(errors.New("spawn policy loader returned no configuration"), robot.ErrCodeInternalError,
			"Restore the configured spawn policy before retrying")
	}
	if opts.PromptContext.Enabled() {
		opts.PromptContext = spawnPromptContextOptions(opts.PromptContext, effective)
	}
	return spawn(ctx, opts, effective)
}
