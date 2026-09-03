package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestFinishToolAcceptsBoundPlan(t *testing.T) {
	result, err := newFinishTool().Call(context.Background(), json.RawMessage(`{"plan":"下午活动 -> 晚餐"}`))
	if err != nil {
		t.Fatal(err)
	}
	output, ok := result.Content.(itineraryOutput)
	if !ok || output.Plan != "下午活动 -> 晚餐" {
		t.Fatalf("unexpected finish output: %#v", result.Content)
	}
}
