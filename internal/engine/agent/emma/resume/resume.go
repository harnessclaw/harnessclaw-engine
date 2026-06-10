// Package resume provides default implementations of wait.Resumer.
//
// TextResumer is the simplest viable strategy: synthesise a follow-up
// user.message that carries the answer, dispatch through the engine's
// existing MessageHandler, and let the LLM continue with the augmented
// context. This works for any model that's good at following multi-turn
// conversation structure (i.e. all production LLMs).
//
// Future strategies — once the engine exposes a "resume in place" entry
// point that re-attaches the answer to the original tool_use_id — can
// be added here without changing the channel layer wiring.
package resume

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/wait"
	"harnessclaw-go/internal/services/api/router"
	"harnessclaw-go/pkg/types"
)

// TextResumer drives recovery via the engine's standard MessageHandler.
// On Resume, it synthesises a new user.message carrying the user's
// answer (framed with context — "you asked X, the user said Y") and
// hands it to the engine. The engine treats it as a normal turn; the
// LLM sees the original prompt + the user's answer in the message
// history and continues sensibly.
//
// Limitations (fine for alpha; refine when needed):
//
//   - The original tool_use_id is left "dangling" in messages. LLMs
//     handle this gracefully — they see "I asked, the user answered" —
//     but a more surgical resumer (one that injects a tool_result with
//     the matching tool_use_id) would be cleaner. That requires engine
//     surgery; this works today.
//   - For sub-agent prompts (Anchor.AgentPath != "main") the synthesised
//     message goes to the top-level agent; the sub-agent's pending tool
//     call is effectively abandoned. Phase 2 will route to the right
//     sub-agent.
type TextResumer struct {
	handler router.MessageDispatcher
	logger  *zap.Logger
}

// New constructs a TextResumer. The handler is the same engine entry
// point that user.message normally goes through (typically the router's
// Handle method).
func New(handler router.MessageDispatcher, logger *zap.Logger) *TextResumer {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TextResumer{handler: handler, logger: logger.Named("resume")}
}

// Resume implements wait.Resumer.
func (r *TextResumer) Resume(ctx context.Context, w *wait.PendingWait, ans wait.Answer) error {
	if w == nil {
		return fmt.Errorf("Resume: nil wait")
	}
	r.logger.Info("resuming session from persisted wait",
		zap.String("session_id", w.SessionID),
		zap.String("request_id", w.RequestID),
		zap.String("kind", string(w.Kind)),
		zap.String("decision", ans.Decision),
	)
	text := buildContinuation(w.Kind, ans)
	in := &types.IncomingMessage{
		ChannelName: "websocket",
		SessionID:   w.SessionID,
		Text:        text,
		RawPayload: map[string]any{
			"resumed_wait":  w.RequestID,
			"resumed_kind":  string(w.Kind),
			"correlation":   w.CorrelationID,
		},
	}
	return r.handler(ctx, in)
}

// buildContinuation produces the synthetic user.message text. The LLM
// reads it as "the user is now answering the question I just asked".
// Phrasing matters — these strings end up in the LLM transcript, so
// they must read as natural multi-turn user input.
func buildContinuation(kind wait.Kind, ans wait.Answer) string {
	switch kind {
	case wait.KindQuestion:
		if ans.Decision != "approved" {
			return "(用户未回答上一个问题，已取消。请基于已有信息继续，或转换思路。)"
		}
		if ans.Output == "" {
			return "(用户回应了问题，但没有给出具体答案。请基于现有上下文继续。)"
		}
		return ans.Output

	case wait.KindPermission:
		if ans.Decision == "approved" {
			return "(用户已批准刚才请求的权限，请继续。)"
		}
		return "(用户拒绝了权限请求。请尝试其他方式或停止此操作。)"

	case wait.KindPlanReview:
		if ans.Decision != "approved" {
			if ans.Output != "" {
				return fmt.Sprintf("(用户拒绝了 plan，理由：%s。请重新规划。)", ans.Output)
			}
			return "(用户拒绝了 plan。请重新规划。)"
		}
		if edits, ok := ans.Edits["updated_steps"]; ok {
			return fmt.Sprintf("(用户编辑并批准了 plan：%v。请按用户编辑后的步骤执行。)", edits)
		}
		return "(用户原样批准了 plan，请按原计划执行。)"

	default:
		return fmt.Sprintf("(用户对一个 %s 类型的提示作出响应：%s)", kind, ans.Output)
	}
}
