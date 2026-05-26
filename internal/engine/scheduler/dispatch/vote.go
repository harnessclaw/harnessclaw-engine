package dispatch

import (
	"context"
	"errors"

	"harnessclaw-go/internal/engine/scheduler/types"
)

// VoteStrategy is a placeholder for consensus voting.
type VoteStrategy struct{}

func (VoteStrategy) Kind() types.Kind { return types.KindVote }

func (VoteStrategy) Capabilities() Capabilities {
	return Capabilities{}
}

func (VoteStrategy) Run(ctx context.Context, taskID types.TaskID, deps Deps) (types.MetaRef, error) {
	return "", errors.New("not implemented")
}
