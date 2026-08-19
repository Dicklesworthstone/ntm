package checkpoint

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
