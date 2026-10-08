package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/export"
	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/store"
	"bilibili-sub/internal/util"
)

// subtitleBodyJSON is the shape B站 serves at subtitle_url: a bare {body:[...]}
// with no {code,message,data} envelope, and from/to in float seconds.
const subtitleBodyJSON = `{"body":[{"from":0.0,"to":1.5,"content":"你好"},{"from":1.5,"to":2.5,"content":"世界"}]}`

// schemeRewriteTransport records every requested URL and downgrades https to
// http so a protocol-relative subtitle URL (normalized to https://) still
// reaches the plain-HTTP test server.
type schemeRewriteTransport struct {
	mu   sync.Mutex
	urls []string
}

func (t *schemeRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.urls = append(t.urls, req.URL.String())
	t.mu.Unlock()
	r := req.Clone(req.Context())
	r.URL.Scheme = "http"
	return http.DefaultTransport.RoundTrip(r)
}

func (t *schemeRewriteTransport) requested() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.urls...)
}

func TestNewDownloaderDefaults(t *testing.T) {
	d := NewDownloader(bili.NewClient("", nopLimiter{}), newTestStore(t), nil, t.TempDir())
	if d.PlayerBaseURL != "https://api.bilibili.com/x/player/wbi/v2" {
		t.Fatalf("PlayerBaseURL=%s", d.PlayerBaseURL)
	}
	if d.HTTP == nil || d.HTTP.Timeout != 30*time.Second {
		t.Fatalf("HTTP=%+v want a 30s client", d.HTTP)
	}
}

func TestDownloaderFetchSubtitle(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, Title: "测试视频", Author: "UP主"})

	var gotUA, gotReferer string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/player/wbi/v2":
			q := r.URL.Query()
			if q.Get("bvid") != v.Bvid || q.Get("cid") != "100" {
				t.Errorf("player params bvid=%q cid=%q", q.Get("bvid"), q.Get("cid"))
			}
			w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[
				{"lan":"en-US","lan_doc":"English","subtitle_url":"` + srv.URL + `/en.json"},
				{"lan":"zh-CN","lan_doc":"中文（简体）","subtitle_url":"` + srv.URL + `/zh.json"}]}}}`))
		case "/zh.json":
			gotUA = r.Header.Get("User-Agent")
			gotReferer = r.Header.Get("Referer")
			w.Write([]byte(subtitleBodyJSON))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	out := t.TempDir()
	d := NewDownloader(bili.NewClient("", nopLimiter{}), st, nil, out)
	d.PlayerBaseURL = srv.URL + "/x/player/wbi/v2"

	doc, err := d.FetchSubtitle(context.Background(), v, "")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Bvid != v.Bvid || doc.Cid != v.Cid || doc.Title != v.Title || doc.Author != v.Author {
		t.Fatalf("identity doc=%+v want bvid/cid/title/author of video", doc)
	}
	if doc.Lang != "zh-CN" {
		t.Fatalf("lang=%q want zh-CN", doc.Lang)
	}
	wantSegments := []ir.Segment{
		{StartMs: 0, EndMs: 1500, Text: "你好"},
		{StartMs: 1500, EndMs: 2500, Text: "世界"},
	}
	if len(doc.Segments) != len(wantSegments) {
		t.Fatalf("segments=%+v want %+v", doc.Segments, wantSegments)
	}
	for i, want := range wantSegments {
		if doc.Segments[i] != want {
			t.Fatalf("segment[%d]=%+v want %+v", i, doc.Segments[i], want)
		}
	}

	if gotUA == "" {
		t.Error("subtitle request carried no User-Agent")
	}
	if gotReferer != "https://www.bilibili.com" {
		t.Errorf("Referer=%q want https://www.bilibili.com", gotReferer)
	}

	srtPath := filepath.Join(out, util.SanitizeFilename(v.Title, v.Bvid)+".srt")
	srtBytes, err := os.ReadFile(srtPath)
	if err != nil {
		t.Fatalf("read %s: %v", srtPath, err)
	}
	srt := string(srtBytes)
	if !strings.Contains(srt, "00:00:00,000 --> 00:00:01,500") {
		t.Errorf("srt missing first cue timeline:\n%s", srt)
	}
	if !strings.Contains(srt, "你好") || !strings.Contains(srt, "世界") {
		t.Errorf("srt missing text:\n%s", srt)
	}

	if got := videoByID(t, st, v.ID); got.State != store.StateFetched || got.Lang != "zh-CN" {
		t.Fatalf("state=%q lang=%q want %q/zh-CN", got.State, got.Lang, store.StateFetched)
	}
	engine, lang, irJSON, err := st.GetTranscript(v.ID)
	if err != nil {
		t.Fatalf("GetTranscript: %v", err)
	}
	if engine != "bilibili" || lang != "zh-CN" {
		t.Fatalf("transcript engine=%q lang=%q want bilibili/zh-CN", engine, lang)
	}
	wantJSON, err := export.Render(doc, export.FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if irJSON != string(wantJSON) {
		t.Fatalf("stored ir_json=%s want %s", irJSON, wantJSON)
	}
}

// TestDownloaderNormalizesProtocolRelativeURL: subtitle_url is protocol-relative
// ("//host/path"); the downloader must fetch it over https.
func TestDownloaderNormalizesProtocolRelativeURL(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, Title: "T"})

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/player":
			w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[
				{"lan":"zh-CN","subtitle_url":"//` + r.Host + `/zh.json"}]}}}`))
		case "/zh.json":
			w.Write([]byte(subtitleBodyJSON))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	rt := &schemeRewriteTransport{}
	d := NewDownloader(bili.NewClient("", nopLimiter{}), st, nil, t.TempDir())
	d.PlayerBaseURL = srv.URL + "/player"
	d.HTTP = &http.Client{Transport: rt}

	doc, err := d.FetchSubtitle(context.Background(), v, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Segments) != 2 {
		t.Fatalf("segments=%+v want 2", doc.Segments)
	}
	reqs := rt.requested()
	if len(reqs) != 1 {
		t.Fatalf("subtitle requests=%v want exactly one", reqs)
	}
	want := "https://" + strings.TrimPrefix(srv.URL, "http://") + "/zh.json"
	if reqs[0] != want {
		t.Fatalf("fetched %q want %q", reqs[0], want)
	}
}

// TestDownloaderNoUsableTrackLeavesStateUnchanged: an empty result (no usable
// URL, or a lang preference that matches nothing) is an error, fetches no
// subtitle body, and persists nothing.
func TestDownloaderNoUsableTrackLeavesStateUnchanged(t *testing.T) {
	cases := []struct {
		name      string
		subtitles string
		pref      string
	}{
		{
			name:      "only a dangling track",
			subtitles: `{"lan":"zh-CN","lan_doc":"中文（简体）","subtitle_url":""}`,
			pref:      "",
		},
		{
			name:      "preference matches no track",
			subtitles: `{"lan":"en-US","lan_doc":"English","subtitle_url":"SUBURL"}`,
			pref:      "fr",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newTestStore(t)
			v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, Title: "T", State: store.StatePending})

			bodyFetched := false
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/player" {
					bodyFetched = true
					w.WriteHeader(http.StatusNotFound)
					return
				}
				subs := strings.ReplaceAll(c.subtitles, "SUBURL", srv.URL+"/zh.json")
				w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[` + subs + `]}}}`))
			}))
			defer srv.Close()

			out := t.TempDir()
			d := NewDownloader(bili.NewClient("", nopLimiter{}), st, nil, out)
			d.PlayerBaseURL = srv.URL + "/player"

			doc, err := d.FetchSubtitle(context.Background(), v, c.pref)
			if err == nil {
				t.Fatalf("err=nil want non-nil (doc=%+v)", doc)
			}
			if doc != nil {
				t.Fatalf("doc=%+v want nil on error", doc)
			}
			if bodyFetched {
				t.Error("subtitle body fetched despite no usable track")
			}
			if got := videoByID(t, st, v.ID); got.State != store.StatePending || got.Lang != "" {
				t.Fatalf("state=%q lang=%q want %q/\"\" (unchanged)", got.State, got.Lang, store.StatePending)
			}
			if _, _, _, err := st.GetTranscript(v.ID); err == nil {
				t.Error("transcript saved on failure")
			}
			srtPath := filepath.Join(out, util.SanitizeFilename(v.Title, v.Bvid)+".srt")
			if _, err := os.Stat(srtPath); !os.IsNotExist(err) {
				t.Errorf("srt written on failure: %v", err)
			}
		})
	}
}

// TestDownloaderSignsPlayerParams checks the player request is WBI-signed when
// an Enumerator is wired in, by re-signing what reached the stub server.
func TestDownloaderSignsPlayerParams(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, Title: "T"})

	navSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/` + testImgKey + `.png","sub_url":"https://i0.hdslb.com/bfs/wbi/` + testSubKey + `.png"}}}`))
	}))
	defer navSrv.Close()

	signer, err := bili.NewWBISigner(testImgKey, testSubKey)
	if err != nil {
		t.Fatal(err)
	}

	var playerSrv *httptest.Server
	playerSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zh.json" {
			w.Write([]byte(subtitleBodyJSON))
			return
		}
		q := r.URL.Query()
		if q.Get("w_rid") == "" || q.Get("wts") == "" {
			t.Errorf("unsigned request: w_rid=%q wts=%q", q.Get("w_rid"), q.Get("wts"))
		}
		params := make(map[string]string, len(q))
		for k, vs := range q {
			if k == "w_rid" {
				continue
			}
			params[k] = vs[0]
		}
		signed, err := signer.Sign(params)
		if err != nil {
			t.Errorf("resign: %v", err)
		} else if signed["w_rid"] != q.Get("w_rid") {
			t.Errorf("w_rid=%s want %s", q.Get("w_rid"), signed["w_rid"])
		}
		w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[
			{"lan":"zh-CN","subtitle_url":"` + playerSrv.URL + `/zh.json"}]}}}`))
	}))
	defer playerSrv.Close()

	e := NewEnumerator(bili.NewClient("", nopLimiter{}))
	e.NavURL = navSrv.URL

	d := NewDownloader(bili.NewClient("", nopLimiter{}), st, e, t.TempDir())
	d.PlayerBaseURL = playerSrv.URL + "/player"

	if _, err := d.FetchSubtitle(context.Background(), v, "zh-CN"); err != nil {
		t.Fatal(err)
	}
}

// TestDownloaderNilDependencies guards against a nil Client/Store panic.
func TestDownloaderNilDependencies(t *testing.T) {
	st := newTestStore(t)
	cases := []struct {
		name string
		d    *Downloader
	}{
		{"nil client", &Downloader{Store: st}},
		{"nil store", &Downloader{Client: bili.NewClient("", nopLimiter{})}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, err := c.d.FetchSubtitle(context.Background(), store.Video{ID: 1, Bvid: "BV1", Cid: 1}, "")
			if err == nil {
				t.Fatalf("err=nil want non-nil (doc=%+v)", doc)
			}
		})
	}
}

// TestDownloaderInvalidSubtitleJSON: a malformed subtitle body surfaces as an
// error instead of a zero-segment document.
func TestDownloaderInvalidSubtitleJSON(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, Title: "T", State: store.StatePending})

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/player" {
			w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[
				{"lan":"zh-CN","subtitle_url":"` + srv.URL + `/zh.json"}]}}}`))
			return
		}
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	d := NewDownloader(bili.NewClient("", nopLimiter{}), st, nil, t.TempDir())
	d.PlayerBaseURL = srv.URL + "/player"

	if _, err := d.FetchSubtitle(context.Background(), v, "zh-CN"); err == nil {
		t.Fatal("err=nil want decode error")
	}
	if got := videoByID(t, st, v.ID); got.State != store.StatePending {
		t.Fatalf("state=%q want %q", got.State, store.StatePending)
	}
	if _, _, _, err := st.GetTranscript(v.ID); err == nil {
		t.Error("transcript saved on failure")
	}
}
