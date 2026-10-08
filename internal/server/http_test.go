package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
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

func TestProbeEndpointPassesURLAndKindToSeam(t *testing.T) {
	const target = "https://www.bilibili.com/video/BV1uv411q7Mv"
	var gotURL, gotKind string
	s := newTestServer(t, Options{
		Probe: func(_ context.Context, url, kind string, _ func(Progress)) (ProbeSummary, error) {
			gotURL, gotKind = url, kind
			return ProbeSummary{Probed: 1, Downloadable: 1}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe", fmt.Sprintf(`{"url":%q,"kind":"single"}`, target))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if gotURL != target || gotKind != "single" {
		t.Fatalf("seam received url=%q kind=%q, want %q/single", gotURL, gotKind, target)
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
		Probe: func(_ context.Context, _, _ string, _ func(Progress)) (ProbeSummary, error) {
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

// TestProbeEndpointResponseCarriesSourceAndKind pins the page-facing contract:
// the response names the parsed source key and its canonical kind so the page
// can scope its list to the source it just probed.
func TestProbeEndpointResponseCarriesSourceAndKind(t *testing.T) {
	s := newTestServer(t, Options{
		Probe: func(context.Context, string, string, func(Progress)) (ProbeSummary, error) {
			return ProbeSummary{Probed: 2}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":"https://space.bilibili.com/12345"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ProbeSummary
		Source string `json:"source"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got.Source != "12345" || got.Kind != "space" {
		t.Fatalf("response = %+v, want source=12345 kind=space", got)
	}
	if got.Probed != 2 {
		t.Fatalf("probed = %d, want the summary fields still present", got.Probed)
	}
}

// TestProbeEndpointExplicitKindOverridesAutoDetection pins the type selector:
// a bare numeric id is ambiguous, so an explicit fav kind must resolve it even
// though ParseURL alone would reject it.
func TestProbeEndpointExplicitKindOverridesAutoDetection(t *testing.T) {
	s := newTestServer(t, Options{
		Probe: func(context.Context, string, string, func(Progress)) (ProbeSummary, error) {
			return ProbeSummary{Probed: 1}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":"999","kind":"fav"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Source string `json:"source"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got.Source != "999" || got.Kind != "fav" {
		t.Fatalf("response = %+v, want source=999 kind=fav", got)
	}
}

func TestProbeEndpointRejectsUnparseableURL(t *testing.T) {
	s := newTestServer(t, Options{
		Probe: func(context.Context, string, string, func(Progress)) (ProbeSummary, error) {
			t.Error("probe seam must not be called for an unparseable url")
			return ProbeSummary{}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":"https://example.com/foo"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

// TestProbeEndpointBroadcastsProgress pins that the seam's onProgress ticks are
// streamed as `progress` events tagged with the probe op, including a start
// (0/N) and the final (N/N) tick the page uses to finish the progress bar.
func TestProbeEndpointBroadcastsProgress(t *testing.T) {
	hub := NewHub()
	events, cancel := hub.Subscribe()
	defer cancel()
	s := newTestServer(t, Options{
		Hub: hub,
		Probe: func(_ context.Context, _, _ string, onProgress func(Progress)) (ProbeSummary, error) {
			onProgress(Progress{Op: "probe", Done: 0, Total: 3})
			onProgress(Progress{Op: "probe", Done: 3, Total: 3})
			return ProbeSummary{Probed: 3}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":"BV1uv411q7Mv"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// The handler broadcasts synchronously, so every event is already buffered.
	var progress []Progress
drain:
	for {
		select {
		case ev := <-events:
			if ev.Type != "progress" {
				continue
			}
			p, ok := ev.Data.(Progress)
			if !ok {
				t.Fatalf("progress data has type %T, want Progress", ev.Data)
			}
			progress = append(progress, p)
		default:
			break drain
		}
	}
	if len(progress) != 2 {
		t.Fatalf("progress events = %v, want a start and a final tick", progress)
	}
	if progress[0] != (Progress{Op: "probe", Done: 0, Total: 3}) {
		t.Fatalf("first tick = %+v, want the start tick probe 0/3", progress[0])
	}
	if progress[1] != (Progress{Op: "probe", Done: 3, Total: 3}) {
		t.Fatalf("last tick = %+v, want the final tick probe 3/3", progress[1])
	}
}

// TestDownloadEndpointBroadcastsProgress pins that POST /api/download streams
// the seam's ticks as `progress` events tagged with the download op, including
// the start (0/2) and the final (2/2) tick.
func TestDownloadEndpointBroadcastsProgress(t *testing.T) {
	hub := NewHub()
	events, cancel := hub.Subscribe()
	defer cancel()
	s := newTestServer(t, Options{
		Hub: hub,
		Download: func(_ context.Context, _ string, _ bool, onProgress func(Progress)) (DownloadSummary, error) {
			onProgress(Progress{Op: "download", Done: 0, Total: 2})
			onProgress(Progress{Op: "download", Done: 2, Total: 2})
			return DownloadSummary{Fetched: 2}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/download", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var progress []Progress
drain:
	for {
		select {
		case ev := <-events:
			if ev.Type != "progress" {
				continue
			}
			p, ok := ev.Data.(Progress)
			if !ok {
				t.Fatalf("progress data has type %T, want Progress", ev.Data)
			}
			progress = append(progress, p)
		default:
			break drain
		}
	}
	if len(progress) != 2 {
		t.Fatalf("progress events = %v, want a start and a final tick", progress)
	}
	if progress[0] != (Progress{Op: "download", Done: 0, Total: 2}) {
		t.Fatalf("first tick = %+v, want the start tick download 0/2", progress[0])
	}
	if progress[1] != (Progress{Op: "download", Done: 2, Total: 2}) {
		t.Fatalf("last tick = %+v, want the final tick download 2/2", progress[1])
	}
}

// TestVideosEndpointFiltersBySource pins GET /api/videos?source=: it returns only
// the rows of the named source, while no source returns every row.
func TestVideosEndpointFiltersBySource(t *testing.T) {
	st := openStore(t)
	if _, err := st.UpsertVideo(&store.Video{Bvid: "BV1aaaa", Cid: 1, SourceKey: "12345", State: store.StatePending}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := st.UpsertVideo(&store.Video{Bvid: "BV1bbbb", Cid: 1, SourceKey: "99999", State: store.StatePending}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	s := New(Options{Store: st})

	rec := serve(t, s, http.MethodGet, "/api/videos?source=12345", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var filtered []store.Video
	if err := json.Unmarshal(rec.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(filtered) != 1 || filtered[0].Bvid != "BV1aaaa" {
		t.Fatalf("filtered = %+v, want only BV1aaaa", filtered)
	}

	rec = serve(t, s, http.MethodGet, "/api/videos", "")
	var all []store.Video
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %+v, want both rows without a source filter", all)
	}
}

func TestDownloadEndpointPassesLangAndTranscribe(t *testing.T) {
	var gotLang string
	var gotTranscribe bool
	s := newTestServer(t, Options{
		Download: func(_ context.Context, lang string, transcribe bool, _ func(Progress)) (DownloadSummary, error) {
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
		Export: func(_ context.Context, formats []string, _ func(Progress)) (string, error) {
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
		Export: func(_ context.Context, _ []string, _ func(Progress)) (string, error) {
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
		Login: func(sessdata, cookie string) error {
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

func TestSessdataEndpointPassesFullCookieWithoutEchoing(t *testing.T) {
	const cookie = "SESSDATA=abc%2Cdef; buvid3=B3; bili_ticket=XYZ"
	var gotCookie string
	s := newTestServer(t, Options{
		Login: func(sessdata, cookie string) error {
			gotCookie = cookie
			return nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/login/sessdata", fmt.Sprintf(`{"cookie":%q}`, cookie))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if gotCookie != cookie {
		t.Fatalf("seam received cookie %q, want the posted value", gotCookie)
	}
	if strings.Contains(rec.Body.String(), "bili_ticket") || strings.Contains(rec.Body.String(), "SESSDATA") {
		t.Fatalf("response %q must not echo the cookie value", rec.Body.String())
	}
}

func TestSessdataEndpointRejectsEmptyValue(t *testing.T) {
	s := newTestServer(t, Options{
		Login: func(string, string) error {
			t.Error("login seam must not be called when both values are empty")
			return nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/login/sessdata", `{"sessdata":"","cookie":""}`)

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
		Probe: func(context.Context, string, string, func(Progress)) (ProbeSummary, error) {
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

func TestLoginStatusEndpoint(t *testing.T) {
	s := newTestServer(t, Options{
		LoginStatus: func(context.Context) (LoginState, error) {
			return LoginState{LoggedIn: true, Uname: "测试用户"}, nil
		},
	})
	rec := serve(t, s, http.MethodGet, "/api/login/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var got LoginState
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if !got.LoggedIn || got.Uname != "测试用户" {
		t.Fatalf("got %+v, want logged in as 测试用户", got)
	}
}

func TestLoginStatusFailureReportsLoggedOutWithoutLeakingTheError(t *testing.T) {
	s := newTestServer(t, Options{
		LoginStatus: func(context.Context) (LoginState, error) {
			return LoginState{}, fmt.Errorf("network down for 1234567890")
		},
	})
	rec := serve(t, s, http.MethodGet, "/api/login/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "network down") {
		t.Fatalf("internal error leaked to the client: %s", rec.Body.String())
	}
	var got LoginState
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.LoggedIn {
		t.Fatalf("got %+v, want not logged in", got)
	}
}

func TestCORSPreflightFromBilibiliIsAllowed(t *testing.T) {
	s := newTestServer(t, Options{
		Probe: func(context.Context, string, string, func(Progress)) (ProbeSummary, error) {
			return ProbeSummary{}, nil
		},
	})
	req := httptest.NewRequest(http.MethodOptions, "/api/probe", nil)
	req.Header.Set("Origin", "https://www.bilibili.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://www.bilibili.com" {
		t.Fatalf("allow-origin = %q, want the bilibili origin", got)
	}
}

func TestCORSRejectsForeignOrigin(t *testing.T) {
	s := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodOptions, "/api/probe", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin was granted CORS: %q", got)
	}
}

// TestHandoffEndpointLogsInProbesAndHidesTheCookie pins the extension bridge:
// the posted cookie reaches Login with its SESSDATA derived, the posted URL
// reaches Probe, the probe summary is returned, and no cookie value leaks into
// the response body.
func TestHandoffEndpointSavesLoginAndReturnsURLWithoutProbing(t *testing.T) {
	const cookie = "SESSDATA=abc%2Cdef; buvid3=B3; bili_ticket=XYZ"
	const target = "https://www.bilibili.com/video/BV1uv411q7Mv"

	var gotSessdata, gotCookie string
	loginCalls := 0
	probed := false
	s := newTestServer(t, Options{
		Login: func(sessdata, c string) error {
			loginCalls++
			gotSessdata, gotCookie = sessdata, c
			return nil
		},
		LoginStatus: func(context.Context) (LoginState, error) {
			return LoginState{LoggedIn: true, Uname: "测试用户"}, nil
		},
		Probe: func(context.Context, string, string, func(Progress)) (ProbeSummary, error) {
			probed = true
			return ProbeSummary{}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/handoff",
		fmt.Sprintf(`{"cookie":%q,"url":%q}`, cookie, target))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if loginCalls != 1 {
		t.Fatalf("Login called %d times, want 1", loginCalls)
	}
	if probed {
		t.Fatal("the extension handoff must NOT trigger a probe")
	}
	if gotCookie != cookie {
		t.Fatalf("Login received cookie %q, want the posted full cookie", gotCookie)
	}
	if gotSessdata != "abc%2Cdef" {
		t.Fatalf("Login received sessdata %q, want the value derived from the cookie", gotSessdata)
	}
	var got HandoffResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if !got.OK || !got.LoggedIn || got.Uname != "测试用户" {
		t.Fatalf("response = %+v, want ok/logged_in/测试用户", got)
	}
	if got.URL != target {
		t.Fatalf("url = %q, want %q echoed back for the input box", got.URL, target)
	}
	for _, secret := range []string{"abc%2Cdef", "SESSDATA", "bili_ticket"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("response %q leaks %q", rec.Body.String(), secret)
		}
	}
}

// TestHandoffEndpointReportsLoginErrorWithoutProbing pins that a failing Login is
// reported but still does not trigger a probe: the extension only sends.
func TestHandoffEndpointReportsLoginErrorWithoutProbing(t *testing.T) {
	probed := false
	s := newTestServer(t, Options{
		Login: func(string, string) error { return errors.New("sessdata 校验失败") },
		Probe: func(context.Context, string, string, func(Progress)) (ProbeSummary, error) {
			probed = true
			return ProbeSummary{Probed: 2}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/handoff",
		`{"cookie":"SESSDATA=abc; buvid3=B3","url":"https://www.bilibili.com/video/BV1"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if probed {
		t.Fatal("a failed login must not trigger a probe either")
	}
	var got HandoffResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got.LoggedIn {
		t.Fatalf("response = %+v, want logged_in=false", got)
	}
	if !strings.Contains(got.Error, "校验失败") {
		t.Fatalf("error = %q, want the Login error reported", got.Error)
	}
}

func TestHandoffEndpointRejectsEmptyCookieAndURL(t *testing.T) {
	s := newTestServer(t, Options{})

	rec := serve(t, s, http.MethodPost, "/api/handoff", `{}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

// TestCORSPreflightFromExtensionIsAllowed pins that the Firefox and Chrome
// extension origins may call the API, with their own origin echoed back.
func TestCORSPreflightFromExtensionIsAllowed(t *testing.T) {
	s := newTestServer(t, Options{})
	for _, origin := range []string{"moz-extension://abcd-1234", "chrome-extension://abcdefghijklmnop"} {
		req := httptest.NewRequest(http.MethodOptions, "/api/handoff", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: code = %d, want 204", origin, rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("%s: allow-origin = %q, want the extension origin echoed", origin, got)
		}
	}
}

func TestHandoffCORSRejectsForeignOrigin(t *testing.T) {
	s := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodPost, "/api/handoff", strings.NewReader(`{"url":"https://www.bilibili.com/video/BV1"}`))
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin was granted CORS: %q", got)
	}
}

func TestVideosEndpointUnknownSourceIsEmptyArray(t *testing.T) {
	st := openStore(t)
	seedVideo(t, st, "BV1uv411q7Mv", store.StatePending)
	s := newTestServer(t, Options{Store: st})

	rec := serve(t, s, http.MethodGet, "/api/videos?source=nope", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("body = %q, want []", got)
	}
}

func TestProbeEndpointRejectsUnknownKind(t *testing.T) {
	s := newTestServer(t, Options{
		Probe: func(context.Context, string, string, func(Progress)) (ProbeSummary, error) {
			t.Fatal("the probe seam must not be called for an unknown kind")
			return ProbeSummary{}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe",
		`{"url":"https://www.bilibili.com/video/BV1uv411q7Mv","kind":"video"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}
