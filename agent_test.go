package zhizhi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/zhizhi-ai/zhizhi-agent-runtime/contract"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
)

type scriptedModel struct{ calls int }

func (m *scriptedModel) ID() string { return "scripted" }
func (m *scriptedModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true, Streaming: true}
}
func (m *scriptedModel) Generate(_ context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	if m.calls == 1 {
		return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{{ID: "call-1", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "weather.get", Arguments: `{"city":"Shenzhen"}`}}}}, nil
	}
	return &model.ModelOutput{Text: "深圳晴天，27 度。"}, nil
}
func (m *scriptedModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestRunFunctionToolThroughPublicAPI(t *testing.T) {
	called := false
	weather := tool.Func("weather.get", "Get weather", func(_ context.Context, args struct {
		City string `json:"city"`
	}) (map[string]any, error) {
		called = args.City == "Shenzhen"
		return map[string]any{"city": args.City, "temperature": 27}, nil
	})
	m := &scriptedModel{}
	a, err := New(WithModel(m), WithTools(weather))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Run(context.Background(), Request{Input: "深圳天气怎么样？"})
	if err != nil || !called || resp.Text == "" || len(resp.Evidence) != 1 || m.calls != 2 {
		t.Fatalf("unexpected response: %+v err=%v calls=%d", resp, err, m.calls)
	}
	if _, err := json.Marshal(resp.Evidence[0].Content); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetAndGuard(t *testing.T) {
	m := &scriptedModel{}
	weather := tool.Func("weather.get", "weather", func(context.Context, struct{}) (string, error) { return "ok", nil })
	a, err := New(WithModel(m), WithTools(weather), WithBudget(Budget{MaxModelCalls: 0}), WithGuard(func(context.Context, tool.Spec) error { return errors.New("blocked") }))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), Request{Input: "hello"})
	if err == nil || err.Error() != "blocked" {
		t.Fatalf("expected guard error, got %v", err)
	}
	m2 := &scriptedModel{}
	a2, err := New(WithModel(m2), WithBudget(Budget{MaxModelCalls: 1}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a2.Run(context.Background(), Request{Input: "hello"})
	if err == nil || m2.calls != 1 {
		t.Fatalf("expected budget error, got %v calls=%d", err, m2.calls)
	}
}

type writeModel struct{ calls int }

func (m *writeModel) ID() string { return "write-scripted" }
func (m *writeModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true}
}
func (m *writeModel) Generate(_ context.Context, _ model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	if m.calls == 1 {
		return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{{ID: "write-call", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "write", Arguments: `{}`}}}}, nil
	}
	return &model.ModelOutput{Text: "done"}, nil
}
func (m *writeModel) Stream(context.Context, model.ModelInput) (model.Stream, error) { return nil, nil }

func TestRunRequiresConfirmationBeforeWrite(t *testing.T) {
	called := false
	write := tool.Func("write", "write", func(context.Context, struct{}) (string, error) { called = true; return "done", nil }, tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyIdempotent), tool.WithConfirmation(tool.ConfirmationAlways))
	a, err := New(WithModel(&writeModel{}), WithTools(write))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Run(context.Background(), Request{Input: "write"})
	if err != nil || called || resp == nil || len(resp.Actions) != 1 || resp.Actions[0].Status != contract.ActionConfirmationRequired {
		t.Fatalf("confirmation gate failed: resp=%+v err=%v called=%v", resp, err, called)
	}
}

type streamStub struct{ sent bool }

func (s *streamStub) Recv(context.Context) (model.StreamEvent, error) {
	if s.sent {
		return model.StreamEvent{}, io.EOF
	}
	s.sent = true
	var delta model.ToolCallDelta
	delta.Index = 0
	delta.ID = "call"
	delta.Name = "weather.get"
	delta.Arguments = `{"city":"Shenzhen"}`
	return model.StreamEvent{ToolCallDeltas: []model.ToolCallDelta{delta}}, nil
}
func (s *streamStub) Close() error { return nil }

type streamModel struct{ calls int }

func (m *streamModel) ID() string { return "stream" }
func (m *streamModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true, Streaming: true}
}
func (m *streamModel) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	return &model.ModelOutput{Text: "stream final"}, nil
}
func (m *streamModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return &streamStub{}, nil
}
func TestAgentStreamCompletesToolCall(t *testing.T) {
	weather := tool.Func("weather.get", "weather", func(context.Context, struct {
		City string `json:"city"`
	}) (string, error) {
		return "sunny", nil
	})
	m := &streamModel{}
	a, err := New(WithModel(m), WithTools(weather))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := a.Stream(context.Background(), Request{Input: "weather"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv(context.Background())
	if err != nil || event.Response == nil || event.Response.Text != "stream final" {
		t.Fatalf("unexpected stream completion: %+v %v", event, err)
	}
}
