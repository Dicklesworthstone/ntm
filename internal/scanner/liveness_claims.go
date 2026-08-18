package scanner

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// WS0-G2 config-key liveness claims for keys consumed by internal/scanner
// (bd-g2-claims-backlog-o787y). Only ubs_path is live: the rest of the
// [scanner] section is read solely by functions with no non-test callers
// (ScanOptionsFromConfig/AutoScanner chain) and is re-tagged to bd-6otuk.
// See internal/config/liveness.go.
func init() {
	config.RegisterReader("scanner.ubs_path", NewScannerWithConfig)
}
