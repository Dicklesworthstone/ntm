package watcher

import (
	"time"

	fswatch "github.com/Dicklesworthstone/ntm/internal/fs/watcher"
)

const DefaultDebounceDuration = fswatch.DefaultDebounceDuration

type Debouncer = fswatch.Debouncer

func NewDebouncer(duration time.Duration) *Debouncer {
	return fswatch.NewDebouncer(duration)
}
