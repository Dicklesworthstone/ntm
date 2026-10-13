package watcher

import "time"

// These test-only helpers live beside the filesystem implementation. They do
// not pull agent reservation, status, or configuration packages into this layer.
func WithErrorHandler(handler ErrorHandler) Option {
	return func(w *Watcher) { w.errorHandler = handler }
}

func WithPolling(force bool) Option {
	return func(w *Watcher) {
		w.forcePoll = force
		if force {
			w.pollMode = true
		}
	}
}

func (w *Watcher) WatchedPaths() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	paths := make([]string, 0, len(w.watchedPaths))
	for p := range w.watchedPaths {
		paths = append(paths, p)
	}
	return paths
}

func (d *Debouncer) Duration() time.Duration { return d.duration }
