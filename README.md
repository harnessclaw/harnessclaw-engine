# HarnessClaw Engine

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

[English](README.md) | [中文](README_zh.md)

An LLM programming assistant engine built with Go. It provides capabilities via the WebSocket protocol, supporting multi-turn dialogues, tool calling, permission control, and skill extension.

## Architecture Overview

```text
┌───────────-──┐   ┌─────────────┐   ┌─────────────┐
│  WebSocket   │   │    HTTP     │   │   Feishu    │
│  Channel     │   │   Channel   │   │  Channel    │
└──────┬───────┘   └──────┬──────┘   └──────┬──────┘
       │                  │                  │
       └──────────────────┼──────────────────┘
                          ▼
                ┌──────────────────┐
                │Router + Middleware│  Auth / RateLimit / Logging
                └────────┬─────────┘
                         ▼
                ┌──────────────────┐
                │   Query Engine   │  5-Phase Loop
                │  (queryloop.go)  │  Preprocessing → LLM Streaming → Error Recovery
                └───┬──────────┬───┘  → Tool Execution → Continuation Check
                    │          │
              ┌─────▼──-─┐  ┌──▼──────────┐
              │ Provider │  │ Tool System │
              │ (LLM)    │  │ 7 Built-in  │
              └───────-──┘  └─────────────┘
```

**Dependency Direction**: Channel → Router → Engine → Provider / Tool (Unidirectional, no circular dependencies)

## Core Features

- **5-Phase Query Loop** — Preprocessing (Auto-compaction) → LLM Streaming Call → Error Recovery (Exponential Backoff) → Tool Execution (Parallel/Serial) → Continuation Check
- **WebSocket Protocol v1.4** — Explicit session handshake, full message lifecycle, bidirectional tool calling (Server-side + Client-side execution), permission request/response stream
- **7 Built-in Tools** — Bash, FileRead, FileEdit, FileWrite, Grep, Glob, WebFetch
- **6-Step Permission Pipeline** — DenyRule → ToolCheckPerm → BypassMode → AlwaysAllowRule → ReadOnlyAutoAllow → ModeDefault, supporting 6 permission modes
- **Skill System** — Loads skills from `SKILL.md` files, supporting YAML frontmatter, parameter substitution, and priority override
- **Multi-Provider Support** — Direct Anthropic SSE client + Bifrost Multi-Provider adapter (Anthropic/OpenAI/Bedrock/Vertex)
- **Context Compaction** — LLM-based conversation summarization + Circuit breaker pattern, automatically triggered when token usage reaches the threshold
- **Session Management** — Thread-safe session state, multi-connection fan-out, idle timeout reclamation

## Project Structure

```text
go_rebuild/
├── cmd/server/           # Entry point & Integration tests
│   ├── main.go           # 11-step startup process
│   └── main_test.go      # E2E tests (build tag: integration)
├── configs/
│   └── config.yaml       # Default configuration
├── internal/
│   ├── channel/           # Multi-protocol access layer (WebSocket / HTTP / Feishu)
│   ├── command/           # Command registration & Priority system
│   ├── config/            # Viper configuration management (50+ defaults)
│   ├── engine/            # Core query engine
│   │   ├── queryloop.go   # QueryEngine main loop (831 lines)
│   │   ├── executor.go    # Parallel/Serial tool executor
│   │   ├── compact/       # LLM context compaction
│   │   ├── context/       # System prompt assembly
│   │   └── session/       # Session state & Lifecycle
│   ├── event/             # In-process pub/sub event bus
│   ├── permission/        # 6-step permission pipeline (6 modes)
│   ├── provider/          # LLM Provider abstraction
│   │   ├── anthropic/     # Direct Anthropic SSE client
│   │   ├── bifrost/       # Multi-Provider adapter
│   │   └── retry/         # Exponential backoff + 529 overload switching
│   ├── router/            # Message routing + Middleware chain
│   ├── skill/             # SKILL.md loading & Parameter substitution
│   ├── storage/           # Storage interfaces (Memory implementation)
│   └── tool/              # Tool system
│       ├── tool.go        # Tool interface + 10 extension interfaces
│       ├── registry.go    # Thread-safe tool registry
│       ├── pool.go        # Immutable per-query tool pool
│       └── bash/fileread/fileedit/filewrite/grep/glob/webfetch/skilltool/
├── pkg/
│   ├── types/             # Shared types (Message, Event, ToolCall, Context)
│   └── errors/            # Domain errors (16 error codes)
├── docs/
│   ├── protocols/         # WebSocket protocol specification (v1.4)
├── Makefile               # Build/Run/Test/Lint
└── go.mod                 # Go 1.26.1
```

## Quick Start

### Prerequisites

- Go 1.26+
- (Optional) [golangci-lint](https://golangci-lint.run/) — For code linting
- (Optional) [ripgrep](https://github.com/BurntSushi/ripgrep) — Runtime dependency for the Grep tool

### Build & Run

```bash
# Build
make build              # Outputs to ./dist/harnessclaw-engine

# Run (using default configuration)
make run                # go run ./cmd/server -config ./configs/config.yaml

# Run directly with a specific configuration file
./dist/harnessclaw-engine -config ./configs/config.yaml
```

### Testing

```bash
# Unit tests
make test               # go test ./... -v -race -count=1

# Coverage report
make test-cover         # Generates coverage.html

# Integration tests (requires real LLM API)
go test -tags=integration ./cmd/server/ -v
go test -tags=integration ./internal/provider/bifrost/ -v
```

### Other Commands

```bash
make fmt                # Format code
make tidy               # Tidy go.mod
make lint               # Run linters
make vuln               # Scan for vulnerabilities
make clean              # Clean build artifacts
```

## Configuration

The configuration file is located at `configs/config.yaml`. Main configuration items:

| Configuration Item | Description | Default Value |
|--------|------|--------|
| `server.port` | HTTP server port | `8080` |
| `channels.websocket.port` | WebSocket port | `8081` |
| `channels.websocket.path` | WebSocket path | `/ws` |
| `llm.default_provider` | LLM Provider | `anthropic` |
| `llm.providers.anthropic.model` | Model name | `astron-code-latest` |
| `engine.max_turns` | Max tool calls per turn | `50` |
| `engine.auto_compact_threshold` | Token ratio threshold for auto-compaction | `0.8` |
| `session.idle_timeout` | Session idle timeout | `30m` |
| `permission.mode` | Permission mode | `default` |
| `tools.*` | Individual tool toggles | All `true` |

## WebSocket Protocol

Connection Address: `ws://host:8081/ws`

### Session Lifecycle

```text
Client                                   Server
  │                                        │
  │── session.create ──────────────────────>│
  │<────────────────────── session.created ─│
  │                                        │
  │── user.message ────────────────────────>│
  │<──────────────────── message.start ─────│
  │<──── content.start / content.delta ─────│  (Streaming Text)
  │<──────── tool.start / tool.end ─────────│  (Server-side Tool)
  │<──────────── tool.call ─────────────────│  (Client-side Tool)
  │── tool.result ─────────────────────────>│
  │<──── permission.request ────────────────│  (Permission Request)
  │── permission.response ─────────────────>│
  │<──────────────── content.stop ──────────│
  │<──────────────── message.stop ──────────│
  │<──────────────── task.end ──────────────│
  │                                        │
  │── abort ───────────────────────────────>│  (Interrupt)
```

For detailed protocol specifications, see [docs/protocols/websocket.md](docs/protocols/websocket.md).

## Documentation

- [WebSocket Protocol Specification v1.4](docs/protocols/websocket.md)

## License

Apache-2.0 License. See [LICENSE](LICENSE) for details.
