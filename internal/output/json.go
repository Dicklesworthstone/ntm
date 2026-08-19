package output

import (
	"encoding/json"
	"io"
	"os"
	"time"
)

// JSON outputs data as JSON to the formatter's writer
func (f *Formatter) JSON(v interface{}) error {
	return WriteJSON(f.writer, v, f.pretty)
}

// WriteJSON writes data as JSON to the given writer
func WriteJSON(w io.Writer, v interface{}, pretty bool) error {
	encoder := json.NewEncoder(w)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(v)
}

// PrintJSON writes data as JSON to stdout
func PrintJSON(v interface{}) error {
	return WriteJSON(os.Stdout, v, true)
}

// Timestamp returns the current UTC time formatted for JSON output
func Timestamp() time.Time {
	return time.Now().UTC()
}
