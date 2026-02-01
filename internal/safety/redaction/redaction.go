package redaction

import redactionlib "github.com/Dicklesworthstone/ntm/internal/redaction"

func ScanAndRedact(input string, cfg Config) Result { return redactionlib.ScanAndRedact(input, cfg) }

func Scan(input string, cfg Config) []Finding { return redactionlib.Scan(input, cfg) }

func Redact(input string, cfg Config) (string, []Finding) { return redactionlib.Redact(input, cfg) }

func ContainsSensitive(input string, cfg Config) bool {
	return redactionlib.ContainsSensitive(input, cfg)
}

func AddLineInfo(input string, findings []Finding) { redactionlib.AddLineInfo(input, findings) }
