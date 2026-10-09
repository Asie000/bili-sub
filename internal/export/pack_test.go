package export

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/util"
)

func TestPack(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	docs := map[string]*ir.Document{
		"BV1": {Bvid: "BV1", Title: "视频一", Author: "UP甲", Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "a"}}},
		"BV2": {Bvid: "BV2", Title: "视频二", Author: "UP乙", Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "b"}}},
	}
	z, err := Pack(dir, now, docs, []string{"srt", "txt"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(z) != "20261008-4.zip" {
		t.Fatalf("bad name: %s", z)
	}
	r, err := zip.OpenReader(z)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	wantPrefix := "20261008-4/"
	for _, f := range r.File {
		if !strings.HasPrefix(f.Name, wantPrefix) {
			t.Fatalf("entry not wrapped: %s", f.Name)
		}
	}
	// 2 视频 × 2 格式 = 4 个文件
	if len(r.File) != 4 {
		t.Fatalf("entries=%d", len(r.File))
	}

	// 每个 entry 必须嵌在 归档基名/作者 两层目录下，文件名必须等于
	// SanitizeFilename 的结果加格式后缀。
	got := make(map[string]bool, len(r.File))
	for _, f := range r.File {
		got[f.Name] = true
		parts := strings.SplitN(f.Name, "/", 3)
		if len(parts) != 3 || parts[0] != strings.TrimSuffix(wantPrefix, "/") {
			t.Errorf("entry %q is not nested under %q", f.Name, wantPrefix)
		}
	}
	for key, d := range docs {
		folder := util.SanitizeDirName(d.Author)
		for _, format := range []string{"srt", "txt"} {
			want := wantPrefix + folder + "/" + util.SanitizeFilename(d.Title, d.Bvid) + "." + format
			if !got[want] {
				t.Errorf("missing entry %q (doc key %q)", want, key)
			}
		}
	}
}

func TestPackRejectsDuplicateEntryName(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	// 两个 map key 不同，但 Author+Title+Bvid 相同 => 同一个 entry 名。
	docs := map[string]*ir.Document{
		"first":  {Bvid: "BV1", Title: "同名", Author: "同一UP", Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "a"}}},
		"second": {Bvid: "BV1", Title: "同名", Author: "同一UP", Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "b"}}},
	}
	z, err := Pack(dir, now, docs, []string{"srt"})
	if err == nil {
		t.Fatalf("Pack accepted duplicate entries, zip=%s", z)
	}
	stem := util.SanitizeFilename("同名", "BV1")
	if !strings.Contains(err.Error(), stem) {
		t.Fatalf("error does not name the duplicate stem %q: %v", stem, err)
	}
	if z != "" {
		t.Fatalf("zipPath=%q on error, want empty", z)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("left %d file(s) behind on failure: %v", len(entries), entries)
	}
}

// TestPackDifferentAuthorsNoCollision pins the folder level's reason to exist:
// two documents sharing a title and bvid but belonging to different authors now
// get distinct entries instead of being rejected as duplicates.
func TestPackDifferentAuthorsNoCollision(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	docs := map[string]*ir.Document{
		"first":  {Bvid: "BV1", Title: "同名", Author: "UP甲", Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "a"}}},
		"second": {Bvid: "BV1", Title: "同名", Author: "UP乙", Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "b"}}},
	}
	z, err := Pack(dir, now, docs, []string{"srt"})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	r, err := zip.OpenReader(z)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.File) != 2 {
		t.Fatalf("entries = %d, want 2 (one per author)", len(r.File))
	}
	stem := util.SanitizeFilename("同名", "BV1")
	base := "20261008-2/"
	for _, folder := range []string{"UP甲", "UP乙"} {
		want := base + folder + "/" + stem + ".srt"
		found := false
		for _, f := range r.File {
			if f.Name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing entry %q; got %v", want, entryNames(r.File))
		}
	}
}

// entryNames lists a zip's entry names for failure messages.
func entryNames(files []*zip.File) []string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name)
	}
	return names
}

func TestPackRejectsNilDocument(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	docs := map[string]*ir.Document{"broken": nil}
	z, err := Pack(dir, now, docs, []string{"srt"})
	if err == nil {
		t.Fatalf("Pack accepted nil document, zip=%s", z)
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error does not name the map key: %v", err)
	}
	if z != "" {
		t.Fatalf("zipPath=%q on error, want empty", z)
	}
}

func TestPackCreatesOutDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "out")
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	docs := map[string]*ir.Document{
		"BV1": {Bvid: "BV1", Title: "视频一", Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "a"}}},
	}
	z, err := Pack(dir, now, docs, []string{"srt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(z); err != nil {
		t.Fatalf("zip not written into created out dir: %v", err)
	}
}

func TestPackUnsupportedFormatLeavesNoArchive(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	docs := map[string]*ir.Document{
		"BV1": {Bvid: "BV1", Title: "视频一", Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "a"}}},
	}
	z, err := Pack(dir, now, docs, []string{"srt", "nope"})
	if err == nil {
		t.Fatalf("Pack accepted unsupported format, zip=%s", z)
	}
	if z != "" {
		t.Fatalf("zipPath=%q on error, want empty", z)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("left %d file(s) behind on failure: %v", len(entries), entries)
	}
}
