package route

import (
	"context"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
	"testing"
)

func TestDefaultRouter(t *testing.T) {
	r := DefaultRouter{}
	intent, _ := r.Route(context.Background(), "weather and restaurant", []model.ToolSpec{{Function: model.FunctionSpec{Name: "weather.current"}}, {Function: model.FunctionSpec{Name: "restaurant.search"}}})
	if intent.Mode != ModeComplex {
		t.Fatal(intent)
	}
	intent, _ = r.Route(context.Background(), "你好", nil)
	if intent.Mode != ModeChat {
		t.Fatal(intent)
	}
	intent, _ = r.Route(context.Background(), "查天气", []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{Name: "weather.current"}}})
	if intent.Mode != ModeSimple {
		t.Fatal(intent)
	}
}

func TestDefaultRouterDoesNotTreatConnectiveWordsAsComplexity(t *testing.T) {
	intent, err := (DefaultRouter{}).Route(context.Background(), "然后帮我看看", []model.ToolSpec{{Function: model.FunctionSpec{Name: "weather.current"}}})
	if err != nil {
		t.Fatal(err)
	}
	if intent.Mode == ModeComplex {
		t.Fatalf("generic connective incorrectly triggered complex mode: %+v", intent)
	}
}
