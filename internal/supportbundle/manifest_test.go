package supportbundle

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestSHA256Hex(t *testing.T) {
	got := SHA256Hex([]byte("abc"))
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("SHA256Hex mismatch: got %q want %q", got, want)
	}
}

func TestSummarizeRedactionFindings(t *testing.T) {
	findings := []redaction.Finding{
		{Category: redaction.CategoryOpenAIKey},
		{Category: redaction.CategoryOpenAIKey},
		{Category: redaction.CategoryAWSAccessKey},
	}

	sum := SummarizeRedactionFindings(findings)
	if sum.Total != 3 {
		t.Fatalf("Total = %d, want 3", sum.Total)
	}
	if sum.ByCategory[redaction.CategoryOpenAIKey] != 2 {
		t.Fatalf("OPENAI_KEY = %d, want 2", sum.ByCategory[redaction.CategoryOpenAIKey])
	}
	if sum.ByCategory[redaction.CategoryAWSAccessKey] != 1 {
		t.Fatalf("AWS_ACCESS_KEY = %d, want 1", sum.ByCategory[redaction.CategoryAWSAccessKey])
	}
}

func TestManifestJSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 2, 2, 2, 3, 4, 0, time.UTC)
	m := Manifest{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   ts,
		NTMVersion:    "test-version",
		Host: Host{
			OS:   "linux",
			Arch: "amd64",
		},
		Files: []File{
			NewFile("config/config.toml", []byte("password=hunter2"), &RedactionSummary{
				Total: 1,
				ByCategory: map[redaction.Category]int{
					redaction.CategoryPassword: 1,
				},
			}),
		},
	}

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got Manifest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.SchemaVersion != m.SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", got.SchemaVersion, m.SchemaVersion)
	}
	if !got.GeneratedAt.Equal(m.GeneratedAt) {
		t.Fatalf("GeneratedAt = %s, want %s", got.GeneratedAt, m.GeneratedAt)
	}
	if got.NTMVersion != m.NTMVersion {
		t.Fatalf("NTMVersion = %q, want %q", got.NTMVersion, m.NTMVersion)
	}
	if got.Host != m.Host {
		t.Fatalf("Host = %#v, want %#v", got.Host, m.Host)
	}
	if len(got.Files) != 1 {
		t.Fatalf("Files len = %d, want 1", len(got.Files))
	}
	if got.Files[0].Path != "config/config.toml" {
		t.Fatalf("Files[0].Path = %q, want %q", got.Files[0].Path, "config/config.toml")
	}
	if got.Files[0].SizeBytes != int64(len("password=hunter2")) {
		t.Fatalf("Files[0].SizeBytes = %d, want %d", got.Files[0].SizeBytes, len("password=hunter2"))
	}
	if got.Files[0].RedactionSummary == nil || got.Files[0].RedactionSummary.Total != 1 {
		t.Fatalf("RedactionSummary = %#v, want Total=1", got.Files[0].RedactionSummary)
	}
}
