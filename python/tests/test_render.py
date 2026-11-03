from __future__ import annotations

import json

from bili_sub.export import (
    FORMAT_JSON,
    FORMAT_MARKDOWN,
    FORMAT_SRT,
    FORMAT_TEXT,
    pack,
    parse_formats,
    render,
)
from bili_sub.filename import sanitize_filename, sanitize_subtitle_name
from bili_sub.types import Document, Segment


def _doc() -> Document:
    return Document(
        bvid="BV1uv411q7Mv",
        cid=123,
        title="标题",
        author="UP主",
        lang="zh",
        segments=[
            Segment(0, 1500, "第一句"),
            Segment(1500, 2500, "  "),
            Segment(2500, 3000, "第二句"),
        ],
    )


def test_srt():
    assert render(_doc(), FORMAT_SRT) == (
        "1\n00:00:00,000 --> 00:00:01,500\n第一句\n\n"
        "2\n00:00:02,500 --> 00:00:03,000\n第二句\n\n"
    )


def test_text_and_markdown():
    assert render(_doc(), FORMAT_TEXT) == "第一句\n第二句"
    md = render(_doc(), FORMAT_MARKDOWN)
    assert md.startswith("# 标题\n\n- 作者：UP主\n- 视频：BV1uv411q7Mv\n\n")
    assert "第一句\n\n" in md


def test_json_has_no_timeline():
    data = json.loads(render(_doc(), FORMAT_JSON))
    assert data["segments"] == [{"text": "第一句"}, {"text": "  "}, {"text": "第二句"}]
    assert "start_ms" not in data["segments"][0]


def test_parse_formats_dedupes_and_rejects():
    assert parse_formats("srt, txt,srt") == ["srt", "txt"]
    try:
        parse_formats("vtt")
    except ValueError as exc:
        assert "unsupported" in str(exc)
    else:
        raise AssertionError("want an error")


def test_filename_rules():
    assert sanitize_filename("a/b:c", "BV1") == "a_b_c [BV1]"
    assert sanitize_filename("", "BV1") == "untitled [BV1]"
    assert sanitize_subtitle_name("UP", "title", "BV1") == "UP - title [BV1]"
    assert sanitize_subtitle_name("", "title", "") == "title"
    long = sanitize_subtitle_name("a" * 120, "b", "BV1")
    assert long.endswith(" [BV1]")
    assert len(long) == 100 + len(" [BV1]")


def test_pack_nests_entries_and_names_zip(tmp_path):
    docs = {"BV1": _doc()}
    import time

    stamp = time.mktime((2024, 1, 2, 12, 0, 0, 0, 0, -1))
    zips = pack(str(tmp_path), docs, {"BV1": "UP - 标题 [BV1]"}, ["srt", "txt"], now=stamp)
    assert zips.endswith("20240102-2.zip")
    import zipfile

    with zipfile.ZipFile(zips) as zf:
        names = zf.namelist()
    assert names == [
        "20240102-2/UP - 标题 [BV1].srt",
        "20240102-2/UP - 标题 [BV1].txt",
    ]
