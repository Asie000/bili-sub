"""Input parsing: which B站 source a URL/ID points at, and its source key."""

from __future__ import annotations

import re
from urllib.parse import unquote_plus

KIND_SINGLE = "single"
KIND_SPACE = "space"
KIND_FAV = "fav"
KIND_SEASON = "season"
KIND_SEARCH = "search"

_SEASON_PREFIX = "season:"
_SEARCH_PREFIX = "search:"

_RE_BVID = re.compile(r"BV[0-9A-Za-z]{10}")
_RE_AVID_URL = re.compile(r"(?i)bilibili\.com/video/av(\d+)")
_RE_AVID_BARE = re.compile(r"(?i)^av(\d+)$")
_RE_SEARCH_URL = re.compile(r"search\.bilibili\.com/[^ ]*[?&]keyword=([^&#]+)")
_RE_SPACE_SERIES = re.compile(r"space\.bilibili\.com/\d+/channel/seriesdetail")
_RE_SPACE_CHANNEL = re.compile(r"space\.bilibili\.com/\d+/channel/")
_RE_OLD_COLLECTION = re.compile(r"business=space_collection")
_RE_SPACE_SEASON = re.compile(
    r"space\.bilibili\.com/(\d+)/channel/collectiondetail\?(?:[^#]*[?&])?sid=(\d+)"
)
_RE_SPACE_LISTS = re.compile(r"space\.bilibili\.com/(\d+)/lists/(\d+)")
_RE_SEASON_PAIR = re.compile(r"^(\d+)[:,](\d+)$")
_RE_SPACE_MID = re.compile(r"space\.bilibili\.com/(\d+)")
_RE_MEDIALIST_PLAY = re.compile(r"medialist/play/(\d+)")
_RE_MEDIA_ID = re.compile(r"media_id=(\d+)")
_RE_MEDIALIST_FAV = re.compile(r"medialist/detail/ml(\d+)")
_RE_FAV_ML = re.compile(r"ml(\d+)")
_RE_FAV_FID = re.compile(r"fid=(\d+)")
_RE_DIGITS = re.compile(r"^\d+$")
_RE_SHORT_LINK = re.compile(r"(?i)^(?:https?://)?b23\.tv/")


def season_key(mid: str, season_id: str) -> str:
    return f"{_SEASON_PREFIX}{mid}:{season_id}"


def split_season_key(key: str) -> tuple[str, str] | None:
    if not key.startswith(_SEASON_PREFIX):
        return None
    rest = key[len(_SEASON_PREFIX):]
    if ":" not in rest:
        return None
    mid, season_id = rest.split(":", 1)
    if not mid or not season_id:
        return None
    return mid, season_id


def season_url(mid: str, season_id: str) -> str:
    return f"https://space.bilibili.com/{mid}/channel/collectiondetail?sid={season_id}"


def normalize_keyword(keyword: str) -> str:
    return " ".join(keyword.split())


def search_key(keyword: str) -> str:
    return _SEARCH_PREFIX + normalize_keyword(keyword)


def split_search_key(key: str) -> str | None:
    if not key.startswith(_SEARCH_PREFIX):
        return None
    keyword = key[len(_SEARCH_PREFIX):]
    return keyword or None


def normalize_source_key(key: str) -> str:
    key = key.strip()
    keyword = split_search_key(key)
    return search_key(keyword) if keyword is not None else key


def is_short_link(raw: str) -> bool:
    return bool(_RE_SHORT_LINK.match(raw.strip()))


def parse_source_kind(value: str) -> str | None:
    value = value.strip().lower()
    if value in (KIND_SINGLE, KIND_SPACE, KIND_FAV, KIND_SEASON, KIND_SEARCH):
        return value
    return None


def _parse_search_url(s: str) -> str | None:
    m = _RE_SEARCH_URL.search(s)
    if not m:
        return None
    try:
        keyword = unquote_plus(m.group(1))
    except ValueError:
        keyword = m.group(1)
    keyword = normalize_keyword(keyword)
    return keyword or None


def _series_unsupported(raw: str) -> ValueError:
    return ValueError(f"这是视频选集（series），暂不支持：{raw!r}；合集（collection）已经支持")


def _parse_season_key(raw: str) -> str:
    s = raw.strip()
    if _RE_SPACE_SERIES.search(s) or "type=series" in s:
        raise _series_unsupported(raw)
    if _RE_OLD_COLLECTION.search(s):
        raise ValueError(
            f"这是旧版「合集」链接（medialist/play?...business=space_collection），"
            f"暂不识别：{raw!r}；请用 {season_url('<mid>', '<sid>')} 形式"
        )
    m = _RE_SPACE_SEASON.search(s)
    if m:
        return season_key(m.group(1), m.group(2))
    m = _RE_SPACE_LISTS.search(s)
    if m:
        return season_key(m.group(1), m.group(2))
    m = _RE_SEASON_PAIR.match(s)
    if m:
        return season_key(m.group(1), m.group(2))
    if _RE_SPACE_CHANNEL.search(s):
        raise ValueError(
            f"识别到合集/选集页面，但没能从中取出 mid 和 sid：{raw!r}"
            f"（请用带 sid= 的完整链接，例如 {season_url('<mid>', '<sid>')}）"
        )
    raise ValueError(
        f"无法从 {raw!r} 解析合集（需要 UP主 mid 和合集 sid，"
        f"例如 {season_url('<mid>', '<sid>')}）"
    )


def _is_season_like(s: str) -> bool:
    return any(
        p.search(s)
        for p in (
            _RE_SPACE_SEASON,
            _RE_SPACE_LISTS,
            _RE_SPACE_SERIES,
            _RE_SPACE_CHANNEL,
            _RE_OLD_COLLECTION,
            _RE_SEASON_PAIR,
        )
    )


def parse_url(raw: str) -> tuple[str, str]:
    s = raw.strip()
    if not s:
        raise ValueError("empty url")
    keyword = _parse_search_url(s)
    if keyword is not None:
        return KIND_SEARCH, search_key(keyword)
    if "search.bilibili.com" in s:
        raise ValueError(
            f"识别到搜索链接，但没能取出关键词：{raw!r}（请检查 keyword= 后面是否有内容）"
        )
    if _is_season_like(s):
        return KIND_SEASON, _parse_season_key(raw)

    for pattern in (_RE_MEDIA_ID, _RE_MEDIALIST_FAV, _RE_FAV_FID):
        m = pattern.search(s)
        if m:
            return KIND_FAV, m.group(1)
    m = _RE_SPACE_MID.search(s)
    if m:
        return KIND_SPACE, m.group(1)
    m = _RE_MEDIALIST_PLAY.search(s)
    if m:
        return KIND_SPACE, m.group(1)
    m = _RE_BVID.search(s)
    if m:
        return KIND_SINGLE, m.group(0)
    m = _RE_AVID_URL.search(s)
    if m:
        return KIND_SINGLE, "av" + m.group(1)
    m = _RE_AVID_BARE.match(s)
    if m:
        return KIND_SINGLE, "av" + m.group(1)
    raise ValueError(f"unrecognized bilibili url or id: {raw!r}")


def parse_url_for_kind(raw: str, kind: str) -> str:
    s = raw.strip()
    if not s:
        raise ValueError("empty url")
    if kind == KIND_SINGLE:
        m = _RE_BVID.search(s)
        if m:
            return m.group(0)
        m = _RE_AVID_URL.search(s) or _RE_AVID_BARE.match(s)
        if m:
            return "av" + m.group(1)
        raise ValueError(f"无法从 {raw!r} 解析视频号（BV号或av号）")
    if kind == KIND_SPACE:
        m = _RE_SPACE_MID.search(s) or _RE_MEDIALIST_PLAY.search(s)
        if m:
            return m.group(1)
        if _RE_DIGITS.match(s):
            return s
        raise ValueError(f"无法从 {raw!r} 解析 UP主 mid")
    if kind == KIND_FAV:
        for pattern in (_RE_MEDIA_ID, _RE_MEDIALIST_FAV, _RE_FAV_ML, _RE_FAV_FID):
            m = pattern.search(s)
            if m:
                return m.group(1)
        if _RE_DIGITS.match(s):
            return s
        raise ValueError(f"无法从 {raw!r} 解析收藏夹 id")
    if kind == KIND_SEASON:
        return _parse_season_key(raw)
    if kind == KIND_SEARCH:
        keyword = _parse_search_url(s)
        if keyword is not None:
            return search_key(keyword)
        if "search.bilibili.com" in s:
            raise ValueError(
                f"识别到搜索链接，但没能取出关键词：{raw!r}（请检查 keyword= 后面是否有内容）"
            )
        keyword = normalize_keyword(s)
        if keyword:
            return search_key(keyword)
        raise ValueError("搜索关键词不能为空")
    raise ValueError(f"未知来源类型 {kind}")
