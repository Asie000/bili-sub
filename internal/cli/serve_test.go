package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/config"
	"bilibili-sub/internal/ir"
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

	if _, err := opts.Probe(context.Background(), "not-a-url", "", false, nil); !errors.Is(err, errOperationBusy) {
		t.Errorf("Probe error = %v, want errOperationBusy", err)
	}
	if _, err := opts.Download(context.Background(), "", false, "", "", "", nil); !errors.Is(err, errOperationBusy) {
		t.Errorf("Download error = %v, want errOperationBusy", err)
	}
	if _, err := opts.Export(context.Background(), []string{"srt"}, false, nil); !errors.Is(err, errOperationBusy) {
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

// TestServeDefaultsToLoopback: the server has no auth at all, so listening on
// every interface has to be something the user asks for, not the default. Binding
// 0.0.0.0 on a shared network hands anyone there the video list, the ability to
// start runs, and POST /api/reset.
func TestServeDefaultsToLoopback(t *testing.T) {
	addr, err := newServeCmd().Flags().GetString("addr")
	if err != nil {
		t.Fatalf("flag: %v", err)
	}
	if addr != "127.0.0.1:8080" {
		t.Errorf("--addr default = %q, want 127.0.0.1:8080", addr)
	}
}

// TestServeStopsWhenTheProcessIsAskedToStop pins the graceful shutdown. main
// now turns Ctrl+C into a context cancellation rather than a kill, so a server
// that only listens would become impossible to stop with the very key its own
// startup message tells the user to press.
func TestServeStopsWhenTheProcessIsAskedToStop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := executeContextOut(t, ctx, "serve", "--addr", "127.0.0.1:0")
		done <- err
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v after a stop request, want a clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve ignored the cancellation: Ctrl+C would appear to do nothing")
	}
}

// TestServeShutsDownWithAnOpenSSEStream pins the shutdown path with a browser
// watching. http.Server.Shutdown closes idle connections and then waits for the
// active ones, and it never cancels request contexts -- so the SSE handler, which
// only returns when its subscription ends, would hold the server "active" for the
// whole grace period and turn every Ctrl+C into a timeout error.
func TestServeShutsDownWithAnOpenSSEStream(t *testing.T) {
	// A port the OS just handed out, so the test knows where to connect.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	rt := newTestRuntime(t)
	defer rt.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- rt.serve(ctx, io.Discard, addr) }()

	base := "http://" + addr
	waitForServer(t, base)

	// Hold the event stream open, the way a browser page does from the moment it
	// loads. The body is deliberately left open until the end of the test.
	stream, err := http.Get(base + "/api/events")
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d, want 200", stream.StatusCode)
	}

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v after Ctrl+C, want a clean stop", err)
		}
		if elapsed := time.Since(start); elapsed > serveShutdownTimeout/2 {
			t.Errorf("serve took %v to stop with a stream open, want much less than the %v grace period", elapsed, serveShutdownTimeout)
		}
	case <-time.After(serveShutdownTimeout * 2):
		t.Fatal("serve did not stop while an SSE stream was open: Ctrl+C would appear to hang")
	}
}

// waitForServer polls until the listener answers, so the test does not depend on
// a sleep long enough for the slowest machine.
func waitForServer(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server never came up")
}

// CLI-side probe computes the 合集 hint, and the browser only ever sees what the
// seam puts into the summary. Dropping it here is invisible from either end.
// TestServeProbeSeamCarriesTheSeasonHint pins the mapping across the seam: the
// CLI-side probe computes the 合集 hint, and the browser only ever sees what the
// seam puts into the summary. Dropping it here is invisible from either end.
func TestServeProbeSeamCarriesTheSeasonHint(t *testing.T) {
	const bvid = "BV1u84y1i7u6"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/view":
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"bvid":%q,"cid":1001,"title":"视频","pubdate":1,"duration":10,"owner":{"mid":406571547,"name":"up"},"pages":[{"cid":1001,"page":1,"part":"P1"}],"ugc_season":{"id":814546,"mid":406571547,"title":"100个办公神技","ep_count":31,"sections":[{"episodes":[{"bvid":%q}]}]}}}`, bvid, bvid)
		case "/x/player/v2":
			_, _ = w.Write([]byte(`{"code":0,"message":"0","data":{"subtitle":{"subtitles":[]}}}`))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	overrideEnumerator(t, srv.URL+"/x/web-interface/view", srv.URL+"/x/web-interface/nav")
	overrideProber(t, srv.URL+"/x/player/v2")

	rt := newTestRuntime(t)
	defer rt.close()
	st, err := rt.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	opts := rt.serveOptions(st, server.NewHub())

	summary, err := opts.Probe(context.Background(), bvid, "", false, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for _, want := range []string{"100个办公神技", "sid=814546"} {
		if !strings.Contains(summary.Hint, want) {
			t.Errorf("summary.Hint = %q, want it to contain %q", summary.Hint, want)
		}
	}
}

// TestServeDownloadSeamTagsTheTranscribeLeg pins that the ASR leg's ticks are not
// reported as more downloads. The browser labels its progress panel from this op,
// so an untagged tick tells the user 获取字幕 while the machine is transcribing --
// and rewrites the download leg's counts with its own.
func TestServeDownloadSeamTagsTheTranscribeLeg(t *testing.T) {
	rt := newTestRuntime(t)
	defer rt.close()
	// Seed into the runtime's own data dir: newTestRuntime picks its own temp
	// directory, so a row planted anywhere else is invisible to it.
	seedVideoRow(t, rt.cfg.DataDir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 88, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})
	overrideAudioPipeline(t, newAudioServer(t).URL)
	overrideASRBackend(t, &fakeASR{segments: []ir.Segment{{StartMs: 0, EndMs: 900, Text: "转写内容"}}}, nil)

	st, err := rt.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	opts := rt.serveOptions(st, server.NewHub())

	// The seam ticks from its own goroutine (the per-video loops call it
	// synchronously), so a plain append is enough here.
	type tick struct {
		op          string
		done, total int
	}
	var ticks []tick
	if _, err := opts.Download(context.Background(), "", true, defaultASRLang, "", "",
		func(p server.Progress) { ticks = append(ticks, tick{p.Op, p.Done, p.Total}) }); err != nil {
		t.Fatalf("Download: %v", err)
	}

	var transcribe []tick
	for _, tk := range ticks {
		switch tk.op {
		case opTranscribe:
			transcribe = append(transcribe, tk)
		case opDownload:
			t.Errorf("tick %+v: nothing was downloadable here, so the download leg must not report progress", tk)
		default:
			t.Errorf("tick %+v: unexpected op", tk)
		}
	}
	if len(transcribe) == 0 {
		t.Fatalf("ticks = %+v, want the ASR leg's ticks tagged %q rather than %q", ticks, opTranscribe, opDownload)
	}
	// The leg reports where it starts and where it ends; the end is what the
	// panel fills to 100% on.
	last := transcribe[len(transcribe)-1]
	if last.done != 1 || last.total != 1 {
		t.Errorf("last transcribe tick = %+v, want 1/1 for the one video", last)
	}
}
