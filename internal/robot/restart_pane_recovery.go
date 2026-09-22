package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// NativeSessionRecovery is a typed internal request to restart one exact agent
// while preserving its existing conversation and launch settings. The caller
// must prove the native binding before constructing this request and confirm
// the resulting binding after startup. Ordinary restart readiness alone does
// not establish conversation recovery.
type NativeSessionRecovery struct {
	ExpectedPane    tmux.Pane
	ExpectedSpec    tmux.AgentLaunchSpec
	WorkingDir      string
	NativeSessionID string
	// BeforeRespawn runs only after replay preflight and a fresh snapshot
	// check, never for dry runs. Account failover uses it to revalidate its
	// process/session proof, activate credentials under a provider lock, and
	// verify the activation. The snapshot is checked again before respawn.
	BeforeRespawn func(context.Context) error
}

func (r *NativeSessionRecovery) validateSnapshot(pane tmux.Pane, spec *tmux.AgentLaunchSpec, dir string) error {
	if r == nil {
		return errors.New("native recovery request is missing")
	}
	expected := r.ExpectedPane
	if expected.ID == "" || expected.PID <= 0 || pane.ID != expected.ID || pane.PID != expected.PID ||
		pane.Type.Canonical() != expected.Type.Canonical() || pane.Title != expected.Title ||
		pane.Index != expected.Index || pane.WindowIndex != expected.WindowIndex ||
		pane.NTMIndex != expected.NTMIndex || pane.IsServicePane() || pane.Dead {
		return errors.New("native recovery pane identity changed")
	}
	if err := r.ExpectedSpec.ValidateReplay(expected.Type); err != nil {
		return fmt.Errorf("native recovery launch inputs: %w", err)
	}
	if spec == nil || !reflect.DeepEqual(*spec, r.ExpectedSpec) {
		return errors.New("native recovery launch specification changed")
	}
	if !filepath.IsAbs(r.WorkingDir) || dir != r.WorkingDir {
		return errors.New("native recovery working directory changed or is not absolute")
	}
	if _, err := tmux.SanitizePaneCommand(dir); err != nil {
		return fmt.Errorf("native recovery working directory: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return errors.New("native recovery working directory is unavailable")
	}
	return nil
}

func (r *NativeSessionRecovery) resumeSpec(saved tmux.AgentLaunchSpec) (tmux.AgentLaunchSpec, error) {
	command, err := agentsession.ResumeLaunchCommand(agentsession.ResumeProvider(string(saved.AgentType)), r.NativeSessionID, saved.Command, agentsession.ResumeLaunchOptions{SystemPromptFile: saved.SystemPromptFile})
	if err != nil {
		return saved, fmt.Errorf("compose native session recovery: %w", err)
	}
	saved.Command = command
	saved.OmittedEnv = append([]string(nil), saved.OmittedEnv...)
	return saved, saved.ValidateReplay(r.ExpectedPane.Type)
}

// Adjust the existing replay plan only after its ordinary preflight succeeds.
// The normal restart executor remains responsible for respawn, specification
// publication, launch delivery, readiness, and reporting partial effects.
func prepareNativeRecoveryLaunchPlan(ctx context.Context, pane tmux.Pane, multiWindow bool, cfg *config.Config, recovery *NativeSessionRecovery, plan *restartLaunchPlan) error {
	key := paneTargetKey(pane, multiWindow)
	spec, exists := plan.Specs[key]
	if !exists || plan.Replay[key] == nil {
		return errors.New("native recovery requires the pane's saved launch specification")
	}
	if err := recovery.validateSnapshot(pane, &spec, plan.Directories[key]); err != nil {
		return err
	}
	spec, err := recovery.resumeSpec(spec)
	if err != nil {
		return err
	}
	replay, err := resilience.PreflightAgentLaunchSpec(ctx, cfg, spec, recovery.WorkingDir)
	if err != nil {
		return err
	}
	plan.Specs[key] = spec
	plan.Commands[key] = spec.Command
	plan.Replay[key] = replay
	return nil
}

func (r *NativeSessionRecovery) observeSnapshot(ctx context.Context, session string, deps RestartPaneDependencies) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	panes, err := deps.ListPanes(ctx, session)
	if err != nil {
		return fmt.Errorf("recheck native recovery pane: %w", err)
	}
	for _, pane := range panes {
		if pane.ID != r.ExpectedPane.ID {
			continue
		}
		spec, err := deps.ReadLaunchSpec(ctx, pane.ID)
		if err != nil {
			return fmt.Errorf("recheck native recovery launch: %w", err)
		}
		dir, err := deps.PaneWorkingDir(ctx, pane.ID)
		if err != nil {
			return fmt.Errorf("recheck native recovery directory: %w", err)
		}
		return r.validateSnapshot(pane, spec, dir)
	}
	return errors.New("native recovery pane is no longer in the session")
}

func (r *NativeSessionRecovery) beforeRespawn(ctx context.Context, session string, deps RestartPaneDependencies) error {
	if err := r.observeSnapshot(ctx, session, deps); err != nil {
		return err
	}
	if r.BeforeRespawn != nil {
		if err := r.BeforeRespawn(ctx); err != nil {
			return err
		}
	}
	return r.observeSnapshot(ctx, session, deps)
}
