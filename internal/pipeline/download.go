package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/export"
	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/store"
	"bilibili-sub/internal/util"
)

// transcriptEngine names the subtitle source for rows in the transcripts table.
const transcriptEngine = "bilibili"

// subtitleHTTPTimeout bounds the subtitle-body fetch. The subtitle host is a CDN
// and the payload is small, so this is deliberately shorter than a video fetch.
const subtitleHTTPTimeout = 30 * time.Second

// subtitleUserAgent and subtitleReferer are the headers the subtitle CDN needs;
// they mirror the unexported values bili.Client sends on API requests.
const (
	subtitleUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	subtitleReferer   = "https://www.bilibili.com"
)

// Downloader turns one video's subtitle track into an IR document, an .srt file
// and a transcripts row. PlayerBaseURL/OutputDir/HTTP are exported so tests can
// point them elsewhere.
type Downloader struct {
	Client        *bili.Client
	Store         *store.Store
	Enumerator    *Enumerator // for the WBI signer (may be nil)
	PlayerBaseURL string      // default https://api.bilibili.com/x/player/wbi/v2
	OutputDir     string
	HTTP          *http.Client // for the subtitle body; default a 30s client
}

func NewDownloader(c *bili.Client, st *store.Store, en *Enumerator, outputDir string) *Downloader {
	return &Downloader{
		Client:        c,
		Store:         st,
		Enumerator:    en,
		PlayerBaseURL: defaultPlayerBaseURL,
		OutputDir:     outputDir,
		HTTP:          &http.Client{Timeout: subtitleHTTPTimeout},
	}
}

// FetchSubtitle downloads the best-matching subtitle track for v, writes the
// rendered .srt under OutputDir, persists the IR JSON in transcripts and marks
// the video fetched. It returns the document it built.
//
// The subtitle body at subtitle_url is not a {code,message,data} envelope: it is
// a bare {"body":[{"from":<sec>,"to":<sec>,"content":"..."}]}, so it is fetched
// with this downloader's own http.Client instead of bili.Client.Get.
func (d *Downloader) FetchSubtitle(ctx context.Context, v store.Video, lang string) (*ir.Document, error) {
	if d.Client == nil || d.Store == nil {
		return nil, errors.New("pipeline: Downloader needs a non-nil Client and Store")
	}

	params := map[string]string{
		"bvid": v.Bvid,
		"cid":  strconv.FormatInt(v.Cid, 10),
	}
	if d.Enumerator != nil {
		signer, err := d.Enumerator.Signer(ctx)
		if err != nil {
			return nil, err
		}
		params, err = signer.Sign(params)
		if err != nil {
			return nil, err
		}
	}

	var resp struct {
		Subtitle struct {
			Subtitles []bili.SubtitleInfo `json:"subtitles"`
		} `json:"subtitle"`
	}
	if err := d.Client.Get(ctx, d.PlayerBaseURL, params, &resp); err != nil {
		return nil, err
	}

	// A track without a URL cannot be fetched, so it must not win PickLang.
	usable := make([]bili.SubtitleInfo, 0, len(resp.Subtitle.Subtitles))
	for _, s := range resp.Subtitle.Subtitles {
		if s.SubURL != "" {
			usable = append(usable, s)
		}
	}
	track, ok := PickLang(usable, lang)
	if !ok {
		return nil, fmt.Errorf("pipeline: no subtitle track for %s with lang=%q (%d usable tracks)", v.Bvid, lang, len(usable))
	}

	segments, err := d.fetchBody(ctx, track.SubURL)
	if err != nil {
		return nil, err
	}

	doc := &ir.Document{
		Bvid:     v.Bvid,
		Cid:      v.Cid,
		Title:    v.Title,
		Author:   v.Author,
		Lang:     track.Lan,
		Segments: segments,
	}

	srt, err := export.Render(doc, export.FormatSRT)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(d.OutputDir, 0o755); err != nil {
		return nil, err
	}
	srtPath := filepath.Join(d.OutputDir, util.SanitizeFilename(v.Title, v.Bvid)+".srt")
	if err := os.WriteFile(srtPath, srt, 0o644); err != nil {
		return nil, err
	}

	irJSON, err := export.Render(doc, export.FormatJSON)
	if err != nil {
		return nil, err
	}
	if err := d.Store.SaveTranscript(v.ID, transcriptEngine, track.Lan, string(irJSON)); err != nil {
		return nil, err
	}
	if err := d.Store.SetState(v.ID, store.StateFetched, track.Lan); err != nil {
		return nil, err
	}
	return doc, nil
}

// fetchBody downloads and decodes one subtitle track. Its URL is
// protocol-relative in real responses ("//aisubtitle.hdslb.com/..."), so it is
// upgraded to https before the request.
func (d *Downloader) fetchBody(ctx context.Context, rawURL string) ([]ir.Segment, error) {
	if strings.HasPrefix(rawURL, "//") {
		rawURL = "https:" + rawURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", subtitleUserAgent)
	req.Header.Set("Referer", subtitleReferer)

	hc := d.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: subtitleHTTPTimeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pipeline: subtitle %s: HTTP %d", rawURL, resp.StatusCode)
	}

	var body struct {
		Body []struct {
			From    float64 `json:"from"`
			To      float64 `json:"to"`
			Content string  `json:"content"`
		} `json:"body"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("pipeline: subtitle %s: %w", rawURL, err)
	}

	segments := make([]ir.Segment, 0, len(body.Body))
	for _, s := range body.Body {
		segments = append(segments, ir.Segment{
			StartMs: int64(s.From * 1000),
			EndMs:   int64(s.To * 1000),
			Text:    s.Content,
		})
	}
	return segments, nil
}
