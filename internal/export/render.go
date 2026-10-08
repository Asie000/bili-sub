package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"bilibili-sub/internal/ir"
)

const (
	FormatSRT      = "srt"
	FormatText     = "txt"
	FormatMarkdown = "md"
	FormatJSON     = "json"
)

func Render(d *ir.Document, format string) ([]byte, error) {
	switch format {
	case FormatSRT:
		return renderSRT(d), nil
	case FormatText:
		return []byte(d.PlainText()), nil
	case FormatMarkdown:
		return renderMD(d), nil
	case FormatJSON:
		return renderJSON(d)
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}
}

// jsonSegment carries subtitle text only: non-srt formats must expose no timeline.
type jsonSegment struct {
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"`
}

type jsonDocument struct {
	Bvid     string        `json:"bvid"`
	Cid      int64         `json:"cid"`
	Title    string        `json:"title"`
	Author   string        `json:"author"`
	Lang     string        `json:"lang"`
	Segments []jsonSegment `json:"segments"`
}

func renderJSON(d *ir.Document) ([]byte, error) {
	var segs []jsonSegment
	if d.Segments != nil {
		segs = make([]jsonSegment, 0, len(d.Segments))
		for _, seg := range d.Segments {
			segs = append(segs, jsonSegment{Text: seg.Text, Speaker: seg.Speaker})
		}
	}
	doc := jsonDocument{
		Bvid:     d.Bvid,
		Cid:      d.Cid,
		Title:    d.Title,
		Author:   d.Author,
		Lang:     d.Lang,
		Segments: segs,
	}
	return json.MarshalIndent(doc, "", "  ")
}

func ts(ms int64) string {
	h := ms / 3600000
	m := (ms % 3600000) / 60000
	s := (ms % 60000) / 1000
	mmm := ms % 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, mmm)
}

func renderSRT(d *ir.Document) []byte {
	var b bytes.Buffer
	n := 0
	for _, seg := range d.Segments {
		t := strings.TrimSpace(seg.Text)
		if t == "" {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", n, ts(seg.StartMs), ts(seg.EndMs), t)
	}
	return b.Bytes()
}

func renderMD(d *ir.Document) []byte {
	var b bytes.Buffer
	title := d.Title
	if title == "" {
		title = d.Bvid
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	if d.Author != "" {
		fmt.Fprintf(&b, "- 作者：%s\n", d.Author)
	}
	fmt.Fprintf(&b, "- 视频：%s\n\n", d.Bvid)
	for _, seg := range d.Segments {
		t := strings.TrimSpace(seg.Text)
		if t == "" {
			continue
		}
		fmt.Fprintf(&b, "%s\n\n", t)
	}
	return b.Bytes()
}
