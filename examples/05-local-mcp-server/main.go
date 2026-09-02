package main

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
)

type input struct {
	Name string `json:"name"`
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "zhizhi-fixture", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "Greet a person"}, func(_ context.Context, _ *mcp.CallToolRequest, in input) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello " + in.Name}}}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	_ = http.ListenAndServe(":8099", handler)
}
