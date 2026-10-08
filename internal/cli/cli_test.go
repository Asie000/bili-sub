package cli

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/ir"
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
	want := []string{"download", "export", "login", "probe", "reset", "retry", "serve", "status", "transcribe"}
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
	id, err := st.UpsertVideo(&store.Video{Bvid: "BV1uv411q7Mv", Cid: 1, Title: "测试标题", State: store.StateFetched, PageIndex: 1})
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

// TestDownloadTranscribeReportsMissingAudioSource pins the Task 15 wiring:
// `download --transcribe` builds the faster-whisper backend but, because v1
// fetches no play audio, it reports the limitation for no-subtitle videos
// instead of pretending to transcribe. Nothing is spawned (the backend starts
// lazily), so the command stays non-fatal.
func TestDownloadTranscribeReportsMissingAudioSource(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)

	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.UpsertVideo(&store.Video{Bvid: "BV1nosubtitle", Cid: 1, Title: "无字幕", State: store.StateNoSubtitle}); err != nil {
		t.Fatalf("upsert no-subtitle video: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	root := NewRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"download", "--transcribe"})
	if err := root.Execute(); err != nil {
		t.Fatalf("download --transcribe: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "faster-whisper") {
		t.Errorf("output = %q, want the ASR backend name", got)
	}
	if !strings.Contains(got, "音频源") {
		t.Errorf("output = %q, want an honest note about the missing audio source", got)
	}
}

// TestWriteSubtitleFormats pins the filename agreement with
// pipeline.Downloader.FetchSubtitle: the downloader owns the .srt, the command
// writes the other formats beside it, and only multi-P videos get a _P{n}
// suffix.
func TestWriteSubtitleFormats(t *testing.T) {
	dir := t.TempDir()
	v := store.Video{Bvid: "BV1uv411q7Mv", Title: "标题", PageIndex: 2}
	doc := &ir.Document{
		Bvid: "BV1uv411q7Mv", Title: "标题",
		Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "你好"}},
	}
	if err := writeSubtitleFormats(dir, v, doc, []string{"srt", "txt", "json"}); err != nil {
		t.Fatalf("writeSubtitleFormats: %v", err)
	}

	stem := "标题 [BV1uv411q7Mv]_P2"
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
