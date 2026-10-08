package bili

import (
	"context"
	"math/rand"
	"time"

	"golang.org/x/time/rate"
)

type rateLimiter struct {
	lim *rate.Limiter
}

func NewRateLimiter(qps float64) Limiter {
	if qps <= 0 {
		qps = 2
	}
	return &rateLimiter{lim: rate.NewLimiter(rate.Limit(qps), 1)}
}

func (r *rateLimiter) Wait(ctx context.Context) error {
	if err := r.lim.Wait(ctx); err != nil {
		return err
	}
	// 抖动，避免节奏过于规律被风控
	jitter := time.Duration(rand.Int63n(int64(200 * time.Millisecond)))
	select {
	case <-time.After(jitter):
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}
