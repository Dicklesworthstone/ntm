package ensemble

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// EarlyStopConfig controls anytime-valid sequential novelty stopping for ensembles.
type EarlyStopConfig struct {
	Enabled             bool    `json:"enabled" toml:"enabled" yaml:"enabled"`
	MinAgentsBeforeStop int     `json:"min_agents_before_stop" toml:"min_agents_before_stop" yaml:"min_agents_before_stop"`
	FindingsThreshold   float64 `json:"findings_threshold" toml:"findings_threshold" yaml:"findings_threshold"`
	SimilarityThreshold float64 `json:"similarity_threshold" toml:"similarity_threshold" yaml:"similarity_threshold"`
	WindowSize          int     `json:"window_size" toml:"window_size" yaml:"window_size"`
}

// EarlyStopConfigFromAppConfig translates config.EnsembleEarlyStopConfig into
// ensemble.EarlyStopConfig, reading every leaf knob directly.
func EarlyStopConfigFromAppConfig(cfg config.EnsembleEarlyStopConfig) EarlyStopConfig {
	return EarlyStopConfig{
		Enabled:             cfg.Enabled,
		MinAgentsBeforeStop: cfg.MinAgents,
		FindingsThreshold:   cfg.FindingsThreshold,
		SimilarityThreshold: cfg.SimilarityThreshold,
		WindowSize:          cfg.WindowSize,
	}
}

const (
	defaultEarlyStopAlpha         = 0.05 // Ville anytime-valid Type-I bound (1/alpha = 20.0)
	defaultEarlyStopNullNovelProb = 0.65 // H0: P(window exhibits novel signal) >= 0.65
	defaultEarlyStopAltNovelProb  = 0.05 // H1: saturated clone novel rate <= 0.05
	defaultEarlyStopMinWindows    = 2
	defaultEarlyStopWindowTokens  = 256
)

var fileLineCitationPattern = regexp.MustCompile(`([A-Za-z0-9_./-]+\.[A-Za-z0-9]+:\d+)`)

// EarlyStopDecision captures the outcome and Graveyard §0.19 EvidenceLedger of
// one sequential early-stop evaluation window.
type EarlyStopDecision struct {
	ShouldStop            bool                  `json:"should_stop"`
	Action                string                `json:"action"` // "continue" | "stop"
	ModeID                string                `json:"mode_id"`
	EValue                float64               `json:"e_value"`
	Threshold             float64               `json:"threshold"`
	MarginalNovelty       float64               `json:"marginal_novelty"`
	MaxSimilarity         float64               `json:"max_similarity"`
	NewCitations          int                   `json:"new_citations"`
	ConsecutiveLowWindows int                   `json:"consecutive_low_windows"`
	CompletedModes        int                   `json:"completed_modes"`
	FallbackActive        bool                  `json:"fallback_active"`
	FallbackReason        string                `json:"fallback_reason,omitempty"`
	Ledger                *state.EvidenceLedger `json:"evidence_ledger"`
}

type modeSequentialState struct {
	eValue                float64
	consecutiveLowWindows int
	lastEvaluatedTokens   int
	decisionSeq           int
}

type completedModeCorpus struct {
	modeID   string
	shingles map[string]struct{}
}

// EarlyStopController implements an anytime-valid sequential e-process / SPRT
// novelty controller (Graveyard §0.18) with conservative fallback and §0.19
// EvidenceLedger audit persistence.
type EarlyStopController struct {
	mu             sync.Mutex
	sessionName    string
	cfg            EarlyStopConfig
	alpha          float64
	completed      []completedModeCorpus
	unionShingles  map[string]struct{}
	unionCitations map[string]struct{}
	modes          map[string]*modeSequentialState
}

// NewEarlyStopController creates a sequential novelty controller from EarlyStopConfig.
func NewEarlyStopController(sessionName string, cfg EarlyStopConfig) *EarlyStopController {
	return &EarlyStopController{
		sessionName:    sessionName,
		cfg:            normalizeEarlyStopConfig(cfg),
		alpha:          defaultEarlyStopAlpha,
		unionShingles:  make(map[string]struct{}),
		unionCitations: make(map[string]struct{}),
		modes:          make(map[string]*modeSequentialState),
	}
}

func normalizeEarlyStopConfig(cfg EarlyStopConfig) EarlyStopConfig {
	if cfg.MinAgentsBeforeStop <= 0 {
		cfg.MinAgentsBeforeStop = 2
	}
	if cfg.FindingsThreshold <= 0 || cfg.FindingsThreshold > 1 {
		cfg.FindingsThreshold = 0.20
	}
	if cfg.SimilarityThreshold <= 0 || cfg.SimilarityThreshold > 1 {
		cfg.SimilarityThreshold = 0.80
	}
	if cfg.WindowSize <= 0 {
		cfg.WindowSize = defaultEarlyStopWindowTokens
	}
	return cfg
}

// RecordCompletedMode adds a finished mode's output to the reference baseline
// corpus used to measure marginal novelty of subsequent streaming modes.
func (c *EarlyStopController) RecordCompletedMode(modeID, text string) {
	if c == nil {
		return
	}
	shingles := extractShingles(text, 3)
	citations := extractFileLineCitations(text)

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, existing := range c.completed {
		if existing.modeID == modeID {
			for s := range shingles {
				existing.shingles[s] = struct{}{}
				c.unionShingles[s] = struct{}{}
			}
			for cite := range citations {
				c.unionCitations[cite] = struct{}{}
			}
			return
		}
	}

	c.completed = append(c.completed, completedModeCorpus{
		modeID:   modeID,
		shingles: shingles,
	})
	for s := range shingles {
		c.unionShingles[s] = struct{}{}
	}
	for cite := range citations {
		c.unionCitations[cite] = struct{}{}
	}
}

// WindowReady reports whether tokensGenerated has advanced by at least one
// configured WindowSize since the mode's last evaluation.
func (c *EarlyStopController) WindowReady(modeID string, tokensGenerated int) bool {
	if c == nil || !c.cfg.Enabled {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.modes[modeID]
	last := 0
	if st != nil {
		last = st.lastEvaluatedTokens
	}
	return tokensGenerated-last >= c.cfg.WindowSize
}

// EvaluateWindow runs one anytime-valid sequential e-process update on the
// mode's latest window text and returns a structured decision + EvidenceLedger.
func (c *EarlyStopController) EvaluateWindow(modeID, windowText string, tokensGenerated int) EarlyStopDecision {
	c.mu.Lock()
	defer c.mu.Unlock()

	st := c.modes[modeID]
	if st == nil {
		st = &modeSequentialState{eValue: 1.0}
		c.modes[modeID] = st
	}
	st.decisionSeq++
	if tokensGenerated > st.lastEvaluatedTokens {
		st.lastEvaluatedTokens = tokensGenerated
	}

	threshold := 1.0 / c.alpha
	completedCount := len(c.completed)

	if !c.cfg.Enabled {
		return c.buildDecision(modeID, st, completedCount, threshold, 1.0, 0.0, 0, false, true, "early_stop_disabled")
	}
	if completedCount < c.cfg.MinAgentsBeforeStop {
		return c.buildDecision(modeID, st, completedCount, threshold, 1.0, 0.0, 0, false, true, "below_min_agents")
	}

	windowShingles := extractShingles(windowText, 3)
	if len(windowShingles) == 0 {
		return c.buildDecision(modeID, st, completedCount, threshold, 1.0, 0.0, 0, false, true, "unparseable_or_empty_window")
	}

	// Compute max Jaccard similarity against completed modes and coverage overlap
	// against the union of completed modes.
	maxSim := 0.0
	for _, comp := range c.completed {
		if sim := shingleJaccardSimilarity(windowShingles, comp.shingles); sim > maxSim {
			maxSim = sim
		}
	}
	if cov := setContainment(windowShingles, c.unionShingles); cov > maxSim {
		maxSim = cov
	}

	citations := extractFileLineCitations(windowText)
	newCitations := 0
	for cite := range citations {
		if _, seen := c.unionCitations[cite]; !seen {
			newCitations++
		}
	}

	citationRate := 0.0
	if len(citations) > 0 {
		citationRate = float64(newCitations) / float64(len(citations))
	}
	marginalNovelty := math.Min(1.0, (1.0-maxSim)+0.5*citationRate)

	// Contrarian protection: any unseen file:line citation proves genuine
	// marginal discovery and resets accumulated redundancy evidence to 1.0.
	if newCitations > 0 {
		st.eValue = 1.0
		st.consecutiveLowWindows = 0
		return c.buildDecision(modeID, st, completedCount, threshold, marginalNovelty, maxSim, newCitations, false, true, "novel_citation_reset")
	}

	// Exact Bernoulli sequential e-variable under H0: P(novel window) >= p0.
	// By Ville's inequality, P_{H0}(exists t: E_t >= 1/alpha) <= alpha = 0.05.
	p0 := defaultEarlyStopNullNovelProb
	p1 := defaultEarlyStopAltNovelProb
	isRedundant := marginalNovelty < c.cfg.FindingsThreshold && maxSim >= c.cfg.SimilarityThreshold
	if isRedundant {
		st.eValue *= (1.0 - p1) / (1.0 - p0)
		st.consecutiveLowWindows++
	} else {
		st.eValue *= p1 / p0
		if st.eValue < 0.05 {
			st.eValue = 0.05
		}
		st.consecutiveLowWindows = 0
	}

	shouldStop := st.consecutiveLowWindows >= defaultEarlyStopMinWindows && st.eValue >= threshold
	fallbackActive := !shouldStop
	fallbackReason := ""
	if fallbackActive {
		fallbackReason = "e_value_below_ville_threshold"
	}
	return c.buildDecision(modeID, st, completedCount, threshold, marginalNovelty, maxSim, newCitations, shouldStop, fallbackActive, fallbackReason)
}

func (c *EarlyStopController) buildDecision(
	modeID string,
	st *modeSequentialState,
	completedCount int,
	threshold, marginalNovelty, maxSim float64,
	newCitations int,
	shouldStop, fallbackActive bool,
	fallbackReason string,
) EarlyStopDecision {
	action := "continue"
	if shouldStop {
		action = "stop"
	}

	// Calibrated posterior redundancy belief pi_t = E_t / (1 + E_t) under
	// equal prior odds; false-stop cost (1-alpha)/alpha = 19.0 vs waste cost 1.0.
	piRedundant := st.eValue / (1.0 + st.eValue)
	if fallbackActive && (fallbackReason == "early_stop_disabled" || fallbackReason == "below_min_agents" || fallbackReason == "unparseable_or_empty_window") {
		piRedundant = 0.0
	}
	lossStop := (1.0 - piRedundant) * ((1.0 - c.alpha) / c.alpha)
	lossContinue := piRedundant * 1.0
	expectedLoss := map[string]float64{
		"stop":     roundMetric(lossStop),
		"continue": roundMetric(lossContinue),
	}
	minLoss := math.Min(lossStop, lossContinue)
	chosenLoss := lossContinue
	if shouldStop {
		chosenLoss = lossStop
	}
	regret := map[string]float64{
		"stop":     roundMetric(lossStop - minLoss),
		"continue": roundMetric(lossContinue - minLoss),
	}
	calibration := math.Max(0.0, math.Min(1.0, 1.0-minLoss))

	decisionID := fmt.Sprintf("earlystop-%s-%s-%d", sanitizeDecisionToken(c.sessionName), sanitizeDecisionToken(modeID), st.decisionSeq)
	ledger := &state.EvidenceLedger{
		SchemaVersion:        "evidence_ledger.v1",
		TraceID:              c.sessionName,
		DecisionID:           decisionID,
		Component:            "ensemble.early_stop",
		Action:               action,
		ExpectedLossByAction: expectedLoss,
		ChosenExpectedLoss:   roundMetric(chosenLoss),
		RegretByAction:       regret,
		CalibrationScore:     roundMetric(calibration),
		FallbackActive:       fallbackActive,
		FallbackReason:       fallbackReason,
		TopFeatures: map[string]float64{
			"e_value":                 roundMetric(st.eValue),
			"ville_threshold":         roundMetric(threshold),
			"marginal_novelty":        roundMetric(marginalNovelty),
			"max_similarity":          roundMetric(maxSim),
			"new_citations":           float64(newCitations),
			"consecutive_low_windows": float64(st.consecutiveLowWindows),
			"completed_modes":         float64(completedCount),
		},
	}

	return EarlyStopDecision{
		ShouldStop:            shouldStop,
		Action:                action,
		ModeID:                modeID,
		EValue:                roundMetric(st.eValue),
		Threshold:             roundMetric(threshold),
		MarginalNovelty:       roundMetric(marginalNovelty),
		MaxSimilarity:         roundMetric(maxSim),
		NewCitations:          newCitations,
		ConsecutiveLowWindows: st.consecutiveLowWindows,
		CompletedModes:        completedCount,
		FallbackActive:        fallbackActive,
		FallbackReason:        fallbackReason,
		Ledger:                ledger,
	}
}

// PersistDecision writes an EarlyStopDecision and its §0.19 EvidenceLedger into
// the runtime state store's audit_decision_log so it is queryable via
// GetDecisionHistory (--robot-policy-decisions / GET /api/v1/policy/decisions).
func PersistEarlyStopDecision(store *state.Store, sessionName string, decision EarlyStopDecision) (int64, error) {
	if store == nil || decision.Ledger == nil {
		return 0, nil
	}
	entityID := sessionName
	if decision.ModeID != "" {
		if entityID != "" {
			entityID = entityID + ":" + decision.ModeID
		} else {
			entityID = decision.ModeID
		}
	}
	reason := fmt.Sprintf("action=%s e_value=%.2f novelty=%.2f similarity=%.2f",
		decision.Action, decision.EValue, decision.MarginalNovelty, decision.MaxSimilarity)
	if decision.FallbackReason != "" {
		reason += " (" + decision.FallbackReason + ")"
	}
	return store.RecordAuditDecision(&state.AuditDecision{
		DecisionType: "early_stop",
		DecisionAt:   time.Now().UTC(),
		ActorType:    "policy",
		ActorID:      "ensemble.early_stop",
		EntityType:   "ensemble_mode",
		EntityID:     entityID,
		Reason:       reason,
		Evidence:     decision.Ledger,
	})
}

func extractShingles(text string, k int) map[string]struct{} {
	words := strings.Fields(strings.ToLower(text))
	out := make(map[string]struct{})
	if len(words) == 0 {
		return out
	}
	if len(words) < k {
		out[strings.Join(words, " ")] = struct{}{}
		return out
	}
	for i := 0; i <= len(words)-k; i++ {
		out[strings.Join(words[i:i+k], " ")] = struct{}{}
	}
	return out
}

func extractFileLineCitations(text string) map[string]struct{} {
	matches := fileLineCitationPattern.FindAllString(text, -1)
	out := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		out[strings.ToLower(m)] = struct{}{}
	}
	return out
}

func shingleJaccardSimilarity(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union <= 0 {
		return 0.0
	}
	return float64(inter) / float64(union)
}

func setContainment(window, baseline map[string]struct{}) float64 {
	if len(window) == 0 || len(baseline) == 0 {
		return 0.0
	}
	inter := 0
	for k := range window {
		if _, ok := baseline[k]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(window))
}

func roundMetric(v float64) float64 {
	return math.Round(v*10000) / 10000
}

func sanitizeDecisionToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "default"
	}
	return strings.NewReplacer(" ", "_", "/", "_", ":", "_").Replace(s)
}
