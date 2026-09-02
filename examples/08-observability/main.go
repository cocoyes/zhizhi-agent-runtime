package main

import (
	"bytes"
	"context"
	"fmt"
	zhizhi "github.com/zhizhi-ai/zhizhi-agent-runtime"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/adapter/model/openaicompat"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/observe"
	"os"
)

func main() {
	var trace bytes.Buffer
	observer := observe.NewJSONL(&trace)
	a, err := zhizhi.New(zhizhi.WithModel(openaicompat.New(openaicompat.Config{BaseURL: os.Getenv("LLM_BASE_URL"), APIKey: os.Getenv("LLM_API_KEY"), Model: os.Getenv("LLM_MODEL")})), zhizhi.WithObserver(observer))
	if err != nil {
		panic(err)
	}
	_, _ = a.Run(context.Background(), zhizhi.Request{Input: "Say hello"})
	fmt.Print(trace.String())
}
