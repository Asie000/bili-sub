package pipeline

import (
	"context"
	"os"
	"testing"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/store"
)

// TestLiveAudioFetch is an opt-in check against the real bilibili API: it
// proves the WBI signature, the playurl flags and the CDN headers work outside
// the fake servers. It is skipped unless the environment supplies BOTH a video
// and a session file:
//
//	BILI_LIVE_BVID     a real video id
//	BILI_LIVE_SESSION  path to a session.json written by `bili-sub login`
//
// The session is required because playurl is login-gated: with only the video id
// set, the call answers -101 and this test would fail rather than skip, turning
// a half-configured environment into a red suite.
func TestLiveAudioFetch(t *testing.T) {
	bvid := os.Getenv("BILI_LIVE_BVID")
	sessionPath := os.Getenv("BILI_LIVE_SESSION")
	if bvid == "" || sessionPath == "" {
		t.Skip("set BILI_LIVE_BVID and BILI_LIVE_SESSION to hit the real bilibili API")
	}
	c := bili.NewClient("", bili.NewRateLimiter(1))
	sessdata, cookie, err := bili.LoadSession(sessionPath)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	c.SESSDATA = sessdata
	c.Cookie = cookie
	t.Logf("session loaded: sessdata=%t cookie=%t", sessdata != "", cookie != "")
	en := NewEnumerator(c)
	ctx := context.Background()

	info, err := en.ResolveVideo(ctx, bvid)
	if err != nil {
		t.Fatalf("resolve %s: %v", bvid, err)
	}
	t.Logf("video: %s cid=%d title=%q", info.Bvid, info.Cid, info.Title)

	dir := t.TempDir()
	af := NewAudioFetcher(c, en, dir)
	path, err := af.Fetch(ctx, store.Video{Bvid: info.Bvid, Cid: info.Cid})
	if err != nil {
		t.Fatalf("fetch audio: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("audio: %s (%d bytes)", path, st.Size())
	if st.Size() < 100*1024 {
		t.Errorf("audio is only %d bytes, suspiciously small", st.Size())
	}
}
