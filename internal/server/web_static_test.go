package server

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// getEmbedded performs a GET straight against the embedded-asset handler, so
// these assertions are about staticHandler alone and not about the API mux in
// front of it.
func getEmbedded(target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	staticHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// TestStaticHandlerServesTheBuiltPageAtRoot covers the one path a browser hits
// first: "/" has to resolve to the committed index.html, and that file has to be
// the Vue shell (an #app mount point plus a bundled module script), not the
// pre-Vue hand-written page.
func TestStaticHandlerServesTheBuiltPageAtRoot(t *testing.T) {
	rec := getEmbedded("/")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200; the embedded index.html is missing", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`<div id="app">`, `src="/assets/`, `<script type="module"`} {
		if !strings.Contains(body, want) {
			t.Errorf("index.html does not contain %q; the embed is stale or not the built page", want)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", ct)
	}
}

// TestStaticHandlerServesTheBundleIndexPointsAt follows the hashed script name
// instead of naming it, because the name changes on every rebuild. A page whose
// script 404s renders nothing at all, which is the failure this catches.
func TestStaticHandlerServesTheBundleIndexPointsAt(t *testing.T) {
	index := getEmbedded("/").Body.String()
	m := regexp.MustCompile(`src="(/assets/[^"]+\.js)"`).FindStringSubmatch(index)
	if m == nil {
		t.Fatalf("index.html references no /assets/*.js bundle: %s", index)
	}

	rec := getEmbedded(m[1])
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", m[1], rec.Code)
	}
	if n := rec.Body.Len(); n < 10000 {
		t.Errorf("GET %s returned %d bytes; that is not the built bundle", m[1], n)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") && !strings.Contains(ct, "ecmascript") {
		t.Errorf("GET %s Content-Type = %q, want a JavaScript type", m[1], ct)
	}
}

// TestStaticHandlerReturns404ForTheOldAssetPaths is the removal check for this
// change: /app.js and /style.css were the two hand-written assets before the Vue
// rewrite and must not be served any more. A cached page still asking for them
// has to fail loudly rather than run a half-working old UI.
func TestStaticHandlerReturns404ForTheOldAssetPaths(t *testing.T) {
	paths := []string{
		"/app.js",
		"/style.css",
		"/assets/does-not-exist.js",
		"/nope",
	}
	for _, target := range paths {
		t.Run(target, func(t *testing.T) {
			if rec := getEmbedded(target); rec.Code != http.StatusNotFound {
				t.Fatalf("GET %s = %d, want 404", target, rec.Code)
			}
		})
	}
}
