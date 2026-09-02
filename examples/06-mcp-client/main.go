package main

import (
	"context"
	"fmt"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/mcp"
	"os"
)

func main() {
	provider, err := mcp.Connect(context.Background(), mcp.Config{ID: "remote", Endpoint: os.Getenv("MCP_DEMO_URL"), AllowTools: []string{"greet"}})
	if err != nil {
		panic(err)
	}
	defer provider.Close()
	for _, tool := range provider.Tools() {
		fmt.Println(tool.Spec().ID)
	}
}
