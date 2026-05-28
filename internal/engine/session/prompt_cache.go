package session

// PromptCacheEntry stores a cached system prompt and the conditions
// under which it was built. The cache is invalidated when any input
// changes enough to affect the output (budget, memoryLen, hasTask,
// date).
//
// Fields are exported so QueryEngine code that's about to migrate
// here in later Tasks can populate / read them across package
// boundaries. Migration happens piecewise: Task 4.5 deletes the
// engine-package mirror once all readers/writers reference this one.
type PromptCacheEntry struct {
	Prompt    string      // cached ToSystemPrompt() result
	Output    interface{} // *prompt.PromptOutput (stored as interface{} to avoid circular import)
	Budget    int         // budget when cached
	HasTask   bool        // whether task state was present
	MemoryLen int         // len(memory) when cached
	Date      string      // date when cached (YYYY-MM-DD)
}
