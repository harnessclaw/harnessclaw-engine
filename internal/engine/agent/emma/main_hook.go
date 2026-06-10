package emma

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// emmaLoopState is the private cross-hook state for one ProcessMessage
// invocation. Replaces the older `loopState` struct that runner.go's
// legacy run() used to keep on its own stack.
//
// All fields are mutated by the observation hooks (OnLLMResponse most
// frequently). lastStopReason is the cross-hook bridge: OnLLMResponse
// computes it, OnToolsDispatched reads it to gate the NextRoundThinking
// event.
type emmaLoopState struct {
	lastUsage       *types.Usage
	lastStopReason  string
	cumulativeUsage types.Usage
}

// emmaHooks builds the observation hooks that emma plugs into
// loop.Config.Hooks. They reproduce the four L1-specific event emissions
// the legacy runner.go did inline:
//
//   OnTurnStart       → EngineEventMessageStart
//   OnLLMResponse     → EngineEventMessageDelta + EngineEventMessageStop
//   OnLLMError        → EngineEventError + MessageDelta{error} + MessageStop
//                        + ctx.Cause diagnostic log
//   OnToolsDispatched → EngineEventNextRoundThinking (gated on stopReason)
//
// ctx is captured for the OnLLMError ctx.Cause read; ls is shared
// mutable state across all four hooks.
func (e *Engine) emmaHooks(
	ctx context.Context,
	sess *session.Session,
	out chan<- types.EngineEvent,
	ls *emmaLoopState,
	prov provider.Provider,
) loop.Hooks {
	return loop.Hooks{
		OnTurnStart: func(turn int) {
			// MessageStart carries the cumulative input-token count as
			// seen BEFORE this turn's LLM call. Same shape as runner.go
			// emitted at runner.go:159-166.
			msgID := "msg_" + uuid.New().String()[:8]
			out <- types.EngineEvent{
				Type:      types.EngineEventMessageStart,
				MessageID: msgID,
				Model:     prov.Name(),
				Usage: &types.Usage{
					InputTokens: ls.cumulativeUsage.InputTokens,
				},
			}
		},

		OnLLMResponse: func(turn int, snap loop.LLMResponseSnapshot) {
			// 1. Accumulate usage (old runner.go:217-223).
			if snap.LastUsage != nil {
				ls.lastUsage = snap.LastUsage
				ls.cumulativeUsage.InputTokens += snap.LastUsage.InputTokens
				ls.cumulativeUsage.OutputTokens += snap.LastUsage.OutputTokens
				ls.cumulativeUsage.CacheRead += snap.LastUsage.CacheRead
				ls.cumulativeUsage.CacheWrite += snap.LastUsage.CacheWrite
			}

			// 2. Synthesize stopReason when upstream omitted one
			// (old runner.go:225-232). The result is stashed on ls so
			// OnToolsDispatched can gate the NextRoundThinking event.
			stopReason := snap.StopReason
			if stopReason == "" {
				if hasToolUse(snap.AssistantMsg) {
					stopReason = "tool_use"
				} else {
					stopReason = "end_turn"
				}
			}
			ls.lastStopReason = stopReason

			// 3. Emit MessageDelta + MessageStop (old runner.go:233-239).
			//
			// NOTE event-ordering shift vs legacy:
			//   - Legacy: MessageDelta/MessageStop fired BEFORE
			//     sess.AddMessage(assistantMsg).
			//   - New: loop.Run does sess.AddMessage first (loop.go L141),
			//     THEN calls this hook. So MessageStop now post-dates the
			//     session insert by a couple of lines.
			// The wire protocol does not depend on session-internal state
			// at MessageStop time, so this reorder is consumer-invisible.
			out <- types.EngineEvent{
				Type:       types.EngineEventMessageDelta,
				StopReason: stopReason,
				Usage:      snap.LastUsage,
			}
			out <- types.EngineEvent{Type: types.EngineEventMessageStop}
		},

		OnLLMError: func(turn int, err error) {
			// Emit the three terminal events the legacy runner produced
			// at runner.go:173-175.
			out <- types.EngineEvent{Type: types.EngineEventError, Error: err}
			out <- types.EngineEvent{Type: types.EngineEventMessageDelta, StopReason: "error", Error: err}
			out <- types.EngineEvent{Type: types.EngineEventMessageStop}

			// ctx.Cause diagnostic log. Mirrors runner.go:177-209: split
			// into "ctx already cancelled" (engine-side abort) vs "ctx
			// still live" (upstream / bifrost error) branches.
			cause := context.Cause(ctx)
			causeStr := "<nil>"
			if cause != nil {
				causeStr = cause.Error()
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				e.logger.Warn("LLM call returned with ctx already cancelled",
					zap.String("session_id", sess.ID),
					zap.Int("turn", turn),
					zap.String("ctx_err", ctxErr.Error()),
					zap.String("ctx_cause", causeStr),
					zap.Error(err))
			} else {
				e.logger.Error("LLM call failed after retries",
					zap.String("session_id", sess.ID),
					zap.Int("turn", turn),
					zap.String("ctx_err", "<nil/live>"),
					zap.String("ctx_cause", causeStr),
					zap.Error(err))
			}
		},

		OnToolsDispatched: func(turn int, calls []types.ToolCall, results []types.ToolResult) {
			// Only fires when at least one tool was called (loop.Run
			// guarantees this). Replicate runner.go:292-301 — emit
			// NextRoundThinking when the model didn't naturally end_turn.
			// stopReason was synthesized in OnLLMResponse just above.
			if ls.lastStopReason != "end_turn" {
				out <- types.EngineEvent{Type: types.EngineEventNextRoundThinking, AgentID: ""}
			}
		},
	}
}

// emmaMainHook is the OnTurnComplete TurnHook. It owns control flow:
//
//   - No tool calls → check whether any async sub-agent is still running;
//     if so, block on the mailbox for a notification and Inject it back.
//     Otherwise return Terminate{Completed}.
//   - Tool calls present → return Decision{} (continue with the next turn).
//
// ctx and mailbox are captured by closure.
func (e *Engine) emmaMainHook(
	ctx context.Context,
	sess *session.Session,
	mailbox *agent.Mailbox,
	out chan<- types.EngineEvent,
) loop.TurnHook {
	return func(snap loop.TurnSnapshot) loop.Decision {
		if !snap.HadToolCalls {
			if e.shouldWaitForAsyncAgents(sess.ID, mailbox) {
				if msg := e.waitForMailboxMessage(ctx, sess.ID, mailbox, out); msg != nil {
					return loop.Decision{Inject: []types.Message{*msg}}
				}
			}
			return loop.Decision{Terminate: &types.Terminal{
				Reason:  types.TerminalCompleted,
				Message: "model finished",
				Turn:    snap.Turn,
			}}
		}
		return loop.Decision{}
	}
}

// hasToolUse reports whether an assistant message carries any tool_use
// blocks. Local helper so emmaHooks doesn't have to import the legacy
// common package.
func hasToolUse(msg types.Message) bool {
	for _, b := range msg.Content {
		if b.Type == types.ContentTypeToolUse {
			return true
		}
	}
	return false
}
