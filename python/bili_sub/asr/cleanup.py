"""Post-processing of raw Whisper output (same rules as the Go version)."""

from __future__ import annotations

import re
import unicodedata

from ..types import Segment
from .t2s import to_simplified

_WHISPER_TOKEN = re.compile(r"<\|[^|]*\|>")


def is_chinese_language(lang: str) -> bool:
    value = lang.strip().lower()
    return value == "" or value == "chinese" or value.startswith("zh")


def is_garbled(text: str) -> bool:
    count = text.count("\ufffd")
    if count == 0:
        return False
    if count >= 3 or "\ufffd\ufffd" in text:
        return True
    total = len("".join(text.split()))
    return total >= 10 and count / total >= 0.2


def _drop_spaces_between_cjk(text: str) -> str:
    out: list[str] = []
    for i, ch in enumerate(text):
        if (
            ch == " "
            and out
            and i + 1 < len(text)
            and _is_han(out[-1])
            and _is_han(text[i + 1])
        ):
            continue
        out.append(ch)
    return "".join(out)


def _is_han(ch: str) -> bool:
    return "\u4e00" <= ch <= "\u9fff" or "\u3400" <= ch <= "\u4dbf"


def clean_text(text: str) -> str:
    text = _WHISPER_TOKEN.sub("", text)
    text = text.replace("\ufffd", "")
    text = " ".join(text.split())
    return _drop_spaces_between_cjk(text.strip())


def dedup_key(text: str) -> str:
    return "".join(
        ch.lower()
        for ch in text
        if not ch.isspace()
        and not unicodedata.category(ch).startswith("P")
        and not unicodedata.category(ch).startswith("S")
    )


def clean_segments(
    segments: list[Segment], language: str = "", simplify: bool = True
) -> list[Segment]:
    if not segments:
        return []
    do_simplify = simplify and is_chinese_language(language)
    out: list[Segment] = []
    previous = ""
    for seg in segments:
        if is_garbled(seg.text):
            continue
        text = clean_text(seg.text)
        if do_simplify:
            text = to_simplified(text)
        if not text:
            continue
        key = dedup_key(text)
        if len(key) >= 2 and key == previous:
            continue
        previous = key
        out.append(
            Segment(
                start_ms=seg.start_ms,
                end_ms=seg.end_ms,
                text=text,
                speaker=seg.speaker,
            )
        )
    return out
