package tstate

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
)

// IDGen generates a fresh TaskID on each call.
type IDGen func() types.TaskID

// SequentialIDs returns an IDGen that produces prefix + monotonically incrementing counter.
func SequentialIDs(prefix string) IDGen {
	var counter uint64
	return func() types.TaskID {
		n := atomic.AddUint64(&counter, 1)
		return types.TaskID(fmt.Sprintf("%s%d", prefix, n))
	}
}

// KernelConfig holds tunable parameters for a Kernel.
type KernelConfig struct {
	IDGen           IDGen
	DefaultLeaseTTL time.Duration
	MaxSpawnDepth   int
}

// NewKernel creates a Kernel backed by the given Store.
// Defaults: DefaultLeaseTTL=30s, MaxSpawnDepth=10.
func NewKernel(s Store, cfg KernelConfig) Kernel {
	if cfg.IDGen == nil {
		cfg.IDGen = SequentialIDs("t-")
	}
	if cfg.DefaultLeaseTTL == 0 {
		cfg.DefaultLeaseTTL = 30 * time.Second
	}
	if cfg.MaxSpawnDepth == 0 {
		cfg.MaxSpawnDepth = 10
	}
	return &kernel{s: s, cfg: cfg}
}

type kernel struct {
	s   Store
	cfg KernelConfig
}

// ─── Reader methods ──────────────────────────────────────────────────────────

func (k *kernel) Get(ctx context.Context, id types.TaskID) (TaskState, error) {
	return k.s.Get(ctx, id)
}

func (k *kernel) ListReady(ctx context.Context, team types.TeamID, limit int) ([]TaskState, error) {
	return k.s.ListByStatus(ctx, team, types.StatusReady, limit)
}

func (k *kernel) ListChildren(ctx context.Context, parent types.TaskID) ([]TaskState, error) {
	return k.s.ListByParent(ctx, parent)
}

func (k *kernel) ListByStatus(ctx context.Context, team types.TeamID, st types.Status, limit int) ([]TaskState, error) {
	return k.s.ListByStatus(ctx, team, st, limit)
}

func (k *kernel) ListPendingDependentOn(ctx context.Context, depID types.TaskID) ([]TaskState, error) {
	return k.s.ListPendingDependentOn(ctx, depID)
}

// ─── Writer methods ──────────────────────────────────────────────────────────

// Admit inserts a new pending task derived directly from a TaskSpec.
func (k *kernel) Admit(ctx context.Context, sp spec.TaskSpec) (types.TaskID, error) {
	if sp.Goal == "" {
		return "", fmt.Errorf("kernel: Admit: Goal must not be empty")
	}
	id := k.cfg.IDGen()
	var deps []types.TaskID
	for _, d := range sp.Deps {
		deps = append(deps, types.TaskID(d))
	}
	ts := TaskState{
		ID:          id,
		TeamID:      "", // populated by caller if needed
		SessionID:   sp.SessionID,
		Kind:        sp.Hint.Kind,
		Status:      types.StatusPending,
		Priority:    sp.Priority,
		Deps:        deps,
		ResourceReq: sp.Resource,
		Budget:      sp.Budget,
		InputRef:    sp.InputRef,
		LeafSpec:    sp,
		CreatedAt:   time.Now(),
	}
	if err := k.s.Insert(ctx, ts); err != nil {
		return "", fmt.Errorf("kernel: Admit: %w", err)
	}
	return id, nil
}

// Derive inserts a child task under the given parent, checking spawn depth cap.
func (k *kernel) Derive(ctx context.Context, parent types.TaskID, sp spec.TaskSpec) (types.TaskID, error) {
	if sp.Goal == "" {
		return "", fmt.Errorf("kernel: Derive: Goal must not be empty")
	}
	var childID types.TaskID
	err := k.s.InTx(ctx, func(tx Tx) error {
		p, err := tx.Get(parent)
		if err != nil {
			return fmt.Errorf("kernel: Derive: parent not found: %w", err)
		}
		if p.SpawnDepth >= k.cfg.MaxSpawnDepth {
			return fmt.Errorf("kernel: Derive: spawn depth cap %d reached", k.cfg.MaxSpawnDepth)
		}
		var deps []types.TaskID
		for _, d := range sp.Deps {
			deps = append(deps, types.TaskID(d))
		}
		childID = k.cfg.IDGen()
		child := TaskState{
			ID:          childID,
			TeamID:      p.TeamID,
			SessionID:   sp.SessionID,
			ParentID:    parent,
			Status:      types.StatusPending,
			Priority:    sp.Priority,
			Deps:        deps,
			ResourceReq: sp.Resource,
			Budget:      sp.Budget,
			InputRef:    sp.InputRef,
			SpawnDepth:  p.SpawnDepth + 1,
			LeafSpec:    sp,
			CreatedAt:   time.Now(),
		}
		return tx.Insert(child)
	})
	if err != nil {
		return "", err
	}
	return childID, nil
}

// RollbackAdmit physically deletes a pending row — not a state machine transition.
func (k *kernel) RollbackAdmit(ctx context.Context, id types.TaskID) error {
	return k.s.Delete(ctx, id)
}

// Cancel transitions a non-terminal task to cancelling and cascades to children.
func (k *kernel) Cancel(ctx context.Context, id types.TaskID) error {
	return k.s.InTx(ctx, func(tx Tx) error {
		ts, err := tx.Get(id)
		if err != nil {
			return err
		}
		if ts.Status.IsTerminal() || ts.Status == types.StatusCancelling {
			return nil // already done / already cancelling
		}
		if err := tx.CAS(id, ts.Status, types.StatusCancelling, Mutation{}); err != nil {
			return err
		}
		// cascade to children
		children, err := tx.ListChildren(id)
		if err != nil {
			return err
		}
		for _, c := range children {
			if c.Status.IsTerminal() || c.Status == types.StatusCancelling {
				continue
			}
			if err := tx.CAS(c.ID, c.Status, types.StatusCancelling, Mutation{}); err != nil {
				return fmt.Errorf("kernel: Cancel: cascade child %s: %w", c.ID, err)
			}
		}
		return nil
	})
}

// MarkReady transitions pending→ready after verifying all deps are succeeded.
func (k *kernel) MarkReady(ctx context.Context, id types.TaskID) error {
	return k.s.InTx(ctx, func(tx Tx) error {
		ts, err := tx.Get(id)
		if err != nil {
			return err
		}
		// check all deps
		for _, depID := range ts.Deps {
			dep, err := tx.Get(depID)
			if err != nil {
				return fmt.Errorf("kernel: MarkReady: dep %s not found: %w", depID, err)
			}
			if dep.Status != types.StatusSucceeded {
				return fmt.Errorf("kernel: MarkReady: dep %s not succeeded (status=%s)", depID, dep.Status)
			}
		}
		return tx.CAS(id, types.StatusPending, types.StatusReady, Mutation{})
	})
}

// Claim transitions ready→running with worker identity and lease.
func (k *kernel) Claim(ctx context.Context, id types.TaskID, worker string, lease time.Duration, attempt int) error {
	ttl := lease
	if ttl == 0 {
		ttl = k.cfg.DefaultLeaseTTL
	}
	l := types.Lease{
		WorkerID:  worker,
		ExpiresAt: time.Now().Add(ttl),
	}
	return k.s.CAS(ctx, id, types.StatusReady, types.StatusRunning, Mutation{
		Lease:   &l,
		Attempt: &attempt,
	})
}

// RenewLease re-stamps the ExpiresAt for a running task; worker must match.
// Wrapped in InTx so the read-validate-write sequence is atomic — prevents
// TOCTOU against a different worker claiming the row after lease-expiry+retry.
func (k *kernel) RenewLease(ctx context.Context, id types.TaskID, worker string) error {
	return k.s.InTx(ctx, func(tx Tx) error {
		ts, err := tx.Get(id)
		if err != nil {
			return fmt.Errorf("kernel: RenewLease: %w", err)
		}
		if ts.Status != types.StatusRunning {
			return fmt.Errorf("kernel: RenewLease: not running (id=%s status=%s)", id, ts.Status)
		}
		if ts.Lease.WorkerID != worker {
			return fmt.Errorf("kernel: RenewLease: lease owner mismatch (id=%s want=%s got=%s)", id, worker, ts.Lease.WorkerID)
		}
		nl := types.Lease{
			WorkerID:  worker,
			ExpiresAt: time.Now().Add(k.cfg.DefaultLeaseTTL),
		}
		return tx.CAS(id, types.StatusRunning, types.StatusRunning, Mutation{Lease: &nl})
	})
}

// Park transitions running→waiting with the set of tasks being waited on.
func (k *kernel) Park(ctx context.Context, id types.TaskID, waitingFor []types.TaskID) error {
	return k.s.CAS(ctx, id, types.StatusRunning, types.StatusWaiting, Mutation{
		WaitingFor: waitingFor,
	})
}

// Resume transitions waiting→running after verifying all WaitingFor children are terminal.
func (k *kernel) Resume(ctx context.Context, id types.TaskID) error {
	return k.s.InTx(ctx, func(tx Tx) error {
		ts, err := tx.Get(id)
		if err != nil {
			return err
		}
		for _, cid := range ts.WaitingFor {
			c, err := tx.Get(cid)
			if err != nil {
				return fmt.Errorf("kernel: Resume: child %s not found: %w", cid, err)
			}
			if !c.Status.IsTerminal() {
				return fmt.Errorf("kernel: Resume: child %s not terminal (status=%s)", cid, c.Status)
			}
		}
		emptyWaiting := []types.TaskID{}
		return tx.CAS(id, types.StatusWaiting, types.StatusRunning, Mutation{
			WaitingFor: emptyWaiting,
		})
	})
}

// Succeed transitions running→succeeded with the result ref.
func (k *kernel) Succeed(ctx context.Context, id types.TaskID, ref types.Ref) error {
	return k.s.CAS(ctx, id, types.StatusRunning, types.StatusSucceeded, Mutation{
		ResultRef: &ref,
	})
}

// FailOrRetry transitions running→failed; if retryable and within MaxFailures, re-queues to ready.
func (k *kernel) FailOrRetry(ctx context.Context, id types.TaskID, reason types.FailureReason, errMsg string, attempt int) error {
	return k.s.InTx(ctx, func(tx Tx) error {
		ts, err := tx.Get(id)
		if err != nil {
			return err
		}
		// CAS running→failed
		if err := tx.CAS(id, types.StatusRunning, types.StatusFailed, Mutation{
			FailedReason: &reason,
			LastError:    &errMsg,
		}); err != nil {
			return err
		}
		// determine if we should retry
		maxFailures := ts.Budget.MaxFailures
		nextAttempt := ts.Attempt + 1
		if reason.Retryable() && maxFailures > 0 && nextAttempt < maxFailures {
			return tx.CAS(id, types.StatusFailed, types.StatusReady, Mutation{
				Attempt: &nextAttempt,
			})
		}
		return nil
	})
}

// Expire is a convenience wrapper for FailOrRetry triggered by the reaper.
func (k *kernel) Expire(ctx context.Context, id types.TaskID, reason types.FailureReason, attempt int) error {
	return k.FailOrRetry(ctx, id, reason, "expired by reaper", attempt)
}

// ConfirmCancelled finalises cancelling→cancelled after all children are terminal.
func (k *kernel) ConfirmCancelled(ctx context.Context, id types.TaskID) error {
	return k.s.InTx(ctx, func(tx Tx) error {
		children, err := tx.ListChildren(id)
		if err != nil {
			return err
		}
		for _, c := range children {
			if !c.Status.IsTerminal() {
				return fmt.Errorf("kernel: ConfirmCancelled: child %s not terminal (status=%s)", c.ID, c.Status)
			}
		}
		return tx.CAS(id, types.StatusCancelling, types.StatusCancelled, Mutation{})
	})
}

// ConfirmSucceededFromStaging promotes a running task to succeeded using an
// externally staged ref. The `attempt` parameter is an epoch guard (spec R3):
// rejects stale reaper confirms that arrive after a retry has incremented
// ts.Attempt. Wrapped in InTx so the read-validate-write is atomic.
func (k *kernel) ConfirmSucceededFromStaging(ctx context.Context, id types.TaskID, ref types.Ref, attempt int) error {
	return k.s.InTx(ctx, func(tx Tx) error {
		ts, err := tx.Get(id)
		if err != nil {
			return fmt.Errorf("kernel: ConfirmSucceededFromStaging: %w", err)
		}
		if ts.Attempt != attempt {
			return fmt.Errorf("kernel: ConfirmSucceededFromStaging: attempt mismatch (id=%s want=%d got=%d)", id, attempt, ts.Attempt)
		}
		return tx.CAS(id, types.StatusRunning, types.StatusSucceeded, Mutation{ResultRef: &ref})
	})
}

// ─── StagingWriter ───────────────────────────────────────────────────────────

type stagingWriter struct {
	s Store
}

// NewStagingWriter returns a StagingWriter that writes only StagedResultRef.
func NewStagingWriter(s Store) StagingWriter {
	return &stagingWriter{s: s}
}

func (sw *stagingWriter) StageResult(ctx context.Context, id types.TaskID, ref types.Ref, attempt int) error {
	return sw.s.UpdateField(ctx, id, FieldStagedResultRef, ref, attempt)
}
