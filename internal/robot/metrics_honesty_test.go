package robot

import (
	"encoding/json"
	"strings"
	"testing"
)

// --robot-metrics reported token_usage and every numeric in agent_stats as 0 on
// a session with twelve agents that had been working for hours, because nothing
// populates them. A machine-readable contract that says "0 prompts, 0 tokens,
// $0" is read by other agents as an idle session, so the payload now names the
// fields it never measured.

func TestUnmeasuredFieldsAreDeclared(t *testing.T) {
	fields := unmeasuredMetricsFields()
	if len(fields) == 0 {
		t.Fatal("no unmeasured fields declared")
	}

	// Everything token_usage exposes is unmeasured; none of it may be omitted
	// from the declaration.
	for _, want := range []string{
		"token_usage.total_tokens",
		"token_usage.total_cost_usd",
		"agent_stats[].prompts_received",
		"agent_stats[].uptime",
		"session_stats.total_prompts",
	} {
		var found bool
		for _, f := range fields {
			if f == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is never populated but is not declared unmeasured", want)
		}
	}
}

// The declaration has to reach the wire, or it protects nobody.
func TestMetricsOutputSerializesUnmeasured(t *testing.T) {
	output := &MetricsOutput{
		RobotResponse: NewRobotResponse(true),
		Period:        "24h",
		Unmeasured:    unmeasuredMetricsFields(),
		AgentStats:    map[string]AgentMetrics{},
	}

	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"unmeasured"`) {
		t.Error("unmeasured list is not serialized")
	}
	if !strings.Contains(string(raw), "token_usage.total_cost_usd") {
		t.Error("the cost field is not declared unmeasured on the wire")
	}
}

// A field that gains a real writer must drop off the list, so this cannot rot
// into a second kind of lie: claiming something is unmeasured when it is not.
// AgentMetrics carries exactly one populated field today — Type — and it must
// never be listed.
func TestMeasuredFieldsAreNotDeclaredUnmeasured(t *testing.T) {
	for _, f := range unmeasuredMetricsFields() {
		switch f {
		case "agent_stats[].type",
			"session_stats.total_agents",
			"session_stats.active_agents",
			"session_stats.files_changed":
			t.Errorf("%s is populated but declared unmeasured", f)
		}
	}
}
