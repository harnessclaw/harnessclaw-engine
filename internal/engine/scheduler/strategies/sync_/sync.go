package sync_

import (
	"context"
	"time"

	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/diskout"
	"harnessclaw-go/internal/engine/scheduler/runtime"
	"harnessclaw-go/internal/engine/scheduler/tasks"
	pkgtypes "harnessclaw-go/pkg/types"
)

type Strategy struct {
	rt      runtime.Runtime
	taskMgr tasks.Manager
	diskOut diskout.Store // 仅 Ctrl+B 切换时用
}

func New(deps scheduler.Deps) *Strategy {
	return &Strategy{rt: deps.Runtime, taskMgr: deps.TaskMgr, diskOut: deps.DiskOutput}
}

func (s *Strategy) Name() string                          { return "sync" }
func (s *Strategy) CanHandle(_ scheduler.SpawnParams) bool { return true } // 兜底

func (s *Strategy) Subscribe(context.Context, pkgtypes.TaskID) (<-chan pkgtypes.EngineEvent, error) {
	return nil, scheduler.ErrNotSubscribable
}

func (s *Strategy) Spawn(ctx context.Context, p scheduler.SpawnParams, st *scheduler.SpawnState) (scheduler.Result, error) {
	startedAt := time.Now()

	bgSignal := s.taskMgr.RegisterForeground(ctx, st.TaskID, st.AgentID)
	defer s.taskMgr.UnregisterForeground(st.TaskID)

	msgs, err := s.rt.Run(ctx, runtime.RunParams{
		AgentID:    st.AgentID,
		Definition: p.Definition,
		Prompt:     p.Prompt,
		Inputs:     p.Inputs,
		InputPaths: p.InputPaths,
		Overrides: runtime.Overrides{
			Model:      p.Overrides.Model,
			MaxTurns:   p.Overrides.MaxTurns,
			Permission: p.Overrides.Permission,
		},
	})
	if err != nil {
		return scheduler.Result{}, err
	}

	var (
		content      []pkgtypes.ContentBlock
		toolCalls    int
		terminal     pkgtypes.Terminal
		usage        pkgtypes.Usage
		deliverables []pkgtypes.Deliverable
		denied       []string
		artifacts    []pkgtypes.ArtifactRef
	)

	for {
		select {
		case <-ctx.Done():
			return scheduler.Result{}, ctx.Err()

		case <-bgSignal:
			return s.handoffToBackground(ctx, p, st, msgs, content, usage, toolCalls, startedAt)

		case evt, ok := <-msgs:
			if !ok {
				return scheduler.Result{
					Status:     scheduler.StatusCompleted,
					StartedAt:  startedAt,
					FinishedAt: time.Now(),
					Usage:      usage,
					Outcome: scheduler.SyncOutcome{
						Content:      content,
						Terminal:     terminal,
						ToolCalls:    toolCalls,
						Deliverables: deliverables,
						DeniedTools:  denied,
						Artifacts:    artifacts,
					},
				}, nil
			}
			// EngineEventDone / EngineEventError 是 sub-agent 自己的终止信号，
			// SyncStrategy.accumulate 本地消费即可。fan-out 给父事件流会让
			// 父（emma）的 wire 翻译层把这条 Done 当作父自己的 turn 结束，
			// 导致 emma 在 dispatch 后续的 LLM 调用被翻译层错开成 turn N+1
			// （UI 上 emma 的 prelude 和 final summary 落在两个 turn 里）。
			if p.Events != nil && evt.Type != pkgtypes.EngineEventDone && evt.Type != pkgtypes.EngineEventError {
				if evt.Type == pkgtypes.EngineEventPermissionRequest {
					// 权限请求绝不能丢：sub-agent 的执行器阻塞等待
					// root UI 的应答，丢帧 = 弹窗永不出现、sub-agent
					// 卡死到 ctx 取消。阻塞式 fan-out（带 ctx 逃生口）。
					select {
					case p.Events <- evt:
					case <-ctx.Done():
						return scheduler.Result{}, ctx.Err()
					}
				} else {
					select {
					case p.Events <- evt:
					default:
					}
				}
			}
			s.accumulate(evt, &content, &toolCalls, &terminal, &usage, &deliverables, &denied, &artifacts)
		}
	}
}

func (s *Strategy) handoffToBackground(
	ctx context.Context, p scheduler.SpawnParams, st *scheduler.SpawnState,
	msgs <-chan pkgtypes.EngineEvent,
	contentSoFar []pkgtypes.ContentBlock,
	_ pkgtypes.Usage, _ int, startedAt time.Time,
) (scheduler.Result, error) {
	bgCtx, bgCancel := context.WithCancel(context.WithoutCancel(ctx))
	st.AbortCtx, st.AbortCancel = bgCtx, bgCancel

	err := s.taskMgr.Register(bgCtx, tasks.RegisterParams{
		TaskID:       st.TaskID,
		AgentID:      st.AgentID,
		Name:         p.Name,
		Description:  p.Description,
		SubagentType: p.Definition.Name,
		Strategy:     "sync→async",
		StartedAt:    startedAt,
		InvokedBy: tasks.InvokerSnapshot{
			Kind:     string(p.InvokedBy.Kind),
			Source:   p.InvokedBy.Source,
			ParentID: p.InvokedBy.ParentID,
		},
	})
	if err != nil {
		bgCancel()
		return scheduler.Result{}, err
	}

	writer, err := s.diskOut.Open(st.TaskID)
	if err != nil {
		bgCancel()
		return scheduler.Result{}, err
	}

	// 已收的 content backfill 写盘
	for _, blk := range contentSoFar {
		_ = writer.AppendBlock(blk)
	}

	go func() {
		defer writer.Close()
		defer bgCancel()
		for evt := range msgs {
			_ = writer.Append(evt)
			_ = s.taskMgr.Tick(bgCtx, st.TaskID, evt)
			// 切后台后不再 fan-out 给 p.Events
		}
		_ = s.taskMgr.Complete(bgCtx, st.TaskID)
	}()

	return scheduler.Result{
		Status:     scheduler.StatusAsyncLaunched,
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
		Outcome: scheduler.AsyncOutcome{
			OutputFile: s.diskOut.Path(st.TaskID),
			Tailable:   true,
		},
	}, nil
}

// accumulate 按 EngineEvent.Type 分支累计 SyncOutcome 的输出字段。
// 字段映射参考 pkg/types/event.go 的事件类型。
func (s *Strategy) accumulate(evt pkgtypes.EngineEvent,
	content *[]pkgtypes.ContentBlock, toolCalls *int,
	terminal *pkgtypes.Terminal, usage *pkgtypes.Usage,
	deliverables *[]pkgtypes.Deliverable, denied *[]string,
	artifacts *[]pkgtypes.ArtifactRef,
) {
	// Artifacts 可能挂在任意事件类型（tool_end / subagent_end / 自定义）
	if len(evt.Artifacts) > 0 {
		*artifacts = append(*artifacts, evt.Artifacts...)
	}
	// DeniedTools 主要挂 subagent_end，但任意带 DeniedTools 的帧都吸收
	if len(evt.DeniedTools) > 0 {
		*denied = append(*denied, evt.DeniedTools...)
	}

	switch evt.Type {
	case pkgtypes.EngineEventText:
		// 文本流：把所有 text chunk 拼到当前 ContentBlock 末尾；
		// 简化处理：每次新增一个 block（消费方拼接也无害），后续可改为
		// "同一 message_start 内合并"。
		*content = append(*content, pkgtypes.ContentBlock{
			Type: pkgtypes.ContentTypeText,
			Text: evt.Text,
		})
	case pkgtypes.EngineEventToolUse, pkgtypes.EngineEventToolCall:
		*toolCalls++
	case pkgtypes.EngineEventDeliverable:
		if evt.Deliverable != nil {
			*deliverables = append(*deliverables, *evt.Deliverable)
		}
	case pkgtypes.EngineEventMessageDelta:
		// message_delta 携带 Usage / stop_reason
		if evt.Usage != nil {
			*usage = *evt.Usage
		}
	case pkgtypes.EngineEventDone:
		// 终止帧：Terminal + 最终 Usage
		if evt.Terminal != nil {
			*terminal = *evt.Terminal
		}
		if evt.Usage != nil {
			*usage = *evt.Usage
		}
	}
	// 其他事件类型对 SyncOutcome 累计无影响 —— 已经透传给 p.Events，前端 UI 看得到。
}

// 编译期接口实现检查
var _ scheduler.Strategy = (*Strategy)(nil)
