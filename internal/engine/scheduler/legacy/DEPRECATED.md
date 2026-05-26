# DEPRECATED — Phase-1 Orchestrate Executor

This package (formerly `internal/engine/orchestrate`) implements the
**Phase-1 串行 / wave-parallel plan executor** used by the
`Orchestrate` LLM tool.

It is on the **deprecation path**. New L2 work goes through the
msgbus-driven scheduler kernel:

- `internal/engine/scheduler/scheduler.go` — kernel
- `internal/engine/scheduler/coordinator.go` — top-level assembler
- `internal/engine/scheduler/dispatch/{react,plan,team,vote}` — strategies

## What lives here

| File | Purpose |
|---|---|
| `plan.go` | `Plan` / `PlanStep` DTOs + `ParsePlan` for the planner-agent output |
| `executor.go` | `PlanExecutor.Execute` — per-step SpawnSync loop with budget / dependency check / emit envelope |

## Current callers

- `internal/tool/orchestrate/orchestrate.go` — the `Orchestrate` tool
  (emma-facing). Still wires through this executor until the scheduler
  plan strategy reaches parity.

## Removal plan

Delete this package once:

1. `scheduler/dispatch/plan` covers all behaviours currently exercised
   here (plan-agent → plan-executor-agent two-phase spawn, budget,
   dependency skip, emit envelope, partial-completion finalization).
2. The `Orchestrate` tool migrates to call `scheduler.NewCoordinator`
   directly with `kind=plan`.
3. All tests in this directory are ported or superseded.

Do **not** add new features here. Bug fixes only — and ideally even
those land in the new scheduler instead.
