// Package definition 是 AgentDefinition 的新规范 import 路径。
// 物理类型仍在 internal/legacy/agent 下；本文件为 PR-0 过渡的 type alias。
// 后续 PR-5 会把实体迁过来，alias 即可删除。
package definition

import (
	legacyagent "harnessclaw-go/internal/legacy/agent"
)

// AgentDefinition 是 agent 的元数据 + 行为契约。
// 详见 internal/legacy/agent/definition.go。
type AgentDefinition = legacyagent.AgentDefinition

// Registry 同义引用。
type Registry = legacyagent.AgentDefinitionRegistry
