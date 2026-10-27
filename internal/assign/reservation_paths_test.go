package assign

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
)

// The bead description from ntm#336, generalized: inputs in a prose
// paragraph, outputs as backticked list items under an ownership heading.
const issue336Description = "Read README.md and docs/research-status.md.\n\n" +
	"## Owned outputs\n\n" +
	"- `docs/specification/work/inventory.md`\n" +
	"- `docs/evidence/inventory.md`\n\n" +
	"## Acceptance criteria\n\n" +
	"- Inventory the modules and cite evidence. Re-run tests/inventory_test.go.\n"

func sortedPaths(paths []string) []string {
	out := append([]string(nil), paths...)
	sort.Strings(out)
	return out
}

func TestReservationPathsForBeadUsesDeclaredOutputs(t *testing.T) {
	got := ReservationPathsForBead("Inventory modules and entry points", issue336Description)
	want := []string{"docs/evidence/inventory.md", "docs/specification/work/inventory.md"}
	if !reflect.DeepEqual(sortedPaths(got), want) {
		t.Fatalf("ReservationPathsForBead() = %v, want exactly the owned outputs %v", got, want)
	}
}

func TestReservationPathsForBeadSectionStyles(t *testing.T) {
	for _, tc := range []struct {
		name        string
		description string
		want        []string
	}{
		{
			name:        "label line",
			description: "Context in internal/old/legacy.go.\n\nFiles to modify:\n- internal/cli/assign.go\n- internal/bv/bv.go\n\nNotes:\n- see internal/old/other.go\n",
			want:        []string{"internal/bv/bv.go", "internal/cli/assign.go"},
		},
		{
			name:        "bold label",
			description: "Uses pkg/input.go.\n\n**Owned files**\n- `pkg/output.go`\n",
			want:        []string{"pkg/output.go"},
		},
		{
			name:        "two owned sections",
			description: "### Outputs\n- a/one.go\n### Inputs\n- a/in.go\n### Owned paths\n- a/two.go\n",
			want:        []string{"a/one.go", "a/two.go"},
		},
		{
			name:        "no declared section uses title and description",
			description: "Update `internal/api/server.go` and internal/api/routes.go",
			want:        []string{"internal/api/routes.go", "internal/api/server.go", "internal/title.go"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title := "Refactor"
			if tc.name == "no declared section uses title and description" {
				title = "Refactor internal/title.go"
			}
			if got := sortedPaths(ReservationPathsForBead(title, tc.description)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ReservationPathsForBead() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReservationPathsForBeadDeclaredButEmptyFailsClosed(t *testing.T) {
	description := "Read internal/cli/assign.go.\n\n## Owned outputs\n\nTo be decided.\n"
	if got := ReservationPathsForBead("Fix internal/cli/assign.go", description); len(got) != 0 {
		t.Fatalf("ReservationPathsForBead() = %v; a declared ownership section with no paths must not fall back to inputs", got)
	}
}

func TestExtractFilePathsBacktickedAndNoFilenamePrefixGlobs(t *testing.T) {
	got := ExtractFilePaths("", "Read README.md and docs/research-status.md, then edit `docs/specification/work/inventory.md` and `.github/ci.yml`.")
	for _, want := range []string{"README.md", "docs/research-status.md", "docs/specification/work/inventory.md", ".github/ci.yml"} {
		found := false
		for _, p := range got {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	for _, p := range got {
		switch p {
		case "docs/research-status/**/*", "docs/specification/work/inventory/**/*":
			t.Errorf("file name became a directory glob: %q (all: %v)", p, got)
		}
	}

	// A directory named on its own still widens to a glob, including at the
	// end of a sentence and inside backticks.
	dirs := ExtractFilePaths("", "Move commands to internal/cli. Touch `pkg/api` too.")
	for _, want := range []string{"internal/cli/**/*", "pkg/api/**/*"} {
		found := false
		for _, p := range dirs {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing directory glob %q in %v", want, dirs)
		}
	}
}

// ntm#371: URL paths, parent escapes and bare wildcards are never project
// files, and Agent Mail refuses the whole request if one is sent.
func TestExtractFilePathsDropsPatternsAgentMailRefuses(t *testing.T) {
	got := ExtractFilePaths("Wire `/IRRIS.asmx` entries",
		"Entries for `/IRRIS.asmx` (non-credential args) and GET /api/v1/foo.json.\n"+
			"Compare ../shared/config.toml and `/srv/app/*.go`; see `**/*` and ** here.\n"+
			"Edit internal/soap/irris.go.")
	if want := []string{"internal/soap/irris.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractFilePaths() = %v, want %v", got, want)
	}
}

// Review of 89b32d9a: the owned section must not under-reserve when it has
// its own sub-labels, a path on the heading line, or a fenced snippet with
// "# comment" lines, and a bead whose files are only in prose still reserves
// them.
func TestReservationPathsForBeadDoesNotUnderReserve(t *testing.T) {
	for _, tc := range []struct {
		name        string
		description string
		want        []string
	}{
		{
			name:        "sub-labels inside a heading section",
			description: "Read internal/old/legacy.go.\n\n## Owned outputs\nPrimary:\n- internal/cli/assign.go\nTests:\n- internal/cli/assign_test.go\n\n## Notes\n- see internal/old/other.go\n",
			want:        []string{"internal/cli/assign.go", "internal/cli/assign_test.go"},
		},
		{
			name:        "deeper heading inside a heading section",
			description: "## Owned outputs\n- a/one.go\n### Tests\n- a/one_test.go\n## Inputs\n- a/in.go\n",
			want:        []string{"a/one.go", "a/one_test.go"},
		},
		{
			name:        "path on the heading line",
			description: "Read pkg/input.go.\n\n## Owned outputs: `pkg/output.go`\n\n## Acceptance\n- tests pass\n",
			want:        []string{"pkg/output.go"},
		},
		{
			name:        "fenced comment does not end the section",
			description: "## Owned outputs\n```sh\n# regenerate\nmake gen\n```\n- gen/out.go\n## Inputs\n- gen/in.go\n",
			want:        []string{"gen/out.go"},
		},
		{
			name:        "fenced comment does not open a section",
			description: "Edit internal/api/server.go.\n```sh\n# Output\necho hi\n```\n",
			want:        []string{"internal/api/server.go"},
		},
		{
			name:        "files only in prose",
			description: "The stall detector in internal/robot/tmux_adapter.go grades shells; fix it and extend internal/robot/is_working.go so the type survives.",
			want:        []string{"internal/robot/is_working.go", "internal/robot/tmux_adapter.go"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sortedPaths(ReservationPathsForBead("Refactor", tc.description)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ReservationPathsForBead() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeclaresOwnedPaths(t *testing.T) {
	if !DeclaresOwnedPaths("Read a.go.\n\n## Deliverables\n- A short report\n") {
		t.Fatal("a Deliverables heading is an owned section")
	}
	if DeclaresOwnedPaths("Read a.go and edit b.go.\n```\n# Outputs\n```\n") {
		t.Fatal("a heading inside a code fence is not an owned section")
	}
}

// The pre-claim resolution every reserving assignment surface shares
// (ntm#336): the description's owned outputs count, a pathless bead is refused
// with a next step, and a declared-but-empty owned section says so rather
// than claim the bead names no files.
func TestReservationPathsForAssignment(t *testing.T) {
	paths, err := ReservationPathsForAssignment("Inventory modules and entry points", issue336Description)
	want := []string{"docs/evidence/inventory.md", "docs/specification/work/inventory.md"}
	if err != nil || !reflect.DeepEqual(sortedPaths(paths), want) {
		t.Fatalf("ReservationPathsForAssignment() = %v, %v; want the owned outputs %v", paths, err, want)
	}

	if _, err := ReservationPathsForAssignment("Think about architecture", "Consider how it should evolve."); !errors.Is(err, ErrNoReservationPaths) {
		t.Fatalf("pathless bead: err = %v, want ErrNoReservationPaths", err)
	}
	if !strings.Contains(ErrNoReservationPaths.Error(), "--reserve-files=false") {
		t.Fatalf("ErrNoReservationPaths names no opt-out: %v", ErrNoReservationPaths)
	}

	_, err = ReservationPathsForAssignment("Write the report", "Read internal/robot/robot.go.\n\n## Deliverables\n- A short written report\n")
	if !errors.Is(err, ErrOwnedSectionHasNoPaths) {
		t.Fatalf("declared-but-empty owned section: err = %v, want ErrOwnedSectionHasNoPaths", err)
	}

	prose, err := ReservationPathsForAssignment("Stop grading shells", "The stall detector in internal/robot/tmux_adapter.go grades shells; fix it there.")
	if err != nil || !reflect.DeepEqual(prose, []string{"internal/robot/tmux_adapter.go"}) {
		t.Fatalf("prose-only paths = %v, err = %v; want the prose path reserved", prose, err)
	}
}

// Port-level discovery reads the live description, keeps the request title
// when it has one, and still finds title paths when the bead is unreadable.
func TestDiscoverReservationPaths(t *testing.T) {
	var readDir, readBead string
	read := func(_ context.Context, projectDir, beadID string) (string, string, error) {
		readDir, readBead = projectDir, beadID
		return "Live title in internal/live/title.go", issue336Description, nil
	}
	request := assignment.ReservationRequest{BeadID: "ntm-336", BeadTitle: "Inventory modules and entry points"}
	got := DiscoverReservationPaths(t.Context(), "/project", request, read)
	if want := []string{"docs/evidence/inventory.md", "docs/specification/work/inventory.md"}; !reflect.DeepEqual(sortedPaths(got), want) {
		t.Fatalf("DiscoverReservationPaths() = %v, want the live owned outputs %v", got, want)
	}
	if readDir != "/project" || readBead != "ntm-336" {
		t.Fatalf("read bead %q in %q, want ntm-336 in /project", readBead, readDir)
	}

	untitled := DiscoverReservationPaths(t.Context(), "/project", assignment.ReservationRequest{BeadID: "ntm-t"},
		func(context.Context, string, string) (string, string, error) {
			return "Fix internal/live/title.go", "", nil
		})
	if !reflect.DeepEqual(untitled, []string{"internal/live/title.go"}) {
		t.Fatalf("untitled request discovered %v, want the live title path", untitled)
	}

	unreadable := DiscoverReservationPaths(t.Context(), "/project", assignment.ReservationRequest{BeadID: "ntm-u", BeadTitle: "Fix internal/req/title.go"},
		func(context.Context, string, string) (string, string, error) {
			return "Ignored internal/ignored/title.go", "Ignored internal/ignored/body.go", errors.New("br unavailable")
		})
	if !reflect.DeepEqual(unreadable, []string{"internal/req/title.go"}) {
		t.Fatalf("unreadable bead discovered %v, want the request title path", unreadable)
	}

	if none := DiscoverReservationPaths(t.Context(), "", assignment.ReservationRequest{BeadID: "ntm-n", BeadTitle: "Think"}, read); len(none) != 0 {
		t.Fatalf("no project and a pathless title discovered %v, want nothing", none)
	}
}
