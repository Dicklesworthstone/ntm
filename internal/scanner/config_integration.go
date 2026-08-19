// Package scanner provides UBS scanner integration with config support.
package scanner

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// NewScannerWithConfig creates a new Scanner using config for UBS path
func NewScannerWithConfig(cfg *config.ScannerConfig) (*Scanner, error) {
	if cfg.UBSPath != "" {
		return &Scanner{binaryPath: cfg.UBSPath}, nil
	}
	return New()
}
