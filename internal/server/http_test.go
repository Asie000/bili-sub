package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bilibili-sub/internal/store"
)

// newTestServer returns a Server backed by an in-memory store. The caller's
// seams are used as-is, so no test ever touches the network.
func newTestServer(t *testing.T, opts Options) *Server {
	t.Helper()
	if opts.Store == nil {
		opts.Store = openStore(t)
	}
	return New(opts)
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open(:memory:): %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedVideo(t *testing.T, st *store.Store, bvid, state string) {
	t.Helper()
	if _, err := st.UpsertVideo(&store.Video{Bvid: bvid, Cid: 1, Title: bvid, State: state}); err != nil {
		t.Fatalf("upsert %s: %v", bvid, err)
	}
}

// serve drives one request through the server's handler.
func serve(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestVideosEndpointEmptyStoreIsEmptyArray(t *testing.T) {
	s := newTestServer(t, Options{})

	rec := serve(t, s, http.MethodGet, "/api/videos", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "[]" {
		t.Fatalf("body = %q, want exactly [] (not null)", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// TestVideosEndpointReturnsStoredRows pins the field names the embedded page
// reads: /api/videos marshals store.Video directly, so the JSON keys are the Go
// field names (Title, Author, Pubdate, State, Lang, IsCharging, ...).
func TestVideosEndpointReturnsStoredRows(t *testing.T) {
	st := openStore(t)
	if _, err := st.UpsertVideo(&store.Video{
		Bvid: "BV1uv411q7Mv", Cid: 1, Title: "标题", Author: "某UP",
		State: store.StateDownloadable, Lang: "zh-Hans",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	s := New(Options{Store: st})

	rec := serve(t, s, http.MethodGet, "/api/videos", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var got []store.Video
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(got) != 1 {
		t.Fatalf("videos = %v, want exactly one", got)
	}
	if got[0].Title != "标题" || got[0].Author != "某UP" || got[0].State != store.StateDownloadable || got[0].Lang != "zh-Hans" {
		t.Fatalf("video = %+v, want the seeded title/author/state/lang", got[0])
	}
}

func TestStatsEndpointCountsStates(t *testing.T) {
	st := openStore(t)
	seedVideo(t, st, "BV1aaaa", store.StatePending)
	seedVideo(t, st, "BV1bbbb", store.StatePending)
	seedVideo(t, st, "BV1cccc", store.StateFailed)
	s := New(Options{Store: st})

	rec := serve(t, s, http.MethodGet, "/api/stats", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var got map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got[store.StatePending] != 2 {
		t.Errorf("pending = %d, want 2 (%v)", got[store.StatePending], got)
	}
	if got[store.StateFailed] != 1 {
		t.Errorf("failed = %d, want 1 (%v)", got[store.StateFailed], got)
	}
	if got[store.StateFetched] != 0 {
		t.Errorf("fetched = %d, want 0 (%v)", got[store.StateFetched], got)
	}
}

func TestProbeEndpointPassesURLToSeam(t *testing.T) {
	const target = "https://www.bilibili.com/video/BV1uv411q7Mv"
	var gotURL string
	s := newTestServer(t, Options{
		Probe: func(_ context.Context, url string) (ProbeSummary, error) {
			gotURL = url
			return ProbeSummary{Probed: 1, Downloadable: 1}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe", fmt.Sprintf(`{"url":%q}`, target))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if gotURL != target {
		t.Fatalf("seam received %q, want %q", gotURL, target)
	}
	var got ProbeSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got.Probed != 1 || got.Downloadable != 1 {
		t.Fatalf("summary = %+v, want probed=1 downloadable=1", got)
	}
}

func TestProbeEndpointRejectsEmptyURL(t *testing.T) {
	s := newTestServer(t, Options{
		Probe: func(_ context.Context, _ string) (ProbeSummary, error) {
			t.Error("probe seam must not be called for an empty url")
			return ProbeSummary{}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":""}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestProbeEndpointWithoutSeamReturns501(t *testing.T) {
	s := newTestServer(t, Options{})

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":"BV1uv411q7Mv"}`)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("code = %d, want 501", rec.Code)
	}
}

func TestDownloadEndpointPassesLangAndTranscribe(t *testing.T) {
	var gotLang string
	var gotTranscribe bool
	s := newTestServer(t, Options{
		Download: func(_ context.Context, lang string, transcribe bool) (DownloadSummary, error) {
			gotLang, gotTranscribe = lang, transcribe
			return DownloadSummary{Fetched: 2, Failed: 1}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/download", `{"lang":"en","transcribe":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if gotLang != "en" || !gotTranscribe {
		t.Fatalf("seam received lang=%q transcribe=%v, want en/true", gotLang, gotTranscribe)
	}
}

func TestExportEndpointReturnsZip(t *testing.T) {
	const zipPath = "data/exports/20261008-2.zip"
	s := newTestServer(t, Options{
		Export: func(_ context.Context, formats []string) (string, error) {
			if len(formats) != 1 || formats[0] != "srt" {
				t.Errorf("formats = %v, want [srt]", formats)
			}
			return zipPath, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/export", `{"formats":["srt"]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got["zip"] != zipPath {
		t.Fatalf("zip = %q, want %q", got["zip"], zipPath)
	}
}

func TestExportEndpointRejectsEmptyFormats(t *testing.T) {
	s := newTestServer(t, Options{
		Export: func(_ context.Context, _ []string) (string, error) {
			t.Error("export seam must not be called with no formats")
			return "", nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/export", `{"formats":[]}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestSessdataEndpointNeverEchoesTheValue(t *testing.T) {
	const secret = "s3cr3t-SESSDATA-value"
	var got string
	s := newTestServer(t, Options{
		Login: func(sessdata string) error {
			got = sessdata
			return nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/login/sessdata", fmt.Sprintf(`{"sessdata":%q}`, secret))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got != secret {
		t.Fatalf("seam received %q, want the posted value", got)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("response %q must not echo the sessdata value", rec.Body.String())
	}
	var got2 map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &got2); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if !got2["ok"] {
		t.Fatalf("body = %q, want {\"ok\":true}", rec.Body.String())
	}
}

func TestSessdataEndpointRejectsEmptyValue(t *testing.T) {
	s := newTestServer(t, Options{
		Login: func(string) error {
			t.Error("login seam must not be called for an empty sessdata")
			return nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/login/sessdata", `{"sessdata":""}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestQRCodeEndpointReturnsURL(t *testing.T) {
	const qrURL = "https://www.bilibili.com/h5/app/bili-qrcode?qrcode_key=abc"
	s := newTestServer(t, Options{
		QRLogin: func(context.Context) (string, error) { return qrURL, nil },
	})

	rec := serve(t, s, http.MethodGet, "/api/login/qrcode", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got["url"] != qrURL {
		t.Fatalf("url = %q, want %q", got["url"], qrURL)
	}
	if !strings.HasPrefix(got["png"], "data:image/png;base64,") {
		t.Fatalf("png = %.40q..., want a data:image/png;base64 URL", got["png"])
	}
	if len(got["png"]) < 200 {
		t.Fatalf("png payload too small (%d bytes), QR was probably not rendered", len(got["png"]))
	}
}

func TestEventsEndpointStreamsBroadcast(t *testing.T) {
	hub := NewHub()
	s := newTestServer(t, Options{Hub: hub})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	// The handler subscribes before flushing its headers, so once GET returns
	// the subscriber exists and one Broadcast is guaranteed to be delivered.
	hub.Broadcast(Event{Type: "progress", Data: map[string]string{"msg": "ok"}})

	type line struct {
		text string
		err  error
	}
	lines := make(chan line, 1)
	go func() {
		text, err := bufio.NewReader(resp.Body).ReadString('\n')
		lines <- line{text, err}
	}()

	select {
	case got := <-lines:
		if got.err != nil {
			t.Fatalf("read event: %v", got.err)
		}
		if !strings.HasPrefix(got.text, "data: ") {
			t.Fatalf("first line = %q, want a data: line", got.text)
		}
		if !strings.Contains(got.text, `"type":"progress"`) || !strings.Contains(got.text, `"msg":"ok"`) {
			t.Fatalf("event line = %q, want the broadcast type and payload", got.text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a broadcast event")
	}
}

// TestZeroValueHubIsUsable pins MINOR 4 for the exported Hub type: a caller may
// pass Options{Hub: &Hub{}} and must not get a nil-map panic out of Subscribe.
func TestZeroValueHubIsUsable(t *testing.T) {
	var h Hub

	events, cancel := h.Subscribe()
	defer cancel()
	h.Broadcast(Event{Type: "probe", Data: map[string]int{"probed": 1}})

	select {
	case ev := <-events:
		if ev.Type != "probe" {
			t.Fatalf("event type = %q, want probe", ev.Type)
		}
	default:
		t.Fatal("a broadcast to a zero-value Hub's subscriber was dropped")
	}
}

// TestZeroValueServerEventsDoesNotPanic pins MINOR 4: a Server that was not
// built by New has no hub when /api/events arrives, so the handler must create
// one instead of dereferencing nil.
func TestZeroValueServerEventsDoesNotPanic(t *testing.T) {
	var s Server // deliberately not built by New

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(rec, req)
	}()
	// The stream ends as soon as its request context is cancelled, so a broken
	// handler fails the wait below instead of hanging the suite.
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleEvents did not return after its request was cancelled")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
}

// TestServerWithNilHubBroadcastsSafely covers the other broadcast path: a Server
// whose hub was never set (in-package construction, or a New regression) must
// not panic when a seam result is published.
func TestServerWithNilHubBroadcastsSafely(t *testing.T) {
	s := &Server{opts: Options{
		Probe: func(context.Context, string) (ProbeSummary, error) {
			return ProbeSummary{Probed: 1}, nil
		},
	}}

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":"BV1uv411q7Mv"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestEmbeddedUIExposesExportFormats pins MINOR 6: the page must offer every
// export format and app.js must post the checked subset rather than a
// hardcoded list.
func TestEmbeddedUIExposesExportFormats(t *testing.T) {
	s := newTestServer(t, Options{})

	index := serve(t, s, http.MethodGet, "/", "").Body.String()
	app := serve(t, s, http.MethodGet, "/app.js", "").Body.String()

	if !strings.Contains(app, "export-format") {
		t.Error("app.js does not read the export-format checkboxes")
	}
	if strings.Contains(app, `["srt"]`) {
		t.Error("app.js still hardcodes the srt-only export list")
	}
	for _, format := range []string{"srt", "txt", "json", "md"} {
		if !strings.Contains(index, `value="`+format+`"`) {
			t.Errorf("index.html offers no %q export checkbox", format)
		}
	}
}
