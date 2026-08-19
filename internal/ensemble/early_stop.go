package ensemble

// EarlyStopConfig controls early stopping behavior for ensembles.
type EarlyStopConfig struct {
	Enabled             bool    `json:"enabled" toml:"enabled" yaml:"enabled"`
	MinAgentsBeforeStop int     `json:"min_agents_before_stop" toml:"min_agents_before_stop" yaml:"min_agents_before_stop"`
	FindingsThreshold   float64 `json:"findings_threshold" toml:"findings_threshold" yaml:"findings_threshold"`
	SimilarityThreshold float64 `json:"similarity_threshold" toml:"similarity_threshold" yaml:"similarity_threshold"`
	WindowSize          int     `json:"window_size" toml:"window_size" yaml:"window_size"`
}
