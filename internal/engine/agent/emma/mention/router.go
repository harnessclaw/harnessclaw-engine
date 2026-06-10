package mention

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/pkg/types"
)

// Router detects @agent_name at the start of a user message and, if
// the named agent is registered, spawns it directly via the new
// scheduler.Scheduler dispatch entry (sync mode).
type Router struct {
	sched  scheduler.Scheduler
	defReg *definition.Registry
	parser *Parser
	logger *zap.Logger
}

// NewRouter constructs a Router. parser may be created from defReg via
// mention.NewParser; passed in to allow reuse / testing.
func NewRouter(sched scheduler.Scheduler, defReg *definition.Registry, parser *Parser) *Router {
	return &Router{
		sched:  sched,
		defReg: defReg,
		parser: parser,
		logger: zap.NewNop(),
	}
}

// TryRoute inspects msg's text. If it starts with @agent_name matching
// a registered AgentDefinition, returns a channel where the spawn's
// events stream and the dispatch result terminates. Returns nil when
// no mention is detected — caller falls through to its main path.
//
// The goroutine appends msg to sess before spawning, so callers MUST
// NOT call sess.AddMessage(*msg) themselves when TryRoute returns a
// non-nil channel.
//
// Caller is responsible for closing/draining the channel.
func (r *Router) TryRoute(ctx context.Context, sess *session.Session, msg *types.Message) <-chan types.EngineEvent {
	text := extractText(msg)
	match := r.parser.Parse(text)
	if !match.Matched {
		return nil
	}
	def := r.defReg.Get(match.AgentName)
	if def == nil {
		return nil
	}

	out := make(chan types.EngineEvent, 64)
	go func() {
		defer close(out)
		sess.AddMessage(*msg)

		out <- types.EngineEvent{
			Type:      types.EngineEventAgentRouted,
			AgentName: def.Name,
			AgentDesc: def.Description,
		}

		// Prepend the agent definition's SystemPrompt to the
		// user-supplied prompt when present, matching the legacy
		// emma.ProcessWithAgent behavior.
		promptText := match.Message
		if def.SystemPrompt != "" {
			promptText = def.SystemPrompt + "\n\n" + match.Message
		}

		if def.AutoTeam && len(def.SubAgents) > 0 {
			r.logger.Info("team workflow not yet implemented, falling back to single agent",
				zap.String("agent", def.Name),
			)
		}

		// 走新 scheduler.Dispatch（sync 模式默认）。
		// p.Events 透传给父订阅者；strategy 内部 fan-out 每帧 evt 给 out。
		res, err := r.sched.Dispatch(ctx, scheduler.SpawnParams{
			Definition:  *def,
			Prompt:      promptText,
			Name:        def.Name,
			Description: def.Description,
			Parent: &scheduler.ParentRef{
				AgentID:   "main",
				SessionID: types.SessionID(sess.ID),
			},
			InvokedBy: scheduler.Invoker{Kind: scheduler.InvokerMention, Source: def.Name},
			Overrides: scheduler.Overrides{Model: def.Model, MaxTurns: def.MaxTurns},
			Events:    out,
		})
		if err != nil {
			out <- types.EngineEvent{Type: types.EngineEventError, Error: err}
			out <- types.EngineEvent{
				Type: types.EngineEventDone,
				Terminal: &types.Terminal{
					Reason:  types.TerminalModelError,
					Message: err.Error(),
				},
				Usage: &types.Usage{},
			}
			return
		}
		// Sync 路径才有 SyncOutcome —— 拿 Terminal / Usage 发 Done event。
		var terminal *types.Terminal
		if sync, ok := res.Outcome.(scheduler.SyncOutcome); ok {
			terminal = &sync.Terminal
		}
		out <- types.EngineEvent{
			Type:     types.EngineEventDone,
			Terminal: terminal,
			Usage:    &res.Usage,
		}
	}()
	return out
}

func extractText(msg *types.Message) string {
	if msg == nil {
		return ""
	}
	for _, b := range msg.Content {
		if b.Type == types.ContentTypeText {
			return b.Text
		}
	}
	return ""
}
