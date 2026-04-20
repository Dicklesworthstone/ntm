// Package claudeconfig provides read/write access to the persistent Claude
// Code CLI configuration file (~/.claude/settings.json), plus snapshot /
// restore helpers that let a swarm invocation save the user's pre-launch
// model selection, mutate it mid-run (to steer swarm agents), and then put
// the original value back when the swarm finishes.
//
// Design goals:
//
//   - Zero data loss on crash. The snapshot file is the source of truth; if
//     the supervisor dies between Snapshot and Restore, the next invocation
//     can reconcile from disk.
//   - Zero surprise for the user. If no snapshot file exists on shutdown —
//     fresh install, corrupt state, the user explicitly cleared it — we
//     leave the current value alone rather than writing a guess.
//   - Zero config churn if the user has not set a model. Settings file is
//     only written when the model field actually needs to change; we never
//     materialize a settings.json that wasn't there before.
//
// Intended wiring (issue #110):
//
//   - Call Snapshot(...) once, before the swarm launches any agents, with a
//     state-dir-relative path such as
//     <XDG_STATE_HOME>/ntm/<swarm-id>/pre-swarm-claude-model.json.
//   - The existing mid-run `claude --model X` path keeps working unchanged
//     — Claude Code persists --model invocations into settings.json itself,
//     which is the behavior this package is intended to unwind.
//   - Call Restore(...) from every swarm-shutdown path: GracefulShutdown,
//     force-kill teardown, signal handlers. Restore is idempotent and
//     safe to call even if Snapshot never ran (it returns nil).
package claudeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SettingsFilename is the Claude Code CLI's settings file inside the
// CLAUDE_CONFIG_DIR (or ~/.claude by default). Public so tests can override.
const SettingsFilename = "settings.json"

// ModelKey is the JSON field Claude Code uses to persist the user's model
// selection. Public so tests can override if Claude Code ever renames it.
const ModelKey = "model"

// ResolveClaudeSettingsPath returns the absolute path of the Claude Code
// settings file, honoring CLAUDE_CONFIG_DIR in the same way the upstream
// CLI does (see coding_agent_usage_tracker#6 for the upstream-CLI contract).
// Returns the path and whether it was resolved from the env var override.
func ResolveClaudeSettingsPath() (string, bool, error) {
	if trimmed := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); trimmed != "" {
		return filepath.Join(trimmed, SettingsFilename), true, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".claude", SettingsFilename), false, nil
}

// ReadModel returns the currently-persisted Claude Code model value.
//   - (value, true, nil)  — settings.json exists and has a non-empty string `model` field
//   - ("",    false, nil) — settings.json absent, unreadable, or missing `model` field
//   - ("",    false, err) — structural error the caller should surface (JSON syntax, IO error)
//
// The distinction between "no model set" and "file absent" collapses on
// purpose: both cases tell Snapshot that there is nothing to stash, and
// both tell Restore that the user never had a persistent selection so
// writing one now would be a novel change rather than a restoration.
func ReadModel(settingsPath string) (string, bool, error) {
	// #nosec G304 -- caller-supplied path; this is a library function and
	// trusts the caller to pass a settings-file path they chose (typically
	// via ResolveClaudeSettingsPath).
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", settingsPath, err)
	}
	if len(raw) == 0 {
		return "", false, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", false, fmt.Errorf("parse %s: %w", settingsPath, err)
	}
	v, ok := parsed[ModelKey]
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false, nil
	}
	return s, true, nil
}

// WriteModel updates the `model` field in settings.json, preserving every
// other field verbatim. Creates the settings file (and its parent dir) if
// missing. Writes are atomic: marshalled JSON is written to a tmpfile in
// the same directory and renamed into place, so a crash mid-write cannot
// leave a truncated settings.json behind.
//
// If `model` is the empty string, the field is removed from settings.json
// rather than written as "". If that leaves the settings object empty and
// the file did not exist to begin with, the empty file is not created.
func WriteModel(settingsPath, model string) error {
	// #nosec G304 -- caller-supplied path (library function).
	raw, err := os.ReadFile(settingsPath)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}

	parsed := map[string]any{}
	if existed && len(raw) > 0 {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return fmt.Errorf("parse %s: %w", settingsPath, err)
		}
	}

	if model == "" {
		delete(parsed, ModelKey)
	} else {
		parsed[ModelKey] = model
	}

	// Do not materialize a brand-new settings.json just to hold an empty
	// object — leaves the Claude Code config in exactly the shape the user
	// had before.
	if !existed && len(parsed) == 0 {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("ensure parent dir: %w", err)
	}
	encoded, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(settingsPath), ".settings-tmp-*")
	if err != nil {
		return fmt.Errorf("create tmpfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write tmpfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sync tmpfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close tmpfile: %w", err)
	}
	if err := os.Rename(tmpName, settingsPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename tmpfile to settings: %w", err)
	}
	return nil
}

// Snapshot captures the current Claude Code model value into snapshotPath.
// It always writes snapshotPath (even when no model is set) so Restore can
// distinguish "user had no model set" from "snapshot never ran."
//
// The snapshot also records whether settings.json existed at all pre-swarm.
// That distinguishes three pre-swarm user states:
//
//   - No settings.json file at all         → SettingsFileExisted=false, HadModelField=false
//   - settings.json exists, no `model`     → SettingsFileExisted=true,  HadModelField=false
//   - settings.json exists, `model` set    → SettingsFileExisted=true,  HadModelField=true
//
// Restore needs the distinction so it can choose between "remove the
// settings.json the swarm caused to materialize" and "leave settings.json
// alone, just delete the model field."
//
// The snapshot JSON is stable and small; multiple swarms can read/write
// their own snapshots concurrently as long as they use distinct paths.
func Snapshot(settingsPath, snapshotPath string) error {
	model, hasModel, err := ReadModel(settingsPath)
	if err != nil {
		return fmt.Errorf("read pre-launch model: %w", err)
	}
	_, statErr := os.Stat(settingsPath)
	settingsFileExisted := statErr == nil
	snap := snapshotFile{
		Version:             snapshotFormatVersion,
		SettingsPath:        settingsPath,
		Model:               model,
		HadModelField:       hasModel,
		SettingsFileExisted: settingsFileExisted,
	}
	encoded, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o755); err != nil {
		return fmt.Errorf("ensure snapshot dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(snapshotPath), ".snap-tmp-*")
	if err != nil {
		return fmt.Errorf("create snapshot tmpfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write snapshot tmpfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sync snapshot tmpfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close snapshot tmpfile: %w", err)
	}
	if err := os.Rename(tmpName, snapshotPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename snapshot tmpfile: %w", err)
	}
	return nil
}

// Restore writes the pre-launch Claude Code model value back into settings.json
// and then deletes the snapshot file. Returns nil if snapshotPath is absent
// (Snapshot was never run for this swarm, or Restore has already run).
//
// Restore is idempotent: calling it twice after a successful restore is a
// no-op rather than an error. This makes it safe to wire into every
// shutdown code path (graceful, forced, signal) without coordination.
func Restore(snapshotPath string) error {
	// #nosec G304 -- caller-supplied snapshot path (library function).
	raw, err := os.ReadFile(snapshotPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read snapshot: %w", err)
	}
	var snap snapshotFile
	if err := json.Unmarshal(raw, &snap); err != nil {
		return fmt.Errorf("parse snapshot: %w", err)
	}
	if snap.SettingsPath == "" {
		return fmt.Errorf("snapshot missing settings_path; refusing to guess")
	}

	switch {
	case snap.HadModelField:
		// User had a persisted model pre-swarm — put it back.
		if err := WriteModel(snap.SettingsPath, snap.Model); err != nil {
			return fmt.Errorf("restore model: %w", err)
		}
	default:
		// User had no persistent model selection — remove whatever the swarm wrote.
		if err := WriteModel(snap.SettingsPath, ""); err != nil {
			return fmt.Errorf("clear model: %w", err)
		}
		// If the user had NO settings.json pre-swarm but one exists now,
		// and clearing the model field reduced it to an empty object `{}`,
		// remove the file so the user's filesystem state matches pre-swarm.
		// (If the file now has other fields — Claude Code or the swarm
		// wrote them — we leave it alone rather than deleting content we
		// don't understand the provenance of.)
		if !snap.SettingsFileExisted {
			if err := removeIfEmptyObject(snap.SettingsPath); err != nil {
				return fmt.Errorf("remove empty post-restore settings: %w", err)
			}
		}
	}

	if err := os.Remove(snapshotPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove snapshot: %w", err)
	}
	return nil
}

// removeIfEmptyObject deletes `path` iff it parses as an empty JSON object
// `{}`. Any other content — parsed map has entries, non-JSON bytes, JSON
// `null` or non-object JSON, read error — is a no-op return-nil so we never
// destroy content that may matter to the user.
func removeIfEmptyObject(path string) error {
	// #nosec G304 -- caller-supplied path (library function).
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return nil //nolint:nilerr // intentional: non-structural errors on this inspection path are not fatal to Restore.
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil //nolint:nilerr // content is non-JSON or structured differently — leave it alone.
	}
	// JSON `null` unmarshals into a map as nil (not an error, not an empty
	// map). Treat that as "some non-object content we don't understand" and
	// leave it alone, same as non-JSON bytes. Only an actually-empty object
	// `{}` — which json.Unmarshal represents as a non-nil zero-length map —
	// is safe to delete.
	if parsed == nil {
		return nil
	}
	if len(parsed) != 0 {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

const snapshotFormatVersion = 1

type snapshotFile struct {
	Version             int    `json:"version"`
	SettingsPath        string `json:"settings_path"`
	Model               string `json:"model"`
	HadModelField       bool   `json:"had_model_field"`
	SettingsFileExisted bool   `json:"settings_file_existed"`
}
