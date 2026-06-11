package router

import (
	"context"

	"harnessclaw-go/pkg/types"
)

// MessageDispatcher is a one-shot delivery callback: it hands a single
// IncomingMessage to the engine entry point (typically Router.Handle).
//
// It lives outside the channel package because its role is "a thin
// abstraction of the engine entry point". Engine-internal subsystems
// that need to inject synthesized messages (for example the emma resume
// path that fabricates a user.message after an error-recovery step)
// can hold this type instead of taking a hard dependency on a concrete
// Router or on a channel.Channel.
type MessageDispatcher func(ctx context.Context, msg *types.IncomingMessage) error
