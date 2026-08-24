// Package mcptool adapts tools discovered on MCP servers into the
// engine's tool.Tool interface.
//
// The split from internal/mcp is deliberate: that package speaks the
// protocol and knows nothing about the engine, while this one knows
// nothing about JSON-RPC. Everything here is a thin projection of an
// mcp.ToolDescriptor onto the interface the query loop already
// understands, so MCP tools flow through permission checks, the tool
// pool, and event emission exactly like a built-in.
package mcptool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"harnessclaw-go/internal/mcp"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// NamePrefix and NameSeparator build the engine-facing tool name from
// the server and tool names: mcp__<server>__<tool>.
//
// Namespacing is not cosmetic. Two servers may both expose a "search"
// tool, and tool.NewToolPool dedupes by name with built-ins winning — so
// unprefixed MCP names would silently shadow each other and could be
// swallowed entirely by a same-named built-in. The mcp__ prefix also
// makes MCP tools trivially greppable in logs and easy to target with a
// permission deny rule.
const (
	NamePrefix    = "mcp"
	NameSeparator = "__"
)

// QualifiedName renders the engine-facing name for a server/tool pair.
func QualifiedName(server, toolName string) string {
	return NamePrefix + NameSeparator + server + NameSeparator + toolName
}

// Caller is the subset of *mcp.Manager this package needs. An interface
// so tests can drive the adapter without spawning a child process.
type Caller interface {
	CallTool(ctx context.Context, server, toolName string, args map[string]any) (*mcp.CallResult, error)
}

// Tool is one MCP-backed tool presented to the LLM.
type Tool struct {
	tool.BaseTool

	caller Caller
	desc   mcp.ToolDescriptor
	name   string
}

// New wraps a single descriptor. Prefer NewTools for the normal case of
// adapting everything a Manager discovered.
func New(caller Caller, desc mcp.ToolDescriptor) *Tool {
	return &Tool{
		caller: caller,
		desc:   desc,
		name:   QualifiedName(desc.Server, desc.Name),
	}
}

// NewTools adapts every descriptor a Manager currently exposes.
//
// Returns a []tool.Tool ready to hand to tool.NewToolPool as its mcpTools
// argument — NOT to Registry.Register, which would sort them into the
// built-in block and destabilise the prompt-cache prefix the pool works
// to keep contiguous.
func NewTools(mgr *mcp.Manager) []tool.Tool {
	if mgr == nil {
		return nil
	}
	descs := mgr.Tools()
	out := make([]tool.Tool, 0, len(descs))
	for _, d := range descs {
		out = append(out, New(mgr, d))
	}
	return out
}

func (t *Tool) Name() string { return t.name }

// Description forwards the server's own text, tagged with its origin.
// The tag earns its tokens: without it an LLM choosing between a
// built-in and an MCP tool of similar description has no signal that one
// crosses a process boundary into third-party code.
func (t *Tool) Description() string {
	desc := strings.TrimSpace(t.desc.Description)
	if desc == "" {
		desc = fmt.Sprintf("Tool %q provided by MCP server %q.", t.desc.Name, t.desc.Server)
	}
	return fmt.Sprintf("%s\n\n(Provided by MCP server %q.)", desc, t.desc.Server)
}

// InputSchema hands back the server's schema untouched. Rewriting it
// would risk diverging from what the server actually validates, and the
// server is the authority on its own arguments.
func (t *Tool) InputSchema() map[string]any { return t.desc.InputSchema }

// IsReadOnly always reports false, even when the server sent
// readOnlyHint: true.
//
// The hint is self-attested by third-party code, and IsReadOnly feeds the
// permission pipeline's ReadOnlyAutoAllow step — so honouring it would
// let any MCP server skip the user's approval prompt just by claiming to
// be harmless. The hint is not discarded: it is kept on the descriptor
// and reflected in SafetyLevel, which informs the prompt without
// bypassing it. A per-server "trust read-only hints" opt-in is the
// natural follow-up for operators who want the quieter behaviour.
func (t *Tool) IsReadOnly() bool { return false }

// IsConcurrencySafe reports false. Read-only-ness is unverifiable here
// (see IsReadOnly), and an MCP server is free to be stateful or
// single-threaded, so parallel dispatch is not ours to assume.
func (t *Tool) IsConcurrencySafe() bool { return false }

// SafetyLevel maps the server's readOnlyHint onto the engine's scale,
// deliberately never returning SafetySafe: that tier is for tools the
// engine itself implements and can reason about. A hinted read-only MCP
// tool is Caution; anything else crosses into third-party side effects
// and is Dangerous.
func (t *Tool) SafetyLevel() tool.SafetyLevel {
	if t.desc.ReadOnly {
		return tool.SafetyCaution
	}
	return tool.SafetyDangerous
}

func (t *Tool) IsEnabled() bool { return true }

// ValidateInput checks only that the payload is a JSON object. Deeper
// schema validation is left to the server, which owns the schema —
// duplicating it here would drift.
//
// Empty input is accepted and forwarded as no arguments, since a
// zero-argument MCP tool is common and some providers send "" rather
// than "{}".
func (t *Tool) ValidateInput(input json.RawMessage) error {
	if len(strings.TrimSpace(string(input))) == 0 {
		return nil
	}
	var probe map[string]any
	if err := json.Unmarshal(input, &probe); err != nil {
		return fmt.Errorf("invalid input for %s: %w", t.name, err)
	}
	return nil
}

// Execute forwards the call to the MCP server.
//
// Two failure modes are kept distinct. A tool that ran and reported
// failure comes back as a ToolResult with IsError set, so the LLM can
// read the message and adapt. A request that did not complete — dead
// transport, timeout — is returned as a Go error, letting toolexec
// classify it (a deadline becomes ToolErrorToolTimeout rather than an
// opaque internal error).
func (t *Tool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	args := map[string]any{}
	if trimmed := strings.TrimSpace(string(input)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, fmt.Errorf("decode input for %s: %w", t.name, err)
		}
	}

	res, err := t.caller.CallTool(ctx, t.desc.Server, t.desc.Name, args)
	if err != nil {
		return nil, err
	}

	content := res.Text
	if strings.TrimSpace(content) == "" {
		// An empty result reads to the LLM as a silent no-op and invites
		// a pointless retry. Say what happened instead.
		if res.IsError {
			content = fmt.Sprintf("MCP tool %q on server %q reported an error with no message.", t.desc.Name, t.desc.Server)
		} else {
			content = fmt.Sprintf("MCP tool %q on server %q completed with no output.", t.desc.Name, t.desc.Server)
		}
	}

	return &types.ToolResult{
		Content: content,
		IsError: res.IsError,
	}, nil
}

// compile-time check that the adapter satisfies the engine's interface.
var _ tool.Tool = (*Tool)(nil)
