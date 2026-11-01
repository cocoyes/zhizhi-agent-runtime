// Example 14 demonstrates lazy Skill discovery with a deterministic offline model.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/skill"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type offlineModel struct{ stage int }

func (*offlineModel) ID() string { return "offline-skills-example" }
func (*offlineModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{Declared: true, ToolCalling: true}
}
func call(name string, args any) *model.ModelOutput {
	b, _ := json.Marshal(args)
	c := model.ToolCallRequest{ID: name, Type: "function"}
	c.Function.Name = name
	c.Function.Arguments = string(b)
	return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{c}}
}
func (m *offlineModel) Generate(_ context.Context, in model.ModelInput) (*model.ModelOutput, error) {
	m.stage++
	switch m.stage {
	case 1:
		return call("skills.search", map[string]string{"query": "greeting"}), nil
	case 2:
		var found struct {
			SelectionID string `json:"selection_id"`
		}
		if err := json.Unmarshal([]byte(in.Messages[len(in.Messages)-1].Content), &found); err != nil {
			return nil, err
		}
		return call("skills.invoke", map[string]string{"selection_id": found.SelectionID, "skill_name": "greeting", "task": "Greet Ada"}), nil
	case 3:
		return call("greeting.compose", map[string]string{"name": "Ada"}), nil
	default:
		return &model.ModelOutput{Text: "Hello, Ada!"}, nil
	}
}
func (*offlineModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, fmt.Errorf("offline example uses Run")
}

func main() {
	ctx := context.Background()
	_, file, _, _ := runtime.Caller(0)
	catalog := skill.NewCatalog(skill.CatalogOptions{})
	report, err := catalog.LoadRoot(ctx, filepath.Join(filepath.Dir(file), "skills"))
	if err != nil {
		panic(err)
	}
	greeting := tool.Func("greeting.compose", "Compose a greeting", func(_ context.Context, in struct {
		Name string `json:"name"`
	}) (string, error) {
		return "Hello, " + in.Name + "!", nil
	})
	agent, err := zhizhi.New(zhizhi.WithModel(&offlineModel{}), zhizhi.WithTools(greeting), zhizhi.WithSkillProvider(catalog, zhizhi.SkillOptions{AllowedTools: []string{"greeting.compose"}}))
	if err != nil {
		panic(err)
	}
	defer agent.Close(ctx)
	mode := zhizhi.RunModeAgentSimple
	response, err := agent.Run(ctx, zhizhi.Request{Input: "Greet Ada using an installed workflow", Mode: &mode})
	if err != nil {
		panic(err)
	}
	fmt.Printf("indexed=%d tool_calls=%d result=%s\n", report.Added, response.ToolCalls, response.Text)
}
