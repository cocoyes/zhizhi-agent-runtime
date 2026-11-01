package zhizhi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/checkpoint"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/skill"
	"github.com/cocoyes/zhizhi-agent-runtime/skill/script"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type fakeScriptExecutor struct {
	calls, created, released int
	workspace                script.Workspace
	requests                 []script.Request
	policy                   string
}

func (f *fakeScriptExecutor) Fingerprint() string {
	if f.policy != "" {
		return f.policy
	}
	return "trusted-script-policy"
}
func (f *fakeScriptExecutor) CreateWorkspace(_ context.Context, owner string) (script.Workspace, error) {
	f.created++
	f.workspace = script.Workspace{ID: "opaque-task", Owner: owner}
	return f.workspace, nil
}
func (f *fakeScriptExecutor) ValidateWorkspace(_ context.Context, w script.Workspace) error {
	if w != f.workspace {
		return fmt.Errorf("workspace mismatch")
	}
	return nil
}
func (f *fakeScriptExecutor) Execute(_ context.Context, r script.Request) (script.Result, error) {
	f.calls++
	f.requests = append(f.requests, r)
	return script.Result{Success: true, Code: script.CodeOK, Started: true, ExitCode: 0, Stdout: "Hello from Docker", Files: []script.File{{Name: "report.txt", Path: "output/report.txt", Size: 12}}, WorkspaceID: r.Workspace.ID}, nil
}
func (f *fakeScriptExecutor) ReleaseWorkspace(context.Context, script.Workspace) error {
	f.released++
	return nil
}
func (f *fakeScriptExecutor) OpenFile(context.Context, script.Workspace, string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("not used")
}
func (f *fakeScriptExecutor) Close(context.Context) error { return nil }

func TestScriptWorkspaceAndApprovalSurviveRepeatedSuspend(t *testing.T) {
	catalog := skillFixture(t)
	sandbox := &fakeScriptExecutor{}
	stage := 0
	m := &skillModel{generate: func(in model.ModelInput) (*model.ModelOutput, error) {
		stage++
		switch stage {
		case 1:
			return skillCall("skills.search", skillSearchInput{Query: "test"}), nil
		case 2:
			var found skillSearchOutput
			_ = json.Unmarshal([]byte(in.Messages[len(in.Messages)-1].Content), &found)
			return skillCall("skills.invoke", skillInvokeInput{SelectionID: found.SelectionID, SkillName: "test-workflow", Task: "run twice"}), nil
		case 3, 4:
			return skillCall("run_skill_script", scriptToolInput{SkillID: "test-workflow", ScriptPath: "scripts/hello.py"}), nil
		default:
			return &model.ModelOutput{Text: "done"}, nil
		}
	}}
	store := checkpoint.NewMemoryStore()
	create := func() Agent {
		a, err := New(WithModel(m), WithCheckpointStore(store), WithSkillProvider(catalog, SkillOptions{ScriptExecutor: sandbox, ScriptConfirmation: tool.ConfirmationAlways}))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	mode := RunModeAgentSimple
	first, err := create().Run(context.Background(), Request{Input: "run", Mode: &mode})
	if err != nil || !first.Suspended {
		t.Fatalf("%+v %v", first, err)
	}
	second, err := create().Resume(context.Background(), ResumeRequest{Checkpoint: first.Checkpoint, Decision: true})
	if err != nil || !second.Suspended || sandbox.calls != 1 || sandbox.released != 0 {
		t.Fatalf("%+v %v calls=%d released=%d", second, err, sandbox.calls, sandbox.released)
	}
	sandbox.policy = "changed-policy"
	if _, err := create().Resume(context.Background(), ResumeRequest{Checkpoint: second.Checkpoint, Decision: true}); !errors.Is(err, skill.ErrCatalogDrift) {
		t.Fatalf("policy change accepted: %v", err)
	}
	sandbox.policy = ""
	final, err := create().Resume(context.Background(), ResumeRequest{Checkpoint: second.Checkpoint, Decision: true})
	if err != nil || final.Suspended || sandbox.calls != 2 || sandbox.created != 1 || sandbox.released != 1 {
		t.Fatalf("%+v %v sandbox=%+v", final, err, sandbox)
	}
	if sandbox.requests[0].Workspace != sandbox.requests[1].Workspace {
		t.Fatal("task workspace changed on resume")
	}
}

func TestSkillScriptToolBoundToSelectedSkillAndExistingResume(t *testing.T) {
	for _, modeName := range []string{"simple", "stream", "complex"} {
		t.Run(modeName, func(t *testing.T) {
			for _, invalid := range []bool{false, true} {
				t.Run(fmt.Sprint(invalid), func(t *testing.T) {
					catalog := skillFixture(t)
					sandbox := &fakeScriptExecutor{}
					stage := 0
					m := &skillModel{generate: func(in model.ModelInput) (*model.ModelOutput, error) {
						stage++
						switch stage {
						case 1:
							for _, spec := range in.Tools {
								if spec.Function.Name == "run_skill_script" {
									t.Fatal("script tool exposed before Skill selection")
								}
							}
							return skillCall("skills.search", skillSearchInput{Query: "test"}), nil
						case 2:
							var found skillSearchOutput
							_ = json.Unmarshal([]byte(in.Messages[len(in.Messages)-1].Content), &found)
							return skillCall("skills.invoke", skillInvokeInput{SelectionID: found.SelectionID, SkillName: "test-workflow", Task: "run script"}), nil
						case 3:
							found := false
							for _, spec := range in.Tools {
								if spec.Function.Name == "run_skill_script" {
									found = true
									var schema map[string]any
									_ = json.Unmarshal(spec.Function.Parameters, &schema)
									properties := schema["properties"].(map[string]any)
									for _, forbidden := range []string{"env", "workspace_id", "image", "mounts"} {
										if _, ok := properties[forbidden]; ok {
											t.Fatalf("exposed privileged argument: %s", forbidden)
										}
									}
								}
							}
							if !found {
								t.Fatal("selected Skill missing script tool")
							}
							id := "test-workflow"
							if invalid {
								id = "other-skill"
							}
							return skillCall("run_skill_script", map[string]any{"skill_id": id, "script_path": "scripts/hello.py", "args": []string{"--name", "Ada"}}), nil
						default:
							return &model.ModelOutput{Text: "done"}, nil
						}
					}}
					var provider model.Model = m
					if modeName == "stream" {
						provider = &skillStreamModel{m}
					}
					p := plannerFunc(func(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
						return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "run", Mode: plan.StepModeAgentic, Goal: "run", SuccessCriteria: "done", Capabilities: []string{"skills.search", "skills.invoke"}, ToolBudget: plan.ToolCallBudget{Max: 8}}}}, nil
					})
					a, err := New(WithModel(provider), WithPlanner(p), WithSkillProvider(catalog, SkillOptions{ScriptExecutor: sandbox, ScriptConfirmation: tool.ConfirmationAlways}))
					if err != nil {
						t.Fatal(err)
					}
					mode := RunModeAgentSimple
					if modeName == "complex" {
						mode = RunModeAgentComplex
					}
					var out *Response
					if modeName == "stream" {
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
							if e == io.EOF {
								break
							}
							if e != nil {
								t.Fatal(e)
							}
						}
					} else {
						out, err = a.Run(context.Background(), Request{Input: "run", Mode: &mode})
					}
					if err != nil || out == nil || !out.Suspended || sandbox.calls != 0 {
						t.Fatalf("%+v %v", out, err)
					}
					if sandbox.released != 0 {
						t.Fatal("suspended workspace released")
					}
					out, err = a.Resume(context.Background(), ResumeRequest{Checkpoint: out.Checkpoint, Decision: true})
					if err != nil || out == nil || out.Suspended {
						t.Fatalf("%+v %v", out, err)
					}
					expected := 1
					if invalid {
						expected = 0
					}
					if sandbox.calls != expected {
						t.Fatalf("calls=%d", sandbox.calls)
					}
					if !invalid && (sandbox.created != 1 || sandbox.released != 1 || sandbox.requests[0].Workspace.Owner == "") {
						t.Fatalf("task lifecycle: %+v", sandbox)
					}
				})
			}
		})
	}
}
