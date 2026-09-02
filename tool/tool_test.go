package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zhizhi-ai/zhizhi-agent-runtime/contract"
)

type testArgs struct {
	City string `json:"city" jsonschema:"required"`
}
type testResult struct {
	City string `json:"city"`
}

func TestFuncGeneratesSchemaAndCallsFunction(t *testing.T) {
	called := false
	v := Func("weather.get", "Get weather", func(_ context.Context, in testArgs) (testResult, error) {
		called = true
		return testResult{City: in.City}, nil
	})
	if v.Spec().ID != "weather.get" || len(v.Spec().InputSchema) == 0 {
		t.Fatalf("invalid spec: %+v", v.Spec())
	}
	var schema map[string]any
	if err := json.Unmarshal(v.Spec().InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$ref"] == nil || schema["$defs"] == nil {
		t.Fatalf("expected reflected schema: %v", schema)
	}
	result, err := v.Call(context.Background(), json.RawMessage(`{"city":"Shenzhen"}`))
	if err != nil || !called || result.Content.(testResult).City != "Shenzhen" {
		t.Fatalf("unexpected result: %+v %v", result, err)
	}
}

func TestWriteToolCreatesActionReceipt(t *testing.T) {
	v := Func("calendar.write", "write", func(context.Context, struct{}) (string, error) { return "created", nil }, WithSideEffect(SideEffectWrite))
	result, err := v.Call(context.Background(), json.RawMessage(`{}`))
	if err != nil || !result.Receipt || result.Action == nil || result.Action.Status != contract.ActionSuccess {
		t.Fatalf("missing action receipt: %+v %v", result, err)
	}
}

func TestRegistryResolvesSanitizedToolID(t *testing.T) {
	v := Func("activity.cached", "cached activity", func(context.Context, struct{}) (string, error) { return "ok", nil }, WithCapabilities("activity.cached"))
	values := NewRegistry(v).ResolveCapability("activity_cached")
	if len(values) != 1 || values[0].Spec().ID != "activity.cached" {
		t.Fatalf("sanitized tool id was not resolved: %+v", values)
	}
}
