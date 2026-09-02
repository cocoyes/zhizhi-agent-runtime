package eval

import "testing"

func TestScenarioDefaults(t *testing.T) {
	if (Scenario{Name: "chat", Input: "hi"}).MaxPlanSteps != 0 {
		t.Fatal("unexpected default")
	}
}
