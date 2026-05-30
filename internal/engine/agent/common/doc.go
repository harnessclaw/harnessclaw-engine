// Package common holds the shared sub-agent setup primitives every
// tier module reuses. It complements loop (LLM execution) and spawn
// (lifecycle): common handles the "set up the sub-agent's environment"
// step that loop and spawn don't.
//
// Helpers:
//   - BuildSubSession         create ephemeral session with standard id
//   - BuildToolPool           3-layer filter to produce sub-agent tool pool
//   - BuildInheritedChecker   permission checker that inherits parent's approvals
//   - BuildSubAgentPrompt     render system prompt via prompt.Builder
//   - WithSubAgentStats       inject sessionstats ctx keys
//   - EmitSubagentStart/End   wire envelope for trace UI
//   - BuildSpawnResult        bridge loop.Result → agent.SpawnResult
//   - StopOnEndTurn / StopOnSubmitResult / ContractEnforcer
//                             TurnHook factories for loop.Config
//
// common has NO tier knowledge. Each helper takes inputs and returns
// values; tier modules orchestrate the calls.
package common
