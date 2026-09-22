package checkpoint

import (
	"fmt"
	"io"
)

// Tar producers commonly pad the final record with zeros. Permit normal
// padding, but do not let a small compressed suffix expand without a bound.
const maxImportTarPadding int64 = 1 << 20

// validateTarGzipEnd must run after tar.Reader reaches EOF, before publishing
// any checkpoint files. Tar EOF does not imply gzip EOF: the gzip checksum and
// uncompressed-size trailer are validated only when the gzip reader is drained.
// Reject hidden payloads after the tar terminator while allowing zero padding.
func validateTarGzipEnd(r io.Reader) error {
	n, err := io.Copy(checkpointTarPaddingWriter{}, io.LimitReader(r, maxImportTarPadding+1))
	if err != nil {
		return fmt.Errorf("invalid checkpoint archive trailer: %w", err)
	}
	if n > maxImportTarPadding {
		return fmt.Errorf("checkpoint archive trailing padding exceeds %d bytes", maxImportTarPadding)
	}
	return nil
}

type checkpointTarPaddingWriter struct{}

func (checkpointTarPaddingWriter) Write(p []byte) (int, error) {
	for i, b := range p {
		if b != 0 {
			return i, fmt.Errorf("non-zero data after tar end-of-archive marker")
		}
	}
	return len(p), nil
}
