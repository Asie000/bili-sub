"""Dash audio download for the ASR leg."""

from __future__ import annotations

from pathlib import Path

import httpx

from ..bili import APIError, Client, REFERER, USER_AGENT
from ..types import Video
from .enumerate import Enumerator

PLAYURL_WBI_URL = "https://api.bilibili.com/x/player/wbi/playurl"
AUDIO_TIMEOUT = 600.0


class AudioFetcher:
    def __init__(
        self,
        client: Client,
        enumerator: Enumerator | None,
        output_dir: str | Path,
        *,
        playurl_url: str = PLAYURL_WBI_URL,
        http: httpx.Client | None = None,
    ):
        self.client = client
        self.enumerator = enumerator
        self.output_dir = Path(output_dir)
        self.playurl_url = playurl_url
        self.http = http or httpx.Client(timeout=AUDIO_TIMEOUT, follow_redirects=True)

    def fetch(self, video: Video) -> Path:
        track = self._resolve(video)
        return self._download(video, track)

    def _resolve(self, video: Video) -> dict:
        params = {
            "bvid": video.bvid,
            "cid": str(video.cid),
            "fnval": "16",
            "fnver": "0",
            "fourk": "1",
        }
        if self.enumerator is not None:
            params = self.enumerator.signer().sign(params)
        try:
            data = self.client.get(self.playurl_url, params) or {}
        except APIError as exc:
            if exc.session_expired:
                raise RuntimeError(
                    f"音频接口要求登录态（{exc}）：请先运行 `bili-sub-py login` 再试"
                ) from exc
            raise
        tracks = (data.get("dash") or {}).get("audio") or []
        track = pick_audio_track(tracks)
        if track is None:
            raise RuntimeError(f"{video.bvid} 没有可用的音频流（playurl 未返回 dash.audio）")
        return track

    def _download(self, video: Video, track: dict) -> Path:
        self.output_dir.mkdir(parents=True, exist_ok=True)
        path = self.output_dir / f"{video.bvid}-{video.cid}.m4s"
        urls = [track.get("baseUrl", "")] + list(track.get("backupUrl") or [])
        last_error: BaseException | None = None
        for url in urls:
            if not url:
                continue
            try:
                self._fetch_to_file(url, path)
                return path
            except Exception as exc:  # noqa: BLE001 - try the next backup URL
                last_error = exc
        path.unlink(missing_ok=True)
        raise RuntimeError(f"下载 {video.bvid} 音频失败: {last_error or 'no audio URL'}")

    def _fetch_to_file(self, url: str, path: Path) -> None:
        # The CDN answers 403 without the referer. The login cookie is not sent:
        # the URL is pre-signed and SESSDATA must not leave the API host.
        with self.http.stream(
            "GET", url, headers={"User-Agent": USER_AGENT, "Referer": REFERER}
        ) as resp:
            if resp.status_code != 200:
                raise RuntimeError(f"HTTP {resp.status_code}")
            tmp = path.with_suffix(path.suffix + ".part")
            try:
                with tmp.open("wb") as fh:
                    for chunk in resp.iter_bytes():
                        fh.write(chunk)
                tmp.replace(path)
            except BaseException:
                tmp.unlink(missing_ok=True)
                raise


def pick_audio_track(tracks: list[dict]) -> dict | None:
    best: dict | None = None
    for track in tracks:
        if not track.get("baseUrl") and not track.get("backupUrl"):
            continue
        if best is None:
            best = track
            continue
        bandwidth = int(track.get("bandwidth") or 0)
        best_bandwidth = int(best.get("bandwidth") or 0)
        if bandwidth > 0 and (best_bandwidth == 0 or bandwidth < best_bandwidth):
            best = track
    return best
