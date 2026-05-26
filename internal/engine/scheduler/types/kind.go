package types

// Kind is the dispatch strategy kind, also used as TaskState.Kind for L3 leaves.
type Kind string

const (
	KindReact Kind = "react"
	KindPlan  Kind = "plan"
	KindTeam  Kind = "team"
	KindVote  Kind = "vote"
	KindLeaf  Kind = "leaf" // L3 sub-agent
)
