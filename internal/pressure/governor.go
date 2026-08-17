package pressure

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Mode controls whether the governor enforces decisions or only records
// them for observation.
type Mode string

const (
	ModeObserve Mode = "observe"
	ModeEnforce Mode = "enforce"
)

// Config configures a Governor.
type Config struct {
	Mode          Mode
	Thresholds    map[Source]Thresholds
	GlobalBudget  Budget
	SessionBudget map[string]Budget
	Providers     []Provider
	Logger        *slog.Logger
	Now           func() time.Time
}

// Governor coordinates providers and snapshot computation.
type Governor struct {
	mode       Mode
	thresholds map[Source]Thresholds
	global     Budget
	sessions   map[string]Budget
	providers  []Provider
	logger     *slog.Logger
	now        func() time.Time

	mu   sync.RWMutex
	last *Snapshot
}

// New returns a Governor seeded with cfg. Missing fields fall back to
// safe defaults so callers can pass `Config{}` for an observe-only
// scaffold.
func New(cfg Config) *Governor {
	mode := cfg.Mode
	if mode != ModeEnforce {
		mode = ModeObserve
	}
	thresholds := cfg.Thresholds
	if thresholds == nil {
		thresholds = DefaultThresholds()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	global := cfg.GlobalBudget
	if global == (Budget{}) {
		global = DefaultBudget()
	}
	return &Governor{
		mode:       mode,
		thresholds: thresholds,
		global:     global,
		sessions:   cfg.SessionBudget,
		providers:  cfg.Providers,
		logger:     logger,
		now:        now,
	}
}

// Mode returns the governor's mode. Read under RLock so concurrent
// writers observe the Go memory-model happens-before edge established
// by the matching write under g.mu.Lock (bd-qjt3s).
func (g *Governor) Mode() Mode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.mode
}

// Refresh polls all providers, builds a fresh Snapshot, stores it as
// `Latest`, and returns it. Provider errors are logged but do not
// prevent the snapshot from being built from the surviving readings.
func (g *Governor) Refresh(ctx context.Context) Snapshot {
	var readings []Reading
	for _, p := range g.providers {
		rs, err := p.Read(ctx)
		if err != nil {
			g.logger.Warn("pressure provider read failed",
				slog.String("provider", p.Name()),
				slog.String("err", err.Error()))
			continue
		}
		readings = append(readings, rs...)
	}
	snap := buildSnapshot(g.now(), readings, g.thresholds)
	g.mu.Lock()
	g.last = &snap
	g.mu.Unlock()
	return snap
}

// Latest returns the last Snapshot, or an empty zero-value Snapshot if
// Refresh has never been called.
func (g *Governor) Latest() Snapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.last == nil {
		return Snapshot{TakenAt: g.now()}
	}
	return *g.last
}

// recommendation picks a hint string based on the action and the
// limiting pressure sources. Hints are stable strings so robot output
// can match on them.
func recommendation(action Action, limiting []Source) string {
	if len(limiting) == 0 {
		return ""
	}
	srcs := make([]string, len(limiting))
	for i, s := range limiting {
		srcs[i] = string(s)
	}
	sort.Strings(srcs)
	switch action {
	case ActionBuildOrTest:
		return "offload to rch: " + joinShort(srcs)
	case ActionPipelineFanout:
		return "reduce parallelism: " + joinShort(srcs)
	case ActionSwarmSpawn:
		return "reduce spawn size or wait for headroom: " + joinShort(srcs)
	case ActionAgentSend, ActionAgentInterrupt:
		return "wait for headroom: " + joinShort(srcs)
	case ActionScannerScan:
		return "lengthen scanner interval: " + joinShort(srcs)
	default:
		return joinShort(srcs)
	}
}

func joinShort(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	default:
		out := s[0]
		for _, v := range s[1:] {
			out += "," + v
		}
		return out
	}
}

func limitingStrings(in []Source) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}
