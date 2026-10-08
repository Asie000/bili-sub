package bili

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiterDelays(t *testing.T) {
	l := NewRateLimiter(50) // 20ms/次
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("too fast: %v", elapsed)
	}
}
