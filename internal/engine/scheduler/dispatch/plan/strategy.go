package plan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"harnessclaw-go/internal/engine/scheduler/dispatch"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/workspace"
)

// Strategy implements dispatch.Strategy for the "plan" kind (§5.3.3).
// Phase 1 skeleton:
//  1. Spawn a planner sub-agent that writes plan.json.
//  2. Parse plan.json for step specs; if non-empty, publish control{spawn}
//     for all steps and wait for woken notify (parent parked by onLifecycle
//     after lifecycle{spawned}).
//  3. Spawn a summarizer sub-agent that aggregates results.
type Strategy struct{ caps dispatch.Capabilities }

// New creates a Strategy with the given Capabilities.
// AllowSubmit is forced true so the plan strategy may dispatch children.
func New(caps dispatch.Capabilities) *Strategy {
	if caps.LeafKind == "" {
		caps.LeafKind = "react-leaf"
	}
	caps.AllowSubmit = true
	return &Strategy{caps: caps}
}

func (Strategy) Kind() types.Kind                    { return types.KindPlan }
func (p *Strategy) Capabilities() dispatch.Capabilities { return p.caps }

// planJSON is the output written by the planner sub-agent.
type planJSON struct {
	Steps []spec.TaskSpec `json:"steps"`
}

// Run executes the plan strategy:
//  1. planner leaf → plan.json
//  2. (phase 2) spawn steps + park parent + wait woken
//  3. summarizer leaf → summary result
func (p *Strategy) Run(ctx context.Context, taskID types.TaskID, deps dispatch.Deps) (types.MetaRef, error) {
	task, err := deps.Reader.Get(ctx, taskID)
	if err != nil {
		return "", err
	}

	// ── 1. planner ──────────────────────────────────────────────────────────
	plannerRes, err := dispatch.SpawnAndWaitOne(ctx, deps.Bus, taskID,
		buildPlannerSpec(task.LeafSpec.Goal, task.SessionID))
	if err != nil {
		return "", err
	}
	if plannerRes.Status != "done" {
		return "", &dispatch.LeafFailedError{Reason: plannerRes.Reason}
	}

	// ── 2. parse plan.json (phase 1: empty unless planner writes it) ────────
	// plannerRes.OutputFile is relative to sessionRoot (e.g. "tasks/<id>/meta.json").
	// Derive the absolute plan.json path by replacing the "meta.json" suffix with
	// "plan.json" and resolving against sessionRoot.
	plan := planJSON{}
	if p.caps.RootDir != "" {
		sessionRoot := workspace.SessionRoot(p.caps.RootDir, task.SessionID)
		relPlanPath := strings.TrimSuffix(plannerRes.OutputFile, "meta.json") + "plan.json"
		planPath := filepath.Join(sessionRoot, relPlanPath)
		if b, readErr := os.ReadFile(planPath); readErr == nil {
			_ = json.Unmarshal(b, &plan)
		}
	}

	if len(plan.Steps) > 0 {
		// R6: subscribe BEFORE publishing so we never miss grant/woken.
		msgCh, cancelSub := deps.Bus.Subscribe(msgbus.AddrScheduler)
		defer cancelSub()

		// Tag each step with session + per-task layout.
		anySpecs := make([]any, len(plan.Steps))
		for i, s := range plan.Steps {
			s.Layout = "per-task"
			s.SessionID = task.SessionID
			anySpecs[i] = s
		}

		// Publish control{spawn} to the scheduler.
		spawnMsgID := string(taskID) + ":plan-spawn"
		_ = deps.Bus.Publish(ctx, msgbus.AgentMessage{
			MsgID:   spawnMsgID,
			Kind:    msgbus.KindControl,
			From:    msgbus.AddrAgent(string(taskID)),
			To:      msgbus.AddrScheduler,
			TaskID:  string(taskID),
			Payload: msgbus.ControlPayload{Cmd: msgbus.CmdSpawn, Body: msgbus.SpawnBody{Specs: anySpecs}},
		})

		// Phase 1: wait for spawn_granted to confirm children were created.
		var spawnedIDs []string
		for len(spawnedIDs) == 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case msg := <-msgCh:
				if msg.Kind != msgbus.KindNotify {
					continue
				}
				np, ok := msg.Payload.(msgbus.NotifyPayload)
				if !ok || msg.TaskID != string(taskID) {
					continue
				}
				if np.Event == msgbus.NotifySpawnGranted {
					spawnedIDs = np.SpawnedIDs
				} else if np.Event == msgbus.NotifySpawnFailed {
					return "", &dispatch.LeafFailedError{Reason: "spawn failed: " + np.Reason}
				}
			}
		}

		// Publish lifecycle{spawned} so onLifecycle can park this parent task
		// while child steps execute.
		_ = deps.Bus.Publish(ctx, msgbus.AgentMessage{
			MsgID:   string(taskID) + ":spawned",
			Kind:    msgbus.KindLifecycle,
			From:    msgbus.AddrAgent(string(taskID)),
			To:      msgbus.AddrScheduler,
			TaskID:  string(taskID),
			Payload: msgbus.LifecyclePayload{
				Event:      msgbus.EventSpawned,
				Attempt:    task.Attempt,
				SpawnedIDs: spawnedIDs,
			},
		})

		// Wait for notify{woken} — published by onTerminal when all children finish.
		for {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case msg := <-msgCh:
				if msg.Kind != msgbus.KindNotify || msg.TaskID != string(taskID) {
					continue
				}
				np, ok := msg.Payload.(msgbus.NotifyPayload)
				if ok && np.Event == msgbus.NotifyWoken {
					goto afterWoken
				}
			}
		}
	afterWoken:
	}

	// ── 3. summarizer ────────────────────────────────────────────────────────
	summRes, err := dispatch.SpawnAndWaitOne(ctx, deps.Bus, taskID,
		buildSummarizerSpec(task.LeafSpec.Goal, task.SessionID, nil))
	if err != nil {
		return "", err
	}
	if summRes.Status != "done" {
		return "", &dispatch.LeafFailedError{Reason: summRes.Reason}
	}
	return types.MetaRef(summRes.OutputFile), nil
}
