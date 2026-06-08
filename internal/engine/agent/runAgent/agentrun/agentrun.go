// Package agentrun is the single, mode-aware entry point for running an
// agent — sync or async, in-process or through the L2 scheduler.
//
// Design intent (Axis-2 of the project structure refactor):
//
//   - The data-driven engine/runner package answers "what does this
//     agent do" (Profile, AllowedTools, MaxTurns, hooks).
//   - agentrun answers "how do we run it" (sync/async, in-process /
//     scheduled / isolated).
//
// Callers (emma tools, mention router, future scheduler integrations)
// stop reaching for agent.AgentSpawner.SpawnSync or scheduler.Coordinator
// directly; instead they hand the runner a Request and either block on
// Run or fire-and-forget through Submit. Adding a new execution mode in
// the future does not touch any caller.
package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	schedtypes "harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/legacy/workspace"
	pkgtypes "harnessclaw-go/pkg/types"
)

// Mode selects the execution path inside Runner.Run / Submit.
type Mode int

const (
	// ModeInproc executes the agent synchronously in the current
	// process via the injected AgentSpawner. This is the 90% path —
	// emma tools, mentions, ad-hoc leaf dispatches — and the only
	// mode wired in this initial cut.
	ModeInproc Mode = iota

	// ModeScheduled routes the agent through the L2 scheduler so it
	// gets task state, retries, lease management, and cross-task
	// dependency tracking. The L3 execution layer lives in
	// internal/agentrun/worker (ConsumerPool + Factory + EventRegistry).
	ModeScheduled
)

// Request describes one agent invocation. Cfg carries the legacy
// SpawnConfig shape (used by ModeInproc); Spec carries the scheduler
// TaskSpec shape (used by ModeScheduled). Mode picks the execution
// path; Events optionally overrides Cfg.ParentOut / the scheduler's
// outCh so callers can plumb a fresh channel per call without mutating
// the underlying value.
type Request struct {
	// Cfg is the in-process spawn config (ModeInproc).
	Cfg *agent.SpawnConfig

	// Spec is the L2 scheduler task spec (ModeScheduled).
	Spec *spec.TaskSpec

	Mode   Mode
	Events chan<- pkgtypes.EngineEvent
}

// Result wraps the executor outcome. SpawnResult is populated by
// ModeInproc; MetaRef is populated by ModeScheduled (it points at the
// meta.json the L3 leaf wrote under the session workspace). Callers
// branch on whichever they expected for the Mode they requested.
type Result struct {
	*agent.SpawnResult
	MetaRef schedtypes.MetaRef
}

// SchedulerBackend is the surface agentrun needs to drive
// ModeScheduled end-to-end (kind selection, submit, polling, event
// fan-out, deliverables promotion). internal/engine/scheduler.Coordinator
// satisfies this interface via simple accessor methods; agentrun owns
// the orchestration logic that used to live inside Coordinator.Run.
type SchedulerBackend interface {
	// Submit admits a TaskSpec and returns the root task ID. The
	// scheduler runs the strategy in the background — agentrun is
	// responsible for waiting on the task and promoting deliverables.
	Submit(ctx context.Context, sp spec.TaskSpec) (schedtypes.TaskID, error)

	// Get returns the current TaskState for id; used by the polling
	// loop to detect terminal status.
	Get(ctx context.Context, id schedtypes.TaskID) (tstate.TaskState, error)

	// RegisterEvents binds the per-Run event channel to the root task
	// ID so L3 sub-agents triggered under that root forward lifecycle
	// events back to the caller's stream.
	RegisterEvents(id schedtypes.TaskID, ch chan<- pkgtypes.EngineEvent)

	// UnregisterEvents releases a binding previously created via
	// RegisterEvents.
	UnregisterEvents(id schedtypes.TaskID)

	// SelectKind classifies a goal into KindReact / KindPlan when the
	// TaskSpec did not pin one. Empty Kind means "no selector
	// configured" — callers default to KindReact.
	SelectKind(goal string) schedtypes.Kind

	// RootDir is the workspace root used to locate session
	// directories during deliverables promotion.
	RootDir() string

	// Logger is the structured logger used to warn on best-effort
	// promotion errors.
	Logger() *slog.Logger
}

// Runner is the single-entry, mode-aware dispatcher. Construct once at
// engine start with New(spawner) and share across all callers; it is
// safe for concurrent use.
type Runner struct {
	spawner agent.AgentSpawner

	// async is an optional AsyncSpawner used by RunBackground. When the
	// underlying AgentSpawner already satisfies AsyncSpawner the
	// constructor auto-detects it; callers can also opt in explicitly
	// via WithAsyncSpawner.
	async agent.AsyncSpawner

	// sched is the optional scheduler backend used by ModeScheduled.
	// nil means scheduled mode is unwired (Run returns
	// ErrSchedulerNotConfigured).
	sched SchedulerBackend
}

// New constructs a Runner backed by the given AgentSpawner. A nil
// spawner is permitted for runners that only use ModeScheduled (e.g.
// the L2 scheduler module's strategy wrappers); ModeInproc will return
// ErrInprocNotSupported in that case. If the spawner also satisfies
// agent.AsyncSpawner the async path is enabled automatically;
// otherwise RunBackground returns ErrAsyncNotSupported.
func New(spawner agent.AgentSpawner) *Runner {
	r := &Runner{spawner: spawner}
	if as, ok := spawner.(agent.AsyncSpawner); ok {
		r.async = as
	}
	return r
}

// WithAsyncSpawner explicitly attaches an AsyncSpawner (used when the
// sync and async paths live in different concrete types). Returns the
// receiver to allow fluent construction.
func (r *Runner) WithAsyncSpawner(s agent.AsyncSpawner) *Runner {
	r.async = s
	return r
}

// WithScheduler attaches the L2 scheduler backend that powers
// ModeScheduled. Returns the receiver to allow fluent construction.
// Callers that never use ModeScheduled may skip this call.
func (r *Runner) WithScheduler(b SchedulerBackend) *Runner {
	r.sched = b
	return r
}

// ErrUnsupportedMode is returned when Run is asked for a Mode that
// the current build doesn't wire — currently anything other than
// ModeInproc.
var ErrUnsupportedMode = errors.New("agentrun: unsupported Mode")

// ErrAsyncNotSupported is returned by RunBackground when the Runner
// was constructed without an AsyncSpawner.
var ErrAsyncNotSupported = errors.New("agentrun: async spawning not supported by this engine configuration")

// ErrSchedulerNotConfigured is returned by Run when ModeScheduled is
// requested but the Runner has no SchedulerBackend wired.
var ErrSchedulerNotConfigured = errors.New("agentrun: scheduler backend not configured")

// ErrInprocNotSupported is returned by Run when ModeInproc is requested
// but the Runner was constructed with a nil AgentSpawner.
var ErrInprocNotSupported = errors.New("agentrun: inproc spawner not configured")

// Run executes the request synchronously and returns the Result.
// Cfg.ParentOut is replaced by req.Events when the latter is non-nil so
// the caller can plumb a per-call event channel without mutating
// Cfg in place.
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	switch req.Mode {
	case ModeInproc:
		if req.Cfg == nil {
			return nil, errors.New("agentrun: Request.Cfg is required for ModeInproc")
		}
		if req.Events != nil {
			cfgCopy := *req.Cfg
			cfgCopy.ParentOut = req.Events
			req.Cfg = &cfgCopy
		}
		res, err := r.spawner.SpawnSync(ctx, req.Cfg)
		if err != nil {
			return nil, err
		}
		return &Result{SpawnResult: res}, nil
	case ModeScheduled:
		if req.Spec == nil {
			return nil, errors.New("agentrun: Request.Spec is required for ModeScheduled")
		}
		if r.sched == nil {
			return nil, ErrSchedulerNotConfigured
		}
		ref, err := r.runScheduled(ctx, *req.Spec, req.Events)
		if err != nil {
			return nil, err
		}
		return &Result{MetaRef: ref}, nil
	default:
		return nil, fmt.Errorf("%w: Mode=%d", ErrUnsupportedMode, req.Mode)
	}
}

// runScheduled drives one ModeScheduled task end-to-end: resolve the
// task kind, submit, register the per-Run event channel, poll tstate
// until terminal, then promote any declared outputs into the session's
// deliverables/ directory.
//
// This logic used to live inside scheduler.Coordinator.Run; relocating
// it here keeps the Coordinator a thin construction wrapper and lets
// every "how the agent runs" decision flow through agentrun, matching
// the Axis-2 split documented at the top of this file.
//
// Polling (vs. bus subscription) avoids a startup race where the
// scheduler router goroutine may not have subscribed to the bus yet
// when the first lifecycle message is published. The kernel is always
// ready and 5 ms is fast enough for tests with low production overhead.
func (r *Runner) runScheduled(ctx context.Context, sp spec.TaskSpec, outCh chan<- pkgtypes.EngineEvent) (schedtypes.MetaRef, error) {
	if sp.Hint.Kind == "" {
		sp.Hint.Kind = r.sched.SelectKind(sp.Goal)
	}
	if sp.Hint.Kind == "" {
		sp.Hint.Kind = schedtypes.KindReact
	}

	taskID, err := r.sched.Submit(ctx, sp)
	if err != nil {
		return "", fmt.Errorf("agentrun: scheduler submit: %w", err)
	}

	// Bind the per-Run event channel to the root task ID so the worker
	// Factory can locate it when walking each leaf task's parent chain.
	if outCh != nil {
		r.sched.RegisterEvents(taskID, outCh)
		defer r.sched.UnregisterEvents(taskID)
	}

	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("agentrun: context cancelled waiting for task %s: %w", taskID, ctx.Err())
		case <-ticker.C:
			ts, err := r.sched.Get(ctx, taskID)
			if err != nil {
				return "", fmt.Errorf("agentrun: get task %s: %w", taskID, err)
			}
			switch ts.Status {
			case schedtypes.StatusSucceeded:
				ref := schedtypes.MetaRef(ts.ResultRef)
				r.promoteToDeliverables(sp.SessionID, ref)
				return ref, nil
			case schedtypes.StatusFailed, schedtypes.StatusCancelled:
				return "", fmt.Errorf("agentrun: task %s terminal: %s (last_error=%q)", taskID, ts.Status, ts.LastError)
			}
		}
	}
}

// promoteToDeliverables copies every output file declared in the final
// task's meta.json into {sessionRoot}/deliverables/ so L1 (emma) has a
// single, stable directory to point the user at. Errors are best-effort:
// a missing source file or a write failure is logged but never
// propagates to the caller.
func (r *Runner) promoteToDeliverables(sessionID string, ref schedtypes.MetaRef) {
	rootDir := r.sched.RootDir()
	if rootDir == "" || sessionID == "" || ref == "" {
		return
	}
	sessionRoot := workspace.SessionRoot(rootDir, sessionID)
	absMetaPath := filepath.Join(sessionRoot, string(ref))
	b, err := os.ReadFile(absMetaPath)
	if err != nil {
		return
	}
	var m workspace.Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return
	}
	delivDir := workspace.DeliverablesDir(rootDir, sessionID)
	if err := os.MkdirAll(delivDir, 0o755); err != nil {
		return
	}
	logger := r.sched.Logger()
	for _, o := range m.Outputs {
		if o.Path == "" {
			continue
		}
		src := o.Path
		if !filepath.IsAbs(src) {
			src = filepath.Join(sessionRoot, o.Path)
		}
		dest := filepath.Join(delivDir, filepath.Base(src))
		if err := copyFile(src, dest); err != nil && logger != nil {
			logger.Warn("agentrun promote: copy failed",
				slog.String("src", src),
				slog.String("err", err.Error()),
			)
		}
	}
}

// copyFile copies src to dst atomically via a temp file.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// RunBackground launches the request through the underlying
// AsyncSpawner and returns the assigned agent ID. The agent continues
// running after the call returns; observers track its lifecycle via
// the AsyncSpawner's broker (see internal/agent/async.go). Use this
// when the LLM marks Task with run_in_background=true.
//
// Returns ErrAsyncNotSupported if the Runner has no AsyncSpawner.
func (r *Runner) RunBackground(ctx context.Context, req Request) (string, error) {
	if req.Cfg == nil {
		return "", errors.New("agentrun: Request.Cfg is required")
	}
	if r.async == nil {
		return "", ErrAsyncNotSupported
	}
	if req.Events != nil {
		cfgCopy := *req.Cfg
		cfgCopy.ParentOut = req.Events
		req.Cfg = &cfgCopy
	}
	return r.async.SpawnAsync(ctx, req.Cfg)
}

// Submit launches the request asynchronously and returns a Handle that
// callers can Wait / Cancel. The current implementation forks a
// goroutine that delegates to Run; ModeScheduled will reuse the same
// Handle contract once the scheduler path lands so callers don't
// branch on Mode at the call site.
func (r *Runner) Submit(ctx context.Context, req Request) (Handle, error) {
	if req.Cfg == nil {
		return nil, errors.New("agentrun: Request.Cfg is required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	h := &handle{
		done:   make(chan struct{}),
		cancel: cancel,
	}
	go func() {
		defer close(h.done)
		res, err := r.Run(runCtx, req)
		h.mu.Lock()
		h.result = res
		h.err = err
		h.mu.Unlock()
	}()
	return h, nil
}

// Handle observes an asynchronous agent invocation.
type Handle interface {
	// Wait blocks until the agent terminates or ctx is cancelled.
	// Calling Wait after the underlying agent has already finished
	// returns the cached result immediately.
	Wait(ctx context.Context) (*Result, error)

	// Cancel requests termination of the underlying agent. It is a
	// no-op once the agent has already terminated.
	Cancel(ctx context.Context) error

	// Done is closed once the agent has terminated (success, failure,
	// or cancellation). Useful for select{} composition.
	Done() <-chan struct{}
}

type handle struct {
	done   chan struct{}
	cancel context.CancelFunc

	mu     sync.Mutex
	result *Result
	err    error
}

func (h *handle) Wait(ctx context.Context) (*Result, error) {
	select {
	case <-h.done:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.result, h.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *handle) Cancel(_ context.Context) error {
	h.cancel()
	return nil
}

func (h *handle) Done() <-chan struct{} { return h.done }
