package cli

import (
	"strings"
	"testing"
)

func TestHexToRGB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		hex        string
		wantR      int
		wantG      int
		wantB      int
	}{
		{"black", "#000000", 0, 0, 0},
		{"white", "#ffffff", 255, 255, 255},
		{"red", "#ff0000", 255, 0, 0},
		{"green", "#00ff00", 0, 255, 0},
		{"blue", "#0000ff", 0, 0, 255},
		{"mid gray", "#808080", 128, 128, 128},
		{"specific color", "#1a2b3c", 26, 43, 60},
		{"uppercase", "#FF00FF", 255, 0, 255},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, g, b := hexToRGB(tc.hex)
			if r != tc.wantR || g != tc.wantG || b != tc.wantB {
				t.Errorf("hexToRGB(%q) = (%d, %d, %d), want (%d, %d, %d)",
					tc.hex, r, g, b, tc.wantR, tc.wantG, tc.wantB)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		b    int64
		want string
	}{
		{"zero", 0, "0 B"},
		{"one byte", 1, "1 B"},
		{"1023 bytes", 1023, "1023 B"},
		{"1 KB", 1024, "1.0 KB"},
		{"1.5 KB", 1536, "1.5 KB"},
		{"1 MB", 1024 * 1024, "1.0 MB"},
		{"1 GB", 1024 * 1024 * 1024, "1.0 GB"},
		{"10 MB", 10 * 1024 * 1024, "10.0 MB"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatBytes(tc.b)
			if got != tc.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tc.b, got, tc.want)
			}
		})
	}
}

func TestFormatCleanupBytes(t *testing.T) {
	t.Parallel()

	// formatCleanupBytes is an alias for formatBytes
	got := formatCleanupBytes(1024)
	want := formatBytes(1024)
	if got != want {
		t.Errorf("formatCleanupBytes(1024) = %q, want %q (same as formatBytes)", got, want)
	}
}

func TestColorToRGB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		c    interface{}
		want string
	}{
		{"hex color", "#ff0000", "255;0;0m"},
		{"non-hex string", "red", "255;255;255m"},       // falls back to white
		{"short string", "#fff", "255;255;255m"},         // not 7 chars, falls back
		{"empty string", "", "255;255;255m"},             // falls back
		{"integer input", 42, "255;255;255m"},            // non-string, falls back
		{"hex black", "#000000", "0;0;0m"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := colorToRGB(tc.c)
			if got != tc.want {
				t.Errorf("colorToRGB(%v) = %q, want %q", tc.c, got, tc.want)
			}
		})
	}
}

func TestColorize(t *testing.T) {
	t.Parallel()

	// colorize wraps colorToRGB with ANSI prefix
	got := colorize("#ff0000")
	if !strings.HasPrefix(got, "\033[38;2;") {
		t.Errorf("colorize should start with ANSI escape, got %q", got)
	}
	if !strings.Contains(got, "255;0;0m") {
		t.Errorf("colorize(#ff0000) should contain 255;0;0m, got %q", got)
	}
}
