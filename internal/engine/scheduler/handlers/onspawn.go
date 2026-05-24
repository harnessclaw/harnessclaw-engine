package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// OnSpawnHandler processes KindControl/CmdSpawn messages, implementing a
// saga-style topological Derive: LocalID→TaskID mapping, dep resolution,
// rollback on failure, and MarkReady+KindTask for leaf-ready children.
// Spec §5.5.4 (v3.1-R4/R8/R11).
type OnSpawnHandler struct {
	reader tstate.Reader
	writer tstate.Writer
	bus    msgbus.Bus
}

func NewOnSpawn(r tstate.Reader, w tstate.Writer, bus msgbus.Bus) *OnSpawnHandler {
	return &OnSpawnHandler{reader: r, writer: w, bus: bus}
}

func (h *OnSpawnHandler) Handle(ctx context.Context, msg msgbus.AgentMessage) {
	p, ok := msg.Payload.(msgbus.ControlPayload)
	if !ok || p.Cmd != msgbus.CmdSpawn {
		return
	}
	body, ok := p.Body.(msgbus.SpawnBody)
	if !ok {
		slog.ErrorContext(ctx, "onspawn.bad_body", slog.String("task_id", msg.TaskID))
		return
	}
	parentID := types.TaskID(msg.TaskID)

	// Parse all specs from the SpawnBody (may be map or already spec.TaskSpec)
	specs, err := parseSpecs(body.Specs)
	if err != nil {
		h.publishSpawnFailed(ctx, msg, fmt.Sprintf("parse specs: %v", err))
		return
	}
	if len(specs) == 0 {
		h.publishSpawnFailed(ctx, msg, "empty specs")
		return
	}

	// Derive in order (topological: caller is responsible for providing them in order).
	// Build localID → TaskID map as we go, resolving Deps references.
	localToTaskID := map[string]types.TaskID{}
	derived := make([]types.TaskID, 0, len(specs))

	for i, sp := range specs {
		resolved := resolveDepRefs(sp.Deps, localToTaskID)
		sp.Deps = resolved

		id, deriveErr := h.writer.Derive(ctx, parentID, sp)
		if deriveErr != nil {
			// Saga rollback: delete all already-derived children
			for _, rid := range derived {
				if rbErr := h.writer.RollbackAdmit(ctx, rid); rbErr != nil {
					slog.ErrorContext(ctx, "onspawn.rollback_failed",
						slog.String("task_id", string(rid)),
						slog.String("err", rbErr.Error()),
					)
				}
			}
			h.publishSpawnFailed(ctx, msg, fmt.Sprintf("derive spec[%d] %q: %v", i, sp.LocalID, deriveErr))
			return
		}
		derived = append(derived, id)
		if sp.LocalID != "" {
			localToTaskID[sp.LocalID] = id
		}
	}

	// For children with no deps: MarkReady + publish KindTask (R8 close loop)
	for j, id := range derived {
		if len(specs[j].Deps) == 0 {
			if err := h.writer.MarkReady(ctx, id); err != nil {
				slog.ErrorContext(ctx, "onspawn.markready_failed",
					slog.String("task_id", string(id)),
					slog.String("err", err.Error()),
				)
				continue
			}
			cur, readErr := h.reader.Get(ctx, id)
			if readErr != nil {
				continue
			}
			_ = h.bus.Publish(ctx, msgbus.AgentMessage{
				MsgID:   fmt.Sprintf("task:%s:ready", string(id)),
				Kind:    msgbus.KindTask,
				To:      msgbus.AddrQueue("leaf"),
				TaskID:  string(id),
				Payload: msgbus.TaskMessage{TaskID: string(id), TaskType: "leaf", Task: cur.LeafSpec.Goal},
			})
		}
	}

	// Publish spawn_granted with list of derived TaskIDs
	spawnedStrs := make([]string, len(derived))
	for i, id := range derived {
		spawnedStrs[i] = string(id)
	}
	_ = h.bus.Publish(ctx, msgbus.AgentMessage{
		MsgID:   fmt.Sprintf("%s:spawn_granted", msg.MsgID),
		Kind:    msgbus.KindNotify,
		To:      msgbus.AddrAgent(msg.TaskID),
		TaskID:  msg.TaskID,
		Payload: msgbus.NotifyPayload{Event: msgbus.NotifySpawnGranted, TaskID: msg.TaskID, SpawnedIDs: spawnedStrs},
	})
}

func (h *OnSpawnHandler) publishSpawnFailed(ctx context.Context, msg msgbus.AgentMessage, reason string) {
	_ = h.bus.Publish(ctx, msgbus.AgentMessage{
		MsgID:  fmt.Sprintf("%s:spawn_failed", msg.MsgID),
		Kind:   msgbus.KindNotify,
		To:     msgbus.AddrAgent(msg.TaskID),
		TaskID: msg.TaskID,
		Payload: msgbus.NotifyPayload{
			Event:  msgbus.NotifySpawnFailed,
			TaskID: msg.TaskID,
			Reason: reason,
		},
	})
}

// resolveDepRefs converts LocalID references to absolute TaskIDs using the mapping built so far.
// References that are already absolute TaskIDs (not found in localToTaskID) are passed through.
func resolveDepRefs(deps []spec.DepRef, localToTaskID map[string]types.TaskID) []spec.DepRef {
	if len(deps) == 0 {
		return deps
	}
	out := make([]spec.DepRef, len(deps))
	for i, dep := range deps {
		s := string(dep)
		if id, ok := localToTaskID[s]; ok {
			out[i] = spec.DepRef(string(id))
		} else {
			out[i] = dep // already an absolute TaskID or pass-through
		}
	}
	return out
}

// parseSpecs converts []any (from SpawnBody.Specs) to []spec.TaskSpec.
// Each element may be a spec.TaskSpec (in-process calls) or a map (JSON-decoded).
func parseSpecs(raw []any) ([]spec.TaskSpec, error) {
	specs := make([]spec.TaskSpec, 0, len(raw))
	for i, item := range raw {
		switch v := item.(type) {
		case spec.TaskSpec:
			specs = append(specs, v)
		case map[string]any:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("spec[%d]: marshal: %w", i, err)
			}
			var sp spec.TaskSpec
			if err := json.Unmarshal(b, &sp); err != nil {
				return nil, fmt.Errorf("spec[%d]: unmarshal: %w", i, err)
			}
			specs = append(specs, sp)
		default:
			return nil, fmt.Errorf("spec[%d]: unsupported type %T", i, item)
		}
	}
	return specs, nil
}
