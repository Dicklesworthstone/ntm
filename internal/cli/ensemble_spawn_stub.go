package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func resolveEnsembleProjectDir(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		dir, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
		return dir, nil
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	return abs, nil
}

var sessionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func defaultEnsembleSessionName(projectDir string) string {
	base := filepath.Base(projectDir)
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "ensemble"
	}
	sanitized := sessionNameSanitizer.ReplaceAllString(base, "-")
	sanitized = strings.Trim(sanitized, "-_")
	if sanitized == "" {
		sanitized = "ensemble"
	}
	return sanitized
}

func uniqueEnsembleSessionName(base string) string {
	name := base
	for i := 1; tmux.SessionExists(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}
