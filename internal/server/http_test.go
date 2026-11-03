package server

import (
	"bufio"
	"context"
	"database/sql"
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
	// The server refuses bodies that are not JSON, on purpose; see decodeJSON.
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// TestMutatingEndpointsRequireJSON pins the CSRF guard. The server has no auth, and
// a page the user visits can still POST to loopback with a "simple" request --
// empty body, text/plain -- which browsers send without a preflight. Requiring the
// JSON content type makes the browser ask permission first, and this server never
// answers, so a cross-origin page cannot start a download run on the machine.
func TestMutatingEndpointsRequireJSON(t *testing.T) {
	called := false
	s := newTestServer(t, Options{
		Download: func(_ context.Context, _ string, _ bool, _, _, _ string, _ func(Progress)) (DownloadSummary, error) {
			called = true
			return DownloadSummary{Fetched: 1}, nil
		},
	})

	// What a plain <form> or a no-cors fetch produces.
	req := httptest.NewRequest(http.MethodPost, "/api/download", strings.NewReader(""))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if called {
		t.Error("the download seam ran: a page the user visits could start a run")
	}
}

func TestVideosEndpointEmptyStoreHasEmptyItems(t *testing.T) {
	s := newTestServer(t, Options{})

	rec := serve(t, s, http.MethodGet, "/api/videos", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("body = %q, want items to be [] (not null)", rec.Body.String())
	}
	var got VideoPage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got.Items == nil {
		t.Fatal("items must be a non-nil empty slice")
	}
	if len(got.Items) != 0 || got.Total != 0 {
		t.Fatalf("page = %+v, want zero items and total", got)
	}
	if got.Offset != 0 || got.Limit != 100 {
		t.Fatalf("offset/limit = %d/%d, want 0/100 defaults", got.Offset, got.Limit)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}
}

// TestVideosEndpointReturnsStoredRows pins the field names the embedded page
// reads: each page item marshals store.Video directly, so the JSON keys are the
// Go field names (Title, Author, Pubdate, State, Lang, IsCharging, ...).
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
	var got VideoPage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %v, want exactly one", got.Items)
	}
	if got.Total != 1 {
		t.Fatalf("total = %d, want 1", got.Total)
	}
	v := got.Items[0]
	if v.Title != "标题" || v.Author != "某UP" || v.State != store.StateDownloadable || v.Lang != "zh-Hans" {
		t.Fatalf("video = %+v, want the seeded title/author/state/lang", v)
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
		Probe: func(_ context.Context, url, kind string, _ bool, _ func(Progress)) (ProbeSummary, error) {
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

func TestProbeEndpointPassesForceToSeam(t *testing.T) {
	var gotForce bool
	s := newTestServer(t, Options{
		Probe: func(_ context.Context, _, _ string, force bool, _ func(Progress)) (ProbeSummary, error) {
			gotForce = force
			return ProbeSummary{Probed: 1, Skipped: 2}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":"BV1uv411q7Mv","force":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if !gotForce {
		t.Fatal("seam received force=false, want true")
	}
	var got ProbeSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got.Skipped != 2 {
		t.Fatalf("skipped = %d, want 2 (the summary must carry the new field)", got.Skipped)
	}
}

// TestProbeEndpointReturnsTheHint pins that the 合集 hint survives the API layer:
// the browser has no terminal to read, so a summary that dropped the field would
// leave the page silently unable to tell the user which link to paste next.
func TestProbeEndpointReturnsTheHint(t *testing.T) {
	const hint = "提示：这条视频属于合集《100个办公神技》，共 31 条。"
	s := newTestServer(t, Options{
		Probe: func(_ context.Context, _, _ string, _ bool, _ func(Progress)) (ProbeSummary, error) {
			return ProbeSummary{Probed: 1, Downloadable: 1, Hint: hint}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":"BV1u84y1i7u6"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got ProbeSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got.Hint != hint {
		t.Errorf("hint = %q, want %q", got.Hint, hint)
	}
}

// TestProbeEndpointReturnsPartialReason pins that a seam which did work and then
// stopped (an expired session, a rate limit) comes back as 200 with the counts
// and the reason, not a 500 that throws both away -- the page has no terminal to
// read the CLI's own output.
func TestProbeEndpointReturnsPartialReason(t *testing.T) {
	const reason = "登录态失效，已停止探测。请先运行 `bili-sub login`。"
	s := newTestServer(t, Options{
		Probe: func(_ context.Context, _, _ string, _ bool, _ func(Progress)) (ProbeSummary, error) {
			return ProbeSummary{Probed: 3, Reason: reason}, errors.New("bilibili code=-101 message=账号未登录")
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":"BV1u84y1i7u6"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got ProbeSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got.Reason != reason {
		t.Errorf("reason = %q, want %q", got.Reason, reason)
	}
	if got.Probed != 3 {
		t.Errorf("probed = %d, want the counts kept", got.Probed)
	}
}

func TestProbeEndpointRejectsEmptyURL(t *testing.T) {
	s := newTestServer(t, Options{
		Probe: func(_ context.Context, _, _ string, _ bool, _ func(Progress)) (ProbeSummary, error) {
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
		Probe: func(context.Context, string, string, bool, func(Progress)) (ProbeSummary, error) {
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

// TestProbeResolvesShortLinkBeforeParsing pins that a b23.tv link is expanded
// before parsing, so both the probe seam and the returned source/kind describe
// the real URL rather than the short one.
func TestProbeResolvesShortLinkBeforeParsing(t *testing.T) {
	var gotURL string
	s := newTestServer(t, Options{
		ResolveURL: func(_ context.Context, _ string) (string, error) {
			return "https://space.bilibili.com/12345", nil
		},
		Probe: func(_ context.Context, url, _ string, _ bool, _ func(Progress)) (ProbeSummary, error) {
			gotURL = url
			return ProbeSummary{Probed: 1}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":"https://b23.tv/aBcDeF"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if gotURL != "https://space.bilibili.com/12345" {
		t.Fatalf("seam got url %q, want the resolved URL", gotURL)
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
}

// TestProbeEndpointExplicitKindOverridesAutoDetection pins the type selector:
// a bare numeric id is ambiguous, so an explicit fav kind must resolve it even
// though ParseURL alone would reject it.
func TestProbeEndpointExplicitKindOverridesAutoDetection(t *testing.T) {
	s := newTestServer(t, Options{
		Probe: func(context.Context, string, string, bool, func(Progress)) (ProbeSummary, error) {
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
		Probe: func(context.Context, string, string, bool, func(Progress)) (ProbeSummary, error) {
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
		Probe: func(_ context.Context, _, _ string, _ bool, onProgress func(Progress)) (ProbeSummary, error) {
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
		Download: func(_ context.Context, _ string, _ bool, _, _, _ string, onProgress func(Progress)) (DownloadSummary, error) {
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

// TestTranscribeEndpointPassesInputs pins POST /api/transcribe: the seam gets
// the page's asr_lang/model/source, and the counts come back as JSON.
func TestTranscribeEndpointPassesInputs(t *testing.T) {
	var gotLang, gotModel, gotSource string
	s := newTestServer(t, Options{
		Transcribe: func(_ context.Context, asrLang, model, source string, onProgress func(Progress)) (TranscribeSummary, error) {
			gotLang, gotModel, gotSource = asrLang, model, source
			onProgress(Progress{Op: "transcribe", Done: 1, Total: 1})
			return TranscribeSummary{Total: 1, Transcribed: 1}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/transcribe", `{"asr_lang":"en","model":"small","source":"12345"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if gotLang != "en" || gotModel != "small" || gotSource != "12345" {
		t.Fatalf("seam got lang=%q model=%q source=%q, want en/small/12345", gotLang, gotModel, gotSource)
	}
	var summary TranscribeSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if summary.Transcribed != 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v, want 1 transcribed", summary)
	}
}

// TestTranscribeEndpointKeepsCountsOnPartWayError pins that a run which
// transcribed some videos and then failed (an expired session, a dead engine)
// answers 200 with the counts and the reason rather than a bare 500 that would
// hide the work already done.
func TestTranscribeEndpointKeepsCountsOnPartWayError(t *testing.T) {
	s := newTestServer(t, Options{
		Transcribe: func(_ context.Context, _, _, _ string, _ func(Progress)) (TranscribeSummary, error) {
			return TranscribeSummary{Total: 3, Transcribed: 1}, errors.New("登录态失效")
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/transcribe", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var summary TranscribeSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if summary.Transcribed != 1 || summary.Error == "" {
		t.Fatalf("summary = %+v, want the count kept and the reason attached", summary)
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
	var filtered VideoPage
	if err := json.Unmarshal(rec.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].Bvid != "BV1aaaa" || filtered.Total != 1 {
		t.Fatalf("filtered = %+v, want only BV1aaaa with total 1", filtered)
	}

	rec = serve(t, s, http.MethodGet, "/api/videos", "")
	var all VideoPage
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(all.Items) != 2 || all.Total != 2 {
		t.Fatalf("all = %+v, want both rows without a source filter", all)
	}
}

func TestDownloadEndpointPassesLangTranscribeAndSource(t *testing.T) {
	var gotLang, gotAsrLang, gotModel, gotSource string
	var gotTranscribe bool
	s := newTestServer(t, Options{
		Download: func(_ context.Context, lang string, transcribe bool, asrLang, model, source string, _ func(Progress)) (DownloadSummary, error) {
			gotLang, gotTranscribe, gotAsrLang, gotModel, gotSource = lang, transcribe, asrLang, model, source
			return DownloadSummary{Fetched: 2, Failed: 1}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/download", `{"lang":"en","transcribe":true,"asr_lang":"zh","model":"large-v3-turbo","source":"12345"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if gotLang != "en" || !gotTranscribe || gotAsrLang != "zh" || gotModel != "large-v3-turbo" || gotSource != "12345" {
		t.Fatalf("seam received lang=%q transcribe=%v asrLang=%q model=%q source=%q, want en/true/zh/large-v3-turbo/12345",
			gotLang, gotTranscribe, gotAsrLang, gotModel, gotSource)
	}
}

// TestDownloadPartialSuccessKeepsTheCounts pins the reporting rule for a run that
// did work and then hit a leg-level failure (an expired session, a worker that
// cannot start): the page must still see what was accomplished, with the reason
// attached, instead of an empty 500 that says nothing happened.
func TestDownloadPartialSuccessKeepsTheCounts(t *testing.T) {
	s := newTestServer(t, Options{
		Download: func(_ context.Context, _ string, _ bool, _, _, _ string, _ func(Progress)) (DownloadSummary, error) {
			return DownloadSummary{Fetched: 3, Transcribed: 1, Failed: 0}, errors.New("音频接口要求登录态")
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/download", `{"transcribe":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 so the counts survive (body %s)", rec.Code, rec.Body.String())
	}
	var got DownloadSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Fetched != 3 || got.Transcribed != 1 {
		t.Errorf("summary = %+v, want the counts preserved", got)
	}
	if !strings.Contains(got.Error, "登录") {
		t.Errorf("error field = %q, want the failure reason", got.Error)
	}
}

// TestDownloadEndpointSurfacesFirstErrorWithoutAnError pins the all-videos-failed
// case: downloadSubtitles returns nil (a per-video failure is not a leg-level
// error), so the only way the reason reaches the page is the summary's Error.
func TestDownloadEndpointSurfacesFirstErrorWithoutAnError(t *testing.T) {
	const reason = "HTTP 412（B站风控限流）：请求过于频繁被临时拦截"
	s := newTestServer(t, Options{
		Download: func(_ context.Context, _ string, _ bool, _, _, _ string, _ func(Progress)) (DownloadSummary, error) {
			return DownloadSummary{Failed: 2, Error: reason}, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/download", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got DownloadSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error != reason {
		t.Errorf("error = %q, want %q", got.Error, reason)
	}
}

// TestDownloadFailsWhenNothingHappened pins the other half: a run that produced
// no counts at all is still a 500, so a real failure is not disguised as success.
func TestDownloadFailsWhenNothingHappened(t *testing.T) {
	s := newTestServer(t, Options{
		Download: func(_ context.Context, _ string, _ bool, _, _, _ string, _ func(Progress)) (DownloadSummary, error) {
			return DownloadSummary{}, errors.New("store 坏了")
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/download", `{}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestExportEndpointReturnsZipAndPassesFlags(t *testing.T) {
	const zipPath = "data/exports/20261008-2.zip"
	var gotIncludeExported bool
	s := newTestServer(t, Options{
		Export: func(_ context.Context, formats []string, includeExported bool, _ func(Progress)) (string, error) {
			if len(formats) != 1 || formats[0] != "srt" {
				t.Errorf("formats = %v, want [srt]", formats)
			}
			gotIncludeExported = includeExported
			return zipPath, nil
		},
	})

	rec := serve(t, s, http.MethodPost, "/api/export", `{"formats":["srt"],"include_exported":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if !gotIncludeExported {
		t.Fatalf("seam received include_exported=false, want true")
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
		Export: func(_ context.Context, _ []string, _ bool, _ func(Progress)) (string, error) {
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

// TestHubCloseEndsEveryStream pins the primitive the shutdown relies on: Close
// ends the subscriptions (that is what makes an SSE handler return), it is safe to
// call twice, a subscriber's own cancel afterwards must not panic on an already
// closed channel, and a handler arriving mid-shutdown is not left hanging.
func TestHubCloseEndsEveryStream(t *testing.T) {
	hub := NewHub()
	first, cancelFirst := hub.Subscribe()
	second, _ := hub.Subscribe()

	hub.Close()
	hub.Close() // idempotent

	for i, ch := range []chan Event{first, second} {
		select {
		case _, open := <-ch:
			if open {
				t.Errorf("subscription %d delivered an event after Close", i)
			}
		case <-time.After(time.Second):
			t.Errorf("subscription %d was not closed by Close", i)
		}
	}
	// In the real handler this runs from a defer, so it happens after Close.
	cancelFirst()

	late, lateCancel := hub.Subscribe()
	defer lateCancel()
	select {
	case _, open := <-late:
		if open {
			t.Error("a subscriber registered after Close got an event")
		}
	case <-time.After(time.Second):
		t.Error("a subscriber registered after Close was left waiting")
	}

	// Broadcast walks the subscriber map; an empty one after Close means it must
	// not reach a closed channel.
	hub.Broadcast(Event{Type: "probe"})
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
		Probe: func(context.Context, string, string, bool, func(Progress)) (ProbeSummary, error) {
			return ProbeSummary{Probed: 1}, nil
		},
	}}

	rec := serve(t, s, http.MethodPost, "/api/probe", `{"url":"BV1uv411q7Mv"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestEmbeddedUIFixesExportToTxt pins the product decision that the page's
// export is a plain-text reading copy: the format is fixed to txt rather than
// chosen from checkboxes. The server API still accepts other formats.
func TestEmbeddedUIFixesExportToTxt(t *testing.T) {
	s := newTestServer(t, Options{})

	index := serve(t, s, http.MethodGet, "/", "").Body.String()
	app := serve(t, s, http.MethodGet, "/app.js", "").Body.String()

	if !strings.Contains(app, `return ["txt"]`) {
		t.Error("app.js should fix the export format to txt")
	}
	if strings.Contains(index, "export-format") {
		t.Error("index.html should no longer offer export format checkboxes")
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
		Probe: func(context.Context, string, string, bool, func(Progress)) (ProbeSummary, error) {
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
		Probe: func(context.Context, string, string, bool, func(Progress)) (ProbeSummary, error) {
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
		Probe: func(context.Context, string, string, bool, func(Progress)) (ProbeSummary, error) {
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

func TestVideosEndpointUnknownSourceHasEmptyItems(t *testing.T) {
	st := openStore(t)
	seedVideo(t, st, "BV1uv411q7Mv", store.StatePending)
	s := newTestServer(t, Options{Store: st})

	rec := serve(t, s, http.MethodGet, "/api/videos?source=nope", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("body = %q, want an empty items array", rec.Body.String())
	}
	var got VideoPage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(got.Items) != 0 || got.Total != 0 {
		t.Fatalf("page = %+v, want no rows for an unknown source", got)
	}
}

// TestVideosEndpointPagination pins GET /api/videos' offset/limit contract: a
// window of the id-ordered rows with the full total, a small limit, and a page
// entirely past the end that still marshals an empty items array.
func TestVideosEndpointPagination(t *testing.T) {
	st := openStore(t)
	for i := 0; i < 5; i++ {
		seedVideo(t, st, fmt.Sprintf("BV%d", i), store.StatePending)
	}
	s := New(Options{Store: st})

	t.Run("window", func(t *testing.T) {
		rec := serve(t, s, http.MethodGet, "/api/videos?offset=1&limit=2", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200", rec.Code)
		}
		var got VideoPage
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		if got.Total != 5 || got.Offset != 1 || got.Limit != 2 {
			t.Fatalf("page meta = %+v, want total=5 offset=1 limit=2", got)
		}
		if len(got.Items) != 2 || got.Items[0].Bvid != "BV1" || got.Items[1].Bvid != "BV2" {
			t.Fatalf("items = %+v, want BV1,BV2", got.Items)
		}
	})

	t.Run("past the end is an empty array", func(t *testing.T) {
		rec := serve(t, s, http.MethodGet, "/api/videos?offset=99", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"items":[]`) {
			t.Fatalf("body = %q, want an empty items array", rec.Body.String())
		}
		var got VideoPage
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		if got.Total != 5 || len(got.Items) != 0 {
			t.Fatalf("page = %+v, want total=5 and no items", got)
		}
	})
}

// TestVideosEndpointLimitClamped pins the limit bounds: an oversized limit is
// capped at 500, a zero/negative limit floors at 1, and a non-numeric limit
// falls back to the default 100. A negative offset is treated as 0.
func TestVideosEndpointLimitClamped(t *testing.T) {
	s := newTestServer(t, Options{})
	tests := []struct {
		query     string
		wantLimit int
		wantOff   int
	}{
		{query: "?limit=1000", wantLimit: 500},
		{query: "?limit=0", wantLimit: 1},
		{query: "?limit=-5", wantLimit: 1},
		{query: "?limit=abc", wantLimit: 100},
		{query: "?offset=-3", wantLimit: 100, wantOff: 0},
		{query: "?offset=abc", wantLimit: 100, wantOff: 0},
	}
	for _, tc := range tests {
		rec := serve(t, s, http.MethodGet, "/api/videos"+tc.query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code = %d, want 200", tc.query, rec.Code)
		}
		var got VideoPage
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: decode %q: %v", tc.query, rec.Body.String(), err)
		}
		if got.Limit != tc.wantLimit || got.Offset != tc.wantOff {
			t.Errorf("%s: limit/offset = %d/%d, want %d/%d", tc.query, got.Limit, got.Offset, tc.wantLimit, tc.wantOff)
		}
	}
}

// TestResetEndpointRequiresConfirm pins POST /api/reset's guard: a missing or
// false confirm is a 400 and touches no data.
func TestResetEndpointRequiresConfirm(t *testing.T) {
	st := openStore(t)
	seedVideo(t, st, "BV1aaaa", store.StateFetched)
	s := New(Options{Store: st})

	for _, body := range []string{`{}`, `{"confirm":false}`, `{"confirm":"yes"}`} {
		rec := serve(t, s, http.MethodPost, "/api/reset", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: code = %d, want 400", body, rec.Code)
		}
	}
	if n, err := st.CountVideos("", ""); err != nil || n != 1 {
		t.Fatalf("confirm guard deleted rows: count=%d err=%v", n, err)
	}
}

// TestResetEndpointDeletesVideosAndBroadcasts pins the confirmed path: it deletes
// every video and transcript, reports the count, and announces a `reset` event
// so other open pages reload their now-empty list.
func TestResetEndpointDeletesVideosAndBroadcasts(t *testing.T) {
	st := openStore(t)
	id, err := st.UpsertVideo(&store.Video{Bvid: "BV1aaaa", Cid: 1, State: store.StateFetched})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.SaveTranscript(id, "bilibili", "zh-Hans", `{"segments":[]}`); err != nil {
		t.Fatalf("save transcript: %v", err)
	}
	seedVideo(t, st, "BV1bbbb", store.StatePending)
	hub := NewHub()
	events, cancel := hub.Subscribe()
	defer cancel()
	s := New(Options{Store: st, Hub: hub})

	rec := serve(t, s, http.MethodPost, "/api/reset", `{"confirm":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got struct {
		OK      bool  `json:"ok"`
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if !got.OK || got.Deleted != 2 {
		t.Fatalf("body = %+v, want ok/deleted=2", got)
	}
	if n, err := st.CountVideos("", ""); err != nil || n != 0 {
		t.Fatalf("videos left after reset: count=%d err=%v", n, err)
	}
	if _, _, _, err := st.GetTranscript(id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("transcript left after reset: err = %v, want sql.ErrNoRows", err)
	}

	select {
	case ev := <-events:
		if ev.Type != "reset" {
			t.Fatalf("event = %+v, want a reset event", ev)
		}
		data, ok := ev.Data.(map[string]int64)
		if !ok || data["deleted"] != 2 {
			t.Fatalf("event data = %#v, want deleted=2", ev.Data)
		}
	default:
		t.Fatal("no reset event was broadcast")
	}
}

func TestProbeEndpointRejectsUnknownKind(t *testing.T) {
	s := newTestServer(t, Options{
		Probe: func(context.Context, string, string, bool, func(Progress)) (ProbeSummary, error) {
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
