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
)

func (k SourceKind) String() string {
	switch k {
	case KindSingle:
		return "single"
	case KindSpace:
		return "space"
	case KindFav:
		return "fav"
	default:
		return fmt.Sprintf("SourceKind(%d)", int(k))
	}
}

var (
	reBvid           = regexp.MustCompile(`BV[0-9A-Za-z]{10}`)
	reSpaceMid       = regexp.MustCompile(`space\.bilibili\.com/(\d+)`)
	reMedialistPlay  = regexp.MustCompile(`medialist/play/(\d+)`)
	reMediaID        = regexp.MustCompile(`media_id=(\d+)`)
	reMedialistFavID = regexp.MustCompile(`medialist/detail/ml(\d+)`)
)

// ParseURL accepts a bilibili video/UP主/收藏夹 URL (with or without scheme, or
// a bare bvid) and returns the source kind plus its key: bvid, mid or media_id.
func ParseURL(raw string) (SourceKind, string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, "", errors.New("empty url")
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
