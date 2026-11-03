from __future__ import annotations

import pytest

from bili_sub.pipeline import urlparse as up


def test_single_video_forms():
    assert up.parse_url("https://www.bilibili.com/video/BV1uv411q7Mv") == (
        "single",
        "BV1uv411q7Mv",
    )
    assert up.parse_url("BV1uv411q7Mv") == ("single", "BV1uv411q7Mv")
    assert up.parse_url("av170001") == ("single", "av170001")
    assert up.parse_url("https://www.bilibili.com/video/av170001") == (
        "single",
        "av170001",
    )


def test_space_and_fav_keys():
    assert up.parse_url("https://space.bilibili.com/123456/video") == ("space", "123456")
    assert up.parse_url("https://space.bilibili.com/123456/favlist?fid=789") == (
        "fav",
        "789",
    )
    assert up.parse_url("media_id=789") == ("fav", "789")


def test_season_forms_beat_space():
    url = "https://space.bilibili.com/123/channel/collectiondetail?sid=456"
    assert up.parse_url(url) == ("season", "season:123:456")
    assert up.parse_url("https://space.bilibili.com/123/lists/456?type=season") == (
        "season",
        "season:123:456",
    )
    assert up.parse_url("123:456") == ("season", "season:123:456")
    assert up.split_season_key("season:123:456") == ("123", "456")
    assert up.split_season_key("season:123") is None


def test_series_is_refused_not_treated_as_space():
    with pytest.raises(ValueError, match="series"):
        up.parse_url("https://space.bilibili.com/123/channel/seriesdetail?sid=9")
    with pytest.raises(ValueError, match="series"):
        up.parse_url_for_kind(
            "https://space.bilibili.com/123/channel/seriesdetail?sid=9", "season"
        )


def test_search_keyword_is_unescaped_and_normalized():
    kind, key = up.parse_url(
        "https://search.bilibili.com/all?keyword=%E5%8A%B3%E5%8A%A8%E6%B3%95"
    )
    assert kind == "search"
    assert key == "search:劳动法"
    assert up.normalize_source_key("search:劳动   法") == "search:劳动 法"


def test_explicit_kind_disambiguates_bare_digits():
    assert up.parse_url_for_kind("123456", "space") == "123456"
    assert up.parse_url_for_kind("123456", "fav") == "123456"
    assert up.parse_url_for_kind("BV1uv411q7Mv", "single") == "BV1uv411q7Mv"


def test_short_link_detection():
    assert up.is_short_link("b23.tv/abc")
    assert up.is_short_link("https://b23.tv/abc")
    assert not up.is_short_link("https://www.bilibili.com/video/BV1uv411q7Mv")


def test_unrecognized_input_raises():
    with pytest.raises(ValueError):
        up.parse_url("https://example.com/nothing")
