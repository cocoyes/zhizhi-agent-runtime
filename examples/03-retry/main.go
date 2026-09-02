package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/policy"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
)

func main() {
	attempts := 0
	read := tool.Func("read", "read", func(context.Context, struct{}) (string, error) {
		attempts++
		if attempts < 2 {
			return "", errors.New("temporary timeout")
		}
		return "ok", nil
	}, tool.WithRetryable(true))
	result, err := (policy.Runner{}).Execute(context.Background(), []tool.Tool{read}, []byte(`{}`))
	fmt.Printf("tool=%s attempts=%d err=%v\n", result.ToolID, result.Attempts, err)
}
