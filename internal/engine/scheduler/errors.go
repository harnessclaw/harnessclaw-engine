package scheduler

import "errors"

var (
	ErrNoStrategy      = errors.New("scheduler: no strategy matched params")
	ErrUnknownStrategy = errors.New("scheduler: Hints.Force names unregistered strategy")
	ErrNilParams       = errors.New("scheduler: nil definition or empty prompt")
	ErrTaskNotFound    = errors.New("scheduler: Subscribe taskID not found")
	ErrNotSubscribable = errors.New("scheduler: strategy does not support live streaming")
)
