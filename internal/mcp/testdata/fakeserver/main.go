//go:build ignore

// Command fakeserver is a minimal stdio MCP server used by the
// integration test in internal/mcp. It is compiled on demand by the
// test (never as part of the normal build — hence the ignore tag) and
// speaks real JSON-RPC over stdin/stdout, so it exercises the production
// stdioDialer path rather than the in-process transport.
//
// It echoes one env var back through a tool result, which is what makes
// the env-case guarantee verifiable end to end: a server handed
// `github_token` instead of `GITHUB_TOKEN` would return an empty string.
package main

import (
	"context"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	s := server.NewMCPServer("fakeserver", "1.0.0")

	s.AddTool(
		mcp.Tool{
			Name:        "echo_env",
			Description: "Return the value of GITHUB_TOKEN as seen by this process.",
			InputSchema: mcp.ToolInputSchema{Type: "object"},
		},
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("token=" + os.Getenv("GITHUB_TOKEN")), nil
		},
	)

	if err := server.ServeStdio(s); err != nil {
		os.Exit(1)
	}
}
