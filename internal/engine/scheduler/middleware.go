package scheduler

import "context"

type Middleware interface {
	Name() string
	Before(ctx context.Context, p SpawnParams, st *SpawnState) (context.Context, error)
	After(ctx context.Context, p SpawnParams, st *SpawnState, r Result, err error)
}
