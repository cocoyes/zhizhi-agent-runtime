package security

import (
	"encoding/json"
	"fmt"
	"strings"
)

type TrustLevel string

const (
	Trusted   TrustLevel = "trusted"
	Untrusted TrustLevel = "untrusted"
)

type Evidence struct {
	Trust TrustLevel `json:"trust"`
	Data  any        `json:"data"`
}

func WrapUntrusted(data any) Evidence { return Evidence{Trust: Untrusted, Data: data} }
func PromptData(data any) string {
	b, _ := json.Marshal(data)
	return "<untrusted-evidence>\n" + string(b) + "\n</untrusted-evidence>"
}
func ValidateToolText(text string) error {
	lower := strings.ToLower(text)
	for _, marker := range []string{"ignore previous instructions", "system prompt", "developer message", "reveal secret"} {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("security: suspicious tool text")
		}
	}
	return nil
}
