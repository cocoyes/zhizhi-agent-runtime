package main

import (
	"context"
	"fmt"
	"os"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"github.com/cocoyes/zhizhi-agent-runtime/adapter/model/openaicompat"
)

func main() {
	a, err := zhizhi.New(
		zhizhi.WithModel(openaicompat.New(openaicompat.Config{BaseURL: os.Getenv("LLM_BASE_URL"),
			APIKey: os.Getenv("LLM_API_KEY"),
			Model:  os.Getenv("LLM_MODEL")})),
		zhizhi.WithSystemPrompt("You are a helpful personal assistant."))
	if err != nil {
		panic(err)
	}
	defer a.Close(context.Background())
	resp, err := a.Run(context.Background(), zhizhi.Request{Input: "订单被取消了怎么回事"})
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Text)
}
