package context

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxSourceFiles     = 512
	maxSourceFileBytes = 1024 * 1024
	maxSourcePackBytes = 8 * 1024 * 1024
	maxSourceEntries   = 32768
	maxSourceDepth     = 128
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

// expandSourceFiles accepts literal files, directories, and globs. A complete
// ** segment matches zero or more directories; other segments use path.Match.
// Selections retain argument order, with sorted, deduplicated matches. Discovery
// never follows encountered symlinks or enters VCS metadata directories. It does
// not interpret .gitignore; callers should select the source subtrees they need.
func expandSourceFiles(ctx context.Context, fsys fs.FS, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("no files specified")
	}
	if len(patterns) > maxSourceFiles {
		return nil, fmt.Errorf("source selection exceeds %d patterns", maxSourceFiles)
	}
	seen := make(map[string]bool)
	files := make([]string, 0, len(patterns))
	remaining := maxSourceEntries
	for _, pattern := range patterns {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pattern = filepath.ToSlash(pattern)
		pattern = strings.TrimSuffix(pattern, "/")
		pattern = strings.TrimPrefix(pattern, "./")
		if !fs.ValidPath(pattern) || strings.ContainsAny(pattern, "\x00\r\n") {
			return nil, fmt.Errorf("source path %q must be relative to the project without parent traversal", pattern)
		}
		if len(pattern) > 4096 || strings.Count(pattern, "/") >= maxSourceDepth {
			return nil, fmt.Errorf("source path exceeds discovery depth or length limit")
		}
		// Prefer literal names, including files/directories containing glob syntax.
		info, err := fs.Stat(fsys, pattern)
		var matches []string
		root, glob := pattern, "**"
		switch {
		case err == nil && !info.IsDir():
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("source file %q: not a regular file", pattern)
			}
			matches = []string{pattern}
		case err != nil:
			if !errors.Is(err, fs.ErrNotExist) || !strings.ContainsAny(pattern, "*?[\\") {
				return nil, fmt.Errorf("source file %q: %w", pattern, err)
			}
			parts := strings.Split(pattern, "/")
			first := 0
			for first < len(parts) && !strings.ContainsAny(parts[first], "*?[\\") {
				first++
			}
			root, glob = path.Join(parts[:first]...), strings.Join(parts[first:], "/")
			if root == "" {
				root = "."
			}
		}
		if matches == nil {
			parts := strings.Split(glob, "/")
			for _, part := range parts {
				if _, err := path.Match(part, ""); err != nil {
					return nil, fmt.Errorf("source pattern %q: %w", pattern, err)
				}
			}
			unseen := 0
			err = walkSourceFiles(ctx, fsys, root, "", parts, 0, &remaining, func(name string) error {
				if !seen[name] {
					unseen++
					if len(files)+unseen > maxSourceFiles {
						return fmt.Errorf("source selection exceeds %d files", maxSourceFiles)
					}
				}
				matches = append(matches, name)
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("source selection %q: %w", pattern, err)
			}
			if len(matches) == 0 {
				return nil, fmt.Errorf("source pattern %q matched no files", pattern)
			}
			sort.Strings(matches)
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

// sourcePathMatch uses a small state machine rather than recursive wildcard
// backtracking. descend says whether any longer path can still match, allowing
// a narrow glob to avoid unrelated (possibly unreadable) subtrees.
func sourcePathMatch(parts []string, name string) (match, descend bool) {
	states := make([]bool, len(parts)+1)
	states[0] = true
	closure := func() {
		for i, part := range parts {
			if states[i] && part == "**" {
				states[i+1] = true
			}
		}
	}
	closure()
	for _, segment := range strings.Split(name, "/") {
		next := make([]bool, len(states))
		for i, part := range parts {
			if !states[i] {
				continue
			}
			if part == "**" {
				next[i] = true
			} else if ok, _ := path.Match(part, segment); ok {
				next[i+1] = true
			}
		}
		states = next
		closure()
	}
	for _, state := range states[:len(parts)] {
		descend = descend || state
	}
	return states[len(parts)], descend
}

func walkSourceFiles(ctx context.Context, fsys fs.FS, root, relative string, parts []string, depth int, remaining *int, visit func(string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth >= maxSourceDepth {
		return fmt.Errorf("source discovery exceeds depth limit %d", maxSourceDepth)
	}
	dir := path.Join(root, relative)
	entries, err := readSourceDirectory(ctx, fsys, dir, remaining)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "/\x00\r\n") {
			return fmt.Errorf("invalid source directory entry %q", name)
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		rel := path.Join(relative, name)
		match, descend := sourcePathMatch(parts, rel)
		if entry.IsDir() {
			if !descend || name == ".git" || name == ".hg" || name == ".svn" {
				continue
			}
			if err := walkSourceFiles(ctx, fsys, root, rel, parts, depth+1, remaining, visit); err != nil {
				return err
			}
		} else if match {
			if !entry.Type().IsRegular() {
				return fmt.Errorf("source file %q: not a regular file", path.Join(root, rel))
			}
			if err := visit(path.Join(root, rel)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Read incrementally: fs.ReadDir/WalkDir otherwise allocate an entire directory
// before a caller can enforce limits. The entry budget spans all selections.
func readSourceDirectory(ctx context.Context, fsys fs.FS, name string, remaining *int) ([]fs.DirEntry, error) {
	file, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	dir, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, fmt.Errorf("source directory %q does not support bounded enumeration", name)
	}
	var entries []fs.DirEntry
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := dir.ReadDir(min(128, *remaining+1))
		*remaining -= len(batch)
		if *remaining < 0 {
			return nil, fmt.Errorf("source discovery exceeds %d directory entries; select a narrower subtree", maxSourceEntries)
		}
		entries = append(entries, batch...)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if len(batch) == 0 {
			return nil, fmt.Errorf("source directory %q: %w", name, io.ErrNoProgress)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
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
