package summary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type lifecycleSummarizer func(context.Context, string, int) (string, error)

func (s lifecycleSummarizer) Summarize(ctx context.Context, prompt string, maxTokens int) (string, error) {
	return s(ctx, prompt, maxTokens)
}

func TestSummaryLifecycleRejectsLateProviderResults(t *testing.T) {
	for _, providerErr := range []error{nil, errors.New("provider failed"), context.Canceled} {
		t.Run(fmt.Sprint(providerErr), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, err := SummarizeSession(ctx, Options{
				Outputs: []AgentOutput{{Output: "Implemented requested work"}},
				Summarizer: lifecycleSummarizer(func(ctx context.Context, _ string, _ int) (string, error) {
					cancel()
					return "late success", providerErr
				}),
			})
			if s != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("stopped owner published summary=%+v error=%v", s, err)
			}
		})
	}
}

func TestSummaryLifecycleDeadlineIsNotFallbackSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	s, err := SummarizeSession(ctx, Options{
		Outputs: []AgentOutput{{Output: "Implemented requested work"}},
		Summarizer: lifecycleSummarizer(func(ctx context.Context, _ string, _ int) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}),
	})
	if s != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline was downgraded to fallback: summary=%+v error=%v", s, err)
	}
}

func TestSummaryLifecycleProviderFailureStillDegradesForLiveOwner(t *testing.T) {
	for _, providerErr := range []error{errors.New("offline"), context.Canceled, context.DeadlineExceeded} {
		s, err := SummarizeSession(context.Background(), Options{
			Outputs: []AgentOutput{{Output: "Implemented requested work"}},
			Summarizer: lifecycleSummarizer(func(context.Context, string, int) (string, error) {
				return "", providerErr
			}),
		})
		if err != nil || s == nil || !strings.Contains(s.Text, "Implemented requested work") {
			t.Fatalf("optional provider failure incorrectly stopped live owner: %+v %v", s, err)
		}
	}
}

func TestSummaryLifecyclePreCancelledDoesNotCallProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	s, err := SummarizeSession(ctx, Options{
		Outputs: []AgentOutput{{Output: "Implemented requested work"}},
		Summarizer: lifecycleSummarizer(func(context.Context, string, int) (string, error) {
			calls++
			return "unexpected", nil
		}),
	})
	if calls != 0 || s != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled summary started provider: calls=%d summary=%+v error=%v", calls, s, err)
	}
}

func TestSummaryLifecycleConcurrentOwnersAreIndependent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := fmt.Sprintf("owner-%d", i)
			s, err := SummarizeSession(ctx, Options{
				Outputs: []AgentOutput{{Output: "Implemented requested work"}},
				Summarizer: lifecycleSummarizer(func(context.Context, string, int) (string, error) {
					if i%2 == 0 {
						cancel()
					}
					return want, nil
				}),
			})
			if i%2 == 0 {
				if s != nil || !errors.Is(err, context.Canceled) {
					t.Errorf("cancelled owner %d published result: %+v %v", i, s, err)
				}
			} else if err != nil || s == nil || s.Text != want {
				t.Errorf("live owner %d affected by another cancellation: %+v %v", i, s, err)
			}
		}(i)
	}
	wg.Wait()
}
