package cli

import (
	"context"
	"errors"
	"sync"

	"bilibili-sub/internal/bili"
)

// sessionExpired reports whether err is bilibili's API code -101 (账号未登录 /
// 登录态失效). Per spec §八 that is the one error that says nothing about the
// video being worked on -- every remaining row would fail identically -- so the
// whole leg stops and warns instead of marking each row failed. -400 stays a
// failure of its own request.
func sessionExpired(err error) bool {
	var apiErr *bili.APIError
	return errors.As(err, &apiErr) && apiErr.SessionExpired()
}

// riskControl reports whether err is bilibili's HTTP 412/429 risk-control block.
// Like an expired session it is about the session rather than one video: every
// later request is likely blocked the same way, so the whole leg stops and warns.
func riskControl(err error) bool {
	return errors.Is(err, bili.ErrRiskControl)
}

// legAbort lets the first worker that sees a whole-leg failure cancel the rest
// of a run and remember the error for the caller. fatal decides which errors are
// whole-leg (an expired session, an unsupported language); workers run
// concurrently, so the once keeps the first hit authoritative and the cancel
// idempotent.
type legAbort struct {
	once   sync.Once
	err    error
	cancel context.CancelFunc
	fatal  func(error) bool
}

func newLegAbort(cancel context.CancelFunc, fatal func(error) bool) *legAbort {
	return &legAbort{cancel: cancel, fatal: fatal}
}

// hit records err and aborts the run when err is a whole-leg failure. It reports
// whether err was that fatal case, so the caller can skip the per-video failure
// bookkeeping (SetState(failed)) for it.
func (a *legAbort) hit(err error) bool {
	if err == nil || a.fatal == nil || !a.fatal(err) {
		return false
	}
	a.once.Do(func() {
		a.err = err
		if a.cancel != nil {
			a.cancel()
		}
	})
	return true
}

// aborted returns the error that stopped the run, or nil.
func (a *legAbort) aborted() error { return a.err }
