package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLocalPerfTracker_TokensPerSecond(t *testing.T) {
	t.Parallel()

	tr := newLocalPerfTracker(10 * time.Second)
	now := time.Unix(1000, 0)

	// 50 tokens over ~2 seconds => ~25 tok/s (within some tolerance).
	tr.addOutputDelta(now.Add(1*time.Second), 20)
	tr.addOutputDelta(now.Add(3*time.Second), 30)

	tps, total, _, _ := tr.snapshot()
	if total != 50 {
		t.Fatalf("total=%d, want 50", total)
	}
	if tps <= 10 || tps >= 60 {
		t.Fatalf("tps=%f, want a reasonable value between 10 and 60", tps)
	}
}

func TestLocalPerfTracker_FirstTokenLatency(t *testing.T) {
	t.Parallel()

	tr := newLocalPerfTracker(10 * time.Second)
	sendAt := time.Unix(2000, 0)
	tr.addPrompt(sendAt)

	tr.addOutputDelta(sendAt.Add(1500*time.Millisecond), 1)
	_, _, last, avg := tr.snapshot()

	if last < 1400*time.Millisecond || last > 1600*time.Millisecond {
		t.Fatalf("last=%s, want ~1.5s", last)
	}
	if avg != last {
		t.Fatalf("avg=%s, want %s", avg, last)
	}
}

func TestFetchOllamaPS(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "models": [
    {"name":"codellama:latest","size":123,"size_vram":456},
    {"name":"cpu-model:1b","size":789,"size_vram":0}
  ]
}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mem, err := fetchOllamaPS(ctx, srv.URL)
	if err != nil {
		t.Fatalf("fetchOllamaPS err=%v", err)
	}
	if mem["codellama:latest"] != 456 {
		t.Fatalf("codellama mem=%d, want 456", mem["codellama:latest"])
	}
	if mem["cpu-model:1b"] != 789 {
		t.Fatalf("cpu-model mem=%d, want 789", mem["cpu-model:1b"])
	}
}
