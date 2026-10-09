package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
// and a transcripts row. PlayerBaseURL/PlayerV2URL/AISubtitleURL/OutputDir/HTTP
// are exported so tests can point them elsewhere.
type Downloader struct {
	Client        *bili.Client
	Store         *store.Store
	Enumerator    *Enumerator // for the WBI signer (may be nil)
	PlayerBaseURL string      // default https://api.bilibili.com/x/player/wbi/v2
	PlayerV2URL   string      // default bili.PlayerV2URL
	AISubtitleURL string      // default bili.AISubtitleStatURL
	OutputDir     string
	HTTP          *http.Client // for the subtitle body; default a 30s client
}

func NewDownloader(c *bili.Client, st *store.Store, en *Enumerator, outputDir string) *Downloader {
	return &Downloader{
		Client:        c,
		Store:         st,
		Enumerator:    en,
		PlayerBaseURL: defaultPlayerBaseURL,
		PlayerV2URL:   bili.PlayerV2URL,
		AISubtitleURL: bili.AISubtitleStatURL,
		OutputDir:     outputDir,
		HTTP:          &http.Client{Timeout: subtitleHTTPTimeout},
	}
}

func (d *Downloader) playerRequest() playerRequest {
	return playerRequest{d.Client, d.Enumerator, d.PlayerBaseURL, d.PlayerV2URL, d.AISubtitleURL}
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

	track, _, ok, err := resolveSubtitleTrack(ctx, d.playerRequest(), v, lang)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("pipeline: no subtitle track for %s with lang=%q", v.Bvid, lang)
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
	// The on-disk layout mirrors the export zip: one flat file per video, its
	// name prefixed with the UP主 so a user's files and archives line up.
	if err := os.MkdirAll(d.OutputDir, 0o755); err != nil {
		return nil, err
	}
	// Only multi-page videos get the _P{n} suffix, so pages of one video do not
	// overwrite each other's .srt; it is appended after the bvid bracket.
	base := util.SanitizeSubtitleName(v.Author, v.Title, v.Bvid)
	if v.PageIndex > 1 {
		base += "_P" + strconv.Itoa(v.PageIndex)
	}
	srtPath := filepath.Join(d.OutputDir, base+".srt")
	if err := os.WriteFile(srtPath, srt, 0o644); err != nil {
		return nil, err
	}

	// The transcripts column is the canonical IR (with timeline), so the export
	// command can rebuild srt from the DB later; export.FormatJSON is a lossy
	// projection and must not be stored here.
	irJSON, err := json.Marshal(doc)
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

// normalizeSubtitleURL turns the three URL forms B站 serves into a fetchable
// absolute URL: a protocol-relative URL ("//host/...") gets an https: prefix, an
// existing http(s):// scheme is kept, and a scheme-less "host/..." gets https://.
func normalizeSubtitleURL(raw string) string {
	switch {
	case strings.HasPrefix(raw, "//"):
		return "https:" + raw
	case strings.HasPrefix(raw, "http://"), strings.HasPrefix(raw, "https://"):
		return raw
	default:
		return "https://" + raw
	}
}

// fetchBody downloads and decodes one subtitle track. Its URL may be
// protocol-relative in real responses ("//aisubtitle.hdslb.com/..."), so it is
// normalized to an absolute https URL before the request.
func (d *Downloader) fetchBody(ctx context.Context, rawURL string) ([]ir.Segment, error) {
	rawURL = normalizeSubtitleURL(rawURL)
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
			StartMs: int64(math.Round(s.From * 1000)),
			EndMs:   int64(math.Round(s.To * 1000)),
			Text:    s.Content,
		})
	}
	return segments, nil
}
