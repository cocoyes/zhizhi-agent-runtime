package e2e

import (
	"os"
	"testing"
)

func TestRealEnvironmentGate(t *testing.T) {
	if os.Getenv("ZHIZHI_REAL_E2E") != "1" {
		t.Skip("set ZHIZHI_REAL_E2E=1 to run real model E2E")
	}
	for _, name := range []string{"LLM_BASE_URL", "LLM_API_KEY", "LLM_MODEL"} {
		if os.Getenv(name) == "" {
			t.Fatalf("%s is required when ZHIZHI_REAL_E2E=1", name)
		}
	}
}
