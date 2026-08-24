// Package mcp is the engine's Model Context Protocol *client* layer: it
// connects to external MCP servers and exposes what they offer as plain
// Go values.
//
// Naming, because it inverts: the engine is the CLIENT. An MCP "server"
// is normally a small local program (`npx -y
// @modelcontextprotocol/server-filesystem`, a company's internal
// knowledge-base binary) that serves tool definitions and executes tool
// calls on request.
//
// Only the stdio transport is implemented. The engine spawns the server
// as a child process and exchanges JSON-RPC 2.0 over its stdin/stdout —
// no sockets, no ports, no TLS. config.SanitizeMCPServers rejects any
// other transport before a Manager ever sees it, so this package can
// assume stdio throughout. URL-based transports (streamable HTTP / SSE)
// slot in behind the same Manager surface later.
//
// Deliberately absent from this package: any knowledge of the engine's
// tool.Tool interface, permission pipeline, or event envelopes. Manager
// hands out ToolDescriptor values and takes CallTool requests; adapting
// those to a tool.Tool the LLM can invoke is
// internal/tools/mcptool's job. Keeping the split means this package is
// testable against a fake MCP server with no engine wiring at all, and
// the dependency runs one way: tools/mcptool → mcp.
package mcp
