// Package watcher provides file and agent-reservation watching.
// The filesystem implementation lives below configuration and orchestration in
// internal/fs/watcher. Keep this API as aliases and forwarding constructors so
// existing callers use that same engine without introducing an import cycle.
package watcher

import (
	"time"

	fswatch "github.com/Dicklesworthstone/ntm/internal/fs/watcher"
)

var ErrClosed = fswatch.ErrClosed

const DefaultPollInterval = fswatch.DefaultPollInterval

type EventType = fswatch.EventType

const (
	Create = fswatch.Create
	Write  = fswatch.Write
	Remove = fswatch.Remove
	Rename = fswatch.Rename
	Chmod  = fswatch.Chmod
	All    = fswatch.All
)

type Event = fswatch.Event
type Handler = fswatch.Handler
type ErrorHandler = fswatch.ErrorHandler
type Watcher = fswatch.Watcher
type Option = fswatch.Option

func New(handler Handler, opts ...Option) (*Watcher, error) {
	return fswatch.New(handler, opts...)
}

func WithDebounceDuration(d time.Duration) Option { return fswatch.WithDebounceDuration(d) }
func WithDebouncer(d *Debouncer) Option           { return fswatch.WithDebouncer(d) }
func WithEventFilter(filter EventType) Option     { return fswatch.WithEventFilter(filter) }
func WithRecursive(recursive bool) Option         { return fswatch.WithRecursive(recursive) }
func WithIgnorePaths(patterns []string) Option    { return fswatch.WithIgnorePaths(patterns) }
func WithPollInterval(d time.Duration) Option     { return fswatch.WithPollInterval(d) }
