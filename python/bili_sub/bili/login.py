"""Session persistence and the passport QR-login flow."""

from __future__ import annotations

import json
import os
import time
from pathlib import Path
from typing import Callable
from urllib.parse import parse_qs, unquote, urlparse

import httpx

from .client import REFERER, USER_AGENT, APIError, Client

QR_GENERATE_URL = "https://passport.bilibili.com/x/passport-login/web/qrcode/generate"
QR_POLL_URL = "https://passport.bilibili.com/x/passport-login/web/qrcode/poll"

QR_NOT_SCAN = 86101
QR_SCANNED = 86090
QR_EXPIRED = 86038


def save_session(path: str | Path, sessdata: str, cookie: str = "") -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    payload = {"sessdata": sessdata}
    if cookie:
        payload["cookie"] = cookie
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8", newline="\n")
    try:
        os.chmod(tmp, 0o600)
    except OSError:
        pass
    os.replace(tmp, path)
    try:
        os.chmod(path, 0o600)
    except OSError:
        pass


def load_session(path: str | Path) -> tuple[str, str]:
    path = Path(path)
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return "", ""
    except (OSError, ValueError) as exc:
        raise ValueError(f"parse {path}: {exc}") from exc
    return data.get("sessdata", "") or "", data.get("cookie", "") or ""


def extract_sessdata(cookie: str) -> str:
    for part in cookie.split(";"):
        name, sep, value = part.strip().partition("=")
        if sep and name == "SESSDATA":
            return value
    return ""


def looks_like_cookie(value: str) -> bool:
    if "SESSDATA=" in value:
        return True
    pairs = 0
    for part in value.split(";"):
        name, sep, _ = part.strip().partition("=")
        if sep and name:
            pairs += 1
    return pairs > 1


def _sessdata_from_url(raw: str) -> str:
    if not raw:
        return ""
    try:
        values = parse_qs(urlparse(raw).query).get("SESSDATA")
        if values:
            return unquote(values[0])
    except ValueError:
        pass
    for sep in ("&", "?", ";"):
        raw = raw.replace(sep, "\0")
    for part in raw.split("\0"):
        name, eq, value = part.partition("=")
        if eq and name == "SESSDATA" and value:
            return unquote(value)
    return ""


class QRLoginClient:
    def __init__(
        self,
        *,
        generate_url: str = QR_GENERATE_URL,
        poll_url: str = QR_POLL_URL,
        interval: float = 2.0,
        timeout: float = 30.0,
        transport: httpx.BaseTransport | None = None,
    ):
        self.generate_url = generate_url
        self.poll_url = poll_url
        self.interval = interval
        self._http = httpx.Client(timeout=timeout, transport=transport)

    def close(self) -> None:
        self._http.close()

    def _get(self, url: str, params: dict[str, str] | None = None) -> tuple[dict, httpx.Response]:
        resp = self._http.get(
            url,
            params=params,
            headers={"User-Agent": USER_AGENT, "Referer": REFERER},
        )
        try:
            if resp.status_code != 200:
                raise httpx.HTTPStatusError(
                    f"HTTP {resp.status_code}", request=resp.request, response=resp
                )
            return resp.json(), resp
        finally:
            resp.close()

    def login(self, on_qr: Callable[[str], None] | None = None) -> str:
        env, _ = self._get(self.generate_url)
        if int(env.get("code") or 0) != 0:
            raise APIError(int(env.get("code") or 0), env.get("message", ""))
        data = env.get("data") or {}
        qr_url = data.get("url", "")
        key = data.get("qrcode_key", "")
        if not key:
            raise RuntimeError("generate qrcode: response carried no qrcode_key")
        if on_qr:
            on_qr(qr_url)

        while True:
            env, resp = self._get(self.poll_url, {"qrcode_key": key})
            if int(env.get("code") or 0) != 0:
                raise APIError(int(env.get("code") or 0), env.get("message", ""))
            poll = env.get("data") or {}
            code = int(poll.get("code") or 0)
            if code == 0:
                sessdata = ""
                for cookie in resp.cookies.jar:
                    if cookie.name == "SESSDATA" and cookie.value:
                        sessdata = cookie.value
                        break
                if not sessdata:
                    sessdata = _sessdata_from_url(poll.get("url", ""))
                if not sessdata:
                    raise RuntimeError(
                        "poll qrcode: login succeeded but response carried no SESSDATA"
                    )
                return sessdata
            if code in (QR_NOT_SCAN, QR_SCANNED):
                time.sleep(self.interval)
                continue
            if code == QR_EXPIRED:
                raise RuntimeError(f"poll qrcode: QR code expired (code {code}), please retry")
            raise APIError(code, poll.get("message", ""))


def check_login(client: Client) -> tuple[bool, str]:
    return client.nav_status()
