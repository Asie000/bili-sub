package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/config"
	"bilibili-sub/internal/server"
	"bilibili-sub/internal/store"
)

// newTestRuntime returns a runtime rooted in a temporary data directory, so no
// test reads or writes the developer's data/ tree.
func newTestRuntime(t *testing.T) *runtime {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.DBPath = filepath.Join(cfg.DataDir, "bili_sub.db")
	return &runtime{cfg: cfg}
}

// TestRuntimeSharesOneRateLimiter pins IMPORTANT 2: the server builds a client
// per request, so a limiter built per call would let every concurrent request
// spend the full QPS budget instead of sharing it.
func TestRuntimeSharesOneRateLimiter(t *testing.T) {
	rt := newTestRuntime(t)

	first, err := rt.client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	second, err := rt.client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	if first.Limiter == nil {
		t.Fatal("client has no limiter")
	}
	if first.Limiter != second.Limiter {
		t.Fatal("two clients from one runtime must share a single rate limiter")
	}
}

// TestServeSeamsSerializePipelineOperations pins the second half of IMPORTANT 2:
// while an operation holds the runtime's lock (as a running probe, download or
// export does), every seam refuses to start another browser operation instead of
// running alongside it. The bogus url keeps a regression from reaching the
// network: it only matters when the busy check is missing.
func TestServeSeamsSerializePipelineOperations(t *testing.T) {
	rt := newTestRuntime(t)
	st, err := store.Open(rt.cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	opts := rt.serveOptions(st, server.NewHub())

	rt.opMu.Lock()
	defer rt.opMu.Unlock()

	if _, err := opts.Probe(context.Background(), "not-a-url", "", nil); !errors.Is(err, errOperationBusy) {
		t.Errorf("Probe error = %v, want errOperationBusy", err)
	}
	if _, err := opts.Download(context.Background(), "", false); !errors.Is(err, errOperationBusy) {
		t.Errorf("Download error = %v, want errOperationBusy", err)
	}
	if _, err := opts.Export(context.Background(), []string{"srt"}); !errors.Is(err, errOperationBusy) {
		t.Errorf("Export error = %v, want errOperationBusy", err)
	}
}

// runQRLogin calls the QRLogin seam with a deadline, so a regression to the old
// hang fails the test instead of blocking the whole suite.
func runQRLogin(t *testing.T, seam func(context.Context) (string, error), ctx context.Context) (string, error) {
	t.Helper()
	type result struct {
		url string
		err error
	}
	done := make(chan result, 1)
	go func() {
		url, err := seam(ctx)
		done <- result{url: url, err: err}
	}()
	select {
	case res := <-done:
		return res.url, res.err
	case <-time.After(10 * time.Second):
		t.Fatal("QRLogin did not return: it hung instead of reporting a result")
		return "", nil
	}
}

// qrTestServer serves the two passport endpoints QRLoginClient talks to.
func qrTestServer(t *testing.T, generate, poll http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/generate", generate)
	mux.HandleFunc("/poll", poll)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// pointQRLoginAt makes the QR flow talk to srv instead of passport.bilibili.com.
func pointQRLoginAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	previous := newQRLoginClient
	newQRLoginClient = func() *bili.QRLoginClient {
		c := bili.NewQRLoginClient()
		c.GenerateURL = srv.URL + "/generate"
		c.PollURL = srv.URL + "/poll"
		return c
	}
	t.Cleanup(func() { newQRLoginClient = previous })
}

// TestQRLoginReportsGenerationFailure pins IMPORTANT 1: the old select waited on
// a channel the goroutine only filled on the success path, so a failed QR-code
// generation left the request hanging with no result.
func TestQRLoginReportsGenerationFailure(t *testing.T) {
	srv := qrTestServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"code":-1,"message":"boom"}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Error("the poll must not start when generation failed")
		})
	pointQRLoginAt(t, srv)

	rt := newTestRuntime(t)
	url, err := runQRLogin(t, rt.qrLogin(server.NewHub()), context.Background())

	if err == nil {
		t.Fatalf("url = %q, err = nil, want the generate failure", url)
	}
	if !strings.Contains(err.Error(), "generate qrcode") {
		t.Fatalf("err = %q, want it to name the generate step", err)
	}
}

// TestQRLoginDeliversURLAndReportsTheScanOverSSE drives the happy path: the seam
// answers with the code's url without waiting for the scan, and the background
// poll saves the SESSDATA and announces the login over SSE.
func TestQRLoginDeliversURLAndReportsTheScanOverSSE(t *testing.T) {
	const qrURL = "https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=KEY1"
	var polls int32
	srv := qrTestServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"url":%q,"qrcode_key":"KEY1"}}`, qrURL)
		},
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&polls, 1)
			w.Header().Add("Set-Cookie", "SESSDATA=abc; Path=/; HttpOnly")
			_, _ = w.Write([]byte(`{"code":0,"message":"0","data":{"code":0,"url":"https://passport.bilibili.com/x"}}`))
		})
	pointQRLoginAt(t, srv)

	rt := newTestRuntime(t)
	hub := server.NewHub()
	events, cancel := hub.Subscribe()
	defer cancel()

	url, err := runQRLogin(t, rt.qrLogin(hub), context.Background())
	if err != nil {
		t.Fatalf("QRLogin: %v", err)
	}
	if url != qrURL {
		t.Fatalf("url = %q, want %q", url, qrURL)
	}

	select {
	case ev := <-events:
		data, ok := ev.Data.(map[string]bool)
		if ev.Type != "login" || !ok || !data["ok"] {
			t.Fatalf("event = %+v, want a login event with ok=true", ev)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no login event after the scan completed")
	}

	if n := atomic.LoadInt32(&polls); n == 0 {
		t.Error("the poll endpoint was never called")
	}
	saved, err := bili.LoadSESSDATA(rt.sessionPath())
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if saved != "abc" {
		t.Fatalf("saved SESSDATA = %q, want abc", saved)
	}
}

// TestQRLoginCancelsThePollWhenTheRequestGoesAway pins the cancellable-poll half
// of IMPORTANT 1: when the browser goes away before the url arrives, the request
// returns and the in-flight poll is cancelled instead of running on.
func TestQRLoginCancelsThePollWhenTheRequestGoesAway(t *testing.T) {
	entered := make(chan struct{}, 1)
	stopped := make(chan struct{})
	srv := qrTestServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			entered <- struct{}{}
			select {
			case <-r.Context().Done():
				close(stopped)
			case <-time.After(10 * time.Second):
			}
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Error("no poll may start once the request is gone")
		})
	pointQRLoginAt(t, srv)

	rt := newTestRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	seamDone := make(chan error, 1)
	go func() {
		_, err := rt.qrLogin(server.NewHub())(ctx)
		seamDone <- err
	}()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the QR flow never called the generate endpoint")
	}
	cancel()

	select {
	case err := <-seamDone:
		if err == nil {
			t.Fatal("QRLogin returned no error after its request was cancelled")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("QRLogin did not return after its request was cancelled")
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the in-flight poll was not cancelled together with the request")
	}
}
