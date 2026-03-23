package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GIILAdapter provides integration with the GIIL (Get Image from Internet Link) tool.
// GIIL downloads images from cloud photo sharing services (iCloud, Dropbox, Google Photos,
// Google Drive) with maximum reliability using Playwright.
type GIILAdapter struct {
	*BaseAdapter
}

// NewGIILAdapter creates a new GIIL adapter
func NewGIILAdapter() *GIILAdapter {
	return &GIILAdapter{
		BaseAdapter: NewBaseAdapter(ToolGIIL, "giil"),
	}
}

// Detect checks if giil is installed
func (a *GIILAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed giil version
func (a *GIILAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get giil version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns the list of giil capabilities
func (a *GIILAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{}

	// Check if giil has specific capabilities by examining help output
	path, installed := a.Detect()
	if !installed {
		return caps, nil
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--help")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run() // Ignore error, just check output

	output := stdout.String()

	// Check for known capabilities
	if strings.Contains(output, "--json") {
		caps = append(caps, CapRobotMode)
	}

	return caps, nil
}

// Health checks if giil is functioning correctly
func (a *GIILAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "giil not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a basic health check
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("giil at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "giil is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if giil has a specific capability
func (a *GIILAdapter) HasCapability(ctx context.Context, cap Capability) bool {
	caps, err := a.Capabilities(ctx)
	if err != nil {
		return false
	}
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}

// Info returns complete giil tool information
func (a *GIILAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// GIIL-specific methods

// GIILMetadata represents metadata about a downloaded image
type GIILMetadata struct {
	URL          string `json:"url,omitempty"`
	DirectURL    string `json:"direct_url,omitempty"`
	Filename     string `json:"filename,omitempty"`
	OutputPath   string `json:"output_path,omitempty"`
	Path         string `json:"path,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	Size         int64  `json:"size,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	Platform     string `json:"platform,omitempty"` // icloud, dropbox, google_photos, google_drive
	DownloadedAt string `json:"downloaded_at,omitempty"`
}

// GetDirectURL extracts the direct download URL without actually downloading
func (a *GIILAdapter) GetDirectURL(ctx context.Context, shareURL string) (*GIILMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute) // URL extraction can take time
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), shareURL, "--print-url", "--json", "--quiet")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("giil url extraction failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		raw := strings.TrimSpace(string(output))
		if raw != "" {
			return &GIILMetadata{
				URL:       shareURL,
				DirectURL: raw,
			}, nil
		}
		return &GIILMetadata{URL: shareURL}, nil
	}

	var meta GIILMetadata
	if err := json.Unmarshal(output, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse giil output: %w", err)
	}
	if meta.OutputPath == "" && meta.Path != "" {
		meta.OutputPath = meta.Path
	}
	if meta.Filename == "" && meta.OutputPath != "" {
		meta.Filename = filepath.Base(meta.OutputPath)
	}
	if meta.URL == "" {
		meta.URL = shareURL
	}

	return &meta, nil
}

// Download downloads an image from a share URL
func (a *GIILAdapter) Download(ctx context.Context, shareURL, outputDir string) (*GIILMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute) // Download can take time
	defer cancel()

	args := []string{shareURL, "--json", "--quiet"}
	if outputDir != "" {
		args = append(args, "--output", outputDir)
	}

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("giil download failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		raw := strings.TrimSpace(string(output))
		if raw != "" {
			return &GIILMetadata{
				URL:        shareURL,
				OutputPath: raw,
				Filename:   filepath.Base(raw),
			}, nil
		}
		return &GIILMetadata{URL: shareURL}, nil
	}

	var meta GIILMetadata
	if err := json.Unmarshal(output, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse giil output: %w", err)
	}
	if meta.OutputPath == "" && meta.Path != "" {
		meta.OutputPath = meta.Path
	}
	if meta.Filename == "" && meta.OutputPath != "" {
		meta.Filename = filepath.Base(meta.OutputPath)
	}
	if meta.URL == "" {
		meta.URL = shareURL
	}

	return &meta, nil
}
