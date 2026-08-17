// docs_truth_test.go — WS7-H2 enumerated docs-truth grep-gate
// (bd-ws7-docs-ux-truth-tqh3l.2).
//
// The 2026-08 reality audit found five SHIPPED features documented as
// "planned" (unflattering-stale drift — same disease as H1, opposite
// direction). G3 executes examples but cannot catch prose, so this gate
// enumerates the EXACT five claims and mechanically asserts, per claim:
//  1. the stale "planned"-class phrasing is GONE from the flagged file, and
//  2. the replacement sentence stating shipped status is PRESENT.
//
// The five audited claims (file:line refers to the pre-fix tree):
//  1. RU flywheel bridge      docs/robot-api-design.md:86  (planned list)
//  2. GIIL flywheel bridge    docs/robot-api-design.md:88  (planned list)
//  3. XF flywheel bridge      docs/robot-api-design.md:89  (planned list)
//  4. palette recents/favorites  command_palette.md:23
//  5. palette pinning            command_palette.md:23
//
// Shipped reality verified against the tree at fix time: --robot-ru-sync,
// --robot-giil-fetch, --robot-xf-search/--robot-xf-status registered in
// internal/cli/root.go; recents/favorites/pins implemented in
// internal/palette/model.go (fetchRecents, ToggleFavorite ctrl+f,
// TogglePin ctrl+p).
package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docsTruthClaim is one enumerated stale claim from the H2 audit.
type docsTruthClaim struct {
	name string
	file string // repo-relative
	// staleRE matches the audited "planned"/"coming soon"-class phrasing that
	// must be ABSENT after the fix.
	staleRE *regexp.Regexp
	// shipped is a literal sentence fragment that must be PRESENT, stating
	// the shipped status.
	shipped string
}

func docsTruthClaims() []docsTruthClaim {
	return []docsTruthClaim{
		{
			name:    "RU-flywheel-bridge",
			file:    "docs/robot-api-design.md",
			staleRE: regexp.MustCompile(`(?i)\*\*RU\*\*[^\n]*--robot-ru-\*`),
			shipped: "**RU** (repo updater): `--robot-ru-sync` — shipped",
		},
		{
			name:    "GIIL-flywheel-bridge",
			file:    "docs/robot-api-design.md",
			staleRE: regexp.MustCompile(`(?i)\*\*GIIL\*\*[^\n]*--robot-giil-\*`),
			shipped: "**GIIL** (image fetch): `--robot-giil-fetch` — shipped",
		},
		{
			name:    "XF-flywheel-bridge",
			file:    "docs/robot-api-design.md",
			staleRE: regexp.MustCompile(`(?i)\*\*XF\*\*[^\n]*--robot-xf-\*`),
			shipped: "**XF** (archive search): `--robot-xf-search`, `--robot-xf-status` — shipped",
		},
		{
			name:    "palette-recents-favorites",
			file:    "command_palette.md",
			staleRE: regexp.MustCompile(`(?i)(recents|favorites)[^\n]*\b(planned|coming soon|not (yet )?available)`),
			shipped: "favorites (`ctrl+f`)",
		},
		{
			name:    "palette-pinning",
			file:    "command_palette.md",
			staleRE: regexp.MustCompile(`(?i)pinning[^\n]*\b(planned|coming soon|not (yet )?available)`),
			shipped: "pinning (`ctrl+p`) are shipped palette features",
		},
	}
}

// TestDocsShippedFeaturesNotDocumentedAsPlanned is the H2 close condition:
// for each enumerated claim, the stale phrasing is gone and the shipped
// statement is present.
func TestDocsShippedFeaturesNotDocumentedAsPlanned(t *testing.T) {
	root := docsRepoRoot(t)
	cache := map[string]string{}
	for _, c := range docsTruthClaims() {
		t.Run(c.name, func(t *testing.T) {
			content, ok := cache[c.file]
			if !ok {
				data, err := os.ReadFile(filepath.Join(root, c.file))
				if err != nil {
					t.Fatalf("reading %s: %v", c.file, err)
				}
				content = string(data)
				cache[c.file] = content
			}
			if loc := c.staleRE.FindString(content); loc != "" {
				t.Errorf("STALE CLAIM REGRESSED — %s again documents a shipped feature as planned/unavailable.\nMatched: %q\nThe feature is shipped; state shipped reality instead (bd-ws7-docs-ux-truth-tqh3l.2).", c.file, loc)
			}
			if !strings.Contains(content, c.shipped) {
				t.Errorf("SHIPPED STATEMENT MISSING — %s no longer contains the corrected sentence %q.\nIf the wording legitimately changed, update this enumerated gate in the same PR (it pins the H2 audit's five claims).", c.file, c.shipped)
			}
		})
	}

	// Belt-and-braces for claims 1-3: RU/GIIL/XF must not reappear anywhere
	// inside the "Planned / rolling out" section of robot-api-design.md.
	data, err := os.ReadFile(filepath.Join(root, "docs/robot-api-design.md"))
	if err != nil {
		t.Fatalf("reading docs/robot-api-design.md: %v", err)
	}
	content := string(data)
	start := strings.Index(content, "**Planned / rolling out**")
	if start < 0 {
		return // section removed entirely — nothing can be mis-listed in it
	}
	section := content[start:]
	if end := strings.Index(section, "\n#"); end >= 0 {
		section = section[:end]
	}
	for _, tool := range []string{"**RU**", "**GIIL**", "**XF**"} {
		if strings.Contains(section, tool) {
			t.Errorf("STALE CLAIM REGRESSED — %s is listed under \"Planned / rolling out\" in docs/robot-api-design.md but its robot bridge is shipped", tool)
		}
	}
}
