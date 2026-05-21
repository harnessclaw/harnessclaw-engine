package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// DefaultIdleTimeout is how long a PlanWriter's consumer goroutine stays
// alive without work before being reclaimed. Exposed because callers
// configuring NewPlanWriterRegistry may want to assert / compare against it.
const DefaultIdleTimeout = 10 * time.Minute

var errStopped = errors.New("plan writer stopped")

// PlanWriterRegistry holds one PlanWriter per active session. Concurrency
// across sessions is unbounded (each session has its own writer); within
// a single session every Apply goes through a single consumer goroutine
// so the on-disk plan.json is never raced.
type PlanWriterRegistry struct {
	rootDir     string
	idleTimeout time.Duration

	mu      sync.Mutex
	writers map[string]*PlanWriter
}

// NewPlanWriterRegistry creates a registry with the default idle timeout
// (DefaultIdleTimeout = 10 minutes). Writers are reclaimed after that
// duration of inactivity and lazily restarted on the next Get.
func NewPlanWriterRegistry(rootDir string) *PlanWriterRegistry {
	return newPlanWriterRegistryWithIdle(rootDir, DefaultIdleTimeout)
}

// newPlanWriterRegistryWithIdle is the test seam — exposes the idle timeout
// so reclaim behavior can be observed in milliseconds.
func newPlanWriterRegistryWithIdle(rootDir string, idle time.Duration) *PlanWriterRegistry {
	return &PlanWriterRegistry{
		rootDir:     rootDir,
		idleTimeout: idle,
		writers:     map[string]*PlanWriter{},
	}
}

// Get returns a writer for the session, lazy-starting if absent.
func (r *PlanWriterRegistry) Get(sessionID string) *PlanWriter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.writers[sessionID]; ok {
		return w
	}
	w := newPlanWriter(r.rootDir, sessionID, r.idleTimeout)
	w.onIdle = func() {
		r.mu.Lock()
		// Pointer-equality match: a lazy restart between idle-close
		// and onIdle callback may have already replaced the entry.
		if existing, ok := r.writers[sessionID]; ok && existing == w {
			delete(r.writers, sessionID)
		}
		r.mu.Unlock()
	}
	r.writers[sessionID] = w
	return w
}

// StopAll signals every writer to stop AND waits for each writer's
// consumer goroutine to exit so any in-flight disk write completes
// before StopAll returns. Used in tests / shutdown.
func (r *PlanWriterRegistry) StopAll(ctx context.Context) {
	r.mu.Lock()
	ws := make([]*PlanWriter, 0, len(r.writers))
	for _, w := range r.writers {
		ws = append(ws, w)
	}
	r.writers = map[string]*PlanWriter{}
	r.mu.Unlock()

	for _, w := range ws {
		w.stop()
	}
	for _, w := range ws {
		w.wg.Wait()
		// The loop goroutine has exited. Drain any items that slipped into
		// the buffered ch in the narrow window between stop() and the
		// goroutine's last drainAndFail call, so Apply callers don't block.
		w.drainAndFail()
	}
}

// PlanWriter serialises plan.json mutations for one session. Apply enqueues
// a mutation closure; a single consumer goroutine drains the channel.
type PlanWriter struct {
	rootDir     string
	sessionID   string
	idleTimeout time.Duration
	onIdle      func()

	ch   chan planMutation // never closed; senders use done to detect shutdown
	done chan struct{}     // closed by stop(); signals senders + loop
	wg   sync.WaitGroup

	mu      sync.Mutex
	stopped bool
}

type planMutation struct {
	apply func(*Plan) error
	reply chan error
}

func newPlanWriter(rootDir, sessionID string, idle time.Duration) *PlanWriter {
	w := &PlanWriter{
		rootDir:     rootDir,
		sessionID:   sessionID,
		idleTimeout: idle,
		ch:          make(chan planMutation, 64),
		done:        make(chan struct{}),
	}
	w.wg.Add(1)
	go w.loop()
	return w
}

func (w *PlanWriter) loop() {
	defer w.wg.Done()
	timer := time.NewTimer(w.idleTimeout)
	defer timer.Stop()
	for {
		select {
		case <-w.done:
			w.drainAndFail()
			return
		case m := <-w.ch:
			m.reply <- w.handle(m.apply)
			// Reset the idle timer for the next iteration. Drain the
			// timer's channel if Stop reports it already fired
			// (Go time docs §timer.Stop) — otherwise the next iteration
			// could see a stale tick.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.idleTimeout)
		case <-timer.C:
			// Timer fired. If items snuck into ch between the timer
			// firing and select dispatch, prefer them — premature
			// reclaim during a burst is wasteful (we'd just lazy-start
			// again on the very next Apply).
			if len(w.ch) > 0 {
				timer.Reset(w.idleTimeout)
				continue
			}
			w.mu.Lock()
			if w.stopped {
				w.mu.Unlock()
				w.drainAndFail()
				return
			}
			w.stopped = true
			close(w.done)
			w.mu.Unlock()
			if w.onIdle != nil {
				w.onIdle()
			}
			w.drainAndFail()
			return
		}
	}
}

// drainAndFail empties any items left in ch and sends errStopped to each
// reply channel so Apply callers don't block forever.
func (w *PlanWriter) drainAndFail() {
	for {
		select {
		case m := <-w.ch:
			m.reply <- errStopped
		default:
			return
		}
	}
}

func (w *PlanWriter) handle(apply func(*Plan) error) error {
	planPath := PlanPath(w.rootDir, w.sessionID)
	b, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	var old Plan
	if err := json.Unmarshal(b, &old); err != nil {
		return fmt.Errorf("unmarshal plan: %w", err)
	}
	// Operate on a deep copy so apply mutations don't corrupt the
	// old-snapshot reference we still need for transition checks.
	next, err := clonePlan(&old)
	if err != nil {
		return fmt.Errorf("clone plan: %w", err)
	}
	if err := apply(next); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if err := next.ValidateTransitionFrom(&old); err != nil {
		return fmt.Errorf("validate transition: %w", err)
	}
	next.UpdatedAt = time.Now().UTC()
	nb, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return writeFileAtomic(planPath, nb, 0o644)
}

// Apply enqueues a mutation and waits for the consumer goroutine to apply
// it. Returns the apply / validate / write error to the caller.
//
// Contract: once a mutation is enqueued it WILL execute. Apply ignores ctx
// cancellation after enqueue to ensure the caller always sees the actual
// mutation result (returning ctx.Err() here would let a successful write
// silently happen behind a "cancelled" return, enabling double-writes on
// caller retry).
//
// The stopped check under mutex is a fast-path guard. The done arm in the
// select below catches the narrow race where stop() fires between the flag
// read and the channel send.
func (w *PlanWriter) Apply(ctx context.Context, fn func(*Plan) error) error {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return errStopped
	}
	w.mu.Unlock()

	reply := make(chan error, 1)
	select {
	case w.ch <- planMutation{apply: fn, reply: reply}:
	case <-w.done:
		return errStopped
	case <-ctx.Done():
		return ctx.Err()
	}
	return <-reply
}

// stop signals the consumer goroutine to shut down. ch is NEVER closed —
// the loop owns ch and exits on done. Callers should use the WaitGroup
// (via Registry.StopAll) to know when the goroutine has actually exited.
func (w *PlanWriter) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.stopped = true
	close(w.done)
}

// clonePlan does deep copy via JSON round-trip. Plan is small enough that
// the marshalling cost is negligible compared to disk I/O.
func clonePlan(p *Plan) (*Plan, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var out Plan
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out.Tasks == nil {
		out.Tasks = map[string]*Task{}
	}
	return &out, nil
}
