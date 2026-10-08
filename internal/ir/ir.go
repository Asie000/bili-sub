package ir

import "strings"

type Segment struct {
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"`
	HasTime bool   `json:"-"` // 导出时是否保留时间轴由渲染器决定
}

type Document struct {
	Bvid     string    `json:"bvid"`
	Cid      int64     `json:"cid"`
	Title    string    `json:"title"`
	Author   string    `json:"author"`
	Lang     string    `json:"lang"`
	Segments []Segment `json:"segments"`
}

func (d *Document) PlainText() string {
	parts := make([]string, 0, len(d.Segments))
	for _, s := range d.Segments {
		t := strings.TrimSpace(s.Text)
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

func (d *Document) DurationMs() int64 {
	var max int64
	for _, s := range d.Segments {
		if s.EndMs > max {
			max = s.EndMs
		}
	}
	return max
}
