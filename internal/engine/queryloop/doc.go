// Package queryloop drives one user turn: assembles the system prompt,
// streams the LLM call, dispatches tool batches, handles plan/step
// approval round-trips, and emits engine events to the caller.
//
// Runner.Run is the public entry point invoked by QueryEngine.ProcessMessage.
package queryloop
