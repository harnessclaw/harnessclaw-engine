package session

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/metric/sessionstats"
)

// statsPersistDebounce is the window over which tracker mutations are
// coalesced into a single SQLite write. Chosen at 1s — short enough
// that the metrics_json column tracks the live tracker closely, long
// enough that a burst of LLM calls inside one orchestration step
// collapses to one disk write.
const statsPersistDebounce = 1 * time.Second

// statsPersistFlushTimeout caps how long Stop / explicit Flush() waits
// before giving up.
const statsPersistFlushTimeout = 5 * time.Second

// statsPersistWorker is the stats analog of persistWorker. One worker
// per session; receives non-blocking "dirty" signals on a 1-cap chan,
// flushes a snapshot through the store at most once per debounce
// window.
//
// Why a separate worker (not reused persistWorker): stats writes fire
// per LLM call (dozens per turn) while message writes fire per turn.
// Mixing them would make the message path pay for stats churn and add
// avoidable contention on a single SQLite write lock.
type statsPersistWorker struct {
	sessionID string
	tracker   *sessionstats.Tracker
	store     Store
	logger    *zap.Logger

	dirty    chan struct{}
	flushNow chan chan struct{}
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

// newStatsPersistWorker constructs and starts the worker goroutine.
func newStatsPersistWorker(sessionID string, tracker *sessionstats.Tracker, store Store, logger *zap.Logger) *statsPersistWorker {
	w := &statsPersistWorker{
		sessionID: sessionID,
		tracker:   tracker,
		store:     store,
		logger:    logger,
		dirty:     make(chan struct{}, 1),
		flushNow:  make(chan chan struct{}, 4),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go w.loop()
	return w
}

// NotifyChan exposes the dirty channel so the Tracker can bind it via
// BindNotify. Send is non-blocking from the Tracker's side.
func (w *statsPersistWorker) NotifyChan() chan<- struct{} { return w.dirty }

// Flush triggers an immediate write and waits up to statsPersistFlushTimeout.
func (w *statsPersistWorker) Flush(ctx context.Context) error {
	ack := make(chan struct{})
	select {
	case w.flushNow <- ack:
	case <-w.stop:
		return nil
	}
	select {
	case <-ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(statsPersistFlushTimeout):
		return context.DeadlineExceeded
	}
}

// Stop performs a final flush and terminates the worker. Idempotent.
func (w *statsPersistWorker) Stop() {
	w.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), statsPersistFlushTimeout)
		_ = w.Flush(ctx)
		cancel()
		close(w.stop)
		<-w.done
	})
}

func (w *statsPersistWorker) loop() {
	defer close(w.done)

	var timer *time.Timer
	var timerC <-chan time.Time

	armTimer := func() {
		if timer == nil {
			timer = time.NewTimer(statsPersistDebounce)
			timerC = timer.C
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(statsPersistDebounce)
		timerC = timer.C
	}
	disarmTimer := func() {
		if timer != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerC = nil
	}

	for {
		select {
		case <-w.stop:
			return
		case <-w.dirty:
			armTimer()
		case <-timerC:
			disarmTimer()
			w.flush()
		case ack := <-w.flushNow:
			disarmTimer()
			w.flush()
			close(ack)
		}
	}
}

func (w *statsPersistWorker) flush() {
	ctx, cancel := context.WithTimeout(context.Background(), statsPersistFlushTimeout)
	defer cancel()
	snap := w.tracker.Snapshot()
	if snap.SessionID == "" {
		snap.SessionID = w.sessionID
	}
	if err := w.store.SaveSessionStats(ctx, w.sessionID, snap); err != nil {
		w.logger.Warn("failed to persist session stats",
			zap.String("session_id", w.sessionID),
			zap.Error(err),
		)
	}
}
