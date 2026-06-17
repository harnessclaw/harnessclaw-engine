// Package builtin holds the AgentDefinition constants for engine-shipped
// agent types (generic, freelancer, plan_design, explore, browser_agent).
//
// Each definition is pure data: Profile, AllowedTools, MaxTurns,
// SystemPrompt, etc. Adding or tweaking a built-in agent type is a one-
// file change here — no engine code edits required. The runner package
// (internal/engine/runner) consumes these definitions via
// runner.RunLeaf(ctx, runner.Input{Def: builtin.X, …}).
//
// External / user-supplied agents live in configs/agents/*.md and are
// loaded at startup through internal/agent/loader.go into the same
// AgentDefinitionRegistry — they are interchangeable with the values
// declared here at runtime.
package builtin
