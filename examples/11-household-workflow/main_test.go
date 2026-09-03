package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"github.com/cocoyes/zhizhi-agent-runtime/observe"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
)

func TestHouseholdWorkflowLive(t *testing.T) {
	if os.Getenv("ZHIZHI_RUN_LIVE_EXAMPLE") != "1" {
		t.Skip("set ZHIZHI_RUN_LIVE_EXAMPLE=1 to run the real model integration test")
	}
	for _, name := range []string{"LLM_BASE_URL", "LLM_API_KEY", "LLM_MODEL"} {
		if os.Getenv(name) == "" {
			t.Skipf("%s is required for the real model integration test", name)
		}
	}
	state := newDemoState()
	var generated plan.Plan
	agent, err := newDemoAgent(state, observe.Func(func(event observe.Event) {
		if event.Type == "planner.completed" {
			generated, _ = event.Details["plan"].(plan.Plan)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())

	mode := zhizhi.RunModeAgentComplex
	response, err := agent.Run(context.Background(), zhizhi.Request{Input: defaultRequest, Mode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	encodedPlan, _ := json.Marshal(generated)
	t.Logf("model plan: %s", encodedPlan)
	if len(generated.Steps) == 0 {
		t.Fatal("model plan must not be empty")
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.maxActive < 2 {
		t.Fatalf("expected search and record to overlap, max concurrent calls=%d", state.maxActive)
	}
	if response.Replans < 1 || state.calls["notify"] < 2 {
		t.Fatalf("expected notify failure followed by replan: replans=%d calls=%v", response.Replans, state.calls)
	}
	if state.lastNotify.Channel != "local" || !(state.lastNotify.At == "20:00" || strings.Contains(state.lastNotify.At, "8点")) {
		t.Fatalf("unexpected notification after replan: %#v", state.lastNotify)
	}
	if state.calls["record"] < 1 {
		t.Fatalf("record was not executed: calls=%v", state.calls)
	}
	equalsDrawer, otherBranch := false, false
	for _, step := range generated.Steps {
		if step.Condition == nil {
			continue
		}
		equalsDrawer = equalsDrawer || step.Condition.Equals == "抽屉"
		otherBranch = otherBranch || step.Condition.NotEquals == "抽屉" || step.Condition.Equals == "其他"
	}
	if !equalsDrawer || !otherBranch {
		t.Fatalf("model did not plan both sides of the condition: %+v", generated.Steps)
	}
}

func TestDemoRegistersOnlySearchNotifyAndRecord(t *testing.T) {
	tools := newDemoTools(newDemoState())
	if len(tools) != 3 {
		t.Fatalf("expected exactly three tools, got %d", len(tools))
	}
	expected := map[string]bool{"search": false, "notify": false, "record": false}
	for _, value := range tools {
		if _, ok := expected[value.Spec().ID]; !ok {
			t.Fatalf("unexpected tool %q", value.Spec().ID)
		}
		expected[value.Spec().ID] = true
		if value.Spec().Description == "" {
			t.Fatalf("tool %q has no description", value.Spec().ID)
		}
	}
	for name, found := range expected {
		if !found {
			t.Fatalf("missing tool %q", name)
		}
	}
}
