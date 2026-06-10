// 临时 alias bridge —— PR-B 把 AgentDefinition / Registry / SubAgentDef /
// PlannerListing / Tier / CostTier / RenderExpectedOutputs /
// RenderSubAgentContract 物理搬到了 internal/engine/agent/definition/。
//
// 本文件让 legacy/agent 包内剩下的文件（service / store / sqlite_store /
// mention / spawner / broker / team / async 等）以及尚未改 import 的
// 外部 callsite 仍能通过 agent.X 访问这些符号。后续 PR-C/D/E 把剩余
// 文件搬走后，本文件随整个 legacy/agent/ 一起删除。
//
// 注意：BrowserAgent* 不在此 bridge —— browser_agent 包通过 common 间接
// 依赖 legacy/agent，alias 会形成 import cycle，故那两个符号必须在
// callsite 直接 import browser_agent。
package agent

import "harnessclaw-go/internal/engine/agent/definition"

type AgentDefinition = definition.AgentDefinition
type AgentDefinitionRegistry = definition.Registry
type SubAgentDef = definition.SubAgentDef
type PlannerListing = definition.PlannerListing
type Tier = definition.Tier
type CostTier = definition.CostTier

const (
	TierCoordinator = definition.TierCoordinator
	TierSubAgent    = definition.TierSubAgent

	CostCheap     = definition.CostCheap
	CostMedium    = definition.CostMedium
	CostExpensive = definition.CostExpensive
)

var (
	NewAgentDefinitionRegistry = definition.NewRegistry
	RenderExpectedOutputs      = definition.RenderExpectedOutputs
	RenderSubAgentContract     = definition.RenderSubAgentContract
)
