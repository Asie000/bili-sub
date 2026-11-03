"""Per-video subtitle probing (player endpoint -> row state)."""

from __future__ import annotations

from dataclasses import dataclass

from ..bili import APIError, Client
from ..store import (
    STATE_CHARGING_LOCKED,
    STATE_NO_SUBTITLE,
    Store,
)
from ..types import SubtitleInfo, Video
from .enumerate import Enumerator
from .subtitles import CHARGING_CODES, resolve_track


@dataclass
class ProbeResult:
    video_id: int
    bvid: str
    has_subtitle: bool = False
    can_download: bool = False
    charging_locked: bool = False
    langs: list[SubtitleInfo] | None = None


class Prober:
    def __init__(self, client: Client, store: Store, enumerator: Enumerator):
        self.client = client
        self.store = store
        self.enumerator = enumerator

    def probe_video(self, video: Video) -> ProbeResult:
        try:
            track, usable = resolve_track(
                self.client, self.enumerator, video, ""
            )
        except APIError as exc:
            if exc.code in CHARGING_CODES:
                self.store.set_state(video.id, STATE_CHARGING_LOCKED, "")
                return ProbeResult(video.id, video.bvid, charging_locked=True)
            raise

        result = ProbeResult(video.id, video.bvid)
        if track is None:
            self.store.set_state(video.id, STATE_NO_SUBTITLE, "")
            return result
        self.store.set_state(video.id, STATE_DOWNLOADABLE, track.lan)
        result.has_subtitle = True
        result.can_download = True
        result.langs = usable
        return result
