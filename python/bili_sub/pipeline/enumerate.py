"""Source enumeration: UP主空间, 收藏夹, 合集, 搜索, 单视频."""

from __future__ import annotations

import threading
import time

from ..bili import APIError, Client, WBISigner, wbi_key
from ..types import VideoInfo
from .pool import run_ordered

NAV_URL = "https://api.bilibili.com/x/web-interface/nav"
SPACE_URL = "https://api.bilibili.com/x/space/wbi/arc/search"
FAV_URL = "https://api.bilibili.com/x/v3/fav/resource/list"
VIEW_URL = "https://api.bilibili.com/x/web-interface/view"
SEASON_URL = "https://api.bilibili.com/x/polymer/web-space/seasons_archives_list"
SEARCH_URL = "https://api.bilibili.com/x/web-interface/wbi/search/type"

SPACE_PAGE_SIZE = 30
FAV_PAGE_SIZE = 20
SEARCH_PAGE_SIZE = 20
SEASON_PAGE_SIZE = 30

DEFAULT_SEARCH_LIMIT = 50

LISTING_RETRY_BACKOFFS = (2.0, 5.0, 12.0)


class Enumerator:
    def __init__(self, client: Client, workers: int = 3):
        self.client = client
        self.workers = max(1, workers or 1)
        self._signer: WBISigner | None = None
        self._lock = threading.Lock()

    # -- helpers --------------------------------------------------------

    def signer(self) -> WBISigner:
        with self._lock:
            if self._signer is not None:
                return self._signer
            nav = self.client.get(NAV_URL) or {}
            wbi = nav.get("wbi_img") or {}
            self._signer = WBISigner(
                wbi_key(wbi.get("img_url", "")), wbi_key(wbi.get("sub_url", ""))
            )
            return self._signer

    def _fetch_listing(self, url: str, params: dict[str, str]) -> dict:
        for attempt in range(len(LISTING_RETRY_BACKOFFS) + 1):
            try:
                return self.client.get(url, params) or {}
            except APIError as exc:
                if exc.code not in (-404, -412):
                    raise
                if attempt >= len(LISTING_RETRY_BACKOFFS):
                    raise RuntimeError(
                        f"B站返回 {exc.code}（{exc.message}）：可能被临时限流，请等几分钟再试"
                    ) from exc
            time.sleep(LISTING_RETRY_BACKOFFS[attempt])
        raise RuntimeError("unreachable")

    def resolve_video(self, video_id: str) -> VideoInfo:
        if video_id.lower().startswith("av"):
            params = {"aid": video_id[2:]}
        else:
            params = {"bvid": video_id}
        data = self.client.get(VIEW_URL, params) or {}
        return VideoInfo.from_api(data)

    def _resolve_all(self, bvids: list[str]) -> list[VideoInfo]:
        def one(bvid: str) -> VideoInfo | None:
            if not bvid:
                return None
            try:
                return self.resolve_video(bvid)
            except APIError as exc:
                if exc.code == -404:
                    return None
                raise

        infos: list[VideoInfo] = []
        for _index, value, error in run_ordered(bvids, self.workers, one):
            if error is not None:
                raise error
            if value is not None:
                infos.append(value)
        return infos

    # -- sources --------------------------------------------------------

    def enumerate_space(self, mid: str) -> list[VideoInfo]:
        signer = self.signer()
        videos: list[VideoInfo] = []
        page = 1
        while True:
            params = signer.sign(
                {
                    "mid": mid,
                    "ps": str(SPACE_PAGE_SIZE),
                    "pn": str(page),
                    "tid": "0",
                    "keyword": "",
                    "order": "pubdate",
                    "web_location": "1550101",
                    "order_avoided": "true",
                }
            )
            data = self._fetch_listing(SPACE_URL, params)
            vlist = (data.get("list") or {}).get("vlist") or []
            if not vlist:
                break
            bvids = [item.get("bvid", "") for item in vlist]
            infos = self._resolve_all(bvids)
            if not infos:
                break
            videos.extend(infos)
            total = int((data.get("page") or {}).get("count") or 0)
            if total > 0 and len(videos) >= total:
                break
            page += 1
        return videos

    def enumerate_fav(self, media_id: str) -> list[VideoInfo]:
        videos: list[VideoInfo] = []
        page = 1
        while True:
            data = self._fetch_listing(
                FAV_URL,
                {
                    "media_id": media_id,
                    "ps": str(FAV_PAGE_SIZE),
                    "pn": str(page),
                    "platform": "web",
                },
            )
            medias = data.get("medias") or []
            infos = self._resolve_all([m.get("bvid", "") for m in medias])
            if not infos:
                break
            videos.extend(infos)
            if not data.get("has_more"):
                break
            page += 1
        return videos

    def enumerate_season(self, mid: str, season_id: str) -> list[VideoInfo]:
        videos: list[VideoInfo] = []
        seen: set[str] = set()
        page = 1
        while True:
            data = self._fetch_listing(
                SEASON_URL,
                {
                    "mid": mid,
                    "season_id": season_id,
                    "sort_reverse": "false",
                    "page_num": str(page),
                    "page_size": str(SEASON_PAGE_SIZE),
                },
            )
            archives = data.get("archives") or []
            if not archives:
                break
            bvids: list[str] = []
            for archive in archives:
                bvid = archive.get("bvid", "")
                if bvid and bvid not in seen:
                    seen.add(bvid)
                    bvids.append(bvid)
            if not bvids:
                break
            infos = self._resolve_all(bvids)
            if not infos:
                break
            videos.extend(infos)
            total = int((data.get("page") or {}).get("total") or 0)
            if total > 0 and len(videos) >= total:
                break
            page += 1
        return videos

    def enumerate_search(self, keyword: str, limit: int = DEFAULT_SEARCH_LIMIT) -> list[VideoInfo]:
        signer = self.signer()
        videos: list[VideoInfo] = []
        seen: set[str] = set()
        page = 1
        while True:
            params = signer.sign(
                {
                    "search_type": "video",
                    "keyword": keyword,
                    "page": str(page),
                    "page_size": str(SEARCH_PAGE_SIZE),
                }
            )
            data = self._fetch_listing(SEARCH_URL, params)
            result = data.get("result") or []
            if not result:
                break
            bvids: list[str] = []
            for item in result:
                bvid = item.get("bvid", "")
                if bvid and bvid not in seen:
                    seen.add(bvid)
                    bvids.append(bvid)
            if limit > 0:
                remaining = limit - len(videos)
                if remaining <= 0:
                    break
                bvids = bvids[:remaining]
            if not bvids:
                # A page of nothing but repeats: the ranking is not stable, and
                # asking for more pages would just loop.
                break
            infos = self._resolve_all(bvids)
            videos.extend(infos)
            if limit > 0 and len(videos) >= limit:
                return videos
            num_pages = int(data.get("numPages") or 0)
            if num_pages > 0 and page >= num_pages:
                break
            page += 1
        return videos
