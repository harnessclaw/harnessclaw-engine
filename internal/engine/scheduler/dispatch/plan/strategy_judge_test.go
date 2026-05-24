package plan_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	schedulerplan "harnessclaw-go/internal/engine/scheduler/dispatch/plan"
	"harnessclaw-go/internal/engine/scheduler/dispatch"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	tstore "harnessclaw-go/internal/engine/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	mstore "harnessclaw-go/internal/msgbus/store"
	"harnessclaw-go/internal/workspace"
)

// fakeSpawnerForJudge simulates the scheduler runtime's spawn handling.
// When it sees a control{spawn} on AddrScheduler, it:
//  1. Assigns a fake child ID.
//  2. Writes plan.json (with invalid steps) + meta.json for the "planner" child.
//  3. Publishes notify{spawn_granted} to the parent.
//  4. Publishes KindResult for the child (status "done", pointing to meta.json).
//
// This is intentionally limited: it only handles the first spawn (planner),
// which is all Run() needs before the judge fires.
func fakeSpawnerForJudge(
	t *testing.T,
	ctx context.Context,
	bus msgbus.Bus,
	rootDir, sessionID, parentTaskID string,
) {
	t.Helper()
	ch, cancel := bus.Subscribe(msgbus.AddrScheduler)
	t.Cleanup(cancel)

	go func() {
		childCounter := 0
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				if msg.Kind != msgbus.KindControl {
					continue
				}
				cp, ok := msg.Payload.(msgbus.ControlPayload)
				if !ok || cp.Cmd != msgbus.CmdSpawn {
					continue
				}
				// Only handle the first spawn (planner).
				childCounter++
				childID := types.TaskID(parentTaskID + "-child-" + string(rune('0'+childCounter)))

				// Write plan.json with a step that has an empty goal (invalid per PlanJudge).
				taskDir := workspace.TaskDir(rootDir, sessionID, string(childID))
				if err := os.MkdirAll(taskDir, 0o755); err != nil {
					t.Logf("fakeSpawner: MkdirAll: %v", err)
					return
				}
				sessionRoot := workspace.SessionRoot(rootDir, sessionID)
				plannerTaskDir := taskDir // plan.json written in the planner child's task dir

				planBytes, _ := json.Marshal(map[string]any{
					"steps": []any{
						map[string]any{"goal": ""}, // empty goal — PlanJudge will reject this
					},
				})
				planPath := filepath.Join(plannerTaskDir, "plan.json")
				if err := os.WriteFile(planPath, planBytes, 0o644); err != nil {
					t.Logf("fakeSpawner: WriteFile plan.json: %v", err)
					return
				}

				// Write meta.json so SpawnAndWaitOne gets a valid OutputFile.
				metaPath := filepath.Join(plannerTaskDir, "meta.json")
				metaBytes, _ := json.Marshal(map[string]any{"status": "done"})
				if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
					t.Logf("fakeSpawner: WriteFile meta.json: %v", err)
					return
				}

				relMeta, err := filepath.Rel(sessionRoot, metaPath)
				if err != nil {
					relMeta = "meta.json"
				}

				// 3. Publish notify{spawn_granted}.
				_ = bus.Publish(ctx, msgbus.AgentMessage{
					MsgID:  "grant-" + string(childID),
					Kind:   msgbus.KindNotify,
					From:   msgbus.AddrScheduler,
					To:     msgbus.AddrScheduler,
					TaskID: parentTaskID,
					Payload: msgbus.NotifyPayload{
						Event:      msgbus.NotifySpawnGranted,
						SpawnedIDs: []string{string(childID)},
					},
				})

				// 4. Publish KindResult for the child.
				_ = bus.Publish(ctx, msgbus.AgentMessage{
					MsgID:  "result-" + string(childID),
					Kind:   msgbus.KindResult,
					From:   msgbus.AddrAgent(string(childID)),
					To:     msgbus.AddrScheduler,
					TaskID: string(childID),
					Payload: msgbus.ResultMessage{
						Status:     "done",
						OutputFile: relMeta,
					},
				})
			}
		}
	}()
}

// TestPlanStrategy_JudgeBlocksInvalidSteps verifies that when PlanJudge is
// configured and plan.json contains steps with empty goals, Run returns an
// error containing "plan validation failed".
func TestPlanStrategy_JudgeBlocksInvalidSteps(t *testing.T) {
	rootDir := t.TempDir()
	sessionID := "sess-judge-test"
	parentGoal := "do something important"

	// ── tstate ───────────────────────────────────────────────────────────────
	mst := mstore.NewMemory()
	bus := msgbus.NewInMem(mst)
	t.Cleanup(func() { _ = bus.Close() })

	tst := tstore.NewMemory()
	t.Cleanup(func() { _ = tst.Close() })
	kernel := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDs("jt-")})
	staging := tstate.NewStagingWriter(tst)

	// ── Admit a plan task into the kernel ────────────────────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	taskID, err := kernel.Admit(ctx, spec.TaskSpec{
		Goal:      parentGoal,
		SessionID: sessionID,
		Hint:      spec.Hint{Kind: types.KindPlan},
	})
	if err != nil {
		t.Fatalf("kernel.Admit: %v", err)
	}

	// ── Start the fake spawner BEFORE calling Run ─────────────────────────────
	fakeSpawnerForJudge(t, ctx, bus, rootDir, sessionID, string(taskID))

	// ── Build strategy with Judge ─────────────────────────────────────────────
	strat := schedulerplan.NewWithConfig(schedulerplan.Config{
		Caps: dispatch.Capabilities{
			RootDir:     rootDir,
			LeafKind:    "react-leaf",
			AllowSubmit: true,
		},
		Judge: schedulerplan.NewPlanJudge(),
	})

	deps := dispatch.Deps{
		Reader:  kernel,
		Bus:     bus,
		Staging: staging,
	}

	// ── Run — expect judge to block the invalid plan ──────────────────────────
	_, runErr := strat.Run(ctx, taskID, deps)
	if runErr == nil {
		t.Fatal("expected Run to return an error from PlanJudge, got nil")
	}
	if !strings.Contains(runErr.Error(), "plan validation failed") {
		t.Fatalf("expected error to contain %q, got: %v", "plan validation failed", runErr)
	}
}
