package bili

import (
	"encoding/json"
	"testing"
)

// TestUgcSeasonVideoCount pins the fallback: ep_count is what the API reports and
// what the hint prints, but it is not always present, and "共 0 条" would be a
// worse answer than counting the episodes the response did carry.
func TestUgcSeasonVideoCount(t *testing.T) {
	if got := (&UgcSeason{EpCount: 31}).VideoCount(); got != 31 {
		t.Errorf("ep_count present: got %d want 31", got)
	}

	two := &UgcSeason{Sections: []UgcSeasonSection{
		{Episodes: []UgcSeasonEpisode{{Bvid: "BV1uv411q7Mv"}, {Bvid: "BV1uv411q7Mx"}}},
		{Episodes: []UgcSeasonEpisode{{Bvid: "BV1uv411q7My"}}},
	}}
	if got := two.VideoCount(); got != 3 {
		t.Errorf("no ep_count: got %d want 3 (summed across sections)", got)
	}

	// ep_count wins when both are present: it is the server's own count and does
	// not depend on the sections this client happens to decode.
	both := &UgcSeason{EpCount: 31, Sections: two.Sections}
	if got := both.VideoCount(); got != 31 {
		t.Errorf("ep_count preferred over counting: got %d want 31", got)
	}

	var nilSeason *UgcSeason
	if got := nilSeason.VideoCount(); got != 0 {
		t.Errorf("nil season: got %d want 0", got)
	}
}

// TestVideoInfoDecodesUgcSeason pins the field names against a real response
// shape: the hint is built from these, so a rename in the API would otherwise
// silently turn into "《》共 0 条".
func TestVideoInfoDecodesUgcSeason(t *testing.T) {
	const body = `{
		"bvid":"BV1u84y1i7u6",
		"ugc_season":{
			"id":814546,
			"mid":406571547,
			"title":"100个办公神技",
			"ep_count":31,
			"sections":[{"episodes":[{"bvid":"BV1u84y1i7u6"},{"bvid":"BV1u84y1i7u7"}]}]
		}
	}`
	var vi VideoInfo
	if err := json.Unmarshal([]byte(body), &vi); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if vi.UgcSeason == nil {
		t.Fatal("UgcSeason not decoded")
	}
	if vi.UgcSeason.ID != 814546 || vi.UgcSeason.Mid != 406571547 {
		t.Errorf("ids = %d/%d", vi.UgcSeason.ID, vi.UgcSeason.Mid)
	}
	if vi.UgcSeason.Title != "100个办公神技" {
		t.Errorf("title = %q", vi.UgcSeason.Title)
	}
	if got := vi.UgcSeason.VideoCount(); got != 31 {
		t.Errorf("VideoCount = %d want 31", got)
	}

	// A video outside any 合集 has no such field, and that must stay nil rather
	// than becoming an empty season the hint would then print.
	var plain VideoInfo
	if err := json.Unmarshal([]byte(`{"bvid":"BV1LJBcYYEZE"}`), &plain); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if plain.UgcSeason != nil {
		t.Errorf("UgcSeason = %+v for a video in no 合集, want nil", plain.UgcSeason)
	}
}
