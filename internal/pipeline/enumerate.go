package pipeline

import (
	"context"
	"path"
	"strconv"
	"strings"
	"sync"

	"bilibili-sub/internal/bili"
)

const (
	defaultNavURL       = "https://api.bilibili.com/x/web-interface/nav"
	defaultSpaceBaseURL = "https://api.bilibili.com/x/space/wbi/arc/search"
	defaultFavBaseURL   = "https://api.bilibili.com/x/v3/fav/resource/list"
	defaultViewBaseURL  = "https://api.bilibili.com/x/web-interface/view"

	spacePageSize = 30
	favPageSize   = 20
)

// Enumerator turns a source (UP主 mid or 收藏夹 media_id) into the list of its
// videos with pages/cids resolved. The BaseURL fields are exported so tests can
// point them at httptest servers.
type Enumerator struct {
	Client       *bili.Client
	SpaceBaseURL string
	FavBaseURL   string
	ViewBaseURL  string
	NavURL       string

	signer *bili.WBISigner
	// mu guards signer: probes are dispatched concurrently, so the lazy build
	// must not race on the field or fetch the nav keys twice.
	mu sync.Mutex
}

func NewEnumerator(c *bili.Client) *Enumerator {
	return &Enumerator{
		Client:       c,
		SpaceBaseURL: defaultSpaceBaseURL,
		FavBaseURL:   defaultFavBaseURL,
		ViewBaseURL:  defaultViewBaseURL,
		NavURL:       defaultNavURL,
	}
}

// ResolveVideo fetches a single video's full info (pages/cid included).
func (e *Enumerator) ResolveVideo(ctx context.Context, bvid string) (*bili.VideoInfo, error) {
	var info bili.VideoInfo
	if err := e.Client.Get(ctx, e.ViewBaseURL, map[string]string{"bvid": bvid}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// EnumerateSpace lists an UP主's videos (ps=30 per page) until the reported
// video count is covered or a page comes back empty. Space requests are
// WBI-signed.
func (e *Enumerator) EnumerateSpace(ctx context.Context, mid string) ([]*bili.VideoInfo, error) {
	signer, err := e.Signer(ctx)
	if err != nil {
		return nil, err
	}
	var videos []*bili.VideoInfo
	for pn := 1; ; pn++ {
		params, err := signer.Sign(map[string]string{
			"mid":           mid,
			"ps":            strconv.Itoa(spacePageSize),
			"pn":            strconv.Itoa(pn),
			"tid":           "0",
			"keyword":       "",
			"order":         "pubdate",
			"web_location":  "1550101",
			"order_avoided": "true",
		})
		if err != nil {
			return nil, err
		}
		var resp struct {
			List struct {
				Vlist []struct {
					Bvid string `json:"bvid"`
					Name string `json:"name"`
				} `json:"vlist"`
			} `json:"list"`
			Page struct {
				Count int `json:"count"`
			} `json:"page"`
		}
		if err := e.Client.Get(ctx, e.SpaceBaseURL, params, &resp); err != nil {
			return nil, err
		}
		if len(resp.List.Vlist) == 0 {
			break
		}
		for _, v := range resp.List.Vlist {
			if v.Bvid == "" {
				continue
			}
			info, err := e.ResolveVideo(ctx, v.Bvid)
			if err != nil {
				return nil, err
			}
			videos = append(videos, info)
		}
		if resp.Page.Count > 0 && len(videos) >= resp.Page.Count {
			break
		}
	}
	return videos, nil
}

// EnumerateFav lists a 收藏夹's videos (ps=20 per page) until has_more is false.
func (e *Enumerator) EnumerateFav(ctx context.Context, mediaID string) ([]*bili.VideoInfo, error) {
	var videos []*bili.VideoInfo
	for pn := 1; ; pn++ {
		var resp struct {
			Medias []struct {
				Bvid string `json:"bvid"`
			} `json:"medias"`
			HasMore bool `json:"has_more"`
		}
		params := map[string]string{
			"media_id": mediaID,
			"ps":       strconv.Itoa(favPageSize),
			"pn":       strconv.Itoa(pn),
			"platform": "web",
		}
		if err := e.Client.Get(ctx, e.FavBaseURL, params, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Medias {
			if m.Bvid == "" {
				continue
			}
			info, err := e.ResolveVideo(ctx, m.Bvid)
			if err != nil {
				return nil, err
			}
			videos = append(videos, info)
		}
		if !resp.HasMore {
			break
		}
	}
	return videos, nil
}

// Signer returns the lazily-built WBI signer, fetching the keys from nav at
// most once per Enumerator. It is safe for concurrent use.
func (e *Enumerator) Signer(ctx context.Context) (*bili.WBISigner, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.signer != nil {
		return e.signer, nil
	}
	var nav bili.NavInfo
	if err := e.Client.Get(ctx, e.NavURL, nil, &nav); err != nil {
		return nil, err
	}
	s, err := bili.NewWBISigner(wbiKey(nav.WbiImg.ImgURL), wbiKey(nav.WbiImg.SubURL))
	if err != nil {
		return nil, err
	}
	e.signer = s
	return s, nil
}

// wbiKey reduces a nav wbi_img URL to its bare key, e.g.
// https://i0.hdslb.com/bfs/wbi/3cfc0ad5....png -> 3cfc0ad5....
func wbiKey(rawURL string) string {
	base := path.Base(rawURL)
	if i := strings.LastIndex(base, "."); i > 0 {
		return base[:i]
	}
	return base
}
