package bili

type VideoInfo struct {
	Bvid     string     `json:"bvid"`
	Aid      int64      `json:"aid"`
	Cid      int64      `json:"cid"`
	Title    string     `json:"title"`
	Pubdate  int64      `json:"pubdate"`
	Duration int        `json:"duration"`
	Owner    Owner      `json:"owner"`
	Pages    []PageInfo `json:"pages"`
	// UgcSeason is set only when the video belongs to a 合集, and it is how a
	// single video link reveals the whole collection it is part of.
	UgcSeason         *UgcSeason `json:"ugc_season"`
	IsUpowerExclusive bool       `json:"is_upower_exclusive"`
	IsUpowerPlay      bool       `json:"is_upower_play"`
	IsUpowerPreview   bool       `json:"is_upower_preview"`
	Rights            struct {
		IsUpowerExclusive bool `json:"is_upower_exclusive"`
	} `json:"rights"`
}

type Owner struct {
	Mid  int64  `json:"mid"`
	Name string `json:"name"`
}

// UgcSeason is the 合集 a video belongs to, as the view endpoint reports it.
// Only the fields needed to point a user at the collection are decoded: the
// episodes are here for the count fallback, not for enumerating (the season
// endpoint is what lists one).
type UgcSeason struct {
	ID       int64              `json:"id"`
	Mid      int64              `json:"mid"`
	Title    string             `json:"title"`
	EpCount  int                `json:"ep_count"`
	Sections []UgcSeasonSection `json:"sections"`
}

// UgcSeasonSection is one section of a 合集. Most collections have exactly one;
// the type exists because the count fallback has to walk them all.
type UgcSeasonSection struct {
	Episodes []UgcSeasonEpisode `json:"episodes"`
}

// UgcSeasonEpisode is one video inside a 合集.
type UgcSeasonEpisode struct {
	Bvid string `json:"bvid"`
}

// VideoCount is how many videos the 合集 holds. ep_count is what the API
// reports; the episodes are counted as a fallback because that field is not
// always present, and telling the user "共 0 条" would be worse than counting.
func (s *UgcSeason) VideoCount() int {
	if s == nil {
		return 0
	}
	if s.EpCount > 0 {
		return s.EpCount
	}
	n := 0
	for _, section := range s.Sections {
		n += len(section.Episodes)
	}
	return n
}

type PageInfo struct {
	Cid      int64  `json:"cid"`
	Page     int    `json:"page"`
	Part     string `json:"part"`
	Duration int    `json:"duration"`
}

type NavInfo struct {
	WbiImg struct {
		ImgURL string `json:"img_url"`
		SubURL string `json:"sub_url"`
	} `json:"wbi_img"`
}

type SubtitleInfo struct {
	Lan    string `json:"lan"`
	LanDoc string `json:"lan_doc"`
	SubURL string `json:"subtitle_url"`
}
