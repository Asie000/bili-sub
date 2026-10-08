package util

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
