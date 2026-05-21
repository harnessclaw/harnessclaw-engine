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

const defaultIdleTimeout = 10 * time.Minute

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

// NewPlanWriterRegistry creates a registry with the default 10-minute idle
// timeout.
func NewPlanWriterRegistry(rootDir string) *PlanWriterRegistry {
	return newPlanWriterRegistryWithIdle(rootDir, defaultIdleTimeout)
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
	w := newPlanWriter(r.rootDir, sessionID, r.idleTimeout, nil)
	w.onIdle = func() {
		r.mu.Lock()
		if existing, ok := r.writers[sessionID]; ok && existing == w {
			delete(r.writers, sessionID)
		}
		r.mu.Unlock()
	}
	r.writers[sessionID] = w
	return w
}

// StopAll signals every writer to stop. Used in tests / shutdown.
func (r *PlanWriterRegistry) StopAll(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.writers {
		w.stop()
	}
	r.writers = map[string]*PlanWriter{}
}

// PlanWriter serialises plan.json mutations for one session. Apply enqueues
// a mutation closure; a single consumer goroutine drains the channel.
type PlanWriter struct {
	rootDir     string
	sessionID   string
	idleTimeout time.Duration
	onIdle      func()

	mu      sync.Mutex
	ch      chan planMutation
	stopped bool
}

type planMutation struct {
	apply func(*Plan) error
	reply chan error
}

func newPlanWriter(rootDir, sessionID string, idle time.Duration, onIdle func()) *PlanWriter {
	w := &PlanWriter{
		rootDir:     rootDir,
		sessionID:   sessionID,
		idleTimeout: idle,
		onIdle:      onIdle,
		ch:          make(chan planMutation, 64),
	}
	go w.loop()
	return w
}

func (w *PlanWriter) loop() {
	timer := time.NewTimer(w.idleTimeout)
	defer timer.Stop()
	for {
		select {
		case m, ok := <-w.ch:
			if !ok {
				return
			}
			m.reply <- w.handle(m.apply)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.idleTimeout)
		case <-timer.C:
			w.mu.Lock()
			w.stopped = true
			close(w.ch)
			w.mu.Unlock()
			if w.onIdle != nil {
				w.onIdle()
			}
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
		return err
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
func (w *PlanWriter) Apply(ctx context.Context, fn func(*Plan) error) error {
	w.mu.Lock()
	stopped := w.stopped
	w.mu.Unlock()
	if stopped {
		return errors.New("plan writer stopped")
	}
	reply := make(chan error, 1)
	select {
	case w.ch <- planMutation{apply: fn, reply: reply}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *PlanWriter) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.stopped = true
	close(w.ch)
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
