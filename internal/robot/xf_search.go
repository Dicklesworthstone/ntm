// Package robot provides machine-readable output for AI agents.
// xf_search.go implements the --robot-xf-search command (bd-7ijsy).
package robot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// XFSearchOutput represents the output for --robot-xf-search.
type XFSearchOutput struct {
	RobotResponse
	Query string                 `json:"query"`
	Mode  string                 `json:"mode,omitempty"`
	Sort  string                 `json:"sort,omitempty"`
	Count int                    `json:"count"`
	Hits  []tools.XFSearchResult `json:"hits"`
}

// XFSearchOptions configures the GetXFSearch operation.
type XFSearchOptions struct {
	Query string
	Limit int
	Mode  string
	Sort  string
}

// GetXFSearch searches the XF archive and returns structured output.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetXFSearch(opts XFSearchOptions) (*XFSearchOutput, error) {
	query := strings.TrimSpace(opts.Query)
	output := &XFSearchOutput{
		RobotResponse: NewRobotResponse(true),
		Query:         query,
		Mode:          opts.Mode,
		Sort:          opts.Sort,
	}

	if query == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("query is required"),
			ErrCodeInvalidFlag,
			"Provide a search query, e.g., --robot-xf-search='error handling'",
		)
		return output, nil
	}

	adapter := tools.NewXFAdapter()
	if _, installed := adapter.Detect(); !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("xf not installed"),
			ErrCodeDependencyMissing,
			"Install xf (X Find) and ensure it is on PATH",
		)
		return output, nil
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	ctx := context.Background()
	results, err := adapter.Search(ctx, query, limit)
	if err != nil {
		code := ErrCodeInternalError
		hint := "Try a different query or run xf doctor"
		if errors.Is(err, tools.ErrTimeout) {
			code = ErrCodeTimeout
			hint = "XF search timed out; try a shorter query or reduce --limit"
		}
		output.RobotResponse = NewErrorResponse(err, code, hint)
		return output, nil
	}

	if results == nil {
		results = []tools.XFSearchResult{}
	}
	output.Hits = results
	output.Count = len(results)

	return output, nil
}

// PrintXFSearch outputs the XF search response as JSON/TOON.
// This is a thin wrapper around GetXFSearch() for CLI output.
func PrintXFSearch(opts XFSearchOptions) error {
	output, err := GetXFSearch(opts)
	if err != nil {
		return err
	}
	return outputJSON(output)
}

// GetXFStatus returns XF tool health information.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetXFStatus() (*XFStatusOutput, error) {
	adapter := tools.NewXFAdapter()
	output := &XFStatusOutput{
		RobotResponse: NewRobotResponse(true),
	}

	path, installed := adapter.Detect()
	output.XFAvailable = installed
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("xf not installed"),
			ErrCodeDependencyMissing,
			"Install xf (X Find) and ensure it is on PATH",
		)
		return output, nil
	}

	ctx := context.Background()
	ver, err := adapter.Version(ctx)
	if err == nil {
		output.Version = ver.String()
	}

	health, err := adapter.Health(ctx)
	if err == nil && health != nil {
		output.Healthy = health.Healthy
		output.Message = health.Message
	}

	_ = path // used only for detection
	return output, nil
}

// XFStatusOutput represents the output for --robot-xf-status.
type XFStatusOutput struct {
	RobotResponse
	XFAvailable bool   `json:"xf_available"`
	Healthy     bool   `json:"healthy"`
	Version     string `json:"version,omitempty"`
	Message     string `json:"message,omitempty"`
}

// PrintXFStatus outputs the XF status response as JSON/TOON.
// This is a thin wrapper around GetXFStatus() for CLI output.
func PrintXFStatus() error {
	output, err := GetXFStatus()
	if err != nil {
		return err
	}
	return outputJSON(output)
}
