package zhizhi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/checkpoint"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/skill"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type skillModel struct {
	generate func(model.ModelInput) (*model.ModelOutput, error)
}

func TestSkillGrantsSurviveOrdinaryCheckpoint(t *testing.T) {
	c := skillFixture(t)
	stage := 0
	var selection string
	m := &skillModel{generate: func(in model.ModelInput) (*model.ModelOutput, error) {
		stage++
		switch stage {
		case 1:
			return skillCall("skills.search", map[string]string{"query": "test"}), nil
		case 2:
			var out struct {
				SelectionID string `json:"selection_id"`
			}
			_ = json.Unmarshal([]byte(in.Messages[len(in.Messages)-1].Content), &out)
			selection = out.SelectionID
			return skillCall("ordinary", struct{}{}), nil
		case 3:
			return skillCall("skills.invoke", map[string]string{"selection_id": selection, "skill_name": "test-workflow", "task": "run"}), nil
		default:
			return &model.ModelOutput{Text: "done"}, nil
		}
	}}
	ordinary := tool.Func("ordinary", "requires approval", func(context.Context, struct{}) (string, error) { return "ok", nil }, tool.WithConfirmation(tool.ConfirmationAlways))
	store := checkpoint.NewMemoryStore()
	makeAgent := func() Agent {
		a, err := New(WithModel(m), WithSkillProvider(c, SkillOptions{}), WithTools(ordinary), WithCheckpointStore(store))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	mode := RunModeAgentSimple
	out, err := makeAgent().Run(context.Background(), Request{Input: "run", Mode: &mode})
	if err != nil || !out.Suspended {
		t.Fatalf("%+v %v", out, err)
	}
	final, err := makeAgent().Resume(context.Background(), ResumeRequest{Checkpoint: out.Checkpoint, Decision: true})
	if err != nil || final.Text != "done" || stage != 5 {
		t.Fatalf("%+v %v stage=%d", final, err, stage)
	}
}

func TestSkillComplexDirectResumePreservesEarlierActions(t *testing.T) {
	c := skillFixture(t)
	writes := 0
	childStage := 0
	m := &skillModel{generate: func(in model.ModelInput) (*model.ModelOutput, error) {
		if len(in.Tools) == 0 {
			return &model.ModelOutput{Text: "done"}, nil
		}
		childStage++
		if childStage <= 2 {
			return skillCall("write", map[string]int{"value": childStage}), nil
		}
		return &model.ModelOutput{Text: "done"}, nil
	}}
	write := tool.Func("write", "write", func(context.Context, struct {
		Value int `json:"value"`
	}) (string, error) {
		writes++
		return "written", nil
	}, tool.WithConfirmation(tool.ConfirmationAlways), tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyNonIdempotent))
	p := plannerFunc(func(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
		return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "search", Capability: "skills.search", Input: map[string]any{"query": "test"}}, {ID: "invoke", Capability: "skills.invoke", DependsOn: []string{"search"}, Input: map[string]any{"skill_name": "test-workflow", "task": "run"}, Bindings: []plan.InputBinding{{SourceStep: "search", SourcePath: "/selection_id", TargetPath: "/selection_id"}}}}}, nil
	})
	a, err := New(WithModel(m), WithSkillProvider(c, SkillOptions{AllowedTools: []string{"write"}}), WithTools(write), WithPlanner(p))
	if err != nil {
		t.Fatal(err)
	}
	mode := RunModeAgentComplex
	out, err := a.Run(context.Background(), Request{Input: "run", Mode: &mode})
	if err != nil || out == nil || !out.Suspended {
		t.Fatalf("%+v %v", out, err)
	}
	out, err = a.Resume(context.Background(), ResumeRequest{Checkpoint: out.Checkpoint, Decision: true})
	if err != nil || out == nil || !out.Suspended || writes != 1 {
		t.Fatalf("%+v %v writes=%d", out, err, writes)
	}
	out, err = a.Resume(context.Background(), ResumeRequest{Checkpoint: out.Checkpoint, Decision: true})
	if err != nil || out == nil || out.Suspended || writes != 2 || len(out.Actions) != 2 {
		t.Fatalf("%+v %v writes=%d", out, err, writes)
	}
}

func TestSkillResumeRejectsInstructionDrift(t *testing.T) {
	c := skillFixture(t)
	s := 0
	m := &skillModel{generate: func(in model.ModelInput) (*model.ModelOutput, error) {
		s++
		switch s {
		case 1:
			return skillCall("skills.search", map[string]string{"query": "test"}), nil
		case 2:
			var found skillSearchOutput
			_ = json.Unmarshal([]byte(in.Messages[len(in.Messages)-1].Content), &found)
			return skillCall("skills.invoke", skillInvokeInput{SelectionID: found.SelectionID, SkillName: "test-workflow", Task: "run"}), nil
		default:
			return skillCall("write", struct{}{}), nil
		}
	}}
	a, err := New(WithModel(m), WithSkillProvider(c, SkillOptions{AllowedTools: []string{"write"}}), WithTools(tool.Func("write", "write", func(context.Context, struct{}) (string, error) { t.Fatal("drift dispatched write"); return "", nil }, tool.WithConfirmation(tool.ConfirmationAlways))))
	if err != nil {
		t.Fatal(err)
	}
	mode := RunModeAgentSimple
	out, err := a.Run(context.Background(), Request{Input: "run", Mode: &mode})
	if err != nil || !out.Suspended {
		t.Fatalf("%+v %v", out, err)
	}
	if err := os.WriteFile(c.Metadata()[0].SkillFile, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = a.Resume(context.Background(), ResumeRequest{Checkpoint: out.Checkpoint, Decision: true})
	if !errors.Is(err, skill.ErrCatalogDrift) {
		t.Fatalf("%v", err)
	}
}

// A real streaming adapter around a scripted provider exercises eventStream,
// including Skill suspension, without networking.
type skillStreamModel struct{ *skillModel }

func (m *skillStreamModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true, Streaming: true}
}
func (m *skillStreamModel) Stream(ctx context.Context, in model.ModelInput) (model.Stream, error) {
	out, err := m.Generate(ctx, in)
	if err != nil {
		return nil, err
	}
	return &skillModelStream{output: out}, nil
}

type skillModelStream struct {
	output *model.ModelOutput
	done   bool
}

func (s *skillModelStream) Recv(context.Context) (model.StreamEvent, error) {
	if s.done {
		return model.StreamEvent{}, io.EOF
	}
	s.done = true
	event := model.StreamEvent{TextDelta: s.output.Text, Usage: &s.output.Usage}
	for i, c := range s.output.ToolCalls {
		event.ToolCallDeltas = append(event.ToolCallDeltas, model.ToolCallDelta{Index: i, ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return event, nil
}
func (s *skillModelStream) Close() error { return nil }

func (m *skillModel) ID() string { return "skill-test" }
func (m *skillModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true}
}
func (m *skillModel) Generate(_ context.Context, in model.ModelInput) (*model.ModelOutput, error) {
	out, err := m.generate(in)
	if out != nil {
		out.Usage = model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}
	}
	return out, err
}
func (m *skillModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	panic("not used")
}
func skillCall(name string, args any) *model.ModelOutput {
	b, _ := json.Marshal(args)
	call := model.ToolCallRequest{ID: name, Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = string(b)
	return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{call}}
}
func skillFixture(t *testing.T) *skill.Catalog {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: test-workflow\ndescription: run test workflow\n---\nPRIVATE INSTRUCTIONS: read then write."), 0600); err != nil {
		t.Fatal(err)
	}
	c := skill.NewCatalog(skill.CatalogOptions{})
	if _, err := c.LoadRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSkillInvocationSuspendsAndResumesWithoutRepeatingRead(t *testing.T) {
	for _, variant := range []string{"simple", "stream", "agentic"} {
		t.Run(variant, func(t *testing.T) {
			c := skillFixture(t)
			reads, writes, stage := 0, 0, 0
			m := &skillModel{generate: func(in model.ModelInput) (*model.ModelOutput, error) {
				stage++
				switch stage {
				case 1:
					b, _ := json.Marshal(in)
					if strings.Contains(string(b), "PRIVATE INSTRUCTIONS") || strings.Contains(string(b), "test-workflow") {
						t.Fatal("catalog leaked into model input")
					}
					return skillCall("skills.search", map[string]any{"query": "test workflow"}), nil
				case 2:
					var found struct {
						SelectionID string `json:"selection_id"`
					}
					if err := json.Unmarshal([]byte(in.Messages[len(in.Messages)-1].Content), &found); err != nil {
						t.Fatal(err)
					}
					return skillCall("skills.invoke", map[string]any{"selection_id": found.SelectionID, "skill_name": "test-workflow", "task": "read then write"}), nil
				case 3:
					for _, spec := range in.Tools {
						if spec.Function.Name == "denied" || spec.Function.Name == "skills.invoke" {
							t.Fatal("child permissions escaped")
						}
					}
					return skillCall("read", struct{}{}), nil
				case 4:
					return skillCall("write", struct{}{}), nil
				default:
					return &model.ModelOutput{Text: "done"}, nil
				}
			}}
			var provider model.Model = m
			if variant == "stream" {
				provider = &skillStreamModel{m}
			}
			p := plannerFunc(func(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
				return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "workflow", Mode: plan.StepModeAgentic, Goal: "run workflow", SuccessCriteria: "workflow finished", Capabilities: []string{"skills.search", "skills.invoke"}, ToolBudget: plan.ToolCallBudget{Max: 8}}}}, nil
			})
			a, err := New(WithModel(provider), WithPlanner(p), WithSkillProvider(c, SkillOptions{AllowedTools: []string{"read", "write"}}), WithTools(
				tool.Func("read", "read", func(context.Context, struct{}) (string, error) { reads++; return "fact", nil }),
				tool.Func("write", "write", func(context.Context, struct{}) (string, error) { writes++; return "receipt", nil }, tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyNonIdempotent), tool.WithConfirmation(tool.ConfirmationAlways)),
				tool.Func("denied", "denied", func(context.Context, struct{}) (string, error) { t.Fatal("denied tool called"); return "", nil }),
			))
			if err != nil {
				t.Fatal(err)
			}
			mode := RunModeAgentSimple
			if variant == "agentic" {
				mode = RunModeAgentComplex
			}
			var first *Response
			if variant == "stream" {
				var stream EventStream
				stream, err = a.Stream(context.Background(), Request{Input: "run workflow", Mode: &mode})
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				for {
					event, e := stream.Recv(context.Background())
					if e == io.EOF {
						break
					}
					if e != nil {
						t.Fatal(e)
					}
					if event.Response != nil {
						first = event.Response
					}
				}
			} else {
				first, err = a.Run(context.Background(), Request{Input: "run workflow", Mode: &mode})
			}
			if err != nil || first == nil || !first.Suspended || reads != 1 || writes != 0 {
				t.Fatalf("%+v %v reads=%d writes=%d", first, err, reads, writes)
			}
			final, err := a.Resume(context.Background(), ResumeRequest{Checkpoint: first.Checkpoint, Decision: true})
			if err != nil || final == nil || final.Suspended || reads != 1 || writes != 1 {
				t.Fatalf("%+v %v reads=%d writes=%d", final, err, reads, writes)
			}
			if len(final.Actions) != 1 || final.Actions[0].ToolID != "write" {
				t.Fatalf("lost nested actions: %+v", final.Actions)
			}
			if final.Usage.TotalTokens != stage*2 {
				t.Fatalf("nested usage lost: %+v stage=%d", final.Usage, stage)
			}
			if _, err := a.Resume(context.Background(), ResumeRequest{Checkpoint: first.Checkpoint, Decision: true}); err == nil || writes != 1 {
				t.Fatalf("approval replayed: %v writes=%d", err, writes)
			}
		})
	}
}

func TestSkillSelectionDoesNotCrossRuns(t *testing.T) {
	c := skillFixture(t)
	var selection string
	stage := 0
	m := &skillModel{generate: func(in model.ModelInput) (*model.ModelOutput, error) {
		stage++
		if stage == 1 {
			return skillCall("skills.search", map[string]string{"query": "test"}), nil
		}
		if stage == 2 {
			var result struct {
				SelectionID string `json:"selection_id"`
			}
			_ = json.Unmarshal([]byte(in.Messages[len(in.Messages)-1].Content), &result)
			selection = result.SelectionID
			return &model.ModelOutput{Text: "found"}, nil
		}
		if stage == 3 {
			return skillCall("skills.invoke", map[string]string{"selection_id": selection, "skill_name": "test-workflow", "task": "run"}), nil
		}
		return &model.ModelOutput{Text: in.Messages[len(in.Messages)-1].Content}, nil
	}}
	a, err := New(WithModel(m), WithSkillProvider(c, SkillOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	mode := RunModeAgentSimple
	if _, err := a.Run(context.Background(), Request{Input: "search", Mode: &mode}); err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(context.Background(), Request{Input: "invoke", Mode: &mode})
	if err != nil || !strings.Contains(out.Text, "selection_mismatch") {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestSkillFailureRetainsActionsAndSharesBudget(t *testing.T) {
	for _, budgetLimit := range []int{0, 3, -1} {
		t.Run(fmt.Sprint(budgetLimit), func(t *testing.T) {
			c := skillFixture(t)
			stage, writes := 0, 0
			m := &skillModel{generate: func(in model.ModelInput) (*model.ModelOutput, error) {
				stage++
				switch stage {
				case 1:
					return skillCall("skills.search", skillSearchInput{Query: "test"}), nil
				case 2:
					var found skillSearchOutput
					_ = json.Unmarshal([]byte(in.Messages[len(in.Messages)-1].Content), &found)
					return skillCall("skills.invoke", skillInvokeInput{SelectionID: found.SelectionID, SkillName: "test-workflow", Task: "run"}), nil
				case 3:
					return skillCall("write", struct{}{}), nil
				default:
					if budgetLimit == -1 && stage == 4 {
						return &model.ModelOutput{Text: "child done"}, nil
					}
					return nil, errors.New("provider unavailable")
				}
			}}
			a, err := New(WithModel(m), WithBudget(Budget{MaxModelCalls: budgetLimit}), WithSkillProvider(c, SkillOptions{AllowedTools: []string{"write"}}), WithTools(tool.Func("write", "write", func(context.Context, struct{}) (string, error) { writes++; return "receipt", nil }, tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyNonIdempotent))))
			if err != nil {
				t.Fatal(err)
			}
			mode := RunModeAgentSimple
			out, err := a.Run(context.Background(), Request{Input: "run", Mode: &mode})
			if err == nil || out == nil || writes != 1 || len(out.Actions) != 1 || out.Stats.ToolCalls != 3 {
				t.Fatalf("%+v %v writes=%d", out, err, writes)
			}
			if budgetLimit > 0 && out.Stats.ModelCalls != 3 {
				t.Fatalf("nested calls escaped request budget: %+v", out.Stats)
			}
		})
	}
}

func TestSkillComplexFailurePreservesNestedEvidence(t *testing.T) {
	for _, agentic := range []bool{false, true} {
		t.Run(fmt.Sprint(agentic), func(t *testing.T) {
			c := skillFixture(t)
			childStage, parentStage := 0, 0
			m := &skillModel{generate: func(in model.ModelInput) (*model.ModelOutput, error) {
				if len(in.Tools) == 0 {
					return &model.ModelOutput{Text: "partial result"}, nil
				}
				for _, spec := range in.Tools {
					if spec.Function.Name == "skills.search" {
						parentStage++
						if parentStage == 1 {
							return skillCall("skills.search", skillSearchInput{Query: "test"}), nil
						}
						var found skillSearchOutput
						_ = json.Unmarshal([]byte(in.Messages[len(in.Messages)-1].Content), &found)
						return skillCall("skills.invoke", skillInvokeInput{SelectionID: found.SelectionID, SkillName: "test-workflow", Task: "run"}), nil
					}
				}
				childStage++
				switch childStage {
				case 1:
					return skillCall("read", struct{}{}), nil
				case 2:
					return skillCall("write", struct{}{}), nil
				default:
					return nil, errors.New("model unavailable")
				}
			}}
			p := plannerFunc(func(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
				if agentic {
					return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "run", Mode: plan.StepModeAgentic, Goal: "run", SuccessCriteria: "done", Capabilities: []string{"skills.search", "skills.invoke"}, ToolBudget: plan.ToolCallBudget{Max: 8}}}}, nil
				}
				return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "find", Capability: "skills.search", Input: map[string]any{"query": "test"}}, {ID: "run", Capability: "skills.invoke", DependsOn: []string{"find"}, Input: map[string]any{"skill_name": "test-workflow", "task": "run"}, Bindings: []plan.InputBinding{{SourceStep: "find", SourcePath: "/selection_id", TargetPath: "/selection_id"}}}}}, nil
			})
			a, err := New(WithModel(m), WithPlanner(p), WithSkillProvider(c, SkillOptions{AllowedTools: []string{"read", "write"}}), WithTools(tool.Func("read", "read", func(context.Context, struct{}) (string, error) { return "known fact", nil }), tool.Func("write", "write", func(context.Context, struct{}) (string, error) { return "receipt", nil }, tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyNonIdempotent))))
			if err != nil {
				t.Fatal(err)
			}
			mode := RunModeAgentComplex
			out, err := a.Run(context.Background(), Request{Input: "run", Mode: &mode})
			if err != nil || out == nil {
				t.Fatalf("%+v %v", out, err)
			}
			found := false
			for _, e := range out.Evidence {
				if e.ToolID == "read" && e.Content == "known fact" {
					found = true
				}
			}
			if !found || len(out.Actions) != 1 {
				t.Fatalf("nested facts lost: %+v", out)
			}
		})
	}
}

type failingSkillStore struct{ checkpoint.Store }

func (failingSkillStore) Save(context.Context, checkpoint.Checkpoint) error {
	return errors.New("storage unavailable")
}

func TestSkillCheckpointFailureReturnsCompletedWrite(t *testing.T) {
	for _, variant := range []string{"simple", "stream", "complex"} {
		t.Run(variant, func(t *testing.T) {
			c := skillFixture(t)
			stage, writes := 0, 0
			m := &skillModel{generate: func(in model.ModelInput) (*model.ModelOutput, error) {
				stage++
				switch stage {
				case 1:
					return skillCall("skills.search", skillSearchInput{Query: "test"}), nil
				case 2:
					var found skillSearchOutput
					_ = json.Unmarshal([]byte(in.Messages[len(in.Messages)-1].Content), &found)
					return skillCall("skills.invoke", skillInvokeInput{SelectionID: found.SelectionID, SkillName: "test-workflow", Task: "run"}), nil
				case 3:
					return skillCall("write", struct{}{}), nil
				default:
					return skillCall("confirm", struct{}{}), nil
				}
			}}
			var provider model.Model = m
			if variant == "stream" {
				provider = &skillStreamModel{m}
			}
			p := plannerFunc(func(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
				return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "run", Mode: plan.StepModeAgentic, Goal: "run", SuccessCriteria: "done", Capabilities: []string{"skills.search", "skills.invoke"}, ToolBudget: plan.ToolCallBudget{Max: 8}}}}, nil
			})
			a, err := New(WithModel(provider), WithPlanner(p), WithSkillProvider(c, SkillOptions{AllowedTools: []string{"write", "confirm"}}), WithCheckpointStore(failingSkillStore{checkpoint.NewMemoryStore()}), WithTools(tool.Func("write", "write", func(context.Context, struct{}) (string, error) { writes++; return "receipt", nil }, tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyNonIdempotent)), tool.Func("confirm", "confirm", func(context.Context, struct{}) (string, error) { t.Fatal("unapproved dispatch"); return "", nil }, tool.WithConfirmation(tool.ConfirmationAlways))))
			if err != nil {
				t.Fatal(err)
			}
			mode := RunModeAgentSimple
			if variant == "complex" {
				mode = RunModeAgentComplex
			}
			var out *Response
			if variant == "stream" {
				stream, e := a.Stream(context.Background(), Request{Input: "run", Mode: &mode})
				if e != nil {
					t.Fatal(e)
				}
				defer stream.Close()
				for {
					event, e := stream.Recv(context.Background())
					if event.Response != nil {
						out = event.Response
					}
					if e != nil {
						err = e
						break
					}
				}
			} else {
				out, err = a.Run(context.Background(), Request{Input: "run", Mode: &mode})
			}
			if err == nil || out == nil || len(out.Actions) != 1 || writes != 1 {
				t.Fatalf("%+v %v writes=%d", out, err, writes)
			}
		})
	}
}
