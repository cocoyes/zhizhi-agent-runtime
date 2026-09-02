package capability

import (
	"context"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
	"testing"
)

func TestBrokerResolvesDeterministically(t *testing.T) {
	a := tool.Func("b", "b", func(context.Context, struct{}) (string, error) { return "", nil }, tool.WithCapabilities("weather"))
	b := tool.Func("a", "a", func(context.Context, struct{}) (string, error) { return "", nil }, tool.WithCapabilities("weather"))
	values, err := NewBroker(tool.NewRegistry(a, b)).Resolve("weather")
	if err != nil || len(values) != 2 || values[0].Tool.Spec().ID != "a" {
		t.Fatalf("unexpected candidates: %+v %v", values, err)
	}
}
