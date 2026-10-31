package serve

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func checkpointDisclosureFixture(t *testing.T, kind string) (*checkpoint.Checkpoint, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cp := &checkpoint.Checkpoint{
		Version: 1, ID: "disclosure-checkpoint", Name: "recovery", SessionName: "disclosure-source",
		WorkingDir: t.TempDir(), CreatedAt: time.Now(), PaneCount: 1,
		Session: checkpoint.SessionState{Panes: []checkpoint.PaneState{{
			Index: 0, AgentType: string(tmux.AgentClaude),
			LaunchSpec: &tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude, Command: "claude --model preserved"},
		}}},
	}
	storage := checkpoint.NewStorage()
	dir := storage.CheckpointDir(cp.SessionName, cp.ID)
	if err := os.MkdirAll(filepath.Join(dir, checkpoint.PanesDir), 0700); err != nil {
		t.Fatal(err)
	}
	artifact := ""
	switch kind {
	case "launch":
		cp.Session.Panes[0].LaunchSpec.Command = "claude --api-key " + wiringFakeSecret
	case "patch":
		cp.Git.PatchFile = checkpoint.GitPatchFile
		artifact = filepath.Join(dir, cp.Git.PatchFile)
		patch := "diff --git a/config b/config\n--- a/config\n+++ b/config\n@@ -1 +1 @@\n-safe\n+" + wiringFakeSecret + "\n"
		if err := os.WriteFile(artifact, []byte(patch), 0600); err != nil {
			t.Fatal(err)
		}
	case "scrollback", "compressed scrollback":
		cp.Session.Panes[0].ScrollbackFile = "panes/pane-0.txt"
		data := []byte("retain useful output\ncredential: " + wiringFakeSecret + "\n")
		if kind == "compressed scrollback" {
			cp.Session.Panes[0].ScrollbackFile += ".gz"
			var encoded bytes.Buffer
			gz := gzip.NewWriter(&encoded)
			if _, err := gz.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			data = encoded.Bytes()
		}
		artifact = filepath.Join(dir, cp.Session.Panes[0].ScrollbackFile)
		if err := os.WriteFile(artifact, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	return cp, artifact
}

func checkpointDisclosureExport(t *testing.T, s *Server, cp *checkpoint.Checkpoint, format string, download, redactScrollback bool) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/sessions/%s/checkpoints/%s/export?format=%s&download=%t&redact_secrets=%t", cp.SessionName, cp.ID, format, download, redactScrollback)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func decodeCheckpointDisclosureArchive(t *testing.T, rec *httptest.ResponseRecorder, download bool) []byte {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", rec.Code, rec.Body)
	}
	if download {
		return append([]byte(nil), rec.Body.Bytes()...)
	}
	var body struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil {
		t.Fatalf("export silently corrupted its archive encoding: %v", err)
	}
	return data
}

func checkpointDisclosureArchiveEntry(t *testing.T, data []byte, format, name string) []byte {
	t.Helper()
	if format == "zip" {
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range archive.File {
			if file.Name != name {
				continue
			}
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			return content
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer gz.Close()
		archive := tar.NewReader(gz)
		for {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if header.Name == name {
				content, err := io.ReadAll(archive)
				if err != nil {
					t.Fatal(err)
				}
				return content
			}
		}
	}
	t.Fatalf("archive omitted entry %s", name)
	return nil
}

func assertCheckpointDisclosureRefusal(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var result APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnprocessableEntity || result.Success || result.ErrorCode != "CHECKPOINT_SENSITIVE_CONTENT" ||
		strings.Contains(rec.Body.String(), wiringFakeSecret) || rec.Header().Get("Content-Disposition") != "" {
		t.Fatalf("unsafe archive was not explicitly refused: status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestCheckpointArchiveDisclosureRejectsLaunchMetadataAcrossWireForms(t *testing.T) {
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		for _, format := range []string{"tar.gz", "zip"} {
			for _, download := range []bool{false, true} {
				for _, explicit := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/download=%t/explicit=%t", mode, format, download, explicit), func(t *testing.T) {
						cp, _ := checkpointDisclosureFixture(t, "launch")
						s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}})
						var logs lockedBuffer
						previous := log.Writer()
						log.SetOutput(&logs)
						defer log.SetOutput(previous)
						assertCheckpointDisclosureRefusal(t, checkpointDisclosureExport(t, s, cp, format, download, explicit))
						stored, err := checkpoint.NewStorage().Load(cp.SessionName, cp.ID)
						if err != nil || stored.Session.Panes[0].LaunchSpec.Command != cp.Session.Panes[0].LaunchSpec.Command {
							t.Fatal("refused export changed the recovery command")
						}
						if strings.Contains(logs.String(), wiringFakeSecret) {
							t.Fatal("archive refusal leaked source contents into logs")
						}
					})
				}
			}
		}
	}
}

func TestCheckpointArchiveDisclosureInspectsPatchAndCompressedScrollback(t *testing.T) {
	for _, kind := range []string{"patch", "scrollback", "compressed scrollback"} {
		for _, format := range []string{"tar.gz", "zip"} {
			t.Run(kind+"/"+format, func(t *testing.T) {
				cp, artifact := checkpointDisclosureFixture(t, kind)
				before, err := os.ReadFile(artifact)
				if err != nil {
					t.Fatal(err)
				}
				s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact}})
				assertCheckpointDisclosureRefusal(t, checkpointDisclosureExport(t, s, cp, format, true, false))
				if kind == "patch" {
					assertCheckpointDisclosureRefusal(t, checkpointDisclosureExport(t, s, cp, format, true, true))
				} else {
					archive := decodeCheckpointDisclosureArchive(t, checkpointDisclosureExport(t, s, cp, format, true, true), true)
					content := checkpointDisclosureArchiveEntry(t, archive, format, cp.Session.Panes[0].ScrollbackFile)
					if kind == "compressed scrollback" {
						gz, err := gzip.NewReader(bytes.NewReader(content))
						if err != nil {
							t.Fatal(err)
						}
						content, err = io.ReadAll(gz)
						gz.Close()
						if err != nil {
							t.Fatal(err)
						}
					}
					if bytes.Contains(content, []byte(wiringFakeSecret)) || !bytes.Contains(content, []byte("retain useful output")) || !bytes.Contains(content, []byte("[REDACTED:")) {
						t.Fatal("explicit scrollback export did not preserve safe content and redact the credential")
					}
				}
				after, err := os.ReadFile(artifact)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("export changed the source artifact")
				}
			})
		}
	}
}

func TestCheckpointArchiveDisclosureRejectsImportBeforeCreationOrOverwrite(t *testing.T) {
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		for _, format := range []string{"tar.gz", "zip"} {
			t.Run(string(mode)+"/"+format, func(t *testing.T) {
				cp, _ := checkpointDisclosureFixture(t, "launch")
				path := filepath.Join(t.TempDir(), "source."+format)
				if _, err := checkpoint.NewStorage().Export(cp.SessionName, cp.ID, path, checkpoint.ExportOptions{Format: checkpoint.ExportFormat(format)}); err != nil {
					t.Fatal(err)
				}
				archive, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}})
				storage := checkpoint.NewStorage()
				old := *cp
				old.SessionName, old.Name = "existing-target", "keep original checkpoint"
				old.Session = checkpoint.SessionState{}
				if err := storage.Save(&old); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(filepath.Join(storage.CheckpointDir(old.SessionName, old.ID), checkpoint.MetadataFile))
				if err != nil {
					t.Fatal(err)
				}
				for _, target := range []string{"new-target", old.SessionName} {
					body := ImportCheckpointRequest{Data: base64.StdEncoding.EncodeToString(archive), AllowOverwrite: true}
					rec := httptest.NewRecorder()
					s.Router().ServeHTTP(rec, wiringJSONRequest(t, http.MethodPost, "/api/v1/sessions/"+target+"/checkpoints/import", body))
					assertCheckpointDisclosureRefusal(t, rec)
				}
				if _, err := os.Stat(storage.CheckpointDir("new-target", cp.ID)); !os.IsNotExist(err) {
					t.Fatal("refused import created a checkpoint")
				}
				after, err := os.ReadFile(filepath.Join(storage.CheckpointDir(old.SessionName, old.ID), checkpoint.MetadataFile))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("refused import replaced the existing checkpoint")
				}
				unchanged, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(archive, unchanged) {
					t.Fatal("refused import changed the submitted archive")
				}
			})
		}
	}
}

func TestCheckpointArchiveDisclosureCleanRoundTripPreservesRecoveryCommand(t *testing.T) {
	for _, format := range []string{"tar.gz", "zip"} {
		for _, download := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/download=%t", format, download), func(t *testing.T) {
				cp, _ := checkpointDisclosureFixture(t, "clean")
				s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeBlock}})
				archive := decodeCheckpointDisclosureArchive(t, checkpointDisclosureExport(t, s, cp, format, download, false), download)
				rec := httptest.NewRecorder()
				s.Router().ServeHTTP(rec, wiringJSONRequest(t, http.MethodPost, "/api/v1/sessions/clean-copy/checkpoints/import", ImportCheckpointRequest{Data: base64.StdEncoding.EncodeToString(archive)}))
				if rec.Code != http.StatusCreated {
					t.Fatalf("clean import status=%d body=%s", rec.Code, rec.Body)
				}
				imported, err := checkpoint.NewStorage().Load("clean-copy", cp.ID)
				if err != nil || imported.Session.Panes[0].LaunchSpec.Command != cp.Session.Panes[0].LaunchSpec.Command {
					t.Fatal("clean archive changed recovery commands")
				}
			})
		}
	}
}

func TestCheckpointImportPublicationOutcomeIsRetained(t *testing.T) {
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock, redaction.ModeOff} {
		for _, published := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/published=%t", mode, published), func(t *testing.T) {
				cp, _ := checkpointDisclosureFixture(t, "clean")
				storage := checkpoint.NewStorage()
				archivePath := filepath.Join(t.TempDir(), "source.zip")
				if _, err := storage.Export(cp.SessionName, cp.ID, archivePath, checkpoint.ExportOptions{Format: checkpoint.FormatZip}); err != nil {
					t.Fatal(err)
				}
				archive, err := os.ReadFile(archivePath)
				if err != nil {
					t.Fatal(err)
				}
				old := *cp
				old.SessionName, old.Name = "publication-target", "original recovery state"
				if err := storage.Save(&old); err != nil {
					t.Fatal(err)
				}
				metadataPath := filepath.Join(storage.CheckpointDir(old.SessionName, old.ID), checkpoint.MetadataFile)
				before, err := os.ReadFile(metadataPath)
				if err != nil {
					t.Fatal(err)
				}
				s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}})
				var logs lockedBuffer
				originalLogOutput := log.Writer()
				log.SetOutput(&logs)
				t.Cleanup(func() { log.SetOutput(originalLogOutput) })
				calls := 0
				importer := func(path string, opts checkpoint.ImportOptions) (*checkpoint.Checkpoint, error) {
					calls++
					var imported *checkpoint.Checkpoint
					if published {
						// Exercise real atomic replacement, then inject only the
						// finalization error that cannot be induced portably over HTTP.
						var err error
						imported, err = storage.Import(path, opts)
						if err != nil {
							t.Fatalf("real import failed: %v", err)
						}
					}
					return imported, fmt.Errorf("wrapped publication failure: %w", &checkpoint.ImportPublicationError{
						Published: published, CheckpointDir: "/private/destination/" + wiringFakeSecret,
						RetainedDir: "/private/retained/" + wiringFakeSecret,
						Cause:       errors.New("private filesystem failure " + wiringFakeSecret),
					})
				}
				router := chi.NewRouter()
				router.Use(s.requestIDMiddlewareFunc, s.redactionMiddleware, s.idempotencyMiddleware)
				router.Post("/api/v1/sessions/{sessionName}/checkpoints/import", func(w http.ResponseWriter, r *http.Request) {
					s.handleImportCheckpointWithImporter(w, r, importer)
				})
				body := ImportCheckpointRequest{Data: base64.StdEncoding.EncodeToString(archive), AllowOverwrite: true}
				do := func(body ImportCheckpointRequest) *httptest.ResponseRecorder {
					req := wiringJSONRequest(t, http.MethodPost, "/api/v1/sessions/"+old.SessionName+"/checkpoints/import", body)
					req.Header.Set("Idempotency-Key", "publication-result")
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)
					return rec
				}
				first := do(body)
				if first.Code != http.StatusInternalServerError {
					t.Fatalf("publication result status=%d body=%s", first.Code, first.Body)
				}
				var result APIError
				if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				wantState, wantCode := "unknown", "CHECKPOINT_IMPORT_PUBLICATION_UNCERTAIN"
				if published {
					wantState, wantCode = "published", "CHECKPOINT_IMPORT_FINALIZATION_FAILED"
				}
				if result.Success || result.ErrorCode != wantCode || result.Details["publication_state"] != wantState ||
					result.Details["published"] != published || result.Details["finalization_complete"] != false ||
					result.Details["inspection_required"] != true || result.Details["retry_safe"] != false ||
					!strings.Contains(result.Hint, "before importing again") {
					t.Fatalf("publication evidence was lost: %+v", result)
				}
				if published {
					reference, ok := result.Details["checkpoint"].(map[string]interface{})
					if !ok || len(reference) != 2 || reference["id"] != cp.ID || reference["session_name"] != old.SessionName {
						t.Fatalf("missing exact published checkpoint reference: %#v", reference)
					}
				} else if _, ok := result.Details["checkpoint"]; ok {
					t.Fatal("uncertain publication invented a confirmed checkpoint reference")
				}
				stored, err := storage.Load(old.SessionName, cp.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantName := old.Name
				if published {
					wantName = cp.Name
				}
				if stored.Name != wantName {
					t.Fatalf("wrong recovery state after reported outcome: got %q, want %q", stored.Name, wantName)
				}
				afterFirst, err := os.ReadFile(metadataPath)
				if err != nil || (bytes.Equal(before, afterFirst) == published) {
					t.Fatal("fixture did not exercise the reported publication state")
				}
				second := do(body)
				if second.Code != first.Code || second.Header().Get("X-Idempotent-Replay") != "true" ||
					!bytes.Equal(second.Body.Bytes(), first.Body.Bytes()) || calls != 1 {
					t.Fatalf("same-key retry re-executed an unsafe import: calls=%d status=%d replay=%q body=%s",
						calls, second.Code, second.Header().Get("X-Idempotent-Replay"), second.Body)
				}
				verify := false
				body.VerifyChecksums = &verify
				mismatch := do(body)
				if mismatch.Code != http.StatusUnprocessableEntity || calls != 1 ||
					!strings.Contains(mismatch.Body.String(), ErrCodeIdempotentReplay) {
					t.Fatalf("retained outcome lost body identity: calls=%d status=%d body=%s", calls, mismatch.Code, mismatch.Body)
				}
				afterReplay, err := os.ReadFile(metadataPath)
				if err != nil || !bytes.Equal(afterFirst, afterReplay) {
					t.Fatal("replay or mismatched request changed published recovery state")
				}
				for _, private := range []string{wiringFakeSecret, "/private/destination/", "/private/retained/", "private filesystem failure"} {
					if strings.Contains(first.Body.String()+second.Body.String()+logs.String(), private) {
						t.Fatal("publication diagnostics disclosed a private path or error cause")
					}
				}
			})
		}
	}
}

func TestCheckpointImportOrdinaryRefusalCanRetry(t *testing.T) {
	cp, _ := checkpointDisclosureFixture(t, "clean")
	storage := checkpoint.NewStorage()
	archivePath := filepath.Join(t.TempDir(), "source.zip")
	if _, err := storage.Export(cp.SessionName, cp.ID, archivePath, checkpoint.ExportOptions{Format: checkpoint.FormatZip}); err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeBlock}})
	calls := 0
	router := chi.NewRouter()
	router.Use(s.redactionMiddleware, s.idempotencyMiddleware)
	router.Post("/api/v1/sessions/{sessionName}/checkpoints/import", func(w http.ResponseWriter, r *http.Request) {
		s.handleImportCheckpointWithImporter(w, r, func(path string, opts checkpoint.ImportOptions) (*checkpoint.Checkpoint, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("ordinary validation failure")
			}
			return storage.Import(path, opts)
		})
	})
	body := ImportCheckpointRequest{Data: base64.StdEncoding.EncodeToString(archive)}
	for i, want := range []int{http.StatusBadRequest, http.StatusCreated, http.StatusCreated} {
		req := wiringJSONRequest(t, http.MethodPost, "/api/v1/sessions/refusal-retry/checkpoints/import", body)
		req.Header.Set("Idempotency-Key", "ordinary-refusal")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want || (rec.Header().Get("X-Idempotent-Replay") == "true") != (i == 2) {
			t.Fatalf("request %d status=%d replay=%q body=%s", i, rec.Code, rec.Header().Get("X-Idempotent-Replay"), rec.Body)
		}
		if i == 0 {
			if _, err := storage.Load("refusal-retry", cp.ID); err == nil {
				t.Fatal("ordinary refusal unexpectedly published a checkpoint")
			}
		}
	}
	if calls != 2 {
		t.Fatalf("ordinary refusal was cached or successful import was replayed by execution: calls=%d", calls)
	}
}

func TestCheckpointImportPublicationWithholdsSensitiveReference(t *testing.T) {
	cp, _ := checkpointDisclosureFixture(t, "clean")
	storage := checkpoint.NewStorage()
	archivePath := filepath.Join(t.TempDir(), "source.zip")
	if _, err := storage.Export(cp.SessionName, cp.ID, archivePath, checkpoint.ExportOptions{Format: checkpoint.FormatZip}); err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	// Even in permissive mode, error diagnostics must not expose a secret or
	// replace a recovery identifier with a plausible but incorrect placeholder.
	s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeOff}})
	router := chi.NewRouter()
	router.Post("/api/v1/sessions/{sessionName}/checkpoints/import", func(w http.ResponseWriter, r *http.Request) {
		s.handleImportCheckpointWithImporter(w, r, func(path string, opts checkpoint.ImportOptions) (*checkpoint.Checkpoint, error) {
			imported, err := storage.Import(path, opts)
			if err != nil {
				t.Fatal(err)
			}
			return imported, &checkpoint.ImportPublicationError{Published: true, Cause: errors.New("sync failure")}
		})
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, wiringJSONRequest(t, http.MethodPost, "/api/v1/sessions/"+wiringFakeSecret+"/checkpoints/import",
		ImportCheckpointRequest{Data: base64.StdEncoding.EncodeToString(archive)}))
	var result APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusInternalServerError || result.Details["checkpoint_reference_withheld"] != true ||
		result.Details["published"] != true || result.Details["checkpoint"] != nil || strings.Contains(rec.Body.String(), wiringFakeSecret) ||
		strings.Contains(rec.Body.String(), "[REDACTED") {
		t.Fatalf("unsafe recovery reference was not explicitly withheld: %s", rec.Body)
	}
	stored, err := storage.Load(wiringFakeSecret, cp.ID)
	if err != nil || stored.SessionName != wiringFakeSecret {
		t.Fatal("safe diagnostic handling changed the published checkpoint identifier")
	}
}

func TestCheckpointArchiveDisclosureNeverSubstitutesInsideBase64(t *testing.T) {
	cp, _ := checkpointDisclosureFixture(t, "clean")
	s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{
		Mode: redaction.ModeRedact, ExtraPatterns: map[redaction.Category][]string{"CUSTOM": {`"data":"[A-Za-z0-9+/=]+"`}},
	}})
	assertCheckpointDisclosureRefusal(t, checkpointDisclosureExport(t, s, cp, "zip", false, false))
	archive := decodeCheckpointDisclosureArchive(t, checkpointDisclosureExport(t, s, cp, "zip", true, false), true)
	if len(checkpointDisclosureArchiveEntry(t, archive, "zip", checkpoint.MetadataFile)) == 0 {
		t.Fatal("binary alternative lost its metadata")
	}
}

func TestCheckpointArchiveDisclosureHonorsPermissiveModesAndExplicitAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  RedactionConfig
	}{
		{"disabled", RedactionConfig{Config: redaction.Config{Mode: redaction.ModeBlock}}},
		{"off", RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeOff}}},
		{"warn", RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeWarn}}},
		{"explicit allowlist", RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeBlock, Allowlist: []string{"^" + wiringFakeSecret + "$"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp, _ := checkpointDisclosureFixture(t, "launch")
			s := newWiringTestServer(t, &tc.cfg)
			archive := decodeCheckpointDisclosureArchive(t, checkpointDisclosureExport(t, s, cp, "zip", true, false), true)
			metadata := checkpointDisclosureArchiveEntry(t, archive, "zip", checkpoint.MetadataFile)
			if !bytes.Contains(metadata, []byte(wiringFakeSecret)) {
				t.Fatal("permissive policy silently changed the recovery command")
			}
			rec := httptest.NewRecorder()
			s.Router().ServeHTTP(rec, wiringJSONRequest(t, http.MethodPost, "/api/v1/sessions/permitted-copy/checkpoints/import", ImportCheckpointRequest{Data: base64.StdEncoding.EncodeToString(archive)}))
			if rec.Code != http.StatusCreated {
				t.Fatalf("permitted import status=%d body=%s", rec.Code, rec.Body)
			}
			imported, err := checkpoint.NewStorage().Load("permitted-copy", cp.ID)
			if err != nil || imported.Session.Panes[0].LaunchSpec.Command != cp.Session.Panes[0].LaunchSpec.Command {
				t.Fatal("permitted import changed recovery values")
			}
		})
	}
}

func restoreCheckpointHTTPFixture(t *testing.T, paneCount int) (*checkpoint.Checkpoint, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	logPath := installFakeTmux(t)
	cp := &checkpoint.Checkpoint{
		Version: 1, ID: "cp-http-restore", Name: "recovery", SessionName: "restore-source",
		WorkingDir: t.TempDir(), CreatedAt: time.Now(), PaneCount: paneCount,
	}
	for i := 0; i < paneCount; i++ {
		cp.Session.Panes = append(cp.Session.Panes, checkpoint.PaneState{Index: i, AgentType: "user"})
	}
	if err := checkpoint.NewStorage().Save(cp); err != nil {
		t.Fatal(err)
	}
	return cp, logPath
}

func checkpointRestoreRequest(ctx context.Context, cp *checkpoint.Checkpoint, body string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", cp.SessionName)
	rctx.URLParams.Add("checkpointId", cp.ID)
	return httptest.NewRequest(http.MethodPost, "/restore", strings.NewReader(body)).WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
}

func TestRestoreCheckpointHTTP_TargetSessionPreservesSource(t *testing.T) {
	cp, logPath := restoreCheckpointHTTPFixture(t, 1)
	req := checkpointRestoreRequest(context.Background(), cp, `{"target_session":"restore-copy","skip_git_check":true}`)
	rec := httptest.NewRecorder()
	(&Server{}).handleRestoreCheckpoint(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["session_name"] != "restore-copy" || result["source_session"] != cp.SessionName || result["panes_restored"] != float64(1) || result["interrupted"] != false {
		t.Fatalf("incorrect restore response: %#v", result)
	}
	stored, err := checkpoint.NewStorage().Load(cp.SessionName, cp.ID)
	if err != nil || stored.SessionName != cp.SessionName {
		t.Fatalf("source checkpoint changed: %#v, %v", stored, err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "new-session -d -s restore-copy") || strings.Contains(string(calls), "kill-session") {
		t.Fatalf("wrong restore target: %s", calls)
	}
}

func TestRestoreCheckpointHTTP_CancellationRetainsPartialProgress(t *testing.T) {
	cp, logPath := restoreCheckpointHTTPFixture(t, 2)
	t.Setenv("NTM_JOB_RESTORE_BLOCK", "split-window")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := checkpointRestoreRequest(ctx, cp, `{"target_session":"restore-copy","skip_git_check":true}`)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Server{}).handleRestoreCheckpoint(rec, req)
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("NTM_JOB_RESTORE_READY")); err == nil {
			break
		}
		select {
		case <-done:
			t.Fatalf("restore returned before blocking: %d %s", rec.Code, rec.Body.String())
		case <-deadline:
			cancel()
			<-done
			t.Fatal("restore did not reach split-window")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled request did not stop restore")
	}
	var result APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusRequestTimeout || result.Success || result.ErrorCode != "CANCELLED" || result.Details["session_name"] != "restore-copy" || result.Details["stage"] != "restoring_layout" || result.Details["panes_restored"] != float64(1) || result.Details["interrupted"] != true {
		t.Fatalf("partial recovery evidence lost: %d %#v", rec.Code, result)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "kill-session") || strings.Contains(string(calls), "respawn-pane") {
		t.Fatalf("cancellation caused further mutations: %s", calls)
	}
}

func TestRestoreCheckpointHTTP_InvalidRequestsDoNotMutate(t *testing.T) {
	cp, logPath := restoreCheckpointHTTPFixture(t, 1)
	for _, body := range []string{
		`{"target_session":"bad:name","force":true}`,
		`{"target_sesion":"restore-copy","force":true}`,
		`{"scrollback_lines":-1,"force":true}`,
		`{"force":true} {"dry_run":true}`,
	} {
		t.Run(body, func(t *testing.T) {
			req := checkpointRestoreRequest(context.Background(), cp, body)
			rec := httptest.NewRecorder()
			(&Server{}).handleRestoreCheckpoint(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if data, err := os.ReadFile(logPath); err == nil && len(data) != 0 {
		t.Fatalf("invalid request reached tmux: %s", data)
	}
}

func TestRollbackResponseToMap(t *testing.T) {

	t.Run("basic fields", func(t *testing.T) {
		r := RollbackResponse{
			CheckpointID:   "cp-123",
			CheckpointName: "before-refactor",
			GitRestored:    true,
			DryRun:         false,
		}

		m := rollbackResponseToMap(r)
		if m["checkpoint_id"] != "cp-123" {
			t.Errorf("checkpoint_id = %v", m["checkpoint_id"])
		}
		if m["checkpoint_name"] != "before-refactor" {
			t.Errorf("checkpoint_name = %v", m["checkpoint_name"])
		}
		if m["git_restored"] != true {
			t.Errorf("git_restored = %v", m["git_restored"])
		}
		if m["dry_run"] != false {
			t.Errorf("dry_run = %v", m["dry_run"])
		}
		if _, ok := m["stash_created"]; ok {
			t.Error("stash_created should not be present when false")
		}
		if _, ok := m["stash_ref"]; ok {
			t.Error("stash_ref should not be present when stash not created")
		}
		if _, ok := m["warnings"]; ok {
			t.Error("warnings should not be present when empty")
		}
	})

	t.Run("with stash", func(t *testing.T) {
		r := RollbackResponse{
			CheckpointID:   "cp-456",
			CheckpointName: "snapshot",
			StashCreated:   true,
			StashRef:       "stash@{0}",
		}

		m := rollbackResponseToMap(r)
		if m["stash_created"] != true {
			t.Errorf("stash_created = %v", m["stash_created"])
		}
		if m["stash_ref"] != "stash@{0}" {
			t.Errorf("stash_ref = %v", m["stash_ref"])
		}
	})

	t.Run("with warnings", func(t *testing.T) {
		r := RollbackResponse{
			CheckpointID:   "cp-789",
			CheckpointName: "pre-deploy",
			Warnings:       []string{"dirty working tree", "untracked files present"},
		}

		m := rollbackResponseToMap(r)
		warnings, ok := m["warnings"].([]string)
		if !ok {
			t.Fatal("warnings should be a string slice")
		}
		if len(warnings) != 2 {
			t.Errorf("warnings count = %d, want 2", len(warnings))
		}
	})

	t.Run("dry run", func(t *testing.T) {
		r := RollbackResponse{
			CheckpointID:   "cp-dry",
			CheckpointName: "test",
			DryRun:         true,
		}

		m := rollbackResponseToMap(r)
		if m["dry_run"] != true {
			t.Errorf("dry_run = %v, want true", m["dry_run"])
		}
	})
}

func TestCheckpointToResponse(t *testing.T) {

	now := time.Now()

	t.Run("basic without details", func(t *testing.T) {
		cp := &checkpoint.Checkpoint{
			ID:          "cp-abc",
			Name:        "initial",
			Description: "first checkpoint",
			SessionName: "dev-session",
			WorkingDir:  "/tmp/project",
			CreatedAt:   now,
			PaneCount:   3,
		}

		resp := checkpointToResponse(cp, false)
		if resp.ID != "cp-abc" {
			t.Errorf("ID = %q", resp.ID)
		}
		if resp.Name != "initial" {
			t.Errorf("Name = %q", resp.Name)
		}
		if resp.Description != "first checkpoint" {
			t.Errorf("Description = %q", resp.Description)
		}
		if resp.SessionName != "dev-session" {
			t.Errorf("SessionName = %q", resp.SessionName)
		}
		if resp.WorkingDir != "/tmp/project" {
			t.Errorf("WorkingDir = %q", resp.WorkingDir)
		}
		if resp.PaneCount != 3 {
			t.Errorf("PaneCount = %d", resp.PaneCount)
		}
		if resp.Git != nil {
			t.Error("Git should be nil when branch is empty")
		}
		if resp.Session != nil {
			t.Error("Session should be nil when includeDetails is false")
		}
	})

	t.Run("with git info", func(t *testing.T) {
		cp := &checkpoint.Checkpoint{
			ID:          "cp-git",
			Name:        "with-git",
			SessionName: "dev",
			CreatedAt:   now,
			Git: checkpoint.GitState{
				Branch:         "main",
				Commit:         "abc123",
				IsDirty:        true,
				StagedCount:    2,
				UnstagedCount:  1,
				UntrackedCount: 3,
				PatchFile:      "diff.patch",
			},
		}

		resp := checkpointToResponse(cp, false)
		if resp.Git == nil {
			t.Fatal("Git should be populated when branch is set")
		}
		if resp.Git.Branch != "main" {
			t.Errorf("Git.Branch = %q", resp.Git.Branch)
		}
		if resp.Git.Commit != "abc123" {
			t.Errorf("Git.Commit = %q", resp.Git.Commit)
		}
		if !resp.Git.IsDirty {
			t.Error("Git.IsDirty should be true")
		}
		if resp.Git.StagedCount != 2 {
			t.Errorf("Git.StagedCount = %d", resp.Git.StagedCount)
		}
		if resp.Git.UnstagedCount != 1 {
			t.Errorf("Git.UnstagedCount = %d", resp.Git.UnstagedCount)
		}
		if resp.Git.UntrackedCount != 3 {
			t.Errorf("Git.UntrackedCount = %d", resp.Git.UntrackedCount)
		}
		if !resp.Git.HasPatch {
			t.Error("Git.HasPatch should be true when PatchFile is set")
		}
	})

	t.Run("with session details", func(t *testing.T) {
		cp := &checkpoint.Checkpoint{
			ID:          "cp-details",
			Name:        "detailed",
			SessionName: "dev",
			CreatedAt:   now,
			PaneCount:   3,
			Session: checkpoint.SessionState{
				Panes: []checkpoint.PaneState{
					{AgentType: "cc"},
					{AgentType: "cod"},
					{AgentType: ""},
				},
				Layout:          "tiled",
				ActivePaneIndex: 1,
			},
		}

		resp := checkpointToResponse(cp, true)
		if resp.Session == nil {
			t.Fatal("Session should be populated when includeDetails is true")
		}
		if resp.Session.PaneCount != 3 {
			t.Errorf("Session.PaneCount = %d", resp.Session.PaneCount)
		}
		if resp.Session.ActivePaneIndex != 1 {
			t.Errorf("Session.ActivePaneIndex = %d", resp.Session.ActivePaneIndex)
		}
		if resp.Session.Layout != "tiled" {
			t.Errorf("Session.Layout = %q", resp.Session.Layout)
		}
		if len(resp.Session.AgentTypes) != 2 {
			t.Errorf("Session.AgentTypes = %v, want [cc cod]", resp.Session.AgentTypes)
		}
	})

	t.Run("metadata only details preserve stored pane count", func(t *testing.T) {
		cp := &checkpoint.Checkpoint{
			ID:          "cp-metadata-only",
			Name:        "metadata-only",
			SessionName: "dev",
			CreatedAt:   now,
			PaneCount:   3,
		}

		resp := checkpointToResponse(cp, true)
		if resp.Session == nil {
			t.Fatal("Session should be populated when includeDetails is true")
		}
		if resp.Session.PaneCount != 3 {
			t.Errorf("Session.PaneCount = %d, want 3", resp.Session.PaneCount)
		}
		if len(resp.Session.AgentTypes) != 0 {
			t.Errorf("Session.AgentTypes = %v, want empty", resp.Session.AgentTypes)
		}
	})

	t.Run("no details skips session", func(t *testing.T) {
		cp := &checkpoint.Checkpoint{
			ID:          "cp-nodetails",
			Name:        "bare",
			SessionName: "dev",
			CreatedAt:   now,
			Session: checkpoint.SessionState{
				Panes: []checkpoint.PaneState{{AgentType: "cc"}},
			},
		}

		resp := checkpointToResponse(cp, false)
		if resp.Session != nil {
			t.Error("Session should be nil when includeDetails is false")
		}
	})
}

// bd-456fv: a checkpoint's WorkingDir is caller-supplied — POST
// /checkpoints/import accepts an arbitrary target_dir and otherwise trusts the
// archive's own metadata.json — and rollback runs `git stash push`, a
// detaching `git checkout`, and `git apply --3way` inside it. Confinement to
// the project directory is what stops a caller holding only sessions:write
// from rewriting an unrelated repository on the host.
func TestResolveCheckpointWorkDirConfinesToProjectDir(t *testing.T) {
	s, _ := setupTestServer(t)
	root := s.projectDirSnapshot()

	inside := filepath.Join(root, "nested", "repo")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("mkdir inside: %v", err)
	}
	outside := t.TempDir()

	t.Run("accepts a directory inside the project", func(t *testing.T) {
		got, err := s.resolveCheckpointWorkDir(inside)
		if err != nil {
			t.Fatalf("resolveCheckpointWorkDir(inside) = %v, want it accepted", err)
		}
		if got == "" {
			t.Fatal("resolved path is empty")
		}
	})

	t.Run("refuses a directory outside the project", func(t *testing.T) {
		if _, err := s.resolveCheckpointWorkDir(outside); err == nil {
			t.Fatalf("resolveCheckpointWorkDir(%q) was accepted; git would run in an unrelated repository", outside)
		}
	})

	t.Run("refuses traversal back out of the project", func(t *testing.T) {
		if _, err := s.resolveCheckpointWorkDir(filepath.Join(root, "..")); err == nil {
			t.Fatal("parent-directory traversal was accepted")
		}
	})

	t.Run("refuses a symlink pointing outside the project", func(t *testing.T) {
		link := filepath.Join(root, "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, err := s.resolveCheckpointWorkDir(link); err == nil {
			t.Fatal("a symlink inside the project pointing outside it was accepted")
		}
	})

	t.Run("refuses a missing directory", func(t *testing.T) {
		if _, err := s.resolveCheckpointWorkDir(filepath.Join(root, "does-not-exist")); err == nil {
			t.Fatal("a nonexistent working directory was accepted")
		}
	})
}

// The commit reaches `git checkout` as a bare argv element and is sliced for
// display, so a non-object-id value is both an argument-injection vector and a
// panic (the slice is on the dry-run path).
func TestCheckpointCommitPatternRejectsNonObjectIDs(t *testing.T) {
	valid := []string{"abc1234", "0123456789abcdef0123456789abcdef01234567"}
	for _, commit := range valid {
		if !checkpointCommitPattern.MatchString(commit) {
			t.Errorf("commit %q rejected, want accepted", commit)
		}
	}
	invalid := []string{"", "abc", "--force", "-B branch", "HEAD", "main", "abc123z", "abc1234 --force"}
	for _, commit := range invalid {
		if checkpointCommitPattern.MatchString(commit) {
			t.Errorf("commit %q accepted, want rejected", commit)
		}
	}
}

// The export handler interpolates sessionName into the Content-Disposition
// header and into a temp filename, but was the one checkpoint handler that
// never validated it. ValidateSessionName's allowlist is what excludes CR/LF.
func TestHandleExportCheckpointValidatesSessionName(t *testing.T) {
	s, _ := setupTestServer(t)

	for _, name := range []string{
		"bad\r\nX-Injected: 1",
		"has space",
		"has/slash",
		"",
	} {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("sessionName", name)
		rctx.URLParams.Add("checkpointId", "cp-1")
		req := httptest.NewRequest("GET", "/api/v1/sessions/x/checkpoints/cp-1/export", nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()

		s.handleExportCheckpoint(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("session name %q: got %d, want 400", name, rec.Code)
		}
		if got := rec.Header().Get("X-Injected"); got != "" {
			t.Errorf("session name %q injected a response header", name)
		}
	}
}
