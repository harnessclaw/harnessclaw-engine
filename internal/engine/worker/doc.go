// Package worker holds the L3 search/execution layer (搭档) — the
// side of the engine that actually carries out tasks dispatched by the
// L2 scheduler.
//
// L3 responsibility split:
//
//   - **Profile**: prompt persona (writer / explorer / planner / coder)
//     — currently lives in internal/engine/prompt.
//   - **Output contract**: the <summary>…</summary> XML wrapper L2
//     uses to extract task status without slurping the entire L3 chat.
//   - **Deliverable validation**: per-task file artifacts L3 produces.
//
// The package is intentionally empty at creation time — it is the
// designated home for L3-side code that is currently scattered across
// engine/ (summary parsing in subagent.go, deliverable checks in
// executor_artifact_test.go). Subsequent PRs will migrate these into
// worker/ so the L3 boundary is visible in the directory tree alongside
// emma/ (L1) and scheduler/ (L2).
package worker
