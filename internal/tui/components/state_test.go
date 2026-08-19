package components

import (
	"strings"
	"testing"
)

func TestRenderEmptyState(t *testing.T) {
	t.Run("basic rendering with all fields", func(t *testing.T) {
		out := RenderEmptyState(EmptyStateOptions{
			Icon:        IconWaiting,
			Title:       "No metrics yet",
			Description: "Data appears when agents start",
			Action:      "Press r to refresh",
			Width:       40,
			Centered:    true,
		})
		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "No metrics yet") {
			t.Errorf("expected title in output, got %q", out)
		}
		if !strings.Contains(out, "Data appears") {
			t.Errorf("expected description in output, got %q", out)
		}
		if !strings.Contains(out, "Press r") {
			t.Errorf("expected action in output, got %q", out)
		}
	})

	t.Run("minimal rendering (title only)", func(t *testing.T) {
		out := RenderEmptyState(EmptyStateOptions{
			Icon:     IconEmpty,
			Title:    "Nothing found",
			Width:    30,
			Centered: true,
		})
		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "Nothing found") {
			t.Errorf("expected title in output, got %q", out)
		}
	})

	t.Run("default title when empty", func(t *testing.T) {
		out := RenderEmptyState(EmptyStateOptions{
			Icon:     IconEmpty,
			Width:    30,
			Centered: true,
		})
		if !strings.Contains(out, "Nothing to show") {
			t.Errorf("expected default title, got %q", out)
		}
	})

	t.Run("success icon styling", func(t *testing.T) {
		out := RenderEmptyState(EmptyStateOptions{
			Icon:        IconSuccess,
			Title:       "All clear",
			Description: "No alerts",
			Width:       30,
			Centered:    true,
		})
		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "All clear") {
			t.Errorf("expected title in output, got %q", out)
		}
	})

	t.Run("external icon for external action needed", func(t *testing.T) {
		out := RenderEmptyState(EmptyStateOptions{
			Icon:        IconExternal,
			Title:       "Not initialized",
			Description: "Run 'br init' in your project",
			Width:       40,
			Centered:    true,
		})
		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "Not initialized") {
			t.Errorf("expected title in output, got %q", out)
		}
	})

	t.Run("unknown icon fallback", func(t *testing.T) {
		out := RenderEmptyState(EmptyStateOptions{
			Icon:     IconUnknown,
			Title:    "Unknown state",
			Width:    30,
			Centered: true,
		})
		if out == "" {
			t.Fatal("expected non-empty output")
		}
	})

	t.Run("truncates long description", func(t *testing.T) {
		longDesc := "This is a very very very long description that should be truncated"
		out := RenderEmptyState(EmptyStateOptions{
			Icon:        IconWaiting,
			Title:       "Test",
			Description: longDesc,
			Width:       20,
			Centered:    true,
		})
		if !strings.Contains(out, "…") {
			t.Errorf("expected truncation ellipsis, got %q", out)
		}
	})

	t.Run("left-aligned mode", func(t *testing.T) {
		out := RenderEmptyState(EmptyStateOptions{
			Icon:     IconEmpty,
			Title:    "No items",
			Width:    30,
			Centered: false,
		})
		if out == "" {
			t.Fatal("expected non-empty output")
		}
		// Left-aligned should have padding
		if !strings.Contains(out, "No items") {
			t.Errorf("expected title in output, got %q", out)
		}
	})

	t.Run("zero width renders without crash", func(t *testing.T) {
		out := RenderEmptyState(EmptyStateOptions{
			Icon:  IconEmpty,
			Title: "Test",
			Width: 0,
		})
		if out == "" {
			t.Fatal("expected non-empty output even with zero width")
		}
	})
}

func TestEmptyStateIcons(t *testing.T) {
	icons := []EmptyStateIcon{
		IconWaiting,
		IconEmpty,
		IconExternal,
		IconSuccess,
		IconUnknown,
	}

	for _, icon := range icons {
		t.Run(string(icon), func(t *testing.T) {
			resolved := resolveEmptyIcon(icon)
			if resolved == "" {
				t.Errorf("icon %q resolved to empty string", icon)
			}
		})
	}

	t.Run("invalid icon uses fallback", func(t *testing.T) {
		resolved := resolveEmptyIcon(EmptyStateIcon("invalid"))
		if resolved == "" {
			t.Error("invalid icon should fallback to info icon, got empty string")
		}
	})
}

func TestRetryState(t *testing.T) {
	// Local helper mirroring the removed RetryState wrapper; exercises the
	// live StateRetrying branch of RenderState.
	RetryState := func(message string, attempt, maxAttempts, width int) string {
		return RenderState(StateOptions{
			Kind:        StateRetrying,
			Message:     message,
			Attempt:     attempt,
			MaxAttempts: maxAttempts,
			Width:       width,
		})
	}

	t.Run("basic retry state", func(t *testing.T) {
		out := RetryState("Retrying connection", 1, 3, 40)
		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "Retrying") {
			t.Errorf("expected 'Retrying' in output, got %q", out)
		}
		if !strings.Contains(out, "Attempt 1 of 3") {
			t.Errorf("expected 'Attempt 1 of 3' in output, got %q", out)
		}
	})

	t.Run("retry with unlimited attempts", func(t *testing.T) {
		out := RetryState("Fetching data", 2, 0, 40)
		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "Attempt 2") {
			t.Errorf("expected 'Attempt 2' in output, got %q", out)
		}
		// Should NOT contain "of" when maxAttempts is 0
		if strings.Contains(out, "of 0") {
			t.Errorf("should not show 'of 0' for unlimited attempts, got %q", out)
		}
	})

	t.Run("default message when empty", func(t *testing.T) {
		out := RetryState("", 1, 3, 40)
		if !strings.Contains(out, "Retrying") {
			t.Errorf("expected default 'Retrying' message, got %q", out)
		}
	})

	t.Run("zero width renders without crash", func(t *testing.T) {
		out := RetryState("Test", 1, 3, 0)
		if out == "" {
			t.Fatal("expected non-empty output even with zero width")
		}
	})

	t.Run("RenderState with StateRetrying", func(t *testing.T) {
		out := RenderState(StateOptions{
			Kind:        StateRetrying,
			Message:     "Custom retry message",
			Attempt:     2,
			MaxAttempts: 5,
			Width:       50,
		})
		if !strings.Contains(out, "Custom retry message") {
			t.Errorf("expected custom message, got %q", out)
		}
		if !strings.Contains(out, "Attempt 2 of 5") {
			t.Errorf("expected attempt info, got %q", out)
		}
	})
}
