package spec

// PromptKit bundles render-time helpers (placeholder; phase 2 migration target
// from internal/agent/prompt.go).
type PromptKit struct {
	SystemTemplate string
	UserTemplate   string
}

// Render is a placeholder until phase 2 migration; intentionally minimal.
func (p PromptKit) Render(goal string) (system, user string) {
	return p.SystemTemplate, goal
}
