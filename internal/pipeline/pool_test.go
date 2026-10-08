package pipeline

import (
	"context"
	"testing"
	"time"
)

func TestRunConcurrent(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	res := Run(context.Background(), items, 3, func(ctx context.Context, n int) (int, error) {
		return n * 2, nil
	})
	if len(res) != 5 {
		t.Fatalf("len=%d", len(res))
	}
	sum := 0
	for _, r := range res {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		sum += r.Value
	}
	if sum != 30 {
		t.Fatalf("sum=%d", sum)
	}
}

func TestRunCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	items := []int{1, 2, 3}
	res := Run(ctx, items, 2, func(ctx context.Context, n int) (int, error) {
		return n, ctx.Err()
	})
	for _, r := range res {
		if r.Err == nil {
			t.Fatal("expected context error")
		}
	}
}

func TestRunKeepsInputOrder(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	res := Run(context.Background(), items, 5, func(ctx context.Context, n int) (int, error) {
		// Earlier items finish last, so completion order is the reverse of input
		// order; the returned slice must still be indexed by input position.
		time.Sleep(time.Duration(len(items)-n) * 20 * time.Millisecond)
		return n * n, nil
	})
	if len(res) != len(items) {
		t.Fatalf("len=%d", len(res))
	}
	for i, r := range res {
		if r.Item != i {
			t.Fatalf("res[%d].Item=%d", i, r.Item)
		}
		if r.Err != nil {
			t.Fatalf("res[%d]=%v", i, r.Err)
		}
		if want := items[i] * items[i]; r.Value != want {
			t.Fatalf("res[%d].Value=%d want %d", i, r.Value, want)
		}
	}
}

func TestRunDefaultsWorkers(t *testing.T) {
	for _, workers := range []int{0, -1} {
		items := []int{1, 2, 3}
		res := Run(context.Background(), items, workers, func(ctx context.Context, n int) (int, error) {
			return n + 1, nil
		})
		if len(res) != len(items) {
			t.Fatalf("workers=%d len=%d", workers, len(res))
		}
		for i, r := range res {
			if r.Err != nil {
				t.Fatalf("workers=%d res[%d]=%v", workers, i, r.Err)
			}
			if want := items[i] + 1; r.Value != want {
				t.Fatalf("workers=%d res[%d].Value=%d want %d", workers, i, r.Value, want)
			}
		}
	}
}

func TestRunStopsTakingItemsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	items := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	var calls int
	res := Run(ctx, items, 1, func(ctx context.Context, n int) (int, error) {
		calls++
		cancel()
		return n, nil
	})
	if calls != 1 {
		t.Fatalf("fn called %d times after cancel, want 1", calls)
	}
	if res[0].Err != nil {
		t.Fatalf("res[0]=%v", res[0].Err)
	}
	for i := 1; i < len(res); i++ {
		if res[i].Err == nil {
			t.Fatalf("res[%d].Err=nil, want context error", i)
		}
	}
}

func TestRunEmpty(t *testing.T) {
	res := Run(context.Background(), []int{}, 4, func(ctx context.Context, n int) (int, error) {
		return n, nil
	})
	if len(res) != 0 {
		t.Fatalf("len=%d", len(res))
	}
}
