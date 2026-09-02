package eval

import (
	"context"
	"fmt"
	"github.com/zhizhi-ai/zhizhi-agent-runtime"
)

type Scenario struct {
	Name                 string
	Input                string
	ExpectedMode         zhizhi.RunMode
	RequiredCapabilities []string
	MaxPlanSteps         int
	MaxBatches           int
	MaxReplans           int
	ExpectedOutcome      string
}
type Result struct {
	Scenario Scenario
	Response *zhizhi.Response
	Err      error
	Passed   bool
	Failures []string
}

func Run(ctx context.Context, agent zhizhi.Agent, scenario Scenario) Result {
	result := Result{Scenario: scenario}
	response, err := agent.Run(ctx, zhizhi.Request{Input: scenario.Input})
	result.Response, result.Err = response, err
	if err != nil {
		result.Failures = append(result.Failures, err.Error())
		return result
	}
	if scenario.MaxPlanSteps > 0 && response.PlanSteps > scenario.MaxPlanSteps {
		result.Failures = append(result.Failures, fmt.Sprintf("plan steps %d > %d", response.PlanSteps, scenario.MaxPlanSteps))
	}
	if scenario.MaxBatches > 0 && response.Batches > scenario.MaxBatches {
		result.Failures = append(result.Failures, fmt.Sprintf("batches %d > %d", response.Batches, scenario.MaxBatches))
	}
	if scenario.MaxReplans > 0 && response.Replans > scenario.MaxReplans {
		result.Failures = append(result.Failures, fmt.Sprintf("replans %d > %d", response.Replans, scenario.MaxReplans))
	}
	result.Passed = len(result.Failures) == 0
	return result
}
