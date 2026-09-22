package checkpoint

import (
	"sort"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Test-only helpers replicating removed production conveniences.

// NewStorageWithDir creates a Storage rooted at a custom directory.
func NewStorageWithDir(dir string) *Storage {
	return &Storage{
		BaseDir: dir,
	}
}

// DefaultImportOptions returns sensible defaults for import.
func DefaultImportOptions() ImportOptions {
	return ImportOptions{
		VerifyChecksums: true,
		AllowOverwrite:  false,
	}
}

// windowLayoutsEqual reports whether two window layout sets are equal.
func windowLayoutsEqual(a, b []WindowLayoutState) bool {
	if len(a) != len(b) {
		return false
	}
	left := cloneWindowLayouts(a)
	right := cloneWindowLayouts(b)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// sortedTmuxPanes compares restored geometry in tests. Production restoration
// pins the pane IDs returned at creation rather than remapping pane positions.
func sortedTmuxPanes(panes []tmux.Pane) []tmux.Pane {
	sorted := append([]tmux.Pane(nil), panes...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].WindowIndex != sorted[j].WindowIndex {
			return sorted[i].WindowIndex < sorted[j].WindowIndex
		}
		return sorted[i].Index < sorted[j].Index
	})
	return sorted
}
