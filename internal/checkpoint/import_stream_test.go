package checkpoint

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

func tarGzipTrailerFixture(t *testing.T, suffix []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	data := []byte("checkpoint data")
	if err := tw.WriteHeader(&tar.Header{Name: "metadata.json", Mode: 0600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	raw.Write(suffix)
	return gzipTrailerFixture(t, raw.Bytes())
}

func gzipTrailerFixture(t *testing.T, raw []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	gw := gzip.NewWriter(&encoded)
	if _, err := gw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

// readThroughTarEnd intentionally reproduces the importer's old stopping
// point. Even corrupted gzip fixtures below must reach tar EOF successfully.
func readThroughTarEnd(t *testing.T, encoded []byte) *gzip.Reader {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gr.Close() })
	tr := tar.NewReader(gr)
	for {
		_, err := tr.Next()
		if err == io.EOF {
			return gr
		}
		if err != nil {
			t.Fatalf("fixture failed before tar EOF: %v", err)
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidateTarGzipEndValidPadding(t *testing.T) {
	for _, size := range []int{0, 512, 8192, int(maxImportTarPadding)} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			gr := readThroughTarEnd(t, tarGzipTrailerFixture(t, make([]byte, size)))
			if err := validateTarGzipEnd(gr); err != nil {
				t.Fatalf("padding size %d: %v", size, err)
			}
		})
	}
}

func TestValidateTarGzipEndDetectsCorruptTrailer(t *testing.T) {
	for _, kind := range []string{"crc", "size", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			// Padding ensures the gzip footer remains unread at tar EOF.
			encoded := tarGzipTrailerFixture(t, make([]byte, 8192))
			want := error(gzip.ErrChecksum)
			switch kind {
			case "crc":
				encoded[len(encoded)-8] ^= 1
			case "size":
				encoded[len(encoded)-4] ^= 1
			case "truncated":
				encoded = encoded[:len(encoded)-3]
				want = io.ErrUnexpectedEOF
			}
			gr := readThroughTarEnd(t, encoded)
			if err := validateTarGzipEnd(gr); !errors.Is(err, want) {
				t.Fatalf("trailer error = %v, want %v", err, want)
			}
		})
	}
}

func TestValidateTarGzipEndRejectsTrailingPayload(t *testing.T) {
	for _, kind := range []string{"in-member", "second-member", "garbage", "padding-bomb"} {
		t.Run(kind, func(t *testing.T) {
			var encoded []byte
			switch kind {
			case "in-member":
				encoded = tarGzipTrailerFixture(t, []byte("hidden archive payload"))
			case "second-member":
				encoded = append(tarGzipTrailerFixture(t, nil), gzipTrailerFixture(t, []byte("hidden archive payload"))...)
			case "garbage":
				encoded = append(tarGzipTrailerFixture(t, nil), []byte("not another gzip member")...)
			case "padding-bomb":
				encoded = tarGzipTrailerFixture(t, make([]byte, maxImportTarPadding+1))
			}
			gr := readThroughTarEnd(t, encoded)
			if err := validateTarGzipEnd(gr); err == nil {
				t.Fatal("trailing payload accepted")
			}
		})
	}
}

type failingTrailerReader struct{ err error }

func (r failingTrailerReader) Read([]byte) (int, error) { return 0, r.err }

func TestValidateTarGzipEndPreservesReadError(t *testing.T) {
	want := errors.New("archive device failure")
	if err := validateTarGzipEnd(failingTrailerReader{err: want}); !errors.Is(err, want) {
		t.Fatalf("read error = %v, want %v", err, want)
	}
}

func TestValidateTarGzipEndBoundsRead(t *testing.T) {
	padding := bytes.NewReader(make([]byte, maxImportTarPadding+100))
	if err := validateTarGzipEnd(padding); err == nil || !strings.Contains(err.Error(), "padding exceeds") {
		t.Fatalf("padding error = %v", err)
	}
	if padding.Len() != 99 {
		t.Fatalf("read beyond bounded padding: %d bytes remain, want 99", padding.Len())
	}
}
