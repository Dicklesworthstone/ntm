package context

import (
	"context"
	"strings"
	"testing"
)

func TestBuildRejectsOversizedMetadata(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := NewContextPackBuilder(nil)
	b.ClearCache()
	t.Cleanup(b.ClearCache)
	pack, err := b.Build(context.Background(), BuildOptions{
		AgentType: "gmi", ProjectDir: t.TempDir(),
		BeadID: strings.Repeat("x", tokenByteBudget(GetTokenBudget("gmi"))+1),
	})
	if err == nil || pack != nil || !strings.Contains(err.Error(), "metadata exceeds") {
		t.Fatalf("oversized metadata was accepted: pack=%v err=%v", pack != nil, err)
	}
	if size, _ := b.CacheStats(); size != 0 {
		t.Fatal("rejected pack entered the cache")
	}
}
