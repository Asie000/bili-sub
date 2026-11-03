"""Shared subtitle-track resolution used by both probing and downloading."""

from __future__ import annotations

from ..bili import AI_SUBTITLE_STAT_URL, PLAYER_V2_URL, Client
from ..types import SubtitleInfo, Video
from .enumerate import Enumerator

PLAYER_WBI_URL = "https://api.bilibili.com/x/player/wbi/v2"

CHARGING_CODES = {-10403, -404, -62002}

LANG_PRIORITY = ("zh-Hans", "zh-CN", "zh", "ai-zh", "en", "en-US")


def lang_family(lan: str) -> str:
    value = lan.strip().lower()
    if value.startswith("ai-"):
        value = value[3:]
    return value.split("-", 1)[0]


def lang_script(lan: str) -> str:
    value = lan.strip().lower()
    if value.startswith("ai-"):
        value = value[3:]
    parts = value.split("-")
    if len(parts) >= 2 and parts[1] in ("hans", "hant"):
        return parts[1]
    return ""


def pick_lang(available: list[SubtitleInfo], pref: str = "") -> SubtitleInfo | None:
    if pref:
        want = pref.lower()
        for track in available:
            if track.lan.lower() == want:
                return track
        family, script = lang_family(want), lang_script(want)
        for track in available:
            if lang_family(track.lan) != family:
                continue
            if script:
                other = lang_script(track.lan)
                if other and other != script:
                    continue
            return track
        return None
    for preferred in LANG_PRIORITY:
        for track in available:
            if track.lan.lower() == preferred.lower():
                return track
    return available[0] if available else None


def _without_lang(available: list[SubtitleInfo], lan: str) -> list[SubtitleInfo]:
    return [t for t in available if t.lan.lower() != lan.lower()]


def fetch_ai_subtitle_url(
    client: Client, video: Video, url: str = AI_SUBTITLE_STAT_URL
) -> str:
    data = client.get(url, {"aid": str(video.aid), "cid": str(video.cid)}) or {}
    return data.get("subtitle_url", "") or ""


def resolve_track(
    client: Client,
    enumerator: Enumerator | None,
    video: Video,
    lang: str = "",
    *,
    player_url: str = PLAYER_WBI_URL,
    player_v2_url: str = PLAYER_V2_URL,
    ai_url: str = AI_SUBTITLE_STAT_URL,
) -> tuple[SubtitleInfo | None, list[SubtitleInfo]]:
    """Return the best track and the usable ones, or (None, []) when none exist.

    An ai-* track carries no URL in the player response; its stat lookup is tried
    and a failure just makes that candidate unusable, never aborts the lookup.
    """
    params = {"cid": str(video.cid)}
    if video.aid > 0:
        endpoint = player_url
        params["aid"] = str(video.aid)
        if enumerator is not None:
            params = enumerator.signer().sign(params)
    else:
        endpoint = player_v2_url
        params["bvid"] = video.bvid

    data = client.get(endpoint, params) or {}
    tracks = [
        SubtitleInfo.from_api(item)
        for item in ((data.get("subtitle") or {}).get("subtitles") or [])
    ]

    candidates = list(tracks)
    chosen: SubtitleInfo | None = None
    while True:
        picked = pick_lang(candidates, lang)
        if picked is None:
            return None, []
        if (
            not picked.subtitle_url
            and picked.lan.startswith("ai-")
            and video.aid > 0
        ):
            try:
                picked.subtitle_url = fetch_ai_subtitle_url(client, video, ai_url)
            except Exception:  # noqa: BLE001 - makes this candidate unusable only
                picked.subtitle_url = ""
        if picked.subtitle_url:
            chosen = picked
            break
        candidates = _without_lang(candidates, picked.lan)

    usable: list[SubtitleInfo] = []
    for track in tracks:
        if track.lan == chosen.lan:
            track = chosen
        if track.subtitle_url:
            usable.append(track)
    return chosen, usable
