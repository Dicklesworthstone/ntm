package robot

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"sync"
)

// RobotVerbosity controls how much detail is included in robot outputs.
// This only applies to JSON/TOON rendering, not --robot-terse.
type RobotVerbosity string

const (
	VerbosityDefault RobotVerbosity = "default"
	VerbosityTerse   RobotVerbosity = "terse"
	VerbosityDebug   RobotVerbosity = "debug"
)

// outputVerbosity controls the robot verbosity profile.
// Access via GetOutputVerbosity/SetOutputVerbosity for thread safety.
var (
	verbosityMu     sync.RWMutex
	outputVerbosity RobotVerbosity = VerbosityDefault
)

// GetOutputVerbosity returns the current verbosity (thread-safe).
func GetOutputVerbosity() RobotVerbosity {
	verbosityMu.RLock()
	defer verbosityMu.RUnlock()
	return outputVerbosity
}

// SetOutputVerbosity sets the verbosity (thread-safe).
func SetOutputVerbosity(v RobotVerbosity) {
	verbosityMu.Lock()
	defer verbosityMu.Unlock()
	outputVerbosity = v
}

// ParseRobotVerbosity converts a string to RobotVerbosity.
// Returns VerbosityDefault for empty string and error for invalid values.
func ParseRobotVerbosity(s string) (RobotVerbosity, error) {
	if s == "" {
		return VerbosityDefault, nil
	}
	v := RobotVerbosity(s)
	switch v {
	case VerbosityDefault, VerbosityTerse, VerbosityDebug:
		return v, nil
	default:
		return "", fmt.Errorf("invalid robot verbosity %q: must be terse, default, or debug", s)
	}
}

func applyVerbosity(payload any, verbosity RobotVerbosity) any {
	switch verbosity {
	case VerbosityTerse:
		return applyTerseProfile(payload)
	case VerbosityDebug:
		return applyDebugProfile(payload)
	default:
		return payload
	}
}

func applyTerseProfile(payload any) any {
	normalized, err := normalizePayload(payload)
	if err != nil {
		return payload
	}
	return shortenKeys(normalized)
}

func applyDebugProfile(payload any) any {
	normalized, err := normalizePayload(payload)
	if err != nil {
		return payload
	}

	debugInfo := map[string]any{
		"format":       GetOutputFormat().String(),
		"verbosity":    string(VerbosityDebug),
		"go_version":   runtime.Version(),
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"payload_type": payloadTypeName(payload),
	}

	switch typed := normalized.(type) {
	case map[string]any:
		if _, exists := typed["_debug"]; !exists {
			typed["_debug"] = debugInfo
		}
		return typed
	case []any:
		return map[string]any{
			"_debug": debugInfo,
			"items":  typed,
		}
	default:
		return map[string]any{
			"_debug": debugInfo,
			"value":  typed,
		}
	}
}

func payloadTypeName(payload any) string {
	if payload == nil {
		return "nil"
	}
	return reflect.TypeOf(payload).String()
}

func normalizePayload(payload any) (any, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func shortenKeys(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, val := range typed {
			if key == "_agent_hints" {
				continue
			}
			shortKey := key
			if mapped, ok := TerseKeyFor(key); ok {
				if _, occupied := typed[mapped]; occupied && mapped != key {
					out[key] = shortenKeys(val)
					continue
				}
				shortKey = mapped
			}
			out[shortKey] = shortenKeys(val)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, val := range typed {
			out[i] = shortenKeys(val)
		}
		return out
	default:
		return value
	}
}
