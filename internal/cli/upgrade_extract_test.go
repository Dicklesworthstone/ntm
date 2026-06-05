package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bd-i7w7q: extractTarGz / extractZip used unbounded io.Copy. A
// malicious or corrupted release artifact could contain a small
// compressed entry that expands to gigabytes. The fix uses io.CopyN
// with maxArchiveEntryBytes+1 so the function can detect overflow.
// Tests override maxArchiveEntryBytes with a small ceiling to drive
// the detection path without authoring gigabyte fixtures.

func writeTarGzWithEntry(t *testing.T, name string, body []byte, mode int64) string {
	t.Helper()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "test.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer f.Close()
	gzw := gzip.NewWriter(f)
	tw := tar.NewWriter(gzw)
	hdr := &tar.Header{
		Name:     name,
		Mode:     mode,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return archivePath
}

func TestExtractTarGz_NormalEntryUnderCapSucceeds(t *testing.T) {
	prev := maxArchiveEntryBytes
	maxArchiveEntryBytes = 1024 // 1 KB ceiling
	t.Cleanup(func() { maxArchiveEntryBytes = prev })

	archive := writeTarGzWithEntry(t, "ntm", bytes.Repeat([]byte("a"), 100), 0o755)
	dest := t.TempDir()

	binaryPath, err := extractTarGz(archive, dest)
	if err != nil {
		t.Fatalf("extractTarGz failed: %v", err)
	}
	if filepath.Base(binaryPath) != "ntm" {
		t.Errorf("binary path = %q, want path ending in ntm", binaryPath)
	}
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if len(data) != 100 {
		t.Errorf("extracted size = %d, want 100", len(data))
	}
}

func TestExtractTarGz_NestedEntryWithoutDirectoryHeaderSucceeds(t *testing.T) {
	archive := writeTarGzWithEntry(t, "release/ntm", []byte("hello"), 0o755)
	dest := t.TempDir()

	binaryPath, err := extractTarGz(archive, dest)
	if err != nil {
		t.Fatalf("extractTarGz failed: %v", err)
	}
	if filepath.Base(binaryPath) != "ntm" {
		t.Errorf("binary path = %q, want path ending in ntm", binaryPath)
	}
	if _, err := os.Stat(binaryPath); err != nil {
		t.Fatalf("stat extracted nested binary: %v", err)
	}
}

func TestExtractTarGz_RejectsTraversalEntry(t *testing.T) {
	archive := writeTarGzWithEntry(t, "../ntm", []byte("hello"), 0o755)
	dest := t.TempDir()

	_, err := extractTarGz(archive, dest)
	if err == nil {
		t.Fatal("extractTarGz returned nil error for traversal entry")
	}
	if !strings.Contains(err.Error(), "illegal archive entry path") {
		t.Fatalf("error = %q, want illegal archive entry path", err.Error())
	}
}

func TestExtractTarGz_EntryExceedsCapErrorsWithBombMessage(t *testing.T) {
	prev := maxArchiveEntryBytes
	maxArchiveEntryBytes = 1024 // 1 KB ceiling
	t.Cleanup(func() { maxArchiveEntryBytes = prev })

	// Body 2 KB > 1 KB cap.
	archive := writeTarGzWithEntry(t, "ntm", bytes.Repeat([]byte("b"), 2048), 0o755)
	dest := t.TempDir()

	_, err := extractTarGz(archive, dest)
	if err == nil {
		t.Fatalf("extractTarGz returned nil error for oversized entry, want bomb-detection error")
	}
	if !strings.Contains(err.Error(), "decompression bomb") {
		t.Errorf("error = %q, want it to mention decompression bomb", err.Error())
	}
}

func writeZipWithEntry(t *testing.T, name string, body []byte) string {
	t.Helper()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "test.zip")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("zip create entry: %v", err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatalf("zip write body: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return archivePath
}

func TestExtractZip_NormalEntryUnderCapSucceeds(t *testing.T) {
	prev := maxArchiveEntryBytes
	maxArchiveEntryBytes = 1024
	t.Cleanup(func() { maxArchiveEntryBytes = prev })

	archive := writeZipWithEntry(t, "ntm", bytes.Repeat([]byte("a"), 100))
	dest := t.TempDir()

	binaryPath, err := extractZip(archive, dest)
	if err != nil {
		t.Fatalf("extractZip failed: %v", err)
	}
	if filepath.Base(binaryPath) != "ntm" {
		t.Errorf("binary path = %q, want path ending in ntm", binaryPath)
	}
}

func TestExtractZip_EntryExceedsCapErrorsWithBombMessage(t *testing.T) {
	prev := maxArchiveEntryBytes
	maxArchiveEntryBytes = 1024
	t.Cleanup(func() { maxArchiveEntryBytes = prev })

	archive := writeZipWithEntry(t, "ntm", bytes.Repeat([]byte("b"), 2048))
	dest := t.TempDir()

	_, err := extractZip(archive, dest)
	if err == nil {
		t.Fatalf("extractZip returned nil error for oversized entry, want bomb-detection error")
	}
	if !strings.Contains(err.Error(), "decompression bomb") {
		t.Errorf("error = %q, want it to mention decompression bomb", err.Error())
	}
}

func TestExtractZip_RejectsTraversalEntry(t *testing.T) {
	archive := writeZipWithEntry(t, "../ntm", []byte("hello"))
	dest := t.TempDir()

	_, err := extractZip(archive, dest)
	if err == nil {
		t.Fatal("extractZip returned nil error for traversal entry")
	}
	if !strings.Contains(err.Error(), "illegal archive entry path") {
		t.Fatalf("error = %q, want illegal archive entry path", err.Error())
	}
}

// bd-o7fx1: zip entries can carry setuid/setgid/sticky via external_attr,
// which f.Mode() translates to os.FileMode with the Go-level Mode* bits
// set. Without masking, os.OpenFile honors them and rename(2) carries
// the setuid bit into root-owned /usr/local/bin/ntm during a sudo
// upgrade. The tar path is currently safe by accident — os.FileMode(int)
// is a uint32 cast that does not set the Go-level bits — but it is
// hardened defensively against a future refactor to header.FileInfo().Mode().

func writeZipWithEntryAndMode(t *testing.T, name string, body []byte, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "test.zip")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
	hdr.SetMode(mode)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatalf("zip create header: %v", err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatalf("zip write body: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return archivePath
}

// Pin that perm bits 0o755 survive the mask — extraction must still
// produce an executable file. (The tar path is safe-by-accident today;
// this guards the perm-preservation contract for the defensive mask.)
func TestExtractTarGz_PreservesLowerPermBitsThroughMask(t *testing.T) {
	archive := writeTarGzWithEntry(t, "ntm", []byte("hello"), 0o6755)
	dest := t.TempDir()

	binaryPath, err := extractTarGz(archive, dest)
	if err != nil {
		t.Fatalf("extractTarGz failed: %v", err)
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatalf("stat extracted binary: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("extracted file perm = %v, want 0o755 (lower 9 bits preserved)", info.Mode().Perm())
	}
}

func TestExtractZip_StripsSetuidSetgidStickyBits(t *testing.T) {
	archive := writeZipWithEntryAndMode(t, "ntm", []byte("hello"), os.FileMode(0o4755)|os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
	dest := t.TempDir()

	binaryPath, err := extractZip(archive, dest)
	if err != nil {
		t.Fatalf("extractZip failed: %v", err)
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatalf("stat extracted binary: %v", err)
	}
	mode := info.Mode()
	if mode&os.ModeSetuid != 0 {
		t.Errorf("extracted file kept setuid bit (mode = %v), want stripped", mode)
	}
	if mode&os.ModeSetgid != 0 {
		t.Errorf("extracted file kept setgid bit (mode = %v), want stripped", mode)
	}
	if mode&os.ModeSticky != 0 {
		t.Errorf("extracted file kept sticky bit (mode = %v), want stripped", mode)
	}
}
