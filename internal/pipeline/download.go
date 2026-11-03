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
	"bilibili-sub/internal/filename"
	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/store"
)

// transcriptEngine names the subtitle source for rows in the transcripts table.
const transcriptEngine = "bilibili"

// ErrNoTrackForLang reports that the video has subtitles but none in the
// requested language family. It is a whole-run argument problem (a typo such as
// --lang ja, or a script mismatch), not a failure of this video, so the caller
// must not mark the row failed or re-download it on the next run.
var ErrNoTrackForLang = errors.New("pipeline: 没有匹配指定语言的字幕轨")

// ErrNoSubtitle reports that the video has no subtitle track at all. Since the
// subtitle check moved out of the listing step and into the download step, this
// is now a normal classification the downloader records (state no_subtitle) and
// moves on -- not a failure. It is distinct from ErrNoTrackForLang, which is
// about the requested language: one is "this video has none", the other is
// "this video has some, but not the family you asked for".
var ErrNoSubtitle = errors.New("pipeline: 视频没有字幕轨")

// ErrChargingLocked reports that the player endpoint refused the subtitle
// because the video is 充电专属 (or otherwise not ours to fetch). It is recorded
// as state charging_locked and never retried, like the prober used to do.
var ErrChargingLocked = errors.New("pipeline: 充电专属，无法获取字幕")

// subtitleHTTPTimeout bounds the subtitle-body fetch. The subtitle host is a CDN
// and the payload is small, so this is deliberately shorter than a video fetch.
const subtitleHTTPTimeout = 30 * time.Second

// browserUserAgent and browserReferer are the headers the subtitle and audio
// CDNs need; they mirror the unexported values bili.Client sends on API
// requests.
const (
	browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	browserReferer   = "https://www.bilibili.com"
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
		// The player endpoint refusing with a charge/permission code is a
		// classification, not a failure: record it and stop retrying this video.
		var apiErr *bili.APIError
		if errors.As(err, &apiErr) && chargingCodes[apiErr.Code] {
			return nil, fmt.Errorf("%w: %s（%s）", ErrChargingLocked, v.Bvid, apiErr.Message)
		}
		return nil, err
	}
	if !ok {
		if lang != "" {
			return nil, fmt.Errorf("%w：%s 没有 lang=%q 的字幕轨", ErrNoTrackForLang, v.Bvid, lang)
		}
		return nil, fmt.Errorf("%w: %s", ErrNoSubtitle, v.Bvid)
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
	base := filename.SanitizeSubtitleName(v.Author, v.Title, v.Bvid)
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
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Referer", browserReferer)

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
