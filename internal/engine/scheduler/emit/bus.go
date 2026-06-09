// Package emit 提供 scheduler 内置的轻量 pub/sub —— 仅为埋点解耦使用。
// 不复用 internal/msgbus，那是 L2 kernel 的 control/notify/result 总线，太重。
package emit

import "context"

type Event struct {
	Topic   string
	Payload any
}

type Subscriber func(ctx context.Context, evt Event)

type Bus interface {
	Publish(ctx context.Context, evt Event) error
	Subscribe(topic string, fn Subscriber) (unsubscribe func())
}
