package archive

import "time"

// Stats returns archive statistics (test-only helper replicating the removed
// production accessor, used to assert live recording behavior).
func (a *Archiver) Stats() ArchiverStats {
	a.mu.RLock()
	defer a.mu.RUnlock()

	panesTracked := len(a.paneStates)
	totalLines := 0
	for _, state := range a.paneStates {
		totalLines += state.TotalLines
	}

	return ArchiverStats{
		Session:      a.sessionName,
		OutputDir:    a.outputDir,
		Started:      a.started,
		Duration:     time.Since(a.started),
		TotalRecords: a.totalRecords,
		PanesTracked: panesTracked,
		TotalLines:   totalLines,
	}
}
