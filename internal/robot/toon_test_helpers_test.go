package robot

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func requireToonBinary(t *testing.T) string {
	t.Helper()

	path, err := toonBinaryPath()
	if err != nil {
		if strings.TrimSpace(os.Getenv("TOON_TRU_BIN")) != "" {
			t.Fatalf("TOON_TRU_BIN env set but invalid: %v", err)
		}
		t.Skipf("toon_rust tru not available: %v", err)
	}

	return path
}

func normalizeJSONPayload(t *testing.T, payload any) any {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}

	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	return out
}

func decodeToJSON(t *testing.T, toon string) []byte {
	t.Helper()

	path := requireToonBinary(t)
	cmd := exec.Command(path, "-d")
	cmd.Stdin = strings.NewReader(toon)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		t.Fatalf("toon_rust decode failed: %s", errMsg)
	}

	return stdout.Bytes()
}

func decodeToValue(t *testing.T, toon string) any {
	t.Helper()

	data := decodeToJSON(t, toon)
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decoded JSON unmarshal: %v", err)
	}

	return out
}

func assertToonRoundTrip(t *testing.T, payload any) {
	t.Helper()

	requireToonBinary(t)
	output, err := toonEncode(payload, "\t")
	if err != nil {
		t.Fatalf("toonEncode: %v", err)
	}

	got := decodeToValue(t, output)
	want := normalizeJSONPayload(t, payload)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TOON round-trip mismatch: got %#v want %#v", got, want)
	}
}

func assertToonDecodesToPayload(t *testing.T, toon string, payload any) {
	t.Helper()

	got := decodeToValue(t, toon)
	want := normalizeJSONPayload(t, payload)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TOON decode mismatch: got %#v want %#v", got, want)
	}
}
