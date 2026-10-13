package asr

import (
	"testing"

	"bilibili-sub/internal/ir"
)

// seg is a one-segment transcript with the default 0-1000ms timing.
func seg(text string) ir.Segment {
	return ir.Segment{StartMs: 0, EndMs: 1000, Text: text}
}

func texts(segments []ir.Segment) []string {
	out := make([]string, 0, len(segments))
	for _, s := range segments {
		out = append(out, s.Text)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCleanSegmentsStripsWhisperTokens pins that whisper's special tokens --
// which leak into the text when timestamp decoding falls back to raw tokens --
// never reach the subtitle file.
func TestCleanSegmentsStripsWhisperTokens(t *testing.T) {
	got := CleanSegments([]ir.Segment{
		seg("<|0.00|>你好<|2.00|>"),
		seg("<|startoftranscript|><|zh|>世界"),
	}, CleanupOptions{Language: "zh", Simplify: true})

	if want := []string{"你好", "世界"}; !equalStrings(texts(got), want) {
		t.Errorf("texts = %q, want %q", texts(got), want)
	}
}

// TestCleanSegmentsDropsGarbledAndEmptySegments pins the two shapes that must
// not become subtitle lines: text the model could not decode, and lines that
// are only whitespace.
func TestCleanSegmentsDropsGarbledAndEmptySegments(t *testing.T) {
	got := CleanSegments([]ir.Segment{
		seg("\uFFFD\uFFFD\uFFFD\uFFFD"),
		seg("   "),
		seg(""),
		seg("\t\n"),
		seg("正常"),
	}, CleanupOptions{Language: "zh"})

	if want := []string{"正常"}; !equalStrings(texts(got), want) {
		t.Errorf("texts = %q, want %q", texts(got), want)
	}
}

// TestCleanSegmentsKeepsSparseReplacementChars pins that a single replacement
// character is cleaned away rather than costing the whole line.
func TestCleanSegmentsKeepsSparseReplacementChars(t *testing.T) {
	got := CleanSegments([]ir.Segment{seg("你\uFFFD好")}, CleanupOptions{Language: "zh"})

	if want := []string{"你好"}; !equalStrings(texts(got), want) {
		t.Errorf("texts = %q, want %q", texts(got), want)
	}
}

// TestCleanSegmentsRemovesSpacesBetweenChinese pins the whitespace rule: a space
// whisper inserted between two Chinese characters is an artefact and goes, while
// a space inside English text is real and stays.
func TestCleanSegmentsRemovesSpacesBetweenChinese(t *testing.T) {
	got := CleanSegments([]ir.Segment{
		seg("你好 世界"),
		seg("hello world"),
		seg("用 Go 写"),
	}, CleanupOptions{Language: "zh", Simplify: true})

	want := []string{"你好世界", "hello world", "用 Go 写"}
	if !equalStrings(texts(got), want) {
		t.Errorf("texts = %q, want %q", texts(got), want)
	}
}

// TestCleanSegmentsFoldsTraditionalToSimplified pins the conversion and its two
// escape hatches: it is off unless asked for, and it does not touch a language
// that is not Chinese.
func TestCleanSegmentsFoldsTraditionalToSimplified(t *testing.T) {
	t.Run("asked for and Chinese", func(t *testing.T) {
		got := CleanSegments([]ir.Segment{seg("鐵飯碗與無線網絡")}, CleanupOptions{Language: "zh", Simplify: true})
		if want := []string{"铁饭碗与无线网络"}; !equalStrings(texts(got), want) {
			t.Errorf("texts = %q, want %q", texts(got), want)
		}
	})

	t.Run("not asked for", func(t *testing.T) {
		got := CleanSegments([]ir.Segment{seg("鐵飯碗")}, CleanupOptions{Language: "zh"})
		if want := []string{"鐵飯碗"}; !equalStrings(texts(got), want) {
			t.Errorf("texts = %q, want %q", texts(got), want)
		}
	})

	t.Run("not Chinese", func(t *testing.T) {
		got := CleanSegments([]ir.Segment{seg("無線")}, CleanupOptions{Language: "ja", Simplify: true})
		if want := []string{"無線"}; !equalStrings(texts(got), want) {
			t.Errorf("texts = %q, want %q", texts(got), want)
		}
	})

	t.Run("traditional-only characters survive", func(t *testing.T) {
		// 著/表/里/面/台 are valid simplified characters too, so a naive
		// character table must leave them alone.
		got := CleanSegments([]ir.Segment{seg("著名表示里面台")}, CleanupOptions{Language: "zh", Simplify: true})
		if want := []string{"著名表示里面台"}; !equalStrings(texts(got), want) {
			t.Errorf("texts = %q, want %q", texts(got), want)
		}
	})
}

// TestCleanSegmentsCollapsesRepeatedSegments pins the hallucination rule: whisper
// can get stuck on silence or music and emit the same line dozens of times.
func TestCleanSegmentsCollapsesRepeatedSegments(t *testing.T) {
	repeated := []ir.Segment{
		{StartMs: 0, EndMs: 100, Text: "请点赞投币收藏"},
		{StartMs: 100, EndMs: 200, Text: "请点赞投币收藏。"},
		{StartMs: 200, EndMs: 300, Text: "请点赞投币 收藏"},
		{StartMs: 300, EndMs: 400, Text: "谢谢大家"},
	}
	got := CleanSegments(repeated, CleanupOptions{Language: "zh", Simplify: true})

	if want := []string{"请点赞投币收藏", "谢谢大家"}; !equalStrings(texts(got), want) {
		t.Errorf("texts = %q, want %q", texts(got), want)
	}
	if got[0].StartMs != 0 || got[0].EndMs != 100 {
		t.Errorf("kept timing = %d-%d, want the first occurrence's 0-100", got[0].StartMs, got[0].EndMs)
	}
}

// TestCleanSegmentsKeepsNonConsecutiveRepeats pins that only a consecutive run is
// collapsed: a phrase that genuinely comes back later is not a hallucination.
func TestCleanSegmentsKeepsNonConsecutiveRepeats(t *testing.T) {
	got := CleanSegments([]ir.Segment{
		seg("好我们继续"),
		seg("中间还有别的内容"),
		seg("好我们继续"),
	}, CleanupOptions{Language: "zh"})

	if want := []string{"好我们继续", "中间还有别的内容", "好我们继续"}; !equalStrings(texts(got), want) {
		t.Errorf("texts = %q, want %q", texts(got), want)
	}
}

// TestCleanSegmentsKeepsShortInterjections pins the guard on the dedup rule: a
// one-character line repeated is conversation, not a hallucination loop.
func TestCleanSegmentsKeepsShortInterjections(t *testing.T) {
	got := CleanSegments([]ir.Segment{seg("嗯"), seg("嗯"), seg("对")}, CleanupOptions{Language: "zh"})

	if want := []string{"嗯", "嗯", "对"}; !equalStrings(texts(got), want) {
		t.Errorf("texts = %q, want %q", texts(got), want)
	}
}

// TestCleanSegmentsEmptyInput pins the trivial case does not panic or allocate a
// surprise.
func TestCleanSegmentsEmptyInput(t *testing.T) {
	if got := CleanSegments(nil, CleanupOptions{Language: "zh"}); len(got) != 0 {
		t.Errorf("CleanSegments(nil) = %v, want empty", got)
	}
}
