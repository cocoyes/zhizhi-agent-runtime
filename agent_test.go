package zhizhi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/checkpoint"
	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/middleware"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/runtimeerr"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
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

type reasoningToolModel struct {
	calls int
	t     *testing.T
}

func (m *reasoningToolModel) ID() string { return "reasoning-tool" }
func (m *reasoningToolModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true}
}
func (m *reasoningToolModel) Generate(_ context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	if m.calls == 1 {
		return &model.ModelOutput{Text: "checking", ReasoningContent: "need the tool", ToolCalls: []model.ToolCallRequest{{ID: "call-1", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "lookup", Arguments: `{}`}}}}, nil
	}
	if len(input.Messages) < 2 || input.Messages[len(input.Messages)-2].ReasoningContent != "need the tool" || input.Messages[len(input.Messages)-2].Content != "checking" {
		m.t.Fatalf("assistant reasoning/content were not preserved for the tool continuation: %+v", input.Messages)
	}
	return &model.ModelOutput{Text: "done"}, nil
}
func (m *reasoningToolModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestRunPreservesReasoningContentAcrossToolCalls(t *testing.T) {
	m := &reasoningToolModel{t: t}
	a, err := New(WithModel(m), WithTools(tool.Func("lookup", "lookup", func(context.Context, struct{}) (string, error) { return "result", nil })))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Run(context.Background(), Request{Input: "lookup"})
	if err != nil || resp.Text != "done" || m.calls != 2 {
		t.Fatalf("unexpected response: %+v err=%v", resp, err)
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

func TestSuspendAndResumeDoesNotRepeatApprovedWork(t *testing.T) {
	called := 0
	write := tool.Func("write", "write", func(context.Context, struct{}) (string, error) { called++; return "done", nil }, tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyIdempotent), tool.WithConfirmation(tool.ConfirmationAlways))
	store := checkpoint.NewMemoryStore()
	a, err := New(WithModel(&writeModel{}), WithTools(write), WithCheckpointStore(store))
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := a.Run(context.Background(), Request{Input: "write"})
	if err != nil || !suspended.Suspended || suspended.Checkpoint == "" || called != 0 {
		t.Fatalf("unexpected suspension: %+v called=%d err=%v", suspended, called, err)
	}
	resumed, err := a.Resume(context.Background(), ResumeRequest{RunID: suspended.RunID, Checkpoint: suspended.Checkpoint, Decision: true})
	if err != nil || resumed.Suspended || resumed.Text != "done" || called != 1 {
		t.Fatalf("unexpected resume: %+v called=%d err=%v", resumed, called, err)
	}
	if _, err := store.Load(context.Background(), suspended.Checkpoint); !errors.Is(err, checkpoint.ErrNotFound) {
		t.Fatalf("completed checkpoint was not deleted: %v", err)
	}
}

type twoWriteModel struct{ calls int }

func (m *twoWriteModel) ID() string { return "two-write" }
func (m *twoWriteModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true}
}
func (m *twoWriteModel) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	if m.calls == 1 {
		return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{toolCall("one", "write.one"), toolCall("two", "write.two")}}, nil
	}
	return &model.ModelOutput{Text: "done"}, nil
}
func (m *twoWriteModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}
func toolCall(id, name string) model.ToolCallRequest {
	var call model.ToolCallRequest
	call.ID = id
	call.Type = "function"
	call.Function.Name = name
	call.Function.Arguments = `{}`
	return call
}

func TestResumeInvalidatesPriorCheckpointOnSecondSuspend(t *testing.T) {
	first, second := 0, 0
	one := tool.Func("write.one", "one", func(context.Context, struct{}) (string, error) { first++; return "one", nil }, tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyIdempotent), tool.WithConfirmation(tool.ConfirmationAlways))
	two := tool.Func("write.two", "two", func(context.Context, struct{}) (string, error) { second++; return "two", nil }, tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyIdempotent), tool.WithConfirmation(tool.ConfirmationAlways))
	store := checkpoint.NewMemoryStore()
	a, err := New(WithModel(&twoWriteModel{}), WithTools(one, two), WithCheckpointStore(store))
	if err != nil {
		t.Fatal(err)
	}
	mode := RunModeAgentSimple
	original, err := a.Run(context.Background(), Request{Input: "write", Mode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	next, err := a.Resume(context.Background(), ResumeRequest{Checkpoint: original.Checkpoint, Decision: true})
	if err != nil || !next.Suspended || first != 1 || second != 0 {
		t.Fatalf("unexpected second suspension: %+v first=%d second=%d err=%v", next, first, second, err)
	}
	if _, err := a.Resume(context.Background(), ResumeRequest{Checkpoint: original.Checkpoint, Decision: true}); !errors.Is(err, checkpoint.ErrNotFound) {
		t.Fatalf("stale checkpoint remained replayable: %v", err)
	}
	final, err := a.Resume(context.Background(), ResumeRequest{Checkpoint: next.Checkpoint, Decision: true})
	if err != nil || final.Text != "done" || first != 1 || second != 1 {
		t.Fatalf("unexpected completion: %+v first=%d second=%d err=%v", final, first, second, err)
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
type finalTextStream struct{ sent bool }

func (s *finalTextStream) Recv(context.Context) (model.StreamEvent, error) {
	if s.sent {
		return model.StreamEvent{}, io.EOF
	}
	s.sent = true
	return model.StreamEvent{TextDelta: "stream final"}, nil
}
func (s *finalTextStream) Close() error { return nil }

func (m *streamModel) ID() string { return "stream" }
func (m *streamModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true, Streaming: true}
}
func (m *streamModel) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	return &model.ModelOutput{Text: "stream final"}, nil
}
func (m *streamModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	m.calls++
	if m.calls == 1 {
		return &streamStub{}, nil
	}
	return &finalTextStream{}, nil
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
	for {
		event, recvErr := stream.Recv(context.Background())
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		if event.Response != nil {
			if event.Response.Text != "stream final" || event.Response.Stats.ModelCalls != 2 {
				t.Fatalf("unexpected stream completion: %+v", event)
			}
			break
		}
	}
}

func TestAgentStreamReportsPartialFailureStats(t *testing.T) {
	weather := tool.Func("weather.get", "weather", func(context.Context, struct {
		City string `json:"city"`
	}) (string, error) {
		return "", errors.New("provider failed")
	})
	a, err := New(WithModel(&streamModel{}), WithTools(weather))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := a.Stream(context.Background(), Request{Input: "weather"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for {
		event, recvErr := stream.Recv(context.Background())
		if recvErr != nil {
			t.Fatalf("failed before failure event: %v", recvErr)
		}
		if event.Type == EventRunFailed {
			if event.Response == nil || event.Response.Outcome != contract.OutcomeFailed || event.Response.Stats.ToolCalls != 1 || event.Response.Stats.FailedTools != 1 {
				t.Fatalf("invalid partial failure response: %+v", event.Response)
			}
			break
		}
	}
}

type dynamicToolModel struct{}

func (dynamicToolModel) ID() string { return "dynamic" }
func (dynamicToolModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true}
}
func (dynamicToolModel) Generate(_ context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	if len(input.Messages) > 0 && input.Messages[len(input.Messages)-1].Role == model.RoleTool {
		return &model.ModelOutput{Text: "done"}, nil
	}
	if len(input.Tools) == 0 {
		return &model.ModelOutput{Text: "no tools"}, nil
	}
	return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{toolCall("dynamic", input.Tools[0].Function.Name)}}, nil
}
func (dynamicToolModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestRunToolsAndMiddlewareAreRequestScoped(t *testing.T) {
	called, modelWrapped, toolWrapped := 0, 0, 0
	dynamic := tool.Func("dynamic", "dynamic", func(context.Context, struct{}) (string, error) { called++; return "ok", nil })
	modelMW := func(next middleware.ModelHandler) middleware.ModelHandler {
		return func(ctx context.Context, input model.ModelInput) (*model.ModelOutput, error) {
			modelWrapped++
			return next(ctx, input)
		}
	}
	toolMW := func(next middleware.ToolHandler) middleware.ToolHandler {
		return func(ctx context.Context, input middleware.ToolInvocation) (tool.Result, error) {
			toolWrapped++
			return next(ctx, input)
		}
	}
	mode := RunModeAgentSimple
	a, err := New(WithModel(dynamicToolModel{}))
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.Run(context.Background(), Request{Input: "use", Mode: &mode}, WithRunTools(dynamic), WithRunModelMiddleware(modelMW), WithRunToolMiddleware(toolMW))
	if err != nil || first.Text != "done" || called != 1 || modelWrapped != 2 || toolWrapped != 1 {
		t.Fatalf("unexpected scoped run: %+v counts=%d/%d/%d err=%v", first, called, modelWrapped, toolWrapped, err)
	}
	second, err := a.Run(context.Background(), Request{Input: "again", Mode: &mode})
	if err != nil || second.Text != "no tools" || called != 1 {
		t.Fatalf("run tool leaked: %+v called=%d err=%v", second, called, err)
	}
}

func TestCloseRejectsNewRunsAndDuplicateRunIDs(t *testing.T) {
	mode := RunModeAgentSimple
	a, err := New(WithModel(dynamicToolModel{}), WithRunIDGenerator(func() (string, error) { return "same", nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), Request{Input: "one", Mode: &mode}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), Request{Input: "two", Mode: &mode}); err == nil {
		t.Fatal("expected duplicate run id rejection")
	} else {
		var structured *runtimeerr.Error
		if !errors.As(err, &structured) {
			t.Fatalf("expected structured error, got %T", err)
		}
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), Request{Input: "three", Mode: &mode}); !errors.Is(err, ErrAgentClosed) {
		t.Fatal("expected closed agent rejection")
	}
}

func TestBudgetErrorsSupportErrorsIs(t *testing.T) {
	weather := tool.Func("weather.get", "weather", func(context.Context, struct {
		City string `json:"city"`
	}) (string, error) {
		return "sunny", nil
	})
	mode := RunModeAgentSimple
	a, err := New(WithModel(&scriptedModel{}), WithTools(weather), WithBudget(Budget{MaxModelCalls: 1}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), Request{Input: "weather", Mode: &mode})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected budget sentinel, got %v", err)
	}
}

func TestResumeUsesRunScopedToolSnapshot(t *testing.T) {
	calls := 0
	dynamicWrite := tool.Func("weather.get", "write", func(context.Context, struct {
		City string `json:"city"`
	}) (string, error) {
		calls++
		return "done", nil
	}, tool.WithSideEffect(tool.SideEffectWrite), tool.WithConfirmation(tool.ConfirmationAlways))
	mode := RunModeAgentSimple
	a, err := New(WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := a.Run(context.Background(), Request{Input: "write", Mode: &mode}, WithRunTools(dynamicWrite))
	if err != nil || !suspended.Suspended || calls != 0 {
		t.Fatalf("unexpected suspension: %+v calls=%d err=%v", suspended, calls, err)
	}
	resumed, err := a.Resume(context.Background(), ResumeRequest{RunID: suspended.RunID, Checkpoint: suspended.Checkpoint, Decision: true}, WithRunTools(dynamicWrite))
	if err != nil || resumed.Suspended || calls != 1 || resumed.Text == "" {
		t.Fatalf("unexpected dynamic resume: %+v calls=%d err=%v", resumed, calls, err)
	}
}
