package codeblock

import (
	"testing"
)

func TestParseSimpleBlock(t *testing.T) {
	text := "Some text\n```python\ndef hello():\n    print('hello')\n```\nMore text"

	blocks := ExtractFromText(text)

	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d", len(blocks))
	}

	block := blocks[0]
	if block.Language != "python" {
		t.Errorf("Language = %q, want %q", block.Language, "python")
	}
	if block.Content != "def hello():\n    print('hello')" {
		t.Errorf("Content = %q, want %q", block.Content, "def hello():\n    print('hello')")
	}
}

func TestParseMultipleBlocks(t *testing.T) {
	text := `# Header

` + "```go" + `
package main

func main() {
    fmt.Println("Hello")
}
` + "```" + `

Some text here.

` + "```bash" + `
echo "Hello World"
` + "```" + `
`

	blocks := ExtractFromText(text)

	if len(blocks) != 2 {
		t.Fatalf("Expected 2 blocks, got %d", len(blocks))
	}

	if blocks[0].Language != "go" {
		t.Errorf("Block 0 language = %q, want %q", blocks[0].Language, "go")
	}
	if blocks[1].Language != "bash" {
		t.Errorf("Block 1 language = %q, want %q", blocks[1].Language, "bash")
	}
}

func TestParseNoLanguage(t *testing.T) {
	text := "```\nplain text\n```"

	blocks := ExtractFromText(text)

	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d", len(blocks))
	}

	if blocks[0].Language != "text" {
		t.Errorf("Language = %q, want %q", blocks[0].Language, "text")
	}
}

func TestParseFilePathComment(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantPath  string
		wantIsNew bool
	}{
		{
			name:      "python file comment",
			text:      "```python\n# src/auth.py\ndef auth():\n    pass\n```",
			wantPath:  "src/auth.py",
			wantIsNew: false,
		},
		{
			name:      "js file comment",
			text:      "```javascript\n// src/utils.js\nfunction util() {}\n```",
			wantPath:  "src/utils.js",
			wantIsNew: false,
		},
		{
			name:      "html file comment",
			text:      "```html\n<!-- templates/index.html -->\n<html></html>\n```",
			wantPath:  "templates/index.html",
			wantIsNew: false,
		},
		{
			name:      "go package detection",
			text:      "```go\npackage mypackage\n\nfunc Do() {}\n```",
			wantPath:  "mypackage/mypackage.go",
			wantIsNew: true,
		},
		{
			name:      "go main package",
			text:      "```go\npackage main\n\nfunc main() {}\n```",
			wantPath:  "main.go",
			wantIsNew: true,
		},
		{
			name:      "no path detectable",
			text:      "```python\nprint('hello')\n```",
			wantPath:  "",
			wantIsNew: false, // No path detectable, isNew defaults to false
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocks := ExtractFromText(tc.text)
			if len(blocks) != 1 {
				t.Fatalf("Expected 1 block, got %d", len(blocks))
			}

			if blocks[0].FilePath != tc.wantPath {
				t.Errorf("FilePath = %q, want %q", blocks[0].FilePath, tc.wantPath)
			}
			if blocks[0].IsNew != tc.wantIsNew {
				t.Errorf("IsNew = %v, want %v", blocks[0].IsNew, tc.wantIsNew)
			}
		})
	}
}

func TestNormalizeLanguage(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"js", "javascript"},
		{"JS", "javascript"},
		{"ts", "typescript"},
		{"py", "python"},
		{"rb", "ruby"},
		{"sh", "bash"},
		{"shell", "bash"},
		{"yml", "yaml"},
		{"", "text"},
		{"go", "go"},
		{"rust", "rust"},
		{"cobol", "cobol"}, // not in languageMap, fallback return
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := normalizeLanguage(tc.input)
			if got != tc.expected {
				t.Errorf("normalizeLanguage(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestLooksLikeFilePath(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"src/main.go", true},
		{"file.py", true},
		{"path/to/file.js", true},
		{"./relative.txt", true},
		{"just text", false},
		{"", false},
		{"single", false},
		{"path/", true},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := looksLikeFilePath(tc.input)
			if got != tc.expected {
				t.Errorf("looksLikeFilePath(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestParseLineNumbers(t *testing.T) {
	text := `Line 1
Line 2
` + "```python" + `
code line 1
code line 2
` + "```" + `
Line after`

	blocks := ExtractFromText(text)

	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d", len(blocks))
	}

	// Code block starts at line 3 (0-indexed: lines 0, 1, then fence)
	if blocks[0].StartLine != 3 {
		t.Errorf("StartLine = %d, want %d", blocks[0].StartLine, 3)
	}
	if blocks[0].EndLine != 6 {
		t.Errorf("EndLine = %d, want %d", blocks[0].EndLine, 6)
	}
}

func TestParseEmptyBlocks(t *testing.T) {
	text := "```python\n```"

	blocks := ExtractFromText(text)

	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d", len(blocks))
	}

	if blocks[0].Content != "" {
		t.Errorf("Content = %q, want empty string", blocks[0].Content)
	}
}

func TestParseNestedFences(t *testing.T) {
	// Note: We don't support nested fences - this tests current behavior
	text := "```markdown\n```python\nprint('hello')\n```\n```"

	blocks := ExtractFromText(text)

	// The inner fence should be captured as part of the content
	// This depends on regex matching behavior
	if len(blocks) == 0 {
		t.Error("Expected at least one block")
	}
}

func TestParseVariableLengthFences(t *testing.T) {
	text := "````markdown\n```python\nprint('hello')\n```\n````"
	blocks := ExtractFromText(text)
	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Language != "markdown" {
		t.Errorf("Language = %q, want markdown", blocks[0].Language)
	}
	expectedContent := "```python\nprint('hello')\n```"
	if blocks[0].Content != expectedContent {
		t.Errorf("Content = %q, want %q", blocks[0].Content, expectedContent)
	}
}

func TestCodeBlockString(t *testing.T) {
	block := CodeBlock{
		Language:   "python",
		Content:    "print('hello')",
		StartLine:  10,
		EndLine:    12,
		FilePath:   "src/main.py",
		IsNew:      false,
		SourcePane: "cc_1",
	}

	// Just verify it can be created and fields are accessible
	if block.Language != "python" {
		t.Errorf("Language = %q, want %q", block.Language, "python")
	}
}

func TestExtractionStruct(t *testing.T) {
	extraction := Extraction{
		Source: "myproject:cc_1",
		Blocks: []CodeBlock{
			{Language: "go", Content: "package main"},
		},
		TotalLines: 100,
	}

	if len(extraction.Blocks) != 1 {
		t.Errorf("Blocks count = %d, want 1", len(extraction.Blocks))
	}
	if extraction.Source != "myproject:cc_1" {
		t.Errorf("Source = %q, want %q", extraction.Source, "myproject:cc_1")
	}
}

func TestDetectLanguage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		// Common extensions from extensionMap
		{"go file", "src/main.go", "go"},
		{"python file", "script.py", "python"},
		{"javascript file", "app.js", "javascript"},
		{"typescript file", "index.ts", "typescript"},
		{"rust file", "lib.rs", "rust"},
		{"ruby file", "config.rb", "ruby"},
		{"java file", "Main.java", "java"},
		{"c file", "util.c", "c"},
		{"cpp file", "algo.cpp", "cpp"},
		{"shell script", "deploy.sh", "bash"},
		{"yaml file", "config.yaml", "yaml"},
		{"yml file", "config.yml", "yaml"},
		{"json file", "data.json", "json"},
		{"html file", "index.html", "html"},
		{"css file", "style.css", "css"},
		{"markdown file", "README.md", "markdown"},
		{"sql file", "schema.sql", "sql"},

		// Filename without extension
		{"dockerfile", "Dockerfile", "dockerfile"},
		{"dockerfile lowercase", "dockerfile", "dockerfile"},
		{"makefile", "Makefile", "makefile"},
		{"makefile lowercase", "makefile", "makefile"},
		{"gnumakefile", "GNUmakefile", "makefile"},

		// Case insensitivity for extensions
		{"uppercase extension", "main.GO", "go"},
		{"mixed case extension", "App.Ts", "typescript"},

		// Path with directories
		{"nested path", "src/internal/utils/helper.go", "go"},
		{"deeply nested", "a/b/c/d/e.py", "python"},

		// No detectable language
		{"no extension", "README", ""},
		{"unknown extension", "file.xyz", ""},
		{"empty string", "", ""},
		{"just directory", "path/to/dir/", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DetectLanguage(tc.path)
			if got != tc.want {
				t.Errorf("DetectLanguage(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
