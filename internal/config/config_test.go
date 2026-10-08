package config

import "testing"

func TestDefault(t *testing.T) {
	c := Default()
	if c.Concurrency <= 0 || c.Concurrency > 5 {
		t.Fatalf("bad concurrency: %d", c.Concurrency)
	}
	if c.QPS <= 0 || c.QPS > 5 {
		t.Fatalf("bad qps: %v", c.QPS)
	}
	if c.SESSDATA != "" {
		t.Fatalf("default sessdata should be empty")
	}
}
