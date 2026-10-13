package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/pipeline"
	"bilibili-sub/internal/store"
)

// navWBIKeys is a nav response whose two wbi keys hold enough characters for the
// signer; the exact values do not matter, only that they are present.
const navWBIKeys = `{"code":0,"message":"0","data":{"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png","sub_url":"https://i0.hdslb.com/bfs/wbi/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png"}}}`

// overrideFullPipeline points every seam `run` touches at one fake server, and
// also aims the client's device-fingerprint fetch at it so the test stays
// hermetic.
func overrideFullPipeline(t *testing.T, base string) {
	t.Helper()
	prevEn, prevProber, prevDownloader := newEnumerator, newProber, newDownloader
	newEnumerator = func(c *bili.Client) *pipeline.Enumerator {
		c.FingerprintURL = base + "/x/frontend/finger/spi"
		en := pipeline.NewEnumerator(c)
		en.ViewBaseURL = base + "/x/web-interface/view"
		en.NavURL = base + "/x/web-interface/nav"
		return en
	}
	newProber = func(c *bili.Client, st *store.Store, en *pipeline.Enumerator) *pipeline.Prober {
		p := pipeline.NewProber(c, st, en)
		// A video whose aid is known goes through the signed endpoint, which is
		// what a real run uses; both are aimed at the same fake path.
		p.PlayerBaseURL = base + "/x/player/v2"
		p.PlayerV2URL = base + "/x/player/v2"
		return p
	}
	newDownloader = func(c *bili.Client, st *store.Store, en *pipeline.Enumerator, dir string) *pipeline.Downloader {
		d := pipeline.NewDownloader(c, st, en, dir)
		d.PlayerBaseURL = base + "/x/player/v2"
		d.PlayerV2URL = base + "/x/player/v2"
		return d
	}
	t.Cleanup(func() { newEnumerator, newProber, newDownloader = prevEn, prevProber, prevDownloader })
}

// subtitlePipeServer serves one video that has a Chinese subtitle.
func subtitlePipeServer(t *testing.T, bvid string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/frontend/finger/spi":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"b_3":"B3","b_4":"B4"}}`)
		case "/x/web-interface/nav":
			_, _ = io.WriteString(w, navWBIKeys)
		case "/x/web-interface/view":
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"bvid":%q,"aid":7,"cid":1001,"title":"视频","pubdate":1,"duration":10,"owner":{"mid":1,"name":"某UP"}}}`, bvid)
		case "/x/player/v2":
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"subtitle":{"subtitles":[{"lan":"zh-CN","lan_doc":"中文","subtitle_url":"http://%s/sub.json"}]}}}`, r.Host)
		case "/sub.json":
			_, _ = io.WriteString(w, `{"body":[{"from":0.0,"to":1.5,"content":"你好"}]}`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// exportsIn lists the zip files an export produced.
func exportsIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "exports"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".zip") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestRunProbesDownloadsAndExports pins the whole point of `run`: one command
// takes a source from nothing to a zip, reusing the same stages the individual
// commands expose.
func TestRunProbesDownloadsAndExports(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)

	const bvid = "BV1uv411q7Mv"
	overrideFullPipeline(t, subtitlePipeServer(t, bvid).URL)

	out, err := executeOut(t, "run", bvid, "--transcribe=false")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}

	v := onlyVideo(t, dir)
	if v.State != store.StateExported {
		t.Errorf("state = %q, want %q", v.State, store.StateExported)
	}
	if got := exportsIn(t, dir); len(got) != 1 {
		t.Errorf("exports = %v, want exactly one zip", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "subtitles", "某UP - 视频 ["+bvid+"].srt")); err != nil {
		t.Errorf("subtitle missing: %v", err)
	}
}

// TestRunStillExportsWhenTheAsrLegAborts pins the deliberate ordering: the ASR
// leg runs before export, but an ASR failure must not cost the user the
// subtitles that were already downloaded and transcribed. The error is still
// returned, so the exit code says something went wrong.
func TestRunStillExportsWhenTheAsrLegAborts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)

	const bvid = "BV1uv411q7Mv"
	overrideFullPipeline(t, subtitlePipeServer(t, bvid).URL)

	// A video from an earlier run that already has a subtitle stored, plus one
	// with no subtitle at all for this run's ASR leg to choke on.
	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	already := store.Video{Bvid: "BV1already11", Cid: 5, Author: "某UP", Title: "旧的", State: store.StateFetched}
	id, err := st.UpsertVideo(&already)
	if err != nil {
		t.Fatalf("seed fetched video: %v", err)
	}
	if err := st.SaveTranscript(id, "bilibili", "zh-CN", `{"bvid":"BV1already11","cid":5,"title":"旧的","author":"某UP","lang":"zh-CN","segments":[{"start_ms":0,"end_ms":1,"text":"x"}]}`); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	if _, err := st.UpsertVideo(&store.Video{
		Bvid: "BV1nosub111", Cid: 6, Author: "某UP", Title: "无字幕",
		State: store.StateNoSubtitle, SourceKey: bvid,
	}); err != nil {
		t.Fatalf("seed no-subtitle video: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	backend := &fakeASR{startErr: fmt.Errorf("python 未安装")}
	overrideASRBackend(t, backend, nil)

	out, err := executeOut(t, "run", bvid)
	if err == nil {
		t.Fatalf("want the ASR failure reported\n%s", out)
	}
	if !strings.Contains(err.Error(), "python") {
		t.Errorf("error = %q, want the ASR failure", err)
	}

	// The ASR failure must not have cost the work: the archive exists and the
	// already-fetched video was packaged and marked exported.
	if got := exportsIn(t, dir); len(got) != 1 {
		t.Fatalf("exports = %v, want the run to export what it had", got)
	}
	st, err = store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	videos, err := st.ListVideos("")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	states := map[string]string{}
	for _, v := range videos {
		states[v.Bvid] = v.State
	}
	if states["BV1already11"] != store.StateExported {
		t.Errorf("already-fetched state = %q, want exported", states["BV1already11"])
	}
	if states["BV1nosub111"] != store.StateNoSubtitle {
		t.Errorf("no-subtitle state = %q, want it left untouched by the aborted leg", states["BV1nosub111"])
	}
}
