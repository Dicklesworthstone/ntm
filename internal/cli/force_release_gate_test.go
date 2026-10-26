package cli

import (
	"encoding/json"
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

func TestForceReleaseCommandUsesSessionProjectPolicy(t *testing.T) {
	for _, test := range []struct {
		name       string
		cwdPolicy  string
		project    string
		homePolicy string
		wantError  string
	}{
		{name: "target denies despite permissive cwd", cwdPolicy: "auto", project: "never", wantError: "disabled by policy"},
		{name: "target permits despite restrictive cwd", cwdPolicy: "never", project: "auto"},
		{name: "unrelated malformed cwd is ignored", cwdPolicy: "malformed", project: "auto"},
		{name: "malformed target is refused", cwdPolicy: "auto", project: "malformed", wantError: "loading policy"},
		{name: "missing target policy needs approval", cwdPolicy: "auto", wantError: "requires approval"},
		{name: "target approval is retained", cwdPolicy: "auto", project: "approval", wantError: "requires approval"},
		{name: "home denial retains precedence", cwdPolicy: "auto", project: "auto", homePolicy: "never", wantError: "disabled by policy"},
		{name: "home permission retains precedence", cwdPolicy: "never", project: "never", homePolicy: "auto"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetFlags()
			t.Cleanup(resetFlags)
			isolateSessionAgentStorage(t)
			isolateOverseerSessionProbe(t)
			projectsBase := canonicalTempDir(t)
			const session = "policy-session"
			project := filepath.Join(projectsBase, session)
			if err := os.MkdirAll(project, 0700); err != nil {
				t.Fatal(err)
			}
			saveSessionAgentForTest(t, session, project, "RedStone")
			oldCfg := cfg
			cfg = &config.Config{ProjectsBase: projectsBase}
			t.Cleanup(func() { cfg = oldCfg })
			cwd := canonicalTempDir(t)
			t.Chdir(cwd)
			writePolicy := func(root, mode string) {
				t.Helper()
				if mode == "" {
					return
				}
				path := filepath.Join(root, ".ntm", "policy.yaml")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				data := "version: 1\nautomation:\n  force_release: " + mode + "\n"
				if mode == "malformed" {
					data = "automation: [\n"
				}
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writePolicy(cwd, test.cwdPolicy)
			writePolicy(project, test.project)
			writePolicy(os.Getenv("HOME"), test.homePolicy)
			stub := newMailStub(t, nil)
			t.Cleanup(stub.Close)
			stub.reservations = []agentmail.FileReservation{{
				ID: 17, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/worker.go",
				CreatedTS: agentmail.FlexTime{Time: time.Now().Add(-time.Hour)},
				ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(time.Hour)}, Exclusive: true,
			}}
			t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
			t.Setenv("NTM_USER", "alice")
			t.Setenv("PATH", t.TempDir())
			cmd := newLocksForceReleaseCmd()
			cmd.SetArgs([]string{session, "17", "--yes", "--note", "holder stopped"})
			_, err := captureStdout(t, func() error { return cmd.ExecuteContext(t.Context()) })
			if test.wantError == "" {
				if err != nil || len(stub.forceReleaseCalls) != 1 {
					t.Fatalf("target policy did not authorize release: error=%v calls=%+v", err, stub.forceReleaseCalls)
				}
				if call := stub.forceReleaseCalls[0]; call.Project != project || call.Agent != "RedStone" {
					t.Fatalf("release escaped selected project or actor: %+v", call)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) || len(stub.forceReleaseCalls) != 0 {
				t.Fatalf("target policy did not block release: error=%v calls=%+v", err, stub.forceReleaseCalls)
			}
			if test.project == "malformed" && (stub.ensureCalled != 0 || len(stub.listCalls) != 0) {
				t.Fatal("malformed target policy reached Agent Mail")
			}
			if test.wantError == "requires approval" {
				engine, store, err := getApprovalEngine()
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				pending, err := engine.ListPending(t.Context())
				if err != nil || len(pending) != 1 {
					t.Fatalf("pending approvals=%+v error=%v", pending, err)
				}
				var resource struct {
					Operation approval.ForceReleaseOperation `json:"operation"`
				}
				if err := json.Unmarshal([]byte(pending[0].Resource), &resource); err != nil || resource.Operation.ProjectKey != project || resource.Operation.AgentName != "RedStone" {
					t.Fatalf("approval lost selected project identity: %+v error=%v", resource, err)
				}
			}
		})
	}
}

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
	isolateOverseerSessionProbe(t)
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
