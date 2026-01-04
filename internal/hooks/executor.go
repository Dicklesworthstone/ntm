// Executor provides hook execution with timeout and error handling.
package hooks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ExecutionResult contains the outcome of running a hook
type ExecutionResult struct {
	// Hook is the hook that was executed
	Hook *CommandHook

	// Success indicates if the hook ran without error
	Success bool

	// Skipped is true if the hook was disabled or not applicable
	Skipped bool

	// Error contains any error that occurred
	Error error

	// ExitCode is the command's exit code (-1 if not applicable)
	ExitCode int

	// Stdout captured from the command
	Stdout string

	// Stderr captured from the command
	Stderr string

	// Duration is how long the hook took to run
	Duration time.Duration

	// TimedOut is true if the hook was killed due to timeout
	TimedOut bool
}

// ExecutionContext provides context for hook execution
type ExecutionContext struct {
	// SessionName is the name of the tmux session
	SessionName string

	// ProjectDir is the project working directory
	ProjectDir string

	// Pane is the pane identifier (if applicable)
	Pane string

	// Message is the message being sent (for send hooks)
	Message string

	// AdditionalEnv contains extra environment variables
	AdditionalEnv map[string]string
}

// Executor runs command hooks with proper timeout and error handling
type Executor struct {
	config *CommandHooksConfig
}

// NewExecutor creates a new hook executor with the given configuration
func NewExecutor(config *CommandHooksConfig) *Executor {
	if config == nil {
		config = EmptyCommandHooksConfig()
	}
	return &Executor{config: config}
}

// NewExecutorFromConfig loads configuration and creates an executor
func NewExecutorFromConfig() (*Executor, error) {
	config, err := LoadAllCommandHooks()
	if err != nil {
		return nil, fmt.Errorf("loading hooks config: %w", err)
	}
	return NewExecutor(config), nil
}

// RunHooksForEvent runs all hooks for a specific event
// Returns results for all hooks (including skipped ones)
// Stops on first error unless hook has ContinueOnError set
func (e *Executor) RunHooksForEvent(ctx context.Context, event CommandEvent, execCtx ExecutionContext) ([]ExecutionResult, error) {
	hooks := e.config.GetHooksForEvent(event)
	if len(hooks) == 0 {
		return nil, nil
	}

	results := make([]ExecutionResult, 0, len(hooks))

	for i := range hooks {
		hook := &hooks[i]

		// Check if context is cancelled
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		result := e.runSingleHook(ctx, hook, execCtx)
		results = append(results, result)

		// Stop on error unless continue_on_error is set
		if !result.Success && !result.Skipped && !hook.ContinueOnError {
			return results, result.Error
		}
	}

	return results, nil
}

// runSingleHook executes a single hook
func (e *Executor) runSingleHook(ctx context.Context, hook *CommandHook, execCtx ExecutionContext) ExecutionResult {
	result := ExecutionResult{
		Hook:     hook,
		ExitCode: -1,
	}

	// Check if hook is enabled
	if !hook.IsEnabled() {
		result.Skipped = true
		result.Success = true
		return result
	}

	startTime := time.Now()

	// Create context with timeout
	timeout := hook.GetTimeout()
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Prepare command
	cmd := exec.CommandContext(hookCtx, "sh", "-c", hook.Command)

	// Set working directory
	workDir := hook.ExpandWorkDir(execCtx.SessionName, execCtx.ProjectDir)
	if workDir != "" {
		cmd.Dir = workDir
	}

	// Set environment
	cmd.Env = buildEnvironment(hook, execCtx)

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run the command
	err := cmd.Run()
	result.Duration = time.Since(startTime)
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()

	if err != nil {
		// Check if it was a timeout
		if hookCtx.Err() == context.DeadlineExceeded {
			result.TimedOut = true
			result.Error = fmt.Errorf("hook %q timed out after %v", hook.Name, timeout)
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			result.Error = fmt.Errorf("hook %q failed with exit code %d: %s", hook.Name, result.ExitCode, strings.TrimSpace(result.Stderr))
		} else {
			result.Error = fmt.Errorf("hook %q failed: %w", hook.Name, err)
		}
		result.Success = false
	} else {
		result.Success = true
		result.ExitCode = 0
	}

	return result
}

// buildEnvironment creates the environment for hook execution
func buildEnvironment(hook *CommandHook, execCtx ExecutionContext) []string {
	// Start with current environment
	env := os.Environ()

	// Add standard NTM variables
	ntmEnv := map[string]string{
		"NTM_SESSION":     execCtx.SessionName,
		"NTM_PROJECT_DIR": execCtx.ProjectDir,
		"NTM_PANE":        execCtx.Pane,
	}

	// Add hook event
	if hook != nil {
		ntmEnv["NTM_HOOK_EVENT"] = string(hook.Event)
		if hook.Name != "" {
			ntmEnv["NTM_HOOK_NAME"] = hook.Name
		}
	}

	// Add message for send hooks (truncated for safety)
	if execCtx.Message != "" {
		msg := execCtx.Message
		if len(msg) > 1000 {
			// Find the last rune boundary that allows for "..." suffix within 1000 bytes.
			targetLen := 1000 - 3
			prevI := 0
			for i := range msg {
				if i > targetLen {
					msg = msg[:prevI] + "..."
					break
				}
				prevI = i
			}
			// If loop completed without breaking, all rune starts were <= targetLen
			// but the string may still exceed 1000 bytes due to multi-byte char at end.
			if len(msg) > 1000 {
				msg = msg[:prevI] + "..."
			}
		}
		ntmEnv["NTM_MESSAGE"] = msg
	}

	// Merge NTM env
	for k, v := range ntmEnv {
		env = append(env, k+"="+v)
	}

	// Add hook-specific env
	if hook != nil && hook.Env != nil {
		for k, v := range hook.Env {
			env = append(env, k+"="+v)
		}
	}

	// Add additional context env
	for k, v := range execCtx.AdditionalEnv {
		env = append(env, k+"="+v)
	}

	return env
}

// HasHooksForEvent checks if there are any enabled hooks for an event
func (e *Executor) HasHooksForEvent(event CommandEvent) bool {
	return e.config.HasHooksForEvent(event)
}

// GetHooksForEvent returns all hooks for a specific event
func (e *Executor) GetHooksForEvent(event CommandEvent) []CommandHook {
	return e.config.GetHooksForEvent(event)
}

// AllErrors returns a combined error from all failed results
func AllErrors(results []ExecutionResult) error {
	var errs []string
	for _, r := range results {
		if !r.Success && !r.Skipped && r.Error != nil {
			errs = append(errs, r.Error.Error())
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("hook errors: %s", strings.Join(errs, "; "))
}

// AnyFailed returns true if any hook failed (excluding skipped hooks)
func AnyFailed(results []ExecutionResult) bool {
	for _, r := range results {
		if !r.Success && !r.Skipped {
			return true
		}
	}
	return false
}

// CountResults returns counts of success, failed, and skipped hooks
func CountResults(results []ExecutionResult) (success, failed, skipped int) {
	for _, r := range results {
		if r.Skipped {
			skipped++
		} else if r.Success {
			success++
		} else {
			failed++
		}
	}
	return
}
