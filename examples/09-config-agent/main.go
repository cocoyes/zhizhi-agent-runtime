package main

import (
	"context"
	"fmt"
	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"os"
)

func main() {
	a, err := zhizhi.Load("agent.yaml")
	if err != nil {
		panic(err)
	}
	resp, err := a.Run(context.Background(), zhizhi.Request{Input: os.Getenv("AGENT_INPUT")})
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Text)
}
