package bili

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type nopLimiter struct{}

func (nopLimiter) Wait(context.Context) error { return nil }

// shrinkRetryBackoff makes the 412 retry wait a millisecond so tests do not
// sleep for the production one-second window.
func shrinkRetryBackoff(t *testing.T) {
	t.Helper()
	previous := retryBackoff
	retryBackoff = time.Millisecond
	t.Cleanup(func() { retryBackoff = previous })
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
	if apiCalls != 2 {
		t.Fatalf("api calls = %d, want 2 (initial + one retry)", apiCalls)
	}
}
