// Package loop drives the inner LLM conversation: provider call,
// streaming event emission, tool dispatch, auto-compaction, and
// turn-end decision.
//
// loop is consumed by every agent that runs an LLM loop:
//   - emma (L1 main agent, via emma's own wrapping)
//   - scheduler module (L2 react strategy)
//   - freelancer / plan_agent / explore / ... (L3 modules)
//
// Each consumer owns its own prompt construction, terminal semantics,
// and event envelope. loop ONLY knows: build req -> stream LLM ->
// dispatch tools -> ask OnTurnComplete -> apply Decision -> repeat.
//
// loop does NOT know:
//   - what "subagent.start" / "subagent.end" events look like
//   - what an AgentDefinition / Profile is
//   - what ExpectedOutputs / OutputSchema mean
//   - how a sub-session relates to a parent session
//
// Those concerns live in agent/common helpers and tier modules.
package loop
