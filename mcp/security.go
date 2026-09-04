package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
)

type trustLevel string

const (
	trusted   trustLevel = "trusted"
	untrusted trustLevel = "untrusted"
)

type trustEvidence struct {
	Trust trustLevel `json:"trust"`
	Data  any        `json:"data"`
}

func wrapUntrusted(data any) trustEvidence { return trustEvidence{Trust: untrusted, Data: data} }
func promptData(data any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "<untrusted-evidence encoding_error=\"true\"></untrusted-evidence>"
	}
	return "<untrusted-evidence>\n" + string(b) + "\n</untrusted-evidence>"
}
func validateToolText(text string) error {
	lower := strings.ToLower(text)
	for _, marker := range []string{"ignore previous instructions", "system prompt", "developer message", "reveal secret"} {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("security: suspicious tool text")
		}
	}
	return nil
}
