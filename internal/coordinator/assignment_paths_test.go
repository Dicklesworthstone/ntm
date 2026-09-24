package coordinator

import (
	"reflect"
	"testing"
	"unicode"
)

func TestAssignmentFileMentionsNormalizeReservationIntent(t *testing.T) {
	for _, tc := range []struct {
		name, title, description string
		want                     []string
	}{
		{"empty", "", "", nil},
		{"prose", "Improve routing", "No files named here", nil},
		{"deduplicate", "Fix src/main.go", "Review src/main.go and tests/main_test.go", []string{"src/main.go", "tests/main_test.go"}},
		{"source locations", "Fix src/main.go:42:7", "See src/main.go#L40-L55 and README.md#design", []string{"src/main.go", "README.md"}},
		{"root location", "Fix main.go:12", "Review Makefile:4", []string{"main.go", "Makefile"}},
		{"markdown local link", "See [guide](docs/guide.md)", "and [code](src/main.go:42)", []string{"docs/guide.md", "src/main.go"}},
		{"markdown remote link", "See [guide](https://example.com/docs/guide.md)", "", nil},
		{"punctuation", "Fix `src/main.go`,", "then (tests/main_test.go).", []string{"src/main.go", "tests/main_test.go"}},
		{"emphasis", "Fix **src/main.go**", "and __README.md__", []string{"src/main.go", "README.md"}},
		{"glob braces", "Review src/*.{go,rs}", "and {main,test}.go", []string{"src/*.{go,rs}", "{main,test}.go"}},
		{"glob classes", "Review [ab]/main.go", "and src/[a-z]*.go", []string{"[ab]/main.go", "src/[a-z]*.go"}},
		{"recursive glob", "Review `src/**/*.go`,", "and *.md", []string{"src/**/*.go", "*.md"}},
		{"numeric file", "Fix 001_init.sql", "and 2026/report.md", []string{"001_init.sql", "2026/report.md"}},
		{"date and quantity", "Process 13714/2m", "on 2026/09/03", nil},
		{"extensionless", "Update Makefile Dockerfile", "and LICENSE", []string{"Makefile", "Dockerfile", "LICENSE"}},
		{"language files", "Update parser.c parser.h", "ui.tsx protocol.proto schema.sql", []string{"parser.c", "parser.h", "ui.tsx", "protocol.proto", "schema.sql"}},
		{"module files", "Update go.mod go.sum", "and Cargo.lock", []string{"go.mod", "go.sum", "Cargo.lock"}},
		{"dotfiles", "Update .env", "and .github/workflows/ci.yml", []string{".env", ".github/workflows/ci.yml"}},
		{"relative paths", "Review ./src/main.go", "and ../shared/main.go", []string{"./src/main.go", "../shared/main.go"}},
		{"windows location", `Review C:\work\main.go:12:2`, "", []string{`C:\work\main.go`}},
		{"windows slash", "Review C:/work/main.go:12", "", []string{"C:/work/main.go"}},
		{"uri schemes", "https://example.com/a.go http://host/a.py", "file:///tmp/a.go s3://bucket/a.json ssh://host/path", nil},
		{"other schemes", "mailto:dev@example.com", "urn:example:file.go custom+v1:src/file.go", nil},
		{"git reference", "git@github.com:owner/repo.git", "owner/repository#123", nil},
		{"angle uri", "<https://example.com/a.go>", "", nil},
		{"bare www uri", "www.example.com/src/main.go", "", nil},
		{"flags are not filenames", "--output=src/main.go", "path=src/main.go", nil},
		{"title and body mixture", "Fix src/main.go:42 using https://example.com/docs", "See [design](docs/design.md#overview), then src/main.go", []string{"src/main.go", "docs/design.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractMentionedFiles(tc.title, tc.description)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractMentionedFiles(%q, %q) = %q, want %q", tc.title, tc.description, got, tc.want)
			}
		})
	}
}

func TestAssignmentFileMentionClassifierRejectsRemoteResources(t *testing.T) {
	for _, remote := range []string{
		"https://example.com/src/main.go", "HTTPS://example.com/a.py", "file:///tmp/main.go",
		"ssh://host/path", "mailto:person@example.com", "custom+v1:src/main.go", "www.example.com/a.go",
	} {
		if isFilePath(remote) {
			t.Errorf("remote resource admitted as a file: %q", remote)
		}
	}
}

func FuzzAssignmentFileMentionsDoNotReserveURLs(f *testing.F) {
	for _, suffix := range []string{"src/main.go", "README.md#design", "a/b:12", "docs/file.go?x=y", ""} {
		f.Add(suffix)
	}
	f.Fuzz(func(t *testing.T, suffix string) {
		// A single URL token must never create a local lease. Whitespace could
		// introduce a separate, legitimate file mention, so omit those cases.
		for _, r := range suffix {
			if unicode.IsSpace(r) {
				return
			}
		}
		got := ExtractMentionedFiles("https://example.com/"+suffix, "")
		if len(got) != 0 {
			t.Fatalf("URL produced reservation intent: %q", got)
		}
	})
}
