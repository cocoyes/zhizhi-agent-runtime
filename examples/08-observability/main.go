package main

import (
	"bytes"
	"context"
	"fmt"
	"os"

	zhizhi "github.com/zhizhi-ai/zhizhi-agent-runtime"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/adapter/model/openaicompat"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/observe"
)

func main() {
	var trace bytes.Buffer
	observer := observe.NewJSONL(&trace)
	a, err := zhizhi.New(zhizhi.WithModel(
		openaicompat.New(openaicompat.Config{BaseURL: os.Getenv("LLM_BASE_URL"),
			APIKey: os.Getenv("LLM_API_KEY"), Model: os.Getenv("LLM_MODEL")})),
		zhizhi.WithObserver(observer))
	if err != nil {
		panic(err)
	}
	mode := zhizhi.RunModeChat
	resp, _ := a.Run(context.Background(), zhizhi.Request{Input: "你好呀", Mode: &mode})
	fmt.Print(trace.String())
	fmt.Println("final:", resp.Text)
}
