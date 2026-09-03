package main

import (
	"context"
	"fmt"
	"os"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"github.com/cocoyes/zhizhi-agent-runtime/adapter/model/openaicompat"
)

func main() {
	a, err := zhizhi.New(zhizhi.WithModel(openaicompat.New(openaicompat.Config{BaseURL: os.Getenv("LLM_BASE_URL"), APIKey: os.Getenv("LLM_API_KEY"), Model: os.Getenv("LLM_MODEL")})))
	if err != nil {
		panic(err)
	}
	s, err := a.Stream(context.Background(), zhizhi.Request{Input: "给我讲个故事吧"})
	if err != nil {
		panic(err)
	}
	defer s.Close()
	for {
		event, err := s.Recv(context.Background())
		if err != nil {
			break
		}
		fmt.Print(event.TextDelta)
	}
}
