package bili

import (
	"context"
	"net/http"
	"net/http/httptest"
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
