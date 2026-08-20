package config

// migrate.go — `ntm config migrate` engine (bd-config-migrate-warning-wall-151x2).
//
// Text-surgical removal of every removed/deprecated (dead) config key from a
// config file: the same line-preserving discipline as upsertTOMLKeys, but for
// deletion. All live keys, comments, ordering, and formatting are preserved
// byte-for-byte; only the dead key lines (including multi-line values), dead
// table blocks ([rotation.dashboard], [integrations.caut], [[accounts.*]]
// array-of-tables blocks, ...), and table headers left empty by the removals
// are dropped. Every dead key is a provable runtime no-op (that is why it was
// removed), so the migration cannot change ntm behavior.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

// MigrationChange is one dead key (or dead table block) removed from the
// config file, with the same disposition sentence the strict-loader error and
// `ntm doctor` print for it.
type MigrationChange struct {
	Key         string `json:"key"`
	Disposition string `json:"disposition"`
	Tier        string `json:"tier"` // DeadKeyTierRemoved | DeadKeyTierDeprecated
}

// MigrationResult reports what `ntm config migrate` did (or, under dry-run,
// would do) to one config file.
type MigrationResult struct {
	Path       string            `json:"path"`
	Exists     bool              `json:"exists"`
	Clean      bool              `json:"clean"`
	DryRun     bool              `json:"dry_run"`
	BackupPath string            `json:"backup_path,omitempty"`
	Changes    []MigrationChange `json:"changes"`
	// Unresolved lists dead keys the surgical editor could not remove (for
	// example a whole-section inline-table root assignment). They require a
	// manual edit; `ntm doctor` names each one.
	Unresolved []MigrationChange `json:"unresolved,omitempty"`
	// Original and Updated carry the file contents for dry-run inspection.
	Original string `json:"-"`
	Updated  string `json:"-"`
}

// MigrateDeadKeys removes every removed/deprecated config key from the config
// file at path (empty = DefaultPath). With dryRun, nothing is written and the
// result reports what would change. Otherwise a timestamped backup
// (<path>.bak.<unix>) is written next to the file before the cleaned contents
// replace it atomically. A missing file or a file with no dead keys is a
// clean no-op success.
func MigrateDeadKeys(path string, dryRun bool) (*MigrationResult, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	result := &MigrationResult{Path: path, DryRun: dryRun, Changes: []MigrationChange{}}

	resolvedPath, err := resolveConfigPersistencePath(path)
	if err != nil {
		return nil, err
	}

	// Serialize with every other config writer (same transaction discipline
	// as PersistTOMLKeys) so a concurrent `ntm config set` cannot interleave.
	configPersistenceMu.Lock()
	defer configPersistenceMu.Unlock()
	if !dryRun {
		if err := os.MkdirAll(filepath.Dir(resolvedPath), 0755); err != nil {
			return nil, fmt.Errorf("creating config directory: %w", err)
		}
		lockCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		unlock, lockErr := acquireConfigPersistenceLock(lockCtx, resolvedPath+".lock")
		if lockErr != nil {
			return nil, fmt.Errorf("locking config %s: %w", path, lockErr)
		}
		defer unlock()
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			result.Clean = true
			return result, nil
		}
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	mode := os.FileMode(0644)
	if info, statErr := os.Stat(resolvedPath); statErr == nil {
		mode = info.Mode().Perm()
	}
	result.Original = string(data)

	// The file must at least be parseable TOML: the surgical editor relies on
	// the same invariant as upsertTOMLKeys (scanTOMLLines only classifies
	// already-valid TOML), and a syntax error is not a dead-key problem.
	var probe map[string]interface{}
	if _, err := toml.Decode(result.Original, &probe); err != nil {
		return nil, fmt.Errorf("config %s is not valid TOML (fix the syntax first, migrate only removes dead keys): %w", path, err)
	}

	updated, changes := removeDeadTOMLKeys(result.Original)
	result.Updated = updated
	result.Changes = changes

	if len(changes) == 0 {
		result.Clean = true
		return result, nil
	}

	// Safety net: the cleaned contents must still be valid TOML, and every
	// dead key we claim to have removed must actually be gone under the same
	// lenient scan doctor uses. Anything still present is reported as
	// unresolved rather than silently claimed fixed.
	cfg := &Config{}
	md, err := toml.Decode(updated, cfg)
	if err != nil {
		return nil, fmt.Errorf("internal error: migrated config would not parse (nothing was written): %w", err)
	}
	removed, deprecated, _ := classifyUndecodedKeys(undecodedConfigFields(md))
	for _, knob := range removed {
		result.Unresolved = append(result.Unresolved, MigrationChange{Key: knob.Key, Disposition: knob.Disposition, Tier: DeadKeyTierRemoved})
	}
	for _, knob := range deprecated {
		result.Unresolved = append(result.Unresolved, MigrationChange{Key: knob.Key, Disposition: knob.Disposition, Tier: DeadKeyTierDeprecated})
	}

	if dryRun {
		return result, nil
	}

	// Timestamped backup FIRST, next to the file, then the atomic rewrite.
	backupPath := resolvedPath + ".bak." + strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(backupPath, data, mode); err != nil {
		return nil, fmt.Errorf("writing backup %s (config left untouched): %w", backupPath, err)
	}
	result.BackupPath = backupPath
	if err := util.AtomicWriteFile(resolvedPath, []byte(updated), mode); err != nil {
		return nil, fmt.Errorf("writing migrated config %s (backup kept at %s): %w", path, backupPath, err)
	}
	return result, nil
}

// removeDeadTOMLKeys performs the line-level surgery: it returns contents
// with every dead key assignment, dead table block, and emptied table header
// removed, plus the list of dead keys found (deduplicated, in file order).
// Everything else — comments, blank lines, ordering, formatting, line
// endings — is preserved byte-for-byte.
func removeDeadTOMLKeys(contents string) (string, []MigrationChange) {
	lines := strings.Split(contents, "\n")
	infos := scanTOMLLines(lines)
	keep := make([]bool, len(lines))
	for i := range keep {
		keep[i] = true
	}

	type tableState struct {
		headerLines []int // header line index; [[array-of-tables]] repeats share state per header
		hasLive     bool
		removedAny  bool
	}
	tablesByName := map[string]*tableState{}
	var tableOrder []string

	seen := map[string]bool{}
	var changes []MigrationChange
	record := func(key, disp, tier string) {
		if seen[key] {
			return
		}
		seen[key] = true
		changes = append(changes, MigrationChange{Key: key, Disposition: disp, Tier: tier})
	}

	var currentPath []string // dotted path of the enclosing table ("" parts for root)
	currentKnown := true     // header parsed successfully → dead-key checks are safe
	var current *tableState  // nil at root or inside an unparsed header
	droppingTable := false   // inside a dead table block

	i := 0
	for i < len(lines) {
		line := lines[i]
		info := infos[i]

		isHeader := !info.startsInMultiline && isTOMLTableHeader(line, info.commentAt)
		if droppingTable && !isHeader {
			keep[i] = false
			i++
			continue
		}
		if isHeader {
			droppingTable = false
			path, ok := parseTOMLHeaderPath(line, info.commentAt)
			if ok {
				full := strings.Join(path, ".")
				if disp, tier, dead := classifyDeadKey(full); dead {
					// Dead table: drop the header and the whole block
					// (comments and values included) up to the next header.
					// [[array-of-tables]] blocks repeat; each block drops.
					keep[i] = false
					droppingTable = true
					record(full, disp, tier)
					current = nil
					i++
					continue
				}
				currentPath = path
				currentKnown = true
				state, exists := tablesByName[full]
				if !exists {
					state = &tableState{}
					tablesByName[full] = state
					tableOrder = append(tableOrder, full)
				}
				state.headerLines = append(state.headerLines, i)
				current = state
			} else {
				// Unrecognized header shape (quoted keys, ...): keep it and
				// everything under it — never guess at deletions.
				currentPath = nil
				currentKnown = false
				current = nil
			}
			i++
			continue
		}

		if info.startsInMultiline {
			// Continuation of a kept multi-line value.
			i++
			continue
		}

		if path, _, ok := tomlAssignmentPath(line, info.mask); ok {
			end := logicalTOMLLineEnd(lines, infos, i)
			if currentKnown {
				full := strings.Join(append(append([]string{}, currentPath...), path...), ".")
				if disp, tier, dead := classifyDeadKey(full); dead {
					for j := i; j <= end; j++ {
						keep[j] = false
					}
					record(full, disp, tier)
					if current != nil {
						current.removedAny = true
					}
					i = end + 1
					continue
				}
			}
			if current != nil {
				current.hasLive = true
			}
			i = end + 1
			continue
		}

		i++
	}

	// A table whose every key was removed loses its header too. Comments and
	// blank lines inside it are preserved (they remain valid at any position).
	for _, name := range tableOrder {
		state := tablesByName[name]
		if state.removedAny && !state.hasLive {
			for _, headerLine := range state.headerLines {
				keep[headerLine] = false
			}
		}
	}

	kept := make([]string, 0, len(lines))
	for idx, line := range lines {
		if keep[idx] {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n"), changes
}

// logicalTOMLLineEnd returns the index of the last physical line of the
// logical TOML assignment starting at line i: multi-line strings extend while
// the following line still starts inside the string (scanTOMLLines tracks
// this), and multi-line arrays/inline tables extend while the structural
// (mask-visible) bracket depth stays positive.
func logicalTOMLLineEnd(lines []string, infos []tomlLineInfo, i int) int {
	depth := structuralBracketDelta(infos[i].mask)
	j := i
	for j+1 < len(lines) {
		next := j + 1
		if !infos[next].startsInMultiline && depth <= 0 {
			break
		}
		depth += structuralBracketDelta(infos[next].mask)
		j = next
	}
	return j
}

// structuralBracketDelta counts net bracket/brace depth on a masked line
// (string contents and comments are already blanked by scanTOMLLines, so
// every bracket seen is structural).
func structuralBracketDelta(mask string) int {
	depth := 0
	for k := 0; k < len(mask); k++ {
		switch mask[k] {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		}
	}
	return depth
}

// parseTOMLHeaderPath extracts the dotted key path from a [table] or
// [[array.of.tables]] header line, using the TOML parser itself for bare/
// quoted key semantics (same probe technique as tomlHeaderMatches).
func parseTOMLHeaderPath(line string, commentAt int) ([]string, bool) {
	if commentAt >= 0 {
		line = line[:commentAt]
	}
	header := strings.TrimSpace(line)
	var inner string
	switch {
	case strings.HasPrefix(header, "[[") && strings.HasSuffix(header, "]]") && len(header) > 4:
		inner = header[2 : len(header)-2]
	case strings.HasPrefix(header, "[") && strings.HasSuffix(header, "]") && len(header) > 2:
		inner = header[1 : len(header)-1]
	default:
		return nil, false
	}
	const probe = "__ntm_migrate_probe__"
	var parsed map[string]interface{}
	if _, err := toml.Decode("["+inner+"]\n"+probe+" = true\n", &parsed); err != nil {
		return nil, false
	}
	var path []string
	current := parsed
	for {
		if len(current) != 1 {
			return nil, false
		}
		for key, value := range current {
			if next, ok := value.(map[string]interface{}); ok {
				path = append(path, key)
				current = next
				continue
			}
			if key == probe {
				return path, len(path) > 0
			}
			return nil, false
		}
	}
}
