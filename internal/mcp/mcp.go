// Package mcp connects the engine to external Model Context Protocol
// servers as a client, discovers their tools, and adapts each remote tool
// into the engine's tool.Tool interface so it flows into the tool pool
// alongside the built-ins.
//
// Server origin is preserved end to end: every discovered tool is exposed
// under the namespaced name "<server>__<remoteName>", and the concrete
// adapter carries the originating server so dedup, deny-rule matching and
// the tool decision record all resolve against (server, tool) rather than a
// bare tool name. That keeps "which server authorized this call"
// attributable and disambiguates same-named tools across servers.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// NameSeparator joins a server name and a remote tool name into the
// namespaced tool identifier exposed to the model and the registry.
const NameSeparator = "__"

// RemoteToolSpec is the transport-agnostic description of a single tool
// advertised by an MCP server. A Session returns these from ListTools; the
// Manager turns each into a tool.Tool. Keeping this separate from the SDK's
// mcp.Tool lets the Manager and its tests stay free of any transport code.
type RemoteToolSpec struct {
	// Name is the tool's bare name on the server (un-namespaced).
	Name string
	// Description is the human-readable description shown to the model.
	Description string
	// InputSchema is the JSON Schema for the tool's arguments. May be nil.
	InputSchema map[string]any
	// ReadOnly reflects the server's readOnlyHint annotation (best effort).
	// Read-only tools are advertised as concurrency-safe.
	ReadOnly bool
}

// Session is a live connection to one MCP server. Implementations wrap a
// concrete transport (stdio subprocess, Streamable HTTP, …). All methods
// must be safe for concurrent use — a single session backs every tool
// discovered from that server, and the engine may execute them in parallel.
type Session interface {
	// ListTools returns the tools the server currently advertises.
	ListTools(ctx context.Context) ([]RemoteToolSpec, error)
	// CallTool invokes a tool by its bare (un-namespaced) name.
	CallTool(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error)
	// Close tears down the connection (and, for stdio, the subprocess).
	Close() error
}

// Connector opens a Session to a server described by cfg. The Manager holds
// one Connector; production uses the mark3labs/mcp-go implementation, tests
// inject a fake so the Manager logic is exercised without any transport.
type Connector interface {
	Connect(ctx context.Context, cfg ServerConfig) (Session, error)
}

// ServerConfig is the connection description the Manager passes to a
// Connector. It mirrors config.MCPServerConfig but lives here so the mcp
// package does not force a config dependency on the Connector contract.
type ServerConfig struct {
	Name      string
	Transport string
	Command   string
	Args      []string
	Env       map[string]string
	URL       string
	Headers   map[string]string
}

// remoteTool adapts one MCP server tool to the engine's tool.Tool
// interface. It holds the originating server name so the origin is
// recoverable for attribution.
type remoteTool struct {
	tool.BaseTool

	server   string // originating server name (attribution)
	remote   string // bare tool name on the server
	name     string // exposed namespaced name: server + NameSeparator + remote
	desc     string
	schema   map[string]any
	readOnly bool

	sess   Session
	logger *zap.Logger
}

// newRemoteTool builds the adapter for a single discovered tool.
func newRemoteTool(server string, spec RemoteToolSpec, sess Session, logger *zap.Logger) *remoteTool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &remoteTool{
		server:   server,
		remote:   spec.Name,
		name:     server + NameSeparator + spec.Name,
		desc:     spec.Description,
		schema:   spec.InputSchema,
		readOnly: spec.ReadOnly,
		sess:     sess,
		logger:   logger,
	}
}

// Name returns the namespaced identifier "<server>__<remoteName>".
func (t *remoteTool) Name() string { return t.name }

// Description returns the server-provided description.
func (t *remoteTool) Description() string { return t.desc }

// InputSchema returns the server-provided JSON Schema (never nil — an empty
// object schema is returned when the server advertised none, so schema
// decoration downstream stays well-formed).
func (t *remoteTool) InputSchema() map[string]any {
	if t.schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return t.schema
}

// IsReadOnly reflects the server's readOnlyHint. Unknown → false (treated as
// a mutating call, so it runs serially).
func (t *remoteTool) IsReadOnly() bool { return t.readOnly }

// IsConcurrencySafe mirrors IsReadOnly: only read-only remote tools are
// eligible for parallel execution with other concurrency-safe tools.
func (t *remoteTool) IsConcurrencySafe() bool { return t.readOnly }

// ServerName reports the originating MCP server (attribution).
func (t *remoteTool) ServerName() string { return t.server }

// RemoteName reports the tool's bare name on the server.
func (t *remoteTool) RemoteName() string { return t.remote }

// Execute forwards the call to the originating server. The framework-injected
// `intent` progress field is stripped before the arguments are sent upstream.
// Transport/tool errors are returned as a failed ToolResult (err == nil) so
// the engine surfaces them to the model rather than aborting the loop.
func (t *remoteTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	args := map[string]any{}
	if len(strings.TrimSpace(string(input))) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return &types.ToolResult{
				Content:   fmt.Sprintf("invalid input for MCP tool %q: %v", t.name, err),
				IsError:   true,
				ErrorType: types.ToolErrorInvalidInput,
			}, nil
		}
	}
	// `intent` is injected on every schema by the pool for UI progress and
	// is not a real tool argument — never forward it to the server.
	delete(args, "intent")

	res, err := t.sess.CallTool(ctx, t.remote, args)
	if err != nil {
		t.logger.Warn("mcp tool call failed",
			zap.String("server", t.server),
			zap.String("tool", t.remote),
			zap.Error(err))
		return &types.ToolResult{
			Content:   fmt.Sprintf("MCP server %q tool %q failed: %v", t.server, t.remote, err),
			IsError:   true,
			ErrorType: types.ToolErrorDependencyFail,
		}, nil
	}
	if res == nil {
		return &types.ToolResult{
			Content:   fmt.Sprintf("MCP server %q returned no result for tool %q", t.server, t.remote),
			IsError:   true,
			ErrorType: types.ToolErrorDependencyFail,
		}, nil
	}
	// Stamp origin onto the result so callers/telemetry can attribute it.
	if res.Metadata == nil {
		res.Metadata = map[string]any{}
	}
	res.Metadata["mcp_server"] = t.server
	res.Metadata["mcp_tool"] = t.remote
	return res, nil
}

// compile-time assertion: remoteTool satisfies the engine tool contract.
var _ tool.Tool = (*remoteTool)(nil)
