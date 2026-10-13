package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/store"
)

// fakeTrack is one dash audio entry of a fake playurl response.
type fakeTrack struct {
	path      string
	bandwidth int
	backup    []string
}

// dashPlayurl builds a playurl response carrying the given tracks. A zero
// bandwidth means the field is absent from the JSON.
func dashPlayurl(cdnBase string, tracks ...fakeTrack) string {
	entries := make([]string, 0, len(tracks))
	for _, tr := range tracks {
		bw := ""
		if tr.bandwidth > 0 {
			bw = fmt.Sprintf(`"bandwidth":%d,`, tr.bandwidth)
		}
		backupField := ""
		if len(tr.backup) > 0 {
			urls := make([]string, 0, len(tr.backup))
			for _, b := range tr.backup {
				urls = append(urls, fmt.Sprintf("%q", cdnBase+b))
			}
			backupField = `,"backupUrl":[` + strings.Join(urls, ",") + `]`
		}
		entries = append(entries, fmt.Sprintf(
			`{"baseUrl":%q,%s"mimeType":"audio/mp4","codecs":"mp4a.40.2"%s}`,
			cdnBase+tr.path, bw, backupField))
	}
	return `{"code":0,"message":"0","data":{"dash":{"audio":[` + strings.Join(entries, ",") + `],"duration":60}}}`
}

const fakeFingerprintBody = `{"code":0,"message":"0","data":{"b_3":"B3VALUE","b_4":"B4VALUE"}}`

const fakeNavBody = `{"code":0,"message":"0","data":{"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png","sub_url":"https://i0.hdslb.com/bfs/wbi/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png"}}}`

// fakeBiliAPI is a fake fingerprint/nav/playurl server. The playurl handler
// records the query it received, which is how a test pins the WBI signature and
// the dash flags.
type fakeBiliAPI struct {
	server    *httptest.Server
	playBody  string
	playQuery map[string]string
	mu        sync.Mutex
}

func newFakeBiliAPI(t *testing.T, playBody string) *fakeBiliAPI {
	t.Helper()
	f := &fakeBiliAPI{playBody: playBody}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/frontend/finger/spi":
			_, _ = io.WriteString(w, fakeFingerprintBody)
		case "/x/web-interface/nav":
			_, _ = io.WriteString(w, fakeNavBody)
		case "/x/player/wbi/playurl":
			f.mu.Lock()
			f.playQuery = map[string]string{}
			for k, v := range r.URL.Query() {
				f.playQuery[k] = v[0]
			}
			f.mu.Unlock()
			_, _ = io.WriteString(w, f.playBody)
		default:
			t.Errorf("unexpected API request path %q", r.URL.Path)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeBiliAPI) query() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.playQuery
}

// fetcher builds an AudioFetcher pointed at this fake API.
func (f *fakeBiliAPI) fetcher(t *testing.T, outputDir string) *AudioFetcher {
	t.Helper()
	c := bili.NewClient("", nopLimiter{})
	c.FingerprintURL = f.server.URL + "/x/frontend/finger/spi"
	en := NewEnumerator(c)
	en.NavURL = f.server.URL + "/x/web-interface/nav"
	af := NewAudioFetcher(c, en, outputDir)
	af.PlayURLBaseURL = f.server.URL + "/x/player/wbi/playurl"
	return af
}

func TestNewAudioFetcherDefaults(t *testing.T) {
	f := NewAudioFetcher(bili.NewClient("", nopLimiter{}), nil, "out")
	if f.PlayURLBaseURL != "https://api.bilibili.com/x/player/wbi/playurl" {
		t.Fatalf("PlayURLBaseURL=%s", f.PlayURLBaseURL)
	}
	if f.OutputDir != "out" {
		t.Fatalf("OutputDir=%s", f.OutputDir)
	}
	if f.HTTP == nil || f.HTTP.Timeout != 10*time.Minute {
		t.Fatalf("HTTP=%+v want a 10m client", f.HTTP)
	}
}

// TestAudioFetcherPicksSmallestTrack pins the selection rule: the lowest
// bandwidth track is enough for speech, so the fetcher must not spend time
// downloading a higher-bitrate one.
func TestAudioFetcherPicksSmallestTrack(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "audio:%s", r.URL.Path)
	}))
	defer cdn.Close()

	api := newFakeBiliAPI(t, dashPlayurl(cdn.URL,
		fakeTrack{path: "/audio/high.m4s", bandwidth: 30280},
		fakeTrack{path: "/audio/low.m4s", bandwidth: 30216},
	))
	f := api.fetcher(t, t.TempDir())

	path, err := f.Fetch(context.Background(), store.Video{Bvid: "BV1uv411q7Mv", Cid: 7, Aid: 3})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if filepath.Ext(path) != ".m4s" {
		t.Errorf("path = %q, want a .m4s file", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audio: %v", err)
	}
	if string(got) != "audio:/audio/low.m4s" {
		t.Errorf("audio body = %q, want the low-bandwidth track", got)
	}
}

// TestAudioFetcherSignsPlayURLAndHidesCookie pins the two request-shape rules:
// the playurl call is WBI-signed with fnval=16, and the audio CDN call carries
// the referer it requires but never the login cookie.
func TestAudioFetcherSignsPlayURLAndHidesCookie(t *testing.T) {
	const bvid = "BV1uv411q7Mv"

	var (
		mu        sync.Mutex
		cdnCookie string
		cdnRef    string
		cdnUA     string
	)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cdnCookie = r.Header.Get("Cookie")
		cdnRef = r.Header.Get("Referer")
		cdnUA = r.Header.Get("User-Agent")
		mu.Unlock()
		_, _ = io.WriteString(w, "audio-bytes")
	}))
	defer cdn.Close()

	api := newFakeBiliAPI(t, dashPlayurl(cdn.URL, fakeTrack{path: "/audio/one.m4s", bandwidth: 30216}))
	f := api.fetcher(t, t.TempDir())
	// The API client holds a login cookie; it must reach the API but not the CDN.
	f.Client.Cookie = "SESSDATA=secret-sessdata; buvid3=B3VALUE"

	if _, err := f.Fetch(context.Background(), store.Video{Bvid: bvid, Cid: 11, Aid: 5}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	q := api.query()
	if q["fnval"] != "16" {
		t.Errorf("fnval = %q, want 16 (dash)", q["fnval"])
	}
	if q["bvid"] != bvid || q["cid"] != "11" {
		t.Errorf("playurl params = %v, want bvid/cid to identify the video", q)
	}
	if q["w_rid"] == "" || q["wts"] == "" {
		t.Errorf("playurl params = %v, want a WBI signature", q)
	}

	mu.Lock()
	defer mu.Unlock()
	if cdnCookie != "" {
		t.Errorf("CDN request Cookie = %q, want none (SESSDATA must not leave the API host)", cdnCookie)
	}
	if cdnRef != "https://www.bilibili.com" {
		t.Errorf("CDN Referer = %q, want the bilibili referer the CDN requires", cdnRef)
	}
	if cdnUA == "" {
		t.Error("CDN request has no User-Agent")
	}
}

// TestAudioFetcherFallsBackToBackupURL pins the retry order: a 403 from the
// primary URL must make the fetcher try the backup URL instead of failing the
// video.
func TestAudioFetcherFallsBackToBackupURL(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
	)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/audio/base.m4s" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, "backup-audio")
	}))
	defer cdn.Close()

	api := newFakeBiliAPI(t, dashPlayurl(cdn.URL,
		fakeTrack{path: "/audio/base.m4s", bandwidth: 30216, backup: []string{"/audio/backup.m4s"}}))
	f := api.fetcher(t, t.TempDir())

	path, err := f.Fetch(context.Background(), store.Video{Bvid: "BV1uv411q7Mv", Cid: 1})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audio: %v", err)
	}
	if string(got) != "backup-audio" {
		t.Errorf("audio body = %q, want the backup track", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 || paths[0] != "/audio/base.m4s" || paths[1] != "/audio/backup.m4s" {
		t.Errorf("CDN paths = %v, want the base URL then its backup", paths)
	}
}

// TestAudioFetcherErrorsWhenNoAudioTracks pins that a video without a dash
// audio list is a clean error and leaves nothing on disk.
func TestAudioFetcherErrorsWhenNoAudioTracks(t *testing.T) {
	api := newFakeBiliAPI(t, `{"code":0,"message":"0","data":{"dash":{"audio":[],"duration":60}}}`)
	dir := t.TempDir()
	f := api.fetcher(t, dir)

	if _, err := f.Fetch(context.Background(), store.Video{Bvid: "BV1uv411q7Mv", Cid: 1}); err == nil {
		t.Fatal("want an error when the playurl response carries no audio track")
	}
	assertEmptyDir(t, dir)
}

// TestAudioFetcherLeavesNoPartialFileWhenEveryURLFails pins the cleanup: a
// failed download must not leave a truncated .m4s behind for a retry to mistake
// for a complete one.
func TestAudioFetcherLeavesNoPartialFileWhenEveryURLFails(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer cdn.Close()

	api := newFakeBiliAPI(t, dashPlayurl(cdn.URL,
		fakeTrack{path: "/audio/one.m4s", bandwidth: 30216, backup: []string{"/audio/two.m4s"}}))
	dir := t.TempDir()
	f := api.fetcher(t, dir)

	if _, err := f.Fetch(context.Background(), store.Video{Bvid: "BV1uv411q7Mv", Cid: 1}); err == nil {
		t.Fatal("want an error when every audio URL answers 403")
	}
	assertEmptyDir(t, dir)
}

// TestAudioFetcherTellsTheUserToLogIn pins the most likely real-world failure:
// the playurl endpoint answers -101 for an anonymous client, and a bare "code=-101"
// error tells the user nothing about what to do next.
func TestAudioFetcherTellsTheUserToLogIn(t *testing.T) {
	api := newFakeBiliAPI(t, `{"code":-101,"message":"账号未登录"}`)
	dir := t.TempDir()
	f := api.fetcher(t, dir)

	_, err := f.Fetch(context.Background(), store.Video{Bvid: "BV1uv411q7Mv", Cid: 1})
	if err == nil {
		t.Fatal("want an error when playurl answers -101")
	}
	if !strings.Contains(err.Error(), "login") {
		t.Errorf("error = %q, want a hint to run the login command", err)
	}
	assertEmptyDir(t, dir)
}

// TestAudioFetcherDoesNotBlameLoginForARejectedRequest pins the other side of
// that hint: -400 is bilibili rejecting this one request, so the caller must
// get the error unchanged (it stays a per-video failure) instead of being told
// to log in again.
func TestAudioFetcherDoesNotBlameLoginForARejectedRequest(t *testing.T) {
	api := newFakeBiliAPI(t, `{"code":-400,"message":"请求错误"}`)
	dir := t.TempDir()
	f := api.fetcher(t, dir)

	_, err := f.Fetch(context.Background(), store.Video{Bvid: "BV1uv411q7Mv", Cid: 1})
	if err == nil {
		t.Fatal("want an error when playurl rejects the request")
	}
	if strings.Contains(err.Error(), "login") {
		t.Errorf("error = %q, want no login hint for a rejected request", err)
	}
	var apiErr *bili.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != -400 {
		t.Errorf("error = %v, want the -400 to survive so the caller can classify it", err)
	}
	assertEmptyDir(t, dir)
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("output dir = %v, want no leftover file", entries)
	}
}
