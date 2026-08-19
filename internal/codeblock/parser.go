// Package codeblock provides markdown code block parsing and extraction.
package codeblock

import (
	"path/filepath"
	"strings"
)

// CodeBlock represents a single code block extracted from markdown.
type CodeBlock struct {
	Language   string `json:"language"`              // From fence (python, bash, etc.)
	Content    string `json:"content"`               // The code itself
	StartLine  int    `json:"start_line"`            // Line number in source
	EndLine    int    `json:"end_line"`              // Ending line number
	FilePath   string `json:"file_path,omitempty"`   // Detected file path (if any)
	IsNew      bool   `json:"is_new,omitempty"`      // Appears to be new file vs modification
	SourcePane string `json:"source_pane,omitempty"` // Pane ID where block was found
}

// Extraction contains all code blocks from a source.
type Extraction struct {
	Source     string      `json:"source"`      // Session/pane identifier
	Blocks     []CodeBlock `json:"blocks"`      // Extracted code blocks
	TotalLines int         `json:"total_lines"` // Total lines in source
}

// Parser extracts code blocks from text.
type Parser struct {
	// Options for parsing
	LanguageFilter []string // Only extract blocks with these languages (empty = all)
}

// NewParser creates a new code block parser.
func NewParser() *Parser {
	return &Parser{}
}

// WithLanguageFilter sets languages to filter for.
func (p *Parser) WithLanguageFilter(langs []string) *Parser {
	p.LanguageFilter = langs
	return p
}

// Parse extracts code blocks from the given text.
func (p *Parser) Parse(text string) []CodeBlock {
	var blocks []CodeBlock
	lines := strings.Split(text, "\n")

	inBlock := false
	fenceLen := 0
	var currentLang string
	var currentContent strings.Builder
	var startLine int

	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r") // Handle CRLF

		// Check for fence
		// A fence line starts with optional whitespace (up to 3 spaces, but we'll assume 0 for simplicity/strictness like the regex)
		// and then at least 3 backticks.
		// The previous regex was ^```, so it required start of line. We'll stick to that.

		isFence := false
		fl := 0
		if strings.HasPrefix(trimmed, "```") {
			// Count backticks
			for j := 0; j < len(trimmed); j++ {
				if trimmed[j] == '`' {
					fl++
				} else {
					break
				}
			}
			isFence = fl >= 3
		}

		if isFence {
			if !inBlock {
				// Start of block
				inBlock = true
				fenceLen = fl
				startLine = i + 1 // 1-based
				// Language is the rest of the line after backticks
				currentLang = strings.TrimSpace(trimmed[fl:])
				currentContent.Reset()
			} else {
				// Potential end of block
				// Closing fence must be at least as long as opening fence
				// and contain no info string (though some parsers allow it, standard markdown says no info string on closing)
				// We'll just check length to be safe and simple.
				// Also, check if it's "just" a fence or fence+stuff.
				// Standard: closing fence is ` ``` ` (at least len) and optional spaces.
				// We'll check if the line *starts* with fence of sufficient length.
				if fl >= fenceLen {
					// Close block
					inBlock = false

					// Apply filter
					if len(p.LanguageFilter) == 0 || p.matchesLanguage(currentLang) {
						content := currentContent.String()
						// Remove trailing newline added by loop
						content = strings.TrimSuffix(content, "\n")

						filePath, isNew := detectFilePath(content, currentLang)

						blocks = append(blocks, CodeBlock{
							Language:  normalizeLanguage(currentLang),
							Content:   content,
							StartLine: startLine,
							EndLine:   i + 1,
							FilePath:  filePath,
							IsNew:     isNew,
						})
					}
				} else {
					// Fence too short, treat as content
					currentContent.WriteString(trimmed)
					currentContent.WriteString("\n")
				}
			}
		} else {
			if inBlock {
				currentContent.WriteString(trimmed)
				currentContent.WriteString("\n")
			}
		}
	}

	return blocks
}

// languageMap maps aliases to canonical language names
var languageMap = map[string]string{
	"js": "javascript", "jsx": "javascript", "javascript": "javascript",
	"ts": "typescript", "tsx": "typescript", "typescript": "typescript",
	"py": "python", "python": "python",
	"rb": "ruby", "ruby": "ruby",
	"sh": "bash", "shell": "bash", "zsh": "bash", "bash": "bash",
	"yml": "yaml", "yaml": "yaml",
	"dockerfile": "docker", "docker": "docker",
	"md": "markdown", "markdown": "markdown",
	"c++": "cpp", "cpp": "cpp", "cc": "cpp", "cxx": "cpp", "hpp": "cpp",
	"c": "c", "h": "c",
	"cs": "csharp", "csharp": "csharp",
	"rs": "rust", "rust": "rust",
	"kt": "kotlin", "kotlin": "kotlin",
	"go":   "go",
	"java": "java",
	"php":  "php",
	"html": "html",
	"css":  "css",
	"scss": "scss", "sass": "scss",
	"less": "less",
	"sql":  "sql",
	"json": "json",
	"xml":  "xml",
	"toml": "toml",
	"lua":  "lua",
	"pl":   "perl", "perl": "perl",
	"r":     "r",
	"swift": "swift",
	"scala": "scala",
	"el":    "elisp", "lisp": "lisp",
	"clj": "clojure",
	"tf":  "terraform",
	"vim": "vim",
}

// matchesLanguage checks if a language matches the filter.
func (p *Parser) matchesLanguage(lang string) bool {
	lang = normalizeLanguage(lang)
	for _, filter := range p.LanguageFilter {
		filter = normalizeLanguage(filter)
		if lang == filter {
			return true
		}
	}
	return false
}

// normalizeLanguage normalizes language names.
func normalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return "text"
	}
	if canonical, ok := languageMap[lang]; ok {
		return canonical
	}
	return lang
}

// detectFilePath attempts to detect the file path from code block content.
func detectFilePath(content, lang string) (path string, isNew bool) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return "", false
	}

	firstLine := strings.TrimSpace(lines[0])

	// Check first line for path comment patterns
	// Python/Shell: # path/to/file.py
	if strings.HasPrefix(firstLine, "# ") {
		candidate := strings.TrimPrefix(firstLine, "# ")
		if looksLikeFilePath(candidate) {
			return candidate, false
		}
	}

	// JavaScript/Go/C: // path/to/file.js
	if strings.HasPrefix(firstLine, "// ") {
		candidate := strings.TrimPrefix(firstLine, "// ")
		if looksLikeFilePath(candidate) {
			return candidate, false
		}
	}

	// HTML/XML: <!-- path/to/file.html -->
	if strings.HasPrefix(firstLine, "<!-- ") && strings.HasSuffix(firstLine, " -->") {
		candidate := strings.TrimPrefix(firstLine, "<!-- ")
		candidate = strings.TrimSuffix(candidate, " -->")
		if looksLikeFilePath(candidate) {
			return candidate, false
		}
	}

	// Try to infer from content
	switch normalizeLanguage(lang) {
	case "go":
		// Look for package declaration
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "package ") {
				pkg := strings.TrimPrefix(line, "package ")
				if pkg != "main" {
					return pkg + "/" + pkg + ".go", true
				}
				return "main.go", true
			}
		}
	case "python":
		// Python code without explicit path comment - we can't reliably detect the path
		// Don't set isNew=true without a path as it's misleading
		return "", false
	}

	return "", false
}

// looksLikeFilePath checks if a string looks like a file path.
func looksLikeFilePath(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}

	// Must have an extension or be a directory path
	ext := filepath.Ext(s)
	if ext != "" {
		return true
	}

	// Check for path separators
	return strings.Contains(s, "/") || strings.Contains(s, "\\")
}

// ExtractFromText is a convenience function to extract code blocks from text.
func ExtractFromText(text string) []CodeBlock {
	return NewParser().Parse(text)
}

// extensionMap maps file extensions to language names
var extensionMap = map[string]string{
	".py":         "python",
	".go":         "go",
	".js":         "javascript",
	".jsx":        "javascript",
	".ts":         "typescript",
	".tsx":        "typescript",
	".rs":         "rust",
	".rb":         "ruby",
	".java":       "java",
	".c":          "c",
	".cpp":        "cpp",
	".cc":         "cpp",
	".h":          "c",
	".hpp":        "cpp",
	".cs":         "csharp",
	".swift":      "swift",
	".kt":         "kotlin",
	".scala":      "scala",
	".php":        "php",
	".sh":         "bash",
	".bash":       "bash",
	".zsh":        "bash",
	".fish":       "fish",
	".sql":        "sql",
	".json":       "json",
	".yaml":       "yaml",
	".yml":        "yaml",
	".toml":       "toml",
	".xml":        "xml",
	".html":       "html",
	".css":        "css",
	".scss":       "scss",
	".sass":       "sass",
	".less":       "less",
	".md":         "markdown",
	".r":          "r",
	".R":          "r",
	".lua":        "lua",
	".pl":         "perl",
	".pm":         "perl",
	".ex":         "elixir",
	".exs":        "elixir",
	".erl":        "erlang",
	".hs":         "haskell",
	".ml":         "ocaml",
	".vim":        "vim",
	".el":         "elisp",
	".clj":        "clojure",
	".tf":         "terraform",
	".vue":        "vue",
	".svelte":     "svelte",
	".dockerfile": "dockerfile",
	".make":       "makefile",
	".cmake":      "cmake",
	".proto":      "protobuf",
	".graphql":    "graphql",
	".gql":        "graphql",
}

// DetectLanguage determines the language identifier for a file path.
func DetectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if lang, ok := extensionMap[ext]; ok {
		return lang
	}

	// Check for filenames without extensions
	base := strings.ToLower(filepath.Base(path))
	if base == "dockerfile" {
		return "dockerfile"
	}
	if base == "makefile" || base == "gnumakefile" {
		return "makefile"
	}

	return "" // No language hint
}
