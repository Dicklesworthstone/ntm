package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ParseMarchingOrders reads a marching-orders file and returns a map of
// agent pane order (0-based, excluding any user pane) to prompt string. The file format is:
//
//	pane:N <prompt text>
//
// Lines starting with # are comments. Blank lines are ignored.
// N is a 0-based agent pane order, matching the spawn order of agent panes.
// All text after the first space following
// the pane:N prefix becomes the prompt for that pane.
func ParseMarchingOrders(path string) (map[int]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open file: %w", err)
	}
	defer f.Close()

	orders := make(map[int]string)
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse pane:N prefix
		if !strings.HasPrefix(line, "pane:") {
			return nil, fmt.Errorf("line %d: expected 'pane:N <prompt>', got %q", lineNum, line)
		}

		rest := line[len("pane:"):]
		spaceIdx := strings.IndexByte(rest, ' ')
		if spaceIdx < 0 {
			return nil, fmt.Errorf("line %d: missing prompt text after pane number", lineNum)
		}

		numStr := rest[:spaceIdx]
		prompt := strings.TrimSpace(rest[spaceIdx+1:])

		paneIdx, err := strconv.Atoi(numStr)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid pane number %q: %w", lineNum, numStr, err)
		}
		if paneIdx < 0 {
			return nil, fmt.Errorf("line %d: pane number must be >= 0, got %d", lineNum, paneIdx)
		}
		if prompt == "" {
			return nil, fmt.Errorf("line %d: empty prompt for pane %d", lineNum, paneIdx)
		}

		if _, exists := orders[paneIdx]; exists {
			return nil, fmt.Errorf("line %d: duplicate entry for pane %d", lineNum, paneIdx)
		}

		orders[paneIdx] = prompt
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}

	if len(orders) == 0 {
		return nil, fmt.Errorf("file contains no valid marching orders")
	}

	return orders, nil
}
