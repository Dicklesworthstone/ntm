package ensemble

import "testing"

func TestGetStrategy(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"manual", false},
		{"adversarial", false},
		{"consensus", false},
		{"creative", false},
		{"analytical", false},
		{"deliberative", false},
		{"prioritized", false},
		{"dialectical", false},
		{"meta-reasoning", false},
		{"voting", false},
		{"argumentation-graph", false},
		{"invalid", true},
		{"", true},
		{"debate", true},
		{"weighted", true},
	}

	for _, tt := range tests {
		cfg, err := GetStrategy(tt.name)
		if tt.wantErr {
			if err == nil {
				t.Errorf("GetStrategy(%q) should error", tt.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("GetStrategy(%q) unexpected error: %v", tt.name, err)
			continue
		}
		if cfg.Name != SynthesisStrategy(tt.name) {
			t.Errorf("GetStrategy(%q).Name = %q", tt.name, cfg.Name)
		}
		if cfg.Description == "" {
			t.Errorf("GetStrategy(%q).Description is empty", tt.name)
		}
	}
}

func TestValidateStrategy(t *testing.T) {
	for _, s := range allStrategies {
		if !ValidateStrategy(string(s)) {
			t.Errorf("ValidateStrategy(%q) = false, want true", s)
		}
	}
	if ValidateStrategy("bogus") {
		t.Error("ValidateStrategy(\"bogus\") = true, want false")
	}
}

func TestMigrateStrategy(t *testing.T) {
	tests := []struct {
		input       string
		want        string
		wantMigrate bool
	}{
		{"debate", "dialectical", true},
		{"weighted", "prioritized", true},
		{"sequential", "manual", true},
		{"best-of", "prioritized", true},
		{"consensus", "consensus", false},
		{"unknown", "unknown", false},
	}

	for _, tt := range tests {
		got, migrated := MigrateStrategy(tt.input)
		if got != tt.want || migrated != tt.wantMigrate {
			t.Errorf("MigrateStrategy(%q) = (%q, %v), want (%q, %v)",
				tt.input, got, migrated, tt.want, tt.wantMigrate)
		}
	}
}

func TestValidateOrMigrateStrategy(t *testing.T) {
	// Valid strategy passes through.
	s, err := ValidateOrMigrateStrategy("consensus")
	if err != nil {
		t.Errorf("ValidateOrMigrateStrategy(\"consensus\") error: %v", err)
	}
	if s != StrategyConsensus {
		t.Errorf("got %q, want %q", s, StrategyConsensus)
	}

	// Deprecated strategy returns error with guidance.
	_, err = ValidateOrMigrateStrategy("debate")
	if err == nil {
		t.Error("ValidateOrMigrateStrategy(\"debate\") should error")
	}

	// Unknown strategy returns error.
	_, err = ValidateOrMigrateStrategy("bogus")
	if err == nil {
		t.Error("ValidateOrMigrateStrategy(\"bogus\") should error")
	}
}
