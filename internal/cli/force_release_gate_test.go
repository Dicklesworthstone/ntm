package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/approval"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/policy"
)

func TestForceReleaseGatePolicyModesNeedNoApprovalStore(t *testing.T) {
	for _, mode := range []string{"never", "auto"} {
		t.Run(mode, func(t *testing.T) {
			pol := &policy.Policy{Automation: policy.AutomationConfig{ForceRelease: mode}}
			decision, err := evaluateForceReleaseGate(t.Context(), pol, nil, approval.ForceReleaseOperation{}, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allowed != (mode == "auto") || decision.Created {
				t.Fatalf("unexpected policy decision: %+v", decision)
			}
		})
	}
}

// Exercise the actual command, saved session identity, real SQLite approval
// store and Agent Mail HTTP adapter. The shared gate's failure/race matrix is
// covered in internal/approval; this proves CLI flags reach that gate.
func TestRunForceReleaseApprovalLifecycle(t *testing.T) {
	resetFlags()
	isolateSessionAgentStorage(t)
	projectsBase := canonicalTempDir(t)
	projectKey := filepath.Join(projectsBase, "approval-session")
	if err := os.MkdirAll(projectKey, 0o755); err != nil {
		t.Fatal(err)
	}
	const session = "approval-session"
	saveSessionAgentForTest(t, session, projectKey, "RedStone")

	stub := newMailStub(t, nil)
	t.Cleanup(stub.Close)
	stub.reservations = []agentmail.FileReservation{{
		ID: 17, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/serve/*",
		CreatedTS: agentmail.FlexTime{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(time.Hour)}, Exclusive: true,
	}}
	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Setenv("NTM_USER", "alice")
	t.Setenv("NTM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	// Never invoke a developer's external SLB/tmux binary from this fixture.
	t.Setenv("PATH", t.TempDir())
	t.Chdir(canonicalTempDir(t))
	policyDir := filepath.Join(os.Getenv("HOME"), ".ntm")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("version: 1\nautomation:\n  force_release: approval\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(predecessor string) error {
		cmd := newLocksForceReleaseCmd()
		args := []string{session, "17", "--note", "holder crashed", "--yes"}
		if predecessor != "" {
			args = append(args, "--new-approval", predecessor)
		}
		cmd.SetArgs(args)
		cmd.SetContext(t.Context())
		return cmd.Execute()
	}
	if err := run(""); err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("first request = %v, want pending approval", err)
	}
	if len(stub.forceReleaseCalls) != 0 {
		t.Fatal("unapproved CLI request reached Agent Mail force-release")
	}
	engine, store, err := getApprovalEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	pending, err := engine.ListPending(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending approvals = %+v, %v", pending, err)
	}
	firstID := pending[0].ID
	if err := engine.Approve(t.Context(), firstID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := run(""); err != nil {
		t.Fatalf("approved CLI release: %v", err)
	}
	if len(stub.forceReleaseCalls) != 1 || stub.forceReleaseCalls[0].Project != projectKey ||
		stub.forceReleaseCalls[0].Agent != "RedStone" {
		t.Fatalf("release did not preserve session project/actor: %+v", stub.forceReleaseCalls)
	}
	if err := run(""); err == nil || !strings.Contains(err.Error(), "consumed") {
		t.Fatalf("spent retry = %v, want explicit consumed outcome", err)
	}
	if len(stub.forceReleaseCalls) != 1 {
		t.Fatal("spent CLI replay repeated external release")
	}

	if err := run(firstID); err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("--new-approval = %v, want a pending successor", err)
	}
	pending, err = engine.ListPending(t.Context())
	if err != nil || len(pending) != 1 || pending[0].ID == firstID {
		t.Fatalf("successor approvals = %+v, %v", pending, err)
	}
	if err := engine.Approve(t.Context(), pending[0].ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := run(firstID); err != nil {
		t.Fatalf("approved successor release: %v", err)
	}
	if err := run(firstID); err == nil || !strings.Contains(err.Error(), "consumed") {
		t.Fatalf("successor replay = %v, want consumed", err)
	}
	if len(stub.forceReleaseCalls) != 2 {
		t.Fatalf("two authorizations dispatched %d releases", len(stub.forceReleaseCalls))
	}
	// A spent authorization remains explicit even when Agent Mail goes
	// offline; replay must not depend on a fresh availability probe.
	stub.Close()
	if err := run(firstID); err == nil || !strings.Contains(err.Error(), "consumed") {
		t.Fatalf("offline spent replay = %v, want consumed", err)
	}
	history, err := engine.History(t.Context())
	if err != nil || len(history) != 2 {
		t.Fatalf("replays minted unexpected authorization: %+v, %v", history, err)
	}
}
