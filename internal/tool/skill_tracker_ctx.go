package tool

import "context"

// skillTrackerKey is the unexported key type for *SkillTracker in ctx.
// Stored as `any` so this package doesn't import internal/engine.
// load_skill / unload_skill / list_loaded_skills type-assert when reading.
type skillTrackerKey struct{}

var skillTrackerContextKey = skillTrackerKey{}

// GetSkillTrackerValue returns whatever the engine stored under the
// skill-tracker key. nil + false when absent (non-freelancer agent).
// Callers in the loadskill / unloadskill / listloadedskills packages
// assert to the concrete *engine.SkillTracker type.
func GetSkillTrackerValue(ctx context.Context) (any, bool) {
	v := ctx.Value(skillTrackerContextKey)
	if v == nil {
		return nil, false
	}
	return v, true
}

// WithSkillTrackerValue attaches a *SkillTracker handle to ctx. The
// engine layer passes the concrete *engine.SkillTracker; the helper
// stays type-agnostic so the tool package doesn't import engine.
func WithSkillTrackerValue(ctx context.Context, tracker any) context.Context {
	return context.WithValue(ctx, skillTrackerContextKey, tracker)
}
