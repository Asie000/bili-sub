package bili

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type nopLimiter struct{}

func (nopLimiter) Wait(context.Context) error { return nil }

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
