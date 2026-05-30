// Package mention implements the @agent_name router that bypasses
// emma's main loop and dispatches directly to a named agent.
//
// Router holds *spawn2.Spawner. emma owns *Router (not Spawner), which
// is how spawner stays out of emma's deps. Stage 8 of the tier-
// decoupling refactor wires this into emma.ProcessMessage.
package mention
