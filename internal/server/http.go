package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/store"
)

// ProbeSummary is the outcome of one probe run, as returned by POST /api/probe.
type ProbeSummary struct {
	Probed         int `json:"probed"`
	Downloadable   int `json:"downloadable"`
	NoSubtitle     int `json:"no_subtitle"`
	ChargingLocked int `json:"charging_locked"`
	Failed         int `json:"failed"`
}

// DownloadSummary is the outcome of one download run, as returned by
// POST /api/download.
type DownloadSummary struct {
	Fetched int `json:"fetched"`
	Failed  int `json:"failed"`
}

// LoginState is what GET /api/login/status reports.
type LoginState struct {
	LoggedIn bool   `json:"logged_in"`
	Uname    string `json:"uname,omitempty"`
}

// HandoffResponse is what POST /api/handoff returns. It reports the resulting
// login state and echoes back the received URL so the page can prefill its
// input; the extension does not trigger a probe. Neither the posted cookie nor
// its SESSDATA is ever copied into this type.
type HandoffResponse struct {
	OK       bool   `json:"ok"`
	LoggedIn bool   `json:"logged_in"`
	Uname    string `json:"uname,omitempty"`
	URL      string `json:"url,omitempty"`
	Error    string `json:"error"`
}

// Options wires the server to the store and the long-running operations. Every
// seam is optional: a nil seam makes its endpoint answer 501 instead of
// panicking. Hub may be nil, in which case the server creates one.
type Options struct {
	Store       *store.Store
	Probe       func(ctx context.Context, url string) (ProbeSummary, error)
	Download    func(ctx context.Context, lang string, transcribe bool) (DownloadSummary, error)
	Export      func(ctx context.Context, formats []string) (string, error)
	Login       func(sessdata, cookie string) error
	LoginStatus func(ctx context.Context) (LoginState, error)
	QRLogin     func(ctx context.Context) (string, error)
	Hub         *Hub
}

// Server serves the JSON API, the SSE stream and the embedded UI.
type Server struct {
	opts Options

	// hub is the event hub shared by the broadcasters and /api/events.
	// hubOnce creates one for a Server that was not built by New, so a
	// zero-value Server streams instead of panicking on a nil hub.
	hubOnce sync.Once
	hub     *Hub
}

func New(opts Options) *Server {
	if opts.Hub == nil {
		opts.Hub = NewHub()
	}
	return &Server{opts: opts, hub: opts.Hub}
}

// eventHub returns the server's event hub, creating it on first use.
func (s *Server) eventHub() *Hub {
	s.hubOnce.Do(func() {
		if s.hub == nil {
			s.hub = NewHub()
		}
	})
	return s.hub
}

// Handler builds the route table. Method-specific patterns mean a wrong method
// gets a 405 from net/http's mux. The whole table is wrapped in the CORS
// middleware so the bookmarklet, which runs on a bilibili page, can POST here.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", staticHandler())
	mux.HandleFunc("GET /api/videos", s.handleVideos)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("POST /api/probe", s.handleProbe)
	mux.HandleFunc("POST /api/download", s.handleDownload)
	mux.HandleFunc("POST /api/export", s.handleExport)
	mux.HandleFunc("POST /api/login/sessdata", s.handleSessdata)
	mux.HandleFunc("POST /api/handoff", s.handleHandoff)
	mux.HandleFunc("GET /api/login/status", s.handleLoginStatus)
	mux.HandleFunc("GET /api/login/qrcode", s.handleQRCode)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	return withCORS(mux)
}

// allowedCORSOrigin reports whether an Origin may call this API from a browser
// page. The bookmarklet runs inside a bilibili page, so bilibili hosts (plus
// localhost) are allowed; the Firefox/Chrome extension origins are allowed too
// so the extension can post directly. A wildcard would let any website drive
// this local tool, so it is deliberately not used.
func allowedCORSOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	// The browser extension posts from its own origin, which browsers do not
	// route through a host permission; allow both extension schemes.
	switch u.Scheme {
	case "moz-extension", "chrome-extension":
		return u.Host != ""
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	return host == "bilibili.com" || strings.HasSuffix(host, ".bilibili.com")
}

// withCORS answers preflight requests and tags allowed origins. Requests with a
// disallowed origin still run (same-origin/curl callers send no Origin) but get
// no CORS grant, so the browser blocks the cross-site read.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); allowedCORSOrigin(origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleVideos returns every stored video. store.Video has no JSON tags, so the
// embedded page reads the Go field names (Title, Author, Pubdate, State, Lang,
// IsCharging, ...). ListVideos already normalizes zero rows to a non-nil slice,
// and the guard keeps the response `[]` rather than `null`.
func (s *Server) handleVideos(w http.ResponseWriter, r *http.Request) {
	if s.opts.Store == nil {
		writeError(w, http.StatusNotImplemented, "store 未配置")
		return
	}
	videos, err := s.opts.Store.ListVideos("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if videos == nil {
		videos = []store.Video{}
	}
	writeJSON(w, http.StatusOK, videos)
}

// handleStats counts the stored videos per state.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.opts.Store == nil {
		writeError(w, http.StatusNotImplemented, "store 未配置")
		return
	}
	videos, err := s.opts.Store.ListVideos("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	stats := make(map[string]int)
	for _, v := range videos {
		stats[v.State]++
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if s.opts.Probe == nil {
		writeError(w, http.StatusNotImplemented, "probe 未启用")
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		writeError(w, http.StatusBadRequest, "url 不能为空")
		return
	}
	summary, err := s.opts.Probe(r.Context(), req.URL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.eventHub().Broadcast(Event{Type: "probe", Data: summary})
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if s.opts.Download == nil {
		writeError(w, http.StatusNotImplemented, "download 未启用")
		return
	}
	var req struct {
		Lang       string `json:"lang"`
		Transcribe bool   `json:"transcribe"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	summary, err := s.opts.Download(r.Context(), req.Lang, req.Transcribe)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.eventHub().Broadcast(Event{Type: "download", Data: summary})
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if s.opts.Export == nil {
		writeError(w, http.StatusNotImplemented, "export 未启用")
		return
	}
	var req struct {
		Formats []string `json:"formats"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if len(req.Formats) == 0 {
		writeError(w, http.StatusBadRequest, "formats 不能为空")
		return
	}
	zipPath, err := s.opts.Export(r.Context(), req.Formats)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.eventHub().Broadcast(Event{Type: "export", Data: map[string]string{"zip": zipPath}})
	writeJSON(w, http.StatusOK, map[string]string{"zip": zipPath})
}

// handleSessdata persists a pasted SESSDATA and/or a full browser Cookie. At
// least one must be non-empty. Neither value is echoed in the response or
// logged.
func (s *Server) handleSessdata(w http.ResponseWriter, r *http.Request) {
	if s.opts.Login == nil {
		writeError(w, http.StatusNotImplemented, "login 未启用")
		return
	}
	var req struct {
		SESSDATA string `json:"sessdata"`
		Cookie   string `json:"cookie"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if req.SESSDATA == "" && req.Cookie == "" {
		writeError(w, http.StatusBadRequest, "sessdata 与 cookie 不能同时为空")
		return
	}
	if err := s.opts.Login(req.SESSDATA, req.Cookie); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleHandoff receives the browser extension's full Cookie plus the current
// tab URL. A cookie logs in with its SESSDATA derived so both halves persist;
// a failing Login is reported but does not stop the probe, which runs when a
// URL is given. The cookie and SESSDATA values are never echoed or logged.
// handleHandoff receives the browser extension's full Cookie plus the current
// page URL. It only stores the login and hands the URL back to the page; the
// user still chooses when to probe. A probe here would re-list every video the
// local database already holds, which is not what the user asked for.
func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cookie string `json:"cookie"`
		URL    string `json:"url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if req.Cookie == "" && req.URL == "" {
		writeError(w, http.StatusBadRequest, "cookie 与 url 不能同时为空")
		return
	}

	resp := HandoffResponse{OK: true, URL: req.URL}
	if req.Cookie != "" && s.opts.Login != nil {
		if err := s.opts.Login(bili.ExtractSESSDATA(req.Cookie), req.Cookie); err != nil {
			resp.Error = "登录失败: " + err.Error()
		} else {
			resp.LoggedIn = true
			if s.opts.LoginStatus != nil {
				if state, err := s.opts.LoginStatus(r.Context()); err == nil {
					resp.LoggedIn = state.LoggedIn
					resp.Uname = state.Uname
				}
			}
		}
	}
	s.eventHub().Broadcast(Event{Type: "handoff", Data: resp})
	writeJSON(w, http.StatusOK, resp)
}

// handleLoginStatus reports whether the stored SESSDATA is still accepted and,
// when it is, the signed-in account name. A failed check (network hiccup, bad
// cookie) is reported as "not logged in" rather than a 5xx: the page only needs
// the boolean.
func (s *Server) handleLoginStatus(w http.ResponseWriter, r *http.Request) {
	if s.opts.LoginStatus == nil {
		writeError(w, http.StatusNotImplemented, "登录状态查询未启用")
		return
	}
	state, err := s.opts.LoginStatus(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, LoginState{})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleQRCode(w http.ResponseWriter, r *http.Request) {
	if s.opts.QRLogin == nil {
		writeError(w, http.StatusNotImplemented, "扫码登录未启用")
		return
	}
	qrURL, err := s.opts.QRLogin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The page shows the code as an image; the reachability of the scan is
	// reported asynchronously over SSE by the QRLogin seam.
	png, err := qrDataURL(qrURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "生成二维码图片失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": qrURL, "png": png})
}

// handleEvents streams Hub events as SSE. It subscribes before flushing the
// response headers so that a client which has received the headers is
// guaranteed to receive subsequently broadcast events. The hub is created on
// first use, so a zero-value Server streams rather than panicking.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "当前服务器不支持流式响应")
		return
	}
	events, cancel := s.eventHub().Subscribe()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-events:
			if !open {
				return
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// maxRequestBody caps how much of a request body is read; every endpoint takes a
// small JSON object.
const maxRequestBody = 1 << 20

// decodeJSON decodes a JSON request body. An empty body leaves dst at its zero
// value so `POST /api/download` needs no body.
func decodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	return json.Unmarshal(body, dst)
}

// writeJSON writes v as a JSON body. Marshalling the server's own types cannot
// fail, but a seam-supplied value could, so the failure path writes a JSON error
// instead of a truncated body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"响应序列化失败"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
