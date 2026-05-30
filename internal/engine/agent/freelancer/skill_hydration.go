package freelancer

import (
	"fmt"

	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/skill/tracker"
)

// hydrateSkills prepares a SkillTracker + augmented prompt for the
// freelancer agent. Returns (tracker, skillBlock, err).
//
// When candidates is empty, returns a fresh empty tracker and an empty
// skillBlock — the freelancer can still search_skill / load_skill at
// runtime to discover skills on demand.
//
// On error (missing skill, too many candidates, nil reader with non-empty
// candidates) returns (nil, "", err) so freelancer.Run can fail fast
// before any LLM call.
//
// Moved from internal/engine/spawn/spawn.go (hydrateFreelancer) under
// Stage 6 of the engine refactor; behavior is intentionally identical so
// the spawn-routed freelancer matches legacy spawn semantics.
func hydrateSkills(reader *skill.Reader, candidates []string, taskPrompt string) (*tracker.SkillTracker, string, error) {
	tr := tracker.NewSkillTracker(3)

	if len(candidates) == 0 {
		return tr, "", nil
	}
	if len(candidates) > 3 {
		return nil, "", fmt.Errorf("candidate_skills limit is 3, got %d", len(candidates))
	}
	if reader == nil {
		return nil, "", fmt.Errorf("skill reader not configured; cannot resolve candidate_skills")
	}

	fulls := make([]*skill.SkillFull, 0, len(candidates))
	for _, name := range candidates {
		full, err := reader.Load(name)
		if err != nil {
			return nil, "", fmt.Errorf("candidate skill %q: %w", name, err)
		}
		fulls = append(fulls, full)
	}
	if err := tr.Preload(fulls); err != nil {
		return nil, "", err
	}
	block := prompt.BuildLoadedSkillsBlock(fulls)
	return tr, block, nil
}

// parseCandidateSkills extracts the candidate_skills array from
// SpawnConfig.Inputs. Returns empty slice for nil / wrong-type input.
// Strings only — non-string elements are skipped (defensive against
// loose JSON unmarshalling).
//
// Moved from internal/engine/spawn/spawn.go under Stage 6.
func parseCandidateSkills(inputs map[string]any) []string {
	if inputs == nil {
		return nil
	}
	raw, ok := inputs["candidate_skills"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
