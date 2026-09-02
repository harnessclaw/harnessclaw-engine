package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"

	"harnessclaw-go/pkg/types"
)

const (
	clientName    = "harnessclaw-engine"
	clientVersion = "0.1.0"
)

// mark3labsConnector is the production Connector, backed by mark3labs/mcp-go.
// It supports the stdio and Streamable HTTP transports.
type mark3labsConnector struct {
	logger *zap.Logger
}

func newMark3labsConnector(logger *zap.Logger) *mark3labsConnector {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &mark3labsConnector{logger: logger}
}

// Connect opens the transport, starts it, and performs the MCP initialize
// handshake. The returned Session is ready for ListTools / CallTool.
func (c *mark3labsConnector) Connect(ctx context.Context, cfg ServerConfig) (Session, error) {
	var (
		cli *mcpclient.Client
		err error
	)

	switch cfg.Transport {
	case "stdio":
		if strings.TrimSpace(cfg.Command) == "" {
			return nil, fmt.Errorf("stdio transport requires a command")
		}
		cli, err = mcpclient.NewStdioMCPClient(cfg.Command, envSlice(cfg.Env), cfg.Args...)
	case "http", "streamable-http", "streamable_http":
		if strings.TrimSpace(cfg.URL) == "" {
			return nil, fmt.Errorf("http transport requires a url")
		}
		var opts []transport.StreamableHTTPCOption
		if len(cfg.Headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(cfg.Headers))
		}
		cli, err = mcpclient.NewStreamableHttpClient(cfg.URL, opts...)
	default:
		return nil, fmt.Errorf("unsupported mcp transport %q (want stdio or http)", cfg.Transport)
	}
	if err != nil {
		return nil, err
	}

	// Start is idempotent; stdio auto-starts on construction, http needs it.
	if err := cli.Start(ctx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("start transport: %w", err)
	}

	initReq := mcpsdk.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpsdk.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpsdk.Implementation{Name: clientName, Version: clientVersion}
	initReq.Params.Capabilities = mcpsdk.ClientCapabilities{}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}

	return &mark3labsSession{cli: cli}, nil
}

// mark3labsSession wraps a live mcp-go client.
type mark3labsSession struct {
	cli *mcpclient.Client
}

func (s *mark3labsSession) ListTools(ctx context.Context) ([]RemoteToolSpec, error) {
	res, err := s.cli.ListTools(ctx, mcpsdk.ListToolsRequest{})
	if err != nil {
		return nil, err
	}
	specs := make([]RemoteToolSpec, 0, len(res.Tools))
	for _, t := range res.Tools {
		specs = append(specs, RemoteToolSpec{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schemaToMap(t),
			ReadOnly:    t.Annotations.ReadOnlyHint != nil && *t.Annotations.ReadOnlyHint,
		})
	}
	return specs, nil
}

func (s *mark3labsSession) CallTool(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	req := mcpsdk.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args

	res, err := s.cli.CallTool(ctx, req)
	if err != nil {
		return nil, err
	}
	return toToolResult(res), nil
}

func (s *mark3labsSession) Close() error { return s.cli.Close() }

// schemaToMap converts an SDK tool's input schema into a plain JSON-Schema
// map. RawInputSchema wins when present (arbitrary schema); otherwise the
// structured InputSchema is marshaled through its own JSON encoding.
func schemaToMap(t mcpsdk.Tool) map[string]any {
	var raw []byte
	if len(t.RawInputSchema) > 0 {
		raw = t.RawInputSchema
	} else {
		b, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil
		}
		raw = b
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// toToolResult flattens an MCP call result into the engine's ToolResult.
// Text content blocks are concatenated; when there is none but structured
// content is present, the structured payload is JSON-encoded as the body.
func toToolResult(res *mcpsdk.CallToolResult) *types.ToolResult {
	if res == nil {
		return &types.ToolResult{IsError: true, ErrorType: types.ToolErrorDependencyFail}
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := mcpsdk.AsTextContent(c); ok {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(tc.Text)
		}
	}
	content := b.String()
	if content == "" && res.StructuredContent != nil {
		if j, err := json.Marshal(res.StructuredContent); err == nil {
			content = string(j)
		}
	}
	out := &types.ToolResult{Content: content, IsError: res.IsError}
	if res.IsError {
		out.ErrorType = types.ToolErrorDependencyFail
	}
	return out
}

// envSlice renders an env map as the "KEY=VALUE" slice the stdio transport
// expects. A nil/empty map yields nil (inherit the parent environment).
func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
