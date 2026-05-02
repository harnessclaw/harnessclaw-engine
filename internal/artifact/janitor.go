package artifact

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Janitor periodically calls Store.PurgeExpired so memory and SQLite don't
// grow unboundedly on a long-running server. Doc §9 — three-layer TTL means
// even "intermediate" artifacts must time out without manual cleanup.
type Janitor struct {
	store    Store
	interval time.Duration
	logger   *zap.Logger
}

// NewJanitor wires a Store + interval. Pass interval=0 to use the default
// of 10 minutes; intervals shorter than 1 minute are clamped to avoid
// hammering the DB on misconfiguration.
func NewJanitor(store Store, interval time.Duration, logger *zap.Logger) *Janitor {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	if interval < 1*time.Minute {
		interval = 1 * time.Minute
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Janitor{
		store:    store,
		interval: interval,
		logger:   logger.Named("artifact_janitor"),
	}
}

// Run blocks until ctx is cancelled, purging on each tick. Spawn it in a
// goroutine from the server's startup sequence and cancel via the same
// context that gates the rest of shutdown.
func (j *Janitor) Run(ctx context.Context) {
	t := time.NewTicker(j.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			n, err := j.store.PurgeExpired(ctx, now.UTC())
			if err != nil {
				j.logger.Warn("purge failed", zap.Error(err))
				continue
			}
			if n > 0 {
				j.logger.Info("purged expired artifacts", zap.Int("count", n))
			}
		}
	}
}
