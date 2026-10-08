package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"bilibili-sub/internal/bili"
)

type nopLimiter struct{}

func (nopLimiter) Wait(context.Context) error { return nil }

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		kind SourceKind
		key  string
	}{
		{"https://www.bilibili.com/video/BV1uv411q7Mv", KindSingle, "BV1uv411q7Mv"},
		{"BV1uv411q7Mv", KindSingle, "BV1uv411q7Mv"},
		{"https://space.bilibili.com/12345", KindSpace, "12345"},
		{"https://space.bilibili.com/12345/video", KindSpace, "12345"},
		{"https://www.bilibili.com/medialist/play/12345?business=space", KindSpace, "12345"},
		{"https://api.bilibili.com/x/v3/fav/resource/list?media_id=999", KindFav, "999"},
		{"https://www.bilibili.com/medialist/detail/ml999", KindFav, "999"},
	}
	for _, c := range cases {
		k, key, err := ParseURL(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if k != c.kind || key != c.key {
			t.Fatalf("%s: got (%v,%s) want (%v,%s)", c.in, k, key, c.kind, c.key)
		}
	}
}

func TestParseURLError(t *testing.T) {
	if _, _, err := ParseURL("https://example.com/foo"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSourceKindString(t *testing.T) {
	cases := map[SourceKind]string{
		KindSingle: "single",
		KindSpace:  "space",
		KindFav:    "fav",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Fatalf("kind %d: got %q want %q", int(kind), got, want)
		}
	}
}

func TestNewEnumeratorDefaults(t *testing.T) {
	e := NewEnumerator(bili.NewClient("", nopLimiter{}))
	if e.NavURL != "https://api.bilibili.com/x/web-interface/nav" {
		t.Fatalf("NavURL=%s", e.NavURL)
	}
	if e.SpaceBaseURL != "https://api.bilibili.com/x/space/wbi/arc/search" {
		t.Fatalf("SpaceBaseURL=%s", e.SpaceBaseURL)
	}
	if e.FavBaseURL != "https://api.bilibili.com/x/v3/fav/resource/list" {
		t.Fatalf("FavBaseURL=%s", e.FavBaseURL)
	}
	if e.ViewBaseURL != "https://api.bilibili.com/x/web-interface/view" {
		t.Fatalf("ViewBaseURL=%s", e.ViewBaseURL)
	}
}

// viewHandler answers the view endpoint for any bvid with a deterministic
// title and a single page, so resolved videos are easy to assert on.
func viewHandler(w http.ResponseWriter, r *http.Request) {
	bvid := r.URL.Query().Get("bvid")
	fmt.Fprintf(w, `{"code":0,"message":"0","data":{"bvid":%q,"aid":1,"cid":100,"title":"title-%s","pages":[{"cid":100,"page":1,"part":"P1"}]}}`, bvid, bvid)
}

func TestResolveVideo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("bvid"); got != "BV1uv411q7Mv" {
			t.Errorf("bvid=%q", got)
		}
		w.Write([]byte(`{"code":0,"message":"0","data":{"bvid":"BV1uv411q7Mv","aid":42,"cid":100,"title":"第一个视频","pages":[{"cid":100,"page":1,"part":"P1"},{"cid":101,"page":2,"part":"P2"}]}}`))
	}))
	defer srv.Close()

	e := NewEnumerator(bili.NewClient("", nopLimiter{}))
	e.ViewBaseURL = srv.URL

	info, err := e.ResolveVideo(context.Background(), "BV1uv411q7Mv")
	if err != nil {
		t.Fatal(err)
	}
	if info.Bvid != "BV1uv411q7Mv" || info.Title != "第一个视频" {
		t.Fatalf("bad info: %+v", info)
	}
	if len(info.Pages) != 2 {
		t.Fatalf("pages=%d want 2", len(info.Pages))
	}
}

func TestEnumerateFav(t *testing.T) {
	viewSrv := httptest.NewServer(http.HandlerFunc(viewHandler))
	defer viewSrv.Close()

	var mu sync.Mutex
	var pages []string
	favSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("media_id") != "999" {
			t.Errorf("media_id=%q", q.Get("media_id"))
		}
		if q.Get("ps") != "20" {
			t.Errorf("ps=%q", q.Get("ps"))
		}
		if q.Get("platform") != "web" {
			t.Errorf("platform=%q want web", q.Get("platform"))
		}
		pn := q.Get("pn")
		mu.Lock()
		pages = append(pages, pn)
		mu.Unlock()
		switch pn {
		case "1":
			w.Write([]byte(`{"code":0,"message":"0","data":{"medias":[{"bvid":"BV1uv411q7Mv","title":"one"}],"has_more":true}}`))
		case "2":
			w.Write([]byte(`{"code":0,"message":"0","data":{"medias":[{"bvid":"BV1uv411q7Mx","title":"two"}],"has_more":false}}`))
		default:
			t.Errorf("unexpected page %q", pn)
			w.Write([]byte(`{"code":0,"message":"0","data":{"medias":[],"has_more":false}}`))
		}
	}))
	defer favSrv.Close()

	e := NewEnumerator(bili.NewClient("", nopLimiter{}))
	e.ViewBaseURL = viewSrv.URL
	e.FavBaseURL = favSrv.URL

	videos, err := e.EnumerateFav(context.Background(), "999")
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 2 {
		t.Fatalf("videos=%d want 2", len(videos))
	}
	if videos[0].Bvid != "BV1uv411q7Mv" || videos[0].Title != "title-BV1uv411q7Mv" {
		t.Fatalf("bad first: %+v", videos[0])
	}
	if videos[1].Bvid != "BV1uv411q7Mx" {
		t.Fatalf("bad second: %+v", videos[1])
	}
	if len(videos[0].Pages) != 1 {
		t.Fatalf("first video pages=%d want 1", len(videos[0].Pages))
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(pages, []string{"1", "2"}) {
		t.Fatalf("fav pages=%v want [1 2]", pages)
	}
}

const (
	testImgKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSubKey = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestEnumerateSpace(t *testing.T) {
	navSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"code":0,"message":"0","data":{"wbi_img":{"img_url":%q,"sub_url":%q}}}`,
			"https://i0.hdslb.com/bfs/wbi/"+testImgKey+".png",
			"https://i0.hdslb.com/bfs/wbi/"+testSubKey+".png")
	}))
	defer navSrv.Close()

	// The enumerator must sign with the keys served by nav; recomputing the
	// signature here proves the params that reached the server were signed.
	signer, err := bili.NewWBISigner(testImgKey, testSubKey)
	if err != nil {
		t.Fatal(err)
	}

	viewSrv := httptest.NewServer(http.HandlerFunc(viewHandler))
	defer viewSrv.Close()

	var mu sync.Mutex
	var pages []string
	spaceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("mid") != "12345" {
			t.Errorf("mid=%q", q.Get("mid"))
		}
		if q.Get("ps") != "30" {
			t.Errorf("ps=%q", q.Get("ps"))
		}
		if q.Get("tid") != "0" {
			t.Errorf("tid=%q want 0", q.Get("tid"))
		}
		if !q.Has("keyword") || q.Get("keyword") != "" {
			t.Errorf("keyword=%q (present=%v) want an empty keyword", q.Get("keyword"), q.Has("keyword"))
		}
		if q.Get("order") != "pubdate" {
			t.Errorf("order=%q want pubdate", q.Get("order"))
		}
		if q.Get("web_location") != "1550101" {
			t.Errorf("web_location=%q want 1550101", q.Get("web_location"))
		}
		if q.Get("order_avoided") != "true" {
			t.Errorf("order_avoided=%q want true", q.Get("order_avoided"))
		}
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
		pn := q.Get("pn")
		mu.Lock()
		pages = append(pages, pn)
		mu.Unlock()
		switch pn {
		case "1":
			w.Write([]byte(`{"code":0,"message":"0","data":{"list":{"vlist":[{"bvid":"BV1uv411q7Mv","name":"first"}]},"page":{"count":2}}}`))
		case "2":
			w.Write([]byte(`{"code":0,"message":"0","data":{"list":{"vlist":[{"bvid":"BV1uv411q7Mx","name":"second"}]},"page":{"count":2}}}`))
		default:
			t.Errorf("unexpected page %q", pn)
			w.Write([]byte(`{"code":0,"message":"0","data":{"list":{"vlist":[]},"page":{"count":2}}}`))
		}
	}))
	defer spaceSrv.Close()

	e := NewEnumerator(bili.NewClient("", nopLimiter{}))
	e.NavURL = navSrv.URL
	e.SpaceBaseURL = spaceSrv.URL
	e.ViewBaseURL = viewSrv.URL

	videos, err := e.EnumerateSpace(context.Background(), "12345")
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 2 {
		t.Fatalf("videos=%d want 2", len(videos))
	}
	if videos[0].Bvid != "BV1uv411q7Mv" || videos[1].Bvid != "BV1uv411q7Mx" {
		t.Fatalf("bad videos: %+v %+v", videos[0], videos[1])
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(pages, []string{"1", "2"}) {
		t.Fatalf("space pages=%v want [1 2]", pages)
	}
}

// TestEnumerateSpaceCachesSigner checks the WBI signer is built once from nav
// and reused, so a second enumeration does not refetch the keys.
func TestEnumerateSpaceCachesSigner(t *testing.T) {
	var mu sync.Mutex
	navHits := 0
	navSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		navHits++
		mu.Unlock()
		fmt.Fprintf(w, `{"code":0,"message":"0","data":{"wbi_img":{"img_url":%q,"sub_url":%q}}}`,
			"https://i0.hdslb.com/bfs/wbi/"+testImgKey+".png",
			"https://i0.hdslb.com/bfs/wbi/"+testSubKey+".png")
	}))
	defer navSrv.Close()

	spaceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"list":{"vlist":[]},"page":{"count":0}}}`))
	}))
	defer spaceSrv.Close()

	e := NewEnumerator(bili.NewClient("", nopLimiter{}))
	e.NavURL = navSrv.URL
	e.SpaceBaseURL = spaceSrv.URL
	e.ViewBaseURL = spaceSrv.URL

	for i := 0; i < 2; i++ {
		videos, err := e.EnumerateSpace(context.Background(), "12345")
		if err != nil {
			t.Fatal(err)
		}
		if len(videos) != 0 {
			t.Fatalf("videos=%d want 0", len(videos))
		}
	}
	if e.signer == nil {
		t.Fatal("signer not cached")
	}
	mu.Lock()
	defer mu.Unlock()
	if navHits != 1 {
		t.Fatalf("navHits=%d want 1", navHits)
	}
}

// TestSignerConcurrent exercises the lazily-built signer from several
// goroutines: run with -race it catches the unlocked double-build/racing field
// write that concurrent probes can trigger.
func TestSignerConcurrent(t *testing.T) {
	var mu sync.Mutex
	navHits := 0
	navSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		navHits++
		mu.Unlock()
		fmt.Fprintf(w, `{"code":0,"message":"0","data":{"wbi_img":{"img_url":%q,"sub_url":%q}}}`,
			"https://i0.hdslb.com/bfs/wbi/"+testImgKey+".png",
			"https://i0.hdslb.com/bfs/wbi/"+testSubKey+".png")
	}))
	defer navSrv.Close()

	e := NewEnumerator(bili.NewClient("", nopLimiter{}))
	e.NavURL = navSrv.URL

	const workers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := e.Signer(context.Background()); err != nil {
				t.Errorf("Signer: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if navHits != 1 {
		t.Fatalf("navHits=%d want 1", navHits)
	}
}
