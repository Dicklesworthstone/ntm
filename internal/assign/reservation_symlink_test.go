package assign

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// fakeReservationClient implements FileReservationClient with a canned
// EnsureProject response, mimicking Agent Mail's server-side behavior of
// canonicalizing project paths through symlinks (GH#239).
type fakeReservationClient struct {
	project *agentmail.Project
}

func (f *fakeReservationClient) EnsureProject(context.Context, string) (*agentmail.Project, error) {
	return f.project, nil
}

func (f *fakeReservationClient) ReservePaths(context.Context, agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	return nil, nil
}

func (f *fakeReservationClient) ListReservations(context.Context, string, string, bool) ([]agentmail.FileReservation, error) {
	return nil, nil
}

func (f *fakeReservationClient) ReleaseReservations(context.Context, string, string, []string, []int) (*agentmail.ReleaseReservationsResult, error) {
	return nil, nil
}

func (f *fakeReservationClient) RenewReservations(context.Context, agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
	return nil, nil
}

func symlinkedProjectDirs(t *testing.T) (realDir, linkDir string) {
	t.Helper()
	base := t.TempDir()
	realDir = filepath.Join(base, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	linkDir = filepath.Join(base, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	// Resolve realDir fully: TempDir may itself sit behind symlinks.
	resolved, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return resolved, linkDir
}

func TestNewFileReservationManagerCanonicalizesSymlinkedKey(t *testing.T) {
	t.Parallel()
	realDir, linkDir := symlinkedProjectDirs(t)
	m := NewFileReservationManager(nil, linkDir)
	if m.projectKey != realDir {
		t.Errorf("projectKey = %q, want canonical %q", m.projectKey, realDir)
	}
}

func TestEnsureProjectAcceptsSymlinkEquivalentHumanKey(t *testing.T) {
	t.Parallel()
	realDir, linkDir := symlinkedProjectDirs(t)

	// Agent Mail registered the project under the canonical path; the caller
	// keys by the symlinked alias. This must bind, not mismatch (GH#239).
	client := &fakeReservationClient{project: &agentmail.Project{ID: 7, HumanKey: realDir}}
	m := NewFileReservationManager(client, linkDir)
	id, err := m.ensureProject(context.Background())
	if err != nil {
		t.Fatalf("ensureProject with symlinked key: %v", err)
	}
	if id != 7 {
		t.Errorf("project ID = %d, want 7", id)
	}
}

func TestEnsureProjectStillFailsClosedOnRealMismatch(t *testing.T) {
	t.Parallel()
	realDir, _ := symlinkedProjectDirs(t)
	otherDir := t.TempDir()

	client := &fakeReservationClient{project: &agentmail.Project{ID: 9, HumanKey: otherDir}}
	m := NewFileReservationManager(client, realDir)
	if _, err := m.ensureProject(context.Background()); err == nil || !strings.Contains(err.Error(), "binding mismatch") {
		t.Fatalf("genuinely different HumanKey must fail closed, got err=%v", err)
	}
}
