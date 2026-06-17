package definition

// 本文件包含 Registry —— Agent definition 的内存容器与 CRUD 机制层。
//
// 跟同包的 definition.go 分工：
//   - definition.go：AgentDefinition / Tier / CostTier / SubAgentDef 等
//     "一个 agent 长什么样"的类型，以及 Validate / EffectiveTier 等
//     类型自带的方法
//   - registry.go：Registry 容器 + Register / Get / All / TeamMembers /
//     Unregister / FindBySkill / ListForPlanner 等增删改查方法
//
// 注：内建 agent 数据 + 注册逻辑不在 definition 包，已搬到
// internal/engine/agent/builtin 包，由调用方显式 builtin.RegisterAll(reg) 装载。

import (
	"fmt"
	"sort"
	"sync"
)

// Registry holds all loaded agent definitions.
type Registry struct {
	mu   sync.RWMutex
	defs map[string]*AgentDefinition
}

// NewRegistry creates a new registry.
func NewRegistry() *Registry {
	return &Registry{
		defs: make(map[string]*AgentDefinition),
	}
}

// Register adds an agent definition after validation. Returns an error when
// the definition fails Validate (e.g., TierSubAgent with no OutputSchema).
// Overwrites if the name already exists — last write wins, same as before.
//
// Callers that want to register a known-good definition without checking the
// error can use MustRegister, which panics on validation failure.
func (r *Registry) Register(def *AgentDefinition) error {
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
func (r *Registry) MustRegister(def *AgentDefinition) {
	if err := r.Register(def); err != nil {
		panic(fmt.Sprintf("agent.MustRegister: %v", err))
	}
}

// Get returns a definition by name, or nil.
func (r *Registry) Get(name string) *AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defs[name]
}

// All returns all registered definitions.
func (r *Registry) All() []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*AgentDefinition, 0, len(r.defs))
	for _, d := range r.defs {
		result = append(result, d)
	}
	return result
}

// Unregister removes an agent definition by name. Returns true if it existed.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.defs[name]
	delete(r.defs, name)
	return ok
}

// TeamMembers returns all definitions marked as team members, sorted by name.
func (r *Registry) TeamMembers() []*AgentDefinition {
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
func (r *Registry) Names() []string {
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
func (r *Registry) FindBySkill(skill string) []*AgentDefinition {
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
func (r *Registry) ListForPlanner() []PlannerListing {
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

