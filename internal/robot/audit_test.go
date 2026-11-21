package robot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func writeRobotAuditFixture(t *testing.T, dir, session string, count int) string {
	t.Helper()
	fname := fmt.Sprintf("%s-%s.jsonl", session, time.Now().Format("2006-01-02"))
	fpath := filepath.Join(dir, fname)

	f, err := os.Create(fpath)
	if err != nil {
		t.Fatalf("create audit fixture: %v", err)
	}
	defer f.Close()

	evTypes := []audit.EventType{
		audit.EventTypeSpawn,
		audit.EventTypeCommand,
		audit.EventTypeSend,
		audit.EventTypeError,
	}
	actors := []audit.Actor{
		audit.ActorUser,
		audit.ActorAgent,
		audit.ActorSystem,
	}

	var prevHash string
	for i := 0; i < count; i++ {
		entry := audit.AuditEntry{
			Timestamp:   time.Now().UTC().Add(-time.Duration(count-i) * time.Minute),
			SessionID:   session,
			EventType:   evTypes[i%len(evTypes)],
			Actor:       actors[i%len(actors)],
			Target:      fmt.Sprintf("%s__pane_%d", session, i),
			Payload:     map[string]interface{}{"msg": fmt.Sprintf("payload_%d", i)},
			Metadata:    map[string]interface{}{"seq": i + 1},
			PrevHash:    prevHash,
			SequenceNum: uint64(i + 1),
		}
		forHash := entry
		forHash.Checksum = ""
		rawHash, _ := json.Marshal(forHash)
		sum := sha256.Sum256(rawHash)
		entry.Checksum = hex.EncodeToString(sum[:])
		prevHash = entry.Checksum

		rawLine, _ := json.Marshal(entry)
		fmt.Fprintln(f, string(rawLine))
	}
	return fpath
}

func TestAuditListShowSearchVerifyAndPagination(t *testing.T) {
	t.Parallel()
	auditDir := t.TempDir()

	// 1. Empty directory list -> non-nil Logs & Entries
	emptyList, err := GetAudit(context.Background(), AuditOptions{AuditDir: auditDir})
	if err != nil {
		t.Fatalf("GetAudit(empty list): %v", err)
	}
	if !emptyList.Success || emptyList.Action != "list" || emptyList.SchemaID != "ntm:robot:audit:v1" {
		t.Fatalf("unexpected empty list output: %+v", emptyList)
	}
	if emptyList.Logs == nil || emptyList.Entries == nil || len(emptyList.Logs) != 0 || emptyList.Count != 0 {
		t.Fatalf("expected non-nil empty Logs & Entries, got %+v", emptyList)
	}

	// Seed two sessions
	writeRobotAuditFixture(t, auditDir, "alpha", 6)
	writeRobotAuditFixture(t, auditDir, "beta", 3)

	// 2. List with pagination
	listPage1, err := GetAudit(context.Background(), AuditOptions{
		AuditDir: auditDir,
		Limit:    1,
		Offset:   0,
	})
	if err != nil {
		t.Fatalf("GetAudit(list page1): %v", err)
	}
	if !listPage1.Success || listPage1.Count != 1 || listPage1.TotalMatches != 2 || !listPage1.HasMore {
		t.Fatalf("unexpected list page1: %+v", listPage1)
	}
	if listPage1.AgentHints == nil || listPage1.AgentHints.NextOffset == nil || *listPage1.AgentHints.NextOffset != 1 {
		t.Fatalf("expected next_offset=1 on list page1, got %+v", listPage1.AgentHints)
	}
	if listPage1.Logs[0].Session != "alpha" {
		t.Fatalf("expected inferred session 'alpha', got %q", listPage1.Logs[0].Session)
	}

	// 3. Show session entries with pagination + continuation
	showPage1, err := GetAudit(context.Background(), AuditOptions{
		Session:  "alpha",
		AuditDir: auditDir,
		Limit:    4,
		Offset:   0,
	})
	if err != nil {
		t.Fatalf("GetAudit(show page1): %v", err)
	}
	if !showPage1.Success || showPage1.Action != "show" || showPage1.Count != 4 || showPage1.TotalMatches != 6 || !showPage1.HasMore {
		t.Fatalf("unexpected show page1: %+v", showPage1)
	}
	if showPage1.AgentHints == nil || showPage1.AgentHints.NextOffset == nil || *showPage1.AgentHints.NextOffset != 4 {
		t.Fatalf("expected next_offset=4, got %+v", showPage1.AgentHints)
	}

	showPage2, err := GetAudit(context.Background(), AuditOptions{
		Session:  "alpha",
		AuditDir: auditDir,
		Limit:    4,
		Offset:   *showPage1.AgentHints.NextOffset,
	})
	if err != nil {
		t.Fatalf("GetAudit(show page2): %v", err)
	}
	if !showPage2.Success || showPage2.Count != 2 || showPage2.TotalMatches != 6 || showPage2.HasMore {
		t.Fatalf("unexpected show page2: %+v", showPage2)
	}

	// 4. Search across sessions with type & actor filter
	searched, err := GetAudit(context.Background(), AuditOptions{
		Pattern:    "payload_",
		EventTypes: "spawn",
		Actors:     "user",
		Days:       7,
		AuditDir:   auditDir,
	})
	if err != nil {
		t.Fatalf("GetAudit(search): %v", err)
	}
	if !searched.Success || searched.Action != "search" || searched.Count == 0 {
		t.Fatalf("expected search matches, got %+v", searched)
	}
	for _, entry := range searched.Entries {
		if entry.EventType != audit.EventTypeSpawn || entry.Actor != audit.ActorUser {
			t.Fatalf("entry violated type/actor filter: %+v", entry)
		}
	}

	// 5. Verify valid session hash chain
	verifiedOut, err := GetAudit(context.Background(), AuditOptions{
		Session:  "alpha",
		Verify:   true,
		AuditDir: auditDir,
	})
	if err != nil {
		t.Fatalf("GetAudit(verify valid): %v", err)
	}
	if !verifiedOut.Success || verifiedOut.Action != "verify" || verifiedOut.Verified == nil || !*verifiedOut.Verified || len(verifiedOut.Files) != 1 {
		t.Fatalf("expected verified pass, got %+v", verifiedOut)
	}

	// 6. Verify tampered file fails cleanly with file breakdown
	corruptPath := filepath.Join(auditDir, "alpha-2000-01-01.jsonl")
	if err := os.WriteFile(corruptPath, []byte("{tampered-json}\n"), 0o644); err != nil {
		t.Fatalf("write corrupt log: %v", err)
	}
	failedVerify, err := GetAudit(context.Background(), AuditOptions{
		Session:  "alpha",
		Verify:   true,
		AuditDir: auditDir,
	})
	if err != nil {
		t.Fatalf("GetAudit(verify tampered): %v", err)
	}
	if failedVerify.Success || failedVerify.Verified == nil || *failedVerify.Verified || len(failedVerify.Files) != 2 {
		t.Fatalf("expected tamper failure across 2 files, got %+v", failedVerify)
	}
}

func TestAuditSourcesErrorCasesAndSchemaRegistry(t *testing.T) {
	t.Parallel()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		t.Fatalf("store.Migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.UpsertSourceHealth(&state.SourceHealth{
		SourceName:  "tmux",
		Available:   true,
		Healthy:     true,
		LastCheckAt: now,
	}); err != nil {
		t.Fatalf("UpsertSourceHealth(tmux): %v", err)
	}
	if err := store.UpsertSourceHealth(&state.SourceHealth{
		SourceName:          "beads",
		Available:           true,
		Healthy:             false,
		ConsecutiveFailures: 2,
		LastError:           "bv timeout",
		LastCheckAt:         now,
	}); err != nil {
		t.Fatalf("UpsertSourceHealth(beads): %v", err)
	}

	sourcesOut, err := GetAudit(context.Background(), AuditOptions{Sources: true, Store: store})
	if err != nil {
		t.Fatalf("GetAudit(sources): %v", err)
	}
	if !sourcesOut.Success || sourcesOut.Action != "sources" || len(sourcesOut.Sources) != 2 {
		t.Fatalf("unexpected sources output: %+v", sourcesOut)
	}
	if sourcesOut.AgentHints == nil || len(sourcesOut.AgentHints.Warnings) == 0 {
		t.Fatalf("expected degraded source warning in AgentHints, got %+v", sourcesOut.AgentHints)
	}

	// Error cases: path traversal, negative limit, missing verify session, cancelled ctx
	badSlug, _ := GetAudit(context.Background(), AuditOptions{Session: "../escape"})
	if badSlug.Success || badSlug.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for path traversal, got %+v", badSlug)
	}

	negLimit, _ := GetAudit(context.Background(), AuditOptions{Limit: -1})
	if negLimit.Success || negLimit.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for negative limit, got %+v", negLimit)
	}

	missingVerify, _ := GetAudit(context.Background(), AuditOptions{
		Session:  "does-not-exist",
		Verify:   true,
		AuditDir: t.TempDir(),
	})
	if missingVerify.Success || missingVerify.ErrorCode != ErrCodeSessionNotFound {
		t.Fatalf("expected SESSION_NOT_FOUND for missing verify session, got %+v", missingVerify)
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledOut, _ := GetAudit(cancelledCtx, AuditOptions{})
	if cancelledOut.Success || cancelledOut.ErrorCode != ErrCodeTimeout {
		t.Fatalf("expected TIMEOUT for cancelled context, got %+v", cancelledOut)
	}

	// Registry + Schema verification
	surface, ok := GetRobotRegistry().Surface("audit")
	if !ok {
		t.Fatal("expected 'audit' surface in GetRobotRegistry()")
	}
	if surface.SchemaID != "ntm:robot:audit:v1" || surface.SchemaType != "audit" || surface.Flag != "--robot-audit" {
		t.Fatalf("unexpected audit surface descriptor: %+v", surface)
	}
	if surface.Paginated == nil || !*surface.Paginated {
		t.Fatalf("expected audit surface Paginated=true, got %+v", surface.Paginated)
	}

	schemaOut, err := GetSchema("audit")
	if err != nil {
		t.Fatalf("GetSchema(audit): %v", err)
	}
	if !schemaOut.Success || schemaOut.Schema == nil {
		t.Fatalf("expected valid JSON schema for audit, got %+v", schemaOut)
	}
	for _, prop := range []string{"schema_id", "schema_version", "action", "session", "logs", "entries", "count", "total_matches", "has_more"} {
		if _, exists := schemaOut.Schema.Properties[prop]; !exists {
			t.Fatalf("expected property %q in audit schema", prop)
		}
	}
}
