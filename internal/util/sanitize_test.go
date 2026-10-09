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

func TestSanitizeDirNameCleansIllegalChars(t *testing.T) {
	got := SanitizeDirName(`a/b:c*d?e"f<g>h|i`)
	want := `a_b_c_d_e_f_g_h_i`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSanitizeDirNameHasNoBvidSuffix(t *testing.T) {
	if got := SanitizeDirName("某UP主"); got != "某UP主" {
		t.Fatalf("got %q, want the plain author name", got)
	}
}

func TestSanitizeDirNameEmptyFallsBack(t *testing.T) {
	for _, in := range []string{"", "   ", "\x00\x0b"} {
		if got := SanitizeDirName(in); got != "未知UP主" {
			t.Errorf("SanitizeDirName(%q) = %q, want 未知UP主", in, got)
		}
	}
}

func TestSanitizeDirNameTruncates(t *testing.T) {
	long := ""
	for i := 0; i < 500; i++ {
		long += "字"
	}
	got := SanitizeDirName(long)
	if n := len([]rune(got)); n != 80 {
		t.Fatalf("len = %d runes, want 80", n)
	}
}
