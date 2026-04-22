// Package mock provides a programmable LLM provider for testing.
package mock

import (
	"context"
	"fmt"
	"sync"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// Response represents a single scripted LLM response.
type Response struct {
	// Text is the text output from the LLM.
	Text string
	// ToolCalls is a list of tool calls the LLM requests.
	ToolCalls []types.ToolCall
	// StopReason is the stop reason (defaults to "end_turn" if no tool calls, "tool_use" if tool calls).
	StopReason string
	// Usage tracks token consumption for this response.
	Usage *types.Usage
	// Error causes the Chat call to return this error instead of streaming.
	Error error
}

// MockProvider implements provider.Provider with scripted responses.
// Responses are consumed in order; if exhausted, returns an error.
type MockProvider struct {
	mu        sync.Mutex
	responses []Response
	calls     []provider.ChatRequest // records all Chat calls for assertions
	name      string
}

// compile-time check that MockProvider satisfies provider.Provider.
var _ provider.Provider = (*MockProvider)(nil)

// New creates a MockProvider with the given scripted responses.
func New(responses ...Response) *MockProvider {
	return &MockProvider{
		responses: responses,
		name:      "mock",
	}
}

// Name returns the provider identifier.
func (m *MockProvider) Name() string { return m.name }

// Chat returns a stream built from the next scripted response.
func (m *MockProvider) Chat(_ context.Context, req *provider.ChatRequest) (*provider.ChatStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record the call.
	if req != nil {
		m.calls = append(m.calls, *req)
	}

	if len(m.responses) == 0 {
		return nil, fmt.Errorf("mock provider: no more scripted responses (call #%d)", len(m.calls))
	}

	resp := m.responses[0]
	m.responses = m.responses[1:]

	if resp.Error != nil {
		return nil, resp.Error
	}

	return BuildStream(resp), nil
}

// CountTokens returns a rough estimate based on text length (for testing).
func (m *MockProvider) CountTokens(_ context.Context, msgs []types.Message) (int, error) {
	total := 0
	for _, msg := range msgs {
		for _, cb := range msg.Content {
			total += len(cb.Text) + len(cb.ToolInput) + len(cb.ToolResult)
		}
	}
	return total / 4, nil
}

// Calls returns all recorded ChatRequest calls (for test assertions).
func (m *MockProvider) Calls() []provider.ChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]provider.ChatRequest, len(m.calls))
	copy(result, m.calls)
	return result
}

// CallCount returns how many times Chat was called.
func (m *MockProvider) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// Remaining returns how many scripted responses are left.
func (m *MockProvider) Remaining() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.responses)
}
