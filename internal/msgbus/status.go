package msgbus

// MsgStatus tracks per-message delivery state inside msgbus (NOT business state).
// Business state lives in tstate.Status. The two are orthogonal:
//   - MsgStatus drives redelivery / ack timeout
//   - tstate.Status drives task scheduling decisions
type MsgStatus string

const (
	MsgQueued    MsgStatus = "queued"
	MsgDelivered MsgStatus = "delivered"
	MsgAcked     MsgStatus = "acked"
	MsgFailed    MsgStatus = "failed"
)
