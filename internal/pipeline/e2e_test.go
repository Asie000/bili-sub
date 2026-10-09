package pipeline

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/export"
	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/store"
	"bilibili-sub/internal/util"
)

// Fixture of the fake B站 video: two pages, one usable zh-CN subtitle track.
const (
	e2eBvid     = "BV1uv411q7Mv"
	e2eTitle    = "端到端测试视频"
	e2eAuthor   = "测试UP主"
	e2ePubdate  = int64(1700000000)
	e2eDuration = 300
	e2ePage1Cid = int64(1001)
	e2ePage2Cid = int64(1002)
)

// guardedTransport fails any request that does not go to one of the fake
// servers, so this test cannot reach the real network even if the pipeline
// grows a new hard-coded B站 URL.
type guardedTransport struct{ hosts map[string]bool }

func (g guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !g.hosts[req.URL.Host] {
		return nil, fmt.Errorf("e2e: request escaped the fake servers: %s", req.URL)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// TestEndToEndPipelineNoNetwork drives the real pipeline end to end against
// fake B站 servers: ResolveVideo → one store.Video per page → probe → download
// → export, then asserts on the .srt on disk, the stored IR and the zip.
func TestEndToEndPipelineNoNetwork(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()

	// The subtitle host is TLS on purpose: subtitle_url is protocol-relative
	// ("//host/...") and the downloader upgrades it to "https://", so only a TLS
	// server can prove the upgrade really happened.
	var subMu sync.Mutex
	var subRequests []string
	subSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subMu.Lock()
		subRequests = append(subRequests, requestURL(r))
		subMu.Unlock()
		if r.URL.Path != "/subtitle.json" {
			t.Errorf("subtitle server: unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, subtitleBodyJSON)
	}))
	defer subSrv.Close()
	protocolRelativeTrack := strings.TrimPrefix(subSrv.URL, "https:") + "/subtitle.json" // "//host/..." as B站 serves it

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"wbi_img":{"img_url":%q,"sub_url":%q}}}`,
				"https://i0.hdslb.com/bfs/wbi/"+testImgKey+".png",
				"https://i0.hdslb.com/bfs/wbi/"+testSubKey+".png")
		case "/x/web-interface/view":
			if got := r.URL.Query().Get("bvid"); got != e2eBvid {
				t.Errorf("view bvid=%q want %q", got, e2eBvid)
			}
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{
				"bvid":%q,"aid":123,"cid":%d,"title":%q,"pubdate":%d,"duration":%d,
				"owner":{"mid":42,"name":%q},
				"pages":[{"cid":%d,"page":1,"part":"P1 开场","duration":150},
				         {"cid":%d,"page":2,"part":"P2 结尾","duration":150}]}}`,
				e2eBvid, e2ePage1Cid, e2eTitle, e2ePubdate, e2eDuration, e2eAuthor,
				e2ePage1Cid, e2ePage2Cid)
		case "/x/player/wbi/v2":
			q := r.URL.Query()
			// The player endpoint is WBI-signed, so reaching it at all proves
			// the nav-keyed signer was built from the fake nav response.
			if q.Get("w_rid") == "" || q.Get("wts") == "" {
				t.Errorf("player request unsigned: w_rid=%q wts=%q", q.Get("w_rid"), q.Get("wts"))
			}
			switch q.Get("cid") {
			case strconv.FormatInt(e2ePage1Cid, 10), strconv.FormatInt(e2ePage2Cid, 10):
			default:
				t.Errorf("player cid=%q want one of the two page cids", q.Get("cid"))
			}
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"subtitle":{"subtitles":[
				{"lan":"zh-CN","lan_doc":"中文（简体）","subtitle_url":%q}]}}}`, protocolRelativeTrack)
		default:
			t.Errorf("api server: unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiSrv.Close()

	client := bili.NewClient("", nopLimiter{})
	client.HTTP.Transport = guardedTransport{hosts: map[string]bool{
		strings.TrimPrefix(apiSrv.URL, "http://"): true,
	}}

	st, err := store.Open(filepath.Join(workDir, "e2e.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	en := NewEnumerator(client)
	en.ViewBaseURL = apiSrv.URL + "/x/web-interface/view"
	en.NavURL = apiSrv.URL + "/x/web-interface/nav"
	en.SpaceBaseURL = apiSrv.URL + "/x/space/wbi/arc/search"
	en.FavBaseURL = apiSrv.URL + "/x/v3/fav/resource/list"

	info, err := en.ResolveVideo(ctx, e2eBvid)
	if err != nil {
		t.Fatalf("ResolveVideo: %v", err)
	}
	if info.Title != e2eTitle || info.Owner.Name != e2eAuthor ||
		info.Pubdate != e2ePubdate || info.Duration != e2eDuration {
		t.Fatalf("ResolveVideo info=%+v want the fake video's title/owner/pubdate/duration", info)
	}
	if len(info.Pages) != 2 {
		t.Fatalf("pages=%d want 2", len(info.Pages))
	}

	videos := e2eExpandPages(info, e2eBvid)
	if len(videos) != len(info.Pages) {
		t.Fatalf("expanded pages=%d want one store.Video per PageInfo (%d)", len(videos), len(info.Pages))
	}
	for i := range videos {
		id, err := st.UpsertVideo(&videos[i])
		if err != nil {
			t.Fatalf("UpsertVideo %s cid=%d: %v", videos[i].Bvid, videos[i].Cid, err)
		}
		videos[i].ID = id
	}
	if rows, err := st.ListVideos(""); err != nil || len(rows) != 2 {
		t.Fatalf("stored videos=%d err=%v want 2 rows", len(rows), err)
	}

	prober := NewProber(client, st, en)
	prober.PlayerBaseURL = apiSrv.URL + "/x/player/wbi/v2"
	outDir := filepath.Join(workDir, "out")
	dl := NewDownloader(client, st, en, outDir)
	dl.PlayerBaseURL = apiSrv.URL + "/x/player/wbi/v2"
	// The subtitle URL is normalized to https, so the downloader needs the TLS
	// test server's client to trust its certificate.
	dl.HTTP = subSrv.Client()

	wantSegments := []ir.Segment{
		{StartMs: 0, EndMs: 1500, Text: "你好"},
		{StartMs: 1500, EndMs: 2500, Text: "世界"},
	}
	for _, v := range videos {
		label := fmt.Sprintf("page %d (cid %d)", v.PageIndex, v.Cid)

		res, err := prober.ProbeVideo(ctx, v)
		if err != nil {
			t.Fatalf("%s: ProbeVideo: %v", label, err)
		}
		if res.ChargingLocked || !res.HasSubtitle || !res.CanDownload {
			t.Fatalf("%s: probe result=%+v want downloadable", label, res)
		}
		if stored := videoByID(t, st, v.ID); stored.State != store.StateDownloadable || stored.Lang != "zh-CN" {
			t.Fatalf("%s: state=%q lang=%q want %q/zh-CN", label, stored.State, stored.Lang, store.StateDownloadable)
		}

		doc, err := dl.FetchSubtitle(ctx, v, "zh-CN")
		if err != nil {
			t.Fatalf("%s: FetchSubtitle: %v", label, err)
		}
		if !reflect.DeepEqual(doc.Segments, wantSegments) {
			t.Fatalf("%s: segments=%+v want %+v", label, doc.Segments, wantSegments)
		}

		// The stored transcript is the canonical IR, timeline included, so the
		// export command can rebuild the .srt from the database later.
		_, _, irJSON, err := st.GetTranscript(v.ID)
		if err != nil {
			t.Fatalf("%s: GetTranscript: %v", label, err)
		}
		var storedDoc ir.Document
		if err := json.Unmarshal([]byte(irJSON), &storedDoc); err != nil {
			t.Fatalf("%s: unmarshal stored ir_json %s: %v", label, irJSON, err)
		}
		if len(storedDoc.Segments) != len(wantSegments) {
			t.Fatalf("%s: stored segments=%+v want %+v", label, storedDoc.Segments, wantSegments)
		}
		for i, seg := range storedDoc.Segments {
			if seg.EndMs <= seg.StartMs {
				t.Fatalf("%s: stored segment[%d]=%+v lost its timeline", label, i, seg)
			}
		}
		if storedDoc.Segments[1].StartMs != 1500 || storedDoc.Segments[1].EndMs != 2500 {
			t.Fatalf("%s: stored second segment=%+v want 1500→2500ms", label, storedDoc.Segments[1])
		}

		// Only pages after the first get the _P{n} suffix, so both pages must
		// land in distinct files.
		stem := util.SanitizeFilename(v.Title, v.Bvid)
		if v.PageIndex > 1 {
			stem += "_P" + strconv.Itoa(v.PageIndex)
		}
		srtBytes, err := os.ReadFile(filepath.Join(outDir, util.SanitizeDirName(v.Author), stem+".srt"))
		if err != nil {
			t.Fatalf("%s: read srt: %v", label, err)
		}
		srt := string(srtBytes)
		if !strings.Contains(srt, "00:00:00,000 --> 00:00:01,500") {
			t.Errorf("%s: srt missing its timeline:\n%s", label, srt)
		}
		if !strings.Contains(srt, "你好") || !strings.Contains(srt, "世界") {
			t.Errorf("%s: srt missing text:\n%s", label, srt)
		}
	}

	subMu.Lock()
	subURLs := append([]string(nil), subRequests...)
	subMu.Unlock()
	if len(subURLs) != len(videos) {
		t.Fatalf("subtitle requests=%v want %d (one body per page)", subURLs, len(videos))
	}
	// The handler is the only place the TLS state is visible, so this is what
	// proves the protocol-relative URL was upgraded to https before fetching.
	wantSubURL := subSrv.URL + "/subtitle.json"
	for _, u := range subURLs {
		if u != wantSubURL {
			t.Errorf("subtitle fetched from %q want %q (protocol-relative URL upgraded to https)", u, wantSubURL)
		}
	}

	// Pack what is in the database, not the in-memory documents, so the archive
	// also proves the IR round-tripped through sqlite. Pages of one video share
	// bvid and title, so each page needs its own key and title suffix — exactly
	// what `bili-sub export` does.
	docs := make(map[string]*ir.Document, len(videos))
	for _, v := range videos {
		_, _, irJSON, err := st.GetTranscript(v.ID)
		if err != nil {
			t.Fatalf("export %s: GetTranscript: %v", v.Bvid, err)
		}
		var doc ir.Document
		if err := json.Unmarshal([]byte(irJSON), &doc); err != nil {
			t.Fatalf("export %s: unmarshal IR: %v", v.Bvid, err)
		}
		key := v.Bvid
		if v.PageIndex > 1 {
			suffix := "_P" + strconv.Itoa(v.PageIndex)
			key += suffix
			doc.Title += suffix
		}
		docs[key] = &doc
	}
	formats := []string{export.FormatSRT, export.FormatText}
	zipPath, err := export.Pack(filepath.Join(workDir, "exports"), time.Now(), docs, formats)
	if err != nil {
		t.Fatalf("export.Pack: %v", err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip %s: %v", zipPath, err)
	}
	defer zr.Close()

	folder := strings.TrimSuffix(filepath.Base(zipPath), ".zip")
	entries := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		entries[f.Name] = f
	}
	if len(entries) != len(videos)*len(formats) {
		t.Fatalf("zip has %d entries want %d (one per page per format)", len(entries), len(videos)*len(formats))
	}
	for name := range entries {
		if !strings.HasPrefix(name, folder+"/") {
			t.Errorf("zip entry %q is not nested under %q/", name, folder)
		}
	}
	srtEntry := folder + "/" + util.SanitizeDirName(e2eAuthor) + "/" + util.SanitizeFilename(e2eTitle, e2eBvid) + ".srt"
	f, ok := entries[srtEntry]
	if !ok {
		t.Fatalf("zip entries=%v want %q", zipNames(entries), srtEntry)
	}
	rc, err := f.Open()
	if err != nil {
		t.Fatalf("open zip entry %q: %v", srtEntry, err)
	}
	defer rc.Close()
	zipped, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read zip entry %q: %v", srtEntry, err)
	}
	if !strings.Contains(string(zipped), "00:00:00,000 --> 00:00:01,500") {
		t.Errorf("zip entry %q lost its timeline:\n%s", srtEntry, zipped)
	}
}

// requestURL reconstructs the absolute URL of an inbound request: a handler only
// sees the request target, while the scheme lives in r.TLS.
func requestURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + r.URL.RequestURI()
}

// e2eExpandPages mirrors cli.expandPages (Ruling F): one store.Video per page,
// or a single page using the video-level cid when the video has no pages list.
func e2eExpandPages(vi *bili.VideoInfo, sourceKey string) []store.Video {
	charging := vi.IsUpowerExclusive || vi.Rights.IsUpowerExclusive || vi.IsUpowerPlay || vi.IsUpowerPreview
	pages := vi.Pages
	if len(pages) == 0 {
		pages = []bili.PageInfo{{Cid: vi.Cid, Page: 1}}
	}
	out := make([]store.Video, 0, len(pages))
	for _, p := range pages {
		out = append(out, store.Video{
			Bvid:       vi.Bvid,
			Cid:        p.Cid,
			Aid:        vi.Aid,
			Author:     vi.Owner.Name,
			Title:      vi.Title,
			Pubdate:    vi.Pubdate,
			Duration:   vi.Duration,
			PageIndex:  p.Page,
			Part:       p.Part,
			IsCharging: charging,
			State:      store.StatePending,
			SourceKey:  sourceKey,
		})
	}
	return out
}

// zipNames lists the entry names in a deterministic order for failure output.
func zipNames(entries map[string]*zip.File) []string {
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
