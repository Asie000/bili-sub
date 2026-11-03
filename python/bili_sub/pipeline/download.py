"""Subtitle body download -> canonical IR -> .srt + transcripts row."""

from __future__ import annotations

import json
from pathlib import Path

import httpx

from ..bili import APIError, Client, REFERER, USER_AGENT
from ..export import FORMAT_SRT, render
from ..filename import page_stem
from ..store import STATE_FETCHED, Store
from ..types import Document, Segment, Video, round_ms
from .enumerate import Enumerator
from .subtitles import CHARGING_CODES, resolve_track

TRANSCRIPT_ENGINE = "bilibili"

SUBTITLE_TIMEOUT = 30.0


class NoSubtitle(Exception):
    pass


class NoTrackForLang(Exception):
    pass


class ChargingLocked(Exception):
    pass


def normalize_subtitle_url(raw: str) -> str:
    if raw.startswith("//"):
        return "https:" + raw
    if raw.startswith(("http://", "https://")):
        return raw
    return "https://" + raw


class Downloader:
    def __init__(
        self,
        client: Client,
        store: Store,
        enumerator: Enumerator,
        output_dir: str | Path,
        *,
        http: httpx.Client | None = None,
    ):
        self.client = client
        self.store = store
        self.enumerator = enumerator
        self.output_dir = Path(output_dir)
        self.http = http or httpx.Client(timeout=SUBTITLE_TIMEOUT)

    def fetch_subtitle(self, video: Video, lang: str = "") -> Document:
        try:
            track, _usable = resolve_track(
                self.client, self.enumerator, video, lang
            )
        except APIError as exc:
            if exc.code in CHARGING_CODES:
                raise ChargingLocked(
                    f"{video.bvid}（{exc.message}）"
                ) from exc
            raise
        if track is None:
            if lang:
                raise NoTrackForLang(f"{video.bvid} 没有 lang={lang!r} 的字幕轨")
            raise NoSubtitle(video.bvid)

        segments = self.fetch_body(track.subtitle_url)
        doc = Document(
            bvid=video.bvid,
            cid=video.cid,
            title=video.title,
            author=video.author,
            lang=track.lan,
            segments=segments,
        )

        self.output_dir.mkdir(parents=True, exist_ok=True)
        srt_path = self.output_dir / (page_stem(video) + ".srt")
        srt_path.write_text(render(doc, FORMAT_SRT), encoding="utf-8", newline="\n")

        ir_json = json.dumps(doc.to_json(), ensure_ascii=False)
        self.store.save_transcript(video.id, TRANSCRIPT_ENGINE, track.lan, ir_json)
        self.store.set_state(video.id, STATE_FETCHED, track.lan)
        return doc

    def fetch_body(self, raw_url: str) -> list[Segment]:
        url = normalize_subtitle_url(raw_url)
        resp = self.http.get(
            url, headers={"User-Agent": USER_AGENT, "Referer": REFERER}
        )
        try:
            if resp.status_code != 200:
                raise RuntimeError(f"subtitle {url}: HTTP {resp.status_code}")
            body = resp.json()
        finally:
            resp.close()
        return [
            Segment(
                start_ms=round_ms(float(item.get("from") or 0)),
                end_ms=round_ms(float(item.get("to") or 0)),
                text=item.get("content", ""),
            )
            for item in body.get("body") or []
        ]
