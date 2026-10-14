package context

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the public native reader with real files. Tool output and assistant
// content can each be larger than the fast tail window without ending a turn.
func TestNativeAccountingBeyondFastTail(t *testing.T) {
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	large := strings.Repeat("x", 3*transcriptTailWindow)
	for _, provider := range []string{"claude", "codex", "omp"} {
		for _, placement := range []string{"tool-after", "usage-content", "model-before"} {
			t.Run(provider+"/"+placement, func(t *testing.T) {
				usage := nativeAccountingFixture(t, provider, at)
				head := `{"type":"turn_context","payload":{"model":"original-model"}}`
				if placement == "usage-content" {
					usage["content"] = large
				}
				body, err := json.Marshal(usage)
				if err != nil {
					t.Fatal(err)
				}
				tool := `{"type":"tool_result","content":"` + large + `"}`
				lines := []string{head, string(body)}
				if placement == "tool-after" {
					lines = append(lines, tool)
				} else if placement == "model-before" {
					lines = []string{head, tool, string(body)}
				}
				path := nativeAccountingFile(t, strings.Join(lines, "\n")+"\n")
				got, err := ReadLatestTranscriptUsage(path)
				if err != nil || got == nil {
					t.Fatalf("lost native %s accounting behind %s: reading=%+v err=%v", provider, placement, got, err)
				}
				if got.Tokens != 120 || got.Model != "original-model" || got.Path != path {
					t.Fatalf("wrong accounting: %+v", got)
				}
			})
		}
	}
}

func TestNativeAccountingDoesNotBorrowFutureModel(t *testing.T) {
	usage, _ := json.Marshal(nativeAccountingFixture(t, "codex", time.Now().UTC()))
	// No model before this reading. A later turn's model does not describe it.
	path := nativeAccountingFile(t, string(usage)+"\n"+`{"type":"turn_context","payload":{"model":"future-model"}}`+"\n")
	got, err := ReadLatestTranscriptUsage(path)
	if err != nil || got == nil || got.Tokens != 120 || got.Model != "" {
		t.Fatalf("a future model was attributed to an earlier reading: %+v, %v", got, err)
	}
}

func TestNativeAccountingUsesObservationTime(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, provider := range []string{"claude", "codex", "omp"} {
		t.Run(provider, func(t *testing.T) {
			body, _ := json.Marshal(nativeAccountingFixture(t, provider, at))
			path := nativeAccountingFile(t, string(body)+"\n"+`{"type":"progress","timestamp":"2026-10-14T16:00:00Z"}`+"\n")
			got, err := ReadLatestTranscriptUsage(path)
			if err != nil || got == nil || !got.UpdatedAt.Equal(at) {
				t.Fatalf("a later filesystem/metadata write refreshed old accounting: %+v, %v", got, err)
			}
			if TranscriptConfidence(got.UpdatedAt, at.Add(time.Hour)) != "medium" {
				t.Fatal("old usage was presented as high-confidence")
			}
		})
	}
}

func TestNativeAccountingMissingOrInvalidTimeStaysUnknown(t *testing.T) {
	for _, timestamp := range []string{"", "not-a-time"} {
		t.Run(fmt.Sprintf("timestamp=%q", timestamp), func(t *testing.T) {
			body := nativeAccountingFixture(t, "claude", time.Now())
			body["timestamp"] = timestamp
			data, _ := json.Marshal(body)
			got, err := ReadLatestTranscriptUsage(nativeAccountingFile(t, string(data)+"\n"))
			if err != nil || got == nil || !got.UpdatedAt.IsZero() {
				t.Fatalf("unknown observation time must not become file mtime: %+v, %v", got, err)
			}
		})
	}
}

func TestNativeAccountingTimeCannotExceedSnapshot(t *testing.T) {
	body, _ := json.Marshal(nativeAccountingFixture(t, "claude", time.Now().Add(24*time.Hour)))
	path := nativeAccountingFile(t, string(body)+"\n")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadLatestTranscriptUsage(path)
	if err != nil || got == nil || !got.UpdatedAt.Equal(info.ModTime()) {
		t.Fatalf("future accounting was not capped by its snapshot: %+v, %v", got, err)
	}
}

func TestNativeAccountingRecoversThroughWorkspaceSurface(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := filepath.Join(home, "project")
	dir := filepath.Join(home, ".claude", "projects", MungeProjectPath(cwd))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(nativeAccountingFixture(t, "claude", time.Now().UTC()))
	path := filepath.Join(dir, "session.jsonl")
	data := string(body) + "\n" + `{"type":"tool_result","content":"` + strings.Repeat("x", 2*transcriptTailWindow) + `"}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got, ok := LatestAgentTranscriptUsage("claude", cwd, time.Time{})
	if !ok || got == nil || got.Tokens != 120 || got.Path != path {
		t.Fatalf("coordinator workspace accounting surface lost a live native reading: %+v, %v", got, ok)
	}
}

func TestNativeAccountingPreservesExactTailBoundary(t *testing.T) {
	usage, _ := json.Marshal(nativeAccountingFixture(t, "claude", time.Now().UTC()))
	record := string(usage) + "\n"
	// The accounting starts exactly at the nominal tail boundary. Its
	// delimiter is one byte before that boundary, not inside the record.
	trailing := "{}\n" + strings.Repeat(" ", transcriptTailWindow-len(record)-4) + "\n"
	path := nativeAccountingFile(t, "{}\n"+record+trailing)
	got, err := ReadLatestTranscriptUsage(path)
	if err != nil || got == nil || got.Tokens != 120 {
		t.Fatalf("complete boundary record discarded: %+v, %v", got, err)
	}
}

func TestNativeAccountingIgnoresNonAccountingModelFields(t *testing.T) {
	usage, _ := json.Marshal(nativeAccountingFixture(t, "codex", time.Now().UTC()))
	data := `{"type":"turn_context","payload":{"model":"original-model"}}` + "\n" +
		`{"type":"tool_result","message":{"model":"tool-model"},"payload":{"model":"payload-model"}}` + "\n" +
		string(usage) + "\n"
	got, err := ReadLatestTranscriptUsage(nativeAccountingFile(t, data))
	if err != nil || got == nil || got.Model != "original-model" {
		t.Fatalf("non-accounting fields changed the model: %+v, %v", got, err)
	}
}

// Large sparse fixtures prove that scan work depends on the bounded tail, not
// on the size of the conversation. The wrapper records actual file reads.
func TestNativeAccountingSnapshotReadBounds(t *testing.T) {
	for _, hasUsage := range []bool{true, false} {
		t.Run(fmt.Sprintf("usage=%v", hasUsage), func(t *testing.T) {
			file, err := os.Create(filepath.Join(t.TempDir(), "sparse.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			const size = int64(1024 * 1024 * 1024)
			if err := file.Truncate(size); err != nil {
				t.Fatal(err)
			}
			tail := []byte("\n{}\n")
			if hasUsage {
				record, _ := json.Marshal(nativeAccountingFixture(t, "claude", time.Now().UTC()))
				tail = append([]byte{'\n'}, record...)
				tail = append(tail, '\n')
			}
			if _, err := file.WriteAt(tail, size-int64(len(tail))); err != nil {
				t.Fatal(err)
			}
			trace := &nativeAccountingReadTrace{ReaderAt: file}
			got, err := readNativeTranscriptTail(trace, size)
			if err != nil || (got != nil) != hasUsage {
				t.Fatalf("snapshot reading: %+v, %v", got, err)
			}
			wantBytes := int64(transcriptScanLimit + 1)
			if hasUsage {
				wantBytes = transcriptTailWindow + 1
			}
			if trace.bytes != wantBytes {
				t.Fatalf("read %d bytes, want exactly %d from a %d-byte file", trace.bytes, wantBytes, size)
			}
			for i, span := range trace.spans {
				if span[0] < size-wantBytes || span[1] > size {
					t.Fatalf("read outside snapshot budget: %v", span)
				}
				for _, previous := range trace.spans[:i] {
					if span[0] < previous[1] && previous[0] < span[1] {
						t.Fatalf("expansion reread prior bytes: %v and %v", previous, span)
					}
				}
			}
		})
	}
}

func TestNativeAccountingRefusesIncompleteSnapshot(t *testing.T) {
	data, _ := json.Marshal(nativeAccountingFixture(t, "claude", time.Now().UTC()))
	// A truncation between stat and read must not return a partial snapshot.
	got, err := readNativeTranscriptTail(strings.NewReader(string(data)), int64(len(data)+100))
	if err == nil || got != nil {
		t.Fatalf("truncated snapshot returned accounting: %+v, %v", got, err)
	}
}

type nativeAccountingReadTrace struct {
	io.ReaderAt
	bytes int64
	spans [][2]int64
}

func (r *nativeAccountingReadTrace) ReadAt(p []byte, offset int64) (int, error) {
	n, err := r.ReaderAt.ReadAt(p, offset)
	r.bytes += int64(n)
	r.spans = append(r.spans, [2]int64{offset, offset + int64(n)})
	return n, err
}

func nativeAccountingFixture(t *testing.T, provider string, at time.Time) map[string]interface{} {
	t.Helper()
	var line string
	switch provider {
	case "claude":
		line = `{"type":"assistant","message":{"role":"assistant","model":"original-model","usage":{"input_tokens":30,"cache_read_input_tokens":60,"cache_creation_input_tokens":10,"output_tokens":20}}}`
	case "codex":
		line = `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":60,"output_tokens":20,"total_tokens":120},"model_context_window":200000}}}`
	case "omp":
		line = `{"type":"message","message":{"role":"assistant","model":"original-model","usage":{"input":30,"cacheRead":60,"cacheWrite":10,"output":20,"totalTokens":120}}}`
	default:
		t.Fatalf("unsupported fixture provider %q", provider)
	}
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(line), &body); err != nil {
		t.Fatal(err)
	}
	body["timestamp"] = at.Format(time.RFC3339Nano)
	return body
}

func nativeAccountingFile(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "native.jsonl")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
