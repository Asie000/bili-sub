package export

import (
	"strings"
	"testing"

	"bilibili-sub/internal/ir"
)

func sample() *ir.Document {
	return &ir.Document{Bvid: "BV1xx", Title: "t", Segments: []ir.Segment{
		{StartMs: 0, EndMs: 1500, Text: "你好"},
		{StartMs: 1500, EndMs: 2500, Text: "世界"},
	}}
}

func TestSRT(t *testing.T) {
	b, err := Render(sample(), FormatSRT)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "00:00:00,000 --> 00:00:01,500") {
		t.Fatalf("bad srt: %s", s)
	}
}

func TestTxtHasNoTimeline(t *testing.T) {
	b, _ := Render(sample(), FormatText)
	s := string(b)
	if strings.Contains(s, "-->") || strings.Contains(s, "00:00") {
		t.Fatalf("txt must not contain timeline: %s", s)
	}
	if s != "你好\n世界" {
		t.Fatalf("got %q", s)
	}
}

func TestMD(t *testing.T) {
	b, _ := Render(sample(), FormatMarkdown)
	if !strings.HasPrefix(string(b), "# ") {
		t.Fatalf("md should start with title: %s", b)
	}
}

func TestUnknownFormat(t *testing.T) {
	if _, err := Render(sample(), "ass"); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}
