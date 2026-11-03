"""Filename cleaning, copied byte-for-byte in behaviour from the Go project."""

from __future__ import annotations

import re
import unicodedata

_BAD = str.maketrans(
    {
        "\\": "_",
        "/": "_",
        ":": "_",
        "*": "_",
        "?": "_",
        '"': "_",
        "<": "_",
        ">": "_",
        "|": "_",
    }
)


def _sanitize_text(name: str, max_chars: int) -> str:
    s = name.replace("\r", "").replace("\n", " ").replace("\t", " ")
    s = s.translate(_BAD)
    s = "".join(ch for ch in s if unicodedata.category(ch) != "Cc")
    s = s.strip()
    return s[:max_chars]


def sanitize_filename(title: str, bvid: str) -> str:
    s = _sanitize_text(title, 100) or "untitled"
    return f"{s} [{bvid}]"


def sanitize_subtitle_name(author: str, title: str, bvid: str) -> str:
    combined = f"{author} - {title}" if author.strip() else title
    s = _sanitize_text(combined, 100) or "untitled"
    if bvid:
        s += f" [{bvid}]"
    return s


def page_stem(video) -> str:
    base = sanitize_subtitle_name(video.author, video.title, video.bvid)
    if video.page_index > 1:
        base += f"_P{video.page_index}"
    return base


_ILLEGAL_EXTRA = re.compile(r"[\x00-\x1f\x7f]")


def safe_component(name: str) -> str:
    """A last-resort guard for zip entries: no path separators, no control chars."""
    return _ILLEGAL_EXTRA.sub("", name).replace("\\", "_").replace("/", "_")
