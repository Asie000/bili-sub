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

func TestPlainTextAndDurationMsEdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		segments     []Segment
		wantText     string
		wantDuration int64
	}{
		{
			name: "blank and whitespace-only middle segments are dropped",
			segments: []Segment{
				{EndMs: 1000, Text: "你好"},
				{EndMs: 2000, Text: ""},
				{EndMs: 3000, Text: "   \t "},
				{EndMs: 4000, Text: "世界"},
			},
			wantText:     "你好\n世界",
			wantDuration: 4000,
		},
		{
			name: "leading and trailing blank segments are dropped",
			segments: []Segment{
				{EndMs: 1000, Text: "  "},
				{EndMs: 2000, Text: "正文"},
				{EndMs: 3000, Text: "\n\t"},
			},
			wantText:     "正文",
			wantDuration: 3000,
		},
		{
			name:         "empty segments",
			segments:     []Segment{},
			wantText:     "",
			wantDuration: 0,
		},
		{
			name:         "nil segments",
			segments:     nil,
			wantText:     "",
			wantDuration: 0,
		},
		{
			name: "unsorted EndMs returns the maximum",
			segments: []Segment{
				{EndMs: 3000, Text: "一"},
				{EndMs: 9000, Text: "二"},
				{EndMs: 1500, Text: "三"},
			},
			wantText:     "一\n二\n三",
			wantDuration: 9000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Document{Segments: tt.segments}
			if got := d.PlainText(); got != tt.wantText {
				t.Errorf("PlainText() = %q, want %q", got, tt.wantText)
			}
			if got := d.DurationMs(); got != tt.wantDuration {
				t.Errorf("DurationMs() = %d, want %d", got, tt.wantDuration)
			}
		})
	}
}
