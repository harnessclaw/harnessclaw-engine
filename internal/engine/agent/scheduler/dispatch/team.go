package dispatch

import (
	"context"
	"errors"

	"harnessclaw-go/internal/engine/agent/scheduler/types"
)

// TeamStrategy is a placeholder for multi-agent team coordination.
type TeamStrategy struct{}

func (TeamStrategy) Kind() types.Kind { return types.KindTeam }

func (TeamStrategy) Capabilities() Capabilities {
	return Capabilities{}
}

func (TeamStrategy) Run(ctx context.Context, taskID types.TaskID, deps Deps) (types.MetaRef, error) {
	return "", errors.New("not implemented")
}
