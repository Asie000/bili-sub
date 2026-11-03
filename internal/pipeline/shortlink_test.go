package pipeline

import (
	"context"
	"testing"
)

// TestIsShortLink pins which inputs are treated as b23.tv links. The host match
// must not be fooled by a lookalike domain: "b23.tv.evil.com" starts with
// "b23.tv" but is a different host, so it must not be resolved.
func TestIsShortLink(t *testing.T) {
	short := []string{
		"https://b23.tv/aBcDeF",
		"http://b23.tv/aBcDeF",
		"b23.tv/aBcDeF",
		"  https://b23.tv/xyz?share=1  ",
	}
	for _, in := range short {
		if !IsShortLink(in) {
			t.Errorf("IsShortLink(%q) = false, want true", in)
		}
	}
	notShort := []string{
		"https://www.bilibili.com/video/BV1uv411q7Mv",
		"https://space.bilibili.com/12345",
		"BV1uv411q7Mv",
		"https://b23.tv.evil.com/x",
		"b23.tvaBcDeF",
		"",
	}
	for _, in := range notShort {
		if IsShortLink(in) {
			t.Errorf("IsShortLink(%q) = true, want false", in)
		}
	}
}

// TestResolveShortLinkPassthrough pins that a non-short URL is returned verbatim
// and without touching the client, so every input can be run through this
// unconditionally. The nil client makes that explicit: a passthrough must not
// dereference it.
func TestResolveShortLinkPassthrough(t *testing.T) {
	const in = "https://www.bilibili.com/video/BV1uv411q7Mv"
	got, err := ResolveShortLink(context.Background(), nil, in)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Fatalf("ResolveShortLink = %q, want the input unchanged", got)
	}
}
