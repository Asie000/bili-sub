"""Core data types shared by the CLI, the pipeline and the web API.

The names and JSON shapes deliberately mirror the Go implementation so an
existing ``data/`` directory and the embedded web page keep working unchanged.
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field
from typing import Any, Iterable


def round_ms(seconds: float) -> int:
    """Round like Go's math.Round (half away from zero), not Python's banker's round."""
    value = float(seconds) * 1000
    return int(math.floor(abs(value) + 0.5)) * (1 if value >= 0 else -1)


@dataclass
class Segment:
    start_ms: int
    end_ms: int
    text: str
    speaker: str = ""

    def to_json(self) -> dict[str, Any]:
        out = {"start_ms": self.start_ms, "end_ms": self.end_ms, "text": self.text}
        if self.speaker:
            out["speaker"] = self.speaker
        return out


@dataclass
class Document:
    bvid: str
    cid: int
    title: str
    author: str
    lang: str
    segments: list[Segment] = field(default_factory=list)

    def plain_text(self) -> str:
        return "\n".join(s.text.strip() for s in self.segments if s.text.strip())

    def to_json(self) -> dict[str, Any]:
        return {
            "bvid": self.bvid,
            "cid": self.cid,
            "title": self.title,
            "author": self.author,
            "lang": self.lang,
            "segments": [s.to_json() for s in self.segments],
        }

    @classmethod
    def from_json(cls, data: dict[str, Any]) -> "Document":
        return cls(
            bvid=data.get("bvid", ""),
            cid=int(data.get("cid") or 0),
            title=data.get("title", ""),
            author=data.get("author", ""),
            lang=data.get("lang", ""),
            segments=[
                Segment(
                    start_ms=int(s.get("start_ms") or 0),
                    end_ms=int(s.get("end_ms") or 0),
                    text=s.get("text", ""),
                    speaker=s.get("speaker", ""),
                )
                for s in data.get("segments") or []
            ],
        )


@dataclass
class Video:
    id: int
    bvid: str
    cid: int
    aid: int
    author: str
    title: str
    pubdate: int
    duration: int
    page_index: int
    part: str
    is_charging: bool
    state: str
    lang: str
    source_key: str

    def to_api(self) -> dict[str, Any]:
        """The exact field names the embedded app.js reads (Go's exported names)."""
        return {
            "ID": self.id,
            "Bvid": self.bvid,
            "Cid": self.cid,
            "Aid": self.aid,
            "Author": self.author,
            "Title": self.title,
            "Pubdate": self.pubdate,
            "Duration": self.duration,
            "PageIndex": self.page_index,
            "Part": self.part,
            "IsCharging": self.is_charging,
            "State": self.state,
            "Lang": self.lang,
            "SourceKey": self.source_key,
        }


@dataclass
class SubtitleInfo:
    lan: str = ""
    lan_doc: str = ""
    subtitle_url: str = ""

    @classmethod
    def from_api(cls, data: dict[str, Any]) -> "SubtitleInfo":
        return cls(
            lan=data.get("lan", ""),
            lan_doc=data.get("lan_doc", ""),
            subtitle_url=data.get("subtitle_url", ""),
        )


@dataclass
class PageInfo:
    cid: int = 0
    page: int = 1
    part: str = ""
    duration: int = 0


@dataclass
class UgcSeason:
    id: int = 0
    mid: int = 0
    title: str = ""
    ep_count: int = 0
    sections: list[list[str]] = field(default_factory=list)

    def video_count(self) -> int:
        if self.ep_count > 0:
            return self.ep_count
        return sum(len(eps) for eps in self.sections)


@dataclass
class VideoInfo:
    bvid: str = ""
    aid: int = 0
    cid: int = 0
    title: str = ""
    pubdate: int = 0
    duration: int = 0
    owner_name: str = ""
    pages: list[PageInfo] = field(default_factory=list)
    season: UgcSeason | None = None
    is_charging: bool = False

    @classmethod
    def from_api(cls, data: dict[str, Any]) -> "VideoInfo":
        season = None
        raw_season = data.get("ugc_season")
        if raw_season:
            sections = [
                [ep.get("bvid", "") for ep in section.get("episodes") or []]
                for section in raw_season.get("sections") or []
            ]
            season = UgcSeason(
                id=int(raw_season.get("id") or 0),
                mid=int(raw_season.get("mid") or 0),
                title=raw_season.get("title", ""),
                ep_count=int(raw_season.get("ep_count") or 0),
                sections=sections,
            )
        rights = data.get("rights") or {}
        charging = bool(
            data.get("is_upower_exclusive")
            or data.get("is_upower_play")
            or data.get("is_upower_preview")
            or rights.get("is_upower_exclusive")
        )
        return cls(
            bvid=data.get("bvid", ""),
            aid=int(data.get("aid") or 0),
            cid=int(data.get("cid") or 0),
            title=data.get("title", ""),
            pubdate=int(data.get("pubdate") or 0),
            duration=int(data.get("duration") or 0),
            owner_name=(data.get("owner") or {}).get("name", ""),
            pages=[
                PageInfo(
                    cid=int(p.get("cid") or 0),
                    page=int(p.get("page") or 1),
                    part=p.get("part", ""),
                    duration=int(p.get("duration") or 0),
                )
                for p in data.get("pages") or []
            ],
            season=season,
            is_charging=charging,
        )


def expand_pages(info: VideoInfo, source_key: str) -> list[Video]:
    """One row per page: a multi-P video is downloaded page by page."""
    pages = info.pages or [PageInfo(cid=info.cid, page=1)]
    return [
        Video(
            id=0,
            bvid=info.bvid,
            cid=p.cid,
            aid=info.aid,
            author=info.owner_name,
            title=info.title,
            pubdate=info.pubdate,
            duration=info.duration,
            page_index=p.page,
            part=p.part,
            is_charging=info.is_charging,
            state="pending",
            lang="",
            source_key=source_key,
        )
        for p in pages
    ]


def distinct(seq: Iterable[int]) -> list[int]:
    seen: set[int] = set()
    out: list[int] = []
    for x in seq:
        if x not in seen:
            seen.add(x)
            out.append(x)
    return out
