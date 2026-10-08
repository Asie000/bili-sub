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
		return json.MarshalIndent(d, "", "  ")
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}
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
	for i, seg := range d.Segments {
		if strings.TrimSpace(seg.Text) == "" {
			continue
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, ts(seg.StartMs), ts(seg.EndMs), strings.TrimSpace(seg.Text))
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
