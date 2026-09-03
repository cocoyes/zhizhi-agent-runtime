package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/cocoyes/zhizhi-agent-runtime/policy"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

func main() {
	first := tool.Func("primary", "primary", func(context.Context, struct{}) (string, error) { return "", errors.New("unavailable") }, tool.WithCapabilities("search"))
	second := tool.Func("backup", "backup", func(context.Context, struct{}) (string, error) { return "backup", nil }, tool.WithCapabilities("search"))
	result, err := (policy.Runner{}).Execute(context.Background(), []tool.Tool{first, second}, []byte(`{}`))
	fmt.Printf("tool=%s fallbacks=%d err=%v\n", result.ToolID, result.Fallbacks, err)
}
