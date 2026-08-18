package config

import (
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

// Watch starts watching the selected global config file and any project config
// discovered from cwd. It calls onChange with the reloaded merged config when a
// change is detected and returns a close function to stop watching.
func Watch(cwd string, onChange func(*Config)) (func(), error) {
	path := DefaultPath()
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving config path: %w", err)
	}
	path = absPath

	// Create watcher with debounce to avoid multiple reloads on single save
	w, err := watcher.New(func(events []watcher.Event) {
		// We care if global config or project config changed
		shouldReload := false
		for _, e := range events {
			// Check global config
			if filepath.Clean(e.Path) == filepath.Clean(path) {
				shouldReload = true
				break
			}
			// Any other event is a project-config change: only config files
			// are added to the watcher, so reloading on any watched-file
			// event is correct without re-resolving the project path.
			shouldReload = true
		}

		if shouldReload {
			// Reload config (merged with project config)
			cfg, err := LoadMerged(cwd, path)
			if err != nil {
				log.Printf("Error reloading config: %v", err)
				return
			}
			// Notify callback
			if onChange != nil {
				onChange(cfg)
			}
		}
	}, watcher.WithDebounceDuration(500*time.Millisecond))

	if err != nil {
		return nil, fmt.Errorf("creating config watcher: %w", err)
	}

	// Add global config file to watcher
	if err := w.Add(path); err != nil {
		dir := filepath.Dir(path)
		if err := w.Add(dir); err != nil {
			w.Close()
			return nil, fmt.Errorf("watching config path %s: %w", path, err)
		}
	}

	// Add project config file to watcher if it exists
	if projectDir, _, err := FindProjectConfig(cwd); err == nil && projectDir != "" {
		projectConfigPath := filepath.Join(projectDir, ".ntm", "config.toml")
		if err := w.Add(projectConfigPath); err != nil {
			// Non-fatal, just log
			log.Printf("Warning: failed to watch project config: %v", err)
		}
	}

	return func() {
		w.Close()
	}, nil
}
