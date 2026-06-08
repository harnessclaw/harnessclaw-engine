package permission

// RuleSource identifies where a permission rule was defined.
// Sources are evaluated in priority order (highest first).
type RuleSource int

const (
	SourceSession         RuleSource = iota // per-session override (highest priority)
	SourceCLIArg                            // --allowedTools / --deniedTools
	SourcePolicy                            // MDM / enterprise policy (lowest priority)
)

// RuleBehavior is the action a rule prescribes.
type RuleBehavior string

const (
	BehaviorAllow RuleBehavior = "allow"
	BehaviorDeny  RuleBehavior = "deny"
)

// Rule is a single permission directive.
type Rule struct {
	Source   RuleSource   `json:"source"`
	Behavior RuleBehavior `json:"behavior"`
	ToolName string       `json:"tool_name"` // exact name or "*" wildcard
	Pattern  string       `json:"pattern"`   // optional argument pattern (future use)
}

// bySourcePriority returns rules grouped and ordered by source priority.
func bySourcePriority(rules []Rule) [][]Rule {
	buckets := make([][]Rule, int(SourcePolicy)+1)
	for i := range buckets {
		buckets[i] = make([]Rule, 0)
	}
	for _, r := range rules {
		idx := int(r.Source)
		if idx >= 0 && idx < len(buckets) {
			buckets[idx] = append(buckets[idx], r)
		}
	}
	return buckets
}
