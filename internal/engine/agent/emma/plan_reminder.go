package emma

import (
	"fmt"
	"os"
	"sync"
	"time"

	"harnessclaw-go/pkg/types"
)

// ActivePlan 记录一个 emma session 当前的 plan 上下文 —— 由 plan agent dispatch
// 成功后，dispatch tool 调 ActivePlanStore.Set 写入；每轮新 user message 处理时，
// emma 从 store 取出最新 plan 内容前置注入到 user message 头部，让 LLM
// 在多 turn 对话里持续看见正确的 plan 文件路径 + 全文。
type ActivePlan struct {
	// Path 是 plan agent 写出来的 plan.md 绝对路径（task_dir 内）。
	Path string
	// TaskID 是 plan agent 跑这一次的 task id —— 便于后续审计 / 跨 turn 关联。
	TaskID string
	// CreatedAt 是写入时间 —— 用于排序、超时清理等后续迭代。
	CreatedAt time.Time
}

// ActivePlanStore 是 emma session ID → ActivePlan 的进程内 map。
//
// MVP 设计要点：
//   - 单 process 内存态：进程重启即清空（这是有意为之 —— task_dir 在 ~/.harnessclaw
//     下，进程重启后无法保证 plan 文件还有效，让用户重新派 plan 比误用旧 plan 安全）
//   - 一个 session 只能有一个 active plan：后派的覆盖先前的（Claude Code 同行为）
//   - 文件读不到时由调用方负责 Clear —— store 自身不做 file IO
//
// 后续可迭代方向（按需）：
//   - 持久化到 SQLite，跨重启可恢复
//   - 文件 watch 机制，user 手动改 plan.md 时 store 也跟新
//   - 大小护栏：plan 全文超过阈值时返回 head + tail + "...截断" hint
//   - 显式失活：用户说"忽略当前 plan" 时 emma 调 Clear
type ActivePlanStore struct {
	mu    sync.RWMutex
	plans map[string]ActivePlan // key: emma 主 sessionID
}

// NewActivePlanStore 返回一个空 store。一个进程持有一个。
func NewActivePlanStore() *ActivePlanStore {
	return &ActivePlanStore{
		plans: make(map[string]ActivePlan),
	}
}

// Set 写入或覆盖 sessionID 对应的 active plan。多次 plan dispatch 时
// 后者覆盖前者。
//
// 签名跟 scheduler.PlanReminderSink interface 对齐 —— *ActivePlanStore
// 满足该接口，所以可以直接当 sink 注入 dispatch tool。CreatedAt 在调用
// 时间戳上由本方法自填，调用方不用关心。
func (s *ActivePlanStore) Set(sessionID, planPath, taskID string) {
	if s == nil || sessionID == "" || planPath == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans[sessionID] = ActivePlan{
		Path:      planPath,
		TaskID:    taskID,
		CreatedAt: time.Now(),
	}
}

// Get 返回 sessionID 对应的 active plan（按值拷贝）。第二返回值 false 表示无活跃 plan。
func (s *ActivePlanStore) Get(sessionID string) (ActivePlan, bool) {
	if s == nil || sessionID == "" {
		return ActivePlan{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	plan, ok := s.plans[sessionID]
	return plan, ok
}

// Clear 删除 sessionID 对应的 active plan。
// 调用场景：plan 文件已被删 / 用户手动失活 / session 结束清理。
func (s *ActivePlanStore) Clear(sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.plans, sessionID)
}

// BuildReminder 读 plan.md 文件内容，拼成 <plan-reminder> 块。
// 返回空字符串表示无须注入（未注册 plan / 文件读取失败 —— 后者会顺手 Clear 掉
// store 里的脏记录，下次 user 消息就不再尝试注入）。
//
// 调用点：emma.ProcessMessage 处理新 user message 之前。
//
// 不在 ActivePlanStore 类型里做这个，是因为 store 不持有 fs / 不需要 imports
// io/os；本函数把 IO 边界限定在调用点。
func (s *ActivePlanStore) BuildReminder(sessionID string) string {
	plan, ok := s.Get(sessionID)
	if !ok {
		return ""
	}
	body, err := os.ReadFile(plan.Path)
	if err != nil {
		// 文件被删 / 移动 / 权限丢失 —— 清理脏 state，本轮不注入。
		s.Clear(sessionID)
		return ""
	}
	return fmt.Sprintf(
		"<plan-reminder>\n"+
			"A plan file exists from plan mode at: %s\n"+
			"Plan contents:\n\n%s\n\n"+
			"If this plan is relevant and not already complete, continue working on it.\n"+
			"派 freelancer / content_creator 执行 plan 时，task 字段里直接引用上面这个绝对路径。\n"+
			"</plan-reminder>",
		plan.Path,
		string(body),
	)
}

// injectPlanReminder 把 reminder 前置到 user message 的第一个 text block。
// reminder 为空时 noop（无 plan 注册 / 文件读不到的场景）。
//
// 实现策略：找第一个 text block，前置 reminder + 两个换行；如果整条消息没有
// text block（纯多模态附件场景），新插一个 text block 在最前面 —— 保持图片
// 等其他 block 不动。
//
// 直接修改入参 msg.Content（指针入参，调用方传 *types.Message）。
func injectPlanReminder(msg *types.Message, reminder string) {
	if reminder == "" || msg == nil {
		return
	}
	for i, block := range msg.Content {
		if block.Type == types.ContentTypeText {
			msg.Content[i].Text = reminder + "\n\n" + block.Text
			return
		}
	}
	// 没有 text block —— 把 reminder 作为新 text block 插到最前面。
	prefix := []types.ContentBlock{{
		Type: types.ContentTypeText,
		Text: reminder,
	}}
	msg.Content = append(prefix, msg.Content...)
}
