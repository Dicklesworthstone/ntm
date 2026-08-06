package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestRobotProcessExitContractReservesUnavailableForNotImplemented(t *testing.T) {
	dependency := robot.NewErrorResponse(errors.New("missing dependency"), robot.ErrCodeDependencyMissing, "install it")
	dependency.Meta = robot.NewResponseMeta("robot-test").WithExitCode(2)
	if got := ExitCode(robot.ExitResultForResponse(dependency, errors.New("missing dependency"), true)); got != 1 {
		t.Fatalf("dependency failure exit = %d, want 1", got)
	}

	unavailable := robot.NewErrorResponse(errors.New("not implemented"), robot.ErrCodeNotImplemented, "use another command")
	if got := ExitCode(robot.ExitResultForResponse(unavailable, errors.New("not implemented"), true)); got != 2 {
		t.Fatalf("NOT_IMPLEMENTED exit = %d, want 2", got)
	}

	success := robot.NewRobotResponseWithMeta(true, robot.NewResponseMeta("robot-test").WithExitCode(2))
	if got := ExitCode(robot.ExitResultForResponse(success, nil, true)); got != 0 {
		t.Fatalf("successful envelope exit = %d, want 0", got)
	}
}

func TestRobotRegistryMatchesLiveCobraAndSchemaContracts(t *testing.T) {
	registry := robot.GetRobotRegistry()
	for _, surface := range registry.Surfaces {
		t.Run(surface.Name, func(t *testing.T) {
			assertLiveRootFlag(t, surface.Flag)
			for _, parameter := range surface.Parameters {
				assertLiveRootFlag(t, parameter.Flag)
				assertRobotParameterMatchesCobra(t, parameter)
			}
			for _, example := range surface.Examples {
				for _, flag := range exampleFlags(example) {
					assertLiveRootFlag(t, flag)
				}
			}

			if stringSliceContains(surface.OutputFormats, "json") {
				if surface.SchemaType == "" || surface.SchemaID == "" {
					t.Fatalf("JSON output lacks a schema binding: source=%q", surface.SchemaSource)
				}
				if _, ok := registry.SchemaBinding(surface.SchemaType); !ok {
					t.Fatalf("schema type %q has no registered Go response type", surface.SchemaType)
				}
				output, err := robot.GetSchema(surface.SchemaType)
				if err != nil {
					t.Fatalf("GetSchema(%q): %v", surface.SchemaType, err)
				}
				if !output.Success || output.Schema == nil || len(output.Schema.Properties) == 0 {
					t.Fatalf("schema %q is not a concrete object schema: %#v", surface.SchemaType, output)
				}
			} else {
				if surface.SchemaType != "" || surface.SchemaID != "" || strings.TrimSpace(surface.SchemaUnavailableReason) == "" {
					t.Fatalf("non-JSON output schema designation is inconsistent: type=%q id=%q reason=%q", surface.SchemaType, surface.SchemaID, surface.SchemaUnavailableReason)
				}
			}
		})
	}
}

func TestRobotExternalSchemaBindingsUseEmittedTypes(t *testing.T) {
	registry := robot.GetRobotRegistry()
	want := map[string]interface{}{
		"context_inject":   ContextInjectResult{},
		"controller_spawn": ControllerResponse{},
		"default_prompts":  DefaultPromptsOutput{},
		"pipeline_cancel":  pipeline.PipelineCancelOutput{},
		"pipeline_list":    pipeline.PipelineListOutput{},
		"pipeline_run":     pipeline.PipelineRunOutput{},
		"pipeline_status":  pipeline.PipelineStatusOutput{},
		"profile_list":     SessionProfileListOutput{},
		"profile_show":     SessionProfileShowOutput{},
		"sequence":         RobotSequenceOutput{},
	}
	for schemaType, expected := range want {
		got, ok := registry.SchemaBinding(schemaType)
		if !ok {
			t.Errorf("missing externally registered schema %q", schemaType)
			continue
		}
		if reflect.TypeOf(got) != reflect.TypeOf(expected) {
			t.Errorf("schema %q binding = %T, want %T", schemaType, got, expected)
		}
	}
}

func assertLiveRootFlag(t *testing.T, flag string) {
	t.Helper()
	name := strings.TrimPrefix(flag, "--")
	if name == "" || liveRootFlag(name) == nil {
		t.Errorf("registry flag %q is not registered on the live root Cobra command", flag)
	}
}

func assertRobotParameterMatchesCobra(t *testing.T, parameter robot.RobotParameter) {
	t.Helper()
	flag := liveRootFlag(strings.TrimPrefix(parameter.Flag, "--"))
	if flag == nil {
		return
	}

	actual := flag.Value.Type()
	valid := false
	switch parameter.Type {
	case "bool":
		valid = actual == "bool"
	case "int":
		valid = actual == "int" || actual == "int64"
	case "float":
		valid = actual == "float64"
	case "duration":
		valid = actual == "duration" || actual == "string"
	case "string":
		valid = actual == "string" || actual == "stringSlice" || actual == "stringArray"
	}
	if !valid {
		t.Errorf("parameter %q type = %q in registry, %q in Cobra", parameter.Flag, parameter.Type, actual)
	}
}

func liveRootFlag(name string) *pflag.Flag {
	if flag := rootCmd.Flags().Lookup(name); flag != nil {
		return flag
	}
	if flag := rootCmd.PersistentFlags().Lookup(name); flag != nil {
		return flag
	}
	return nil
}

func exampleFlags(example string) []string {
	var words []string
	var word strings.Builder
	var delimiter rune
	flush := func() {
		if word.Len() != 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for _, r := range example {
		switch {
		case delimiter != 0:
			word.WriteRune(r)
			if r == delimiter {
				delimiter = 0
			}
		case r == '\'' || r == '"':
			delimiter = r
			word.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			word.WriteRune(r)
		}
	}
	flush()

	flags := make([]string, 0, len(words))
	for _, word := range words {
		if !strings.HasPrefix(word, "--") {
			continue
		}
		flag := word
		if index := strings.IndexByte(flag, '='); index >= 0 {
			flag = flag[:index]
		}
		flags = append(flags, flag)
	}
	return flags
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
