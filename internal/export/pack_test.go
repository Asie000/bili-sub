package export

import (
	"archive/zip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bilibili-sub/internal/ir"
)

func TestPack(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	docs := map[string]*ir.Document{
		"BV1": {Bvid: "BV1", Title: "视频一", Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "a"}}},
		"BV2": {Bvid: "BV2", Title: "视频二", Segments: []ir.Segment{{StartMs: 0, EndMs: 1000, Text: "b"}}},
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
}
