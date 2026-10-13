package pipeline

import (
	"errors"
	"fmt"
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

var (
	reBvid           = regexp.MustCompile(`BV[0-9A-Za-z]{10}`)
	reSpaceSeries    = regexp.MustCompile(`space\.bilibili\.com/(\d+)/channel/seriesdetail\?(?:[^#]*[?&])?sid=(\d+)`)
	reSpaceSeason    = regexp.MustCompile(`space\.bilibili\.com/(\d+)/channel/collectiondetail\?(?:[^#]*[?&])?sid=(\d+)`)
	reSpaceLists     = regexp.MustCompile(`space\.bilibili\.com/(\d+)/lists/(\d+)`)
	reSeasonPair     = regexp.MustCompile(`^(\d+)[:,](\d+)$`)
	reSpaceMid       = regexp.MustCompile(`space\.bilibili\.com/(\d+)`)
	reMedialistPlay  = regexp.MustCompile(`medialist/play/(\d+)`)
	reMediaID        = regexp.MustCompile(`media_id=(\d+)`)
	reMedialistFavID = regexp.MustCompile(`medialist/detail/ml(\d+)`)
	reFavMl          = regexp.MustCompile(`ml(\d+)`)
	reFavFid         = regexp.MustCompile(`fid=(\d+)`)
	reDigitsOnly     = regexp.MustCompile(`^\d+$`)
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
	if m := reSpaceSeries.FindStringSubmatch(s); m != nil {
		return 0, "", seriesUnsupported(raw)
	}
	if m := reSpaceSeason.FindStringSubmatch(s); m != nil {
		return KindSeason, SeasonKey(m[1], m[2]), nil
	}
	if m := reSpaceLists.FindStringSubmatch(s); m != nil {
		if strings.Contains(s, "type=series") {
			return 0, "", seriesUnsupported(raw)
		}
		return KindSeason, SeasonKey(m[1], m[2]), nil
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

// seriesUnsupported names a 视频选集 instead of guessing at it. The two look
// alike (a UP主 path plus a sid) but the API that lists a 选集 is a different
// one, and this has no enumerator yet, so saying so is the honest answer.
func seriesUnsupported(raw string) error {
	return fmt.Errorf("这是视频选集（series），暂不支持：%q；合集（collection）已经支持", raw)
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
		return "", fmt.Errorf("无法从 %q 解析合集（需要 UP主 mid 和合集 sid，例如 %s）",
			raw, SeasonURL("<mid>", "<sid>"))
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
	default:
		return 0, false
	}
}
