from __future__ import annotations

from bili_sub.pipeline.audio import pick_audio_track
from bili_sub.pipeline.subtitles import lang_family, lang_script, pick_lang
from bili_sub.types import SubtitleInfo


def _t(lan: str, url: str = "u") -> SubtitleInfo:
    return SubtitleInfo(lan=lan, subtitle_url=url)


def test_pick_lang_default_priority():
    tracks = [_t("en"), _t("ai-zh"), _t("zh-CN")]
    assert pick_lang(tracks).lan == "zh-CN"


def test_pick_lang_family_and_script():
    assert pick_lang([_t("zh-CN")], "zh-Hans").lan == "zh-CN"
    assert pick_lang([_t("en-US")], "en").lan == "en-US"
    assert pick_lang([_t("zh-Hant")], "zh-Hans") is None


def test_pick_lang_exact_first():
    assert pick_lang([_t("ai-zh"), _t("zh")], "zh").lan == "zh"


def test_lang_family_and_script():
    assert lang_family("ai-zh-Hans") == "zh"
    assert lang_script("zh-Hant") == "hant"
    assert lang_script("zh") == ""


def test_pick_audio_prefers_smallest_positive_bandwidth():
    tracks = [
        {"baseUrl": "a", "bandwidth": 192000},
        {"baseUrl": "b", "bandwidth": 64000},
        {"baseUrl": "", "backupUrl": ["c"], "bandwidth": 32000},
        {"baseUrl": "", "bandwidth": 0},
    ]
    assert pick_audio_track(tracks)["backupUrl"] == ["c"]


def test_pick_audio_ignores_empty_urls():
    assert pick_audio_track([{"baseUrl": "", "bandwidth": 1}]) is None
    assert pick_audio_track([]) is None
