package config

import (
	"fmt"
	"regexp"
	"strings"
)

// labelRegex validates label strings: must start with alphanumeric,
// then alphanumeric, dash, or underscore.
var labelRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// SessionLabelSeparator is the delimiter between a session's base project name
// and its optional label. This constant is the ONLY place the session-label
// delimiter is defined (WS0-G6 single-definition contract); every maker and
// extractor must go through the helpers in this file.
const SessionLabelSeparator = "--"

// PaneTitleSeparator is the delimiter between a session name and the per-pane
// agent label inside a tmux pane title (e.g. "myproject__cc_1"). Like
// SessionLabelSeparator, its single definition lives here.
const PaneTitleSeparator = "__"

// ParseSessionLabel splits a session name into base project name and optional label.
//
// DELIMITER-COLLISION RULE (the one documented resolution, WS0-G6): the
// separator is the LAST occurrence of "--" (double-dash). Labels are validated
// to never contain "--" (ValidateLabel), while base project names come from
// directory names that may legitimately contain "--"; splitting on the LAST
// occurrence therefore guarantees FormatSessionName(base, label) round-trips
// through ParseSessionLabel for every valid label, even when base contains "--".
//
// Returns (base, label) where label is empty if no "--" present.
//
// Examples:
//
//	ParseSessionLabel("myproject")             -> ("myproject", "")
//	ParseSessionLabel("my-project--frontend")  -> ("my-project", "frontend")
//	ParseSessionLabel("foo--bar--baz")         -> ("foo--bar", "baz")
//	ParseSessionLabel("my--project--fix")      -> ("my--project", "fix")
func ParseSessionLabel(sessionName string) (base, label string) {
	idx := strings.LastIndex(sessionName, SessionLabelSeparator)
	if idx < 0 {
		return sessionName, ""
	}
	return sessionName[:idx], sessionName[idx+len(SessionLabelSeparator):]
}

// FormatSessionName constructs a labeled session name from base and label.
// If label is empty, returns base unchanged.
func FormatSessionName(base, label string) string {
	if label == "" {
		return base
	}
	return base + SessionLabelSeparator + label
}

// HasLabel returns true if the session name contains a label separator.
func HasLabel(sessionName string) bool {
	return strings.Contains(sessionName, SessionLabelSeparator)
}

// SessionBase returns just the base project name, stripping any label.
// Convenience wrapper around ParseSessionLabel.
//
// Examples:
//
//	SessionBase("myproject")            -> "myproject"
//	SessionBase("myproject--frontend")  -> "myproject"
//	SessionBase("my-project--frontend") -> "my-project"
func SessionBase(sessionName string) string {
	base, _ := ParseSessionLabel(sessionName)
	return base
}

// ValidateLabel checks if a label string is valid.
// Rules:
//   - Must start with alphanumeric character
//   - Can contain alphanumeric, dash, underscore
//   - Must not be empty
//   - Must not exceed 50 characters
//   - Must not itself contain "--" (the separator)
func ValidateLabel(label string) error {
	if label == "" {
		return fmt.Errorf("label must not be empty")
	}
	if len(label) > 50 {
		return fmt.Errorf("label must not exceed 50 characters (got %d)", len(label))
	}
	if strings.Contains(label, SessionLabelSeparator) {
		return fmt.Errorf("label must not contain '--' (reserved as separator)")
	}
	if !labelRegex.MatchString(label) {
		return fmt.Errorf("label must start with alphanumeric and contain only alphanumeric, dash, underscore")
	}
	return nil
}

// ValidateProjectName checks that a project name does not contain the reserved
// label separator "--". Project names with "--" would be ambiguous when combined
// with labels (e.g., "my--project--frontend" is unparseable).
func ValidateProjectName(project string) error {
	if strings.Contains(project, SessionLabelSeparator) {
		return fmt.Errorf("project name %q contains '--' which is reserved as the session-label separator", project)
	}
	return nil
}

// SessionLabel extracts the label from a session name, or empty string if unlabeled.
// Convenience wrapper around ParseSessionLabel.
//
// Examples:
//
//	SessionLabel("myproject")            -> ""
//	SessionLabel("myproject--frontend")  -> "frontend"
func SessionLabel(sessionName string) string {
	_, label := ParseSessionLabel(sessionName)
	return label
}

// UserPaneTitle returns the canonical tmux pane title for a session's reserved
// user pane (pane index 0 on spawn). It follows the same
// {session}__{type}_{index} convention as agent panes (tmux.FormatPaneName),
// using the "user" agent type so pane-title parsers classify it as AgentUser.
//
// This is the single maker for the user-pane title (WS0-G6): callers must not
// re-derive the delimiter or the "user" suffix.
func UserPaneTitle(session string) string {
	return session + PaneTitleSeparator + "user_0"
}

// PaneDisplayLabel derives the human-friendly per-pane label from a tmux pane
// title by stripping the "<session>__" prefix that the pane-title maker
// (tmux.FormatPaneName) adds. If the title carries no usable label, it falls
// back to "pane <index>".
//
// This is the single display-label extractor (WS0-G6): the pane-title
// delimiter must not be re-derived at call sites.
func PaneDisplayLabel(session, title string, paneIndex int) string {
	label := strings.TrimSpace(title)
	label = strings.TrimPrefix(label, session+PaneTitleSeparator)
	if label == "" {
		return fmt.Sprintf("pane %d", paneIndex)
	}
	return label
}
