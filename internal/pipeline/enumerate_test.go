package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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

// TestParseURLSeasonBeatsSpace pins the trap this feature exists to avoid: a
// 合集 URL starts with space.bilibili.com/<mid>/..., so testing the space
// pattern first returns the UP主's whole backlog -- 54 videos instead of the 5
// the link points at -- with no error and no warning anywhere.
func TestParseURLSeasonBeatsSpace(t *testing.T) {
	cases := []struct {
		in  string
		key string
	}{
		{"https://space.bilibili.com/453013730/channel/collectiondetail?sid=9241496", "season:453013730:9241496"},
		{"https://space.bilibili.com/453013730/channel/collectiondetail?sid=9241496&ctype=21", "season:453013730:9241496"},
		{"space.bilibili.com/453013730/lists/9241496?type=season", "season:453013730:9241496"},
		{"https://space.bilibili.com/453013730/lists/9241496", "season:453013730:9241496"},
	}
	for _, c := range cases {
		kind, key, err := ParseURL(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if kind != KindSeason || key != c.key {
			t.Fatalf("%s: got (%v,%s) want (season,%s)", c.in, kind, key, c.key)
		}
	}
}

// TestSeasonURLRoundTrips pins that the link the CLI tells the user to paste is
// one this parser accepts: a hint that produced an unparseable URL would send
// them in a circle.
func TestSeasonURLRoundTrips(t *testing.T) {
	for _, c := range []struct{ mid, sid string }{
		{"453013730", "9241496"},
		{"406571547", "814546"},
	} {
		link := SeasonURL(c.mid, c.sid)
		kind, key, err := ParseURL(link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		if kind != KindSeason || key != SeasonKey(c.mid, c.sid) {
			t.Fatalf("%s: got (%v,%s) want (season,%s)", link, kind, key, SeasonKey(c.mid, c.sid))
		}
		mid, sid, ok := SplitSeasonKey(key)
		if !ok || mid != c.mid || sid != c.sid {
			t.Fatalf("SplitSeasonKey(%q) = (%q,%q,%v)", key, mid, sid, ok)
		}
	}
}

func TestSplitSeasonKeyRejectsNonSeasonKeys(t *testing.T) {
	for _, key := range []string{"", "453013730", "BV1uv411q7Mv", "season:", "season:1", "season::2", "season:1:"} {
		if mid, sid, ok := SplitSeasonKey(key); ok {
			t.Errorf("SplitSeasonKey(%q) = (%q,%q,true), want ok=false", key, mid, sid)
		}
	}
}

// TestParseURLSeriesIsNamedNotSilentlyWrong: a 视频选集 is not a 合集 and has no
// enumerator yet. Saying so is the whole point -- falling through to the space
// pattern would download the UP主's entire backlog instead.
func TestParseURLSeriesIsNamedNotSilentlyWrong(t *testing.T) {
	_, _, err := ParseURL("https://space.bilibili.com/453013730/channel/seriesdetail?sid=123")
	if err == nil {
		t.Fatal("want an error naming the unsupported source")
	}
	if !strings.Contains(err.Error(), "选集") {
		t.Fatalf("err = %v, want it to name 视频选集 as unsupported", err)
	}
}

func TestSourceKindString(t *testing.T) {
	cases := map[SourceKind]string{
		KindSingle: "single",
		KindSpace:  "space",
		KindFav:    "fav",
		KindSeason: "season",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Fatalf("kind %d: got %q want %q", int(kind), got, want)
		}
	}
}

func TestParseURLForKind(t *testing.T) {
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
		{"12345", KindSpace, "12345"},
		{"https://api.bilibili.com/x/v3/fav/resource/list?media_id=999", KindFav, "999"},
		{"https://www.bilibili.com/medialist/detail/ml999", KindFav, "999"},
		{"https://example.com/fav?fid=777", KindFav, "777"},
		{"999", KindFav, "999"},
		{"https://space.bilibili.com/453013730/channel/collectiondetail?sid=9241496", KindSeason, "season:453013730:9241496"},
		{"https://space.bilibili.com/453013730/lists/9241496?type=season", KindSeason, "season:453013730:9241496"},
	}
	for _, c := range cases {
		key, err := ParseURLForKind(c.in, c.kind)
		if err != nil {
			t.Fatalf("%s as %s: %v", c.in, c.kind, err)
		}
		if key != c.key {
			t.Fatalf("%s as %s: got %q want %q", c.in, c.kind, key, c.key)
		}
	}
}

func TestParseURLForKindError(t *testing.T) {
	cases := []struct {
		in   string
		kind SourceKind
	}{
		{"https://space.bilibili.com/12345", KindSingle},
		{"BV1uv411q7Mv", KindSpace},
		{"https://example.com/foo", KindFav},
		{"", KindSingle},
		// 合集需要 mid 和 sid 两个号码，只给一个就用不了。
		{"https://space.bilibili.com/453013730/channel/collectiondetail?sid=9241496", KindSingle},
		{"https://www.bilibili.com/video/BV1uv411q7Mv", KindSeason},
	}
	for _, c := range cases {
		if key, err := ParseURLForKind(c.in, c.kind); err == nil {
			t.Fatalf("ParseURLForKind(%q, %s) = %q, want error", c.in, c.kind, key)
		}
	}
}

func TestParseSourceKind(t *testing.T) {
	cases := []struct {
		in   string
		kind SourceKind
		ok   bool
	}{
		{"single", KindSingle, true},
		{"SPACE", KindSpace, true},
		{"Fav", KindFav, true},
		{" fav ", KindFav, true},
		{"season", KindSeason, true},
		{" Season ", KindSeason, true},
		{"", 0, false},
		{"video", 0, false},
	}
	for _, c := range cases {
		kind, ok := ParseSourceKind(c.in)
		if ok != c.ok || (ok && kind != c.kind) {
			t.Fatalf("ParseSourceKind(%q) = (%v,%v), want (%v,%v)", c.in, kind, ok, c.kind, c.ok)
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
	if e.SeasonBaseURL != "https://api.bilibili.com/x/polymer/web-space/seasons_archives_list" {
		t.Fatalf("SeasonBaseURL=%s", e.SeasonBaseURL)
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

// TestEnumerateSeason pins the 合集 listing: it pages through
// seasons_archives_list with both the mid and the season_id, then resolves every
// bvid it finds. The second step is not optional -- the listing's archives[]
// carry no cid, so ResolveVideo is what turns a 合集 into rows the rest of the
// pipeline can actually fetch.
func TestEnumerateSeason(t *testing.T) {
	viewSrv := httptest.NewServer(http.HandlerFunc(viewHandler))
	defer viewSrv.Close()

	var mu sync.Mutex
	var got []string
	seasonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		got = append(got, fmt.Sprintf("mid=%s sid=%s pn=%s ps=%s",
			q.Get("mid"), q.Get("season_id"), q.Get("page_num"), q.Get("page_size")))
		mu.Unlock()
		switch q.Get("page_num") {
		case "1":
			w.Write([]byte(`{"code":0,"message":"0","data":{"archives":[
				{"bvid":"BV1uv411q7Mv","title":"one"},
				{"bvid":"BV1uv411q7Mx","title":"two"}],
				"page":{"page_num":1,"page_size":2,"total":3}}}`))
		case "2":
			w.Write([]byte(`{"code":0,"message":"0","data":{"archives":[
				{"bvid":"BV1uv411q7My","title":"three"}],
				"page":{"page_num":2,"page_size":2,"total":3}}}`))
		default:
			t.Errorf("unexpected page_num %q", q.Get("page_num"))
			w.Write([]byte(`{"code":0,"message":"0","data":{"archives":[]}}`))
		}
	}))
	defer seasonSrv.Close()

	e := NewEnumerator(bili.NewClient("", nopLimiter{}))
	e.ViewBaseURL = viewSrv.URL
	e.SeasonBaseURL = seasonSrv.URL

	videos, err := e.EnumerateSeason(context.Background(), "453013730", "9241496")
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 3 {
		t.Fatalf("videos=%d want 3", len(videos))
	}
	// Titles come from the view handler, which is how this proves every bvid went
	// through ResolveVideo rather than being used straight from the listing.
	want := []string{"title-BV1uv411q7Mv", "title-BV1uv411q7Mx", "title-BV1uv411q7My"}
	for i, w := range want {
		if videos[i].Title != w {
			t.Errorf("videos[%d].Title = %q, want %q", i, videos[i].Title, w)
		}
		if videos[i].Cid != 100 {
			t.Errorf("videos[%d].Cid = %d, want the cid the listing does not carry", i, videos[i].Cid)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("season requests = %v, want one per page", got)
	}
	if got[0] != "mid=453013730 sid=9241496 pn=1 ps=30" {
		t.Errorf("first request = %q", got[0])
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
