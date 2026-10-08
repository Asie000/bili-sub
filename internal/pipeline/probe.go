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

type Prober struct {
	Client        *bili.Client
	Store         *store.Store
	Enumerator    *Enumerator
	PlayerBaseURL string
}

func NewProber(c *bili.Client, st *store.Store, en *Enumerator) *Prober {
	return &Prober{
		Client:        c,
		Store:         st,
		Enumerator:    en,
		PlayerBaseURL: defaultPlayerBaseURL,
	}
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
	params := map[string]string{
		"bvid": v.Bvid,
		"cid":  strconv.FormatInt(v.Cid, 10),
	}
	if p.Enumerator != nil {
		signer, err := p.Enumerator.Signer(ctx)
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
	err := p.Client.Get(ctx, p.PlayerBaseURL, params, &resp)
	if err != nil {
		var apiErr *bili.APIError
		if errors.As(err, &apiErr) && chargingCodes[apiErr.Code] {
			return p.markChargingLocked(v)
		}
		return nil, err
	}

	// A track without a URL cannot be fetched; ignore those rather than
	// marking the video downloadable, so Langs only ever lists usable tracks.
	usable := make([]bili.SubtitleInfo, 0, len(resp.Subtitle.Subtitles))
	for _, s := range resp.Subtitle.Subtitles {
		if s.SubURL != "" {
			usable = append(usable, s)
		}
	}

	res := &ProbeResult{VideoID: v.ID, Bvid: v.Bvid}
	if len(usable) == 0 {
		if err := p.Store.SetState(v.ID, store.StateNoSubtitle, ""); err != nil {
			return nil, err
		}
		return res, nil
	}
	lang := ""
	if best, ok := PickLang(usable, ""); ok {
		lang = best.Lan
	}
	if err := p.Store.SetState(v.ID, store.StateDownloadable, lang); err != nil {
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
