package serve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Admission already preserves JSON numbers. Dispatch must not silently round
// them during the second decode into the operation's typed request. The same
// helper feeds file workflows, inline workflows and resume overrides.
func TestJobPipelineVariablesPreserveNumbers(t *testing.T) {
	for _, literal := range []string{
		"9007199254740993", "-9007199254740993", "9223372036854775807",
		"18446744073709551615", "0.10000000000000000000001", "9.007199254740993e15",
	} {
		for _, kind := range []string{"pipeline_run", "pipeline_exec", "pipeline_resume"} {
			t.Run(kind+"/"+literal, func(t *testing.T) {
				body := `{"type":"` + kind + `","params":{"session":"numeric","variables":{"id":` + literal + `,"nested":[{"id":` + literal + `}],"as_text":"` + literal + `"}}}`
				envelope, err := decodeCreateJobRequest(strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				before, err := json.Marshal(envelope.Params)
				if err != nil {
					t.Fatal(err)
				}
				var variables map[string]interface{}
				switch kind {
				case "pipeline_run":
					var request PipelineRunRequest
					err = decodeJobParams(envelope.Params, &request)
					variables = request.Variables
				case "pipeline_exec":
					var request PipelineExecRequest
					err = decodeJobParams(envelope.Params, &request)
					variables = request.Variables
				case "pipeline_resume":
					var request PipelineResumeRequest
					err = decodeJobParams(envelope.Params, &request)
					variables = request.Variables
				}
				if err != nil {
					t.Fatal(err)
				}
				got, ok := variables["id"].(json.Number)
				if !ok || got.String() != literal {
					t.Fatalf("dispatch changed numeric input %s to %v (%T)", literal, variables["id"], variables["id"])
				}
				nested := variables["nested"].([]interface{})[0].(map[string]interface{})["id"]
				if number, ok := nested.(json.Number); !ok || number.String() != literal {
					t.Fatalf("nested workflow variable changed: %v (%T)", nested, nested)
				}
				if text, ok := variables["as_text"].(string); !ok || text != literal {
					t.Fatal("number preservation coerced a string variable")
				}
				after, err := json.Marshal(envelope.Params)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("typed decoding mutated the fingerprinted request: %v", err)
				}
				// Re-encoding stays a JSON number, never a quoted surrogate.
				encoded, err := json.Marshal(got)
				if err != nil || string(encoded) != literal {
					t.Fatalf("numeric spelling/type lost on forwarding: %s %v", encoded, err)
				}
			})
		}
	}
}

func TestJobNumericRequestIdentitiesRemainDistinctAtExecution(t *testing.T) {
	var identifiers []string
	for _, literal := range []string{"9007199254740992", "9007199254740993"} {
		envelope, err := decodeCreateJobRequest(strings.NewReader(`{"type":"pipeline_run","params":{"variables":{"id":` + literal + `}}}`))
		if err != nil {
			t.Fatal(err)
		}
		var request PipelineRunRequest
		if err := decodeJobParams(envelope.Params, &request); err != nil {
			t.Fatal(err)
		}
		// The pipeline substitutor renders scalar values via their text
		// representation. Distinct admitted integers must not name one target.
		identifiers = append(identifiers, fmt.Sprint(request.Variables["id"]))
	}
	if identifiers[0] != "9007199254740992" || identifiers[1] != "9007199254740993" {
		t.Fatalf("distinct admitted inputs collapsed at execution: %v", identifiers)
	}
}

func TestJobNumberPreservationKeepsTypedValidation(t *testing.T) {
	type controls struct {
		Count int64  `json:"count"`
		Safe  bool   `json:"safe"`
		Name  string `json:"name"`
	}
	for _, tc := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"integer", `{"count":9223372036854775807,"safe":true,"name":"n"}`, true},
		{"negative", `{"count":-9223372036854775808}`, true},
		{"overflow", `{"count":9223372036854775808}`, false},
		{"fraction", `{"count":1.5}`, false},
		{"numeric boolean", `{"safe":1}`, false},
		{"numeric string", `{"name":42}`, false},
		{"unknown safety flag", `{"saef":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoder := json.NewDecoder(strings.NewReader(tc.body))
			decoder.UseNumber()
			var params map[string]interface{}
			if err := decoder.Decode(&params); err != nil {
				t.Fatal(err)
			}
			var request controls
			err := decodeJobParams(params, &request)
			if (err == nil) != tc.valid {
				t.Fatalf("typed request validation changed: %+v %v", request, err)
			}
			if tc.valid && fmt.Sprint(request.Count) != params["count"].(json.Number).String() {
				t.Fatal("typed integer was rounded")
			}
		})
	}
}
