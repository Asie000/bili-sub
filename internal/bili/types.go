package bili

type VideoInfo struct {
	Bvid              string     `json:"bvid"`
	Aid               int64      `json:"aid"`
	Cid               int64      `json:"cid"`
	Title             string     `json:"title"`
	Pubdate           int64      `json:"pubdate"`
	Duration          int        `json:"duration"`
	Owner             Owner      `json:"owner"`
	Pages             []PageInfo `json:"pages"`
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
