package middleware

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"harnessclaw-go/pkg/errors"
	"harnessclaw-go/pkg/types"
)

// RateLimit creates a per-user rate limiting middleware.
// maxRequests is the maximum number of requests allowed within the window.
// Expired buckets are lazily cleaned every cleanInterval requests.
func RateLimit(maxRequests int, window time.Duration) Middleware {
	var mu sync.Mutex
	counters := make(map[string]*rateBucket)
	var requestCount atomic.Int64
	const cleanInterval = 100

	return func(next Handler) Handler {
		return func(ctx context.Context, msg *types.IncomingMessage) error {
			mu.Lock()

			// Lazy cleanup: every cleanInterval requests, purge expired buckets.
			if requestCount.Add(1)%cleanInterval == 0 {
				now := time.Now()
				for k, b := range counters {
					if now.Sub(b.windowStart) > window {
						delete(counters, k)
					}
				}
			}

			bucket, ok := counters[msg.UserID]
			if !ok || time.Since(bucket.windowStart) > window {
				bucket = &rateBucket{windowStart: time.Now()}
				counters[msg.UserID] = bucket
			}
			bucket.count++
			count := bucket.count
			mu.Unlock()

			if count > maxRequests {
				return errors.New(errors.CodeRateLimit, "rate limit exceeded")
			}

			return next(ctx, msg)
		}
	}
}

type rateBucket struct {
	windowStart time.Time
	count       int
}
