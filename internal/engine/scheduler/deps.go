package scheduler

import "go.uber.org/zap"

// Deps 是 scheduler 包对外的依赖注入面。
// 所有字段都是 interface —— 测试时用 fake 一对一替换。
// 注意：不出现 provider/loop/compactor 等 LLM 类型，目标 4「LLM 解耦」由 Runtime 接口承担。
type Deps struct {
	Log *zap.Logger // 可选
	// Runtime/TaskMgr/DiskOutput/Bus 在 sub-package 就绪后逐步补充（task 1.6/1.7/1.9/1.10）
}
