package context

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
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
	for _, names := range [][]string{nil, {"../secret"}, {"/etc/passwd"}, {"."}, {"a/../ok.go"}, {"missing"}, {"missing*.go"}, {"["}, {"bad\nname"}} {
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
