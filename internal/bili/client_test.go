package bili

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type nopLimiter struct{}

func (nopLimiter) Wait(context.Context) error { return nil }

// shrinkRetryBackoff makes the 412 retries wait a millisecond so tests do not
// sleep for the production backoff windows.
func shrinkRetryBackoff(t *testing.T) {
	t.Helper()
	previous := retryBackoffs
	retryBackoffs = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryBackoffs = previous })
}

func TestGetSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://www.bilibili.com" {
			t.Errorf("missing referer")
		}
		w.Write([]byte(`{"code":0,"message":"0","data":{"bvid":"BV1xx","title":"hi"}}`))
	}))
	defer srv.Close()

	c := NewClient("", nopLimiter{})
	var out struct {
		Bvid  string `json:"bvid"`
		Title string `json:"title"`
	}
	if err := c.Get(context.Background(), srv.URL, nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.Bvid != "BV1xx" || out.Title != "hi" {
		t.Fatalf("bad out: %+v", out)
	}
}

func TestGetAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":-101,"message":"账号未登录"}`))
	}))
	defer srv.Close()
	c := NewClient("", nopLimiter{})
	err := c.Get(context.Background(), srv.URL, nil, &struct{}{})
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want APIError, got %T", err)
	}
	if ae.Code != -101 {
		t.Fatalf("code=%d", ae.Code)
	}
	if !ae.Unauthorized() {
		t.Fatal("should be unauthorized")
	}
}

// TestGetExhaustedRetriesIsARiskControlError pins the sentinel every caller keys
// on: once the retries are used up and B站 is still answering 412, the error must
// wrap ErrRiskControl so a whole leg stops instead of retrying each remaining
// row and marking them all failed.
func TestGetExhaustedRetriesIsARiskControlError(t *testing.T) {
	shrinkRetryBackoff(t)
	fp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"b_3":"B3VALUE","b_4":"B4VALUE"}}`)
	}))
	defer fp.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer api.Close()

	c := NewClient("SESS", nopLimiter{})
	c.FingerprintURL = fp.URL

	err := c.Get(context.Background(), api.URL, nil, nil)
	if !errors.Is(err, ErrRiskControl) {
		t.Fatalf("err = %v, want it to wrap ErrRiskControl", err)
	}
}

// TestGetRetriesAfterTooManyRequests pins 429 to the same path as 412. B站's own
// gate answers 412, but 429 is the generic "too many requests" it can return, and
// the docs have promised 412/429 all along.
func TestGetRetriesAfterTooManyRequests(t *testing.T) {
	shrinkRetryBackoff(t)
	fp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"b_3":"B3VALUE","b_4":"B4VALUE"}}`)
	}))
	defer fp.Close()

	var apiCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		if apiCalls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"code":0,"message":"0","data":{"ok":true}}`)
	}))
	defer api.Close()

	c := NewClient("SESS", nopLimiter{})
	c.FingerprintURL = fp.URL

	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.Get(context.Background(), api.URL, nil, &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !out.OK {
		t.Fatal("payload was not decoded on the retry")
	}
	if apiCalls != 2 {
		t.Errorf("apiCalls = %d, want 2 (one 429, then the retry)", apiCalls)
	}
}

func TestGetRetriesWithBuvidAfter412(t *testing.T) {
	shrinkRetryBackoff(t)
	var fingerprintHits int
	fp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fingerprintHits++
		fmt.Fprint(w, `{"code":0,"data":{"b_3":"B3VALUE","b_4":"B4VALUE"}}`)
	}))
	defer fp.Close()

	var apiCalls int
	var retryCookie string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		if apiCalls == 1 {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		retryCookie = r.Header.Get("Cookie")
		fmt.Fprint(w, `{"code":0,"message":"0","data":{"ok":true}}`)
	}))
	defer api.Close()

	c := NewClient("SESS", nopLimiter{})
	c.FingerprintURL = fp.URL

	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.Get(context.Background(), api.URL, nil, &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !out.OK {
		t.Fatal("payload was not decoded on the retry")
	}
	if apiCalls != 2 {
		t.Fatalf("api calls = %d, want 2 (one 412 then one retry)", apiCalls)
	}
	if fingerprintHits == 0 {
		t.Fatal("fingerprint endpoint was never called")
	}
	for _, want := range []string{"SESSDATA=SESS", "buvid3=B3VALUE", "buvid4=B4VALUE"} {
		if !strings.Contains(retryCookie, want) {
			t.Fatalf("retry cookie %q is missing %q", retryCookie, want)
		}
	}
}

// A full browser Cookie is sent verbatim and the anonymous fingerprint is not
// fetched: the cookie already carries buvid3/buvid4 and overwriting them would
// defeat the whole point.
func TestFullCookieIsSentVerbatimWithoutFingerprint(t *testing.T) {
	const cookie = "SESSDATA=abc%2Cdef; buvid3=B3VALUE; bili_ticket=XYZ"
	var fingerprintHits int
	fp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fingerprintHits++
		fmt.Fprint(w, `{"code":0,"data":{"b_3":"B3VALUE","b_4":"B4VALUE"}}`)
	}))
	defer fp.Close()

	var gotCookie string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		fmt.Fprint(w, `{"code":0,"message":"0","data":{"ok":true}}`)
	}))
	defer api.Close()

	c := NewClient("", nopLimiter{})
	c.Cookie = cookie
	c.FingerprintURL = fp.URL

	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.Get(context.Background(), api.URL, nil, &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotCookie != cookie {
		t.Fatalf("Cookie header = %q, want %q verbatim", gotCookie, cookie)
	}
	if !out.OK {
		t.Fatal("payload was not decoded")
	}
	if fingerprintHits != 0 {
		t.Fatalf("fingerprint endpoint called %d times, want 0 with a full cookie", fingerprintHits)
	}
}

// TestEnsureBuvidIsFetchedOnceForConcurrentCallers pins that concurrent workers
// all wait for the single fingerprint fetch instead of racing past a flag: with
// a flag, every worker but the first sent its request bare, which is exactly the
// request the wbi endpoints answer with a 412.
func TestEnsureBuvidIsFetchedOnceForConcurrentCallers(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	fp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release // hold the fetch open so early-returning callers are visible
		fmt.Fprint(w, `{"code":0,"data":{"b_3":"B3VALUE","b_4":"B4VALUE"}}`)
	}))
	defer fp.Close()

	c := NewClient("SESS", nopLimiter{})
	c.FingerprintURL = fp.URL

	const n = 8
	var wg sync.WaitGroup
	returned := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.ensureBuvid(context.Background())
			returned <- struct{}{}
		}()
	}
	// One goroutine holds the fetch blocked on release; if the others did not wait
	// they would have returned by now.
	time.Sleep(50 * time.Millisecond)
	select {
	case <-returned:
		t.Fatal("a caller returned before the fingerprint fetch finished")
	default:
	}
	close(release)
	wg.Wait()
	if got := hits.Load(); got != 1 {
		t.Fatalf("fingerprint hits = %d, want 1", got)
	}
	cookie := c.cookieHeader()
	for _, want := range []string{"buvid3=B3VALUE", "buvid4=B4VALUE"} {
		if !strings.Contains(cookie, want) {
			t.Fatalf("cookie = %q, want %q", cookie, want)
		}
	}
}

// A SESSDATA-only client has no browser fingerprint, so it still fetches and
// appends the anonymous buvid cookies exactly as before.
func TestSESSDATAOnlyClientAppendsBuvid(t *testing.T) {
	fp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"b_3":"B3VALUE","b_4":"B4VALUE"}}`)
	}))
	defer fp.Close()

	var gotCookie string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		fmt.Fprint(w, `{"code":0,"message":"0","data":{"ok":true}}`)
	}))
	defer api.Close()

	c := NewClient("SESS", nopLimiter{})
	c.FingerprintURL = fp.URL

	if err := c.Get(context.Background(), api.URL, nil, &struct{}{}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, want := range []string{"SESSDATA=SESS", "buvid3=B3VALUE", "buvid4=B4VALUE"} {
		if !strings.Contains(gotCookie, want) {
			t.Fatalf("cookie %q is missing %q", gotCookie, want)
		}
	}
}

// Two 412 responses with unchanged cookies collapse into one clear risk-control
// error instead of a bare status code.
func TestGetReturnsClearErrorAfterRepeated412(t *testing.T) {
	shrinkRetryBackoff(t)
	fp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"b_3":"B3","b_4":"B4"}}`)
	}))
	defer fp.Close()

	var apiCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer api.Close()

	c := NewClient("SESS", nopLimiter{})
	c.FingerprintURL = fp.URL

	err := c.Get(context.Background(), api.URL, nil, &struct{}{})
	if err == nil {
		t.Fatal("want an error after two 412 responses")
	}
	if !strings.Contains(err.Error(), "412") || !strings.Contains(err.Error(), "完整 Cookie") {
		t.Fatalf("error = %v, want the 412 risk-control hint", err)
	}
	if apiCalls != 4 {
		t.Fatalf("api calls = %d, want 4 (initial + three retries)", apiCalls)
	}
}

// TestAPIErrorSessionExpiredIsNarrowerThanUnauthorized pins why the two
// predicates both exist: only -101 means the login itself is gone, so only
// -101 may stop a run and ask the user to log in again. -400 stays a per-request
// failure.
func TestAPIErrorSessionExpiredIsNarrowerThanUnauthorized(t *testing.T) {
	expired := &APIError{Code: -101, Message: "账号未登录"}
	rejected := &APIError{Code: -400, Message: "请求错误"}

	if !expired.SessionExpired() {
		t.Error("-101 must count as an expired session")
	}
	if rejected.SessionExpired() {
		t.Error("-400 is a rejected request, not an expired session")
	}
	if !expired.Unauthorized() || !rejected.Unauthorized() {
		t.Error("Unauthorized must keep covering both codes: it is exported API")
	}
}

// TestResolveURLFollowsRedirect pins that ResolveURL reports the address a
// redirect chain ends on, which is the whole point of the b23.tv short-link
// support: the token's meaning only exists after the hop.
func TestResolveURLFollowsRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer target.Close()

	final := target.URL + "/video/BV1uv411q7Mv"
	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final, http.StatusFound)
	}))
	defer short.Close()

	c := NewClient("", nopLimiter{})
	got, err := c.ResolveURL(context.Background(), short.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got != final {
		t.Fatalf("ResolveURL = %q, want %q", got, final)
	}
}
