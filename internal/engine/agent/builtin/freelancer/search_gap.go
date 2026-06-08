package freelancer

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"harnessclaw-go/pkg/types"
)

// File container for "capability gap" detectors. Currently houses
// SearchGapDetector only — search is the single capability that has
// "any-of-two backends" semantics.
//
// Moved from internal/engine/spawn/capability_gap.go under Stage 6 of
// the engine refactor. The legacy spawn package retains its own copy
// during the transition; Stage 8 deletes the spawn-side duplicate.

// EmitFunc is the channel-emit closure passed in by callers. The ctx
// argument propagates the spawn-call context so the emit step can be
// cancelled cleanly (e.g. select on a channel send vs ctx.Done()).
// Returning an error tells the detector that the seen-marker must be
// rolled back so the next spawn can retry.
type EmitFunc func(ctx context.Context, ev types.EngineEvent) error

// SearchGapDetector emits at most one CardSystem notice per session
// when a TierSubAgent spawns with web_search or tavily_search in its
// declared AllowedTools but neither tool is present in the final tool
// pool (typically because both are disabled in yaml).
type SearchGapDetector struct {
	seen sync.Map // sessionID(string) -> struct{}{}
	log  *zap.Logger
}

// searchToolNames are the registered tool names of the two interchangeable
// search backends. Keep in sync with cmd/server/main.go's builtInTools
// table.
var searchToolNames = []string{"web_search", "tavily_search"}

// NewSearchGapDetector returns a fresh detector. log may be nil
// (replaced with zap.NewNop()).
func NewSearchGapDetector(log *zap.Logger) *SearchGapDetector {
	if log == nil {
		log = zap.NewNop()
	}
	return &SearchGapDetector{log: log}
}

// CheckAndEmit inspects one TierSubAgent spawn. Safe to call on a nil
// receiver (returns early). emit is the channel-send closure; returning
// an error from it triggers a one-shot rollback of the per-session seen
// marker so the next spawn can re-attempt.
func (d *SearchGapDetector) CheckAndEmit(
	ctx context.Context,
	sessionID, agentName string,
	declared, final []string,
	emit EmitFunc,
) {
	if d == nil || sessionID == "" {
		return
	}
	if !shouldWarn(declared, final) {
		return
	}
	if _, loaded := d.seen.LoadOrStore(sessionID, struct{}{}); loaded {
		return
	}
	ev := types.EngineEvent{
		Type: types.EngineEventSystemNotice,
		SystemNotice: &types.SystemNotice{
			Topic: "search_capability_gap",
			Title: "搜索能力不可用",
			Summary: fmt.Sprintf(
				"本次任务派到的 sub-agent (%s) 依赖网络搜索，但配置中 web_search 和 tavily_search 均未启用，结果可能依赖训练知识、缺乏时效性和来源核查。",
				agentName,
			),
			ActionHint: "去设置页，搜索配置，配置相关服务",
			Icon:       "warning",
		},
	}
	if err := emit(ctx, ev); err != nil {
		d.log.Warn("emit system card (search gap) failed",
			zap.String("session_id", sessionID),
			zap.String("agent", agentName),
			zap.Error(err))
		// Rollback. Same TOCTOU window as the legacy spawn copy.
		d.seen.Delete(sessionID)
		return
	}
	d.log.Info("search capability gap detected",
		zap.String("session_id", sessionID),
		zap.String("agent", agentName),
		zap.Strings("declared", declared))
}

// Forget releases the per-session seen entry. Optional — missing call
// only leaks one zero-byte map entry per session, recovered on process
// restart.
func (d *SearchGapDetector) Forget(sessionID string) {
	if d == nil {
		return
	}
	d.seen.Delete(sessionID)
}

// shouldWarn is the pure predicate so the gating logic is independently
// testable from the emit plumbing. Definition declares search if it
// names either backend; runtime has search if either backend remains
// in the final tool pool.
func shouldWarn(declared, final []string) bool {
	return containsAnySlice(declared, searchToolNames) && !containsAnySlice(final, searchToolNames)
}

// containsAnySlice reports whether haystack contains at least one element
// from needles.
func containsAnySlice(haystack, needles []string) bool {
	if len(haystack) == 0 || len(needles) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(haystack))
	for _, n := range haystack {
		set[n] = struct{}{}
	}
	for _, w := range needles {
		if _, ok := set[w]; ok {
			return true
		}
	}
	return false
}
