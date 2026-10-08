package ir

import "testing"

func TestPlainText(t *testing.T) {
	d := &Document{Segments: []Segment{
		{StartMs: 0, EndMs: 1000, Text: "你好"},
		{StartMs: 1000, EndMs: 2000, Text: "世界"},
	}}
	if got := d.PlainText(); got != "你好\n世界" {
		t.Fatalf("got %q", got)
	}
	if d.DurationMs() != 2000 {
		t.Fatalf("duration=%d", d.DurationMs())
	}
}
