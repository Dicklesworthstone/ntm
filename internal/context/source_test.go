package context

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"
)

func TestPrepareSourceContextNative(t *testing.T) {
	files := fstest.MapFS{
		"main.go":  &fstest.MapFile{Data: []byte("package main\nfunc main() {}\n")},
		"lib/a.go": &fstest.MapFile{Data: []byte("package lib\n")},
		"lib/b.go": &fstest.MapFile{Data: []byte("package lib\n// B\n")},
	}
	got, err := prepareSourceContext(context.Background(), files, []string{"main.go", "lib/*.go", "main.go"}, 1000, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "File: \"main.go\"") != 1 || !strings.Contains(got, "func main() {}") {
		t.Fatalf("source content or deduplication missing: %q", got)
	}
	if strings.Index(got, "lib/a.go") > strings.Index(got, "lib/b.go") {
		t.Fatalf("glob order is unstable: %q", got)
	}
	if len(got) > 4000 {
		t.Fatal("budget exceeded")
	}
}

func TestSourceBudgetPreservesEverySelectedFile(t *testing.T) {
	files := fstest.MapFS{
		"large.go": &fstest.MapFile{Data: []byte(strings.Repeat("long line\n", 10000))},
		"small.go": &fstest.MapFile{Data: []byte("package small\n")},
	}
	for _, format := range []string{"", "compact"} {
		got, err := prepareSourceContext(context.Background(), files, []string{"large.go", "small.go"}, 100, format)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > 400 || !strings.Contains(got, "[truncated:") || !strings.Contains(got, "package small") {
			t.Fatalf("budget or fair allocation failed: len=%d text=%q", len(got), got)
		}
		if strings.Count(got, "```\n") != 4 {
			t.Fatalf("unbalanced file fences: %q", got)
		}
	}
}

func TestSourceContextInputErrors(t *testing.T) {
	files := fstest.MapFS{"ok.go": &fstest.MapFile{Data: []byte("package ok")}}
	for _, names := range [][]string{nil, {"../secret"}, {"/etc/passwd"}, {"a/../ok.go"}, {"missing"}, {"missing*.go"}, {"["}, {"bad\nname"}} {
		if _, err := prepareSourceContext(context.Background(), files, names, 100, ""); err == nil {
			t.Errorf("accepted invalid selection %q", names)
		}
	}
	for _, budget := range []int{-1, 0, 1} {
		if _, err := prepareSourceContext(context.Background(), files, []string{"ok.go"}, budget, ""); err == nil {
			t.Errorf("accepted insufficient budget %d", budget)
		}
	}
	if _, err := prepareSourceContext(context.Background(), files, []string{"ok.go"}, 100, "xml"); err == nil {
		t.Fatal("accepted unknown format")
	}
	if _, err := prepareSourceContext(context.Background(), files, []string{"ok.go"}, math.MaxInt, ""); err != nil {
		t.Fatalf("overflowing multiplication for large budget: %v", err)
	}
}

func TestSourceContextBinaryAndUnicode(t *testing.T) {
	files := fstest.MapFS{
		"binary":     &fstest.MapFile{Data: []byte("abc\x00private")},
		"invalid":    &fstest.MapFile{Data: []byte{0xff, 0xfe, 0xff, 0xfe}},
		"unicode.go": &fstest.MapFile{Data: []byte(strings.Repeat("日本語🙂", 1000))},
	}
	got, err := prepareSourceContext(context.Background(), files, []string{"binary", "invalid", "unicode.go"}, 180, "compact")
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got) || len(got) > 720 || strings.Contains(got, "private") || strings.Count(got, "omitted:") != 2 {
		t.Fatalf("invalid encoding, binary leakage, or budget overflow: %q", got)
	}
}

func TestSourceContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := prepareSourceContext(ctx, fstest.MapFS{}, []string{"x"}, 100, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	reader := &sourceContextReader{ctx: ctx, reader: strings.NewReader("secret")}
	if _, err := reader.Read(make([]byte, 10)); !errors.Is(err, context.Canceled) {
		t.Fatalf("read ignored cancellation: %v", err)
	}
}

func TestSourceContextRegularFilesOnly(t *testing.T) {
	for _, mode := range []fs.FileMode{fs.ModeDir, fs.ModeNamedPipe, fs.ModeDevice, fs.ModeSocket} {
		files := fstest.MapFS{"special": &fstest.MapFile{Mode: mode}}
		if _, err := prepareSourceContext(context.Background(), files, []string{"special"}, 100, ""); err == nil {
			t.Errorf("accepted non-regular file %v", mode)
		}
	}
}

func TestSourceContextBoundedRead(t *testing.T) {
	files := fstest.MapFS{"big": &fstest.MapFile{Data: []byte(strings.Repeat("x", maxSourceFileBytes+100))}}
	data, truncated, err := readSourceFile(context.Background(), files, "big", maxSourceFileBytes)
	if err != nil || !truncated || len(data) != maxSourceFileBytes {
		t.Fatalf("unbounded read: len=%d truncated=%v err=%v", len(data), truncated, err)
	}
}

func TestSourceContextLiteralGlobNameAndLimit(t *testing.T) {
	files := fstest.MapFS{"[x].go": &fstest.MapFile{Data: []byte("literal")}}
	names, err := expandSourceFiles(context.Background(), files, []string{"./[x].go", "[x].go"})
	if err != nil || !reflect.DeepEqual(names, []string{"[x].go"}) {
		t.Fatalf("literal glob filename broken: %v %v", names, err)
	}
	if _, err := expandSourceFiles(context.Background(), files, make([]string, maxSourceFiles+1)); err == nil {
		t.Fatal("unbounded pattern count")
	}
}

func TestSourceFenceAndTruncationBoundaries(t *testing.T) {
	text := "```\nthis is source, not a new context section\n```\n" + strings.Repeat("é", 100)
	for budget := 0; budget < 500; budget++ {
		got := renderSourceFile("note.md", text, budget, false, false)
		if len(got) > budget || !utf8.ValidString(got) {
			t.Fatalf("invalid output at byte budget %d: %q", budget, got)
		}
		if got != "" && (!strings.Contains(got, "````\n") || !strings.HasSuffix(got, "\n````\n")) {
			t.Fatalf("unclosed or injectable fence: %q", got)
		}
	}
}

func TestSourceDirectoryAndRecursiveSelections(t *testing.T) {
	files := fstest.MapFS{
		"README.md":             {Data: []byte("overview")},
		"src/main.go":           {Data: []byte("package main")},
		"src/lib/a.go":          {Data: []byte("package lib")},
		"src/lib/deep/b.go":     {Data: []byte("package deep")},
		"src/lib/notes.txt":     {Data: []byte("notes")},
		".git/config":           {Data: []byte("private git configuration")},
		"src/.git/objects/data": {Data: []byte("git object")},
		"src/link":              {Mode: fs.ModeSymlink, Data: []byte("lib")},
	}
	for _, tc := range []struct {
		pattern string
		want    []string
	}{
		{"src", []string{"src/lib/a.go", "src/lib/deep/b.go", "src/lib/notes.txt", "src/main.go"}},
		{"./src/", []string{"src/lib/a.go", "src/lib/deep/b.go", "src/lib/notes.txt", "src/main.go"}},
		{"src/**/*.go", []string{"src/lib/a.go", "src/lib/deep/b.go", "src/main.go"}},
		{"src/**/**/?.go", []string{"src/lib/a.go", "src/lib/deep/b.go"}},
		{"src/*.go", []string{"src/main.go"}},
		{"src/*/*.go", []string{"src/lib/a.go"}},
		{".", []string{"README.md", "src/lib/a.go", "src/lib/deep/b.go", "src/lib/notes.txt", "src/main.go"}},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			got, err := expandSourceFiles(context.Background(), files, []string{tc.pattern})
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("selection %q = %v, %v; want %v", tc.pattern, got, err, tc.want)
			}
		})
	}
	names, err := expandSourceFiles(context.Background(), files, []string{"src/main.go", "src/**/*.go", "src"})
	want := []string{"src/main.go", "src/lib/a.go", "src/lib/deep/b.go", "src/lib/notes.txt"}
	if err != nil || !reflect.DeepEqual(names, want) {
		t.Fatalf("selection order/deduplication = %v, %v; want %v", names, err, want)
	}
	got, err := prepareSourceContext(context.Background(), files, []string{"src/**/*.go"}, 1000, "")
	if err != nil || strings.Count(got, "### File:") != 3 || !strings.Contains(got, "package deep") {
		t.Fatalf("recursive source did not reach rendered pack: %q, %v", got, err)
	}
}

func TestSourceRecursiveSelectionLimits(t *testing.T) {
	files := fstest.MapFS{}
	for i := 0; i <= maxSourceFiles; i++ {
		files[fmt.Sprintf("src/%04d.go", i)] = &fstest.MapFile{Data: []byte("package p")}
	}
	for _, pattern := range []string{"src", "src/*.go", "src/**/*.go"} {
		got, err := expandSourceFiles(context.Background(), files, []string{pattern})
		if err == nil || got != nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized %q selection returned partial success: %v, %v", pattern, got, err)
		}
	}
}

type sourceFailingFS struct {
	fs.FS
	failPath string
	cancel   context.CancelFunc
}

func (f sourceFailingFS) Open(name string) (fs.File, error) {
	if name == f.failPath {
		if f.cancel != nil {
			f.cancel()
		} else {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
		}
	}
	return f.FS.Open(name)
}

func TestSourceRecursiveSelectionFailureIsNotPartialSuccess(t *testing.T) {
	files := fstest.MapFS{
		"src/a.go":         {Data: []byte("package a")},
		"src/nested/b.go":  {Data: []byte("package b")},
		"unrelated/bad.go": {Data: []byte("unrelated")},
	}
	for _, cancelDuringRead := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		wrapped := sourceFailingFS{FS: files, failPath: "src/nested"}
		want := fs.ErrPermission
		if cancelDuringRead {
			wrapped.cancel = cancel
			want = context.Canceled
		}
		got, err := expandSourceFiles(ctx, wrapped, []string{"src/**/*.go"})
		cancel()
		if got != nil || !errors.Is(err, want) {
			t.Fatalf("partial selection leaked on traversal failure: %v, %v; want %v", got, err, want)
		}
	}
	// A narrow pattern must not inspect an irrelevant subtree, even if it is unreadable.
	wrapped := sourceFailingFS{FS: files, failPath: "src/nested"}
	got, err := expandSourceFiles(context.Background(), wrapped, []string{"src/*.go"})
	if err != nil || !reflect.DeepEqual(got, []string{"src/a.go"}) {
		t.Fatalf("single-segment glob descended into unrelated directories: %v, %v", got, err)
	}
}

func TestSourceDirectoryLiteralGlobNamesAndEmptySelections(t *testing.T) {
	files := fstest.MapFS{
		"[literal]/a.go": {Data: []byte("literal directory")},
		"empty":          {Mode: fs.ModeDir},
	}
	got, err := expandSourceFiles(context.Background(), files, []string{"[literal]"})
	if err != nil || !reflect.DeepEqual(got, []string{"[literal]/a.go"}) {
		t.Fatalf("literal directory treated as glob: %v, %v", got, err)
	}
	for _, pattern := range []string{"empty", "empty/**", "**/*.rs", "**/[", "[literal]/../secret"} {
		if got, err := expandSourceFiles(context.Background(), files, []string{pattern}); err == nil || got != nil {
			t.Errorf("invalid/empty selection %q = %v, %v", pattern, got, err)
		}
	}
}

func TestSourceDirectoryDoesNotFollowDiscoveredSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "loop")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "a.go"), filepath.Join(dir, "link.go")); err != nil {
		t.Fatal(err)
	}
	got, err := expandSourceFiles(context.Background(), os.DirFS(dir), []string{"**/*.go"})
	if err != nil || !reflect.DeepEqual(got, []string{"a.go"}) {
		t.Fatalf("followed discovered symlink: %v, %v", got, err)
	}
}

// A directory with only nonmatching entries must still be bounded. Generating
// entries lazily also detects accidental ReadDir(-1) or unbounded read batches.
type sourceEndlessDirectory struct {
	fs.File
	read int
}

func (d *sourceEndlessDirectory) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 || n > 128 {
		return nil, fmt.Errorf("unbounded ReadDir request: %d", n)
	}
	entries := make([]fs.DirEntry, n)
	for i := range entries {
		entries[i] = fs.FileInfoToDirEntry(sourceEntryInfo{name: fmt.Sprintf("entry-%08d.txt", d.read)})
		d.read++
	}
	return entries, nil
}

func (d *sourceEndlessDirectory) Close() error { return nil }

type sourceEntryInfo struct{ name string }

func (i sourceEntryInfo) Name() string       { return i.name }
func (i sourceEntryInfo) Size() int64        { return 0 }
func (i sourceEntryInfo) Mode() fs.FileMode  { return 0 }
func (i sourceEntryInfo) ModTime() time.Time { return time.Time{} }
func (i sourceEntryInfo) IsDir() bool        { return false }
func (i sourceEntryInfo) Sys() any           { return nil }

func TestSourceDiscoveryEntryAndDepthBounds(t *testing.T) {
	dir := &sourceEndlessDirectory{}
	remaining := maxSourceEntries
	_, err := readSourceDirectory(context.Background(), sourceOpenFS{file: dir}, ".", &remaining)
	if err == nil || !strings.Contains(err.Error(), "directory entries") || dir.read != maxSourceEntries+1 {
		t.Fatalf("unbounded directory enumeration: read=%d err=%v", dir.read, err)
	}
	files := fstest.MapFS{strings.Repeat("deep/", maxSourceDepth) + "a.go": {Data: []byte("deep")}}
	if got, err := expandSourceFiles(context.Background(), files, []string{"**/*.go"}); err == nil || got != nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("unbounded recursion: %v, %v", got, err)
	}
	remaining = 5
	if _, err := readSourceDirectory(context.Background(), sourceOpenFS{file: &sourceEmptyDirectory{}}, ".", &remaining); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("non-progressing directory did not fail: %v", err)
	}
}

type sourceOpenFS struct{ file fs.File }

func (f sourceOpenFS) Open(string) (fs.File, error) { return f.file, nil }

type sourceEmptyDirectory struct{ sourceEndlessDirectory }

func (d *sourceEmptyDirectory) ReadDir(int) ([]fs.DirEntry, error) { return nil, nil }
