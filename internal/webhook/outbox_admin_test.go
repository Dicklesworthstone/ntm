//go:build !windows

package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func adminEntry(t *testing.T, m *WebhookManager) OutboxEntry {
	t.Helper()
	snapshot, err := m.ListOutbox()
	if err != nil || len(snapshot.Entries) != 1 {
		t.Fatalf("ListOutbox = %+v, %v", snapshot, err)
	}
	return snapshot.Entries[0]
}

func failedAdminFixture(t *testing.T, url string) (*WebhookManager, string, WebhookConfig) {
	t.Helper()
	dir := t.TempDir()
	wh := outboxTestWebhook(url)
	m := startOutboxManager(t, dir, wh)
	if err := m.Dispatch(Event{ID: "admin", Type: "agent.error", Session: "private-project",
		Message: "sensitive-event-body", Details: map[string]string{"sensitive-detail": "not-for-listing"}}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return adminEntry(t, m).State == "failed" })
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	return m, dir, wh
}

func TestOutboxAdministrationListIsReadOnly(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	m, dir, _ := failedAdminFixture(t, server.URL)
	if err := os.WriteFile(filepath.Join(dir, "untrusted.json"), []byte("sensitive-malformed-record"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := make(map[string][]byte)
	modified := make(map[string]time.Time)
	for _, entry := range before {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		saved[entry.Name()], modified[entry.Name()] = data, info.ModTime()
	}
	snapshot, err := m.ListOutbox()
	if err != nil || len(snapshot.Entries) != 2 {
		t.Fatalf("ListOutbox = %+v, %v", snapshot, err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sensitive-event-body", "sensitive-detail", "not-for-listing", "sensitive-malformed-record", server.URL, "outbox-signing-fixture", "outbox-auth-fixture"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("inspection exposed %q", secret)
		}
	}
	if snapshot.Entries[1].State != "invalid" || snapshot.Entries[1].Revision != "" {
		t.Fatalf("invalid record was not reported safely: %+v", snapshot.Entries)
	}
	after, err := os.ReadDir(dir)
	if err != nil || len(after) != len(before) {
		t.Fatalf("inspection changed directory: %v", err)
	}
	for _, entry := range after {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		info, statErr := entry.Info()
		if err != nil || statErr != nil || !bytes.Equal(saved[entry.Name()], data) || !info.ModTime().Equal(modified[entry.Name()]) {
			t.Fatalf("inspection changed %s", entry.Name())
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("inspection sent HTTP: requests=%d", got)
	}
}

func TestOutboxAdministrationDoesNotCreateAbsentStorage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	m := NewManager(ManagerConfig{OutboxDir: dir})
	result, err := m.ListOutbox()
	if err != nil || !result.Enabled || result.Entries == nil || len(result.Entries) != 0 {
		t.Fatalf("absent snapshot = %+v, %v", result, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("inspection created storage: %v", err)
	}
	disabled, err := NewManager(ManagerConfig{}).ListOutbox()
	if err != nil || disabled.Enabled || disabled.Entries == nil {
		t.Fatalf("disabled snapshot = %+v, %v", disabled, err)
	}
}

func TestOutboxRecoveryPreservesIdentityBudgetAndFailureEvidence(t *testing.T) {
	var healthy atomic.Bool
	requests := make(chan outboxRequest, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureOutboxRequest(r)
		if healthy.Load() {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	wh := outboxTestWebhook(server.URL)
	wh.Retry.MaxRetries = 2
	m := startOutboxManager(t, dir, wh)
	if err := m.Dispatch(Event{ID: "cycles", Type: "bead.completed", Message: "frozen"}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return adminEntry(t, m).State == "failed" })
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	entry := adminEntry(t, m)
	if entry.Attempt != 2 || !entry.RetryAllowed {
		t.Fatalf("initial retry budget: %+v", entry)
	}
	_, before, _ := readOnlyOutboxRecord(t, dir)
	if _, err := m.RetryOutbox(entry.Record, entry.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RetryOutbox(entry.Record, entry.Revision); err == nil {
		t.Fatal("repeated authorization with stale revision succeeded")
	}
	_, authorized, _ := readOnlyOutboxRecord(t, dir)
	if authorized.Attempt != before.Attempt || authorized.Failed || len(authorized.Recovery) != 1 ||
		authorized.Recovery[0].Failure != *before.LastAttempt || !reflect.DeepEqual(authorized.Event, before.Event) ||
		!bytes.Equal(authorized.Payload, before.Payload) || !authorized.NextRetry.Equal(before.NextRetry) {
		t.Fatalf("authorization rewrote evidence: before=%+v after=%+v", before, authorized)
	}
	if len(requests) != 2 {
		t.Fatal("authorization started a sender")
	}
	second := startOutboxManager(t, dir, wh)
	awaitOutbox(t, func() bool { return adminEntry(t, second).State == "failed" })
	if err := second.Stop(); err != nil {
		t.Fatal(err)
	}
	entry = adminEntry(t, second)
	if entry.Attempt != 4 || entry.Recoveries != 1 {
		t.Fatalf("recovery must receive exactly the configured fresh budget: %+v", entry)
	}
	healthy.Store(true)
	if _, err := second.RetryOutbox(entry.Record, entry.Revision); err != nil {
		t.Fatal(err)
	}
	third := startOutboxManager(t, dir, wh)
	awaitOutbox(t, func() bool {
		result, err := third.ListOutbox()
		return err == nil && len(result.Entries) == 0
	})
	if err := third.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 5 {
		t.Fatalf("wire attempts = %d, want 5", len(requests))
	}
	var original outboxRequest
	for i := 1; i <= 5; i++ {
		r := <-requests
		if i == 1 {
			original = r
		}
		if r.attempt != fmt.Sprint(i) || r.id != original.id || r.signature != original.signature || !bytes.Equal(r.body, original.body) {
			t.Fatalf("recovery changed wire identity: attempt %d %+v", i, r)
		}
	}
}

func TestOutboxRecoveryHonorsTerminalRetryAfter(t *testing.T) {
	requests := make(chan outboxRequest, 3)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureOutboxRequest(r)
		if count.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	wh := outboxTestWebhook(server.URL)
	wh.Retry.MaxRetries = 1
	m := startOutboxManager(t, dir, wh)
	if err := m.Dispatch(Event{ID: "cooldown", Type: "agent.error"}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return adminEntry(t, m).State == "failed" })
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	first := <-requests
	entry := adminEntry(t, m)
	if !entry.NextRetry.After(first.at.Add(900 * time.Millisecond)) {
		t.Fatalf("terminal receipt lost receiver cooldown: %+v", entry)
	}
	if _, err := m.RetryOutbox(entry.Record, entry.Revision); err != nil {
		t.Fatal(err)
	}
	second := startOutboxManager(t, dir, wh)
	select {
	case after := <-requests:
		if after.at.Sub(first.at) < time.Second || after.attempt != "2" {
			t.Fatalf("recovery bypassed cooldown or reset attempts: %+v", after)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("authorized delivery did not resume")
	}
	if err := second.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxRecoveryRejectsChangedOrUnsafeStateWithoutWrites(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	_, dir, wh := failedAdminFixture(t, server.URL)
	_, fixture, data := readOnlyOutboxRecord(t, dir)
	cases := []string{"stale revision", "disabled", "missing endpoint", "changed URL", "changed secret", "privacy", "pending", "invalid record", "busy", "excess recoveries"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			key := outboxKey(fixture.ID)
			if err := os.WriteFile(filepath.Join(dir, key), data, 0600); err != nil {
				t.Fatal(err)
			}
			m := NewManager(ManagerConfig{OutboxDir: dir})
			if err := m.Register(wh); err != nil {
				t.Fatal(err)
			}
			entry := adminEntry(t, m)
			switch name {
			case "stale revision":
				entry.Revision = strings.Repeat("0", 64)
			case "disabled", "changed URL", "changed secret":
				changed := wh
				if name == "disabled" {
					changed.Enabled = false
				} else if name == "changed URL" {
					changed.URL += "/different-destination"
				} else {
					changed.Secret = "different-signing-key"
				}
				if err := m.Register(changed); err != nil {
					t.Fatal(err)
				}
			case "missing endpoint":
				m = NewManager(ManagerConfig{OutboxDir: dir})
			case "privacy":
				m.config.PersistenceAllowed = func(string) bool { return false }
			case "pending", "excess recoveries":
				record := fixture
				if name == "pending" {
					record.Failed = false
				} else {
					record.Attempt = maxOutboxRecoveries + 1
					last := *record.LastAttempt
					last.Attempt = record.Attempt
					record.LastAttempt = &last
					for i := 1; i <= maxOutboxRecoveries; i++ {
						failure := last
						failure.Attempt = i
						record.Recovery = append(record.Recovery, outboxRecovery{AuthorizedAt: time.Now(), AfterAttempt: i, Failure: failure})
					}
				}
				encoded, _ := json.Marshal(record)
				if err := os.WriteFile(filepath.Join(dir, key), encoded, 0600); err != nil {
					t.Fatal(err)
				}
				entry = adminEntry(t, m)
			case "invalid record":
				if err := os.WriteFile(filepath.Join(dir, key), []byte("not JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			case "busy":
				o, err := m.administrativeOutbox()
				if err != nil {
					t.Fatal(err)
				}
				lock, err := o.lockKey(key)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			before, err := os.ReadFile(filepath.Join(dir, key))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.RetryOutbox(entry.Record, entry.Revision); err == nil {
				t.Fatal("unsafe recovery was authorized")
			}
			after, err := os.ReadFile(filepath.Join(dir, key))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refused recovery changed evidence: %v", err)
			}
		})
	}
}

func TestOutboxRecoveryProcessHelper(t *testing.T) {
	if os.Getenv("NTM_OUTBOX_ADMIN_HELPER") != "1" {
		return
	}
	m := NewManager(ManagerConfig{OutboxDir: os.Getenv("NTM_OUTBOX_ADMIN_DIR")})
	if err := m.Register(outboxTestWebhook(os.Getenv("NTM_OUTBOX_ADMIN_URL"))); err != nil {
		t.Fatal(err)
	}
	var err error
	if os.Getenv("NTM_OUTBOX_ADMIN_ACTION") == "archive" {
		_, err = m.ArchiveOutbox(os.Getenv("NTM_OUTBOX_ADMIN_KEY"), os.Getenv("NTM_OUTBOX_ADMIN_REVISION"))
	} else {
		_, err = m.RetryOutbox(os.Getenv("NTM_OUTBOX_ADMIN_KEY"), os.Getenv("NTM_OUTBOX_ADMIN_REVISION"))
	}
	if err == nil {
		fmt.Println("RECOVERY_AUTHORIZED")
	} else {
		fmt.Println("RECOVERY_REFUSED")
	}
}

func TestOutboxRecoveryIndependentProcessesAuthorizeOnlyOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	m, dir, _ := failedAdminFixture(t, server.URL)
	entry := adminEntry(t, m)
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestOutboxRecoveryProcessHelper$")
			cmd.Env = append(os.Environ(), "NTM_OUTBOX_ADMIN_HELPER=1", "NTM_OUTBOX_ADMIN_DIR="+dir,
				"NTM_OUTBOX_ADMIN_URL="+server.URL, "NTM_OUTBOX_ADMIN_KEY="+entry.Record, "NTM_OUTBOX_ADMIN_REVISION="+entry.Revision)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("recovery subprocess failed: %v\n%s", err, out)
			}
			results <- string(out)
		}()
	}
	wg.Wait()
	close(results)
	authorized := 0
	for result := range results {
		if strings.Contains(result, "RECOVERY_AUTHORIZED") {
			authorized++
		}
	}
	if authorized != 1 || adminEntry(t, m).Recoveries != 1 {
		t.Fatalf("concurrent authorizations=%d, want one", authorized)
	}
}

func TestOutboxRecoveryRejectsInconsistentHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	m, dir, _ := failedAdminFixture(t, server.URL)
	entry := adminEntry(t, m)
	if _, err := m.RetryOutbox(entry.Record, entry.Revision); err != nil {
		t.Fatal(err)
	}
	path, record, _ := readOnlyOutboxRecord(t, dir)
	record.Recovery[0].AfterAttempt = 0
	data, _ := json.Marshal(record)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got := adminEntry(t, m)
	if got.State != "invalid" || got.RetryAllowed {
		t.Fatalf("inconsistent recovery was accepted: %+v", got)
	}
}

// This unchanged-receiver probe also runs against the pre-recovery implementation.
func TestTerminalWebhookFailureRetainsCooldownEvidence(t *testing.T) {
	before := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	dir := t.TempDir()
	wh := outboxTestWebhook(server.URL)
	wh.Retry.MaxRetries = 1
	m := startOutboxManager(t, dir, wh)
	if err := m.Dispatch(Event{ID: "terminal-cooldown", Type: "agent.error"}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool {
		_, record, _ := readOnlyOutboxRecord(t, dir)
		return record.Failed
	})
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	_, record, _ := readOnlyOutboxRecord(t, dir)
	if record.NextRetry.Before(before.Add(59 * time.Second)) {
		t.Fatalf("terminal failure discarded receiver cooldown: next_retry=%v", record.NextRetry)
	}
}

func TestOutboxArchiveReclaimsCapacityAndPreservesExactEvidence(t *testing.T) {
	var received atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if healthy.Load() {
			w.WriteHeader(204)
		} else {
			w.WriteHeader(403)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{OutboxDir: dir, OutboxLimit: 1, WorkerCount: 1})
	if err := m.Register(outboxTestWebhook(server.URL)); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	if err := m.Dispatch(Event{ID: "failed", Type: "agent.error", Message: "preserve exactly"}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return adminEntry(t, m).State == "failed" })
	entry := adminEntry(t, m)
	if !entry.ArchiveAllowed {
		t.Fatal("failed receipt should be archivable")
	}
	path, _, evidence := readOnlyOutboxRecord(t, dir)
	if err := m.Dispatch(Event{ID: "next", Type: "agent.error"}); err == nil {
		t.Fatal("terminal receipt should consume the sole capacity slot")
	}
	archived, err := m.ArchiveOutbox(entry.Record, entry.Revision)
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := os.ReadFile(archived.Path)
	if err != nil || !bytes.Equal(evidence, preserved) {
		t.Fatalf("archive changed evidence: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("active slot was not reclaimed: %v", err)
	}
	for _, name := range []string{archived.Path, filepath.Join(filepath.Dir(archived.Path), "manifest.json")} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("archive file not private: %s %v", name, err)
		}
	}
	repeated, err := m.ArchiveOutbox(entry.Record, entry.Revision)
	if err != nil || repeated != archived {
		t.Fatalf("completed archive not idempotent: %+v %v", repeated, err)
	}
	healthy.Store(true)
	if err := m.Dispatch(Event{ID: "next", Type: "agent.error"}); err != nil {
		t.Fatalf("archive did not restore admission: %v", err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := received.Load(); got != 2 {
		t.Fatalf("archival replayed old work or lost new work: requests=%d", got)
	}
	if _, err := m.RetryOutbox(entry.Record, entry.Revision); err == nil {
		t.Fatal("archived record was automatically eligible for delivery")
	}
}

func TestOutboxArchiveRefusesUnsafeDestinationsWithoutRemovingSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	_, dir, wh := failedAdminFixture(t, server.URL)
	_, fixture, data := readOnlyOutboxRecord(t, dir)
	for _, name := range []string{"pending", "stale", "malformed", "busy", "archive symlink", "archive file", "public archive", "revision symlink", "conflicting copy", "manifest symlink", "corrupt manifest"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			key := outboxKey(fixture.ID)
			source := filepath.Join(dir, key)
			if err := os.WriteFile(source, data, 0600); err != nil {
				t.Fatal(err)
			}
			m := NewManager(ManagerConfig{OutboxDir: dir})
			if err := m.Register(wh); err != nil {
				t.Fatal(err)
			}
			entry := adminEntry(t, m)
			archive := filepath.Join(dir, "archived")
			revisionDir := filepath.Join(archive, entry.Revision)
			switch name {
			case "pending":
				record := fixture
				record.Failed = false
				encoded, _ := json.Marshal(record)
				if err := os.WriteFile(source, encoded, 0600); err != nil {
					t.Fatal(err)
				}
				entry = adminEntry(t, m)
			case "stale":
				entry.Revision = strings.Repeat("0", 64)
			case "malformed":
				if err := os.WriteFile(source, []byte("not JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			case "busy":
				o, err := m.administrativeOutbox()
				if err != nil {
					t.Fatal(err)
				}
				lock, err := o.lockKey(key)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "archive symlink":
				if err := os.Symlink(t.TempDir(), archive); err != nil {
					t.Fatal(err)
				}
			case "archive file":
				if err := os.WriteFile(archive, []byte("do not replace"), 0600); err != nil {
					t.Fatal(err)
				}
			case "public archive":
				if err := os.Mkdir(archive, 0755); err != nil {
					t.Fatal(err)
				}
			case "revision symlink":
				if err := os.Mkdir(archive, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), revisionDir); err != nil {
					t.Fatal(err)
				}
			case "conflicting copy", "manifest symlink", "corrupt manifest":
				if err := os.MkdirAll(revisionDir, 0700); err != nil {
					t.Fatal(err)
				}
				if name == "conflicting copy" {
					// Even a byte-identical independent copy is not the retained
					// source inode; never replace a pre-existing archive entry.
					if err := os.WriteFile(filepath.Join(revisionDir, key), data, 0600); err != nil {
						t.Fatal(err)
					}
				} else if name == "manifest symlink" {
					if err := os.Symlink(source, filepath.Join(revisionDir, "manifest.json")); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(filepath.Join(revisionDir, "manifest.json"), []byte("partial prior evidence"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.ArchiveOutbox(entry.Record, entry.Revision); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			after, err := os.ReadFile(source)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refused archive removed or changed the source: %v", err)
			}
		})
	}
}

func TestOutboxArchiveRecoversInterruptedPublication(t *testing.T) {
	for _, withManifest := range []bool{false, true} {
		t.Run(fmt.Sprint(withManifest), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
			defer server.Close()
			m, dir, _ := failedAdminFixture(t, server.URL)
			entry := adminEntry(t, m)
			source, _, data := readOnlyOutboxRecord(t, dir)
			revisionDir := filepath.Join(dir, "archived", entry.Revision)
			if err := os.MkdirAll(revisionDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(source, filepath.Join(revisionDir, entry.Record)); err != nil {
				t.Fatal(err)
			}
			var prior outboxArchiveManifest
			if withManifest {
				var err error
				prior, err = ensureArchiveManifest(revisionDir, entry.Record, entry.Revision, true)
				if err != nil {
					t.Fatal(err)
				}
			}
			// These are the retained filesystem states on either side of
			// manifest publication, before the active name has been unlinked.
			result, err := m.ArchiveOutbox(entry.Record, entry.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if withManifest && !result.ArchivedAt.Equal(prior.ArchivedAt) {
				t.Fatal("recovery rewrote an existing archive authorization time")
			}
			preserved, err := os.ReadFile(result.Path)
			if err != nil || !bytes.Equal(data, preserved) {
				t.Fatalf("interrupted archive lost bytes: %v", err)
			}
		})
	}
}

func TestOutboxArchiveRetryDoesNotRemoveReadmittedWork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	m, dir, _ := failedAdminFixture(t, server.URL)
	entry := adminEntry(t, m)
	_, record, _ := readOnlyOutboxRecord(t, dir)
	archived, err := m.ArchiveOutbox(entry.Record, entry.Revision)
	if err != nil {
		t.Fatal(err)
	}
	record.Failed, record.Attempt, record.LastAttempt = false, 0, nil
	record.Event.Timestamp = time.Now().UTC()
	if err := m.outbox.enqueue(record); err != nil {
		t.Fatal(err)
	}
	_, _, before := readOnlyOutboxRecord(t, dir)
	if _, err := m.ArchiveOutbox(entry.Record, entry.Revision); err == nil {
		t.Fatal("old archive command removed readmitted work")
	}
	_, _, after := readOnlyOutboxRecord(t, dir)
	if !bytes.Equal(before, after) {
		t.Fatal("newly admitted receipt changed")
	}
	if _, err := os.Stat(archived.Path); err != nil {
		t.Fatal("old archive disappeared")
	}
}

func TestOutboxArchiveRacesRecoveryAcrossProcesses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	m, dir, _ := failedAdminFixture(t, server.URL)
	entry := adminEntry(t, m)
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for _, action := range []string{"retry", "archive"} {
		wg.Add(1)
		go func(action string) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestOutboxRecoveryProcessHelper$")
			cmd.Env = append(os.Environ(), "NTM_OUTBOX_ADMIN_HELPER=1", "NTM_OUTBOX_ADMIN_DIR="+dir,
				"NTM_OUTBOX_ADMIN_URL="+server.URL, "NTM_OUTBOX_ADMIN_KEY="+entry.Record,
				"NTM_OUTBOX_ADMIN_REVISION="+entry.Revision, "NTM_OUTBOX_ADMIN_ACTION="+action)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("%s subprocess failed: %v\n%s", action, err, out)
			}
			results <- string(out)
		}(action)
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for result := range results {
		if strings.Contains(result, "RECOVERY_AUTHORIZED") {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("competing archive/recovery operations succeeded %d times, want 1", succeeded)
	}
	snapshot, err := m.ListOutbox()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "archived", entry.Revision, entry.Record)
	if len(snapshot.Entries) == 0 {
		if _, err := os.Stat(archive); err != nil {
			t.Fatalf("neither live nor archived evidence survived: %v", err)
		}
	} else if len(snapshot.Entries) != 1 || snapshot.Entries[0].Recoveries != 1 || snapshot.Entries[0].State == "failed" {
		t.Fatalf("unexpected surviving recovery state: %+v", snapshot)
	}
}

func TestOutboxArchivePreservesRecoveryHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	m, dir, wh := failedAdminFixture(t, server.URL)
	entry := adminEntry(t, m)
	if _, err := m.RetryOutbox(entry.Record, entry.Revision); err != nil {
		t.Fatal(err)
	}
	next := startOutboxManager(t, dir, wh)
	awaitOutbox(t, func() bool { return adminEntry(t, next).State == "failed" })
	if err := next.Stop(); err != nil {
		t.Fatal(err)
	}
	entry = adminEntry(t, next)
	if entry.Recoveries != 1 || entry.Attempt != 2 {
		t.Fatalf("missing prior recovery: %+v", entry)
	}
	_, _, evidence := readOnlyOutboxRecord(t, dir)
	result, err := next.ArchiveOutbox(entry.Record, entry.Revision)
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := os.ReadFile(result.Path)
	if err != nil || !bytes.Equal(evidence, preserved) {
		t.Fatalf("archive lost recovery audit history: %v", err)
	}
}
