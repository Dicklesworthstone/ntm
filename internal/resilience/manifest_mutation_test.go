//go:build unix

package resilience

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

// TestManifestMutationProcess is executed in a fresh OS process, sharing only
// XDG_DATA_HOME with its parent. No in-process mutex can make these tests pass.
func TestManifestMutationProcess(t *testing.T) {
	mode := os.Getenv("NTM_MANIFEST_MUTATION_HELPER")
	if mode == "" {
		return
	}
	fmt.Println("ready")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	index, _ := strconv.Atoi(os.Getenv("NTM_MANIFEST_MUTATION_INDEX"))
	if mode == "rebind" {
		var expected AgentConfig
		if err := json.Unmarshal([]byte(os.Getenv("NTM_MANIFEST_REBIND_EXPECTED")), &expected); err != nil {
			t.Fatal(err)
		}
		applied, err := RebindAgentPaneContext(context.Background(), "fleet", expected, "%31", 7)
		if err != nil || !applied {
			t.Fatalf("manifest continuation = %v, %v", applied, err)
		}
		return
	}
	if mode == "hold" {
		err := mutateManifest(context.Background(), "fleet", func(string) error {
			fmt.Println("locked")
			// Exit without running the deferred close, like an abruptly exited
			// owner. The kernel must release its lock for the next operation.
			os.Exit(0)
			return nil
		})
		t.Fatal(err)
	}
	count := 1
	if mode == "batch" {
		count = 8
	}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("%%%d", 100+index*count+i)
		if err := UpsertAgentConfig("fleet", "/different-caller-path", AgentConfig{
			PaneID: id, PaneIndex: index*count + i, Type: "cc", Model: id, Command: "claude " + id,
			LaunchBinding: &LaunchBinding{Provider: "cc", Launcher: "caam", Identifier: id},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

type manifestChild struct {
	input io.WriteCloser
	lines *bufio.Reader
	done  <-chan error
	log   *bytes.Buffer
}

func startManifestChild(t *testing.T, mode string, index int) manifestChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManifestMutationProcess$")
	cmd.Env = append(os.Environ(), "NTM_MANIFEST_MUTATION_HELPER="+mode, "NTM_MANIFEST_MUTATION_INDEX="+strconv.Itoa(index))
	var log bytes.Buffer
	cmd.Stderr = &log
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stdout)
	line, err := r.ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("child failed to start: %q %v", line, err)
	}
	finished := make(chan error, 1)
	// All helpers write little enough output to fit the pipe; no output is
	// treated as evidence of success. Wait's exit status is authoritative.
	go func() { finished <- cmd.Wait() }()
	t.Cleanup(func() { _ = input.Close() })
	return manifestChild{input, r, finished, &log}
}

func releaseManifestChild(t *testing.T, child manifestChild) {
	t.Helper()
	if _, err := io.WriteString(child.input, "go\n"); err != nil {
		t.Fatal(err)
	}
}

func waitManifestChild(t *testing.T, child manifestChild) {
	t.Helper()
	select {
	case err := <-child.done:
		if err != nil {
			t.Fatalf("manifest child failed: %v: %s", err, child.log.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("manifest child did not exit")
	}
}

func saveMutationFixture(t *testing.T) *SpawnManifest {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := &SpawnManifest{
		Session: "fleet", ProjectDir: "/original-project", AutoRestart: true,
		SessionIdentity: "12:$4:567", ConfigPath: "/operator.toml", MonitorGeneration: "generation",
		AccountRotation: &RotationMonitorOptions{Providers: []string{"claude"}, PollSeconds: 3},
		Agents:          []AgentConfig{{PaneID: "%1", Type: "cod", Command: "codex", Model: "original"}},
	}
	if err := SaveManifest(m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManifestUpsertReadsAfterAcquiringOwnership(t *testing.T) {
	initial := saveMutationFixture(t)
	lock, err := tryMonitorLock(filepath.Join(ManifestDir(), "fleet.mutation.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	child := startManifestChild(t, "one", 0)
	releaseManifestChild(t, child)
	select {
	case err := <-child.done:
		t.Fatalf("UpsertAgentConfig escaped the preceding writer's lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	// The preceding owner commits a new row while the other process waits.
	// Reading before ownership and merely locking SaveManifest still loses it.
	initial.Agents = append(initial.Agents, AgentConfig{PaneID: "%2", Type: "gmi", Command: "gemini"})
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := util.AtomicWriteFile(filepath.Join(ManifestDir(), "fleet.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	waitManifestChild(t, child)
	got, err := LoadManifest("fleet")
	if err != nil || len(got.Agents) != 3 {
		t.Fatalf("lost an independently committed agent: %+v, %v", got, err)
	}
	if got.Agents[1].PaneID != "%2" || got.Agents[2].PaneID != "%100" {
		t.Fatalf("wrong merged records: %+v", got.Agents)
	}
	got.Agents = initial.Agents
	if !reflect.DeepEqual(got, initial) {
		t.Fatalf("upsert changed session ownership/policy: %+v", got)
	}
}

func TestManifestConcurrentProcessesRetainEveryAgent(t *testing.T) {
	initial := saveMutationFixture(t)
	const workers = 8
	children := make([]manifestChild, workers)
	for i := range children {
		children[i] = startManifestChild(t, "batch", i)
	}
	for _, child := range children {
		releaseManifestChild(t, child)
	}
	for _, child := range children {
		waitManifestChild(t, child)
	}
	got, err := LoadManifest("fleet")
	if err != nil || len(got.Agents) != 1+workers*8 {
		t.Fatalf("concurrent additions lost metadata: %+v, %v", got, err)
	}
	seen := make(map[string]bool)
	for _, agent := range got.Agents {
		if seen[agent.PaneID] {
			t.Fatalf("duplicate agent %s", agent.PaneID)
		}
		seen[agent.PaneID] = true
		if agent.PaneID != "%1" && (agent.LaunchBinding == nil || agent.LaunchBinding.Identifier != agent.PaneID || agent.Model != agent.PaneID) {
			t.Fatalf("lost per-agent launch affinity: %+v", agent)
		}
	}
	for i := 100; i < 100+workers*8; i++ {
		if !seen[fmt.Sprintf("%%%d", i)] {
			t.Fatalf("missing pane %%%d", i)
		}
	}
	got.Agents = initial.Agents
	if !reflect.DeepEqual(got, initial) {
		t.Fatal("concurrent updates changed session policy")
	}
}

func TestManifestSaveDeleteAndUpsertShareOwnership(t *testing.T) {
	for _, operation := range []string{"save", "delete", "upsert"} {
		t.Run(operation, func(t *testing.T) {
			initial := saveMutationFixture(t)
			path := filepath.Join(ManifestDir(), "fleet.mutation.lock")
			lock, err := tryMonitorLock(path)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			started, done := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				switch operation {
				case "save":
					done <- SaveManifest(initial)
				case "delete":
					done <- DeleteManifest("fleet")
				default:
					done <- UpsertAgentConfig("fleet", "", AgentConfig{PaneID: "%3", Type: "cc"})
				}
			}()
			<-started
			select {
			case err := <-done:
				t.Fatalf("%s bypassed ownership: %v", operation, err)
			case <-time.After(40 * time.Millisecond):
			}
			// Readers and unrelated sessions do not wait behind this writer.
			if got, err := LoadManifest("fleet"); err != nil || !reflect.DeepEqual(got, initial) {
				t.Fatalf("read blocked or changed: %+v %v", got, err)
			}
			if err := UpsertAgentConfig("other", "/other", AgentConfig{PaneID: "%9", Type: "cc"}); err != nil {
				t.Fatalf("independent session blocked: %v", err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("mutation replaced/unlinked the rendezvous inode")
			}
		})
	}
}

func TestManifestMutationFailureAndPanicReleaseOwnership(t *testing.T) {
	saveMutationFixture(t)
	want := errors.New("injected mutation failure")
	if err := mutateManifest(context.Background(), "fleet", func(string) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if got := recover(); got != want {
				t.Fatalf("panic = %v", got)
			}
		}()
		_ = mutateManifest(context.Background(), "fleet", func(string) error { panic(want) })
	}()
	if err := UpsertAgentConfig("fleet", "", AgentConfig{PaneID: "%3"}); err != nil {
		t.Fatalf("prior failure stranded ownership: %v", err)
	}
}

func TestManifestMutationCancellationNeverRunsCallback(t *testing.T) {
	saveMutationFixture(t)
	lock, err := tryMonitorLock(filepath.Join(ManifestDir(), "fleet.mutation.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = mutateManifest(ctx, "fleet", func(string) error { t.Error("cancelled mutation ran"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait did not preserve its deadline: %v", err)
	}
	_ = lock.Close()
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	err = mutateManifest(cancelled, "not-created", func(string) error { t.Error("pre-cancelled mutation ran"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ManifestDir(), "not-created.mutation.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pre-cancelled operation created a lock")
	}
}

func TestManifestOwnerExitReleasesLock(t *testing.T) {
	saveMutationFixture(t)
	child := startManifestChild(t, "hold", 0)
	releaseManifestChild(t, child)
	waitManifestChild(t, child)
	if err := UpsertAgentConfig("fleet", "", AgentConfig{PaneID: "%3"}); err != nil {
		t.Fatalf("exited owner stranded manifest: %v", err)
	}
}

func TestManifestUpsertRejectsCorruptAmbiguousAndMisboundFiles(t *testing.T) {
	for _, raw := range []string{
		`{`,
		`null`,
		`{"session":"another-session","agents":[]}`,
		`{"session":"fleet","agents":[{"pane_id":"%2"},{"pane_id":"%2"}]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			saveMutationFixture(t)
			path := filepath.Join(ManifestDir(), "fleet.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err := UpsertAgentConfig("fleet", "", AgentConfig{PaneID: "%2"}); err == nil {
				t.Fatal("invalid manifest authorized an update")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != raw {
				t.Fatalf("failed update replaced original evidence: %q %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(ManifestDir(), "another-session.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("manifest content redirected a write to another session")
			}
		})
	}
}

func TestManifestUpsertReplacesOnlyRequestedAgent(t *testing.T) {
	initial := saveMutationFixture(t)
	agent := AgentConfig{PaneID: "%1", Type: "cod", Model: "new-model", Command: "new-command",
		LaunchBinding: &LaunchBinding{Provider: "cod", Launcher: "caam", Identifier: "retained-profile"}}
	if err := UpsertAgentConfig("fleet", "/wrong-default", agent); err != nil {
		t.Fatal(err)
	}
	agent.LaunchBinding.Identifier = "caller-mutated"
	got, err := LoadManifest("fleet")
	if err != nil || len(got.Agents) != 1 || got.Agents[0].Model != "new-model" || got.Agents[0].LaunchBinding.Identifier != "retained-profile" {
		t.Fatalf("replacement/affinity lost: %+v %v", got, err)
	}
	got.Agents = initial.Agents
	if !reflect.DeepEqual(got, initial) {
		t.Fatal("per-agent update changed session-level fields")
	}
	if err := UpsertAgentConfig("new-session", "/new", AgentConfig{PaneID: "%20"}); err != nil {
		t.Fatal(err)
	}
	created, err := LoadManifest("new-session")
	if err != nil || created.AutoRestart || created.AccountRotation != nil || created.ProjectDir != "/new" {
		t.Fatalf("new metadata silently enabled automation: %+v %v", created, err)
	}
}

func TestManifestContinuationPreservesLaunchAndPolicy(t *testing.T) {
	initial := saveMutationFixture(t)
	expected := initial.Agents[0]
	expected.PaneIndex = 3
	expected.ProjectDir = "/per-agent-worktree"
	expected.LaunchBinding = &LaunchBinding{Provider: "cod", Launcher: "caam", Identifier: "original-account"}
	initial.Agents = []AgentConfig{expected, {PaneID: "%2", PaneIndex: 1, Type: "gmi", Command: "gemini"}}
	if err := SaveManifest(initial); err != nil {
		t.Fatal(err)
	}
	applied, err := RebindAgentPaneContext(context.Background(), "fleet", expected, "%31", 7)
	if err != nil || !applied {
		t.Fatalf("continuation = %v, %v", applied, err)
	}
	got, err := LoadManifest("fleet")
	if err != nil {
		t.Fatal(err)
	}
	want := *initial
	want.Agents = append([]AgentConfig(nil), initial.Agents...)
	want.Agents[0].PaneID = "%31"
	want.Agents[0].PaneIndex = 7
	if !reflect.DeepEqual(got, &want) {
		t.Fatalf("continuation changed launch metadata, neighbors, or policy:\ngot %+v\nwant %+v", got, &want)
	}
	if expected.PaneID != "%1" || expected.LaunchBinding.Identifier != "original-account" {
		t.Fatalf("continuation mutated the caller's observation: %+v", expected)
	}
	before, err := os.ReadFile(filepath.Join(ManifestDir(), "fleet.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A crash can occur after the atomic write but before its journal receipt.
	// Repeating that exact update must not strand an already-moved record.
	applied, err = RebindAgentPaneContext(context.Background(), "fleet", expected, "%31", 7)
	if err != nil || !applied {
		t.Fatalf("replayed continuation = %v, %v", applied, err)
	}
	after, err := os.ReadFile(filepath.Join(ManifestDir(), "fleet.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("replayed continuation rewrote the manifest: %v", err)
	}
}

func TestManifestContinuationRefusesChangedOrAmbiguousOwnership(t *testing.T) {
	for _, scenario := range []string{"changed-command", "changed-binding", "missing-source", "occupied-destination", "duplicate-source", "duplicate-destination"} {
		t.Run(scenario, func(t *testing.T) {
			initial := saveMutationFixture(t)
			expected := initial.Agents[0]
			expected.LaunchBinding = &LaunchBinding{Provider: "cod", Launcher: "caam", Identifier: "original"}
			initial.Agents[0] = expected
			switch scenario {
			case "changed-command":
				initial.Agents[0].Command = "codex --model changed"
			case "changed-binding":
				initial.Agents[0].LaunchBinding = &LaunchBinding{Provider: "cod", Launcher: "caam", Identifier: "different"}
			case "missing-source":
				initial.Agents = nil
			case "occupied-destination":
				initial.Agents = append(initial.Agents, AgentConfig{PaneID: "%31", Type: "cc", Command: "claude"})
			case "duplicate-source":
				initial.Agents = append(initial.Agents, expected)
			case "duplicate-destination":
				initial.Agents = []AgentConfig{{PaneID: "%31"}, {PaneID: "%31"}}
			}
			if err := SaveManifest(initial); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(ManifestDir(), "fleet.json"))
			if err != nil {
				t.Fatal(err)
			}
			applied, err := RebindAgentPaneContext(context.Background(), "fleet", expected, "%31", 7)
			if applied {
				t.Fatalf("%s authorized ownership transfer", scenario)
			}
			if (strings.HasPrefix(scenario, "duplicate-") || scenario == "occupied-destination") && err == nil {
				t.Fatalf("%s did not report an ownership conflict", scenario)
			}
			after, readErr := os.ReadFile(filepath.Join(ManifestDir(), "fleet.json"))
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("refused continuation changed original evidence: %v", readErr)
			}
		})
	}
}

func TestManifestContinuationWaitsForWriterAndKeepsIndependentUpdates(t *testing.T) {
	initial := saveMutationFixture(t)
	encoded, err := json.Marshal(initial.Agents[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_MANIFEST_REBIND_EXPECTED", string(encoded))
	lock, err := tryMonitorLock(filepath.Join(ManifestDir(), "fleet.mutation.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	child := startManifestChild(t, "rebind", 0)
	releaseManifestChild(t, child)
	select {
	case err := <-child.done:
		t.Fatalf("continuation bypassed the preceding writer's fence: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	initial.Agents = append(initial.Agents, AgentConfig{PaneID: "%2", Type: "gmi", Command: "gemini", Model: "neighbor"})
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := util.AtomicWriteFile(filepath.Join(ManifestDir(), "fleet.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	waitManifestChild(t, child)
	got, err := LoadManifest("fleet")
	if err != nil {
		t.Fatal(err)
	}
	initial.Agents[0].PaneID = "%31"
	initial.Agents[0].PaneIndex = 7
	if !reflect.DeepEqual(got, initial) {
		t.Fatalf("continuation lost an independent committed update: %+v", got)
	}
}

func TestManifestContinuationCancellationAndMissingManifest(t *testing.T) {
	initial := saveMutationFixture(t)
	lock, err := tryMonitorLock(filepath.Join(ManifestDir(), "fleet.mutation.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	applied, err := RebindAgentPaneContext(ctx, "fleet", initial.Agents[0], "%31", 7)
	if applied || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled continuation = %v, %v", applied, err)
	}
	got, err := LoadManifest("fleet")
	if err != nil || !reflect.DeepEqual(got, initial) {
		t.Fatalf("cancelled continuation changed manifest: %+v %v", got, err)
	}
	applied, err = RebindAgentPaneContext(context.Background(), "untracked", initial.Agents[0], "%31", 7)
	if applied || err != nil {
		t.Fatalf("absent manifest continuation = %v, %v", applied, err)
	}
	if _, err := os.Stat(filepath.Join(ManifestDir(), "untracked.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("continuation invented a manifest for an untracked session: %v", err)
	}
}

func TestManifestUnsafeLockCannotAuthorizeMutation(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "directory", "shared"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			if err := os.MkdirAll(ManifestDir(), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(ManifestDir(), "fleet.mutation.lock")
			target := filepath.Join(t.TempDir(), "untouched")
			if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(target, path)
			case "hardlink":
				err = os.Link(target, path)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			default:
				err = os.WriteFile(path, nil, 0600)
				if err == nil {
					err = os.Chmod(path, 0644)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = mutateManifest(ctx, "fleet", func(string) error { t.Error("unsafe file admitted mutation"); return nil })
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unsafe lock error = %v", err)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != "unchanged" {
				t.Fatal("lock open changed a target")
			}
		})
	}
}

func TestManifestLockIsCloseOnExecAndNotTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := tryMonitorLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, lock.Fd(), syscall.F_GETFD, 0)
	if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatalf("descriptor inherited by exec: flags=%d errno=%v", flags, errno)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "retained" {
		t.Fatal("acquiring lock truncated it")
	}
}

func TestManifestInvalidInputAndMissingDeleteHaveNoEffects(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if SaveManifest(nil) == nil {
		t.Fatal("nil manifest accepted")
	}
	for _, name := range []string{"", "..", "../other", "a/b", `a\b`} {
		if UpsertAgentConfig(name, "", AgentConfig{PaneID: "%1"}) == nil {
			t.Fatalf("invalid session %q accepted", name)
		}
	}
	if err := DeleteManifest("absent"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ManifestDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid/idempotent operations created state")
	}
	if err := UpsertAgentConfig("fleet", "", AgentConfig{PaneID: " "}); err == nil || !strings.Contains(err.Error(), "pane ID") {
		t.Fatal(err)
	}
}
