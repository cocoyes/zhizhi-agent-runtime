package tool

import (
	"context"
	"testing"
)

func TestBrokerResolvesDeterministically(t *testing.T) {
	a := Func("b", "b", func(context.Context, struct{}) (string, error) { return "", nil }, WithCapabilities("weather"))
	b := Func("a", "a", func(context.Context, struct{}) (string, error) { return "", nil }, WithCapabilities("weather"))
	values, err := NewBroker(NewRegistry(a, b)).Resolve("weather")
	if err != nil || len(values) != 2 || values[0].Tool.Spec().ID != "a" {
		t.Fatalf("unexpected candidates: %+v %v", values, err)
	}
}
