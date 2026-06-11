package middlewares

import "harnessclaw-go/internal/engine/scheduler"

// DefaultChain 返回 scheduler 内置 middleware 的标准执行顺序：
//
//	Identity → AgentContext → TaskRegister → Analytics
//
// 调用方（emma wireScheduler）把它传给 scheduler.NewDispatcher。
// 写死顺序是 scheduler 的内部不变量 —— 装配者不需要、也不应该改顺序。
func DefaultChain(deps scheduler.Deps) []scheduler.Middleware {
	return []scheduler.Middleware{
		Identity{},
		AgentContext{},
		TaskRegister{Mgr: deps.TaskMgr},
		Analytics{Bus: deps.Bus},
	}
}
