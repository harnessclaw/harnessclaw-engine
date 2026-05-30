package mention

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/spawn2"
	"harnessclaw-go/pkg/types"
)

// Router detects @agent_name at the start of a user message and, if
// the named agent is registered, spawns it directly (bypassing the
// host engine's main loop).
type Router struct {
	spawner *spawn2.Spawner
	defReg  *agent.AgentDefinitionRegistry
	parser  *agent.MentionParser
	logger  *zap.Logger
}

// NewRouter constructs a Router. parser may be created from defReg via
// agent.NewMentionParser; passed in to allow reuse / testing.
func NewRouter(spawner *spawn2.Spawner, defReg *agent.AgentDefinitionRegistry, parser *agent.MentionParser) *Router {
	return &Router{
		spawner: spawner,
		defReg:  defReg,
		parser:  parser,
		logger:  zap.NewNop(),
	}
}

// TryRoute inspects msg's text. If it starts with @agent_name matching
// a registered AgentDefinition, returns a channel where the spawn's
// events stream and the dispatch result terminates. Returns nil when
// no mention is detected — caller falls through to its main path.
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

		cfg := &agent.SpawnConfig{
			Prompt:          match.Message,
			SubagentType:    def.Name,
			Name:            def.Name,
			Description:     def.Description,
			AgentType:       def.AgentType,
			ParentSessionID: sess.ID,
			ParentAgentID:   "main",
			ParentOut:       out,
		}

		out <- types.EngineEvent{
			Type:      types.EngineEventAgentRouted,
			AgentName: def.Name,
			AgentDesc: def.Description,
		}

		result, err := r.spawner.Sync(ctx, cfg)
		if err != nil {
			out <- types.EngineEvent{Type: types.EngineEventError, Error: err}
			out <- types.EngineEvent{
				Type: types.EngineEventDone,
				Terminal: &types.Terminal{
					Reason:  types.TerminalModelError,
					Message: err.Error(),
				},
			}
			return
		}
		out <- types.EngineEvent{
			Type:     types.EngineEventDone,
			Terminal: result.Terminal,
			Usage:    result.Usage,
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
