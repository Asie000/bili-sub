package pipeline

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// SourceKind is the type of bilibili source a user URL/ID points at.
type SourceKind int

const (
	// KindSingle is a single video, identified by its bvid.
	KindSingle SourceKind = iota
	// KindSpace is an UP主's video list, identified by mid.
	KindSpace
	// KindFav is a 收藏夹, identified by media_id.
	KindFav
	// KindSeason is a 合集 (an UP主's curated season). The API takes the UP主's
	// mid and the season's sid together, so its key is "season:<mid>:<sid>".
	KindSeason
	// KindSearch is a keyword search. Its key is "search:<keyword>".
	KindSearch
)

func (k SourceKind) String() string {
	switch k {
	case KindSingle:
		return "single"
	case KindSpace:
		return "space"
	case KindFav:
		return "fav"
	case KindSeason:
		return "season"
	case KindSearch:
		return "search"
	default:
		return fmt.Sprintf("SourceKind(%d)", int(k))
	}
}

// seasonKeyPrefix marks a source key that needs two ids rather than the single
// one the other kinds use.
const seasonKeyPrefix = "season:"

// SeasonKey builds the source key for a 合集.
func SeasonKey(mid, seasonID string) string {
	return seasonKeyPrefix + mid + ":" + seasonID
}

// SplitSeasonKey reverses SeasonKey. ok is false for anything that is not a
// 合集 key, so callers cannot silently act on a half-parsed one.
func SplitSeasonKey(key string) (mid, seasonID string, ok bool) {
	rest, found := strings.CutPrefix(key, seasonKeyPrefix)
	if !found {
		return "", "", false
	}
	mid, seasonID, found = strings.Cut(rest, ":")
	if !found || mid == "" || seasonID == "" {
		return "", "", false
	}
	return mid, seasonID, true
}

// SeasonURL is the 合集 link to hand back to a user who has to paste one. It is
// the inverse of the first pattern in ParseURL, which the round-trip test pins.
func SeasonURL(mid, seasonID string) string {
	return "https://space.bilibili.com/" + mid + "/channel/collectiondetail?sid=" + seasonID
}

// searchKeyPrefix marks a source key that is a keyword rather than an id.
const searchKeyPrefix = "search:"

// NormalizeKeyword folds whitespace so that " 劳动法 " and "劳动法" are one
// keyword. The key groups rows and is what --source filters on, so two spellings
// must not become two sources.
func NormalizeKeyword(keyword string) string {
	return strings.Join(strings.Fields(keyword), " ")
}

// SearchKey builds the source key for a keyword search.
func SearchKey(keyword string) string {
	return searchKeyPrefix + NormalizeKeyword(keyword)
}

// SplitSearchKey reverses SearchKey. ok is false for anything that is not a
// search key, so a caller cannot silently act on a half-parsed one.
func SplitSearchKey(key string) (keyword string, ok bool) {
	keyword, found := strings.CutPrefix(key, searchKeyPrefix)
	if !found || keyword == "" {
		return "", false
	}
	return keyword, true
}

// NormalizeSourceKey applies the same normalization the writing side applies, so
// `--source "search:劳动  法"` finds the rows stored under `search:劳动 法`
// instead of silently matching none and printing "先运行 probe". Keys for the
// other kinds are ids, so they only lose surrounding whitespace.
func NormalizeSourceKey(key string) string {
	key = strings.TrimSpace(key)
	if keyword, ok := SplitSearchKey(key); ok {
		return SearchKey(keyword)
	}
	return key
}

var (
	reBvid = regexp.MustCompile(`BV[0-9A-Za-z]{10}`)
	// Search links carry the keyword, so they must be tested before the bvid
	// pattern: a link whose keyword happens to be a bvid is a search, not a video.
	reSearchURL = regexp.MustCompile(`search\.bilibili\.com/[^ ]*[?&]keyword=([^&#]+)`)
	// reSpaceSeriesPage matches any series page, with or without a usable sid:
	// knowing it is a 选集 is enough to say so instead of guessing.
	reSpaceSeriesPage = regexp.MustCompile(`space\.bilibili\.com/\d+/channel/seriesdetail`)
	// reSpaceChannel matches any UP主 channel page. A channel link that the
	// specific patterns below do not cover is a 合集/选集 we failed to parse, and
	// saying so beats letting it fall through to the space pattern.
	reSpaceChannel = regexp.MustCompile(`space\.bilibili\.com/\d+/channel/`)
	// reOldCollectionForm is the pre-合集 `medialist/play/<mid>?business=space_collection
	// &business_id=<sid>` link. It carries both ids but in a shape this parser does
	// not read, so it gets its own message rather than the silent space fallback.
	reOldCollectionForm = regexp.MustCompile(`business=space_collection`)
	reSpaceSeason       = regexp.MustCompile(`space\.bilibili\.com/(\d+)/channel/collectiondetail\?(?:[^#]*[?&])?sid=(\d+)`)
	reSpaceLists        = regexp.MustCompile(`space\.bilibili\.com/(\d+)/lists/(\d+)`)
	reSeasonPair        = regexp.MustCompile(`^(\d+)[:,](\d+)$`)
	reSpaceMid          = regexp.MustCompile(`space\.bilibili\.com/(\d+)`)
	reMedialistPlay     = regexp.MustCompile(`medialist/play/(\d+)`)
	reMediaID           = regexp.MustCompile(`media_id=(\d+)`)
	reMedialistFavID    = regexp.MustCompile(`medialist/detail/ml(\d+)`)
	reFavMl             = regexp.MustCompile(`ml(\d+)`)
	reFavFid            = regexp.MustCompile(`fid=(\d+)`)
	reDigitsOnly        = regexp.MustCompile(`^\d+$`)
)

// ParseURL accepts a bilibili video/UP主/收藏夹/合集 URL (with or without
// scheme, or a bare bvid) and returns the source kind plus its key.
func ParseURL(raw string) (SourceKind, string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, "", errors.New("empty url")
	}
	// 合集 and 视频选集 are checked before the space pattern on purpose: both
	// start with space.bilibili.com/<mid>/, so testing the space pattern first
	// hands back the UP主's entire backlog instead of the list the link points
	// at -- 54 videos instead of 5, with no error and no warning.
	if keyword, ok := parseSearchURL(s); ok {
		return KindSearch, SearchKey(keyword), nil
	}
	if strings.Contains(s, "search.bilibili.com") {
		return 0, "", fmt.Errorf("识别到搜索链接，但没能取出关键词：%q（请检查 keyword= 后面是否有内容）", raw)
	}
	if isSeasonLike(s) {
		key, err := parseSeasonKey(raw)
		if err != nil {
			return 0, "", err
		}
		return KindSeason, key, nil
	}
	if m := reMediaID.FindStringSubmatch(s); m != nil {
		return KindFav, m[1], nil
	}
	if m := reMedialistFavID.FindStringSubmatch(s); m != nil {
		return KindFav, m[1], nil
	}
	if m := reSpaceMid.FindStringSubmatch(s); m != nil {
		return KindSpace, m[1], nil
	}
	if m := reMedialistPlay.FindStringSubmatch(s); m != nil {
		return KindSpace, m[1], nil
	}
	if m := reBvid.FindString(s); m != "" {
		return KindSingle, m, nil
	}
	return 0, "", fmt.Errorf("unrecognized bilibili url or id: %q", raw)
}

// isSeasonLike reports whether the input is a 合集/选集 attempt at all. Anything
// it accepts but parseSeasonKey cannot read gets an error, never the space
// fallback: a wrong answer the user cannot notice is worse than a refusal.
func isSeasonLike(s string) bool {
	return reSpaceSeason.MatchString(s) || reSpaceLists.MatchString(s) ||
		reSpaceSeriesPage.MatchString(s) || reSpaceChannel.MatchString(s) ||
		reOldCollectionForm.MatchString(s) || reSeasonPair.MatchString(s)
}

// parseSeasonKey is the single implementation of "what 合集 is this link?", shared
// by ParseURL and ParseURLForKind. They used to have their own copies of the
// patterns, and the explicit-kind one was missing the 选集 guard -- so
// `--kind season <series link>` quietly produced a season key.
func parseSeasonKey(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if reSpaceSeriesPage.MatchString(s) || strings.Contains(s, "type=series") {
		return "", seriesUnsupported(raw)
	}
	if reOldCollectionForm.MatchString(s) {
		return "", fmt.Errorf("这是旧版「合集」链接（medialist/play?...business=space_collection），暂不识别：%q；请用 %s 形式",
			raw, SeasonURL("<mid>", "<sid>"))
	}
	if m := reSpaceSeason.FindStringSubmatch(s); m != nil {
		return SeasonKey(m[1], m[2]), nil
	}
	if m := reSpaceLists.FindStringSubmatch(s); m != nil {
		return SeasonKey(m[1], m[2]), nil
	}
	// 合集需要两个号码，所以也接受直接写出来："453013730:9241496"。
	if m := reSeasonPair.FindStringSubmatch(s); m != nil {
		return SeasonKey(m[1], m[2]), nil
	}
	if reSpaceChannel.MatchString(s) {
		return "", fmt.Errorf("识别到合集/选集页面，但没能从中取出 mid 和 sid：%q（请用带 sid= 的完整链接，例如 %s）",
			raw, SeasonURL("<mid>", "<sid>"))
	}
	return "", fmt.Errorf("无法从 %q 解析合集（需要 UP主 mid 和合集 sid，例如 %s）",
		raw, SeasonURL("<mid>", "<sid>"))
}

// seriesUnsupported names a 视频选集 instead of guessing at it. The two look
// alike (a UP主 path plus a sid) but the API that lists a 选集 is a different
// one, and this has no enumerator yet, so saying so is the honest answer.
func seriesUnsupported(raw string) error {
	return fmt.Errorf("这是视频选集（series），暂不支持：%q；合集（collection）已经支持", raw)
}

// parseSearchURL pulls the keyword out of a search.bilibili.com link. The keyword
// arrives percent-encoded (a Chinese keyword is unreadable in a link), so it is
// unescaped here; a malformed escape falls back to the raw text, because
// searching for what the user typed verbatim still does something useful.
func parseSearchURL(s string) (keyword string, ok bool) {
	m := reSearchURL.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	keyword, err := url.QueryUnescape(m[1])
	if err != nil {
		keyword = m[1]
	}
	if keyword = NormalizeKeyword(keyword); keyword == "" {
		return "", false
	}
	return keyword, true
}

// ParseURLForKind extracts the key for a source kind the user chose explicitly,
// bypassing ParseURL's auto-detection. It is what lets the web UI's type
// selector disambiguate a bare numeric id (space mid vs 收藏夹 id).
func ParseURLForKind(raw string, kind SourceKind) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("empty url")
	}
	switch kind {
	case KindSingle:
		if m := reBvid.FindString(s); m != "" {
			return m, nil
		}
		return "", fmt.Errorf("无法从 %q 解析视频号（BV号）", raw)
	case KindSpace:
		if m := reSpaceMid.FindStringSubmatch(s); m != nil {
			return m[1], nil
		}
		if m := reMedialistPlay.FindStringSubmatch(s); m != nil {
			return m[1], nil
		}
		if reDigitsOnly.MatchString(s) {
			return s, nil
		}
		return "", fmt.Errorf("无法从 %q 解析 UP主 mid", raw)
	case KindFav:
		if m := reMediaID.FindStringSubmatch(s); m != nil {
			return m[1], nil
		}
		if m := reMedialistFavID.FindStringSubmatch(s); m != nil {
			return m[1], nil
		}
		if m := reFavMl.FindStringSubmatch(s); m != nil {
			return m[1], nil
		}
		if m := reFavFid.FindStringSubmatch(s); m != nil {
			return m[1], nil
		}
		if reDigitsOnly.MatchString(s) {
			return s, nil
		}
		return "", fmt.Errorf("无法从 %q 解析收藏夹 id", raw)
	case KindSeason:
		return parseSeasonKey(raw)
	case KindSearch:
		// A keyword is not a URL, and it is also accepted as a search link so a
		// user can paste either one.
		if keyword, ok := parseSearchURL(s); ok {
			return SearchKey(keyword), nil
		}
		// A search link whose keyword could not be read is an error, not a
		// keyword: searching for the URL text itself is never what was meant.
		if strings.Contains(s, "search.bilibili.com") {
			return "", fmt.Errorf("识别到搜索链接，但没能取出关键词：%q（请检查 keyword= 后面是否有内容）", raw)
		}
		if keyword := NormalizeKeyword(s); keyword != "" {
			return SearchKey(keyword), nil
		}
		return "", errors.New("搜索关键词不能为空")
	default:
		return "", fmt.Errorf("未知来源类型 %s", kind)
	}
}

// ParseSourceKind maps the web UI's type selector value to a SourceKind. An
// empty or unknown string reports ok=false so the caller falls back to
// ParseURL's auto-detection.
func ParseSourceKind(s string) (SourceKind, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "single":
		return KindSingle, true
	case "space":
		return KindSpace, true
	case "fav":
		return KindFav, true
	case "season":
		return KindSeason, true
	case "search":
		return KindSearch, true
	default:
		return 0, false
	}
}
