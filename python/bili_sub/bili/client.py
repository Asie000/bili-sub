"""B站 API client: headers, device fingerprint, risk-control backoff."""

from __future__ import annotations

import threading
import time
from typing import Any

import httpx

from .limiter import RateLimiter

USER_AGENT = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)
REFERER = "https://www.bilibili.com"

PLAYER_V2_URL = "https://api.bilibili.com/x/player/v2"
AI_SUBTITLE_STAT_URL = "https://api.bilibili.com/x/player/v2/ai/subtitle/search/stat"
DEFAULT_FINGERPRINT_URL = "https://api.bilibili.com/x/frontend/finger/spi"
DEFAULT_NAV_URL = "https://api.bilibili.com/x/web-interface/nav"

RETRY_BACKOFFS = (2.0, 5.0, 12.0)


class APIError(Exception):
    def __init__(self, code: int, message: str):
        super().__init__(f"bilibili code={code} message={message}")
        self.code = code
        self.message = message

    @property
    def session_expired(self) -> bool:
        return self.code == -101

    @property
    def unauthorized(self) -> bool:
        return self.code in (-101, -400)


class RiskControlError(Exception):
    """HTTP 412/429 that survived every retry: a whole-run problem."""


class Client:
    def __init__(
        self,
        sessdata: str = "",
        limiter: RateLimiter | None = None,
        cookie: str = "",
        *,
        timeout: float = 20.0,
        transport: httpx.BaseTransport | None = None,
    ):
        self.sessdata = sessdata
        self.cookie = cookie
        self.limiter = limiter
        self.fingerprint_url = DEFAULT_FINGERPRINT_URL
        self._http = httpx.Client(
            timeout=timeout,
            follow_redirects=True,
            transport=transport,
        )
        self._lock = threading.Lock()
        self._buvid_lock = threading.Lock()
        self._buvid_done = False
        self._buvid3 = ""
        self._buvid4 = ""

    def close(self) -> None:
        self._http.close()

    # -- cookies --------------------------------------------------------

    def _cookie_header(self) -> str:
        if self.cookie:
            return self.cookie
        parts = []
        if self.sessdata:
            parts.append(f"SESSDATA={self.sessdata}")
        if self._buvid3:
            parts.append(f"buvid3={self._buvid3}")
        if self._buvid4:
            parts.append(f"buvid4={self._buvid4}")
        return "; ".join(parts)

    def _headers(self) -> dict[str, str]:
        headers = {"User-Agent": USER_AGENT, "Referer": REFERER}
        cookie = self._cookie_header()
        if cookie:
            headers["Cookie"] = cookie
        return headers

    def _ensure_buvid(self) -> None:
        if self.cookie:
            return
        # Hold the lock for the fetch itself: this mirrors sync.Once, where the
        # other callers wait instead of racing out a fingerprint-less request.
        with self._buvid_lock:
            if self._buvid_done:
                return
            self._buvid_done = True
            self._fetch_buvid()

    def _fetch_buvid(self) -> None:
        try:
            resp = self._http.get(
                self.fingerprint_url,
                headers={"User-Agent": USER_AGENT, "Referer": REFERER},
            )
            if resp.status_code != 200:
                return
            env = resp.json()
            if env.get("code") != 0:
                return
            data = env.get("data") or {}
            with self._lock:
                self._buvid3 = data.get("b_3", "") or ""
                self._buvid4 = data.get("b_4", "") or ""
        except (httpx.HTTPError, ValueError):
            return

    # -- requests -------------------------------------------------------

    def _do(self, url: str, params: dict[str, str] | None = None) -> httpx.Response:
        if self.limiter is not None:
            self.limiter.wait()
        return self._http.get(url, params=params, headers=self._headers())

    def get(self, url: str, params: dict[str, str] | None = None) -> Any:
        self._ensure_buvid()
        resp = self._do(url, params)
        if resp.status_code in (412, 429):
            for wait in RETRY_BACKOFFS:
                resp.close()
                time.sleep(wait)
                resp = self._do(url, params)
                if resp.status_code not in (412, 429):
                    break
        if resp.status_code in (412, 429):
            code = resp.status_code
            resp.close()
            raise RiskControlError(
                f"bili: 请求被风控限流（HTTP {code}）：请求过于频繁被临时拦截，"
                "请等几分钟再试；若持续出现，可在页面登录框粘贴浏览器完整 Cookie"
            )
        try:
            if resp.status_code != 200:
                raise httpx.HTTPStatusError(
                    f"HTTP {resp.status_code}", request=resp.request, response=resp
                )
            env = resp.json()
        finally:
            resp.close()
        code = int(env.get("code") or 0)
        if code != 0:
            raise APIError(code, env.get("message", ""))
        return env.get("data")

    def resolve_url(self, url: str) -> str:
        if self.limiter is not None:
            self.limiter.wait()
        resp = self._http.get(url, headers=self._headers())
        resp.close()
        return str(resp.url)

    def nav_status(self) -> tuple[bool, str]:
        data = self.get(DEFAULT_NAV_URL) or {}
        return bool(data.get("isLogin")), data.get("uname", "") or ""

    def check_login(self) -> bool:
        return self.nav_status()[0]
