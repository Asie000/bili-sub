package cli

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
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
	"bilibili-sub/internal/export"
	"bilibili-sub/internal/filename"
	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/pipeline"
	"bilibili-sub/internal/store"
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
	want := base + filename.SanitizeSubtitleName("测试UP主", "测试标题", "BV1uv411q7Mv")
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

// overrideEnumeratorWith points the probe/download enumerator at a fake view and
// nav server, then lets the caller redirect whichever listing endpoints its test
// needs -- one helper instead of one per source kind.
func overrideEnumeratorWith(t *testing.T, viewURL, navURL string, configure func(*pipeline.Enumerator)) {
	t.Helper()
	prev := newEnumerator
	newEnumerator = func(c *bili.Client) *pipeline.Enumerator {
		en := pipeline.NewEnumerator(c)
		en.ViewBaseURL = viewURL
		en.NavURL = navURL
		if configure != nil {
			configure(en)
		}
		return en
	}
	t.Cleanup(func() { newEnumerator = prev })
}

// overrideEnumerator points the probe/download enumerator at a fake view server
// for the duration of one test.
func overrideEnumerator(t *testing.T, viewURL, navURL string) {
	t.Helper()
	overrideEnumeratorWith(t, viewURL, navURL, nil)
}

// overrideSeasonEnumerator is overrideEnumerator plus the 合集 listing endpoint.
func overrideSeasonEnumerator(t *testing.T, viewURL, navURL, seasonURL string) {
	t.Helper()
	overrideEnumeratorWith(t, viewURL, navURL, func(en *pipeline.Enumerator) {
		en.SeasonBaseURL = seasonURL
	})
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
	stats, err := rt.probe(context.Background(), &out, bvid, "single", false, pipeline.DefaultSearchLimit, nil)
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
	stats, err = rt.probe(context.Background(), io.Discard, bvid, "single", true, pipeline.DefaultSearchLimit, nil)
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
		base + filename.SanitizeSubtitleName("UP甲", "源一", "BV1aaaa1111a") + ".srt": true,
		base + filename.SanitizeSubtitleName("UP乙", "源二", "BV1bbbb2222b") + ".srt": true,
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

// TestExportDisambiguatesLongMultiPartNames pins the collision that only shows
// up for a multi-P video whose "author - title" is long enough to hit
// SanitizeSubtitleName's 100-rune truncation: putting _P2 on the title made both
// pages share one stem, so Pack rejected the archive and export failed forever
// (the rows were never marked exported). The stem now carries _P{n} after the
// bvid tag, exactly like the on-disk download name.
func TestExportDisambiguatesLongMultiPartNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)

	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	author := strings.Repeat("甲", 40)
	title := strings.Repeat("题", 60)
	const bvid = "BV1x411x7xx"
	for page := 1; page <= 2; page++ {
		id, err := st.UpsertVideo(&store.Video{Bvid: bvid, Cid: int64(1000 + page), Title: title, Author: author, State: store.StateFetched, PageIndex: page})
		if err != nil {
			t.Fatalf("upsert page %d: %v", page, err)
		}
		blob, err := json.Marshal(ir.Document{Bvid: bvid, Cid: int64(1000 + page), Title: title, Author: author, Lang: "zh-Hans",
			Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "你好"}}})
		if err != nil {
			t.Fatalf("marshal page %d: %v", page, err)
		}
		if err := st.SaveTranscript(id, "bilibili", "zh-Hans", string(blob)); err != nil {
			t.Fatalf("save transcript page %d: %v", page, err)
		}
	}
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
		t.Fatalf("zip entries = %d, want 2 distinct multi-P names", len(zr.File))
	}
	base := strings.TrimSuffix(filepath.Base(zips[0]), ".zip") + "/"
	stem := filename.SanitizeSubtitleName(author, title, bvid)
	want := map[string]bool{base + stem + ".srt": true, base + stem + "_P2.srt": true}
	for _, f := range zr.File {
		if !want[f.Name] {
			t.Errorf("unexpected zip entry %q; want %v", f.Name, want)
		}
	}
}

// TestProbeStopsWhenSessionExpired pins spec §八 for the probe leg: a -101 is a
// whole-leg failure, so it must not mark the row failed and must tell the user
// to log in. The unsigned view succeeds; the WBI-signed player call is where
// the expired session surfaces.
func TestProbeStopsWhenSessionExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/view":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"bvid":"BV1uv411q7Mv","aid":123,"cid":1001,"title":"视频","pubdate":1,"duration":10,"owner":{"mid":1,"name":"up"}}}`)
		case "/x/web-interface/nav":
			_, _ = io.WriteString(w, `{"code":-101,"message":"账号未登录"}`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	overrideEnumerator(t, srv.URL+"/x/web-interface/view", srv.URL+"/x/web-interface/nav")

	rt := newTestRuntime(t)
	defer rt.close()

	var out strings.Builder
	stats, err := rt.probe(context.Background(), &out, "BV1uv411q7Mv", "single", false, pipeline.DefaultSearchLimit, nil)
	if !sessionExpired(err) {
		t.Fatalf("err = %v, want the -101 session error", err)
	}
	if stats.Failed != 0 {
		t.Fatalf("stats = %+v, want failed=0 (a -101 is not a per-video failure)", stats)
	}
	if stats.FirstError == "" {
		t.Error("FirstError must carry the reason for the web summary")
	}
	if !strings.Contains(out.String(), "登录态失效") {
		t.Errorf("output = %q, want the login hint", out.String())
	}
	st, err := rt.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	rows, err := st.ListVideosFiltered("", "BV1uv411q7Mv")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].State == store.StateFailed {
		t.Fatalf("rows = %+v, want the row left not-failed", rows)
	}
}

// TestDownloadStopsWhenSessionExpired is the download-leg counterpart of
// TestProbeStopsWhenSessionExpired.
func TestDownloadStopsWhenSessionExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			_, _ = io.WriteString(w, `{"code":-101,"message":"账号未登录"}`)
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
	if _, err := st.UpsertVideo(&store.Video{Bvid: "BV1aaaa1111a", Cid: 1, Aid: 123, Title: "源一", State: store.StateDownloadable, SourceKey: "111"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var out strings.Builder
	stats, err := rt.download(context.Background(), &out, "", []string{"srt"}, false, defaultASRLang, "", "111", nil)
	if !sessionExpired(err) {
		t.Fatalf("err = %v, want the -101 session error", err)
	}
	if stats.Failed != 0 {
		t.Fatalf("stats = %+v, want failed=0", stats)
	}
	if stats.FirstError == "" {
		t.Error("FirstError must carry the reason for the web summary")
	}
	if !strings.Contains(out.String(), "登录态失效") {
		t.Errorf("output = %q, want the login hint", out.String())
	}
	rows, err := st.ListVideosFiltered("", "111")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].State != store.StateDownloadable {
		t.Fatalf("rows = %+v, want the row left downloadable", rows)
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
	stats, err := rt.probe(context.Background(), &out, link, "", false, pipeline.DefaultSearchLimit, nil)
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
	stats, err := rt.probe(context.Background(), &out, bvid, "", false, pipeline.DefaultSearchLimit, nil)
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
	// The same sentence has to reach the browser, which has no terminal to read.
	// Comparing against the printed bytes pins that the two cannot drift apart.
	if !strings.Contains(stats.Hint, "sid=814546") {
		t.Errorf("stats.Hint = %q, want the 合集 link so the page can offer it", stats.Hint)
	}
	if !strings.Contains(got, stats.Hint+"\n") {
		t.Errorf("printed output %q does not contain stats.Hint %q verbatim", got, stats.Hint)
	}
}

// TestProbeSearchKeysRowsByKeywordAndStopsAtTheLimit pins both halves of a
// keyword probe: the rows are grouped under search:<keyword>, which is what
// --source filters on, and the run creates at most --limit of them. One keyword
// can match a thousand videos and every hit costs a view call, so a run with no
// cap would be eight minutes long.
func TestProbeSearchKeysRowsByKeywordAndStopsAtTheLimit(t *testing.T) {
	navBody := fmt.Sprintf(`{"code":0,"message":"0","data":{"wbi_img":{"img_url":%q,"sub_url":%q}}}`,
		"https://i0.hdslb.com/bfs/wbi/"+strings.Repeat("a", 32)+".png",
		"https://i0.hdslb.com/bfs/wbi/"+strings.Repeat("b", 32)+".png")

	var mu sync.Mutex
	var searchPages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			_, _ = io.WriteString(w, navBody)
		case "/x/web-interface/wbi/search/type":
			q := r.URL.Query()
			if q.Get("keyword") != "劳动法" {
				t.Errorf("keyword=%q, want the normalized keyword", q.Get("keyword"))
			}
			page := q.Get("page")
			mu.Lock()
			searchPages = append(searchPages, page)
			mu.Unlock()
			// Two fresh hits per page, so only the limit can stop this run: a
			// listing that ran out would end it for the wrong reason.
			switch page {
			case "1":
				_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"numPages":50,"result":[{"bvid":"BV1uv411q7Mv"},{"bvid":"BV1uv411q7Mx"}]}}`)
			case "2":
				_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"numPages":50,"result":[{"bvid":"BV1uv411q7My"},{"bvid":"BV1uv411q7Mz"}]}}`)
			default:
				t.Errorf("unexpected search page %q: the limit should have stopped the run", page)
				_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"numPages":50,"result":[]}}`)
			}
		case "/x/web-interface/view":
			bvid := r.URL.Query().Get("bvid")
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"bvid":%q,"cid":1001,"title":"视频","pubdate":1,"duration":10,"owner":{"mid":1,"name":"up"},"pages":[{"cid":1001,"page":1,"part":"P1"}]}}`, bvid)
		case "/x/player/v2":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"subtitle":{"subtitles":[]}}}`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	overrideEnumeratorWith(t, srv.URL+"/x/web-interface/view", srv.URL+"/x/web-interface/nav",
		func(en *pipeline.Enumerator) { en.SearchBaseURL = srv.URL + "/x/web-interface/wbi/search/type" })
	overrideProber(t, srv.URL+"/x/player/v2")

	rt := newTestRuntime(t)
	defer rt.close()

	// Whitespace around the keyword, because that is what a pasted keyword looks
	// like and the source key has to normalize it away.
	var out strings.Builder
	stats, err := rt.probe(context.Background(), &out, " 劳动法 ", "search", false, 3, nil)
	if err != nil {
		t.Fatalf("probe: %v\n%s", err, out.String())
	}
	if stats.Total != 3 {
		t.Fatalf("stats = %+v, want exactly 3 rows from a limit of 3", stats)
	}

	st, err := rt.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	rows, err := st.ListVideosFiltered("", "search:劳动法")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows keyed by search:劳动法 = %d, want 3", len(rows))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(searchPages) != 2 || searchPages[0] != "1" || searchPages[1] != "2" {
		t.Fatalf("search pages = %v, want [1 2]", searchPages)
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
	stats, err := rt.probe(context.Background(), &out, bvid, "", false, pipeline.DefaultSearchLimit, nil)
	if err != nil {
		t.Fatalf("probe: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "合集") {
		t.Errorf("output = %q, want no 合集 hint for a video in no 合集", out.String())
	}
	if stats.Hint != "" {
		t.Errorf("stats.Hint = %q, want empty when the video belongs to no 合集", stats.Hint)
	}
}

// TestSearchLimitRejectsNegatives: a negative --limit used to mean "unlimited"
// in silence, because the check is `limit > 0` -- one dropped minus sign away
// from walking a thousand results and a thousand view calls.
func TestSearchLimitRejectsNegatives(t *testing.T) {
	for _, args := range [][]string{
		{"probe", "--kind", "search", "--limit", "-1", "kw"},
		{"run", "--kind", "search", "--limit", "-1", "kw"},
	} {
		out, err := executeOut(t, args...)
		if err == nil {
			t.Errorf("%v: no error, want --limit < 0 rejected\n%s", args, out)
		}
	}
}

// TestDownloadReportsCancellationNotFailures pins the report of a run the user
// stopped. Every item the pool never reached carries the cancellation, and
// printing a "失败" line per remaining video (then "共 N，成功 0，失败 N") describes
// failures that never happened -- the rows are untouched, which is the point.
func TestDownloadReportsCancellationNotFailures(t *testing.T) {
	rt := newTestRuntime(t)
	defer rt.close()
	st, err := rt.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	for i, bvid := range []string{"BV1aaaa1111a", "BV1bbbb2222b"} {
		v := store.Video{Bvid: bvid, Cid: int64(100 + i), Author: "up", Title: "t", State: store.StateDownloadable, Lang: "zh"}
		if _, err := st.UpsertVideo(&v); err != nil {
			t.Fatalf("seed %s: %v", bvid, err)
		}
	}
	overrideDownloader(t, "http://127.0.0.1:1/player")

	ctx, cancel := context.WithCancel(context.Background())
	// Cancelled before anything starts, so every item reports ctx.Err() -- the
	// same shape a Ctrl+C leaves behind mid-run.
	cancel()

	var out strings.Builder
	stats, err := rt.downloadSubtitles(ctx, &out, "zh", []string{export.FormatSRT}, "", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	got := out.String()
	if strings.Contains(got, "失败") {
		t.Errorf("output = %q, want no failure lines for videos that were never tried", got)
	}
	if !strings.Contains(got, "已取消") {
		t.Errorf("output = %q, want it to say the run was cancelled", got)
	}
	if stats.Failed != 0 {
		t.Errorf("stats.Failed = %d, want 0: nothing failed, the run was stopped", stats.Failed)
	}
	rows, err := st.ListVideosFiltered(store.StateDownloadable, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("downloadable rows = %d, want 2 untouched by the cancelled run", len(rows))
	}
}

// TestSourceFlagMatchesTheNormalizedSearchKey: the probe writes the source key
// normalized ("search:劳动 法", one space), but --source is free text. Without
// normalizing the read side too, "search:劳动  法" or " search:劳动法 " matches
// nothing and the run reports "没有需要转写的无字幕视频" -- which reads like an empty
// source rather than a mistyped key.
func TestSourceFlagMatchesTheNormalizedSearchKey(t *testing.T) {
	rt := newTestRuntime(t)
	defer rt.close()
	seedVideoRow(t, rt.cfg.DataDir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 88, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle, SourceKey: "search:劳动 法",
	})
	overrideAudioPipeline(t, newAudioServer(t).URL)
	overrideASRBackend(t, &fakeASR{segments: []ir.Segment{{StartMs: 0, EndMs: 900, Text: "转写内容"}}}, nil)

	var out strings.Builder
	stats, err := rt.transcribe(context.Background(), &out, "zh", "", " search:劳动  法 ", nil)
	if err != nil {
		t.Fatalf("transcribe: %v\n%s", err, out.String())
	}
	if stats.OK != 1 {
		t.Fatalf("stats = %+v, output = %q, want the row found through the messy key", stats, out.String())
	}
}
