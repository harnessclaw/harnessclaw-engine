package builtin

import (
	"fmt"

	"harnessclaw-go/internal/engine/agent/definition"
)

// RegisterAll registers all unconditional builtin AgentDefinitions into
// reg. 取代了原来的 (*definition.Registry).RegisterBuiltins() 方法 ——
// 把"内建 agent 数据"和"注册逻辑"都收敛到 builtin 包，符合 doc.go
// 描述的「one-file change here, no engine code edits required」意图。
//
// 解决了原来的 import cycle 困局：
//   - 之前 RegisterBuiltins 在 definition 包内，definition 不能 import
//     builtin（cycle），所以 agent 字面量被迫内联在 definition.go 里 ——
//     跟原设计意图脱节。
//   - 现在依赖单向：builtin → definition。注册逻辑跟数据同居，调用方
//     （main.go / 测试）显式 import builtin 即可。
//
// browser-agent 不在这里 ——
// 它走 cmd/server/main.go 的条件注册（cfg.Tools.BrowserAgent.Enabled
// 才装），跟其他无条件 builtin 性质不同。
func RegisterAll(reg *definition.Registry) error {
	defs := []*definition.AgentDefinition{
		&PlanDesign,
		&Freelancer,
		&ContentCreator,
	}
	for _, def := range defs {
		if err := reg.Register(def); err != nil {
			return fmt.Errorf("register builtin %q: %w", def.Name, err)
		}
	}
	return nil
}
