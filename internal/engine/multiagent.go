package engine

import (
	"harnessclaw-go/internal/agent"
)

// SetAgentRegistry configures the agent registry for async agent support.
func (qe *QueryEngine) SetAgentRegistry(reg *agent.AgentRegistry) {
	qe.agentRegistry = reg
}

// SetMessageBroker configures the message broker for inter-agent communication.
func (qe *QueryEngine) SetMessageBroker(broker *agent.MessageBroker) {
	qe.messageBroker = broker
}
