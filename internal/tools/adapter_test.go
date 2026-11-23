package tools

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestCapabilityProbesReadInstalledHelp: ubs prints --help on stderr, pt and
// rano on stdout, and rano has no `help` subcommand, so each probe must read
// both streams of `--help` to see the robot surface.
func TestCapabilityProbesReadInstalledHelp(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adapter Adapter
	}{
		{"ubs", NewUBSAdapter()},
		{"pt", NewPTAdapter()},
		{"rano", NewRanoAdapter()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, installed := tc.adapter.Detect(); !installed {
				t.Skipf("%s not installed", tc.name)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			caps, err := tc.adapter.Capabilities(ctx)
			if err != nil || !slices.Contains(caps, CapRobotMode) {
				t.Fatalf("Capabilities() = %v, %v; want robot mode from %s --help", caps, err, tc.name)
			}
		})
	}
}

func TestParseStandardVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantMajor int
		wantMinor int
		wantPatch int
		wantRaw   string
	}{
		{"simple version", "1.2.3", 1, 2, 3, "1.2.3"},
		{"with prefix", "v1.2.3", 1, 2, 3, "v1.2.3"},
		{"embedded in text", "bv version 0.31.0", 0, 31, 0, "bv version 0.31.0"},
		{"large numbers", "10.200.3000", 10, 200, 3000, "10.200.3000"},
		{"zero version", "0.0.0", 0, 0, 0, "0.0.0"},
		{"no version found", "no version here", 0, 0, 0, "no version here"},
		{"empty string", "", 0, 0, 0, ""},
		{"with whitespace", "  1.5.0  ", 1, 5, 0, "1.5.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, err := ParseStandardVersion(tc.input)
			if err != nil {
				t.Fatalf("ParseStandardVersion(%q) error: %v", tc.input, err)
			}
			if v.Major != tc.wantMajor || v.Minor != tc.wantMinor || v.Patch != tc.wantPatch {
				t.Errorf("ParseStandardVersion(%q) = %d.%d.%d, want %d.%d.%d",
					tc.input, v.Major, v.Minor, v.Patch, tc.wantMajor, tc.wantMinor, tc.wantPatch)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    Version
		want string
	}{
		{"with raw", Version{Major: 1, Minor: 2, Patch: 3, Raw: "v1.2.3"}, "v1.2.3"},
		{"raw takes priority", Version{Major: 1, Minor: 0, Patch: 0, Raw: "custom-1.0"}, "custom-1.0"},
		{"no raw", Version{Major: 1, Minor: 2, Patch: 3}, "1.2.3"},
		{"zero version no raw", Version{}, "0.0.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.v.String()
			if got != tc.want {
				t.Errorf("Version.String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAllTools(t *testing.T) {
	t.Parallel()

	tools := AllTools()

	if len(tools) == 0 {
		t.Fatal("AllTools() returned empty list")
	}

	// Check that known tools are present
	required := []ToolName{ToolBV, ToolBD, ToolAM, ToolCM, ToolCASS, ToolS2P, ToolDCG, ToolUBS}
	for _, r := range required {
		found := false
		for _, tool := range tools {
			if tool == r {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AllTools() missing required tool %q", r)
		}
	}

	// Check uniqueness
	seen := make(map[ToolName]bool)
	for _, tool := range tools {
		if seen[tool] {
			t.Errorf("AllTools() contains duplicate: %q", tool)
		}
		seen[tool] = true
	}
}

func TestNewLimitedBuffer(t *testing.T) {
	t.Parallel()

	t.Run("within limit", func(t *testing.T) {
		t.Parallel()
		buf := NewLimitedBuffer(100)
		n, err := buf.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("Write() error: %v", err)
		}
		if n != 5 {
			t.Errorf("Write() = %d, want 5", n)
		}
		if buf.String() != "hello" {
			t.Errorf("buffer content = %q, want %q", buf.String(), "hello")
		}
	})

	t.Run("exceeds limit", func(t *testing.T) {
		t.Parallel()
		buf := NewLimitedBuffer(5)
		_, err := buf.Write([]byte("hello world"))
		if err != ErrOutputLimitExceeded {
			t.Errorf("Write() error = %v, want ErrOutputLimitExceeded", err)
		}
	})

	t.Run("exact limit", func(t *testing.T) {
		t.Parallel()
		buf := NewLimitedBuffer(5)
		_, err := buf.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("Write() error: %v", err)
		}
		// Second write should fail
		_, err = buf.Write([]byte("!"))
		if err != ErrOutputLimitExceeded {
			t.Errorf("second Write() error = %v, want ErrOutputLimitExceeded", err)
		}
	})

	t.Run("multiple writes within limit", func(t *testing.T) {
		t.Parallel()
		buf := NewLimitedBuffer(20)
		buf.Write([]byte("hello "))
		buf.Write([]byte("world"))
		if buf.String() != "hello world" {
			t.Errorf("buffer content = %q, want %q", buf.String(), "hello world")
		}
	})

	t.Run("io.Copy enforces limit without ReadFrom bypass", func(t *testing.T) {
		t.Parallel()
		buf := NewLimitedBuffer(16)
		if _, ok := any(buf).(io.ReaderFrom); ok {
			t.Fatal("LimitedBuffer must not implement io.ReaderFrom")
		}
		_, err := io.Copy(buf, bytes.NewReader(bytes.Repeat([]byte("a"), 128)))
		if !errors.Is(err, ErrOutputLimitExceeded) {
			t.Fatalf("io.Copy() error = %v, want ErrOutputLimitExceeded", err)
		}
		if buf.Len() != 16 {
			t.Fatalf("buf.Len() = %d, want 16", buf.Len())
		}
	})
}

func TestVersionCompareAndAtLeast(t *testing.T) {
	t.Parallel()

	v1 := Version{Major: 1, Minor: 2, Patch: 3}
	v2 := Version{Major: 1, Minor: 2, Patch: 4}
	v3 := Version{Major: 2, Minor: 0, Patch: 0}

	if v1.Compare(v1) != 0 {
		t.Errorf("v1.Compare(v1) = %d, want 0", v1.Compare(v1))
	}
	if v1.Compare(v2) >= 0 {
		t.Errorf("v1.Compare(v2) = %d, want < 0", v1.Compare(v2))
	}
	if v3.Compare(v2) <= 0 {
		t.Errorf("v3.Compare(v2) = %d, want > 0", v3.Compare(v2))
	}

	if !v2.AtLeast(v1) {
		t.Error("v2.AtLeast(v1) should be true")
	}
	if v1.AtLeast(v3) {
		t.Error("v1.AtLeast(v3) should be false")
	}
}

func TestBaseAdapterBasics(t *testing.T) {
	t.Parallel()

	adapter := NewBaseAdapter(ToolBV, "bv")
	if adapter.Name() != ToolBV {
		t.Errorf("Name() = %q, want %q", adapter.Name(), ToolBV)
	}
	if adapter.BinaryName() != "bv" {
		t.Errorf("BinaryName() = %q, want %q", adapter.BinaryName(), "bv")
	}
	if adapter.Timeout() != 30*time.Second {
		t.Errorf("Timeout() = %v, want %v", adapter.Timeout(), 30*time.Second)
	}
}

func TestJFPAdapter_FlagEqualsAndOptionTerminator(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "jfp-args.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + "'" + argsFile + "'" + "\nprintf '[]\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "jfp"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	adapter := NewJFPAdapter()
	readArgs := func() []string {
		t.Helper()
		raw, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}

	if _, err := adapter.ListByCategory(context.Background(), "--evil-cat"); err != nil {
		t.Fatalf("ListByCategory: %v", err)
	}
	if got, want := readArgs(), []string{"list", "--category=--evil-cat", "--json"}; !slices.Equal(got, want) {
		t.Fatalf("ListByCategory argv = %q, want %q", got, want)
	}

	if _, err := adapter.Search(context.Background(), "--dash-query"); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got, want := readArgs(), []string{"search", " --dash-query", "--json"}; !slices.Equal(got, want) {
		t.Fatalf("Search argv = %q, want %q", got, want)
	}

	if _, err := adapter.Show(context.Background(), "--evil-id"); err == nil {
		t.Fatal("Show(--evil-id) succeeded, want flag-injection rejection")
	}
	if _, err := adapter.Export(context.Background(), []string{"--id-1"}, "md"); err == nil {
		t.Fatal("Export(--id-1) succeeded, want flag-injection rejection")
	}

	if _, err := adapter.Export(context.Background(), []string{"idea-wizard"}, "--fmt"); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if got, want := readArgs(), []string{"export", "--format=--fmt", "idea-wizard", "--json"}; !slices.Equal(got, want) {
		t.Fatalf("Export argv = %q, want %q", got, want)
	}
}
