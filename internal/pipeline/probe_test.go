package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// seedVideo inserts v and returns it with the assigned ID.
func seedVideo(t *testing.T, st *store.Store, v store.Video) store.Video {
	t.Helper()
	id, err := st.UpsertVideo(&v)
	if err != nil {
		t.Fatal(err)
	}
	v.ID = id
	return v
}

func videoByID(t *testing.T, st *store.Store, id int64) store.Video {
	t.Helper()
	videos, err := st.ListVideos("")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range videos {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("video %d not found", id)
	return store.Video{}
}

const twoSubtitlesBody = `{"code":0,"message":"0","data":{"subtitle":{"subtitles":[
	{"lan":"en-US","lan_doc":"English","subtitle_url":"//aisubtitle.hdslb.com/en.json"},
	{"lan":"zh-CN","lan_doc":"中文（简体）","subtitle_url":"//aisubtitle.hdslb.com/zh.json"}]}}}`

func TestNewProberDefaults(t *testing.T) {
	p := NewProber(bili.NewClient("", nopLimiter{}), nil, nil)
	if p.PlayerBaseURL != "https://api.bilibili.com/x/player/wbi/v2" {
		t.Fatalf("PlayerBaseURL=%s", p.PlayerBaseURL)
	}
	if p.PlayerV2URL != "https://api.bilibili.com/x/player/v2" {
		t.Fatalf("PlayerV2URL=%s", p.PlayerV2URL)
	}
	if p.AISubtitleURL != "https://api.bilibili.com/x/player/v2/ai/subtitle/search/stat" {
		t.Fatalf("AISubtitleURL=%s", p.AISubtitleURL)
	}
}

func TestProbeHasSubtitle(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("bvid") != "BV1uv411q7Mv" || q.Get("cid") != "100" {
			t.Errorf("params bvid=%q cid=%q", q.Get("bvid"), q.Get("cid"))
		}
		w.Write([]byte(twoSubtitlesBody))
	}))
	defer srv.Close()

	p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
	p.PlayerV2URL = srv.URL

	res, err := p.ProbeVideo(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasSubtitle || !res.CanDownload {
		t.Fatalf("HasSubtitle=%v CanDownload=%v want true/true", res.HasSubtitle, res.CanDownload)
	}
	if len(res.Langs) != 2 {
		t.Fatalf("langs=%d want 2", len(res.Langs))
	}
	if res.VideoID != v.ID || res.Bvid != v.Bvid {
		t.Fatalf("result identity: %+v", res)
	}
	if res.ChargingLocked {
		t.Fatal("ChargingLocked=true want false")
	}
	got := videoByID(t, st, v.ID)
	if got.State != store.StateDownloadable {
		t.Fatalf("state=%q want %q", got.State, store.StateDownloadable)
	}
	if got.Lang != "zh-CN" {
		t.Fatalf("lang=%q want zh-CN", got.Lang)
	}
}

func TestProbeNoSubtitle(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[]}}}`))
	}))
	defer srv.Close()

	p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
	p.PlayerV2URL = srv.URL

	res, err := p.ProbeVideo(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasSubtitle || res.CanDownload {
		t.Fatalf("HasSubtitle=%v CanDownload=%v want false/false", res.HasSubtitle, res.CanDownload)
	}
	if got := videoByID(t, st, v.ID); got.State != store.StateNoSubtitle {
		t.Fatalf("state=%q want %q", got.State, store.StateNoSubtitle)
	}
}

func TestProbeChargingLocked(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, IsCharging: true})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":-10403,"message":"no permission"}`))
	}))
	defer srv.Close()

	p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
	p.PlayerV2URL = srv.URL

	res, err := p.ProbeVideo(context.Background(), v)
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if !res.ChargingLocked || res.HasSubtitle || res.CanDownload {
		t.Fatalf("result=%+v want charging locked only", res)
	}
	if res.VideoID != v.ID || res.Bvid != v.Bvid {
		t.Fatalf("result identity: %+v", res)
	}
	if got := videoByID(t, st, v.ID); got.State != store.StateChargingLocked {
		t.Fatalf("state=%q want %q", got.State, store.StateChargingLocked)
	}
}

// TestProbeChargingLockedCodes pins which API failures count as "charging
// locked": only the no-permission/not-found/charging-seal codes. A video known
// to be charging does not turn an unrelated API error (e.g. -400 session
// expiry) into a charge lock.
func TestProbeChargingLockedCodes(t *testing.T) {
	cases := []struct {
		name         string
		code         int
		isCharging   bool
		wantCharging bool
		wantErr      bool
	}{
		{"no permission code", -10403, false, true, false},
		{"not found code", -404, false, true, false},
		{"charging seal code", -62002, false, true, false},
		{"charging video with other api error", -400, true, false, true},
		{"other api error on free video", -400, false, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newTestStore(t)
			v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, IsCharging: c.isCharging, State: store.StatePending})

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"code":` + strconv.Itoa(c.code) + `,"message":"x"}`))
			}))
			defer srv.Close()

			p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
			p.PlayerV2URL = srv.URL

			res, err := p.ProbeVideo(context.Background(), v)
			if c.wantErr {
				if err == nil {
					t.Fatalf("err=nil want non-nil (result=%+v)", res)
				}
				if res != nil {
					t.Fatalf("res=%+v want nil on error", res)
				}
				if got := videoByID(t, st, v.ID); got.State != store.StatePending || got.Lang != "" {
					t.Fatalf("state=%q lang=%q want %q/\"\" (unchanged)", got.State, got.Lang, store.StatePending)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.ChargingLocked != c.wantCharging {
				t.Fatalf("ChargingLocked=%v want %v", res.ChargingLocked, c.wantCharging)
			}
			wantState := store.StateChargingLocked
			if got := videoByID(t, st, v.ID); got.State != wantState {
				t.Fatalf("state=%q want %q", got.State, wantState)
			}
		})
	}
}

// TestProbeErrorLeavesStateUnchanged covers failures that are not charge locks:
// an ordinary API error on a charging video and a non-API transport failure
// must both surface as errors and leave the row untouched.
func TestProbeErrorLeavesStateUnchanged(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"api error -400 on charging video", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"code":-400,"message":"请求错误"}`))
		}},
		{"http 500 on charging video", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newTestStore(t)
			v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, IsCharging: true, State: store.StatePending})

			srv := httptest.NewServer(c.handler)
			defer srv.Close()

			p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
			p.PlayerV2URL = srv.URL

			res, err := p.ProbeVideo(context.Background(), v)
			if err == nil {
				t.Fatalf("err=nil want non-nil (result=%+v)", res)
			}
			if res != nil {
				t.Fatalf("res=%+v want nil on error", res)
			}
			if got := videoByID(t, st, v.ID); got.State != store.StatePending || got.Lang != "" {
				t.Fatalf("state=%q lang=%q want %q/\"\" (unchanged)", got.State, got.Lang, store.StatePending)
			}
		})
	}
}

// TestProbeNilDependencies guards against a nil Client/Store panic.
func TestProbeNilDependencies(t *testing.T) {
	st := newTestStore(t)
	cases := []struct {
		name string
		p    *Prober
	}{
		{"nil client", &Prober{Store: st}},
		{"nil store", &Prober{Client: bili.NewClient("", nopLimiter{})}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := c.p.ProbeVideo(context.Background(), store.Video{ID: 1, Bvid: "BV1", Cid: 1})
			if err == nil {
				t.Fatalf("err=nil want non-nil (result=%+v)", res)
			}
		})
	}
}

// TestProbeIgnoresTracksWithoutURL: a track with an empty subtitle_url cannot be
// fetched, so on its own it must count as no subtitle rather than downloadable.
func TestProbeIgnoresTracksWithoutURL(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[{"lan":"zh-CN","lan_doc":"中文（简体）","subtitle_url":""}]}}}`))
	}))
	defer srv.Close()

	p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
	p.PlayerV2URL = srv.URL

	res, err := p.ProbeVideo(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasSubtitle || res.CanDownload || len(res.Langs) != 0 {
		t.Fatalf("res=%+v want no subtitle", res)
	}
	if got := videoByID(t, st, v.ID); got.State != store.StateNoSubtitle {
		t.Fatalf("state=%q want %q", got.State, store.StateNoSubtitle)
	}
}

// TestProbeUsableTrackAmongDanglingOnes: only tracks with a concrete URL are
// offered as langs, so when the highest-priority track is dangling (an empty
// subtitle_url that is not an ai-* one) the lookup falls through to the next
// usable track instead of reporting no_subtitle.
func TestProbeUsableTrackAmongDanglingOnes(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[
			{"lan":"zh-CN","lan_doc":"中文（简体）","subtitle_url":""},
			{"lan":"en-US","lan_doc":"English","subtitle_url":"//aisubtitle.hdslb.com/en.json"}]}}}`))
	}))
	defer srv.Close()

	p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
	p.PlayerV2URL = srv.URL

	res, err := p.ProbeVideo(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasSubtitle || !res.CanDownload {
		t.Fatalf("HasSubtitle=%v CanDownload=%v want true/true", res.HasSubtitle, res.CanDownload)
	}
	if len(res.Langs) != 1 || res.Langs[0].Lan != "en-US" {
		t.Fatalf("langs=%+v want only en-US", res.Langs)
	}
	got := videoByID(t, st, v.ID)
	if got.State != store.StateDownloadable || got.Lang != "en-US" {
		t.Fatalf("state=%q lang=%q want %q/en-US", got.State, got.Lang, store.StateDownloadable)
	}
}

// TestProbeAISubtitleFallback: an ai-* track whose subtitle_url is empty is not
// dropped; its real URL comes from the AI subtitle stat endpoint.
func TestProbeAISubtitleFallback(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, Aid: 42})

	playerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[
			{"lan":"ai-zh","lan_doc":"中文（AI）","subtitle_url":""}]}}}`))
	}))
	defer playerSrv.Close()

	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("aid") != "42" || q.Get("cid") != "100" {
			t.Errorf("ai stat params aid=%q cid=%q want 42/100", q.Get("aid"), q.Get("cid"))
		}
		w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle_url":"//aisubtitle.hdslb.com/ai.json"}}`))
	}))
	defer aiSrv.Close()

	p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
	p.PlayerBaseURL = playerSrv.URL
	p.AISubtitleURL = aiSrv.URL

	res, err := p.ProbeVideo(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasSubtitle || !res.CanDownload {
		t.Fatalf("HasSubtitle=%v CanDownload=%v want true/true", res.HasSubtitle, res.CanDownload)
	}
	if len(res.Langs) != 1 || res.Langs[0].Lan != "ai-zh" || res.Langs[0].SubURL != "//aisubtitle.hdslb.com/ai.json" {
		t.Fatalf("langs=%+v want ai-zh with resolved URL", res.Langs)
	}
	got := videoByID(t, st, v.ID)
	if got.State != store.StateDownloadable || got.Lang != "ai-zh" {
		t.Fatalf("state=%q lang=%q want %q/ai-zh", got.State, got.Lang, store.StateDownloadable)
	}
}

// TestProbeAISubtitleRequiresAid: without an aid the AI stat endpoint cannot be
// called, so an ai-* track with an empty URL is still no_subtitle.
func TestProbeAISubtitleRequiresAid(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[
			{"lan":"ai-zh","lan_doc":"中文（AI）","subtitle_url":""}]}}}`))
	}))
	defer srv.Close()

	p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
	p.PlayerV2URL = srv.URL

	res, err := p.ProbeVideo(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasSubtitle || res.CanDownload || len(res.Langs) != 0 {
		t.Fatalf("res=%+v want no subtitle", res)
	}
	if got := videoByID(t, st, v.ID); got.State != store.StateNoSubtitle {
		t.Fatalf("state=%q want %q", got.State, store.StateNoSubtitle)
	}
}

// TestProbeAIStatFailureIsNotChargingLocked: a failed AI-stat lookup makes only
// that candidate unusable. It must not abort with an error (which ProbeVideo
// would map to charging_locked for a -10403 code) and must not stop the loop
// from trying a lower-priority usable track.
func TestProbeAIStatFailureIsNotChargingLocked(t *testing.T) {
	cases := []struct {
		name      string
		aiStatus  int
		aiBody    string
		subtitles string
		wantState string
		wantLang  string
		wantDown  bool
	}{
		{
			name:      "api error falls through to usable track",
			aiStatus:  http.StatusOK,
			aiBody:    `{"code":-10403,"message":"no permission"}`,
			subtitles: `{"lan":"ai-zh","lan_doc":"中文（AI）","subtitle_url":""},{"lan":"en-US","lan_doc":"English","subtitle_url":"//aisubtitle.hdslb.com/en.json"}`,
			wantState: store.StateDownloadable,
			wantLang:  "en-US",
			wantDown:  true,
		},
		{
			name:      "no fallback leaves state no_subtitle",
			aiStatus:  http.StatusInternalServerError,
			aiBody:    `boom`,
			subtitles: `{"lan":"ai-zh","lan_doc":"中文（AI）","subtitle_url":""}`,
			wantState: store.StateNoSubtitle,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newTestStore(t)
			v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, Aid: 42, IsCharging: true})

			playerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[` + c.subtitles + `]}}}`))
			}))
			defer playerSrv.Close()

			aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.aiStatus)
				w.Write([]byte(c.aiBody))
			}))
			defer aiSrv.Close()

			p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
			p.PlayerBaseURL = playerSrv.URL
			p.AISubtitleURL = aiSrv.URL

			res, err := p.ProbeVideo(context.Background(), v)
			if err != nil {
				t.Fatalf("err=%v want nil (AI-stat failure must not abort)", err)
			}
			if res.ChargingLocked {
				t.Fatalf("ChargingLocked=true want false (result=%+v)", res)
			}
			if res.CanDownload != c.wantDown || res.HasSubtitle != c.wantDown {
				t.Fatalf("HasSubtitle=%v CanDownload=%v want %v", res.HasSubtitle, res.CanDownload, c.wantDown)
			}
			got := videoByID(t, st, v.ID)
			if got.State != c.wantState || got.Lang != c.wantLang {
				t.Fatalf("state=%q lang=%q want %q/%q", got.State, got.Lang, c.wantState, c.wantLang)
			}
		})
	}
}

// TestProbeEndpointSelection pins the player endpoint choice: a known aid uses
// the WBI endpoint with aid+cid, while an unknown aid falls back to the
// unsigned x/player/v2 with cid+bvid.
func TestProbeEndpointSelection(t *testing.T) {
	t.Run("aid uses wbi endpoint", func(t *testing.T) {
		st := newTestStore(t)
		v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, Aid: 42})

		wbiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("aid") != "42" || q.Get("cid") != "100" {
				t.Errorf("wbi params aid=%q cid=%q want 42/100", q.Get("aid"), q.Get("cid"))
			}
			if q.Has("bvid") {
				t.Errorf("wbi request carried bvid=%q, want aid only", q.Get("bvid"))
			}
			w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[]}}}`))
		}))
		defer wbiSrv.Close()
		v2Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("player/v2 endpoint hit for a video with an aid")
			w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[]}}}`))
		}))
		defer v2Srv.Close()

		p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
		p.PlayerBaseURL = wbiSrv.URL
		p.PlayerV2URL = v2Srv.URL

		if _, err := p.ProbeVideo(context.Background(), v); err != nil {
			t.Fatal(err)
		}
		if got := videoByID(t, st, v.ID); got.State != store.StateNoSubtitle {
			t.Fatalf("state=%q want %q", got.State, store.StateNoSubtitle)
		}
	})

	t.Run("no aid uses player v2 endpoint", func(t *testing.T) {
		st := newTestStore(t)
		v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100})

		wbiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("wbi endpoint hit for a video without an aid")
			w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[]}}}`))
		}))
		defer wbiSrv.Close()
		v2Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("cid") != "100" || q.Get("bvid") != "BV1uv411q7Mv" {
				t.Errorf("v2 params cid=%q bvid=%q want 100/BV1uv411q7Mv", q.Get("cid"), q.Get("bvid"))
			}
			if q.Has("aid") {
				t.Errorf("v2 request carried aid=%q, want cid+bvid only", q.Get("aid"))
			}
			w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[]}}}`))
		}))
		defer v2Srv.Close()

		p := NewProber(bili.NewClient("", nopLimiter{}), st, nil)
		p.PlayerBaseURL = wbiSrv.URL
		p.PlayerV2URL = v2Srv.URL

		if _, err := p.ProbeVideo(context.Background(), v); err != nil {
			t.Fatal(err)
		}
		if got := videoByID(t, st, v.ID); got.State != store.StateNoSubtitle {
			t.Fatalf("state=%q want %q", got.State, store.StateNoSubtitle)
		}
	})
}

// TestProbeSignsParams checks the player request is WBI-signed when a signer is
// available, by re-signing what reached the stub server.
func TestProbeSignsParams(t *testing.T) {
	st := newTestStore(t)
	v := seedVideo(t, st, store.Video{Bvid: "BV1uv411q7Mv", Cid: 100, Aid: 42})

	navSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/` + testImgKey + `.png","sub_url":"https://i0.hdslb.com/bfs/wbi/` + testSubKey + `.png"}}}`))
	}))
	defer navSrv.Close()

	signer, err := bili.NewWBISigner(testImgKey, testSubKey)
	if err != nil {
		t.Fatal(err)
	}

	playerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotRid, gotWts := q.Get("w_rid"), q.Get("wts")
		if gotRid == "" || gotWts == "" {
			t.Errorf("unsigned request: w_rid=%q wts=%q", gotRid, gotWts)
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
		} else if signed["w_rid"] != gotRid {
			t.Errorf("w_rid=%s want %s", gotRid, signed["w_rid"])
		}
		w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[]}}}`))
	}))
	defer playerSrv.Close()

	e := NewEnumerator(bili.NewClient("", nopLimiter{}))
	e.NavURL = navSrv.URL

	p := NewProber(bili.NewClient("", nopLimiter{}), st, e)
	p.PlayerBaseURL = playerSrv.URL

	if _, err := p.ProbeVideo(context.Background(), v); err != nil {
		t.Fatal(err)
	}
}

func TestPickLang(t *testing.T) {
	subs := func(lans ...string) []bili.SubtitleInfo {
		out := make([]bili.SubtitleInfo, 0, len(lans))
		for _, l := range lans {
			out = append(out, bili.SubtitleInfo{Lan: l})
		}
		return out
	}
	cases := []struct {
		name    string
		avail   []bili.SubtitleInfo
		pref    string
		wantLan string
		wantOK  bool
	}{
		{"default prefers zh-CN over en/ai-zh", subs("en-US", "ai-zh", "zh-CN"), "", "zh-CN", true},
		{"default prefers zh-Hans over zh", subs("zh", "zh-Hans"), "", "zh-Hans", true},
		{"default falls back to ai-zh", subs("ai-zh", "en-US"), "", "ai-zh", true},
		{"default falls back to en", subs("ja", "en-US"), "", "en-US", true},
		{"default falls back to any", subs("ja"), "", "ja", true},
		{"pref en picks en-US", subs("zh-CN", "en-US"), "en", "en-US", true},
		{"pref exact beats prefix", subs("en-GB", "en"), "en", "en", true},
		{"pref is case-insensitive", subs("zh-CN"), "ZH-cn", "zh-CN", true},
		{"pref zh-Hans finds zh-CN", subs("en-US", "zh-CN"), "zh-Hans", "zh-CN", true},
		{"pref zh-Hans finds generic zh", subs("zh"), "zh-Hans", "zh", true},
		{"pref zh-Hans refuses the opposite script", subs("zh-Hant"), "zh-Hans", "", false},
		{"pref zh finds ai-zh", subs("ai-zh"), "zh", "ai-zh", true},
		{"pref en-GB matches the en family", subs("en"), "en-GB", "en", true},
		{"pref zh-TW finds zh-Hant", subs("zh-Hant"), "zh-TW", "zh-Hant", true},
		{"no match", subs("zh-CN", "en-US"), "fr", "", false},
		{"empty", nil, "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := PickLang(c.avail, c.pref)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v (got %+v)", ok, c.wantOK, got)
			}
			if got.Lan != c.wantLan {
				t.Fatalf("lan=%q want %q", got.Lan, c.wantLan)
			}
		})
	}
}
