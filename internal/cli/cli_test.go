package cli

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
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/pipeline"
	"bilibili-sub/internal/store"
	"bilibili-sub/internal/util"
)

// TestRuntimeClientLoadsFullCookieFromSession pins that a session file carrying
// a full browser Cookie reaches the client, which then sends it verbatim.
func TestRuntimeClientLoadsFullCookieFromSession(t *testing.T) {
	rt := newTestRuntime(t)
	const cookie = "SESSDATA=abc%2Cdef; buvid3=B3VALUE; bili_ticket=XYZ"
	if err := bili.SaveSession(rt.sessionPath(), "abc%2Cdef", cookie); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	c, err := rt.client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if c.Cookie != cookie {
		t.Fatalf("client.Cookie = %q, want %q", c.Cookie, cookie)
	}
	if c.SESSDATA != "abc%2Cdef" {
		t.Fatalf("client.SESSDATA = %q, want abc%%2Cdef", c.SESSDATA)
	}
}

// TestLoginCommandRejectsConflictingCredentials covers the flag validation,
// which runs before the runtime is built, so no data directory is created and
// no network is touched.
func TestLoginCommandRejectsConflictingCredentials(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"sessdata and cookie", []string{"--sessdata", "a", "--cookie", "SESSDATA=a; buvid3=b"}},
		{"cookie and qr", []string{"--cookie", "SESSDATA=a; buvid3=b", "--qr"}},
		{"sessdata and qr", []string{"--sessdata", "a", "--qr"}},
		{"none", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newLoginCmd()
			cmd.SetArgs(tc.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err == nil {
				t.Fatal("want an error for conflicting or missing login credentials")
			}
		})
	}
}

func TestRootHasCommands(t *testing.T) {
	root := NewRootCmd()
	want := map[string]bool{"login": true, "probe": true, "download": true, "export": true, "status": true, "serve": true}
	for _, c := range root.Commands() {
		delete(want, c.Name())
	}
	if len(want) != 0 {
		t.Fatalf("missing commands: %v", want)
	}
}

// TestRootRegistersAllCommands pins the full command set from the design spec:
// the brief's TestRootHasCommands only samples six of the nine.
func TestRootRegistersAllCommands(t *testing.T) {
	root := NewRootCmd()
	want := []string{"download", "export", "login", "probe", "reset", "retry", "run", "serve", "status", "transcribe"}
	got := map[string]bool{}
	for _, c := range root.Commands() {
		got[c.Name()] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing command %q", name)
		}
	}
	if n := len(root.Commands()); n != len(want) {
		t.Errorf("root has %d commands, want %d", n, len(want))
	}
}

// execute runs the command tree with args and returns RunE's error. Output is
// discarded so the table printers do not pollute test logs.
func execute(args ...string) error {
	root := NewRootCmd()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return root.Execute()
}

func TestExportRejectsUnknownFormat(t *testing.T) {
	// Keep config/store away from the developer's working tree even if a
	// command starts touching the database before validating its flags.
	t.Setenv("BILI_DATA_DIR", t.TempDir())

	err := execute("export", "--format", "pdf")
	if err == nil {
		t.Fatal("export --format pdf: error = nil, want an unsupported-format error")
	}
	if !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("export --format pdf: error = %q, want it to mention unsupported format", err)
	}
}

func TestResetRequiresYes(t *testing.T) {
	t.Setenv("BILI_DATA_DIR", t.TempDir())

	err := execute("reset")
	if err == nil {
		t.Fatal("reset without --yes: error = nil, want a confirmation error")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("reset without --yes: error = %q, want it to mention --yes", err)
	}
}

func TestResetWithYesDeletesDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "keep.db"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed data dir: %v", err)
	}

	if err := execute("reset", "--yes"); err != nil {
		t.Fatalf("reset --yes: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("reset --yes left %s behind (stat err = %v)", dir, err)
	}
}

// TestExportWritesZipFromStoredTranscript exercises the export wiring without
// the network: seed a fetched video + IR row, then run the command and inspect
// the archive it produced.
func TestExportWritesZipFromStoredTranscript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)

	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	id, err := st.UpsertVideo(&store.Video{Bvid: "BV1uv411q7Mv", Cid: 1, Title: "测试标题", Author: "测试UP主", State: store.StateFetched, PageIndex: 1})
	if err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	doc := ir.Document{
		Bvid: "BV1uv411q7Mv", Cid: 1, Title: "测试标题", Lang: "zh-Hans",
		Segments: []ir.Segment{{StartMs: 0, EndMs: 1500, Text: "你好"}},
	}
	blob, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal IR: %v", err)
	}
	if err := st.SaveTranscript(id, "bilibili", "zh-Hans", string(blob)); err != nil {
		t.Fatalf("save transcript: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	if err := execute("export", "--format", "srt,txt"); err != nil {
		t.Fatalf("export: %v", err)
	}

	zips, err := filepath.Glob(filepath.Join(dir, "exports", "*.zip"))
	if err != nil {
		t.Fatalf("glob exports: %v", err)
	}
	if len(zips) != 1 {
		t.Fatalf("exported zips = %v, want exactly one", zips)
	}
	zr, err := zip.OpenReader(zips[0])
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	if len(zr.File) != 2 {
		t.Fatalf("zip entries = %d, want 2 (srt+txt)", len(zr.File))
	}
	// Every entry nests under the archive base, and the flat file name carries
	// the UP主 prefix.
	base := strings.TrimSuffix(filepath.Base(zips[0]), ".zip") + "/"
	want := base + util.SanitizeSubtitleName("测试UP主", "测试标题", "BV1uv411q7Mv")
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, want) {
			t.Errorf("entry %q, want it nested under %q", f.Name, want)
		}
	}

	// A successful export marks the packaged video exported, so a later default
	// export would skip it.
	st, err = store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	got, err := st.ListVideos(store.StateExported)
	if err != nil {
		t.Fatalf("list exported: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("exported rows = %+v, want the one packaged video", got)
	}
}

// TestResetRejectsUnsafeDataDir pins the guard: DataDir values that would wipe
// the working directory or a filesystem root must be refused before
// os.RemoveAll runs.
func TestResetRejectsUnsafeDataDir(t *testing.T) {
	for _, dir := range []string{"..", ".", string(filepath.Separator)} {
		t.Run(dir, func(t *testing.T) {
			t.Setenv("BILI_DATA_DIR", dir)
			err := execute("reset", "--yes")
			if err == nil {
				t.Fatalf("reset --yes with BILI_DATA_DIR=%q: error = nil, want a refusal", dir)
			}
			if !strings.Contains(err.Error(), "拒绝删除") {
				t.Fatalf("reset --yes with BILI_DATA_DIR=%q: error = %q, want a refusal", dir, err)
			}
		})
	}
}

// TestCheckResetPathRejectsEscapes pins the hardened guard directly: running a
// relative escape through `reset --yes` would call os.RemoveAll, so a guard
// regression must fail here without touching the filesystem. `../..` cleans to
// a path whose Dir is `..`, which the older filepath.Dir(p)==p check missed.
func TestCheckResetPathRejectsEscapes(t *testing.T) {
	unsafe := []string{"..", "../..", "data/../../..", ".", "/"}
	if filepath.Separator == '\\' { // the package already declares `runtime`
		// `C:` has a volume name but no root, so filepath.Dir does not flag
		// it; `C:\` is the volume root case.
		unsafe = append(unsafe, "C:", `C:\`)
	}
	for _, dir := range unsafe {
		t.Run(dir, func(t *testing.T) {
			if err := checkResetPath(dir); err == nil {
				t.Fatalf("checkResetPath(%q) = nil, want a refusal", dir)
			}
		})
	}
}

// TestCheckResetPathAcceptsRelativeDir keeps the guard from over-rejecting the
// ordinary data directories the CLI writes to.
func TestCheckResetPathAcceptsRelativeDir(t *testing.T) {
	for _, dir := range []string{"data", "data/subtitles", "bili-data"} {
		if err := checkResetPath(dir); err != nil {
			t.Errorf("checkResetPath(%q) = %v, want nil", dir, err)
		}
	}
}

// TestExportSkipsUnreadableTranscripts pins the hardening: a fetched video with
// no transcript row (sql.ErrNoRows) or a corrupted IR blob must be skipped with
// a warning, not abort the whole archive.
func TestExportSkipsUnreadableTranscripts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)

	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	validID, err := st.UpsertVideo(&store.Video{Bvid: "BV1valid1111", Cid: 1, Title: "有效视频", State: store.StateFetched, PageIndex: 1})
	if err != nil {
		t.Fatalf("upsert valid: %v", err)
	}
	doc := ir.Document{
		Bvid: "BV1valid1111", Cid: 1, Title: "有效视频", Lang: "zh-Hans",
		Segments: []ir.Segment{{StartMs: 0, EndMs: 1500, Text: "你好"}},
	}
	blob, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal IR: %v", err)
	}
	if err := st.SaveTranscript(validID, "bilibili", "zh-Hans", string(blob)); err != nil {
		t.Fatalf("save valid transcript: %v", err)
	}

	// fetched but no transcripts row at all -> GetTranscript returns sql.ErrNoRows.
	if _, err := st.UpsertVideo(&store.Video{Bvid: "BV1missing11", Cid: 2, Title: "缺失字幕", State: store.StateFetched, PageIndex: 1}); err != nil {
		t.Fatalf("upsert missing: %v", err)
	}
	// fetched with a row whose ir_json cannot unmarshal.
	corruptID, err := st.UpsertVideo(&store.Video{Bvid: "BV1corrupt11", Cid: 3, Title: "损坏字幕", State: store.StateFetched, PageIndex: 1})
	if err != nil {
		t.Fatalf("upsert corrupt: %v", err)
	}
	if err := st.SaveTranscript(corruptID, "bilibili", "zh-Hans", "{not json"); err != nil {
		t.Fatalf("save corrupt transcript: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	if err := execute("export", "--format", "srt,txt"); err != nil {
		t.Fatalf("export: %v", err)
	}

	zips, err := filepath.Glob(filepath.Join(dir, "exports", "*.zip"))
	if err != nil {
		t.Fatalf("glob exports: %v", err)
	}
	if len(zips) != 1 {
		t.Fatalf("exported zips = %v, want exactly one", zips)
	}
	zr, err := zip.OpenReader(zips[0])
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	if len(zr.File) != 2 {
		t.Fatalf("zip entries = %d, want 2 (the one valid video in srt+txt)", len(zr.File))
	}
	for _, f := range zr.File {
		if !strings.Contains(f.Name, "BV1valid1111") {
			t.Errorf("zip entry %q should belong to the valid video", f.Name)
		}
		if strings.Contains(f.Name, "BV1missing11") || strings.Contains(f.Name, "BV1corrupt11") {
			t.Errorf("zip entry %q should have been skipped", f.Name)
		}
	}
}

// TestWriteSubtitleFormats pins the filename agreement with
// pipeline.Downloader.FetchSubtitle: the downloader owns the .srt, the command
// writes the other formats beside it, and only multi-P videos get a _P{n}
// suffix. Both land in the same per-author folder the downloader uses.
func TestWriteSubtitleFormats(t *testing.T) {
	dir := t.TempDir()
	v := store.Video{Bvid: "BV1uv411q7Mv", Title: "标题", Author: "某UP", PageIndex: 2}
	doc := &ir.Document{
		Bvid: "BV1uv411q7Mv", Title: "标题",
		Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "你好"}},
	}
	if err := writeSubtitleFormats(dir, v, doc, []string{"srt", "txt", "json"}); err != nil {
		t.Fatalf("writeSubtitleFormats: %v", err)
	}

	stem := "某UP - 标题 [BV1uv411q7Mv]_P2"
	if pageStem(v) != stem {
		t.Fatalf("pageStem = %q, want %q", pageStem(v), stem)
	}
	if _, err := os.Stat(filepath.Join(dir, stem+".srt")); !os.IsNotExist(err) {
		t.Errorf(".srt must be left to FetchSubtitle, stat err = %v", err)
	}
	for _, format := range []string{"txt", "json"} {
		if _, err := os.Stat(filepath.Join(dir, stem+"."+format)); err != nil {
			t.Errorf("%s.%s missing: %v", stem, format, err)
		}
	}
}

func TestParseFormats(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{name: "all four", in: "srt,txt,json,md", want: []string{"srt", "txt", "json", "md"}},
		{name: "spaces and duplicates", in: " srt , txt , srt ", want: []string{"srt", "txt"}},
		{name: "unknown", in: "srt,pdf", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "only separators", in: " , ,", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFormats(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseFormats(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFormats(%q): %v", tt.in, err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("parseFormats(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestExpandPages pins Ruling F: one store.Video per page, with the video-level
// fallback when the API returns no pages list.
func TestExpandPages(t *testing.T) {
	t.Run("multi page", func(t *testing.T) {
		vi := &bili.VideoInfo{
			Bvid:     "BV1uv411q7Mv",
			Aid:      42,
			Cid:      99,
			Title:    "合集",
			Pubdate:  1700000000,
			Duration: 600,
			Owner:    bili.Owner{Name: "某UP"},
			Pages: []bili.PageInfo{
				{Cid: 1, Page: 1, Part: "第一集"},
				{Cid: 2, Page: 2, Part: "第二集"},
			},
		}
		vi.IsUpowerPlay = true

		got := expandPages(vi, "12345")
		if len(got) != 2 {
			t.Fatalf("expandPages len = %d, want 2", len(got))
		}
		want := []store.Video{
			{Bvid: "BV1uv411q7Mv", Aid: 42, Cid: 1, Author: "某UP", Title: "合集", Pubdate: 1700000000, Duration: 600, PageIndex: 1, Part: "第一集", IsCharging: true, State: store.StatePending, SourceKey: "12345"},
			{Bvid: "BV1uv411q7Mv", Aid: 42, Cid: 2, Author: "某UP", Title: "合集", Pubdate: 1700000000, Duration: 600, PageIndex: 2, Part: "第二集", IsCharging: true, State: store.StatePending, SourceKey: "12345"},
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("video[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("no pages falls back to video cid", func(t *testing.T) {
		vi := &bili.VideoInfo{Bvid: "BV1xx411c7mD", Aid: 7, Cid: 7, Title: "单P"}
		vi.IsUpowerPreview = true

		got := expandPages(vi, "BV1xx411c7mD")
		if len(got) != 1 {
			t.Fatalf("expandPages len = %d, want 1", len(got))
		}
		if got[0].Cid != 7 || got[0].PageIndex != 1 || got[0].Part != "" {
			t.Errorf("video = %+v, want cid=7 page=1 part=\"\"", got[0])
		}
		if got[0].Aid != 7 {
			t.Errorf("Aid = %d, want 7", got[0].Aid)
		}
		if !got[0].IsCharging {
			t.Errorf("IsCharging = false, want true (is_upower_preview)")
		}
		if got[0].State != store.StatePending {
			t.Errorf("State = %q, want %q", got[0].State, store.StatePending)
		}
	})
}

// TestProgressEmitterThrottles pins the shared progress throttle: the first tick
// is always emitted, ticks inside the interval window are suppressed, and the
// final (done == total) tick is always emitted so the page can finish its bar.
func TestProgressEmitterThrottles(t *testing.T) {
	tests := []struct {
		name  string
		total int
		dones []int
		want  []int
	}{
		{name: "first tick is always emitted", total: 5, dones: []int{0}, want: []int{0}},
		{name: "ticks inside the interval window are suppressed", total: 5, dones: []int{0, 1, 2}, want: []int{0}},
		{name: "the final tick is always emitted", total: 5, dones: []int{0, 1, 5}, want: []int{0, 5}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			tick := newProgressEmitter(tc.total, func(done, total int) {
				if total != tc.total {
					t.Errorf("emit total = %d, want %d", total, tc.total)
				}
				got = append(got, done)
			})
			for _, done := range tc.dones {
				tick(done)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("emitted dones = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestProgressEmitterNilEmitIsNoop pins that a command run without a progress
// sink (the CLI path passes nil) can tick without a nil-call panic.
func TestProgressEmitterNilEmitIsNoop(t *testing.T) {
	tick := newProgressEmitter(3, nil)
	tick(0)
	tick(1)
	tick(3)
}

// --- Task 23: scaling (paged list, source scoping, skip completed) ---

// overrideEnumerator points the probe/download enumerator at a fake view server
// for the duration of one test.
func overrideEnumerator(t *testing.T, viewURL, navURL string) {
	t.Helper()
	prev := newEnumerator
	newEnumerator = func(c *bili.Client) *pipeline.Enumerator {
		en := pipeline.NewEnumerator(c)
		en.ViewBaseURL = viewURL
		en.NavURL = navURL
		return en
	}
	t.Cleanup(func() { newEnumerator = prev })
}

// overrideSeasonEnumerator is overrideEnumerator plus the 合集 listing endpoint.
func overrideSeasonEnumerator(t *testing.T, viewURL, navURL, seasonURL string) {
	t.Helper()
	prev := newEnumerator
	newEnumerator = func(c *bili.Client) *pipeline.Enumerator {
		en := pipeline.NewEnumerator(c)
		en.ViewBaseURL = viewURL
		en.NavURL = navURL
		en.SeasonBaseURL = seasonURL
		return en
	}
	t.Cleanup(func() { newEnumerator = prev })
}

// overrideProber points the prober's unsigned player endpoint at a fake server.
func overrideProber(t *testing.T, playerV2URL string) {
	t.Helper()
	prev := newProber
	newProber = func(c *bili.Client, st *store.Store, en *pipeline.Enumerator) *pipeline.Prober {
		p := pipeline.NewProber(c, st, en)
		p.PlayerV2URL = playerV2URL
		return p
	}
	t.Cleanup(func() { newProber = prev })
}

// overrideDownloader points the downloader's unsigned player endpoint at a fake
// server.
func overrideDownloader(t *testing.T, playerV2URL string) {
	t.Helper()
	prev := newDownloader
	newDownloader = func(c *bili.Client, st *store.Store, en *pipeline.Enumerator, outputDir string) *pipeline.Downloader {
		d := pipeline.NewDownloader(c, st, en, outputDir)
		d.PlayerV2URL = playerV2URL
		return d
	}
	t.Cleanup(func() { newDownloader = prev })
}

// TestProbeSkipsCompletedVideos drives the real probe against a fake view server
// with a seeded fetched row: the completed video must not be probed (no player
// request), must be counted in stats.Skipped, and must keep its state. --force
// then re-probes it.
func TestProbeSkipsCompletedVideos(t *testing.T) {
	const bvid = "BV1uv411q7Mv"
	var playerRequests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/view":
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"bvid":%q,"cid":1001,"title":"视频","pubdate":1,"duration":10,"owner":{"mid":1,"name":"up"}}}`, bvid)
		case "/x/player/v2":
			atomic.AddInt32(&playerRequests, 1)
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"subtitle":{"subtitles":[{"lan":"zh-CN","subtitle_url":"https://example.invalid/x.json"}]}}}`)
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
	// A row the earlier pipeline already finished: same bvid/cid, source keyed
	// exactly as probe will key it.
	if _, err := st.UpsertVideo(&store.Video{Bvid: bvid, Cid: 1001, Title: "视频", State: store.StateFetched, SourceKey: bvid}); err != nil {
		t.Fatalf("seed completed row: %v", err)
	}

	var out strings.Builder
	stats, err := rt.probe(context.Background(), &out, bvid, "single", false, nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if stats.Total != 1 || stats.Skipped != 1 {
		t.Fatalf("stats = %+v, want total=1 skipped=1", stats)
	}
	if n := atomic.LoadInt32(&playerRequests); n != 0 {
		t.Fatalf("a completed video was probed: %d player requests", n)
	}
	if !strings.Contains(out.String(), "已完成") {
		t.Errorf("output = %q, want the completed video shown as 已完成", out.String())
	}
	rows, err := st.ListVideosFiltered("", bvid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].State != store.StateFetched {
		t.Fatalf("rows = %+v, want the fetched state left untouched", rows)
	}

	// --force re-probes the same video and advances its state.
	stats, err = rt.probe(context.Background(), io.Discard, bvid, "single", true, nil)
	if err != nil {
		t.Fatalf("probe --force: %v", err)
	}
	if stats.Skipped != 0 {
		t.Fatalf("stats.Skipped = %d, want 0 with force", stats.Skipped)
	}
	if n := atomic.LoadInt32(&playerRequests); n == 0 {
		t.Fatal("probe --force did not ask the player endpoint")
	}
	rows, err = st.ListVideosFiltered("", bvid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].State != store.StateDownloadable {
		t.Fatalf("rows = %+v, want state downloadable after force probe", rows)
	}
}

// TestDownloadScopesToSource seeds downloadable rows in two sources and proves
// download with a source only touches the named one: exactly one player request,
// for the source's bvid, and the other source's row stays downloadable.
func TestDownloadScopesToSource(t *testing.T) {
	var mu sync.Mutex
	var requested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/player/v2":
			mu.Lock()
			requested = append(requested, r.URL.Query().Get("bvid"))
			mu.Unlock()
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"subtitle":{"subtitles":[{"lan":"zh-CN","subtitle_url":"http://%s/sub.json"}]}}}`, r.Host)
		case "/sub.json":
			_, _ = io.WriteString(w, `{"body":[{"from":0,"to":1,"content":"你好"}]}`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	overrideEnumerator(t, srv.URL+"/x/web-interface/view", srv.URL+"/x/web-interface/nav")
	overrideDownloader(t, srv.URL+"/x/player/v2")

	rt := newTestRuntime(t)
	defer rt.close()
	st, err := rt.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := st.UpsertVideo(&store.Video{Bvid: "BV1aaaa1111a", Cid: 1, Title: "源一", State: store.StateDownloadable, SourceKey: "111"}); err != nil {
		t.Fatalf("seed source 111: %v", err)
	}
	if _, err := st.UpsertVideo(&store.Video{Bvid: "BV1bbbb2222b", Cid: 2, Title: "源二", State: store.StateDownloadable, SourceKey: "222"}); err != nil {
		t.Fatalf("seed source 222: %v", err)
	}

	stats, err := rt.download(context.Background(), io.Discard, "", []string{"srt"}, false, defaultASRLang, "", "111", nil)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if stats.Total != 1 || stats.Fetched != 1 {
		t.Fatalf("stats = %+v, want total=1 fetched=1 (only source 111)", stats)
	}
	mu.Lock()
	gotRequested := append([]string(nil), requested...)
	mu.Unlock()
	if len(gotRequested) != 1 || gotRequested[0] != "BV1aaaa1111a" {
		t.Fatalf("player requests = %v, want only BV1aaaa1111a", gotRequested)
	}

	other, err := st.ListVideosFiltered("", "222")
	if err != nil {
		t.Fatalf("list source 222: %v", err)
	}
	if len(other) != 1 || other[0].State != store.StateDownloadable {
		t.Fatalf("source 222 rows = %+v, want the untouched downloadable row", other)
	}
}

// TestExportPackagesOnlyNotYetExported pins the new contract: a default export
// packages the fetched rows under their author's folder, marks them exported
// afterwards, and a second default run finds nothing left. --include-exported
// opts back into re-packaging the already-exported rows. Export is no longer
// scoped by source, so both sources are packed together.
func TestExportPackagesOnlyNotYetExported(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)

	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	seed := func(bvid, title, author, state string) {
		t.Helper()
		id, err := st.UpsertVideo(&store.Video{Bvid: bvid, Cid: 1, Title: title, Author: author, State: state, PageIndex: 1})
		if err != nil {
			t.Fatalf("upsert %s: %v", bvid, err)
		}
		// The IR deliberately omits Author: export must fall back to the row's.
		doc := ir.Document{Bvid: bvid, Cid: 1, Title: title, Lang: "zh-Hans",
			Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "你好"}}}
		blob, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal %s: %v", bvid, err)
		}
		if err := st.SaveTranscript(id, "bilibili", "zh-Hans", string(blob)); err != nil {
			t.Fatalf("save transcript %s: %v", bvid, err)
		}
	}
	seed("BV1aaaa1111a", "源一", "UP甲", store.StateFetched)
	seed("BV1bbbb2222b", "源二", "UP乙", store.StateTranscribed)
	seed("BV1cccc3333c", "已导出", "UP丙", store.StateExported)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	if err := execute("export", "--format", "srt"); err != nil {
		t.Fatalf("export: %v", err)
	}

	zips, err := filepath.Glob(filepath.Join(dir, "exports", "*.zip"))
	if err != nil {
		t.Fatalf("glob exports: %v", err)
	}
	if len(zips) != 1 {
		t.Fatalf("exported zips = %v, want exactly one", zips)
	}
	zr, err := zip.OpenReader(zips[0])
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	if len(zr.File) != 2 {
		t.Fatalf("zip entries = %d, want 2 (the two not-yet-exported videos)", len(zr.File))
	}
	base := strings.TrimSuffix(filepath.Base(zips[0]), ".zip") + "/"
	wantEntries := map[string]bool{
		base + util.SanitizeSubtitleName("UP甲", "源一", "BV1aaaa1111a") + ".srt": true,
		base + util.SanitizeSubtitleName("UP乙", "源二", "BV1bbbb2222b") + ".srt": true,
	}
	for _, f := range zr.File {
		if !wantEntries[f.Name] {
			t.Errorf("unexpected zip entry %q; want the two per-author entries %v", f.Name, wantEntries)
		}
	}

	// Every seeded row is exported after a run that packaged two of them.
	st, err = store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	exported, err := st.ListVideos(store.StateExported)
	if err != nil {
		t.Fatalf("list exported: %v", err)
	}
	if len(exported) != 3 {
		t.Fatalf("exported rows = %d, want all 3", len(exported))
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// A second default export now has nothing left to package: it is a friendly
	// no-op that creates no new archive.
	if err := execute("export", "--format", "srt"); err != nil {
		t.Fatalf("second default export: %v, want a friendly no-op", err)
	}
	stillZips, err := filepath.Glob(filepath.Join(dir, "exports", "*.zip"))
	if err != nil {
		t.Fatalf("glob exports: %v", err)
	}
	if len(stillZips) != 1 {
		t.Fatalf("zips after the no-op export = %v, want still one", stillZips)
	}

	// --include-exported re-packages the already-exported rows too.
	if err := execute("export", "--format", "srt", "--include-exported"); err != nil {
		t.Fatalf("export --include-exported: %v", err)
	}
	zips, err = filepath.Glob(filepath.Join(dir, "exports", "*.zip"))
	if err != nil {
		t.Fatalf("glob exports: %v", err)
	}
	if len(zips) != 2 {
		t.Fatalf("exported zips = %v, want two after --include-exported", zips)
	}
	zr2, err := zip.OpenReader(zips[len(zips)-1])
	if err != nil {
		t.Fatalf("open second zip: %v", err)
	}
	defer zr2.Close()
	if len(zr2.File) != 3 {
		t.Fatalf("second zip entries = %d, want all 3 with --include-exported", len(zr2.File))
	}
}

// TestProbeSeasonKeysRowsByTheCollection pins the trap this feature exists to
// close: a 合集 URL starts with space.bilibili.com/<mid>/, so before this the
// probe silently returned the UP主's entire backlog instead of the collection
// the link points at. The rows must be keyed by the season, which is what
// --source filtering and a later re-probe both read.
func TestProbeSeasonKeysRowsByTheCollection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/polymer/web-space/seasons_archives_list":
			q := r.URL.Query()
			if q.Get("mid") != "453013730" || q.Get("season_id") != "9241496" {
				t.Errorf("season request mid=%q sid=%q", q.Get("mid"), q.Get("season_id"))
			}
			if q.Get("page_num") != "1" || q.Get("page_size") != "30" {
				t.Errorf("season paging = pn=%q ps=%q", q.Get("page_num"), q.Get("page_size"))
			}
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"archives":[{"bvid":"BV1uv411q7Mv"},{"bvid":"BV1uv411q7Mx"}],"page":{"page_num":1,"page_size":30,"total":2}}}`)
		case "/x/web-interface/view":
			bvid := r.URL.Query().Get("bvid")
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"bvid":%q,"cid":1001,"title":"视频","pubdate":1,"duration":10,"owner":{"mid":453013730,"name":"up"},"pages":[{"cid":1001,"page":1,"part":"P1"}]}}`, bvid)
		case "/x/player/v2":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"subtitle":{"subtitles":[]}}}`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	overrideSeasonEnumerator(t, srv.URL+"/x/web-interface/view", srv.URL+"/x/web-interface/nav",
		srv.URL+"/x/polymer/web-space/seasons_archives_list")
	overrideProber(t, srv.URL+"/x/player/v2")

	rt := newTestRuntime(t)
	defer rt.close()

	const link = "https://space.bilibili.com/453013730/channel/collectiondetail?sid=9241496"
	var out strings.Builder
	stats, err := rt.probe(context.Background(), &out, link, "", false, nil)
	if err != nil {
		t.Fatalf("probe: %v\n%s", err, out.String())
	}
	if stats.Total != 2 {
		t.Fatalf("stats = %+v, want the 合集's 2 videos rather than the UP主's whole space", stats)
	}

	st, err := rt.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	rows, err := st.ListVideosFiltered("", "season:453013730:9241496")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows keyed by the season = %d, want 2", len(rows))
	}
	spaceRows, err := st.ListVideosFiltered("", "453013730")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(spaceRows) != 0 {
		t.Errorf("rows keyed by the bare mid = %d, want 0: this link is not the UP主's space", len(spaceRows))
	}
}

// TestProbeSingleVideoPointsAtItsSeason pins the answer to "I pasted one video
// and it is one episode of 31": the run still collects exactly that one video
// (expanding would turn a 1-video request into 31 and 30x the requests), and the
// output hands over the link that would collect the rest.
func TestProbeSingleVideoPointsAtItsSeason(t *testing.T) {
	const bvid = "BV1u84y1i7u6"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/view":
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"bvid":%q,"cid":1001,"title":"视频","pubdate":1,"duration":10,"owner":{"mid":406571547,"name":"up"},"pages":[{"cid":1001,"page":1,"part":"P1"}],"ugc_season":{"id":814546,"mid":406571547,"title":"100个办公神技","ep_count":31,"sections":[{"episodes":[{"bvid":%q}]}]}}}`, bvid, bvid)
		case "/x/player/v2":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"subtitle":{"subtitles":[]}}}`)
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

	var out strings.Builder
	stats, err := rt.probe(context.Background(), &out, bvid, "", false, nil)
	if err != nil {
		t.Fatalf("probe: %v\n%s", err, out.String())
	}
	if stats.Total != 1 {
		t.Fatalf("stats = %+v, want the single video left alone", stats)
	}
	got := out.String()
	for _, want := range []string{"100个办公神技", "共 31 条", "sid=814546"} {
		if !strings.Contains(got, want) {
			t.Errorf("output = %q, want it to contain %q", got, want)
		}
	}
}

// TestProbeSingleVideoWithoutASeasonStaysQuiet: most videos belong to no 合集, so
// the hint must not appear for them -- a stray "属于合集《》" on every probe would
// be noise.
func TestProbeSingleVideoWithoutASeasonStaysQuiet(t *testing.T) {
	const bvid = "BV1LJBcYYEZE"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/view":
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"bvid":%q,"cid":1001,"title":"视频","pubdate":1,"duration":10,"owner":{"mid":1,"name":"up"},"pages":[{"cid":1001,"page":1,"part":"P1"}]}}`, bvid)
		case "/x/player/v2":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"subtitle":{"subtitles":[]}}}`)
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

	var out strings.Builder
	if _, err := rt.probe(context.Background(), &out, bvid, "", false, nil); err != nil {
		t.Fatalf("probe: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "合集") {
		t.Errorf("output = %q, want no 合集 hint for a video in no 合集", out.String())
	}
}
