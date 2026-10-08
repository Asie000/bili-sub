package bili

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestSaveLoadSESSDATA(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.json")
	if err := SaveSESSDATA(p, "abc"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSESSDATA(p)
	if err != nil || got != "abc" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestSaveSESSDATAWritesJSONAndCreatesParentDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data", "session.json")
	if err := SaveSESSDATA(p, "abc"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"sessdata":"abc"}` {
		t.Fatalf("file content = %s", b)
	}
}

func TestSaveSESSDATAPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not enforced on Windows")
	}
	p := filepath.Join(t.TempDir(), "session.json")
	if err := SaveSESSDATA(p, "abc"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %v, want 0600", got)
	}
}

func TestLoadSESSDATAMissingFile(t *testing.T) {
	got, err := LoadSESSDATA(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestCheckLoginAt(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"logged in", `{"code":0,"message":"0","data":{"isLogin":true,"mid":42}}`, true},
		{"logged out", `{"code":0,"message":"0","data":{"isLogin":false}}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewClient("", nopLimiter{})
			got, err := c.CheckLoginAt(context.Background(), srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("isLogin = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestQRLoginSuccessFromSetCookie(t *testing.T) {
	var generateCalls, pollCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/generate", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&generateCalls, 1)
		if r.Header.Get("User-Agent") == "" || r.Header.Get("Referer") == "" {
			t.Error("generate missing UA/Referer headers")
		}
		w.Write([]byte(`{"code":0,"message":"0","data":{"url":"https://passport.bilibili.com/h5-app/passport/login/scan?navhide=1&qrcode_key=KEY123","qrcode_key":"KEY123"}}`))
	})
	mux.HandleFunc("/poll", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pollCalls, 1)
		if got := r.URL.Query().Get("qrcode_key"); got != "KEY123" {
			t.Errorf("qrcode_key = %q, want KEY123", got)
		}
		if r.Header.Get("User-Agent") == "" || r.Header.Get("Referer") == "" {
			t.Error("poll missing UA/Referer headers")
		}
		w.Header().Add("Set-Cookie", "SESSDATA=abc; Path=/; HttpOnly")
		w.Write([]byte(`{"code":0,"message":"0","data":{"code":0,"url":"https://passport.bilibili.com/h5-app/passport/login/scan?navhide=1&qrcode_key=KEY123&SESSDATA=fromurl"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	q := NewQRLoginClient()
	q.GenerateURL = srv.URL + "/generate"
	q.PollURL = srv.URL + "/poll"
	q.pollInterval = time.Millisecond

	var gotQR string
	sess, err := q.Login(context.Background(), func(u string) { gotQR = u })
	if err != nil {
		t.Fatal(err)
	}
	if sess != "abc" {
		t.Fatalf("sessdata = %q, want abc", sess)
	}
	if want := "https://passport.bilibili.com/h5-app/passport/login/scan?navhide=1&qrcode_key=KEY123"; gotQR != want {
		t.Fatalf("onQR url = %q, want %q", gotQR, want)
	}
	if n := atomic.LoadInt32(&generateCalls); n != 1 {
		t.Errorf("generate calls = %d, want 1", n)
	}
	if n := atomic.LoadInt32(&pollCalls); n != 1 {
		t.Errorf("poll calls = %d, want 1 (first poll already succeeds)", n)
	}
}

func TestQRLoginSESSDATAFromPollURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/generate", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"url":"https://passport.bilibili.com/x?qrcode_key=KEY1","qrcode_key":"KEY1"}}`))
	})
	mux.HandleFunc("/poll", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"code":0,"url":"https://passport.bilibili.com/h5-app/passport/login/scan?navhide=1&SESSDATA=from-url&gourl=hi"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	q := NewQRLoginClient()
	q.GenerateURL = srv.URL + "/generate"
	q.PollURL = srv.URL + "/poll"
	q.pollInterval = time.Millisecond

	sess, err := q.Login(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess != "from-url" {
		t.Fatalf("sessdata = %q, want from-url", sess)
	}
}

func TestQRLoginExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/generate" {
			w.Write([]byte(`{"code":0,"message":"0","data":{"url":"https://passport.bilibili.com/x?qrcode_key=KEY1","qrcode_key":"KEY1"}}`))
			return
		}
		w.Write([]byte(`{"code":0,"message":"0","data":{"code":86038,"message":"二维码已失效"}}`))
	}))
	defer srv.Close()

	q := NewQRLoginClient()
	q.GenerateURL = srv.URL + "/generate"
	q.PollURL = srv.URL + "/poll"
	q.pollInterval = time.Millisecond

	if _, err := q.Login(context.Background(), nil); err == nil {
		t.Fatal("want error for expired QR code")
	}
}

func TestQRLoginPollsUntilContextEnds(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/generate", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"url":"https://passport.bilibili.com/x?qrcode_key=KEY1","qrcode_key":"KEY1"}}`))
	})
	mux.HandleFunc("/poll", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"0","data":{"code":86101,"message":"未扫码"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	q := NewQRLoginClient()
	q.GenerateURL = srv.URL + "/generate"
	q.PollURL = srv.URL + "/poll"
	q.pollInterval = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := q.Login(ctx, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestQRLoginGenerateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":-400,"message":"请求错误"}`))
	}))
	defer srv.Close()

	q := NewQRLoginClient()
	q.GenerateURL = srv.URL + "/generate"
	q.PollURL = srv.URL + "/poll"

	if _, err := q.Login(context.Background(), nil); err == nil {
		t.Fatal("want error when generate fails")
	}
}
