package middlewares

import (
	"context"

	"harnessclaw-go/internal/engine/scheduler"
	pkgtypes "harnessclaw-go/pkg/types"
)

// AgentContext 把 SpawnState 的身份字段塞进 context.Value。
// 下游 Runtime / loop / logger 用 AgentCtxFrom(ctx) 取，自动给 metric/trace 打标。
type AgentContext struct{}

type agentCtxKey struct{}

type AgentCtxValue struct {
	AgentID       pkgtypes.AgentID
	TaskID        pkgtypes.TaskID
	ParentAgentID pkgtypes.AgentID
	// ParentStepID 是父 agent 调用 spawn 时的 tool_use_id ——
	// wire 翻译层用它把 sub-agent 卡挂到正确的父 tool_use 节点之下。
	// 缺这个会导致 sub-agent 错挂到祖父的 tool_use 上，UI 层级错乱。
	ParentStepID  string
	SessionID     pkgtypes.SessionID
	RootSessionID pkgtypes.SessionID
	SubagentType  string // 来自 Definition.Name（agent definition 名）—— 用于观察性 label
	InvokedBy     scheduler.Invoker
}

func WithAgentCtx(ctx context.Context, v AgentCtxValue) context.Context {
	return context.WithValue(ctx, agentCtxKey{}, v)
}

func AgentCtxFrom(ctx context.Context) (AgentCtxValue, bool) {
	v, ok := ctx.Value(agentCtxKey{}).(AgentCtxValue)
	return v, ok
}

func (AgentContext) Name() string { return "agent_context" }

func (AgentContext) Before(ctx context.Context, p scheduler.SpawnParams, st *scheduler.SpawnState) (context.Context, error) {
	v := AgentCtxValue{
		AgentID:      st.AgentID,
		TaskID:       st.TaskID,
		SubagentType: p.Definition.Name, // 用 Definition.Name 作为 observability 标签
		InvokedBy:    p.InvokedBy,
	}
	if p.Parent != nil {
		v.ParentAgentID = p.Parent.AgentID
		v.ParentStepID = p.Parent.StepID
		v.SessionID = p.Parent.SessionID
		v.RootSessionID = p.Parent.RootSessionID
	}
	return WithAgentCtx(ctx, v), nil
}

func (AgentContext) After(context.Context, scheduler.SpawnParams, *scheduler.SpawnState, scheduler.Result, error) {
}
