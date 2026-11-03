"""Canonical IR rendering and zip packaging (same formats and naming as Go)."""

from __future__ import annotations

import json
import os
import time
import zipfile
from typing import Iterable

from .filename import sanitize_subtitle_name
from .types import Document

FORMAT_SRT = "srt"
FORMAT_TEXT = "txt"
FORMAT_MARKDOWN = "md"
FORMAT_JSON = "json"

SUPPORTED_FORMATS = (FORMAT_SRT, FORMAT_TEXT, FORMAT_MARKDOWN, FORMAT_JSON)


def format_timestamp(ms: int) -> str:
    ms = max(0, int(ms))
    return f"{ms // 3600000:02d}:{(ms % 3600000) // 60000:02d}:{(ms % 60000) // 1000:02d},{ms % 1000:03d}"


def render_srt(doc: Document) -> str:
    out: list[str] = []
    n = 0
    for seg in doc.segments:
        text = seg.text.strip()
        if not text:
            continue
        n += 1
        out.append(
            f"{n}\n{format_timestamp(seg.start_ms)} --> {format_timestamp(seg.end_ms)}\n{text}\n\n"
        )
    return "".join(out)


def render_markdown(doc: Document) -> str:
    title = doc.title or doc.bvid
    parts = [f"# {title}\n\n"]
    if doc.author:
        parts.append(f"- 作者：{doc.author}\n")
    parts.append(f"- 视频：{doc.bvid}\n\n")
    for seg in doc.segments:
        text = seg.text.strip()
        if text:
            parts.append(text + "\n\n")
    return "".join(parts)


def render_json(doc: Document) -> str:
    payload = {
        "bvid": doc.bvid,
        "cid": doc.cid,
        "title": doc.title,
        "author": doc.author,
        "lang": doc.lang,
        "segments": [
            ({"text": s.text, "speaker": s.speaker} if s.speaker else {"text": s.text})
            for s in doc.segments
        ],
    }
    return json.dumps(payload, ensure_ascii=False, indent=2)


def render(doc: Document, fmt: str) -> str:
    if fmt == FORMAT_SRT:
        return render_srt(doc)
    if fmt == FORMAT_TEXT:
        return doc.plain_text()
    if fmt == FORMAT_MARKDOWN:
        return render_markdown(doc)
    if fmt == FORMAT_JSON:
        return render_json(doc)
    raise ValueError(f"unsupported format: {fmt}")


def parse_formats(raw: str) -> list[str]:
    out: list[str] = []
    for part in raw.split(","):
        part = part.strip()
        if not part:
            continue
        if part not in SUPPORTED_FORMATS:
            raise ValueError(f'unsupported format "{part}" (want one of srt,txt,md,json)')
        if part not in out:
            out.append(part)
    if not out:
        raise ValueError("no format given (want one of srt,txt,md,json)")
    return out


def pack(
    out_dir: str,
    docs: dict[str, Document],
    stems: dict[str, str],
    formats: Iterable[str],
    now: float | None = None,
) -> str:
    """Write one zip named YYYYMMDD-N.zip, every entry nested under its base."""
    formats = list(formats)
    stamp = time.strftime("%Y%m%d", time.localtime(now if now is not None else time.time()))
    base = f"{stamp}-{len(docs) * len(formats)}"
    os.makedirs(out_dir, exist_ok=True)
    zip_path = os.path.join(out_dir, base + ".zip")
    written: set[str] = set()
    try:
        with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as zf:
            for key in sorted(docs):
                doc = docs[key]
                stem = stems.get(key) or sanitize_subtitle_name(doc.author, doc.title, doc.bvid)
                for fmt in formats:
                    entry = f"{base}/{stem}.{fmt}"
                    if entry in written:
                        raise ValueError(
                            f'duplicate zip entry "{entry}" for stem "{stem}": '
                            "two documents share the same author, title and bvid"
                        )
                    zf.writestr(entry, render(doc, fmt))
                    written.add(entry)
    except BaseException:
        try:
            os.remove(zip_path)
        except OSError:
            pass
        raise
    return zip_path
