package tools

import (
	"testing"
	"time"
)

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
}

func TestParseVersionHelpers(t *testing.T) {
	t.Parallel()

	t.Run("parseVersion delegates to standard parser", func(t *testing.T) {
		t.Parallel()
		v, err := parseVersion("tool v2.4.6")
		if err != nil {
			t.Fatalf("parseVersion error: %v", err)
		}
		if v.Major != 2 || v.Minor != 4 || v.Patch != 6 {
			t.Errorf("parseVersion = %d.%d.%d, want 2.4.6", v.Major, v.Minor, v.Patch)
		}
	})

	t.Run("parseACFSVersion delegates to standard parser", func(t *testing.T) {
		t.Parallel()
		v, err := parseACFSVersion("1.0.3")
		if err != nil {
			t.Fatalf("parseACFSVersion error: %v", err)
		}
		if v.Major != 1 || v.Minor != 0 || v.Patch != 3 {
			t.Errorf("parseACFSVersion = %d.%d.%d, want 1.0.3", v.Major, v.Minor, v.Patch)
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
