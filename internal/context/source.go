package context

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxSourceFiles     = 512
	maxSourceFileBytes = 1024 * 1024
	maxSourcePackBytes = 8 * 1024 * 1024
)

// prepareSourceContext builds file context without an external executable. The
// caller must supply a confined filesystem (os.Root.FS for production callers).
// Budgets use the context pack's estimated four-bytes-per-token convention;
// they are not a claim of exact model-tokenizer accounting.
func prepareSourceContext(ctx context.Context, fsys fs.FS, patterns []string, tokenBudget int, format string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if tokenBudget <= 0 {
		return "", fmt.Errorf("source context requires a positive token budget")
	}
	if format != "" && format != "compact" {
		return "", fmt.Errorf("unsupported source context format %q", format)
	}
	files, err := expandSourceFiles(ctx, fsys, patterns)
	if err != nil {
		return "", err
	}
	// Clamp before multiplying, including for MaxInt-sized budgets.
	byteBudget := maxSourcePackBytes
	if tokenBudget < maxSourcePackBytes/4 {
		byteBudget = tokenBudget * 4
	}

	var out strings.Builder
	for i, name := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		// Give each remaining file a share, carrying unused space forward.
		// A large first file must not consume all later files' context.
		share := (byteBudget - out.Len()) / (len(files) - i)
		data, truncated, err := readSourceFile(ctx, fsys, name, min(share, maxSourceFileBytes))
		if err != nil {
			return "", fmt.Errorf("source file %q: %w", name, err)
		}
		var section string
		if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
			section = fmt.Sprintf("File: %q [omitted: binary or non-UTF-8]\n", name)
			if len(section) > share {
				section = ""
			}
		} else {
			section = renderSourceFile(name, string(data), share, truncated, format == "compact")
		}
		if section == "" {
			return "", fmt.Errorf("source token budget too small to represent %d files; increase budget or select fewer files", len(files))
		}
		out.WriteString(section)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// expandSourceFiles preserves explicit selection order, sorts matches within a
// glob (fs.Glob's contract), and removes duplicates before allocating budgets.
func expandSourceFiles(ctx context.Context, fsys fs.FS, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("no files specified")
	}
	if len(patterns) > maxSourceFiles {
		return nil, fmt.Errorf("source selection exceeds %d patterns", maxSourceFiles)
	}
	seen := make(map[string]bool)
	files := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pattern = filepath.ToSlash(pattern)
		pattern = strings.TrimPrefix(pattern, "./")
		if !fs.ValidPath(pattern) || pattern == "." || strings.ContainsAny(pattern, "\x00\r\n") {
			return nil, fmt.Errorf("source path %q must be relative to the project without parent traversal", pattern)
		}
		// Prefer a literal file, including names which contain glob characters.
		matches := []string{pattern}
		if _, err := fs.Stat(fsys, pattern); err != nil {
			if !strings.ContainsAny(pattern, "*?[") {
				return nil, fmt.Errorf("source file %q: %w", pattern, err)
			}
			matches, err = fs.Glob(fsys, pattern)
			if err != nil {
				return nil, fmt.Errorf("source pattern %q: %w", pattern, err)
			}
			if len(matches) == 0 {
				return nil, fmt.Errorf("source pattern %q matched no files", pattern)
			}
		}
		for _, name := range matches {
			if !seen[name] {
				if len(files) == maxSourceFiles {
					return nil, fmt.Errorf("source selection exceeds %d files", maxSourceFiles)
				}
				seen[name] = true
				files = append(files, name)
			}
		}
	}
	return files, nil
}

func readSourceFile(ctx context.Context, fsys fs.FS, name string, limit int) ([]byte, bool, error) {
	// Check before opening so a selected FIFO/device is not opened normally.
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("not a regular file")
	}
	file, err := fsys.Open(name)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	// Also inspect the opened handle: the path may have changed since Stat.
	info, err = file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(&sourceContextReader{ctx: ctx, reader: file}, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
		// Drop only a possibly incomplete final rune, not invalid interior data.
		for n := 0; n < utf8.UTFMax-1 && len(data) > 0 && !utf8.Valid(data); n++ {
			data = data[:len(data)-1]
		}
	}
	return data, truncated, nil
}

type sourceContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *sourceContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func renderSourceFile(name, text string, budget int, truncated, compact bool) string {
	// Use a fence longer than any backtick run in the source. Embedded Markdown
	// must not be able to terminate the containing file's code block.
	longest, run := 2, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	header := fmt.Sprintf("### File: %q\n\n%s\n", name, fence)
	if compact {
		header = fmt.Sprintf("File: %q\n%s\n", name, fence)
	}
	footer := "\n" + fence + "\n"
	marker := "\n[truncated: source context budget]\n"
	available := budget - len(header) - len(footer)
	if available < 0 {
		return ""
	}
	if truncated || len(text) > available {
		available -= len(marker)
		if available < 0 {
			return ""
		}
		if len(text) > available {
			text = text[:available]
			for !utf8.ValidString(text) && len(text) > 0 {
				text = text[:len(text)-1]
			}
		}
		text += marker
	}
	return header + text + footer
}
