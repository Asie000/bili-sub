package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

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

// Options wires the server to the store and the long-running operations. Every
// seam is optional: a nil seam makes its endpoint answer 501 instead of
// panicking. Hub may be nil, in which case the server creates one.
type Options struct {
	Store    *store.Store
	Probe    func(ctx context.Context, url string) (ProbeSummary, error)
	Download func(ctx context.Context, lang string, transcribe bool) (DownloadSummary, error)
	Export   func(ctx context.Context, formats []string) (string, error)
	Login    func(sessdata string) error
	QRLogin  func(ctx context.Context) (string, error)
	Hub      *Hub
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
// gets a 405 from net/http's mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", staticHandler())
	mux.HandleFunc("GET /api/videos", s.handleVideos)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("POST /api/probe", s.handleProbe)
	mux.HandleFunc("POST /api/download", s.handleDownload)
	mux.HandleFunc("POST /api/export", s.handleExport)
	mux.HandleFunc("POST /api/login/sessdata", s.handleSessdata)
	mux.HandleFunc("GET /api/login/qrcode", s.handleQRCode)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	return mux
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

// handleSessdata persists a pasted SESSDATA. The value is never echoed in the
// response or logged.
func (s *Server) handleSessdata(w http.ResponseWriter, r *http.Request) {
	if s.opts.Login == nil {
		writeError(w, http.StatusNotImplemented, "login 未启用")
		return
	}
	var req struct {
		SESSDATA string `json:"sessdata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if req.SESSDATA == "" {
		writeError(w, http.StatusBadRequest, "sessdata 不能为空")
		return
	}
	if err := s.opts.Login(req.SESSDATA); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	writeJSON(w, http.StatusOK, map[string]string{"url": qrURL})
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"响应序列化失败"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
