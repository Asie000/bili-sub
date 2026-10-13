package filename

import (
	"strings"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	got := SanitizeFilename(`a/b:c*d?e"f<g>h|i`, "BV1xx")
	want := `a_b_c_d_e_f_g_h_i [BV1xx]`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSanitizeTruncate(t *testing.T) {
	long := ""
	for i := 0; i < 500; i++ {
		long += "字"
	}
	got := SanitizeFilename(long, "BV1xx")
	if len([]rune(got)) > 130 {
		t.Fatalf("too long: %d", len([]rune(got)))
	}
	if !strings.HasSuffix(got, " [BV1xx]") {
		t.Fatalf("must keep bvid suffix: %q", got)
	}
}

func TestSanitizeStripsControlChars(t *testing.T) {
	got := SanitizeFilename("a\x00b\x0bc", "BV1")
	want := "abc [BV1]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSanitizeSubtitleName(t *testing.T) {
	tests := []struct {
		name          string
		author, title string
		bvid          string
		want          string
	}{
		{"normal", "某UP主", "标题", "BV1xx", "某UP主 - 标题 [BV1xx]"},
		{"empty author", "", "标题", "BV1xx", "标题 [BV1xx]"},
		{"blank author", "   ", "标题", "BV1xx", "标题 [BV1xx]"},
		{"empty title", "某UP主", "", "BV1xx", "某UP主 - [BV1xx]"},
		{"illegal chars", `a/b:c*d?e"f<g>h|i`, "标:题", "BV1", `a_b_c_d_e_f_g_h_i - 标_题 [BV1]`},
		{"strips control chars", "UP\x00主", "标\x0b题", "BV1", "UP主 - 标题 [BV1]"},
		{"bvid absent", "某UP主", "标题", "", "某UP主 - 标题"},
		{"all empty", "", "", "", "untitled"},
		{"empty falls back with bvid", "", "", "BV1xx", "untitled [BV1xx]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeSubtitleName(tt.author, tt.title, tt.bvid); got != tt.want {
				t.Fatalf("SanitizeSubtitleName(%q, %q, %q) = %q, want %q", tt.author, tt.title, tt.bvid, got, tt.want)
			}
		})
	}
}

// TestSanitizeSubtitleNameTruncatesKeepsSuffix pins that the 100-rune cap
// applies to the combined "author - title" part only: the bvid bracket is
// appended afterwards and is never truncated away.
func TestSanitizeSubtitleNameTruncatesKeepsSuffix(t *testing.T) {
	long := strings.Repeat("字", 500)
	got := SanitizeSubtitleName(long, long, "BV1xx")
	if !strings.HasSuffix(got, " [BV1xx]") {
		t.Fatalf("must keep bvid suffix: %q", got)
	}
	if n := len([]rune(got)); n > 110 {
		t.Fatalf("too long: %d runes (%q)", n, got)
	}
	if n := len([]rune(got)); n != 100+len(" [BV1xx]") {
		t.Fatalf("author - title part not capped at 100 runes: %d", n)
	}
}
