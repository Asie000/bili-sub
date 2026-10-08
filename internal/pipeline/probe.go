package pipeline

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/store"
)

const defaultPlayerBaseURL = "https://api.bilibili.com/x/player/wbi/v2"

// ProbeResult reports what a single video's player endpoint revealed: whether
// subtitles exist and which ones, and whether the video is locked behind
// 充电专属/权限 instead.
type ProbeResult struct {
	VideoID        int64
	Bvid           string
	HasSubtitle    bool
	Langs          []bili.SubtitleInfo
	CanDownload    bool
	ChargingLocked bool
}

// Prober asks a video's player endpoint for its subtitle list and persists the
// resulting state. The BaseURL fields are exported so tests can point them at
// httptest servers.
type Prober struct {
	Client        *bili.Client
	Store         *store.Store
	Enumerator    *Enumerator
	PlayerBaseURL string // x/player/wbi/v2, WBI-signed, used when the video has an aid
	PlayerV2URL   string // x/player/v2, unsigned fallback when the aid is unknown
	AISubtitleURL string // x/player/v2/ai/subtitle/search/stat, resolves ai-* track URLs
}

func NewProber(c *bili.Client, st *store.Store, en *Enumerator) *Prober {
	return &Prober{
		Client:        c,
		Store:         st,
		Enumerator:    en,
		PlayerBaseURL: defaultPlayerBaseURL,
		PlayerV2URL:   bili.PlayerV2URL,
		AISubtitleURL: bili.AISubtitleStatURL,
	}
}

// playerRequest is the endpoint configuration a subtitle lookup needs. Prober
// and Downloader each build one from their own fields so both go through the
// same endpoint selection and AI fallback.
type playerRequest struct {
	client        *bili.Client
	enumerator    *Enumerator
	playerBaseURL string
	playerV2URL   string
	aiSubtitleURL string
}

func (p *Prober) playerRequest() playerRequest {
	return playerRequest{p.Client, p.Enumerator, p.PlayerBaseURL, p.PlayerV2URL, p.AISubtitleURL}
}

// chargingCodes are the player-endpoint failures that mean "this video's
// subtitles are not ours to fetch": no permission (usually 充电专属), a
// missing/private video, and the explicit 充电专属 seal.
var chargingCodes = map[int]bool{
	-10403: true,
	-404:   true,
	-62002: true,
}

// ProbeVideo asks the player endpoint for a video's subtitles and persists the
// resulting state. A charging/permission failure (see chargingCodes) is not an
// error: it is a recorded outcome (ChargingLocked), so the pipeline can skip the
// video. Every other failure — session expiry, rate limiting, risk control, a
// transport error — is returned and leaves the row's state untouched, because
// none of them mean the video is charge-locked.
func (p *Prober) ProbeVideo(ctx context.Context, v store.Video) (*ProbeResult, error) {
	if p.Client == nil || p.Store == nil {
		return nil, errors.New("pipeline: Prober needs a non-nil Client and Store")
	}
	track, usable, ok, err := resolveSubtitleTrack(ctx, p.playerRequest(), v, "")
	if err != nil {
		var apiErr *bili.APIError
		if errors.As(err, &apiErr) && chargingCodes[apiErr.Code] {
			return p.markChargingLocked(v)
		}
		return nil, err
	}

	res := &ProbeResult{VideoID: v.ID, Bvid: v.Bvid}
	if !ok {
		if err := p.Store.SetState(v.ID, store.StateNoSubtitle, ""); err != nil {
			return nil, err
		}
		return res, nil
	}
	if err := p.Store.SetState(v.ID, store.StateDownloadable, track.Lan); err != nil {
		return nil, err
	}
	res.HasSubtitle = true
	res.CanDownload = true
	res.Langs = usable
	return res, nil
}

func (p *Prober) markChargingLocked(v store.Video) (*ProbeResult, error) {
	if err := p.Store.SetState(v.ID, store.StateChargingLocked, ""); err != nil {
		return nil, err
	}
	return &ProbeResult{VideoID: v.ID, Bvid: v.Bvid, ChargingLocked: true}, nil
}

// resolveSubtitleTrack asks the correct player endpoint for v's subtitle list,
// picks the best track for lang (the default priority order when lang == ""),
// and resolves its download URL. Tracks with an empty subtitle_url are kept
// rather than dropped, because AI-only videos expose their subtitle that way;
// an ai-* track's URL is fetched from the AI subtitle stat endpoint. It returns
// the chosen track, the usable URL-bearing tracks, and whether one was resolved.
// Shared by Prober and Downloader so the two cannot drift.
func resolveSubtitleTrack(ctx context.Context, pr playerRequest, v store.Video, lang string) (bili.SubtitleInfo, []bili.SubtitleInfo, bool, error) {
	params := map[string]string{
		"cid": strconv.FormatInt(v.Cid, 10),
	}
	endpoint := pr.playerV2URL
	if v.Aid > 0 {
		endpoint = pr.playerBaseURL
		params["aid"] = strconv.FormatInt(v.Aid, 10)
		if pr.enumerator != nil {
			signer, err := pr.enumerator.Signer(ctx)
			if err != nil {
				return bili.SubtitleInfo{}, nil, false, err
			}
			params, err = signer.Sign(params)
			if err != nil {
				return bili.SubtitleInfo{}, nil, false, err
			}
		}
	} else {
		params["bvid"] = v.Bvid
	}

	var resp struct {
		Subtitle struct {
			Subtitles []bili.SubtitleInfo `json:"subtitles"`
		} `json:"subtitle"`
	}
	if err := pr.client.Get(ctx, endpoint, params, &resp); err != nil {
		return bili.SubtitleInfo{}, nil, false, err
	}

	track, ok := PickLang(resp.Subtitle.Subtitles, lang)
	if !ok {
		return bili.SubtitleInfo{}, nil, false, nil
	}
	if track.SubURL == "" && strings.HasPrefix(track.Lan, "ai-") && v.Aid > 0 {
		u, err := fetchAISubtitleURL(ctx, pr, v)
		if err != nil {
			return bili.SubtitleInfo{}, nil, false, err
		}
		track.SubURL = u
	}
	if track.SubURL == "" {
		return bili.SubtitleInfo{}, nil, false, nil
	}

	usable := make([]bili.SubtitleInfo, 0, len(resp.Subtitle.Subtitles))
	for _, s := range resp.Subtitle.Subtitles {
		if s.Lan == track.Lan {
			s = track
		}
		if s.SubURL != "" {
			usable = append(usable, s)
		}
	}
	return track, usable, true, nil
}

// fetchAISubtitleURL resolves the real subtitle body URL of an AI track from the
// AI subtitle stat endpoint. The call is unsigned and carries the login cookie
// through the shared client, like the player request.
func fetchAISubtitleURL(ctx context.Context, pr playerRequest, v store.Video) (string, error) {
	params := map[string]string{
		"aid": strconv.FormatInt(v.Aid, 10),
		"cid": strconv.FormatInt(v.Cid, 10),
	}
	var resp struct {
		SubtitleURL string `json:"subtitle_url"`
	}
	if err := pr.client.Get(ctx, pr.aiSubtitleURL, params, &resp); err != nil {
		return "", err
	}
	return resp.SubtitleURL, nil
}

// langPriority is the default subtitle preference order: human Chinese first,
// then the AI Chinese track, then English, then anything.
var langPriority = []string{"zh-Hans", "zh-CN", "zh", "ai-zh", "en", "en-US"}

// PickLang chooses one subtitle track. A non-empty pref wins when it matches a
// track's lan (case-insensitively, exact match before prefix); otherwise the
// default priority order decides, falling back to the first available track.
func PickLang(available []bili.SubtitleInfo, pref string) (bili.SubtitleInfo, bool) {
	if pref != "" {
		want := strings.ToLower(pref)
		for _, s := range available {
			if strings.ToLower(s.Lan) == want {
				return s, true
			}
		}
		for _, s := range available {
			if strings.HasPrefix(strings.ToLower(s.Lan), want+"-") {
				return s, true
			}
		}
		return bili.SubtitleInfo{}, false
	}
	for _, p := range langPriority {
		for _, s := range available {
			if strings.EqualFold(s.Lan, p) {
				return s, true
			}
		}
	}
	if len(available) > 0 {
		return available[0], true
	}
	return bili.SubtitleInfo{}, false
}
