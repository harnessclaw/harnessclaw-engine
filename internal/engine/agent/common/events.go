package common

import (
	"harnessclaw-go/pkg/types"
)

// StartEvent carries everything an EmitSubagentStart caller wants on
// the wire envelope. Caller fills these; common stamps the right
// EngineEvent fields.
//
// ParentAgentID + ParentSessionID are the explicit parent-tracking
// fields. The front-end can't see engine ctx propagation, so the
// envelope must carry them so the FE can build the agent tree without
// inferring. "main" is the conventional ParentAgentID when emma
// dispatches.
type StartEvent struct {
	AgentID         string
	AgentName       string
	AgentDesc       string
	AgentTask       string
	AgentType       string
	SubagentType    string
	// Fork is reserved for future use — captures whether the caller
	// asked for a forked sub-agent (vs in-line dispatch). EngineEvent
	// currently has no Fork field; this is plumbed here so tier modules
	// can populate it now without churning call sites when the wire
	// field lands.
	Fork            bool
	ParentAgentID   string
	ParentSessionID string
	ParentStepID    string
	LoadedSkills    []types.LoadedSkillInfo
}

// EmitSubagentStart writes a subagent_start event to out. Non-blocking;
// drops on a full or unbuffered channel with no receiver. nil channel
// is a silent no-op so callers don't have to guard tests.
func EmitSubagentStart(out chan<- types.EngineEvent, s StartEvent) {
	if out == nil {
		return
	}
	evt := types.EngineEvent{
		Type:            types.EngineEventSubAgentStart,
		AgentID:         s.AgentID,
		AgentName:       s.AgentName,
		AgentDesc:       s.AgentDesc,
		AgentTask:       s.AgentTask,
		AgentType:       s.AgentType,
		SubagentType:    s.SubagentType,
		ParentAgentID:   s.ParentAgentID,
		ParentSessionID: s.ParentSessionID,
		ParentStepID:    s.ParentStepID,
		LoadedSkills:    s.LoadedSkills,
	}
	select {
	case out <- evt:
	default:
	}
}

// EndEvent carries everything for subagent_end. ParentAgentID +
// ParentSessionID mirror StartEvent so the FE can pair the two halves
// of a sub-agent run without ambient context.
type EndEvent struct {
	AgentID         string
	AgentName       string
	AgentStatus     string
	SubagentType    string
	DurationMs      int64
	Usage           *types.Usage
	Terminal        *types.Terminal
	Artifacts       []types.ArtifactRef
	ParentAgentID   string
	ParentSessionID string
}

// EmitSubagentEnd writes a subagent_end event to out. Non-blocking;
// same semantics as EmitSubagentStart.
func EmitSubagentEnd(out chan<- types.EngineEvent, e EndEvent) {
	if out == nil {
		return
	}
	evt := types.EngineEvent{
		Type:            types.EngineEventSubAgentEnd,
		AgentID:         e.AgentID,
		AgentName:       e.AgentName,
		AgentStatus:     e.AgentStatus,
		SubagentType:    e.SubagentType,
		Duration:        e.DurationMs,
		Usage:           e.Usage,
		Terminal:        e.Terminal,
		Artifacts:       e.Artifacts,
		ParentAgentID:   e.ParentAgentID,
		ParentSessionID: e.ParentSessionID,
	}
	select {
	case out <- evt:
	default:
	}
}

// BuildSpawnResult converts loop output + caller metadata into an
// SpawnResult. Tier modules call this immediately before
// returning from their Run. Pointer copies of Terminal/Usage are made
// internally so callers can pass values without worrying about
// aliasing.
func BuildSpawnResult(sessionID, agentID, output string, terminal types.Terminal, usage types.Usage, numTurns int) *SpawnResult {
	term := terminal
	use := usage
	return &SpawnResult{
		Output:    output,
		Terminal:  &term,
		Usage:     &use,
		SessionID: sessionID,
		AgentID:   agentID,
		NumTurns:  numTurns,
	}
}
