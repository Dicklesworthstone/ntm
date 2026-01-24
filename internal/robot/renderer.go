// Package robot provides machine-readable output for AI agents.
// renderer.go implements the rendering interface for JSON and TOON output formats.
//
// # Renderer Architecture
//
// The renderer provides a single entry point for all robot command output:
//
//	output, err := robot.Render(payload, robot.FormatJSON)
//
// This centralizes serialization logic and allows seamless switching between
// JSON and TOON formats based on user preference or environment.
//
// # Format Selection
//
// The RobotFormat type controls output format:
//   - FormatJSON: Standard JSON (current behavior, fully supported)
//   - FormatTOON: TOON format (token-efficient tabular encoding)
//   - FormatAuto: Automatic selection based on environment (future)
//
// # Content-Type Hints
//
// Each renderer provides a content-type hint for tooling integration:
//   - JSON: "application/json"
//   - TOON: "text/x-toon"
//
// # Backward Compatibility
//
// The JSON renderer produces output identical to the existing encodeJSON/outputJSON
// functions. Migration path:
//
//	// Before:
//	encodeJSON(payload)
//
//	// After (when ready to support format selection):
//	robot.Output(payload, format)
package robot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// RobotFormat specifies the output serialization format for robot commands.
type RobotFormat string

const (
	// FormatJSON produces pretty-printed JSON output.
	// This is the default and matches current robot behavior exactly.
	FormatJSON RobotFormat = "json"

	// FormatTOON produces TOON-encoded output for token efficiency.
	// TOON uses tab-separated values with schema headers for uniform arrays.
	FormatTOON RobotFormat = "toon"

	// FormatAuto selects the format automatically based on environment.
	// Currently defaults to JSON. Future: detect from config or env var.
	FormatAuto RobotFormat = "auto"
)

// String returns the string representation of the format.
func (f RobotFormat) String() string {
	return string(f)
}

// IsValid returns true if the format is a recognized value.
func (f RobotFormat) IsValid() bool {
	switch f {
	case FormatJSON, FormatTOON, FormatAuto:
		return true
	default:
		return false
	}
}

// ParseRobotFormat converts a string to RobotFormat.
// Returns FormatAuto for empty string, error for invalid values.
func ParseRobotFormat(s string) (RobotFormat, error) {
	if s == "" {
		return FormatAuto, nil
	}
	f := RobotFormat(s)
	if !f.IsValid() {
		return "", fmt.Errorf("invalid robot format %q: must be json, toon, or auto", s)
	}
	return f, nil
}

// Renderer is the interface for robot output serialization.
// Implementations encode payloads to their respective formats.
type Renderer interface {
	// Render encodes the payload to the renderer's format.
	// Returns the encoded string and any encoding error.
	Render(payload any) (string, error)

	// ContentType returns the MIME type hint for this format.
	// Useful for HTTP responses or format detection.
	ContentType() string

	// Format returns the RobotFormat this renderer produces.
	Format() RobotFormat
}

// =============================================================================
// JSON Renderer
// =============================================================================

// JSONRenderer produces pretty-printed JSON output.
// This implementation preserves exact compatibility with existing robot output.
type JSONRenderer struct {
	// Indent is the string used for indentation. Default: "  " (two spaces).
	Indent string
}

// NewJSONRenderer creates a JSON renderer with default settings.
func NewJSONRenderer() *JSONRenderer {
	return &JSONRenderer{
		Indent: "  ",
	}
}

// Render encodes the payload as pretty-printed JSON.
func (r *JSONRenderer) Render(payload any) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", r.Indent)
	if err := encoder.Encode(payload); err != nil {
		return "", fmt.Errorf("json encode: %w", err)
	}
	return buf.String(), nil
}

// ContentType returns the JSON MIME type.
func (r *JSONRenderer) ContentType() string {
	return "application/json"
}

// Format returns FormatJSON.
func (r *JSONRenderer) Format() RobotFormat {
	return FormatJSON
}

// =============================================================================
// TOON Renderer
// =============================================================================

// TOONRenderer produces TOON-encoded output for token efficiency.
// TOON (Token-Oriented Object Notation) uses tab-separated values with schema
// headers, providing significant token savings over JSON for AI model consumption.
//
// Encoding is performed by toon_rust's `tru` binary via toonEncode to ensure a
// single canonical implementation across languages/tools.
type TOONRenderer struct {
	// Delimiter is the field separator. Default: "\t" (tab).
	Delimiter string
}

// NewTOONRenderer creates a TOON renderer with default settings.
func NewTOONRenderer() *TOONRenderer {
	return &TOONRenderer{
		Delimiter: "\t",
	}
}

// Render encodes the payload as TOON.
// Returns an error for unsupported payload shapes.
func (r *TOONRenderer) Render(payload any) (string, error) {
	return toonEncode(payload, r.Delimiter)
}

// ContentType returns the TOON MIME type.
func (r *TOONRenderer) ContentType() string {
	return "text/x-toon"
}

// Format returns FormatTOON.
func (r *TOONRenderer) Format() RobotFormat {
	return FormatTOON
}

// =============================================================================
// Global Render Function (Single Entry Point)
// =============================================================================

// defaultRenderer is used when no specific renderer is requested.
var defaultRenderer = NewJSONRenderer()

// Render encodes the payload using the specified format.
// This is the single entry point for all robot command serialization.
//
// Format behavior:
//   - FormatJSON: Pretty-printed JSON (matches current robot output)
//   - FormatTOON: Token-efficient tabular encoding for uniform arrays
//   - FormatAuto: Currently defaults to JSON
//
// Example:
//
//	output, err := robot.Render(myResponse, robot.FormatJSON)
//	if err != nil {
//	    return err
//	}
//	fmt.Print(output)
func Render(payload any, format RobotFormat) (string, error) {
	renderer := GetRenderer(format)
	return renderer.Render(payload)
}

// GetRenderer returns the appropriate renderer for the given format.
// For FormatAuto, currently returns the JSON renderer.
func GetRenderer(format RobotFormat) Renderer {
	switch format {
	case FormatJSON:
		return defaultRenderer
	case FormatTOON:
		return NewTOONRenderer()
	case FormatAuto:
		// Auto currently defaults to JSON
		// Future: detect from NTM_ROBOT_FORMAT env var or config
		return defaultRenderer
	default:
		// Unknown format falls back to JSON
		return defaultRenderer
	}
}

// GetContentType returns the content-type hint for the given format.
// This is useful for tooling that needs to know the output format without
// actually rendering.
func GetContentType(format RobotFormat) string {
	return GetRenderer(format).ContentType()
}

// =============================================================================
// Output Helpers (Write to stdout)
// =============================================================================

// Output renders the payload and writes it to stdout.
// This is the recommended function for robot commands.
//
// Example:
//
//	return robot.Output(myResponse, robot.FormatJSON)
func Output(payload any, format RobotFormat) error {
	return OutputTo(os.Stdout, payload, format)
}

// OutputTo renders the payload and writes it to the specified writer.
// Useful for testing or redirecting output.
func OutputTo(w io.Writer, payload any, format RobotFormat) error {
	output, err := Render(payload, format)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, output)
	return err
}

// =============================================================================
// Render Result (for advanced use cases)
// =============================================================================

// RenderResult contains both the rendered output and metadata.
// Use this when you need the content-type along with the rendered string.
type RenderResult struct {
	// Output is the rendered payload string.
	Output string

	// ContentType is the MIME type of the output.
	ContentType string

	// Format is the format that was used for rendering.
	Format RobotFormat
}

// RenderWithMeta encodes the payload and returns full metadata.
// Use this when you need content-type information (e.g., HTTP responses).
func RenderWithMeta(payload any, format RobotFormat) (RenderResult, error) {
	renderer := GetRenderer(format)
	output, err := renderer.Render(payload)
	if err != nil {
		return RenderResult{}, err
	}
	return RenderResult{
		Output:      output,
		ContentType: renderer.ContentType(),
		Format:      renderer.Format(),
	}, nil
}
