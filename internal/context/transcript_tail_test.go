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
					if message, ok := usage["message"].(map[string]interface{}); ok {
						message["content"] = []map[string]string{{"type": "text", "text": large}}
					} else {
						// Codex token events do not carry assistant content;
						// exercise a large unknown metadata extension instead.
						usage["trace"] = large
					}
				}
				body, err := json.Marshal(usage)
				if err != nil {
					t.Fatal(err)
				}
				tool := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"` + large + `"}]}}`
				if provider == "codex" {
					tool = `{"type":"response_item","payload":{"type":"function_call_output","output":"` + large + `"}}`
				} else if provider == "omp" {
					tool = `{"type":"message","message":{"role":"toolResult","content":[{"type":"text","text":"` + large + `"}]}}`
				}
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

// Compaction records describe a history rewrite, not another billed turn.
// Pre-compaction counts (and projected tokensAfter) are not current native
// occupancy. Only a later provider accounting record restores that evidence.
func TestNativeAccountingHistoryResetBarriers(t *testing.T) {
	at := time.Now().UTC().Add(-time.Minute)
	cases := []struct{ name, provider, reset string }{
		{"Claude compact boundary", "claude", `{"type":"system","subtype":"compact_boundary","compactMetadata":{"preTokens":120}}`},
		{"Codex compacted history", "codex", `{"type":"compacted","payload":{"message":"summary","replacement_history":[]}}`},
		{"Codex rollback", "codex", `{"type":"event_msg","payload":{"type":"thread_rolled_back","num_turns":1}}`},
		{"OMP compaction", "omp", `{"type":"compaction","firstKeptEntryId":"b","tokensBefore":120,"tokensAfter":10,"summary":"summary"}`},
		{"OMP branch summary", "omp", `{"type":"branch_summary","fromId":"b","summary":"another branch"}`},
		{"escaped discriminator", "claude", `{"type":"system","subtype":"compact_\u0062oundary"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(nativeAccountingFixture(t, tc.provider, at))
			prefix := `{"type":"turn_context","payload":{"model":"previous-window-model"}}` + "\n"
			data := prefix + string(before) + "\n" + tc.reset + "\n" + `{"type":"progress"}` + "\n"
			path := nativeAccountingFile(t, data)
			got, err := ReadLatestTranscriptUsage(path)
			if err != nil || got != nil {
				t.Fatalf("pre-reset accounting survived history replacement: %+v, %v", got, err)
			}
			after := nativeAccountingFixture(t, tc.provider, at.Add(time.Second))
			fresh, _ := json.Marshal(after)
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := f.Write(append(fresh, '\n'))
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("append fresh accounting: %v, %v", writeErr, closeErr)
			}
			got, err = ReadLatestTranscriptUsage(path)
			if err != nil || got == nil || got.Tokens != 120 || !got.UpdatedAt.Equal(at.Add(time.Second)) {
				t.Fatalf("post-reset accounting did not recover: %+v, %v", got, err)
			}
			if tc.provider == "codex" && got.Model != "" {
				t.Fatalf("pre-reset model survived history replacement: %+v", got)
			}
		})
	}
}

func TestNativeAccountingMalformedLatestDoesNotReviveEarlier(t *testing.T) {
	at := time.Now().UTC().Add(-time.Minute)
	before, _ := json.Marshal(nativeAccountingFixture(t, "claude", at))
	maxInt := int(^uint(0) >> 1)
	invalid := []string{
		`{"type":"system","subtype":"compact_boundary"`,
		`{"type":"assistant","message":{"usage":{"input_tokens":"unknown"}}}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":-1}}}`,
		fmt.Sprintf(`{"type":"assistant","message":{"usage":{"input_tokens":%d,"output_tokens":1}}}`, maxInt),
		`{"type":"message","message":{"role":"assistant","usage":{"input":100,"output":20,"totalTokens":10}}}`,
		`{"type":"message","message":{"role":"assistant","usage":{"input":100,"cacheRead":-1,"totalTokens":100}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"cached_input_tokens":20,"total_tokens":30}}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":20,"total_tokens":10}}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"total_tokens":100},"model_context_window":-1}}}`,
	}
	for i, line := range invalid {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			data := string(before) + "\n" + line + "\n"
			got, err := ReadLatestTranscriptUsage(nativeAccountingFile(t, data))
			if err != nil || got != nil {
				t.Fatalf("invalid latest evidence retained or invented native usage: %+v, %v", got, err)
			}
			// Corruption is a barrier, not a permanent loss of observability.
			fresh, _ := json.Marshal(nativeAccountingFixture(t, "claude", at.Add(time.Second)))
			got, err = ReadLatestTranscriptUsage(nativeAccountingFile(t, data+string(fresh)+"\n"))
			if err != nil || got == nil || !got.UpdatedAt.Equal(at.Add(time.Second)) {
				t.Fatalf("valid evidence after corruption did not recover: %+v, %v", got, err)
			}
		})
	}
}

func TestNativeAccountingInformationalRecordsPreserveReading(t *testing.T) {
	at := time.Now().UTC().Add(-time.Minute)
	before, _ := json.Marshal(nativeAccountingFixture(t, "claude", at))
	for _, line := range []string{
		`{"type":"message","message":{"role":"assistant","usage":{"input":0,"output":0,"totalTokens":0}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":10}}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":null}}}`,
		`{"type":"tool_result","content":"compacted compact_boundary compaction branch_summary thread_rolled_back"}`,
		`{"type":"progress","message":"valid unrelated string field","payload":"not a native accounting payload"}`,
		`{"type":"response_item","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1,"total_tokens":1}}}}`,
	} {
		got, err := ReadLatestTranscriptUsage(nativeAccountingFile(t, string(before)+"\n"+line+"\n"))
		if err != nil || got == nil || got.Tokens != 120 || !got.UpdatedAt.Equal(at) {
			t.Fatalf("informational record changed accounting: record=%s reading=%+v err=%v", line, got, err)
		}
	}
}

func TestNativeAccountingResetStopsLookback(t *testing.T) {
	data := strings.Repeat(" ", 4*transcriptTailWindow) + "\n" +
		`{"type":"compacted","payload":{"replacement_history":[]}}` + "\n"
	trace := &nativeAccountingReadTrace{ReaderAt: strings.NewReader(data)}
	got, err := readNativeTranscriptTail(trace, int64(len(data)))
	if err != nil || got != nil || trace.bytes != transcriptTailWindow+1 {
		t.Fatalf("lookback crossed a known invalidation barrier: %+v err=%v bytes=%d", got, err, trace.bytes)
	}
}

func TestNativeAccountingConfidenceRejectsFutureTime(t *testing.T) {
	now := time.Now()
	if got := TranscriptConfidence(now.Add(time.Second), now); got != "medium" {
		t.Fatalf("future observation confidence = %s", got)
	}
}

func TestNativeAccountingPreservesAdditionalNativeTokens(t *testing.T) {
	at := time.Now().UTC().Add(-time.Minute)
	for _, provider := range []string{"codex", "omp"} {
		t.Run(provider, func(t *testing.T) {
			body := nativeAccountingFixture(t, provider, at)
			if provider == "codex" {
				payload := body["payload"].(map[string]interface{})
				info := payload["info"].(map[string]interface{})
				info["last_token_usage"].(map[string]interface{})["total_tokens"] = 150
			} else {
				message := body["message"].(map[string]interface{})
				message["usage"].(map[string]interface{})["totalTokens"] = 150
			}
			data, _ := json.Marshal(body)
			got, err := ReadLatestTranscriptUsage(nativeAccountingFile(t, string(data)+"\n"))
			if err != nil || got == nil || got.Tokens != 150 {
				t.Fatalf("native total was replaced with an incomplete component sum: %+v, %v", got, err)
			}
		})
	}
}
