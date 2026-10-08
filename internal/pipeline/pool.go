package pipeline

import (
	"context"
	"sync"
)

// Result is the outcome of running Fn on one input. Item is the index of that
// input in the slice passed to Run, so a caller can pair Value/Err with the
// original item even when the work finishes out of order.
type Result[R any] struct {
	Item  int
	Value R
	Err   error
}

// Pool fans items out over a fixed number of workers running Fn and collects
// one Result per input.
type Pool[T any, R any] struct {
	// Workers is the number of goroutines running Fn concurrently. A value <= 0
	// means 1.
	Workers int
	// Fn does the work for one item. It must honour ctx cancellation.
	Fn func(context.Context, T) (R, error)
}

// Run applies Fn to every item with at most Workers goroutines in flight and
// returns one Result per input, in input order (Result.Item == the input's
// index). It always returns len(items) results: an item a worker never picked
// up because ctx was cancelled comes back with ctx.Err() instead of a Value.
func (p Pool[T, R]) Run(ctx context.Context, items []T) []Result[R] {
	if len(items) == 0 {
		return []Result[R]{}
	}
	workers := p.Workers
	if workers <= 0 {
		workers = 1
	}
	if workers > len(items) {
		workers = len(items)
	}

	results := make([]Result[R], len(items))
	// filled[i] is written only by the worker that owns index i, and read only
	// after wg.Wait, so no extra locking is needed around results.
	filled := make([]bool, len(items))
	indices := make(chan int)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case i, ok := <-indices:
					if !ok {
						return
					}
					// The select above can still hand out a queued index after
					// cancellation, so re-check before starting new work.
					if ctx.Err() != nil {
						return
					}
					value, err := p.Fn(ctx, items[i])
					results[i] = Result[R]{Item: i, Value: value, Err: err}
					filled[i] = true
				}
			}
		}()
	}

	// The dispatcher stops feeding indices as soon as ctx is cancelled and
	// closes the channel, which ends any worker blocked on receive.
	go func() {
		defer close(indices)
		for i := range items {
			select {
			case <-ctx.Done():
				return
			case indices <- i:
			}
		}
	}()

	wg.Wait()
	// Anything left unfilled was skipped because of cancellation, which is also
	// why ctx.Err() is non-nil here.
	for i := range results {
		if !filled[i] {
			results[i] = Result[R]{Item: i, Err: ctx.Err()}
		}
	}
	return results
}

// Run is the one-shot form of Pool: it applies fn to every item with the given
// worker count and returns the results in input order.
func Run[T any, R any](ctx context.Context, items []T, workers int, fn func(context.Context, T) (R, error)) []Result[R] {
	return Pool[T, R]{Workers: workers, Fn: fn}.Run(ctx, items)
}
