package agent

import (
	"fmt"
	"sort"
	"sync"

	"harnessclaw-go/internal/tool"
)

// Tier classifies an agent's role in the L1 / L2 / L3 hierarchy. The engine
// uses Tier to pick a driver: coordinators run the dispatch-capable
// runSubAgentLoop, sub-agents run the leaf-only runSubAgentDriver.
//
// Default for an empty AgentDefinition.Tier is TierCoordinator — backward
// compatible with the pre-tier registrations.
type Tier string

const (
	// TierCoordinator can dispatch other agents and integrate their results.
	// Specialists (the L2 entry point) and Plan / Explore / general-purpose
	// fall into this tier. The runSubAgentLoop driver serves coordinators.
	TierCoordinator Tier = "coordinator"

	// TierSubAgent is a leaf executor — single responsibility, runs a pure
	// ReAct loop, MUST submit a structured result via SubmitTaskResult, and
	// is forbidden from calling Task / Specialists / Orchestrate (no further
	// dispatch). Workers like writer / researcher / analyst belong here.
	TierSubAgent Tier = "sub_agent"
)

// CostTier hints at how expensive a single run of this agent is. Planners
// use it to balance task complexity against budget — cheap models for
// simple extraction, expensive models for multi-step analysis.
type CostTier string

const (
	CostCheap     CostTier = "cheap"
	CostMedium    CostTier = "medium"
	CostExpensive CostTier = "expensive"
)

// AgentDefinition describes a custom agent type loaded from configuration
// (e.g., .claude/agents/*.md or YAML files). It determines which tools,
// prompt, and model a spawned agent will use.
type AgentDefinition struct {
	// Name is the unique identifier for this agent type (e.g., "researcher", "test-runner").
	Name string `json:"name"`

	// DisplayName is a human-readable name shown in UI surfaces.
	DisplayName string `json:"display_name,omitempty"`

	// Description is a human-readable summary of what this agent does.
	Description string `json:"description"`

	// Profile selects the prompt profile: "full", "explore", or "plan".
	Profile string `json:"profile,omitempty"`

	// AutoTeam enables automatic team mode where sub-agents are spawned.
	AutoTeam bool `json:"auto_team,omitempty"`

	// SubAgents defines predefined sub-agents spawned automatically in team mode.
	SubAgents []SubAgentDef `json:"sub_agents,omitempty"`

	// Tools is an optional tool whitelist separate from AllowedTools.
	// When set, only these tools are available to the agent.
	Tools []string `json:"tools,omitempty"`

	// SystemPrompt is the system prompt for this agent type.
	SystemPrompt string `json:"system_prompt,omitempty"`

	// Model overrides the default LLM model for this agent type.
	Model string `json:"model,omitempty"`

	// AgentType controls tool filtering (sync, async, teammate, coordinator, custom).
	AgentType tool.AgentType `json:"agent_type"`

	// AllowedTools is the whitelist of tools this agent can use.
	// Empty means use the default filtering for the AgentType.
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// DisallowedTools is an additional blacklist applied after AgentType filtering.
	DisallowedTools []string `json:"disallowed_tools,omitempty"`

	// Skills serves two purposes:
	//   1. SkillTool whitelist — when non-empty, the agent can only invoke
	//      skill commands listed here (existing behavior).
	//   2. Capability tags — Registry.FindBySkill matches against this list
	//      so a planner can pick agents by what they can do, not by name.
	// Treat each entry as both an invocation token and a capability label.
	Skills []string `json:"skills,omitempty"`

	// MaxTurns overrides the default max turns for this agent type.
	MaxTurns int `json:"max_turns,omitempty"`

	// Personality describes the agent's character traits for the team table.
	Personality string `json:"personality,omitempty"`

	// Triggers describes typical user phrases that should route to this agent.
	// Used for programmatic routing decisions, not displayed in the prompt.
	Triggers string `json:"triggers,omitempty"`

	// Source indicates where this definition was loaded from.
	Source string `json:"source,omitempty"` // e.g., file path

	// IsBuiltin marks the definition as a system built-in that cannot be deleted.
	IsBuiltin bool `json:"is_builtin,omitempty"`

	// IsTeamMember marks this agent as one of emma's team members (搭档).
	// Team members appear in emma's dynamic team table in the prompt.
	IsTeamMember bool `json:"is_team_member,omitempty"`

	// --- L3 sub-agent metadata (Tier == TierSubAgent only) -----------------
	//
	// These fields describe the sub-agent's contract to the planner: what it
	// accepts, what it produces, what it can't do, and how expensive it is.
	// They are required for TierSubAgent and ignored for TierCoordinator.

	// Tier classifies the agent. Empty defaults to TierCoordinator. Setting
	// TierSubAgent triggers strict registration (OutputSchema required) and
	// routes spawns through runSubAgentDriver instead of runSubAgentLoop.
	Tier Tier `json:"tier,omitempty"`

	// OutputSchema is the JSON Schema the sub-agent's structured result must
	// satisfy. Mandatory for TierSubAgent — Register rejects sub-agents
	// without one. Drives the SubmitTaskResult tool's validation: the L3
	// MUST submit a payload matching this schema before the loop terminates.
	OutputSchema map[string]any `json:"output_schema,omitempty"`

	// InputSchema is the JSON Schema the spawn prompt is expected to carry.
	// Optional: when set, SpawnSync can validate cfg.Inputs before invoking
	// the sub-agent. Empty means "no input contract", same as today.
	InputSchema map[string]any `json:"input_schema,omitempty"`

	// Limitations lists what this sub-agent CANNOT do. Surfaced to planners
	// to prevent misuse (LLMs over-attribute capability without explicit
	// limits). Each entry is one short user-facing sentence.
	Limitations []string `json:"limitations,omitempty"`

	// ExampleTasks lists prototypical prompts this sub-agent handles well.
	// Used as few-shot guidance when a planner generates dispatches.
	ExampleTasks []string `json:"example_tasks,omitempty"`

	// CostTier indicates this sub-agent's run cost relative to peers.
	// Default (empty string) is treated as CostMedium.
	CostTier CostTier `json:"cost_tier,omitempty"`

	// TypicalLatencyMs is a rough guess at end-to-end runtime for the
	// planner's scheduling heuristics. Zero means "unknown".
	TypicalLatencyMs int `json:"typical_latency_ms,omitempty"`

	// Temperature overrides the LLM sampling temperature for this agent.
	// Pointer so "not set" is distinguishable from 0.0 (deterministic) —
	// nil means "use whatever the engine default is", value means "use
	// exactly this". writers benefit from 0.6-0.8 (more creative
	// phrasings); coders / extractors want 0.1-0.3 (deterministic, less
	// invention). Without per-agent temperature, every sub-agent inherits
	// the global default, which is wrong for at least half the workers.
	Temperature *float64 `json:"temperature,omitempty"`
}

// SubAgentDef describes a predefined sub-agent spawned automatically.
type SubAgentDef struct {
	Name      string         `json:"name"`
	Role      string         `json:"role"`
	AgentType tool.AgentType `json:"agent_type"`
	Profile   string         `json:"profile"`
}

// Validate checks the AgentDefinition for self-consistency. Strict rules
// for TierSubAgent are enforced here so any caller — Register, hot-reload,
// import from disk — gets the same answer.
//
// Rules:
//   - Name is required.
//   - TierSubAgent requires OutputSchema (the structured result contract).
//   - TierSubAgent forbids dispatch tools in AllowedTools (no Task, no
//     Specialists, no Orchestrate). The runtime driver also strips them
//     defensively, but rejecting at registration catches misconfig early.
func (d *AgentDefinition) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("agent definition: Name is required")
	}
	if d.Tier == TierSubAgent {
		if len(d.OutputSchema) == 0 {
			return fmt.Errorf("agent %q: TierSubAgent requires OutputSchema", d.Name)
		}
		for _, t := range d.AllowedTools {
			if t == "Task" || t == "Specialists" || t == "Orchestrate" {
				return fmt.Errorf("agent %q: TierSubAgent cannot dispatch (tool %q forbidden)", d.Name, t)
			}
		}
	}
	return nil
}

// EffectiveTier returns Tier with the empty default resolved to TierCoordinator.
// Use this anywhere you branch on tier — never compare d.Tier to "" directly.
func (d *AgentDefinition) EffectiveTier() Tier {
	if d == nil || d.Tier == "" {
		return TierCoordinator
	}
	return d.Tier
}

// floatPtr returns a *float64 — used to set optional Temperature etc.
// without polluting the call site.
func floatPtr(v float64) *float64 { return &v }

// MaybeAugmentForSubAgent returns AllowedTools augmented with the terminal
// tools every L3 must reach (SubmitTaskResult + EscalateToPlanner). The
// augment is a no-op for nil definitions or non-SubAgent tiers, and idempotent
// when the names are already present.
//
// Why we don't bake this into the LLM prompt instead: the schema-level
// availability is what makes a tool callable; injecting "you may submit"
// into the prompt while the tool is filtered out of the pool produces a
// silent-fail loop that nudges to no avail.
func (d *AgentDefinition) MaybeAugmentForSubAgent() []string {
	if d == nil || d.EffectiveTier() != TierSubAgent || len(d.AllowedTools) == 0 {
		// Nil def, coordinator tier, or no whitelist → return as-is.
		// (Empty whitelist means "use AgentType default", which is a
		// different code path that we don't want to disturb.)
		if d == nil {
			return nil
		}
		return d.AllowedTools
	}
	required := []string{"SubmitTaskResult", "EscalateToPlanner"}
	out := make([]string, 0, len(d.AllowedTools)+len(required))
	out = append(out, d.AllowedTools...)
	seen := make(map[string]bool, len(out))
	for _, t := range out {
		seen[t] = true
	}
	for _, t := range required {
		if !seen[t] {
			out = append(out, t)
		}
	}
	return out
}

// AgentDefinitionRegistry holds all loaded agent definitions.
type AgentDefinitionRegistry struct {
	mu   sync.RWMutex
	defs map[string]*AgentDefinition
}

// NewAgentDefinitionRegistry creates a new registry.
func NewAgentDefinitionRegistry() *AgentDefinitionRegistry {
	return &AgentDefinitionRegistry{
		defs: make(map[string]*AgentDefinition),
	}
}

// Register adds an agent definition after validation. Returns an error when
// the definition fails Validate (e.g., TierSubAgent with no OutputSchema).
// Overwrites if the name already exists — last write wins, same as before.
//
// Callers that want to register a known-good definition without checking the
// error can use MustRegister, which panics on validation failure.
func (r *AgentDefinitionRegistry) Register(def *AgentDefinition) error {
	if def == nil {
		return fmt.Errorf("agent definition: nil")
	}
	if err := def.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defs[def.Name] = def
	return nil
}

// MustRegister registers a definition and panics on validation failure.
// Use this for built-in registrations where a validation error is a programmer
// bug, not runtime input.
func (r *AgentDefinitionRegistry) MustRegister(def *AgentDefinition) {
	if err := r.Register(def); err != nil {
		panic(fmt.Sprintf("agent.MustRegister: %v", err))
	}
}

// Get returns a definition by name, or nil.
func (r *AgentDefinitionRegistry) Get(name string) *AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defs[name]
}

// All returns all registered definitions.
func (r *AgentDefinitionRegistry) All() []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*AgentDefinition, 0, len(r.defs))
	for _, d := range r.defs {
		result = append(result, d)
	}
	return result
}

// Unregister removes an agent definition by name. Returns true if it existed.
func (r *AgentDefinitionRegistry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.defs[name]
	delete(r.defs, name)
	return ok
}

// TeamMembers returns all definitions marked as team members, sorted by name.
func (r *AgentDefinitionRegistry) TeamMembers() []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var members []*AgentDefinition
	for _, d := range r.defs {
		if d.IsTeamMember {
			members = append(members, d)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	return members
}

// Names returns all registered agent definition names.
func (r *AgentDefinitionRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.defs))
	for name := range r.defs {
		names = append(names, name)
	}
	return names
}

// FindBySkill returns every registered definition whose Skills list contains
// skill. The match is exact, case-sensitive — capability tags are an
// internal vocabulary, not free-form text.
//
// Used by L2 planners to enumerate "who can do X" without knowing names.
// Result order is by definition name (stable, for deterministic prompts).
func (r *AgentDefinitionRegistry) FindBySkill(skill string) []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var matches []*AgentDefinition
	for _, d := range r.defs {
		for _, s := range d.Skills {
			if s == skill {
				matches = append(matches, d)
				break
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })
	return matches
}

// PlannerListing is the structured snapshot a planner consumes when picking
// which sub-agent to dispatch. Strips fields the planner doesn't need
// (Source, IsBuiltin, etc.) and surfaces the contract in one place.
type PlannerListing struct {
	Name             string         `json:"name"`
	DisplayName      string         `json:"display_name,omitempty"`
	Description      string         `json:"description"`
	Skills           []string       `json:"skills,omitempty"`
	Limitations      []string       `json:"limitations,omitempty"`
	ExampleTasks     []string       `json:"example_tasks,omitempty"`
	CostTier         CostTier       `json:"cost_tier,omitempty"`
	TypicalLatencyMs int            `json:"typical_latency_ms,omitempty"`
	OutputSchema     map[string]any `json:"output_schema,omitempty"`
}

// ListForPlanner returns a planner-shaped view of every TierSubAgent in the
// registry, sorted by name. Coordinators are excluded — a planner picks
// among leaves, not among other planners.
func (r *AgentDefinitionRegistry) ListForPlanner() []PlannerListing {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []PlannerListing
	for _, d := range r.defs {
		if d.EffectiveTier() != TierSubAgent {
			continue
		}
		out = append(out, PlannerListing{
			Name:             d.Name,
			DisplayName:      d.DisplayName,
			Description:      d.Description,
			Skills:           d.Skills,
			Limitations:      d.Limitations,
			ExampleTasks:     d.ExampleTasks,
			CostTier:         d.CostTier,
			TypicalLatencyMs: d.TypicalLatencyMs,
			OutputSchema:     d.OutputSchema,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RegisterBuiltins adds the standard built-in agent definitions. All
// built-ins use MustRegister — a validation failure here is a programmer
// bug in this file, not runtime input.
//
// Tier policy:
//   - Built-in workers are migrating to TierSubAgent one at a time. As
//     each gets a hand-crafted OutputSchema / Limitations / Skills /
//     CostTier, it flips. Workers still on TierCoordinator (the default)
//     run the legacy runSubAgentLoop until promoted.
//   - System agents (specialists / general-purpose / Plan / Explore) are
//     coordinators by design — they may dispatch and integrate.
func (r *AgentDefinitionRegistry) RegisterBuiltins() {
	// --- 核心团队成员（对应 emma 团队表） ---

	// writer — 第一个升级到 L3 的内置 worker。专写"成文"产物（邮件、文案、
	// 报告、翻译、润色）。不做事实核查（researcher 的事）、不出表/图（analyst 的
	// 事）、不写代码（developer 的事）。OutputSchema 描述他向 planner
	// 承诺的契约：一份命名好的文本产物 + 极简的格式/字数/语气元数据，content
	// 始终走 ArtifactWrite，不在 schema 里塞正文。
	r.MustRegister(&AgentDefinition{
		Name:        "writer",
		DisplayName: "小林",
		Description: "文案写作、邮件、报告、翻译、润色",
		AgentType:   tool.AgentTypeSync,
		Profile:     "worker",
		IsTeamMember: true,
		SystemPrompt: `你是专业写作执行者。
你的专长：商务邮件、报告、翻译、润色改稿、摘要提炼。

工作流程：
1. 读取任务要求，明确受众、格式、字数上限、语气
2. 如有参考文档，先用 ArtifactRead 读取内容
3. 如需核实术语、人名、日期等事实，用 TavilySearch 查证（不臆造）
4. 正文用 ArtifactWrite 持久化——不要把大段文字写入 SubmitTaskResult
5. 调用 SubmitTaskResult 提交元数据：artifact_role / format / word_count / tone

约束：
- 素材有未经验证的事实时直接转述，不擅自补全或核查
- 不写代码，不生成图表，不做数据分析
- 单次任务超过 3000 字时调 EscalateToPlanner 拆分，不强行分轮完成
- tone 没有明确指定时，根据受众自行判断并在 SubmitTaskResult 里显式声明`,

		Tier: TierSubAgent,
		AllowedTools: []string{
			"ArtifactRead",
			"ArtifactWrite",
			"SubmitTaskResult",
			"EscalateToPlanner",
			"TavilySearch",
		},
		Skills: []string{
			"writing",
			"email_drafting",
			"translation",
			"summarization",
			"polishing",
		},
		InputSchema: map[string]any{
			"type":        "object",
			"description": "Writer 的输入契约。",
			"required":    []string{"task"},
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "写作任务描述：目的、受众、内容要点。",
				},
				"format": map[string]any{
					"type": "string",
					"enum": []string{"markdown", "plaintext", "html"},
				},
				"tone": map[string]any{
					"type": "string",
					"enum": []string{"formal", "professional", "friendly", "casual", "neutral"},
				},
				"word_limit": map[string]any{
					"type":    "integer",
					"minimum": 1,
				},
				"language": map[string]any{
					"type":        "string",
					"description": "ISO 639-1 语种，如 zh / en。",
				},
				"reference_artifact_id": map[string]any{
					"type":        "string",
					"description": "参考 artifact ID，如润色/翻译已有文档时使用。",
				},
			},
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"artifact_role", "format", "word_count", "tone"},
			"properties": map[string]any{
				"artifact_role": map[string]any{
					"type": "string",
					"enum": []string{"draft", "final", "revision"},
				},
				"format": map[string]any{
					"type": "string",
					"enum": []string{"markdown", "plaintext", "html"},
				},
				"word_count": map[string]any{
					"type":    "integer",
					"minimum": 1,
				},
				"tone": map[string]any{
					"type": "string",
					"enum": []string{"formal", "professional", "friendly", "casual", "neutral"},
				},
				"language": map[string]any{
					"type": "string",
				},
			},
		},
		Limitations: []string{
			"不做事实核查——给的素材里有未经验证的事实，直接转述，不擅自补全",
			"不生成长篇结构化报告（>3000 字）——超长任务请拆给 multi-step planner",
			"不写代码——交给 developer",
			"不出表格 / 图表 / 数据可视化——交给 analyst",
			"不处理需要专业领域知识的法律、医疗、财务定性结论",
		},
		ExampleTasks: []string{
			"写一封正式商务邮件，向客户解释项目延期，约 200 字",
			"把这段英文产品文档翻译成中文，保留专业术语",
			"润色这篇周报，提升专业感但保留作者原本的语气节奏",
			"给一份长会议纪要写 150 字以内的摘要",
			"把这段口语化笔记改写成对外邮件草稿",
		},
		CostTier:         CostCheap,
		TypicalLatencyMs: 3000,
		Temperature:      floatPtr(0.7),
	})
	r.MustRegister(&AgentDefinition{
		Name:        "researcher",
		DisplayName: "小瑞",
		Description: "信息搜索、行业调研、事实核查、资料整理、摘要",
		AgentType:   tool.AgentTypeSync,
		Profile:     "explore",
		IsTeamMember: true,
		SystemPrompt: `你是信息调研执行者。
你的专长：网页搜索、事实核查、资料整理、摘要生成。

工具使用策略：
- TavilySearch：主力搜索工具，每个问题至少从 2 个不同角度各查一次
- ArtifactRead：读取调用方传入的参考文档
- ArtifactWrite：持久化调研报告（含来源 URL、核心摘要、可信度说明）

工作流程：
1. 拆解调研问题，列出 2-3 个搜索角度
2. 逐角度用 TavilySearch 搜索，优先权威来源（官网 > 学术 > 主流媒体 > 博客）
3. 交叉比对结果，标注每条信息的来源和时效
4. 用 ArtifactWrite 持久化报告
5. 调用 SubmitTaskResult：artifact_role / source_count（实际引用源数）/ confidence

confidence 评级：
- high：≥2 个权威来源交叉印证
- medium：单一来源或间接引用
- low：推测或无法验证

约束：
- 只整理转述已有信息，不做原创推断或观点输出
- 无法找到可靠来源时如实汇报 confidence=low，不杜撰
- 实时数据（股价/天气）结果注明抓取时间，提示用户自行确认`,

		Tier: TierSubAgent,
		AllowedTools: []string{
			"ArtifactRead",
			"ArtifactWrite",
			"SubmitTaskResult",
			"EscalateToPlanner",
			"TavilySearch",
		},
		Skills: []string{
			"research",
			"web_search",
			"fact_checking",
			"summarization",
		},
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "调研问题或待核实的声明。",
				},
				"depth": map[string]any{
					"type": "string",
					"enum": []string{"quick", "standard", "thorough"},
				},
				"output_format": map[string]any{
					"type": "string",
					"enum": []string{"research_report", "fact_check", "summary"},
				},
			},
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"artifact_role", "source_count", "confidence"},
			"properties": map[string]any{
				"artifact_role": map[string]any{
					"type": "string",
					"enum": []string{"research_report", "fact_check", "summary"},
				},
				"source_count": map[string]any{
					"type":    "integer",
					"minimum": 1,
				},
				"confidence": map[string]any{
					"type": "string",
					"enum": []string{"high", "medium", "low"},
				},
			},
		},
		Limitations: []string{
			"不做原创分析或观点推断——只整理、转述、归纳已有信息",
			"不写代码——交给 developer",
			"不出图表——交给 analyst",
			"不保证实时数据（股价、天气、实时库存）——搜索结果有时效性",
			"不访问需要登录的内容——只处理公开可索引页面",
		},
		ExampleTasks: []string{
			"调研大模型推理优化的最新进展，整理成带来源的要点列表",
			"查找电动车续航前三名的最新数据，标注来源和更新时间",
			"核实'OpenAI 在 2024 年收购了 Rockset'这一说法是否属实",
			"搜集 vLLM / SGLang 的 GitHub star 数和最近 release 时间",
			"把这篇长文提炼成 200 字以内的中文摘要，保留核心论点",
		},
		CostTier:         CostMedium,
		TypicalLatencyMs: 8000,
		Temperature:      floatPtr(0.3),
	})
	r.MustRegister(&AgentDefinition{
		Name:        "analyst",
		DisplayName: "小数",
		Description: "数据分析、表格处理、图表描述、趋势解读",
		AgentType:   tool.AgentTypeSync,
		Profile:     "worker",
		IsTeamMember: true,
		SystemPrompt: `你是数据分析执行者。
你的专长：数值计算、趋势解读、对比分析、表格生成。

工具使用策略：
- ArtifactRead：读取调用方提供的数据文件（必须先读再分析）
- TavilySearch：补充行业基准数据或术语解释（不用于原始数据采集）
- ArtifactWrite：持久化分析报告（markdown 表格 / csv / json）

工作流程：
1. 如果数据在 artifact 里，用 ArtifactRead 读取
2. 明确分析目标：trend / comparison / distribution / correlation / summary
3. 执行计算，标注方法（如"按简单算术平均"、"同比增长率 = (本期-同期)/同期"）
4. 生成结论，用 ArtifactWrite 持久化
5. 调用 SubmitTaskResult：artifact_role / analysis_type / data_format

约束：
- 数据必须由调用方提供，不自行采集原始数据
- 只呈现数据模式，不输出定性结论（不给投资/法律/医疗建议）
- 不生成可渲染图表文件——用 markdown 表格或文字描述代替
- 计算结果有异常值时明确标注，不静默忽略`,

		Tier: TierSubAgent,
		AllowedTools: []string{
			"ArtifactRead",
			"ArtifactWrite",
			"SubmitTaskResult",
			"EscalateToPlanner",
			"TavilySearch",
		},
		Skills: []string{
			"data_analysis",
			"financial_analysis",
			"trend_analysis",
			"charting",
		},
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"task"},
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "分析任务描述。",
				},
				"data_artifact_id": map[string]any{
					"type":        "string",
					"description": "包含待分析数据的 artifact ID。",
				},
				"analysis_type": map[string]any{
					"type": "string",
					"enum": []string{"trend", "comparison", "distribution", "correlation", "summary"},
				},
				"output_format": map[string]any{
					"type": "string",
					"enum": []string{"markdown", "csv", "json"},
				},
			},
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"artifact_role", "analysis_type", "data_format"},
			"properties": map[string]any{
				"artifact_role": map[string]any{
					"type": "string",
					"enum": []string{"analysis_report", "data_table", "chart_description"},
				},
				"analysis_type": map[string]any{
					"type": "string",
					"enum": []string{"trend", "comparison", "distribution", "correlation", "summary"},
				},
				"data_format": map[string]any{
					"type": "string",
					"enum": []string{"markdown", "csv", "json", "plaintext"},
				},
			},
		},
		Limitations: []string{
			"不负责原始数据采集——数据必须由 researcher 或用户提供",
			"不写代码——交给 developer",
			"不生成真实可渲染的图表文件——只输出图表描述或文本形式的数据",
			"不做定性结论（如法律/医疗/财务建议）——只呈现数据模式",
		},
		ExampleTasks: []string{
			"分析这份 Q4 销售数据，找出增长最快的三个品类",
			"把这两份月报做同比对比，生成 markdown 表格",
			"计算这组数字的平均值、中位数、标准差，并解读分布特征",
			"基于这份用户行为日志，分析留存率趋势",
		},
		CostTier:         CostMedium,
		TypicalLatencyMs: 6000,
		Temperature:      floatPtr(0.2),
	})
	r.MustRegister(&AgentDefinition{
		Name:        "developer",
		DisplayName: "小程",
		Description: "代码编写、调试、技术文档、工具脚本开发",
		AgentType:   tool.AgentTypeSync,
		Profile:     "worker",
		IsTeamMember: true,
		SystemPrompt: `你是代码开发执行者。
你的专长：代码编写、调试、单元测试、技术文档。

工具使用策略：
- ArtifactRead：读取已有代码或需求文档（调试/重写任务必须先读）
- ArtifactWrite：持久化代码文件（每个逻辑单元一个 artifact）
- Bash：运行代码做验证——仅限执行测试/编译，不做系统操作或外部 API 调用

工作流程：
1. 如有已有代码，先用 ArtifactRead 读取，理解上下文
2. 明确任务：语言、功能要求、接口约定、是否需要运行验证
3. 编写代码
4. run_and_verify=true 或有测试要求时，用 Bash 运行并修复到通过
5. 用 ArtifactWrite 持久化，调用 SubmitTaskResult：artifact_role / language / tested

tested 字段规则：
- true：实际用 Bash 运行过或有测试用例覆盖并通过
- false：纯静态检查或无法运行的场景（需在 summary 说明原因）

约束：
- Bash 只执行代码验证，不做文件删除、网络请求、系统配置变更
- 单任务代码量超过 500 行时，调 EscalateToPlanner 请求拆分
- 不做 UI/UX 设计，不做数据分析，不写非技术性文档`,

		Tier: TierSubAgent,
		// Bash is included because developer must be able to run and verify code.
		// WithoutDangerousUnless keeps Bash when it appears in AllowedTools.
		AllowedTools: []string{
			"ArtifactRead",
			"ArtifactWrite",
			"SubmitTaskResult",
			"EscalateToPlanner",
			"Bash",
		},
		Skills: []string{
			"code_generation",
			"debugging",
			"code_review",
			"technical_writing",
		},
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"task"},
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "开发任务描述。",
				},
				"language": map[string]any{
					"type":        "string",
					"description": "目标编程语言，如 go / python / typescript / shell。",
				},
				"source_artifact_id": map[string]any{
					"type":        "string",
					"description": "已有代码的 artifact ID（调试/重写时使用）。",
				},
				"run_and_verify": map[string]any{
					"type":        "boolean",
					"description": "是否需要运行代码验证正确性。",
				},
			},
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"artifact_role", "language", "tested"},
			"properties": map[string]any{
				"artifact_role": map[string]any{
					"type": "string",
					"enum": []string{"implementation", "utility_script", "test_suite", "technical_doc"},
				},
				"language": map[string]any{
					"type": "string",
				},
				"tested": map[string]any{
					"type": "boolean",
				},
			},
		},
		Limitations: []string{
			"不做 UI/UX 设计——只写逻辑层代码",
			"不做非技术性写作——交给 writer",
			"不做数据分析——交给 analyst",
			"代码超过 500 行的大型重构请拆成多个子任务",
			"不能访问外部 API 或需要认证的服务",
		},
		ExampleTasks: []string{
			"写一个 Go HTTP 中间件，实现请求限速（100 req/s per IP）",
			"把这段 Python 脚本重写成类型安全的 TypeScript",
			"调试这个 SQL 查询，找出为什么它的性能比预期慢 10 倍",
			"给这个 Go 包写单元测试，覆盖主要边界条件",
			"写一个 shell 脚本，每天备份指定目录并保留最近 7 天",
		},
		CostTier:         CostMedium,
		TypicalLatencyMs: 10000,
		Temperature:      floatPtr(0.15),
	})
	r.MustRegister(&AgentDefinition{
		Name:        "travel_planner",
		DisplayName: "小旅",
		Description: "出行规划、行程安排、目的地调研、路线设计",
		AgentType:   tool.AgentTypeSync,
		Profile:     "worker",
		IsTeamMember: true,
		SystemPrompt: `你是出行规划执行者。
你的专长：行程设计、目的地调研、路线规划、景点筛选。

工具使用策略：
- TavilySearch：查询景点信息、开放时间、交通方式、住宿区域推荐
- ArtifactRead：读取调用方提供的参考资料（如已有行程草稿）
- ArtifactWrite：持久化完整行程文档

工作流程：
1. 明确目的地、天数、预算档位、兴趣偏好（文化/自然/美食/购物）
2. 用 TavilySearch 调研目的地核心景点，了解交通方式和住宿区域
3. 按天编排行程，注意：
   - 相邻景点地理位置合理，减少折返
   - 每天留出餐饮和休息时间（上午重点景点，下午次要景点）
   - 每日行程不超过 3-4 个主要地点
4. 用 ArtifactWrite 持久化（markdown 格式，按天分节）
5. 调用 SubmitTaskResult：artifact_role / destination / duration_days

约束：
- 不保证景点/酒店实时可用性，所有推荐注明"建议出发前确认"
- 不做餐饮或购物具体推荐——提示"可咨询推荐服务获取详细建议"
- 不做实际预订，只生成计划文档
- 跨国行程注明签证要求提示和时区差异`,

		Tier: TierSubAgent,
		AllowedTools: []string{
			"ArtifactRead",
			"ArtifactWrite",
			"SubmitTaskResult",
			"EscalateToPlanner",
			"TavilySearch",
		},
		Skills: []string{
			"travel_planning",
			"itinerary_design",
			"destination_research",
		},
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"destination"},
			"properties": map[string]any{
				"destination": map[string]any{
					"type":        "string",
					"description": "目的地（城市、国家或景点名称）。",
				},
				"duration_days": map[string]any{
					"type":    "integer",
					"minimum": 1,
				},
				"interests": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"budget_level": map[string]any{
					"type": "string",
					"enum": []string{"budget", "mid_range", "luxury"},
				},
				"departure_city": map[string]any{
					"type": "string",
				},
			},
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"artifact_role", "destination", "duration_days"},
			"properties": map[string]any{
				"artifact_role": map[string]any{
					"type": "string",
					"enum": []string{"travel_plan", "itinerary", "destination_guide"},
				},
				"destination": map[string]any{
					"type": "string",
				},
				"duration_days": map[string]any{
					"type":    "integer",
					"minimum": 1,
				},
			},
		},
		Limitations: []string{
			"不做实际预订或购票——只生成计划和建议",
			"不保证景点/酒店实时可用性——建议用户自行确认",
			"不处理需要账号登录才能查询的内容",
			"不做餐饮或购物推荐——交给 recommender",
		},
		ExampleTasks: []string{
			"规划一个北京 2 天 1 夜的周末行程，包含故宫和后海",
			"设计一条适合情侣的云南 5 日游路线，预算中等",
			"推荐东京值得去的博物馆和历史景点，整理成行程安排",
			"规划从上海出发到杭州的一日游，含交通和时间安排",
		},
		CostTier:         CostCheap,
		TypicalLatencyMs: 6000,
		Temperature:      floatPtr(0.5),
	})
	r.MustRegister(&AgentDefinition{
		Name:        "recommender",
		DisplayName: "小荐",
		Description: "餐饮推荐、购物比价、娱乐推荐、产品对比",
		AgentType:   tool.AgentTypeSync,
		Profile:     "worker",
		IsTeamMember: true,
		SystemPrompt: `你是推荐执行者。
你的专长：餐饮推荐、产品对比、购物选购、娱乐场所推荐。

工具使用策略：
- TavilySearch：搜索候选项（优先点评网站、权威测评、品牌官网）
- ArtifactWrite：持久化推荐清单（markdown 表格，含关键对比维度）

工作流程：
1. 明确 category（dining/shopping/entertainment/product）和需求细节
2. 用 TavilySearch 搜索符合条件的候选项，至少获取 5 个候选
3. 按关键维度筛选并排序：
   - dining：位置 / 人均价格 / 特色菜 / 评分
   - product/shopping：价格 / 核心参数 / 优缺点 / 适用场景
   - entertainment：位置 / 价格 / 适合人群 / 预约要求
4. 输出 3-10 个推荐项，用 ArtifactWrite 持久化（markdown 表格）
5. 调用 SubmitTaskResult：artifact_role / category / item_count

约束：
- 餐饮/娱乐类没有明确 location 时，提示调用方补充后再推荐
- 所有推荐注明数据来源和参考时间，不保证实时可用性
- 不做出行路线规划——仅给推荐清单，路线安排提示咨询行程规划
- 产品对比要基于已有公开数据，不自行生成测评结论`,

		Tier: TierSubAgent,
		AllowedTools: []string{
			"ArtifactRead",
			"ArtifactWrite",
			"SubmitTaskResult",
			"EscalateToPlanner",
			"TavilySearch",
		},
		Skills: []string{
			"restaurant_recommendation",
			"shopping_comparison",
			"entertainment_recommendation",
			"product_review",
		},
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"category", "query"},
			"properties": map[string]any{
				"category": map[string]any{
					"type": "string",
					"enum": []string{"dining", "shopping", "entertainment", "product"},
				},
				"query": map[string]any{
					"type":        "string",
					"description": "推荐需求描述。",
				},
				"location": map[string]any{
					"type":        "string",
					"description": "地理位置（城市/区域），餐饮/娱乐类必须提供。",
				},
				"budget_range": map[string]any{
					"type":        "string",
					"description": "预算范围描述，如'人均 200-400'或'500-1500 元'。",
				},
				"count": map[string]any{
					"type":    "integer",
					"minimum": 1,
				},
			},
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"artifact_role", "category", "item_count"},
			"properties": map[string]any{
				"artifact_role": map[string]any{
					"type": "string",
					"enum": []string{"recommendation_list", "comparison_table"},
				},
				"category": map[string]any{
					"type": "string",
					"enum": []string{"dining", "shopping", "entertainment", "product"},
				},
				"item_count": map[string]any{
					"type":    "integer",
					"minimum": 1,
				},
			},
		},
		Limitations: []string{
			"不做出行路线规划——交给 travel_planner",
			"不做实际预订或支付——只提供推荐清单",
			"不保证实时可用性（库存、营业状态）——建议用户自行确认",
			"不处理需要账号登录才能查询的内容",
		},
		ExampleTasks: []string{
			"推荐上海浦东适合商务宴请的餐厅，人均 300-500，3 个选项",
			"比较 iPhone 16 Pro 和三星 S25 Ultra 的性价比",
			"推荐 5 个适合居家办公的降噪耳机，价格在 500-1500 元",
			"找北京朝阳区适合周末带小孩去的亲子游乐场所",
		},
		CostTier:         CostCheap,
		TypicalLatencyMs: 5000,
		Temperature:      floatPtr(0.5),
	})
	r.MustRegister(&AgentDefinition{
		Name:        "scheduler",
		DisplayName: "小时",
		Description: "日程管理、会议安排、时间规划、提醒设置",
		AgentType:   tool.AgentTypeSync,
		Profile:     "worker",
		IsTeamMember: true,
		SystemPrompt: `你是日程规划执行者。
你的专长：时间块排布、会议时间协调、跨时区安排。

工具使用策略：
- TavilySearch：查询时区换算、节假日信息（跨时区/跨地区任务时使用）
- ArtifactWrite：持久化日程方案文档
- ArtifactRead：读取调用方提供的任务清单或现有日程

工作流程：
1. 理解任务类型：新建日程 / 会议安排 / 时间块规划
2. 整理约束条件：空闲时间段、优先级、预估时长、时区（如有）
3. 生成排期方案，遵循：
   - 深度工作放精力高峰期（通常上午，避免会议打断）
   - 相邻会议间留 10-15 分钟缓冲
   - 跨时区会议用所有参与方时区对照标注
   - 高优先级任务优先排，低优先级填空档
4. 用 ArtifactWrite 持久化（markdown 表格或列表格式）
5. 调用 SubmitTaskResult：artifact_role / slot_count / format

slot_count 计算规则：实际安排的独立时间段数量（一个会议 = 1，一个工作块 = 1）

约束：
- 没有用户真实日历访问权限——只能基于任务描述的信息排期
- 不能创建日历事件或发送邀请，只输出文本方案
- 不写会议纪要、会议内容——只做时间安排，内容交给 writer`,

		Tier: TierSubAgent,
		AllowedTools: []string{
			"ArtifactRead",
			"ArtifactWrite",
			"SubmitTaskResult",
			"EscalateToPlanner",
			"TavilySearch",
		},
		Skills: []string{
			"scheduling",
			"calendar_planning",
			"time_management",
			"meeting_coordination",
		},
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"task"},
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "日程安排任务描述。",
				},
				"available_slots": map[string]any{
					"type":        "string",
					"description": "用户的空闲时间描述。",
				},
				"constraints": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"output_format": map[string]any{
					"type": "string",
					"enum": []string{"markdown", "plaintext"},
				},
			},
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"artifact_role", "slot_count", "format"},
			"properties": map[string]any{
				"artifact_role": map[string]any{
					"type": "string",
					"enum": []string{"schedule", "meeting_plan", "time_block_proposal"},
				},
				"slot_count": map[string]any{
					"type":    "integer",
					"minimum": 1,
				},
				"format": map[string]any{
					"type": "string",
					"enum": []string{"markdown", "plaintext"},
				},
			},
		},
		Limitations: []string{
			"不能实际创建或修改日历——只生成文本形式的日程方案",
			"不能发送会议邀请或通知",
			"没有用户真实日历访问权限——只能基于用户描述的空闲时间排期",
			"不做内容型任务（如写会议纪要）——交给 writer",
		},
		ExampleTasks: []string{
			"帮我排下周的工作计划，包含 3 个会议和 2 个深度工作块",
			"把这 5 个会议请求安排进周三，留出午饭和缓冲时间",
			"给一个跨时区（北京/纽约/伦敦）的三方会议找最优时间窗口",
			"把这份任务清单按优先级和预估时长做成本周日程",
		},
		CostTier:         CostCheap,
		TypicalLatencyMs: 4000,
		Temperature:      floatPtr(0.3),
	})

	// --- 系统级 agent（不在用户可见的搭档表中） ---
	r.MustRegister(&AgentDefinition{
		Name:        "specialists",
		DisplayName: "Specialists",
		Description: "L2 调度统筹者：拆解任务、派 L3 sub-agent、整合产出、检查质量",
		AgentType:   tool.AgentTypeSync,
		Profile:     "specialists",
		// Specialists needs an explicit tool whitelist so it can use the
		// Task tool to dispatch L3 sub-agents. The tool filter pipeline
		// in subagent.go treats AllowedTools as authoritative — it
		// bypasses the AgentType blacklist (which would otherwise block
		// Task for sync sub-agents).
		//
		// ArtifactWrite/ArtifactRead are listed explicitly because the
		// whitelist semantics drop EVERYTHING not named here — including
		// tools that are on by default for unrestricted L3 workers. Without
		// these two, L2 cannot persist its integrated output or read back
		// what its L3 children produced, breaking the doc §6.A loop.
		AllowedTools: []string{"Task", "WebSearch", "TavilySearch", "ArtifactWrite", "ArtifactRead"},
	})
	r.MustRegister(&AgentDefinition{
		Name:        "general-purpose",
		DisplayName: "通用执行者",
		Description: "通用 agent，处理不属于特定搭档领域的复杂多步骤任务",
		AgentType:   tool.AgentTypeSync,
		Profile:     "worker",
	})
	r.MustRegister(&AgentDefinition{
		Name:        "Explore",
		DisplayName: "探索者",
		Description: "快速只读探索 agent，搜索代码库、定位文件",
		AgentType:   tool.AgentTypeSync,
		Profile:     "explore",
	})
	r.MustRegister(&AgentDefinition{
		Name:        "Plan",
		DisplayName: "规划者",
		Description: "方案设计 agent，需求分析、架构设计、方案对比",
		AgentType:   tool.AgentTypeSync,
		Profile:     "plan",
	})
	// Coordinator definition removed — orchestration logic moved to application code (Phase 2).
}
