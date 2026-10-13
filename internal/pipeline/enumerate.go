package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"bilibili-sub/internal/bili"
)

const (
	defaultNavURL        = "https://api.bilibili.com/x/web-interface/nav"
	defaultSpaceBaseURL  = "https://api.bilibili.com/x/space/wbi/arc/search"
	defaultFavBaseURL    = "https://api.bilibili.com/x/v3/fav/resource/list"
	defaultViewBaseURL   = "https://api.bilibili.com/x/web-interface/view"
	defaultSeasonBaseURL = "https://api.bilibili.com/x/polymer/web-space/seasons_archives_list"
	defaultSearchBaseURL = "https://api.bilibili.com/x/web-interface/wbi/search/type"

	spacePageSize = 30
	favPageSize   = 20
	// searchPageSize is what the search endpoint hands back per page.
	searchPageSize = 20
	// seasonPageSize is what seasons_archives_list accepts. Asking for more than
	// 30 answers with a null data payload instead of clamping, so this is a real
	// limit and not a tuning knob.
	seasonPageSize = 30
)

// DefaultSearchLimit is how many hits a search collects unless the caller asks
// for a different number. One keyword can match a thousand videos, every hit
// costs a view call for its cid, and search is the endpoint most likely to answer
// -412 -- so an unbounded default would turn one command into eight minutes of
// requests and a thousand new rows.
const DefaultSearchLimit = 50

// Enumerator turns a source (UP主 mid, 收藏夹 media_id or 合集 sid) into the list
// of its videos with pages/cids resolved. The BaseURL fields are exported so
// tests can point them at httptest servers.
type Enumerator struct {
	Client        *bili.Client
	SpaceBaseURL  string
	FavBaseURL    string
	ViewBaseURL   string
	SeasonBaseURL string
	SearchBaseURL string
	NavURL        string

	signer *bili.WBISigner
	// mu guards signer: probes are dispatched concurrently, so the lazy build
	// must not race on the field or fetch the nav keys twice.
	mu sync.Mutex
}

func NewEnumerator(c *bili.Client) *Enumerator {
	return &Enumerator{
		Client:        c,
		SpaceBaseURL:  defaultSpaceBaseURL,
		FavBaseURL:    defaultFavBaseURL,
		ViewBaseURL:   defaultViewBaseURL,
		SeasonBaseURL: defaultSeasonBaseURL,
		SearchBaseURL: defaultSearchBaseURL,
		NavURL:        defaultNavURL,
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
// listingRetryBackoffs are the waits before retrying a listing page that
// bilibili answered with an API-level rate-limit code. The space endpoint
// intermittently answers `-404 啥都木有` (and occasionally `-412`) under load
// even for an account that clearly has videos, so a couple of backed-off
// retries turn a spurious failure into a normal result.
var listingRetryBackoffs = []time.Duration{2 * time.Second, 5 * time.Second, 12 * time.Second}

// fetchListingPage calls Get and retries the transient rate-limit codes.
func (e *Enumerator) fetchListingPage(ctx context.Context, url string, params map[string]string, out any) error {
	for attempt := 0; ; attempt++ {
		err := e.Client.Get(ctx, url, params, out)
		if err == nil {
			return nil
		}
		var apiErr *bili.APIError
		if !errors.As(err, &apiErr) || (apiErr.Code != -404 && apiErr.Code != -412) {
			return err
		}
		if attempt >= len(listingRetryBackoffs) {
			return fmt.Errorf("B站返回 %d（%s）：可能被临时限流，请等几分钟再试", apiErr.Code, apiErr.Message)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(listingRetryBackoffs[attempt]):
		}
	}
}

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
		if err := e.fetchListingPage(ctx, e.SpaceBaseURL, params, &resp); err != nil {
			return nil, err
		}
		if len(resp.List.Vlist) == 0 {
			break
		}
		bvids := make([]string, 0, len(resp.List.Vlist))
		for _, v := range resp.List.Vlist {
			bvids = append(bvids, v.Bvid)
		}
		infos, err := e.resolveAll(ctx, bvids)
		if err != nil {
			return nil, err
		}
		videos = append(videos, infos...)
		if resp.Page.Count > 0 && len(videos) >= resp.Page.Count {
			break
		}
	}
	return videos, nil
}

// resolveAll turns listing bvids into full VideoInfos. Every listing that feeds
// it -- 合集, UP主主页, 收藏夹 -- carries no cid, so this is where a source becomes
// rows the rest of the pipeline can actually fetch. A video that is gone (-404)
// is skipped rather than failing the whole listing; anything else is returned.
func (e *Enumerator) resolveAll(ctx context.Context, bvids []string) ([]*bili.VideoInfo, error) {
	infos := make([]*bili.VideoInfo, 0, len(bvids))
	for _, bvid := range bvids {
		if bvid == "" {
			continue
		}
		info, err := e.ResolveVideo(ctx, bvid)
		if err != nil {
			var apiErr *bili.APIError
			if errors.As(err, &apiErr) && apiErr.Code == -404 {
				continue
			}
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// EnumerateSeason lists a 合集 (ps=30 per page) until the listing reports no
// more. It takes both ids because the endpoint does: the mid says whose season
// it is, the season_id which season.
func (e *Enumerator) EnumerateSeason(ctx context.Context, mid, seasonID string) ([]*bili.VideoInfo, error) {
	var videos []*bili.VideoInfo
	for pn := 1; ; pn++ {
		var resp struct {
			Archives []struct {
				Bvid string `json:"bvid"`
			} `json:"archives"`
			Page struct {
				Total int `json:"total"`
			} `json:"page"`
		}
		params := map[string]string{
			"mid":          mid,
			"season_id":    seasonID,
			"sort_reverse": "false",
			"page_num":     strconv.Itoa(pn),
			"page_size":    strconv.Itoa(seasonPageSize),
		}
		if err := e.fetchListingPage(ctx, e.SeasonBaseURL, params, &resp); err != nil {
			return nil, err
		}
		if len(resp.Archives) == 0 {
			break
		}
		bvids := make([]string, 0, len(resp.Archives))
		for _, a := range resp.Archives {
			bvids = append(bvids, a.Bvid)
		}
		infos, err := e.resolveAll(ctx, bvids)
		if err != nil {
			return nil, err
		}
		videos = append(videos, infos...)
		if resp.Page.Total > 0 && len(videos) >= resp.Page.Total {
			break
		}
	}
	return videos, nil
}

// EnumerateSearch lists a keyword's video hits, stopping at limit resolved videos
// (0 means no limit). The cap is a promise about how many rows one run creates,
// so it cuts the list rather than rounding up to a page boundary.
//
// The search response's own title is ignored on purpose: it can carry <em
// class="keyword"> highlight markup, and every hit needs a view call anyway for
// the cid -- which brings the clean title, the integer duration (search reports
// "2:12" as a string) and the 充电 flags with it.
func (e *Enumerator) EnumerateSearch(ctx context.Context, keyword string, limit int) ([]*bili.VideoInfo, error) {
	signer, err := e.Signer(ctx)
	if err != nil {
		return nil, err
	}
	var videos []*bili.VideoInfo
	for page := 1; ; page++ {
		params, err := signer.Sign(map[string]string{
			"search_type": "video",
			"keyword":     keyword,
			"page":        strconv.Itoa(page),
			"page_size":   strconv.Itoa(searchPageSize),
		})
		if err != nil {
			return nil, err
		}
		var resp struct {
			NumPages int `json:"numPages"`
			Result   []struct {
				Bvid string `json:"bvid"`
			} `json:"result"`
		}
		if err := e.fetchListingPage(ctx, e.SearchBaseURL, params, &resp); err != nil {
			return nil, err
		}
		// An empty page is how the listing really ends; numPages is a claim, and
		// the endpoint caps out at a thousand results anyway.
		if len(resp.Result) == 0 {
			break
		}
		bvids := make([]string, 0, len(resp.Result))
		for _, r := range resp.Result {
			bvids = append(bvids, r.Bvid)
		}
		infos, err := e.resolveAll(ctx, bvids)
		if err != nil {
			return nil, err
		}
		videos = append(videos, infos...)
		if limit > 0 && len(videos) >= limit {
			return videos[:limit], nil
		}
		if resp.NumPages > 0 && page >= resp.NumPages {
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
		if err := e.fetchListingPage(ctx, e.FavBaseURL, params, &resp); err != nil {
			return nil, err
		}
		bvids := make([]string, 0, len(resp.Medias))
		for _, m := range resp.Medias {
			bvids = append(bvids, m.Bvid)
		}
		infos, err := e.resolveAll(ctx, bvids)
		if err != nil {
			return nil, err
		}
		videos = append(videos, infos...)
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
