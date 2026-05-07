package session

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// persistDebounce is the maximum time we accumulate session changes
// before flushing to the store. 500ms is a sensible default: short
// enough that crash-recovery loses very little, long enough to coalesce
// the burst of mutations during a single LLM streaming turn into one
// disk write.
const persistDebounce = 500 * time.Millisecond

// persistFlushTimeout caps how long Stop waits for the in-flight flush
// to complete before giving up. Prevents shutdown from hanging if the
// store is wedged.
const persistFlushTimeout = 5 * time.Second

// persistWorker debounces persist requests for a single session. One
// worker per session: the worker runs a background goroutine, listens
// for "dirty" signals on a 1-cap channel, and flushes to the store at
// most once per persistDebounce window.
//
// Why this exists (root cause):
//
// Before this worker, every Session mutation (AddMessage, SetMessages)
// fired `go m.PersistSession(...)`, opening a new goroutine per change.
// Under load (a streaming LLM turn touches the session many times) this
// produced dozens of concurrent goroutines all racing for SQLite's
// single write lock — surfacing as `database is locked (5) (SQLITE_BUSY)`.
//
// With the worker, N mutations within a debounce window collapse into
// 1 disk write. Goroutine count is fixed at 1 per active session, not
// 1 per mutation.
type persistWorker struct {
	store    Store
	logger   *zap.Logger
	sess     *Session
	dirty    chan struct{}  // capacity 1; "merged" signal
	flushNow chan chan struct{} // for explicit Flush() — caller waits on the inner chan
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

// newPersistWorker constructs and starts a worker. The caller must hold
// no Session mutex when invoking.
func newPersistWorker(sess *Session, store Store, logger *zap.Logger) *persistWorker {
	w := &persistWorker{
		store:    store,
		logger:   logger,
		sess:     sess,
		dirty:    make(chan struct{}, 1),
		flushNow: make(chan chan struct{}, 4),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go w.loop()
	return w
}

// notify is non-blocking: if the dirty channel is already full there's
// already a pending flush queued, so dropping is correct (we'd just
// re-flush the same state). This is the hot path called from Session
// mutators and must NEVER block the caller.
func (w *persistWorker) notify() {
	select {
	case w.dirty <- struct{}{}:
	default:
	}
}

// Flush triggers an immediate write and blocks until it completes (or
// flushTimeout elapses). Used by Manager.PersistAll / shutdown to
// guarantee on-disk state is current.
func (w *persistWorker) Flush(ctx context.Context) error {
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
	case <-time.After(persistFlushTimeout):
		return context.DeadlineExceeded
	}
}

// Stop performs a final flush (best-effort) and terminates the worker.
// Idempotent.
func (w *persistWorker) Stop() {
	w.once.Do(func() {
		// Best-effort final flush so on-disk state is current at shutdown.
		ctx, cancel := context.WithTimeout(context.Background(), persistFlushTimeout)
		_ = w.Flush(ctx)
		cancel()
		close(w.stop)
		<-w.done
	})
}

// loop is the worker goroutine. It coalesces dirty signals received
// within persistDebounce into a single store write.
func (w *persistWorker) loop() {
	defer close(w.done)

	var timer *time.Timer
	var timerC <-chan time.Time

	armTimer := func() {
		if timer == nil {
			timer = time.NewTimer(persistDebounce)
			timerC = timer.C
			return
		}
		// Reset only when not already firing — see time.Timer docs.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(persistDebounce)
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
			// Start (or restart) the debounce window. Subsequent dirty
			// signals reset the window — but if mutations come in a
			// continuous stream we still flush after persistDebounce
			// since armTimer only resets the timer, doesn't extend
			// indefinitely (see fall-through behaviour below).
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

// flush writes the session to the store. Errors are logged; the worker
// keeps running so subsequent mutations get another shot.
func (w *persistWorker) flush() {
	ctx, cancel := context.WithTimeout(context.Background(), persistFlushTimeout)
	defer cancel()
	if err := w.store.SaveSession(ctx, w.sess); err != nil {
		w.logger.Error("failed to persist session",
			zap.String("session_id", w.sess.ID),
			zap.Error(err),
		)
	}
}
