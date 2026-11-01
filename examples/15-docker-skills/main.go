// Example 15 uses a deterministic tool-calling model and a real Docker Engine.
// Replace WithModel with an application model to let an LLM choose the same tools.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/skill"
	"github.com/cocoyes/zhizhi-agent-runtime/skill/script"
	dockerscript "github.com/cocoyes/zhizhi-agent-runtime/skill/script/docker"
)

type demoModel struct{ stage int }

func (*demoModel) ID() string { return "docker-tool-calling-demo" }
func (*demoModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{Declared: true, ToolCalling: true}
}
func call(name string, args any) *model.ModelOutput {
	raw, _ := json.Marshal(args)
	request := model.ToolCallRequest{ID: name, Type: "function"}
	request.Function.Name = name
	request.Function.Arguments = string(raw)
	return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{request}}
}
func (m *demoModel) Generate(_ context.Context, in model.ModelInput) (*model.ModelOutput, error) {
	m.stage++
	switch m.stage {
	case 1:
		return call("skills.search", map[string]string{"query": "audited Python hello"}), nil
	case 2:
		var search struct {
			SelectionID string `json:"selection_id"`
		}
		if err := json.Unmarshal([]byte(in.Messages[len(in.Messages)-1].Content), &search); err != nil {
			return nil, err
		}
		return call("skills.invoke", map[string]string{"selection_id": search.SelectionID, "skill_name": "test-skill", "task": "Run the approved hello script and return its report"}), nil
	case 3:
		return call("run_skill_script", map[string]any{"skill_id": "test-skill", "script_path": "scripts/hello.py", "args": []string{}}), nil
	default:
		return &model.ModelOutput{Text: "The approved Python script completed; inspect the tool evidence for its JSON and report."}, nil
	}
}
func (*demoModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, errors.New("demo uses Run")
}

func run() error {
	ctx := context.Background()
	_, file, _, _ := runtime.Caller(0)
	catalog := skill.NewCatalog(skill.CatalogOptions{})
	if _, err := catalog.LoadRoot(ctx, filepath.Join(filepath.Dir(file), "skills")); err != nil {
		return err
	}
	executor, err := dockerscript.New(ctx, dockerscript.Config{
		Enabled: true, Catalog: catalog, AutoPull: true, Image: os.Getenv("SKILL_PYTHON_IMAGE"),
		WorkspaceRoot: os.Getenv("SKILL_WORKSPACE_ROOT"), Timeout: 2 * time.Minute,
		Registrations: []script.Registration{{SkillID: "test-skill", Scripts: []script.EntryPoint{{Path: "scripts/hello.py", Version: "no-args-v1", ValidateArgs: func(args []string) error {
			if len(args) > 0 {
				return script.ErrPermission
			}
			return nil
		}}}}},
	})
	if err != nil {
		return err
	}
	defer executor.Close(ctx)
	agent, err := zhizhi.New(zhizhi.WithModel(&demoModel{}), zhizhi.WithSkillProvider(catalog, zhizhi.SkillOptions{ScriptExecutor: executor}))
	if err != nil {
		return err
	}
	defer agent.Close(ctx)
	mode := zhizhi.RunModeAgentSimple
	response, err := agent.Run(ctx, zhizhi.Request{Input: "Execute the hello Skill and produce a report", Mode: &mode})
	if err != nil {
		return err
	}
	fmt.Println(response.Text)
	for _, evidence := range response.Evidence {
		if evidence.ToolID != "run_skill_script" {
			continue
		}
		raw, err := json.Marshal(evidence.Content)
		if err != nil {
			return err
		}
		var result script.Result
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		fmt.Printf("exit=%d stdout=%s", result.ExitCode, result.Stdout)
		workspace := script.Workspace{ID: result.WorkspaceID, Owner: response.RunID}
		for _, file := range result.Files {
			reader, err := executor.OpenFile(ctx, workspace, file.Path)
			if err != nil {
				return err
			}
			content, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				return err
			}
			fmt.Printf("file=%s size=%d content=%s", file.Path, file.Size, content)
		}
	}
	// Outputs are retained by default. A service can upload them using OpenFile,
	// then call DeleteWorkspace with the same owner-bound reference.
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
